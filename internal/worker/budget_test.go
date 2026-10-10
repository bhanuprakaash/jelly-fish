package worker_test

import (
	"context"
	"log/slog"
	"slices"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/bhanuprakaash/jelly-fish/internal/eventlog"
	"github.com/bhanuprakaash/jelly-fish/internal/msg"
	"github.com/bhanuprakaash/jelly-fish/internal/provider"
	"github.com/bhanuprakaash/jelly-fish/internal/provider/fake"
	"github.com/bhanuprakaash/jelly-fish/internal/stream"
	"github.com/bhanuprakaash/jelly-fish/internal/testdb"
	"github.com/bhanuprakaash/jelly-fish/internal/tool"
	"github.com/bhanuprakaash/jelly-fish/internal/worker"
)

// priced is a Catalog that charges $1 per input token for every model.
type priced struct{}

func (priced) Lookup(id string) (provider.ModelInfo, bool) {
	return provider.ModelInfo{ID: id, Provider: fake.Name, Price: &provider.Prices{Input: 1_000_000}}, true
}

func startPriced(t *testing.T, pool *pgxpool.Pool, p provider.Provider, tools *tool.Registry) {
	t.Helper()
	gw := worker.Gateway{Fake: p, Catalog: priced{}}
	ctx, cancel := context.WithCancel(t.Context())
	w := worker.New(pool, gw, tools, stream.NewPGDeltaBus(pool), worker.Lease{TTL: 30 * time.Second, Heartbeat: 10 * time.Second}, eventlog.Upcasters{}, slog.New(slog.DiscardHandler))
	worker.SetTitleTimeout(w, 0)
	done := make(chan struct{})
	go func() { w.Run(ctx); close(done) }()
	t.Cleanup(func() { cancel(); <-done })
}

func TestTurnLimitParksBeforeTheNextTurn(t *testing.T) {
	pool := testdb.NewPool(t)
	sid, _ := newFakeSession(t, pool)
	_, err := eventlog.NewStore(pool).Append(t.Context(), sid, nil, []eventlog.NewEvent{{
		Type: eventlog.TypeConfigChanged, Actor: "user", Payload: map[string]any{"budget": eventlog.Budget{Turns: 1}},
	}}, nil)
	if err != nil {
		t.Fatal(err)
	}
	pause := provider.Response{Message: msg.AssistantText("one moment"), StopReason: provider.StopReasonPauseTurn}
	p := &replies{list: []provider.Response{pause, done()}}
	startTools(t, pool, p, 10*time.Second, nil)
	waitStatus(t, pool, sid, eventlog.StatusAwaitingApproval, 1)

	evs := loadEvents(t, pool, sid)
	if n := len(ofType(evs, eventlog.TypeTurnStarted)); n != 1 {
		t.Fatalf("turn.started events = %d, want 1", n)
	}
	exceeded := ofType(evs, eventlog.TypeBudgetExceeded)
	if len(exceeded) != 1 || exceeded[0].Payload["dimension"] != "turns" || exceeded[0].Payload["limit"] != 1.0 || exceeded[0].Payload["used"] != 1.0 {
		t.Fatalf("budget.exceeded = %v, want one {turns, limit 1, used 1}", exceeded)
	}
	asked := ofType(evs, eventlog.TypeApprovalRequested)
	if len(asked) != 1 || asked[0].Payload["kind"] != "budget" || asked[0].Payload["dimension"] != "turns" {
		t.Fatalf("approval.requested = %v, want one {budget, turns}", asked)
	}
	var types []string
	for _, e := range evs[len(evs)-3:] {
		types = append(types, e.Type)
	}
	if want := []string{eventlog.TypeBudgetExceeded, eventlog.TypeApprovalRequested, eventlog.TypeStatusChanged}; !slices.Equal(types, want) {
		t.Fatalf("last events = %v, want %v", types, want)
	}
	var leaseOwner *string
	if err := pool.QueryRow(t.Context(), `SELECT lease_owner FROM sessions WHERE id = $1`, sid).Scan(&leaseOwner); err != nil {
		t.Fatal(err)
	}
	if leaseOwner != nil {
		t.Fatalf("lease_owner = %q, want NULL", *leaseOwner)
	}
}

func TestDollarLimitCrossedByAReplyParksBeforeItsTools(t *testing.T) {
	pool := testdb.NewPool(t)
	sid, _ := newFakeSession(t, pool)
	reply := toolUse("a")
	reply.Usage = provider.Usage{Input: 3}
	p := &replies{list: []provider.Response{reply, done()}}
	startPriced(t, pool, p, tool.NewRegistry(fn{id: "a", call: func(context.Context) tool.Result { return tool.TextResult("ok", false) }}))
	waitStatus(t, pool, sid, eventlog.StatusAwaitingApproval, 1)

	evs := loadEvents(t, pool, sid)
	if got := toolTypes(evs); len(got) != 0 {
		t.Fatalf("tool events = %v, want none", got)
	}
	exceeded := ofType(evs, eventlog.TypeBudgetExceeded)
	if len(exceeded) != 1 || exceeded[0].Payload["dimension"] != "dollars" || exceeded[0].Payload["limit"] != 2_000_000.0 || exceeded[0].Payload["used"] != 3_000_000.0 {
		t.Fatalf("budget.exceeded = %v, want one {dollars, limit 2000000, used 3000000}", exceeded)
	}
}

func TestUserMessageResetsTurnsButNotTokensOrDollars(t *testing.T) {
	user := func(seq int64) eventlog.Event {
		return ev(t, seq, eventlog.TypeUserMessage, map[string]any{"message": msg.UserText("hi")})
	}
	turn := func(seq int64) eventlog.Event {
		return ev(t, seq, eventlog.TypeTurnStarted, map[string]any{"turn_id": "t", "input_through_seq": 1})
	}
	created := ev(t, 1, eventlog.TypeSessionCreated, map[string]any{"agent": map[string]any{"model": "fake", "budget": eventlog.Budget{Tokens: 1000, CostMicros: 50, Turns: 2}}})
	usage := ev(t, 5, eventlog.TypeUsageRecorded, eventlog.UsageRecorded{Kind: eventlog.KindLLM, Provider: "fake", Quantity: 10, Unit: "input_tokens", CostMicros: 5})

	st, err := worker.Fold([]eventlog.Event{created, user(2), turn(3), turn(4), usage})
	if err != nil {
		t.Fatal(err)
	}
	if st.TurnsSinceUser != 2 {
		t.Fatalf("TurnsSinceUser = %d, want 2", st.TurnsSinceUser)
	}
	st, err = worker.Fold([]eventlog.Event{created, user(2), turn(3), turn(4), usage, user(6)})
	if err != nil {
		t.Fatal(err)
	}
	if st.TurnsSinceUser != 0 || st.TokensUsed != 10 || st.CostMicros != 5 {
		t.Fatalf("after a user.message: turns %d, tokens %d, cost %d; want 0, 10, 5", st.TurnsSinceUser, st.TokensUsed, st.CostMicros)
	}
}

func TestAllowRaisesALimitByOneMoreOriginalEachTime(t *testing.T) {
	log := []eventlog.Event{ev(t, 1, eventlog.TypeSessionCreated, map[string]any{"agent": map[string]any{"model": "fake", "budget": eventlog.Budget{Tokens: 1000, Turns: 25}}})}
	ask := func(id string) {
		log = append(log, ev(t, int64(len(log)+1), eventlog.TypeApprovalRequested, map[string]any{"approval_id": id, "kind": "budget", "dimension": "tokens"}))
	}
	answer := func(id, decision string) {
		log = append(log, ev(t, int64(len(log)+1), eventlog.TypeApprovalResolved, map[string]any{"approval_id": id, "decision": decision, "by": "user"}))
	}
	tokenLimit := func() int64 {
		st, err := worker.Fold(log)
		if err != nil {
			t.Fatal(err)
		}
		return st.Limit("tokens")
	}

	ask("a1")
	if got := tokenLimit(); got != 1000 {
		t.Fatalf("limit while asking = %d, want 1000", got)
	}
	answer("a1", "allow")
	if got := tokenLimit(); got != 2000 {
		t.Fatalf("limit after one allow = %d, want 2000", got)
	}
	ask("a2")
	answer("a2", "allow")
	if got := tokenLimit(); got != 3000 {
		t.Fatalf("limit after two allows = %d, want 3000", got)
	}
	ask("a3")
	answer("a3", "deny")
	if got := tokenLimit(); got != 3000 {
		t.Fatalf("limit after a deny = %d, want 3000", got)
	}
}
