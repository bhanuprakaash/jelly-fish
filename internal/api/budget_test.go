package api

import (
	"encoding/json"
	"net/http"
	"testing"

	"github.com/google/uuid"

	"github.com/bhanuprakaash/jelly-fish/internal/eventlog"
	"github.com/bhanuprakaash/jelly-fish/internal/testdb"
)

func TestChangeBudgetHandler(t *testing.T) {
	e := newLoginEnv(t)
	alice := testdb.NewUser(t, e.pool)
	ac := e.signInUser(t, alice)
	chat := e.newChat(t, ac, "hello")
	repo := eventlog.NewRepo(e.pool, "fake")
	budgets := func() []eventlog.Budget {
		evs, err := repo.ListEvents(t.Context(), alice.Scope(), chat, 0)
		if err != nil {
			t.Fatal(err)
		}
		var out []eventlog.Budget
		for _, ev := range evs {
			var p struct{ Budget *eventlog.Budget }
			if err := json.Unmarshal(ev.Payload, &p); err != nil {
				t.Fatal(err)
			}
			if ev.Type == eventlog.TypeConfigChanged && p.Budget != nil {
				out = append(out, *p.Budget)
			}
		}
		return out
	}
	put := func(id uuid.UUID, body any) int {
		return e.do(http.MethodPut, "/api/sessions/"+id.String()+"/budget", body, ac).Code
	}

	for _, tt := range []struct {
		name string
		body any
	}{
		{"zero tokens", map[string]any{"tokens": 0, "cost_micros": 4_000_000, "turns": 50}},
		{"negative turns", map[string]any{"tokens": 1_000_000, "cost_micros": 4_000_000, "turns": -1}},
		{"missing cost_micros", map[string]any{"tokens": 1_000_000, "turns": 50}},
		{"not a number", map[string]any{"tokens": "lots", "cost_micros": 4_000_000, "turns": 50}},
	} {
		t.Run(tt.name, func(t *testing.T) {
			if code := put(chat, tt.body); code != http.StatusBadRequest {
				t.Errorf("status %d, want 400", code)
			}
		})
	}
	if got := budgets(); len(got) != 0 {
		t.Fatalf("budgets = %+v after rejected requests, want none", got)
	}

	if code := put(chat, map[string]any{"tokens": 1_000_000, "cost_micros": 4_000_000, "turns": 50}); code != http.StatusNoContent {
		t.Fatalf("status %d, want 204", code)
	}
	if got, want := budgets(), (eventlog.Budget{Tokens: 1_000_000, CostMicros: 4_000_000, Turns: 50}); len(got) != 1 || got[0] != want {
		t.Fatalf("budgets = %+v, want %+v", got, want)
	}

	if code := put(uuid.New(), map[string]any{"tokens": 1, "cost_micros": 1, "turns": 1}); code != http.StatusNotFound {
		t.Errorf("unknown chat: status %d, want 404", code)
	}
}
