// Package api serves the public HTTP API.
package api

import (
	"encoding/json"
	"log/slog"
	"net/http"
)

// NewServer builds the api HTTP server listening on addr.
func NewServer(addr string, logger *slog.Logger) *http.Server {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /api/hello", handleHello)
	return &http.Server{Addr: addr, Handler: mux}
}

func handleHello(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]string{"message": "hello"})
}
