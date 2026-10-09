package provider_test

import (
	"encoding/json"
	"testing"

	"github.com/bhanuprakaash/jelly-fish/internal/msg"
	"github.com/bhanuprakaash/jelly-fish/internal/provider"
)

func TestForeignSourcesListsCitedPagesAsText(t *testing.T) {
	searched := msg.Message{MsgV: msg.CurrentVersion, Role: msg.RoleAssistant, Parts: []msg.Part{
		{Kind: msg.KindNative, Native: &msg.Native{Provider: "anthropic", Type: "web_search_tool_result", Raw: json.RawMessage(`{}`)}},
		{Kind: msg.KindText, Text: "Sunny.", Citations: []msg.Citation{{URL: "https://example.com/paris", Title: "Paris weather"}}},
		{Kind: msg.KindText, Text: " Warm.", Citations: []msg.Citation{{URL: "https://example.com/paris", Title: "Paris weather"}, {URL: "https://example.com/uv"}}},
	}}
	history := []msg.Message{msg.UserText("weather?"), searched}

	got := provider.ForeignSources(history, "openai")
	want := " Warm.\n\nSources:\n- Paris weather (https://example.com/paris)\n- https://example.com/uv"
	if text := got[1].Parts[2].Text; text != want {
		t.Errorf("last text = %q, want %q", text, want)
	}
	if text := got[1].Parts[1].Text; text != "Sunny." {
		t.Errorf("first text = %q, want it unchanged", text)
	}
	if searched.Parts[2].Text != " Warm." {
		t.Error("the stored message was changed")
	}

	same := provider.ForeignSources(history, "anthropic")
	if text := same[1].Parts[2].Text; text != " Warm." {
		t.Errorf("same Provider text = %q, want it unchanged", text)
	}
}
