//go:build !dev

package main

import "github.com/bhanuprakaash/jelly-fish/internal/provider"

// newProvider returns no Provider: the Fake Provider must not exist outside
// dev builds, and there is no real one to wire.
func newProvider() provider.Provider { return nil }
