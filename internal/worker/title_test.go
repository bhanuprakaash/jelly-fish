package worker_test

import (
	"context"
	"encoding/json"
	"log/slog"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/bhanuprakaash/jelly-fish/internal/eventlog"
	"github.com/bhanuprakaash/jelly-fish/internal/msg"
	"github.com/bhanuprakaash/jelly-fish/internal/provider"
	"github.com/bhanuprakaash/jelly-fish/internal/provider/fake"
	"github.com/bhanuprakaash/jelly-fish/internal/stream"
	"github.com/bhanuprakaash/jelly-fish/internal/testdb"
	"github.com/bhanuprakaash/jelly-fish/internal/worker"
)

// titleProvider is named like the fake so Gateway.Fake routes to it. It
// answers a Title request (spotted by provider.TitleMessage) with a quoted,
// padded title, and a turn with the fake's echo.
type titleProvider struct {
	turn fake.Provider
	// titleErr fails every Title call.
	titleErr error
	// gate, if set, holds a Title call until it is closed. A call that
	// honors ctx also returns when ctx ends.
	gate      chan struct{}
	ignoreCtx bool
	// turnErr fails the first turnFailures turn calls.
	turnErr      error
	turnFailures int

	started chan struct{}

	mu         sync.Mutex
	titleCalls int
	turnCalls  int
}

func newTitleProvider() *titleProvider {
	return &titleProvider{
		turn:    fake.Provider{WordDelay: time.Millisecond, MinReply: 20 * time.Millisecond},
		started: make(chan struct{}, 1),
	}
}

func (*titleProvider) Name() string { return fake.Name }

func (p *titleProvider) Stream(ctx context.Context, req provider.Request, onDelta func(provider.Delta)) (provider.Response, error) {
	last := req.Messages[len(req.Messages)-1].Text()
	if _, ok := provider.TitleMessage(last); !ok {
		p.mu.Lock()
		p.turnCalls++
		fail := p.turnCalls <= p.turnFailures
		p.mu.Unlock()
		if fail {
			return provider.Response{}, p.turnErr
		}
		return p.turn.Stream(ctx, req, onDelta)
	}

	p.mu.Lock()
	p.titleCalls++
	p.mu.Unlock()
	select {
	case p.started <- struct{}{}:
	default:
	}
	if p.gate != nil {
		if p.ignoreCtx {
			<-p.gate
		} else {
			select {
			case <-p.gate:
			case <-ctx.Done():
				return provider.Response{}, ctx.Err()
			}
		}
	}
	if p.titleErr != nil {
		return provider.Response{}, p.titleErr
	}
	return provider.Response{
		Message:    msg.AssistantText("  \"Fancy Title\"\n"),
		StopReason: provider.StopReasonEndTurn,
		Usage:      provider.Usage{Input: 5, Output: 3},
	}, nil
}

func (p *titleProvider) titleCount() int {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.titleCalls
}

// startTitleWorker runs a Worker with Titles on; a zero titleTimeout keeps
// the default. It returns a stop that waits for the Worker's goroutines.
func startTitleWorker(t *testing.T, pool *pgxpool.Pool, p provider.Provider, heartbeat, titleTimeout time.Duration) (stop func()) {
	t.Helper()
	gw := worker.Gateway{Fake: p, Sleep: func(context.Context, time.Duration) error { return nil }}
	w := worker.New(pool, gw, stream.NewPGDeltaBus(pool), worker.Lease{TTL: 30 * time.Second, Heartbeat: heartbeat}, eventlog.Upcasters{}, slog.New(slog.DiscardHandler))
	if titleTimeout > 0 {
		worker.SetTitleTimeout(w, titleTimeout)
	}
	ctx, cancel := context.WithCancel(t.Context())
	done := make(chan struct{})
	go func() { w.Run(ctx); close(done) }()
	var once sync.Once
	stop = func() { once.Do(func() { cancel(); <-done }) }
	t.Cleanup(stop)
	return stop
}

func loadLog(t *testing.T, pool *pgxpool.Pool, sid uuid.UUID) []eventlog.Event {
	t.Helper()
	evs, err := eventlog.NewStore(pool).Load(t.Context(), sid)
	if err != nil {
		t.Fatal(err)
	}
	return evs
}

// seqOf is the seq of the first event of typ that match accepts, or 0.
func seqOf(evs []eventlog.Event, typ string, match func(eventlog.Event) bool) int64 {
	for _, e := range evs {
		if e.Type == typ && (match == nil || match(e)) {
			return e.Seq
		}
	}
	return 0
}

func countType(evs []eventlog.Event, typ string) int {
	n := 0
	for _, e := range evs {
		if e.Type == typ {
			n++
		}
	}
	return n
}

func renamedBy(t *testing.T, evs []eventlog.Event) []string {
	t.Helper()
	var by []string
	for _, e := range evs {
		if e.Type != eventlog.TypeSessionRenamed {
			continue
		}
		var p eventlog.SessionRenamed
		if err := json.Unmarshal(e.Payload, &p); err != nil {
			t.Fatal(err)
		}
		by = append(by, p.By)
	}
	return by
}

func isStatusTo(to string) func(eventlog.Event) bool {
	return func(e eventlog.Event) bool {
		var p struct {
			To string `json:"to"`
		}
		return json.Unmarshal(e.Payload, &p) == nil && p.To == to
	}
}

func sessionTitle(t *testing.T, pool *pgxpool.Pool, sid uuid.UUID) *string {
	t.Helper()
	var title *string
	if err := pool.QueryRow(t.Context(), `SELECT title FROM sessions WHERE id = $1`, sid).Scan(&title); err != nil {
		t.Fatal(err)
	}
	return title
}

func newTitleSession(t *testing.T, pool *pgxpool.Pool, text string) (uuid.UUID, eventlog.TenantScope) {
	t.Helper()
	scope := testdb.NewUser(t, pool).Scope()
	sid := uuid.New()
	if _, err := eventlog.NewRepo(pool, fake.Name).CreateSession(t.Context(), scope, sid, uuid.New(), text, ""); err != nil {
		t.Fatal(err)
	}
	return sid, scope
}

func waitStarted(t *testing.T, p *titleProvider) {
	t.Helper()
	select {
	case <-p.started:
	case <-time.After(15 * time.Second):
		t.Fatal("title call never started")
	}
}

func TestAutoTitleLandsWhileTheFirstTurnRuns(t *testing.T) {
	pool := testdb.NewPool(t)
	sid, _ := newTitleSession(t, pool, "hello world")
	p := newTitleProvider()
	p.turn.MinReply = 300 * time.Millisecond
	startTitleWorker(t, pool, p, 10*time.Second, 0)
	waitStatus(t, pool, sid, eventlog.StatusAwaitingUser, 1)

	evs := loadLog(t, pool, sid)
	renamed := seqOf(evs, eventlog.TypeSessionRenamed, nil)
	reply := seqOf(evs, eventlog.TypeLLMResponse, nil)
	if renamed == 0 || renamed > reply {
		t.Fatalf("session.renamed seq %d, llm.response seq %d: the Title should land while the Turn runs", renamed, reply)
	}
	if by := renamedBy(t, evs); len(by) != 1 || by[0] != eventlog.RenamedByAuto {
		t.Fatalf("renamed by = %v", by)
	}
	if title := sessionTitle(t, pool, sid); title == nil || *title != "Fancy Title" {
		t.Fatalf("title = %v, want the cleaned Title", title)
	}
	if n := countType(evs, eventlog.TypeTurnStarted); n != 1 {
		t.Fatalf("turn.started = %d, want 1: the Title is not a Turn", n)
	}
	if n := countType(evs, eventlog.TypeLLMResponse); n != 1 {
		t.Fatalf("llm.response = %d, want only the Turn's", n)
	}

	var epoch, claimEpoch int64
	if err := pool.QueryRow(t.Context(), `SELECT lease_epoch FROM events WHERE session_id = $1 AND type = $2`, sid, eventlog.TypeSessionRenamed).Scan(&epoch); err != nil {
		t.Fatal(err)
	}
	if err := pool.QueryRow(t.Context(), `SELECT lease_epoch FROM sessions WHERE id = $1`, sid).Scan(&claimEpoch); err != nil {
		t.Fatal(err)
	}
	if epoch != claimEpoch {
		t.Fatalf("session.renamed lease_epoch = %d, want the claim's %d", epoch, claimEpoch)
	}

	var titleUsage int
	for _, e := range evs {
		if e.Type == eventlog.TypeUsageRecorded && e.CorrelationID == "" {
			titleUsage++
		}
	}
	if titleUsage != 2 {
		t.Fatalf("usage.recorded without a turn = %d, want 2 (input, output)", titleUsage)
	}
	var tokens int64
	if err := pool.QueryRow(t.Context(), `SELECT tokens_used FROM sessions WHERE id = $1`, sid).Scan(&tokens); err != nil {
		t.Fatal(err)
	}
	if tokens <= 8 {
		t.Fatalf("tokens_used = %d, want the Title's 8 on top of the Turn's", tokens)
	}
	assertFoldMatchesRow(t, pool, sid)
}

func TestUserRenameBeforeAutoLandsWins(t *testing.T) {
	pool := testdb.NewPool(t)
	sid, scope := newTitleSession(t, pool, "hello")
	p := newTitleProvider()
	p.gate = make(chan struct{})
	startTitleWorker(t, pool, p, 10*time.Second, 0)
	waitStarted(t, p)

	if err := eventlog.NewRepo(pool, fake.Name).Rename(t.Context(), scope, sid, "Mine"); err != nil {
		t.Fatal(err)
	}
	close(p.gate)
	waitStatus(t, pool, sid, eventlog.StatusAwaitingUser, 1)

	evs := loadLog(t, pool, sid)
	if by := renamedBy(t, evs); len(by) != 1 || by[0] != eventlog.RenamedByUser {
		t.Fatalf("renamed by = %v, want only the user's", by)
	}
	if title := sessionTitle(t, pool, sid); title == nil || *title != "Mine" {
		t.Fatalf("title = %v", title)
	}
	var titleUsage int
	for _, e := range evs {
		if e.Type == eventlog.TypeUsageRecorded && e.CorrelationID == "" {
			titleUsage++
		}
	}
	if titleUsage != 2 {
		t.Fatalf("title usage events = %d, want 2: the tokens were spent", titleUsage)
	}
}

func TestTitleCallFailureLeavesPlaceholderAndNeverRetries(t *testing.T) {
	pool := testdb.NewPool(t)
	sid, _ := newTitleSession(t, pool, "hello")
	p := newTitleProvider()
	p.titleErr = providerErr(provider.KindProviderDown, 3)
	startTitleWorker(t, pool, p, 10*time.Second, 0)
	waitStatus(t, pool, sid, eventlog.StatusAwaitingUser, 1)

	if title := sessionTitle(t, pool, sid); title != nil {
		t.Fatalf("title = %q, want the placeholder", *title)
	}
	if n := p.titleCount(); n != 1 {
		t.Fatalf("title calls = %d, want exactly 1 (no retry)", n)
	}
	evs := loadLog(t, pool, sid)
	if n := countType(evs, eventlog.TypeSessionError); n != 0 {
		t.Fatalf("session.error events = %d: a failed Title must not touch the session", n)
	}
	if n := countType(evs, eventlog.TypeTurnStarted); n != 1 {
		t.Fatalf("turn.started = %d, want 1", n)
	}
	var partial int
	for _, e := range evs {
		if e.Type == eventlog.TypeUsageRecorded && e.CorrelationID == "" {
			partial++
		}
	}
	if partial != 1 {
		t.Fatalf("title usage events = %d, want the failed call's 1", partial)
	}
}

func TestSlowTitleTimesOutWithoutBlockingPark(t *testing.T) {
	pool := testdb.NewPool(t)
	sid, _ := newTitleSession(t, pool, "hello")
	p := newTitleProvider()
	p.gate = make(chan struct{}) // never opened: the call waits for its ctx
	startTitleWorker(t, pool, p, 10*time.Second, 200*time.Millisecond)

	start := time.Now()
	waitStatus(t, pool, sid, eventlog.StatusAwaitingUser, 1)
	if took := time.Since(start); took > 3*time.Second {
		t.Fatalf("parked after %v, want the 200ms Title timeout to bound it", took)
	}
	if by := renamedBy(t, loadLog(t, pool, sid)); len(by) != 0 {
		t.Fatalf("renamed by = %v, want none", by)
	}
}

func TestParkWaitsForTheTitleToSettle(t *testing.T) {
	pool := testdb.NewPool(t)
	sid, _ := newTitleSession(t, pool, "hello")
	p := newTitleProvider()
	p.gate = make(chan struct{})
	startTitleWorker(t, pool, p, 10*time.Second, 0)
	waitStarted(t, p)

	// The quick Turn finishes and its reply lands, yet the session stays
	// running until the Title settles.
	deadline := time.Now().Add(15 * time.Second)
	for seqOf(loadLog(t, pool, sid), eventlog.TypeLLMResponse, nil) == 0 {
		if time.Now().After(deadline) {
			t.Fatal("the Turn's reply never landed")
		}
		time.Sleep(10 * time.Millisecond)
	}
	var status string
	if err := pool.QueryRow(t.Context(), `SELECT status FROM sessions WHERE id = $1`, sid).Scan(&status); err != nil {
		t.Fatal(err)
	}
	if status != eventlog.StatusRunning {
		t.Fatalf("status = %s while the Title is in flight, want running", status)
	}

	close(p.gate)
	waitStatus(t, pool, sid, eventlog.StatusAwaitingUser, 1)
	evs := loadLog(t, pool, sid)
	renamed := seqOf(evs, eventlog.TypeSessionRenamed, nil)
	parked := seqOf(evs, eventlog.TypeStatusChanged, isStatusTo(eventlog.StatusAwaitingUser))
	if renamed == 0 || renamed > parked {
		t.Fatalf("session.renamed seq %d, park seq %d: the Title must land before the park", renamed, parked)
	}
}

func TestTitleDroppedWhenLeaseIsLost(t *testing.T) {
	pool := testdb.NewPool(t)
	sid, _ := newTitleSession(t, pool, "hello")
	p := newTitleProvider()
	p.gate = make(chan struct{})
	p.ignoreCtx = true
	stop := startTitleWorker(t, pool, p, 20*time.Millisecond, 0)
	waitStarted(t, p)

	if _, err := pool.Exec(t.Context(), `UPDATE sessions SET lease_expires_at = now() - interval '1 second' WHERE id = $1`, sid); err != nil {
		t.Fatal(err)
	}
	c, ok, err := eventlog.NewStore(pool).Claim(t.Context(), "other", 30*time.Second)
	if err != nil || !ok {
		t.Fatalf("Claim: ok=%v err=%v", ok, err)
	}
	close(p.gate)
	stop()

	evs := loadLog(t, pool, sid)
	if by := renamedBy(t, evs); len(by) != 0 {
		t.Fatalf("renamed by = %v: a Title from a lost Lease must not land", by)
	}
	if last := evs[len(evs)-1]; last.Type != eventlog.TypeStatusChanged || !isStatusTo(eventlog.StatusRunning)(last) {
		t.Fatalf("last event = %s %s, want the other worker's claim (epoch %d) and no stray write", last.Type, last.Payload, c.Fence.Epoch)
	}
}

func TestTitleOnceAcrossRescueAndLaterTurns(t *testing.T) {
	t.Run("a rescued session already has its first turn", func(t *testing.T) {
		pool := testdb.NewPool(t)
		sid, _ := newTitleSession(t, pool, "hello")
		store := eventlog.NewStore(pool)
		c, ok, err := store.Claim(t.Context(), "dead", 30*time.Second)
		if err != nil || !ok {
			t.Fatalf("Claim: ok=%v err=%v", ok, err)
		}
		if _, err := store.AppendFenced(t.Context(), sid, c.Fence, []eventlog.NewEvent{{
			Type: eventlog.TypeTurnStarted, Actor: "worker:dead",
			Payload: map[string]any{"turn_id": "t1", "input_through_seq": 3},
		}}, nil); err != nil {
			t.Fatal(err)
		}
		if _, err := pool.Exec(t.Context(), `UPDATE sessions SET lease_expires_at = now() - interval '1 second' WHERE id = $1`, sid); err != nil {
			t.Fatal(err)
		}

		p := newTitleProvider()
		startTitleWorker(t, pool, p, 10*time.Second, 0)
		waitStatus(t, pool, sid, eventlog.StatusAwaitingUser, 1)
		if n := p.titleCount(); n != 0 {
			t.Fatalf("title calls = %d, want 0: at most once per session", n)
		}
	})

	t.Run("a later turn", func(t *testing.T) {
		pool := testdb.NewPool(t)
		sid, scope := newTitleSession(t, pool, "hello")
		p := newTitleProvider()
		startTitleWorker(t, pool, p, 10*time.Second, 0)
		waitStatus(t, pool, sid, eventlog.StatusAwaitingUser, 1)

		if _, err := eventlog.NewRepo(pool, fake.Name).PostMessage(t.Context(), scope, sid, uuid.New(), "again"); err != nil {
			t.Fatal(err)
		}
		waitStatus(t, pool, sid, eventlog.StatusAwaitingUser, 2)
		if n := p.titleCount(); n != 1 {
			t.Fatalf("title calls = %d, want 1", n)
		}
		if by := renamedBy(t, loadLog(t, pool, sid)); len(by) != 1 {
			t.Fatalf("renamed by = %v, want one", by)
		}
	})
}

// seedSession inserts a runnable session with session.created and a first
// user.message, for shapes CreateSession can't make.
func seedSession(t *testing.T, pool *pgxpool.Pool, scope eventlog.TenantScope, trigger string, parent *uuid.UUID) uuid.UUID {
	t.Helper()
	sid := uuid.New()
	depth := 0
	if parent != nil {
		depth = 1
	}
	if _, err := pool.Exec(t.Context(), `
		INSERT INTO sessions (id, workspace_id, project_id, user_id, agent_id, status, last_seq, ready_at, trigger, parent_id, depth)
		SELECT $1, $2, p.id, $3, $4, 'runnable', 2, now(), $5, $6, $7
		FROM projects p WHERE p.workspace_id = $2 AND p.user_id = $3 AND p.name = 'Personal'`,
		sid, scope.WorkspaceID, scope.UserID, uuid.New(), trigger, parent, depth); err != nil {
		t.Fatal(err)
	}
	created := map[string]any{"agent": map[string]string{"model": fake.Name}, "trigger": trigger}
	if parent != nil {
		created["parent_id"] = parent
	}
	for seq, e := range []struct {
		typ     string
		payload any
	}{
		{eventlog.TypeSessionCreated, created},
		{eventlog.TypeUserMessage, map[string]any{"client_msg_id": uuid.New(), "message": msg.UserText("hello")}},
	} {
		b, err := json.Marshal(e.payload)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := pool.Exec(t.Context(), `
			INSERT INTO events (session_id, seq, workspace_id, type, actor, payload) VALUES ($1, $2, $3, $4, 'test', $5)`,
			sid, seq+1, scope.WorkspaceID, e.typ, b); err != nil {
			t.Fatal(err)
		}
	}
	return sid
}

func TestNoTitleCallForChildSystemOrAlreadyRenamedSession(t *testing.T) {
	tests := []struct {
		name string
		seed func(t *testing.T, pool *pgxpool.Pool, scope eventlog.TenantScope) uuid.UUID
	}{
		{"child session", func(t *testing.T, pool *pgxpool.Pool, scope eventlog.TenantScope) uuid.UUID {
			parent := seedSession(t, pool, scope, eventlog.TriggerUserMessage, nil)
			if _, err := pool.Exec(t.Context(), `UPDATE sessions SET status = 'completed' WHERE id = $1`, parent); err != nil {
				t.Fatal(err)
			}
			return seedSession(t, pool, scope, eventlog.TriggerUserMessage, &parent)
		}},
		{"system session", func(t *testing.T, pool *pgxpool.Pool, scope eventlog.TenantScope) uuid.UUID {
			return seedSession(t, pool, scope, "memory_tidy", nil)
		}},
		{"already renamed", func(t *testing.T, pool *pgxpool.Pool, scope eventlog.TenantScope) uuid.UUID {
			sid := seedSession(t, pool, scope, eventlog.TriggerUserMessage, nil)
			if err := eventlog.NewRepo(pool, fake.Name).Rename(t.Context(), scope, sid, "Mine"); err != nil {
				t.Fatal(err)
			}
			return sid
		}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			pool := testdb.NewPool(t)
			sid := tt.seed(t, pool, testdb.NewUser(t, pool).Scope())
			p := newTitleProvider()
			startTitleWorker(t, pool, p, 10*time.Second, 0)
			waitStatus(t, pool, sid, eventlog.StatusAwaitingUser, 1)
			if n := p.titleCount(); n != 0 {
				t.Fatalf("title calls = %d, want 0", n)
			}
		})
	}
}

func TestTitleErrorStaleParkDoesNotRerunTurn(t *testing.T) {
	t.Run("the Title lands before the park", func(t *testing.T) {
		pool := testdb.NewPool(t)
		sid, _ := newTitleSession(t, pool, "hello")
		p := newTitleProvider()
		p.turnErr, p.turnFailures = providerErr(provider.KindKeyInvalid, 0), 1
		startTitleWorker(t, pool, p, 10*time.Second, 0)
		waitStatus(t, pool, sid, eventlog.StatusAwaitingUser, 0)

		evs := loadLog(t, pool, sid)
		if n := countType(evs, eventlog.TypeTurnStarted); n != 1 {
			t.Fatalf("turn.started = %d, want 1: the Title's write must not make the park stale", n)
		}
		if n := countType(evs, eventlog.TypeSessionError); n != 1 {
			t.Fatalf("session.error = %d, want 1", n)
		}
		if title := sessionTitle(t, pool, sid); title == nil || *title != "Fancy Title" {
			t.Fatalf("title = %v", title)
		}
	})

	t.Run("a message lands before the Title", func(t *testing.T) {
		pool := testdb.NewPool(t)
		sid, scope := newTitleSession(t, pool, "hello")
		p := newTitleProvider()
		p.gate = make(chan struct{})
		p.turnErr, p.turnFailures = providerErr(provider.KindKeyInvalid, 0), 1
		startTitleWorker(t, pool, p, 10*time.Second, 0)
		waitStarted(t, p)

		if _, err := eventlog.NewRepo(pool, fake.Name).PostMessage(t.Context(), scope, sid, uuid.New(), "more"); err != nil {
			t.Fatal(err)
		}
		close(p.gate)
		waitStatus(t, pool, sid, eventlog.StatusAwaitingUser, 1)

		evs := loadLog(t, pool, sid)
		if n := countType(evs, eventlog.TypeTurnStarted); n != 2 {
			t.Fatalf("turn.started = %d, want 2: the stale park re-runs the Turn", n)
		}
		if n := p.titleCount(); n != 1 {
			t.Fatalf("title calls = %d, want 1", n)
		}
		assertFoldMatchesRow(t, pool, sid)
	})
}
