package worker_test

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/bhanuprakaash/jelly-fish/internal/eventlog"
	"github.com/bhanuprakaash/jelly-fish/internal/msg"
	"github.com/bhanuprakaash/jelly-fish/internal/provider/fake"
	"github.com/bhanuprakaash/jelly-fish/internal/stream"
	"github.com/bhanuprakaash/jelly-fish/internal/testdb"
	"github.com/bhanuprakaash/jelly-fish/internal/worker"
)

func startWorker(t *testing.T, pool *pgxpool.Pool) {
	t.Helper()
	startWorkerWith(t, pool, eventlog.Upcasters{})
}

func startWorkerWith(t *testing.T, pool *pgxpool.Pool, upcast eventlog.Upcasters) {
	t.Helper()
	ctx, cancel := context.WithCancel(t.Context())
	w := worker.New(pool, fake.Provider{WordDelay: time.Millisecond, MinReply: 20 * time.Millisecond}, stream.NewPGDeltaBus(pool), worker.Lease{TTL: 30 * time.Second, Heartbeat: 10 * time.Second}, upcast, slog.New(slog.DiscardHandler))
	done := make(chan struct{})
	go func() { w.Run(ctx); close(done) }()
	t.Cleanup(func() { cancel(); <-done })
}

func waitStatus(t *testing.T, pool *pgxpool.Pool, sid uuid.UUID, want string, turns int) {
	t.Helper()
	deadline := time.Now().Add(15 * time.Second)
	for time.Now().Before(deadline) {
		var status string
		var got int
		if err := pool.QueryRow(t.Context(), `SELECT status, turns FROM sessions WHERE id = $1`, sid).Scan(&status, &got); err != nil {
			t.Fatal(err)
		}
		if status == want && got == turns {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("session never reached status %s with %d turns", want, turns)
}

func assertFoldMatchesRow(t *testing.T, pool *pgxpool.Pool, sid uuid.UUID) {
	t.Helper()
	evs, err := eventlog.NewStore(pool).Load(t.Context(), sid)
	if err != nil {
		t.Fatal(err)
	}
	st, err := worker.Fold(evs)
	if err != nil {
		t.Fatal(err)
	}
	var status string
	var last, tokens, cost int64
	var turns int
	err = pool.QueryRow(t.Context(),
		`SELECT status, last_seq, tokens_used, cost_micros, turns FROM sessions WHERE id = $1`, sid,
	).Scan(&status, &last, &tokens, &cost, &turns)
	if err != nil {
		t.Fatal(err)
	}
	if st.Status != status || st.LastSeq != last || st.TokensUsed != tokens || st.CostMicros != cost || st.Turns != turns {
		t.Fatalf("fold = {%s %d %d %d %d}, row = {%s %d %d %d %d}",
			st.Status, st.LastSeq, st.TokensUsed, st.CostMicros, st.Turns, status, last, tokens, cost, turns)
	}
}

func TestWorkerRepliesAndParks(t *testing.T) {
	pool := testdb.NewPool(t)
	repo := eventlog.NewRepo(pool)
	scope := testdb.NewUser(t, pool).Scope()
	sid := uuid.New()
	if _, err := repo.CreateSession(t.Context(), scope, sid, uuid.New(), "hello world"); err != nil {
		t.Fatal(err)
	}
	startWorker(t, pool)
	waitStatus(t, pool, sid, eventlog.StatusAwaitingUser, 1)
	assertFoldMatchesRow(t, pool, sid)

	var usageRows int
	if err := pool.QueryRow(t.Context(), `SELECT count(*) FROM usage WHERE session_id = $1 AND provider = 'fake'`, sid).Scan(&usageRows); err != nil {
		t.Fatal(err)
	}
	if usageRows != 2 {
		t.Fatalf("usage rows = %d, want 2 (input, output)", usageRows)
	}

	// A follow-up resumes the parked session for a second turn.
	if _, err := repo.PostMessage(t.Context(), scope, sid, uuid.New(), "again"); err != nil {
		t.Fatal(err)
	}
	waitStatus(t, pool, sid, eventlog.StatusAwaitingUser, 2)
	assertFoldMatchesRow(t, pool, sid)
}

func TestParkAfterNewMessageIsStale(t *testing.T) {
	pool := testdb.NewPool(t)
	repo := eventlog.NewRepo(pool)
	store := eventlog.NewStore(pool)
	sid := uuid.New()
	if _, err := repo.CreateSession(t.Context(), testdb.NewUser(t, pool).Scope(), sid, uuid.New(), "hi"); err != nil {
		t.Fatal(err)
	}
	c, ok, err := store.Claim(t.Context(), "w1", 30*time.Second)
	if err != nil || !ok {
		t.Fatalf("Claim: ok=%v err=%v", ok, err)
	}
	folded := int64(3) // created, user.message, claimed

	// A message lands between fold and park.
	if _, err := repo.PostMessage(t.Context(), testdb.NewUser(t, pool).Scope(), sid, uuid.New(), "more"); err != nil {
		t.Fatal(err)
	}

	f := c.Fence
	f.ExpectSeq = &folded
	_, err = store.AppendFenced(t.Context(), sid, f, nil, &eventlog.StatusChange{To: eventlog.StatusAwaitingUser, Reason: "end_turn"})
	if !errors.Is(err, eventlog.ErrStale) {
		t.Fatalf("park err = %v, want ErrStale", err)
	}

	evs, err := store.Load(t.Context(), sid)
	if err != nil {
		t.Fatal(err)
	}
	st, err := worker.Fold(evs)
	if err != nil {
		t.Fatal(err)
	}
	if got := worker.Decide(st).Kind; got != worker.StepStartTurn {
		t.Fatalf("Decide after refold = %d, want StepStartTurn", got)
	}
}

func TestFencedAppendFromOldEpochIsRejected(t *testing.T) {
	pool := testdb.NewPool(t)
	store := eventlog.NewStore(pool)
	sid := uuid.New()
	if _, err := eventlog.NewRepo(pool).CreateSession(t.Context(), testdb.NewUser(t, pool).Scope(), sid, uuid.New(), "hi"); err != nil {
		t.Fatal(err)
	}
	c, _, err := store.Claim(t.Context(), "w1", 30*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	stale := c.Fence
	stale.Epoch--
	_, err = store.AppendFenced(t.Context(), sid, stale, []eventlog.NewEvent{{Type: "x", Actor: "w", Payload: map[string]int{}}}, nil)
	if !errors.Is(err, eventlog.ErrLeaseLost) {
		t.Fatalf("err = %v, want ErrLeaseLost", err)
	}
}

func TestRescuedTurnIsRerun(t *testing.T) {
	pool := testdb.NewPool(t)
	store := eventlog.NewStore(pool)
	sid := uuid.New()
	if _, err := eventlog.NewRepo(pool).CreateSession(t.Context(), testdb.NewUser(t, pool).Scope(), sid, uuid.New(), "hi"); err != nil {
		t.Fatal(err)
	}

	// A worker claims, starts a turn, and dies before the reply.
	c, _, err := store.Claim(t.Context(), "dead", 30*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	_, err = store.AppendFenced(t.Context(), sid, c.Fence, []eventlog.NewEvent{{
		Type: eventlog.TypeTurnStarted, Actor: "worker:dead",
		Payload: map[string]any{"turn_id": "t1", "input_through_seq": 3},
	}}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(t.Context(), `UPDATE sessions SET lease_expires_at = now() - interval '1 second' WHERE id = $1`, sid); err != nil {
		t.Fatal(err)
	}

	startWorker(t, pool)
	waitStatus(t, pool, sid, eventlog.StatusAwaitingUser, 1)
	assertFoldMatchesRow(t, pool, sid)
}

func TestCrashLoopFailsSessionWithoutDeciding(t *testing.T) {
	pool := testdb.NewPool(t)
	store := eventlog.NewStore(pool)
	sid := uuid.New()
	if _, err := eventlog.NewRepo(pool).CreateSession(t.Context(), testdb.NewUser(t, pool).Scope(), sid, uuid.New(), "hi"); err != nil {
		t.Fatal(err)
	}

	// Five Workers claim and die before their first append.
	for range 5 {
		if _, ok, err := store.Claim(t.Context(), "dead", 30*time.Second); err != nil || !ok {
			t.Fatalf("Claim: ok=%v err=%v", ok, err)
		}
		if _, err := pool.Exec(t.Context(), `UPDATE sessions SET lease_expires_at = now() - interval '1 second' WHERE id = $1`, sid); err != nil {
			t.Fatal(err)
		}
	}

	startWorker(t, pool)
	waitStatus(t, pool, sid, eventlog.StatusFailed, 0)

	evs, err := store.Load(t.Context(), sid)
	if err != nil {
		t.Fatal(err)
	}
	// created, user.message, 5 claims, then the 6th claim and the failure.
	var tail []string
	for _, e := range evs[7:] {
		tail = append(tail, e.Type)
	}
	want := []string{eventlog.TypeStatusChanged, eventlog.TypeSessionError, eventlog.TypeStatusChanged}
	if !slices.Equal(tail, want) {
		t.Fatalf("events after the 5th claim = %v, want %v", tail, want)
	}
	var p struct {
		Code      string `json:"code"`
		Retryable bool   `json:"retryable"`
	}
	if err := json.Unmarshal(evs[8].Payload, &p); err != nil {
		t.Fatal(err)
	}
	if p.Code != "crash_loop" || p.Retryable {
		t.Fatalf("session.error = %+v, want crash_loop, not retryable", p)
	}
	assertFoldMatchesRow(t, pool, sid)
}

func TestStreamedTurnPublishesDeltasNotEvents(t *testing.T) {
	pool := testdb.NewPool(t)
	sid := uuid.New()
	if _, err := eventlog.NewRepo(pool).CreateSession(t.Context(), testdb.NewUser(t, pool).Scope(), sid, uuid.New(), "hello world"); err != nil {
		t.Fatal(err)
	}

	listener, err := pool.Acquire(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Release()
	if _, err := listener.Exec(t.Context(), "LISTEN jf_stream"); err != nil {
		t.Fatal(err)
	}

	startWorker(t, pool)
	waitStatus(t, pool, sid, eventlog.StatusAwaitingUser, 1)

	var streamed strings.Builder
	for {
		ctx, cancel := context.WithTimeout(t.Context(), 200*time.Millisecond)
		n, err := listener.Conn().WaitForNotification(ctx)
		cancel()
		if err != nil {
			break
		}
		if len(n.Payload) >= 8000 {
			t.Fatalf("payload is %d bytes, want < 8000", len(n.Payload))
		}
		var d stream.Delta
		if err := json.Unmarshal([]byte(n.Payload), &d); err != nil {
			t.Fatal(err)
		}
		streamed.WriteString(d.Text)
	}

	evs, err := eventlog.NewStore(pool).Load(t.Context(), sid)
	if err != nil {
		t.Fatal(err)
	}
	var replies int
	var reply string
	for _, e := range evs {
		if e.Type == eventlog.TypeLLMResponse {
			replies++
			var p struct{ Message msg.Message }
			if err := json.Unmarshal(e.Payload, &p); err != nil {
				t.Fatal(err)
			}
			reply = p.Message.Text()
		}
	}
	if replies != 1 {
		t.Fatalf("llm.response events = %d, want 1", replies)
	}
	if streamed.String() != reply {
		t.Fatalf("streamed %q, want the final reply %q", streamed.String(), reply)
	}
}
