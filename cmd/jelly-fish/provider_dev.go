//go:build dev

package main

import (
	"github.com/bhanuprakaash/jelly-fish/internal/provider"
	"github.com/bhanuprakaash/jelly-fish/internal/provider/fake"
)

// newProvider wires the echo Fake Provider into dev builds only.
func newProvider() provider.Provider { return fake.Provider{} }
