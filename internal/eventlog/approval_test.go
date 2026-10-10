package eventlog_test

import (
	"encoding/json"
	"errors"
	"reflect"
	"testing"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/bhanuprakaash/jelly-fish/internal/eventlog"
	"github.com/bhanuprakaash/jelly-fish/internal/testdb"
)

func parkOnBudget(t *testing.T, pool *pgxpool.Pool, repo *eventlog.Repo, scope eventlog.TenantScope) (sessionID, approvalID uuid.UUID) {
	t.Helper()
	sessionID, approvalID = uuid.New(), uuid.New()
	if _, err := repo.CreateSession(t.Context(), scope, sessionID, uuid.New(), "hello", "", false); err != nil {
		t.Fatalf("CreateSession: %v", err)
	}
	_, err := eventlog.NewStore(pool).Append(t.Context(), sessionID, nil, []eventlog.NewEvent{
		{Type: eventlog.TypeBudgetExceeded, Actor: "worker", Payload: map[string]any{"dimension": "tokens", "limit": 1000, "used": 1200}},
		{Type: eventlog.TypeApprovalRequested, Actor: "worker", Payload: map[string]any{"approval_id": approvalID, "kind": "budget", "dimension": "tokens"}},
	}, &eventlog.StatusChange{To: eventlog.StatusAwaitingApproval, Reason: "budget"})
	if err != nil {
		t.Fatalf("park: %v", err)
	}
	return sessionID, approvalID
}

func sessionState(t *testing.T, pool *pgxpool.Pool, sessionID uuid.UUID) (status string, lastSeq int64) {
	t.Helper()
	if err := pool.QueryRow(t.Context(), `SELECT status, last_seq FROM sessions WHERE id = $1`, sessionID).Scan(&status, &lastSeq); err != nil {
		t.Fatal(err)
	}
	return status, lastSeq
}

func TestResolveApproval_AllowMakesTheSessionRunnable(t *testing.T) {
	pool := testdb.NewPool(t)
	repo := eventlog.NewRepo(pool, "fake")
	scope := testdb.NewUser(t, pool).Scope()
	sessionID, approvalID := parkOnBudget(t, pool, repo, scope)

	if err := repo.ResolveApproval(t.Context(), scope, sessionID, approvalID, eventlog.Answer{Decision: eventlog.DecisionAllow}); err != nil {
		t.Fatalf("ResolveApproval: %v", err)
	}

	evs, err := repo.ListEvents(t.Context(), scope, sessionID, 0)
	if err != nil {
		t.Fatal(err)
	}
	resolved := evs[len(evs)-2]
	want := `{"by": "` + scope.UserID.String() + `", "decision": "allow", "approval_id": "` + approvalID.String() + `"}`
	if resolved.Type != eventlog.TypeApprovalResolved || string(resolved.Payload) != want {
		t.Fatalf("event = %s %s, want approval.resolved %s", resolved.Type, resolved.Payload, want)
	}
	if status, _ := sessionState(t, pool, sessionID); status != eventlog.StatusRunnable {
		t.Fatalf("status = %s, want runnable", status)
	}
}

func TestResolveApproval_DenyParksThenAMessageResumes(t *testing.T) {
	pool := testdb.NewPool(t)
	repo := eventlog.NewRepo(pool, "fake")
	scope := testdb.NewUser(t, pool).Scope()
	sessionID, approvalID := parkOnBudget(t, pool, repo, scope)

	if err := repo.ResolveApproval(t.Context(), scope, sessionID, approvalID, eventlog.Answer{Decision: eventlog.DecisionDeny}); err != nil {
		t.Fatalf("ResolveApproval: %v", err)
	}
	if status, _ := sessionState(t, pool, sessionID); status != eventlog.StatusAwaitingUser {
		t.Fatalf("status after deny = %s, want awaiting_user", status)
	}

	if _, err := repo.PostMessage(t.Context(), scope, sessionID, uuid.New(), "try again"); err != nil {
		t.Fatalf("PostMessage: %v", err)
	}
	if status, _ := sessionState(t, pool, sessionID); status != eventlog.StatusRunnable {
		t.Fatalf("status after a message = %s, want runnable", status)
	}
}

func TestResolveApproval_OnlyTheOpenApprovalCanBeAnswered(t *testing.T) {
	pool := testdb.NewPool(t)
	repo := eventlog.NewRepo(pool, "fake")
	scope := testdb.NewUser(t, pool).Scope()
	sessionID, approvalID := parkOnBudget(t, pool, repo, scope)

	if err := repo.ResolveApproval(t.Context(), scope, sessionID, uuid.New(), eventlog.Answer{Decision: eventlog.DecisionAllow}); !errors.Is(err, eventlog.ErrNoOpenApproval) {
		t.Fatalf("unknown approval: err = %v, want ErrNoOpenApproval", err)
	}
	if status, _ := sessionState(t, pool, sessionID); status != eventlog.StatusAwaitingApproval {
		t.Fatalf("status after refused answers = %s, want awaiting_approval", status)
	}

	if err := repo.ResolveApproval(t.Context(), scope, sessionID, approvalID, eventlog.Answer{Decision: eventlog.DecisionAllow}); err != nil {
		t.Fatalf("first answer: %v", err)
	}
	_, seq := sessionState(t, pool, sessionID)
	if err := repo.ResolveApproval(t.Context(), scope, sessionID, approvalID, eventlog.Answer{Decision: eventlog.DecisionAllow}); !errors.Is(err, eventlog.ErrNoOpenApproval) {
		t.Fatalf("second answer: err = %v, want ErrNoOpenApproval", err)
	}
	if _, got := sessionState(t, pool, sessionID); got != seq {
		t.Fatalf("last_seq = %d after a refused answer, want %d", got, seq)
	}
}

func TestResolveApproval_SessionNotAwaitingApprovalIsRefused(t *testing.T) {
	pool := testdb.NewPool(t)
	repo := eventlog.NewRepo(pool, "fake")
	scope := testdb.NewUser(t, pool).Scope()
	sessionID := uuid.New()
	if _, err := repo.CreateSession(t.Context(), scope, sessionID, uuid.New(), "hello", "", false); err != nil {
		t.Fatal(err)
	}
	_, seq := sessionState(t, pool, sessionID)

	if err := repo.ResolveApproval(t.Context(), scope, sessionID, uuid.New(), eventlog.Answer{Decision: eventlog.DecisionAllow}); !errors.Is(err, eventlog.ErrNoOpenApproval) {
		t.Fatalf("err = %v, want ErrNoOpenApproval", err)
	}
	if _, got := sessionState(t, pool, sessionID); got != seq {
		t.Fatalf("last_seq = %d, want %d", got, seq)
	}
	other := testdb.NewUser(t, pool).Scope()
	if err := repo.ResolveApproval(t.Context(), other, sessionID, uuid.New(), eventlog.Answer{Decision: eventlog.DecisionAllow}); !errors.Is(err, eventlog.ErrNotFound) {
		t.Fatalf("other tenant: err = %v, want ErrNotFound", err)
	}
}

func TestResolveApproval_ToolAnswerKeepsItsReasonAndAllAndResumesTheSession(t *testing.T) {
	tests := []struct {
		name   string
		answer eventlog.Answer
		want   map[string]any
	}{
		{"deny with a reason", eventlog.Answer{Decision: eventlog.DecisionDeny, Reason: "no"}, map[string]any{"decision": "deny", "reason": "no"}},
		{"allow all", eventlog.Answer{Decision: eventlog.DecisionAllow, All: true}, map[string]any{"decision": "allow", "all": true}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			pool := testdb.NewPool(t)
			repo := eventlog.NewRepo(pool, "fake")
			scope := testdb.NewUser(t, pool).Scope()
			sessionID, approvalID := uuid.New(), uuid.New()
			if _, err := repo.CreateSession(t.Context(), scope, sessionID, uuid.New(), "hello", "", false); err != nil {
				t.Fatal(err)
			}
			_, err := eventlog.NewStore(pool).Append(t.Context(), sessionID, nil, []eventlog.NewEvent{
				{Type: eventlog.TypeApprovalRequested, Actor: "worker", Payload: map[string]any{"approval_id": approvalID, "kind": "tool", "tool_call_id": "c1"}},
			}, &eventlog.StatusChange{To: eventlog.StatusAwaitingApproval, Reason: "tool"})
			if err != nil {
				t.Fatal(err)
			}

			if err := repo.ResolveApproval(t.Context(), scope, sessionID, approvalID, tt.answer); err != nil {
				t.Fatal(err)
			}

			evs, err := repo.ListEvents(t.Context(), scope, sessionID, 0)
			if err != nil {
				t.Fatal(err)
			}
			resolved := evs[len(evs)-2]
			var got map[string]any
			if err := json.Unmarshal(resolved.Payload, &got); err != nil {
				t.Fatal(err)
			}
			tt.want["approval_id"], tt.want["by"], tt.want["scope"] = approvalID.String(), scope.UserID.String(), "once"
			if resolved.Type != eventlog.TypeApprovalResolved || !reflect.DeepEqual(got, tt.want) {
				t.Fatalf("event = %s %v, want approval.resolved %v", resolved.Type, got, tt.want)
			}
			if status, _ := sessionState(t, pool, sessionID); status != eventlog.StatusRunnable {
				t.Fatalf("status = %s, want runnable", status)
			}
		})
	}
}
