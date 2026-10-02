package msg_test

import (
	"encoding/json"
	"errors"
	"testing"

	"github.com/bhanuprakaash/jelly-fish/internal/msg"
)

func TestV1UpcastsByteIdentically(t *testing.T) {
	tests := []struct{ name, v1, want string }{
		{
			"user text",
			`{"msg_v":1,"role":"user","parts":[{"type":"text","text":"hi there"}]}`,
			`{"msg_v":2,"role":"user","parts":[{"k":"text","text":"hi there"}]}`,
		},
		{
			"assistant, two parts",
			`{"msg_v":1,"role":"assistant","parts":[{"type":"text","text":"a"},{"type":"text","text":"b"}]}`,
			`{"msg_v":2,"role":"assistant","parts":[{"k":"text","text":"a"},{"k":"text","text":"b"}]}`,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			var m msg.Message
			if err := json.Unmarshal([]byte(tc.v1), &m); err != nil {
				t.Fatal(err)
			}
			got, err := json.Marshal(m)
			if err != nil {
				t.Fatal(err)
			}
			if string(got) != tc.want {
				t.Fatalf("upcast v1 =\n%s\nwant\n%s", got, tc.want)
			}
		})
	}
}

func TestCurrentMessageRoundTrips(t *testing.T) {
	m := msg.Message{MsgV: msg.CurrentVersion, Role: msg.RoleAssistant, Parts: []msg.Part{
		{Kind: msg.KindThinking, Thinking: &msg.Thinking{Text: "hmm", Provider: "anthropic", Model: "m", Opaque: []byte("sig")}},
		{Kind: msg.KindText, Text: "hello"},
		{Kind: msg.KindToolUse, ToolUse: &msg.ToolUse{ID: "c1", Name: "search", Args: json.RawMessage(`{"q":"x"}`), Opaque: []byte("toolu_1")}},
		{Kind: msg.KindToolResult, ToolResult: &msg.ToolResult{CallID: "c1", Parts: []msg.Part{{Kind: msg.KindText, Text: "found"}}, IsError: true}},
		{Kind: msg.KindNative, Native: &msg.Native{Provider: "anthropic", Type: "redacted_thinking", Raw: json.RawMessage(`{"data":"x"}`)}},
	}}
	b, err := json.Marshal(m)
	if err != nil {
		t.Fatal(err)
	}
	var back msg.Message
	if err := json.Unmarshal(b, &back); err != nil {
		t.Fatal(err)
	}
	again, err := json.Marshal(back)
	if err != nil {
		t.Fatal(err)
	}
	if string(again) != string(b) {
		t.Fatalf("round trip =\n%s\nwant\n%s", again, b)
	}
}

func TestUnreadableMessage(t *testing.T) {
	tests := []struct{ name, in string }{
		{"newer msg_v", `{"msg_v":3,"role":"user","parts":[{"k":"text","text":"x"}]}`},
		{"unknown kind", `{"msg_v":2,"role":"user","parts":[{"k":"hologram"}]}`},
		{"unknown kind inside a tool result", `{"msg_v":2,"role":"user","parts":[{"k":"tool_result","tr":{"call_id":"c1","parts":[{"k":"hologram"}]}}]}`},
		{"unknown v1 type", `{"msg_v":1,"role":"user","parts":[{"type":"hologram"}]}`},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			var m msg.Message
			err := json.Unmarshal([]byte(tc.in), &m)
			if !errors.Is(err, msg.ErrUnsupported) {
				t.Fatalf("err = %v, want ErrUnsupported", err)
			}
		})
	}
}

func TestNewMessagesCarryCurrentVersion(t *testing.T) {
	b, err := json.Marshal(msg.UserText("hi"))
	if err != nil {
		t.Fatal(err)
	}
	if want := `{"msg_v":2,"role":"user","parts":[{"k":"text","text":"hi"}]}`; string(b) != want {
		t.Fatalf("UserText = %s, want %s", b, want)
	}
}
