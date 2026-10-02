package auth_test

import (
	"errors"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/bhanuprakaash/jelly-fish/internal/auth"
	"github.com/bhanuprakaash/jelly-fish/internal/testdb"
)

const bootEmail = "Admin@Example.test"

func countRows(t *testing.T, pool *pgxpool.Pool, query string, args ...any) int {
	t.Helper()
	var n int
	if err := pool.QueryRow(t.Context(), query, args...).Scan(&n); err != nil {
		t.Fatalf("%s: %v", query, err)
	}
	return n
}

// requestLogin fails the test unless a code and link are issued for email.
func requestLogin(t *testing.T, s *auth.Store, email string) (code, link string) {
	t.Helper()
	code, link, err := s.RequestCode(t.Context(), email)
	if err != nil {
		t.Fatalf("RequestCode: %v", err)
	}
	if code == "" || link == "" {
		t.Fatalf("RequestCode(%q) issued code %q, link %q", email, code, link)
	}
	return code, link
}

func requestCode(t *testing.T, s *auth.Store, email string) string {
	t.Helper()
	code, _ := requestLogin(t, s, email)
	return code
}

func TestBootstrapAdminIsCreateOnce(t *testing.T) {
	pool := testdb.NewPool(t)

	created, err := auth.BootstrapAdmin(t.Context(), pool, bootEmail)
	if err != nil || !created {
		t.Fatalf("first BootstrapAdmin = %v, %v; want true, nil", created, err)
	}
	if _, err := pool.Exec(t.Context(), `UPDATE users SET is_admin = false`); err != nil {
		t.Fatal(err)
	}
	created, err = auth.BootstrapAdmin(t.Context(), pool, "admin@example.test")
	if err != nil || created {
		t.Fatalf("second BootstrapAdmin = %v, %v; want false, nil", created, err)
	}

	if n := countRows(t, pool, `SELECT count(*) FROM users`); n != 1 {
		t.Errorf("users = %d, want 1", n)
	}
	if n := countRows(t, pool, `SELECT count(*) FROM workspaces`); n != 1 {
		t.Errorf("workspaces = %d, want 1", n)
	}
	if n := countRows(t, pool, `SELECT count(*) FROM projects WHERE name = 'Personal'`); n != 1 {
		t.Errorf("Personal projects = %d, want 1", n)
	}
	if n := countRows(t, pool, `SELECT count(*) FROM users WHERE is_admin`); n != 0 {
		t.Errorf("second bootstrap changed an existing user (admins = %d)", n)
	}
}

func TestRequestCode(t *testing.T) {
	pool := testdb.NewPool(t)
	s := auth.NewStore(pool)
	u := testdb.NewUser(t, pool)

	t.Run("unknown email gets no code", func(t *testing.T) {
		code, link, err := s.RequestCode(t.Context(), "nobody@example.test")
		if err != nil || code != "" || link != "" {
			t.Fatalf("RequestCode = %q, %q, %v; want nothing", code, link, err)
		}
	})

	t.Run("disabled user gets no code", func(t *testing.T) {
		d := testdb.NewUser(t, pool)
		if _, err := pool.Exec(t.Context(), `UPDATE users SET disabled_at = now() WHERE id = $1`, d.ID); err != nil {
			t.Fatal(err)
		}
		if code, _, err := s.RequestCode(t.Context(), d.Email); err != nil || code != "" {
			t.Fatalf("RequestCode = %q, %v; want no code", code, err)
		}
	})

	t.Run("fourth code in 15 minutes is withheld", func(t *testing.T) {
		for range 3 {
			requestCode(t, s, strings.ToUpper(u.Email))
		}
		if code, _, err := s.RequestCode(t.Context(), u.Email); err != nil || code != "" {
			t.Fatalf("4th RequestCode = %q, %v; want no code", code, err)
		}
	})
}

func TestVerifyCode(t *testing.T) {
	pool := testdb.NewPool(t)
	s := auth.NewStore(pool)

	t.Run("right code signs in once", func(t *testing.T) {
		u := testdb.NewUser(t, pool)
		code := requestCode(t, s, u.Email)
		token, err := s.VerifyCode(t.Context(), u.Email, code, "ua")
		if err != nil || token == "" {
			t.Fatalf("VerifyCode = %q, %v", token, err)
		}
		got, err := s.Authenticate(t.Context(), token)
		if err != nil || got.ID != u.ID || got.WorkspaceID != u.WorkspaceID {
			t.Fatalf("Authenticate = %+v, %v; want %v", got, err, u.ID)
		}
		if _, err := s.VerifyCode(t.Context(), u.Email, code, "ua"); !errors.Is(err, auth.ErrInvalidCode) {
			t.Errorf("reusing the code: err = %v, want ErrInvalidCode", err)
		}
	})

	t.Run("expired code fails", func(t *testing.T) {
		u := testdb.NewUser(t, pool)
		code := requestCode(t, s, u.Email)
		if _, err := pool.Exec(t.Context(), `UPDATE login_codes SET created_at = now() - interval '11 minutes', expires_at = now() - interval '1 minute' WHERE email = $1`, u.Email); err != nil {
			t.Fatal(err)
		}
		if _, err := s.VerifyCode(t.Context(), u.Email, code, "ua"); !errors.Is(err, auth.ErrInvalidCode) {
			t.Fatalf("err = %v, want ErrInvalidCode", err)
		}
	})

	t.Run("five wrong guesses lock the code", func(t *testing.T) {
		u := testdb.NewUser(t, pool)
		code := requestCode(t, s, u.Email)
		wrong := "000000"
		if code == wrong {
			wrong = "000001"
		}
		for range 5 {
			if _, err := s.VerifyCode(t.Context(), u.Email, wrong, "ua"); !errors.Is(err, auth.ErrInvalidCode) {
				t.Fatalf("wrong guess: err = %v, want ErrInvalidCode", err)
			}
		}
		if _, err := s.VerifyCode(t.Context(), u.Email, code, "ua"); !errors.Is(err, auth.ErrInvalidCode) {
			t.Fatalf("right code after lockout: err = %v, want ErrInvalidCode", err)
		}
	})

	t.Run("code of a user disabled since is refused", func(t *testing.T) {
		u := testdb.NewUser(t, pool)
		code := requestCode(t, s, u.Email)
		if _, err := pool.Exec(t.Context(), `UPDATE users SET disabled_at = now() WHERE id = $1`, u.ID); err != nil {
			t.Fatal(err)
		}
		if _, err := s.VerifyCode(t.Context(), u.Email, code, "ua"); !errors.Is(err, auth.ErrInvalidCode) {
			t.Fatalf("err = %v, want ErrInvalidCode", err)
		}
	})
}

func TestVerifyLink(t *testing.T) {
	pool := testdb.NewPool(t)
	s := auth.NewStore(pool)

	t.Run("link signs in once and consumes the code", func(t *testing.T) {
		u := testdb.NewUser(t, pool)
		code, link := requestLogin(t, s, u.Email)
		token, err := s.VerifyLink(t.Context(), link, "ua")
		if err != nil || token == "" {
			t.Fatalf("VerifyLink = %q, %v", token, err)
		}
		if got, err := s.Authenticate(t.Context(), token); err != nil || got.ID != u.ID {
			t.Fatalf("Authenticate = %+v, %v; want %v", got, err, u.ID)
		}
		if _, err := s.VerifyLink(t.Context(), link, "ua"); !errors.Is(err, auth.ErrInvalidCode) {
			t.Errorf("reusing the link: err = %v, want ErrInvalidCode", err)
		}
		if _, err := s.VerifyCode(t.Context(), u.Email, code, "ua"); !errors.Is(err, auth.ErrInvalidCode) {
			t.Errorf("code after link: err = %v, want ErrInvalidCode", err)
		}
	})

	t.Run("code consumes the link", func(t *testing.T) {
		u := testdb.NewUser(t, pool)
		code, link := requestLogin(t, s, u.Email)
		if _, err := s.VerifyCode(t.Context(), u.Email, code, "ua"); err != nil {
			t.Fatal(err)
		}
		if _, err := s.VerifyLink(t.Context(), link, "ua"); !errors.Is(err, auth.ErrInvalidCode) {
			t.Fatalf("link after code: err = %v, want ErrInvalidCode", err)
		}
	})

	t.Run("unknown and expired links fail", func(t *testing.T) {
		u := testdb.NewUser(t, pool)
		_, link := requestLogin(t, s, u.Email)
		if _, err := s.VerifyLink(t.Context(), "nope", "ua"); !errors.Is(err, auth.ErrInvalidCode) {
			t.Errorf("unknown link: err = %v, want ErrInvalidCode", err)
		}
		if _, err := pool.Exec(t.Context(), `UPDATE login_codes SET expires_at = now() - interval '1 minute' WHERE email = $1`, u.Email); err != nil {
			t.Fatal(err)
		}
		if _, err := s.VerifyLink(t.Context(), link, "ua"); !errors.Is(err, auth.ErrInvalidCode) {
			t.Errorf("expired link: err = %v, want ErrInvalidCode", err)
		}
	})

	t.Run("link of a user disabled since is refused", func(t *testing.T) {
		u := testdb.NewUser(t, pool)
		_, link := requestLogin(t, s, u.Email)
		if _, err := pool.Exec(t.Context(), `UPDATE users SET disabled_at = now() WHERE id = $1`, u.ID); err != nil {
			t.Fatal(err)
		}
		if _, err := s.VerifyLink(t.Context(), link, "ua"); !errors.Is(err, auth.ErrInvalidCode) {
			t.Fatalf("err = %v, want ErrInvalidCode", err)
		}
	})
}

func TestOnlyNewestCodeVerifies(t *testing.T) {
	pool := testdb.NewPool(t)
	s := auth.NewStore(pool)
	u := testdb.NewUser(t, pool)

	older := requestCode(t, s, u.Email)
	newer := requestCode(t, s, u.Email)
	if older == newer {
		t.Skip("random codes collided")
	}
	if _, err := s.VerifyCode(t.Context(), u.Email, older, "ua"); !errors.Is(err, auth.ErrInvalidCode) {
		t.Fatalf("older code: err = %v, want ErrInvalidCode", err)
	}
	if _, err := s.VerifyCode(t.Context(), u.Email, newer, "ua"); err != nil {
		t.Fatalf("newer code: %v", err)
	}
}

func TestNoPlaintextCredentialsStored(t *testing.T) {
	pool := testdb.NewPool(t)
	s := auth.NewStore(pool)
	u := testdb.NewUser(t, pool)

	code, link := requestLogin(t, s, u.Email)
	cookie, err := s.VerifyLink(t.Context(), link, "ua")
	if err != nil {
		t.Fatal(err)
	}

	for _, secret := range []string{code, link, cookie} {
		// Every column of both tables, as text, the way a dump would show it.
		n := countRows(t, pool, `
			SELECT (SELECT count(*) FROM login_codes c WHERE c::text LIKE '%' || $1 || '%')
			     + (SELECT count(*) FROM login_sessions ls WHERE ls::text LIKE '%' || $1 || '%')`, secret)
		if n != 0 {
			t.Errorf("secret %q appears in %d stored rows", secret, n)
		}
	}
}

func TestAuthenticate(t *testing.T) {
	pool := testdb.NewPool(t)
	s := auth.NewStore(pool)
	u := testdb.NewUser(t, pool)
	token, err := s.VerifyCode(t.Context(), u.Email, requestCode(t, s, u.Email), "ua")
	if err != nil {
		t.Fatal(err)
	}

	t.Run("stores only a hash of the token", func(t *testing.T) {
		if n := countRows(t, pool, `SELECT count(*) FROM login_sessions WHERE token_hash = sha256($1::bytea)`, token); n != 1 {
			t.Errorf("sessions with sha256(token) = %d, want 1", n)
		}
	})

	t.Run("unknown token", func(t *testing.T) {
		if _, err := s.Authenticate(t.Context(), "nope"); !errors.Is(err, auth.ErrUnauthenticated) {
			t.Fatalf("err = %v, want ErrUnauthenticated", err)
		}
	})

	t.Run("slides at most once an hour", func(t *testing.T) {
		seen := func() (string, string) {
			var last, exp string
			err := pool.QueryRow(t.Context(), `SELECT last_seen_at::text, expires_at::text FROM login_sessions`).Scan(&last, &exp)
			if err != nil {
				t.Fatal(err)
			}
			return last, exp
		}
		last0, _ := seen()
		if _, err := s.Authenticate(t.Context(), token); err != nil {
			t.Fatal(err)
		}
		if last1, _ := seen(); last1 != last0 {
			t.Fatalf("fresh session was rewritten: %s -> %s", last0, last1)
		}

		if _, err := pool.Exec(t.Context(), `UPDATE login_sessions SET last_seen_at = now() - interval '2 hours', expires_at = now() + interval '1 day'`); err != nil {
			t.Fatal(err)
		}
		if _, err := s.Authenticate(t.Context(), token); err != nil {
			t.Fatal(err)
		}
		if n := countRows(t, pool, `SELECT count(*) FROM login_sessions WHERE last_seen_at > now() - interval '1 minute' AND expires_at > now() + interval '29 days'`); n != 1 {
			t.Errorf("stale session was not extended")
		}
	})

	t.Run("expired session", func(t *testing.T) {
		if _, err := pool.Exec(t.Context(), `UPDATE login_sessions SET expires_at = now() - interval '1 second'`); err != nil {
			t.Fatal(err)
		}
		if _, err := s.Authenticate(t.Context(), token); !errors.Is(err, auth.ErrUnauthenticated) {
			t.Fatalf("err = %v, want ErrUnauthenticated", err)
		}
	})
}
