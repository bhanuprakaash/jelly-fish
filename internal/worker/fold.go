package worker

import (
	"cmp"
	"encoding/json"
	"fmt"

	"github.com/bhanuprakaash/jelly-fish/internal/eventlog"
	"github.com/bhanuprakaash/jelly-fish/internal/msg"
	"github.com/bhanuprakaash/jelly-fish/internal/provider"
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
	// Calls are the tool calls of the latest llm.response, in the order the
	// model asked.
	Calls []Call
	// ResultsAt is the index in Messages of the user message that holds the
	// tool results of Calls.
	ResultsAt int
	// ToolsSinceUser names the tools whose results arrived since the last
	// user.message, for taint (memory.md §5.2).
	ToolsSinceUser []string
	// ToolsRan is set while the latest reply's tool results wait for a turn
	// to read them.
	ToolsRan bool
	// Pauses counts the replies in a row that stopped with pause_turn.
	Pauses int
}

// CallStatus is how far a tool call has got.
type CallStatus int

// Call statuses, in the order a call passes through them.
const (
	CallAsked CallStatus = iota + 1
	CallRequested
	CallStarted
	CallDone
)

// Call is one tool call the model asked for.
type Call struct {
	ID   string
	Name string
	Args json.RawMessage
	// TurnID is the turn whose reply asked for the call.
	TurnID string
	Status CallStatus
	// Result is the tool_result part, once the call is done.
	Result *msg.Part
}

// calls lists the calls that have status.
func (st State) calls(status CallStatus) []Call {
	var out []Call
	for _, c := range st.Calls {
		if c.Status == status {
			out = append(out, c)
		}
	}
	return out
}

func (st *State) call(id string) (*Call, error) {
	for i := range st.Calls {
		if st.Calls[i].ID == id {
			return &st.Calls[i], nil
		}
	}
	return nil, fmt.Errorf("unknown tool call %q", id)
}

// setResult records c's result and rewrites the results message, so results
// sit in the order of the calls whatever order they finished in.
func (st *State) setResult(c *Call, part msg.Part) {
	c.Status = CallDone
	c.Result = &part
	var parts []msg.Part
	for _, other := range st.Calls {
		if other.Result != nil {
			parts = append(parts, *other.Result)
		}
	}
	st.Messages[st.ResultsAt].Parts = parts
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
	Provider        string
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
		st.ToolsSinceUser = nil
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
			Provider        string `json:"provider"`
			InputThroughSeq int64  `json:"input_through_seq"`
		}
		if err := json.Unmarshal(e.Payload, &p); err != nil {
			return err
		}
		st.OpenTurn = &Turn{ID: p.TurnID, Provider: p.Provider, InputThroughSeq: p.InputThroughSeq}
		st.TurnsStarted++
	case eventlog.TypeSessionRenamed:
		st.Renamed = true
	case eventlog.TypeLLMResponse:
		var p struct {
			Message    msg.Message         `json:"message"`
			StopReason provider.StopReason `json:"stop_reason"`
		}
		if err := json.Unmarshal(e.Payload, &p); err != nil {
			return err
		}
		var turnID, providerName string
		if st.OpenTurn != nil {
			st.InputThroughSeq = st.OpenTurn.InputThroughSeq
			turnID, providerName = st.OpenTurn.ID, st.OpenTurn.Provider
		}
		st.OpenTurn = nil
		st.Turns++
		st.Messages = append(st.Messages, p.Message)
		st.Calls = nil
		for _, part := range p.Message.Parts {
			if part.Kind == msg.KindToolUse {
				part.ToolUse.Provider = providerName
				st.Calls = append(st.Calls, Call{ID: part.ToolUse.ID, Name: part.ToolUse.Name, Args: part.ToolUse.Args, TurnID: turnID, Status: CallAsked})
			}
		}
		if p.StopReason == provider.StopReasonPauseTurn {
			st.Pauses++
		} else {
			st.Pauses = 0
		}
		st.ToolsRan = len(st.Calls) > 0
		if st.ToolsRan {
			st.ResultsAt = len(st.Messages)
			st.Messages = append(st.Messages, msg.Message{MsgV: msg.CurrentVersion, Role: msg.RoleUser})
		}
	case eventlog.TypeToolRequested, eventlog.TypeToolStarted:
		var p struct {
			CallID string `json:"tool_call_id"`
		}
		if err := json.Unmarshal(e.Payload, &p); err != nil {
			return err
		}
		c, err := st.call(p.CallID)
		if err != nil {
			return err
		}
		c.Status = CallRequested
		if e.Type == eventlog.TypeToolStarted {
			c.Status = CallStarted
		}
	case eventlog.TypeToolCompleted:
		var p struct {
			CallID string      `json:"tool_call_id"`
			Result msg.Message `json:"result"`
		}
		if err := json.Unmarshal(e.Payload, &p); err != nil {
			return err
		}
		c, err := st.call(p.CallID)
		if err != nil {
			return err
		}
		if len(p.Result.Parts) != 1 || p.Result.Parts[0].Kind != msg.KindToolResult {
			return fmt.Errorf("tool call %q: result is not one tool_result", p.CallID)
		}
		st.setResult(c, p.Result.Parts[0])
		st.ToolsSinceUser = append(st.ToolsSinceUser, c.Name)
	case eventlog.TypeToolInterrupted:
		var p struct {
			CallID string `json:"tool_call_id"`
			Note   string `json:"note"`
		}
		if err := json.Unmarshal(e.Payload, &p); err != nil {
			return err
		}
		c, err := st.call(p.CallID)
		if err != nil {
			return err
		}
		st.setResult(c, msg.Part{Kind: msg.KindToolResult, ToolResult: &msg.ToolResult{
			CallID: p.CallID, IsError: true, Parts: []msg.Part{{Kind: msg.KindText, Text: p.Note}},
		}})
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
			if p.Unit != eventlog.UnitWebSearchRequests {
				st.TokensUsed += p.Quantity
			}
			st.CostMicros += p.CostMicros
		}
	}
	return nil
}
