package eventlog

import (
	"context"
	"errors"
	"fmt"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

// ActivityRow is one session's status as the Activity Stream reports it.
type ActivityRow struct {
	SessionID uuid.UUID
	// RootID is the top-level session of SessionID's chat, found through the
	// parent chain (at most two levels deep).
	RootID   uuid.UUID
	ParentID *uuid.UUID
	Status   string
}

// activitySelect reads the User's sessions whose root was started by a User
// message, so System Sessions and their children never show.
const activitySelect = `
	SELECT s.id, r.id, s.parent_id, s.status
	FROM sessions s
	LEFT JOIN sessions p ON p.id = s.parent_id
	JOIN sessions r ON r.id = COALESCE(p.parent_id, s.parent_id, s.id)
	WHERE s.workspace_id = $1 AND s.user_id = $2 AND r.trigger = $3`

// ActivitySnapshot returns the User's sessions that are not waiting on the
// User or finished (streaming.md §4.2), oldest first.
func (r *Repo) ActivitySnapshot(ctx context.Context, scope TenantScope) ([]ActivityRow, error) {
	rows, err := r.pool.Query(ctx, activitySelect+`
		  AND s.status NOT IN ($4, $5)
		ORDER BY s.created_at, s.id`,
		scope.WorkspaceID, scope.UserID, TriggerUserMessage, StatusAwaitingUser, StatusCompleted)
	if err != nil {
		return nil, fmt.Errorf("query activity snapshot: %w", err)
	}
	out, err := pgx.CollectRows(rows, scanActivityRow)
	if err != nil {
		return nil, fmt.Errorf("read activity snapshot: %w", err)
	}
	return out, nil
}

// ActivityStatusOf returns sid's current activity row in any status, so a
// frame can clear a badge. It reports false for a session that is missing,
// another tenant's, or part of a System Session's tree.
func (r *Repo) ActivityStatusOf(ctx context.Context, scope TenantScope, sid uuid.UUID) (ActivityRow, bool, error) {
	rows, err := r.pool.Query(ctx, activitySelect+` AND s.id = $4`,
		scope.WorkspaceID, scope.UserID, TriggerUserMessage, sid)
	if err != nil {
		return ActivityRow{}, false, fmt.Errorf("query activity status: %w", err)
	}
	row, err := pgx.CollectOneRow(rows, scanActivityRow)
	if errors.Is(err, pgx.ErrNoRows) {
		return ActivityRow{}, false, nil
	}
	if err != nil {
		return ActivityRow{}, false, fmt.Errorf("read activity status: %w", err)
	}
	return row, true, nil
}

func scanActivityRow(row pgx.CollectableRow) (ActivityRow, error) {
	var a ActivityRow
	err := row.Scan(&a.SessionID, &a.RootID, &a.ParentID, &a.Status)
	return a, err
}
