// Package catalog is the Model Catalog: limits and capabilities of known
// models, embedded from models.json and reviewed like code
// (provider-gateway.md §3.4).
package catalog

import (
	_ "embed"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/bhanuprakaash/jelly-fish/internal/provider"
)

//go:embed models.json
var modelsJSON []byte

// Catalog looks up models by id.
type Catalog struct {
	byID map[string]provider.ModelInfo
}

// Load parses the embedded catalog and rejects an incomplete entry, so a bad
// edit fails at startup.
func Load() (Catalog, error) {
	var f struct {
		Models []provider.ModelInfo `json:"models"`
	}
	if err := json.Unmarshal(modelsJSON, &f); err != nil {
		return Catalog{}, fmt.Errorf("parse models.json: %w", err)
	}
	c := Catalog{byID: make(map[string]provider.ModelInfo, len(f.Models))}
	for _, m := range f.Models {
		if err := check(m); err != nil {
			return Catalog{}, fmt.Errorf("models.json %q: %w", m.ID, err)
		}
		if _, dup := c.byID[m.ID]; dup {
			return Catalog{}, fmt.Errorf("models.json: duplicate id %q", m.ID)
		}
		c.byID[m.ID] = m
	}
	return c, nil
}

func check(m provider.ModelInfo) error {
	switch {
	case m.Provider != "anthropic" && m.Provider != "openai" && m.Provider != "gemini":
		return fmt.Errorf("unknown provider %q", m.Provider)
	case m.ContextWindow <= 0 || m.MaxOutput <= 0:
		return errors.New("context_window and max_output must be positive")
	case m.Thinking != provider.ThinkingAdaptiveOnly && m.Thinking != provider.ThinkingAlwaysOn && m.Thinking != provider.ThinkingNone:
		return fmt.Errorf("unknown thinking mode %q", m.Thinking)
	}
	return nil
}

// Lookup returns the entry for id, or false if the catalog doesn't know it.
func (c Catalog) Lookup(id string) (provider.ModelInfo, bool) {
	m, ok := c.byID[id]
	return m, ok
}
