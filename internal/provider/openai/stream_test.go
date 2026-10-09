package openai

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/bhanuprakaash/jelly-fish/internal/memory"
	"github.com/bhanuprakaash/jelly-fish/internal/msg"
	"github.com/bhanuprakaash/jelly-fish/internal/provider"
	"github.com/bhanuprakaash/jelly-fish/internal/provider/anthropic"
	"github.com/bhanuprakaash/jelly-fish/internal/provider/cassette"
	"github.com/bhanuprakaash/jelly-fish/internal/provider/catalog"
)

// updating reports whether JF_UPDATE_GOLDEN=1 asks to rewrite goldens.
func updating() bool { return os.Getenv("JF_UPDATE_GOLDEN") == "1" }

const (
	sol  = "gpt-6.1-sol"  // reasons, strict outputs
	mini = "gpt-5.4-mini" // no reasoning
)

func testCatalog(t *testing.T) catalog.Catalog {
	t.Helper()
	c, err := catalog.Load()
	if err != nil {
		t.Fatal(err)
	}
	return c
}

// sseServer answers every request with the SSE fixture file and keeps the
// last request body.
func sseServer(t *testing.T, fixture string) (Client, *[]byte) {
	t.Helper()
	sse, err := os.ReadFile(filepath.Join("testdata", fixture))
	if err != nil {
		t.Fatal(err)
	}
	var body []byte
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ = io.ReadAll(r.Body)
		w.Header().Set("Content-Type", "text/event-stream")
		w.Header().Set("X-Request-Id", "req_fixture")
		_, _ = w.Write(sse)
	}))
	t.Cleanup(srv.Close)
	return Client{BaseURL: srv.URL, Catalog: testCatalog(t)}, &body
}

func collect(t *testing.T, p provider.Provider, req provider.Request) (provider.Response, []provider.Delta) {
	t.Helper()
	var deltas []provider.Delta
	resp, err := p.Stream(t.Context(), req, func(d provider.Delta) { deltas = append(deltas, d) })
	if err != nil {
		t.Fatal(err)
	}
	return resp, deltas
}

func userHi() provider.Request {
	return provider.Request{Model: mini, Messages: []msg.Message{msg.UserText("Say hi.")}}
}

func TestStreamLunaText(t *testing.T) {
	c := Client{HTTPClient: cassette.Open(t, "testdata/luna_text.cassette.json"), Catalog: testCatalog(t)}
	req := provider.Request{Model: "gpt-6-luna", Messages: []msg.Message{msg.UserText("Say hi in five words.")}}
	resp, deltas := collect(t, c.Provider(cassette.Key(t, "OPENAI_API_KEY")), req)

	var streamed strings.Builder
	for _, d := range deltas {
		if d.Kind == provider.DeltaText {
			streamed.WriteString(d.Text)
		}
	}
	if got := resp.Message.Text(); got == "" || got != streamed.String() {
		t.Fatalf("text %q, streamed %q", got, streamed.String())
	}
	if resp.StopReason != provider.StopReasonEndTurn || resp.Usage.Input == 0 || resp.Usage.Output == 0 || resp.RequestID == "" {
		t.Fatalf("resp = %+v", resp)
	}
}

func TestStreamText(t *testing.T) {
	c, _ := sseServer(t, "text.sse")
	resp, deltas := collect(t, c.Provider("k"), userHi())

	want := []provider.Delta{{Idx: 0, Text: "Hello"}, {Idx: 0, Text: " there"}}
	if len(deltas) != 2 || deltas[0] != want[0] || deltas[1] != want[1] {
		t.Fatalf("deltas = %+v, want %+v", deltas, want)
	}
	m := resp.Message
	if m.MsgV != msg.CurrentVersion || m.Role != msg.RoleAssistant || len(m.Parts) != 1 || m.Text() != "Hello there" {
		t.Fatalf("message = %+v", m)
	}
	if resp.StopReason != provider.StopReasonEndTurn || resp.RequestID != "req_fixture" {
		t.Fatalf("resp = %+v", resp)
	}
	if want := (provider.Usage{Input: 100, CacheRead: 2000, Output: 5}); resp.Usage != want {
		t.Errorf("usage = %+v, want %+v", resp.Usage, want)
	}
}

func TestStreamReasoningAndToolCall(t *testing.T) {
	c, _ := sseServer(t, "reasoning_tool.sse")
	resp, deltas := collect(t, c.Provider("k"), provider.Request{Model: sol, Messages: []msg.Message{msg.UserText("hi")}})

	if len(deltas) != 6 {
		t.Fatalf("deltas = %+v", deltas)
	}
	thinking := []provider.Delta{
		{Idx: 0, Kind: provider.DeltaThinking, Text: "Check memory."},
		{Idx: 0, Kind: provider.DeltaThinking, Text: "\n\n"},
		{Idx: 0, Kind: provider.DeltaThinking, Text: "Then answer."},
	}
	for i, d := range thinking {
		if deltas[i] != d {
			t.Errorf("delta %d = %+v, want %+v", i, deltas[i], d)
		}
	}
	start, a1, a2 := deltas[3], deltas[4], deltas[5]
	if start.Kind != provider.DeltaToolStart || start.Idx != 1 || start.Name != "search" || start.CallID == "" {
		t.Fatalf("tool_start = %+v", start)
	}
	if a1.Kind != provider.DeltaToolArgs || a1.CallID != start.CallID || a1.Text+a2.Text != `{"q":"cats"}` {
		t.Fatalf("tool_args = %+v %+v", a1, a2)
	}

	parts := resp.Message.Parts
	if len(parts) != 2 {
		t.Fatalf("parts = %+v", parts)
	}
	th := parts[0].Thinking
	if parts[0].Kind != msg.KindThinking || th.Text != "Check memory.\n\nThen answer." || th.Provider != Name || th.Model != sol {
		t.Errorf("thinking = %+v", th)
	}
	var item struct {
		ID  string `json:"id"`
		Enc string `json:"encrypted_content"`
	}
	if err := json.Unmarshal(th.Opaque, &item); err != nil || item.ID != "rs_1" || item.Enc != "gAAAAenc+/=" {
		t.Errorf("opaque = %s (%v)", th.Opaque, err)
	}
	tu := parts[1].ToolUse
	if tu.ID != start.CallID || tu.Name != "search" || string(tu.Args) != `{"q":"cats"}` || string(tu.Opaque) != "call_abc" {
		t.Fatalf("tool use = %+v", tu)
	}
	if resp.StopReason != provider.StopReasonToolUse {
		t.Errorf("stop = %q", resp.StopReason)
	}
	if want := (provider.Usage{Input: 150, CacheRead: 100, CacheWrite5m: 50, Output: 40, Reasoning: 25}); resp.Usage != want {
		t.Errorf("usage = %+v, want %+v", resp.Usage, want)
	}
}

func TestStopReasons(t *testing.T) {
	tests := []struct {
		fixture string
		want    provider.StopReason
	}{
		{"incomplete.sse", provider.StopReasonMaxTokens},
		{"context_exceeded.sse", provider.StopReasonContextExceeded},
	}
	for _, tc := range tests {
		t.Run(tc.fixture, func(t *testing.T) {
			c, _ := sseServer(t, tc.fixture)
			resp, err := c.Provider("k").Stream(t.Context(), userHi(), func(provider.Delta) {})
			if err != nil {
				t.Fatalf("err = %v, want a stop reason", err)
			}
			if resp.StopReason != tc.want || resp.Message.Parts == nil {
				t.Fatalf("resp = %+v, want stop %q and non-nil parts", resp, tc.want)
			}
		})
	}
}

func TestOversizedPromptRejectedUpFrontIsAStop(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusBadRequest)
		_, _ = w.Write([]byte(errBody("invalid_request_error", "context_length_exceeded", "too long")))
	}))
	t.Cleanup(srv.Close)
	c := Client{BaseURL: srv.URL, Catalog: testCatalog(t)}
	resp, err := c.Provider("k").Stream(t.Context(), userHi(), func(provider.Delta) {})
	if err != nil || resp.StopReason != provider.StopReasonContextExceeded || resp.Message.Parts == nil {
		t.Fatalf("resp = %+v, err %v; want context_exceeded with non-nil parts", resp, err)
	}
}

func TestNoThinkingAsksForNoReasoning(t *testing.T) {
	c, body := sseServer(t, "text.sse")
	collect(t, c.Provider("k"), provider.Request{Model: sol, Messages: []msg.Message{msg.UserText("hi")}, NoThinking: true})
	var sent map[string]json.RawMessage
	if err := json.Unmarshal(*body, &sent); err != nil {
		t.Fatal(err)
	}
	if sent["reasoning"] != nil || sent["include"] != nil {
		t.Fatalf("sent reasoning %s include %s, want neither", sent["reasoning"], sent["include"])
	}
}

func TestReasoningWithNothingAfterItIsNotReplayed(t *testing.T) {
	c, body := sseServer(t, "text.sse")
	req := provider.Request{Model: sol, Messages: []msg.Message{
		msg.UserText("hi"),
		// A reply cut off at max_tokens, its tool call removed.
		{MsgV: msg.CurrentVersion, Role: msg.RoleAssistant, Parts: []msg.Part{
			{Kind: msg.KindThinking, Thinking: &msg.Thinking{Provider: Name, Opaque: []byte(`{"type":"reasoning","id":"rs_1","summary":[]}`)}},
		}},
		msg.UserText("go on"),
	}}
	collect(t, c.Provider("k"), req)
	if strings.Contains(string(*body), "rs_1") {
		t.Fatalf("request replays the lone reasoning item: %s", *body)
	}
}

func TestFailedToolResultIsSentWithErrorPrefix(t *testing.T) {
	c, body := sseServer(t, "text.sse")
	req := userHi()
	req.Messages = append(req.Messages,
		msg.Message{MsgV: msg.CurrentVersion, Role: msg.RoleAssistant, Parts: []msg.Part{
			{Kind: msg.KindToolUse, ToolUse: &msg.ToolUse{ID: "ours", Name: "f", Args: json.RawMessage(`{}`), Provider: Name, Opaque: []byte("call_1")}},
		}},
		msg.Message{MsgV: msg.CurrentVersion, Role: msg.RoleUser, Parts: []msg.Part{
			{Kind: msg.KindToolResult, ToolResult: &msg.ToolResult{CallID: "ours", Parts: []msg.Part{{Kind: msg.KindText, Text: "no such file"}}, IsError: true}},
		}})
	collect(t, c.Provider("k"), req)

	var sent struct {
		Input []map[string]any `json:"input"`
	}
	if err := json.Unmarshal(*body, &sent); err != nil {
		t.Fatal(err)
	}
	out := sent.Input[len(sent.Input)-1]
	if out["type"] != "function_call_output" || out["call_id"] != "call_1" || out["output"] != "Error: no such file" {
		t.Fatalf("last input = %v", out)
	}
}

func TestMemoryToolSchemaIsByteIdenticalToAnthropics(t *testing.T) {
	def := memory.New(nil).Def()
	tools := []provider.ToolSpec{{Name: def.Name, Description: def.Description, Schema: def.Schema, Strict: def.Strict}}

	c, body := sseServer(t, "text.sse")
	collect(t, c.Provider("k"), provider.Request{Model: sol, Messages: []msg.Message{msg.UserText("hi")}, Tools: tools})
	var sent struct {
		Tools []struct {
			Parameters json.RawMessage `json:"parameters"`
			Strict     bool            `json:"strict"`
		} `json:"tools"`
	}
	if err := json.Unmarshal(*body, &sent); err != nil {
		t.Fatal(err)
	}

	var claudeBody []byte
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		claudeBody, _ = io.ReadAll(r.Body)
		w.WriteHeader(http.StatusBadRequest)
	}))
	t.Cleanup(srv.Close)
	claude := anthropic.Client{BaseURL: srv.URL, Catalog: testCatalog(t)}
	_, _ = claude.Provider("k").Stream(t.Context(), provider.Request{Model: "claude-haiku-4-5-20251001", Messages: []msg.Message{msg.UserText("hi")}, Tools: tools}, func(provider.Delta) {})
	var claudeSent struct {
		Tools []struct {
			InputSchema json.RawMessage `json:"input_schema"`
		} `json:"tools"`
	}
	if err := json.Unmarshal(claudeBody, &claudeSent); err != nil {
		t.Fatal(err)
	}

	if len(sent.Tools) != 1 || !sent.Tools[0].Strict || len(claudeSent.Tools) != 1 {
		t.Fatalf("sent tools = %+v, anthropic %+v", sent.Tools, claudeSent.Tools)
	}
	if got, want := string(sent.Tools[0].Parameters), string(claudeSent.Tools[0].InputSchema); got != want {
		t.Fatalf("parameters =\n%s\nanthropic input_schema =\n%s", got, want)
	}
}

func TestRequestGoldens(t *testing.T) {
	history := []msg.Message{
		msg.UserText("find cats"),
		{MsgV: msg.CurrentVersion, Role: msg.RoleAssistant, Parts: []msg.Part{
			{Kind: msg.KindThinking, Thinking: &msg.Thinking{Text: "search it", Provider: Name, Model: sol, Opaque: []byte(`{"type":"reasoning","id":"rs_1","summary":[{"type":"summary_text","text":"search it"}],"encrypted_content":"ENC"}`)}},
			{Kind: msg.KindThinking, Thinking: &msg.Thinking{Text: "foreign", Provider: "anthropic", Opaque: []byte("sig")}},
			{Kind: msg.KindNative, Native: &msg.Native{Provider: "anthropic", Type: "redacted_thinking", Raw: json.RawMessage(`{}`)}},
			{Kind: msg.KindText, Text: "Searching."},
			{Kind: msg.KindToolUse, ToolUse: &msg.ToolUse{ID: "call-ours", Name: "search", Args: json.RawMessage(`{"q":"cats"}`), Provider: Name, Opaque: []byte("call_abc")}},
		}},
		{MsgV: msg.CurrentVersion, Role: msg.RoleUser, Parts: []msg.Part{
			{Kind: msg.KindToolResult, ToolResult: &msg.ToolResult{CallID: "call-ours", Parts: []msg.Part{{Kind: msg.KindText, Text: "3 cats"}, {Kind: msg.KindText, Text: "and a dog"}}}},
		}},
	}
	foreign := []msg.Message{
		msg.UserText("remember cats"),
		{MsgV: msg.CurrentVersion, Role: msg.RoleAssistant, Parts: []msg.Part{
			{Kind: msg.KindThinking, Thinking: &msg.Thinking{Text: "store it", Provider: "anthropic", Opaque: []byte("sig")}},
			{Kind: msg.KindText, Text: "Saving."},
			{Kind: msg.KindToolUse, ToolUse: &msg.ToolUse{ID: "7f0c9a52-1d3e-4b6a-9c58-2e41b0a7d3f1", Name: "memory", Args: json.RawMessage(`{"command":"view"}`), Provider: "anthropic", Opaque: []byte("toolu_01ABC")}},
		}},
		{MsgV: msg.CurrentVersion, Role: msg.RoleUser, Parts: []msg.Part{
			{Kind: msg.KindToolResult, ToolResult: &msg.ToolResult{CallID: "7f0c9a52-1d3e-4b6a-9c58-2e41b0a7d3f1", Parts: []msg.Part{{Kind: msg.KindText, Text: "empty"}}}},
		}},
	}
	tests := []struct {
		name string
		req  provider.Request
	}{
		{"text", provider.Request{Model: mini, System: []string{"You are helpful.", "Project: cats."}, Messages: []msg.Message{msg.UserText("hi")}, CacheKey: "sess-1"}},
		{"reasoning", provider.Request{Model: sol, Messages: []msg.Message{msg.UserText("hi")}}},
		{"tools", provider.Request{Model: sol, Messages: []msg.Message{msg.UserText("hi")}, Tools: []provider.ToolSpec{
			{Name: "memory", Description: "Read and write memories.", Schema: json.RawMessage(`{"type":"object","properties":{"command":{"type":"string"}},"required":["command"],"additionalProperties":false}`), Strict: true},
			// A Connector-style schema: constraints strict mode can't take.
			{Name: "lookup", Description: "Look up a code.", Schema: json.RawMessage(`{"type":"object","properties":{"code":{"type":"string","pattern":"^[A-Z]+$","minLength":2}}}`)},
		}}},
		{"replay", provider.Request{Model: sol, Messages: history}},
		{"foreign_replay", provider.Request{Model: sol, Messages: foreign}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			c, body := sseServer(t, "text.sse")
			collect(t, c.Provider("k"), tc.req)
			var pretty bytes.Buffer
			if err := json.Indent(&pretty, *body, "", "  "); err != nil {
				t.Fatal(err)
			}
			pretty.WriteByte('\n')
			path := filepath.Join("testdata", tc.name+".request.json")
			if updating() {
				if err := os.WriteFile(path, pretty.Bytes(), 0o644); err != nil {
					t.Fatal(err)
				}
				return
			}
			want, err := os.ReadFile(path)
			if err != nil {
				t.Fatalf("%v (run with JF_UPDATE_GOLDEN=1)", err)
			}
			if pretty.String() != string(want) {
				t.Fatalf("request =\n%s\nwant\n%s", pretty.String(), want)
			}
		})
	}
}
