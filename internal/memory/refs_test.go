package memory_test

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/bhanuprakaash/jelly-fish/internal/memory"
	"github.com/bhanuprakaash/jelly-fish/internal/msg"
	"github.com/bhanuprakaash/jelly-fish/internal/tool"
)

func TestRedactSwapsMemoryTextAndLostSpotsIt(t *testing.T) {
	tests := []struct {
		name, args string
		want       string
		changed    bool
		lost       bool
	}{
		{"create", `{"command":"create","path":"/memories/a.md","title":"T","content":"secret text"}`,
			`{"command":"create","content":"(memory text not stored)","path":"/memories/a.md","title":"(memory text not stored)"}`, true, true},
		{"str_replace", `{"command":"str_replace","old_str":"a","new_str":"b"}`,
			`{"command":"str_replace","new_str":"(memory text not stored)","old_str":"(memory text not stored)"}`, true, true},
		{"view has no text", `{"command":"view","path":"/memories"}`, `{"command":"view","path":"/memories"}`, false, false},
		{"not an object", `"oops secret"`, `{}`, true, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, changed := memory.Redact(json.RawMessage(tt.args))
			if string(got) != tt.want || changed != tt.changed {
				t.Fatalf("Redact = %s, %v; want %s, %v", got, changed, tt.want, tt.changed)
			}
			if lost := memory.Lost(got); lost != tt.lost {
				t.Fatalf("Lost(%s) = %v, want %v", got, lost, tt.lost)
			}
		})
	}
}

func use(id string, args map[string]any) msg.Part {
	raw, _ := json.Marshal(args)
	redacted, _ := memory.Redact(raw)
	return msg.Part{Kind: msg.KindToolUse, ToolUse: &msg.ToolUse{ID: id, Name: memory.ToolName, Args: redacted}}
}

func answer(id string, res tool.Result) msg.Part {
	return msg.Part{Kind: msg.KindToolResult, ToolResult: &msg.ToolResult{CallID: id, Parts: res.Content}}
}

func TestResolveRendersRefsFromRevisionsAndMarksDeletedOnes(t *testing.T) {
	e := newEnv(t)
	createArgs := map[string]any{"command": "create", "scope": "user", "path": "/memories/a.md", "title": "Alpha", "kind": "fact", "content": "alpha body"}
	created, _ := e.run(createArgs)
	e.run(map[string]any{"command": "create", "scope": "project", "path": "/memories/b.md", "title": "Beta", "kind": "fact", "content": "beta body"})
	viewed, _ := e.run(map[string]any{"command": "view", "scope": "user", "path": "/memories/a.md"})
	index, _ := e.run(map[string]any{"command": "view", "path": "/memories"})

	history := []msg.Message{
		{Role: msg.RoleAssistant, Parts: []msg.Part{use("c1", createArgs), use("c2", map[string]any{"command": "view", "scope": "user", "path": "/memories/a.md"}), use("c3", map[string]any{"command": "view", "path": "/memories"})}},
		{Role: msg.RoleUser, Parts: []msg.Part{answer("c1", created), answer("c2", viewed), answer("c3", index)}},
	}

	resolve := func() (args string, results []string) {
		t.Helper()
		out, err := memory.Resolve(t.Context(), e.pool, history)
		if err != nil {
			t.Fatal(err)
		}
		for _, p := range out[1].Parts {
			results = append(results, msg.Message{Parts: p.ToolResult.Parts}.Text())
		}
		return string(out[0].Parts[0].ToolUse.Args), results
	}

	args, results := resolve()
	if !strings.Contains(args, `"content":"alpha body"`) || !strings.Contains(args, `"title":"Alpha"`) {
		t.Fatalf("create args = %s, want the text put back", args)
	}
	wantIndex := "<user_memory>\n/memories/a.md — Alpha\n</user_memory>\n<project_memory>\n/memories/b.md — Beta\n</project_memory>"
	if results[0] != "created /memories/a.md" || results[1] != "alpha body" || results[2] != wantIndex {
		t.Fatalf("results = %q", results)
	}
	if strings.Contains(string(history[0].Parts[0].ToolUse.Args), "alpha body") {
		t.Fatal("Resolve changed its input")
	}

	if _, err := e.pool.Exec(t.Context(), `DELETE FROM memories WHERE path = '/memories/a.md'`); err != nil {
		t.Fatal(err)
	}
	args, results = resolve()
	if !strings.Contains(args, `"content":"(this memory was deleted)"`) {
		t.Fatalf("create args = %s", args)
	}
	wantIndex = "<project_memory>\n/memories/b.md — Beta\n</project_memory>"
	if results[1] != "(this memory was deleted)" || results[2] != wantIndex {
		t.Fatalf("results after delete = %q", results)
	}
}
