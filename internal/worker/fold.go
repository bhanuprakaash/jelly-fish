package worker

import (
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
	// PendingUserSeqs are user.messages the latest turn has not seen.
	PendingUserSeqs []int64
	// Messages is the conversation so far, in the neutral format.
	Messages []msg.Message
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
		}
		if err := json.Unmarshal(e.Payload, &p); err != nil {
			return err
		}
		st.Status = eventlog.StatusRunnable
		st.Model = p.Agent.Model
	case eventlog.TypeUserMessage:
		var p struct {
			Message msg.Message `json:"message"`
		}
		if err := json.Unmarshal(e.Payload, &p); err != nil {
			return err
		}
		if err := p.Message.Validate(); err != nil {
			return err
		}
		st.Messages = append(st.Messages, p.Message)
		*userSeqs = append(*userSeqs, e.Seq)
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
	case eventlog.TypeLLMResponse:
		var p struct {
			Message msg.Message `json:"message"`
		}
		if err := json.Unmarshal(e.Payload, &p); err != nil {
			return err
		}
		if err := p.Message.Validate(); err != nil {
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
