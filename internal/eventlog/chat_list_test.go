package eventlog_test

import (
	"encoding/json"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/bhanuprakaash/jelly-fish/internal/eventlog"
	"github.com/bhanuprakaash/jelly-fish/internal/testdb"
)

func TestCreateSession_RecordsTrigger(t *testing.T) {
	pool := testdb.NewPool(t)
	repo := eventlog.NewRepo(pool, "fake")
	scope := testdb.NewUser(t, pool).Scope()
	sid := uuid.New()
	if _, err := repo.CreateSession(t.Context(), scope, sid, uuid.New(), "hi", "", false); err != nil {
		t.Fatalf("CreateSession: %v", err)
	}

	var col string
	if err := pool.QueryRow(t.Context(), `SELECT trigger FROM sessions WHERE id = $1`, sid).Scan(&col); err != nil {
		t.Fatalf("read trigger column: %v", err)
	}
	if col != "user_message" {
		t.Errorf("sessions.trigger = %q, want user_message", col)
	}

	evs, err := repo.ListEvents(t.Context(), scope, sid, 0)
	if err != nil {
		t.Fatalf("ListEvents: %v", err)
	}
	var created struct {
		Trigger string `json:"trigger"`
	}
	if err := json.Unmarshal(evs[0].Payload, &created); err != nil {
		t.Fatalf("unmarshal session.created: %v", err)
	}
	if created.Trigger != "user_message" {
		t.Errorf("session.created trigger = %q, want user_message", created.Trigger)
	}
}

// insertSessionRow mimics CreateSession's insert for sessions CreateSession
// can't make: System Sessions and child sessions.
func insertSessionRow(t *testing.T, pool *pgxpool.Pool, scope eventlog.TenantScope, trigger string, parentID *uuid.UUID) uuid.UUID {
	t.Helper()
	id := uuid.New()
	depth := 0
	if parentID != nil {
		depth = 1
	}
	_, err := pool.Exec(t.Context(), `
		INSERT INTO sessions (id, workspace_id, project_id, user_id, agent_id, status, trigger, parent_id, depth)
		SELECT $1, $2, p.id, $3, $4, 'completed', $5, $6, $7
		FROM projects p
		WHERE p.workspace_id = $2 AND p.user_id = $3 AND p.name = 'Personal'`,
		id, scope.WorkspaceID, scope.UserID, uuid.New(), trigger, parentID, depth)
	if err != nil {
		t.Fatalf("insert session row: %v", err)
	}
	return id
}

func setUpdatedAt(t *testing.T, pool *pgxpool.Pool, id uuid.UUID, ago time.Duration) {
	t.Helper()
	if _, err := pool.Exec(t.Context(), `UPDATE sessions SET updated_at = now() - $2 * interval '1 second' WHERE id = $1`, id, ago.Seconds()); err != nil {
		t.Fatalf("set updated_at: %v", err)
	}
}

func listIDs(t *testing.T, repo *eventlog.Repo, scope eventlog.TenantScope) []uuid.UUID {
	t.Helper()
	rows, err := repo.ListSessions(t.Context(), scope)
	if err != nil {
		t.Fatalf("ListSessions: %v", err)
	}
	ids := make([]uuid.UUID, len(rows))
	for i, r := range rows {
		ids[i] = r.ID
	}
	return ids
}

func TestListSessions_OnlyTopLevelNonSystem(t *testing.T) {
	pool := testdb.NewPool(t)
	repo := eventlog.NewRepo(pool, "fake")
	scope := testdb.NewUser(t, pool).Scope()
	other := testdb.NewUser(t, pool).Scope()

	a, b, c := uuid.New(), uuid.New(), uuid.New()
	for _, id := range []uuid.UUID{a, b, c} {
		if _, err := repo.CreateSession(t.Context(), scope, id, uuid.New(), "hello", "", false); err != nil {
			t.Fatalf("CreateSession: %v", err)
		}
	}
	insertSessionRow(t, pool, scope, "memory_tidy", nil)
	insertSessionRow(t, pool, scope, eventlog.TriggerUserMessage, &a)
	if _, err := repo.CreateSession(t.Context(), other, uuid.New(), uuid.New(), "not mine", "", false); err != nil {
		t.Fatalf("CreateSession other: %v", err)
	}
	setUpdatedAt(t, pool, a, 3*time.Hour)
	setUpdatedAt(t, pool, b, 2*time.Hour)
	setUpdatedAt(t, pool, c, 1*time.Hour)

	if got, want := listIDs(t, repo, scope), []uuid.UUID{c, b, a}; !slices.Equal(got, want) {
		t.Fatalf("ids = %v, want %v", got, want)
	}

	if _, err := repo.PostMessage(t.Context(), scope, a, uuid.New(), "again"); err != nil {
		t.Fatalf("PostMessage: %v", err)
	}
	if got, want := listIDs(t, repo, scope), []uuid.UUID{a, c, b}; !slices.Equal(got, want) {
		t.Errorf("after activity on the oldest: ids = %v, want %v", got, want)
	}
}

func TestListSessions_EmptyForNewUser(t *testing.T) {
	pool := testdb.NewPool(t)
	repo := eventlog.NewRepo(pool, "fake")
	rows, err := repo.ListSessions(t.Context(), testdb.NewUser(t, pool).Scope())
	if err != nil {
		t.Fatalf("ListSessions: %v", err)
	}
	if len(rows) != 0 {
		t.Errorf("rows = %v, want none", rows)
	}
}

func TestListSessions_PlaceholderFromFirstUserMessage(t *testing.T) {
	tests := []struct {
		name    string
		message string
		title   string // set on the session when non-empty
		want    string
	}{
		{"short as is", "hello there", "", "hello there"},
		{"exactly 60 runes is not cut", strings.Repeat("a", 60), "", strings.Repeat("a", 60)},
		{"long is cut at 60 runes", strings.Repeat("a", 100), "", strings.Repeat("a", 60) + "…"},
		{"whitespace collapsed", "  hello\n\n  big \t world  ", "", "hello big world"},
		{"cut by rune not byte", strings.Repeat("é", 70), "", strings.Repeat("é", 60) + "…"},
		{"Title wins", "hello there", "Trip plan", "Trip plan"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			pool := testdb.NewPool(t)
			repo := eventlog.NewRepo(pool, "fake")
			scope := testdb.NewUser(t, pool).Scope()
			sid := uuid.New()
			if _, err := repo.CreateSession(t.Context(), scope, sid, uuid.New(), tt.message, "", false); err != nil {
				t.Fatalf("CreateSession: %v", err)
			}
			if tt.title != "" {
				if _, err := pool.Exec(t.Context(), `UPDATE sessions SET title = $2 WHERE id = $1`, sid, tt.title); err != nil {
					t.Fatalf("set title: %v", err)
				}
			}
			// The placeholder is the first message's, not the latest's.
			if _, err := repo.PostMessage(t.Context(), scope, sid, uuid.New(), "a follow-up"); err != nil {
				t.Fatalf("PostMessage: %v", err)
			}

			rows, err := repo.ListSessions(t.Context(), scope)
			if err != nil {
				t.Fatalf("ListSessions: %v", err)
			}
			if len(rows) != 1 {
				t.Fatalf("len(rows) = %d, want 1", len(rows))
			}
			if rows[0].Title != tt.want {
				t.Errorf("Title = %q, want %q", rows[0].Title, tt.want)
			}
			if rows[0].Titled != (tt.title != "") {
				t.Errorf("Titled = %v, want %v", rows[0].Titled, tt.title != "")
			}
		})
	}
}
