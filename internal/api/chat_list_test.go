package api

import (
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"testing"

	"github.com/google/uuid"

	"github.com/bhanuprakaash/jelly-fish/internal/testdb"
)

func TestListSessionsHandler(t *testing.T) {
	e := newLoginEnv(t)
	alice := testdb.NewUser(t, e.pool)
	ac := e.signInUser(t, alice)
	bc := e.signInUser(t, testdb.NewUser(t, e.pool))

	if got := e.status("/api/sessions", http.MethodGet, nil); got != http.StatusUnauthorized {
		t.Errorf("without cookie: status %d, want 401", got)
	}

	first, second := uuid.New(), uuid.New()
	long := strings.Repeat("x", 100)
	for _, c := range []struct {
		id  uuid.UUID
		msg string
	}{{first, "first chat"}, {second, long}} {
		rr := e.do(http.MethodPost, "/api/sessions", map[string]any{
			"session_id": c.id, "client_msg_id": uuid.New(), "message": c.msg,
		}, ac)
		if rr.Code != http.StatusOK {
			t.Fatalf("create session: status %d, body %s", rr.Code, rr.Body)
		}
	}
	for _, q := range []string{
		`INSERT INTO sessions (id, workspace_id, project_id, user_id, agent_id, status, trigger)
		 SELECT gen_random_uuid(), workspace_id, project_id, user_id, agent_id, 'completed', 'memory_tidy' FROM sessions WHERE id = $1`,
		`INSERT INTO sessions (id, workspace_id, project_id, user_id, agent_id, status, parent_id, depth)
		 SELECT gen_random_uuid(), workspace_id, project_id, user_id, agent_id, 'completed', id, 1 FROM sessions WHERE id = $1`,
	} {
		if _, err := e.pool.Exec(t.Context(), q, first); err != nil {
			t.Fatalf("insert hidden session: %v", err)
		}
	}
	if _, err := e.pool.Exec(t.Context(), `UPDATE sessions SET updated_at = now() - interval '1 hour' WHERE id = $1`, first); err != nil {
		t.Fatalf("set updated_at: %v", err)
	}

	rr := e.do(http.MethodGet, "/api/sessions", nil, ac)
	if rr.Code != http.StatusOK {
		t.Fatalf("list: status %d, body %s", rr.Code, rr.Body)
	}
	var rows []map[string]any
	if err := json.Unmarshal(rr.Body.Bytes(), &rows); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(rows) != 2 || rows[0]["id"] != second.String() || rows[1]["id"] != first.String() {
		t.Fatalf("rows = %v, want [%s %s]", rows, second, first)
	}
	var keys []string
	for k := range rows[0] {
		keys = append(keys, k)
	}
	slices.Sort(keys)
	if want := []string{"id", "status", "title", "titled", "updated_at"}; !slices.Equal(keys, want) {
		t.Errorf("keys = %v, want %v", keys, want)
	}
	if want := strings.Repeat("x", 60) + "…"; rows[0]["title"] != want || rows[0]["titled"] != false {
		t.Errorf("placeholder row = title %q titled %v, want %q false", rows[0]["title"], rows[0]["titled"], want)
	}

	rr = e.do(http.MethodGet, "/api/sessions", nil, bc)
	if got := strings.TrimSpace(rr.Body.String()); rr.Code != http.StatusOK || got != "[]" {
		t.Errorf("other user: status %d body %q, want 200 []", rr.Code, got)
	}
}

func TestListSessionsHandler_RepoError(t *testing.T) {
	srv := newTestServer(t, &fakeRepo{listSessionsErr: errors.New("db down")})
	req := signIn(httptest.NewRequest(http.MethodGet, "/api/sessions", nil))
	rr := httptest.NewRecorder()
	srv.Handler.ServeHTTP(rr, req)
	if rr.Code != http.StatusInternalServerError {
		t.Errorf("status = %d, want 500", rr.Code)
	}
}

func TestCreateSessionIncognitoFlag(t *testing.T) {
	e := newLoginEnv(t)
	c := e.signInUser(t, testdb.NewUser(t, e.pool))
	normal, incognito := uuid.New(), uuid.New()
	for id, body := range map[uuid.UUID]map[string]any{
		normal:    {"session_id": normal, "client_msg_id": uuid.New(), "message": "hi"},
		incognito: {"session_id": incognito, "client_msg_id": uuid.New(), "message": "hi", "incognito": true},
	} {
		if rr := e.do(http.MethodPost, "/api/sessions", body, c); rr.Code != http.StatusOK {
			t.Fatalf("create %s: status %d, body %s", id, rr.Code, rr.Body)
		}
	}
	for id, want := range map[uuid.UUID]bool{normal: false, incognito: true} {
		var got bool
		if err := e.pool.QueryRow(t.Context(), `SELECT incognito FROM sessions WHERE id = $1`, id).Scan(&got); err != nil || got != want {
			t.Errorf("session %s incognito = %v (%v), want %v", id, got, err, want)
		}
	}
}
