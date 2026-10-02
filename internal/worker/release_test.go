package worker_test

import (
	"errors"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/bhanuprakaash/jelly-fish/internal/eventlog"
	"github.com/bhanuprakaash/jelly-fish/internal/msg"
	"github.com/bhanuprakaash/jelly-fish/internal/testdb"
	"github.com/bhanuprakaash/jelly-fish/internal/worker"
)

const futureType = "test.renamed"

func newSession(t *testing.T, pool *pgxpool.Pool) uuid.UUID {
	t.Helper()
	sid := uuid.New()
	if _, err := eventlog.NewRepo(pool, "fake").CreateSession(t.Context(), testdb.NewUser(t, pool).Scope(), sid, uuid.New(), "hi"); err != nil {
		t.Fatal(err)
	}
	return sid
}

func appendEvent(t *testing.T, pool *pgxpool.Pool, sid uuid.UUID, typ string, payload any) {
	t.Helper()
	ev := eventlog.NewEvent{Type: typ, Actor: "test", Payload: payload}
	if _, err := eventlog.NewStore(pool).Append(t.Context(), sid, nil, []eventlog.NewEvent{ev}, nil); err != nil {
		t.Fatal(err)
	}
}

func eventTypes(t *testing.T, pool *pgxpool.Pool, sid uuid.UUID) []string {
	t.Helper()
	evs, err := eventlog.NewStore(pool).Load(t.Context(), sid)
	if err != nil {
		t.Fatal(err)
	}
	types := make([]string, len(evs))
	for i, e := range evs {
		types[i] = e.Type
	}
	return types
}

func sessionRow(t *testing.T, pool *pgxpool.Pool, sid uuid.UUID) (status string, attempts int, leaseExpired bool) {
	t.Helper()
	err := pool.QueryRow(t.Context(),
		`SELECT status, recovery_attempts, coalesce(lease_expires_at <= now(), false) FROM sessions WHERE id = $1`, sid,
	).Scan(&status, &attempts, &leaseExpired)
	if err != nil {
		t.Fatal(err)
	}
	return status, attempts, leaseExpired
}

// assertReleased starts a Worker that cannot read sid, and checks that it
// claims once, hands the Lease back, and then leaves the session alone: the
// only new event is the claim's own, however many times it is woken.
func assertReleased(t *testing.T, pool *pgxpool.Pool, sid uuid.UUID, wantAttempts int) {
	t.Helper()
	before := len(eventTypes(t, pool, sid))
	startWorker(t, pool)

	deadline := time.Now().Add(15 * time.Second)
	for {
		status, attempts, expired := sessionRow(t, pool, sid)
		if status == eventlog.StatusRunning && expired && attempts == wantAttempts && len(eventTypes(t, pool, sid)) == before+1 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("session never released: status=%s attempts=%d expired=%v", status, attempts, expired)
		}
		time.Sleep(20 * time.Millisecond)
	}

	for range 5 {
		if _, err := pool.Exec(t.Context(), `SELECT pg_notify('jf_runnable', $1)`, sid.String()); err != nil {
			t.Fatal(err)
		}
		time.Sleep(100 * time.Millisecond)
	}

	types := eventTypes(t, pool, sid)
	if len(types) != before+1 || types[before] != eventlog.TypeStatusChanged {
		t.Fatalf("session has %d events, want %d: only the claim's status_changed after the first %d", len(types), before+1, before)
	}
	status, attempts, _ := sessionRow(t, pool, sid)
	if status != eventlog.StatusRunning || attempts != wantAttempts {
		t.Fatalf("row = {%s %d}, want {running %d}", status, attempts, wantAttempts)
	}
}

func TestRollingDeployStaleWorkerReleasesForCompatibleWorker(t *testing.T) {
	pool := testdb.NewPool(t)
	sid := newSession(t, pool)
	appendEvent(t, pool, sid, futureType, map[string]int{"a": 1})
	if _, err := pool.Exec(t.Context(), `UPDATE events SET schema_version = 2 WHERE session_id = $1 AND type = $2`, sid, futureType); err != nil {
		t.Fatal(err)
	}

	assertReleased(t, pool, sid, 0)
	assertFoldMatchesRow(t, pool, sid)

	// A Worker that knows v2 takes over while the stale one is still running.
	noop := func(p []byte) ([]byte, error) { return p, nil }
	startWorkerWith(t, pool, eventlog.Upcasters{futureType: {noop}})
	waitStatus(t, pool, sid, eventlog.StatusAwaitingUser, 1)
	assertFoldMatchesRow(t, pool, sid)
}

func TestStaleWorkerReleasesUnreadableMessage(t *testing.T) {
	tests := []struct {
		name    string
		typ     string
		message map[string]any
	}{
		{
			"user.message with a newer msg_v", eventlog.TypeUserMessage,
			map[string]any{"msg_v": msg.CurrentVersion + 1, "role": msg.RoleUser, "parts": []map[string]string{{"k": string(msg.KindText), "text": "x"}}},
		},
		{
			"user.message with an unknown Part kind", eventlog.TypeUserMessage,
			map[string]any{"msg_v": msg.CurrentVersion, "role": msg.RoleUser, "parts": []map[string]string{{"k": "hologram"}}},
		},
		{
			"llm.response with an unknown Part kind", eventlog.TypeLLMResponse,
			map[string]any{"msg_v": msg.CurrentVersion, "role": msg.RoleAssistant, "parts": []map[string]string{{"k": "hologram"}}},
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			pool := testdb.NewPool(t)
			sid := newSession(t, pool)
			appendEvent(t, pool, sid, tc.typ, map[string]any{"client_msg_id": uuid.New(), "message": tc.message})
			assertReleased(t, pool, sid, 0)
		})
	}
}

func TestStaleWorkerReleaseDoesNotAdvanceCrashLoop(t *testing.T) {
	pool := testdb.NewPool(t)
	store := eventlog.NewStore(pool)
	sid := newSession(t, pool)
	appendEvent(t, pool, sid, futureType, map[string]int{"a": 1})
	if _, err := pool.Exec(t.Context(), `UPDATE events SET schema_version = 2 WHERE session_id = $1 AND type = $2`, sid, futureType); err != nil {
		t.Fatal(err)
	}

	// Four earlier claims died, so one more attempt is the last allowed.
	for range 4 {
		if _, ok, err := store.Claim(t.Context(), "dead", 30*time.Second); err != nil || !ok {
			t.Fatalf("Claim: ok=%v err=%v", ok, err)
		}
		if _, err := pool.Exec(t.Context(), `UPDATE sessions SET lease_expires_at = now() - interval '1 second' WHERE id = $1`, sid); err != nil {
			t.Fatal(err)
		}
	}

	assertReleased(t, pool, sid, 4)
	if status, _, _ := sessionRow(t, pool, sid); status == eventlog.StatusFailed {
		t.Fatal("stale worker failed the session")
	}
}

func TestFoldRejectsUnreadableMessageWithSentinel(t *testing.T) {
	ev := func(typ string, payload string) eventlog.Event {
		return eventlog.Event{Seq: 1, Type: typ, SchemaVersion: 1, Payload: []byte(payload)}
	}
	tests := []struct {
		name string
		ev   eventlog.Event
		want error
	}{
		{"current message", ev(eventlog.TypeUserMessage, `{"message":{"msg_v":2,"role":"user","parts":[{"k":"text","text":"x"}]}}`), nil},
		{"older message", ev(eventlog.TypeUserMessage, `{"message":{"msg_v":1,"role":"user","parts":[{"type":"text","text":"x"}]}}`), nil},
		{"newer msg_v", ev(eventlog.TypeUserMessage, `{"message":{"msg_v":3,"role":"user","parts":[{"k":"text","text":"x"}]}}`), msg.ErrUnsupported},
		{"unknown Part kind", ev(eventlog.TypeLLMResponse, `{"message":{"msg_v":2,"role":"assistant","parts":[{"k":"hologram"}]}}`), msg.ErrUnsupported},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			_, err := worker.Fold([]eventlog.Event{tc.ev})
			if !errors.Is(err, tc.want) {
				t.Fatalf("err = %v, want %v", err, tc.want)
			}
		})
	}
}
