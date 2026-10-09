package worker_test

import (
	"encoding/json"
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

func TestFoldSendsToolResultsInCallOrder(t *testing.T) {
	m := msg.AssistantText("checking")
	for _, id := range []string{"a", "b"} {
		m.Parts = append(m.Parts, msg.Part{Kind: msg.KindToolUse, ToolUse: &msg.ToolUse{ID: id, Name: "sleep", Args: json.RawMessage(`{}`)}})
	}
	result := func(id, text string) msg.Message {
		return msg.Message{MsgV: msg.CurrentVersion, Role: msg.RoleUser, Parts: []msg.Part{
			{Kind: msg.KindToolResult, ToolResult: &msg.ToolResult{CallID: id, Parts: []msg.Part{{Kind: msg.KindText, Text: text}}}},
		}}
	}
	evs := []eventlog.Event{
		ev(t, 1, eventlog.TypeSessionCreated, map[string]any{"agent": map[string]string{"model": "fake"}}),
		ev(t, 2, eventlog.TypeUserMessage, map[string]any{"message": msg.UserText("hi")}),
		ev(t, 3, eventlog.TypeTurnStarted, map[string]any{"turn_id": "t1", "input_through_seq": 2}),
		ev(t, 4, eventlog.TypeLLMResponse, map[string]any{"turn_id": "t1", "message": m, "stop_reason": "tool_use"}),
		ev(t, 5, eventlog.TypeToolRequested, map[string]any{"tool_call_id": "a", "tool": "sleep", "args": json.RawMessage(`{}`)}),
		ev(t, 6, eventlog.TypeToolRequested, map[string]any{"tool_call_id": "b", "tool": "sleep", "args": json.RawMessage(`{}`)}),
		ev(t, 7, eventlog.TypeToolStarted, map[string]any{"tool_call_id": "a"}),
		ev(t, 8, eventlog.TypeToolStarted, map[string]any{"tool_call_id": "b"}),
		ev(t, 9, eventlog.TypeToolCompleted, map[string]any{"tool_call_id": "b", "result": result("b", "B done")}),
		// Steering sent while the tools ran.
		ev(t, 10, eventlog.TypeUserMessage, map[string]any{"message": msg.UserText("also this")}),
		ev(t, 11, eventlog.TypeToolInterrupted, map[string]any{"tool_call_id": "a", "reason": "worker_lost", "note": "outcome unknown; check before retrying"}),
	}
	st, err := worker.Fold(evs)
	if err != nil {
		t.Fatal(err)
	}
	got, err := json.Marshal(st.Messages[2:])
	if err != nil {
		t.Fatal(err)
	}
	want := `[{"msg_v":2,"role":"user","parts":[` +
		`{"k":"tool_result","tr":{"call_id":"a","parts":[{"k":"text","text":"outcome unknown; check before retrying"}],"is_error":true}},` +
		`{"k":"tool_result","tr":{"call_id":"b","parts":[{"k":"text","text":"B done"}]}}]},` +
		`{"msg_v":2,"role":"user","parts":[{"k":"text","text":"also this"}]}]`
	if string(got) != want {
		t.Fatalf("messages after the reply =\n%s\nwant\n%s", got, want)
	}
}

func TestFoldTagsToolCallsWithTheirTurnsProvider(t *testing.T) {
	reply := func(id string) msg.Message {
		return msg.Message{MsgV: msg.CurrentVersion, Role: msg.RoleAssistant, Parts: []msg.Part{
			{Kind: msg.KindToolUse, ToolUse: &msg.ToolUse{ID: id, Name: "memory", Args: json.RawMessage(`{}`)}},
		}}
	}
	evs := []eventlog.Event{
		ev(t, 1, eventlog.TypeSessionCreated, map[string]any{"agent": map[string]string{"model": "claude-haiku-4-5-20251001"}}),
		ev(t, 2, eventlog.TypeUserMessage, map[string]any{"message": msg.UserText("hi")}),
		ev(t, 3, eventlog.TypeTurnStarted, map[string]any{"turn_id": "t1", "provider": "anthropic", "model": "claude-haiku-4-5-20251001", "input_through_seq": 2}),
		ev(t, 4, eventlog.TypeLLMResponse, map[string]any{"turn_id": "t1", "message": reply("c1"), "stop_reason": "tool_use"}),
		ev(t, 5, eventlog.TypeConfigChanged, map[string]any{"model": "gemini-3.5-flash-lite"}),
		ev(t, 6, eventlog.TypeTurnStarted, map[string]any{"turn_id": "t2", "provider": "gemini", "model": "gemini-3.5-flash-lite", "input_through_seq": 5}),
		ev(t, 7, eventlog.TypeLLMResponse, map[string]any{"turn_id": "t2", "message": reply("c2"), "stop_reason": "tool_use"}),
	}
	st, err := worker.Fold(evs)
	if err != nil {
		t.Fatal(err)
	}
	got := map[string]string{}
	for _, m := range st.Messages {
		for _, p := range m.Parts {
			if p.Kind == msg.KindToolUse {
				got[p.ToolUse.ID] = p.ToolUse.Provider
			}
		}
	}
	if got["c1"] != "anthropic" || got["c2"] != "gemini" || len(got) != 2 {
		t.Fatalf("providers by call = %v, want c1 anthropic and c2 gemini", got)
	}
}
