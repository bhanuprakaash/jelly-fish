// Package stream feeds the Session stream (SSE): the in-process hub of hints,
// the token delta bus, the LISTEN loop feeding both, and the UI event
// serializer (docs/design/streaming.md).
package stream

import (
	"strings"
	"sync"

	"github.com/google/uuid"
)

// activityHintBuffer is how many session hints an Activity stream queues
// before it falls back to a fresh snapshot (streaming.md §5.3).
const activityHintBuffer = 64

// Hub fans out "seq > lastSent" hints to Session stream handlers subscribed
// to a session, and "this session's status changed" hints to Activity stream
// handlers subscribed to a user, fed by Listen (event-log.md §5.12). It is a
// hint only; subscribers always re-read Postgres for the actual state.
type Hub struct {
	mu       sync.Mutex
	sessions map[uuid.UUID]map[chan struct{}]struct{}
	activity map[uuid.UUID]map[*activityClient]struct{}
}

// activityClient is one open Activity stream.
type activityClient struct {
	// hints are sessions to re-read.
	hints chan uuid.UUID
	// resync means hints overflowed or were missed: send a fresh snapshot.
	resync chan struct{}
}

// signalResync marks c as needing a fresh snapshot; one pending signal is enough.
func (c *activityClient) signalResync() {
	select {
	case c.resync <- struct{}{}:
	default:
	}
}

// NewHub builds an empty Hub.
func NewHub() *Hub {
	return &Hub{
		sessions: make(map[uuid.UUID]map[chan struct{}]struct{}),
		activity: make(map[uuid.UUID]map[*activityClient]struct{}),
	}
}

// SubscribeActivity registers an Activity stream for userID. hints carries the
// sessions of that user whose status changed; resync fires when hints could
// not keep up and the stream must send a fresh snapshot. Neither channel
// blocks the sender. Call unsubscribe when done.
func (h *Hub) SubscribeActivity(userID uuid.UUID) (hints <-chan uuid.UUID, resync <-chan struct{}, unsubscribe func()) {
	c := &activityClient{hints: make(chan uuid.UUID, activityHintBuffer), resync: make(chan struct{}, 1)}

	h.mu.Lock()
	defer h.mu.Unlock()
	set, ok := h.activity[userID]
	if !ok {
		set = make(map[*activityClient]struct{})
		h.activity[userID] = set
	}
	set[c] = struct{}{}

	return c.hints, c.resync, func() {
		h.mu.Lock()
		defer h.mu.Unlock()
		delete(h.activity[userID], c)
		if len(h.activity[userID]) == 0 {
			delete(h.activity, userID)
		}
	}
}

// NotifyActivity hints userID's Activity streams that sid's status changed. A
// stream whose queue is full gets a resync instead. It never blocks, and
// ignores a user with no stream open on this node.
func (h *Hub) NotifyActivity(userID, sid uuid.UUID) {
	h.mu.Lock()
	defer h.mu.Unlock()
	for c := range h.activity[userID] {
		select {
		case c.hints <- sid:
		default:
			c.signalResync()
		}
	}
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
	for _, set := range h.activity {
		for c := range set {
			c.signalResync()
		}
	}
}

// activityFromPayload parses jf_activity's "<user_id>:<session_id>" hint payload.
func activityFromPayload(payload string) (user, sid uuid.UUID, ok bool) {
	userStr, sidStr, ok := strings.Cut(payload, ":")
	if !ok {
		return uuid.UUID{}, uuid.UUID{}, false
	}
	user, err := uuid.Parse(userStr)
	if err != nil {
		return uuid.UUID{}, uuid.UUID{}, false
	}
	sid, err = uuid.Parse(sidStr)
	return user, sid, err == nil
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
