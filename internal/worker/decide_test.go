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

	toolReply := func(seq int64, stop string) eventlog.Event {
		m := msg.AssistantText("checking")
		for _, id := range []string{"a", "b"} {
			m.Parts = append(m.Parts, msg.Part{Kind: msg.KindToolUse, ToolUse: &msg.ToolUse{ID: id, Name: "sleep", Args: json.RawMessage(`{}`)}})
		}
		return ev(t, seq, eventlog.TypeLLMResponse, map[string]any{"turn_id": "t1", "message": m, "stop_reason": stop})
	}
	requested := func(seq int64, id string) eventlog.Event {
		return ev(t, seq, eventlog.TypeToolRequested, map[string]any{"tool_call_id": id, "tool": "sleep", "args": json.RawMessage(`{}`)})
	}
	asking := func(seq int64, id string) eventlog.Event {
		return ev(t, seq, eventlog.TypeToolRequested, map[string]any{"tool_call_id": id, "tool": "sleep", "args": json.RawMessage(`{}`), "ask": true})
	}
	card := func(seq int64, id string) eventlog.Event {
		return ev(t, seq, eventlog.TypeApprovalRequested, map[string]any{"approval_id": "ap-" + id, "kind": "tool", "tool_call_id": id})
	}
	answer := func(seq int64, id, decision string) eventlog.Event {
		return ev(t, seq, eventlog.TypeApprovalResolved, map[string]any{"approval_id": "ap-" + id, "decision": decision})
	}
	started := func(seq int64, id string) eventlog.Event {
		return ev(t, seq, eventlog.TypeToolStarted, map[string]any{"tool_call_id": id})
	}
	completed := func(seq int64, id string) eventlog.Event {
		return ev(t, seq, eventlog.TypeToolCompleted, map[string]any{"tool_call_id": id, "result": msg.Message{MsgV: msg.CurrentVersion, Role: msg.RoleUser, Parts: []msg.Part{
			{Kind: msg.KindToolResult, ToolResult: &msg.ToolResult{CallID: id, Parts: []msg.Part{{Kind: msg.KindText, Text: "ok"}}}},
		}}})
	}
	toolInterrupted := func(seq int64, id string) eventlog.Event {
		return ev(t, seq, eventlog.TypeToolInterrupted, map[string]any{"tool_call_id": id, "reason": "worker_lost", "note": "outcome unknown; check before retrying"})
	}
	asked := []eventlog.Event{created(), user(2), claimed(3), turn(4, 3), toolReply(5, "tool_use")}

	tests := []struct {
		name string
		evs  []eventlog.Event
		want worker.Step
	}{
		{"tool_use requests the tools", asked, worker.Step{Kind: worker.StepRequestTools}},
		{"requested tools start", append(asked[:5:5], requested(6, "a"), requested(7, "b")), worker.Step{Kind: worker.StepStartTool}},
		{"a call that asks is asked about before anything starts", append(asked[:5:5], asking(6, "a"), asking(7, "b")), worker.Step{Kind: worker.StepRequestApproval}},
		{"the next call that asks is asked about after an allow", append(asked[:5:5], asking(6, "a"), asking(7, "b"), card(8, "a"), answer(9, "a", "allow")), worker.Step{Kind: worker.StepRequestApproval}},
		{"the batch starts once every call that asks is allowed", append(asked[:5:5], asking(6, "a"), asking(7, "b"), card(8, "a"), answer(9, "a", "allow"), card(10, "b"), answer(11, "b", "allow")), worker.Step{Kind: worker.StepStartTool}},
		{"a denied call is closed before anything starts", append(asked[:5:5], requested(6, "a"), asking(7, "b"), card(8, "b"), answer(9, "b", "deny")), worker.Step{Kind: worker.StepFinishDenied}},
		{"a requested tool starts before a waiting message", append(asked[:5:5], requested(6, "a"), requested(7, "b"), user(8)), worker.Step{Kind: worker.StepStartTool}},
		{"a started tool with no result is interrupted", append(asked[:5:5], requested(6, "a"), requested(7, "b"), started(8, "a")), worker.Step{Kind: worker.StepMarkToolsInterrupted}},
		{"the rest start after one completes", append(asked[:5:5], requested(6, "a"), requested(7, "b"), started(8, "a"), completed(9, "a")), worker.Step{Kind: worker.StepStartTool}},
		{"all results in starts a turn", append(asked[:5:5], requested(6, "a"), requested(7, "b"), started(8, "a"), started(9, "b"), completed(10, "b"), completed(11, "a")), worker.Step{Kind: worker.StepStartTurn}},
		{"an interrupted tool counts as a result", append(asked[:5:5], requested(6, "a"), requested(7, "b"), started(8, "a"), started(9, "b"), completed(10, "b"), claimed(11), toolInterrupted(12, "a")), worker.Step{Kind: worker.StepStartTurn}},
		{"the next turn's reply completes", append(asked[:5:5], requested(6, "a"), requested(7, "b"), started(8, "a"), started(9, "b"), completed(10, "b"), completed(11, "a"), turn(12, 11), reply(13)), worker.Step{Kind: worker.StepComplete}},
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
