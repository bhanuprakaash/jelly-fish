package openai

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

func TestListModels(t *testing.T) {
	c, req := serve(t, 200, `{"object":"list","data":[{"id":"gpt-6.1-sol","object":"model","created":1,"owned_by":"openai"}]}`)
	models, err := c.ListModels(t.Context(), "sk-test")
	if err != nil {
		t.Fatal(err)
	}
	if len(models) != 1 || models[0] != (provider.Model{ID: "gpt-6.1-sol", DisplayName: "gpt-6.1-sol"}) {
		t.Fatalf("models = %+v", models)
	}
	if req.URL.Path != "/models" || req.Header.Get("Authorization") != "Bearer sk-test" {
		t.Errorf("request = %s %v", req.URL.Path, req.Header)
	}
}

func TestListModelsErrors(t *testing.T) {
	tests := []struct {
		name         string
		status       int
		wantRejected bool
	}{{"401", 401, true}, {"403", 403, true}, {"500", 500, false}}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			c, _ := serve(t, tt.status, errBody("invalid_request_error", "invalid_api_key", "no"))
			_, err := c.ListModels(t.Context(), "sk-canary")
			if err == nil {
				t.Fatal("no error")
			}
			if got := errors.Is(err, provider.ErrKeyRejected); got != tt.wantRejected {
				t.Errorf("rejected = %v, want %v (err %v)", got, tt.wantRejected, err)
			}
		})
	}
}
