package eventlog

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
)

// Store appends events to the log and updates their projections
// (sessions.last_seq, sessions.status) in one transaction (event-log.md
// §5.1). It is not itself tenant-scoped: a nil scope is used by callers that
// already authorized another way (the Worker's claim and fence).
type Store struct {
	pool *pgxpool.Pool
}

// NewStore builds a Store backed by pool.
func NewStore(pool *pgxpool.Pool) *Store {
	return &Store{pool: pool}
}

// Append writes evs, plus next's session.status_changed if set, as the next
// gapless seqs for sid, in one transaction, and returns their assigned seqs.
// When scope is non-nil the update is also filtered by it, so a session in
// another tenant is reported as ErrNotFound.
func (s *Store) Append(ctx context.Context, sid uuid.UUID, scope *TenantScope, evs []NewEvent, next *StatusChange) ([]int64, error) {
	return s.append(ctx, sid, scope, nil, evs, next)
}

// AppendFenced is Append for the Worker holding the Lease: it fails with
// ErrLeaseLost if f no longer owns sid, or ErrStale if f.ExpectSeq is set and
// the log has moved on (event-log.md §5.1).
func (s *Store) AppendFenced(ctx context.Context, sid uuid.UUID, f Fence, evs []NewEvent, next *StatusChange) ([]int64, error) {
	return s.append(ctx, sid, nil, &f, evs, next)
}

func (s *Store) append(ctx context.Context, sid uuid.UUID, scope *TenantScope, f *Fence, evs []NewEvent, next *StatusChange) ([]int64, error) {
	var seqs []int64
	err := pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		var err error
		seqs, err = s.appendTx(ctx, tx, sid, scope, f, evs, next)
		return err
	})
	return seqs, err
}

func (s *Store) appendTx(ctx context.Context, tx pgx.Tx, sid uuid.UUID, scope *TenantScope, f *Fence, evs []NewEvent, next *StatusChange) ([]int64, error) {
	// The status is read under the row lock before the bump, so a
	// conditional StatusChange decides on the status this append really
	// sees.
	var fromStatus string
	q := `SELECT status FROM sessions WHERE id = $1`
	args := []any{sid}
	if scope != nil {
		q += ` AND workspace_id = $2 AND user_id = $3`
		args = append(args, scope.WorkspaceID, scope.UserID)
	}
	if err := tx.QueryRow(ctx, q+` FOR UPDATE`, args...).Scan(&fromStatus); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, ErrNotFound
		}
		return nil, fmt.Errorf("lock session: %w", err)
	}
	if next != nil && !next.applies(fromStatus) {
		next = nil
	}

	evs, wakeAt, err := withTimer(ctx, tx, evs, next)
	if err != nil {
		return nil, err
	}
	n := len(evs)
	if next != nil {
		n++
	}
	if n == 0 && f == nil {
		return nil, nil
	}

	q = `UPDATE sessions SET last_seq = last_seq + $2, updated_at = now()`
	args = []any{sid, n}
	var epoch *int64
	if f != nil {
		q += `, recovery_attempts = 0 WHERE id = $1 AND lease_owner = $3 AND lease_epoch = $4 AND lease_expires_at > now()`
		args = append(args, f.Owner, f.Epoch)
		if f.ExpectSeq != nil {
			q += ` AND last_seq = $5`
			args = append(args, *f.ExpectSeq)
		}
		epoch = &f.Epoch
	} else {
		q += ` WHERE id = $1`
	}
	var last int64
	var workspaceID, userID uuid.UUID
	if err := tx.QueryRow(ctx, q+` RETURNING last_seq, workspace_id, user_id`, args...).Scan(&last, &workspaceID, &userID); err != nil {
		if !errors.Is(err, pgx.ErrNoRows) {
			return nil, fmt.Errorf("bump last_seq: %w", err)
		}
		if f == nil {
			return nil, ErrNotFound
		}
		if f.ExpectSeq != nil && s.fenceHolds(ctx, tx, sid, *f) {
			return nil, ErrStale
		}
		return nil, ErrLeaseLost
	}

	// Copy rather than append to evs directly: its backing array may have
	// spare capacity the caller still owns.
	all := make([]NewEvent, len(evs), len(evs)+1)
	copy(all, evs)
	if next != nil {
		all = append(all, next.event(fromStatus))
	}
	seqs := make([]int64, len(all))
	var delta counters
	for i, e := range all {
		seq := last - int64(len(all)-1-i)
		seqs[i] = seq
		if err := insertEvent(ctx, tx, sid, workspaceID, seq, epoch, e); err != nil {
			return nil, err
		}
		if err := project(ctx, tx, sid, seq, e, &delta); err != nil {
			return nil, err
		}
	}
	if err := delta.apply(ctx, tx, sid); err != nil {
		return nil, err
	}

	if next != nil {
		// Leaving running (parking) or being resumed clears the Lease;
		// only a claim sets it.
		_, err := tx.Exec(ctx, `
			UPDATE sessions SET status = $2,
			  ready_at = CASE WHEN $2 = 'runnable' THEN now() ELSE ready_at END,
			  wake_at = CASE WHEN $2 = 'sleeping' THEN $3::timestamptz ELSE NULL END,
			  cancel_requested = CASE WHEN $2 = 'running' THEN cancel_requested ELSE false END,
			  lease_owner = CASE WHEN $2 = 'running' THEN lease_owner END,
			  lease_expires_at = CASE WHEN $2 = 'running' THEN lease_expires_at END
			WHERE id = $1`, sid, next.To, wakeAt)
		if err != nil {
			return nil, fmt.Errorf("apply status: %w", err)
		}
	}
	activity := slices.ContainsFunc(all, func(e NewEvent) bool {
		return e.Type == TypeStatusChanged || e.Type == TypeSessionCreated
	})
	if err := notifyAppend(ctx, tx, sid, userID, last, next != nil && next.To == StatusRunnable, activity); err != nil {
		return nil, err
	}
	return seqs, nil
}

// withTimer adds the timer.set a change to sleeping with a WakeIn calls for,
// and returns the wake time it carries (nil if none).
func withTimer(ctx context.Context, tx pgx.Tx, evs []NewEvent, next *StatusChange) ([]NewEvent, *time.Time, error) {
	if next == nil || next.To != StatusSleeping || next.WakeIn <= 0 {
		return evs, nil, nil
	}
	var wakeAt time.Time
	if err := tx.QueryRow(ctx, `SELECT now() + $1::interval`, next.WakeIn).Scan(&wakeAt); err != nil {
		return nil, nil, fmt.Errorf("compute wake_at: %w", err)
	}
	timer := NewEvent{Type: TypeTimerSet, Actor: "system", Payload: map[string]any{"wake_at": wakeAt, "reason": next.Reason}}
	return append(slices.Clone(evs), timer), &wakeAt, nil
}

func (s *Store) fenceHolds(ctx context.Context, tx pgx.Tx, sid uuid.UUID, f Fence) bool {
	var ok bool
	err := tx.QueryRow(ctx, `
		SELECT true FROM sessions
		WHERE id = $1 AND lease_owner = $2 AND lease_epoch = $3 AND lease_expires_at > now()`,
		sid, f.Owner, f.Epoch).Scan(&ok)
	return err == nil && ok
}

// counters accumulates the sessions-row projections of one append.
type counters struct {
	tokens, cost int64
	turns        int
}

func (c counters) apply(ctx context.Context, tx pgx.Tx, sid uuid.UUID) error {
	if c == (counters{}) {
		return nil
	}
	_, err := tx.Exec(ctx, `
		UPDATE sessions SET tokens_used = tokens_used + $2, cost_micros = cost_micros + $3, turns = turns + $4
		WHERE id = $1`, sid, c.tokens, c.cost, c.turns)
	if err != nil {
		return fmt.Errorf("project counters: %w", err)
	}
	return nil
}

// project applies e's effect on tables other than events (event-log.md
// §5.1): usage rows and the budget counters.
func project(ctx context.Context, tx pgx.Tx, sid uuid.UUID, seq int64, e NewEvent, c *counters) error {
	switch e.Type {
	case TypeLLMResponse:
		c.turns++
	case TypeUsageRecorded:
		u, ok := e.Payload.(UsageRecorded)
		if !ok {
			return fmt.Errorf("project %s: payload is %T", e.Type, e.Payload)
		}
		_, err := tx.Exec(ctx, `
			INSERT INTO usage (session_id, seq, workspace_id, project_id, user_id, kind, provider, model, quantity, unit, cost_micros, created_at)
			SELECT id, $2, workspace_id, project_id, user_id, $3, $4, NULLIF($5, ''), $6, $7, $8, now()
			FROM sessions WHERE id = $1`,
			sid, seq, u.Kind, u.Provider, u.Model, u.Quantity, u.Unit, u.CostMicros)
		if err != nil {
			return fmt.Errorf("insert usage: %w", err)
		}
		if u.Kind == KindLLM {
			c.tokens += u.Quantity
			c.cost += u.CostMicros
		}
	}
	return nil
}

// Claim is a session leased to a Worker.
type Claim struct {
	SessionID uuid.UUID
	// UserID owns the session, and so the Provider Keys its turns run on.
	UserID uuid.UUID
	Fence  Fence
	// RecoveryAttempts counts claims since the session's last successful
	// fenced append, this one included (event-log.md §5.6).
	RecoveryAttempts int
}

// Claim leases the oldest runnable session (or one whose Lease expired) to
// owner for ttl, or reports false if there is none (event-log.md §5.2). It
// bumps lease_epoch and appends session.status_changed{running} in the same
// tx. Sessions in skip are passed over, so a Worker that released one doesn't
// take it straight back.
func (s *Store) Claim(ctx context.Context, owner string, ttl time.Duration, skip ...uuid.UUID) (Claim, bool, error) {
	if skip == nil {
		skip = []uuid.UUID{}
	}
	var c Claim
	var found bool
	err := pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		var workspaceID uuid.UUID
		var last int64
		var from string
		err := tx.QueryRow(ctx, `
			WITH c AS (
			  SELECT id, status FROM sessions
			  WHERE (status = 'runnable' OR (status = 'sleeping' AND wake_at <= now())
			         OR (status = 'running' AND lease_expires_at < now()))
			    AND id <> ALL($3)
			  ORDER BY ready_at NULLS LAST
			  FOR UPDATE SKIP LOCKED LIMIT 1)
			UPDATE sessions s SET status = 'running', lease_owner = $1, lease_epoch = s.lease_epoch + 1,
			  lease_expires_at = now() + $2::interval,
			  recovery_attempts = s.recovery_attempts + 1, wake_at = NULL,
			  last_seq = s.last_seq + CASE WHEN c.status = 'sleeping' THEN 2 ELSE 1 END, updated_at = now()
			FROM c WHERE s.id = c.id
			RETURNING s.id, s.lease_epoch, s.recovery_attempts, s.last_seq, s.workspace_id, s.user_id, c.status`,
			owner, ttl, skip).Scan(&c.SessionID, &c.Fence.Epoch, &c.RecoveryAttempts, &last, &workspaceID, &c.UserID, &from)
		if errors.Is(err, pgx.ErrNoRows) {
			return nil
		}
		if err != nil {
			return fmt.Errorf("claim session: %w", err)
		}
		c.Fence.Owner = owner
		found = true

		if from == StatusSleeping {
			fired := NewEvent{Type: TypeTimerFired, Actor: "system", Payload: map[string]string{"reason": "wake_at"}}
			if err := insertEvent(ctx, tx, c.SessionID, workspaceID, last-1, &c.Fence.Epoch, fired); err != nil {
				return err
			}
		}
		ev := StatusChange{To: StatusRunning, Reason: "claimed"}.event(from)
		if err := insertEvent(ctx, tx, c.SessionID, workspaceID, last, &c.Fence.Epoch, ev); err != nil {
			return err
		}
		return notifyAppend(ctx, tx, c.SessionID, c.UserID, last, false, true)
	})
	return c, found, err
}

// Heartbeat extends f's Lease to ttl from now and reports whether the User
// asked to interrupt the session, or returns ErrLeaseLost if f no longer
// holds the Lease (event-log.md §5.2).
func (s *Store) Heartbeat(ctx context.Context, sid uuid.UUID, f Fence, ttl time.Duration) (cancelRequested bool, err error) {
	err = s.pool.QueryRow(ctx, `
		UPDATE sessions SET lease_expires_at = now() + $4::interval
		WHERE id = $1 AND lease_owner = $2 AND lease_epoch = $3 AND lease_expires_at > now()
		RETURNING cancel_requested`,
		sid, f.Owner, f.Epoch, ttl).Scan(&cancelRequested)
	if errors.Is(err, pgx.ErrNoRows) {
		return false, ErrLeaseLost
	}
	if err != nil {
		return false, fmt.Errorf("heartbeat: %w", err)
	}
	return cancelRequested, nil
}

// Release hands f's Lease back without writing an event: the session stays
// running with an expired Lease, which the next Claim rescues. This claim's
// recovery attempt is undone so releasing never counts toward the crash-loop
// limit. It returns ErrLeaseLost if f no longer holds the Lease.
func (s *Store) Release(ctx context.Context, sid uuid.UUID, f Fence) error {
	var notified string
	err := s.pool.QueryRow(ctx, `
		WITH r AS (
		  UPDATE sessions SET lease_expires_at = now(), recovery_attempts = greatest(recovery_attempts - 1, 0)
		  WHERE id = $1 AND lease_owner = $2 AND lease_epoch = $3 AND lease_expires_at > now()
		  RETURNING id)
		SELECT pg_notify('jf_runnable', id::text)::text FROM r`,
		sid, f.Owner, f.Epoch).Scan(&notified)
	if errors.Is(err, pgx.ErrNoRows) {
		return ErrLeaseLost
	}
	if err != nil {
		return fmt.Errorf("release: %w", err)
	}
	return nil
}

// Load returns all of sid's events in order, for the Worker's fold. Payloads
// are as stored; run them through Upcasters.Events before folding.
func (s *Store) Load(ctx context.Context, sid uuid.UUID) ([]Event, error) {
	rows, err := s.pool.Query(ctx, `
		SELECT seq, type, schema_version, actor, coalesce(correlation_id, ''), payload, created_at
		FROM events WHERE session_id = $1 ORDER BY seq`, sid)
	if err != nil {
		return nil, fmt.Errorf("query events: %w", err)
	}
	defer rows.Close()

	var evs []Event
	for rows.Next() {
		var e Event
		if err := rows.Scan(&e.Seq, &e.Type, &e.SchemaVersion, &e.Actor, &e.CorrelationID, &e.Payload, &e.CreatedAt); err != nil {
			return nil, fmt.Errorf("scan event: %w", err)
		}
		evs = append(evs, e)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate events: %w", err)
	}
	return evs, nil
}

// notifyAppend emits the jf_runnable (if runnable), jf_activity (if activity:
// the batch holds a session.status_changed or session.created) and jf_events
// NOTIFYs for a batch appended at seq, shared by CreateSession's insert path
// and appendTx (event-log.md §5.1, §5.12; streaming.md D17).
func notifyAppend(ctx context.Context, tx pgx.Tx, sid, userID uuid.UUID, seq int64, runnable, activity bool) error {
	if runnable {
		if _, err := tx.Exec(ctx, `SELECT pg_notify('jf_runnable', $1)`, sid.String()); err != nil {
			return fmt.Errorf("notify jf_runnable: %w", err)
		}
	}
	if activity {
		if _, err := tx.Exec(ctx, `SELECT pg_notify('jf_activity', $1)`, userID.String()+":"+sid.String()); err != nil {
			return fmt.Errorf("notify jf_activity: %w", err)
		}
	}
	if _, err := tx.Exec(ctx, `SELECT pg_notify('jf_events', $1)`, fmt.Sprintf("%s:%d", sid, seq)); err != nil {
		return fmt.Errorf("notify jf_events: %w", err)
	}
	return nil
}

func insertEvent(ctx context.Context, tx pgx.Tx, sid, workspaceID uuid.UUID, seq int64, leaseEpoch *int64, e NewEvent) error {
	payload, err := json.Marshal(e.Payload)
	if err != nil {
		return fmt.Errorf("marshal %s payload: %w", e.Type, err)
	}
	_, err = tx.Exec(ctx, `
		INSERT INTO events (session_id, seq, workspace_id, type, actor, correlation_id, lease_epoch, payload)
		VALUES ($1, $2, $3, $4, $5, NULLIF($6, ''), $7, $8)`,
		sid, seq, workspaceID, e.Type, e.Actor, e.CorrelationID, leaseEpoch, payload)
	if err != nil {
		if isUniqueViolation(err) {
			return ErrDuplicate
		}
		return fmt.Errorf("insert %s: %w", e.Type, err)
	}
	return nil
}

func isUniqueViolation(err error) bool {
	var pgErr *pgconn.PgError
	return errors.As(err, &pgErr) && pgErr.Code == "23505"
}
