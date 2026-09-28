// Package api serves the public HTTP API and the embedded PWA.
package api

import (
	"encoding/json"
	"io/fs"
	"log/slog"
	"net/http"
	"strings"
)

// NewServer builds the api HTTP server listening on addr. webFS is served at
// "/" with SPA fallback: unknown paths return the root's index.html so
// client-side routing works.
func NewServer(addr string, logger *slog.Logger, webFS fs.FS) *http.Server {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /api/hello", handleHello)
	mux.Handle("/", spaHandler(webFS))
	return &http.Server{Addr: addr, Handler: mux}
}

func handleHello(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]string{"message": "hello"})
}

func spaHandler(webFS fs.FS) http.Handler {
	fileServer := http.FileServerFS(webFS)
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		name := strings.TrimPrefix(r.URL.Path, "/")
		if name == "" {
			name = "."
		}
		if _, err := fs.Stat(webFS, name); err != nil {
			r = r.Clone(r.Context())
			r.URL.Path = "/"
		}
		fileServer.ServeHTTP(w, r)
	})
}
