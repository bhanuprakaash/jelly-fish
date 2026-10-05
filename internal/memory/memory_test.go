package memory_test

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/bhanuprakaash/jelly-fish/internal/eventlog"
	"github.com/bhanuprakaash/jelly-fish/internal/memory"
	"github.com/bhanuprakaash/jelly-fish/internal/msg"
	"github.com/bhanuprakaash/jelly-fish/internal/provider/fake"
	"github.com/bhanuprakaash/jelly-fish/internal/testdb"
	"github.com/bhanuprakaash/jelly-fish/internal/tool"
)

type env struct {
	t    *testing.T
	pool *pgxpool.Pool
	sess tool.Session
	tool memory.Tool
}

func newEnv(t *testing.T) env {
	t.Helper()
	pool := testdb.NewPool(t)
	u := testdb.NewUser(t, pool)
	sid := uuid.New()
	if _, err := eventlog.NewRepo(pool, fake.Name).CreateSession(t.Context(), u.Scope(), sid, uuid.New(), "hi", ""); err != nil {
		t.Fatal(err)
	}
	sess := tool.Session{ID: sid, UserID: u.ID, WorkspaceID: u.WorkspaceID}
	if err := pool.QueryRow(t.Context(), `SELECT project_id FROM sessions WHERE id = $1`, sid).Scan(&sess.ProjectID); err != nil {
		t.Fatal(err)
	}
	return env{t: t, pool: pool, sess: sess, tool: memory.New(pool)}
}

// run calls the tool the way the Worker does: a write's Commit runs in a
// transaction of its own, which stands in for the one that records the call.
func (e env) run(args map[string]any) (tool.Result, []tool.Event) {
	e.t.Helper()
	raw, err := json.Marshal(args)
	if err != nil {
		e.t.Fatal(err)
	}
	res, err := e.tool.Call(e.t.Context(), tool.CallInput{CallID: "c", Args: raw, Session: e.sess})
	if err != nil {
		e.t.Fatal(err)
	}
	if res.Commit == nil {
		return res, nil
	}
	var evs []tool.Event
	err = pgx.BeginFunc(e.t.Context(), e.pool, func(tx pgx.Tx) error {
		var err error
		res, evs, err = res.Commit(e.t.Context(), tx)
		return err
	})
	if err != nil {
		e.t.Fatal(err)
	}
	return res, evs
}

func text(res tool.Result) string {
	var parts []string
	for _, p := range res.Content {
		if p.Kind == msg.KindText {
			parts = append(parts, p.Text)
		}
	}
	return strings.Join(parts, "\n")
}

func ref(t *testing.T, res tool.Result) msg.MemoryRef {
	t.Helper()
	for _, p := range res.Content {
		if p.Kind == msg.KindMemoryRef {
			return *p.MemoryRef
		}
	}
	t.Fatalf("result has no memory ref: %+v", res)
	return msg.MemoryRef{}
}

func count(t *testing.T, pool *pgxpool.Pool, query string, args ...any) int {
	t.Helper()
	var n int
	if err := pool.QueryRow(t.Context(), query, args...).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

func (e env) create(path, content string) tool.Result {
	e.t.Helper()
	res, _ := e.run(map[string]any{"command": "create", "scope": "user", "path": path, "title": "T " + path, "kind": "fact", "content": content})
	return res
}

// seed inserts n active memories straight into the tables.
func (e env) seed(scope string, n int, title string) {
	e.t.Helper()
	var project any
	if scope == "project" {
		project = e.sess.ProjectID
	}
	for i := range n {
		_, err := e.pool.Exec(e.t.Context(), `
			INSERT INTO memories (id, workspace_id, user_id, scope, project_id, path, kind, title, content, written_by)
			VALUES (gen_random_uuid(), $1, $2, $3, $4, $5, 'fact', $6, 'x', 'user')`,
			e.sess.WorkspaceID, e.sess.UserID, scope, project, fmt.Sprintf("/memories/seed-%s-%d.md", scope, i), title)
		if err != nil {
			e.t.Fatal(err)
		}
	}
}

func TestSchemaChecksScopeAndUniqueness(t *testing.T) {
	e := newEnv(t)
	other := uuid.New()
	if _, err := e.pool.Exec(t.Context(), `INSERT INTO projects (id, workspace_id, user_id, name) VALUES ($1, $2, $3, 'Other')`, other, e.sess.WorkspaceID, e.sess.UserID); err != nil {
		t.Fatal(err)
	}
	tests := []struct {
		name, scope string
		project     any
		path        string
		wantErr     bool
	}{
		{"user memory with a project", "user", e.sess.ProjectID, "/memories/a.md", true},
		{"project memory without a project", "project", nil, "/memories/a.md", true},
		{"user memory", "user", nil, "/memories/a.md", false},
		{"same user path again, project NULL", "user", nil, "/memories/a.md", true},
		{"same path in project scope", "project", e.sess.ProjectID, "/memories/a.md", false},
		{"same project path again", "project", e.sess.ProjectID, "/memories/a.md", true},
		{"same path in another project", "project", other, "/memories/a.md", false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := e.pool.Exec(t.Context(), `
				INSERT INTO memories (id, workspace_id, user_id, scope, project_id, path, kind, title, content, written_by)
				VALUES (gen_random_uuid(), $1, $2, $3, $4, $5, 'fact', 't', 'c', 'agent')`,
				e.sess.WorkspaceID, e.sess.UserID, tt.scope, tt.project, tt.path)
			if (err != nil) != tt.wantErr {
				t.Fatalf("err = %v, wantErr %v", err, tt.wantErr)
			}
		})
	}
}

func TestEachWriteAddsOneRevision(t *testing.T) {
	e := newEnv(t)
	steps := []struct {
		args map[string]any
		op   string
		path string
	}{
		{map[string]any{"command": "create", "scope": "user", "path": "/memories/trip.md", "title": "Trip", "kind": "fact", "content": "Budget 50k\nTrains"}, "create", "/memories/trip.md"},
		{map[string]any{"command": "str_replace", "scope": "user", "path": "/memories/trip.md", "old_str": "50k", "new_str": "80k"}, "str_replace", "/memories/trip.md"},
		{map[string]any{"command": "insert", "scope": "user", "path": "/memories/trip.md", "insert_line": 1, "content": "Flights are stressful"}, "insert", "/memories/trip.md"},
		{map[string]any{"command": "rename", "scope": "user", "path": "/memories/trip.md", "new_path": "/memories/goa.md"}, "rename", "/memories/goa.md"},
	}
	var id string
	for i, s := range steps {
		res, evs := e.run(s.args)
		if res.IsError {
			t.Fatalf("step %d: %s", i, text(res))
		}
		r := ref(t, res)
		if i == 0 {
			id = r.MemoryID
		}
		if r.MemoryID != id || r.Version != i+1 {
			t.Fatalf("step %d: ref = %+v, want memory %s version %d", i, r, id, i+1)
		}
		if n := count(t, e.pool, `SELECT count(*) FROM memory_revisions WHERE memory_id = $1`, id); n != i+1 {
			t.Fatalf("step %d: %d revisions, want %d", i, n, i+1)
		}
		want := map[string]any{"memory_id": uuid.MustParse(id), "path": s.path, "op": s.op, "version": i + 1}
		if len(evs) != 1 || evs[0].Type != "memory.written" || fmt.Sprint(evs[0].Payload) != fmt.Sprint(want) {
			t.Fatalf("step %d: events = %+v, want memory.written %v", i, evs, want)
		}
	}
	var path, content, writtenBy string
	var version int
	err := e.pool.QueryRow(t.Context(), `SELECT path, content, written_by, version FROM memories WHERE id = $1`, id).Scan(&path, &content, &writtenBy, &version)
	if err != nil {
		t.Fatal(err)
	}
	if path != "/memories/goa.md" || content != "Budget 80k\nFlights are stressful\nTrains" || writtenBy != "agent" || version != 4 {
		t.Fatalf("row = %q %q %q v%d", path, content, writtenBy, version)
	}
	var revPath, revTitle string
	if err := e.pool.QueryRow(t.Context(), `SELECT path, title FROM memory_revisions WHERE memory_id = $1 AND version = 1`, id).Scan(&revPath, &revTitle); err != nil {
		t.Fatal(err)
	}
	if revPath != "/memories/trip.md" || revTitle != "Trip" {
		t.Fatalf("revision 1 holds path %q title %q", revPath, revTitle)
	}
}

func TestViewBumpsReadCount(t *testing.T) {
	e := newEnv(t)
	id := ref(t, e.create("/memories/a.md", "hello")).MemoryID
	for i := range 2 {
		res, _ := e.run(map[string]any{"command": "view", "scope": "user", "path": "/memories/a.md"})
		if got := ref(t, res); got.MemoryID != id || got.Version != 1 {
			t.Fatalf("view %d = %+v", i, got)
		}
	}
	var reads int
	var lastRead *string
	if err := e.pool.QueryRow(t.Context(), `SELECT read_count, last_read_at::text FROM memories WHERE id = $1`, id).Scan(&reads, &lastRead); err != nil {
		t.Fatal(err)
	}
	if reads != 2 || lastRead == nil {
		t.Fatalf("read_count = %d, last_read_at = %v", reads, lastRead)
	}
}

func TestPendingEntryIsHiddenFromViewAndIndex(t *testing.T) {
	e := newEnv(t)
	e.sess.Tainted = true
	id := ref(t, e.create("/memories/p.md", "from a web page")).MemoryID

	var status string
	var tainted bool
	if err := e.pool.QueryRow(t.Context(), `SELECT status, tainted FROM memories WHERE id = $1`, id).Scan(&status, &tainted); err != nil {
		t.Fatal(err)
	}
	if status != "pending_review" || !tainted {
		t.Fatalf("status = %q tainted = %v", status, tainted)
	}
	res, _ := e.run(map[string]any{"command": "view", "scope": "user", "path": "/memories/p.md"})
	if !res.IsError || text(res) != "no memory at /memories/p.md" {
		t.Fatalf("view = %+v, want the not-found error", res)
	}
	if n := count(t, e.pool, `SELECT read_count FROM memories WHERE id = $1`, id); n != 0 {
		t.Fatalf("read_count = %d, want 0", n)
	}
	res, _ = e.run(map[string]any{"command": "view", "path": "/memories"})
	if text(res) != "(no memories)" {
		t.Fatalf("index = %q", text(res))
	}
	res, _ = e.run(map[string]any{"command": "str_replace", "scope": "user", "path": "/memories/p.md", "old_str": "web", "new_str": "x"})
	if !res.IsError || text(res) != "no memory at /memories/p.md" {
		t.Fatalf("str_replace = %+v, want the not-found error", res)
	}
}

func TestCreateOverPendingKeepsItPending(t *testing.T) {
	e := newEnv(t)
	e.sess.Tainted = true
	id := ref(t, e.create("/memories/p.md", "first")).MemoryID

	e.sess.Tainted = false
	res := e.create("/memories/p.md", "second")
	if res.IsError || text(res) != "created /memories/p.md" || ref(t, res).Version != 2 {
		t.Fatalf("result = %+v, want a plain success at version 2", res)
	}
	var status, content string
	var tainted bool
	if err := e.pool.QueryRow(t.Context(), `SELECT status, tainted, content FROM memories WHERE id = $1`, id).Scan(&status, &tainted, &content); err != nil {
		t.Fatal(err)
	}
	if status != "pending_review" || !tainted || content != "second" {
		t.Fatalf("row = %q %v %q", status, tainted, content)
	}

	// An active memory is not overwritten.
	e.create("/memories/a.md", "one")
	if res := e.create("/memories/a.md", "two"); !res.IsError {
		t.Fatalf("create over an active memory = %+v, want an error", res)
	}
}

func TestRenameOverPendingKeepsItPending(t *testing.T) {
	e := newEnv(t)
	e.sess.Tainted = true
	hidden := ref(t, e.create("/memories/p.md", "from a web page")).MemoryID

	e.sess.Tainted = false
	id := ref(t, e.create("/memories/a.md", "mine")).MemoryID
	res, _ := e.run(map[string]any{"command": "rename", "scope": "user", "path": "/memories/a.md", "new_path": "/memories/p.md"})
	if res.IsError || text(res) != "renamed /memories/a.md to /memories/p.md" {
		t.Fatalf("result = %+v, want a plain success", res)
	}
	var status, content string
	if err := e.pool.QueryRow(t.Context(), `SELECT status, content FROM memories WHERE id = $1`, id).Scan(&status, &content); err != nil {
		t.Fatal(err)
	}
	if status != "pending_review" || content != "mine" {
		t.Fatalf("row = %q %q", status, content)
	}
	if n := count(t, e.pool, `SELECT count(*) FROM memories WHERE id = $1`, hidden); n != 0 {
		t.Fatalf("hidden entry rows = %d, want 0", n)
	}
}

func TestCapsRefuseAndWriteNothing(t *testing.T) {
	tests := []struct {
		name  string
		setup func(e env)
		write map[string]any
		want  string
	}{
		{"content over 4 KB", func(env) {}, map[string]any{"content": strings.Repeat("a", 4097)}, "limit is 4 KB"},
		{"201st memory in a scope", func(e env) {
			e.seed("user", 199, "t")
			e.sess.Tainted = true
			e.create("/memories/hidden.md", "x")
		}, map[string]any{}, "200 memories"},
		{"index over 200 lines", func(e env) {
			e.seed("user", 100, "t")
			e.seed("project", 100, "t")
		}, map[string]any{"scope": "project"}, "200 lines"},
		{"index over 25 KB", func(e env) { e.seed("user", 26, strings.Repeat("t", 1000)) }, map[string]any{}, "25 KB"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			e := newEnv(t)
			tt.setup(e)
			memories := count(t, e.pool, `SELECT count(*) FROM memories`)
			revisions := count(t, e.pool, `SELECT count(*) FROM memory_revisions`)

			args := map[string]any{"command": "create", "scope": "user", "path": "/memories/new.md", "title": "New", "kind": "fact", "content": "x"}
			for k, v := range tt.write {
				args[k] = v
			}
			res, evs := e.run(args)
			if !res.IsError || !strings.Contains(text(res), tt.want) || !strings.Contains(text(res), "Consolidate") || len(evs) != 0 {
				t.Fatalf("result = %+v events %v, want an error with %q and Consolidate", res, evs, tt.want)
			}
			if got := count(t, e.pool, `SELECT count(*) FROM memories`); got != memories {
				t.Fatalf("memories = %d, want %d", got, memories)
			}
			if got := count(t, e.pool, `SELECT count(*) FROM memory_revisions`); got != revisions {
				t.Fatalf("revisions = %d, want %d", got, revisions)
			}
		})
	}
}

func TestEditPastTheContentCapIsRefused(t *testing.T) {
	e := newEnv(t)
	e.create("/memories/a.md", strings.Repeat("a", 4000))
	res, _ := e.run(map[string]any{"command": "insert", "scope": "user", "path": "/memories/a.md", "insert_line": 0, "content": strings.Repeat("b", 200)})
	if !res.IsError || !strings.Contains(text(res), "Consolidate") {
		t.Fatalf("result = %+v", res)
	}
	if n := count(t, e.pool, `SELECT version FROM memories`); n != 1 {
		t.Fatalf("version = %d, want 1", n)
	}
}

func TestPathRules(t *testing.T) {
	tests := []struct {
		path, want string
	}{
		{"/memories/../etc/passwd", ""},
		{"/memories/a/../../b.md", ""},
		{"/etc/passwd", ""},
		{"/memories", ""},
		{"notes.md", ""},
		{"/memories//a/./b.md", "/memories/a/b.md"},
	}
	for _, tt := range tests {
		t.Run(tt.path, func(t *testing.T) {
			e := newEnv(t)
			res := e.create(tt.path, "x")
			if tt.want == "" {
				if !res.IsError || count(t, e.pool, `SELECT count(*) FROM memories`) != 0 {
					t.Fatalf("result = %+v, want a refusal and no row", res)
				}
				return
			}
			var got string
			if err := e.pool.QueryRow(t.Context(), `SELECT path FROM memories`).Scan(&got); err != nil || got != tt.want {
				t.Fatalf("path = %q (%v), want %q", got, err, tt.want)
			}
		})
	}
}

func TestSecretsAreRefused(t *testing.T) {
	refused := []string{
		"key is sk-ant-api03-abcdef",
		"sk-proj-1234",
		"AIzaSyD-1234",
		"AKIAIOSFODNN7EXAMPLE",
		"token ghp_abc123",
		"xoxb-123-456",
		"-----BEGIN RSA PRIVATE KEY-----\nMIIE",
		"-----BEGIN PRIVATE KEY-----",
	}
	e := newEnv(t)
	for _, content := range refused {
		res := e.create("/memories/s.md", content)
		if !res.IsError || !strings.Contains(text(res), "secret") {
			t.Fatalf("%q: result = %+v, want the no-secrets error", content, res)
		}
	}
	if n := count(t, e.pool, `SELECT count(*) FROM memories`) + count(t, e.pool, `SELECT count(*) FROM memory_revisions`); n != 0 {
		t.Fatalf("%d rows written", n)
	}
	if res := e.create("/memories/ok.md", "Prefers risk-averse plans and task-based work"); res.IsError {
		t.Fatalf("plain text refused: %s", text(res))
	}
}

func TestDeleteIsHard(t *testing.T) {
	e := newEnv(t)
	e.create("/memories/a.md", "one")
	e.run(map[string]any{"command": "str_replace", "scope": "user", "path": "/memories/a.md", "old_str": "one", "new_str": "two"})
	res, evs := e.run(map[string]any{"command": "delete", "scope": "user", "path": "/memories/a.md"})
	if res.IsError || len(evs) != 1 || evs[0].Payload.(map[string]any)["op"] != "delete" {
		t.Fatalf("result = %+v events %v", res, evs)
	}
	if n := count(t, e.pool, `SELECT count(*) FROM memories`) + count(t, e.pool, `SELECT count(*) FROM memory_revisions`); n != 0 {
		t.Fatalf("%d rows left", n)
	}
}

func TestChildSessionCanOnlyView(t *testing.T) {
	e := newEnv(t)
	e.create("/memories/a.md", "one")
	e.sess.Child = true
	res := e.create("/memories/b.md", "two")
	if !res.IsError || count(t, e.pool, `SELECT count(*) FROM memories`) != 1 {
		t.Fatalf("child write = %+v", res)
	}
	if res, _ := e.run(map[string]any{"command": "view", "scope": "user", "path": "/memories/a.md"}); res.IsError {
		t.Fatalf("child view: %s", text(res))
	}
}

func TestProjectMemoryBelongsToTheSessionsProject(t *testing.T) {
	e := newEnv(t)
	res, _ := e.run(map[string]any{"command": "create", "scope": "project", "path": "/memories/p.md", "title": "P", "kind": "fact", "content": "x"})
	if res.IsError {
		t.Fatal(text(res))
	}
	var project string
	if err := e.pool.QueryRow(t.Context(), `SELECT project_id::text FROM memories`).Scan(&project); err != nil || project != e.sess.ProjectID.String() {
		t.Fatalf("project_id = %q (%v)", project, err)
	}
	e.create("/memories/u.md", "x")
	var isNull bool
	if err := e.pool.QueryRow(t.Context(), `SELECT project_id IS NULL FROM memories WHERE scope = 'user'`).Scan(&isNull); err != nil || !isNull {
		t.Fatalf("user memory project_id null = %v (%v)", isNull, err)
	}

	other := e.sess
	other.ProjectID = uuid.New()
	if _, err := e.pool.Exec(t.Context(), `INSERT INTO projects (id, workspace_id, user_id, name) VALUES ($1, $2, $3, 'Other')`, other.ProjectID, e.sess.WorkspaceID, e.sess.UserID); err != nil {
		t.Fatal(err)
	}
	e.sess = other
	idx, _ := e.run(map[string]any{"command": "view", "path": "/memories"})
	if len(idx.Content) != 1 {
		t.Fatalf("another project sees %d memories, want only the user one", len(idx.Content))
	}
}

func TestParallelSafeCallIsViewOnly(t *testing.T) {
	tests := map[string]bool{
		`{"command":"view","path":"/memories"}`:   true,
		`{"command":"create","path":"/memories"}`: false,
		`{"command":"delete"}`:                    false,
		`not json`:                                false,
	}
	for args, want := range tests {
		if got := memory.New(nil).ParallelSafeCall(json.RawMessage(args)); got != want {
			t.Errorf("ParallelSafeCall(%s) = %v, want %v", args, got, want)
		}
	}
}
