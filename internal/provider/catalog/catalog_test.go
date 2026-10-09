package catalog_test

import (
	"testing"

	"github.com/bhanuprakaash/jelly-fish/internal/provider"
	"github.com/bhanuprakaash/jelly-fish/internal/provider/catalog"
)

func TestLookup(t *testing.T) {
	c, err := catalog.Load()
	if err != nil {
		t.Fatal(err)
	}
	tests := []struct {
		id       string
		provider string
		window   int64
		thinking provider.Thinking
		forced   bool
	}{
		{"claude-haiku-4-5-20251001", "anthropic", 200_000, provider.ThinkingNone, true},
		{"claude-opus-5-5", "anthropic", 1_000_000, provider.ThinkingAdaptiveOnly, false},
		{"claude-sonnet-5-5", "anthropic", 1_000_000, provider.ThinkingAdaptiveOnly, false},
		{"gpt-6-astra", "openai", 1_050_000, provider.ThinkingAlwaysOn, true},
		{"gemini-3.8-flash", "gemini", 1_048_576, provider.ThinkingAlwaysOn, true},
	}
	for _, tc := range tests {
		t.Run(tc.id, func(t *testing.T) {
			m, ok := c.Lookup(tc.id)
			if !ok {
				t.Fatal("not in catalog")
			}
			if m.Provider != tc.provider || m.ContextWindow != tc.window || m.Thinking != tc.thinking || m.ForcedToolChoice != tc.forced || m.MaxOutput == 0 {
				t.Fatalf("got %+v", m)
			}
		})
	}
	if _, ok := c.Lookup("claude-nonexistent"); ok {
		t.Fatal("unknown id found")
	}
}

func TestPrices(t *testing.T) {
	c, err := catalog.Load()
	if err != nil {
		t.Fatal(err)
	}
	m, _ := c.Lookup("claude-haiku-4-5-20251001")
	want := provider.Prices{Input: 1, CacheRead: 0.1, CacheWrite5m: 1.25, CacheWrite1h: 2, Output: 5, WebSearch: 10}
	if m.Price == nil || *m.Price != want {
		t.Fatalf("haiku price = %+v, want %+v", m.Price, want)
	}
	for _, m := range c.All() {
		if m.Price == nil || m.Price.Input <= 0 || m.Price.Output <= 0 {
			t.Errorf("%s: unpriced: %+v", m.ID, m.Price)
		} else if m.WebSearch && m.Price.WebSearch <= 0 {
			t.Errorf("%s: searches without a search price", m.ID)
		}
	}
}

func TestAllKeepsFileOrder(t *testing.T) {
	c, err := catalog.Load()
	if err != nil {
		t.Fatal(err)
	}
	all := c.All()
	if len(all) == 0 || all[0].ID != "claude-fable-5-1" {
		t.Fatalf("first = %+v", all)
	}
	all[0].ID = "mutated"
	if c.All()[0].ID != "claude-fable-5-1" {
		t.Fatal("All shares its slice with the caller")
	}
}
