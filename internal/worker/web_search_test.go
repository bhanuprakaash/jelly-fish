package worker

import (
	"net/http"
	"slices"
	"testing"

	"github.com/google/uuid"

	"github.com/bhanuprakaash/jelly-fish/internal/eventlog"
	"github.com/bhanuprakaash/jelly-fish/internal/provider/anthropic"
	"github.com/bhanuprakaash/jelly-fish/internal/provider/catalog"
	"github.com/bhanuprakaash/jelly-fish/internal/testdb"
)

// searchSSE is a Haiku reply that ran one web search, hand-written from
// Anthropic's documented stream format.
const searchSSE = `event: message_start
data: {"type":"message_start","message":{"id":"msg_3","type":"message","role":"assistant","model":"claude-haiku-4-5-20251001","content":[],"stop_reason":null,"stop_sequence":null,"usage":{"input_tokens":100,"cache_creation_input_tokens":0,"cache_read_input_tokens":0,"output_tokens":1}}}

event: content_block_start
data: {"type":"content_block_start","index":0,"content_block":{"type":"server_tool_use","id":"srvtoolu_01A","name":"web_search","input":{"query":"weather in Paris"}}}

event: content_block_stop
data: {"type":"content_block_stop","index":0}

event: content_block_start
data: {"type":"content_block_start","index":1,"content_block":{"type":"web_search_tool_result","tool_use_id":"srvtoolu_01A","content":[{"type":"web_search_result","title":"Paris weather","url":"https://example.com/paris","encrypted_content":"EqgfCioIARgBIiQ","page_age":null}]}}

event: content_block_stop
data: {"type":"content_block_stop","index":1}

event: content_block_start
data: {"type":"content_block_start","index":2,"content_block":{"type":"text","text":""}}

event: content_block_delta
data: {"type":"content_block_delta","index":2,"delta":{"type":"text_delta","text":"Sunny."}}

event: content_block_stop
data: {"type":"content_block_stop","index":2}

event: message_delta
data: {"type":"message_delta","delta":{"stop_reason":"end_turn","stop_sequence":null},"usage":{"output_tokens":50,"server_tool_use":{"web_search_requests":1,"web_fetch_requests":0}}}

event: message_stop
data: {"type":"message_stop"}

`

func TestSearchTurnRecordsOneSearchRowPricedFromTheCatalog(t *testing.T) {
	pool := testdb.NewPool(t)
	api := newFakeAnthropicSSE(t, http.StatusOK, []byte(searchSSE))
	gw := testGateway(t, pool, api.srv.URL)
	user := testdb.NewUser(t, pool)
	insertSealed(t, pool, gw.Keyring, user.ID, anthropic.Name, "sk-ant-users-own")
	sid := uuid.New()
	if _, err := eventlog.NewRepo(pool, haiku).CreateSession(t.Context(), user.Scope(), sid, uuid.New(), "weather in Paris", "", false); err != nil {
		t.Fatal(err)
	}
	runWorker(t, pool, gw)
	waitFor(t, pool, sid, eventlog.StatusAwaitingUser)

	// Searches are $10 per 1,000: 10,000 micro-dollars each.
	type row struct {
		kind, unit      string
		quantity, micro int64
	}
	want := []row{{"llm", "input_tokens", 100, 100}, {"llm", "output_tokens", 50, 250}, {"llm", "web_search_requests", 1, 10000}}
	rows, err := pool.Query(t.Context(), `SELECT kind, unit, quantity, cost_micros FROM usage WHERE session_id = $1 ORDER BY seq`, sid)
	if err != nil {
		t.Fatal(err)
	}
	var got []row
	for rows.Next() {
		var r row
		if err := rows.Scan(&r.kind, &r.unit, &r.quantity, &r.micro); err != nil {
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

	// The search costs money but is not a token.
	var tokens, cost int64
	if err := pool.QueryRow(t.Context(), `SELECT tokens_used, cost_micros FROM sessions WHERE id = $1`, sid).Scan(&tokens, &cost); err != nil {
		t.Fatal(err)
	}
	if tokens != 150 || cost != 10350 {
		t.Fatalf("session tokens_used = %d, cost_micros = %d, want 150, 10350", tokens, cost)
	}
	evs, err := eventlog.NewStore(pool).Load(t.Context(), sid)
	if err != nil {
		t.Fatal(err)
	}
	st, err := Fold(evs)
	if err != nil {
		t.Fatal(err)
	}
	if st.TokensUsed != 150 || st.CostMicros != 10350 {
		t.Fatalf("fold tokens_used = %d, cost_micros = %d, want 150, 10350", st.TokensUsed, st.CostMicros)
	}
}

func TestWebSearchIsOnlyForCatalogModelsWithTheFlag(t *testing.T) {
	c, err := catalog.Load()
	if err != nil {
		t.Fatal(err)
	}
	for model, want := range map[string]bool{haiku: true, "claude-next-preview": false} {
		if got := (Gateway{Catalog: c}).webSearch(model); got != want {
			t.Errorf("webSearch(%q) = %v, want %v", model, got, want)
		}
	}
	if (Gateway{}).webSearch(haiku) {
		t.Error("webSearch with no catalog = true, want false")
	}
}
