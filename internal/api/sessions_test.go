package api

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"maps"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/bhanuprakaash/jelly-fish/internal/eventlog"
	"github.com/bhanuprakaash/jelly-fish/internal/stream"
)

type fakeRepo struct {
	createSeq, postSeq, lastSeq int64
	createErr, postErr          error
	lastSeqErr, listErr         error
	listSessionsErr             error
	interruptErr, retryErr      error
	interrupted, retried        []uuid.UUID
	events                      []eventlog.Event
}

func (f *fakeRepo) Interrupt(_ context.Context, _ eventlog.TenantScope, sid uuid.UUID) error {
	f.interrupted = append(f.interrupted, sid)
	return f.interruptErr
}

func (f *fakeRepo) Retry(_ context.Context, _ eventlog.TenantScope, sid uuid.UUID) error {
	f.retried = append(f.retried, sid)
	return f.retryErr
}

func (f *fakeRepo) CreateSession(context.Context, eventlog.TenantScope, uuid.UUID, uuid.UUID, string, string) (int64, error) {
	return f.createSeq, f.createErr
}

func (f *fakeRepo) ChangeModel(context.Context, eventlog.TenantScope, uuid.UUID, string) error {
	return nil
}

func (f *fakeRepo) Rename(context.Context, eventlog.TenantScope, uuid.UUID, string) error {
	return nil
}

func (f *fakeRepo) SessionModel(context.Context, eventlog.TenantScope, uuid.UUID) (string, error) {
	return "", nil
}

func (f *fakeRepo) PostMessage(context.Context, eventlog.TenantScope, uuid.UUID, uuid.UUID, string) (int64, error) {
	return f.postSeq, f.postErr
}

func (f *fakeRepo) SessionLastSeq(context.Context, eventlog.TenantScope, uuid.UUID) (int64, error) {
	return f.lastSeq, f.lastSeqErr
}

func (f *fakeRepo) ListSessions(context.Context, eventlog.TenantScope) ([]eventlog.SessionSummary, error) {
	return nil, f.listSessionsErr
}

func (f *fakeRepo) ActivitySnapshot(context.Context, eventlog.TenantScope) ([]eventlog.ActivityRow, error) {
	return nil, nil
}

func (f *fakeRepo) ActivityStatusOf(context.Context, eventlog.TenantScope, uuid.UUID) (eventlog.ActivityRow, bool, error) {
	return eventlog.ActivityRow{}, false, nil
}

func (f *fakeRepo) ListEvents(_ context.Context, _ eventlog.TenantScope, _ uuid.UUID, after int64) ([]eventlog.Event, error) {
	var evs []eventlog.Event
	for _, e := range f.events {
		if e.Seq > after {
			evs = append(evs, e)
		}
	}
	return evs, f.listErr
}

func TestCreateSessionHandler(t *testing.T) {
	tests := []struct {
		name       string
		body       string
		repo       *fakeRepo
		wantStatus int
	}{
		{
			name:       "valid request",
			body:       `{"session_id":"` + uuid.New().String() + `","client_msg_id":"` + uuid.New().String() + `","message":"hi"}`,
			repo:       &fakeRepo{createSeq: 2},
			wantStatus: http.StatusOK,
		},
		{
			name:       "missing message",
			body:       `{"session_id":"` + uuid.New().String() + `","client_msg_id":"` + uuid.New().String() + `","message":""}`,
			repo:       &fakeRepo{},
			wantStatus: http.StatusBadRequest,
		},
		{
			name:       "invalid json",
			body:       `not json`,
			repo:       &fakeRepo{},
			wantStatus: http.StatusBadRequest,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			srv := newTestServer(t, tt.repo)
			req := signIn(httptest.NewRequest(http.MethodPost, "/api/sessions", bytes.NewBufferString(tt.body)))
			rr := httptest.NewRecorder()
			srv.Handler.ServeHTTP(rr, req)

			if rr.Code != tt.wantStatus {
				t.Fatalf("status = %d, want %d (body %s)", rr.Code, tt.wantStatus, rr.Body.String())
			}
			if tt.wantStatus == http.StatusOK {
				var got struct {
					LastSeq int64 `json:"last_seq"`
				}
				if err := json.NewDecoder(rr.Body).Decode(&got); err != nil {
					t.Fatalf("decode body: %v", err)
				}
				if got.LastSeq != tt.repo.createSeq {
					t.Errorf("last_seq = %d, want %d", got.LastSeq, tt.repo.createSeq)
				}
			}
		})
	}
}

func TestPostMessageHandler(t *testing.T) {
	tests := []struct {
		name       string
		body       string
		repo       *fakeRepo
		wantStatus int
	}{
		{
			name:       "valid request",
			body:       `{"client_msg_id":"` + uuid.New().String() + `","message":"hi"}`,
			repo:       &fakeRepo{postSeq: 3},
			wantStatus: http.StatusOK,
		},
		{
			name:       "session not found",
			body:       `{"client_msg_id":"` + uuid.New().String() + `","message":"hi"}`,
			repo:       &fakeRepo{postErr: eventlog.ErrNotFound},
			wantStatus: http.StatusNotFound,
		},
		{
			name:       "missing client_msg_id",
			body:       `{"message":"hi"}`,
			repo:       &fakeRepo{},
			wantStatus: http.StatusBadRequest,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			srv := newTestServer(t, tt.repo)
			path := "/api/sessions/" + uuid.New().String() + "/messages"
			req := signIn(httptest.NewRequest(http.MethodPost, path, bytes.NewBufferString(tt.body)))
			rr := httptest.NewRecorder()
			srv.Handler.ServeHTTP(rr, req)

			if rr.Code != tt.wantStatus {
				t.Fatalf("status = %d, want %d (body %s)", rr.Code, tt.wantStatus, rr.Body.String())
			}
		})
	}
}

func TestInterruptAndRetryHandlers(t *testing.T) {
	tests := []struct {
		name       string
		action     string
		repo       *fakeRepo
		wantStatus int
	}{
		{"interrupt", "interrupt", &fakeRepo{}, http.StatusNoContent},
		{"interrupt of another tenant's session", "interrupt", &fakeRepo{interruptErr: eventlog.ErrNotFound}, http.StatusNotFound},
		{"interrupt failing", "interrupt", &fakeRepo{interruptErr: errors.New("db down")}, http.StatusInternalServerError},
		{"retry", "retry", &fakeRepo{}, http.StatusNoContent},
		{"retry of another tenant's session", "retry", &fakeRepo{retryErr: eventlog.ErrNotFound}, http.StatusNotFound},
		{"retry failing", "retry", &fakeRepo{retryErr: errors.New("db down")}, http.StatusInternalServerError},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			sid := uuid.New()
			srv := newTestServer(t, tt.repo)
			req := signIn(httptest.NewRequest(http.MethodPost, "/api/sessions/"+sid.String()+"/"+tt.action, nil))
			rr := httptest.NewRecorder()
			srv.Handler.ServeHTTP(rr, req)

			if rr.Code != tt.wantStatus {
				t.Fatalf("status = %d, want %d (body %s)", rr.Code, tt.wantStatus, rr.Body.String())
			}
			called := tt.repo.interrupted
			if tt.action == "retry" {
				called = tt.repo.retried
			}
			if len(called) != 1 || called[0] != sid {
				t.Fatalf("repo called with %v, want the path's session id", called)
			}
		})
	}
}

func TestSessionEventsHandler(t *testing.T) {
	t.Run("not found", func(t *testing.T) {
		srv := newTestServer(t, &fakeRepo{lastSeqErr: eventlog.ErrNotFound})
		req := signIn(httptest.NewRequest(http.MethodGet, "/api/sessions/"+uuid.New().String()+"/events", nil))
		rr := httptest.NewRecorder()
		srv.Handler.ServeHTTP(rr, req)

		if rr.Code != http.StatusNotFound {
			t.Fatalf("status = %d, want %d", rr.Code, http.StatusNotFound)
		}
	})

	t.Run("replays events then closes on disconnect", func(t *testing.T) {
		repo := &fakeRepo{
			lastSeq: 3,
			events: []eventlog.Event{
				{Seq: 1, Type: eventlog.TypeSessionCreated, CreatedAt: time.Now(), Payload: []byte(`{"a":1}`)},
				{Seq: 2, Type: eventlog.TypeUserMessage, CreatedAt: time.Now(), Payload: []byte(`{"b":2}`)},
				// No serializer for this type, so it must never reach the wire.
				{Seq: 3, Type: "internal.no_serializer", CreatedAt: time.Now(), Payload: []byte(`{"secret":3}`)},
				{Seq: 4, Type: eventlog.TypeTurnInterrupted, CreatedAt: time.Now(), Payload: []byte(`{"turn_id":"t1"}`)},
			},
		}
		srv := newTestServer(t, repo)

		ctx, cancel := context.WithCancel(context.Background())
		req := signIn(httptest.NewRequest(http.MethodGet, "/api/sessions/"+uuid.New().String()+"/events", nil)).WithContext(ctx)
		rr := httptest.NewRecorder()

		done := make(chan struct{})
		go func() {
			srv.Handler.ServeHTTP(rr, req)
			close(done)
		}()

		time.Sleep(50 * time.Millisecond)
		cancel()

		select {
		case <-done:
		case <-time.After(2 * time.Second):
			t.Fatal("handler did not exit after client disconnect")
		}

		if ct := rr.Header().Get("Content-Type"); ct != "text/event-stream" {
			t.Errorf("Content-Type = %q, want text/event-stream", ct)
		}
		body := rr.Body.String()
		if !strings.Contains(body, "id: 1\n") {
			t.Errorf("body missing seq 1 frame: %q", body)
		}
		if !strings.Contains(body, "id: 2\n") {
			t.Errorf("body missing seq 2 frame: %q", body)
		}
		if !strings.Contains(body, "id: 4\n") {
			t.Errorf("body missing turn.interrupted frame: %q", body)
		}
		if strings.Contains(body, "id: 3\n") || strings.Contains(body, "secret") {
			t.Errorf("event with no serializer was sent: %q", body)
		}
	})
}

// streamWriter is an http.ResponseWriter for SSE tests: safe to read while
// the handler writes, and it fails any write not preceded by SetWriteDeadline.
type streamWriter struct {
	mu        sync.Mutex
	header    http.Header
	status    int
	buf       strings.Builder
	writeErr  error
	armed     bool
	unarmed   int
	deadlines []time.Duration
	flushed   chan struct{}
}

func newStreamWriter() *streamWriter {
	return &streamWriter{header: http.Header{}, flushed: make(chan struct{}, 1)}
}

func (w *streamWriter) Header() http.Header { return w.header }

func (w *streamWriter) WriteHeader(status int) {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.status = status
}

func (w *streamWriter) Write(p []byte) (int, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.writeErr != nil {
		return 0, w.writeErr
	}
	if !w.armed {
		w.unarmed++
	}
	w.armed = false
	return w.buf.Write(p)
}

func (w *streamWriter) Flush() {
	select {
	case w.flushed <- struct{}{}:
	default:
	}
}

func (w *streamWriter) SetWriteDeadline(t time.Time) error {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.armed = true
	w.deadlines = append(w.deadlines, time.Until(t))
	return nil
}

func (w *streamWriter) body() string {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.buf.String()
}

// waitFor blocks until the body contains want.
func (w *streamWriter) waitFor(t *testing.T, want string) {
	t.Helper()
	timeout := time.After(5 * time.Second)
	for !strings.Contains(w.body(), want) {
		select {
		case <-w.flushed:
		case <-timeout:
			t.Fatalf("timed out waiting for %q in %q", want, w.body())
		}
	}
}

func newStreams(t *testing.T, repo SessionRepo, hub *stream.Hub, deltas DeltaSubscriber) *sessionStreams {
	t.Helper()
	return &sessionStreams{
		repo: repo, hub: hub, deltas: deltas, metrics: newTestMetrics(t), logger: slog.New(slog.DiscardHandler),
		limiter:      newStreamLimiter(maxStreamsPerUser),
		writeTimeout: writeTimeout,
		pingInterval: pingInterval,
		logins:       fakeLogins{active: true},
	}
}

// serve runs h against a streamWriter until the returned stop is called.
func serve(t *testing.T, h http.HandlerFunc, sid uuid.UUID) (w *streamWriter, stop func()) {
	t.Helper()
	return serveRequest(t, h, sid, "", nil)
}

// serveRequest is serve with a query string and extra request headers.
func serveRequest(t *testing.T, h http.HandlerFunc, sid uuid.UUID, query string, header http.Header) (w *streamWriter, stop func()) {
	t.Helper()
	ctx, cancel := context.WithCancel(t.Context())
	req := httptest.NewRequest(http.MethodGet, "/api/sessions/"+sid.String()+"/events"+query, nil).WithContext(ctx)
	req.SetPathValue("id", sid.String())
	maps.Copy(req.Header, header)
	w = newStreamWriter()
	done := make(chan struct{})
	go func() {
		h(w, req)
		close(done)
	}()
	return w, func() {
		cancel()
		select {
		case <-done:
		case <-time.After(2 * time.Second):
			t.Fatal("handler did not exit after client disconnect")
		}
	}
}

func TestSessionEventsStreamsDeltasWithoutID(t *testing.T) {
	bus := &fakeDeltaBus{ch: make(chan stream.Delta, 1)}
	h := newStreams(t, &fakeRepo{}, stream.NewHub(), bus).handle
	w, stop := serve(t, h, uuid.New())
	defer stop()

	bus.ch <- stream.Delta{SessionID: uuid.New(), TurnID: "t1", Kind: stream.KindText, Text: "Hello wor"}
	want := "event: delta\ndata: {\"turn_id\":\"t1\",\"idx\":0,\"kind\":\"text\",\"text\":\"Hello wor\"}\n\n"
	w.waitFor(t, want)
	if strings.Contains(w.body(), "id:") {
		t.Errorf("delta frame carries an id: %q", w.body())
	}
}

func TestSessionEventsArmsWriteDeadlineBeforeEveryWrite(t *testing.T) {
	bus := &fakeDeltaBus{ch: make(chan stream.Delta, 2)}
	repo := &fakeRepo{lastSeq: 1, events: []eventlog.Event{{Seq: 1, Type: eventlog.TypeUserMessage, Payload: []byte(`{}`)}}}
	h := newStreams(t, repo, stream.NewHub(), bus).handle
	w, stop := serve(t, h, uuid.New())

	bus.ch <- stream.Delta{TurnID: "t1", Kind: stream.KindText, Text: "a"}
	bus.ch <- stream.Delta{TurnID: "t1", Kind: stream.KindText, Text: "b"}
	w.waitFor(t, `"text":"b"`)
	stop()

	w.mu.Lock()
	defer w.mu.Unlock()
	// retry frame, replayed event, two deltas.
	if len(w.deadlines) != 4 || w.unarmed != 0 {
		t.Fatalf("deadlines set = %d, writes without one = %d; want 4 and 0", len(w.deadlines), w.unarmed)
	}
	for _, d := range w.deadlines {
		if d <= 0 || d > writeTimeout {
			t.Errorf("deadline in %v, want within (0, %v]", d, writeTimeout)
		}
	}
}

// stagedRepo serves no events to the replay, then one llm.response.
type stagedRepo struct {
	fakeRepo
	calls atomic.Int32
}

func (r *stagedRepo) ListEvents(context.Context, eventlog.TenantScope, uuid.UUID, int64) ([]eventlog.Event, error) {
	if r.calls.Add(1) == 1 {
		return nil, nil
	}
	return []eventlog.Event{{Seq: 1, Type: eventlog.TypeLLMResponse, Payload: []byte(`{"turn_id":"t1"}`)}}, nil
}

func TestSessionEventsSendsQueuedDeltasBeforeDurableFrames(t *testing.T) {
	// select picks among ready cases at random, so repeat to make an
	// unordered handler fail.
	for range 20 {
		hub := stream.NewHub()
		sid := uuid.New()
		bus := &fakeDeltaBus{ch: make(chan stream.Delta, 1)}
		// The hub is subscribed by now: the hint and the delta are both
		// pending when the handler first selects.
		bus.onSubscribe = func() {
			bus.ch <- stream.Delta{TurnID: "t1", Kind: stream.KindText, Text: "last words"}
			hub.Notify(sid)
		}
		h := newStreams(t, &stagedRepo{}, hub, bus).handle
		w, stop := serve(t, h, sid)

		w.waitFor(t, "id: 1\n")
		stop()

		body := w.body()
		if strings.Index(body, "event: delta") > strings.Index(body, "id: 1\n") || !strings.Contains(body, "event: delta") {
			t.Fatalf("delta did not precede the llm.response frame: %q", body)
		}
	}
}

func TestParseAfter(t *testing.T) {
	tests := []struct {
		name        string
		lastEventID string
		after       string
		lastSeq     int64
		want        int64
	}{
		{"no header or query replays from 0", "", "", 10, 0},
		{"header wins over query", "5", "1", 10, 5},
		{"query used when no header", "", "7", 10, 7},
		{"non-numeric header replays from 0", "abc", "", 10, 0},
		{"header past last_seq replays from 0", "20", "", 10, 0},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodGet, "/api/sessions/x/events?after="+tt.after, nil)
			if tt.lastEventID != "" {
				req.Header.Set("Last-Event-ID", tt.lastEventID)
			}
			got := parseAfter(req, tt.lastSeq)
			if got != tt.want {
				t.Errorf("parseAfter() = %d, want %d", got, tt.want)
			}
		})
	}
}
