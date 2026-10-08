package gemini

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/bhanuprakaash/jelly-fish/internal/provider"
)

func serve(t *testing.T, status int, body string) (Client, *http.Request) {
	t.Helper()
	var got http.Request
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got = *r.Clone(r.Context())
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)
		_, _ = w.Write([]byte(body))
	}))
	t.Cleanup(srv.Close)
	return Client{BaseURL: srv.URL}, &got
}

func TestListModelsDropsThePrefix(t *testing.T) {
	c, req := serve(t, 200, `{"models":[{"name":"models/gemini-3.8-flash","displayName":"Gemini 3.8 Flash","inputTokenLimit":1048576,"outputTokenLimit":65536},{"name":"models/gemini-embedding-001"}]}`)
	models, err := c.ListModels(t.Context(), "AIza-test")
	if err != nil {
		t.Fatal(err)
	}
	want := []provider.Model{{ID: "gemini-3.8-flash", DisplayName: "Gemini 3.8 Flash"}, {ID: "gemini-embedding-001", DisplayName: "gemini-embedding-001"}}
	if len(models) != 2 || models[0] != want[0] || models[1] != want[1] {
		t.Fatalf("models = %+v, want %+v", models, want)
	}
	if req.URL.Path != "/v1beta/models" || req.Header.Get("X-Goog-Api-Key") != "AIza-test" {
		t.Errorf("request = %s %v", req.URL.Path, req.Header)
	}
}

func TestListModelsErrors(t *testing.T) {
	tests := []struct {
		name         string
		status       int
		body         string
		wantRejected bool
	}{
		{"400 bad key", 400, errBody(400, "INVALID_ARGUMENT", "API key not valid.", badKey), true},
		{"401", 401, errBody(401, "UNAUTHENTICATED", "no"), true},
		{"403", 403, errBody(403, "PERMISSION_DENIED", "no"), true},
		{"500", 500, errBody(500, "INTERNAL", "no"), false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			c, _ := serve(t, tt.status, tt.body)
			_, err := c.ListModels(t.Context(), "AIza-canary")
			if err == nil {
				t.Fatal("no error")
			}
			if got := errors.Is(err, provider.ErrKeyRejected); got != tt.wantRejected {
				t.Errorf("rejected = %v, want %v (err %v)", got, tt.wantRejected, err)
			}
		})
	}
}
