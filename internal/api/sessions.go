package api

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"os"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/google/uuid"
	"github.com/prometheus/client_golang/prometheus"

	"github.com/bhanuprakaash/jelly-fish/internal/auth"
	"github.com/bhanuprakaash/jelly-fish/internal/eventlog"
	"github.com/bhanuprakaash/jelly-fish/internal/stream"
)

// pingInterval keeps intermediaries and EventSource clients from timing out
// an idle Session stream (streaming.md §4.1).
const pingInterval = 15 * time.Second

// writeTimeout bounds every Session stream write, so a stalled client closes
// its own connection instead of holding the handler (streaming.md §5.2).
const writeTimeout = 10 * time.Second

// DeltaSubscriber feeds a Session stream the live text of in-flight turns.
type DeltaSubscriber interface {
	// Subscribe returns deltas for sid until unsubscribe is called.
	Subscribe(sid uuid.UUID) (deltas <-chan stream.Delta, unsubscribe func())
}

var _ DeltaSubscriber = (*stream.PGDeltaBus)(nil)

type createSessionRequest struct {
	SessionID   uuid.UUID `json:"session_id"`
	ClientMsgID uuid.UUID `json:"client_msg_id"`
	Message     string    `json:"message"`
	// Model is optional; empty starts the session on the default model.
	Model string `json:"model"`
	// Incognito is fixed at create: no memory tool, no memory in the prompt.
	Incognito bool `json:"incognito"`
}

func handleCreateSession(repo SessionRepo, models ModelConfig, logger *slog.Logger) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var req createSessionRequest
		if !decodeJSON(w, r, &req) {
			return
		}
		if req.SessionID == uuid.Nil || req.ClientMsgID == uuid.Nil || strings.TrimSpace(req.Message) == "" {
			writeError(w, http.StatusBadRequest, "session_id, client_msg_id and message are required")
			return
		}

		scope := scopeFrom(r)
		if req.Model != "" {
			groups, err := models.pickable(r.Context(), scope.UserID)
			if err != nil {
				logger.Error("list models", "error", err, "session_id", req.SessionID)
				writeError(w, http.StatusInternalServerError, "could not create session")
				return
			}
			if _, ok := providerOf(groups, req.Model); !ok {
				writeError(w, http.StatusUnprocessableEntity, "model is not available")
				return
			}
		}
		last, err := repo.CreateSession(r.Context(), scope, req.SessionID, req.ClientMsgID, req.Message, req.Model, req.Incognito)
		if err != nil {
			logger.Error("create session", "error", err, "session_id", req.SessionID)
			writeError(w, http.StatusInternalServerError, "could not create session")
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"session_id": req.SessionID, "last_seq": last})
	}
}

type chatSummary struct {
	ID        uuid.UUID `json:"id"`
	Title     string    `json:"title"`
	Titled    bool      `json:"titled"`
	Status    string    `json:"status"`
	UpdatedAt time.Time `json:"updated_at"`
	Incognito bool      `json:"incognito"`
}

func handleListSessions(repo SessionRepo, logger *slog.Logger) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		sessions, err := repo.ListSessions(r.Context(), scopeFrom(r))
		if err != nil {
			logger.Error("list sessions", "error", err)
			writeError(w, http.StatusInternalServerError, "could not list sessions")
			return
		}
		out := make([]chatSummary, 0, len(sessions))
		for _, s := range sessions {
			out = append(out, chatSummary{ID: s.ID, Title: s.Title, Titled: s.Titled, Status: s.Status, UpdatedAt: s.UpdatedAt, Incognito: s.Incognito})
		}
		writeJSON(w, http.StatusOK, out)
	}
}

type postMessageRequest struct {
	ClientMsgID uuid.UUID `json:"client_msg_id"`
	Message     string    `json:"message"`
}

func handlePostMessage(repo SessionRepo, logger *slog.Logger) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		sessionID, ok := pathID(w, r, "session not found")
		if !ok {
			return
		}
		var req postMessageRequest
		if !decodeJSON(w, r, &req) {
			return
		}
		if req.ClientMsgID == uuid.Nil || strings.TrimSpace(req.Message) == "" {
			writeError(w, http.StatusBadRequest, "client_msg_id and message are required")
			return
		}

		scope := scopeFrom(r)
		seq, err := repo.PostMessage(r.Context(), scope, sessionID, req.ClientMsgID, req.Message)
		if err != nil {
			if errors.Is(err, eventlog.ErrNotFound) {
				writeError(w, http.StatusNotFound, "session not found")
				return
			}
			logger.Error("post message", "error", err, "session_id", sessionID)
			writeError(w, http.StatusInternalServerError, "could not post message")
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"seq": seq})
	}
}

type renameRequest struct {
	Title string `json:"title"`
}

func handleRenameSession(repo SessionRepo, logger *slog.Logger) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		sessionID, ok := pathID(w, r, "session not found")
		if !ok {
			return
		}
		var req renameRequest
		if !decodeJSON(w, r, &req) {
			return
		}
		title := strings.TrimSpace(req.Title)
		if n := utf8.RuneCountInString(title); n < 1 || n > eventlog.MaxTitleRunes {
			writeError(w, http.StatusBadRequest, "title must be 1-100 characters")
			return
		}
		switch err := repo.Rename(r.Context(), scopeFrom(r), sessionID, title); {
		case errors.Is(err, eventlog.ErrNotFound):
			writeError(w, http.StatusNotFound, "session not found")
		case errors.Is(err, eventlog.ErrChildSession):
			writeError(w, http.StatusUnprocessableEntity, "a child session can't be renamed")
		case err != nil:
			logger.Error("rename session", "error", err, "session_id", sessionID)
			writeError(w, http.StatusInternalServerError, "could not rename session")
		default:
			w.WriteHeader(http.StatusNoContent)
		}
	}
}

type resolveApprovalRequest struct {
	Decision string `json:"decision"`
}

func handleResolveApproval(repo SessionRepo, logger *slog.Logger) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		sessionID, ok := pathID(w, r, "session not found")
		if !ok {
			return
		}
		approvalID, err := uuid.Parse(r.PathValue("approval_id"))
		if err != nil {
			writeError(w, http.StatusNotFound, "approval not found")
			return
		}
		var req resolveApprovalRequest
		if !decodeJSON(w, r, &req) {
			return
		}
		if req.Decision != eventlog.DecisionAllow && req.Decision != eventlog.DecisionDeny {
			writeError(w, http.StatusBadRequest, "decision must be allow or deny")
			return
		}
		switch err := repo.ResolveApproval(r.Context(), scopeFrom(r), sessionID, approvalID, req.Decision); {
		case errors.Is(err, eventlog.ErrNotFound):
			writeError(w, http.StatusNotFound, "session not found")
		case errors.Is(err, eventlog.ErrNoOpenApproval):
			writeError(w, http.StatusConflict, "no such open approval")
		case err != nil:
			logger.Error("resolve approval", "error", err, "session_id", sessionID)
			writeError(w, http.StatusInternalServerError, "could not resolve approval")
		default:
			w.WriteHeader(http.StatusNoContent)
		}
	}
}

func handleGetBlob(repo SessionRepo, logger *slog.Logger) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		sessionID, ok := pathID(w, r, "blob not found")
		if !ok {
			return
		}
		mime, data, err := repo.Blob(r.Context(), scopeFrom(r), sessionID, r.PathValue("sha256"))
		if errors.Is(err, eventlog.ErrNotFound) {
			writeError(w, http.StatusNotFound, "blob not found")
			return
		}
		if err != nil {
			logger.Error("get blob", "error", err, "session_id", sessionID)
			writeError(w, http.StatusInternalServerError, "could not load blob")
			return
		}
		w.Header().Set("Content-Type", mime)
		w.Header().Set("X-Content-Type-Options", "nosniff")
		_, _ = w.Write(data)
	}
}

// handleSessionAction serves a POST on a session that takes no body and
// answers 204: do is one of the repo's tenant-scoped actions.
func handleSessionAction(do func(context.Context, eventlog.TenantScope, uuid.UUID) error, logger *slog.Logger) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		sessionID, ok := pathID(w, r, "session not found")
		if !ok {
			return
		}
		if err := do(r.Context(), scopeFrom(r), sessionID); err != nil {
			if errors.Is(err, eventlog.ErrNotFound) {
				writeError(w, http.StatusNotFound, "session not found")
				return
			}
			logger.Error("session action", "route", r.Pattern, "error", err, "session_id", sessionID)
			writeError(w, http.StatusInternalServerError, "could not update session")
			return
		}
		w.WriteHeader(http.StatusNoContent)
	}
}

// sessionStreams serves the Session stream and the Activity stream.
type sessionStreams struct {
	repo    SessionRepo
	hub     *stream.Hub
	deltas  DeltaSubscriber
	limiter *streamLimiter
	metrics *stream.Metrics
	logger  *slog.Logger
	logins  loginChecker
	// writeTimeout bounds each write; pingInterval paces the idle ping.
	writeTimeout time.Duration
	pingInterval time.Duration
}

// handle serves the Session stream: a replay of events with seq > after, then
// live via Hub hints, interleaved with text deltas (event-log.md §5.12,
// streaming.md §5.1).
func (s *sessionStreams) handle(w http.ResponseWriter, r *http.Request) {
	sessionID, ok := pathID(w, r, "session not found")
	if !ok {
		return
	}
	scope := scopeFrom(r)
	ctx := r.Context()

	lastSeq, err := s.repo.SessionLastSeq(ctx, scope, sessionID)
	if err != nil {
		if errors.Is(err, eventlog.ErrNotFound) {
			writeError(w, http.StatusNotFound, "session not found")
			return
		}
		s.logger.Error("look up session", "error", err, "session_id", sessionID)
		writeError(w, http.StatusInternalServerError, "could not open stream")
		return
	}
	end, ok := s.admit(w, scope.UserID, stream.KindSession)
	if !ok {
		return
	}
	defer end()
	after := parseAfter(r, lastSeq)

	// Subscribe before the replay query, so no hint is missed in between
	// (event-log.md §5.12 step 2 precedes step 3).
	hint, unsubscribe := s.hub.Subscribe(sessionID)
	defer unsubscribe()
	deltaCh, unsubscribeDeltas := s.deltas.Subscribe(sessionID)
	defer unsubscribeDeltas()

	sw, ok := s.startSSE(w)
	if !ok {
		return
	}

	lastSent, ok := s.sendEvents(ctx, sw, scope, sessionID, after)
	if !ok {
		return
	}

	ticker := time.NewTicker(s.pingInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-hint:
			// Deltas queued before this hint belong ahead of its durable frames.
			if !sw.pendingDeltas(deltaCh) {
				return
			}
			sent, ok := s.sendEvents(ctx, sw, scope, sessionID, lastSent)
			if !ok {
				return
			}
			lastSent = sent
		case d := <-deltaCh:
			if !sw.delta(d) {
				return
			}
		case <-ticker.C:
			if !s.loginHolds(ctx) {
				return
			}
			if !sw.frame(": ping\n\n") || !sw.pendingDeltas(deltaCh) {
				return
			}
			// The re-query catches a hint that never arrived.
			sent, ok := s.sendEvents(ctx, sw, scope, sessionID, lastSent)
			if !ok {
				return
			}
			lastSent = sent
		}
	}
}

// admit counts a stream of kind against userID's cap. It answers 429 and
// reports false when userID is at the cap; otherwise call end when the stream
// closes.
func (s *sessionStreams) admit(w http.ResponseWriter, userID uuid.UUID, kind string) (end func(), ok bool) {
	release, ok := s.limiter.acquire(userID)
	if !ok {
		s.metrics.Refusals.Inc()
		writeError(w, http.StatusTooManyRequests, "too many open streams")
		return nil, false
	}
	open := s.metrics.OpenStreams.WithLabelValues(kind)
	open.Inc()
	return func() {
		open.Dec()
		release()
	}, true
}

// startSSE sends the SSE headers and the reconnect delay. It reports false if
// that write failed and the stream should close.
func (s *sessionStreams) startSSE(w http.ResponseWriter) (*sseWriter, bool) {
	sw := &sseWriter{w: w, rc: http.NewResponseController(w), timeout: s.writeTimeout, deadlineCloses: s.metrics.WriteDeadlineCloses}
	h := w.Header()
	h.Set("Content-Type", "text/event-stream")
	h.Set("Cache-Control", "no-cache")
	h.Set("X-Accel-Buffering", "no")
	w.WriteHeader(http.StatusOK)
	return sw, sw.frame("retry: 2000\n\n")
}

// loginHolds reports whether the stream's Login Session is still valid and its
// User enabled. A failed check keeps the stream open: the next ping asks
// again, and a reconnect is authenticated anyway (auth-keys.md §5.5).
func (s *sessionStreams) loginHolds(ctx context.Context) bool {
	u, _ := auth.UserFrom(ctx)
	ok, err := s.logins.LoginActive(ctx, u.LoginSessionID)
	if err != nil {
		s.logger.Error("check login session", "error", err)
		return true
	}
	return ok
}

// sendEvents writes every event with seq > after as a durable frame and
// returns the new high-water seq. ok is false if the write failed and the
// stream should close.
func (s *sessionStreams) sendEvents(ctx context.Context, sw *sseWriter, scope eventlog.TenantScope, sessionID uuid.UUID, after int64) (int64, bool) {
	evs, err := s.repo.ListEvents(ctx, scope, sessionID, after)
	if err != nil {
		return after, false
	}
	lastSent := after
	for _, e := range evs {
		lastSent = e.Seq
		ui, ok := stream.ToUI(e)
		if !ok {
			continue
		}
		data, err := json.Marshal(ui)
		if err != nil {
			continue
		}
		if !sw.frame("id: " + strconv.FormatInt(e.Seq, 10) + "\ndata: " + string(data) + "\n\n") {
			return lastSent, false
		}
	}
	return lastSent, true
}

// sseWriter writes SSE frames, each within its write timeout.
type sseWriter struct {
	w              http.ResponseWriter
	rc             *http.ResponseController
	timeout        time.Duration
	deadlineCloses prometheus.Counter
}

// pendingDeltas writes every delta already queued on deltaCh. It reports
// false if a write failed and the stream should close.
func (sw *sseWriter) pendingDeltas(deltaCh <-chan stream.Delta) bool {
	for {
		select {
		case d := <-deltaCh:
			if !sw.delta(d) {
				return false
			}
		default:
			return true
		}
	}
}

// delta writes d as an id-less "delta" frame, so a reconnect resumes from the
// last durable seq. It reports false if the write failed.
func (sw *sseWriter) delta(d stream.Delta) bool {
	d.SessionID = uuid.Nil
	data, err := json.Marshal(d)
	if err != nil {
		return true
	}
	return sw.frame("event: delta\ndata: " + string(data) + "\n\n")
}

// frame writes and flushes one SSE frame within the write timeout. It reports
// false if that failed and the stream should close.
func (sw *sseWriter) frame(frame string) bool {
	if err := sw.rc.SetWriteDeadline(time.Now().Add(sw.timeout)); err != nil && !errors.Is(err, http.ErrNotSupported) {
		return false
	}
	_, err := io.WriteString(sw.w, frame)
	if err == nil {
		err = sw.rc.Flush()
	}
	if errors.Is(err, os.ErrDeadlineExceeded) {
		sw.deadlineCloses.Inc()
	}
	return err == nil
}

// parseAfter reads Last-Event-ID (winning over ?after), defaulting to a full
// replay (0) on a missing, non-numeric or too-high id (streaming.md D5).
func parseAfter(r *http.Request, lastSeq int64) int64 {
	raw := r.Header.Get("Last-Event-ID")
	if raw == "" {
		raw = r.URL.Query().Get("after")
	}
	after, err := strconv.ParseInt(raw, 10, 64)
	if err != nil || after < 0 || after > lastSeq {
		return 0
	}
	return after
}

// scopeFrom returns the TenantScope of the User authn put on r.
func scopeFrom(r *http.Request) eventlog.TenantScope {
	u, _ := auth.UserFrom(r.Context())
	return u.Scope()
}

// pathID parses the {id} path value, answering 404 with notFound if it is not
// a UUID.
func pathID(w http.ResponseWriter, r *http.Request, notFound string) (uuid.UUID, bool) {
	id, err := uuid.Parse(r.PathValue("id"))
	if err != nil {
		writeError(w, http.StatusNotFound, notFound)
		return uuid.UUID{}, false
	}
	return id, true
}

func decodeJSON(w http.ResponseWriter, r *http.Request, v any) bool {
	if err := json.NewDecoder(r.Body).Decode(v); err != nil {
		writeError(w, http.StatusBadRequest, "invalid JSON body")
		return false
	}
	return true
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

func writeError(w http.ResponseWriter, status int, message string) {
	writeJSON(w, status, map[string]string{"error": message})
}

// handleChangeBudget sets all three of a session's limits; each must be given
// and above zero.
func handleChangeBudget(repo SessionRepo, logger *slog.Logger) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		sessionID, ok := pathID(w, r, "session not found")
		if !ok {
			return
		}
		var b eventlog.Budget
		if !decodeJSON(w, r, &b) {
			return
		}
		if b.Tokens <= 0 || b.CostMicros <= 0 || b.Turns <= 0 {
			writeError(w, http.StatusBadRequest, "tokens, cost_micros and turns are required and must be above zero")
			return
		}
		switch err := repo.ChangeBudget(r.Context(), scopeFrom(r), sessionID, b); {
		case errors.Is(err, eventlog.ErrNotFound):
			writeError(w, http.StatusNotFound, "session not found")
		case err != nil:
			logger.Error("change budget", "error", err, "session_id", sessionID)
			writeError(w, http.StatusInternalServerError, "could not change budget")
		default:
			w.WriteHeader(http.StatusNoContent)
		}
	}
}
