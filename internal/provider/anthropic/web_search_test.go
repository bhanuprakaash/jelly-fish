package anthropic

import (
	"encoding/json"
	"testing"

	"github.com/bhanuprakaash/jelly-fish/internal/msg"
	"github.com/bhanuprakaash/jelly-fish/internal/provider"
)

// The fixture is written by hand from Anthropic's documented stream format,
// not recorded.
func TestStreamWebSearch(t *testing.T) {
	c, _, _ := sseServer(t, "web_search.sse")
	resp, _ := collect(t, c.Provider("k"), provider.Request{Model: haiku, Messages: []msg.Message{msg.UserText("weather in Paris")}, WebSearch: true})

	parts := resp.Message.Parts
	if len(parts) != 5 {
		t.Fatalf("parts = %+v", parts)
	}
	wantTypes := []string{"server_tool_use", "web_search_tool_result", "server_tool_use", "web_fetch_tool_result"}
	for i, typ := range wantTypes {
		if parts[i].Kind != msg.KindNative || parts[i].Native.Provider != Name || parts[i].Native.Type != typ {
			t.Errorf("part %d = %+v, want native %s", i, parts[i], typ)
		}
	}
	var input struct {
		Input struct {
			Query string `json:"query"`
		} `json:"input"`
	}
	if err := json.Unmarshal(parts[0].Native.Raw, &input); err != nil || input.Input.Query != "weather in Paris" {
		t.Errorf("search call raw = %s", parts[0].Native.Raw)
	}
	want := msg.Part{Kind: msg.KindText, Text: "It is sunny in Paris.", Citations: []msg.Citation{{URL: "https://example.com/paris", Title: "Paris weather"}}}
	if parts[4].Kind != want.Kind || parts[4].Text != want.Text || len(parts[4].Citations) != 1 || parts[4].Citations[0] != want.Citations[0] {
		t.Errorf("text = %+v, want %+v", parts[4], want)
	}
	if resp.Usage.WebSearches != 1 {
		t.Errorf("usage = %+v, want 1 web search", resp.Usage)
	}
}
