//go:build dev

package main

import (
	"github.com/bhanuprakaash/jelly-fish/internal/provider"
	"github.com/bhanuprakaash/jelly-fish/internal/provider/fake"
)

// buildDefaultModel is the model new sessions use when JF_DEFAULT_MODEL is
// unset.
const buildDefaultModel = fake.Name

// devFake wires the echo Fake Provider into dev builds only.
func devFake() provider.Provider { return fake.Provider{} }
