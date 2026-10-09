package worker

import (
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/bhanuprakaash/jelly-fish/internal/eventlog"
	"github.com/bhanuprakaash/jelly-fish/internal/msg"
	"github.com/bhanuprakaash/jelly-fish/internal/provider/anthropic"
	"github.com/bhanuprakaash/jelly-fish/internal/provider/catalog"
	"github.com/bhanuprakaash/jelly-fish/internal/provider/gemini"
	"github.com/bhanuprakaash/jelly-fish/internal/provider/openai"
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
	return newFakeAnthropicSSE(t, modelsStatus, []byte(cachedSSE))
}

// cachedSSE is a Haiku text reply that bills every usage class.
const cachedSSE = `event: message_start
data: {"type":"message_start","message":{"id":"msg_2","type":"message","role":"assistant","model":"claude-haiku-4-5-20251001","content":[],"stop_reason":null,"stop_sequence":null,"usage":{"input_tokens":30,"cache_creation_input_tokens":300,"cache_read_input_tokens":2000,"cache_creation":{"ephemeral_5m_input_tokens":100,"ephemeral_1h_input_tokens":200},"output_tokens":1}}}

event: content_block_start
data: {"type":"content_block_start","index":0,"content_block":{"type":"text","text":""}}

event: content_block_delta
data: {"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"Hi."}}

event: content_block_stop
data: {"type":"content_block_stop","index":0}

event: message_delta
data: {"type":"message_delta","delta":{"stop_reason":"end_turn","stop_sequence":null},"usage":{"output_tokens":20}}

event: message_stop
data: {"type":"message_stop"}

`

// newFakeAnthropicSSE is newFakeAnthropic replying with sse to every turn.
func newFakeAnthropicSSE(t *testing.T, modelsStatus int, sse []byte) *fakeAnthropic {
	t.Helper()
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
	return Gateway{Keyring: parseKeyring(t, keyM1), Keys: providerkeys.NewStore(pool), Catalog: c, Adapters: map[string]Adapter{anthropic.Name: anthropic.Client{BaseURL: base, Catalog: c}}}
}

func runWorker(t *testing.T, pool *pgxpool.Pool, gw Gateway) {
	t.Helper()
	ctx, cancel := context.WithCancel(t.Context())
	w := New(pool, gw, nil, stream.NewPGDeltaBus(pool), Lease{TTL: 30 * time.Second, Heartbeat: 10 * time.Second}, eventlog.Upcasters{}, slog.New(slog.DiscardHandler))
	w.titleTimeout = 0
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
	if _, err := eventlog.NewRepo(pool, haiku).CreateSession(t.Context(), user.Scope(), sid, uuid.New(), "find cats", "", false); err != nil {
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

// usageSSE is a Haiku text reply with Usage{Input:100, CacheRead:20, Output:50}.
const usageSSE = `event: message_start
data: {"type":"message_start","message":{"id":"msg_u","type":"message","role":"assistant","model":"claude-haiku-4-5-20251001","content":[],"stop_reason":null,"stop_sequence":null,"usage":{"input_tokens":100,"cache_creation_input_tokens":0,"cache_read_input_tokens":20,"output_tokens":1}}}

event: content_block_start
data: {"type":"content_block_start","index":0,"content_block":{"type":"text","text":""}}

event: content_block_delta
data: {"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"Hi."}}

event: content_block_stop
data: {"type":"content_block_stop","index":0}

event: message_delta
data: {"type":"message_delta","delta":{"stop_reason":"end_turn","stop_sequence":null},"usage":{"output_tokens":50}}

event: message_stop
data: {"type":"message_stop"}

`

func TestUsageIsOneRowPerClassPricedFromTheCatalog(t *testing.T) {
	pool := testdb.NewPool(t)
	api := newFakeAnthropicSSE(t, http.StatusOK, []byte(usageSSE))
	gw := testGateway(t, pool, api.srv.URL)
	user := testdb.NewUser(t, pool)
	insertSealed(t, pool, gw.Keyring, user.ID, anthropic.Name, "sk-ant-users-own")
	sid := uuid.New()
	if _, err := eventlog.NewRepo(pool, haiku).CreateSession(t.Context(), user.Scope(), sid, uuid.New(), "hi", "", false); err != nil {
		t.Fatal(err)
	}
	runWorker(t, pool, gw)
	waitFor(t, pool, sid, eventlog.StatusAwaitingUser)

	// Haiku 4.5 is $1 input, $0.10 cache read, $5 output per million tokens.
	type row struct {
		unit            string
		quantity, micro int64
	}
	want := []row{{"input_tokens", 100, 100}, {"cache_read_tokens", 20, 2}, {"output_tokens", 50, 250}}

	rows, err := pool.Query(t.Context(), `
		SELECT e.payload->>'unit', (e.payload->>'quantity')::bigint, (e.payload->>'cost_micros')::bigint
		FROM events e JOIN usage u ON u.session_id = e.session_id AND u.seq = e.seq
		WHERE e.session_id = $1 AND e.type = $2 AND e.payload->>'provider' = 'anthropic' AND e.payload->>'model' = $3
		  AND u.provider = 'anthropic' AND u.model = $3 AND u.cost_micros = (e.payload->>'cost_micros')::bigint
		ORDER BY e.seq`, sid, eventlog.TypeUsageRecorded, haiku)
	if err != nil {
		t.Fatal(err)
	}
	var got []row
	for rows.Next() {
		var r row
		if err := rows.Scan(&r.unit, &r.quantity, &r.micro); err != nil {
			t.Fatal(err)
		}
		got = append(got, r)
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(got, want) {
		t.Fatalf("usage = %v, want %v", got, want)
	}

	// Rows written in one transaction share its xmin.
	var txs int
	if err := pool.QueryRow(t.Context(), `SELECT count(DISTINCT xmin::text) FROM events WHERE session_id = $1 AND type IN ($2, $3)`,
		sid, eventlog.TypeLLMResponse, eventlog.TypeUsageRecorded).Scan(&txs); err != nil {
		t.Fatal(err)
	}
	if txs != 1 {
		t.Fatalf("llm.response and usage.recorded span %d transactions, want 1", txs)
	}
	var cost int64
	if err := pool.QueryRow(t.Context(), `SELECT cost_micros FROM sessions WHERE id = $1`, sid).Scan(&cost); err != nil {
		t.Fatal(err)
	}
	if cost != 352 {
		t.Fatalf("session cost_micros = %d, want 352", cost)
	}
}

func TestModelOnlyInTheLiveListRunsUnpriced(t *testing.T) {
	pool := testdb.NewPool(t)
	api := newFakeAnthropicSSE(t, http.StatusOK, []byte(usageSSE))
	gw := testGateway(t, pool, api.srv.URL)
	user := testdb.NewUser(t, pool)
	insertSealed(t, pool, gw.Keyring, user.ID, anthropic.Name, "sk-ant-users-own")
	const preview = "claude-next-preview"
	if _, err := pool.Exec(t.Context(), `UPDATE provider_keys SET models = $2 WHERE user_id = $1`,
		user.ID, `[{"id":"`+preview+`","display_name":"Claude Next"}]`); err != nil {
		t.Fatal(err)
	}
	sid := uuid.New()
	if _, err := eventlog.NewRepo(pool, preview).CreateSession(t.Context(), user.Scope(), sid, uuid.New(), "hi", "", false); err != nil {
		t.Fatal(err)
	}
	runWorker(t, pool, gw)
	waitFor(t, pool, sid, eventlog.StatusAwaitingUser)

	var n, cost int64
	if err := pool.QueryRow(t.Context(), `SELECT count(*), coalesce(sum(cost_micros), -1) FROM usage WHERE session_id = $1 AND provider = 'anthropic' AND model = $2`,
		sid, preview).Scan(&n, &cost); err != nil {
		t.Fatal(err)
	}
	if n != 3 || cost != 0 {
		t.Fatalf("usage rows = %d costing %d, want 3 costing 0", n, cost)
	}
}

func TestNextTurnRunsOnTheChangedModel(t *testing.T) {
	pool := testdb.NewPool(t)
	api := newFakeAnthropicSSE(t, http.StatusOK, []byte(usageSSE))
	gw := testGateway(t, pool, api.srv.URL)
	user := testdb.NewUser(t, pool)
	insertSealed(t, pool, gw.Keyring, user.ID, anthropic.Name, "sk-ant-users-own")
	repo := eventlog.NewRepo(pool, haiku)
	sid := uuid.New()
	if _, err := repo.CreateSession(t.Context(), user.Scope(), sid, uuid.New(), "hi", "", false); err != nil {
		t.Fatal(err)
	}
	runWorker(t, pool, gw)
	waitFor(t, pool, sid, eventlog.StatusAwaitingUser)

	const sonnet = "claude-sonnet-5-5"
	if err := repo.ChangeModel(t.Context(), user.Scope(), sid, sonnet); err != nil {
		t.Fatal(err)
	}
	if _, err := repo.PostMessage(t.Context(), user.Scope(), sid, uuid.New(), "again"); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(15 * time.Second)
	var models []string
	for time.Now().Before(deadline) && len(models) < 2 {
		rows, err := pool.Query(t.Context(), `SELECT payload->>'model' FROM events WHERE session_id = $1 AND type = $2 ORDER BY seq`, sid, eventlog.TypeTurnStarted)
		if err != nil {
			t.Fatal(err)
		}
		models, err = pgx.CollectRows(rows, pgx.RowTo[string])
		if err != nil {
			t.Fatal(err)
		}
		time.Sleep(20 * time.Millisecond)
	}
	if !slices.Equal(models, []string{haiku, sonnet}) {
		t.Fatalf("turn.started models = %v, want %v", models, []string{haiku, sonnet})
	}
}

func TestRescuedTurnRunsOnTheChangedModel(t *testing.T) {
	pool := testdb.NewPool(t)
	api := newFakeAnthropicSSE(t, http.StatusOK, []byte(usageSSE))
	gw := testGateway(t, pool, api.srv.URL)
	user := testdb.NewUser(t, pool)
	insertSealed(t, pool, gw.Keyring, user.ID, anthropic.Name, "sk-ant-users-own")
	repo := eventlog.NewRepo(pool, haiku)
	sid := uuid.New()
	if _, err := repo.CreateSession(t.Context(), user.Scope(), sid, uuid.New(), "hi", "", false); err != nil {
		t.Fatal(err)
	}

	// A worker claims, starts a turn on Haiku, and dies before the reply.
	store := eventlog.NewStore(pool)
	c, _, err := store.Claim(t.Context(), "dead", 30*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	_, err = store.AppendFenced(t.Context(), sid, c.Fence, []eventlog.NewEvent{{
		Type: eventlog.TypeTurnStarted, Actor: "worker:dead",
		Payload: map[string]any{"turn_id": "t1", "provider": anthropic.Name, "model": haiku, "input_through_seq": 2},
	}}, nil)
	if err != nil {
		t.Fatal(err)
	}
	const sonnet = "claude-sonnet-5-5"
	if err := repo.ChangeModel(t.Context(), user.Scope(), sid, sonnet); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(t.Context(), `UPDATE sessions SET lease_expires_at = now() - interval '1 second' WHERE id = $1`, sid); err != nil {
		t.Fatal(err)
	}

	runWorker(t, pool, gw)
	waitFor(t, pool, sid, eventlog.StatusAwaitingUser)
	rows, err := pool.Query(t.Context(), `SELECT payload->>'model' FROM events WHERE session_id = $1 AND type = $2 ORDER BY seq`, sid, eventlog.TypeTurnStarted)
	if err != nil {
		t.Fatal(err)
	}
	models, err := pgx.CollectRows(rows, pgx.RowTo[string])
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(models, []string{haiku, sonnet}) {
		t.Fatalf("turn.started models = %v, want %v", models, []string{haiku, sonnet})
	}
}

func TestModelNowhereKnownIsModelUnavailable(t *testing.T) {
	pool := testdb.NewPool(t)
	api := newFakeAnthropic(t, http.StatusOK)
	gw := testGateway(t, pool, api.srv.URL)
	user := testdb.NewUser(t, pool)
	insertSealed(t, pool, gw.Keyring, user.ID, anthropic.Name, "sk-ant-users-own")
	sid := uuid.New()
	if _, err := eventlog.NewRepo(pool, "claude-no-such-model").CreateSession(t.Context(), user.Scope(), sid, uuid.New(), "hi", "", false); err != nil {
		t.Fatal(err)
	}
	runWorker(t, pool, gw)
	waitFor(t, pool, sid, eventlog.StatusAwaitingUser)
	if keys := api.seenKeys(); len(keys) != 0 {
		t.Fatalf("Anthropic was called %d times", len(keys))
	}
	var code string
	if err := pool.QueryRow(t.Context(), `SELECT payload->>'code' FROM events WHERE session_id = $1 AND type = $2`, sid, eventlog.TypeSessionError).Scan(&code); err != nil {
		t.Fatal(err)
	}
	if code != "model_unavailable" {
		t.Fatalf("session.error code = %s, want model_unavailable", code)
	}
}

func TestTurnWithoutAKeyIsKeyInvalid(t *testing.T) {
	pool := testdb.NewPool(t)
	api := newFakeAnthropic(t, http.StatusOK)
	user := testdb.NewUser(t, pool)
	sid := uuid.New()
	if _, err := eventlog.NewRepo(pool, haiku).CreateSession(t.Context(), user.Scope(), sid, uuid.New(), "hi", "", false); err != nil {
		t.Fatal(err)
	}
	runWorker(t, pool, testGateway(t, pool, api.srv.URL))
	waitFor(t, pool, sid, eventlog.StatusAwaitingUser)
	if keys := api.seenKeys(); len(keys) != 0 {
		t.Fatalf("Anthropic was called %d times", len(keys))
	}

	var code string
	var retryable bool
	if err := pool.QueryRow(t.Context(), `SELECT payload->>'code', (payload->>'retryable')::bool FROM events WHERE session_id = $1 AND type = $2`, sid, eventlog.TypeSessionError).Scan(&code, &retryable); err != nil {
		t.Fatal(err)
	}
	if code != "key_invalid" || retryable {
		t.Fatalf("session.error = %s retryable=%v, want key_invalid, not retryable", code, retryable)
	}
}

func TestRefreshModelsUpdatesEachKeysList(t *testing.T) {
	pool := testdb.NewPool(t)
	ok := newFakeAnthropic(t, http.StatusOK)
	gw := testGateway(t, pool, ok.srv.URL)
	good, bad := testdb.NewUser(t, pool), testdb.NewUser(t, pool)
	insertSealed(t, pool, gw.Keyring, good.ID, anthropic.Name, "sk-ant-good")
	oai := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"object":"list","data":[{"id":"gpt-6.1-sol","object":"model","created":1,"owned_by":"openai"}]}`))
	}))
	t.Cleanup(oai.Close)
	gw.Adapters[openai.Name] = openai.Client{BaseURL: oai.URL}
	insertSealed(t, pool, gw.Keyring, good.ID, openai.Name, "sk-oai-good")
	goog := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"models":[{"name":"models/gemini-3.8-flash"}]}`))
	}))
	t.Cleanup(goog.Close)
	gw.Adapters[gemini.Name] = gemini.Client{BaseURL: goog.URL}
	insertSealed(t, pool, gw.Keyring, good.ID, gemini.Name, "AIza-good")
	// A row sealed for another user fails to open and is left alone.
	ct, keyID, err := gw.Keyring.Seal([]byte("sk-ant-bad"), []byte("someone-else|anthropic"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(t.Context(), `INSERT INTO provider_keys (user_id, provider, ciphertext, key_id, last4, models, models_fetched_at)
		VALUES ($1, 'anthropic', $2, $3, 'last', '[]', now() - interval '2 days')`, bad.ID, ct, keyID); err != nil {
		t.Fatal(err)
	}

	n, err := RefreshModels(t.Context(), gw.Keys, gw.Keyring, gw.Adapters)
	if n != 3 || err == nil {
		t.Fatalf("RefreshModels = %d, %v; want 3 and the bad row's error", n, err)
	}
	if strings.Contains(err.Error(), "sk-ant") {
		t.Fatalf("error carries a key: %v", err)
	}
	if keys := ok.seenKeys(); len(keys) != 1 || keys[0] != "sk-ant-good" {
		t.Fatalf("models list called with %q", keys)
	}

	var models string
	var fresh bool
	for prov, want := range map[string]string{anthropic.Name: haiku, openai.Name: "gpt-6.1-sol", gemini.Name: "gemini-3.8-flash"} {
		if err := pool.QueryRow(t.Context(), `SELECT models::text, models_fetched_at > now() - interval '1 minute' FROM provider_keys WHERE user_id = $1 AND provider = $2`, good.ID, prov).Scan(&models, &fresh); err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(models, want) || !fresh {
			t.Fatalf("good %s row: models %s, fresh %v", prov, models, fresh)
		}
	}
	if err := pool.QueryRow(t.Context(), `SELECT models::text, models_fetched_at > now() - interval '1 minute' FROM provider_keys WHERE user_id = $1`, bad.ID).Scan(&models, &fresh); err != nil {
		t.Fatal(err)
	}
	if models != "[]" || fresh {
		t.Fatalf("bad row was touched: models %s, fresh %v", models, fresh)
	}
}

func TestPickRoutesEachModelToItsProvider(t *testing.T) {
	pool := testdb.NewPool(t)
	gw := testGateway(t, pool, "http://unused")
	gw.Adapters[openai.Name] = openai.Client{}
	gw.Adapters[gemini.Name] = gemini.Client{}
	u := testdb.NewUser(t, pool)
	insertSealed(t, pool, gw.Keyring, u.ID, anthropic.Name, "sk-ant-x")
	insertSealed(t, pool, gw.Keyring, u.ID, openai.Name, "sk-oai-x")
	insertSealed(t, pool, gw.Keyring, u.ID, gemini.Name, "AIza-x")
	for model, want := range map[string]string{haiku: anthropic.Name, "gpt-6-luna": openai.Name, "gemini-3.8-flash": gemini.Name} {
		p, err := gw.pick(t.Context(), u.ID, model)
		if err != nil || p.Name() != want {
			t.Fatalf("pick(%s) = %v, %v; want %s", model, p, err, want)
		}
	}
}
