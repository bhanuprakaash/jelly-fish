package anthropic

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/bhanuprakaash/jelly-fish/internal/msg"
	"github.com/bhanuprakaash/jelly-fish/internal/provider"
	"github.com/bhanuprakaash/jelly-fish/internal/provider/cassette"
	"github.com/bhanuprakaash/jelly-fish/internal/provider/catalog"
)

// updating reports whether JF_UPDATE_GOLDEN=1 asks to rewrite goldens.
func updating() bool { return os.Getenv("JF_UPDATE_GOLDEN") == "1" }

const (
	haiku = "claude-haiku-4-5-20251001"
	opus  = "claude-opus-5-5"
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
func sseServer(t *testing.T, fixture string) (Client, *[]byte, *atomic.Int32) {
	t.Helper()
	sse, err := os.ReadFile(filepath.Join("testdata", fixture))
	if err != nil {
		t.Fatal(err)
	}
	var body []byte
	var hits atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		body, _ = io.ReadAll(r.Body)
		w.Header().Set("Content-Type", "text/event-stream")
		w.Header().Set("Request-Id", "req_fixture")
		_, _ = w.Write(sse)
	}))
	t.Cleanup(srv.Close)
	return Client{BaseURL: srv.URL, Catalog: testCatalog(t)}, &body, &hits
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
	return provider.Request{Model: haiku, Messages: []msg.Message{msg.UserText("Say hi in five words.")}}
}

func TestStreamHaikuText(t *testing.T) {
	c := Client{HTTPClient: cassette.Open(t, "testdata/haiku_text.cassette.json"), Catalog: testCatalog(t)}
	resp, deltas := collect(t, c.Provider(cassette.Key(t, "ANTHROPIC_API_KEY")), userHi())

	var streamed strings.Builder
	for _, d := range deltas {
		if d.Kind != provider.DeltaText || d.Idx != 0 {
			t.Fatalf("delta %+v, want text at idx 0", d)
		}
		streamed.WriteString(d.Text)
	}
	got := resp.Message
	if got.MsgV != msg.CurrentVersion || got.Role != msg.RoleAssistant || len(got.Parts) != 1 || got.Parts[0].Kind != msg.KindText {
		t.Fatalf("message = %+v", got)
	}
	if got.Text() == "" || got.Text() != streamed.String() {
		t.Fatalf("text %q, streamed %q", got.Text(), streamed.String())
	}
	if resp.StopReason != provider.StopReasonEndTurn || resp.Usage.Input == 0 || resp.Usage.Output == 0 || resp.RequestID == "" {
		t.Fatalf("resp = %+v", resp)
	}
}

func TestStreamThinkingAndRedacted(t *testing.T) {
	c, _, _ := sseServer(t, "thinking.sse")
	resp, deltas := collect(t, c.Provider("k"), provider.Request{Model: opus, Messages: []msg.Message{msg.UserText("hi")}})

	want := []provider.Delta{
		{Idx: 0, Kind: provider.DeltaThinking, Text: "Let me "},
		{Idx: 0, Kind: provider.DeltaThinking, Text: "think."},
		{Idx: 2, Kind: provider.DeltaText, Text: "Hello"},
		{Idx: 2, Kind: provider.DeltaText, Text: " there"},
	}
	if len(deltas) != len(want) {
		t.Fatalf("deltas = %+v", deltas)
	}
	for i := range want {
		if deltas[i] != want[i] {
			t.Errorf("delta %d = %+v, want %+v", i, deltas[i], want[i])
		}
	}

	parts := resp.Message.Parts
	if len(parts) != 3 {
		t.Fatalf("parts = %+v", parts)
	}
	th := parts[0].Thinking
	if parts[0].Kind != msg.KindThinking || th.Text != "Let me think." || string(th.Opaque) != "EqQBCkYIBxgCKkA+sig/==" || th.Provider != Name || th.Model != opus {
		t.Errorf("thinking = %+v", th)
	}
	nat := parts[1].Native
	if parts[1].Kind != msg.KindNative || nat.Provider != Name || nat.Type != "redacted_thinking" || !strings.Contains(string(nat.Raw), "EmwKAhgBEgy3va3pzix") {
		t.Errorf("native = %+v", nat)
	}
	if parts[2].Kind != msg.KindText || parts[2].Text != "Hello there" {
		t.Errorf("text = %+v", parts[2])
	}
	if resp.Usage != (provider.Usage{Input: 12, Output: 40, Reasoning: 25}) {
		t.Errorf("usage = %+v", resp.Usage)
	}
}

func TestStreamToolUseAndCacheUsage(t *testing.T) {
	c, _, _ := sseServer(t, "tool_use.sse")
	resp, deltas := collect(t, c.Provider("k"), userHi())

	if len(deltas) != 4 {
		t.Fatalf("deltas = %+v", deltas)
	}
	start, args1, args2 := deltas[1], deltas[2], deltas[3]
	if start.Kind != provider.DeltaToolStart || start.Idx != 1 || start.Name != "search" || start.CallID == "" {
		t.Fatalf("tool_start = %+v", start)
	}
	if args1.Kind != provider.DeltaToolArgs || args1.CallID != start.CallID || args1.Text+args2.Text != `{"q":"cats"}` {
		t.Fatalf("tool_args = %+v %+v", args1, args2)
	}

	tu := resp.Message.Parts[1].ToolUse
	if tu.ID != start.CallID || tu.Name != "search" || string(tu.Args) != `{"q":"cats"}` || string(tu.Opaque) != "toolu_01ABC" {
		t.Fatalf("tool use = %+v", tu)
	}
	if resp.StopReason != provider.StopReasonToolUse {
		t.Errorf("stop = %q", resp.StopReason)
	}
	if want := (provider.Usage{Input: 30, CacheRead: 2000, CacheWrite5m: 100, CacheWrite1h: 200, Output: 20}); resp.Usage != want {
		t.Errorf("usage = %+v, want %+v", resp.Usage, want)
	}
}

func TestStopReasons(t *testing.T) {
	tests := []struct {
		fixture string
		want    provider.StopReason
	}{
		{"context_exceeded.sse", provider.StopReasonContextExceeded},
		{"unknown_stop.sse", provider.StopReasonOther},
	}
	for _, tc := range tests {
		t.Run(tc.fixture, func(t *testing.T) {
			c, _, _ := sseServer(t, tc.fixture)
			resp, err := c.Provider("k").Stream(t.Context(), userHi(), func(provider.Delta) {})
			if err != nil {
				t.Fatalf("err = %v, want a stop reason", err)
			}
			if resp.StopReason != tc.want || resp.Message.Text() == "" {
				t.Fatalf("resp = %+v", resp)
			}
		})
	}
}

func TestRefusalCarriesStopDetails(t *testing.T) {
	c, _, _ := sseServer(t, "refusal.sse")
	resp, err := c.Provider("k").Stream(t.Context(), userHi(), func(provider.Delta) {})
	if err != nil {
		t.Fatal(err)
	}
	if resp.StopReason != provider.StopReasonRefusal || resp.StopDetail != "cyber. Declined by a policy classifier." {
		t.Fatalf("stop = %q, detail = %q", resp.StopReason, resp.StopDetail)
	}
}

func TestStreamDoesNotRetry(t *testing.T) {
	var hits atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(529)
		_, _ = w.Write([]byte(`{"type":"error","error":{"type":"overloaded_error","message":"Overloaded"}}`))
	}))
	t.Cleanup(srv.Close)
	c := Client{BaseURL: srv.URL, Catalog: testCatalog(t)}
	if _, err := c.Provider("sk-ant-canary").Stream(t.Context(), userHi(), func(provider.Delta) {}); err == nil {
		t.Fatal("no error")
	} else if strings.Contains(err.Error(), "sk-ant-canary") {
		t.Fatalf("error carries the key: %v", err)
	}
	if n := hits.Load(); n != 1 {
		t.Fatalf("%d attempts, want 1", n)
	}
}

func TestRequestGoldens(t *testing.T) {
	history := []msg.Message{
		msg.UserText("find cats"),
		{MsgV: msg.CurrentVersion, Role: msg.RoleAssistant, Parts: []msg.Part{
			{Kind: msg.KindThinking, Thinking: &msg.Thinking{Text: "search it", Provider: Name, Model: opus, Opaque: []byte("sig-1")}},
			{Kind: msg.KindThinking, Thinking: &msg.Thinking{Text: "foreign", Provider: "openai", Opaque: []byte("enc")}},
			{Kind: msg.KindNative, Native: &msg.Native{Provider: Name, Type: "redacted_thinking", Raw: json.RawMessage(`{"type":"redacted_thinking","data":"RED"}`)}},
			{Kind: msg.KindNative, Native: &msg.Native{Provider: "gemini", Type: "x", Raw: json.RawMessage(`{}`)}},
			{Kind: msg.KindText, Text: "Searching."},
			{Kind: msg.KindToolUse, ToolUse: &msg.ToolUse{ID: "call-ours", Name: "search", Args: json.RawMessage(`{"q":"cats"}`), Provider: Name, Opaque: []byte("toolu_01ABC")}},
		}},
		{MsgV: msg.CurrentVersion, Role: msg.RoleUser, Parts: []msg.Part{
			{Kind: msg.KindToolResult, ToolResult: &msg.ToolResult{CallID: "call-ours", Parts: []msg.Part{{Kind: msg.KindText, Text: "3 cats"}}, IsError: true}},
		}},
	}
	foreign := []msg.Message{
		msg.UserText("remember cats"),
		{MsgV: msg.CurrentVersion, Role: msg.RoleAssistant, Parts: []msg.Part{
			{Kind: msg.KindThinking, Thinking: &msg.Thinking{Text: "store it", Provider: "openai", Opaque: []byte(`{"type":"reasoning","id":"rs_1"}`)}},
			{Kind: msg.KindText, Text: "Saving."},
			{Kind: msg.KindToolUse, ToolUse: &msg.ToolUse{ID: "7f0c9a52-1d3e-4b6a-9c58-2e41b0a7d3f1", Name: "memory", Args: json.RawMessage(`{"command":"view"}`), Provider: "openai", Opaque: []byte("call_abc")}},
		}},
		{MsgV: msg.CurrentVersion, Role: msg.RoleUser, Parts: []msg.Part{
			{Kind: msg.KindToolResult, ToolResult: &msg.ToolResult{CallID: "7f0c9a52-1d3e-4b6a-9c58-2e41b0a7d3f1", Parts: []msg.Part{{Kind: msg.KindText, Text: "empty"}}}},
		}},
	}
	tests := []struct {
		name string
		req  provider.Request
	}{
		{"text", userHi()},
		{"adaptive_thinking", provider.Request{Model: opus, Messages: []msg.Message{msg.UserText("hi")}}},
		{"tools", provider.Request{Model: haiku, Messages: []msg.Message{msg.UserText("hi")}, Tools: []provider.ToolSpec{
			{Name: "memory", Description: "Read and write memories.", Schema: json.RawMessage(`{"type":"object","properties":{"command":{"type":"string"}},"required":["command"],"additionalProperties":false}`), Strict: true},
			// A Connector-style schema: constraints strict mode can't take.
			{Name: "lookup", Description: "Look up a code.", Schema: json.RawMessage(`{"type":"object","properties":{"code":{"type":"string","pattern":"^[A-Z]+$","minLength":2}}}`)},
			{Name: "sleep", Description: "Wait.", Schema: json.RawMessage(`{"type":"object"}`)},
		}}},
		{"web_search", provider.Request{Model: haiku, Messages: []msg.Message{msg.UserText("hi")}, WebSearch: true, Tools: []provider.ToolSpec{
			{Name: "sleep", Description: "Wait.", Schema: json.RawMessage(`{"type":"object"}`)},
		}}},
		{"web_search_replay", provider.Request{Model: haiku, Messages: []msg.Message{
			msg.UserText("weather in Paris"),
			{MsgV: msg.CurrentVersion, Role: msg.RoleAssistant, Parts: []msg.Part{
				{Kind: msg.KindNative, Native: &msg.Native{Provider: Name, Type: "server_tool_use", Raw: json.RawMessage(`{"type":"server_tool_use","id":"srvtoolu_01A","name":"web_search","input":{"query":"weather in Paris"}}`)}},
				{Kind: msg.KindNative, Native: &msg.Native{Provider: Name, Type: "web_search_tool_result", Raw: json.RawMessage(`{"type":"web_search_tool_result","tool_use_id":"srvtoolu_01A","content":[{"type":"web_search_result","title":"Paris weather","url":"https://example.com/paris","encrypted_content":"EqgfCioIARgBIiQ","page_age":"April 30, 2026"}]}`)}},
				{Kind: msg.KindNative, Native: &msg.Native{Provider: "gemini", Type: "grounding_metadata", Raw: json.RawMessage(`{}`)}},
				{Kind: msg.KindText, Text: "It is sunny in Paris.", Citations: []msg.Citation{{URL: "https://example.com/paris", Title: "Paris weather"}}},
			}},
			msg.UserText("thanks"),
		}}},
		{"web_search_unanswered_call", provider.Request{Model: haiku, Messages: []msg.Message{
			msg.UserText("weather in Paris"),
			{MsgV: msg.CurrentVersion, Role: msg.RoleAssistant, Parts: []msg.Part{
				{Kind: msg.KindText, Text: "Searching."},
				{Kind: msg.KindNative, Native: &msg.Native{Provider: Name, Type: "server_tool_use", Raw: json.RawMessage(`{"type":"server_tool_use","id":"srvtoolu_01A","name":"web_search","input":{"query":"weather in Paris"}}`)}},
				{Kind: msg.KindNative, Native: &msg.Native{Provider: Name, Type: "web_search_tool_result", Raw: json.RawMessage(`{"type":"web_search_tool_result","tool_use_id":"srvtoolu_01A","content":[]}`)}},
				{Kind: msg.KindNative, Native: &msg.Native{Provider: Name, Type: "server_tool_use", Raw: json.RawMessage(`{"type":"server_tool_use","id":"srvtoolu_01B","name":"web_search","input":{"query":"Paris forecast"}}`)}},
			}},
			msg.UserText("thanks"),
		}}},
		{"web_search_paused_call", provider.Request{Model: haiku, Messages: []msg.Message{
			msg.UserText("weather in Paris"),
			{MsgV: msg.CurrentVersion, Role: msg.RoleAssistant, Parts: []msg.Part{
				{Kind: msg.KindText, Text: "Searching."},
				{Kind: msg.KindNative, Native: &msg.Native{Provider: Name, Type: "server_tool_use", Raw: json.RawMessage(`{"type":"server_tool_use","id":"srvtoolu_01B","name":"web_search","input":{"query":"Paris forecast"}}`)}},
			}},
		}}},
		{"breakpoint_before_stored_blocks", provider.Request{Model: haiku, Messages: []msg.Message{
			msg.UserText("weather in Paris"),
			{MsgV: msg.CurrentVersion, Role: msg.RoleAssistant, Parts: []msg.Part{
				{Kind: msg.KindText, Text: "Searching."},
				{Kind: msg.KindNative, Native: &msg.Native{Provider: Name, Type: "server_tool_use", Raw: json.RawMessage(`{"type":"server_tool_use","id":"srvtoolu_01A","name":"web_search","input":{"query":"weather in Paris"}}`)}},
				{Kind: msg.KindNative, Native: &msg.Native{Provider: Name, Type: "web_search_tool_result", Raw: json.RawMessage(`{"type":"web_search_tool_result","tool_use_id":"srvtoolu_01A","content":[]}`)}},
			}},
		}}},
		{"replay", provider.Request{Model: opus, Messages: history}},
		{"foreign_replay", provider.Request{Model: opus, Messages: foreign}},
		{"breakpoints", provider.Request{
			Model:      haiku,
			System:     []string{"You are helpful.", "Project: cats."},
			SummaryEnd: 1,
			Messages: []msg.Message{
				msg.UserText("summary so far"),
				msg.AssistantText("ok"),
				msg.UserText("next"),
			},
		}},
		{"breakpoint_after_dropped_message", provider.Request{
			Model:      haiku,
			SummaryEnd: 2,
			Messages: []msg.Message{
				msg.UserText("summary so far"),
				// Only a foreign Provider's thinking: nothing to send.
				{MsgV: msg.CurrentVersion, Role: msg.RoleAssistant, Parts: []msg.Part{
					{Kind: msg.KindThinking, Thinking: &msg.Thinking{Text: "x", Provider: "openai"}},
				}},
				msg.AssistantText("ok"),
				msg.UserText("next"),
			},
		}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			c, body, _ := sseServer(t, "unknown_stop.sse")
			if _, err := c.Provider("k").Stream(t.Context(), tc.req, func(provider.Delta) {}); err != nil {
				t.Fatal(err)
			}
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

func TestStreamNoThinkingOmitsThinkingParam(t *testing.T) {
	for _, tc := range []struct {
		name       string
		noThinking bool
		want       bool
	}{{"default model thinking", false, true}, {"NoThinking", true, false}} {
		t.Run(tc.name, func(t *testing.T) {
			c, body, _ := sseServer(t, "unknown_stop.sse")
			req := provider.Request{Model: opus, Messages: []msg.Message{msg.UserText("hi")}, NoThinking: tc.noThinking}
			collect(t, c.Provider("key"), req)
			var sent struct {
				Thinking json.RawMessage `json:"thinking"`
			}
			if err := json.Unmarshal(*body, &sent); err != nil {
				t.Fatal(err)
			}
			if (sent.Thinking != nil) != tc.want {
				t.Fatalf("thinking = %s, want present = %v", sent.Thinking, tc.want)
			}
		})
	}
}
