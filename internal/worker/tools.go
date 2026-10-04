package worker

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/bhanuprakaash/jelly-fish/internal/eventlog"
	"github.com/bhanuprakaash/jelly-fish/internal/msg"
	"github.com/bhanuprakaash/jelly-fish/internal/tool"
	tooltracing "github.com/bhanuprakaash/jelly-fish/internal/tool/tracing"
)

// maxBatch is how many parallel-safe calls run at once (agent-loop.md D1).
const maxBatch = 4

const (
	noteWorkerLost = "outcome unknown; check before retrying"
	noteUserStop   = "interrupted by user; it may have partially run"
	noteNotRun     = "not run: interrupted by user"
)

// requestTools records every call of the latest reply in one transaction.
func (w *Worker) requestTools(ctx context.Context, c eventlog.Claim, f eventlog.Fence, st State) error {
	var evs []eventlog.NewEvent
	for _, call := range st.calls(CallAsked) {
		evs = append(evs, w.requestedEvent(call))
	}
	_, err := w.store.AppendFenced(ctx, c.SessionID, f, evs, nil)
	return err
}

func (w *Worker) requestedEvent(call Call) eventlog.NewEvent {
	return eventlog.NewEvent{
		Type:          eventlog.TypeToolRequested,
		Actor:         w.actor(),
		CorrelationID: call.ID,
		Payload:       map[string]any{"tool_call_id": call.ID, "tool": call.Name, "args": call.Args},
	}
}

// nextBatch is the first requested call plus, when it is parallel-safe, the
// parallel-safe calls right after it, up to maxBatch.
func (w *Worker) nextBatch(st State) []Call {
	reqs := st.calls(CallRequested)
	if len(reqs) == 0 || !w.parallelSafe(reqs[0]) {
		return reqs[:min(1, len(reqs))]
	}
	n := 1
	for n < len(reqs) && n < maxBatch && w.parallelSafe(reqs[n]) {
		n++
	}
	return reqs[:n]
}

func (w *Worker) parallelSafe(call Call) bool {
	t, ok := w.tools.Get(call.Name)
	return ok && t.Def().ParallelSafe
}

// startTools commits tool.call.started for the next batch, and only then runs
// it: a started call is never run again (event-log.md §5.5).
func (w *Worker) startTools(ctx context.Context, c eventlog.Claim, f eventlog.Fence, st State) error {
	batch := w.nextBatch(st)
	evs := make([]eventlog.NewEvent, len(batch))
	for i, call := range batch {
		evs[i] = eventlog.NewEvent{
			Type:          eventlog.TypeToolStarted,
			Actor:         w.actor(),
			CorrelationID: call.ID,
			Payload:       map[string]string{"tool_call_id": call.ID},
		}
	}
	seqs, err := w.store.AppendFenced(ctx, c.SessionID, f, evs, nil)
	if err != nil {
		return err
	}

	errs := make([]error, len(batch))
	var wg sync.WaitGroup
	for i, call := range batch {
		wg.Go(func() {
			errs[i] = w.runCall(ctx, c, f, call, fmt.Sprintf("%s:%d", c.SessionID, seqs[i]))
		})
	}
	wg.Wait()
	return errors.Join(errs...)
}

// runCall calls one tool and records its result. A call cut short by ctx
// records nothing: whoever ends the drive closes it.
func (w *Worker) runCall(ctx context.Context, c eventlog.Claim, f eventlog.Fence, call Call, key string) error {
	began := time.Now()
	res := w.invoke(ctx, c, call, key)
	if err := ctx.Err(); err != nil {
		return err
	}
	_, err := w.store.AppendFenced(ctx, c.SessionID, f, []eventlog.NewEvent{w.completedEvent(call.ID, res, time.Since(began))}, nil)
	return err
}

// invoke turns every way a call can fail into an error Result for the model.
func (w *Worker) invoke(ctx context.Context, c eventlog.Claim, call Call, key string) tool.Result {
	t, ok := w.tools.Get(call.Name)
	if !ok {
		return tool.TextResult(fmt.Sprintf("unknown tool %q", call.Name), true)
	}
	timeout := cmp.Or(t.Def().Timeout, tool.DefaultTimeout)
	cctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	res, err := w.traced(t, c, call).Call(cctx, tool.CallInput{CallID: call.ID, IdempotencyKey: key, Args: call.Args})
	switch {
	case ctx.Err() != nil:
		return res
	case errors.Is(cctx.Err(), context.DeadlineExceeded):
		return tool.TextResult(fmt.Sprintf("timed out after %s; the call may have partially run", timeout), true)
	case err != nil:
		w.logger.Error("tool call", "session_id", c.SessionID, "tool", call.Name, "error", err)
		return tool.TextResult("tool failed", true)
	}
	return res
}

func (w *Worker) traced(t tool.Tool, c eventlog.Claim, call Call) tool.Tool {
	if w.gateway.Tracer == nil {
		return t
	}
	return tooltracing.Wrap(t, w.gateway.Tracer, tooltracing.IDs{SessionID: c.SessionID.String(), TurnID: call.TurnID})
}

func (w *Worker) completedEvent(id string, res tool.Result, d time.Duration) eventlog.NewEvent {
	return eventlog.NewEvent{
		Type:          eventlog.TypeToolCompleted,
		Actor:         w.actor(),
		CorrelationID: id,
		Payload: map[string]any{
			"tool_call_id": id,
			"result": msg.Message{MsgV: msg.CurrentVersion, Role: msg.RoleUser, Parts: []msg.Part{
				{Kind: msg.KindToolResult, ToolResult: &msg.ToolResult{CallID: id, Parts: res.Content, IsError: res.IsError}},
			}},
			"is_error":    res.IsError,
			"duration_ms": d.Milliseconds(),
		},
	}
}

func (w *Worker) interruptedEvent(id, reason, note string) eventlog.NewEvent {
	return eventlog.NewEvent{
		Type:          eventlog.TypeToolInterrupted,
		Actor:         w.actor(),
		CorrelationID: id,
		Payload:       map[string]string{"tool_call_id": id, "reason": reason, "note": note},
	}
}

// markToolsInterrupted closes the calls a dead Worker left started. Their
// outcome is unknown and they are never run again.
func (w *Worker) markToolsInterrupted(ctx context.Context, c eventlog.Claim, f eventlog.Fence, st State) error {
	var evs []eventlog.NewEvent
	for _, call := range st.calls(CallStarted) {
		evs = append(evs, w.interruptedEvent(call.ID, "worker_lost", noteWorkerLost))
	}
	_, err := w.store.AppendFenced(ctx, c.SessionID, f, evs, nil)
	return err
}

// userStopEvents closes every call of the batch that has no result, so the
// model's next prompt has an answer for each tool_use (agent-loop.md §5.4).
func (w *Worker) userStopEvents(st State) []eventlog.NewEvent {
	var evs []eventlog.NewEvent
	for _, call := range st.Calls {
		switch call.Status {
		case CallStarted:
			evs = append(evs, w.interruptedEvent(call.ID, "user_interrupt", noteUserStop))
		case CallAsked:
			evs = append(evs, w.requestedEvent(call), w.completedEvent(call.ID, tool.TextResult(noteNotRun, true), 0))
		case CallRequested:
			evs = append(evs, w.completedEvent(call.ID, tool.TextResult(noteNotRun, true), 0))
		}
	}
	return evs
}
