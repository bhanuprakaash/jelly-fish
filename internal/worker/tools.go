package worker

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"slices"
	"sync"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/bhanuprakaash/jelly-fish/internal/eventlog"
	"github.com/bhanuprakaash/jelly-fish/internal/memory"
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
	if p, byArgs := t.(tool.ParallelByArgs); ok && byArgs {
		return p.ParallelSafeCall(call.Args)
	}
	return ok && t.Def().ParallelSafe
}

// redactMemoryCalls swaps the memory text in the args of memory calls for a
// placeholder, so the stored reply holds none, and keeps the real args for
// this drive to run the calls with.
func (w *Worker) redactMemoryCalls(sid uuid.UUID, parts []msg.Part) []msg.Part {
	parts = slices.Clone(parts)
	for i, p := range parts {
		if p.Kind != msg.KindToolUse || p.ToolUse.Name != memory.ToolName {
			continue
		}
		args, changed := memory.Redact(p.ToolUse.Args)
		if !changed {
			continue
		}
		w.held.put(sid, p.ToolUse.ID, p.ToolUse.Args)
		use := *p.ToolUse
		use.Args = args
		parts[i].ToolUse = &use
	}
	return parts
}

// session is the Session a tool call sees.
func (w *Worker) session(c eventlog.Claim, st State) tool.Session {
	tainted := slices.ContainsFunc(st.ToolsSinceUser, func(name string) bool {
		t, ok := w.tools.Get(name)
		return ok && t.Def().Untrusted
	})
	return tool.Session{ID: c.SessionID, UserID: c.UserID, WorkspaceID: c.WorkspaceID, ProjectID: c.ProjectID, Child: !st.TopLevel, Tainted: tainted}
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

	sess := w.session(c, st)
	errs := make([]error, len(batch))
	var wg sync.WaitGroup
	for i, call := range batch {
		wg.Go(func() {
			errs[i] = w.runCall(ctx, c, f, call, fmt.Sprintf("%s:%d", c.SessionID, seqs[i]), sess)
		})
	}
	wg.Wait()
	return errors.Join(errs...)
}

// runCall calls one tool and records its result. A call cut short by ctx
// records nothing: whoever ends the drive closes it.
func (w *Worker) runCall(ctx context.Context, c eventlog.Claim, f eventlog.Fence, call Call, key string, sess tool.Session) error {
	began := time.Now()
	res := w.invoke(ctx, c, call, key, sess)
	if err := ctx.Err(); err != nil {
		return err
	}
	took := time.Since(began)
	_, err := w.store.AppendFencedFunc(ctx, c.SessionID, f, func(ctx context.Context, tx pgx.Tx) ([]eventlog.NewEvent, error) {
		var evs []eventlog.NewEvent
		if res.Commit != nil {
			var added []tool.Event
			res, added = w.commit(ctx, tx, c, call, res)
			for _, e := range added {
				evs = append(evs, eventlog.NewEvent{Type: e.Type, Actor: w.actor(), CorrelationID: call.ID, Payload: e.Payload})
			}
		}
		return append(evs, w.completedEvent(call.ID, res, took)), nil
	})
	return err
}

// commit runs res.Commit in a savepoint, so a failure turns the call into an
// error Result instead of failing the append it rides on.
func (w *Worker) commit(ctx context.Context, tx pgx.Tx, c eventlog.Claim, call Call, res tool.Result) (tool.Result, []tool.Event) {
	var out tool.Result
	var added []tool.Event
	err := pgx.BeginFunc(ctx, tx, func(sp pgx.Tx) error {
		var err error
		out, added, err = res.Commit(ctx, sp)
		return err
	})
	if err != nil {
		w.logger.Error("tool commit", "session_id", c.SessionID, "tool", call.Name, "error", err)
		return tool.TextResult("tool failed", true), nil
	}
	return out, added
}

// invoke turns every way a call can fail into an error Result for the model.
func (w *Worker) invoke(ctx context.Context, c eventlog.Claim, call Call, key string, sess tool.Session) tool.Result {
	t, ok := w.tools.Get(call.Name)
	if !ok || c.Incognito && call.Name == memory.ToolName {
		return tool.TextResult(fmt.Sprintf("unknown tool %q", call.Name), true)
	}
	args := call.Args
	if call.Name == memory.ToolName {
		kept, found := w.held.take(c.SessionID, call.ID)
		switch {
		case found:
			args = kept
		case memory.Lost(args):
			return tool.TextResult("arguments lost; call again", true)
		}
	}
	timeout := cmp.Or(t.Def().Timeout, tool.DefaultTimeout)
	cctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	res, err := w.traced(t, c, call).Call(cctx, tool.CallInput{CallID: call.ID, IdempotencyKey: key, Args: args, Session: sess})
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
