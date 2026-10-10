package worker_test

import (
	"context"
	"encoding/json"
	"log/slog"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/bhanuprakaash/jelly-fish/internal/blob"
	"github.com/bhanuprakaash/jelly-fish/internal/connector"
	"github.com/bhanuprakaash/jelly-fish/internal/eventlog"
	"github.com/bhanuprakaash/jelly-fish/internal/keyring"
	"github.com/bhanuprakaash/jelly-fish/internal/mcpclient"
	"github.com/bhanuprakaash/jelly-fish/internal/mcpclient/mcptest"
	"github.com/bhanuprakaash/jelly-fish/internal/msg"
	"github.com/bhanuprakaash/jelly-fish/internal/provider"
	"github.com/bhanuprakaash/jelly-fish/internal/stream"
	"github.com/bhanuprakaash/jelly-fish/internal/testdb"
	"github.com/bhanuprakaash/jelly-fish/internal/tool"
	"github.com/bhanuprakaash/jelly-fish/internal/worker"
)

type connectorEnv struct {
	pool  *pgxpool.Pool
	kr    *keyring.Keyring
	store *connector.Store
	// stop ends the running Worker.
	stop func()
	// configure, if set, adjusts each Worker start builds.
	configure func(*worker.Worker)
}

// newConnectorEnv starts a Worker that offers builtins and each session's
// connector tools.
func newConnectorEnv(t *testing.T, p provider.Provider, builtins ...tool.Tool) *connectorEnv {
	t.Helper()
	pool := testdb.NewPool(t)
	kr, err := keyring.Parse("m1:AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA=")
	if err != nil {
		t.Fatal(err)
	}
	e := &connectorEnv{pool: pool, kr: kr, store: connector.NewStore(pool)}
	e.start(t, p, builtins...)
	return e
}

// start runs a new Worker; call it after stop to simulate a restart.
func (e *connectorEnv) start(t *testing.T, p provider.Provider, builtins ...tool.Tool) {
	t.Helper()
	w := worker.New(e.pool, worker.Gateway{Fake: p, Keyring: e.kr}, tool.NewRegistry(builtins...), stream.NewPGDeltaBus(e.pool), worker.Lease{TTL: 30 * time.Second, Heartbeat: 10 * time.Second}, eventlog.Upcasters{}, slog.New(slog.DiscardHandler))
	w.UseConnectors(e.store, mcpclient.New(true))
	worker.SetTitleTimeout(w, 0)
	if e.configure != nil {
		e.configure(w)
	}
	ctx, cancel := context.WithCancel(t.Context())
	done := make(chan struct{})
	go func() { w.Run(ctx); close(done) }()
	e.stop = sync.OnceFunc(func() { cancel(); <-done })
	t.Cleanup(e.stop)
}

// addConnector attaches a connector for server to userID's project, with the
// credential "s3cret" in header X-API-Key, and its tools cached age ago.
func (e *connectorEnv) addConnector(t *testing.T, userID uuid.UUID, srv *mcptest.Server, age time.Duration) uuid.UUID {
	t.Helper()
	projectID, err := e.store.ProjectID(t.Context(), userID)
	if err != nil {
		t.Fatal(err)
	}
	id := uuid.New()
	ct, keyID, err := e.kr.Seal([]byte("s3cret"), connector.AAD(projectID, id))
	if err != nil {
		t.Fatal(err)
	}
	tools := []connector.CachedTool{connector.FromRaw(mcpclient.RawTool{Name: "search_pages", InputSchema: json.RawMessage(`{"type":"object"}`)})}
	if _, err := e.store.Create(t.Context(), connector.Connector{ID: id, ProjectID: projectID, Name: "Notion", URL: srv.URL, AuthHeader: "X-API-Key", Era: mcpclient.EraModern},
		tools, &connector.Sealed{Ciphertext: ct, KeyID: keyID}); err != nil {
		t.Fatal(err)
	}
	if _, err := e.pool.Exec(t.Context(), `UPDATE connector_tools SET fetched_at = now() - $2::interval WHERE connector_id = $1`, id, age); err != nil {
		t.Fatal(err)
	}
	return id
}

// newSession starts a session for scope's user.
func (e *connectorEnv) newSession(t *testing.T, scope eventlog.TenantScope) uuid.UUID {
	t.Helper()
	sid := uuid.New()
	if _, err := eventlog.NewRepo(e.pool, "fake").CreateSession(t.Context(), scope, sid, uuid.New(), "hi", "", false); err != nil {
		t.Fatal(err)
	}
	return sid
}

func (e *connectorEnv) toolNames(t *testing.T, sid uuid.UUID, turn int) []string {
	t.Helper()
	ref := blobRef(t, ofType(loadEvents(t, e.pool, sid), eventlog.TypeTurnStarted)[turn].Payload, "tools_blob")
	_, data, err := blob.Get(t.Context(), e.pool, ref.SHA256)
	if err != nil {
		t.Fatal(err)
	}
	var defs []tool.Def
	if err := json.Unmarshal(data, &defs); err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, d := range defs {
		names = append(names, d.Name)
	}
	return names
}

func callNotion() provider.Response {
	m := msg.AssistantText("on it")
	m.Parts = append(m.Parts, msg.Part{Kind: msg.KindToolUse, ToolUse: &msg.ToolUse{ID: "c1", Name: "notion__search_pages", Args: json.RawMessage(`{"q":"plans"}`)}})
	return provider.Response{Message: m, StopReason: provider.StopReasonToolUse}
}

func TestConnectorToolsAreScopedToTheProjectAndCalledUnprefixed(t *testing.T) {
	p := &replies{list: []provider.Response{callNotion(), done()}}
	e := newConnectorEnv(t, p)
	srv := mcptest.Start(t, mcptest.Options{})
	srv.AddTool("search_pages", &mcp.CallToolResult{Content: []mcp.Content{&mcp.TextContent{Text: strings.Repeat("n", 100)}}})

	user := testdb.NewUser(t, e.pool)
	scope := user.Scope()
	id := e.addConnector(t, user.ID, srv, time.Minute)
	sid := e.newSession(t, scope)
	waitStatus(t, e.pool, sid, eventlog.StatusAwaitingApproval, 1)
	resolveLatest(t, e.pool, scope, sid, eventlog.Answer{Decision: eventlog.DecisionAllow})
	waitStatus(t, e.pool, sid, eventlog.StatusAwaitingUser, 2)

	if got := e.toolNames(t, sid, 0); !slices.Equal(got, []string{"notion__search_pages"}) {
		t.Fatalf("tools of the project's session = %v", got)
	}
	if name, args := srv.LastCall(); name != "search_pages" || string(args) != `{"q":"plans"}` {
		t.Fatalf("server saw %q %s, want the unprefixed name", name, args)
	}
	if got := srv.Header().Get("X-Api-Key"); got != "s3cret" {
		t.Fatalf("server saw X-API-Key %q", got)
	}
	if got := p.requests()[1].Messages[2].Parts[0].ToolResult.Parts[0].Text; got != strings.Repeat("n", 100) {
		t.Fatalf("model saw %q", got)
	}

	other, _ := newFakeSession(t, e.pool)
	waitStatus(t, e.pool, other, eventlog.StatusAwaitingUser, 1)
	if got := e.toolNames(t, other, 0); len(got) != 0 {
		t.Fatalf("another project's session sees %v", got)
	}

	projectID, err := e.store.ProjectID(t.Context(), user.ID)
	if err != nil {
		t.Fatal(err)
	}
	if err := e.store.SetToolEnabled(t.Context(), projectID, id, "search_pages", false); err != nil {
		t.Fatal(err)
	}
	if _, err := eventlog.NewRepo(e.pool, "fake").PostMessage(t.Context(), scope, sid, uuid.New(), "again"); err != nil {
		t.Fatal(err)
	}
	waitStatus(t, e.pool, sid, eventlog.StatusAwaitingUser, 3)
	if got := e.toolNames(t, sid, 2); len(got) != 0 {
		t.Fatalf("disabled tool still offered: %v", got)
	}
}

func TestStaleToolsAreListedAgainAtTurnStart(t *testing.T) {
	for name, tc := range map[string]struct {
		age  time.Duration
		want bool
	}{
		"11 minutes old": {11 * time.Minute, true},
		"1 minute old":   {time.Minute, false},
	} {
		t.Run(name, func(t *testing.T) {
			e := newConnectorEnv(t, &replies{list: []provider.Response{done()}})
			srv := mcptest.Start(t, mcptest.Options{})
			srv.AddTool("search_pages", &mcp.CallToolResult{})
			user := testdb.NewUser(t, e.pool)
			e.addConnector(t, user.ID, srv, tc.age)
			sid := e.newSession(t, user.Scope())
			waitStatus(t, e.pool, sid, eventlog.StatusAwaitingUser, 1)

			if got := slices.Contains(srv.Methods(), "tools/list"); got != tc.want {
				t.Fatalf("tools/list sent = %v, server saw %v", got, srv.Methods())
			}
		})
	}
}
