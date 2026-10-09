package eventlog

import (
	"cmp"
	"context"
	"errors"
	"fmt"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// Repo is the API-facing, tenant-scoped view of the event log. Every
// exported method takes a TenantScope (enforced by TestRepoMethodsTakeScope)
// and reports a session outside it as ErrNotFound (event-log.md §5.15).
type Repo struct {
	pool         *pgxpool.Pool
	store        *Store
	defaultModel string
}

// NewRepo builds a Repo backed by pool whose new sessions use defaultModel.
func NewRepo(pool *pgxpool.Pool, defaultModel string) *Repo {
	return &Repo{pool: pool, store: NewStore(pool), defaultModel: defaultModel}
}

var errNoPersonalProject = errors.New("user has no Personal project")

// CreateSession creates a session in the User's "Personal" Project with a
// session.created + user.message pair (session.created snapshotting the
// hard-coded "General" agent on model, or the default model if it is empty)
// and returns its last_seq. Repeating the same sessionID returns the existing session's
// last_seq instead of creating a second one. An incognito session never gets
// the memory tool or memory in its prompt.
func (r *Repo) CreateSession(ctx context.Context, scope TenantScope, sessionID, clientMsgID uuid.UUID, text, model string, incognito bool) (int64, error) {
	var last int64
	err := pgx.BeginFunc(ctx, r.pool, func(tx pgx.Tx) error {
		found, err := scanLastSeq(ctx, tx, scope, sessionID, &last)
		if err != nil {
			return err
		}
		if found {
			return nil
		}

		// A savepoint, so a unique-violation here only unwinds the insert:
		// Postgres otherwise aborts the whole transaction on any statement
		// error, which would break the fallback query below.
		insertErr := pgx.BeginFunc(ctx, tx, func(spTx pgx.Tx) error {
			tag, err := spTx.Exec(ctx, `
				INSERT INTO sessions (id, workspace_id, project_id, user_id, agent_id, status, last_seq, ready_at, trigger, incognito)
				SELECT $1, $2, p.id, $3, $4, $5, 2, now(), $7, $8
				FROM projects p
				WHERE p.workspace_id = $2 AND p.user_id = $3 AND p.name = $6`,
				sessionID, scope.WorkspaceID, scope.UserID, generalAgent(), StatusRunnable, PersonalProject, TriggerUserMessage, incognito)
			if err == nil && tag.RowsAffected() == 0 {
				return errNoPersonalProject
			}
			return err
		})
		if insertErr != nil {
			if isUniqueViolation(insertErr) {
				// Raced with another create, or the id collides with another
				// tenant's session: either way, resolve like a repeat.
				found, err := scanLastSeq(ctx, tx, scope, sessionID, &last)
				if err != nil {
					return err
				}
				if !found {
					return ErrNotFound
				}
				return nil
			}
			return fmt.Errorf("insert session: %w", insertErr)
		}

		actor := "user:" + scope.UserID.String()
		if err := insertEvent(ctx, tx, sessionID, scope.WorkspaceID, 1, nil, NewEvent{
			Type: TypeSessionCreated, Actor: actor, Payload: sessionCreatedPayload(cmp.Or(model, r.defaultModel)),
		}); err != nil {
			return err
		}
		if err := insertEvent(ctx, tx, sessionID, scope.WorkspaceID, 2, nil, NewEvent{
			Type: TypeUserMessage, Actor: actor, Payload: userMessagePayload(clientMsgID, text),
		}); err != nil {
			return err
		}
		if err := notifyAppend(ctx, tx, sessionID, scope.UserID, 2, true, true); err != nil {
			return err
		}
		last = 2
		return nil
	})
	return last, err
}

// PostMessage appends a follow-up user.message to an existing session and
// returns its seq, resuming the session if it was waiting on the user or
// sleeping before a retry.
// Repeating the same clientMsgID returns the existing seq instead of
// appending a second event.
func (r *Repo) PostMessage(ctx context.Context, scope TenantScope, sessionID, clientMsgID uuid.UUID, text string) (int64, error) {
	actor := "user:" + scope.UserID.String()
	seqs, err := r.store.Append(ctx, sessionID, &scope, []NewEvent{
		{Type: TypeUserMessage, Actor: actor, Payload: userMessagePayload(clientMsgID, text)},
	}, &StatusChange{
		To:     StatusRunnable,
		Reason: "user_message",
		From:   []string{StatusAwaitingUser, StatusSleeping, StatusCompleted, StatusFailed},
	})
	if err != nil {
		if errors.Is(err, ErrDuplicate) {
			return r.existingMessageSeq(ctx, scope, sessionID, clientMsgID)
		}
		return 0, err
	}
	return seqs[0], nil
}

// Interrupt appends the User's user.interrupt. A sleeping session ("Stop
// retrying") parks as awaiting_user at once. A running one is only flagged:
// its Worker's heartbeat cancels the in-flight call (event-log.md §5.8, §8).
// A session in any other status is left alone (§5.17).
func (r *Repo) Interrupt(ctx context.Context, scope TenantScope, sessionID uuid.UUID) error {
	interrupt := NewEvent{Type: TypeUserInterrupt, Actor: "user:" + scope.UserID.String(), Payload: map[string]string{"reason": "user_request"}}
	return pgx.BeginFunc(ctx, r.pool, func(tx pgx.Tx) error {
		status, err := lockStatus(ctx, tx, scope, sessionID)
		if err != nil {
			return err
		}
		switch status {
		case StatusSleeping:
			_, err = r.store.appendTx(ctx, tx, sessionID, &scope, nil, []NewEvent{interrupt}, &StatusChange{To: StatusAwaitingUser, Reason: "interrupted"})
		case StatusRunning:
			if _, err = r.store.appendTx(ctx, tx, sessionID, &scope, nil, []NewEvent{interrupt}, nil); err == nil {
				_, err = tx.Exec(ctx, `UPDATE sessions SET cancel_requested = true WHERE id = $1`, sessionID)
			}
		}
		return err
	})
}

// Retry is the User's "Retry now". It makes a session runnable when it is
// sleeping, failed, or awaiting_user because its last turn hit a stop-class
// error (the newest event other than a status change is a session.error
// with retryable false). An awaiting_user session that ended normally, or
// a session in any other status, is left alone.
func (r *Repo) Retry(ctx context.Context, scope TenantScope, sessionID uuid.UUID) error {
	return pgx.BeginFunc(ctx, r.pool, func(tx pgx.Tx) error {
		status, err := lockStatus(ctx, tx, scope, sessionID)
		if err != nil {
			return err
		}
		switch status {
		case StatusSleeping, StatusFailed:
		case StatusAwaitingUser:
			stopped, err := endsInStopError(ctx, tx, sessionID)
			if err != nil || !stopped {
				return err
			}
		default:
			return nil
		}
		if _, err := r.store.appendTx(ctx, tx, sessionID, &scope, nil, nil, &StatusChange{To: StatusRunnable, Reason: "user_retry"}); err != nil {
			return err
		}
		// A session failed by crash_loop would otherwise fail again at once.
		if _, err := tx.Exec(ctx, `UPDATE sessions SET recovery_attempts = 0 WHERE id = $1`, sessionID); err != nil {
			return fmt.Errorf("reset recovery attempts: %w", err)
		}
		return nil
	})
}

// ChangeModel appends the User's session.config_changed{model}; the next
// turn runs on model. A session awaiting the user after an error another
// model can fix (model_unavailable, billing) becomes runnable too, so that
// turn re-runs on the new model.
func (r *Repo) ChangeModel(ctx context.Context, scope TenantScope, sessionID uuid.UUID, model string) error {
	changed := NewEvent{Type: TypeConfigChanged, Actor: "user:" + scope.UserID.String(), Payload: map[string]string{"model": model}}
	return pgx.BeginFunc(ctx, r.pool, func(tx pgx.Tx) error {
		status, err := lockStatus(ctx, tx, scope, sessionID)
		if err != nil {
			return err
		}
		var resume *StatusChange
		if status == StatusAwaitingUser {
			fixable, err := endsInModelError(ctx, tx, sessionID)
			if err != nil {
				return err
			}
			if fixable {
				resume = &StatusChange{To: StatusRunnable, Reason: "model_changed"}
			}
		}
		_, err = r.store.appendTx(ctx, tx, sessionID, &scope, nil, []NewEvent{changed}, resume)
		return err
	})
}

// Rename sets the Title of a top-level session to title, which the caller has
// already trimmed and checked. A Child Session is refused with
// ErrChildSession. Like every API write it is unfenced, so it lands even
// while a Worker holds the Lease.
func (r *Repo) Rename(ctx context.Context, scope TenantScope, sessionID uuid.UUID, title string) error {
	renamed := NewEvent{
		Type:    TypeSessionRenamed,
		Actor:   "user:" + scope.UserID.String(),
		Payload: SessionRenamed{Title: title, By: RenamedByUser},
	}
	return pgx.BeginFunc(ctx, r.pool, func(tx pgx.Tx) error {
		var child bool
		err := tx.QueryRow(ctx,
			`SELECT parent_id IS NOT NULL FROM sessions WHERE id = $1 AND workspace_id = $2 AND user_id = $3 FOR UPDATE`,
			sessionID, scope.WorkspaceID, scope.UserID).Scan(&child)
		if errors.Is(err, pgx.ErrNoRows) {
			return ErrNotFound
		}
		if err != nil {
			return fmt.Errorf("lock session: %w", err)
		}
		if child {
			return ErrChildSession
		}
		_, err = r.store.appendTx(ctx, tx, sessionID, &scope, nil, []NewEvent{renamed}, nil)
		return err
	})
}

func lockStatus(ctx context.Context, tx pgx.Tx, scope TenantScope, sessionID uuid.UUID) (string, error) {
	var status string
	err := tx.QueryRow(ctx,
		`SELECT status FROM sessions WHERE id = $1 AND workspace_id = $2 AND user_id = $3 FOR UPDATE`,
		sessionID, scope.WorkspaceID, scope.UserID).Scan(&status)
	if errors.Is(err, pgx.ErrNoRows) {
		return "", ErrNotFound
	}
	if err != nil {
		return "", fmt.Errorf("lock session: %w", err)
	}
	return status, nil
}

func endsInStopError(ctx context.Context, tx pgx.Tx, sessionID uuid.UUID) (bool, error) {
	var stop bool
	err := tx.QueryRow(ctx, `
		SELECT type = $2 AND payload->>'retryable' = 'false' FROM events
		WHERE session_id = $1 AND type <> $3
		ORDER BY seq DESC LIMIT 1`, sessionID, TypeSessionError, TypeStatusChanged).Scan(&stop)
	if err != nil {
		return false, fmt.Errorf("look up last event: %w", err)
	}
	return stop, nil
}

// endsInModelError reports whether the newest event other than a status
// change is a session.error that switching model can fix.
func endsInModelError(ctx context.Context, tx pgx.Tx, sessionID uuid.UUID) (bool, error) {
	var fixable bool
	err := tx.QueryRow(ctx, `
		SELECT type = $2 AND payload->>'code' IN ('model_unavailable', 'billing') FROM events
		WHERE session_id = $1 AND type <> $3
		ORDER BY seq DESC LIMIT 1`, sessionID, TypeSessionError, TypeStatusChanged).Scan(&fixable)
	if err != nil {
		return false, fmt.Errorf("look up last event: %w", err)
	}
	return fixable, nil
}

func (r *Repo) existingMessageSeq(ctx context.Context, scope TenantScope, sessionID, clientMsgID uuid.UUID) (int64, error) {
	var seq int64
	err := r.pool.QueryRow(ctx, `
		SELECT e.seq FROM events e JOIN sessions s ON s.id = e.session_id
		WHERE e.session_id = $1 AND s.workspace_id = $2 AND s.user_id = $3
		  AND e.type = $4 AND e.payload->>'client_msg_id' = $5`,
		sessionID, scope.WorkspaceID, scope.UserID, TypeUserMessage, clientMsgID.String(),
	).Scan(&seq)
	if errors.Is(err, pgx.ErrNoRows) {
		// Store.Append only returned ErrDuplicate because this row exists;
		// its absence here means something else changed the log concurrently,
		// not that the session itself is missing.
		return 0, fmt.Errorf("look up existing user.message for client_msg_id %s: %w", clientMsgID, err)
	}
	return seq, err
}

// SessionLastSeq returns a session's last_seq, or ErrNotFound if it doesn't
// exist in scope.
func (r *Repo) SessionLastSeq(ctx context.Context, scope TenantScope, sessionID uuid.UUID) (int64, error) {
	var last int64
	found, err := scanLastSeq(ctx, r.pool, scope, sessionID, &last)
	if err != nil {
		return 0, err
	}
	if !found {
		return 0, ErrNotFound
	}
	return last, nil
}

// ListEvents returns sid's events with seq > after, in order.
func (r *Repo) ListEvents(ctx context.Context, scope TenantScope, sessionID uuid.UUID, after int64) ([]Event, error) {
	rows, err := r.pool.Query(ctx, `
		SELECT e.seq, e.type, e.actor, coalesce(e.correlation_id, ''), e.payload, e.created_at
		FROM events e JOIN sessions s ON s.id = e.session_id
		WHERE e.session_id = $1 AND s.workspace_id = $2 AND s.user_id = $3 AND e.seq > $4
		ORDER BY e.seq`,
		sessionID, scope.WorkspaceID, scope.UserID, after)
	if err != nil {
		return nil, fmt.Errorf("query events: %w", err)
	}
	defer rows.Close()

	var evs []Event
	for rows.Next() {
		var e Event
		if err := rows.Scan(&e.Seq, &e.Type, &e.Actor, &e.CorrelationID, &e.Payload, &e.CreatedAt); err != nil {
			return nil, fmt.Errorf("scan event: %w", err)
		}
		evs = append(evs, e)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate events: %w", err)
	}
	return evs, nil
}

type queryRower interface {
	QueryRow(ctx context.Context, sql string, args ...any) pgx.Row
}

func scanLastSeq(ctx context.Context, q queryRower, scope TenantScope, sessionID uuid.UUID, last *int64) (bool, error) {
	err := q.QueryRow(ctx,
		`SELECT last_seq FROM sessions WHERE id = $1 AND workspace_id = $2 AND user_id = $3`,
		sessionID, scope.WorkspaceID, scope.UserID,
	).Scan(last)
	if errors.Is(err, pgx.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("look up session: %w", err)
	}
	return true, nil
}
