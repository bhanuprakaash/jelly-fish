package mcpclient

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/bhanuprakaash/jelly-fish/internal/mcpclient/mcptest"
	"github.com/bhanuprakaash/jelly-fish/internal/msg"
)

func testCtx(t *testing.T) context.Context {
	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
	t.Cleanup(cancel)
	return ctx
}

func textResult(s string) *mcp.CallToolResult {
	return &mcp.CallToolResult{Content: []mcp.Content{&mcp.TextContent{Text: s}}}
}

func equalStrings(t *testing.T, got, want []string) {
	t.Helper()
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Fatalf("got %v, want %v", got, want)
	}
}

func TestLegacyServerIsProbedOnceThenRemembered(t *testing.T) {
	srv := mcptest.Start(t, mcptest.Options{Legacy: true})
	srv.AddTool("echo", textResult("hi"))
	c := New(true)

	tools, era, err := c.ListTools(testCtx(t), Target{URL: srv.URL})
	if err != nil {
		t.Fatal(err)
	}
	if era != EraLegacy || len(tools) != 1 || tools[0].Name != "echo" {
		t.Fatalf("got era %q tools %+v", era, tools)
	}
	equalStrings(t, srv.Methods(), []string{"server/discover", "initialize", "notifications/initialized", "tools/list"})

	if _, _, err := c.ListTools(testCtx(t), Target{URL: srv.URL, Era: era}); err != nil {
		t.Fatal(err)
	}
	equalStrings(t, srv.Methods()[4:], []string{"initialize", "notifications/initialized", "tools/list"})
}

func TestModernServerNeverGetsInitialize(t *testing.T) {
	srv := mcptest.Start(t, mcptest.Options{})
	srv.AddTool("echo", textResult("hi"))

	_, era, err := New(true).ListTools(testCtx(t), Target{URL: srv.URL})
	if err != nil {
		t.Fatal(err)
	}
	if era != EraModern {
		t.Fatalf("era %q", era)
	}
	equalStrings(t, srv.Methods(), []string{"server/discover", "tools/list"})
}

func TestListToolsCarriesTheServersTTLHint(t *testing.T) {
	for name, hint := range map[string]int{"hint": 1500, "none": 0} {
		t.Run(name, func(t *testing.T) {
			srv := mcptest.Start(t, mcptest.Options{TTLMs: hint})
			srv.AddTool("echo", textResult("hi"))

			tools, _, err := New(true).ListTools(testCtx(t), Target{URL: srv.URL})
			if err != nil {
				t.Fatal(err)
			}
			if len(tools) != 1 {
				t.Fatalf("tools = %+v, want one", tools)
			}
			if got := tools[0].TTLMs; (hint == 0) != (got == nil) || (got != nil && *got != hint) {
				t.Fatalf("TTLMs = %v, want %d (0 meaning nil)", got, hint)
			}
		})
	}
}

func TestServerThatAnswersNeitherProbeIsOutdated(t *testing.T) {
	hs := httptest.NewServer(http.NotFoundHandler())
	t.Cleanup(hs.Close)

	_, _, err := New(true).ListTools(testCtx(t), Target{URL: hs.URL})
	if !errors.Is(err, ErrOutdatedServer) {
		t.Fatalf("got %v", err)
	}
}

func TestUnreachableServerIsNotOutdated(t *testing.T) {
	hs := httptest.NewServer(http.NotFoundHandler())
	hs.Close()

	_, _, err := New(true).ListTools(testCtx(t), Target{URL: hs.URL})
	if err == nil || errors.Is(err, ErrOutdatedServer) {
		t.Fatalf("got %v", err)
	}
}

func TestCallToolSendsTheNameAndArgsGiven(t *testing.T) {
	srv := mcptest.Start(t, mcptest.Options{})
	srv.AddTool("notion__search", textResult("found"))

	res, err := New(true).CallTool(testCtx(t), Target{URL: srv.URL}, "notion__search", json.RawMessage(`{"q":"plans"}`), nil)
	if err != nil {
		t.Fatal(err)
	}
	name, args := srv.LastCall()
	if name != "notion__search" || string(args) != `{"q":"plans"}` {
		t.Fatalf("server saw %q %s", name, args)
	}
	if len(res.Content) != 1 || res.Content[0].Text != "found" {
		t.Fatalf("got %+v", res)
	}
}

func TestResultMapping(t *testing.T) {
	srv := mcptest.Start(t, mcptest.Options{})
	srv.AddTool("mixed", &mcp.CallToolResult{
		IsError: true,
		Content: []mcp.Content{
			&mcp.TextContent{Text: "boom"},
			&mcp.ResourceLink{URI: "file:///a.txt", Name: "a"},
			&mcp.AudioContent{Data: []byte("x"), MIMEType: "audio/wav"},
			&mcp.ImageContent{Data: []byte("png"), MIMEType: "image/png"},
		},
	})
	srv.AddTool("structured", &mcp.CallToolResult{StructuredContent: map[string]any{"n": 1}})

	res, err := New(true).CallTool(testCtx(t), Target{URL: srv.URL}, "mixed", nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	if !res.IsError || len(res.Content) != 4 {
		t.Fatalf("got %+v", res)
	}
	for i, want := range []string{"boom", "file:///a.txt", "[audio returned, not supported]"} {
		if res.Content[i].Kind != msg.KindText || res.Content[i].Text != want {
			t.Fatalf("part %d: got %+v, want text %q", i, res.Content[i], want)
		}
	}
	img := res.Content[3]
	if img.Kind != msg.KindNative || img.Native.Type != "image" || string(img.Native.Raw) != `{"data":"cG5n","mimeType":"image/png"}` {
		t.Fatalf("image part %+v", img)
	}

	res, err = New(true).CallTool(testCtx(t), Target{URL: srv.URL}, "structured", nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Content) != 1 || res.Content[0].Text != `{"n":1}` {
		t.Fatalf("got %+v", res)
	}
}

func TestInputRequiredIsReturnedAndResumedByTheCaller(t *testing.T) {
	srv := mcptest.Start(t, mcptest.Options{})
	srv.AddInputRequiredTool("ask")
	c := New(true)

	res, err := c.CallTool(testCtx(t), Target{URL: srv.URL}, "ask", nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	ir := res.InputRequired
	if ir == nil || ir.RequestState != "state-1" || ir.Requests["confirm"].Message != "Proceed?" || ir.Requests["confirm"].Mode != "form" {
		t.Fatalf("got %+v", res)
	}

	meta := &ResumeMeta{RequestState: ir.RequestState, InputResponses: map[string]InputResponse{"confirm": {Action: "accept"}}}
	res, err = c.CallTool(testCtx(t), Target{URL: srv.URL}, "ask", nil, meta)
	if err != nil {
		t.Fatal(err)
	}
	if res.InputRequired != nil || len(res.Content) != 1 || res.Content[0].Text != "done" {
		t.Fatalf("got %+v", res)
	}
}

func TestClientDeclaresOnlyElicitation(t *testing.T) {
	for _, legacy := range []bool{false, true} {
		srv := mcptest.Start(t, mcptest.Options{Legacy: legacy})
		if _, _, err := New(true).ListTools(testCtx(t), Target{URL: srv.URL}); err != nil {
			t.Fatal(err)
		}
		var caps map[string]any
		if err := json.Unmarshal(srv.ClientCapabilities(), &caps); err != nil {
			t.Fatal(err)
		}
		if _, ok := caps["elicitation"]; !ok || caps["sampling"] != nil || caps["roots"] != nil {
			t.Fatalf("legacy=%v declared %v", legacy, caps)
		}
	}
}

func TestLogsHoldNoCredential(t *testing.T) {
	var logs bytes.Buffer
	prev := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(&logs, &slog.HandlerOptions{Level: slog.LevelDebug})))
	t.Cleanup(func() { slog.SetDefault(prev) })

	srv := mcptest.Start(t, mcptest.Options{})
	srv.AddTool("echo", textResult("hi"))
	target := Target{URL: srv.URL, Header: http.Header{"Authorization": {"Bearer s3cret-token"}, "X-Api-Key": {"k3y-value"}}}

	c := New(true)
	_, _ = c.CallTool(testCtx(t), target, "echo", nil, nil)
	_, _ = c.CallTool(testCtx(t), target, "missing", nil, nil)

	if got := srv.Header().Get("X-Api-Key"); got != "k3y-value" {
		t.Fatalf("server saw X-Api-Key %q", got)
	}
	if strings.Contains(logs.String(), "s3cret-token") || strings.Contains(logs.String(), "k3y-value") {
		t.Fatalf("log holds a credential:\n%s", logs.String())
	}
}

func TestCredentialHeaderGoesOnlyToTheConnectorHost(t *testing.T) {
	var got string
	other := httptest.NewServer(http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) { got = r.Header.Get("X-Api-Key") }))
	t.Cleanup(other.Close)

	cred := &credTransport{base: http.DefaultTransport, host: "connector.example", header: http.Header{"X-Api-Key": {"k3y-value"}}}
	req, err := http.NewRequestWithContext(testCtx(t), http.MethodGet, other.URL, nil)
	if err != nil {
		t.Fatal(err)
	}
	resp, err := cred.RoundTrip(req)
	if err != nil {
		t.Fatal(err)
	}
	_ = resp.Body.Close()
	if got != "" {
		t.Fatalf("other host saw X-Api-Key %q", got)
	}
}
