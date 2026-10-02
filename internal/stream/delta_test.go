package stream_test

import (
	"context"
	"encoding/json"
	"log/slog"
	"slices"
	"strings"
	"sync"
	"testing"
	"testing/synctest"
	"time"

	"github.com/google/uuid"
	"github.com/prometheus/client_golang/prometheus/testutil"

	"github.com/bhanuprakaash/jelly-fish/internal/stream"
	"github.com/bhanuprakaash/jelly-fish/internal/testdb"
)

// recorder collects published deltas.
type recorder struct {
	mu   sync.Mutex
	sent []string
}

func (r *recorder) publish(_ context.Context, d stream.Delta) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.sent = append(r.sent, d.Text)
	return nil
}

func (r *recorder) texts() []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return slices.Clone(r.sent)
}

func TestBatcherFlushesOnInterval(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		rec := &recorder{}
		b := stream.NewBatcher(rec.publish, uuid.New(), "t1", stream.CoalesceInterval, slog.New(slog.DiscardHandler))
		stop := b.Start(t.Context())

		// Tokens within one interval go out as one batch, even with no
		// further token to trigger it.
		b.Add("a")
		b.Add(" b")
		time.Sleep(2 * stream.CoalesceInterval)
		if got, want := rec.texts(), []string{"a b"}; !slices.Equal(got, want) {
			t.Fatalf("after first interval published %q, want %q", got, want)
		}

		// A pause publishes nothing.
		time.Sleep(10 * stream.CoalesceInterval)
		if got := rec.texts(); len(got) != 1 {
			t.Fatalf("idle interval published %q", got)
		}

		b.Add(" c")
		time.Sleep(2 * stream.CoalesceInterval)
		if got, want := rec.texts(), []string{"a b", " c"}; !slices.Equal(got, want) {
			t.Fatalf("after pause published %q, want %q", got, want)
		}

		// stop publishes what is left before returning.
		b.Add(" d")
		stop()
		if got, want := rec.texts(), []string{"a b", " c", " d"}; !slices.Equal(got, want) {
			t.Fatalf("after stop published %q, want %q", got, want)
		}
	})
}

func TestBatcherResetDropsBufferedTextAndTellsSubscribers(t *testing.T) {
	var mu sync.Mutex
	var sent []stream.Delta
	publish := func(_ context.Context, d stream.Delta) error {
		mu.Lock()
		defer mu.Unlock()
		sent = append(sent, d)
		return nil
	}
	sid := uuid.New()
	b := stream.NewBatcher(publish, sid, "t1", time.Hour, slog.New(slog.DiscardHandler))
	b.Add("first attempt")
	b.Reset(t.Context())
	b.Add("second")
	b.Start(t.Context())()

	mu.Lock()
	defer mu.Unlock()
	if len(sent) != 2 || sent[0].Kind != stream.KindReset || sent[0].TurnID != "t1" || sent[0].SessionID != sid || sent[1].Text != "second" {
		t.Fatalf("published %+v, want a reset for t1 then only the second attempt's text", sent)
	}
}

func TestPGDeltaBusSplitsAndDelivers(t *testing.T) {
	pool := testdb.NewPool(t)
	bus := stream.NewPGDeltaBus(pool)
	ctx, cancel := context.WithCancel(t.Context())
	var wg sync.WaitGroup
	t.Cleanup(func() { cancel(); wg.Wait() })
	metrics := newMetrics(t)
	wg.Go(func() { stream.Listen(ctx, pool, slog.New(slog.DiscardHandler), stream.NewHub(), bus, metrics) })

	sid := uuid.New()
	ch, unsubscribe := bus.Subscribe(sid)
	defer unsubscribe()

	// Raw LISTEN alongside, to inspect the payload sizes on the wire.
	raw, err := pool.Acquire(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer raw.Release()
	if _, err := raw.Exec(ctx, "LISTEN jf_stream"); err != nil {
		t.Fatal(err)
	}

	// Quotes and multi-byte runes expand when JSON-escaped.
	text := strings.Repeat("é\"\\\n😀 word ", 3000)
	// Give the Listen goroutine time to LISTEN before publishing.
	time.Sleep(200 * time.Millisecond)
	if err := bus.Publish(ctx, stream.Delta{SessionID: sid, TurnID: "t1", Kind: stream.KindText, Text: text}); err != nil {
		t.Fatal(err)
	}

	var got strings.Builder
	for got.Len() < len(text) {
		select {
		case d := <-ch:
			if d.TurnID != "t1" {
				t.Fatalf("turn_id = %q", d.TurnID)
			}
			got.WriteString(d.Text)
		case <-time.After(5 * time.Second):
			t.Fatalf("timed out with %d of %d bytes", got.Len(), len(text))
		}
	}
	if got.String() != text {
		t.Fatal("reassembled text differs")
	}

	// Every payload the raw listener saw is small and valid.
	for {
		wctx, wcancel := context.WithTimeout(ctx, 200*time.Millisecond)
		n, err := raw.Conn().WaitForNotification(wctx)
		wcancel()
		if err != nil {
			break
		}
		if len(n.Payload) >= 8000 {
			t.Fatalf("payload is %d bytes, want < 8000", len(n.Payload))
		}
		var d stream.Delta
		if err := json.Unmarshal([]byte(n.Payload), &d); err != nil {
			t.Fatalf("payload not JSON: %v", err)
		}
	}
}

func TestPGDeltaBusFreezesTurnOfSlowSubscriber(t *testing.T) {
	pool := testdb.NewPool(t)
	bus := stream.NewPGDeltaBus(pool)
	ctx, cancel := context.WithCancel(t.Context())
	var wg sync.WaitGroup
	t.Cleanup(func() { cancel(); wg.Wait() })
	metrics := newMetrics(t)
	wg.Go(func() { stream.Listen(ctx, pool, slog.New(slog.DiscardHandler), stream.NewHub(), bus, metrics) })

	sid := uuid.New()
	ch, unsubscribe := bus.Subscribe(sid)
	defer unsubscribe()

	publish := func(turnID, text string) {
		t.Helper()
		if err := bus.Publish(ctx, stream.Delta{SessionID: sid, TurnID: turnID, Kind: stream.KindText, Text: text}); err != nil {
			t.Fatal(err)
		}
	}
	recv := func() stream.Delta {
		t.Helper()
		select {
		case d := <-ch:
			return d
		case <-time.After(5 * time.Second):
			t.Fatal("timed out waiting for a delta")
			return stream.Delta{}
		}
	}

	// Publish until Listen is LISTENing, then clear the probe.
	for ready := false; !ready; {
		publish("probe", "p")
		select {
		case <-ch:
			ready = true
		case <-time.After(100 * time.Millisecond):
		}
	}

	// A second subscriber keeps up and reports when it sees the t2 marker.
	// Notifications arrive in order, so by then every t1 delta was routed.
	fast, unsubscribeFast := bus.Subscribe(sid)
	defer unsubscribeFast()
	markerSeen := make(chan struct{})
	wg.Go(func() {
		for {
			select {
			case d := <-fast:
				if d.TurnID == "t2" && d.Text == "marker" {
					close(markerSeen)
					return
				}
			case <-ctx.Done():
				return
			}
		}
	})

	// Nobody reads ch, so it overflows and t1 is gapped for it.
	for range 200 {
		publish("t1", "x")
	}
	publish("t2", "marker")
	select {
	case <-markerSeen:
	case <-time.After(5 * time.Second):
		t.Fatal("timed out waiting for the marker")
	}
	for len(ch) > 0 {
		<-ch
	}
	if dropped := testutil.ToFloat64(metrics.DroppedDeltas); dropped < 100 {
		t.Errorf("dropped deltas = %v, want the overflow of the 200 published", dropped)
	}

	publish("t1", "after the gap")
	publish("t3", "fresh turn")
	if d := recv(); d.TurnID != "t3" {
		t.Fatalf("got turn %q text %q, want the fresh turn t3: a gapped turn kept streaming", d.TurnID, d.Text)
	}
}
