package eventlog_test

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"sort"
	"sync"
	"testing"

	"github.com/google/uuid"

	"github.com/bhanuprakaash/jelly-fish/internal/eventlog"
	"github.com/bhanuprakaash/jelly-fish/internal/testdb"
)

func TestCreateSession(t *testing.T) {
	pool := testdb.NewPool(t)
	repo := eventlog.NewRepo(pool)
	scope := eventlog.DevScope()
	sessionID := uuid.New()
	clientMsgID := uuid.New()

	last, err := repo.CreateSession(t.Context(), scope, sessionID, clientMsgID, "hello")
	if err != nil {
		t.Fatalf("CreateSession: %v", err)
	}
	if last != 2 {
		t.Fatalf("last_seq = %d, want 2", last)
	}

	evs, err := repo.ListEvents(t.Context(), scope, sessionID, 0)
	if err != nil {
		t.Fatalf("ListEvents: %v", err)
	}
	if len(evs) != 2 {
		t.Fatalf("len(events) = %d, want 2", len(evs))
	}
	if evs[0].Type != eventlog.TypeSessionCreated || evs[0].Seq != 1 {
		t.Errorf("events[0] = %+v, want session.created seq 1", evs[0])
	}
	if evs[1].Type != eventlog.TypeUserMessage || evs[1].Seq != 2 {
		t.Errorf("events[1] = %+v, want user.message seq 2", evs[1])
	}

	var created struct {
		AgentID string `json:"agent_id"`
		Agent   struct {
			Name  string `json:"name"`
			Model string `json:"model"`
		} `json:"agent"`
	}
	if err := json.Unmarshal(evs[0].Payload, &created); err != nil {
		t.Fatalf("unmarshal session.created payload: %v", err)
	}
	if created.Agent.Name != "General" {
		t.Errorf("agent.name = %q, want General", created.Agent.Name)
	}
}

func TestCreateSession_RepeatIsIdempotent(t *testing.T) {
	pool := testdb.NewPool(t)
	repo := eventlog.NewRepo(pool)
	scope := eventlog.DevScope()
	sessionID := uuid.New()
	clientMsgID := uuid.New()

	first, err := repo.CreateSession(t.Context(), scope, sessionID, clientMsgID, "hello")
	if err != nil {
		t.Fatalf("first CreateSession: %v", err)
	}
	second, err := repo.CreateSession(t.Context(), scope, sessionID, uuid.New(), "hello again")
	if err != nil {
		t.Fatalf("second CreateSession: %v", err)
	}
	if second != first {
		t.Fatalf("second last_seq = %d, want %d (unchanged)", second, first)
	}

	evs, err := repo.ListEvents(t.Context(), scope, sessionID, 0)
	if err != nil {
		t.Fatalf("ListEvents: %v", err)
	}
	if len(evs) != 2 {
		t.Fatalf("len(events) = %d, want 2 (repeat created no new events)", len(evs))
	}
}

func TestCreateSession_ConcurrentRaceIsIdempotent(t *testing.T) {
	pool := testdb.NewPool(t)
	repo := eventlog.NewRepo(pool)
	scope := eventlog.DevScope()
	sessionID := uuid.New()

	const n = 10
	var wg sync.WaitGroup
	lasts := make([]int64, n)
	errs := make([]error, n)
	for i := range n {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			lasts[i], errs[i] = repo.CreateSession(context.Background(), scope, sessionID, uuid.New(), "hello")
		}(i)
	}
	wg.Wait()

	for i, err := range errs {
		// One goroutine's INSERT always hits the others' unique violation on
		// sessionID; that must resolve like a repeat, not fail the request.
		if err != nil {
			t.Fatalf("CreateSession[%d]: %v", i, err)
		}
		if lasts[i] != 2 {
			t.Errorf("CreateSession[%d] last_seq = %d, want 2", i, lasts[i])
		}
	}

	evs, err := repo.ListEvents(t.Context(), scope, sessionID, 0)
	if err != nil {
		t.Fatalf("ListEvents: %v", err)
	}
	if len(evs) != 2 {
		t.Fatalf("len(events) = %d, want 2 (race produced only one session)", len(evs))
	}
}

func TestPostMessage_DuplicateClientMsgIDIsIdempotent(t *testing.T) {
	pool := testdb.NewPool(t)
	repo := eventlog.NewRepo(pool)
	scope := eventlog.DevScope()
	sessionID := uuid.New()

	if _, err := repo.CreateSession(t.Context(), scope, sessionID, uuid.New(), "hello"); err != nil {
		t.Fatalf("CreateSession: %v", err)
	}

	clientMsgID := uuid.New()
	first, err := repo.PostMessage(t.Context(), scope, sessionID, clientMsgID, "follow up")
	if err != nil {
		t.Fatalf("first PostMessage: %v", err)
	}
	second, err := repo.PostMessage(t.Context(), scope, sessionID, clientMsgID, "follow up")
	if err != nil {
		t.Fatalf("second PostMessage: %v", err)
	}
	if second != first {
		t.Fatalf("second seq = %d, want %d", second, first)
	}

	evs, err := repo.ListEvents(t.Context(), scope, sessionID, 2)
	if err != nil {
		t.Fatalf("ListEvents: %v", err)
	}
	if len(evs) != 1 {
		t.Fatalf("len(follow-up events) = %d, want 1", len(evs))
	}
}

func TestPostMessage_GaplessSeqUnderConcurrency(t *testing.T) {
	pool := testdb.NewPool(t)
	repo := eventlog.NewRepo(pool)
	scope := eventlog.DevScope()
	sessionID := uuid.New()

	if _, err := repo.CreateSession(t.Context(), scope, sessionID, uuid.New(), "hello"); err != nil {
		t.Fatalf("CreateSession: %v", err)
	}

	const n = 1000
	dup := uuid.New()
	if _, err := repo.PostMessage(t.Context(), scope, sessionID, dup, "already sent"); err != nil {
		t.Fatalf("seed duplicate message: %v", err)
	}

	var wg sync.WaitGroup
	seqs := make([]int64, n)
	errs := make([]error, n)
	for i := range n {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			// Every 10th call repeats the already-used client_msg_id: a
			// forced rollback (unique violation) that must not open a gap.
			id := uuid.New()
			if i%10 == 0 {
				id = dup
			}
			seqs[i], errs[i] = repo.PostMessage(context.Background(), scope, sessionID, id, "msg")
		}(i)
	}
	wg.Wait()

	seen := map[int64]int{}
	for i, err := range errs {
		if err != nil {
			t.Fatalf("PostMessage[%d]: %v", i, err)
		}
		seen[seqs[i]]++
	}

	distinct := make([]int64, 0, len(seen))
	for seq := range seen {
		distinct = append(distinct, seq)
	}
	sort.Slice(distinct, func(i, j int) bool { return distinct[i] < distinct[j] })

	// The duplicate id always resolves to the same seq (3), so it collapses
	// every repeat into one entry; the unique ids get one seq each.
	wantCount := n - n/10 + 1
	if len(distinct) != wantCount {
		t.Fatalf("distinct seqs = %d, want %d", len(distinct), wantCount)
	}
	for i, seq := range distinct {
		want := int64(3 + i)
		if seq != want {
			t.Fatalf("seqs not gapless: distinct[%d] = %d, want %d (all: %v)", i, seq, want, distinct)
		}
	}
}

func TestTenancy_WrongScopeIsNotFound(t *testing.T) {
	pool := testdb.NewPool(t)
	repo := eventlog.NewRepo(pool)
	scope := eventlog.DevScope()
	other := eventlog.TenantScope{WorkspaceID: uuid.New(), UserID: uuid.New()}
	sessionID := uuid.New()

	if _, err := repo.CreateSession(t.Context(), scope, sessionID, uuid.New(), "hello"); err != nil {
		t.Fatalf("CreateSession: %v", err)
	}

	if _, err := repo.SessionLastSeq(t.Context(), other, sessionID); !errors.Is(err, eventlog.ErrNotFound) {
		t.Errorf("SessionLastSeq wrong scope: err = %v, want ErrNotFound", err)
	}
	if _, err := repo.PostMessage(t.Context(), other, sessionID, uuid.New(), "hi"); !errors.Is(err, eventlog.ErrNotFound) {
		t.Errorf("PostMessage wrong scope: err = %v, want ErrNotFound", err)
	}
	if evs, err := repo.ListEvents(t.Context(), other, sessionID, 0); err != nil || len(evs) != 0 {
		t.Errorf("ListEvents wrong scope: (%v, %v), want (empty, nil)", evs, err)
	}
}

// TestRepoMethodsTakeTenantScope enforces event-log.md §5.15: every Repo
// method takes a TenantScope.
func TestRepoMethodsTakeTenantScope(t *testing.T) {
	repoType := reflect.TypeOf(&eventlog.Repo{})
	scopeType := reflect.TypeOf(eventlog.TenantScope{})

	for i := range repoType.NumMethod() {
		m := repoType.Method(i)
		found := false
		for j := range m.Type.NumIn() {
			if m.Type.In(j) == scopeType {
				found = true
				break
			}
		}
		if !found {
			t.Errorf("Repo.%s does not take a TenantScope", m.Name)
		}
	}
}
