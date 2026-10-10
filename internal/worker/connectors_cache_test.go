package worker_test

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"slices"
	"sync/atomic"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/bhanuprakaash/jelly-fish/internal/connector"
	"github.com/bhanuprakaash/jelly-fish/internal/eventlog"
	"github.com/bhanuprakaash/jelly-fish/internal/mcpclient"
	"github.com/bhanuprakaash/jelly-fish/internal/mcpclient/mcptest"
	"github.com/bhanuprakaash/jelly-fish/internal/provider"
	"github.com/bhanuprakaash/jelly-fish/internal/testdb"
)

func TestAConnectorThatCannotBeListedIsNotRetriedUntilItsTTLPasses(t *testing.T) {
	var hits atomic.Int32
	hs := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		hits.Add(1)
		http.Error(w, "down", http.StatusServiceUnavailable)
	}))
	t.Cleanup(hs.Close)
	e := newConnectorEnv(t, &replies{list: []provider.Response{done(), done()}})
	user := testdb.NewUser(t, e.pool)
	e.addConnector(t, user.ID, &mcptest.Server{URL: hs.URL}, 11*time.Minute)
	sid := e.newSession(t, user.Scope())
	waitStatus(t, e.pool, sid, eventlog.StatusAwaitingUser, 1)

	first := hits.Load()
	if first == 0 {
		t.Fatal("the stale connector was not listed")
	}
	if _, err := eventlog.NewRepo(e.pool, "fake").PostMessage(t.Context(), user.Scope(), sid, uuid.New(), "again"); err != nil {
		t.Fatal(err)
	}
	waitStatus(t, e.pool, sid, eventlog.StatusAwaitingUser, 2)
	if got := hits.Load(); got != first {
		t.Fatalf("the server was hit %d times, want %d: the second drive listed it again", got, first)
	}
}

func TestToolNamesModelsRejectAreCachedButNotOffered(t *testing.T) {
	e := newConnectorEnv(t, &replies{list: []provider.Response{done()}})
	srv := mcptest.Start(t, mcptest.Options{})
	user := testdb.NewUser(t, e.pool)
	id := e.addConnector(t, user.ID, srv, time.Minute)
	var tools []connector.CachedTool
	for _, name := range []string{"search_pages", "a.b"} {
		tools = append(tools, connector.FromRaw(mcpclient.RawTool{Name: name, InputSchema: json.RawMessage(`{"type":"object"}`)}))
	}
	if err := e.store.Replace(t.Context(), id, tools); err != nil {
		t.Fatal(err)
	}
	sid := e.newSession(t, user.Scope())
	waitStatus(t, e.pool, sid, eventlog.StatusAwaitingUser, 1)

	if got := e.toolNames(t, sid, 0); !slices.Equal(got, []string{"notion__search_pages"}) {
		t.Fatalf("offered %v, want only notion__search_pages", got)
	}
	projectID, err := e.store.ProjectID(t.Context(), user.ID)
	if err != nil {
		t.Fatal(err)
	}
	_, cached, err := e.store.List(t.Context(), projectID)
	if err != nil {
		t.Fatal(err)
	}
	if n := len(cached[id]); n != 2 {
		t.Fatalf("%d tools cached, want 2", n)
	}
}
