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
// except /api/auth/* needs a Login Session; authCfg backs that. Cross-site
// requests can't change state or open streams.
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
	keys := &providerKeyHandlers{cfg: authCfg.ProviderKeys, logger: logger}
	protected := http.NewServeMux()
	protected.HandleFunc("GET /api/hello", handleHello)
	protected.HandleFunc("GET /api/me", handleMe)
	protected.HandleFunc("POST /api/auth/logout", authH.handleLogout)
	protected.HandleFunc("POST /api/auth/logout-all", authH.handleLogoutAll)
	protected.HandleFunc("GET /api/me/login-sessions", authH.handleListLoginSessions)
	protected.HandleFunc("DELETE /api/me/login-sessions/{id}", authH.handleDeleteLoginSession)

	models := &modelHandlers{cfg: authCfg.Models, logger: logger}
	protected.HandleFunc("GET /api/models", models.handleList)
	protected.HandleFunc("GET /api/provider-keys", keys.handleListKeys)
	protected.HandleFunc("PUT /api/provider-keys/{p}", keys.handlePutKey)
	protected.HandleFunc("DELETE /api/provider-keys/{p}", keys.handleDeleteKey)

	mem := &memoryHandlers{pages: authCfg.Memories, logger: logger}
	protected.HandleFunc("GET /api/memories", mem.list)
	protected.HandleFunc("GET /api/memories/{id}/revisions", mem.revisions)
	protected.HandleFunc("PATCH /api/memories/{id}", mem.edit)
	protected.HandleFunc("POST /api/memories/{id}/approve", mem.approve)
	protected.HandleFunc("POST /api/memories/{id}/undo", mem.undo)
	protected.HandleFunc("DELETE /api/memories/{id}", mem.delete)
	protected.HandleFunc("PATCH /api/projects/{id}", mem.patchProject)

	admin := http.NewServeMux()
	admin.HandleFunc("POST /api/admin/invites", authH.handleCreateInvite)
	admin.HandleFunc("POST /api/admin/invites/{id}/resend", authH.handleResendInvite)
	admin.HandleFunc("DELETE /api/admin/invites/{id}", authH.handleRevokeInvite)
	admin.HandleFunc("GET /api/admin/users", authH.handleListUsers)
	admin.HandleFunc("POST /api/admin/users/{id}/disable", authH.handleDisableUser)
	admin.HandleFunc("POST /api/admin/users/{id}/enable", authH.handleEnableUser)
	admin.HandleFunc("POST /api/admin/users/{id}/make-admin", authH.handleMakeAdmin)

	protected.Handle("/api/admin/", authH.adminOnly(admin))
	protected.HandleFunc("GET /api/sessions", handleListSessions(repo, logger))
	protected.HandleFunc("POST /api/sessions", handleCreateSession(repo, authCfg.Models, logger))
	protected.HandleFunc("PUT /api/sessions/{id}/model", models.handleChangeModel(repo))
	protected.HandleFunc("PUT /api/sessions/{id}/budget", handleChangeBudget(repo, logger))
	protected.HandleFunc("PUT /api/sessions/{id}/title", handleRenameSession(repo, logger))
	protected.HandleFunc("POST /api/sessions/{id}/messages", handlePostMessage(repo, logger))
	protected.HandleFunc("POST /api/sessions/{id}/interrupt", handleSessionAction(repo.Interrupt, logger))
	protected.HandleFunc("POST /api/sessions/{id}/retry", handleSessionAction(repo.Retry, logger))
	protected.HandleFunc("POST /api/sessions/{id}/approvals/{approval_id}", handleResolveApproval(repo, logger))
	protected.HandleFunc("GET /api/sessions/{id}/blobs/{sha256}", handleGetBlob(repo, logger))
	protected.Handle("GET /api/sessions/{id}/events", rejectCrossSite(http.HandlerFunc(streams.handle)))
	protected.Handle("GET /api/activity", rejectCrossSite(http.HandlerFunc(streams.handleActivity)))

	mux := http.NewServeMux()
	mux.HandleFunc("POST /api/auth/code", authH.handleRequestCode)
	mux.HandleFunc("POST /api/auth/code/verify", authH.handleVerifyCode)
	mux.HandleFunc("POST /api/auth/link", authH.handleVerifyLink)
	mux.Handle("/api/", authH.authn(protected))
	mux.Handle("/", spaHandler(webFS))
	// CSRF defence, ahead of Authn (auth-keys.md D3).
	srv := &http.Server{Addr: addr, Handler: http.NewCrossOriginProtection().Handler(mux)}
	// Shutdown lets in-flight code emails finish rather than drop a code.
	srv.RegisterOnShutdown(authH.bg.Wait)
	return srv, authH
}

func handleHello(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]string{"message": "hello"})
}

// rejectCrossSite refuses requests another site's page made. Streams need it
// on top of CrossOriginProtection, which lets every GET through (auth-keys.md D4).
func rejectCrossSite(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Sec-Fetch-Site") == "cross-site" {
			writeError(w, http.StatusForbidden, "cross-site request")
			return
		}
		next.ServeHTTP(w, r)
	})
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
