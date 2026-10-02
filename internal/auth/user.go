package auth

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/bhanuprakaash/jelly-fish/internal/eventlog"
)

// User is the signed-in person behind a request.
type User struct {
	ID          uuid.UUID
	WorkspaceID uuid.UUID
	Email       string
	Name        string
	IsAdmin     bool
}

// Scope is the TenantScope every Repo call for this User runs under.
func (u User) Scope() eventlog.TenantScope {
	return eventlog.TenantScope{WorkspaceID: u.WorkspaceID, UserID: u.ID}
}

// NormalizeEmail lowercases and trims an email, the form users.email holds.
func NormalizeEmail(email string) string {
	return strings.ToLower(strings.TrimSpace(email))
}

// CreateUser inserts a workspace, a User and their "Personal" Project in tx.
func CreateUser(ctx context.Context, tx pgx.Tx, email string, isAdmin bool) (User, error) {
	u := User{ID: uuid.New(), WorkspaceID: uuid.New(), Email: NormalizeEmail(email), IsAdmin: isAdmin}
	if _, err := tx.Exec(ctx, `INSERT INTO workspaces (id) VALUES ($1)`, u.WorkspaceID); err != nil {
		return User{}, fmt.Errorf("insert workspace: %w", err)
	}
	if _, err := tx.Exec(ctx,
		`INSERT INTO users (id, workspace_id, email, is_admin) VALUES ($1, $2, $3, $4)`,
		u.ID, u.WorkspaceID, u.Email, u.IsAdmin); err != nil {
		return User{}, fmt.Errorf("insert user: %w", err)
	}
	if _, err := tx.Exec(ctx,
		`INSERT INTO projects (id, workspace_id, user_id, name) VALUES ($1, $2, $3, $4)`,
		uuid.New(), u.WorkspaceID, u.ID, eventlog.PersonalProject); err != nil {
		return User{}, fmt.Errorf("insert project: %w", err)
	}
	return u, nil
}

// BootstrapAdmin creates the Admin for email unless a User with that email
// exists, and reports whether it did. Existing Users are never changed
// (auth-keys.md §5.6).
func BootstrapAdmin(ctx context.Context, pool *pgxpool.Pool, email string) (bool, error) {
	created := false
	err := pgx.BeginFunc(ctx, pool, func(tx pgx.Tx) error {
		var exists bool
		if err := tx.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM users WHERE email = $1)`, NormalizeEmail(email)).Scan(&exists); err != nil {
			return fmt.Errorf("look up user: %w", err)
		}
		if exists {
			return nil
		}
		if _, err := CreateUser(ctx, tx, email, true); err != nil {
			return err
		}
		created = true
		return nil
	})
	if isUniqueViolation(err) {
		// Another process bootstrapped the same email first.
		return false, nil
	}
	return created, err
}

func isUniqueViolation(err error) bool {
	var pgErr *pgconn.PgError
	return errors.As(err, &pgErr) && pgErr.Code == "23505"
}
