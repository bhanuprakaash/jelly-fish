package eventlog_test

import (
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/bhanuprakaash/jelly-fish/internal/eventlog"
	"github.com/bhanuprakaash/jelly-fish/internal/testdb"
)

func TestStoreAppend_StatusChangeIsProjectedInSameTx(t *testing.T) {
	pool := testdb.NewPool(t)
	repo := eventlog.NewRepo(pool)
	store := eventlog.NewStore(pool)
	scope := eventlog.DevScope()
	sessionID := uuid.New()

	if _, err := repo.CreateSession(t.Context(), scope, sessionID, uuid.New(), "hello"); err != nil {
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
	if _, err := eventlog.NewRepo(pool).CreateSession(t.Context(), eventlog.DevScope(), sid, uuid.New(), "hi"); err != nil {
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
