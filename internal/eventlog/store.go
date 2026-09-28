package eventlog

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
)

// Store appends events to the log and updates their projections
// (sessions.last_seq, sessions.status) in one transaction (event-log.md
// §5.1). It is not itself tenant-scoped: a nil scope is used by callers that
// already authorized another way (the Worker's claim, in a later slice).
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
	var seqs []int64
	err := pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		var err error
		seqs, err = s.appendTx(ctx, tx, sid, scope, evs, next)
		return err
	})
	return seqs, err
}

func (s *Store) appendTx(ctx context.Context, tx pgx.Tx, sid uuid.UUID, scope *TenantScope, evs []NewEvent, next *StatusChange) ([]int64, error) {
	n := len(evs)
	if next != nil {
		n++
	}

	q := `UPDATE sessions SET last_seq = last_seq + $2, updated_at = now() WHERE id = $1`
	args := []any{sid, n}
	if scope != nil {
		q += ` AND workspace_id = $3 AND user_id = $4`
		args = append(args, scope.WorkspaceID, scope.UserID)
	}
	q += ` RETURNING last_seq, workspace_id, user_id, status`

	var last int64
	var workspaceID, userID uuid.UUID
	var fromStatus string
	if err := tx.QueryRow(ctx, q, args...).Scan(&last, &workspaceID, &userID, &fromStatus); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, ErrNotFound
		}
		return nil, fmt.Errorf("bump last_seq: %w", err)
	}

	// Copy rather than append to evs directly: its backing array may have
	// spare capacity the caller still owns.
	all := make([]NewEvent, len(evs), len(evs)+1)
	copy(all, evs)
	if next != nil {
		all = append(all, next.event(fromStatus))
	}
	seqs := make([]int64, len(all))
	for i, e := range all {
		seq := last - int64(len(all)-1-i)
		seqs[i] = seq
		if err := insertEvent(ctx, tx, sid, workspaceID, seq, e); err != nil {
			return nil, err
		}
	}

	if next != nil {
		if _, err := tx.Exec(ctx, `UPDATE sessions SET status = $2 WHERE id = $1`, sid, next.To); err != nil {
			return nil, fmt.Errorf("apply status: %w", err)
		}
		if next.To == StatusRunnable {
			if _, err := tx.Exec(ctx, `SELECT pg_notify('jf_runnable', $1)`, sid.String()); err != nil {
				return nil, fmt.Errorf("notify jf_runnable: %w", err)
			}
		}
		if _, err := tx.Exec(ctx, `SELECT pg_notify('jf_activity', $1)`, userID.String()+":"+sid.String()); err != nil {
			return nil, fmt.Errorf("notify jf_activity: %w", err)
		}
	}
	if _, err := tx.Exec(ctx, `SELECT pg_notify('jf_events', $1)`, fmt.Sprintf("%s:%d", sid, last)); err != nil {
		return nil, fmt.Errorf("notify jf_events: %w", err)
	}
	return seqs, nil
}

func insertEvent(ctx context.Context, tx pgx.Tx, sid, workspaceID uuid.UUID, seq int64, e NewEvent) error {
	payload, err := json.Marshal(e.Payload)
	if err != nil {
		return fmt.Errorf("marshal %s payload: %w", e.Type, err)
	}
	_, err = tx.Exec(ctx, `
		INSERT INTO events (session_id, seq, workspace_id, type, actor, correlation_id, payload)
		VALUES ($1, $2, $3, $4, $5, NULLIF($6, ''), $7)`,
		sid, seq, workspaceID, e.Type, e.Actor, e.CorrelationID, payload)
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
