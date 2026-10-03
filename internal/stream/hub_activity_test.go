package stream

import (
	"testing"

	"github.com/google/uuid"
)

func pendingHints(hints <-chan uuid.UUID) []uuid.UUID {
	var got []uuid.UUID
	for {
		select {
		case sid := <-hints:
			got = append(got, sid)
		default:
			return got
		}
	}
}

func resyncPending(resync <-chan struct{}) bool {
	select {
	case <-resync:
		return true
	default:
		return false
	}
}

func TestHubActivity_RoutesByUser(t *testing.T) {
	hub := NewHub()
	alice, bob := uuid.New(), uuid.New()
	aliceTab1, _, unsub1 := hub.SubscribeActivity(alice)
	defer unsub1()
	aliceTab2, _, unsub2 := hub.SubscribeActivity(alice)
	defer unsub2()
	bobTab, _, unsub3 := hub.SubscribeActivity(bob)
	defer unsub3()

	sid := uuid.New()
	hub.NotifyActivity(alice, sid)

	for name, ch := range map[string]<-chan uuid.UUID{"first tab": aliceTab1, "second tab": aliceTab2} {
		if got := pendingHints(ch); len(got) != 1 || got[0] != sid {
			t.Errorf("%s hints = %v, want [%s]", name, got, sid)
		}
	}
	if got := pendingHints(bobTab); len(got) != 0 {
		t.Errorf("another user's hints = %v, want none", got)
	}
}

func TestHubActivity_NoClientIgnored(t *testing.T) {
	hub := NewHub()
	hub.NotifyActivity(uuid.New(), uuid.New())
	if len(hub.activity) != 0 {
		t.Fatalf("hub tracks %d users after a notify with no client, want 0", len(hub.activity))
	}
}

func TestHubActivity_OverflowSignalsResync(t *testing.T) {
	hub := NewHub()
	user := uuid.New()
	hints, resync, unsub := hub.SubscribeActivity(user)
	defer unsub()

	for range activityHintBuffer {
		hub.NotifyActivity(user, uuid.New())
	}
	if resyncPending(resync) {
		t.Fatal("resync signalled before the hint buffer filled")
	}

	// Overflow never blocks and leaves exactly one resync pending.
	for range 5 {
		hub.NotifyActivity(user, uuid.New())
	}
	if !resyncPending(resync) {
		t.Fatal("no resync after the hint buffer overflowed")
	}
	if resyncPending(resync) {
		t.Fatal("more than one resync pending")
	}

	if got := pendingHints(hints); len(got) != activityHintBuffer {
		t.Fatalf("drained %d hints, want %d", len(got), activityHintBuffer)
	}
	sid := uuid.New()
	hub.NotifyActivity(user, sid)
	if got := pendingHints(hints); len(got) != 1 || got[0] != sid {
		t.Fatalf("hints after drain = %v, want [%s]", got, sid)
	}
}

func TestHubActivity_UnsubscribeStopsDelivery(t *testing.T) {
	hub := NewHub()
	user := uuid.New()
	hints, _, unsub := hub.SubscribeActivity(user)
	unsub()

	hub.NotifyActivity(user, uuid.New())
	if got := pendingHints(hints); len(got) != 0 {
		t.Fatalf("hints after unsubscribe = %v, want none", got)
	}
	if len(hub.activity) != 0 {
		t.Fatalf("hub tracks %d users after the last unsubscribe, want 0", len(hub.activity))
	}
}

func TestHubHintAllResyncsActivityClients(t *testing.T) {
	hub := NewHub()
	_, resync, unsub := hub.SubscribeActivity(uuid.New())
	defer unsub()

	hub.hintAll()
	if !resyncPending(resync) {
		t.Fatal("hintAll did not signal a resync")
	}
}

func TestActivityFromPayload(t *testing.T) {
	user, sid := uuid.New(), uuid.New()
	tests := []struct {
		name    string
		payload string
		ok      bool
	}{
		{"valid", user.String() + ":" + sid.String(), true},
		{"no separator", user.String(), false},
		{"bad user", "x:" + sid.String(), false},
		{"bad session", user.String() + ":x", false},
		{"empty", "", false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			gotUser, gotSid, ok := activityFromPayload(tt.payload)
			if ok != tt.ok {
				t.Fatalf("ok = %v, want %v", ok, tt.ok)
			}
			if ok && (gotUser != user || gotSid != sid) {
				t.Errorf("parsed (%s, %s), want (%s, %s)", gotUser, gotSid, user, sid)
			}
		})
	}
}
