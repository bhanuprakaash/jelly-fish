package worker

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/bhanuprakaash/jelly-fish/internal/connector"
	"github.com/bhanuprakaash/jelly-fish/internal/eventlog"
	"github.com/bhanuprakaash/jelly-fish/internal/stream"
	"github.com/bhanuprakaash/jelly-fish/internal/tool"
)

const (
	// defaultLegacyHold is how long a legacy elicitation holds its call and
	// the Worker's Lease (agent-loop.md D16).
	defaultLegacyHold = 5 * time.Minute
	elicitChannel     = "jf_elicit"
	// legacyKey is the request key of a legacy elicitation/create, which has none of its own.
	legacyKey = "elicitation/create"
)

// errHoldTimedOut ends a call whose legacy elicitation went unanswered.
var errHoldTimedOut = errors.New("needs user input; timed out")

// elicitWaiters holds the calls blocked on a legacy elicitation, so a
// jf_elicit NOTIFY can wake the one it names.
type elicitWaiters struct {
	mu    sync.Mutex
	chans map[string]chan struct{}
}

func (e *elicitWaiters) add(id string) <-chan struct{} {
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.chans == nil {
		e.chans = map[string]chan struct{}{}
	}
	ch := make(chan struct{}, 1)
	e.chans[id] = ch
	return ch
}

func (e *elicitWaiters) remove(id string) {
	e.mu.Lock()
	defer e.mu.Unlock()
	delete(e.chans, id)
}

// wake wakes the call waiting on id; an id this Worker does not hold is ignored.
func (e *elicitWaiters) wake(id string) {
	e.mu.Lock()
	ch := e.chans[id]
	e.mu.Unlock()
	if ch == nil {
		return
	}
	select {
	case ch <- struct{}{}:
	default:
	}
}

// resume is what a call that was answered sends its server: the answer under
// every request key, and the server's request state back as it came.
func (c Call) resume() *tool.Resume {
	if c.Answer == nil {
		return nil
	}
	r := &tool.Resume{RequestState: c.Elicitation.RequestState, InputResponses: map[string]tool.InputResponse{}}
	for _, key := range c.Elicitation.Keys {
		r.InputResponses[key] = tool.InputResponse{Action: c.Answer.Action, Content: c.Answer.Content}
	}
	return r
}

// elicitationEvent asks the User what a call's server wants to know.
func (w *Worker) elicitationEvent(id string, call Call, t tool.Tool, requests map[string]tool.InputRequest, state string, legacy bool) eventlog.NewEvent {
	asked := make(map[string]map[string]any, len(requests))
	for key, r := range requests {
		q := map[string]any{"mode": r.Mode, "message": r.Message}
		if len(r.RequestedSchema) > 0 {
			q["schema"] = r.RequestedSchema
		}
		if r.URL != "" {
			q["url"] = r.URL
		}
		asked[key] = q
	}
	p := map[string]any{"elicitation_id": id, "tool_call_id": call.ID, "requests": asked}
	if ct, ok := t.(connector.Tool); ok {
		p["connector_id"], p["connector"] = ct.Conn.ID, ct.Conn.Name
	}
	if state != "" {
		p["request_state"] = state
	}
	if legacy {
		p["legacy"] = true
	}
	return eventlog.NewEvent{Type: eventlog.TypeElicitationRequested, Actor: w.actor(), CorrelationID: call.ID, Payload: p}
}

// parkOnInput records the elicitations the batch's calls asked, if any, and
// parks the session until the User answers.
func (w *Worker) parkOnInput(ctx context.Context, c eventlog.Claim, f eventlog.Fence, asks []*eventlog.NewEvent) error {
	var evs []eventlog.NewEvent
	for _, ev := range asks {
		if ev != nil {
			evs = append(evs, *ev)
		}
	}
	if len(evs) == 0 {
		return nil
	}
	if _, err := w.store.AppendFenced(ctx, c.SessionID, f, evs, &eventlog.StatusChange{To: eventlog.StatusAwaitingUser, Reason: "elicitation"}); err != nil {
		return err
	}
	return errParked
}

// holdElicitation asks the User on behalf of a legacy elicitation/create and
// blocks for the answer, keeping the session running and its Lease held. The
// User's answer wakes it through jf_elicit; the Lease heartbeat's interval is
// the fallback poll.
func (w *Worker) holdElicitation(ctx context.Context, c eventlog.Claim, f eventlog.Fence, call Call, t tool.Tool, req tool.InputRequest) (tool.InputResponse, error) {
	id := uuid.NewString()
	wake := w.elicits.add(id)
	defer w.elicits.remove(id)
	ev := w.elicitationEvent(id, call, t, map[string]tool.InputRequest{legacyKey: req}, "", true)
	if _, err := w.store.AppendFenced(ctx, c.SessionID, f, []eventlog.NewEvent{ev}, nil); err != nil {
		return tool.InputResponse{}, err
	}
	deadline := time.NewTimer(w.legacyHold)
	defer deadline.Stop()
	poll := time.NewTicker(w.lease.Heartbeat)
	defer poll.Stop()
	for {
		select {
		case <-ctx.Done():
			return tool.InputResponse{}, ctx.Err()
		case <-deadline.C:
			return tool.InputResponse{}, errHoldTimedOut
		case <-wake:
		case <-poll.C:
		}
		var action string
		var content []byte
		err := w.pool.QueryRow(ctx, `
			SELECT payload->>'action', payload->'content' FROM events
			WHERE session_id = $1 AND type = $2 AND payload->>'elicitation_id' = $3`,
			c.SessionID, eventlog.TypeElicitationResolved, id).Scan(&action, &content)
		switch {
		case err == nil:
			return tool.InputResponse{Action: action, Content: content}, nil
		case !errors.Is(err, pgx.ErrNoRows):
			return tool.InputResponse{}, fmt.Errorf("look up elicitation answer: %w", err)
		}
	}
}

// publishProgress sends a call's progress to the session streams. It is never
// stored, and does not extend the call's timeout.
func (w *Worker) publishProgress(ctx context.Context, c eventlog.Claim, call Call, message string, progress, total float64) {
	text := message
	if text == "" && total > 0 {
		text = fmt.Sprintf("%g of %g", progress, total)
	}
	if text == "" {
		return
	}
	d := stream.Delta{SessionID: c.SessionID, TurnID: call.TurnID, Kind: stream.KindProgress, CallID: call.ID, Text: text}
	if err := w.deltas.Publish(ctx, d); err != nil && ctx.Err() == nil {
		w.logger.Warn("publish progress", "session_id", c.SessionID, "error", err)
	}
}
