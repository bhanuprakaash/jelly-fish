package api

import (
	"bufio"
	"context"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"slices"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/prometheus/client_golang/prometheus/testutil"

	"github.com/bhanuprakaash/jelly-fish/internal/eventlog"
	"github.com/bhanuprakaash/jelly-fish/internal/stream"
	"github.com/bhanuprakaash/jelly-fish/internal/testdb"
)

// messageEvents returns user.message events with seq 1..n.
func messageEvents(n int) []eventlog.Event {
	evs := make([]eventlog.Event, n)
	for i := range evs {
		evs[i] = eventlog.Event{Seq: int64(i + 1), Type: eventlog.TypeUserMessage, CreatedAt: time.Now(), Payload: []byte(`{}`)}
	}
	return evs
}

func seqRange(from, to int64) []int64 {
	var seqs []int64
	for s := from; s <= to; s++ {
		seqs = append(seqs, s)
	}
	return seqs
}

// frameIDs returns the id of every "id: N" line in an SSE body, in order.
func frameIDs(t *testing.T, body string) []int64 {
	t.Helper()
	var ids []int64
	for line := range strings.SplitSeq(body, "\n") {
		raw, ok := strings.CutPrefix(line, "id: ")
		if !ok {
			continue
		}
		id, err := strconv.ParseInt(raw, 10, 64)
		if err != nil {
			t.Fatalf("bad frame id %q", raw)
		}
		ids = append(ids, id)
	}
	return ids
}

func TestSessionEventsResume(t *testing.T) {
	tests := []struct {
		name        string
		lastEventID string
		query       string
		want        []int64
	}{
		{"Last-Event-ID resumes after it", "55", "", seqRange(56, 60)},
		{"Last-Event-ID beats ?after", "50", "?after=10", seqRange(51, 60)},
		{"?after used without a header", "", "?after=58", seqRange(59, 60)},
		{"non-numeric Last-Event-ID replays from 1", "abc", "", seqRange(1, 60)},
		{"Last-Event-ID past last_seq replays from 1", "160", "", seqRange(1, 60)},
		{"Last-Event-ID at last_seq sends nothing", "60", "", nil},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			repo := &fakeRepo{lastSeq: 60, events: messageEvents(60)}
			h := newStreams(t, repo, stream.NewHub(), &fakeDeltaBus{}).handle
			header := http.Header{}
			if tt.lastEventID != "" {
				header.Set("Last-Event-ID", tt.lastEventID)
			}
			w, stop := serveRequest(t, h, uuid.New(), tt.query, header)
			w.waitFor(t, "retry: 2000\n\n")
			stop()

			if w.status != http.StatusOK {
				t.Errorf("status = %d, want %d", w.status, http.StatusOK)
			}
			if got := frameIDs(t, w.body()); !slices.Equal(got, tt.want) {
				t.Errorf("frame ids = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestSessionEventsHeaders(t *testing.T) {
	h := newStreams(t, &fakeRepo{}, stream.NewHub(), &fakeDeltaBus{}).handle
	w, stop := serve(t, h, uuid.New())
	w.waitFor(t, "retry: 2000\n\n")
	stop()

	want := map[string]string{
		"Content-Type":      "text/event-stream",
		"Cache-Control":     "no-cache",
		"X-Accel-Buffering": "no",
	}
	for k, v := range want {
		if got := w.Header().Get(k); got != v {
			t.Errorf("%s = %q, want %q", k, got, v)
		}
	}
	if got := w.Header().Get("Content-Encoding"); got != "" {
		t.Errorf("Content-Encoding = %q, want none", got)
	}
}

func TestSessionEventsNeverSendInternals(t *testing.T) {
	repo := &fakeRepo{lastSeq: 5, events: []eventlog.Event{
		{Seq: 1, Type: eventlog.TypeSessionCreated, Actor: "user:u1", Payload: []byte(`{"agent":{"name":"General"}}`)},
		{Seq: 2, Type: eventlog.TypeTurnStarted, Actor: "worker:w1", Payload: []byte(`{"lease_epoch":3,"tools_hash":"x"}`)},
		{Seq: 3, Type: eventlog.TypeLLMResponse, Actor: "worker:w1", Payload: []byte(`{"turn_id":"t1","stop_reason":"end_turn","usage":{"input_tokens":3}}`)},
		{Seq: 4, Type: eventlog.TypeUsageRecorded, Actor: "worker:w1", Payload: []byte(`{"kind":"input_tokens","quantity":3}`)},
		{Seq: 5, Type: "approval.requested", Actor: "jev", Payload: []byte(`{"confidence":0.9,"by":"jev"}`)},
	}}
	h := newStreams(t, repo, stream.NewHub(), &fakeDeltaBus{}).handle
	w, stop := serve(t, h, uuid.New())
	w.waitFor(t, "id: 3\n")
	stop()

	body := strings.ToLower(w.body())
	for _, banned := range []string{"jev", "lease_epoch", "usage", "confidence", "worker:"} {
		if strings.Contains(body, banned) {
			t.Errorf("body contains %q: %q", banned, w.body())
		}
	}
	if got, want := frameIDs(t, w.body()), []int64{1, 3}; !slices.Equal(got, want) {
		t.Errorf("frame ids = %v, want %v", got, want)
	}
}

func TestSessionEventsStreamCap(t *testing.T) {
	streams := newStreams(t, &fakeRepo{}, stream.NewHub(), &fakeDeltaBus{})
	sid := uuid.New()

	stops := make([]func(), maxStreamsPerUser)
	for i := range stops {
		w, stop := serve(t, streams.handle, sid)
		w.waitFor(t, "retry: 2000\n\n")
		stops[i] = stop
	}
	open := streams.metrics.OpenStreams.WithLabelValues(stream.KindSession)
	if got := testutil.ToFloat64(open); got != maxStreamsPerUser {
		t.Fatalf("open streams = %v, want %d", got, maxStreamsPerUser)
	}

	refused := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/api/sessions/"+sid.String()+"/events", nil)
	req.SetPathValue("id", sid.String())
	streams.handle(refused, req)
	if refused.Code != http.StatusTooManyRequests {
		t.Fatalf("21st stream status = %d, want %d", refused.Code, http.StatusTooManyRequests)
	}
	if ct := refused.Header().Get("Content-Type"); strings.Contains(ct, "event-stream") {
		t.Errorf("refused response Content-Type = %q, want a non-SSE body", ct)
	}
	if got := testutil.ToFloat64(streams.metrics.Refusals); got != 1 {
		t.Errorf("refusals = %v, want 1", got)
	}

	stops[0]()
	w, stop := serve(t, streams.handle, sid)
	defer stop()
	w.waitFor(t, "retry: 2000\n\n")
	if w.status != http.StatusOK {
		t.Errorf("stream after closing one: status = %d, want %d", w.status, http.StatusOK)
	}
	for _, stop := range stops[1:] {
		stop()
	}
	if got := testutil.ToFloat64(open); got != 1 {
		t.Errorf("open streams after closing the rest = %v, want 1", got)
	}
}

func TestSessionEventsPingRequeriesEvents(t *testing.T) {
	streams := newStreams(t, &stagedRepo{}, stream.NewHub(), &fakeDeltaBus{})
	streams.pingInterval = 10 * time.Millisecond
	w, stop := serve(t, streams.handle, uuid.New())
	defer stop()

	// No hint is ever sent: only the ping's re-query can find the event.
	w.waitFor(t, "id: 1\n")
	if !strings.Contains(w.body(), ": ping\n\n") {
		t.Errorf("body has no ping: %q", w.body())
	}
}

func TestSessionEventsClosesOnWriteFailure(t *testing.T) {
	tests := []struct {
		name         string
		writeErr     error
		wantDeadline float64
	}{
		{"missed write deadline is counted", os.ErrDeadlineExceeded, 1},
		{"other write errors are not counted", io.ErrClosedPipe, 0},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			streams := newStreams(t, &fakeRepo{}, stream.NewHub(), &fakeDeltaBus{})
			sid := uuid.New()
			req := httptest.NewRequest(http.MethodGet, "/api/sessions/"+sid.String()+"/events", nil)
			req.SetPathValue("id", sid.String())
			w := newStreamWriter()
			w.writeErr = tt.writeErr

			done := make(chan struct{})
			go func() {
				streams.handle(w, req)
				close(done)
			}()
			select {
			case <-done:
			case <-time.After(2 * time.Second):
				t.Fatal("handler kept the stream open after a failed write")
			}

			if got := testutil.ToFloat64(streams.metrics.WriteDeadlineCloses); got != tt.wantDeadline {
				t.Errorf("write-deadline closes = %v, want %v", got, tt.wantDeadline)
			}
			if got := testutil.ToFloat64(streams.metrics.OpenStreams.WithLabelValues(stream.KindSession)); got != 0 {
				t.Errorf("open streams = %v, want 0", got)
			}
		})
	}
}

// fanoutBus gives every subscriber its own 64-slot queue, dropping when full.
type fanoutBus struct {
	mu   sync.Mutex
	subs map[chan stream.Delta]struct{}
}

func (b *fanoutBus) Subscribe(uuid.UUID) (<-chan stream.Delta, func()) {
	ch := make(chan stream.Delta, 64)
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.subs == nil {
		b.subs = make(map[chan stream.Delta]struct{})
	}
	b.subs[ch] = struct{}{}
	return ch, func() {
		b.mu.Lock()
		defer b.mu.Unlock()
		delete(b.subs, ch)
	}
}

func (b *fanoutBus) send(d stream.Delta) {
	b.mu.Lock()
	defer b.mu.Unlock()
	for ch := range b.subs {
		select {
		case ch <- d:
		default:
		}
	}
}

func TestSessionEventsNeverReadingClientIsClosedAndDoesNotStallOthers(t *testing.T) {
	bus := &fanoutBus{}
	streams := newStreams(t, &fakeRepo{}, stream.NewHub(), bus)
	streams.writeTimeout = 200 * time.Millisecond
	mux := http.NewServeMux()
	mux.HandleFunc("GET /api/sessions/{id}/events", streams.handle)
	srv := httptest.NewServer(mux)
	defer srv.Close()
	sid := uuid.New()
	path := "/api/sessions/" + sid.String() + "/events"
	open := streams.metrics.OpenStreams.WithLabelValues(stream.KindSession)

	// A raw connection that sends the request and never reads the response.
	slow, err := net.Dial("tcp", srv.Listener.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = slow.Close() }()
	if tcp, ok := slow.(*net.TCPConn); ok {
		_ = tcp.SetReadBuffer(1024)
	}
	if _, err := io.WriteString(slow, "GET "+path+" HTTP/1.1\r\nHost: x\r\n\r\n"); err != nil {
		t.Fatal(err)
	}

	req, err := http.NewRequestWithContext(t.Context(), http.MethodGet, srv.URL+path, nil)
	if err != nil {
		t.Fatal(err)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()
	marker := make(chan struct{})
	go func() {
		r := bufio.NewReader(resp.Body)
		for {
			line, err := r.ReadString('\n')
			if err != nil {
				return
			}
			if strings.Contains(line, `"text":"marker"`) {
				close(marker)
				return
			}
		}
	}()

	big := strings.Repeat("x", 64<<10)
	deadline := time.After(10 * time.Second)
	for testutil.ToFloat64(streams.metrics.WriteDeadlineCloses) < 1 {
		select {
		case <-deadline:
			t.Fatal("never-reading client was not closed")
		case <-time.After(time.Millisecond):
			bus.send(stream.Delta{TurnID: "t1", Kind: stream.KindText, Text: big})
		}
	}

	// The reader may still hold a backlog that fills its queue, so resend.
	markerDeadline := time.After(5 * time.Second)
	for got := false; !got; {
		bus.send(stream.Delta{TurnID: "t2", Kind: stream.KindText, Text: "marker"})
		select {
		case <-marker:
			got = true
		case <-markerDeadline:
			t.Fatal("the reading client did not get the marker after the slow one was closed")
		case <-time.After(10 * time.Millisecond):
		}
	}
	if got := testutil.ToFloat64(open); got != 1 {
		t.Errorf("open streams = %v, want 1 (the reader)", got)
	}
}

// gatedWriter blocks every write until gate is closed, and reports its first
// attempt on writing.
type gatedWriter struct {
	*streamWriter
	gate    chan struct{}
	writing chan struct{}
	once    sync.Once
}

func (g *gatedWriter) Write(p []byte) (int, error) {
	g.once.Do(func() { close(g.writing) })
	<-g.gate
	return g.streamWriter.Write(p)
}

func TestSessionEventsPausedClientDropsDeltasButGetsLLMResponse(t *testing.T) {
	const queue = 64
	const flood = 100

	pool := testdb.NewPool(t)
	bus := stream.NewPGDeltaBus(pool)
	hub := stream.NewHub()
	metrics := newTestMetrics(t)
	ctx, cancel := context.WithCancel(t.Context())
	var wg sync.WaitGroup
	t.Cleanup(func() { cancel(); wg.Wait() })
	wg.Go(func() { stream.Listen(ctx, pool, slog.New(slog.DiscardHandler), hub, bus, metrics) })

	// Publish to an unrelated session until Listen is LISTENing.
	probeSID := uuid.New()
	probe, unsubscribeProbe := bus.Subscribe(probeSID)
	defer unsubscribeProbe()
	for ready := false; !ready; {
		if err := bus.Publish(ctx, stream.Delta{SessionID: probeSID, TurnID: "probe", Kind: stream.KindText, Text: "p"}); err != nil {
			t.Fatal(err)
		}
		select {
		case <-probe:
			ready = true
		case <-time.After(100 * time.Millisecond):
		}
	}

	sid := uuid.New()
	streams := newStreams(t, &stagedRepo{}, hub, bus)
	streams.metrics = metrics
	reqCtx, cancelReq := context.WithCancel(t.Context())
	req := httptest.NewRequest(http.MethodGet, "/api/sessions/"+sid.String()+"/events", nil).WithContext(reqCtx)
	req.SetPathValue("id", sid.String())
	w := &gatedWriter{streamWriter: newStreamWriter(), gate: make(chan struct{}), writing: make(chan struct{})}
	done := make(chan struct{})
	go func() {
		streams.handle(w, req)
		close(done)
	}()
	defer func() {
		cancelReq()
		<-done
	}()

	// The first write comes after the handler subscribed, and blocks it there
	// without reading deltas.
	<-w.writing
	for range flood {
		if err := bus.Publish(ctx, stream.Delta{SessionID: sid, TurnID: "t1", Kind: stream.KindText, Text: "x"}); err != nil {
			t.Fatal(err)
		}
	}
	dropDeadline := time.Now().Add(5 * time.Second)
	for testutil.ToFloat64(metrics.DroppedDeltas) < flood-queue {
		if time.Now().After(dropDeadline) {
			t.Fatalf("dropped deltas = %v, want %d", testutil.ToFloat64(metrics.DroppedDeltas), flood-queue)
		}
		time.Sleep(time.Millisecond)
	}
	hub.Notify(sid)
	close(w.gate)

	w.waitFor(t, "id: 1\n")
	body := w.body()
	if got := strings.Count(body, "event: delta"); got != queue {
		t.Errorf("delta frames = %d, want the %d that fit the queue", got, queue)
	}
	if !strings.Contains(body, "llm.response") {
		t.Errorf("llm.response missing: %q", body)
	}
	if got := testutil.ToFloat64(metrics.DroppedDeltas); got != flood-queue {
		t.Errorf("dropped deltas = %v, want %d", got, flood-queue)
	}
}

func TestStreamLimiter(t *testing.T) {
	limiter := newStreamLimiter(2)
	alice, bob := uuid.New(), uuid.New()

	releaseA1, ok := limiter.acquire(alice)
	if !ok {
		t.Fatal("first stream refused")
	}
	if _, ok := limiter.acquire(alice); !ok {
		t.Fatal("second stream refused")
	}
	if _, ok := limiter.acquire(alice); ok {
		t.Error("third stream for the same user accepted")
	}
	if _, ok := limiter.acquire(bob); !ok {
		t.Error("another user refused because of alice's streams")
	}
	releaseA1()
	if _, ok := limiter.acquire(alice); !ok {
		t.Error("stream refused after one was released")
	}
}
