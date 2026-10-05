//go:build !dev

package main

import (
	"github.com/bhanuprakaash/jelly-fish/internal/provider"
	"github.com/bhanuprakaash/jelly-fish/internal/tool"
)

// buildDefaultModel is the model new sessions use when JF_DEFAULT_MODEL is
// unset.
const buildDefaultModel = "claude-haiku-4-5-20251001"

// devFake returns no Provider: the Fake Provider must not exist outside dev
// builds.
func devFake() provider.Provider { return nil }

// devTools returns no Tools: the fake Tools must not exist outside dev builds.
func devTools() []tool.Tool { return nil }
