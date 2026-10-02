package main

import (
	"cmp"
	"fmt"

	"github.com/bhanuprakaash/jelly-fish/internal/config"
	"github.com/bhanuprakaash/jelly-fish/internal/provider/anthropic"
	"github.com/bhanuprakaash/jelly-fish/internal/provider/catalog"
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
	if info.Provider != anthropic.Name {
		return "", fmt.Errorf("JF_DEFAULT_MODEL %q: provider %s is not supported", model, info.Provider)
	}
	return model, nil
}
