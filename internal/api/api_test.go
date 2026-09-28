package api

import (
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"
	"testing/fstest"
)

func testWebFS() fstest.MapFS {
	return fstest.MapFS{
		"index.html": &fstest.MapFile{Data: []byte("shell")},
		"app.js":     &fstest.MapFile{Data: []byte("console.log('hi')")},
	}
}

func TestHello(t *testing.T) {
	srv := NewServer(":0", slog.New(slog.DiscardHandler), testWebFS())

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

func TestSPA(t *testing.T) {
	tests := []struct {
		name string
		path string
		want string
	}{
		{"root serves index", "/", "shell"},
		{"unknown route falls back to index", "/sessions/123", "shell"},
		{"known asset served directly", "/app.js", "console.log('hi')"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			srv := NewServer(":0", slog.New(slog.DiscardHandler), testWebFS())

			rr := httptest.NewRecorder()
			req := httptest.NewRequest(http.MethodGet, tt.path, nil)
			srv.Handler.ServeHTTP(rr, req)

			if rr.Code != http.StatusOK {
				t.Fatalf("status = %d, want %d", rr.Code, http.StatusOK)
			}
			if rr.Body.String() != tt.want {
				t.Fatalf("body = %q, want %q", rr.Body.String(), tt.want)
			}
		})
	}
}
