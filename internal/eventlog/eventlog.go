// Package eventlog implements the Session event log: the append-only store
// of everything that happens in a session, and the tenant-scoped Repo the
// API uses to read and write it (docs/design/event-log.md).
package eventlog

import (
	"errors"
	"time"

	"github.com/google/uuid"
)

// Event types this slice writes and reads (event-log.md §4).
const (
	TypeSessionCreated = "session.created"
	TypeUserMessage    = "user.message"
	TypeStatusChanged  = "session.status_changed"
)

// Session statuses this slice uses (event-log.md §3).
const (
	StatusRunnable = "runnable"
)

// ErrNotFound is returned for a session that doesn't exist, or belongs to
// another tenant, which looks the same from the outside (event-log.md §5.15).
var ErrNotFound = errors.New("session not found")

// ErrDuplicate is returned internally when a unique constraint (e.g. a
// repeated client_msg_id) rejects an insert; callers translate it.
var ErrDuplicate = errors.New("duplicate event")

// Event is one row of the append-only log.
type Event struct {
	Seq           int64
	Type          string
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
type StatusChange struct {
	To     string
	Reason string
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

// TenantScope narrows every Repo call to one workspace and user
// (event-log.md §5.15). A session outside it is reported as ErrNotFound.
type TenantScope struct {
	WorkspaceID uuid.UUID
	UserID      uuid.UUID
}
