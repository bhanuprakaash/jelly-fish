package anthropic

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/bhanuprakaash/jelly-fish/internal/provider"
)

const modelsBody = `{"data":[{"type":"model","id":"claude-haiku-4-5-20251001","display_name":"Claude Haiku 4.5","created_at":"2025-10-01T00:00:00Z"}],"has_more":false,"first_id":"claude-haiku-4-5-20251001","last_id":"claude-haiku-4-5-20251001"}`

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
	c, req := serve(t, 200, modelsBody)
	models, err := c.ListModels(t.Context(), "sk-ant-test")
	if err != nil {
		t.Fatal(err)
	}
	if len(models) != 1 || models[0].ID != "claude-haiku-4-5-20251001" || models[0].DisplayName != "Claude Haiku 4.5" {
		t.Fatalf("models = %+v", models)
	}
	if req.URL.Path != "/v1/models" {
		t.Errorf("path = %q", req.URL.Path)
	}
	if req.Header.Get("x-api-key") != "sk-ant-test" {
		t.Errorf("x-api-key = %q", req.Header.Get("x-api-key"))
	}
	if req.Header.Get("anthropic-version") == "" {
		t.Error("no anthropic-version header")
	}
}

func TestListModelsErrors(t *testing.T) {
	tests := []struct {
		name         string
		status       int
		body         string
		wantRejected bool
	}{
		{"401", 401, `{"type":"error","error":{"type":"authentication_error","message":"invalid x-api-key"}}`, true},
		{"403", 403, `{"type":"error","error":{"type":"permission_error","message":"not allowed"}}`, true},
		{"500", 500, `{"type":"error","error":{"type":"api_error","message":"boom"}}`, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			c, _ := serve(t, tt.status, tt.body)
			_, err := c.ListModels(t.Context(), "sk-ant-canary")
			if err == nil {
				t.Fatal("no error")
			}
			if got := errors.Is(err, provider.ErrKeyRejected); got != tt.wantRejected {
				t.Errorf("rejected = %v, want %v (err %v)", got, tt.wantRejected, err)
			}
		})
	}
}
