// Package approver decides whether a tool call needs the User's approval
// (docs/design/approver.md §5.1).
package approver

import (
	"github.com/bhanuprakaash/jelly-fish/internal/connector"
	"github.com/bhanuprakaash/jelly-fish/internal/tool"
)

// Who decides a call.
const (
	ByUser = "user"
	ByMode = "mode:ask"
)

// ReasonChanged is why the User is asked again about a tool they approved.
const ReasonChanged = "This tool changed since you approved it."

// Verdict is whether a call waits for the User, and why.
type Verdict struct {
	Ask bool
	// By is who decides: the User when Ask is set, else the mode.
	By string
	// Reason is empty, or ReasonChanged when the tool's definition no longer
	// matches the one the User approved.
	Reason string
}

// Decide lets built-in tools run and makes every Connector tool ask. A nil t,
// a tool the registry lacks, runs and fails on its own.
func Decide(t tool.Tool) Verdict {
	ct, ok := t.(connector.Tool)
	if !ok {
		return Verdict{By: ByMode}
	}
	v := Verdict{Ask: true, By: ByUser}
	if ct.CurrentHash() != ct.Cached.Hash {
		v.Reason = ReasonChanged
	}
	return v
}
