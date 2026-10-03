package worker_test

import (
	"testing"

	"github.com/bhanuprakaash/jelly-fish/internal/eventlog"
	"github.com/bhanuprakaash/jelly-fish/internal/msg"
	"github.com/bhanuprakaash/jelly-fish/internal/worker"
)

func TestFoldCountsRetryableErrorsSinceTheLastUserMessage(t *testing.T) {
	created := ev(t, 1, eventlog.TypeSessionCreated, map[string]any{"agent": map[string]string{"model": "fake"}})
	user := func(seq int64) eventlog.Event {
		return ev(t, seq, eventlog.TypeUserMessage, map[string]any{"message": msg.UserText("hi")})
	}
	turn := ev(t, 3, eventlog.TypeTurnStarted, map[string]any{"turn_id": "t1", "input_through_seq": 2})
	sessionErr := func(seq int64, retryable bool, corr string) eventlog.Event {
		e := ev(t, seq, eventlog.TypeSessionError, map[string]any{"code": "provider_down", "retryable": retryable})
		e.CorrelationID = corr
		return e
	}

	tests := []struct {
		name     string
		evs      []eventlog.Event
		wantStep int
		wantOpen bool
	}{
		{"none", []eventlog.Event{created, user(2)}, 0, false},
		{"one retryable error", []eventlog.Event{created, user(2), turn, sessionErr(4, true, "t1")}, 1, false},
		{"stop-class errors do not count", []eventlog.Event{created, user(2), turn, sessionErr(4, false, "t1")}, 0, false},
		{"a new user message starts over", []eventlog.Event{created, user(2), turn, sessionErr(4, true, "t1"), user(5)}, 0, false},
		{"errors after it count again", []eventlog.Event{created, user(2), turn, sessionErr(4, true, "t1"), user(5), sessionErr(6, true, "t2")}, 1, false},
		{"an uncorrelated error leaves the turn open", []eventlog.Event{created, user(2), turn, sessionErr(4, false, "")}, 0, true},
		{"another turn's error leaves it open", []eventlog.Event{created, user(2), turn, sessionErr(4, true, "t0")}, 1, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			st, err := worker.Fold(tt.evs)
			if err != nil {
				t.Fatal(err)
			}
			if st.RetryStep != tt.wantStep || (st.OpenTurn != nil) != tt.wantOpen {
				t.Fatalf("RetryStep=%d open=%v, want %d and %v", st.RetryStep, st.OpenTurn != nil, tt.wantStep, tt.wantOpen)
			}
		})
	}
}

func TestFoldTakesTheLatestModel(t *testing.T) {
	created := ev(t, 1, eventlog.TypeSessionCreated, map[string]any{"agent": map[string]string{"model": "fake"}})
	changed := func(seq int64, payload map[string]any) eventlog.Event {
		return ev(t, seq, eventlog.TypeConfigChanged, payload)
	}
	tests := []struct {
		name string
		evs  []eventlog.Event
		want string
	}{
		{"created", []eventlog.Event{created}, "fake"},
		{"one change", []eventlog.Event{created, changed(2, map[string]any{"model": "claude-opus-5-5"})}, "claude-opus-5-5"},
		{"latest wins", []eventlog.Event{created, changed(2, map[string]any{"model": "claude-opus-5-5"}), changed(3, map[string]any{"model": "claude-sonnet-5-5"})}, "claude-sonnet-5-5"},
		{"a change without a model keeps it", []eventlog.Event{created, changed(2, map[string]any{"model": "claude-opus-5-5"}), changed(3, map[string]any{"mode": "ask"})}, "claude-opus-5-5"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			st, err := worker.Fold(tt.evs)
			if err != nil {
				t.Fatal(err)
			}
			if st.Model != tt.want {
				t.Fatalf("Model = %q, want %q", st.Model, tt.want)
			}
		})
	}
}

func TestFoldTitleInputs(t *testing.T) {
	created := func(payload map[string]any) eventlog.Event {
		payload["agent"] = map[string]string{"model": "fake"}
		return ev(t, 1, eventlog.TypeSessionCreated, payload)
	}
	user := ev(t, 2, eventlog.TypeUserMessage, map[string]any{"message": msg.UserText("hi")})
	turn := ev(t, 3, eventlog.TypeTurnStarted, map[string]any{"turn_id": "t1", "input_through_seq": 2})
	renamed := ev(t, 4, eventlog.TypeSessionRenamed, map[string]any{"title": "Hi", "by": "user"})

	tests := []struct {
		name        string
		evs         []eventlog.Event
		wantTrigger string
		wantTop     bool
		wantTurns   int
		wantRenamed bool
	}{
		{"a fresh top-level chat", []eventlog.Event{created(map[string]any{"trigger": "user_message"}), user}, "user_message", true, 0, false},
		{"no trigger means a user message", []eventlog.Event{created(map[string]any{}), user}, "user_message", true, 0, false},
		{"a null parent is top level", []eventlog.Event{created(map[string]any{"parent_id": nil}), user}, "user_message", true, 0, false},
		{"a child session", []eventlog.Event{created(map[string]any{"parent_id": "5f1f3c3e-0000-4000-8000-000000000000"}), user}, "user_message", false, 0, false},
		{"a system session", []eventlog.Event{created(map[string]any{"trigger": "memory_tidy"}), user}, "memory_tidy", true, 0, false},
		{"a started turn", []eventlog.Event{created(map[string]any{}), user, turn}, "user_message", true, 1, false},
		{"a rename", []eventlog.Event{created(map[string]any{}), user, renamed}, "user_message", true, 0, true},
		{"no message yet", []eventlog.Event{created(map[string]any{})}, "user_message", true, 0, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			st, err := worker.Fold(tt.evs)
			if err != nil {
				t.Fatal(err)
			}
			if st.Trigger != tt.wantTrigger || st.TopLevel != tt.wantTop || st.TurnsStarted != tt.wantTurns || st.Renamed != tt.wantRenamed {
				t.Fatalf("Trigger=%q TopLevel=%v TurnsStarted=%d Renamed=%v", st.Trigger, st.TopLevel, st.TurnsStarted, st.Renamed)
			}
		})
	}
}
