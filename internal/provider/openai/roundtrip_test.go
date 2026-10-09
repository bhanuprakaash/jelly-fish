package openai

import (
	"encoding/json"
	"fmt"
	"math/rand"
	"reflect"
	"testing"
	"testing/quick"

	"github.com/openai/openai-go/v3/responses"

	"github.com/bhanuprakaash/jelly-fish/internal/msg"
)

// opaque is a random Provider token: printable ASCII and base64 symbols,
// like OpenAI encrypted content and call ids.
type opaque string

func (opaque) Generate(r *rand.Rand, size int) reflect.Value {
	const chars = "ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz0123456789+/=_-"
	b := make([]byte, 1+r.Intn(size*8+1))
	for i := range b {
		b[i] = chars[r.Intn(len(chars))]
	}
	return reflect.ValueOf(opaque(b))
}

// TestOpaqueRoundTrip sends a message to the wire and reads the same items
// back as a response: every Opaque field must come back byte for byte.
func TestOpaqueRoundTrip(t *testing.T) {
	prop := func(enc, callID opaque, summary string) bool {
		s, _ := json.Marshal(summary)
		item := fmt.Sprintf(`{"type":"reasoning","id":"rs_1","summary":[{"type":"summary_text","text":%s}],"encrypted_content":%q}`, s, enc)
		in := msg.Message{MsgV: msg.CurrentVersion, Role: msg.RoleAssistant, Parts: []msg.Part{
			{Kind: msg.KindThinking, Thinking: &msg.Thinking{Text: summary, Provider: Name, Model: sol, Opaque: []byte(item)}},
			{Kind: msg.KindToolUse, ToolUse: &msg.ToolUse{ID: "ours", Name: "f", Args: json.RawMessage(`{}`), Provider: Name, Opaque: []byte(callID)}},
		}}
		input, err := toInput(nil, []msg.Message{in})
		if err != nil {
			t.Log(err)
			return false
		}
		wire, err := json.Marshal(map[string]any{"status": "completed", "output": input})
		if err != nil {
			t.Log(err)
			return false
		}
		var resp responses.Response
		if err := json.Unmarshal(wire, &resp); err != nil {
			t.Log(err)
			return false
		}
		out := fromResponse(resp, sol, func(int) string { return "ours" })
		if len(out.Parts) != 2 {
			t.Logf("parts = %+v", out.Parts)
			return false
		}
		return string(out.Parts[0].Thinking.Opaque) == item &&
			out.Parts[0].Thinking.Text == summary &&
			string(out.Parts[1].ToolUse.Opaque) == string(callID)
	}
	if err := quick.Check(prop, &quick.Config{MaxCount: 500}); err != nil {
		t.Fatal(err)
	}
}
