package api

import (
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestHello(t *testing.T) {
	srv := NewServer(":0", slog.New(slog.DiscardHandler))

	rr := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/api/hello", nil)
	srv.Handler.ServeHTTP(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d", rr.Code, http.StatusOK)
	}

	var body struct {
		Message string `json:"message"`
	}
	if err := json.NewDecoder(rr.Body).Decode(&body); err != nil {
		t.Fatalf("decode body: %v", err)
	}
	if body.Message != "hello" {
		t.Fatalf("message = %q, want %q", body.Message, "hello")
	}
}
