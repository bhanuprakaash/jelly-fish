package api

import (
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"slices"
	"sync"
	"testing"
	"testing/fstest"

	"github.com/google/uuid"
	"github.com/prometheus/client_golang/prometheus"

	"github.com/bhanuprakaash/jelly-fish/internal/auth"
	"github.com/bhanuprakaash/jelly-fish/internal/stream"
)

func testWebFS() fstest.MapFS {
	return fstest.MapFS{
		"index.html": &fstest.MapFile{Data: []byte("shell")},
		"app.js":     &fstest.MapFile{Data: []byte("console.log('hi')")},
	}
}

func newTestMetrics(t *testing.T) *stream.Metrics {
	t.Helper()
	m, err := stream.NewMetrics(prometheus.NewRegistry())
	if err != nil {
		t.Fatal(err)
	}
	return m
}

func newTestServer(t *testing.T, repo SessionRepo) *http.Server {
	t.Helper()
	return NewServer(":0", slog.New(slog.DiscardHandler), testWebFS(), repo, stream.NewHub(), &fakeDeltaBus{}, newTestMetrics(t),
		AuthConfig{Authenticator: fakeAuthenticator{}, Mailer: &fakeMailer{}})
}

const testToken = "test-token"

// fakeAuthenticator signs in the one holder of testToken.
type fakeAuthenticator struct{}

func (fakeAuthenticator) RequestCode(context.Context, string) (string, string, error) {
	return "", "", nil
}

func (fakeAuthenticator) VerifyLink(context.Context, string, string) (string, error) {
	return "", auth.ErrInvalidCode
}

func (fakeAuthenticator) VerifyCode(context.Context, string, string, string) (string, error) {
	return "", auth.ErrInvalidCode
}

func (fakeAuthenticator) Authenticate(_ context.Context, token string) (auth.User, error) {
	if token != testToken {
		return auth.User{}, auth.ErrUnauthenticated
	}
	return auth.User{ID: uuid.New(), WorkspaceID: uuid.New(), Email: "me@example.test"}, nil
}

// signIn adds the Login Session cookie fakeAuthenticator accepts.
func signIn(req *http.Request) *http.Request {
	req.AddCookie(&http.Cookie{Name: loginCookie, Value: testToken})
	return req
}

// fakeMailer records the emails sent to it.
type fakeMailer struct {
	mu   sync.Mutex
	sent []sentMail
}

type sentMail struct{ to, subject, text string }

func (m *fakeMailer) Send(_ context.Context, to, subject, text string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.sent = append(m.sent, sentMail{to, subject, text})
	return nil
}

func (m *fakeMailer) sentMails() []sentMail {
	m.mu.Lock()
	defer m.mu.Unlock()
	return slices.Clone(m.sent)
}

// fakeDeltaBus hands its subscribers whatever is sent on ch. onSubscribe, if
// set, runs as each subscription starts.
type fakeDeltaBus struct {
	ch          chan stream.Delta
	onSubscribe func()
}

func (f *fakeDeltaBus) Subscribe(uuid.UUID) (<-chan stream.Delta, func()) {
	if f.onSubscribe != nil {
		f.onSubscribe()
	}
	return f.ch, func() {}
}

func TestHello(t *testing.T) {
	srv := newTestServer(t, &fakeRepo{})

	rr := httptest.NewRecorder()
	req := signIn(httptest.NewRequest(http.MethodGet, "/api/hello", nil))
	srv.Handler.ServeHTTP(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d", rr.Code, http.StatusOK)
	}

	var body struct {
		Message string `json:"message"`
	}
	if err := json.NewDecoder(rr.Body).Decode(&body); err != nil {
		t.Fatalf("decode body: %v", err)
	}
	if body.Message != "hello" {
		t.Fatalf("message = %q, want %q", body.Message, "hello")
	}
}

func TestSPA(t *testing.T) {
	tests := []struct {
		name string
		path string
		want string
	}{
		{"root serves index", "/", "shell"},
		{"unknown route falls back to index", "/sessions/123", "shell"},
		{"known asset served directly", "/app.js", "console.log('hi')"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			srv := newTestServer(t, &fakeRepo{})

			rr := httptest.NewRecorder()
			req := httptest.NewRequest(http.MethodGet, tt.path, nil)
			srv.Handler.ServeHTTP(rr, req)

			if rr.Code != http.StatusOK {
				t.Fatalf("status = %d, want %d", rr.Code, http.StatusOK)
			}
			if rr.Body.String() != tt.want {
				t.Fatalf("body = %q, want %q", rr.Body.String(), tt.want)
			}
		})
	}
}
