package worker

import "github.com/bhanuprakaash/jelly-fish/internal/eventlog"

// StepKind is what exec does next.
type StepKind int

// Step kinds, in Decide's priority order.
const (
	// StepMarkInterrupted closes a turn a dead Worker left open.
	StepMarkInterrupted StepKind = iota + 1
	// StepMarkToolsInterrupted closes tool calls a dead Worker left started.
	StepMarkToolsInterrupted
	// StepStartTool runs the next batch of requested tool calls.
	StepStartTool
	// StepRequestTools records every call of the latest reply.
	StepRequestTools
	// StepStartTurn calls the Provider over the conversation so far.
	StepStartTurn
	// StepComplete parks the session awaiting the user.
	StepComplete
)

// maxPauses is how many pause_turn replies in a row the loop resumes; a
// Provider that keeps pausing ends the turn instead of running up cost.
const maxPauses = 5

// Parks reports whether the step ends the drive loop by changing status.
func (k StepKind) Parks() bool { return k == StepComplete }

// Spends reports whether the step makes the session do more work, so the
// Budget applies before it.
func (k StepKind) Spends() bool {
	return k == StepRequestTools || k == StepStartTool || k == StepStartTurn
}

// exceeded reports the first Budget dimension st has used up. Turns count
// only before a new turn, which is what they limit.
func exceeded(st State, newTurn bool) (dimension string, limit, used int64, ok bool) {
	dims := []struct {
		name string
		used int64
	}{{eventlog.DimTokens, st.TokensUsed}, {eventlog.DimDollars, st.CostMicros}}
	if newTurn {
		dims = append(dims, struct {
			name string
			used int64
		}{eventlog.DimTurns, int64(st.TurnsSinceUser)})
	}
	for _, d := range dims {
		if l := st.Limit(d.name); l > 0 && d.used >= l {
			return d.name, l, d.used, true
		}
	}
	return "", 0, 0, false
}

// Step is Decide's verdict for a State.
type Step struct {
	Kind StepKind
	// TurnID is the open turn a StepMarkInterrupted closes.
	TurnID string
}

// Decide picks the next Step for st. It is pure (event-log.md §5.4).
func Decide(st State) Step {
	switch {
	case st.OpenTurn != nil:
		return Step{Kind: StepMarkInterrupted, TurnID: st.OpenTurn.ID}
	case len(st.calls(CallStarted)) > 0:
		return Step{Kind: StepMarkToolsInterrupted}
	case len(st.calls(CallRequested)) > 0:
		return Step{Kind: StepStartTool}
	case len(st.calls(CallAsked)) > 0:
		return Step{Kind: StepRequestTools}
	case st.ToolsRan || len(st.PendingUserSeqs) > 0 || (st.Pauses > 0 && st.Pauses <= maxPauses):
		return Step{Kind: StepStartTurn}
	default:
		return Step{Kind: StepComplete}
	}
}
