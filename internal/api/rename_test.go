package api

import (
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/bhanuprakaash/jelly-fish/internal/auth"
	"github.com/bhanuprakaash/jelly-fish/internal/eventlog"
	"github.com/bhanuprakaash/jelly-fish/internal/stream"
	"github.com/bhanuprakaash/jelly-fish/internal/testdb"
)

type listedChat struct {
	ID     string `json:"id"`
	Title  string `json:"title"`
	Titled bool   `json:"titled"`
}

func (e *loginEnv) newChat(t *testing.T, c *http.Cookie, message string) uuid.UUID {
	t.Helper()
	id := uuid.New()
	rr := e.do(http.MethodPost, "/api/sessions", map[string]any{"session_id": id, "client_msg_id": uuid.New(), "message": message}, c)
	if rr.Code != http.StatusOK {
		t.Fatalf("create session: status %d, body %s", rr.Code, rr.Body)
	}
	return id
}

func (e *loginEnv) listedChat(t *testing.T, c *http.Cookie, id uuid.UUID) listedChat {
	t.Helper()
	rr := e.do(http.MethodGet, "/api/sessions", nil, c)
	var rows []listedChat
	if err := json.Unmarshal(rr.Body.Bytes(), &rows); err != nil {
		t.Fatalf("decode list: %v", err)
	}
	for _, r := range rows {
		if r.ID == id.String() {
			return r
		}
	}
	t.Fatalf("chat %s not listed: %s", id, rr.Body)
	return listedChat{}
}

func TestRenameSessionHandler(t *testing.T) {
	e := newLoginEnv(t)
	alice := testdb.NewUser(t, e.pool)
	ac := e.signInUser(t, alice)
	bc := e.signInUser(t, testdb.NewUser(t, e.pool))
	chat := e.newChat(t, ac, "hello")
	rename := func(c *http.Cookie, id uuid.UUID, body any) *httptest.ResponseRecorder {
		return e.do(http.MethodPut, "/api/sessions/"+id.String()+"/title", body, c)
	}

	if rr := rename(nil, chat, map[string]string{"title": "x"}); rr.Code != http.StatusUnauthorized {
		t.Errorf("without cookie: status %d, want 401", rr.Code)
	}

	if rr := rename(ac, chat, map[string]string{"title": "  Hi  "}); rr.Code != http.StatusNoContent {
		t.Fatalf("rename: status %d, body %s", rr.Code, rr.Body)
	}
	if got := e.listedChat(t, ac, chat); got.Title != "Hi" || !got.Titled {
		t.Fatalf("listed = %+v, want the trimmed title, titled", got)
	}

	for _, tt := range []struct {
		name string
		body any
	}{
		{"empty", map[string]string{"title": ""}},
		{"whitespace only", map[string]string{"title": " \t\n "}},
		{"101 characters", map[string]string{"title": strings.Repeat("x", 101)}},
		{"101 multi-byte characters", map[string]string{"title": strings.Repeat("é", 101)}},
		{"missing", map[string]string{}},
		{"invalid JSON", "not an object"},
	} {
		if rr := rename(ac, chat, tt.body); rr.Code != http.StatusBadRequest {
			t.Errorf("%s: status %d, want 400", tt.name, rr.Code)
		}
	}
	if got := e.listedChat(t, ac, chat); got.Title != "Hi" {
		t.Fatalf("title = %q after rejected renames", got.Title)
	}

	long := strings.Repeat("é", 100)
	if rr := rename(ac, chat, map[string]string{"title": long}); rr.Code != http.StatusNoContent {
		t.Fatalf("100 multi-byte characters: status %d, want 204", rr.Code)
	}
	if got := e.listedChat(t, ac, chat); got.Title != long {
		t.Fatalf("title = %q", got.Title)
	}

	if rr := rename(bc, chat, map[string]string{"title": "Mine"}); rr.Code != http.StatusNotFound {
		t.Errorf("other user's chat: status %d, want 404", rr.Code)
	}
	if rr := rename(ac, uuid.New(), map[string]string{"title": "x"}); rr.Code != http.StatusNotFound {
		t.Errorf("unknown chat: status %d, want 404", rr.Code)
	}

	child := insertSession(t, e.pool, alice.Scope(), eventlog.TriggerUserMessage, &chat)
	if rr := rename(ac, child, map[string]string{"title": "x"}); rr.Code != http.StatusUnprocessableEntity {
		t.Errorf("child session: status %d, want 422", rr.Code)
	}
}

func TestSessionStreamEmitsRenamedFrameAfterPut(t *testing.T) {
	e := newLoginEnv(t)
	alice := testdb.NewUser(t, e.pool)
	ac := e.signInUser(t, alice)
	chat := e.newChat(t, ac, "hello")

	hub := stream.NewHub()
	ctx, cancel := context.WithCancel(t.Context())
	var wg sync.WaitGroup
	t.Cleanup(func() { cancel(); wg.Wait() })
	_, listening, unsubscribe := hub.SubscribeActivity(uuid.New())
	defer unsubscribe()
	wg.Go(func() {
		stream.Listen(ctx, e.pool, slog.New(slog.DiscardHandler), hub, stream.NewPGDeltaBus(e.pool), newTestMetrics(t))
	})
	select {
	case <-listening:
	case <-time.After(5 * time.Second):
		t.Fatal("timed out waiting for Listen to start")
	}

	streams := newStreams(t, eventlog.NewRepo(e.pool, "fake"), hub, &fakeDeltaBus{})
	w, stop := serveSessionAs(t, streams.handle, alice, chat)
	defer stop()
	w.waitFor(t, `"type":"user.message"`)

	if rr := e.do(http.MethodPut, "/api/sessions/"+chat.String()+"/title", map[string]string{"title": "Trip plan"}, ac); rr.Code != http.StatusNoContent {
		t.Fatalf("rename: status %d, body %s", rr.Code, rr.Body)
	}
	w.waitFor(t, `"type":"session.renamed"`)
	if !strings.Contains(w.body(), `"payload":{"by":"user","title":"Trip plan"}`) {
		t.Fatalf("frame = %q, want the title and who set it", w.body())
	}
}

// serveSessionAs runs the Session stream handler for sid as user.
func serveSessionAs(t *testing.T, h http.HandlerFunc, user auth.User, sid uuid.UUID) (w *streamWriter, stop func()) {
	t.Helper()
	ctx, cancel := context.WithCancel(auth.WithUser(t.Context(), user))
	req := httptest.NewRequest(http.MethodGet, "/", nil).WithContext(ctx)
	req.SetPathValue("id", sid.String())
	w = newStreamWriter()
	done := make(chan struct{})
	go func() {
		h(w, req)
		close(done)
	}()
	return w, func() {
		cancel()
		select {
		case <-done:
		case <-time.After(2 * time.Second):
			t.Fatal("handler did not exit after client disconnect")
		}
	}
}
