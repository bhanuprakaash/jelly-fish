package eventlog_test

import (
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/bhanuprakaash/jelly-fish/internal/eventlog"
	"github.com/bhanuprakaash/jelly-fish/internal/testdb"
)

func TestStoreAppend_StatusChangeIsProjectedInSameTx(t *testing.T) {
	pool := testdb.NewPool(t)
	repo := eventlog.NewRepo(pool, "fake")
	store := eventlog.NewStore(pool)
	scope := testdb.NewUser(t, pool).Scope()
	sessionID := uuid.New()

	if _, err := repo.CreateSession(t.Context(), scope, sessionID, uuid.New(), "hello", ""); err != nil {
		t.Fatalf("CreateSession: %v", err)
	}

	seqs, err := store.Append(t.Context(), sessionID, nil, nil, &eventlog.StatusChange{
		To:     "awaiting_user",
		Reason: "end_turn",
	})
	if err != nil {
		t.Fatalf("Append: %v", err)
	}
	if len(seqs) != 1 || seqs[0] != 3 {
		t.Fatalf("seqs = %v, want [3]", seqs)
	}

	var status string
	err = pool.QueryRow(t.Context(), `SELECT status FROM sessions WHERE id = $1`, sessionID).Scan(&status)
	if err != nil {
		t.Fatalf("query status: %v", err)
	}
	if status != "awaiting_user" {
		t.Fatalf("sessions.status = %q, want awaiting_user", status)
	}

	evs, err := repo.ListEvents(t.Context(), scope, sessionID, 2)
	if err != nil {
		t.Fatalf("ListEvents: %v", err)
	}
	if len(evs) != 1 || evs[0].Type != eventlog.TypeStatusChanged {
		t.Fatalf("events = %+v, want one session.status_changed", evs)
	}

	var payload struct {
		From   string `json:"from"`
		To     string `json:"to"`
		Reason string `json:"reason"`
	}
	if err := json.Unmarshal(evs[0].Payload, &payload); err != nil {
		t.Fatalf("unmarshal payload: %v", err)
	}
	if payload.From != eventlog.StatusRunnable || payload.To != "awaiting_user" || payload.Reason != "end_turn" {
		t.Fatalf("payload = %+v, want from=runnable to=awaiting_user reason=end_turn", payload)
	}
}

func TestStoreAppend_UnknownSessionIsNotFound(t *testing.T) {
	pool := testdb.NewPool(t)
	store := eventlog.NewStore(pool)

	_, err := store.Append(t.Context(), uuid.New(), nil, []eventlog.NewEvent{
		{Type: "user.message", Actor: "user:x", Payload: map[string]string{"x": "y"}},
	}, nil)
	if !errors.Is(err, eventlog.ErrNotFound) {
		t.Fatalf("Append on unknown session: err = %v, want ErrNotFound", err)
	}
}

func TestAppendFenced_NoOpStillChecksLease(t *testing.T) {
	pool := testdb.NewPool(t)
	store := eventlog.NewStore(pool)
	sid := uuid.New()
	if _, err := eventlog.NewRepo(pool, "fake").CreateSession(t.Context(), testdb.NewUser(t, pool).Scope(), sid, uuid.New(), "hi", ""); err != nil {
		t.Fatalf("CreateSession: %v", err)
	}
	c, _, err := store.Claim(t.Context(), "w1", 30*time.Second)
	if err != nil {
		t.Fatalf("Claim: %v", err)
	}
	stale := c.Fence
	stale.Epoch--

	// The status change doesn't apply (session is running), leaving nothing
	// to write; a stale fence must still be rejected.
	_, err = store.AppendFenced(t.Context(), sid, stale, nil, &eventlog.StatusChange{
		To: eventlog.StatusRunnable, From: []string{eventlog.StatusAwaitingUser},
	})
	if !errors.Is(err, eventlog.ErrLeaseLost) {
		t.Fatalf("err = %v, want ErrLeaseLost", err)
	}
}

func TestRelease(t *testing.T) {
	pool := testdb.NewPool(t)
	store := eventlog.NewStore(pool)
	sid := uuid.New()
	if _, err := eventlog.NewRepo(pool, "fake").CreateSession(t.Context(), testdb.NewUser(t, pool).Scope(), sid, uuid.New(), "hi", ""); err != nil {
		t.Fatalf("CreateSession: %v", err)
	}
	row := func() (status string, last int64, attempts int) {
		t.Helper()
		err := pool.QueryRow(t.Context(), `SELECT status, last_seq, recovery_attempts FROM sessions WHERE id = $1`, sid).Scan(&status, &last, &attempts)
		if err != nil {
			t.Fatal(err)
		}
		return status, last, attempts
	}

	c, ok, err := store.Claim(t.Context(), "w1", 30*time.Second)
	if err != nil || !ok {
		t.Fatalf("Claim: ok=%v err=%v", ok, err)
	}
	_, claimedLast, _ := row()

	if err := store.Release(t.Context(), sid, c.Fence); err != nil {
		t.Fatalf("Release: %v", err)
	}
	status, last, attempts := row()
	if status != eventlog.StatusRunning || last != claimedLast || attempts != 0 {
		t.Fatalf("row after release = {%s %d %d}, want {running %d 0}: no event, attempt undone", status, last, attempts, claimedLast)
	}
	if err := store.Release(t.Context(), sid, c.Fence); !errors.Is(err, eventlog.ErrLeaseLost) {
		t.Fatalf("second Release err = %v, want ErrLeaseLost", err)
	}

	if _, ok, err := store.Claim(t.Context(), "w1", 30*time.Second, sid); err != nil || ok {
		t.Fatalf("Claim skipping the released session: ok=%v err=%v, want none", ok, err)
	}
	c2, ok, err := store.Claim(t.Context(), "w2", 30*time.Second)
	if err != nil || !ok {
		t.Fatalf("Claim by another Worker: ok=%v err=%v", ok, err)
	}
	if c2.Fence.Epoch != c.Fence.Epoch+1 {
		t.Fatalf("epoch = %d, want %d", c2.Fence.Epoch, c.Fence.Epoch+1)
	}
	if err := store.Release(t.Context(), sid, c.Fence); !errors.Is(err, eventlog.ErrLeaseLost) {
		t.Fatalf("Release with the old fence err = %v, want ErrLeaseLost", err)
	}
}

func TestReleaseNeverCountsTowardCrashLoop(t *testing.T) {
	pool := testdb.NewPool(t)
	store := eventlog.NewStore(pool)
	sid := uuid.New()
	if _, err := eventlog.NewRepo(pool, "fake").CreateSession(t.Context(), testdb.NewUser(t, pool).Scope(), sid, uuid.New(), "hi", ""); err != nil {
		t.Fatalf("CreateSession: %v", err)
	}
	for range 10 {
		c, ok, err := store.Claim(t.Context(), "stale", 30*time.Second)
		if err != nil || !ok {
			t.Fatalf("Claim: ok=%v err=%v", ok, err)
		}
		if c.RecoveryAttempts != 1 {
			t.Fatalf("RecoveryAttempts = %d, want 1: releases must not accumulate", c.RecoveryAttempts)
		}
		if err := store.Release(t.Context(), sid, c.Fence); err != nil {
			t.Fatalf("Release: %v", err)
		}
	}
}

func sleepingSession(t *testing.T, pool *pgxpool.Pool, wake time.Duration) (uuid.UUID, eventlog.TenantScope) {
	t.Helper()
	store := eventlog.NewStore(pool)
	scope := testdb.NewUser(t, pool).Scope()
	sid := uuid.New()
	if _, err := eventlog.NewRepo(pool, "fake").CreateSession(t.Context(), scope, sid, uuid.New(), "hi", ""); err != nil {
		t.Fatal(err)
	}
	c, ok, err := store.Claim(t.Context(), "w1", 30*time.Second)
	if err != nil || !ok {
		t.Fatalf("Claim: ok=%v err=%v", ok, err)
	}
	if _, err := store.AppendFenced(t.Context(), sid, c.Fence, nil, &eventlog.StatusChange{To: eventlog.StatusSleeping, Reason: "provider_down", WakeIn: wake}); err != nil {
		t.Fatal(err)
	}
	return sid, scope
}

func TestSleepingStatusChangeSetsWakeAtFromTheDBClock(t *testing.T) {
	pool := testdb.NewPool(t)
	sid, scope := sleepingSession(t, pool, time.Minute)

	var status string
	var inWindow bool
	if err := pool.QueryRow(t.Context(), `
		SELECT status, wake_at BETWEEN now() + interval '55 seconds' AND now() + interval '65 seconds'
		FROM sessions WHERE id = $1`, sid).Scan(&status, &inWindow); err != nil {
		t.Fatal(err)
	}
	if status != eventlog.StatusSleeping || !inWindow {
		t.Fatalf("status=%s wake_at about now+1m: %v", status, inWindow)
	}

	evs, err := eventlog.NewRepo(pool, "fake").ListEvents(t.Context(), scope, sid, 3)
	if err != nil {
		t.Fatal(err)
	}
	if len(evs) != 2 || evs[0].Type != eventlog.TypeTimerSet || evs[1].Type != eventlog.TypeStatusChanged {
		t.Fatalf("events = %+v, want timer.set then status_changed", evs)
	}
	var p struct {
		WakeAt time.Time `json:"wake_at"`
		Reason string    `json:"reason"`
	}
	if err := json.Unmarshal(evs[0].Payload, &p); err != nil {
		t.Fatal(err)
	}
	var matches bool
	if err := pool.QueryRow(t.Context(), `SELECT wake_at = $2 FROM sessions WHERE id = $1`, sid, p.WakeAt).Scan(&matches); err != nil {
		t.Fatal(err)
	}
	if !matches || p.Reason != "provider_down" {
		t.Fatalf("timer.set = %+v, wake_at must equal the column", p)
	}
}

func TestClaimWakesSleepingSessionAtWakeAt(t *testing.T) {
	pool := testdb.NewPool(t)
	store := eventlog.NewStore(pool)
	sid, scope := sleepingSession(t, pool, time.Minute)

	if _, ok, err := store.Claim(t.Context(), "w2", 30*time.Second); err != nil || ok {
		t.Fatalf("Claim before wake_at: ok=%v err=%v, want none", ok, err)
	}
	if _, err := pool.Exec(t.Context(), `UPDATE sessions SET wake_at = now() - interval '1 second' WHERE id = $1`, sid); err != nil {
		t.Fatal(err)
	}
	var before int64
	if err := pool.QueryRow(t.Context(), `SELECT last_seq FROM sessions WHERE id = $1`, sid).Scan(&before); err != nil {
		t.Fatal(err)
	}

	c, ok, err := store.Claim(t.Context(), "w2", 30*time.Second)
	if err != nil || !ok || c.SessionID != sid {
		t.Fatalf("Claim at wake_at: ok=%v err=%v", ok, err)
	}
	evs, err := eventlog.NewRepo(pool, "fake").ListEvents(t.Context(), scope, sid, before)
	if err != nil {
		t.Fatal(err)
	}
	if len(evs) != 2 || evs[0].Type != eventlog.TypeTimerFired || evs[1].Type != eventlog.TypeStatusChanged || evs[1].Seq != before+2 {
		t.Fatalf("events = %+v, want timer.fired then status_changed at last_seq+2", evs)
	}
	var wake *time.Time
	var status string
	if err := pool.QueryRow(t.Context(), `SELECT status, wake_at FROM sessions WHERE id = $1`, sid).Scan(&status, &wake); err != nil {
		t.Fatal(err)
	}
	if status != eventlog.StatusRunning || wake != nil {
		t.Fatalf("status=%s wake_at=%v, want running with wake_at cleared", status, wake)
	}
}

func TestHeartbeatReportsCancelRequested(t *testing.T) {
	pool := testdb.NewPool(t)
	store := eventlog.NewStore(pool)
	sid := uuid.New()
	if _, err := eventlog.NewRepo(pool, "fake").CreateSession(t.Context(), testdb.NewUser(t, pool).Scope(), sid, uuid.New(), "hi", ""); err != nil {
		t.Fatal(err)
	}
	c, _, err := store.Claim(t.Context(), "w1", 30*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	if cancel, err := store.Heartbeat(t.Context(), sid, c.Fence, 30*time.Second); err != nil || cancel {
		t.Fatalf("Heartbeat = %v, %v, want no cancel", cancel, err)
	}
	if _, err := pool.Exec(t.Context(), `UPDATE sessions SET cancel_requested = true WHERE id = $1`, sid); err != nil {
		t.Fatal(err)
	}
	if cancel, err := store.Heartbeat(t.Context(), sid, c.Fence, 30*time.Second); err != nil || !cancel {
		t.Fatalf("Heartbeat = %v, %v, want cancel", cancel, err)
	}
}
