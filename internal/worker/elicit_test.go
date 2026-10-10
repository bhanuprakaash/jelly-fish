package worker_test

import (
	"context"
	"encoding/json"
	"reflect"
	"slices"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/bhanuprakaash/jelly-fish/internal/auth"
	"github.com/bhanuprakaash/jelly-fish/internal/connector"
	"github.com/bhanuprakaash/jelly-fish/internal/eventlog"
	"github.com/bhanuprakaash/jelly-fish/internal/mcpclient"
	"github.com/bhanuprakaash/jelly-fish/internal/mcpclient/mcptest"
	"github.com/bhanuprakaash/jelly-fish/internal/msg"
	"github.com/bhanuprakaash/jelly-fish/internal/provider"
	"github.com/bhanuprakaash/jelly-fish/internal/stream"
	"github.com/bhanuprakaash/jelly-fish/internal/testdb"
	"github.com/bhanuprakaash/jelly-fish/internal/tool"
	"github.com/bhanuprakaash/jelly-fish/internal/worker"
)

// callOf is a reply that calls name once, as c1, with args.
func callOf(name, args string) provider.Response {
	m := msg.AssistantText("on it")
	m.Parts = append(m.Parts, msg.Part{Kind: msg.KindToolUse, ToolUse: &msg.ToolUse{ID: "c1", Name: name, Args: json.RawMessage(args)}})
	return provider.Response{Message: m, StopReason: provider.StopReasonToolUse}
}

// elicitRun is a session whose model calls the Notion connector tool name
// once, with the args {"q":"plans"}, then finishes.
type elicitRun struct {
	*connectorEnv
	p    *replies
	user auth.User
	srv  *mcptest.Server
	sid  uuid.UUID
}

func newElicitRun(t *testing.T, opts mcptest.Options, name string, add func(*mcptest.Server), builtins ...tool.Tool) *elicitRun {
	t.Helper()
	p := &replies{list: []provider.Response{callOf("notion__"+name, `{"q":"plans"}`), done()}}
	e := newConnectorEnv(t, p, builtins...)
	srv := mcptest.Start(t, opts)
	add(srv)
	user := testdb.NewUser(t, e.pool)
	connID := e.addConnector(t, user.ID, srv, time.Minute)
	raw := mcpclient.RawTool{Name: name, InputSchema: json.RawMessage(`{"type":"object"}`)}
	if err := e.store.Replace(t.Context(), connID, []connector.CachedTool{connector.FromRaw(raw)}); err != nil {
		t.Fatal(err)
	}
	return &elicitRun{connectorEnv: e, p: p, user: user, srv: srv}
}

// begin starts the session and approves the call, the way every connector call needs.
func (r *elicitRun) begin(t *testing.T) {
	t.Helper()
	r.sid = r.newSession(t, r.user.Scope())
	waitStatus(t, r.pool, r.sid, eventlog.StatusAwaitingApproval, 1)
	resolveLatest(t, r.pool, r.user.Scope(), r.sid, eventlog.Answer{Decision: eventlog.DecisionAllow})
}

// awaitElicitation waits for the nth elicitation.requested and returns its payload.
func (r *elicitRun) awaitElicitation(t *testing.T, n int) map[string]any {
	t.Helper()
	deadline := time.Now().Add(15 * time.Second)
	for time.Now().Before(deadline) {
		asked := ofType(loadEvents(t, r.pool, r.sid), eventlog.TypeElicitationRequested)
		if len(asked) == n {
			return asked[n-1].Payload
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("elicitation %d never opened", n)
	return nil
}

func (r *elicitRun) answer(t *testing.T, payload map[string]any, a eventlog.ElicitationAnswer) {
	t.Helper()
	id, err := uuid.Parse(payload["elicitation_id"].(string))
	if err != nil {
		t.Fatal(err)
	}
	if err := eventlog.NewRepo(r.pool, "fake").ResolveElicitation(t.Context(), r.user.Scope(), r.sid, id, a); err != nil {
		t.Fatal(err)
	}
}

// leaseHeld reports whether a Worker holds the session.
func (r *elicitRun) leaseHeld(t *testing.T) bool {
	t.Helper()
	var held bool
	if err := r.pool.QueryRow(t.Context(), `SELECT lease_owner IS NOT NULL FROM sessions WHERE id = $1`, r.sid).Scan(&held); err != nil {
		t.Fatal(err)
	}
	return held
}

// modelSaw is the text of the call's result, as the model's next prompt held it.
func (r *elicitRun) modelSaw() string {
	return r.p.requests()[1].Messages[2].Parts[0].ToolResult.Parts[0].Text
}

func decode(t *testing.T, raw json.RawMessage) any {
	t.Helper()
	var v any
	if err := json.Unmarshal(raw, &v); err != nil {
		t.Fatal(err)
	}
	return v
}

func TestInputRequiredParksAndTheAnswerResumesTheSameCall(t *testing.T) {
	r := newElicitRun(t, mcptest.Options{}, "ask", func(s *mcptest.Server) { s.AddInputRequiredTool("ask") })
	r.begin(t)

	card := r.awaitElicitation(t, 1)
	waitStatus(t, r.pool, r.sid, eventlog.StatusAwaitingUser, 1)
	evs := loadEvents(t, r.pool, r.sid)
	wantRequests := map[string]any{"confirm": map[string]any{"mode": "form", "message": "Proceed?"}}
	if card["request_state"] != "state-1" || card["tool_call_id"] != "c1" || card["connector"] != "Notion" || !reflect.DeepEqual(card["requests"], wantRequests) {
		t.Fatalf("elicitation.requested = %v", card)
	}
	if n := len(ofType(evs, eventlog.TypeToolCompleted)); n != 0 || r.leaseHeld(t) {
		t.Fatalf("while parked: %d completed calls, lease held = %v, want none and released", n, r.leaseHeld(t))
	}

	r.stop()
	r.start(t, r.p)
	r.answer(t, card, eventlog.ElicitationAnswer{Action: eventlog.ActionAccept, Content: json.RawMessage(`{"confirm":true}`)})
	waitStatus(t, r.pool, r.sid, eventlog.StatusAwaitingUser, 2)

	if name, args := r.srv.LastCall(); name != "ask" || string(args) != `{"q":"plans"}` {
		t.Fatalf("resumed call was %q %s, want the original args", name, args)
	}
	state, responses := r.srv.LastRetry()
	want := map[string]any{"confirm": map[string]any{"action": "accept", "content": map[string]any{"confirm": true}}}
	if state != "state-1" || !reflect.DeepEqual(decode(t, responses), want) {
		t.Fatalf("server got state %q responses %s, want state-1 and %v", state, responses, want)
	}
	evs = loadEvents(t, r.pool, r.sid)
	if got := callIDs(evs, eventlog.TypeToolStarted); !slices.Equal(got, []string{"c1", "c1"}) {
		t.Fatalf("started %v, want c1 twice", got)
	}
	started := ofType(evs, eventlog.TypeToolStarted)
	if started[0].Payload["resumed"] != nil || started[1].Payload["resumed"] != true {
		t.Fatalf("resumed flags = %v, %v, want unset then true", started[0].Payload["resumed"], started[1].Payload["resumed"])
	}
	if got := callIDs(evs, eventlog.TypeToolCompleted); !slices.Equal(got, []string{"c1"}) {
		t.Fatalf("completed %v, want c1 once", got)
	}
	if got := r.modelSaw(); got != "done" {
		t.Fatalf("model saw %q, want done", got)
	}
}

func TestDeclineAndCancelReachTheServerUnchanged(t *testing.T) {
	for _, action := range []string{eventlog.ActionDecline, eventlog.ActionCancel} {
		t.Run(action, func(t *testing.T) {
			r := newElicitRun(t, mcptest.Options{}, "ask", func(s *mcptest.Server) { s.AddInputRequiredTool("ask") })
			r.begin(t)
			r.answer(t, r.awaitElicitation(t, 1), eventlog.ElicitationAnswer{Action: action})
			waitStatus(t, r.pool, r.sid, eventlog.StatusAwaitingUser, 2)

			_, responses := r.srv.LastRetry()
			if want := map[string]any{"confirm": map[string]any{"action": action}}; !reflect.DeepEqual(decode(t, responses), want) {
				t.Fatalf("server got %s, want %v", responses, want)
			}
		})
	}
}

// asker is a parallel-safe built-in that asks for input until it is resumed.
type asker struct {
	resumed atomic.Pointer[tool.Resume]
	calls   atomic.Int32
	keys    chan string
}

func (*asker) Def() tool.Def { return tool.Def{Name: "tool-b", ParallelSafe: true} }

func (a *asker) Call(_ context.Context, in tool.CallInput) (tool.Result, error) {
	a.calls.Add(1)
	a.keys <- in.IdempotencyKey
	if in.Resume == nil {
		return tool.Result{InputRequired: &tool.InputRequired{
			Requests:     map[string]tool.InputRequest{"k": {Mode: "form", Message: "Sure?"}},
			RequestState: "s",
		}}, nil
	}
	a.resumed.Store(in.Resume)
	return tool.TextResult("resumed", false), nil
}

func TestSiblingsFinishBeforeTheParkAndOnlyTheAskerRunsAgain(t *testing.T) {
	var sleeps atomic.Int32
	sleeper := fn{id: "a", def: tool.Def{ParallelSafe: true}, call: func(ctx context.Context) tool.Result {
		sleeps.Add(1)
		select {
		case <-time.After(300 * time.Millisecond):
		case <-ctx.Done():
		}
		return tool.TextResult("slept", false)
	}}
	ask := &asker{keys: make(chan string, 2)}
	p := &replies{list: []provider.Response{callsOf("tool-b", "tool-a"), done()}}
	e := newConnectorEnv(t, p, sleeper, ask)
	user := testdb.NewUser(t, e.pool)
	r := &elicitRun{connectorEnv: e, p: p, user: user}
	r.sid = e.newSession(t, user.Scope())

	card := r.awaitElicitation(t, 1)
	waitStatus(t, e.pool, r.sid, eventlog.StatusAwaitingUser, 1)
	evs := loadEvents(t, e.pool, r.sid)
	if got := callIDs(evs, eventlog.TypeToolCompleted); !slices.Equal(got, []string{"c2"}) {
		t.Fatalf("completed before the park: %v, want only the sleeping sibling c2", got)
	}
	if i, j := slices.IndexFunc(evs, func(e loggedEvent) bool { return e.Type == eventlog.TypeToolCompleted }), slices.IndexFunc(evs, func(e loggedEvent) bool { return e.Type == eventlog.TypeElicitationRequested }); i > j {
		t.Fatalf("elicitation.requested (event %d) came before the sibling's tool.call.completed (event %d)", j, i)
	}

	r.answer(t, card, eventlog.ElicitationAnswer{Action: eventlog.ActionAccept})
	waitStatus(t, e.pool, r.sid, eventlog.StatusAwaitingUser, 2)

	if sleeps.Load() != 1 || ask.calls.Load() != 2 {
		t.Fatalf("the sibling ran %d times and the asker %d, want 1 and 2", sleeps.Load(), ask.calls.Load())
	}
	if got := ask.resumed.Load(); got.RequestState != "s" || !reflect.DeepEqual(got.InputResponses, map[string]tool.InputResponse{"k": {Action: "accept"}}) {
		t.Fatalf("resume = %+v", got)
	}
	if got := callIDs(loadEvents(t, e.pool, r.sid), eventlog.TypeToolStarted); !slices.Equal(got, []string{"c1", "c2", "c1"}) {
		t.Fatalf("started %v, want c1 and c2, then c1 again for the resume", got)
	}
	if first, second := <-ask.keys, <-ask.keys; first != second {
		t.Fatalf("idempotency keys %q then %q, want the same", first, second)
	}
}

// resumeHolder asks for input, then blocks once resumed until its ctx ends.
type resumeHolder struct {
	runs    atomic.Int32
	resumed chan struct{}
}

func (*resumeHolder) Def() tool.Def { return tool.Def{Name: "tool-b"} }

func (h *resumeHolder) Call(ctx context.Context, in tool.CallInput) (tool.Result, error) {
	if in.Resume == nil {
		return tool.Result{InputRequired: &tool.InputRequired{
			Requests:     map[string]tool.InputRequest{"k": {Mode: "form", Message: "Sure?"}},
			RequestState: "s",
		}}, nil
	}
	h.runs.Add(1)
	h.resumed <- struct{}{}
	<-ctx.Done()
	return tool.TextResult("cut", true), nil
}

func TestAWorkerLostMidResumeNeverRunsTheCallAgain(t *testing.T) {
	hold := &resumeHolder{resumed: make(chan struct{}, 2)}
	p := &replies{list: []provider.Response{callOf("tool-b", `{}`), done()}}
	e := newConnectorEnv(t, p, hold)
	user := testdb.NewUser(t, e.pool)
	r := &elicitRun{connectorEnv: e, p: p, user: user}
	r.sid = e.newSession(t, user.Scope())

	r.answer(t, r.awaitElicitation(t, 1), eventlog.ElicitationAnswer{Action: eventlog.ActionAccept})
	<-hold.resumed
	e.stop()
	if _, err := e.pool.Exec(t.Context(), `UPDATE sessions SET lease_expires_at = now() - interval '1 second' WHERE id = $1`, r.sid); err != nil {
		t.Fatal(err)
	}
	e.start(t, p, hold)
	waitStatus(t, e.pool, r.sid, eventlog.StatusAwaitingUser, 2)

	interrupted := ofType(loadEvents(t, e.pool, r.sid), eventlog.TypeToolInterrupted)
	if len(interrupted) != 1 || interrupted[0].Payload["tool_call_id"] != "c1" || interrupted[0].Payload["reason"] != "worker_lost" {
		t.Fatalf("interrupted = %+v, want c1 by worker_lost", interrupted)
	}
	if n := hold.runs.Load(); n != 1 {
		t.Fatalf("the resumed call ran %d times, want 1", n)
	}
}

func TestAMessageWhileParkedClosesTheUnansweredCall(t *testing.T) {
	r := newElicitRun(t, mcptest.Options{}, "ask", func(s *mcptest.Server) { s.AddInputRequiredTool("ask") })
	r.begin(t)
	card := r.awaitElicitation(t, 1)
	waitStatus(t, r.pool, r.sid, eventlog.StatusAwaitingUser, 1)

	if _, err := eventlog.NewRepo(r.pool, "fake").PostMessage(t.Context(), r.user.Scope(), r.sid, uuid.New(), "never mind"); err != nil {
		t.Fatal(err)
	}
	waitStatus(t, r.pool, r.sid, eventlog.StatusAwaitingUser, 2)

	if got := callIDs(loadEvents(t, r.pool, r.sid), eventlog.TypeToolInterrupted); !slices.Equal(got, []string{"c1"}) {
		t.Fatalf("interrupted %v, want c1", got)
	}
	id := uuid.MustParse(card["elicitation_id"].(string))
	if err := eventlog.NewRepo(r.pool, "fake").ResolveElicitation(t.Context(), r.user.Scope(), r.sid, id, eventlog.ElicitationAnswer{Action: eventlog.ActionAccept}); err == nil {
		t.Fatal("an elicitation of an ended call was answered")
	}
}

func TestLegacyElicitationHoldsTheWorkerAndTimesOutTheCall(t *testing.T) {
	r := newElicitRun(t, mcptest.Options{Legacy: true}, "ask", func(s *mcptest.Server) { s.AddLegacyElicitTool("ask", "Name?") })
	r.configure = func(w *worker.Worker) { worker.SetLegacyHold(w, 2*time.Second) }
	r.stop()
	r.start(t, r.p)
	r.begin(t)

	card := r.awaitElicitation(t, 1)
	if card["legacy"] != true || card["request_state"] != nil || card["tool_call_id"] != "c1" {
		t.Fatalf("elicitation.requested = %v, want a legacy one for c1 with no request state", card)
	}
	var status string
	if err := r.pool.QueryRow(t.Context(), `SELECT status FROM sessions WHERE id = $1`, r.sid).Scan(&status); err != nil || status != eventlog.StatusRunning || !r.leaseHeld(t) {
		t.Fatalf("mid-hold: status %q lease held %v (err %v), want running and held", status, r.leaseHeld(t), err)
	}

	waitStatus(t, r.pool, r.sid, eventlog.StatusAwaitingUser, 2)
	evs := loadEvents(t, r.pool, r.sid)
	done := ofType(evs, eventlog.TypeToolCompleted)
	if len(done) != 1 || done[0].Payload["is_error"] != true || r.modelSaw() != "needs user input; timed out" {
		t.Fatalf("completed %v, model saw %q, want one error: needs user input; timed out", done, r.modelSaw())
	}
	assertNoStatusChangeDuringHold(t, evs)
}

// assertNoStatusChangeDuringHold checks that nothing changed the session's
// status from the legacy elicitation to the end of its call.
func assertNoStatusChangeDuringHold(t *testing.T, evs []loggedEvent) {
	t.Helper()
	asked := slices.IndexFunc(evs, func(e loggedEvent) bool { return e.Type == eventlog.TypeElicitationRequested })
	ended := slices.IndexFunc(evs, func(e loggedEvent) bool { return e.Type == eventlog.TypeToolCompleted })
	for _, e := range evs[asked:ended] {
		if e.Type == eventlog.TypeStatusChanged {
			t.Fatalf("status changed during the hold: %v", e.Payload)
		}
	}
}

func TestLegacyElicitationAnsweredInTimeCompletesTheCall(t *testing.T) {
	r := newElicitRun(t, mcptest.Options{Legacy: true}, "ask", func(s *mcptest.Server) { s.AddLegacyElicitTool("ask", "Name?") })
	r.begin(t)

	card := r.awaitElicitation(t, 1)
	answered := time.Now()
	r.answer(t, card, eventlog.ElicitationAnswer{Action: eventlog.ActionAccept, Content: json.RawMessage(`{"name":"Ada"}`)})
	waitStatus(t, r.pool, r.sid, eventlog.StatusAwaitingUser, 2)

	if got := r.modelSaw(); got != `accept {"name":"Ada"}` {
		t.Fatalf("model saw %q", got)
	}
	if took := time.Since(answered); took > 5*time.Second {
		t.Fatalf("the answer took %s to reach the held call, want a jf_elicit wake-up, not the 10 s poll", took)
	}
	assertNoStatusChangeDuringHold(t, loadEvents(t, r.pool, r.sid))
}

// deltaLog records the deltas a Worker publishes.
type deltaLog struct {
	mu  sync.Mutex
	got []stream.Delta
}

func (d *deltaLog) Publish(_ context.Context, delta stream.Delta) error {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.got = append(d.got, delta)
	return nil
}

func (d *deltaLog) progress() []string {
	d.mu.Lock()
	defer d.mu.Unlock()
	var out []string
	for _, x := range d.got {
		if x.Kind == stream.KindProgress && x.CallID == "c1" {
			out = append(out, x.Text)
		}
	}
	slices.Sort(out)
	return out
}

// tenOfTen is the sorted progress text of ten steps.
func tenOfTen() []string {
	return []string{"1 of 10", "10 of 10", "2 of 10", "3 of 10", "4 of 10", "5 of 10", "6 of 10", "7 of 10", "8 of 10", "9 of 10"}
}

func TestProgressIsPublishedAsDeltasAndNeverStored(t *testing.T) {
	r := newElicitRun(t, mcptest.Options{}, "work", func(s *mcptest.Server) { s.AddProgressTool("work", 10, 0) })
	log := &deltaLog{}
	r.configure = func(w *worker.Worker) { worker.SetDeltas(w, log) }
	r.stop()
	r.start(t, r.p)
	r.begin(t)
	waitStatus(t, r.pool, r.sid, eventlog.StatusAwaitingUser, 2)

	if got := log.progress(); !slices.Equal(got, tenOfTen()) {
		t.Fatalf("progress deltas = %v, want 1 of 10 to 10 of 10", got)
	}
	var between int64
	err := r.pool.QueryRow(t.Context(), `
		SELECT (SELECT seq FROM events WHERE session_id = $1 AND type = 'tool.call.completed')
		     - (SELECT seq FROM events WHERE session_id = $1 AND type = 'tool.call.started')`, r.sid).Scan(&between)
	if err != nil || between != 1 {
		t.Fatalf("events from tool.call.started to tool.call.completed differ by %d (err %v), want 1: progress stores nothing", between, err)
	}
}

// reporter is a built-in that reports ten steps, then blocks until its call ends.
type reporter struct{}

func (reporter) Def() tool.Def { return tool.Def{Name: "tool-a", Timeout: 300 * time.Millisecond} }

func (reporter) Call(ctx context.Context, in tool.CallInput) (tool.Result, error) {
	for i := 1; i <= 10; i++ {
		in.Progress("", float64(i), 10)
	}
	<-ctx.Done()
	return tool.Result{}, ctx.Err()
}

func TestProgressDoesNotExtendTheTimeout(t *testing.T) {
	p := &replies{list: []provider.Response{callOf("tool-a", `{}`), done()}}
	e := newConnectorEnv(t, p, reporter{})
	log := &deltaLog{}
	e.configure = func(w *worker.Worker) { worker.SetDeltas(w, log) }
	e.stop()
	e.start(t, p, reporter{})
	sid, _ := newFakeSession(t, e.pool)
	waitStatus(t, e.pool, sid, eventlog.StatusAwaitingUser, 2)

	if got := log.progress(); !slices.Equal(got, tenOfTen()) {
		t.Fatalf("progress deltas = %v, want 1 of 10 to 10 of 10", got)
	}
	done := ofType(loadEvents(t, e.pool, sid), eventlog.TypeToolCompleted)
	took, _ := done[0].Payload["duration_ms"].(float64)
	if got := p.requests()[1].Messages[2].Parts[0].ToolResult.Parts[0].Text; got != "timed out after 300ms; the call may have partially run" || took < 300 || took > 3000 {
		t.Fatalf("model saw %q after %vms, want the timeout on schedule", got, took)
	}
}
