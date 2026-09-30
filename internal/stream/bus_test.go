package stream

import (
	"encoding/json"
	"testing"

	"github.com/google/uuid"
)

func payload(t *testing.T, sid uuid.UUID, turnID, text string) string {
	t.Helper()
	b, err := json.Marshal(Delta{SessionID: sid, TurnID: turnID, Kind: KindText, Text: text})
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func TestSlowSubscriberFreezesTurnOthersUnaffected(t *testing.T) {
	bus := NewPGDeltaBus(nil)
	sid := uuid.New()
	slow, unsubscribeSlow := bus.Subscribe(sid)
	defer unsubscribeSlow()
	fast, unsubscribeFast := bus.Subscribe(sid)
	defer unsubscribeFast()

	deliver := func(turnID, text string) {
		t.Helper()
		if err := bus.deliverPayload(payload(t, sid, turnID, text)); err != nil {
			t.Fatal(err)
		}
	}

	// Fill slow's buffer, then overflow it: the overflowing delta is lost.
	for range subscriberBuffer {
		deliver("t1", "x")
		<-fast
	}
	deliver("t1", "lost")
	<-fast

	// Once slow catches up, later deltas of the gapped turn still skip it, so
	// the bubble never shows text with a hole in it.
	for range subscriberBuffer {
		<-slow
	}
	deliver("t1", "after")
	if d := <-fast; d.Text != "after" {
		t.Fatalf("fast subscriber got %q, want after", d.Text)
	}
	select {
	case d := <-slow:
		t.Fatalf("gapped turn delivered %q to the slow subscriber", d.Text)
	default:
	}

	// Another turn is unaffected.
	deliver("t2", "fresh")
	if d := <-slow; d.TurnID != "t2" {
		t.Fatalf("slow subscriber got turn %q, want t2", d.TurnID)
	}
}
