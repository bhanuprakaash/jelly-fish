package gemini

import (
	"testing"

	"github.com/bhanuprakaash/jelly-fish/internal/msg"
	"github.com/bhanuprakaash/jelly-fish/internal/provider"
)

// The fixture is written by hand from Google's documented grounding
// metadata, not recorded.
func TestStreamWebSearch(t *testing.T) {
	c, _ := sseServer(t, "web_search.sse")
	resp, _ := collect(t, c.Provider("k"), provider.Request{Model: flash, Messages: []msg.Message{msg.UserText("weather in Paris")}, WebSearch: true})

	parts := resp.Message.Parts
	if len(parts) != 2 {
		t.Fatalf("parts = %+v", parts)
	}
	wantCites := []msg.Citation{
		{URL: "https://vertexaisearch.cloud.google.com/grounding-api-redirect/AbF9wX", Title: "example.com"},
		{URL: "https://vertexaisearch.cloud.google.com/grounding-api-redirect/Zq81mK", Title: "weather.example.org"},
	}
	if parts[0].Kind != msg.KindText || parts[0].Text != "It is sunny in Paris." || len(parts[0].Citations) != 2 || parts[0].Citations[0] != wantCites[0] || parts[0].Citations[1] != wantCites[1] {
		t.Errorf("text = %+v, want citations %+v", parts[0], wantCites)
	}
	if n := parts[1].Native; parts[1].Kind != msg.KindNative || n.Provider != Name || n.Type != "grounding_metadata" {
		t.Errorf("part 1 = %+v, want native grounding_metadata", parts[1])
	}
	if resp.Usage.WebSearches != 2 {
		t.Errorf("usage = %+v, want 2 web searches", resp.Usage)
	}
}
