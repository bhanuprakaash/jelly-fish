package worker_test

import (
	"context"
	"encoding/json"
	"log/slog"
	"slices"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
	"go.opentelemetry.io/otel/trace"

	"github.com/bhanuprakaash/jelly-fish/internal/eventlog"
	"github.com/bhanuprakaash/jelly-fish/internal/msg"
	"github.com/bhanuprakaash/jelly-fish/internal/provider"
	"github.com/bhanuprakaash/jelly-fish/internal/provider/fake"
	"github.com/bhanuprakaash/jelly-fish/internal/stream"
	"github.com/bhanuprakaash/jelly-fish/internal/testdb"
	"github.com/bhanuprakaash/jelly-fish/internal/tool"
	"github.com/bhanuprakaash/jelly-fish/internal/worker"
)

// scripted is a Provider named like the fake, so Gateway.Fake routes to it.
// Call i fails with results[i] (the last entry repeats); a nil entry
// succeeds, and block waits for ctx instead.
type scripted struct {
	mu      sync.Mutex
	results []error
	block   bool
	calls   int
	started chan struct{}
}

func (*scripted) Name() string { return fake.Name }

func (s *scripted) Stream(ctx context.Context, _ provider.Request, onDelta func(provider.Delta)) (provider.Response, error) {
	s.mu.Lock()
	err := s.results[min(s.calls, len(s.results)-1)]
	s.calls++
	s.mu.Unlock()
	if s.block {
		select {
		case s.started <- struct{}{}:
		default:
		}
		<-ctx.Done()
		return provider.Response{}, ctx.Err()
	}
	onDelta(provider.Delta{Kind: provider.DeltaText, Text: "partial"})
	if err != nil {
		return provider.Response{}, err
	}
	return provider.Response{Message: msg.AssistantText("ok"), StopReason: provider.StopReasonEndTurn, Usage: provider.Usage{Input: 3, Output: 4}}, nil
}

func (s *scripted) callCount() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.calls
}

func providerErr(kind provider.ErrorKind, input int64) error {
	return &provider.Error{Kind: kind, RequestID: "req_1", Usage: provider.Usage{Input: input}}
}

func startScripted(t *testing.T, pool *pgxpool.Pool, p provider.Provider, heartbeat time.Duration) (stop func()) {
	t.Helper()
	return startTools(t, pool, p, heartbeat, nil)
}

func startTools(t *testing.T, pool *pgxpool.Pool, p provider.Provider, heartbeat time.Duration, tools *tool.Registry) (stop func()) {
	t.Helper()
	return startTraced(t, pool, p, heartbeat, tools, nil)
}

func startTraced(t *testing.T, pool *pgxpool.Pool, p provider.Provider, heartbeat time.Duration, tools *tool.Registry, tp trace.TracerProvider) (stop func()) {
	t.Helper()
	gw := worker.Gateway{Fake: p, Tracer: tp, Sleep: func(context.Context, time.Duration) error { return nil }}
	ctx, cancel := context.WithCancel(t.Context())
	w := worker.New(pool, gw, tools, stream.NewPGDeltaBus(pool), worker.Lease{TTL: 30 * time.Second, Heartbeat: heartbeat}, eventlog.Upcasters{}, slog.New(slog.DiscardHandler))
	worker.SetTitleTimeout(w, 0)
	done := make(chan struct{})
	go func() { w.Run(ctx); close(done) }()
	var once sync.Once
	stop = func() { once.Do(func() { cancel(); <-done }) }
	t.Cleanup(stop)
	return stop
}

func newFakeSession(t *testing.T, pool *pgxpool.Pool) (uuid.UUID, eventlog.TenantScope) {
	t.Helper()
	scope := testdb.NewUser(t, pool).Scope()
	sid := uuid.New()
	if _, err := eventlog.NewRepo(pool, fake.Name).CreateSession(t.Context(), scope, sid, uuid.New(), "hi", "", false); err != nil {
		t.Fatal(err)
	}
	return sid, scope
}

// waitSleeping waits for the session to sleep with wake_at between lo and hi
// from the database's now(); an earlier, backdated sleep doesn't match.
func waitSleeping(t *testing.T, pool *pgxpool.Pool, sid uuid.UUID, lo, hi time.Duration) {
	t.Helper()
	deadline := time.Now().Add(15 * time.Second)
	for time.Now().Before(deadline) {
		var ok bool
		err := pool.QueryRow(t.Context(), `
			SELECT status = 'sleeping' AND wake_at BETWEEN now() + $2::interval AND now() + $3::interval
			FROM sessions WHERE id = $1`, sid, lo, hi).Scan(&ok)
		if err != nil {
			t.Fatal(err)
		}
		if ok {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("session never slept for %v to %v", lo, hi)
}

func wakeNow(t *testing.T, pool *pgxpool.Pool, sid uuid.UUID) {
	t.Helper()
	if _, err := pool.Exec(t.Context(), `UPDATE sessions SET wake_at = now() - interval '1 second' WHERE id = $1`, sid); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(t.Context(), `SELECT pg_notify('jf_runnable', $1)`, sid.String()); err != nil {
		t.Fatal(err)
	}
}

type loggedEvent struct {
	Type    string
	Payload map[string]any
}

func loadEvents(t *testing.T, pool *pgxpool.Pool, sid uuid.UUID) []loggedEvent {
	t.Helper()
	evs, err := eventlog.NewStore(pool).Load(t.Context(), sid)
	if err != nil {
		t.Fatal(err)
	}
	out := make([]loggedEvent, len(evs))
	for i, e := range evs {
		out[i].Type = e.Type
		if err := json.Unmarshal(e.Payload, &out[i].Payload); err != nil {
			t.Fatal(err)
		}
	}
	return out
}

func ofType(evs []loggedEvent, typ string) []loggedEvent {
	var out []loggedEvent
	for _, e := range evs {
		if e.Type == typ {
			out = append(out, e)
		}
	}
	return out
}

func TestProviderDownSleepsOnTheBackoffLadderThenFails(t *testing.T) {
	pool := testdb.NewPool(t)
	sid, _ := newFakeSession(t, pool)
	p := &scripted{results: []error{providerErr(provider.KindProviderDown, 7)}}
	stop := startScripted(t, pool, p, 10*time.Second)

	waitSleeping(t, pool, sid, 55*time.Second, 65*time.Second)
	if n := p.callCount(); n != 5 {
		t.Fatalf("provider calls = %d, want 1 + 4 quick retries", n)
	}
	assertFoldMatchesRow(t, pool, sid)

	// The failed attempts' usage is recorded once, summed.
	var rows int
	var qty int64
	if err := pool.QueryRow(t.Context(), `SELECT count(*), coalesce(sum(quantity), 0)::bigint FROM usage WHERE session_id = $1 AND unit = 'input_tokens'`, sid).Scan(&rows, &qty); err != nil {
		t.Fatal(err)
	}
	if rows != 1 || qty != 35 {
		t.Fatalf("input usage rows = %d, quantity = %d, want 1 and 35 (5 attempts of 7)", rows, qty)
	}

	// A different Worker takes over each wake: the step comes from the log.
	stop()
	wakeNow(t, pool, sid)
	startScripted(t, pool, p, 10*time.Second)
	waitSleeping(t, pool, sid, 4*time.Minute, 5*time.Minute+5*time.Second)

	wakeNow(t, pool, sid)
	waitSleeping(t, pool, sid, 14*time.Minute, 15*time.Minute+5*time.Second)

	wakeNow(t, pool, sid)
	waitStatus(t, pool, sid, eventlog.StatusFailed, 0)
	assertFoldMatchesRow(t, pool, sid)

	evs := loadEvents(t, pool, sid)
	var retryable []bool
	for _, e := range ofType(evs, eventlog.TypeSessionError) {
		retryable = append(retryable, e.Payload["retryable"].(bool))
		if e.Payload["code"] != "provider_down" || e.Payload["request_id"] != "req_1" || e.Payload["message"] == "" {
			t.Fatalf("session.error = %v", e.Payload)
		}
	}
	if !slices.Equal(retryable, []bool{true, true, true, false}) {
		t.Fatalf("session.error retryable = %v, want three retryable then a final one", retryable)
	}
	if n := len(ofType(evs, eventlog.TypeTimerSet)); n != 3 {
		t.Fatalf("timer.set events = %d, want 3", n)
	}
	if n := len(ofType(evs, eventlog.TypeTimerFired)); n != 3 {
		t.Fatalf("timer.fired events = %d, want 3", n)
	}
	if n := len(ofType(evs, eventlog.TypeTurnInterrupted)); n != 0 {
		t.Fatalf("turn.interrupted events = %d: a retry must start a fresh turn, not recover a lost one", n)
	}
	if n := p.callCount(); n != 20 {
		t.Fatalf("provider calls = %d, want 4 rounds of 5", n)
	}
}

func TestSleepingSessionRecoversWhenTheProviderDoes(t *testing.T) {
	pool := testdb.NewPool(t)
	sid, _ := newFakeSession(t, pool)
	failing := providerErr(provider.KindRateLimited, 0)
	p := &scripted{results: []error{failing, failing, failing, failing, failing, nil}}
	startScripted(t, pool, p, 10*time.Second)

	waitSleeping(t, pool, sid, 55*time.Second, 65*time.Second)
	wakeNow(t, pool, sid)
	waitStatus(t, pool, sid, eventlog.StatusAwaitingUser, 1)
	assertFoldMatchesRow(t, pool, sid)
}

func TestStopClassErrorsParkAwaitingUser(t *testing.T) {
	for _, kind := range []provider.ErrorKind{
		provider.KindKeyInvalid, provider.KindBilling, provider.KindModelUnavailable, provider.KindTooLarge, provider.KindBug,
	} {
		t.Run(string(kind), func(t *testing.T) {
			pool := testdb.NewPool(t)
			sid, _ := newFakeSession(t, pool)
			p := &scripted{results: []error{providerErr(kind, 5)}}
			startScripted(t, pool, p, 10*time.Second)

			waitStatus(t, pool, sid, eventlog.StatusAwaitingUser, 0)
			assertFoldMatchesRow(t, pool, sid)
			if n := p.callCount(); n != 1 {
				t.Fatalf("provider calls = %d, want 1: stop classes are not retried", n)
			}
			evs := loadEvents(t, pool, sid)
			errs := ofType(evs, eventlog.TypeSessionError)
			if len(errs) != 1 || errs[0].Payload["code"] != string(kind) || errs[0].Payload["retryable"] != false || errs[0].Payload["request_id"] != "req_1" {
				t.Fatalf("session.error = %v", errs)
			}
			last := evs[len(evs)-1]
			if last.Type != eventlog.TypeStatusChanged || last.Payload["to"] != "awaiting_user" || last.Payload["reason"] != "error" {
				t.Fatalf("last event = %+v", last)
			}
			var qty int64
			if err := pool.QueryRow(t.Context(), `SELECT coalesce(sum(quantity), 0)::bigint FROM usage WHERE session_id = $1`, sid).Scan(&qty); err != nil {
				t.Fatal(err)
			}
			if qty != 5 {
				t.Fatalf("usage recorded = %d, want the failed attempt's 5", qty)
			}
		})
	}
}

func TestLongWaitSleepsUntilTheProviderIsReady(t *testing.T) {
	pool := testdb.NewPool(t)
	sid, _ := newFakeSession(t, pool)
	p := &scripted{results: []error{&provider.Error{Kind: provider.KindLongWait, RetryAfter: 90 * time.Second, RequestID: "req_1"}, nil}}
	startScripted(t, pool, p, 10*time.Second)

	waitSleeping(t, pool, sid, 85*time.Second, 95*time.Second)
	if n := p.callCount(); n != 1 {
		t.Fatalf("provider calls = %d, want 1: a long wait is not retried in-process", n)
	}
	if n := len(ofType(loadEvents(t, pool, sid), eventlog.TypeSessionError)); n != 0 {
		t.Fatalf("session.error events = %d, want none for a long wait", n)
	}

	wakeNow(t, pool, sid)
	waitStatus(t, pool, sid, eventlog.StatusAwaitingUser, 1)
	assertFoldMatchesRow(t, pool, sid)
	for _, e := range ofType(loadEvents(t, pool, sid), eventlog.TypeTurnInterrupted) {
		if e.Payload["reason"] == "worker_lost" {
			t.Fatal("the paused turn was recovered as worker_lost")
		}
	}
}

func TestInterruptKeepsASleepingSessionFromRetrying(t *testing.T) {
	pool := testdb.NewPool(t)
	sid, scope := newFakeSession(t, pool)
	p := &scripted{results: []error{providerErr(provider.KindProviderDown, 0)}}
	startScripted(t, pool, p, 10*time.Second)
	waitSleeping(t, pool, sid, 55*time.Second, 65*time.Second)

	if err := eventlog.NewRepo(pool, fake.Name).Interrupt(t.Context(), scope, sid); err != nil {
		t.Fatal(err)
	}
	wakeNow(t, pool, sid)
	time.Sleep(500 * time.Millisecond)
	waitStatus(t, pool, sid, eventlog.StatusAwaitingUser, 0)
	if n := p.callCount(); n != 5 {
		t.Fatalf("provider calls = %d, want no further retry after Stop retrying", n)
	}
	assertFoldMatchesRow(t, pool, sid)
}

func blockingProvider() *scripted {
	return &scripted{results: []error{nil}, block: true, started: make(chan struct{}, 1)}
}

func waitProviderCalled(t *testing.T, p *scripted) {
	t.Helper()
	select {
	case <-p.started:
	case <-time.After(15 * time.Second):
		t.Fatal("provider never called")
	}
}

func assertStoppedByUser(t *testing.T, pool *pgxpool.Pool, sid uuid.UUID, p *scripted) {
	t.Helper()
	assertFoldMatchesRow(t, pool, sid)
	evs := loadEvents(t, pool, sid)
	ti := ofType(evs, eventlog.TypeTurnInterrupted)
	if len(ti) != 1 || ti[0].Payload["reason"] != "user_interrupt" {
		t.Fatalf("turn.interrupted = %v, want one user_interrupt", ti)
	}
	last := evs[len(evs)-1]
	if last.Payload["to"] != "awaiting_user" || last.Payload["reason"] != "interrupted" {
		t.Fatalf("last event = %+v", last)
	}
	var cancel bool
	if err := pool.QueryRow(t.Context(), `SELECT cancel_requested FROM sessions WHERE id = $1`, sid).Scan(&cancel); err != nil || cancel {
		t.Fatalf("cancel_requested = %v, err = %v, want cleared", cancel, err)
	}
	if n := p.callCount(); n != 1 {
		t.Fatalf("provider calls = %d", n)
	}
}

func TestInterruptStopsARunningTurnWithinASecond(t *testing.T) {
	pool := testdb.NewPool(t)
	sid, scope := newFakeSession(t, pool)
	p := blockingProvider()
	startScripted(t, pool, p, 30*time.Second)
	waitProviderCalled(t, p)

	began := time.Now()
	if err := eventlog.NewRepo(pool, fake.Name).Interrupt(t.Context(), scope, sid); err != nil {
		t.Fatal(err)
	}
	waitStatus(t, pool, sid, eventlog.StatusAwaitingUser, 0)
	if took := time.Since(began); took > time.Second {
		t.Fatalf("stopped after %v, want under 1s", took)
	}
	assertStoppedByUser(t, pool, sid, p)
}

func TestDisablingTheUserStopsItsRunningTurn(t *testing.T) {
	pool := testdb.NewPool(t)
	sid, scope := newFakeSession(t, pool)
	p := blockingProvider()
	startScripted(t, pool, p, 30*time.Second)
	waitProviderCalled(t, p)

	if err := eventlog.NewStore(pool).InterruptUserSessions(t.Context(), scope.UserID, "user:admin"); err != nil {
		t.Fatal(err)
	}
	waitStatus(t, pool, sid, eventlog.StatusAwaitingUser, 0)

	evs := loadEvents(t, pool, sid)
	ui := ofType(evs, eventlog.TypeUserInterrupt)
	if len(ui) != 1 || ui[0].Payload["reason"] != "user_disabled" {
		t.Fatalf("user.interrupt = %v, want one user_disabled", ui)
	}
	ti := ofType(evs, eventlog.TypeTurnInterrupted)
	if len(ti) != 1 || ti[0].Payload["reason"] != "user_interrupt" {
		t.Fatalf("turn.interrupted = %v, want one user_interrupt", ti)
	}
}

// Flagging the row without a notify is what a lost notify looks like.
func TestInterruptReachesARunningTurnThroughTheHeartbeat(t *testing.T) {
	pool := testdb.NewPool(t)
	sid, _ := newFakeSession(t, pool)
	p := blockingProvider()
	startScripted(t, pool, p, time.Second)
	waitProviderCalled(t, p)

	began := time.Now()
	if _, err := pool.Exec(t.Context(), `UPDATE sessions SET cancel_requested = true WHERE id = $1`, sid); err != nil {
		t.Fatal(err)
	}
	waitStatus(t, pool, sid, eventlog.StatusAwaitingUser, 0)
	if took := time.Since(began); took > 10*time.Second {
		t.Fatalf("stopped after %v, want within 10s", took)
	}
	assertStoppedByUser(t, pool, sid, p)
}

func TestCancelNotifyForAnotherSessionIsIgnored(t *testing.T) {
	pool := testdb.NewPool(t)
	sid, scope := newFakeSession(t, pool)
	p := blockingProvider()
	startScripted(t, pool, p, 30*time.Second)
	waitProviderCalled(t, p)

	for _, payload := range []string{uuid.NewString(), "not a session id"} {
		if _, err := pool.Exec(t.Context(), `SELECT pg_notify('jf_cancel', $1)`, payload); err != nil {
			t.Fatal(err)
		}
	}
	time.Sleep(300 * time.Millisecond)
	if n := len(ofType(loadEvents(t, pool, sid), eventlog.TypeTurnInterrupted)); n != 0 {
		t.Fatalf("turn.interrupted events = %d, want none", n)
	}
	var status string
	var cancel bool
	if err := pool.QueryRow(t.Context(), `SELECT status, cancel_requested FROM sessions WHERE id = $1`, sid).Scan(&status, &cancel); err != nil {
		t.Fatal(err)
	}
	if status != eventlog.StatusRunning || cancel {
		t.Fatalf("status = %s, cancel_requested = %v, want running without cancel", status, cancel)
	}

	if err := eventlog.NewRepo(pool, fake.Name).Interrupt(t.Context(), scope, sid); err != nil {
		t.Fatal(err)
	}
	waitStatus(t, pool, sid, eventlog.StatusAwaitingUser, 0)
}

func TestFailedAttemptsResetTheTurnsPartialText(t *testing.T) {
	pool := testdb.NewPool(t)
	sid, _ := newFakeSession(t, pool)

	listener, err := pool.Acquire(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Release()
	if _, err := listener.Exec(t.Context(), "LISTEN jf_stream"); err != nil {
		t.Fatal(err)
	}

	p := &scripted{results: []error{providerErr(provider.KindProviderDown, 0)}}
	startScripted(t, pool, p, 10*time.Second)
	waitSleeping(t, pool, sid, 55*time.Second, 65*time.Second)

	var resets int
	for {
		ctx, cancel := context.WithTimeout(t.Context(), 300*time.Millisecond)
		n, err := listener.Conn().WaitForNotification(ctx)
		cancel()
		if err != nil {
			break
		}
		var d stream.Delta
		if err := json.Unmarshal([]byte(n.Payload), &d); err != nil {
			t.Fatal(err)
		}
		if d.Kind == stream.KindReset && d.TurnID != "" {
			resets++
		}
	}
	if resets != 5 {
		t.Fatalf("reset deltas = %d, want one per retry (4) and one when the turn gave up", resets)
	}
}

// steered is scripted, plus a user.message posted during its first call.
type steered struct {
	*scripted
	post func()
	once sync.Once
}

func (s *steered) Stream(ctx context.Context, req provider.Request, onDelta func(provider.Delta)) (provider.Response, error) {
	s.once.Do(s.post)
	return s.scripted.Stream(ctx, req, onDelta)
}

func TestFailedTurnKeepsItsUsageWhenAMessageArrivesMeanwhile(t *testing.T) {
	pool := testdb.NewPool(t)
	sid, scope := newFakeSession(t, pool)
	down := providerErr(provider.KindProviderDown, 7)
	p := &steered{
		scripted: &scripted{results: []error{down, down, down, down, down, nil}},
		post: func() {
			if _, err := eventlog.NewRepo(pool, fake.Name).PostMessage(t.Context(), scope, sid, uuid.New(), "hello?"); err != nil {
				t.Error(err)
			}
		},
	}
	startScripted(t, pool, p, 10*time.Second)
	waitStatus(t, pool, sid, eventlog.StatusAwaitingUser, 1)

	var usage int64
	var reasons []string
	for _, e := range loadEvents(t, pool, sid) {
		switch e.Type {
		case eventlog.TypeUsageRecorded:
			if e.Payload["unit"] == "input_tokens" && usage == 0 {
				usage = int64(e.Payload["quantity"].(float64))
			}
		case eventlog.TypeTurnInterrupted:
			reasons = append(reasons, e.Payload["reason"].(string))
		case eventlog.TypeSessionError:
			t.Fatalf("session.error written for a stale park: %v", e.Payload)
		}
	}
	if usage != 5*7 {
		t.Fatalf("failed attempts' input usage = %d, want %d", usage, 5*7)
	}
	if !slices.Equal(reasons, []string{"provider_error"}) {
		t.Fatalf("turn.interrupted reasons = %v, want [provider_error]", reasons)
	}
	assertFoldMatchesRow(t, pool, sid)
}
