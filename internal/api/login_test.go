package api

import (
	"bytes"
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"regexp"
	"testing"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/bhanuprakaash/jelly-fish/internal/auth"
	"github.com/bhanuprakaash/jelly-fish/internal/eventlog"
	"github.com/bhanuprakaash/jelly-fish/internal/stream"
	"github.com/bhanuprakaash/jelly-fish/internal/testdb"
)

// loginEnv is a real server over a real database, with a recording Mailer.
type loginEnv struct {
	pool   *pgxpool.Pool
	srv    *http.Server
	auth   *authHandlers
	mailer *fakeMailer
}

func newLoginEnv(t *testing.T) *loginEnv {
	t.Helper()
	pool := testdb.NewPool(t)
	mailer := &fakeMailer{}
	srv, authH := newServer(":0", slog.New(slog.DiscardHandler), testWebFS(), eventlog.NewRepo(pool), stream.NewHub(), &fakeDeltaBus{}, newTestMetrics(t),
		AuthConfig{Authenticator: auth.NewStore(pool), Mailer: mailer})
	return &loginEnv{pool: pool, srv: srv, auth: authH, mailer: mailer}
}

func (e *loginEnv) do(method, path string, body any, cookie *http.Cookie) *httptest.ResponseRecorder {
	var buf bytes.Buffer
	if body != nil {
		_ = json.NewEncoder(&buf).Encode(body)
	}
	req := httptest.NewRequest(method, path, &buf)
	if cookie != nil {
		req.AddCookie(cookie)
	}
	rr := httptest.NewRecorder()
	e.srv.Handler.ServeHTTP(rr, req)
	return rr
}

// waitMail waits for in-flight background sends, then returns what was sent.
func (e *loginEnv) waitMail(t *testing.T) []sentMail {
	t.Helper()
	e.auth.bg.Wait()
	return e.mailer.sentMails()
}

var codePattern = regexp.MustCompile(`\b\d{6}\b`)

// signInUser runs the real code flow for u and returns its cookie.
func (e *loginEnv) signInUser(t *testing.T, u auth.User) *http.Cookie {
	t.Helper()
	if rr := e.do(http.MethodPost, "/api/auth/code", map[string]string{"email": u.Email}, nil); rr.Code != http.StatusAccepted {
		t.Fatalf("request code: status %d", rr.Code)
	}
	mails := e.waitMail(t)
	if len(mails) == 0 {
		t.Fatal("no code email sent")
	}
	code := codePattern.FindString(mails[len(mails)-1].text)
	rr := e.do(http.MethodPost, "/api/auth/code/verify", map[string]string{"email": u.Email, "code": code}, nil)
	if rr.Code != http.StatusNoContent {
		t.Fatalf("verify: status %d, body %s", rr.Code, rr.Body)
	}
	for _, c := range rr.Result().Cookies() {
		if c.Name == loginCookie {
			return c
		}
	}
	t.Fatal("verify set no login cookie")
	return nil
}

func TestRequestCodeAnswersAlikeForUnknownEmail(t *testing.T) {
	e := newLoginEnv(t)
	u := testdb.NewUser(t, e.pool)

	known := e.do(http.MethodPost, "/api/auth/code", map[string]string{"email": u.Email}, nil)
	unknown := e.do(http.MethodPost, "/api/auth/code", map[string]string{"email": "nobody@example.test"}, nil)

	if known.Code != http.StatusAccepted || unknown.Code != http.StatusAccepted {
		t.Fatalf("statuses = %d, %d; want 202, 202", known.Code, unknown.Code)
	}
	if known.Body.String() != unknown.Body.String() {
		t.Errorf("bodies differ: %q vs %q", known.Body, unknown.Body)
	}
	mails := e.waitMail(t)
	if len(mails) != 1 || mails[0].to != u.Email {
		t.Fatalf("sent = %+v, want exactly one email to %s", mails, u.Email)
	}
}

func TestRequestCodeWithMalformedEmailIsStill202(t *testing.T) {
	e := newLoginEnv(t)
	rr := e.do(http.MethodPost, "/api/auth/code", map[string]string{"email": "not an email"}, nil)
	if rr.Code != http.StatusAccepted {
		t.Fatalf("status = %d, want 202", rr.Code)
	}
	if mails := e.waitMail(t); len(mails) != 0 {
		t.Errorf("sent = %+v, want none", mails)
	}
}

func TestVerifyCodeSetsCookie(t *testing.T) {
	e := newLoginEnv(t)
	u := testdb.NewUser(t, e.pool)
	c := e.signInUser(t, u)

	if !c.HttpOnly || !c.Secure || c.SameSite != http.SameSiteLaxMode || c.Path != "/" || c.MaxAge != 30*24*3600 {
		t.Errorf("cookie = %+v, want HttpOnly Secure SameSite=Lax Path=/ Max-Age=30d", c)
	}
	rr := e.do(http.MethodGet, "/api/me", nil, c)
	if rr.Code != http.StatusOK {
		t.Fatalf("/api/me status = %d", rr.Code)
	}
	var me struct {
		ID    uuid.UUID `json:"id"`
		Email string    `json:"email"`
	}
	if err := json.NewDecoder(rr.Body).Decode(&me); err != nil || me.ID != u.ID || me.Email != u.Email {
		t.Errorf("/api/me = %+v, %v; want %v %s", me, err, u.ID, u.Email)
	}
}

func TestVerifyWrongCodeIs401(t *testing.T) {
	e := newLoginEnv(t)
	u := testdb.NewUser(t, e.pool)
	e.do(http.MethodPost, "/api/auth/code", map[string]string{"email": u.Email}, nil)

	rr := e.do(http.MethodPost, "/api/auth/code/verify", map[string]string{"email": u.Email, "code": "abcdef"}, nil)
	if rr.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401", rr.Code)
	}
	if len(rr.Result().Cookies()) != 0 {
		t.Error("a failed verify set a cookie")
	}
}

func TestInsecureCookieForLocalHTTP(t *testing.T) {
	a := &authHandlers{cfg: AuthConfig{InsecureCookie: true}}
	if got := a.cookieName(); got != "jf_login" {
		t.Errorf("cookie name = %q, want jf_login", got)
	}
}

func TestAPIRequiresLogin(t *testing.T) {
	e := newLoginEnv(t)
	sid := uuid.NewString()
	for _, tt := range []struct{ method, path string }{
		{http.MethodGet, "/api/hello"},
		{http.MethodGet, "/api/me"},
		{http.MethodPost, "/api/sessions"},
		{http.MethodPost, "/api/sessions/" + sid + "/messages"},
		{http.MethodGet, "/api/sessions/" + sid + "/events"},
		{http.MethodGet, "/api/anything-else"},
	} {
		t.Run(tt.method+" "+tt.path, func(t *testing.T) {
			if rr := e.do(tt.method, tt.path, nil, nil); rr.Code != http.StatusUnauthorized {
				t.Errorf("status = %d, want 401", rr.Code)
			}
			bad := &http.Cookie{Name: loginCookie, Value: "forged"}
			if rr := e.do(tt.method, tt.path, nil, bad); rr.Code != http.StatusUnauthorized {
				t.Errorf("forged cookie: status = %d, want 401", rr.Code)
			}
		})
	}

	t.Run("static assets and login screen stay public", func(t *testing.T) {
		for _, path := range []string{"/", "/app.js", "/login"} {
			if rr := e.do(http.MethodGet, path, nil, nil); rr.Code != http.StatusOK {
				t.Errorf("GET %s status = %d, want 200", path, rr.Code)
			}
		}
	})
}

func TestUsersCannotReachEachOthersSessions(t *testing.T) {
	e := newLoginEnv(t)
	alice, bob := testdb.NewUser(t, e.pool), testdb.NewUser(t, e.pool)
	aliceCookie, bobCookie := e.signInUser(t, alice), e.signInUser(t, bob)

	sid := uuid.New()
	rr := e.do(http.MethodPost, "/api/sessions", map[string]any{
		"session_id": sid, "client_msg_id": uuid.New(), "message": "hi",
	}, aliceCookie)
	if rr.Code != http.StatusOK {
		t.Fatalf("alice create: status %d, body %s", rr.Code, rr.Body)
	}

	var projectUser, projectName string
	err := e.pool.QueryRow(t.Context(), `
		SELECT p.user_id::text, p.name FROM sessions s JOIN projects p ON p.id = s.project_id WHERE s.id = $1`, sid).Scan(&projectUser, &projectName)
	if err != nil || projectUser != alice.ID.String() || projectName != "Personal" {
		t.Errorf("session project = user %s %q, %v; want alice's Personal", projectUser, projectName, err)
	}

	post := e.do(http.MethodPost, "/api/sessions/"+sid.String()+"/messages", map[string]any{
		"client_msg_id": uuid.New(), "message": "mine now",
	}, bobCookie)
	if post.Code != http.StatusNotFound {
		t.Errorf("bob post: status = %d, want 404", post.Code)
	}
	if stream := e.do(http.MethodGet, "/api/sessions/"+sid.String()+"/events", nil, bobCookie); stream.Code != http.StatusNotFound {
		t.Errorf("bob stream: status = %d, want 404", stream.Code)
	}
	if own := e.do(http.MethodPost, "/api/sessions/"+sid.String()+"/messages", map[string]any{
		"client_msg_id": uuid.New(), "message": "again",
	}, aliceCookie); own.Code != http.StatusOK {
		t.Errorf("alice post: status = %d, want 200", own.Code)
	}
}
