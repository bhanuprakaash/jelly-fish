package api

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/prometheus/client_golang/prometheus/testutil"

	"github.com/bhanuprakaash/jelly-fish/internal/auth"
	"github.com/bhanuprakaash/jelly-fish/internal/eventlog"
	"github.com/bhanuprakaash/jelly-fish/internal/stream"
)

// activityRepo serves the Activity stream from rows the test edits while the
// handler runs. statusGate, if set, blocks the first ActivityStatusOf after it
// has reported on entered.
type activityRepo struct {
	fakeRepo

	mu         sync.Mutex
	snapshot   []eventlog.ActivityRow
	status     map[uuid.UUID]eventlog.ActivityRow
	snapshots  int
	err        error
	entered    chan struct{}
	statusGate chan struct{}
}

func (r *activityRepo) ActivitySnapshot(context.Context, eventlog.TenantScope) ([]eventlog.ActivityRow, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.snapshots++
	return r.snapshot, r.err
}

func (r *activityRepo) ActivityStatusOf(_ context.Context, _ eventlog.TenantScope, sid uuid.UUID) (eventlog.ActivityRow, bool, error) {
	r.mu.Lock()
	gate := r.statusGate
	r.statusGate = nil
	r.mu.Unlock()
	if gate != nil {
		r.entered <- struct{}{}
		<-gate
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	row, ok := r.status[sid]
	return row, ok, r.err
}

func (r *activityRepo) setSnapshot(rows ...eventlog.ActivityRow) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.snapshot = rows
}

func (r *activityRepo) snapshotCalls() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.snapshots
}

func testUser() auth.User {
	return auth.User{ID: uuid.MustParse("aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa"), WorkspaceID: uuid.MustParse("bbbbbbbb-bbbb-4bbb-8bbb-bbbbbbbbbbbb")}
}

// serveAs runs h on a GET as user against a streamWriter until stop is called.
func serveAs(t *testing.T, h http.HandlerFunc, user auth.User) (w *streamWriter, stop func()) {
	t.Helper()
	ctx, cancel := context.WithCancel(auth.WithUser(t.Context(), user))
	req := httptest.NewRequest(http.MethodGet, "/", nil).WithContext(ctx)
	req.SetPathValue("id", uuid.NewString())
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

func newActivityStreams(t *testing.T, repo SessionRepo, hub *stream.Hub) *sessionStreams {
	t.Helper()
	return newStreams(t, repo, hub, &fakeDeltaBus{})
}

func rootID() uuid.UUID  { return uuid.MustParse("11111111-1111-4111-8111-111111111111") }
func childID() uuid.UUID { return uuid.MustParse("22222222-2222-4222-8222-222222222222") }

func ptr(id uuid.UUID) *uuid.UUID { return &id }

func TestActivityStreamSnapshotFirst(t *testing.T) {
	w, stop := serveAs(t, newActivityStreams(t, &activityRepo{}, stream.NewHub()).handleActivity, testUser())
	defer stop()

	const want = "retry: 2000\n\nevent: snapshot\ndata: []\n\n"
	w.waitFor(t, want)
	if got := w.body(); !strings.HasPrefix(got, want) || strings.Contains(got, "id:") {
		t.Errorf("body = %q, want it to start with %q and carry no id", got, want)
	}
}

func TestActivityStreamSnapshotListsRows(t *testing.T) {
	repo := &activityRepo{snapshot: []eventlog.ActivityRow{
		{SessionID: rootID(), RootID: rootID(), Status: eventlog.StatusRunning},
		{SessionID: childID(), RootID: rootID(), ParentID: ptr(rootID()), Status: eventlog.StatusAwaitingApproval},
	}}
	w, stop := serveAs(t, newActivityStreams(t, repo, stream.NewHub()).handleActivity, testUser())
	defer stop()

	w.waitFor(t, "event: snapshot\ndata: "+
		`[{"session_id":"11111111-1111-4111-8111-111111111111","root_id":"11111111-1111-4111-8111-111111111111","parent_id":null,"status":"running","needs_approval":false},`+
		`{"session_id":"22222222-2222-4222-8222-222222222222","root_id":"11111111-1111-4111-8111-111111111111","parent_id":"11111111-1111-4111-8111-111111111111","status":"awaiting_approval","needs_approval":true}]`+
		"\n\n")
}

func TestActivityStreamStatusFrame(t *testing.T) {
	hub := stream.NewHub()
	repo := &activityRepo{status: map[uuid.UUID]eventlog.ActivityRow{
		childID(): {SessionID: childID(), RootID: rootID(), ParentID: ptr(rootID()), Status: eventlog.StatusAwaitingApproval},
	}}
	w, stop := serveAs(t, newActivityStreams(t, repo, hub).handleActivity, testUser())
	defer stop()

	// The snapshot is written after the handler subscribed.
	w.waitFor(t, "event: snapshot")
	hub.NotifyActivity(testUser().ID, childID())
	w.waitFor(t, "event: status\ndata: "+
		`{"session_id":"22222222-2222-4222-8222-222222222222","root_id":"11111111-1111-4111-8111-111111111111","parent_id":"11111111-1111-4111-8111-111111111111","status":"awaiting_approval","needs_approval":true}`+
		"\n\n")
	if strings.Contains(w.body(), "\nid:") {
		t.Errorf("status frame carries an id: %q", w.body())
	}
}

func TestActivityStreamSkipsInvisible(t *testing.T) {
	hub := stream.NewHub()
	hidden := uuid.New()
	repo := &activityRepo{status: map[uuid.UUID]eventlog.ActivityRow{
		rootID(): {SessionID: rootID(), RootID: rootID(), Status: eventlog.StatusRunning},
	}}
	w, stop := serveAs(t, newActivityStreams(t, repo, hub).handleActivity, testUser())
	defer stop()

	w.waitFor(t, "event: snapshot")
	hub.NotifyActivity(testUser().ID, hidden)
	hub.NotifyActivity(testUser().ID, rootID())
	// Hints are handled in order, so the hidden one has been handled by now.
	w.waitFor(t, `"status":"running"`)
	if strings.Contains(w.body(), hidden.String()) || strings.Count(w.body(), "event: status") != 1 {
		t.Errorf("body = %q, want one status frame for the visible session only", w.body())
	}
}

func TestActivityStreamOverflowSendsFreshSnapshot(t *testing.T) {
	hub := stream.NewHub()
	gate := make(chan struct{})
	repo := &activityRepo{
		status:     map[uuid.UUID]eventlog.ActivityRow{rootID(): {SessionID: rootID(), RootID: rootID(), Status: eventlog.StatusRunning}},
		entered:    make(chan struct{}, 1),
		statusGate: gate,
	}
	w, stop := serveAs(t, newActivityStreams(t, repo, hub).handleActivity, testUser())
	defer stop()

	w.waitFor(t, "event: snapshot")
	// Park the handler inside its first status lookup, then overflow its queue.
	hub.NotifyActivity(testUser().ID, rootID())
	<-repo.entered
	for range 100 {
		hub.NotifyActivity(testUser().ID, rootID())
	}
	repo.setSnapshot(eventlog.ActivityRow{SessionID: childID(), RootID: rootID(), ParentID: ptr(rootID()), Status: eventlog.StatusSleeping})
	close(gate)

	w.waitFor(t, `"status":"sleeping"`)
	if got := strings.Count(w.body(), "event: snapshot"); got != 2 {
		t.Errorf("snapshot frames = %d, want 2 (connect, then the resync)", got)
	}
}

func TestActivityStreamHeaders(t *testing.T) {
	w, stop := serveAs(t, newActivityStreams(t, &activityRepo{}, stream.NewHub()).handleActivity, testUser())
	defer stop()
	w.waitFor(t, "retry: 2000\n\n")

	w.mu.Lock()
	defer w.mu.Unlock()
	want := map[string]string{"Content-Type": "text/event-stream", "Cache-Control": "no-cache", "X-Accel-Buffering": "no"}
	for k, v := range want {
		if got := w.header.Get(k); got != v {
			t.Errorf("%s = %q, want %q", k, got, v)
		}
	}
	if w.status != http.StatusOK {
		t.Errorf("status = %d, want 200", w.status)
	}
}

func TestActivityStreamPing(t *testing.T) {
	repo := &activityRepo{}
	streams := newActivityStreams(t, repo, stream.NewHub())
	streams.pingInterval = 10 * time.Millisecond
	w, stop := serveAs(t, streams.handleActivity, testUser())
	defer stop()

	w.waitFor(t, ": ping\n\n")
	w.waitFor(t, ": ping\n\n: ping\n\n")
	if got := repo.snapshotCalls(); got != 1 {
		t.Errorf("snapshot queries = %d, want 1: a ping does not re-query", got)
	}
}

func TestActivityStreamArmsWriteDeadlineBeforeEveryWrite(t *testing.T) {
	hub := stream.NewHub()
	repo := &activityRepo{status: map[uuid.UUID]eventlog.ActivityRow{rootID(): {SessionID: rootID(), RootID: rootID(), Status: eventlog.StatusRunning}}}
	w, stop := serveAs(t, newActivityStreams(t, repo, hub).handleActivity, testUser())

	w.waitFor(t, "event: snapshot")
	hub.NotifyActivity(testUser().ID, rootID())
	w.waitFor(t, "event: status")
	stop()

	w.mu.Lock()
	defer w.mu.Unlock()
	// retry, snapshot, status.
	if len(w.deadlines) != 3 || w.unarmed != 0 {
		t.Fatalf("deadlines set = %d, writes without one = %d; want 3 and 0", len(w.deadlines), w.unarmed)
	}
	for _, d := range w.deadlines {
		if d <= 0 || d > writeTimeout {
			t.Errorf("deadline in %v, want within (0, %v]", d, writeTimeout)
		}
	}
}

func TestActivityStreamClosesWhenLoginEnds(t *testing.T) {
	streams := newActivityStreams(t, &activityRepo{}, stream.NewHub())
	streams.logins = fakeLogins{active: false}
	streams.pingInterval = 10 * time.Millisecond
	req := httptest.NewRequest(http.MethodGet, "/", nil).WithContext(auth.WithUser(t.Context(), testUser()))
	done := make(chan struct{})
	go func() {
		streams.handleActivity(newStreamWriter(), req)
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("stream stayed open after its Login Session ended")
	}
}

func TestActivityStreamClosesOnRepoError(t *testing.T) {
	repo := &activityRepo{err: context.DeadlineExceeded}
	streams := newActivityStreams(t, repo, stream.NewHub())
	w := newStreamWriter()
	req := httptest.NewRequest(http.MethodGet, "/", nil).WithContext(auth.WithUser(t.Context(), testUser()))
	streams.handleActivity(w, req)

	// A 500 would stop EventSource retrying; closing the stream makes it retry.
	if w.status != http.StatusOK || strings.Contains(w.body(), "event:") {
		t.Errorf("status = %d, body = %q, want 200 and no data frame", w.status, w.body())
	}
	if got := testutil.ToFloat64(streams.metrics.OpenStreams.WithLabelValues(stream.KindActivity)); got != 0 {
		t.Errorf("open activity streams = %v, want 0", got)
	}
}

func TestStreamCapSharedAcrossKinds(t *testing.T) {
	const each = maxStreamsPerUser / 2
	streams := newActivityStreams(t, &activityRepo{}, stream.NewHub())
	var stops []func()
	open := func(h http.HandlerFunc) {
		w, stop := serveAs(t, h, testUser())
		w.waitFor(t, "retry: 2000\n\n")
		stops = append(stops, stop)
	}
	for range each {
		open(streams.handle)
		open(streams.handleActivity)
	}
	gauge := func(kind string) float64 {
		return testutil.ToFloat64(streams.metrics.OpenStreams.WithLabelValues(kind))
	}
	if gauge(stream.KindSession) != each || gauge(stream.KindActivity) != each {
		t.Fatalf("open streams: session %v, activity %v; want %d each", gauge(stream.KindSession), gauge(stream.KindActivity), each)
	}

	refuse := func(h http.HandlerFunc) {
		t.Helper()
		rr := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodGet, "/", nil).WithContext(auth.WithUser(t.Context(), testUser()))
		req.SetPathValue("id", uuid.NewString())
		h(rr, req)
		if rr.Code != http.StatusTooManyRequests {
			t.Fatalf("21st stream status = %d, want 429", rr.Code)
		}
		if strings.Contains(rr.Header().Get("Content-Type"), "event-stream") {
			t.Errorf("refused response is SSE: %q", rr.Header().Get("Content-Type"))
		}
	}
	refuse(streams.handleActivity)
	refuse(streams.handle)
	if got := testutil.ToFloat64(streams.metrics.Refusals); got != 2 {
		t.Errorf("refusals = %v, want 2", got)
	}

	// An Activity Stream closing frees a slot for either kind.
	stops[1]()
	if gauge(stream.KindActivity) != each-1 {
		t.Errorf("open activity streams after a close = %v, want %d", gauge(stream.KindActivity), each-1)
	}
	w, stop := serveAs(t, streams.handle, testUser())
	defer stop()
	w.waitFor(t, "retry: 2000\n\n")

	for _, stop := range stops {
		stop()
	}
}

func TestActivityRequiresLogin(t *testing.T) {
	srv := newTestServer(t, &fakeRepo{})
	rr := httptest.NewRecorder()
	srv.Handler.ServeHTTP(rr, httptest.NewRequest(http.MethodGet, "/api/activity", nil))
	if rr.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401", rr.Code)
	}
}
