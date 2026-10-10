package eventlog

import (
	"context"
	"fmt"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

// InterruptUserSessions interrupts every live session of the User with
// reason user_disabled, as actor, in one transaction: a running session is
// cancelled, a runnable or sleeping one parks as awaiting_user. Sessions
// already awaiting the User or an approval are left alone (auth-keys.md
// §5.7). The caller's authorization is the Admin-only route, not a
// TenantScope.
func (s *Store) InterruptUserSessions(ctx context.Context, userID uuid.UUID, actor string) error {
	interrupt := NewEvent{Type: TypeUserInterrupt, Actor: actor, Payload: map[string]string{"reason": "user_disabled"}}
	return pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		rows, err := tx.Query(ctx, `
			SELECT id, status FROM sessions
			WHERE user_id = $1 AND status IN ($2, $3, $4) FOR UPDATE`,
			userID, StatusRunning, StatusRunnable, StatusSleeping)
		if err != nil {
			return fmt.Errorf("lock live sessions: %w", err)
		}
		live, err := pgx.CollectRows(rows, pgx.RowToStructByPos[struct {
			ID     uuid.UUID
			Status string
		}])
		if err != nil {
			return fmt.Errorf("read live sessions: %w", err)
		}
		for _, l := range live {
			if l.Status == StatusRunning {
				err = s.interruptRunning(ctx, tx, l.ID, nil, interrupt)
			} else {
				_, err = s.appendTx(ctx, tx, l.ID, nil, nil, []NewEvent{interrupt}, &StatusChange{To: StatusAwaitingUser, Reason: "interrupted"})
			}
			if err != nil {
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
