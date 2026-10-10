package worker_test

import (
	"context"
	"encoding/json"
	"fmt"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/google/uuid"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"
	"go.uber.org/goleak"

	"github.com/bhanuprakaash/jelly-fish/internal/eventlog"
	"github.com/bhanuprakaash/jelly-fish/internal/msg"
	"github.com/bhanuprakaash/jelly-fish/internal/provider"
	"github.com/bhanuprakaash/jelly-fish/internal/provider/fake"
	"github.com/bhanuprakaash/jelly-fish/internal/testdb"
	"github.com/bhanuprakaash/jelly-fish/internal/tool"
)

// replies is a Provider named like the fake that answers each call from a
// script, the last entry repeating, and records the requests it got.
type replies struct {
	mu   sync.Mutex
	list []provider.Response
	reqs []provider.Request
}

func (*replies) Name() string { return fake.Name }

func (r *replies) Stream(_ context.Context, req provider.Request, _ func(provider.Delta)) (provider.Response, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	resp := r.list[min(len(r.reqs), len(r.list)-1)]
	r.reqs = append(r.reqs, req)
	return resp, nil
}

func (r *replies) requests() []provider.Request {
	r.mu.Lock()
	defer r.mu.Unlock()
	return slices.Clone(r.reqs)
}

func toolUse(ids ...string) provider.Response {
	m := msg.AssistantText("on it")
	for _, id := range ids {
		m.Parts = append(m.Parts, msg.Part{Kind: msg.KindToolUse, ToolUse: &msg.ToolUse{ID: id, Name: "tool-" + id, Args: json.RawMessage(`{}`)}})
	}
	return provider.Response{Message: m, StopReason: provider.StopReasonToolUse}
}

func done() provider.Response {
	return provider.Response{Message: msg.AssistantText("all done"), StopReason: provider.StopReasonEndTurn}
}

// fn is a Tool named "tool-<id>" backed by a function.
type fn struct {
	id   string
	def  tool.Def
	call func(ctx context.Context) tool.Result
}

func (f fn) Def() tool.Def {
	f.def.Name = "tool-" + f.id
	return f.def
}

func (f fn) Call(ctx context.Context, _ tool.CallInput) (tool.Result, error) { return f.call(ctx), nil }

func toolTypes(evs []loggedEvent) []string {
	var out []string
	for _, e := range evs {
		switch e.Type {
		case eventlog.TypeToolRequested, eventlog.TypeToolStarted, eventlog.TypeToolCompleted, eventlog.TypeToolInterrupted:
			out = append(out, e.Type)
		}
	}
	return out
}

func callIDs(evs []loggedEvent, typ string) []string {
	var out []string
	for _, e := range ofType(evs, typ) {
		out = append(out, e.Payload["tool_call_id"].(string))
	}
	return out
}

func TestParallelSafeCallsRunTogetherThenTheRestAlone(t *testing.T) {
	pool := testdb.NewPool(t)
	sid, _ := newFakeSession(t, pool)

	var running, finished atomic.Int32
	bothRunning := make(chan struct{})
	parallel := func(id string) fn {
		return fn{id: id, def: tool.Def{ParallelSafe: true}, call: func(ctx context.Context) tool.Result {
			if running.Add(1) == 2 {
				close(bothRunning)
			}
			select {
			case <-bothRunning:
			case <-ctx.Done():
				return tool.TextResult("never ran together", true)
			}
			finished.Add(1)
			return tool.TextResult("result "+id, false)
		}}
	}
	var sawBothFinished atomic.Bool
	c := fn{id: "c", call: func(context.Context) tool.Result {
		sawBothFinished.Store(finished.Load() == 2)
		return tool.TextResult("result c", false)
	}}
	p := &replies{list: []provider.Response{toolUse("a", "b", "c"), done()}}

	stop := startTools(t, pool, p, 10*time.Second, tool.NewRegistry(parallel("a"), parallel("b"), c))
	waitStatus(t, pool, sid, eventlog.StatusAwaitingUser, 2)
	stop()
	if err := goleak.Find(goleak.IgnoreCurrent(), goleak.IgnoreAnyFunction("github.com/jackc/pgx/v5/pgxpool.(*Pool).backgroundHealthCheck")); err != nil {
		t.Errorf("goroutines left behind: %v", err)
	}

	evs := loadEvents(t, pool, sid)
	want := []string{
		eventlog.TypeToolRequested, eventlog.TypeToolRequested, eventlog.TypeToolRequested,
		eventlog.TypeToolStarted, eventlog.TypeToolStarted,
		eventlog.TypeToolCompleted, eventlog.TypeToolCompleted,
		eventlog.TypeToolStarted, eventlog.TypeToolCompleted,
	}
	if got := toolTypes(evs); !slices.Equal(got, want) {
		t.Fatalf("tool events = %v, want %v", got, want)
	}
	if got := callIDs(evs, eventlog.TypeToolRequested); !slices.Equal(got, []string{"a", "b", "c"}) {
		t.Fatalf("requested = %v, want the model's order", got)
	}
	if !sawBothFinished.Load() {
		t.Fatal("c started before a and b were done")
	}

	reqs := p.requests()
	if len(reqs) != 2 {
		t.Fatalf("StartTurn calls = %d, want 2", len(reqs))
	}
	if n := len(reqs[0].Tools); n != 3 {
		t.Fatalf("first request offered %d tools, want 3", n)
	}
	results := reqs[1].Messages[len(reqs[1].Messages)-1]
	var order []string
	for _, part := range results.Parts {
		order = append(order, part.ToolResult.CallID)
	}
	if !slices.Equal(order, []string{"a", "b", "c"}) {
		t.Fatalf("results sent in order %v, want a b c", order)
	}
	assertFoldMatchesRow(t, pool, sid)
}

func TestRequestedCallsLandInOneTransaction(t *testing.T) {
	pool := testdb.NewPool(t)
	sid, _ := newFakeSession(t, pool)
	quick := func(id string) fn {
		return fn{id: id, call: func(context.Context) tool.Result { return tool.TextResult("ok", false) }}
	}
	p := &replies{list: []provider.Response{toolUse("a", "b", "c"), done()}}
	startTools(t, pool, p, 10*time.Second, tool.NewRegistry(quick("a"), quick("b"), quick("c")))
	waitStatus(t, pool, sid, eventlog.StatusAwaitingUser, 2)

	var seqs []int64
	rows, err := pool.Query(t.Context(), `SELECT seq FROM events WHERE session_id = $1 AND type = $2 ORDER BY seq`, sid, eventlog.TypeToolRequested)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	for rows.Next() {
		var s int64
		if err := rows.Scan(&s); err != nil {
			t.Fatal(err)
		}
		seqs = append(seqs, s)
	}
	if len(seqs) != 3 || seqs[2]-seqs[0] != 2 {
		t.Fatalf("requested seqs = %v, want three in a row", seqs)
	}
}

func TestCallPastItsTimeoutIsAnErrorResultAndNotRetried(t *testing.T) {
	pool := testdb.NewPool(t)
	sid, _ := newFakeSession(t, pool)
	var calls atomic.Int32
	slow := fn{id: "a", def: tool.Def{Timeout: 50 * time.Millisecond}, call: func(ctx context.Context) tool.Result {
		calls.Add(1)
		<-ctx.Done()
		return tool.Result{}
	}}
	p := &replies{list: []provider.Response{toolUse("a"), done()}}
	startTools(t, pool, p, 10*time.Second, tool.NewRegistry(slow))
	waitStatus(t, pool, sid, eventlog.StatusAwaitingUser, 2)

	completed := ofType(loadEvents(t, pool, sid), eventlog.TypeToolCompleted)
	if len(completed) != 1 || completed[0].Payload["is_error"] != true {
		t.Fatalf("completed = %+v, want one error", completed)
	}
	text := p.requests()[1].Messages[2].Parts[0].ToolResult.Parts[0].Text
	if text != "timed out after 50ms; the call may have partially run" {
		t.Fatalf("model saw %q", text)
	}
	if calls.Load() != 1 {
		t.Fatalf("tool ran %d times, want 1", calls.Load())
	}
}

func TestUnknownToolIsAnErrorResult(t *testing.T) {
	pool := testdb.NewPool(t)
	sid, _ := newFakeSession(t, pool)
	p := &replies{list: []provider.Response{toolUse("missing"), done()}}
	startTools(t, pool, p, 10*time.Second, tool.NewRegistry())
	waitStatus(t, pool, sid, eventlog.StatusAwaitingUser, 2)

	tr := p.requests()[1].Messages[2].Parts[0].ToolResult
	if !tr.IsError || tr.Parts[0].Text != `unknown tool "tool-missing"` {
		t.Fatalf("result = %+v", tr)
	}
}

func TestCutOffToolUseIsDroppedAndParks(t *testing.T) {
	pool := testdb.NewPool(t)
	sid, _ := newFakeSession(t, pool)
	cut := toolUse("a")
	cut.StopReason = provider.StopReasonMaxTokens
	p := &replies{list: []provider.Response{cut, done()}}
	startTools(t, pool, p, 10*time.Second, tool.NewRegistry())
	waitStatus(t, pool, sid, eventlog.StatusAwaitingUser, 1)

	evs := loadEvents(t, pool, sid)
	if got := toolTypes(evs); len(got) != 0 {
		t.Fatalf("tool events = %v, want none", got)
	}
	var reply msg.Message
	raw, err := json.Marshal(ofType(evs, eventlog.TypeLLMResponse)[0].Payload["message"])
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(raw, &reply); err != nil {
		t.Fatal(err)
	}
	if len(reply.Parts) != 1 || reply.Parts[0].Kind != msg.KindText || reply.Parts[0].Text != "on it" {
		t.Fatalf("reply parts = %+v, want the text only", reply.Parts)
	}
	if n := len(p.requests()); n != 1 {
		t.Fatalf("provider calls = %d, want 1", n)
	}
}

func TestInterruptClosesEveryCallOfTheBatch(t *testing.T) {
	pool := testdb.NewPool(t)
	sid, scope := newFakeSession(t, pool)
	started := make(chan struct{})
	parallel := tool.Def{ParallelSafe: true}
	a := fn{id: "a", def: parallel, call: func(ctx context.Context) tool.Result {
		close(started)
		<-ctx.Done()
		return tool.Result{}
	}}
	b := fn{id: "b", def: parallel, call: func(context.Context) tool.Result { return tool.TextResult("result b", false) }}
	c := fn{id: "c", call: func(context.Context) tool.Result { t.Error("c ran"); return tool.Result{} }}
	p := &replies{list: []provider.Response{toolUse("a", "b", "c"), done()}}
	startTools(t, pool, p, 10*time.Second, tool.NewRegistry(a, b, c))

	<-started
	deadline := time.Now().Add(15 * time.Second)
	for len(ofType(loadEvents(t, pool, sid), eventlog.TypeToolCompleted)) == 0 {
		if time.Now().After(deadline) {
			t.Fatal("tool b never completed")
		}
		time.Sleep(10 * time.Millisecond)
	}
	if err := eventlog.NewRepo(pool, fake.Name).Interrupt(t.Context(), scope, sid); err != nil {
		t.Fatal(err)
	}
	waitStatus(t, pool, sid, eventlog.StatusAwaitingUser, 1)

	evs := loadEvents(t, pool, sid)
	interrupted := ofType(evs, eventlog.TypeToolInterrupted)
	if len(interrupted) != 1 || interrupted[0].Payload["tool_call_id"] != "a" || interrupted[0].Payload["reason"] != "user_interrupt" {
		t.Fatalf("interrupted = %+v, want a by user_interrupt", interrupted)
	}
	completed := ofType(evs, eventlog.TypeToolCompleted)
	if len(completed) != 2 || completed[0].Payload["tool_call_id"] != "b" || completed[0].Payload["is_error"] == true {
		t.Fatalf("completed = %+v, want b done first", completed)
	}
	if completed[1].Payload["tool_call_id"] != "c" || completed[1].Payload["is_error"] != true {
		t.Fatalf("completed = %+v, want c as an error", completed)
	}
	if note := fmt.Sprint(completed[1].Payload["result"]); !strings.Contains(note, "not run: interrupted by user") {
		t.Fatalf("c result = %s, want the not-run note", note)
	}

	// The next message resumes with an answer for every call.
	if _, err := eventlog.NewRepo(pool, fake.Name).PostMessage(t.Context(), scope, sid, uuid.New(), "go on"); err != nil {
		t.Fatal(err)
	}
	waitStatus(t, pool, sid, eventlog.StatusAwaitingUser, 2)
	reqs := p.requests()
	if n := len(reqs[len(reqs)-1].Messages[2].Parts); n != 3 {
		t.Fatalf("results sent = %d, want 3", n)
	}
}

func TestEachToolCallGetsOneSpan(t *testing.T) {
	pool := testdb.NewPool(t)
	sid, _ := newFakeSession(t, pool)
	rec := tracetest.NewSpanRecorder()
	tp := sdktrace.NewTracerProvider(sdktrace.WithSpanProcessor(rec))
	t.Cleanup(func() { _ = tp.Shutdown(context.Background()) })
	quick := func(id string) fn {
		return fn{id: id, call: func(context.Context) tool.Result { return tool.TextResult("ok", false) }}
	}
	p := &replies{list: []provider.Response{toolUse("a", "b"), done()}}
	startTraced(t, pool, p, 10*time.Second, tool.NewRegistry(quick("a"), quick("b")), tp)
	waitStatus(t, pool, sid, eventlog.StatusAwaitingUser, 2)

	var got []string
	for _, s := range rec.Ended() {
		if s.Name() != "tool.call" {
			continue
		}
		attrs := map[string]string{}
		for _, kv := range s.Attributes() {
			attrs[string(kv.Key)] = kv.Value.Emit()
		}
		if attrs["jf.session_id"] != sid.String() {
			t.Errorf("span session = %q, want %s", attrs["jf.session_id"], sid)
		}
		if attrs["jf.turn_id"] == "" || attrs["jf.is_error"] != "false" {
			t.Errorf("span turn = %q, is_error = %q, want a turn and false", attrs["jf.turn_id"], attrs["jf.is_error"])
		}
		got = append(got, attrs["jf.tool"])
	}
	slices.Sort(got)
	if !slices.Equal(got, []string{"tool-a", "tool-b"}) {
		t.Fatalf("tool spans = %v, want one each for tool-a and tool-b", got)
	}
}
