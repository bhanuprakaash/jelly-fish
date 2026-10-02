package worker

import (
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/bhanuprakaash/jelly-fish/internal/eventlog"
	"github.com/bhanuprakaash/jelly-fish/internal/msg"
	"github.com/bhanuprakaash/jelly-fish/internal/provider/anthropic"
	"github.com/bhanuprakaash/jelly-fish/internal/provider/catalog"
	"github.com/bhanuprakaash/jelly-fish/internal/providerkeys"
	"github.com/bhanuprakaash/jelly-fish/internal/stream"
	"github.com/bhanuprakaash/jelly-fish/internal/testdb"
)

const haiku = "claude-haiku-4-5-20251001"

// fakeAnthropic serves the adapter's Haiku SSE fixture and the models list,
// and records the x-api-key of every call.
type fakeAnthropic struct {
	srv  *httptest.Server
	mu   sync.Mutex
	keys []string
}

func newFakeAnthropic(t *testing.T, modelsStatus int) *fakeAnthropic {
	t.Helper()
	sse, err := os.ReadFile("../provider/anthropic/testdata/tool_use.sse")
	if err != nil {
		t.Fatal(err)
	}
	f := &fakeAnthropic{}
	f.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		f.mu.Lock()
		f.keys = append(f.keys, r.Header.Get("x-api-key"))
		f.mu.Unlock()
		if r.URL.Path == "/v1/models" {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(modelsStatus)
			_, _ = w.Write([]byte(`{"data":[{"type":"model","id":"claude-haiku-4-5-20251001","display_name":"Claude Haiku 4.5","created_at":"2025-10-01T00:00:00Z","max_input_tokens":200000,"max_tokens":64000}],"has_more":false}`))
			return
		}
		w.Header().Set("Content-Type", "text/event-stream")
		w.Header().Set("Request-Id", "req_test")
		_, _ = w.Write(sse)
	}))
	t.Cleanup(f.srv.Close)
	return f
}

func (f *fakeAnthropic) seenKeys() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.keys...)
}

func testGateway(t *testing.T, pool *pgxpool.Pool, base string) Gateway {
	t.Helper()
	c, err := catalog.Load()
	if err != nil {
		t.Fatal(err)
	}
	return Gateway{Keyring: parseKeyring(t, keyM1), Keys: providerkeys.NewStore(pool), Catalog: c, Anthropic: anthropic.Client{BaseURL: base, Catalog: c}}
}

func runWorker(t *testing.T, pool *pgxpool.Pool, gw Gateway) {
	t.Helper()
	ctx, cancel := context.WithCancel(t.Context())
	w := New(pool, gw, stream.NewPGDeltaBus(pool), Lease{TTL: 30 * time.Second, Heartbeat: 10 * time.Second}, eventlog.Upcasters{}, slog.New(slog.DiscardHandler))
	done := make(chan struct{})
	go func() { w.Run(ctx); close(done) }()
	t.Cleanup(func() { cancel(); <-done })
}

func waitFor(t *testing.T, pool *pgxpool.Pool, sid uuid.UUID, want string) {
	t.Helper()
	deadline := time.Now().Add(15 * time.Second)
	var status string
	for time.Now().Before(deadline) {
		if err := pool.QueryRow(t.Context(), `SELECT status FROM sessions WHERE id = $1`, sid).Scan(&status); err != nil {
			t.Fatal(err)
		}
		if status == want {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("session status %s, want %s", status, want)
}

func TestTurnRunsOnTheUsersAnthropicKey(t *testing.T) {
	pool := testdb.NewPool(t)
	api := newFakeAnthropic(t, http.StatusOK)
	gw := testGateway(t, pool, api.srv.URL)
	user := testdb.NewUser(t, pool)
	insertSealed(t, pool, gw.Keyring, user.ID, anthropic.Name, "sk-ant-users-own")

	sid := uuid.New()
	if _, err := eventlog.NewRepo(pool, haiku).CreateSession(t.Context(), user.Scope(), sid, uuid.New(), "find cats"); err != nil {
		t.Fatal(err)
	}
	runWorker(t, pool, gw)
	waitFor(t, pool, sid, eventlog.StatusAwaitingUser)

	if keys := api.seenKeys(); len(keys) != 1 || keys[0] != "sk-ant-users-own" {
		t.Fatalf("Anthropic saw keys %q, want the user's own once", keys)
	}

	var raw []byte
	if err := pool.QueryRow(t.Context(), `SELECT payload FROM events WHERE session_id = $1 AND type = $2`, sid, eventlog.TypeLLMResponse).Scan(&raw); err != nil {
		t.Fatal(err)
	}
	var stored struct {
		Message struct {
			MsgV int `json:"msg_v"`
		} `json:"message"`
	}
	if err := json.Unmarshal(raw, &stored); err != nil {
		t.Fatal(err)
	}
	if stored.Message.MsgV != msg.CurrentVersion {
		t.Fatalf("stored llm.response msg_v = %d, want %d: %s", stored.Message.MsgV, msg.CurrentVersion, raw)
	}
	if strings.Contains(string(raw), "sk-ant-users-own") {
		t.Fatal("llm.response carries the key")
	}

	rows, err := pool.Query(t.Context(), `SELECT unit, quantity::bigint FROM usage WHERE session_id = $1 AND provider = 'anthropic' AND model = $2 ORDER BY id`, sid, haiku)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	got := map[string]int64{}
	for rows.Next() {
		var unit string
		var n int64
		if err := rows.Scan(&unit, &n); err != nil {
			t.Fatal(err)
		}
		got[unit] = n
	}
	want := map[string]int64{"input_tokens": 30, "cache_read_tokens": 2000, "cache_write_5m_tokens": 100, "cache_write_1h_tokens": 200, "output_tokens": 20}
	if len(got) != len(want) {
		t.Fatalf("usage = %v, want %v", got, want)
	}
	for u, n := range want {
		if got[u] != n {
			t.Errorf("usage %s = %d, want %d", u, got[u], n)
		}
	}
}

func TestTurnWithoutAKeyFailsTheSession(t *testing.T) {
	pool := testdb.NewPool(t)
	api := newFakeAnthropic(t, http.StatusOK)
	user := testdb.NewUser(t, pool)
	sid := uuid.New()
	if _, err := eventlog.NewRepo(pool, haiku).CreateSession(t.Context(), user.Scope(), sid, uuid.New(), "hi"); err != nil {
		t.Fatal(err)
	}
	runWorker(t, pool, testGateway(t, pool, api.srv.URL))
	waitFor(t, pool, sid, eventlog.StatusFailed)
	if keys := api.seenKeys(); len(keys) != 0 {
		t.Fatalf("Anthropic was called %d times", len(keys))
	}
}

func TestRefreshModelsUpdatesEachKeysList(t *testing.T) {
	pool := testdb.NewPool(t)
	ok := newFakeAnthropic(t, http.StatusOK)
	gw := testGateway(t, pool, ok.srv.URL)
	good, bad := testdb.NewUser(t, pool), testdb.NewUser(t, pool)
	insertSealed(t, pool, gw.Keyring, good.ID, anthropic.Name, "sk-ant-good")
	// A row sealed for another user fails to open and is left alone.
	ct, keyID, err := gw.Keyring.Seal([]byte("sk-ant-bad"), []byte("someone-else|anthropic"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(t.Context(), `INSERT INTO provider_keys (user_id, provider, ciphertext, key_id, last4, models, models_fetched_at)
		VALUES ($1, 'anthropic', $2, $3, 'last', '[]', now() - interval '2 days')`, bad.ID, ct, keyID); err != nil {
		t.Fatal(err)
	}

	n, err := RefreshModels(t.Context(), pool, gw.Keyring, gw.Anthropic)
	if n != 1 || err == nil {
		t.Fatalf("RefreshModels = %d, %v; want 1 and the bad row's error", n, err)
	}
	if strings.Contains(err.Error(), "sk-ant") {
		t.Fatalf("error carries a key: %v", err)
	}
	if keys := ok.seenKeys(); len(keys) != 1 || keys[0] != "sk-ant-good" {
		t.Fatalf("models list called with %q", keys)
	}

	var models string
	var fresh bool
	if err := pool.QueryRow(t.Context(), `SELECT models::text, models_fetched_at > now() - interval '1 minute' FROM provider_keys WHERE user_id = $1`, good.ID).Scan(&models, &fresh); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(models, haiku) || !fresh {
		t.Fatalf("good row: models %s, fresh %v", models, fresh)
	}
	if err := pool.QueryRow(t.Context(), `SELECT models::text, models_fetched_at > now() - interval '1 minute' FROM provider_keys WHERE user_id = $1`, bad.ID).Scan(&models, &fresh); err != nil {
		t.Fatal(err)
	}
	if models != "[]" || fresh {
		t.Fatalf("bad row was touched: models %s, fresh %v", models, fresh)
	}
}
