package eventlog_test

import (
	"encoding/json"
	"errors"
	"testing"

	"github.com/google/uuid"

	"github.com/bhanuprakaash/jelly-fish/internal/eventlog"
	"github.com/bhanuprakaash/jelly-fish/internal/testdb"
)

func TestResolveElicitation_OnlyTheOpenOneCanBeAnsweredAndItResumesTheSession(t *testing.T) {
	pool := testdb.NewPool(t)
	repo := eventlog.NewRepo(pool, "fake")
	scope := testdb.NewUser(t, pool).Scope()
	sessionID, elicitationID := uuid.New(), uuid.New()
	if _, err := repo.CreateSession(t.Context(), scope, sessionID, uuid.New(), "hello", "", false); err != nil {
		t.Fatal(err)
	}
	answer := eventlog.ElicitationAnswer{Action: eventlog.ActionAccept, Content: json.RawMessage(`{"confirm":true}`)}

	if err := repo.ResolveElicitation(t.Context(), scope, sessionID, elicitationID, answer); !errors.Is(err, eventlog.ErrNoOpenElicitation) {
		t.Fatalf("no elicitation: err = %v, want ErrNoOpenElicitation", err)
	}

	_, err := eventlog.NewStore(pool).Append(t.Context(), sessionID, nil, []eventlog.NewEvent{{
		Type: eventlog.TypeElicitationRequested, Actor: "worker",
		Payload: map[string]any{"elicitation_id": elicitationID, "tool_call_id": "c1", "requests": map[string]any{"confirm": map[string]any{"mode": "form", "message": "Proceed?"}}, "request_state": "state-1"},
	}}, &eventlog.StatusChange{To: eventlog.StatusAwaitingUser, Reason: "elicitation"})
	if err != nil {
		t.Fatal(err)
	}
	if err := repo.ResolveElicitation(t.Context(), scope, sessionID, uuid.New(), answer); !errors.Is(err, eventlog.ErrNoOpenElicitation) {
		t.Fatalf("unknown elicitation: err = %v, want ErrNoOpenElicitation", err)
	}

	if err := repo.ResolveElicitation(t.Context(), scope, sessionID, elicitationID, answer); err != nil {
		t.Fatalf("ResolveElicitation: %v", err)
	}
	evs, err := repo.ListEvents(t.Context(), scope, sessionID, 0)
	if err != nil {
		t.Fatal(err)
	}
	resolved := evs[len(evs)-2]
	want := `{"by": "` + scope.UserID.String() + `", "action": "accept", "content": {"confirm": true}, "tool_call_id": "c1", "elicitation_id": "` + elicitationID.String() + `"}`
	if resolved.Type != eventlog.TypeElicitationResolved || string(resolved.Payload) != want {
		t.Fatalf("event = %s %s, want elicitation.resolved %s", resolved.Type, resolved.Payload, want)
	}
	if status, _ := sessionState(t, pool, sessionID); status != eventlog.StatusRunnable {
		t.Fatalf("status = %s, want runnable", status)
	}
	if err := repo.ResolveElicitation(t.Context(), scope, sessionID, elicitationID, answer); !errors.Is(err, eventlog.ErrNoOpenElicitation) {
		t.Fatalf("second answer: err = %v, want ErrNoOpenElicitation", err)
	}
}
