package worker

// StepKind is what exec does next.
type StepKind int

// Step kinds, in Decide's priority order.
const (
	// StepMarkInterrupted closes a turn a dead Worker left open.
	StepMarkInterrupted StepKind = iota + 1
	// StepStartTurn calls the Provider over the conversation so far.
	StepStartTurn
	// StepComplete parks the session awaiting the user.
	StepComplete
)

// Parks reports whether the step ends the drive loop by changing status.
func (k StepKind) Parks() bool { return k == StepComplete }

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
	case len(st.PendingUserSeqs) > 0:
		return Step{Kind: StepStartTurn}
	default:
		return Step{Kind: StepComplete}
	}
}
