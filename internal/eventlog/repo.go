package eventlog

import (
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
	pool  *pgxpool.Pool
	store *Store
}

// NewRepo builds a Repo backed by pool.
func NewRepo(pool *pgxpool.Pool) *Repo {
	return &Repo{pool: pool, store: NewStore(pool)}
}

var errNoPersonalProject = errors.New("user has no Personal project")

// CreateSession creates a session in the User's "Personal" Project with a session.created + user.message pair
// (session.created snapshotting the hard-coded "General" agent) and returns
// its last_seq. Repeating the same sessionID returns the existing session's
// last_seq instead of creating a second one.
func (r *Repo) CreateSession(ctx context.Context, scope TenantScope, sessionID, clientMsgID uuid.UUID, text string) (int64, error) {
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
				INSERT INTO sessions (id, workspace_id, project_id, user_id, agent_id, status, last_seq, ready_at)
				SELECT $1, $2, p.id, $3, $4, $5, 2, now()
				FROM projects p
				WHERE p.workspace_id = $2 AND p.user_id = $3 AND p.name = 'Personal'`,
				sessionID, scope.WorkspaceID, scope.UserID, generalAgent(), StatusRunnable)
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
			Type: TypeSessionCreated, Actor: actor, Payload: sessionCreatedPayload(),
		}); err != nil {
			return err
		}
		if err := insertEvent(ctx, tx, sessionID, scope.WorkspaceID, 2, nil, NewEvent{
			Type: TypeUserMessage, Actor: actor, Payload: userMessagePayload(clientMsgID, text),
		}); err != nil {
			return err
		}
		if err := notifyAppend(ctx, tx, sessionID, scope.UserID, 2, true); err != nil {
			return err
		}
		last = 2
		return nil
	})
	return last, err
}

// PostMessage appends a follow-up user.message to an existing session and
// returns its seq, resuming the session if it was waiting on the user.
// Repeating the same clientMsgID returns the existing seq instead of
// appending a second event.
func (r *Repo) PostMessage(ctx context.Context, scope TenantScope, sessionID, clientMsgID uuid.UUID, text string) (int64, error) {
	actor := "user:" + scope.UserID.String()
	seqs, err := r.store.Append(ctx, sessionID, &scope, []NewEvent{
		{Type: TypeUserMessage, Actor: actor, Payload: userMessagePayload(clientMsgID, text)},
	}, &StatusChange{
		To:     StatusRunnable,
		Reason: "user_message",
		From:   []string{StatusAwaitingUser, StatusCompleted, StatusFailed},
	})
	if err != nil {
		if errors.Is(err, ErrDuplicate) {
			return r.existingMessageSeq(ctx, scope, sessionID, clientMsgID)
		}
		return 0, err
	}
	return seqs[0], nil
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
