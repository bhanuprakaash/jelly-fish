// Package stream feeds the Session stream (SSE): the in-process hub of hints
// fed by LISTEN jf_events, and the UI event serializer (docs/design/streaming.md).
package stream

import (
	"context"
	"log/slog"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
)

// listenRetryDelay is how long Hub.Listen waits before re-LISTENing after a
// dropped connection (event-log.md §5.7: re-LISTEN, then poll).
const listenRetryDelay = 3 * time.Second

// Hub fans out "seq > lastSent" hints to Session stream handlers subscribed
// to a session, fed by one dedicated LISTEN connection per API process
// (event-log.md §5.12). It is a hint only; subscribers always re-read
// Postgres for the actual events.
type Hub struct {
	mu       sync.Mutex
	sessions map[uuid.UUID]map[chan struct{}]struct{}
}

// NewHub builds an empty Hub.
func NewHub() *Hub {
	return &Hub{sessions: make(map[uuid.UUID]map[chan struct{}]struct{})}
}

// Subscribe registers hint interest in sid. The returned channel has
// capacity 1: a pending hint already means "re-query", so hints are
// coalesced and never block the sender. Call unsubscribe when done.
func (h *Hub) Subscribe(sid uuid.UUID) (hint <-chan struct{}, unsubscribe func()) {
	ch := make(chan struct{}, 1)

	h.mu.Lock()
	set, ok := h.sessions[sid]
	if !ok {
		set = make(map[chan struct{}]struct{})
		h.sessions[sid] = set
	}
	set[ch] = struct{}{}
	h.mu.Unlock()

	return ch, func() {
		h.mu.Lock()
		defer h.mu.Unlock()
		delete(h.sessions[sid], ch)
		if len(h.sessions[sid]) == 0 {
			delete(h.sessions, sid)
		}
	}
}

// Notify hints every subscriber of sid to re-query. It never blocks.
func (h *Hub) Notify(sid uuid.UUID) {
	h.mu.Lock()
	defer h.mu.Unlock()
	for ch := range h.sessions[sid] {
		select {
		case ch <- struct{}{}:
		default:
		}
	}
}

// Listen holds one dedicated, non-pooled connection LISTENing on jf_events
// and routes hints to the Hub, until ctx is cancelled. On a dropped
// connection it reconnects and re-LISTENs (event-log.md §5.7).
func (h *Hub) Listen(ctx context.Context, pool *pgxpool.Pool, logger *slog.Logger) {
	for {
		if ctx.Err() != nil {
			return
		}
		if err := h.listenOnce(ctx, pool); err != nil && ctx.Err() == nil {
			logger.Warn("jf_events listen dropped", "error", err)
		}
		select {
		case <-ctx.Done():
			return
		case <-time.After(listenRetryDelay):
		}
	}
}

func (h *Hub) listenOnce(ctx context.Context, pool *pgxpool.Pool) error {
	conn, err := pool.Acquire(ctx)
	if err != nil {
		return err
	}
	defer conn.Release()

	if _, err := conn.Exec(ctx, "LISTEN jf_events"); err != nil {
		return err
	}
	h.hintAll() // catch up on anything missed while disconnected

	for {
		notif, err := conn.Conn().WaitForNotification(ctx)
		if err != nil {
			return err
		}
		if sid, ok := sessionFromPayload(notif.Payload); ok {
			h.Notify(sid)
		}
	}
}

func (h *Hub) hintAll() {
	h.mu.Lock()
	defer h.mu.Unlock()
	for _, set := range h.sessions {
		for ch := range set {
			select {
			case ch <- struct{}{}:
			default:
			}
		}
	}
}

// sessionFromPayload parses jf_events's "<session_id>:<seq>" hint payload.
func sessionFromPayload(payload string) (uuid.UUID, bool) {
	sidStr, _, ok := strings.Cut(payload, ":")
	if !ok {
		return uuid.UUID{}, false
	}
	sid, err := uuid.Parse(sidStr)
	return sid, err == nil
}
