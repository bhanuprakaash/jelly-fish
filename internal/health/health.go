// Package health serves the /healthz and /readyz endpoints shared by the api and worker roles.
package health

import (
	"context"
	"log/slog"
	"net/http"
)

// Checker reports readiness, e.g. a database ping plus schema check.
type Checker interface {
	Ready(ctx context.Context) error
}

// NewServer builds the health HTTP server listening on addr.
func NewServer(addr string, checker Checker, logger *slog.Logger) *http.Server {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", handleHealthz)
	mux.HandleFunc("GET /readyz", handleReadyz(checker, logger))
	return &http.Server{Addr: addr, Handler: mux}
}

func handleHealthz(w http.ResponseWriter, r *http.Request) {
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write([]byte("ok"))
}

func handleReadyz(checker Checker, logger *slog.Logger) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if err := checker.Ready(r.Context()); err != nil {
			logger.Warn("not ready", "error", err)
			w.WriteHeader(http.StatusServiceUnavailable)
			_, _ = w.Write([]byte("not ready"))
			return
		}
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("ok"))
	}
}
