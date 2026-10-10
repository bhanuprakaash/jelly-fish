package stream

import (
	"strings"
	"testing"

	"github.com/bhanuprakaash/jelly-fish/internal/eventlog"
)

func TestToUI(t *testing.T) {
	tests := []struct {
		name        string
		event       eventlog.Event
		wantSent    bool
		wantPayload string
	}{
		{
			name:        "llm.response drops usage",
			event:       eventlog.Event{Seq: 4, Type: eventlog.TypeLLMResponse, Actor: "worker:w1", Payload: []byte(`{"turn_id":"t1","stop_reason":"end_turn","usage":{"input_tokens":3}}`)},
			wantSent:    true,
			wantPayload: `{"stop_reason":"end_turn","turn_id":"t1"}`,
		},
		{
			name:        "user.message passes through",
			event:       eventlog.Event{Seq: 2, Type: eventlog.TypeUserMessage, Payload: []byte(`{"a":1}`)},
			wantSent:    true,
			wantPayload: `{"a":1}`,
		},
		{
			name:        "session.error drops the message",
			event:       eventlog.Event{Seq: 9, Type: eventlog.TypeSessionError, Payload: []byte(`{"code":"bug","message":"internal text","retryable":false,"request_id":"req_1","turn_id":"t1","extra":1}`)},
			wantSent:    true,
			wantPayload: `{"code":"bug","request_id":"req_1","retryable":false,"turn_id":"t1"}`,
		},
		{
			name:        "session.config_changed keeps only the model",
			event:       eventlog.Event{Seq: 5, Type: eventlog.TypeConfigChanged, Payload: []byte(`{"model":"claude-opus-5-5","snapshot":{"x":1}}`)},
			wantSent:    true,
			wantPayload: `{"model":"claude-opus-5-5"}`,
		},
		{
			name:        "session.config_changed keeps the budget",
			event:       eventlog.Event{Seq: 5, Type: eventlog.TypeConfigChanged, Payload: []byte(`{"budget":{"tokens":600,"cost_micros":0,"turns":3},"snapshot":{"x":1}}`)},
			wantSent:    true,
			wantPayload: `{"budget":{"tokens":600,"cost_micros":0,"turns":3}}`,
		},
		{
			name:        "budget.exceeded keeps dimension, limit and used",
			event:       eventlog.Event{Seq: 11, Type: eventlog.TypeBudgetExceeded, Actor: "worker:w1", Payload: []byte(`{"dimension":"tokens","limit":1000,"used":1200,"other":1}`)},
			wantSent:    true,
			wantPayload: `{"dimension":"tokens","limit":1000,"used":1200}`,
		},
		{
			name:        "approval.requested keeps the approval id, kind and dimension",
			event:       eventlog.Event{Seq: 12, Type: eventlog.TypeApprovalRequested, Actor: "worker:w1", Payload: []byte(`{"approval_id":"a1","kind":"budget","dimension":"tokens","other":1}`)},
			wantSent:    true,
			wantPayload: `{"approval_id":"a1","dimension":"tokens","kind":"budget"}`,
		},
		{
			name:        "approval.resolved drops who answered",
			event:       eventlog.Event{Seq: 13, Type: eventlog.TypeApprovalResolved, Payload: []byte(`{"approval_id":"a1","decision":"allow","by":"user"}`)},
			wantSent:    true,
			wantPayload: `{"approval_id":"a1","decision":"allow"}`,
		},
		{
			name:        "session.error without a request id",
			event:       eventlog.Event{Seq: 9, Type: eventlog.TypeSessionError, Payload: []byte(`{"code":"crash_loop","message":"x","retryable":false}`)},
			wantSent:    true,
			wantPayload: `{"code":"crash_loop","retryable":false}`,
		},
		{
			name:        "timer.set keeps wake_at and reason",
			event:       eventlog.Event{Seq: 10, Type: eventlog.TypeTimerSet, Payload: []byte(`{"wake_at":"2026-10-02T10:00:00Z","reason":"provider_down","other":1}`)},
			wantSent:    true,
			wantPayload: `{"reason":"provider_down","wake_at":"2026-10-02T10:00:00Z"}`,
		},
		{
			name:        "session.renamed passes title and by",
			event:       eventlog.Event{Seq: 6, Type: eventlog.TypeSessionRenamed, Actor: "worker:w1", Payload: []byte(`{"title":"Trip plan","by":"auto"}`)},
			wantSent:    true,
			wantPayload: `{"title":"Trip plan","by":"auto"}`,
		},
		{
			name:        "tool.call.completed passes through",
			event:       eventlog.Event{Seq: 7, Type: eventlog.TypeToolCompleted, Actor: "worker:w1", Payload: []byte(`{"tool_call_id":"c1","is_error":true}`)},
			wantSent:    true,
			wantPayload: `{"tool_call_id":"c1","is_error":true}`,
		},
		{name: "usage.recorded is never sent", event: eventlog.Event{Type: eventlog.TypeUsageRecorded, Payload: []byte(`{}`)}},
		{name: "turn.started is never sent", event: eventlog.Event{Type: eventlog.TypeTurnStarted, Payload: []byte(`{"tools_hash":"x"}`)}},
		{name: "llm.response with a malformed payload is not sent", event: eventlog.Event{Type: eventlog.TypeLLMResponse, Payload: []byte(`not json`)}},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ui, ok := ToUI(tt.event)
			if ok != tt.wantSent {
				t.Fatalf("ToUI sent = %v, want %v", ok, tt.wantSent)
			}
			if !ok {
				return
			}
			if got := string(ui.Payload); got != tt.wantPayload {
				t.Errorf("payload = %s, want %s", got, tt.wantPayload)
			}
			if strings.Contains(string(ui.Payload), "worker:") {
				t.Errorf("payload leaks the actor: %s", ui.Payload)
			}
		})
	}
}
