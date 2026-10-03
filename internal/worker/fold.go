package worker

import (
	"cmp"
	"encoding/json"
	"fmt"

	"github.com/bhanuprakaash/jelly-fish/internal/eventlog"
	"github.com/bhanuprakaash/jelly-fish/internal/msg"
)

// State is a session folded from its events; it mirrors the sessions row's
// projected columns plus what Decide needs (event-log.md §5.4).
type State struct {
	LastSeq    int64
	Status     string
	Model      string
	TokensUsed int64
	CostMicros int64
	Turns      int
	// InputThroughSeq is the last_seq the latest completed turn was started
	// from. An interrupted turn doesn't advance it, so its input is re-run.
	InputThroughSeq int64
	// OpenTurn is set for a turn.started with no llm.response or
	// turn.interrupted after it.
	OpenTurn *Turn
	// RetryStep is how many retryable session.errors came since the last
	// user.message; it picks the next sleep of the 1/5/15 minute backoff
	// (agent-loop.md §5.3).
	RetryStep int
	// PendingUserSeqs are user.messages the latest turn has not seen.
	PendingUserSeqs []int64
	// Messages is the conversation so far, in the neutral format.
	Messages []msg.Message
	// TurnsStarted counts turn.started events, so it is 0 only before the
	// session's first Turn.
	TurnsStarted int
	// Renamed is set once any session.renamed exists, from either side.
	Renamed bool
	// Trigger is what started the session; anything but user_message marks
	// a System Session.
	Trigger string
	// TopLevel is false for a Child Session.
	TopLevel bool
}

// wantsTitle reports whether the session is a top-level chat still waiting
// for its first Turn and a Title: the Worker then asks for one alongside
// that Turn (event-log.md D45).
func (st State) wantsTitle() bool {
	return st.TurnsStarted == 0 && !st.Renamed && st.TopLevel && st.Trigger == eventlog.TriggerUserMessage && len(st.Messages) > 0
}

// Turn identifies one model call.
type Turn struct {
	ID              string
	InputThroughSeq int64
}

// Fold rebuilds a session's State from its events. It is pure: no I/O, no
// clock, so harness changes between crashes are safe.
func Fold(evs []eventlog.Event) (State, error) {
	var st State
	var userSeqs []int64
	for _, e := range evs {
		st.LastSeq = e.Seq
		if err := st.apply(e, &userSeqs); err != nil {
			return State{}, fmt.Errorf("fold %s seq %d: %w", e.Type, e.Seq, err)
		}
	}
	for _, seq := range userSeqs {
		if seq > st.InputThroughSeq {
			st.PendingUserSeqs = append(st.PendingUserSeqs, seq)
		}
	}
	return st, nil
}

func (st *State) apply(e eventlog.Event, userSeqs *[]int64) error {
	switch e.Type {
	case eventlog.TypeSessionCreated:
		var p struct {
			Agent struct {
				Model string `json:"model"`
			} `json:"agent"`
			Trigger  string  `json:"trigger"`
			ParentID *string `json:"parent_id"`
		}
		if err := json.Unmarshal(e.Payload, &p); err != nil {
			return err
		}
		st.Status = eventlog.StatusRunnable
		st.Model = p.Agent.Model
		st.Trigger = cmp.Or(p.Trigger, eventlog.TriggerUserMessage)
		st.TopLevel = p.ParentID == nil
	case eventlog.TypeConfigChanged:
		var p struct {
			Model string `json:"model"`
		}
		if err := json.Unmarshal(e.Payload, &p); err != nil {
			return err
		}
		if p.Model != "" {
			st.Model = p.Model
		}
	case eventlog.TypeUserMessage:
		var p struct {
			Message msg.Message `json:"message"`
		}
		if err := json.Unmarshal(e.Payload, &p); err != nil {
			return err
		}
		st.Messages = append(st.Messages, p.Message)
		*userSeqs = append(*userSeqs, e.Seq)
		st.RetryStep = 0
	case eventlog.TypeStatusChanged:
		var p struct {
			To string `json:"to"`
		}
		if err := json.Unmarshal(e.Payload, &p); err != nil {
			return err
		}
		st.Status = p.To
	case eventlog.TypeTurnStarted:
		var p struct {
			TurnID          string `json:"turn_id"`
			InputThroughSeq int64  `json:"input_through_seq"`
		}
		if err := json.Unmarshal(e.Payload, &p); err != nil {
			return err
		}
		st.OpenTurn = &Turn{ID: p.TurnID, InputThroughSeq: p.InputThroughSeq}
		st.TurnsStarted++
	case eventlog.TypeSessionRenamed:
		st.Renamed = true
	case eventlog.TypeLLMResponse:
		var p struct {
			Message msg.Message `json:"message"`
		}
		if err := json.Unmarshal(e.Payload, &p); err != nil {
			return err
		}
		if st.OpenTurn != nil {
			st.InputThroughSeq = st.OpenTurn.InputThroughSeq
		}
		st.OpenTurn = nil
		st.Turns++
		st.Messages = append(st.Messages, p.Message)
	case eventlog.TypeTurnInterrupted:
		st.OpenTurn = nil
	case eventlog.TypeSessionError:
		var p struct {
			Retryable bool `json:"retryable"`
		}
		if err := json.Unmarshal(e.Payload, &p); err != nil {
			return err
		}
		if p.Retryable {
			st.RetryStep++
		}
		// Only an error the turn itself raised ends it; a crash_loop error
		// has no turn.
		if st.OpenTurn != nil && e.CorrelationID == st.OpenTurn.ID {
			st.OpenTurn = nil
		}
	case eventlog.TypeUsageRecorded:
		var p eventlog.UsageRecorded
		if err := json.Unmarshal(e.Payload, &p); err != nil {
			return err
		}
		if p.Kind == eventlog.KindLLM {
			st.TokensUsed += p.Quantity
			st.CostMicros += p.CostMicros
		}
	}
	return nil
}
