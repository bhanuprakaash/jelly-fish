// Package stream feeds the Session stream (SSE): the in-process hub of hints,
// the token delta bus, the LISTEN loop feeding both, and the UI event
// serializer (docs/design/streaming.md).
package stream

import (
	"strings"
	"sync"

	"github.com/google/uuid"
)

// Hub fans out "seq > lastSent" hints to Session stream handlers subscribed
// to a session, fed by Listen (event-log.md §5.12). It is a hint only;
// subscribers always re-read Postgres for the actual events.
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
	defer h.mu.Unlock()
	set, ok := h.sessions[sid]
	if !ok {
		set = make(map[chan struct{}]struct{})
		h.sessions[sid] = set
	}
	set[ch] = struct{}{}

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
