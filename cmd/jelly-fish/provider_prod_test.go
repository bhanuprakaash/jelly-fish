//go:build !dev

package main

import (
	"testing"

	"github.com/bhanuprakaash/jelly-fish/internal/config"
	"github.com/bhanuprakaash/jelly-fish/internal/provider/catalog"
)

func TestNonDevBuildHasNoFakeProvider(t *testing.T) {
	if devFake() != nil {
		t.Fatal("non-dev build wired the Fake Provider")
	}
}

func TestNonDevBuildHasNoFakeTools(t *testing.T) {
	if devTools() != nil {
		t.Fatal("non-dev build wired the fake Tools")
	}
}

func TestDefaultModel(t *testing.T) {
	cat, err := catalog.Load()
	if err != nil {
		t.Fatal(err)
	}
	tests := []struct {
		env, want string
		ok        bool
	}{
		{"", "claude-haiku-4-5-20251001", true},
		{"claude-opus-5-5", "claude-opus-5-5", true},
		{"fake", "", false},
		{"gpt-6-astra", "gpt-6-astra", true},
		{"gemini-3.8-flash", "gemini-3.8-flash", true},
		{"no-such-model", "", false},
	}
	for _, tc := range tests {
		t.Run(tc.env, func(t *testing.T) {
			got, err := defaultModel(config.Config{DefaultModel: tc.env}, cat)
			if (err == nil) != tc.ok || got != tc.want {
				t.Fatalf("defaultModel(%q) = %q, %v", tc.env, got, err)
			}
		})
	}
}
