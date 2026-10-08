package api

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/bhanuprakaash/jelly-fish/internal/auth"
	"github.com/bhanuprakaash/jelly-fish/internal/eventlog"
	"github.com/bhanuprakaash/jelly-fish/internal/keyring"
	"github.com/bhanuprakaash/jelly-fish/internal/provider"
	"github.com/bhanuprakaash/jelly-fish/internal/provider/catalog"
	"github.com/bhanuprakaash/jelly-fish/internal/provider/gemini"
	"github.com/bhanuprakaash/jelly-fish/internal/providerkeys"
	"github.com/bhanuprakaash/jelly-fish/internal/stream"
	"github.com/bhanuprakaash/jelly-fish/internal/testdb"
)

const canaryKey = "sk-ant-CANARY-7f3a9c1e5d"

type fakeLister struct {
	err   error
	empty bool
	mu    sync.Mutex
	calls []string
}

func (f *fakeLister) ListModels(_ context.Context, key string) ([]provider.Model, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls = append(f.calls, key)
	if f.err != nil {
		return nil, f.err
	}
	if f.empty {
		return nil, nil
	}
	return []provider.Model{{ID: "claude-haiku-4-5", DisplayName: "Claude Haiku 4.5"}}, nil
}

// geminiModelsServer is a fake Gemini models endpoint: it rejects the key
// "AIza-bad" the way Gemini does, a 400 naming API_KEY_INVALID.
func geminiModelsServer(t *testing.T) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if r.Header.Get("X-Goog-Api-Key") == "AIza-bad" {
			w.WriteHeader(http.StatusBadRequest)
			_, _ = w.Write([]byte(`{"error":{"code":400,"message":"API key not valid.","status":"INVALID_ARGUMENT","details":[{"@type":"type.googleapis.com/google.rpc.ErrorInfo","reason":"API_KEY_INVALID"}]}}`))
			return
		}
		_, _ = w.Write([]byte(`{"models":[{"name":"models/gemini-embedding-001"},{"name":"models/gemini-3.8-flash","displayName":"Gemini 3.8 Flash"}]}`))
	}))
	t.Cleanup(srv.Close)
	return srv
}

type keysEnv struct {
	*loginEnv
	lister *fakeLister
	openai *fakeLister
	logs   *bytes.Buffer
	kr     *keyring.Keyring
	user   auth.User
	cookie *http.Cookie
}

func newKeysEnv(t *testing.T) *keysEnv {
	t.Helper()
	pool := testdb.NewPool(t)
	kr, err := keyring.Parse("m1:AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA=")
	if err != nil {
		t.Fatal(err)
	}
	logs := &bytes.Buffer{}
	logger := slog.New(slog.NewJSONHandler(logs, &slog.HandlerOptions{Level: slog.LevelDebug}))
	lister, openai := &fakeLister{}, &fakeLister{}
	gem := gemini.Client{BaseURL: geminiModelsServer(t).URL}
	cat, err := catalog.Load()
	if err != nil {
		t.Fatal(err)
	}
	store := auth.NewStore(pool, logger)
	srv, authH := newServer(":0", logger, testWebFS(), eventlog.NewRepo(pool, "fake"), stream.NewHub(), &fakeDeltaBus{}, newTestMetrics(t),
		AuthConfig{
			Authenticator: store, Admin: store, Mailer: &fakeMailer{}, PublicURL: testPublicURL,
			ProviderKeys: ProviderKeyConfig{Store: providerkeys.NewStore(pool), Sealer: kr, Models: map[string]ModelLister{"anthropic": lister, "openai": openai, "gemini": gem}},
			Models:       ModelConfig{Lists: providerkeys.NewStore(pool), Catalog: cat, Default: "fake", Fake: "fake"},
		})
	e := &keysEnv{loginEnv: &loginEnv{pool: pool, srv: srv, auth: authH, mailer: authH.cfg.Mailer.(*fakeMailer)}, lister: lister, openai: openai, logs: logs, kr: kr}
	e.user = testdb.NewUser(t, pool)
	e.cookie = e.signInUser(t, e.user)
	return e
}

func (e *keysEnv) rowCount(t *testing.T) int {
	t.Helper()
	return countRows(t, e.pool, "provider_keys")
}

func countRows(t *testing.T, pool *pgxpool.Pool, table string) int {
	t.Helper()
	var n int
	if err := pool.QueryRow(t.Context(), "SELECT count(*) FROM "+table).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

func TestProviderKeyLifecycle(t *testing.T) {
	e := newKeysEnv(t)

	rr := e.do(http.MethodPut, "/api/provider-keys/anthropic", map[string]string{"key": "  " + canaryKey + "\n"}, e.cookie)
	if rr.Code != http.StatusOK {
		t.Fatalf("PUT status = %d, body %s", rr.Code, rr.Body)
	}
	if got := e.lister.calls; len(got) != 1 || got[0] != canaryKey {
		t.Errorf("lister saw %q, want the trimmed key", got)
	}

	var ct []byte
	var keyID, last4, models string
	err := e.pool.QueryRow(t.Context(), `SELECT ciphertext, key_id, last4, models::text FROM provider_keys WHERE user_id = $1`, e.user.ID).
		Scan(&ct, &keyID, &last4, &models)
	if err != nil {
		t.Fatal(err)
	}
	if keyID != "m1" || last4 != "1e5d" || !strings.Contains(models, "claude-haiku-4-5") {
		t.Errorf("row = key_id %q last4 %q models %s", keyID, last4, models)
	}
	pt, err := e.kr.Open(ct, keyID, providerkeys.AAD(e.user.ID, "anthropic"))
	if err != nil || string(pt) != canaryKey {
		t.Fatalf("stored key does not open to the plaintext: %v", err)
	}

	list := e.do(http.MethodGet, "/api/provider-keys", nil, e.cookie)
	body := strings.TrimSpace(list.Body.String())
	if list.Code != http.StatusOK || !strings.Contains(body, `"provider":"anthropic"`) || !strings.Contains(body, `"last4":"1e5d"`) || !strings.Contains(body, `"updated_at"`) {
		t.Errorf("GET = %d %s", list.Code, body)
	}
	if strings.Contains(body, "ciphertext") || strings.Contains(body, "models") {
		t.Errorf("GET leaks stored fields: %s", body)
	}

	if rr := e.do(http.MethodDelete, "/api/provider-keys/anthropic", nil, e.cookie); rr.Code != http.StatusNoContent {
		t.Errorf("DELETE status = %d", rr.Code)
	}
	if rr := e.do(http.MethodDelete, "/api/provider-keys/anthropic", nil, e.cookie); rr.Code != http.StatusNotFound {
		t.Errorf("second DELETE status = %d, want 404", rr.Code)
	}
	if n := e.rowCount(t); n != 0 {
		t.Errorf("rows after delete = %d", n)
	}
}

func TestPutProviderKeyChecksTheKeyWithItsOwnProvider(t *testing.T) {
	e := newKeysEnv(t)
	if rr := e.do(http.MethodPut, "/api/provider-keys/openai", map[string]string{"key": "sk-proj-abcd"}, e.cookie); rr.Code != http.StatusOK {
		t.Fatalf("PUT status = %d, body %s", rr.Code, rr.Body)
	}
	if len(e.openai.calls) != 1 || len(e.lister.calls) != 0 {
		t.Fatalf("openai lister calls %q, anthropic %q", e.openai.calls, e.lister.calls)
	}
	var p string
	if err := e.pool.QueryRow(t.Context(), `SELECT provider FROM provider_keys WHERE user_id = $1`, e.user.ID).Scan(&p); err != nil || p != "openai" {
		t.Fatalf("stored provider = %q, %v", p, err)
	}
}

func TestPutGeminiKeyChecksItAgainstTheModelsEndpoint(t *testing.T) {
	e := newKeysEnv(t)
	if rr := e.do(http.MethodPut, "/api/provider-keys/gemini", map[string]string{"key": "AIza-bad"}, e.cookie); rr.Code != http.StatusUnprocessableEntity || e.rowCount(t) != 0 {
		t.Fatalf("bad key: PUT = %d, rows %d; want 422 and none", rr.Code, e.rowCount(t))
	}

	if rr := e.do(http.MethodPut, "/api/provider-keys/gemini", map[string]string{"key": "AIza-good"}, e.cookie); rr.Code != http.StatusOK {
		t.Fatalf("PUT status = %d, body %s", rr.Code, rr.Body)
	}
	var models string
	if err := e.pool.QueryRow(t.Context(), `SELECT models::text FROM provider_keys WHERE user_id = $1 AND provider = 'gemini'`, e.user.ID).Scan(&models); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(models, `"gemini-3.8-flash"`) || !strings.Contains(models, `"gemini-embedding-001"`) || strings.Contains(models, "models/") {
		t.Errorf("stored models = %s, want the full list without the models/ prefix", models)
	}

	g := e.models(t).Providers[2]
	if g.Provider != "gemini" || !g.Available || len(g.Models) != 1 || g.Models[0].ID != "gemini-3.8-flash" {
		t.Fatalf("picker = %+v, want only gemini-3.8-flash", g)
	}
}

func TestPutProviderKeyStoresEmptyModelListAsArray(t *testing.T) {
	e := newKeysEnv(t)
	e.lister.empty = true
	if rr := e.do(http.MethodPut, "/api/provider-keys/anthropic", map[string]string{"key": canaryKey}, e.cookie); rr.Code != http.StatusOK {
		t.Fatalf("PUT status = %d, body %s", rr.Code, rr.Body)
	}
	var models string
	if err := e.pool.QueryRow(t.Context(), `SELECT models::text FROM provider_keys WHERE user_id = $1`, e.user.ID).Scan(&models); err != nil {
		t.Fatal(err)
	}
	if models != "[]" {
		t.Errorf("models = %s, want []", models)
	}
}

func TestPutProviderKeyReplacesTheExistingKey(t *testing.T) {
	e := newKeysEnv(t)
	put := func(key string) {
		t.Helper()
		if rr := e.do(http.MethodPut, "/api/provider-keys/anthropic", map[string]string{"key": key}, e.cookie); rr.Code != http.StatusOK {
			t.Fatalf("PUT status = %d, body %s", rr.Code, rr.Body)
		}
	}
	read := func() (ct []byte, last4 string, updated time.Time) {
		t.Helper()
		err := e.pool.QueryRow(t.Context(), `SELECT ciphertext, last4, updated_at FROM provider_keys WHERE user_id = $1`, e.user.ID).Scan(&ct, &last4, &updated)
		if err != nil {
			t.Fatal(err)
		}
		return ct, last4, updated
	}
	put(canaryKey)
	ct1, last1, upd1 := read()
	put("sk-ant-SECOND-0000aaaa")
	ct2, last2, upd2 := read()

	if last1 != "1e5d" || last2 != "aaaa" {
		t.Errorf("last4 = %q then %q, want 1e5d then aaaa", last1, last2)
	}
	if bytes.Equal(ct1, ct2) {
		t.Error("ciphertext unchanged after replace")
	}
	if !upd2.After(upd1) {
		t.Errorf("updated_at did not advance: %v then %v", upd1, upd2)
	}
	if n := e.rowCount(t); n != 1 {
		t.Errorf("rows = %d, want 1", n)
	}
}

func TestPutProviderKeyWritesNothingOnFailure(t *testing.T) {
	tests := []struct {
		name       string
		err        error
		wantStatus int
		wantError  string
	}{
		{"rejected", provider.ErrKeyRejected, http.StatusUnprocessableEntity, "Key rejected"},
		{"provider unreachable", errors.New("boom"), http.StatusBadGateway, "could not reach provider"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			e := newKeysEnv(t)
			e.lister.err = tt.err
			rr := e.do(http.MethodPut, "/api/provider-keys/anthropic", map[string]string{"key": canaryKey}, e.cookie)
			if rr.Code != tt.wantStatus || !strings.Contains(rr.Body.String(), tt.wantError) {
				t.Errorf("response = %d %s", rr.Code, rr.Body)
			}
			if n := e.rowCount(t); n != 0 {
				t.Errorf("rows = %d, want 0", n)
			}
		})
	}
}

func TestPutProviderKeyRejectsBadRequests(t *testing.T) {
	tests := []struct {
		name, path string
		body       map[string]string
		want       int
	}{
		{"unsupported provider", "/api/provider-keys/mistral", map[string]string{"key": "sk-x"}, http.StatusNotFound},
		{"empty key", "/api/provider-keys/anthropic", map[string]string{"key": "  "}, http.StatusBadRequest},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			e := newKeysEnv(t)
			if rr := e.do(http.MethodPut, tt.path, tt.body, e.cookie); rr.Code != tt.want {
				t.Errorf("status = %d, want %d", rr.Code, tt.want)
			}
			if len(e.lister.calls) != 0 || e.rowCount(t) != 0 {
				t.Error("bad request reached the provider or the table")
			}
		})
	}
}

func TestProviderKeysAreScopedToTheUser(t *testing.T) {
	e := newKeysEnv(t)
	e.do(http.MethodPut, "/api/provider-keys/anthropic", map[string]string{"key": canaryKey}, e.cookie)

	other := testdb.NewUser(t, e.pool)
	otherCookie := e.signInUser(t, other)
	if rr := e.do(http.MethodGet, "/api/provider-keys", nil, otherCookie); strings.TrimSpace(rr.Body.String()) != "[]" {
		t.Errorf("other user's list = %s, want []", rr.Body)
	}
	if rr := e.do(http.MethodDelete, "/api/provider-keys/anthropic", nil, otherCookie); rr.Code != http.StatusNotFound {
		t.Errorf("other user's DELETE = %d, want 404", rr.Code)
	}
	if e.rowCount(t) != 1 {
		t.Error("other user deleted the row")
	}
}

func TestCanaryKeyNeverLeaks(t *testing.T) {
	e := newKeysEnv(t)
	var bodies []string
	record := func(method, path string, body any, wantStatus int) {
		rr := e.do(method, path, body, e.cookie)
		if rr.Code != wantStatus {
			t.Fatalf("%s %s = %d, body %s", method, path, rr.Code, rr.Body)
		}
		bodies = append(bodies, rr.Body.String())
	}
	put := map[string]string{"key": canaryKey}
	record(http.MethodPut, "/api/provider-keys/anthropic", put, http.StatusOK)
	record(http.MethodGet, "/api/provider-keys", nil, http.StatusOK)
	record(http.MethodPut, "/api/provider-keys/anthropic", put, http.StatusOK)
	e.lister.err = provider.ErrKeyRejected
	record(http.MethodPut, "/api/provider-keys/anthropic", put, http.StatusUnprocessableEntity)
	e.lister.err = errors.New("upstream down")
	record(http.MethodPut, "/api/provider-keys/anthropic", put, http.StatusBadGateway)
	record(http.MethodDelete, "/api/provider-keys/anthropic", nil, http.StatusNoContent)
	record(http.MethodPut, "/api/provider-keys/anthropic", put, http.StatusBadGateway)
	e.lister.err = nil
	record(http.MethodPut, "/api/provider-keys/anthropic", put, http.StatusOK)

	for _, b := range bodies {
		if strings.Contains(b, canaryKey) {
			t.Errorf("response body leaks the key: %s", b)
		}
	}
	if strings.Contains(e.logs.String(), canaryKey) {
		t.Errorf("logs leak the key:\n%s", e.logs)
	}
	var stored int
	err := e.pool.QueryRow(t.Context(), `SELECT count(*) FROM provider_keys WHERE position(convert_to($1::text, 'UTF8') in ciphertext) > 0`, canaryKey).Scan(&stored)
	if err != nil || stored != 0 {
		t.Errorf("ciphertext contains the key: %d rows, err %v", stored, err)
	}
	err = e.pool.QueryRow(t.Context(), `SELECT count(*) FROM events WHERE payload::text LIKE '%'||$1||'%'`, canaryKey).Scan(&stored)
	if err != nil || stored != 0 {
		t.Errorf("event payloads contain the key: %d rows, err %v", stored, err)
	}
}
