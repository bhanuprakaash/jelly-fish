//go:build dev

package main

import (
	"os"

	"github.com/bhanuprakaash/jelly-fish/internal/provider"
	"github.com/bhanuprakaash/jelly-fish/internal/provider/fake"
	"github.com/bhanuprakaash/jelly-fish/internal/tool"
	faketool "github.com/bhanuprakaash/jelly-fish/internal/tool/fake"
)

// buildDefaultModel is the model new sessions use when JF_DEFAULT_MODEL is
// unset.
const buildDefaultModel = fake.Name

// devFake wires the echo Fake Provider into dev builds only.
func devFake() provider.Provider { return fake.Provider{} }

// devTools wires the fake Tools into dev builds only; JF_FAKE_TOOL_LOG names
// the file slow_side_effect appends to.
func devTools() *tool.Registry { return faketool.New(os.Getenv("JF_FAKE_TOOL_LOG")) }
