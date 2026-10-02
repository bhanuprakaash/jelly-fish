// Package auth implements passwordless login: emailed codes, Login Sessions
// and the Admin bootstrap (docs/design/auth-keys.md).
package auth

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"errors"
	"fmt"
	"math/big"
	"strings"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/bhanuprakaash/jelly-fish/internal/eventlog"
)

const (
	// maxCodeAttempts is the number of wrong guesses that kills a login code.
	maxCodeAttempts = 5
	// maxCodesPerWindow caps login codes sent to one email per 15 minutes.
	maxCodesPerWindow = 3
	personalProject   = "Personal"
)

// ErrUnauthenticated is returned for a missing, expired or unknown Login
// Session, or one whose User is disabled.
var ErrUnauthenticated = errors.New("unauthenticated")

// ErrInvalidCode is returned for a wrong, expired, used or locked-out login
// code, so callers cannot tell the cases apart.
var ErrInvalidCode = errors.New("invalid code")

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

// Store keeps login codes and Login Sessions in Postgres.
type Store struct {
	pool *pgxpool.Pool
}

// NewStore builds a Store backed by pool.
func NewStore(pool *pgxpool.Pool) *Store {
	return &Store{pool: pool}
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
		uuid.New(), u.WorkspaceID, u.ID, personalProject); err != nil {
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

// RequestCode records a login code for email and returns it for sending. It
// returns "" when nothing should be sent: the email is not an enabled User, or
// three codes went out in the last 15 minutes (auth-keys.md §5.1).
func (s *Store) RequestCode(ctx context.Context, email string) (string, error) {
	email = NormalizeEmail(email)
	var allowed bool
	err := s.pool.QueryRow(ctx,
		`SELECT EXISTS (SELECT 1 FROM users WHERE email = $1 AND disabled_at IS NULL)`, email).Scan(&allowed)
	if err != nil {
		return "", fmt.Errorf("look up user: %w", err)
	}
	if !allowed {
		return "", nil
	}

	code, err := newCode()
	if err != nil {
		return "", err
	}
	link, err := randomToken()
	if err != nil {
		return "", err
	}
	// The count and insert are one statement, so concurrent requests cannot
	// both slip under the limit and a row only exists for a code that is sent.
	tag, err := s.pool.Exec(ctx, `
		INSERT INTO login_codes (id, email, code_hash, link_hash, expires_at)
		SELECT $1, $2, $3, $4, now() + interval '10 minutes'
		WHERE (SELECT count(*) FROM login_codes
		       WHERE email = $2 AND created_at > now() - interval '15 minutes') < $5`,
		uuid.New(), email, hash(code), hash(link), maxCodesPerWindow)
	if err != nil {
		return "", fmt.Errorf("insert login code: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return "", nil
	}
	return code, nil
}

// VerifyCode checks the newest unused, unexpired code for email and, if it
// matches, starts a Login Session and returns its cookie token. Every failure
// is ErrInvalidCode.
func (s *Store) VerifyCode(ctx context.Context, email, code, userAgent string) (string, error) {
	email = NormalizeEmail(email)
	var token string
	wrong := false
	err := pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		var id uuid.UUID
		var codeHash []byte
		var attempts int
		err := tx.QueryRow(ctx, `
			SELECT id, code_hash, attempts FROM login_codes
			WHERE email = $1 AND used_at IS NULL AND expires_at > now()
			ORDER BY created_at DESC LIMIT 1 FOR UPDATE`, email).Scan(&id, &codeHash, &attempts)
		if errors.Is(err, pgx.ErrNoRows) {
			wrong = true
			return nil
		}
		if err != nil {
			return fmt.Errorf("select login code: %w", err)
		}
		if attempts >= maxCodeAttempts {
			wrong = true
			return nil
		}
		if subtle.ConstantTimeCompare(codeHash, hash(strings.TrimSpace(code))) != 1 {
			wrong = true
			// Committed with the tx, so guesses count even though the
			// caller gets an error.
			if _, err := tx.Exec(ctx, `UPDATE login_codes SET attempts = attempts + 1 WHERE id = $1`, id); err != nil {
				return fmt.Errorf("count attempt: %w", err)
			}
			return nil
		}
		if _, err := tx.Exec(ctx, `UPDATE login_codes SET used_at = now() WHERE id = $1`, id); err != nil {
			return fmt.Errorf("use login code: %w", err)
		}
		token, wrong, err = login(ctx, tx, email, userAgent)
		return err
	})
	if err != nil {
		return "", err
	}
	if wrong {
		return "", ErrInvalidCode
	}
	return token, nil
}

// login starts a Login Session for the enabled User with email. wrong is true
// when there is none (auth-keys.md §5.3).
func login(ctx context.Context, tx pgx.Tx, email, userAgent string) (token string, wrong bool, err error) {
	var userID uuid.UUID
	err = tx.QueryRow(ctx, `SELECT id FROM users WHERE email = $1 AND disabled_at IS NULL`, email).Scan(&userID)
	if errors.Is(err, pgx.ErrNoRows) {
		return "", true, nil
	}
	if err != nil {
		return "", false, fmt.Errorf("look up user: %w", err)
	}
	token, err = randomToken()
	if err != nil {
		return "", false, err
	}
	if _, err := tx.Exec(ctx, `
		INSERT INTO login_sessions (id, user_id, token_hash, user_agent, expires_at)
		VALUES ($1, $2, $3, $4, now() + interval '30 days')`,
		uuid.New(), userID, hash(token), userAgent); err != nil {
		return "", false, fmt.Errorf("insert login session: %w", err)
	}
	return token, false, nil
}

// Authenticate resolves a cookie token to its User. It extends the Login
// Session when it was last seen over an hour ago, so a session costs at most
// one write per hour (auth-keys.md §5.4).
func (s *Store) Authenticate(ctx context.Context, token string) (User, error) {
	var u User
	var loginID uuid.UUID
	var stale bool
	var name *string
	err := s.pool.QueryRow(ctx, `
		SELECT ls.id, ls.last_seen_at < now() - interval '1 hour',
		       u.id, u.workspace_id, u.email, u.name, u.is_admin
		FROM login_sessions ls JOIN users u ON u.id = ls.user_id
		WHERE ls.token_hash = $1 AND ls.expires_at > now() AND u.disabled_at IS NULL`,
		hash(token)).Scan(&loginID, &stale, &u.ID, &u.WorkspaceID, &u.Email, &name, &u.IsAdmin)
	if errors.Is(err, pgx.ErrNoRows) {
		return User{}, ErrUnauthenticated
	}
	if err != nil {
		return User{}, fmt.Errorf("look up login session: %w", err)
	}
	if name != nil {
		u.Name = *name
	}
	if stale {
		// Best effort: a failed extension must not reject a valid session.
		_, _ = s.pool.Exec(ctx, `
			UPDATE login_sessions SET last_seen_at = now(), expires_at = now() + interval '30 days'
			WHERE id = $1 AND last_seen_at < now() - interval '1 hour'`, loginID)
	}
	return u, nil
}

type userKey struct{}

// WithUser returns ctx carrying u, as set by the Authn middleware.
func WithUser(ctx context.Context, u User) context.Context {
	return context.WithValue(ctx, userKey{}, u)
}

// UserFrom returns the User WithUser stored in ctx.
func UserFrom(ctx context.Context) (User, bool) {
	u, ok := ctx.Value(userKey{}).(User)
	return u, ok
}

func hash(s string) []byte {
	h := sha256.Sum256([]byte(s))
	return h[:]
}

// newCode returns a uniformly random 6-digit code.
func newCode() (string, error) {
	n, err := rand.Int(rand.Reader, big.NewInt(1_000_000))
	if err != nil {
		return "", fmt.Errorf("generate code: %w", err)
	}
	return fmt.Sprintf("%06d", n.Int64()), nil
}

// randomToken returns 32 random bytes, base64url-encoded.
func randomToken() (string, error) {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		return "", fmt.Errorf("generate token: %w", err)
	}
	return base64.RawURLEncoding.EncodeToString(b), nil
}

func isUniqueViolation(err error) bool {
	var pgErr *pgconn.PgError
	return errors.As(err, &pgErr) && pgErr.Code == "23505"
}
