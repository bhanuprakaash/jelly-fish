package health

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"
)

type fakeChecker struct {
	err error
}

func (f fakeChecker) Ready(ctx context.Context) error {
	return f.err
}

func TestHealthz(t *testing.T) {
	srv := NewServer(":0", fakeChecker{}, slog.New(slog.DiscardHandler))

	rr := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/healthz", nil)
	srv.Handler.ServeHTTP(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("healthz status = %d, want %d", rr.Code, http.StatusOK)
	}
}

func TestReadyz(t *testing.T) {
	tests := []struct {
		name    string
		checker Checker
		want    int
	}{
		{"ready", fakeChecker{}, http.StatusOK},
		{"not ready", fakeChecker{err: errors.New("db down")}, http.StatusServiceUnavailable},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			srv := NewServer(":0", tt.checker, slog.New(slog.DiscardHandler))

			rr := httptest.NewRecorder()
			req := httptest.NewRequest(http.MethodGet, "/readyz", nil)
			srv.Handler.ServeHTTP(rr, req)

			if rr.Code != tt.want {
				t.Fatalf("readyz status = %d, want %d", rr.Code, tt.want)
			}
			if _, err := io.ReadAll(rr.Body); err != nil {
				t.Fatalf("read body: %v", err)
			}
		})
	}
}
