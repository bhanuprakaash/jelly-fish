package openai

import (
	"testing"

	"github.com/bhanuprakaash/jelly-fish/internal/msg"
	"github.com/bhanuprakaash/jelly-fish/internal/provider"
)

// The fixture is written by hand from OpenAI's documented Responses format,
// not recorded.
func TestStreamWebSearch(t *testing.T) {
	c, _ := sseServer(t, "web_search.sse")
	resp, _ := collect(t, c.Provider("k"), provider.Request{Model: mini, Messages: []msg.Message{msg.UserText("weather in Paris")}, WebSearch: true})

	parts := resp.Message.Parts
	if len(parts) != 2 {
		t.Fatalf("parts = %+v", parts)
	}
	if n := parts[0].Native; parts[0].Kind != msg.KindNative || n.Provider != Name || n.Type != "web_search_call" {
		t.Errorf("part 0 = %+v, want native web_search_call", parts[0])
	}
	wantCites := []msg.Citation{{URL: "https://example.com/paris", Title: "Paris weather"}, {URL: "https://example.com/paris/week", Title: "Paris this week"}}
	if parts[1].Kind != msg.KindText || parts[1].Text != "It is sunny in Paris." || len(parts[1].Citations) != 2 || parts[1].Citations[0] != wantCites[0] || parts[1].Citations[1] != wantCites[1] {
		t.Errorf("text = %+v, want citations %+v", parts[1], wantCites)
	}
	if resp.Usage.WebSearches != 1 {
		t.Errorf("usage = %+v, want 1 web search", resp.Usage)
	}
}

func TestStreamWebSearchBillsOnlySearchActions(t *testing.T) {
	c, _ := sseServer(t, "web_search_open_page.sse")
	resp, _ := collect(t, c.Provider("k"), provider.Request{Model: mini, Messages: []msg.Message{msg.UserText("go release notes")}, WebSearch: true})

	if resp.Usage.WebSearches != 1 {
		t.Errorf("usage = %+v, want 1 web search for one search and one open_page", resp.Usage)
	}
}
