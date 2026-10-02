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
