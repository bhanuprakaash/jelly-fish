package stream_test

import (
	"context"
	"log/slog"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/bhanuprakaash/jelly-fish/internal/stream"
	"github.com/bhanuprakaash/jelly-fish/internal/testdb"
)

func TestListenRoutesJFActivityToUserClients(t *testing.T) {
	pool := testdb.NewPool(t)
	hub := stream.NewHub()
	ctx, cancel := context.WithCancel(t.Context())
	var wg sync.WaitGroup
	t.Cleanup(func() { cancel(); wg.Wait() })
	wg.Go(func() {
		stream.Listen(ctx, pool, slog.New(slog.DiscardHandler), hub, stream.NewPGDeltaBus(pool), newMetrics(t))
	})

	user, other := uuid.New(), uuid.New()
	hints, resync, unsubscribe := hub.SubscribeActivity(user)
	defer unsubscribe()
	otherHints, _, unsubscribeOther := hub.SubscribeActivity(other)
	defer unsubscribeOther()

	// Listen resyncs its clients once it is LISTENing.
	select {
	case <-resync:
	case <-time.After(5 * time.Second):
		t.Fatal("timed out waiting for Listen to start")
	}

	notify := func(payload string) {
		t.Helper()
		if _, err := pool.Exec(ctx, `SELECT pg_notify('jf_activity', $1)`, payload); err != nil {
			t.Fatal(err)
		}
	}
	sid, otherSid := uuid.New(), uuid.New()
	notify(other.String() + ":" + otherSid.String())
	notify("garbage")
	notify(user.String() + ":not-a-uuid")
	notify(user.String() + ":" + sid.String())

	select {
	case got := <-hints:
		if got != sid {
			t.Fatalf("hint = %s, want %s (malformed payloads are ignored)", got, sid)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("timed out waiting for the hint")
	}
	// Notifications arrive in commit order, so the other user's is already routed.
	select {
	case got := <-otherHints:
		if got != otherSid {
			t.Fatalf("other user's hint = %s, want %s", got, otherSid)
		}
	default:
		t.Fatal("other user got no hint")
	}
	select {
	case got := <-hints:
		t.Fatalf("extra hint %s", got)
	case got := <-otherHints:
		t.Fatalf("extra hint for the other user %s", got)
	default:
	}
}
