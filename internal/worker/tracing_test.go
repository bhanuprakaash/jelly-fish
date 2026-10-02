package worker_test

import (
	"context"
	"log/slog"
	"testing"
	"time"

	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"

	"github.com/bhanuprakaash/jelly-fish/internal/eventlog"
	"github.com/bhanuprakaash/jelly-fish/internal/provider"
	"github.com/bhanuprakaash/jelly-fish/internal/stream"
	"github.com/bhanuprakaash/jelly-fish/internal/testdb"
	"github.com/bhanuprakaash/jelly-fish/internal/worker"
)

func TestTurnSpansCarrySessionAndTurnIDs(t *testing.T) {
	pool := testdb.NewPool(t)
	sid, _ := newFakeSession(t, pool)
	rec := tracetest.NewSpanRecorder()
	tp := sdktrace.NewTracerProvider(sdktrace.WithSpanProcessor(rec))
	t.Cleanup(func() { _ = tp.Shutdown(context.Background()) })

	p := &scripted{results: []error{providerErr(provider.KindProviderDown, 1), nil}}
	gw := worker.Gateway{Fake: p, Tracer: tp, Sleep: func(context.Context, time.Duration) error { return nil }}
	ctx, cancel := context.WithCancel(t.Context())
	w := worker.New(pool, gw, stream.NewPGDeltaBus(pool), worker.Lease{TTL: 30 * time.Second, Heartbeat: 10 * time.Second}, eventlog.Upcasters{}, slog.New(slog.DiscardHandler))
	done := make(chan struct{})
	go func() { w.Run(ctx); close(done) }()
	t.Cleanup(func() { cancel(); <-done })
	waitStatus(t, pool, sid, eventlog.StatusAwaitingUser, 1)

	var turnID string
	for _, e := range loadEvents(t, pool, sid) {
		if e.Type == eventlog.TypeTurnStarted {
			turnID, _ = e.Payload["turn_id"].(string)
		}
	}
	spans := rec.Ended()
	if len(spans) != 2 {
		t.Fatalf("got %d spans, want 2 (failed attempt, then success)", len(spans))
	}
	for i, s := range spans {
		got := map[string]string{}
		for _, kv := range s.Attributes() {
			got[string(kv.Key)] = kv.Value.Emit()
		}
		if got["jf.session_id"] != sid.String() || got["jf.turn_id"] != turnID || turnID == "" {
			t.Errorf("span %d ids = %q/%q, want %s/%q", i, got["jf.session_id"], got["jf.turn_id"], sid, turnID)
		}
	}
}
