package api

import (
	"context"
	"encoding/json"
	"net/http"
	"time"

	"github.com/google/uuid"

	"github.com/bhanuprakaash/jelly-fish/internal/eventlog"
	"github.com/bhanuprakaash/jelly-fish/internal/stream"
)

// activityEntry is a session in an Activity stream frame (streaming.md §4.2).
type activityEntry struct {
	SessionID     uuid.UUID  `json:"session_id"`
	RootID        uuid.UUID  `json:"root_id"`
	ParentID      *uuid.UUID `json:"parent_id"`
	Status        string     `json:"status"`
	NeedsApproval bool       `json:"needs_approval"`
}

func newActivityEntry(r eventlog.ActivityRow) activityEntry {
	return activityEntry{
		SessionID:     r.SessionID,
		RootID:        r.RootID,
		ParentID:      r.ParentID,
		Status:        r.Status,
		NeedsApproval: r.Status == eventlog.StatusAwaitingApproval,
	}
}

// handleActivity serves the Activity stream: a snapshot of the User's busy
// sessions, then a status frame whenever one of their sessions changes status
// (streaming.md §4.2, §5.3). Frames carry no id, so a reconnect starts from a
// fresh snapshot.
func (s *sessionStreams) handleActivity(w http.ResponseWriter, r *http.Request) {
	scope := scopeFrom(r)
	ctx := r.Context()

	end, ok := s.admit(w, scope.UserID, stream.KindActivity)
	if !ok {
		return
	}
	defer end()

	// Subscribe before the snapshot query, so no change is missed in between.
	hints, resync, unsubscribe := s.hub.SubscribeActivity(scope.UserID)
	defer unsubscribe()

	sw, ok := s.startSSE(w)
	if !ok || !s.sendSnapshot(ctx, sw, scope) {
		return
	}

	ticker := time.NewTicker(s.pingInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case sid := <-hints:
			if !s.sendStatus(ctx, sw, scope, sid) {
				return
			}
		case <-resync:
			// Queued hints are covered by the snapshot.
			drainHints(hints)
			if !s.sendSnapshot(ctx, sw, scope) {
				return
			}
		case <-ticker.C:
			if !s.loginHolds(ctx) || !sw.frame(": ping\n\n") {
				return
			}
		}
	}
}

func drainHints(hints <-chan uuid.UUID) {
	for {
		select {
		case <-hints:
		default:
			return
		}
	}
}

// sendSnapshot writes the User's busy sessions. It reports false if the query
// or the write failed and the stream should close: EventSource retries a
// closed stream, but not an error response.
func (s *sessionStreams) sendSnapshot(ctx context.Context, sw *sseWriter, scope eventlog.TenantScope) bool {
	rows, err := s.repo.ActivitySnapshot(ctx, scope)
	if err != nil {
		if ctx.Err() == nil {
			s.logger.Error("activity snapshot", "error", err, "user_id", scope.UserID)
		}
		return false
	}
	entries := make([]activityEntry, len(rows))
	for i, row := range rows {
		entries[i] = newActivityEntry(row)
	}
	return sw.event("snapshot", entries)
}

// sendStatus writes sid's current status, unless the User can't see it. It
// reports false if the query or the write failed and the stream should close.
func (s *sessionStreams) sendStatus(ctx context.Context, sw *sseWriter, scope eventlog.TenantScope, sid uuid.UUID) bool {
	row, ok, err := s.repo.ActivityStatusOf(ctx, scope, sid)
	if err != nil {
		if ctx.Err() == nil {
			s.logger.Error("activity status", "error", err, "user_id", scope.UserID, "session_id", sid)
		}
		return false
	}
	if !ok {
		return true
	}
	return sw.event("status", newActivityEntry(row))
}

// event writes v as an id-less frame of the named event. It reports false if
// the write failed.
func (sw *sseWriter) event(name string, v any) bool {
	data, err := json.Marshal(v)
	if err != nil {
		return true
	}
	return sw.frame("event: " + name + "\ndata: " + string(data) + "\n\n")
}
