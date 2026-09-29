package api

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/bhanuprakaash/jelly-fish/internal/eventlog"
	"github.com/bhanuprakaash/jelly-fish/internal/stream"
)

// pingInterval keeps intermediaries and EventSource clients from timing out
// an idle Session stream (streaming.md §4.1).
const pingInterval = 15 * time.Second

type createSessionRequest struct {
	SessionID   uuid.UUID `json:"session_id"`
	ClientMsgID uuid.UUID `json:"client_msg_id"`
	Message     string    `json:"message"`
}

func handleCreateSession(repo SessionRepo, logger *slog.Logger) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var req createSessionRequest
		if !decodeJSON(w, r, &req) {
			return
		}
		if req.SessionID == uuid.Nil || req.ClientMsgID == uuid.Nil || strings.TrimSpace(req.Message) == "" {
			writeError(w, http.StatusBadRequest, "session_id, client_msg_id and message are required")
			return
		}

		scope := eventlog.DevScope()
		last, err := repo.CreateSession(r.Context(), scope, req.SessionID, req.ClientMsgID, req.Message)
		if err != nil {
			logger.Error("create session", "error", err, "session_id", req.SessionID)
			writeError(w, http.StatusInternalServerError, "could not create session")
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"session_id": req.SessionID, "last_seq": last})
	}
}

type postMessageRequest struct {
	ClientMsgID uuid.UUID `json:"client_msg_id"`
	Message     string    `json:"message"`
}

func handlePostMessage(repo SessionRepo, logger *slog.Logger) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		sessionID, ok := pathUUID(w, r)
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

		scope := eventlog.DevScope()
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

// handleSessionEvents serves the Session stream: a replay of events with
// seq > after, then live via Hub hints (event-log.md §5.12, streaming.md §5.1).
func handleSessionEvents(repo SessionRepo, hub *stream.Hub, logger *slog.Logger) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		sessionID, ok := pathUUID(w, r)
		if !ok {
			return
		}
		scope := eventlog.DevScope()
		ctx := r.Context()

		lastSeq, err := repo.SessionLastSeq(ctx, scope, sessionID)
		if err != nil {
			if errors.Is(err, eventlog.ErrNotFound) {
				writeError(w, http.StatusNotFound, "session not found")
				return
			}
			logger.Error("look up session", "error", err, "session_id", sessionID)
			writeError(w, http.StatusInternalServerError, "could not open stream")
			return
		}
		after := parseAfter(r, lastSeq)

		// Subscribe before the replay query, so no hint is missed in between
		// (event-log.md §5.12 step 2 precedes step 3).
		hint, unsubscribe := hub.Subscribe(sessionID)
		defer unsubscribe()

		rc := http.NewResponseController(w)
		h := w.Header()
		h.Set("Content-Type", "text/event-stream")
		h.Set("Cache-Control", "no-cache")
		h.Set("X-Accel-Buffering", "no")
		w.WriteHeader(http.StatusOK)
		if _, err := w.Write([]byte("retry: 2000\n\n")); err != nil {
			return
		}
		_ = rc.Flush()

		lastSent, ok := sendEvents(ctx, w, rc, repo, scope, sessionID, after)
		if !ok {
			return
		}

		ticker := time.NewTicker(pingInterval)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-hint:
				sent, ok := sendEvents(ctx, w, rc, repo, scope, sessionID, lastSent)
				if !ok {
					return
				}
				lastSent = sent
			case <-ticker.C:
				if _, err := w.Write([]byte(": ping\n\n")); err != nil {
					return
				}
				if err := rc.Flush(); err != nil {
					return
				}
			}
		}
	}
}

// sendEvents writes every event with seq > after as a durable frame and
// returns the new high-water seq. ok is false if the write failed and the
// stream should close.
func sendEvents(ctx context.Context, w http.ResponseWriter, rc *http.ResponseController, repo SessionRepo, scope eventlog.TenantScope, sessionID uuid.UUID, after int64) (int64, bool) {
	evs, err := repo.ListEvents(ctx, scope, sessionID, after)
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
		if _, err := w.Write([]byte("id: " + strconv.FormatInt(e.Seq, 10) + "\ndata: " + string(data) + "\n\n")); err != nil {
			return lastSent, false
		}
	}
	if len(evs) > 0 {
		if err := rc.Flush(); err != nil {
			return lastSent, false
		}
	}
	return lastSent, true
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

func pathUUID(w http.ResponseWriter, r *http.Request) (uuid.UUID, bool) {
	id, err := uuid.Parse(r.PathValue("id"))
	if err != nil {
		writeError(w, http.StatusNotFound, "session not found")
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
