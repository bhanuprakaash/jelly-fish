package worker_test

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/bhanuprakaash/jelly-fish/internal/eventlog"
	"github.com/bhanuprakaash/jelly-fish/internal/memory"
	"github.com/bhanuprakaash/jelly-fish/internal/msg"
	"github.com/bhanuprakaash/jelly-fish/internal/provider"
	"github.com/bhanuprakaash/jelly-fish/internal/provider/fake"
	"github.com/bhanuprakaash/jelly-fish/internal/testdb"
	"github.com/bhanuprakaash/jelly-fish/internal/tool"
	"github.com/bhanuprakaash/jelly-fish/internal/worker"
)

const needle = "needle-body-7431"

func memCall(id string, args map[string]any) provider.Response {
	raw, err := json.Marshal(args)
	if err != nil {
		panic(err)
	}
	m := msg.AssistantText("on it")
	m.Parts = append(m.Parts, msg.Part{Kind: msg.KindToolUse, ToolUse: &msg.ToolUse{ID: id, Name: memory.ToolName, Args: raw}})
	return provider.Response{Message: m, StopReason: provider.StopReasonToolUse}
}

func create(id, path string) provider.Response {
	return memCall(id, map[string]any{"command": "create", "scope": "user", "path": path, "title": "needle-title-2290", "kind": "fact", "content": needle})
}

func view(id, path string) provider.Response {
	return memCall(id, map[string]any{"command": "view", "scope": "user", "path": path})
}

func queryCount(t *testing.T, pool *pgxpool.Pool, query string, args ...any) int {
	t.Helper()
	var n int
	if err := pool.QueryRow(t.Context(), query, args...).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

func resultText(m msg.Message, i int) string {
	return msg.Message{Parts: m.Parts[i].ToolResult.Parts}.Text()
}

func TestMemoryTextNeverReachesTheEventLog(t *testing.T) {
	pool := testdb.NewPool(t)
	sid, scope := newFakeSession(t, pool)
	p := &replies{list: []provider.Response{create("c1", "/memories/a.md"), view("c2", "/memories/a.md"), done()}}
	startTools(t, pool, p, 10*time.Second, tool.NewRegistry(memory.New(pool)))
	waitStatus(t, pool, sid, eventlog.StatusAwaitingUser, 3)

	if n := queryCount(t, pool, `SELECT count(*) FROM events WHERE session_id = $1 AND (payload::text LIKE '%needle-body%' OR payload::text LIKE '%needle-title%')`, sid); n != 0 {
		t.Fatalf("%d events hold memory text", n)
	}

	var id uuid.UUID
	var content string
	if err := pool.QueryRow(t.Context(), `SELECT id, content FROM memories WHERE user_id = $1`, scope.UserID).Scan(&id, &content); err != nil || content != needle {
		t.Fatalf("memory row: content %q, err %v", content, err)
	}
	evs := loadEvents(t, pool, sid)
	written := ofType(evs, eventlog.TypeMemoryWritten)
	if len(written) != 1 {
		t.Fatalf("memory.written events = %d, want 1", len(written))
	}
	got := written[0].Payload
	if got["memory_id"] != id.String() || got["path"] != "/memories/a.md" || got["op"] != "create" || got["version"] != float64(1) || len(got) != 4 {
		t.Fatalf("memory.written = %v", got)
	}
	var writtenSeq, completedSeq int64
	err := pool.QueryRow(t.Context(), `
		SELECT (SELECT seq FROM events WHERE session_id = $1 AND type = 'memory.written'),
		       (SELECT min(seq) FROM events WHERE session_id = $1 AND type = 'tool.call.completed')`, sid).Scan(&writtenSeq, &completedSeq)
	if err != nil || completedSeq != writtenSeq+1 {
		t.Fatalf("memory.written at seq %d, first completed at %d (%v), want them in one append", writtenSeq, completedSeq, err)
	}

	// The model gets the text back: in its own create call, and in the view.
	last := p.requests()[2].Messages
	args := string(last[1].Parts[1].ToolUse.Args)
	if !strings.Contains(args, needle) || !strings.Contains(args, "needle-title-2290") {
		t.Fatalf("create args sent to the model = %s", args)
	}
	if got := resultText(last[2], 0); got != "created /memories/a.md" {
		t.Fatalf("create result = %q", got)
	}
	if got := resultText(last[4], 0); got != needle {
		t.Fatalf("view result = %q, want the memory text", got)
	}
	assertFoldMatchesRow(t, pool, sid)
}

func TestModelSeesWhatItsEditsWrote(t *testing.T) {
	pool := testdb.NewPool(t)
	sid, _ := newFakeSession(t, pool)
	replace := memCall("c2", map[string]any{"command": "str_replace", "scope": "user", "path": "/memories/a.md", "old_str": "body", "new_str": "edited"})
	insert := memCall("c3", map[string]any{"command": "insert", "scope": "user", "path": "/memories/a.md", "insert_line": 0, "content": "first line"})
	p := &replies{list: []provider.Response{create("c1", "/memories/a.md"), replace, insert, done()}}
	startTools(t, pool, p, 10*time.Second, tool.NewRegistry(memory.New(pool)))
	waitStatus(t, pool, sid, eventlog.StatusAwaitingUser, 4)

	last := p.requests()[3].Messages
	for i, want := range map[int]string{
		4: "It now reads:\nneedle-edited-7431",
		6: "It now reads:\nfirst line\nneedle-edited-7431",
	} {
		parts := last[i].Parts[0].ToolResult.Parts
		if len(parts) != 2 || parts[0].Text != "updated /memories/a.md" || parts[1].Text != want {
			t.Fatalf("result %d = %+v, want the ack then %q", i, parts, want)
		}
	}
}

func TestWriteAfterUntrustedOutputIsPendingUntilTheNextUserMessage(t *testing.T) {
	pool := testdb.NewPool(t)
	sid, scope := newFakeSession(t, pool)
	fetch := fn{id: "f", def: tool.Def{Untrusted: true}, call: func(context.Context) tool.Result { return tool.TextResult("page", false) }}
	p := &replies{list: []provider.Response{
		toolUse("f"), create("c1", "/memories/a.md"), view("c2", "/memories/a.md"), done(),
		create("c3", "/memories/b.md"), done(),
	}}
	startTools(t, pool, p, 10*time.Second, tool.NewRegistry(fetch, memory.New(pool)))
	waitStatus(t, pool, sid, eventlog.StatusAwaitingUser, 4)

	var status string
	var tainted bool
	var reads int
	err := pool.QueryRow(t.Context(), `SELECT status, tainted, read_count FROM memories WHERE path = '/memories/a.md'`).Scan(&status, &tainted, &reads)
	if err != nil || status != "pending_review" || !tainted || reads != 0 {
		t.Fatalf("a.md: %q tainted=%v reads=%d (%v), want pending_review, tainted, unread", status, tainted, reads, err)
	}
	seen := p.requests()[3].Messages
	if got := resultText(seen[len(seen)-1], 0); got != "no memory at /memories/a.md" {
		t.Fatalf("view of a pending memory = %q", got)
	}

	if _, err := eventlog.NewRepo(pool, fake.Name).PostMessage(t.Context(), scope, sid, uuid.New(), "go on"); err != nil {
		t.Fatal(err)
	}
	waitStatus(t, pool, sid, eventlog.StatusAwaitingUser, 6)
	if err := pool.QueryRow(t.Context(), `SELECT status, tainted FROM memories WHERE path = '/memories/b.md'`).Scan(&status, &tainted); err != nil || status != "active" || tainted {
		t.Fatalf("b.md: %q tainted=%v (%v), want active", status, tainted, err)
	}
}

func TestCallWhoseArgsWereLostGetsAnErrorResult(t *testing.T) {
	pool := testdb.NewPool(t)
	sid, scope := newFakeSession(t, pool)

	// The state a Worker leaves when it dies after llm.response: the reply
	// is stored with its text redacted, and the real args are gone.
	reply := create("c1", "/memories/a.md").Message
	args, _ := memory.Redact(reply.Parts[1].ToolUse.Args)
	reply.Parts[1].ToolUse.Args = args
	_, err := eventlog.NewStore(pool).Append(t.Context(), sid, nil, []eventlog.NewEvent{{
		Type: eventlog.TypeLLMResponse, Actor: "test",
		Payload: map[string]any{"message": reply, "stop_reason": provider.StopReasonToolUse, "usage": map[string]int64{}},
	}}, nil)
	if err != nil {
		t.Fatal(err)
	}

	p := &replies{list: []provider.Response{done()}}
	startTools(t, pool, p, 10*time.Second, tool.NewRegistry(memory.New(pool)))
	waitStatus(t, pool, sid, eventlog.StatusAwaitingUser, 2)

	if n := queryCount(t, pool, `SELECT count(*) FROM memories WHERE user_id = $1`, scope.UserID); n != 0 {
		t.Fatalf("%d memories written from redacted args", n)
	}
	seen := p.requests()[0].Messages
	tr := seen[len(seen)-1].Parts[0].ToolResult
	if !tr.IsError || resultText(seen[len(seen)-1], 0) != "arguments lost; call again" {
		t.Fatalf("result = %+v, want the arguments-lost error", tr)
	}
}

func TestDeletedMemoryShowsAsDeletedInAnOlderChat(t *testing.T) {
	pool := testdb.NewPool(t)
	sid, scope := newFakeSession(t, pool)
	p := &replies{list: []provider.Response{create("c1", "/memories/a.md"), view("c2", "/memories/a.md"), done()}}
	startTools(t, pool, p, 10*time.Second, tool.NewRegistry(memory.New(pool)))
	waitStatus(t, pool, sid, eventlog.StatusAwaitingUser, 3)

	if _, err := pool.Exec(t.Context(), `DELETE FROM memories WHERE user_id = $1`, scope.UserID); err != nil {
		t.Fatal(err)
	}
	if n := queryCount(t, pool, `SELECT count(*) FROM memory_revisions`); n != 0 {
		t.Fatalf("%d revisions left", n)
	}
	if _, err := eventlog.NewRepo(pool, fake.Name).PostMessage(t.Context(), scope, sid, uuid.New(), "again"); err != nil {
		t.Fatal(err)
	}
	waitStatus(t, pool, sid, eventlog.StatusAwaitingUser, 4)

	msgs := p.requests()[3].Messages
	if got := resultText(msgs[4], 0); got != "(this memory was deleted)" {
		t.Fatalf("old view result = %q", got)
	}
	if args := string(msgs[1].Parts[1].ToolUse.Args); !strings.Contains(args, `"content":"(this memory was deleted)"`) {
		t.Fatalf("old create args = %s", args)
	}
}

func TestMemoryRowAndEventsCommitTogether(t *testing.T) {
	pool := testdb.NewPool(t)
	sid, scope := newFakeSession(t, pool)
	_, err := pool.Exec(t.Context(), `
		CREATE FUNCTION refuse_completed() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN RAISE EXCEPTION 'refused'; END $$;
		CREATE TRIGGER refuse_completed BEFORE INSERT ON events
		FOR EACH ROW WHEN (NEW.type = 'tool.call.completed') EXECUTE FUNCTION refuse_completed()`)
	if err != nil {
		t.Fatal(err)
	}
	p := &replies{list: []provider.Response{create("c1", "/memories/a.md"), done()}}
	startTools(t, pool, p, 10*time.Second, tool.NewRegistry(memory.New(pool)))
	waitStatus(t, pool, sid, eventlog.StatusFailed, 1)

	if n := queryCount(t, pool, `SELECT count(*) FROM memories WHERE user_id = $1`, scope.UserID) + queryCount(t, pool, `SELECT count(*) FROM memory_revisions`) +
		queryCount(t, pool, `SELECT count(*) FROM events WHERE session_id = $1 AND type = 'memory.written'`, sid); n != 0 {
		t.Fatalf("%d rows survived the failed append", n)
	}
}

func TestFoldListsToolsSinceTheLastUserMessage(t *testing.T) {
	created := ev(t, 1, eventlog.TypeSessionCreated, map[string]any{"agent": map[string]string{"model": "fake"}})
	user := func(seq int64) eventlog.Event {
		return ev(t, seq, eventlog.TypeUserMessage, map[string]any{"message": msg.UserText("hi")})
	}
	reply := ev(t, 3, eventlog.TypeLLMResponse, map[string]any{"message": toolUse("a").Message})
	completed := ev(t, 4, eventlog.TypeToolCompleted, map[string]any{"tool_call_id": "a", "result": msg.Message{MsgV: msg.CurrentVersion, Role: msg.RoleUser, Parts: []msg.Part{
		{Kind: msg.KindToolResult, ToolResult: &msg.ToolResult{CallID: "a"}},
	}}})

	st, err := worker.Fold([]eventlog.Event{created, user(2), reply, completed})
	if err != nil || len(st.ToolsSinceUser) != 1 || st.ToolsSinceUser[0] != "tool-a" {
		t.Fatalf("ToolsSinceUser = %v (%v), want [tool-a]", st.ToolsSinceUser, err)
	}
	st, err = worker.Fold([]eventlog.Event{created, user(2), reply, completed, user(5)})
	if err != nil || len(st.ToolsSinceUser) != 0 {
		t.Fatalf("ToolsSinceUser after a user message = %v (%v), want none", st.ToolsSinceUser, err)
	}
}
