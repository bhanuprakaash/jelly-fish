package gemini

import (
	"bytes"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"google.golang.org/genai"

	"github.com/bhanuprakaash/jelly-fish/internal/memory"
	"github.com/bhanuprakaash/jelly-fish/internal/msg"
	"github.com/bhanuprakaash/jelly-fish/internal/provider"
	"github.com/bhanuprakaash/jelly-fish/internal/provider/anthropic"
	"github.com/bhanuprakaash/jelly-fish/internal/provider/cassette"
	"github.com/bhanuprakaash/jelly-fish/internal/provider/catalog"
	"github.com/bhanuprakaash/jelly-fish/internal/provider/openai"
)

func updating() bool { return os.Getenv("JF_UPDATE_GOLDEN") == "1" }

const flash = "gemini-3.8-flash" // always thinks

func testCatalog(t *testing.T) catalog.Catalog {
	t.Helper()
	c, err := catalog.Load()
	if err != nil {
		t.Fatal(err)
	}
	return c
}

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
	return provider.Request{Model: flash, Messages: []msg.Message{msg.UserText("Say hi.")}}
}

func TestStreamFlashLiteText(t *testing.T) {
	c := Client{HTTPClient: cassette.Open(t, "testdata/flash_lite_text.cassette.json"), Catalog: testCatalog(t)}
	req := provider.Request{Model: "gemini-3.5-flash-lite", Messages: []msg.Message{msg.UserText("Say hi in five words.")}}
	resp, deltas := collect(t, c.Provider(cassette.Key(t, "GEMINI_API_KEY")), req)

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
	if resp.StopReason != provider.StopReasonEndTurn || resp.RequestID != "resp_text" {
		t.Fatalf("resp = %+v", resp)
	}
	if want := (provider.Usage{Input: 100, CacheRead: 2000, Output: 5}); resp.Usage != want {
		t.Errorf("usage = %+v, want %+v", resp.Usage, want)
	}
}

func TestStreamThinkingWithSignature(t *testing.T) {
	c, _ := sseServer(t, "thinking.sse")
	resp, deltas := collect(t, c.Provider("k"), userHi())

	want := []provider.Delta{
		{Idx: 0, Kind: provider.DeltaThinking, Text: "Check memory."},
		{Idx: 0, Kind: provider.DeltaThinking, Text: " Then answer."},
		{Idx: 2, Kind: provider.DeltaText, Text: "Hi"},
		{Idx: 2, Kind: provider.DeltaText, Text: " there"},
	}
	if len(deltas) != len(want) {
		t.Fatalf("deltas = %+v, want %+v", deltas, want)
	}
	for i, d := range want {
		if deltas[i] != d {
			t.Errorf("delta %d = %+v, want %+v", i, deltas[i], d)
		}
	}

	parts := resp.Message.Parts
	if len(parts) != 3 {
		t.Fatalf("parts = %+v", parts)
	}
	if th := parts[0].Thinking; parts[0].Kind != msg.KindThinking || th.Text != "Check memory. Then answer." || th.Provider != Name || th.Model != flash || th.Opaque != nil {
		t.Errorf("summary = %+v", parts[0])
	}
	if th := parts[1].Thinking; parts[1].Kind != msg.KindThinking || th.Text != "" || th.Provider != Name || string(th.Opaque) != "sig-one" {
		t.Errorf("signature = %+v", parts[1])
	}
	if parts[2].Kind != msg.KindText || parts[2].Text != "Hi there" {
		t.Errorf("text = %+v", parts[2])
	}
	// Thoughts are counted apart from the candidates and billed as output.
	if want := (provider.Usage{Input: 100, Output: 27, Reasoning: 25}); resp.Usage != want {
		t.Errorf("usage = %+v, want %+v", resp.Usage, want)
	}
}

func TestStreamFunctionCall(t *testing.T) {
	c, _ := sseServer(t, "function_call.sse")
	resp, deltas := collect(t, c.Provider("k"), userHi())

	if len(deltas) != 3 {
		t.Fatalf("deltas = %+v", deltas)
	}
	start, args := deltas[1], deltas[2]
	if start.Kind != provider.DeltaToolStart || start.Idx != 2 || start.Name != "search" || start.CallID == "" {
		t.Fatalf("tool_start = %+v", start)
	}
	if args.Kind != provider.DeltaToolArgs || args.Idx != 2 || args.CallID != start.CallID || args.Text != `{"q":"cats"}` {
		t.Fatalf("tool_args = %+v", args)
	}

	parts := resp.Message.Parts
	if len(parts) != 3 || string(parts[1].Thinking.Opaque) != "sig-call" {
		t.Fatalf("parts = %+v", parts)
	}
	tu := parts[2].ToolUse
	if tu.ID != start.CallID || tu.Name != "search" || string(tu.Args) != `{"q":"cats"}` || string(tu.Opaque) != "fc_abc" {
		t.Fatalf("tool use = %+v", tu)
	}
	if resp.StopReason != provider.StopReasonToolUse {
		t.Errorf("stop = %q", resp.StopReason)
	}
	if want := (provider.Usage{Input: 50, CacheRead: 100, Output: 40, Reasoning: 25}); resp.Usage != want {
		t.Errorf("usage = %+v, want %+v", resp.Usage, want)
	}
}

func TestRefusalsAreStopsAndLogged(t *testing.T) {
	tests := []struct {
		fixture string
		logged  string
	}{
		{"safety.sse", `"finish_reason":"SAFETY"`},
		{"prompt_blocked.sse", `"block_reason":"PROHIBITED_CONTENT"`},
	}
	for _, tc := range tests {
		t.Run(tc.fixture, func(t *testing.T) {
			var logs bytes.Buffer
			c, _ := sseServer(t, tc.fixture)
			c.Logger = slog.New(slog.NewJSONHandler(&logs, nil))
			resp, err := c.Provider("k").Stream(t.Context(), userHi(), func(provider.Delta) {})
			if err != nil {
				t.Fatalf("err = %v, want a stop reason", err)
			}
			if resp.StopReason != provider.StopReasonRefusal || resp.Message.Parts == nil || len(resp.Message.Parts) != 0 {
				t.Fatalf("resp = %+v, want a refusal with no parts", resp)
			}
			if !strings.Contains(logs.String(), tc.logged) {
				t.Fatalf("log = %s, want %s", logs.String(), tc.logged)
			}
		})
	}
}

func TestOtherStopIsLogged(t *testing.T) {
	var logs bytes.Buffer
	c, _ := sseServer(t, "malformed_call.sse")
	c.Logger = slog.New(slog.NewJSONHandler(&logs, nil))
	resp, err := c.Provider("k").Stream(t.Context(), userHi(), func(provider.Delta) {})
	if err != nil || resp.StopReason != provider.StopReasonOther {
		t.Fatalf("resp = %+v, err %v; want an other stop", resp, err)
	}
	if !strings.Contains(logs.String(), `"finish_reason":"MALFORMED_FUNCTION_CALL"`) {
		t.Fatalf("log = %s, want the finish reason", logs.String())
	}
}

func TestStopReasonPerFinishReason(t *testing.T) {
	withCall := msg.Message{Parts: []msg.Part{{Kind: msg.KindToolUse, ToolUse: &msg.ToolUse{}}}}
	text := msg.Message{Parts: []msg.Part{{Kind: msg.KindText, Text: "hi"}}}
	tests := []struct {
		name   string
		finish genai.FinishReason
		block  genai.BlockedReason
		m      msg.Message
		want   provider.StopReason
	}{
		{"STOP", "STOP", "", text, provider.StopReasonEndTurn},
		{"STOP with a call", "STOP", "", withCall, provider.StopReasonToolUse},
		{"MAX_TOKENS", "MAX_TOKENS", "", text, provider.StopReasonMaxTokens},
		{"SAFETY", "SAFETY", "", text, provider.StopReasonRefusal},
		{"PROHIBITED_CONTENT", "PROHIBITED_CONTENT", "", text, provider.StopReasonRefusal},
		{"BLOCKLIST", "BLOCKLIST", "", text, provider.StopReasonRefusal},
		{"SPII", "SPII", "", text, provider.StopReasonRefusal},
		{"RECITATION", "RECITATION", "", text, provider.StopReasonRefusal},
		{"IMAGE_SAFETY", "IMAGE_SAFETY", "", text, provider.StopReasonRefusal},
		{"MALFORMED_FUNCTION_CALL", "MALFORMED_FUNCTION_CALL", "", text, provider.StopReasonOther},
		{"UNEXPECTED_TOOL_CALL", "UNEXPECTED_TOOL_CALL", "", text, provider.StopReasonOther},
		{"unknown", "SOMETHING_NEW", "", text, provider.StopReasonOther},
		{"blocked prompt", "", "PROHIBITED_CONTENT", msg.Message{}, provider.StopReasonRefusal},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := stopReason(tc.finish, tc.block, tc.m); got != tc.want {
				t.Fatalf("stopReason = %q, want %q", got, tc.want)
			}
		})
	}
}

func TestOversizedPromptIsAStop(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusBadRequest)
		_, _ = w.Write([]byte(errBody(400, "INVALID_ARGUMENT", "The input token count (1100000) exceeds the maximum number of tokens allowed (1048576).")))
	}))
	t.Cleanup(srv.Close)
	c := Client{BaseURL: srv.URL, Catalog: testCatalog(t)}
	resp, err := c.Provider("k").Stream(t.Context(), userHi(), func(provider.Delta) {})
	if err != nil || resp.StopReason != provider.StopReasonContextExceeded || resp.Message.Parts == nil {
		t.Fatalf("resp = %+v, err %v; want context_exceeded with non-nil parts", resp, err)
	}
}

func TestNoThinkingAsksForNoThoughts(t *testing.T) {
	c, body := sseServer(t, "text.sse")
	collect(t, c.Provider("k"), provider.Request{Model: flash, Messages: []msg.Message{msg.UserText("hi")}, NoThinking: true})
	if strings.Contains(string(*body), "thinkingConfig") {
		t.Fatalf("request = %s, want no thinkingConfig", *body)
	}
}

func TestToolResultsGoBackAsFunctionResponses(t *testing.T) {
	c, body := sseServer(t, "text.sse")
	call := func(id string, opaque string) msg.Part {
		return msg.Part{Kind: msg.KindToolUse, ToolUse: &msg.ToolUse{ID: id, Name: "f", Args: json.RawMessage(`{}`), Opaque: []byte(opaque)}}
	}
	result := func(id, text string, isErr bool) msg.Part {
		return msg.Part{Kind: msg.KindToolResult, ToolResult: &msg.ToolResult{CallID: id, Parts: []msg.Part{{Kind: msg.KindText, Text: text}}, IsError: isErr}}
	}
	req := userHi()
	req.Messages = append(req.Messages,
		msg.Message{MsgV: msg.CurrentVersion, Role: msg.RoleAssistant, Parts: []msg.Part{call("a", "fc_1"), call("b", "")}},
		msg.Message{MsgV: msg.CurrentVersion, Role: msg.RoleUser, Parts: []msg.Part{result("a", "ok", false), result("b", "no such file", true)}})
	collect(t, c.Provider("k"), req)

	var sent struct {
		Contents []struct {
			Role  string `json:"role"`
			Parts []struct {
				FunctionResponse map[string]any `json:"functionResponse"`
			} `json:"parts"`
		} `json:"contents"`
	}
	if err := json.Unmarshal(*body, &sent); err != nil {
		t.Fatal(err)
	}
	last := sent.Contents[len(sent.Contents)-1]
	got, _ := json.Marshal([]any{last.Parts[0].FunctionResponse, last.Parts[1].FunctionResponse})
	want := `[{"id":"fc_1","name":"f","response":{"output":"ok"}},{"name":"f","response":{"error":"no such file"}}]`
	if last.Role != "user" || string(got) != want {
		t.Fatalf("last content = %s %s, want user %s", last.Role, got, want)
	}
}

func TestToolResultWithoutItsCallIsAnError(t *testing.T) {
	_, err := toContents([]msg.Message{{MsgV: msg.CurrentVersion, Role: msg.RoleUser, Parts: []msg.Part{
		{Kind: msg.KindToolResult, ToolResult: &msg.ToolResult{CallID: "gone", Parts: []msg.Part{{Kind: msg.KindText, Text: "ok"}}}},
	}}})
	if err == nil || err.Error() != "gemini: tool result for unknown call gone" {
		t.Fatalf("err = %v", err)
	}
}

func TestMemoryToolSchemaMatchesTheOtherAdapters(t *testing.T) {
	def := memory.New(nil).Def()
	tools := []provider.ToolSpec{{Name: def.Name, Description: def.Description, Schema: def.Schema, Strict: def.Strict}}

	c, body := sseServer(t, "text.sse")
	collect(t, c.Provider("k"), provider.Request{Model: flash, Messages: []msg.Message{msg.UserText("hi")}, Tools: tools})
	var sent struct {
		Tools []struct {
			FunctionDeclarations []struct {
				ParametersJSONSchema json.RawMessage `json:"parametersJsonSchema"`
			} `json:"functionDeclarations"`
		} `json:"tools"`
	}
	if err := json.Unmarshal(*body, &sent); err != nil {
		t.Fatal(err)
	}

	claudeBody := refusedBody(t, func(u string) provider.Provider {
		return anthropic.Client{BaseURL: u, Catalog: testCatalog(t)}.Provider("k")
	}, "claude-haiku-4-5-20251001", tools)
	gptBody := refusedBody(t, func(u string) provider.Provider {
		return openai.Client{BaseURL: u, Catalog: testCatalog(t)}.Provider("k")
	}, "gpt-6.1-sol", tools)
	var claudeSent struct {
		Tools []struct {
			InputSchema json.RawMessage `json:"input_schema"`
		} `json:"tools"`
	}
	var gptSent struct {
		Tools []struct {
			Parameters json.RawMessage `json:"parameters"`
		} `json:"tools"`
	}
	if err := json.Unmarshal(claudeBody, &claudeSent); err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(gptBody, &gptSent); err != nil {
		t.Fatal(err)
	}

	if len(sent.Tools) != 1 || len(sent.Tools[0].FunctionDeclarations) != 1 || len(claudeSent.Tools) != 1 || len(gptSent.Tools) != 1 {
		t.Fatalf("sent tools = %s, anthropic %s, openai %s", *body, claudeBody, gptBody)
	}
	// The SDK re-encodes the schema through a map, which sorts its keys.
	got := sorted(t, sent.Tools[0].FunctionDeclarations[0].ParametersJSONSchema)
	if want := sorted(t, claudeSent.Tools[0].InputSchema); got != want {
		t.Fatalf("parametersJsonSchema =\n%s\nanthropic input_schema =\n%s", got, want)
	}
	if want := sorted(t, gptSent.Tools[0].Parameters); got != want {
		t.Fatalf("parametersJsonSchema =\n%s\nopenai parameters =\n%s", got, want)
	}
}

// refusedBody is the request body another adapter sends for tools, from a
// server that answers 400.
func refusedBody(t *testing.T, newProvider func(baseURL string) provider.Provider, model string, tools []provider.ToolSpec) []byte {
	t.Helper()
	var body []byte
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ = io.ReadAll(r.Body)
		w.WriteHeader(http.StatusBadRequest)
	}))
	t.Cleanup(srv.Close)
	_, _ = newProvider(srv.URL).Stream(t.Context(), provider.Request{Model: model, Messages: []msg.Message{msg.UserText("hi")}, Tools: tools}, func(provider.Delta) {})
	return body
}

func sorted(t *testing.T, raw json.RawMessage) string {
	t.Helper()
	var v any
	if err := json.Unmarshal(raw, &v); err != nil {
		t.Fatal(err)
	}
	out, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	return string(out)
}

func TestRequestGoldens(t *testing.T) {
	history := []msg.Message{
		msg.UserText("find cats"),
		{MsgV: msg.CurrentVersion, Role: msg.RoleAssistant, Parts: []msg.Part{
			{Kind: msg.KindThinking, Thinking: &msg.Thinking{Text: "search it", Provider: Name, Model: flash}},
			{Kind: msg.KindThinking, Thinking: &msg.Thinking{Provider: Name, Model: flash, Opaque: []byte("sig-text")}},
			{Kind: msg.KindThinking, Thinking: &msg.Thinking{Text: "foreign", Provider: "anthropic", Opaque: []byte("sig")}},
			{Kind: msg.KindNative, Native: &msg.Native{Provider: "anthropic", Type: "redacted_thinking", Raw: json.RawMessage(`{}`)}},
			{Kind: msg.KindText, Text: "Searching."},
			{Kind: msg.KindThinking, Thinking: &msg.Thinking{Provider: Name, Model: flash, Opaque: []byte("sig-call")}},
			{Kind: msg.KindToolUse, ToolUse: &msg.ToolUse{ID: "call-ours", Name: "search", Args: json.RawMessage(`{"q":"cats"}`), Opaque: []byte("fc_abc")}},
		}},
		{MsgV: msg.CurrentVersion, Role: msg.RoleUser, Parts: []msg.Part{
			{Kind: msg.KindToolResult, ToolResult: &msg.ToolResult{CallID: "call-ours", Parts: []msg.Part{{Kind: msg.KindText, Text: "3 cats"}}}},
		}},
	}
	tests := []struct {
		name string
		req  provider.Request
	}{
		{"text", provider.Request{Model: flash, System: []string{"You are helpful.", "Project: cats."}, Messages: []msg.Message{msg.UserText("hi")}, CacheKey: "sess-1"}},
		{"tools", provider.Request{Model: flash, Messages: []msg.Message{msg.UserText("hi")}, Tools: []provider.ToolSpec{
			{Name: "memory", Description: "Read and write memories.", Schema: json.RawMessage(`{"type":"object","properties":{"command":{"type":"string"}},"required":["command"],"additionalProperties":false}`), Strict: true},
			// A Connector-style schema: constraints strict mode can't take.
			{Name: "lookup", Description: "Look up a code.", Schema: json.RawMessage(`{"type":"object","properties":{"code":{"type":"string","pattern":"^[A-Z]+$","minLength":2}}}`)},
		}}},
		{"replay", provider.Request{Model: flash, Messages: history}},
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
