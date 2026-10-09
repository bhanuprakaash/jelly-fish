package gemini

import (
	"bytes"
	"encoding/json"
	"testing"
	"testing/quick"

	"google.golang.org/genai"

	"github.com/bhanuprakaash/jelly-fish/internal/msg"
	"github.com/bhanuprakaash/jelly-fish/internal/provider"
)

// TestOpaqueRoundTrip sends a message to the wire as JSON and reads the same
// parts back as a streamed reply: every Opaque field must come back byte for
// byte. Signatures hold any bytes; call ids are text.
func TestOpaqueRoundTrip(t *testing.T) {
	prop := func(textSig, callSig []byte, callID, summary string) bool {
		if len(textSig) == 0 || len(callSig) == 0 || callID == "" {
			return true
		}
		in := msg.Message{MsgV: msg.CurrentVersion, Role: msg.RoleAssistant, Parts: []msg.Part{
			{Kind: msg.KindThinking, Thinking: &msg.Thinking{Text: summary, Provider: Name, Model: flash}},
			{Kind: msg.KindThinking, Thinking: &msg.Thinking{Provider: Name, Model: flash, Opaque: textSig}},
			{Kind: msg.KindText, Text: "hi"},
			{Kind: msg.KindThinking, Thinking: &msg.Thinking{Provider: Name, Model: flash, Opaque: callSig}},
			{Kind: msg.KindToolUse, ToolUse: &msg.ToolUse{ID: "ours", Name: "f", Args: json.RawMessage(`{"a":1}`), Provider: Name, Opaque: []byte(callID)}},
		}}
		contents, err := toContents([]msg.Message{in})
		if err != nil {
			t.Log(err)
			return false
		}
		wire, err := json.Marshal(contents)
		if err != nil {
			t.Log(err)
			return false
		}
		var back []*genai.Content
		if err := json.Unmarshal(wire, &back); err != nil || len(back) != 1 {
			t.Log(err, back)
			return false
		}
		r := reply{model: flash, onDelta: func(provider.Delta) {}}
		for _, p := range back[0].Parts {
			r.add(p)
		}
		if len(r.parts) != 4 {
			t.Logf("parts = %+v", r.parts)
			return false
		}
		return bytes.Equal(r.parts[0].Thinking.Opaque, textSig) &&
			r.parts[1].Text == "hi" &&
			bytes.Equal(r.parts[2].Thinking.Opaque, callSig) &&
			string(r.parts[3].ToolUse.Opaque) == callID
	}
	if err := quick.Check(prop, &quick.Config{MaxCount: 500}); err != nil {
		t.Fatal(err)
	}
}
