package provider_test

import (
	"testing"

	"github.com/bhanuprakaash/jelly-fish/internal/provider"
)

func TestTitlePromptRoundTrips(t *testing.T) {
	const want = "Write a 3–6 word title for a chat that starts with: plan a trip. Reply with the title only."
	if got := provider.TitlePrompt("plan a trip"); got != want {
		t.Fatalf("TitlePrompt = %q, want %q", got, want)
	}
	for _, msg := range []string{"plan a trip", "", "multi\nline. Reply with the title only. oops"} {
		got, ok := provider.TitleMessage(provider.TitlePrompt(msg))
		if !ok || got != msg {
			t.Errorf("TitleMessage(TitlePrompt(%q)) = %q, %v", msg, got, ok)
		}
	}
	for _, not := range []string{"hello", "Write a 3–6 word title for a chat that starts with: x", ""} {
		if _, ok := provider.TitleMessage(not); ok {
			t.Errorf("TitleMessage(%q) matched", not)
		}
	}
}
