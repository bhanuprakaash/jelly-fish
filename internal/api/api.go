// Package api serves the public HTTP API and the embedded PWA.
package api

import (
	"encoding/json"
	"io/fs"
	"log/slog"
	"net/http"
	"strings"

	"github.com/bhanuprakaash/jelly-fish/internal/stream"
)

// NewServer builds the api HTTP server listening on addr. webFS is served at
// "/" with SPA fallback: unknown paths return the root's index.html so
// client-side routing works. repo backs the session endpoints; hub and
// deltas feed their SSE stream, which reports to metrics. Every /api route
// except /api/auth/* needs a Login Session; authCfg backs that.
func NewServer(addr string, logger *slog.Logger, webFS fs.FS, repo SessionRepo, hub *stream.Hub, deltas DeltaSubscriber, metrics *stream.Metrics, authCfg AuthConfig) *http.Server {
	srv, _ := newServer(addr, logger, webFS, repo, hub, deltas, metrics, authCfg)
	return srv
}

// newServer is NewServer, also returning the auth handlers so tests can wait
// for background emails.
func newServer(addr string, logger *slog.Logger, webFS fs.FS, repo SessionRepo, hub *stream.Hub, deltas DeltaSubscriber, metrics *stream.Metrics, authCfg AuthConfig) (*http.Server, *authHandlers) {
	authH := &authHandlers{cfg: authCfg, logger: logger}
	streams := &sessionStreams{
		repo: repo, hub: hub, deltas: deltas, metrics: metrics, logger: logger,
		logins:       authCfg.Authenticator,
		limiter:      newStreamLimiter(maxStreamsPerUser),
		writeTimeout: writeTimeout,
		pingInterval: pingInterval,
	}
	protected := http.NewServeMux()
	protected.HandleFunc("GET /api/hello", handleHello)
	protected.HandleFunc("GET /api/me", handleMe)
	protected.HandleFunc("POST /api/auth/logout", authH.handleLogout)
	protected.HandleFunc("POST /api/auth/logout-all", authH.handleLogoutAll)
	protected.HandleFunc("GET /api/me/login-sessions", authH.handleListLoginSessions)
	protected.HandleFunc("DELETE /api/me/login-sessions/{id}", authH.handleDeleteLoginSession)
	protected.HandleFunc("POST /api/sessions", handleCreateSession(repo, logger))
	protected.HandleFunc("POST /api/sessions/{id}/messages", handlePostMessage(repo, logger))
	protected.HandleFunc("GET /api/sessions/{id}/events", streams.handle)

	mux := http.NewServeMux()
	mux.HandleFunc("POST /api/auth/code", authH.handleRequestCode)
	mux.HandleFunc("POST /api/auth/code/verify", authH.handleVerifyCode)
	mux.HandleFunc("POST /api/auth/link", authH.handleVerifyLink)
	mux.Handle("/api/", authH.authn(protected))
	mux.Handle("/", spaHandler(webFS))
	srv := &http.Server{Addr: addr, Handler: mux}
	// Shutdown lets in-flight code emails finish rather than drop a code.
	srv.RegisterOnShutdown(authH.bg.Wait)
	return srv, authH
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
