package main

import (
	"cmp"
	"fmt"
	"log/slog"

	"github.com/bhanuprakaash/jelly-fish/internal/config"
	"github.com/bhanuprakaash/jelly-fish/internal/provider/anthropic"
	"github.com/bhanuprakaash/jelly-fish/internal/provider/catalog"
	"github.com/bhanuprakaash/jelly-fish/internal/provider/gemini"
	"github.com/bhanuprakaash/jelly-fish/internal/provider/openai"
	"github.com/bhanuprakaash/jelly-fish/internal/worker"
)

// defaultModel resolves the model new sessions use: JF_DEFAULT_MODEL, else
// the build's own default. It must be one a Worker of this build can serve.
func defaultModel(cfg config.Config, cat catalog.Catalog) (string, error) {
	model := cmp.Or(cfg.DefaultModel, buildDefaultModel)
	if fake := devFake(); fake != nil && model == fake.Name() {
		return model, nil
	}
	info, ok := cat.Lookup(model)
	if !ok {
		return "", fmt.Errorf("JF_DEFAULT_MODEL %q is not in the model catalog", model)
	}
	if _, ok := adapters(cat, nil)[info.Provider]; !ok {
		return "", fmt.Errorf("JF_DEFAULT_MODEL %q: provider %s is not supported", model, info.Provider)
	}
	return model, nil
}

// adapters are the Providers this build serves, by name. logger may be nil.
func adapters(cat catalog.Catalog, logger *slog.Logger) map[string]worker.Adapter {
	return map[string]worker.Adapter{
		anthropic.Name: anthropic.Client{Catalog: cat},
		openai.Name:    openai.Client{Catalog: cat},
		gemini.Name:    gemini.Client{Catalog: cat, Logger: logger},
	}
}
