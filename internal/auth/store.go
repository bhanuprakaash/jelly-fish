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
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

const (
	// maxCodeAttempts is the number of wrong guesses that kills a login code.
	maxCodeAttempts = 5
	// maxCodesPerWindow caps login codes sent to one email per 15 minutes.
	maxCodesPerWindow = 3
	// SessionLifetime is how long a Login Session lasts without being seen.
	SessionLifetime = 30 * 24 * time.Hour
	// slideAfter is how stale last_seen_at must be before the next request
	// extends the Login Session.
	slideAfter = time.Hour
)

// ErrUnauthenticated is returned for a missing, expired or unknown Login
// Session, or one whose User is disabled.
var ErrUnauthenticated = errors.New("unauthenticated")

// ErrInvalidCode is returned for a wrong, expired, used or locked-out login
// code, so callers cannot tell the cases apart.
var ErrInvalidCode = errors.New("invalid code")

// Store keeps login codes and Login Sessions in Postgres.
type Store struct {
	pool *pgxpool.Pool
}

// NewStore builds a Store backed by pool.
func NewStore(pool *pgxpool.Pool) *Store {
	return &Store{pool: pool}
}

// RequestCode records a login code for email and returns it with its link
// token for sending. Both are "" when nothing should be sent: the email is not
// an enabled User, or three codes went out in the last 15 minutes
// (auth-keys.md §5.1).
func (s *Store) RequestCode(ctx context.Context, email string) (code, link string, err error) {
	email = NormalizeEmail(email)
	var allowed bool
	err = s.pool.QueryRow(ctx,
		`SELECT EXISTS (SELECT 1 FROM users WHERE email = $1 AND disabled_at IS NULL)`, email).Scan(&allowed)
	if err != nil {
		return "", "", fmt.Errorf("look up user: %w", err)
	}
	if !allowed {
		return "", "", nil
	}

	code, err = newCode()
	if err != nil {
		return "", "", err
	}
	link, err = randomToken()
	if err != nil {
		return "", "", err
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
		return "", "", fmt.Errorf("insert login code: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return "", "", nil
	}
	return code, link, nil
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

// VerifyLink starts a Login Session for the unused, unexpired code behind link
// and returns its cookie token. Using the link consumes the code too. Every
// failure is ErrInvalidCode.
func (s *Store) VerifyLink(ctx context.Context, link, userAgent string) (string, error) {
	var token string
	wrong := false
	err := pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		var id uuid.UUID
		var email string
		err := tx.QueryRow(ctx, `
			SELECT id, email FROM login_codes
			WHERE link_hash = $1 AND used_at IS NULL AND expires_at > now() FOR UPDATE`,
			hash(link)).Scan(&id, &email)
		if errors.Is(err, pgx.ErrNoRows) {
			wrong = true
			return nil
		}
		if err != nil {
			return fmt.Errorf("select login link: %w", err)
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
		VALUES ($1, $2, $3, $4, now() + make_interval(secs => $5))`,
		uuid.New(), userID, hash(token), userAgent, SessionLifetime.Seconds()); err != nil {
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
		SELECT ls.id, ls.last_seen_at < now() - make_interval(secs => $2),
		       u.id, u.workspace_id, u.email, u.name, u.is_admin
		FROM login_sessions ls JOIN users u ON u.id = ls.user_id
		WHERE ls.token_hash = $1 AND ls.expires_at > now() AND u.disabled_at IS NULL`,
		hash(token), slideAfter.Seconds()).Scan(&loginID, &stale, &u.ID, &u.WorkspaceID, &u.Email, &name, &u.IsAdmin)
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
			UPDATE login_sessions SET last_seen_at = now(), expires_at = now() + make_interval(secs => $2)
			WHERE id = $1 AND last_seen_at < now() - make_interval(secs => $3)`,
			loginID, SessionLifetime.Seconds(), slideAfter.Seconds())
	}
	return u, nil
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
