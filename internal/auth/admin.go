package auth

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

// InviteLifetime is how long an Invite stays open after it is sent.
const InviteLifetime = 7 * 24 * time.Hour

// ErrAlreadyUser is returned when inviting an email that already has a User.
var ErrAlreadyUser = errors.New("already a user")

// ErrLastAdmin is returned when disabling a User would leave no active Admin.
var ErrLastAdmin = errors.New("last active admin")

// ManagedUser is a User as an Admin sees them.
type ManagedUser struct {
	ID         uuid.UUID
	Email      string
	Name       string
	IsAdmin    bool
	DisabledAt *time.Time
	CreatedAt  time.Time
}

// Invite is an Admin's permission for one email to sign in for the first time.
type Invite struct {
	ID        uuid.UUID
	Email     string
	ExpiresAt time.Time
	CreatedAt time.Time
}

// CreateInvite opens an Invite for email, or re-opens the one already there
// with a fresh expiry; created is false in that case. It is ErrAlreadyUser
// when email belongs to a User (auth-keys.md §4.1).
func (s *Store) CreateInvite(ctx context.Context, invitedBy uuid.UUID, email string) (inv Invite, created bool, err error) {
	email = NormalizeEmail(email)
	err = pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		// Same lock as login, so an Invite cannot be opened for an email whose
		// User a login is creating right now.
		if _, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtext($1))`, email); err != nil {
			return fmt.Errorf("lock email: %w", err)
		}
		var exists bool
		if err := tx.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM users WHERE email = $1)`, email).Scan(&exists); err != nil {
			return fmt.Errorf("look up user: %w", err)
		}
		if exists {
			return ErrAlreadyUser
		}
		// xmax is 0 only on a freshly inserted row, which tells an insert from
		// the ON CONFLICT update.
		err := tx.QueryRow(ctx, `
			INSERT INTO invites (id, email, invited_by, expires_at)
			VALUES ($1, $2, $3, now() + make_interval(secs => $4))
			ON CONFLICT (email) WHERE accepted_at IS NULL
			DO UPDATE SET expires_at = EXCLUDED.expires_at, invited_by = EXCLUDED.invited_by
			RETURNING id, email, expires_at, created_at, xmax = 0`,
			uuid.New(), email, invitedBy, InviteLifetime.Seconds()).
			Scan(&inv.ID, &inv.Email, &inv.ExpiresAt, &inv.CreatedAt, &created)
		if err != nil {
			return fmt.Errorf("insert invite: %w", err)
		}
		return nil
	})
	return inv, created, err
}

// ResendInvite resets the expiry of an open Invite. It is ErrNotFound when
// there is no open Invite with that id.
func (s *Store) ResendInvite(ctx context.Context, id uuid.UUID) (Invite, error) {
	var inv Invite
	err := s.pool.QueryRow(ctx, `
		UPDATE invites SET expires_at = now() + make_interval(secs => $2)
		WHERE id = $1 AND accepted_at IS NULL
		RETURNING id, email, expires_at, created_at`,
		id, InviteLifetime.Seconds()).Scan(&inv.ID, &inv.Email, &inv.ExpiresAt, &inv.CreatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return Invite{}, ErrNotFound
	}
	if err != nil {
		return Invite{}, fmt.Errorf("resend invite: %w", err)
	}
	return inv, nil
}

// RevokeInvite deletes an open Invite. It is ErrNotFound when there is no open
// Invite with that id.
func (s *Store) RevokeInvite(ctx context.Context, id uuid.UUID) error {
	tag, err := s.pool.Exec(ctx, `DELETE FROM invites WHERE id = $1 AND accepted_at IS NULL`, id)
	if err != nil {
		return fmt.Errorf("revoke invite: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}

// AdminList returns every User and every open Invite, expired ones included
// so they can be resent.
func (s *Store) AdminList(ctx context.Context) ([]ManagedUser, []Invite, error) {
	rows, err := s.pool.Query(ctx, `
		SELECT id, email, coalesce(name, ''), is_admin, disabled_at, created_at
		FROM users ORDER BY created_at, email`)
	if err != nil {
		return nil, nil, fmt.Errorf("list users: %w", err)
	}
	users, err := pgx.CollectRows(rows, func(row pgx.CollectableRow) (ManagedUser, error) {
		var m ManagedUser
		err := row.Scan(&m.ID, &m.Email, &m.Name, &m.IsAdmin, &m.DisabledAt, &m.CreatedAt)
		return m, err
	})
	if err != nil {
		return nil, nil, fmt.Errorf("read users: %w", err)
	}

	rows, err = s.pool.Query(ctx, `
		SELECT id, email, expires_at, created_at FROM invites
		WHERE accepted_at IS NULL ORDER BY created_at, email`)
	if err != nil {
		return nil, nil, fmt.Errorf("list invites: %w", err)
	}
	invites, err := pgx.CollectRows(rows, func(row pgx.CollectableRow) (Invite, error) {
		var inv Invite
		err := row.Scan(&inv.ID, &inv.Email, &inv.ExpiresAt, &inv.CreatedAt)
		return inv, err
	})
	if err != nil {
		return nil, nil, fmt.Errorf("read invites: %w", err)
	}
	return users, invites, nil
}

// DisableUser sets disabled_at and ends all of the User's Login Sessions, so
// open streams close at their next ping (auth-keys.md §5.7). It is ErrLastAdmin
// when no active Admin would remain, and ErrNotFound for an unknown id.
func (s *Store) DisableUser(ctx context.Context, id uuid.UUID) error {
	return pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		// Serializes disables so two Admins cannot disable each other at once.
		if _, err := tx.Exec(ctx, `SELECT id FROM users WHERE is_admin AND disabled_at IS NULL FOR UPDATE`); err != nil {
			return fmt.Errorf("lock admins: %w", err)
		}
		var isAdmin bool
		err := tx.QueryRow(ctx,
			`UPDATE users SET disabled_at = coalesce(disabled_at, now()) WHERE id = $1 RETURNING is_admin`, id).Scan(&isAdmin)
		if errors.Is(err, pgx.ErrNoRows) {
			return ErrNotFound
		}
		if err != nil {
			return fmt.Errorf("disable user: %w", err)
		}
		if _, err := tx.Exec(ctx, `DELETE FROM login_sessions WHERE user_id = $1`, id); err != nil {
			return fmt.Errorf("delete login sessions: %w", err)
		}
		var remaining bool
		if err := tx.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM users WHERE is_admin AND disabled_at IS NULL)`).Scan(&remaining); err != nil {
			return fmt.Errorf("count active admins: %w", err)
		}
		if isAdmin && !remaining {
			return ErrLastAdmin
		}
		return nil
	})
}

// EnableUser clears disabled_at. It is ErrNotFound for an unknown id.
func (s *Store) EnableUser(ctx context.Context, id uuid.UUID) error {
	return s.updateUser(ctx, `UPDATE users SET disabled_at = NULL WHERE id = $1`, id)
}

// MakeAdmin sets is_admin. It is ErrNotFound for an unknown id.
func (s *Store) MakeAdmin(ctx context.Context, id uuid.UUID) error {
	return s.updateUser(ctx, `UPDATE users SET is_admin = true WHERE id = $1`, id)
}

func (s *Store) updateUser(ctx context.Context, query string, id uuid.UUID) error {
	tag, err := s.pool.Exec(ctx, query, id)
	if err != nil {
		return fmt.Errorf("update user: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}
