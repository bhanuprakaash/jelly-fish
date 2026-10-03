// Package eventlog implements the Session event log: the append-only store
// of everything that happens in a session, and the tenant-scoped Repo the
// API uses to read and write it (docs/design/event-log.md).
package eventlog

import (
	"errors"
	"slices"
	"time"

	"github.com/google/uuid"
)

// Event types this slice writes and reads (event-log.md §4).
const (
	TypeSessionCreated   = "session.created"
	TypeUserMessage      = "user.message"
	TypeStatusChanged    = "session.status_changed"
	TypeTurnStarted      = "turn.started"
	TypeTurnInterrupted  = "turn.interrupted"
	TypeLLMResponse      = "llm.response"
	TypeUsageRecorded    = "usage.recorded"
	TypeSessionError     = "session.error"
	TypeSessionCompleted = "session.completed"
	TypeUserInterrupt    = "user.interrupt"
	TypeTimerSet         = "timer.set"
	TypeTimerFired       = "timer.fired"
	TypeConfigChanged    = "session.config_changed"
)

// Session statuses this slice uses (event-log.md §3).
const (
	StatusRunnable     = "runnable"
	StatusRunning      = "running"
	StatusSleeping     = "sleeping"
	StatusAwaitingUser = "awaiting_user"
	StatusCompleted    = "completed"
	StatusFailed       = "failed"
)

// TriggerUserMessage is the sessions.trigger of a session a User started with
// a message; any other trigger marks a System Session (event-log.md §3, D43).
const TriggerUserMessage = "user_message"

// ErrNotFound is returned for a session that doesn't exist, or belongs to
// another tenant, which looks the same from the outside (event-log.md §5.15).
var ErrNotFound = errors.New("session not found")

// ErrDuplicate is returned internally when a unique constraint (e.g. a
// repeated client_msg_id) rejects an insert; callers translate it.
var ErrDuplicate = errors.New("duplicate event")

// ErrStale is returned by a fenced park whose ExpectSeq no longer matches
// last_seq: new events arrived after the fold, so the Worker must refold
// (event-log.md §5.1).
var ErrStale = errors.New("session has new events")

// ErrLeaseLost is returned by a fenced append from a Worker that no longer
// holds the Lease.
var ErrLeaseLost = errors.New("lease lost")

// Event is one row of the append-only log.
type Event struct {
	Seq           int64
	Type          string
	SchemaVersion int
	Actor         string
	CorrelationID string
	Payload       []byte // jsonb, as stored
	CreatedAt     time.Time
}

// NewEvent is an event about to be appended; Payload is marshaled to jsonb.
type NewEvent struct {
	Type          string
	Actor         string
	CorrelationID string
	Payload       any
}

// StatusChange appends session.status_changed and updates sessions.status in
// the same transaction as the rest of an Append call (event-log.md §3, §5.1).
// A non-empty From restricts it to sessions currently in one of those
// statuses; otherwise the change is skipped and the events are still written.
// A non-zero WakeIn on a change to sleeping also appends timer.set and sets
// sessions.wake_at, both from the database clock so Workers need not agree
// on the time (event-log.md §5.2).
type StatusChange struct {
	To     string
	Reason string
	From   []string
	WakeIn time.Duration
}

func (sc StatusChange) applies(current string) bool {
	return len(sc.From) == 0 || slices.Contains(sc.From, current)
}

func (sc StatusChange) event(from string) NewEvent {
	return NewEvent{
		Type:  TypeStatusChanged,
		Actor: "system",
		Payload: map[string]string{
			"from":   from,
			"to":     sc.To,
			"reason": sc.Reason,
		},
	}
}

// Fence proves an append comes from the Worker holding the Lease
// (event-log.md §5.1). ExpectSeq is set on appends that park the session.
type Fence struct {
	Owner     string
	Epoch     int64
	ExpectSeq *int64
}

// PersonalProject is the name of the Project every User starts with.
const PersonalProject = "Personal"

// TenantScope narrows every Repo call to one workspace and user
// (event-log.md §5.15). A session outside it is reported as ErrNotFound.
type TenantScope struct {
	WorkspaceID uuid.UUID
	UserID      uuid.UUID
}
