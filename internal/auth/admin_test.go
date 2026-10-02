package auth_test

import (
	"errors"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/bhanuprakaash/jelly-fish/internal/auth"
	"github.com/bhanuprakaash/jelly-fish/internal/testdb"
)

func TestFirstLoginFromInviteCreatesUser(t *testing.T) {
	pool := testdb.NewPool(t)
	s := newStore(pool)
	admin := testdb.NewAdmin(t, pool)
	email := uuid.NewString() + "@example.test"

	if _, _, err := s.CreateInvite(t.Context(), admin.ID, "  "+email+" "); err != nil {
		t.Fatalf("CreateInvite: %v", err)
	}
	before := countRows(t, pool, `SELECT count(*) FROM users`)

	code := requestCode(t, s, email)
	token, err := s.VerifyCode(t.Context(), email, code, "ua")
	if err != nil || token == "" {
		t.Fatalf("VerifyCode = %q, %v", token, err)
	}
	got, err := s.Authenticate(t.Context(), token)
	if err != nil || got.Email != email || got.IsAdmin {
		t.Fatalf("Authenticate = %+v, %v; want a non-admin User %s", got, err, email)
	}

	if n := countRows(t, pool, `SELECT count(*) FROM users`); n != before+1 {
		t.Errorf("users = %d, want %d", n, before+1)
	}
	if n := countRows(t, pool, `SELECT count(*) FROM workspaces w JOIN users u ON u.workspace_id = w.id WHERE u.email = $1`, email); n != 1 {
		t.Errorf("workspaces for the new User = %d, want 1", n)
	}
	if n := countRows(t, pool, `SELECT count(*) FROM projects WHERE user_id = $1 AND name = 'Personal'`, got.ID); n != 1 {
		t.Errorf("Personal projects = %d, want 1", n)
	}
	if n := countRows(t, pool, `SELECT count(*) FROM invites WHERE email = $1 AND accepted_at IS NOT NULL`, email); n != 1 {
		t.Errorf("accepted invites = %d, want 1", n)
	}

	// A second sign-in is an ordinary login: nothing new is created.
	code = requestCode(t, s, email)
	if _, err := s.VerifyCode(t.Context(), email, code, "ua"); err != nil {
		t.Fatalf("second VerifyCode: %v", err)
	}
	if n := countRows(t, pool, `SELECT count(*) FROM users WHERE email = $1`, email); n != 1 {
		t.Errorf("users with that email = %d, want 1", n)
	}
	if n := countRows(t, pool, `SELECT count(*) FROM projects WHERE user_id = $1`, got.ID); n != 1 {
		t.Errorf("projects = %d, want 1", n)
	}
}

func TestInviteThatCannotLogIn(t *testing.T) {
	pool := testdb.NewPool(t)
	s := newStore(pool)
	admin := testdb.NewAdmin(t, pool)

	cases := []struct {
		name   string
		strand func(t *testing.T, email string, inv auth.Invite)
	}{
		{"expired", func(t *testing.T, email string, _ auth.Invite) {
			if _, err := pool.Exec(t.Context(), `UPDATE invites SET expires_at = now() - interval '1 minute' WHERE email = $1`, email); err != nil {
				t.Fatal(err)
			}
		}},
		{"revoked", func(t *testing.T, _ string, inv auth.Invite) {
			if err := s.RevokeInvite(t.Context(), inv.ID); err != nil {
				t.Fatal(err)
			}
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name+" invite sends no code", func(t *testing.T) {
			email := uuid.NewString() + "@example.test"
			inv, _, err := s.CreateInvite(t.Context(), admin.ID, email)
			if err != nil {
				t.Fatal(err)
			}
			tc.strand(t, email, inv)
			code, link, err := s.RequestCode(t.Context(), email)
			if err != nil || code != "" || link != "" {
				t.Fatalf("RequestCode = %q, %q, %v; want nothing", code, link, err)
			}
		})

		t.Run(tc.name+" invite after a code was sent lets nobody in", func(t *testing.T) {
			email := uuid.NewString() + "@example.test"
			inv, _, err := s.CreateInvite(t.Context(), admin.ID, email)
			if err != nil {
				t.Fatal(err)
			}
			code := requestCode(t, s, email)
			tc.strand(t, email, inv)
			if _, err := s.VerifyCode(t.Context(), email, code, "ua"); !errors.Is(err, auth.ErrInvalidCode) {
				t.Fatalf("VerifyCode err = %v, want ErrInvalidCode", err)
			}
			if n := countRows(t, pool, `SELECT count(*) FROM users WHERE email = $1`, email); n != 0 {
				t.Errorf("users created = %d, want 0", n)
			}
		})
	}
}

func TestCreateInvite(t *testing.T) {
	pool := testdb.NewPool(t)
	s := newStore(pool)
	admin := testdb.NewAdmin(t, pool)

	t.Run("an existing User is refused", func(t *testing.T) {
		u := testdb.NewUser(t, pool)
		if _, _, err := s.CreateInvite(t.Context(), admin.ID, u.Email); !errors.Is(err, auth.ErrAlreadyUser) {
			t.Fatalf("err = %v, want ErrAlreadyUser", err)
		}
		if n := countRows(t, pool, `SELECT count(*) FROM invites WHERE email = $1`, u.Email); n != 0 {
			t.Errorf("invites = %d, want 0", n)
		}
	})

	t.Run("expires in 7 days", func(t *testing.T) {
		inv, created, err := s.CreateInvite(t.Context(), admin.ID, uuid.NewString()+"@example.test")
		if err != nil || !created {
			t.Fatalf("CreateInvite = %v, %v", created, err)
		}
		if d := time.Until(inv.ExpiresAt); d < 7*24*time.Hour-time.Minute || d > 7*24*time.Hour {
			t.Errorf("expires in %v, want 7 days", d)
		}
	})

	t.Run("inviting again re-opens the same Invite", func(t *testing.T) {
		email := uuid.NewString() + "@example.test"
		first, _, err := s.CreateInvite(t.Context(), admin.ID, email)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := pool.Exec(t.Context(), `UPDATE invites SET expires_at = now() - interval '1 day' WHERE id = $1`, first.ID); err != nil {
			t.Fatal(err)
		}
		again, created, err := s.CreateInvite(t.Context(), admin.ID, email)
		if err != nil || created || again.ID != first.ID || !again.ExpiresAt.After(time.Now()) {
			t.Fatalf("CreateInvite = %+v, created %v, %v; want the same Invite, re-opened", again, created, err)
		}
	})
}

func TestResendAndRevokeInvite(t *testing.T) {
	pool := testdb.NewPool(t)
	s := newStore(pool)
	admin := testdb.NewAdmin(t, pool)
	email := uuid.NewString() + "@example.test"
	inv, _, err := s.CreateInvite(t.Context(), admin.ID, email)
	if err != nil {
		t.Fatal(err)
	}

	if _, err := pool.Exec(t.Context(), `UPDATE invites SET expires_at = now() - interval '1 day' WHERE id = $1`, inv.ID); err != nil {
		t.Fatal(err)
	}
	resent, err := s.ResendInvite(t.Context(), inv.ID)
	if err != nil || resent.Email != email || time.Until(resent.ExpiresAt) < 6*24*time.Hour {
		t.Fatalf("ResendInvite = %+v, %v; want a fresh 7-day expiry", resent, err)
	}
	if code, _, err := s.RequestCode(t.Context(), email); err != nil || code == "" {
		t.Fatalf("RequestCode after resend = %q, %v; want a code", code, err)
	}

	if err := s.RevokeInvite(t.Context(), inv.ID); err != nil {
		t.Fatalf("RevokeInvite: %v", err)
	}
	if err := s.RevokeInvite(t.Context(), inv.ID); !errors.Is(err, auth.ErrNotFound) {
		t.Errorf("second RevokeInvite err = %v, want ErrNotFound", err)
	}
	if _, err := s.ResendInvite(t.Context(), inv.ID); !errors.Is(err, auth.ErrNotFound) {
		t.Errorf("ResendInvite after revoke err = %v, want ErrNotFound", err)
	}
}

func TestDisableAndEnableUser(t *testing.T) {
	pool := testdb.NewPool(t)
	s := newStore(pool)
	u := testdb.NewUser(t, pool)
	token := signIn(t, s, u, "ua")
	au, err := s.Authenticate(t.Context(), token)
	if err != nil {
		t.Fatal(err)
	}

	if err := s.DisableUser(t.Context(), u.ID); err != nil {
		t.Fatalf("DisableUser: %v", err)
	}
	if n := countRows(t, pool, `SELECT count(*) FROM login_sessions WHERE user_id = $1`, u.ID); n != 0 {
		t.Errorf("login sessions left = %d, want 0", n)
	}
	if _, err := s.Authenticate(t.Context(), token); !errors.Is(err, auth.ErrUnauthenticated) {
		t.Errorf("old cookie err = %v, want ErrUnauthenticated", err)
	}
	if ok, err := s.LoginActive(t.Context(), au.LoginSessionID); err != nil || ok {
		t.Errorf("LoginActive = %v, %v; want false", ok, err)
	}
	if code, _, err := s.RequestCode(t.Context(), u.Email); err != nil || code != "" {
		t.Errorf("RequestCode = %q, %v; want no code for a disabled User", code, err)
	}

	if err := s.EnableUser(t.Context(), u.ID); err != nil {
		t.Fatalf("EnableUser: %v", err)
	}
	code := requestCode(t, s, u.Email)
	if _, err := s.VerifyCode(t.Context(), u.Email, code, "ua"); err != nil {
		t.Errorf("sign-in after enable: %v", err)
	}

	missing := uuid.New()
	for name, fn := range map[string]func() error{
		"DisableUser": func() error { return s.DisableUser(t.Context(), missing) },
		"EnableUser":  func() error { return s.EnableUser(t.Context(), missing) },
		"MakeAdmin":   func() error { return s.MakeAdmin(t.Context(), missing) },
	} {
		if err := fn(); !errors.Is(err, auth.ErrNotFound) {
			t.Errorf("%s of an unknown User err = %v, want ErrNotFound", name, err)
		}
	}
}

func TestDisableKeepsAnActiveAdmin(t *testing.T) {
	pool := testdb.NewPool(t)
	s := newStore(pool)
	a := testdb.NewAdmin(t, pool)

	if err := s.DisableUser(t.Context(), a.ID); !errors.Is(err, auth.ErrLastAdmin) {
		t.Fatalf("last Admin disabling themselves err = %v, want ErrLastAdmin", err)
	}
	if n := countRows(t, pool, `SELECT count(*) FROM users WHERE id = $1 AND disabled_at IS NULL`, a.ID); n != 1 {
		t.Error("the refused disable still took effect")
	}

	b := testdb.NewAdmin(t, pool)
	if err := s.DisableUser(t.Context(), a.ID); err != nil {
		t.Fatalf("Admin disabling themselves with another Admin around: %v", err)
	}
	// b is now the only active Admin, so a stale request from the disabled a
	// cannot take b out too.
	if err := s.DisableUser(t.Context(), b.ID); !errors.Is(err, auth.ErrLastAdmin) {
		t.Fatalf("disabling the only active Admin err = %v, want ErrLastAdmin", err)
	}

	// With no active Admin left, re-disabling the already disabled a changes
	// nothing, so it is a no-op rather than ErrLastAdmin.
	if _, err := pool.Exec(t.Context(), `UPDATE users SET disabled_at = now() WHERE id = $1`, b.ID); err != nil {
		t.Fatal(err)
	}
	if err := s.DisableUser(t.Context(), a.ID); err != nil {
		t.Fatalf("re-disabling an already disabled Admin: %v", err)
	}
}

func TestMakeAdminAndAdminList(t *testing.T) {
	pool := testdb.NewPool(t)
	s := newStore(pool)
	admin := testdb.NewAdmin(t, pool)
	u := testdb.NewUser(t, pool)
	open, _, err := s.CreateInvite(t.Context(), admin.ID, uuid.NewString()+"@example.test")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(t.Context(), `UPDATE invites SET expires_at = now() - interval '1 day' WHERE id = $1`, open.ID); err != nil {
		t.Fatal(err)
	}
	done, _, err := s.CreateInvite(t.Context(), admin.ID, uuid.NewString()+"@example.test")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(t.Context(), `UPDATE invites SET accepted_at = now() WHERE id = $1`, done.ID); err != nil {
		t.Fatal(err)
	}
	if err := s.DisableUser(t.Context(), u.ID); err != nil {
		t.Fatal(err)
	}

	if err := s.MakeAdmin(t.Context(), u.ID); err != nil {
		t.Fatalf("MakeAdmin: %v", err)
	}

	users, invites, err := s.AdminList(t.Context())
	if err != nil {
		t.Fatalf("AdminList: %v", err)
	}
	byID := map[uuid.UUID]auth.ManagedUser{}
	for _, m := range users {
		byID[m.ID] = m
	}
	if got := byID[u.ID]; !got.IsAdmin || got.DisabledAt == nil || got.Email != u.Email {
		t.Errorf("listed User = %+v, want an admin with disabled_at set", got)
	}
	if got := byID[admin.ID]; !got.IsAdmin || got.DisabledAt != nil {
		t.Errorf("listed Admin = %+v, want an active admin", got)
	}
	if len(invites) != 1 || invites[0].ID != open.ID {
		t.Errorf("invites = %+v, want only the open (expired) one", invites)
	}
}
