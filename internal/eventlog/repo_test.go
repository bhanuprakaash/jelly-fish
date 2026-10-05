package eventlog_test

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"slices"
	"sort"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/bhanuprakaash/jelly-fish/internal/eventlog"
	"github.com/bhanuprakaash/jelly-fish/internal/testdb"
)

func TestCreateSession(t *testing.T) {
	pool := testdb.NewPool(t)
	repo := eventlog.NewRepo(pool, "fake")
	scope := testdb.NewUser(t, pool).Scope()
	sessionID := uuid.New()
	clientMsgID := uuid.New()

	last, err := repo.CreateSession(t.Context(), scope, sessionID, clientMsgID, "hello", "", false)
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
	repo := eventlog.NewRepo(pool, "fake")
	scope := testdb.NewUser(t, pool).Scope()
	sessionID := uuid.New()
	clientMsgID := uuid.New()

	first, err := repo.CreateSession(t.Context(), scope, sessionID, clientMsgID, "hello", "", false)
	if err != nil {
		t.Fatalf("first CreateSession: %v", err)
	}
	second, err := repo.CreateSession(t.Context(), scope, sessionID, uuid.New(), "hello again", "", false)
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
	repo := eventlog.NewRepo(pool, "fake")
	scope := testdb.NewUser(t, pool).Scope()
	sessionID := uuid.New()

	const n = 10
	var wg sync.WaitGroup
	lasts := make([]int64, n)
	errs := make([]error, n)
	for i := range n {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			lasts[i], errs[i] = repo.CreateSession(context.Background(), scope, sessionID, uuid.New(), "hello", "", false)
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
	repo := eventlog.NewRepo(pool, "fake")
	scope := testdb.NewUser(t, pool).Scope()
	sessionID := uuid.New()

	if _, err := repo.CreateSession(t.Context(), scope, sessionID, uuid.New(), "hello", "", false); err != nil {
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
	repo := eventlog.NewRepo(pool, "fake")
	scope := testdb.NewUser(t, pool).Scope()
	sessionID := uuid.New()

	if _, err := repo.CreateSession(t.Context(), scope, sessionID, uuid.New(), "hello", "", false); err != nil {
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
	repo := eventlog.NewRepo(pool, "fake")
	scope := testdb.NewUser(t, pool).Scope()
	other := testdb.NewUser(t, pool).Scope()
	sessionID := uuid.New()

	if _, err := repo.CreateSession(t.Context(), scope, sessionID, uuid.New(), "hello", "", false); err != nil {
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

type interruptRow struct {
	status  string
	wake    *time.Time
	cancel  bool
	attempt int
}

func readRow(t *testing.T, pool *pgxpool.Pool, sid uuid.UUID) interruptRow {
	t.Helper()
	var r interruptRow
	if err := pool.QueryRow(t.Context(), `SELECT status, wake_at, cancel_requested, recovery_attempts FROM sessions WHERE id = $1`, sid).Scan(&r.status, &r.wake, &r.cancel, &r.attempt); err != nil {
		t.Fatal(err)
	}
	return r
}

func eventTypes(t *testing.T, repo *eventlog.Repo, scope eventlog.TenantScope, sid uuid.UUID, after int64) []string {
	t.Helper()
	evs, err := repo.ListEvents(t.Context(), scope, sid, after)
	if err != nil {
		t.Fatal(err)
	}
	var types []string
	for _, e := range evs {
		types = append(types, e.Type)
	}
	return types
}

func TestInterruptParksASleepingSession(t *testing.T) {
	pool := testdb.NewPool(t)
	repo := eventlog.NewRepo(pool, "fake")
	sid, scope := sleepingSession(t, pool, time.Minute)
	last, err := repo.SessionLastSeq(t.Context(), scope, sid)
	if err != nil {
		t.Fatal(err)
	}

	if err := repo.Interrupt(t.Context(), scope, sid); err != nil {
		t.Fatal(err)
	}
	if r := readRow(t, pool, sid); r.status != eventlog.StatusAwaitingUser || r.wake != nil || r.cancel {
		t.Fatalf("row = %+v, want awaiting_user with wake_at cleared", r)
	}
	want := []string{eventlog.TypeUserInterrupt, eventlog.TypeStatusChanged}
	if got := eventTypes(t, repo, scope, sid, last); !reflect.DeepEqual(got, want) {
		t.Fatalf("events = %v, want %v", got, want)
	}

	// Nothing wakes it afterwards, and a repeat writes nothing.
	if _, ok, err := eventlog.NewStore(pool).Claim(t.Context(), "w", time.Minute); err != nil || ok {
		t.Fatalf("Claim after Stop retrying: ok=%v err=%v", ok, err)
	}
	if err := repo.Interrupt(t.Context(), scope, sid); err != nil {
		t.Fatal(err)
	}
	if got := eventTypes(t, repo, scope, sid, last); len(got) != 2 {
		t.Fatalf("events after a repeat = %v", got)
	}
}

func TestInterruptAfterAWorkerClaimedFlagsTheRunningSession(t *testing.T) {
	pool := testdb.NewPool(t)
	repo := eventlog.NewRepo(pool, "fake")
	store := eventlog.NewStore(pool)
	sid, scope := sleepingSession(t, pool, time.Minute)
	if _, err := pool.Exec(t.Context(), `UPDATE sessions SET wake_at = now() - interval '1 second' WHERE id = $1`, sid); err != nil {
		t.Fatal(err)
	}
	if _, ok, err := store.Claim(t.Context(), "w2", 30*time.Second); err != nil || !ok {
		t.Fatalf("Claim: ok=%v err=%v", ok, err)
	}
	last, err := repo.SessionLastSeq(t.Context(), scope, sid)
	if err != nil {
		t.Fatal(err)
	}

	if err := repo.Interrupt(t.Context(), scope, sid); err != nil {
		t.Fatal(err)
	}
	if r := readRow(t, pool, sid); r.status != eventlog.StatusRunning || !r.cancel {
		t.Fatalf("row = %+v, want running with cancel_requested", r)
	}
	if got := eventTypes(t, repo, scope, sid, last); !reflect.DeepEqual(got, []string{eventlog.TypeUserInterrupt}) {
		t.Fatalf("events = %v, want just user.interrupt", got)
	}
}

func TestInterruptIgnoresOtherStatusesAndTenants(t *testing.T) {
	pool := testdb.NewPool(t)
	repo := eventlog.NewRepo(pool, "fake")
	scope := testdb.NewUser(t, pool).Scope()
	sid := uuid.New()
	if _, err := repo.CreateSession(t.Context(), scope, sid, uuid.New(), "hi", "", false); err != nil {
		t.Fatal(err)
	}
	if err := repo.Interrupt(t.Context(), scope, sid); err != nil {
		t.Fatal(err)
	}
	if got := eventTypes(t, repo, scope, sid, 2); len(got) != 0 {
		t.Fatalf("a runnable session got events %v", got)
	}
	if err := repo.Interrupt(t.Context(), testdb.NewUser(t, pool).Scope(), sid); !errors.Is(err, eventlog.ErrNotFound) {
		t.Fatalf("other tenant: err = %v, want ErrNotFound", err)
	}
}

func parkedWith(t *testing.T, pool *pgxpool.Pool, retryable bool, to string) (uuid.UUID, eventlog.TenantScope) {
	t.Helper()
	return parkedOn(t, pool, "key_invalid", retryable, to)
}

// parkedOn is parkedWith for a session.error of code.
func parkedOn(t *testing.T, pool *pgxpool.Pool, code string, retryable bool, to string) (uuid.UUID, eventlog.TenantScope) {
	t.Helper()
	store := eventlog.NewStore(pool)
	scope := testdb.NewUser(t, pool).Scope()
	sid := uuid.New()
	if _, err := eventlog.NewRepo(pool, "fake").CreateSession(t.Context(), scope, sid, uuid.New(), "hi", "", false); err != nil {
		t.Fatal(err)
	}
	c, ok, err := store.Claim(t.Context(), "w1", 30*time.Second)
	if err != nil || !ok {
		t.Fatalf("Claim: ok=%v err=%v", ok, err)
	}
	if _, err := store.AppendFenced(t.Context(), sid, c.Fence, []eventlog.NewEvent{{
		Type: eventlog.TypeSessionError, Actor: "worker:w1",
		Payload: map[string]any{"code": code, "message": "m", "retryable": retryable},
	}}, &eventlog.StatusChange{To: to, Reason: "error"}); err != nil {
		t.Fatal(err)
	}
	return sid, scope
}

func TestRetryResumesWhereTheUserCan(t *testing.T) {
	tests := []struct {
		name      string
		retryable bool
		to        string
		want      string
	}{
		{"after a stop-class error", false, eventlog.StatusAwaitingUser, eventlog.StatusRunnable},
		{"after retries are exhausted", false, eventlog.StatusFailed, eventlog.StatusRunnable},
		{"while sleeping", true, eventlog.StatusSleeping, eventlog.StatusRunnable},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			pool := testdb.NewPool(t)
			repo := eventlog.NewRepo(pool, "fake")
			sid, scope := parkedWith(t, pool, tc.retryable, tc.to)
			if _, err := pool.Exec(t.Context(), `UPDATE sessions SET recovery_attempts = 9, wake_at = now() + interval '1 hour' WHERE id = $1`, sid); err != nil {
				t.Fatal(err)
			}
			last, _ := repo.SessionLastSeq(t.Context(), scope, sid)

			if err := repo.Retry(t.Context(), scope, sid); err != nil {
				t.Fatal(err)
			}
			if r := readRow(t, pool, sid); r.status != tc.want || r.wake != nil || r.attempt != 0 {
				t.Fatalf("row = %+v, want %s with wake_at cleared and attempts reset", r, tc.want)
			}
			evs, err := repo.ListEvents(t.Context(), scope, sid, last)
			if err != nil || len(evs) != 1 || evs[0].Type != eventlog.TypeStatusChanged {
				t.Fatalf("events = %+v, %v", evs, err)
			}
			var p struct{ To, Reason string }
			if err := json.Unmarshal(evs[0].Payload, &p); err != nil {
				t.Fatal(err)
			}
			if p.To != "runnable" || p.Reason != "user_retry" {
				t.Fatalf("status_changed = %+v", p)
			}
		})
	}
}

func TestRetryIsANoOpElsewhere(t *testing.T) {
	pool := testdb.NewPool(t)
	repo := eventlog.NewRepo(pool, "fake")

	// A session that ended normally has no error to retry.
	sid, scope := parkedWith(t, pool, true, eventlog.StatusAwaitingUser)
	last, _ := repo.SessionLastSeq(t.Context(), scope, sid)
	if err := repo.Retry(t.Context(), scope, sid); err != nil {
		t.Fatal(err)
	}
	if got := eventTypes(t, repo, scope, sid, last); len(got) != 0 {
		t.Fatalf("retryable error parked as awaiting_user got events %v", got)
	}

	// A runnable session is already going.
	runnable := uuid.New()
	if _, err := repo.CreateSession(t.Context(), scope, runnable, uuid.New(), "hi", "", false); err != nil {
		t.Fatal(err)
	}
	if err := repo.Retry(t.Context(), scope, runnable); err != nil {
		t.Fatal(err)
	}
	if got := eventTypes(t, repo, scope, runnable, 2); len(got) != 0 {
		t.Fatalf("runnable session got events %v", got)
	}

	if err := repo.Retry(t.Context(), testdb.NewUser(t, pool).Scope(), sid); !errors.Is(err, eventlog.ErrNotFound) {
		t.Fatalf("other tenant: err = %v, want ErrNotFound", err)
	}
}

func TestPostMessageWakesASleepingSession(t *testing.T) {
	pool := testdb.NewPool(t)
	repo := eventlog.NewRepo(pool, "fake")
	sid, scope := sleepingSession(t, pool, time.Hour)

	if _, err := repo.PostMessage(t.Context(), scope, sid, uuid.New(), "still there?"); err != nil {
		t.Fatal(err)
	}
	if r := readRow(t, pool, sid); r.status != eventlog.StatusRunnable || r.wake != nil {
		t.Fatalf("row = %+v, want runnable with wake_at cleared", r)
	}
}

func TestCreateSessionOnAChosenModel(t *testing.T) {
	pool := testdb.NewPool(t)
	repo := eventlog.NewRepo(pool, "fake")
	scope := testdb.NewUser(t, pool).Scope()
	chosen, dflt := uuid.New(), uuid.New()
	if _, err := repo.CreateSession(t.Context(), scope, chosen, uuid.New(), "hi", "claude-opus-5-5", false); err != nil {
		t.Fatal(err)
	}
	if _, err := repo.CreateSession(t.Context(), scope, dflt, uuid.New(), "hi", "", false); err != nil {
		t.Fatal(err)
	}
	for sid, want := range map[uuid.UUID]string{chosen: "claude-opus-5-5", dflt: "fake"} {
		if got, err := repo.SessionModel(t.Context(), scope, sid); err != nil || got != want {
			t.Errorf("SessionModel = %q, %v; want %q", got, err, want)
		}
	}
}

func TestChangeModelAfterAModelErrorResumes(t *testing.T) {
	for _, code := range []string{"model_unavailable", "billing"} {
		t.Run(code, func(t *testing.T) { changeModelResumes(t, code) })
	}
}

func changeModelResumes(t *testing.T, code string) {
	pool := testdb.NewPool(t)
	repo := eventlog.NewRepo(pool, "fake")
	stopped, scope := parkedOn(t, pool, code, false, eventlog.StatusAwaitingUser)
	last, _ := repo.SessionLastSeq(t.Context(), scope, stopped)
	if err := repo.ChangeModel(t.Context(), scope, stopped, "claude-opus-5-5"); err != nil {
		t.Fatal(err)
	}
	if got := eventTypes(t, repo, scope, stopped, last); !slices.Equal(got, []string{eventlog.TypeConfigChanged, eventlog.TypeStatusChanged}) {
		t.Fatalf("events = %v", got)
	}
	evs, _ := repo.ListEvents(t.Context(), scope, stopped, last)
	var changed map[string]string
	var status struct{ To, Reason string }
	if err := json.Unmarshal(evs[0].Payload, &changed); err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(evs[1].Payload, &status); err != nil {
		t.Fatal(err)
	}
	if len(changed) != 1 || changed["model"] != "claude-opus-5-5" || status.To != "runnable" || status.Reason != "model_changed" {
		t.Fatalf("payloads = %s, %s", evs[0].Payload, evs[1].Payload)
	}
	if r := readRow(t, pool, stopped); r.status != eventlog.StatusRunnable {
		t.Fatalf("status = %s, want runnable", r.status)
	}
	if got, _ := repo.SessionModel(t.Context(), scope, stopped); got != "claude-opus-5-5" {
		t.Fatalf("SessionModel = %q", got)
	}
}

// A session that ended normally, or stopped on an error a new model doesn't
// fix, only records the change for its next turn.
func TestChangeModelElsewhereOnlyRecordsIt(t *testing.T) {
	for _, tc := range []struct {
		name      string
		retryable bool
		code      string
	}{{"after a reply", true, "provider_down"}, {"after key_invalid", false, "key_invalid"}, {"after bug", false, "bug"}} {
		t.Run(tc.name, func(t *testing.T) {
			pool := testdb.NewPool(t)
			repo := eventlog.NewRepo(pool, "fake")
			sid, scope := parkedOn(t, pool, tc.code, tc.retryable, eventlog.StatusAwaitingUser)
			last, _ := repo.SessionLastSeq(t.Context(), scope, sid)
			if err := repo.ChangeModel(t.Context(), scope, sid, "claude-opus-5-5"); err != nil {
				t.Fatal(err)
			}
			if got := eventTypes(t, repo, scope, sid, last); !slices.Equal(got, []string{eventlog.TypeConfigChanged}) {
				t.Fatalf("events = %v", got)
			}
		})
	}
}

func TestChangeModelChecksTenancy(t *testing.T) {
	pool := testdb.NewPool(t)
	repo := eventlog.NewRepo(pool, "fake")
	ended, _ := parkedWith(t, pool, true, eventlog.StatusAwaitingUser)

	if err := repo.ChangeModel(t.Context(), testdb.NewUser(t, pool).Scope(), ended, "x"); !errors.Is(err, eventlog.ErrNotFound) {
		t.Fatalf("other tenant: err = %v, want ErrNotFound", err)
	}
	if _, err := repo.SessionModel(t.Context(), testdb.NewUser(t, pool).Scope(), ended); !errors.Is(err, eventlog.ErrNotFound) {
		t.Fatalf("other tenant SessionModel: err = %v, want ErrNotFound", err)
	}
}
