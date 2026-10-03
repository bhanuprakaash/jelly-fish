package eventlog_test

import (
	"slices"
	"testing"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/bhanuprakaash/jelly-fish/internal/eventlog"
	"github.com/bhanuprakaash/jelly-fish/internal/testdb"
)

func setStatus(t *testing.T, pool *pgxpool.Pool, id uuid.UUID, status string) {
	t.Helper()
	if _, err := pool.Exec(t.Context(), `UPDATE sessions SET status = $2 WHERE id = $1`, id, status); err != nil {
		t.Fatalf("set status: %v", err)
	}
}

func TestActivitySnapshot_BusyNonSystemOwnOnly(t *testing.T) {
	pool := testdb.NewPool(t)
	repo := eventlog.NewRepo(pool, "fake")
	scope := testdb.NewUser(t, pool).Scope()
	other := testdb.NewUser(t, pool).Scope()

	chat := func(status string) uuid.UUID {
		t.Helper()
		id := uuid.New()
		if _, err := repo.CreateSession(t.Context(), scope, id, uuid.New(), "hi", ""); err != nil {
			t.Fatalf("CreateSession: %v", err)
		}
		setStatus(t, pool, id, status)
		return id
	}
	running := chat(eventlog.StatusRunning)
	chat(eventlog.StatusAwaitingUser)
	chat(eventlog.StatusCompleted)
	failed := chat(eventlog.StatusFailed)
	runnable := chat(eventlog.StatusRunnable)

	child := insertSessionRow(t, pool, scope, eventlog.TriggerUserMessage, &runnable)
	setStatus(t, pool, child, eventlog.StatusAwaitingApproval)
	tidy := insertSessionRow(t, pool, scope, "memory_tidy", nil)
	setStatus(t, pool, tidy, eventlog.StatusRunnable)
	if _, err := repo.CreateSession(t.Context(), other, uuid.New(), uuid.New(), "not mine", ""); err != nil {
		t.Fatalf("CreateSession other: %v", err)
	}

	got, err := repo.ActivitySnapshot(t.Context(), scope)
	if err != nil {
		t.Fatalf("ActivitySnapshot: %v", err)
	}
	want := []eventlog.ActivityRow{
		{SessionID: running, RootID: running, Status: eventlog.StatusRunning},
		{SessionID: failed, RootID: failed, Status: eventlog.StatusFailed},
		{SessionID: runnable, RootID: runnable, Status: eventlog.StatusRunnable},
		{SessionID: child, RootID: runnable, ParentID: &runnable, Status: eventlog.StatusAwaitingApproval},
	}
	if !slices.EqualFunc(got, want, func(a, b eventlog.ActivityRow) bool {
		return a.SessionID == b.SessionID && a.RootID == b.RootID && a.Status == b.Status &&
			(a.ParentID == nil) == (b.ParentID == nil) && (a.ParentID == nil || *a.ParentID == *b.ParentID)
	}) {
		t.Fatalf("snapshot = %+v, want %+v", got, want)
	}
}

func TestActivityStatusOf_RootID(t *testing.T) {
	pool := testdb.NewPool(t)
	repo := eventlog.NewRepo(pool, "fake")
	scope := testdb.NewUser(t, pool).Scope()
	root := uuid.New()
	if _, err := repo.CreateSession(t.Context(), scope, root, uuid.New(), "hi", ""); err != nil {
		t.Fatal(err)
	}
	child := insertSessionRow(t, pool, scope, eventlog.TriggerUserMessage, &root)
	grandchild := insertSessionRow(t, pool, scope, eventlog.TriggerUserMessage, &child)

	tests := []struct {
		name       string
		sid        uuid.UUID
		wantParent *uuid.UUID
	}{
		{"root", root, nil},
		{"child", child, &root},
		{"grandchild", grandchild, &child},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Completed rows are visible too: a status frame must be able to
			// clear a badge.
			got, ok, err := repo.ActivityStatusOf(t.Context(), scope, tt.sid)
			if err != nil || !ok {
				t.Fatalf("ActivityStatusOf: ok=%v err=%v", ok, err)
			}
			if got.SessionID != tt.sid || got.RootID != root {
				t.Errorf("row = %+v, want session %s under root %s", got, tt.sid, root)
			}
			if (got.ParentID == nil) != (tt.wantParent == nil) || (got.ParentID != nil && *got.ParentID != *tt.wantParent) {
				t.Errorf("parent = %v, want %v", got.ParentID, tt.wantParent)
			}
		})
	}
}

func TestActivityStatusOf_HiddenCases(t *testing.T) {
	pool := testdb.NewPool(t)
	repo := eventlog.NewRepo(pool, "fake")
	scope := testdb.NewUser(t, pool).Scope()
	other := testdb.NewUser(t, pool).Scope()
	mine := uuid.New()
	if _, err := repo.CreateSession(t.Context(), scope, mine, uuid.New(), "hi", ""); err != nil {
		t.Fatal(err)
	}
	tidy := insertSessionRow(t, pool, scope, "memory_tidy", nil)

	tests := []struct {
		name  string
		scope eventlog.TenantScope
		sid   uuid.UUID
	}{
		{"system session", scope, tidy},
		{"another tenant's session", other, mine},
		{"missing session", scope, uuid.New()},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if _, ok, err := repo.ActivityStatusOf(t.Context(), tt.scope, tt.sid); err != nil || ok {
				t.Fatalf("ActivityStatusOf: ok=%v err=%v, want hidden", ok, err)
			}
		})
	}
}

func TestActivityStatusOf_ChildOfSystemSessionHidden(t *testing.T) {
	pool := testdb.NewPool(t)
	repo := eventlog.NewRepo(pool, "fake")
	scope := testdb.NewUser(t, pool).Scope()
	tidy := insertSessionRow(t, pool, scope, "memory_tidy", nil)
	child := insertSessionRow(t, pool, scope, eventlog.TriggerUserMessage, &tidy)
	setStatus(t, pool, child, eventlog.StatusRunning)

	if _, ok, err := repo.ActivityStatusOf(t.Context(), scope, child); err != nil || ok {
		t.Fatalf("ActivityStatusOf: ok=%v err=%v, want hidden", ok, err)
	}
	snap, err := repo.ActivitySnapshot(t.Context(), scope)
	if err != nil || len(snap) != 0 {
		t.Fatalf("snapshot = %+v err=%v, want empty", snap, err)
	}
}
