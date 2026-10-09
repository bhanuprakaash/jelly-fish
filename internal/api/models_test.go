package api

import (
	"context"
	"encoding/json"
	"net/http"
	"testing"

	"github.com/google/uuid"

	"github.com/bhanuprakaash/jelly-fish/internal/eventlog"
	"github.com/bhanuprakaash/jelly-fish/internal/provider"
	"github.com/bhanuprakaash/jelly-fish/internal/provider/catalog"
)

type modelsJSON struct {
	Default   string `json:"default"`
	Providers []struct {
		Provider  string `json:"provider"`
		Available bool   `json:"available"`
		Models    []struct {
			ID            string `json:"id"`
			DisplayName   string `json:"display_name"`
			ContextWindow int64  `json:"context_window"`
			MaxOutput     int64  `json:"max_output"`
			Price         *struct {
				Input  float64 `json:"input"`
				Output float64 `json:"output"`
			} `json:"price"`
		} `json:"models"`
	} `json:"providers"`
}

func (e *keysEnv) models(t *testing.T) modelsJSON {
	t.Helper()
	rr := e.do(http.MethodGet, "/api/models", nil, e.cookie)
	if rr.Code != http.StatusOK {
		t.Fatalf("GET /api/models = %d %s", rr.Code, rr.Body)
	}
	var got modelsJSON
	if err := json.Unmarshal(rr.Body.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	return got
}

func TestModelsWithOnlyAnAnthropicKey(t *testing.T) {
	e := newKeysEnv(t)
	if rr := e.do(http.MethodPut, "/api/provider-keys/anthropic", map[string]string{"key": canaryKey}, e.cookie); rr.Code != http.StatusOK {
		t.Fatalf("PUT key = %d", rr.Code)
	}
	if _, err := e.pool.Exec(t.Context(), `UPDATE provider_keys SET models = $2 WHERE user_id = $1`, e.user.ID,
		`[{"id":"claude-haiku-4-5-20251001","display_name":"Claude Haiku 4.5","max_input_tokens":190000},{"id":"claude-next-preview","display_name":"Claude Next"}]`); err != nil {
		t.Fatal(err)
	}
	calls := len(e.lister.calls)

	got := e.models(t)
	if len(e.lister.calls) != calls {
		t.Fatal("GET /api/models called the Provider")
	}
	if got.Default != "fake" {
		t.Errorf("default = %q", got.Default)
	}
	if len(got.Providers) != 4 || got.Providers[3].Provider != "fake" {
		t.Fatalf("providers = %+v, want anthropic, openai, gemini, fake", got.Providers)
	}

	a := got.Providers[0]
	if a.Provider != "anthropic" || !a.Available || len(a.Models) != 2 {
		t.Fatalf("anthropic = %+v, want its live list of 2, available", a)
	}
	haiku, next := a.Models[0], a.Models[1]
	// The live limit wins over the catalog's; the price is the catalog's.
	if haiku.ID != "claude-haiku-4-5-20251001" || haiku.DisplayName != "Claude Haiku 4.5" || haiku.ContextWindow != 190_000 ||
		haiku.MaxOutput != 64_000 || haiku.Price == nil || haiku.Price.Input != 1 || haiku.Price.Output != 5 {
		t.Errorf("haiku = %+v", haiku)
	}
	if next.ID != "claude-next-preview" || next.Price != nil {
		t.Errorf("live-only model = %+v, want no price", next)
	}

	for i, name := range []string{"openai", "gemini"} {
		p := got.Providers[i+1]
		if p.Provider != name || p.Available || len(p.Models) == 0 {
			t.Errorf("%s = %+v, want its catalog models, unavailable", name, p)
		}
	}
}

func TestModelsShowOnlyCatalogOpenAIModels(t *testing.T) {
	e := newKeysEnv(t)
	if rr := e.do(http.MethodPut, "/api/provider-keys/openai", map[string]string{"key": "sk-proj-abcd"}, e.cookie); rr.Code != http.StatusOK {
		t.Fatalf("PUT key = %d", rr.Code)
	}
	if _, err := e.pool.Exec(t.Context(), `UPDATE provider_keys SET models = $2 WHERE user_id = $1`, e.user.ID,
		`[{"id":"tts-1"},{"id":"gpt-6-luna"},{"id":"gpt-6-sol"},{"id":"gpt-6.1-sol"}]`); err != nil {
		t.Fatal(err)
	}

	o := e.models(t).Providers[1]
	var ids []string
	for _, m := range o.Models {
		ids = append(ids, m.ID)
	}
	if o.Provider != "openai" || !o.Available || len(ids) != 2 || ids[0] != "gpt-6.1-sol" || ids[1] != "gpt-6-luna" {
		t.Fatalf("openai = %v available %v, want [gpt-6.1-sol gpt-6-luna] in catalog order", ids, o.Available)
	}
}

func TestModelsWithoutKeysAreAllUnavailable(t *testing.T) {
	e := newKeysEnv(t)
	for _, p := range e.models(t).Providers {
		if p.Available != (p.Provider == "fake") {
			t.Errorf("%s is available with no key saved", p.Provider)
		}
	}
}

func TestModelsListFakeOnlyInDevBuilds(t *testing.T) {
	cat, err := catalog.Load()
	if err != nil {
		t.Fatal(err)
	}
	for _, fake := range []string{"", "fake"} {
		groups, err := ModelConfig{Lists: noLists{}, Catalog: cat, Fake: fake}.pickable(t.Context(), uuid.New())
		if err != nil {
			t.Fatal(err)
		}
		last := groups[len(groups)-1]
		listed := last.Provider == "fake" && last.Available && len(last.Models) == 1 && last.Models[0].ID == "fake"
		if listed != (fake != "") {
			t.Errorf("Fake %q: last group = %+v", fake, last)
		}
	}
}

type noLists struct{}

func (noLists) Models(context.Context, uuid.UUID) (map[string][]provider.Model, error) {
	return map[string][]provider.Model{}, nil
}

// saveKeys gives e's user an Anthropic key listing haiku and sonnet, and an
// OpenAI key listing gpt-6-astra.
func (e *keysEnv) saveKeys(t *testing.T) {
	t.Helper()
	if rr := e.do(http.MethodPut, "/api/provider-keys/anthropic", map[string]string{"key": canaryKey}, e.cookie); rr.Code != http.StatusOK {
		t.Fatalf("PUT key = %d", rr.Code)
	}
	if _, err := e.pool.Exec(t.Context(), `UPDATE provider_keys SET models = '[{"id":"claude-haiku-4-5-20251001"},{"id":"claude-sonnet-5-5"}]' WHERE user_id = $1`, e.user.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := e.pool.Exec(t.Context(), `INSERT INTO provider_keys (user_id, provider, ciphertext, key_id, last4, models, models_fetched_at)
		VALUES ($1, 'openai', 'x', 'm1', 'last', '[{"id":"gpt-6-astra"}]', now())`, e.user.ID); err != nil {
		t.Fatal(err)
	}
}

func (e *keysEnv) newSession(t *testing.T, model string) uuid.UUID {
	t.Helper()
	sid := uuid.New()
	rr := e.do(http.MethodPost, "/api/sessions", map[string]any{"session_id": sid, "client_msg_id": uuid.New(), "message": "hi", "model": model}, e.cookie)
	if rr.Code != http.StatusOK {
		t.Fatalf("POST /api/sessions on %q = %d %s", model, rr.Code, rr.Body)
	}
	return sid
}

func (e *keysEnv) sessionModel(t *testing.T, sid uuid.UUID) string {
	t.Helper()
	m, err := eventlog.NewRepo(e.pool, "fake").SessionModel(t.Context(), eventlog.TenantScope{WorkspaceID: e.user.WorkspaceID, UserID: e.user.ID}, sid)
	if err != nil {
		t.Fatal(err)
	}
	return m
}

func TestCreateSessionOnAPickedModel(t *testing.T) {
	e := newKeysEnv(t)
	e.saveKeys(t)
	if sid := e.newSession(t, "claude-sonnet-5-5"); e.sessionModel(t, sid) != "claude-sonnet-5-5" {
		t.Fatal("session not created on the picked model")
	}
	if sid := e.newSession(t, ""); e.sessionModel(t, sid) != "fake" {
		t.Fatal("session without a model not on the default")
	}
	// gemini has no key; claude-opus-5-5 isn't in this key's live list.
	for _, m := range []string{"gemini-3.8-flash", "claude-opus-5-5", "no-such-model"} {
		rr := e.do(http.MethodPost, "/api/sessions", map[string]any{"session_id": uuid.New(), "client_msg_id": uuid.New(), "message": "hi", "model": m}, e.cookie)
		if rr.Code != http.StatusUnprocessableEntity {
			t.Errorf("POST /api/sessions on %s = %d, want 422", m, rr.Code)
		}
	}
}

func TestChangeSessionModel(t *testing.T) {
	e := newKeysEnv(t)
	e.saveKeys(t)
	haiku := e.newSession(t, "claude-haiku-4-5-20251001")
	fake := e.newSession(t, "fake")
	// A session on a catalog model this key's live list no longer has.
	hidden := uuid.New()
	if _, err := eventlog.NewRepo(e.pool, "fake").CreateSession(t.Context(), eventlog.TenantScope{WorkspaceID: e.user.WorkspaceID, UserID: e.user.ID}, hidden, uuid.New(), "hi", "claude-opus-5-5", false); err != nil {
		t.Fatal(err)
	}

	tests := []struct {
		name  string
		sid   uuid.UUID
		model string
		want  int
	}{
		{"within anthropic", haiku, "claude-sonnet-5-5", http.StatusNoContent},
		{"across providers", haiku, "gpt-6-astra", http.StatusNoContent},
		{"no key for it", haiku, "gemini-3.8-flash", http.StatusUnprocessableEntity},
		{"not in the live list", haiku, "claude-opus-5-5", http.StatusUnprocessableEntity},
		{"from a hidden model across providers", hidden, "gpt-6-astra", http.StatusNoContent},
		{"from a hidden model within its provider", hidden, "claude-sonnet-5-5", http.StatusNoContent},
		{"to fake", haiku, "fake", http.StatusNoContent},
		{"fake to openai", fake, "gpt-6-astra", http.StatusNoContent},
		{"empty", fake, "", http.StatusBadRequest},
		{"unknown session", uuid.New(), "fake", http.StatusNotFound},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			before := ""
			if tc.want != http.StatusNotFound {
				before = e.sessionModel(t, tc.sid)
			}
			rr := e.do(http.MethodPut, "/api/sessions/"+tc.sid.String()+"/model", map[string]string{"model": tc.model}, e.cookie)
			if rr.Code != tc.want {
				t.Fatalf("status = %d %s, want %d", rr.Code, rr.Body, tc.want)
			}
			if tc.want == http.StatusNotFound {
				return
			}
			want := before
			if tc.want == http.StatusNoContent {
				want = tc.model
			}
			if got := e.sessionModel(t, tc.sid); got != want {
				t.Fatalf("model = %q, want %q", got, want)
			}
		})
	}
}

func TestChangeSessionModelToAnotherProviderAppendsOneEvent(t *testing.T) {
	e := newKeysEnv(t)
	e.saveKeys(t)
	sid := e.newSession(t, "claude-haiku-4-5-20251001")

	rr := e.do(http.MethodPut, "/api/sessions/"+sid.String()+"/model", map[string]string{"model": "gpt-6-astra"}, e.cookie)
	if rr.Code != http.StatusNoContent {
		t.Fatalf("status = %d %s, want 204", rr.Code, rr.Body)
	}
	var payload string
	var n int
	if err := e.pool.QueryRow(t.Context(), `SELECT count(*), coalesce(max(payload->>'model'), '') FROM events WHERE session_id = $1 AND type = $2`,
		sid, eventlog.TypeConfigChanged).Scan(&n, &payload); err != nil {
		t.Fatal(err)
	}
	if n != 1 || payload != "gpt-6-astra" {
		t.Fatalf("session.config_changed events = %d for %q, want 1 for gpt-6-astra", n, payload)
	}
}
