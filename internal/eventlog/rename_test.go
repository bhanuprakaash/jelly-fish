package eventlog_test

import (
	"encoding/json"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/bhanuprakaash/jelly-fish/internal/eventlog"
	"github.com/bhanuprakaash/jelly-fish/internal/testdb"
)

func TestCleanTitle(t *testing.T) {
	long := ""
	for range 120 {
		long += "é"
	}
	tests := []struct{ name, raw, want string }{
		{"trims", "  Trip planning \n", "Trip planning"},
		{"strips straight quotes", `"Trip planning"`, "Trip planning"},
		{"strips curly quotes", "“Trip planning”", "Trip planning"},
		{"strips quotes around padded text", ` " Trip planning " `, "Trip planning"},
		{"keeps inner quotes", `Say "hi" now`, `Say "hi" now`},
		{"empty stays empty", `  ""  `, ""},
		{"caps at 100 runes", long, long[:200]},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := eventlog.CleanTitle(tt.raw); got != tt.want {
				t.Fatalf("CleanTitle(%q) = %q, want %q", tt.raw, got, tt.want)
			}
		})
	}
}

func newChat(t *testing.T) (*pgxpool.Pool, *eventlog.Repo, eventlog.TenantScope, uuid.UUID) {
	t.Helper()
	pool := testdb.NewPool(t)
	repo := eventlog.NewRepo(pool, "fake")
	scope := testdb.NewUser(t, pool).Scope()
	return pool, repo, scope, createChat(t, repo, scope)
}

func createChat(t *testing.T, repo *eventlog.Repo, scope eventlog.TenantScope) uuid.UUID {
	t.Helper()
	sid := uuid.New()
	if _, err := repo.CreateSession(t.Context(), scope, sid, uuid.New(), "hi", "", false); err != nil {
		t.Fatal(err)
	}
	return sid
}

func sessionTitle(t *testing.T, pool *pgxpool.Pool, sid uuid.UUID) string {
	t.Helper()
	var title *string
	if err := pool.QueryRow(t.Context(), `SELECT title FROM sessions WHERE id = $1`, sid).Scan(&title); err != nil {
		t.Fatal(err)
	}
	if title == nil {
		return ""
	}
	return *title
}

func renamedBy(t *testing.T, store *eventlog.Store, sid uuid.UUID) (seqs []int64, by []string) {
	t.Helper()
	evs, err := store.Load(t.Context(), sid)
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range evs {
		if e.Type != eventlog.TypeSessionRenamed {
			continue
		}
		var p eventlog.SessionRenamed
		if err := json.Unmarshal(e.Payload, &p); err != nil {
			t.Fatal(err)
		}
		seqs = append(seqs, e.Seq)
		by = append(by, p.By)
	}
	return seqs, by
}

func claim(t *testing.T, store *eventlog.Store) eventlog.Claim {
	t.Helper()
	c, ok, err := store.Claim(t.Context(), "w1", 30*time.Second)
	if err != nil || !ok {
		t.Fatalf("Claim: ok=%v err=%v", ok, err)
	}
	return c
}

func autoTitle(title string) []eventlog.NewEvent {
	return []eventlog.NewEvent{
		{Type: eventlog.TypeUsageRecorded, Actor: "worker:w1", Payload: eventlog.UsageRecorded{Kind: eventlog.KindLLM, Provider: "fake", Quantity: 7, Unit: "input_tokens"}},
		{Type: eventlog.TypeSessionRenamed, Actor: "worker:w1", Payload: eventlog.SessionRenamed{Title: title, By: eventlog.RenamedByAuto}},
	}
}

func TestRenameWritesEventAndProjectsTitleInOneTx(t *testing.T) {
	pool, repo, scope, sid := newChat(t)
	if err := repo.Rename(t.Context(), scope, sid, "Trip plan"); err != nil {
		t.Fatal(err)
	}
	if got := sessionTitle(t, pool, sid); got != "Trip plan" {
		t.Fatalf("title = %q", got)
	}
	evs, err := repo.ListEvents(t.Context(), scope, sid, 2)
	if err != nil {
		t.Fatal(err)
	}
	if len(evs) != 1 || evs[0].Type != eventlog.TypeSessionRenamed || evs[0].Actor != "user:"+scope.UserID.String() {
		t.Fatalf("events = %+v", evs)
	}
	var p eventlog.SessionRenamed
	if err := json.Unmarshal(evs[0].Payload, &p); err != nil || p != (eventlog.SessionRenamed{Title: "Trip plan", By: eventlog.RenamedByUser}) {
		t.Fatalf("payload = %s (%v)", evs[0].Payload, err)
	}
	last, err := repo.SessionLastSeq(t.Context(), scope, sid)
	if err != nil || last != evs[0].Seq {
		t.Fatalf("last_seq = %d (%v), want %d", last, err, evs[0].Seq)
	}
}

func TestRenameChildIsRefused(t *testing.T) {
	pool, repo, scope, parent := newChat(t)
	child := insertSessionRow(t, pool, scope, eventlog.TriggerUserMessage, &parent)
	err := repo.Rename(t.Context(), scope, child, "Nope")
	if !errors.Is(err, eventlog.ErrChildSession) {
		t.Fatalf("err = %v, want ErrChildSession", err)
	}
	if got := sessionTitle(t, pool, child); got != "" {
		t.Fatalf("title = %q", got)
	}
}

func TestRenameOtherTenantIsNotFound(t *testing.T) {
	pool, repo, _, sid := newChat(t)
	other := testdb.NewUser(t, pool).Scope()
	if err := repo.Rename(t.Context(), other, sid, "Mine now"); !errors.Is(err, eventlog.ErrNotFound) {
		t.Fatalf("err = %v, want ErrNotFound", err)
	}
	if err := repo.Rename(t.Context(), other, uuid.New(), "x"); !errors.Is(err, eventlog.ErrNotFound) {
		t.Fatalf("unknown id err = %v, want ErrNotFound", err)
	}
}

func TestAutoRenameAfterUserRenameIsDroppedButUsageKept(t *testing.T) {
	pool, repo, scope, sid := newChat(t)
	store := eventlog.NewStore(pool)
	c := claim(t, store)
	if err := repo.Rename(t.Context(), scope, sid, "Mine"); err != nil {
		t.Fatal(err)
	}

	seqs, err := store.AppendFenced(t.Context(), sid, c.Fence, autoTitle("Theirs"), nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(seqs) != 1 {
		t.Fatalf("seqs = %v, want just the usage event", seqs)
	}
	if got := sessionTitle(t, pool, sid); got != "Mine" {
		t.Fatalf("title = %q", got)
	}
	if _, by := renamedBy(t, store, sid); len(by) != 1 || by[0] != eventlog.RenamedByUser {
		t.Fatalf("renamed by = %v", by)
	}
	var tokens int64
	if err := pool.QueryRow(t.Context(), `SELECT tokens_used FROM sessions WHERE id = $1`, sid).Scan(&tokens); err != nil || tokens != 7 {
		t.Fatalf("tokens_used = %d (%v), want 7", tokens, err)
	}
	last, err := repo.SessionLastSeq(t.Context(), scope, sid)
	if err != nil || last != seqs[0] {
		t.Fatalf("last_seq = %d (%v), want %d", last, err, seqs[0])
	}
}

func TestAutoRenameBeforeUserRenameIsSuperseded(t *testing.T) {
	pool, repo, scope, sid := newChat(t)
	store := eventlog.NewStore(pool)
	c := claim(t, store)
	if _, err := store.AppendFenced(t.Context(), sid, c.Fence, autoTitle("Auto"), nil); err != nil {
		t.Fatal(err)
	}
	if got := sessionTitle(t, pool, sid); got != "Auto" {
		t.Fatalf("title = %q", got)
	}
	if err := repo.Rename(t.Context(), scope, sid, "Mine"); err != nil {
		t.Fatal(err)
	}
	if got := sessionTitle(t, pool, sid); got != "Mine" {
		t.Fatalf("title = %q", got)
	}
}

func TestRenameRaceNeverWritesAutoAfterUser(t *testing.T) {
	pool := testdb.NewPool(t)
	repo := eventlog.NewRepo(pool, "fake")
	store := eventlog.NewStore(pool)
	scope := testdb.NewUser(t, pool).Scope()
	for i := range 30 {
		sid := createChat(t, repo, scope)
		c := claim(t, store)

		start := make(chan struct{})
		var wg sync.WaitGroup
		errs := make([]error, 2)
		wg.Go(func() {
			<-start
			_, errs[0] = store.AppendFenced(t.Context(), sid, c.Fence, autoTitle("Auto"), nil)
		})
		wg.Go(func() {
			<-start
			errs[1] = repo.Rename(t.Context(), scope, sid, "Mine")
		})
		close(start)
		wg.Wait()
		if errs[0] != nil || errs[1] != nil {
			t.Fatalf("iteration %d: errs = %v", i, errs)
		}

		seqs, by := renamedBy(t, store, sid)
		seenUser := false
		for j, who := range by {
			if who == eventlog.RenamedByAuto && seenUser {
				t.Fatalf("iteration %d: auto at seq %d after user: %v %v", i, seqs[j], seqs, by)
			}
			seenUser = seenUser || who == eventlog.RenamedByUser
		}
		if len(by) == 0 || by[len(by)-1] != eventlog.RenamedByUser {
			t.Fatalf("iteration %d: renamed by = %v, want the user's last", i, by)
		}
		if got := sessionTitle(t, pool, sid); got != "Mine" {
			t.Fatalf("iteration %d: title = %q", i, got)
		}
	}
}

func TestFencedRenameFromOldEpochIsRejected(t *testing.T) {
	pool, _, _, sid := newChat(t)
	store := eventlog.NewStore(pool)
	old := claim(t, store)
	if _, err := pool.Exec(t.Context(), `UPDATE sessions SET lease_expires_at = now() - interval '1 second' WHERE id = $1`, sid); err != nil {
		t.Fatal(err)
	}
	if c, ok, err := store.Claim(t.Context(), "w2", 30*time.Second); err != nil || !ok || c.Fence.Epoch != old.Fence.Epoch+1 {
		t.Fatalf("reclaim: %+v ok=%v err=%v", c, ok, err)
	}

	_, err := store.AppendFenced(t.Context(), sid, old.Fence, autoTitle("Zombie"), nil)
	if !errors.Is(err, eventlog.ErrLeaseLost) {
		t.Fatalf("err = %v, want ErrLeaseLost", err)
	}
	if got := sessionTitle(t, pool, sid); got != "" {
		t.Fatalf("title = %q", got)
	}
	if _, by := renamedBy(t, store, sid); len(by) != 0 {
		t.Fatalf("renamed by = %v", by)
	}
}
