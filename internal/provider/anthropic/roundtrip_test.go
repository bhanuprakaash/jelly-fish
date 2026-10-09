package anthropic

import (
	"encoding/json"
	"fmt"
	"math/rand"
	"reflect"
	"testing"
	"testing/quick"

	sdk "github.com/anthropics/anthropic-sdk-go"

	"github.com/bhanuprakaash/jelly-fish/internal/msg"
)

// opaque is a random Provider token: printable ASCII and base64 symbols,
// like Anthropic signatures, ids and redacted data.
type opaque string

func (opaque) Generate(r *rand.Rand, size int) reflect.Value {
	const chars = "ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz0123456789+/=_-"
	b := make([]byte, 1+r.Intn(size*8+1))
	for i := range b {
		b[i] = chars[r.Intn(len(chars))]
	}
	return reflect.ValueOf(opaque(b))
}

// TestOpaqueRoundTrip sends a message to the wire and reads the same blocks
// back as a response: every Opaque field must come back byte for byte.
func TestOpaqueRoundTrip(t *testing.T) {
	prop := func(sig, data, callID opaque, text string) bool {
		in := msg.Message{MsgV: msg.CurrentVersion, Role: msg.RoleAssistant, Parts: []msg.Part{
			{Kind: msg.KindThinking, Thinking: &msg.Thinking{Text: text, Provider: Name, Model: opus, Opaque: []byte(sig)}},
			{Kind: msg.KindNative, Native: &msg.Native{Provider: Name, Type: "redacted_thinking", Raw: json.RawMessage(fmt.Sprintf(`{"type":"redacted_thinking","data":%q}`, data))}},
			{Kind: msg.KindToolUse, ToolUse: &msg.ToolUse{ID: "ours", Name: "f", Args: json.RawMessage(`{}`), Provider: Name, Opaque: []byte(callID)}},
		}}
		params, _, err := toParams([]msg.Message{in})
		if err != nil {
			t.Log(err)
			return false
		}
		wire, err := json.Marshal(map[string]any{"role": "assistant", "content": params[0].Content})
		if err != nil {
			t.Log(err)
			return false
		}
		var resp sdk.Message
		if err := json.Unmarshal(wire, &resp); err != nil {
			t.Log(err)
			return false
		}
		out := fromMessage(resp, opus, func(int) string { return "ours" })
		if len(out.Parts) != 3 {
			t.Logf("parts = %+v", out.Parts)
			return false
		}
		return string(out.Parts[0].Thinking.Opaque) == string(sig) &&
			out.Parts[0].Thinking.Text == text &&
			string(out.Parts[1].Native.Raw) == string(in.Parts[1].Native.Raw) &&
			string(out.Parts[2].ToolUse.Opaque) == string(callID)
	}
	if err := quick.Check(prop, &quick.Config{MaxCount: 500}); err != nil {
		t.Fatal(err)
	}
}
