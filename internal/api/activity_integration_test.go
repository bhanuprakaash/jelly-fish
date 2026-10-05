package api

import (
	"context"
	"log/slog"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/bhanuprakaash/jelly-fish/internal/eventlog"
	"github.com/bhanuprakaash/jelly-fish/internal/stream"
	"github.com/bhanuprakaash/jelly-fish/internal/testdb"
)

// insertSession inserts a session row CreateSession can't make: a System
// Session or a child.
func insertSession(t *testing.T, pool *pgxpool.Pool, scope eventlog.TenantScope, trigger string, parentID *uuid.UUID) uuid.UUID {
	t.Helper()
	id := uuid.New()
	depth := 0
	if parentID != nil {
		depth = 1
	}
	_, err := pool.Exec(t.Context(), `
		INSERT INTO sessions (id, workspace_id, project_id, user_id, agent_id, status, trigger, parent_id, depth)
		SELECT $1, $2, p.id, $3, $4, 'running', $5, $6, $7
		FROM projects p
		WHERE p.workspace_id = $2 AND p.user_id = $3 AND p.name = 'Personal'`,
		id, scope.WorkspaceID, scope.UserID, uuid.New(), trigger, parentID, depth)
	if err != nil {
		t.Fatalf("insert session: %v", err)
	}
	return id
}

func TestActivityStreamChildApprovalReachesRoot(t *testing.T) {
	pool := testdb.NewPool(t)
	repo := eventlog.NewRepo(pool, "fake")
	store := eventlog.NewStore(pool)
	user := testdb.NewUser(t, pool)
	other := testdb.NewUser(t, pool)

	hub := stream.NewHub()
	ctx, cancel := context.WithCancel(t.Context())
	var wg sync.WaitGroup
	t.Cleanup(func() { cancel(); wg.Wait() })
	// Listen resyncs every Activity client once it is LISTENing.
	_, listening, unsubscribe := hub.SubscribeActivity(uuid.New())
	defer unsubscribe()
	metrics := newTestMetrics(t)
	wg.Go(func() {
		stream.Listen(ctx, pool, slog.New(slog.DiscardHandler), hub, stream.NewPGDeltaBus(pool), metrics)
	})
	select {
	case <-listening:
	case <-time.After(5 * time.Second):
		t.Fatal("timed out waiting for Listen to start")
	}

	// Creating the parent notifies too. Wait for that hint to be routed, so it
	// can't surface as a status frame after the handler has subscribed.
	created, _, unsubscribeCreated := hub.SubscribeActivity(user.ID)
	defer unsubscribeCreated()
	parent := uuid.New()
	if _, err := repo.CreateSession(ctx, user.Scope(), parent, uuid.New(), "hi", "", false); err != nil {
		t.Fatal(err)
	}
	select {
	case <-created:
	case <-time.After(5 * time.Second):
		t.Fatal("timed out waiting for the create hint")
	}
	streams := newStreams(t, repo, hub, &fakeDeltaBus{})
	w, stop := serveAs(t, streams.handleActivity, user)
	defer stop()
	w.waitFor(t, `"session_id":"`+parent.String()+`","root_id":"`+parent.String()+`","parent_id":null,"status":"runnable","needs_approval":false}`)

	child := insertSession(t, pool, user.Scope(), eventlog.TriggerUserMessage, &parent)
	tidy := insertSession(t, pool, user.Scope(), "memory_tidy", nil)
	foreign, foreignChat := other.Scope(), uuid.New()
	if _, err := repo.CreateSession(ctx, foreign, foreignChat, uuid.New(), "not mine", "", false); err != nil {
		t.Fatal(err)
	}
	for _, c := range []struct {
		sid uuid.UUID
		to  string
	}{
		{child, eventlog.StatusAwaitingApproval},
		{tidy, eventlog.StatusAwaitingApproval},
		{foreignChat, eventlog.StatusAwaitingUser},
		// Notifications arrive in commit order: once this frame is out,
		// everything before it has been routed.
		{parent, eventlog.StatusAwaitingUser},
	} {
		if _, err := store.Append(ctx, c.sid, nil, nil, &eventlog.StatusChange{To: c.to}); err != nil {
			t.Fatal(err)
		}
	}

	w.waitFor(t, `"session_id":"`+parent.String()+`","root_id":"`+parent.String()+`","parent_id":null,"status":"awaiting_user"`)
	body := w.body()
	statuses := strings.Split(body, "event: status\n")[1:]
	if len(statuses) != 2 {
		t.Fatalf("status frames = %d, want 2 (child approval, parent marker): %q", len(statuses), body)
	}
	wantChild := `data: {"session_id":"` + child.String() + `","root_id":"` + parent.String() + `","parent_id":"` + parent.String() + `","status":"awaiting_approval","needs_approval":true}` + "\n\n"
	if statuses[0] != wantChild {
		t.Errorf("first status frame = %q, want %q", statuses[0], wantChild)
	}
	for _, hidden := range []uuid.UUID{tidy, foreignChat} {
		if strings.Contains(body, hidden.String()) {
			t.Errorf("frames mention %s, which the User must not see: %q", hidden, body)
		}
	}
}
