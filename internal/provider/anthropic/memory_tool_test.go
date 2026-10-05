package anthropic

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"

	"github.com/bhanuprakaash/jelly-fish/internal/memory"
	"github.com/bhanuprakaash/jelly-fish/internal/msg"
	"github.com/bhanuprakaash/jelly-fish/internal/provider"
)

func TestMemoryToolIsSentAsOurOwnFunctionUnchanged(t *testing.T) {
	def := memory.New(nil).Def()
	c, body, _ := sseServer(t, "unknown_stop.sse")
	req := provider.Request{Model: haiku, Messages: []msg.Message{msg.UserText("hi")}, Tools: []provider.ToolSpec{{Name: def.Name, Description: def.Description, Schema: def.Schema}}}
	collect(t, c.Provider("k"), req)

	var sent struct {
		Tools []map[string]any `json:"tools"`
	}
	if err := json.Unmarshal(*body, &sent); err != nil {
		t.Fatal(err)
	}
	var schema any
	if err := json.Unmarshal(def.Schema, &schema); err != nil {
		t.Fatal(err)
	}
	want := map[string]any{"name": "memory", "description": def.Description, "input_schema": schema}
	if len(sent.Tools) != 1 || !reflect.DeepEqual(sent.Tools[0], want) {
		t.Fatalf("sent tools = %v, want exactly %v", sent.Tools, want)
	}
	if strings.Contains(string(*body), "memory_20250818") {
		t.Fatal("request uses the native memory tool")
	}
}
