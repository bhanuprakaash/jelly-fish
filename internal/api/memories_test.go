package api

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"testing"

	"github.com/google/uuid"

	"github.com/bhanuprakaash/jelly-fish/internal/auth"
	"github.com/bhanuprakaash/jelly-fish/internal/testdb"
)

// seedMemory inserts a user-scope memory whose revisions are contents, oldest
// first; the memory holds the last one.
func (e *loginEnv) seedMemory(t *testing.T, u auth.User, path, status string, contents ...string) uuid.UUID {
	t.Helper()
	id := uuid.New()
	last := contents[len(contents)-1]
	if _, err := e.pool.Exec(t.Context(), `
		INSERT INTO memories (id, workspace_id, user_id, scope, path, kind, title, content, status, tainted, written_by, version)
		VALUES ($1, $2, $3, 'user', $4, 'fact', 'title '||$4, $5, $6, $6 = 'pending_review', 'agent', $7)`,
		id, u.WorkspaceID, u.ID, path, last, status, len(contents)); err != nil {
		t.Fatalf("seed memory: %v", err)
	}
	for i, c := range contents {
		if _, err := e.pool.Exec(t.Context(), `
			INSERT INTO memory_revisions (memory_id, version, path, title, content, written_by)
			VALUES ($1, $2, $3, 'title '||$3, $4, 'agent')`, id, i+1, path, c); err != nil {
			t.Fatalf("seed revision: %v", err)
		}
	}
	return id
}

type overviewJSON struct {
	Project struct {
		ID            uuid.UUID `json:"id"`
		UseUserMemory bool      `json:"use_user_memory"`
	} `json:"project"`
	Memories []struct {
		Path    string `json:"path"`
		Content string `json:"content"`
		Status  string `json:"status"`
		Version int    `json:"version"`
		Stale   bool   `json:"stale"`
	} `json:"memories"`
}

func (e *loginEnv) overview(t *testing.T, c *http.Cookie) overviewJSON {
	t.Helper()
	rr := e.do(http.MethodGet, "/api/memories", nil, c)
	if rr.Code != http.StatusOK {
		t.Fatalf("list memories: status %d, body %s", rr.Code, rr.Body)
	}
	var o overviewJSON
	if err := json.Unmarshal(rr.Body.Bytes(), &o); err != nil {
		t.Fatalf("decode: %v", err)
	}
	return o
}

func (e *loginEnv) count(t *testing.T, query string, args ...any) int {
	t.Helper()
	var n int
	if err := e.pool.QueryRow(t.Context(), query, args...).Scan(&n); err != nil {
		t.Fatalf("%s: %v", query, err)
	}
	return n
}

func TestMemoryPageEditApproveDelete(t *testing.T) {
	e := newLoginEnv(t)
	u := testdb.NewUser(t, e.pool)
	c := e.signInUser(t, u)
	edited := e.seedMemory(t, u, "/memories/a.md", "pending_review", "old")
	approved := e.seedMemory(t, u, "/memories/b.md", "pending_review", "keep")
	deleted := e.seedMemory(t, u, "/memories/c.md", "active", "x")
	events := e.count(t, `SELECT count(*) FROM events`)

	for _, step := range []struct {
		method, path string
		body         any
		want         int
	}{
		{http.MethodPatch, "/api/memories/" + edited.String(), map[string]any{"content": "new", "version": 1}, http.StatusNoContent},
		{http.MethodPost, "/api/memories/" + approved.String() + "/approve", map[string]int{"version": 1}, http.StatusNoContent},
		{http.MethodDelete, "/api/memories/" + deleted.String(), nil, http.StatusNoContent},
		{http.MethodPatch, "/api/memories/" + edited.String(), map[string]any{"content": "key sk-ant-abc", "version": 2}, http.StatusUnprocessableEntity},
		{http.MethodPatch, "/api/memories/" + edited.String(), map[string]any{"content": strings.Repeat("x", 4097), "version": 2}, http.StatusUnprocessableEntity},
	} {
		if rr := e.do(step.method, step.path, step.body, c); rr.Code != step.want {
			t.Fatalf("%s %s: status %d, want %d, body %s", step.method, step.path, rr.Code, step.want, rr.Body)
		}
	}

	if got := e.count(t, `SELECT count(*) FROM events`); got != events {
		t.Errorf("events = %d, want %d: page actions write none", got, events)
	}
	got := map[string]string{}
	for _, m := range e.overview(t, c).Memories {
		got[m.Path] = fmt.Sprintf("%s %s v%d", m.Content, m.Status, m.Version)
	}
	want := map[string]string{"/memories/a.md": "new active v2", "/memories/b.md": "keep active v2"}
	if fmt.Sprint(got) != fmt.Sprint(want) {
		t.Errorf("memories = %v, want %v", got, want)
	}
	for _, id := range []uuid.UUID{edited, approved} {
		n := e.count(t, `SELECT count(*) FROM memory_revisions WHERE memory_id = $1 AND version = 2 AND written_by = 'user' AND session_id IS NULL`, id)
		if n != 1 {
			t.Errorf("memory %s: %d user revisions at v2, want 1", id, n)
		}
	}
	if n := e.count(t, `SELECT count(*) FROM memory_revisions WHERE memory_id = $1`, deleted); n != 0 {
		t.Errorf("deleted memory kept %d revisions", n)
	}
	if n := e.count(t, `SELECT count(*) FROM memories WHERE tainted`); n != 0 {
		t.Errorf("%d memories still tainted", n)
	}
}

func TestMemoryStaleCardIsRefused(t *testing.T) {
	e := newLoginEnv(t)
	u := testdb.NewUser(t, e.pool)
	c := e.signInUser(t, u)
	id := e.seedMemory(t, u, "/memories/a.md", "pending_review", "from a web page", "agent overwrite")
	url := "/api/memories/" + id.String()

	for name, step := range map[string]struct {
		method, path string
		body         any
	}{
		"approve": {http.MethodPost, url + "/approve", map[string]int{"version": 1}},
		"edit":    {http.MethodPatch, url, map[string]any{"content": "mine", "version": 1}},
	} {
		t.Run(name, func(t *testing.T) {
			rr := e.do(step.method, step.path, step.body, c)
			if got := strings.TrimSpace(rr.Body.String()); rr.Code != http.StatusConflict || got != `{"error":"changed since"}` {
				t.Fatalf("%d %s, want 409 changed since", rr.Code, got)
			}
			if e.count(t, `SELECT count(*) FROM memories WHERE id = $1 AND status = 'pending_review' AND version = 2 AND content = 'agent overwrite'`, id) != 1 {
				t.Fatal("memory changed by a stale request")
			}
		})
	}
	if rr := e.do(http.MethodPost, url+"/approve", map[string]int{"version": 2}, c); rr.Code != http.StatusNoContent {
		t.Fatalf("approve at the current version: %d %s", rr.Code, rr.Body)
	}
}

func TestMemoryChipUndo(t *testing.T) {
	e := newLoginEnv(t)
	u := testdb.NewUser(t, e.pool)
	c := e.signInUser(t, u)
	created := e.seedMemory(t, u, "/memories/new.md", "active", "first")
	replaced := e.seedMemory(t, u, "/memories/r.md", "active", "before", "after")

	undo := func(id uuid.UUID, version int) (int, string) {
		rr := e.do(http.MethodPost, "/api/memories/"+id.String()+"/undo", map[string]int{"version": version}, c)
		return rr.Code, strings.TrimSpace(rr.Body.String())
	}
	pending := e.seedMemory(t, u, "/memories/p.md", "pending_review", "from a web page", "edited")
	if code, body := undo(pending, 2); code != http.StatusConflict {
		t.Errorf("undo pending: %d %s, want 409", code, body)
	}
	if code, body := undo(created, 1); code != http.StatusNoContent {
		t.Fatalf("undo create: %d %s", code, body)
	}
	if n := e.count(t, `SELECT count(*) FROM memories WHERE id = $1`, created); n != 0 {
		t.Errorf("created memory survived undo")
	}
	if code, body := undo(replaced, 1); code != http.StatusConflict || body != `{"error":"changed since"}` {
		t.Errorf("undo at stale version: %d %s, want 409 changed since", code, body)
	}
	if code, body := undo(replaced, 2); code != http.StatusNoContent {
		t.Fatalf("undo str_replace: %d %s", code, body)
	}
	var content, by string
	var version int
	err := e.pool.QueryRow(t.Context(), `
		SELECT m.content, m.version, r.written_by FROM memories m JOIN memory_revisions r ON r.memory_id = m.id AND r.version = m.version
		WHERE m.id = $1`, replaced).Scan(&content, &version, &by)
	if err != nil || content != "before" || version != 3 || by != "user" {
		t.Errorf("after undo: content %q v%d by %q (%v), want before v3 user", content, version, by, err)
	}
}

func TestMemoryStale(t *testing.T) {
	e := newLoginEnv(t)
	u := testdb.NewUser(t, e.pool)
	c := e.signInUser(t, u)
	old := e.seedMemory(t, u, "/memories/old.md", "active", "x")
	recent := e.seedMemory(t, u, "/memories/recent.md", "active", "x")
	unread := e.seedMemory(t, u, "/memories/unread.md", "active", "x")
	for id, set := range map[uuid.UUID]string{
		old:    "last_read_at = now() - interval '91 days'",
		recent: "last_read_at = now() - interval '1 day', created_at = now() - interval '200 days'",
		unread: "created_at = now() - interval '91 days'",
	} {
		if _, err := e.pool.Exec(t.Context(), `UPDATE memories SET `+set+` WHERE id = $1`, id); err != nil {
			t.Fatal(err)
		}
	}
	got := map[string]bool{}
	for _, m := range e.overview(t, c).Memories {
		got[m.Path] = m.Stale
	}
	want := map[string]bool{"/memories/old.md": true, "/memories/recent.md": false, "/memories/unread.md": true}
	if fmt.Sprint(got) != fmt.Sprint(want) {
		t.Errorf("stale = %v, want %v", got, want)
	}
	if n := e.count(t, `SELECT count(*) FROM memories`); n != 3 {
		t.Errorf("%d memories left, want 3: stale ones are never deleted", n)
	}
}

func TestMemoryUseUserMemoryToggle(t *testing.T) {
	e := newLoginEnv(t)
	c := e.signInUser(t, testdb.NewUser(t, e.pool))
	o := e.overview(t, c)
	if !o.Project.UseUserMemory {
		t.Fatal("use_user_memory should start on")
	}
	rr := e.do(http.MethodPatch, "/api/projects/"+o.Project.ID.String(), map[string]bool{"use_user_memory": false}, c)
	if rr.Code != http.StatusNoContent {
		t.Fatalf("toggle: status %d, body %s", rr.Code, rr.Body)
	}
	if e.count(t, `SELECT count(*) FROM projects WHERE id = $1 AND NOT use_user_memory`, o.Project.ID) != 1 || e.overview(t, c).Project.UseUserMemory {
		t.Error("use_user_memory not off after toggle")
	}
}

func TestMemoryTenancy(t *testing.T) {
	e := newLoginEnv(t)
	alice, bob := testdb.NewUser(t, e.pool), testdb.NewUser(t, e.pool)
	ac, bc := e.signInUser(t, alice), e.signInUser(t, bob)
	id := e.seedMemory(t, alice, "/memories/a.md", "pending_review", "secret plans")

	for _, r := range []struct{ method, path string }{
		{http.MethodGet, "/api/memories/" + id.String() + "/revisions"},
		{http.MethodPatch, "/api/memories/" + id.String()},
		{http.MethodPost, "/api/memories/" + id.String() + "/approve"},
		{http.MethodPost, "/api/memories/" + id.String() + "/undo"},
		{http.MethodDelete, "/api/memories/" + id.String()},
	} {
		t.Run(r.method+" "+strings.TrimPrefix(r.path, "/api/memories/"+id.String()), func(t *testing.T) {
			body := map[string]any{"content": "x", "version": 1}
			if rr := e.do(r.method, r.path, body, bc); rr.Code != http.StatusNotFound {
				t.Errorf("as other user: status %d, want 404", rr.Code)
			}
		})
	}
	t.Run("list", func(t *testing.T) {
		if m := e.overview(t, bc).Memories; len(m) != 0 {
			t.Errorf("other user lists %d memories, want 0", len(m))
		}
	})
	t.Run("toggle project", func(t *testing.T) {
		pid := e.overview(t, ac).Project.ID
		if rr := e.do(http.MethodPatch, "/api/projects/"+pid.String(), map[string]bool{"use_user_memory": false}, bc); rr.Code != http.StatusNotFound {
			t.Errorf("status %d, want 404", rr.Code)
		}
	})
	t.Run("memory unchanged", func(t *testing.T) {
		if e.count(t, `SELECT count(*) FROM memories WHERE id = $1 AND status = 'pending_review' AND content = 'secret plans'`, id) != 1 {
			t.Error("other user's memory changed")
		}
	})
}
