package api

import (
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/google/uuid"

	"github.com/bhanuprakaash/jelly-fish/internal/auth"
	"github.com/bhanuprakaash/jelly-fish/internal/testdb"
)

// signInEmail runs the real code flow for email and returns its cookie.
func (e *loginEnv) signInEmail(t *testing.T, email string) *http.Cookie {
	t.Helper()
	return e.signInUser(t, auth.User{Email: email})
}

// adminCookie returns an Admin and a signed-in cookie for them.
func (e *loginEnv) adminCookie(t *testing.T) (auth.User, *http.Cookie) {
	t.Helper()
	a := testdb.NewAdmin(t, e.pool)
	return a, e.signInUser(t, a)
}

func (e *loginEnv) invite(t *testing.T, c *http.Cookie, email string) *httptest.ResponseRecorder {
	t.Helper()
	return e.do(http.MethodPost, "/api/admin/invites", map[string]string{"email": email}, c)
}

func TestAdminRoutesAreNotFoundForNonAdmins(t *testing.T) {
	e := newLoginEnv(t)
	u := testdb.NewUser(t, e.pool)
	c := e.signInUser(t, u)
	id := uuid.NewString()

	routes := []struct{ method, path string }{
		{http.MethodPost, "/api/admin/invites"},
		{http.MethodPost, "/api/admin/invites/" + id + "/resend"},
		{http.MethodDelete, "/api/admin/invites/" + id},
		{http.MethodGet, "/api/admin/users"},
		{http.MethodPost, "/api/admin/users/" + id + "/disable"},
		{http.MethodPost, "/api/admin/users/" + id + "/enable"},
		{http.MethodPost, "/api/admin/users/" + id + "/make-admin"},
		{http.MethodGet, "/api/admin/anything-else"},
	}
	for _, r := range routes {
		t.Run(r.method+" "+r.path, func(t *testing.T) {
			if got := e.status(r.path, r.method, c); got != http.StatusNotFound {
				t.Errorf("non-admin status = %d, want 404", got)
			}
			if got := e.status(r.path, r.method, nil); got != http.StatusUnauthorized {
				t.Errorf("signed-out status = %d, want 401", got)
			}
		})
	}
}

func TestAdminInvitesAFriendWhoSignsIn(t *testing.T) {
	e := newLoginEnv(t)
	_, ac := e.adminCookie(t)
	friend := uuid.NewString() + "@example.test"

	rr := e.invite(t, ac, "  "+strings.ToUpper(friend)+" ")
	if rr.Code != http.StatusCreated {
		t.Fatalf("invite: status %d, body %s", rr.Code, rr.Body)
	}
	mails := e.waitMail(t)
	last := mails[len(mails)-1]
	if last.to != friend || !strings.Contains(last.text, testPublicURL) {
		t.Fatalf("invite email = %+v, want one to %s pointing at %s", last, friend, testPublicURL)
	}
	if codePattern.MatchString(last.text) {
		t.Errorf("invite email carries a code:\n%s", last.text)
	}

	fc := e.signInEmail(t, friend)
	rr = e.do(http.MethodGet, "/api/me", nil, fc)
	var me struct {
		Email   string `json:"email"`
		IsAdmin bool   `json:"is_admin"`
	}
	if err := json.NewDecoder(rr.Body).Decode(&me); err != nil || me.Email != friend || me.IsAdmin {
		t.Fatalf("/api/me = %+v, %v; want the friend, not an admin", me, err)
	}

	if rr := e.invite(t, ac, friend); rr.Code != http.StatusConflict {
		t.Errorf("inviting an existing User: status %d, want 409", rr.Code)
	}
}

func TestAdminInviteAgainResends(t *testing.T) {
	e := newLoginEnv(t)
	_, ac := e.adminCookie(t)
	base := len(e.waitMail(t))
	friend := uuid.NewString() + "@example.test"

	if rr := e.invite(t, ac, friend); rr.Code != http.StatusCreated {
		t.Fatalf("first invite: status %d", rr.Code)
	}
	if rr := e.invite(t, ac, friend); rr.Code != http.StatusOK {
		t.Fatalf("second invite: status %d, body %s", rr.Code, rr.Body)
	}
	if n := len(e.waitMail(t)) - base; n != 2 {
		t.Errorf("invite emails = %d, want one per invite", n)
	}
}

func TestAdminInviteMailFailureKeepsInvite(t *testing.T) {
	e := newLoginEnv(t)
	_, ac := e.adminCookie(t)
	e.mailer.err = errors.New("smtp down")
	friend := uuid.NewString() + "@example.test"

	if rr := e.invite(t, ac, friend); rr.Code != http.StatusBadGateway {
		t.Fatalf("status %d, want 502", rr.Code)
	}
	e.mailer.err = nil
	if rr := e.invite(t, ac, friend); rr.Code != http.StatusOK {
		t.Errorf("invite after the failed send: status %d, want 200 (invite kept)", rr.Code)
	}
}

func TestAdminInviteRejectsBadEmail(t *testing.T) {
	e := newLoginEnv(t)
	_, ac := e.adminCookie(t)
	if rr := e.invite(t, ac, "not an email"); rr.Code != http.StatusBadRequest {
		t.Errorf("status %d, want 400", rr.Code)
	}
}

func TestAdminResendAndRevokeInvite(t *testing.T) {
	e := newLoginEnv(t)
	_, ac := e.adminCookie(t)
	base := len(e.waitMail(t))
	friend := uuid.NewString() + "@example.test"
	rr := e.invite(t, ac, friend)
	var inv struct {
		ID string `json:"id"`
	}
	if err := json.NewDecoder(rr.Body).Decode(&inv); err != nil || inv.ID == "" {
		t.Fatalf("invite body: %v", err)
	}

	if got := e.status("/api/admin/invites/"+inv.ID+"/resend", http.MethodPost, ac); got != http.StatusNoContent {
		t.Fatalf("resend: status %d, want 204", got)
	}
	if n := len(e.waitMail(t)) - base; n != 2 {
		t.Errorf("emails after resend = %d, want 2", n)
	}

	if got := e.status("/api/admin/invites/"+inv.ID, http.MethodDelete, ac); got != http.StatusNoContent {
		t.Fatalf("revoke: status %d, want 204", got)
	}
	if got := e.status("/api/admin/invites/"+inv.ID, http.MethodDelete, ac); got != http.StatusNotFound {
		t.Errorf("revoke again: status %d, want 404", got)
	}
	if got := e.status("/api/admin/invites/"+inv.ID+"/resend", http.MethodPost, ac); got != http.StatusNotFound {
		t.Errorf("resend after revoke: status %d, want 404", got)
	}
	if got := e.status("/api/admin/invites/not-a-uuid", http.MethodDelete, ac); got != http.StatusNotFound {
		t.Errorf("revoke bad id: status %d, want 404", got)
	}

	// Revoked: asking for a code sends nothing more.
	e.do(http.MethodPost, "/api/auth/code", map[string]string{"email": friend}, nil)
	if n := len(e.waitMail(t)) - base; n != 2 {
		t.Errorf("emails after code request for a revoked invite = %d, want still 2", n)
	}
}

func TestAdminListsUsersAndInvites(t *testing.T) {
	e := newLoginEnv(t)
	a, ac := e.adminCookie(t)
	friend := uuid.NewString() + "@example.test"
	e.invite(t, ac, friend)

	rr := e.do(http.MethodGet, "/api/admin/users", nil, ac)
	if rr.Code != http.StatusOK {
		t.Fatalf("status %d, body %s", rr.Code, rr.Body)
	}
	var body struct {
		Users []struct {
			ID      string `json:"id"`
			Email   string `json:"email"`
			IsAdmin bool   `json:"is_admin"`
		} `json:"users"`
		Invites []struct {
			Email string `json:"email"`
		} `json:"invites"`
	}
	if err := json.NewDecoder(rr.Body).Decode(&body); err != nil {
		t.Fatal(err)
	}
	if len(body.Users) != 1 || body.Users[0].ID != a.ID.String() || !body.Users[0].IsAdmin {
		t.Errorf("users = %+v, want just the admin", body.Users)
	}
	if len(body.Invites) != 1 || body.Invites[0].Email != friend {
		t.Errorf("invites = %+v, want the friend's", body.Invites)
	}
}

func TestAdminDisablesAndEnablesUser(t *testing.T) {
	e := newLoginEnv(t)
	_, ac := e.adminCookie(t)
	u := testdb.NewUser(t, e.pool)
	uc := e.signInUser(t, u)
	base := "/api/admin/users/" + u.ID.String()

	if got := e.status(base+"/disable", http.MethodPost, ac); got != http.StatusNoContent {
		t.Fatalf("disable: status %d, want 204", got)
	}
	if got := e.status("/api/me", http.MethodGet, uc); got != http.StatusUnauthorized {
		t.Errorf("disabled User's old cookie: status %d, want 401", got)
	}
	e.do(http.MethodPost, "/api/auth/code", map[string]string{"email": u.Email}, nil)
	before := len(e.waitMail(t))
	e.do(http.MethodPost, "/api/auth/code", map[string]string{"email": u.Email}, nil)
	if n := len(e.waitMail(t)); n != before {
		t.Errorf("a disabled User was sent a code")
	}

	if got := e.status(base+"/enable", http.MethodPost, ac); got != http.StatusNoContent {
		t.Fatalf("enable: status %d, want 204", got)
	}
	uc = e.signInUser(t, u)
	if got := e.status("/api/me", http.MethodGet, uc); got != http.StatusOK {
		t.Errorf("after enable: /api/me status %d, want 200", got)
	}

	missing := "/api/admin/users/" + uuid.NewString()
	for _, action := range []string{"disable", "enable", "make-admin"} {
		if got := e.status(missing+"/"+action, http.MethodPost, ac); got != http.StatusNotFound {
			t.Errorf("%s unknown User: status %d, want 404", action, got)
		}
	}
}

func TestAdminCannotDisableTheLastAdmin(t *testing.T) {
	e := newLoginEnv(t)
	a, ac := e.adminCookie(t)
	self := "/api/admin/users/" + a.ID.String()

	if got := e.status(self+"/disable", http.MethodPost, ac); got != http.StatusConflict {
		t.Fatalf("last Admin disabling themselves: status %d, want 409", got)
	}
	if got := e.status("/api/me", http.MethodGet, ac); got != http.StatusOK {
		t.Errorf("the refused disable ended the Admin's login: /api/me status %d", got)
	}

	b := testdb.NewUser(t, e.pool)
	if got := e.status("/api/admin/users/"+b.ID.String()+"/make-admin", http.MethodPost, ac); got != http.StatusNoContent {
		t.Fatalf("make-admin: status %d, want 204", got)
	}
	bc := e.signInUser(t, b)
	if got := e.status("/api/admin/users", http.MethodGet, bc); got != http.StatusOK {
		t.Errorf("new Admin on /api/admin/users: status %d, want 200", got)
	}
	if got := e.status(self+"/disable", http.MethodPost, ac); got != http.StatusNoContent {
		t.Errorf("Admin disabling themselves with another Admin around: status %d, want 204", got)
	}
}
