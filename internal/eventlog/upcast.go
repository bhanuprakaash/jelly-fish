package eventlog

import (
	"errors"
	"fmt"
)

// ErrSchemaTooNew is returned for an event written in a newer schema_version
// than this binary has upcasters for, so it cannot be read safely
// (event-log.md §5.19).
var ErrSchemaTooNew = errors.New("event schema is newer than this worker knows")

// Upcaster rewrites a payload one schema_version forward. It must be pure.
type Upcaster func(payload []byte) ([]byte, error)

// Upcasters maps an event type to its upcasters in version order: element i
// turns version i+1 into i+2. A type with none is known at version 1, so the
// zero value is usable. It is read-only once wired into a Worker, and stored
// rows are never rewritten (event-log.md §5.4).
type Upcasters map[string][]Upcaster

// Upcast brings payload, stored at version, up to the latest version of typ.
// It returns ErrSchemaTooNew if version is beyond the latest.
func (u Upcasters) Upcast(typ string, version int, payload []byte) ([]byte, error) {
	steps := u[typ]
	if version > len(steps)+1 {
		return nil, fmt.Errorf("%s v%d: %w", typ, version, ErrSchemaTooNew)
	}
	for v := max(version, 1); v <= len(steps); v++ {
		var err error
		if payload, err = steps[v-1](payload); err != nil {
			return nil, fmt.Errorf("upcast %s v%d: %w", typ, v, err)
		}
	}
	return payload, nil
}

// Events returns evs with every payload upcast to the latest version and
// SchemaVersion set to match; evs itself is left untouched.
func (u Upcasters) Events(evs []Event) ([]Event, error) {
	out := make([]Event, len(evs))
	for i, e := range evs {
		p, err := u.Upcast(e.Type, e.SchemaVersion, e.Payload)
		if err != nil {
			return nil, fmt.Errorf("seq %d: %w", e.Seq, err)
		}
		e.Payload = p
		e.SchemaVersion = len(u[e.Type]) + 1
		out[i] = e
	}
	return out, nil
}
