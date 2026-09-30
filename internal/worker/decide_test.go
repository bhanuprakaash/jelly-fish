package worker_test

import (
	"encoding/json"
	"testing"

	"github.com/bhanuprakaash/jelly-fish/internal/eventlog"
	"github.com/bhanuprakaash/jelly-fish/internal/msg"
	"github.com/bhanuprakaash/jelly-fish/internal/worker"
)

func ev(t *testing.T, seq int64, typ string, payload any) eventlog.Event {
	t.Helper()
	b, err := json.Marshal(payload)
	if err != nil {
		t.Fatal(err)
	}
	return eventlog.Event{Seq: seq, Type: typ, Payload: b}
}

func TestDecide(t *testing.T) {
	created := func() eventlog.Event {
		return ev(t, 1, eventlog.TypeSessionCreated, map[string]any{"agent": map[string]string{"model": "fake"}})
	}
	user := func(seq int64) eventlog.Event {
		return ev(t, seq, eventlog.TypeUserMessage, map[string]any{"message": msg.UserText("hi")})
	}
	claimed := func(seq int64) eventlog.Event {
		return ev(t, seq, eventlog.TypeStatusChanged, map[string]string{"from": "runnable", "to": "running"})
	}
	turn := func(seq, through int64) eventlog.Event {
		return ev(t, seq, eventlog.TypeTurnStarted, map[string]any{"turn_id": "t1", "input_through_seq": through})
	}
	interrupted := func(seq int64) eventlog.Event {
		return ev(t, seq, eventlog.TypeTurnInterrupted, map[string]string{"turn_id": "t1", "reason": "worker_lost"})
	}
	reply := func(seq int64) eventlog.Event {
		return ev(t, seq, eventlog.TypeLLMResponse, map[string]any{"turn_id": "t1", "message": msg.AssistantText("echo: hi"), "stop_reason": "end_turn"})
	}

	tests := []struct {
		name string
		evs  []eventlog.Event
		want worker.Step
	}{
		{"pending user message starts a turn", []eventlog.Event{created(), user(2), claimed(3)}, worker.Step{Kind: worker.StepStartTurn}},
		{"end_turn completes", []eventlog.Event{created(), user(2), claimed(3), turn(4, 3), reply(5)}, worker.Step{Kind: worker.StepComplete}},
		{"message sent during the turn starts another", []eventlog.Event{created(), user(2), claimed(3), turn(4, 3), user(5), reply(6)}, worker.Step{Kind: worker.StepStartTurn}},
		{"open turn is interrupted", []eventlog.Event{created(), user(2), claimed(3), turn(4, 3)}, worker.Step{Kind: worker.StepMarkInterrupted, TurnID: "t1"}},
		{"open turn is interrupted even with a newer message", []eventlog.Event{created(), user(2), claimed(3), turn(4, 3), user(5), claimed(6)}, worker.Step{Kind: worker.StepMarkInterrupted, TurnID: "t1"}},
		{"interrupted turn is re-run", []eventlog.Event{created(), user(2), claimed(3), turn(4, 3), claimed(5), interrupted(6)}, worker.Step{Kind: worker.StepStartTurn}},
		{"turn interrupted twice is still re-run", []eventlog.Event{created(), user(2), claimed(3), turn(4, 3), claimed(5), interrupted(6), turn(7, 3), claimed(8), interrupted(9)}, worker.Step{Kind: worker.StepStartTurn}},
		{"re-run turn that completes parks", []eventlog.Event{created(), user(2), claimed(3), turn(4, 3), claimed(5), interrupted(6), turn(7, 3), reply(8)}, worker.Step{Kind: worker.StepComplete}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			st, err := worker.Fold(tt.evs)
			if err != nil {
				t.Fatalf("Fold: %v", err)
			}
			if got := worker.Decide(st); got != tt.want {
				t.Fatalf("Decide = %+v, want %+v", got, tt.want)
			}
		})
	}
}
