package api

import (
	"encoding/json"
	"log/slog"
	"net/http"
	"strings"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/bhanuprakaash/jelly-fish/internal/auth"
	"github.com/bhanuprakaash/jelly-fish/internal/connector"
	"github.com/bhanuprakaash/jelly-fish/internal/eventlog"
	"github.com/bhanuprakaash/jelly-fish/internal/keyring"
	"github.com/bhanuprakaash/jelly-fish/internal/mcpclient"
	"github.com/bhanuprakaash/jelly-fish/internal/mcpclient/mcptest"
	"github.com/bhanuprakaash/jelly-fish/internal/stream"
	"github.com/bhanuprakaash/jelly-fish/internal/testdb"
)

type connectorsEnv struct {
	*loginEnv
	cookie *http.Cookie
	server *mcptest.Server
}

func newConnectorsEnv(t *testing.T) *connectorsEnv {
	t.Helper()
	pool := testdb.NewPool(t)
	kr, err := keyring.Parse("m1:AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA=")
	if err != nil {
		t.Fatal(err)
	}
	store := auth.NewStore(pool, slog.New(slog.DiscardHandler))
	srv, authH := newServer(":0", slog.New(slog.DiscardHandler), testWebFS(), eventlog.NewRepo(pool, "fake"), stream.NewHub(), &fakeDeltaBus{}, newTestMetrics(t),
		AuthConfig{
			Authenticator: store, Admin: store, Mailer: &fakeMailer{}, PublicURL: testPublicURL,
			Connectors: ConnectorConfig{Store: connector.NewStore(pool), Client: mcpclient.New(true), Sealer: kr},
		})
	e := &connectorsEnv{loginEnv: &loginEnv{pool: pool, srv: srv, auth: authH, mailer: authH.cfg.Mailer.(*fakeMailer)}, server: mcptest.Start(t, mcptest.Options{})}
	e.server.AddTool("search_pages", &mcp.CallToolResult{})
	e.server.AddTool("create_page", &mcp.CallToolResult{})
	e.cookie = e.signInUser(t, testdb.NewUser(t, pool))
	return e
}

func TestConnectorLifecycle(t *testing.T) {
	e := newConnectorsEnv(t)

	probe := e.do(http.MethodPost, "/api/connectors/probe", map[string]any{"url": e.server.URL}, e.cookie)
	if probe.Code != http.StatusOK || !strings.Contains(probe.Body.String(), `"name":"search_pages"`) || !strings.Contains(probe.Body.String(), `"era":"modern"`) {
		t.Fatalf("probe = %d %s", probe.Code, probe.Body)
	}

	create := e.do(http.MethodPost, "/api/connectors", map[string]any{
		"name": "Notion", "url": e.server.URL, "auth_header": "X-API-Key", "secret": "s3cret", "era": "modern",
		"tools": []map[string]any{{"name": "search_pages", "enabled": true}, {"name": "create_page", "enabled": false}},
	}, e.cookie)
	if create.Code != http.StatusCreated {
		t.Fatalf("create = %d %s", create.Code, create.Body)
	}
	var created struct {
		ID    string `json:"id"`
		Slug  string `json:"slug"`
		Tools []struct {
			Name    string `json:"name"`
			Enabled bool   `json:"enabled"`
		} `json:"tools"`
	}
	if err := json.Unmarshal(create.Body.Bytes(), &created); err != nil || created.Slug != "notion" {
		t.Fatalf("created = %+v, %v", created, err)
	}

	list := e.do(http.MethodGet, "/api/connectors", nil, e.cookie).Body.String()
	for _, want := range []string{`"slug":"notion"`, `"auth_header":"X-API-Key"`, `"name":"create_page","description":"","enabled":false`} {
		if !strings.Contains(list, want) {
			t.Errorf("list lacks %s: %s", want, list)
		}
	}
	if strings.Contains(list, "s3cret") {
		t.Fatalf("list leaks the secret: %s", list)
	}

	if rr := e.do(http.MethodPost, "/api/connectors", map[string]any{"name": "Mine", "slug": "notion", "url": e.server.URL, "era": "modern"}, e.cookie); rr.Code != http.StatusConflict {
		t.Errorf("taken slug = %d, want 409", rr.Code)
	}
	if rr := e.do(http.MethodPatch, "/api/connectors/"+created.ID+"/tools/create_page", map[string]bool{"enabled": true}, e.cookie); rr.Code != http.StatusNoContent {
		t.Errorf("enable tool = %d", rr.Code)
	}
	if list := e.do(http.MethodGet, "/api/connectors", nil, e.cookie).Body.String(); strings.Contains(list, `"enabled":false`) {
		t.Errorf("tool still disabled: %s", list)
	}

	if rr := e.do(http.MethodDelete, "/api/connectors/"+created.ID, nil, e.cookie); rr.Code != http.StatusNoContent {
		t.Fatalf("delete = %d", rr.Code)
	}
	if got := strings.TrimSpace(e.do(http.MethodGet, "/api/connectors", nil, e.cookie).Body.String()); got != "[]" {
		t.Errorf("list after delete = %s", got)
	}
	for _, table := range []string{"connector_credentials", "connector_tools"} {
		if n := countRows(t, e.pool, table); n != 0 {
			t.Errorf("%s rows after delete = %d", table, n)
		}
	}
}

func TestProbeRefusesAddressesTheGuardRejects(t *testing.T) {
	e := newConnectorsEnv(t)
	rr := e.do(http.MethodPost, "/api/connectors/probe", map[string]any{"url": "http://10.0.0.1/mcp"}, e.cookie)
	if rr.Code != http.StatusUnprocessableEntity || !strings.Contains(rr.Body.String(), "This address can't be used") {
		t.Fatalf("probe = %d %s", rr.Code, rr.Body)
	}
}

func TestConnectorsAreScopedToTheCallersProject(t *testing.T) {
	e := newConnectorsEnv(t)
	mine := e.do(http.MethodPost, "/api/connectors", map[string]any{"name": "Notion", "url": e.server.URL, "era": "modern"}, e.cookie)
	var created struct {
		ID string `json:"id"`
	}
	if err := json.Unmarshal(mine.Body.Bytes(), &created); err != nil {
		t.Fatal(err)
	}
	other := e.signInUser(t, testdb.NewUser(t, e.pool))
	if got := strings.TrimSpace(e.do(http.MethodGet, "/api/connectors", nil, other).Body.String()); got != "[]" {
		t.Errorf("another user lists %s", got)
	}
	if rr := e.do(http.MethodDelete, "/api/connectors/"+created.ID, nil, other); rr.Code != http.StatusNotFound {
		t.Errorf("another user's delete = %d, want 404", rr.Code)
	}
}

func TestProbeRejectsAnAuthHeaderItCannotSend(t *testing.T) {
	e := newConnectorsEnv(t)
	for name, c := range map[string]struct{ header, secret, want string }{
		"no secret":       {"X-API-Key", "", "secret is required with auth_header"},
		"not a token":     {"X Key", "s3cret", "auth_header is not allowed"},
		"reserved header": {"content-type", "s3cret", "auth_header is not allowed"},
	} {
		t.Run(name, func(t *testing.T) {
			rr := e.do(http.MethodPost, "/api/connectors/probe", map[string]any{"url": e.server.URL, "auth_header": c.header, "secret": c.secret}, e.cookie)
			if rr.Code != http.StatusBadRequest || !strings.Contains(rr.Body.String(), c.want) {
				t.Fatalf("probe = %d %s, want 400 %q", rr.Code, rr.Body, c.want)
			}
		})
	}
}
