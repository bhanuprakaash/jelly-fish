package eventlog

import (
	"context"
	"fmt"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

// InterruptUserSessions interrupts every running session of the User with
// reason user_disabled, as actor, in one transaction. Parked sessions are
// left alone (auth-keys.md §5.7). The caller's authorization is the
// Admin-only route, not a TenantScope.
func (s *Store) InterruptUserSessions(ctx context.Context, userID uuid.UUID, actor string) error {
	interrupt := NewEvent{Type: TypeUserInterrupt, Actor: actor, Payload: map[string]string{"reason": "user_disabled"}}
	return pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		rows, err := tx.Query(ctx, `SELECT id FROM sessions WHERE user_id = $1 AND status = $2 FOR UPDATE`, userID, StatusRunning)
		if err != nil {
			return fmt.Errorf("lock running sessions: %w", err)
		}
		ids, err := pgx.CollectRows(rows, pgx.RowTo[uuid.UUID])
		if err != nil {
			return fmt.Errorf("read running sessions: %w", err)
		}
		for _, id := range ids {
			if err := s.interruptRunning(ctx, tx, id, nil, interrupt); err != nil {
				return err
			}
		}
		return nil
	})
}

func (s *Store) interruptRunning(ctx context.Context, tx pgx.Tx, sessionID uuid.UUID, scope *TenantScope, interrupt NewEvent) error {
	if _, err := s.appendTx(ctx, tx, sessionID, scope, nil, []NewEvent{interrupt}, nil); err != nil {
		return err
	}
	if _, err := tx.Exec(ctx, `UPDATE sessions SET cancel_requested = true WHERE id = $1`, sessionID); err != nil {
		return fmt.Errorf("flag cancel: %w", err)
	}
	if _, err := tx.Exec(ctx, `SELECT pg_notify('jf_cancel', $1)`, sessionID.String()); err != nil {
		return fmt.Errorf("notify cancel: %w", err)
	}
	return nil
}
