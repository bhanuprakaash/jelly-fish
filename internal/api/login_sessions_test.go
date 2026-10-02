package api

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/bhanuprakaash/jelly-fish/internal/stream"
	"github.com/bhanuprakaash/jelly-fish/internal/testdb"
)

type deviceJSON struct {
	ID        uuid.UUID `json:"id"`
	UserAgent string    `json:"user_agent"`
	Current   bool      `json:"current"`
}

func (e *loginEnv) devices(t *testing.T, c *http.Cookie) []deviceJSON {
	t.Helper()
	rr := e.do(http.MethodGet, "/api/me/login-sessions", nil, c)
	if rr.Code != http.StatusOK {
		t.Fatalf("list devices: status %d, body %s", rr.Code, rr.Body)
	}
	var list []deviceJSON
	if err := json.NewDecoder(rr.Body).Decode(&list); err != nil {
		t.Fatal(err)
	}
	return list
}

func (e *loginEnv) status(path, method string, c *http.Cookie) int {
	return e.do(method, path, nil, c).Code
}

func TestLogoutEndsThisDeviceOnly(t *testing.T) {
	e := newLoginEnv(t)
	u := testdb.NewUser(t, e.pool)
	phone, laptop := e.signInUser(t, u), e.signInUser(t, u)

	rr := e.do(http.MethodPost, "/api/auth/logout", nil, phone)
	if rr.Code != http.StatusNoContent {
		t.Fatalf("logout: status %d", rr.Code)
	}
	var cleared bool
	for _, c := range rr.Result().Cookies() {
		cleared = cleared || (c.Name == loginCookie && c.MaxAge < 0)
	}
	if !cleared {
		t.Error("logout did not clear the cookie")
	}
	if got := e.status("/api/me", http.MethodGet, phone); got != http.StatusUnauthorized {
		t.Errorf("logged-out device: status %d, want 401", got)
	}
	if got := e.status("/api/me", http.MethodGet, laptop); got != http.StatusOK {
		t.Errorf("other device: status %d, want 200", got)
	}
}

func TestLogoutAllEndsEveryDevice(t *testing.T) {
	e := newLoginEnv(t)
	alice, bob := testdb.NewUser(t, e.pool), testdb.NewUser(t, e.pool)
	phone, laptop, bobCookie := e.signInUser(t, alice), e.signInUser(t, alice), e.signInUser(t, bob)

	if got := e.status("/api/auth/logout-all", http.MethodPost, phone); got != http.StatusNoContent {
		t.Fatalf("logout-all: status %d", got)
	}
	for name, c := range map[string]*http.Cookie{"phone": phone, "laptop": laptop} {
		if got := e.status("/api/me", http.MethodGet, c); got != http.StatusUnauthorized {
			t.Errorf("%s after logout-all: status %d, want 401", name, got)
		}
	}
	if got := e.status("/api/me", http.MethodGet, bobCookie); got != http.StatusOK {
		t.Errorf("another User: status %d, want 200", got)
	}
}

func TestLogoutNeedsLogin(t *testing.T) {
	e := newLoginEnv(t)
	for _, path := range []string{"/api/auth/logout", "/api/auth/logout-all"} {
		if got := e.status(path, http.MethodPost, nil); got != http.StatusUnauthorized {
			t.Errorf("POST %s without cookie: status %d, want 401", path, got)
		}
	}
}

func TestDeviceList(t *testing.T) {
	e := newLoginEnv(t)
	u := testdb.NewUser(t, e.pool)
	phone, _ := e.signInUser(t, u), e.signInUser(t, u)
	other := e.signInUser(t, testdb.NewUser(t, e.pool))

	list := e.devices(t, phone)
	if len(list) != 2 {
		t.Fatalf("devices = %+v, want 2", list)
	}
	current := 0
	for _, d := range list {
		if d.Current {
			current++
		}
	}
	if current != 1 {
		t.Errorf("%d devices marked current, want 1", current)
	}
	if got := e.devices(t, other); len(got) != 1 {
		t.Errorf("another User sees %d devices, want 1", len(got))
	}
}

func TestDeleteDevice(t *testing.T) {
	e := newLoginEnv(t)
	alice, bob := testdb.NewUser(t, e.pool), testdb.NewUser(t, e.pool)
	phone, laptop := e.signInUser(t, alice), e.signInUser(t, alice)
	bobCookie := e.signInUser(t, bob)

	var phoneID uuid.UUID
	for _, d := range e.devices(t, laptop) {
		if !d.Current {
			phoneID = d.ID
		}
	}
	path := "/api/me/login-sessions/"

	if got := e.status(path+phoneID.String(), http.MethodDelete, bobCookie); got != http.StatusNotFound {
		t.Errorf("deleting another User's device: status %d, want 404", got)
	}
	if got := e.status(path+"not-a-uuid", http.MethodDelete, laptop); got != http.StatusNotFound {
		t.Errorf("bad id: status %d, want 404", got)
	}
	if got := e.status("/api/me", http.MethodGet, phone); got != http.StatusOK {
		t.Fatalf("phone after foreign delete: status %d, want 200", got)
	}

	if got := e.status(path+phoneID.String(), http.MethodDelete, laptop); got != http.StatusNoContent {
		t.Fatalf("delete own device: status %d, want 204", got)
	}
	if got := e.status("/api/me", http.MethodGet, phone); got != http.StatusUnauthorized {
		t.Errorf("deleted device: status %d, want 401", got)
	}
	if got := e.status("/api/me", http.MethodGet, laptop); got != http.StatusOK {
		t.Errorf("other device: status %d, want 200", got)
	}
}

func TestStreamClosesWhenLoginEnds(t *testing.T) {
	run := func(t *testing.T, logins loginChecker) (w *streamWriter, done <-chan struct{}) {
		t.Helper()
		s := newStreams(t, &fakeRepo{}, stream.NewHub(), &fakeDeltaBus{})
		s.logins = logins
		s.pingInterval = 10 * time.Millisecond
		ctx := t.Context()
		req := httptest.NewRequest(http.MethodGet, "/", nil).WithContext(ctx)
		req.SetPathValue("id", uuid.NewString())
		w = newStreamWriter()
		ch := make(chan struct{})
		go func() {
			s.handle(w, req)
			close(ch)
		}()
		return w, ch
	}

	t.Run("closes on the next ping once the login is gone", func(t *testing.T) {
		_, done := run(t, fakeLogins{active: false})
		select {
		case <-done:
		case <-time.After(2 * time.Second):
			t.Fatal("stream stayed open after its Login Session ended")
		}
	})

	t.Run("stays open while the login is active", func(t *testing.T) {
		w, done := run(t, fakeLogins{active: true})
		w.waitFor(t, ": ping\n\n")
		select {
		case <-done:
			t.Fatal("stream closed with an active Login Session")
		case <-time.After(50 * time.Millisecond):
		}
	})
}
