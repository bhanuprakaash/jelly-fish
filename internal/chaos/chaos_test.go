//go:build chaos

// Package chaos kills and freezes real worker processes to prove a session
// survives them (docs/design/event-log.md §9). It runs only under the chaos
// build tag: `make chaos`.
package chaos

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/bhanuprakaash/jelly-fish/internal/eventlog"
	"github.com/bhanuprakaash/jelly-fish/internal/testdb"
)

const (
	leaseTTL  = 2 * time.Second
	heartbeat = 500 * time.Millisecond
	// A worker polls every 3 s, so a rescue takes up to TTL + poll.
	rescueWithin = 30 * time.Second
)

type syncBuf struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (s *syncBuf) Write(p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.Write(p)
}

func (s *syncBuf) String() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.String()
}

type proc struct {
	cmd *exec.Cmd
	log *syncBuf
}

func (p *proc) kill(t *testing.T) {
	t.Helper()
	if err := p.cmd.Process.Signal(syscall.SIGKILL); err != nil {
		t.Fatalf("kill -9: %v", err)
	}
}

func (p *proc) signal(t *testing.T, sig syscall.Signal) {
	t.Helper()
	if err := p.cmd.Process.Signal(sig); err != nil {
		t.Fatalf("signal %v: %v", sig, err)
	}
}

// build compiles the dev binary once per test; workers must run the real
// binary so that kill -9 hits the worker itself.
func build(t *testing.T) string {
	t.Helper()
	bin := filepath.Join(t.TempDir(), "jelly-fish")
	out, err := exec.CommandContext(t.Context(), "go", "build", "-tags", "dev", "-o", bin, "../../cmd/jelly-fish").CombinedOutput()
	if err != nil {
		t.Fatalf("build jelly-fish: %v\n%s", err, out)
	}
	return bin
}

func spawn(t *testing.T, bin string, pool *pgxpool.Pool) *proc {
	t.Helper()
	log := &syncBuf{}
	cmd := exec.CommandContext(context.WithoutCancel(t.Context()), bin, "worker")
	cmd.Env = append(os.Environ(),
		"DATABASE_URL="+pool.Config().ConnString(),
		"HEALTH_ADDR=127.0.0.1:0",
		"JF_LEASE_TTL="+leaseTTL.String(),
		"JF_HEARTBEAT="+heartbeat.String(),
	)
	cmd.Stdout, cmd.Stderr = log, log
	if err := cmd.Start(); err != nil {
		t.Fatalf("start worker: %v", err)
	}
	p := &proc{cmd: cmd, log: log}
	t.Cleanup(func() {
		_ = cmd.Process.Signal(syscall.SIGCONT)
		_ = cmd.Process.Kill()
		_ = cmd.Wait()
		if t.Failed() {
			t.Logf("worker %d log:\n%s", cmd.Process.Pid, log)
		}
	})
	return p
}

func newSession(t *testing.T, pool *pgxpool.Pool, text string) uuid.UUID {
	t.Helper()
	sid := uuid.New()
	if _, err := eventlog.NewRepo(pool).CreateSession(t.Context(), eventlog.DevScope(), sid, uuid.New(), text); err != nil {
		t.Fatal(err)
	}
	return sid
}

func waitFor(t *testing.T, what string, within time.Duration, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(within)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for %s", what)
}

func count(t *testing.T, pool *pgxpool.Pool, query string, args ...any) int {
	t.Helper()
	var n int
	if err := pool.QueryRow(t.Context(), query, args...).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

func hasEvent(t *testing.T, pool *pgxpool.Pool, sid uuid.UUID, typ string) bool {
	t.Helper()
	return count(t, pool, `SELECT count(*) FROM events WHERE session_id = $1 AND type = $2`, sid, typ) > 0
}

func leaseEpoch(t *testing.T, pool *pgxpool.Pool, sid uuid.UUID) int {
	t.Helper()
	return count(t, pool, `SELECT lease_epoch FROM sessions WHERE id = $1`, sid)
}

func waitSettled(t *testing.T, pool *pgxpool.Pool, sid uuid.UUID, status string) {
	t.Helper()
	waitFor(t, "status "+status, rescueWithin, func() bool {
		return count(t, pool, `SELECT count(*) FROM sessions WHERE id = $1 AND status = $2`, sid, status) == 1
	})
}

func types(t *testing.T, pool *pgxpool.Pool, sid uuid.UUID) []string {
	t.Helper()
	evs, err := eventlog.NewStore(pool).Load(t.Context(), sid)
	if err != nil {
		t.Fatal(err)
	}
	out := make([]string, len(evs))
	for i, e := range evs {
		out[i] = e.Type
	}
	return out
}

func TestKillDuringStreamingResumes(t *testing.T) {
	pool := testdb.NewPool(t)
	bin := build(t)
	sid := newSession(t, pool, "/slow 6s hello")

	a := spawn(t, bin, pool)
	waitFor(t, "turn.started", rescueWithin, func() bool { return hasEvent(t, pool, sid, eventlog.TypeTurnStarted) })
	time.Sleep(time.Second) // mid-stream
	a.kill(t)

	spawn(t, bin, pool)
	waitSettled(t, pool, sid, eventlog.StatusAwaitingUser)

	var turnEvents []string
	for _, typ := range types(t, pool, sid) {
		if slices.Contains([]string{eventlog.TypeTurnStarted, eventlog.TypeTurnInterrupted, eventlog.TypeLLMResponse}, typ) {
			turnEvents = append(turnEvents, typ)
		}
	}
	want := []string{eventlog.TypeTurnStarted, eventlog.TypeTurnInterrupted, eventlog.TypeTurnStarted, eventlog.TypeLLMResponse}
	if !slices.Equal(turnEvents, want) {
		t.Fatalf("turn events = %v, want %v", turnEvents, want)
	}

	var payload []byte
	err := pool.QueryRow(t.Context(), `SELECT payload FROM events WHERE session_id = $1 AND type = $2`, sid, eventlog.TypeTurnInterrupted).Scan(&payload)
	if err != nil {
		t.Fatal(err)
	}
	var p struct {
		Reason string `json:"reason"`
	}
	if err := json.Unmarshal(payload, &p); err != nil || p.Reason != "worker_lost" {
		t.Fatalf("turn.interrupted payload = %s (err %v), want reason worker_lost", payload, err)
	}
}

func TestZombieWorkerIsFencedOut(t *testing.T) {
	pool := testdb.NewPool(t)
	bin := build(t)
	sid := newSession(t, pool, "/slow 6s hello")

	zombie := spawn(t, bin, pool)
	waitFor(t, "turn.started", rescueWithin, func() bool { return hasEvent(t, pool, sid, eventlog.TypeTurnStarted) })
	time.Sleep(time.Second) // mid-stream, not inside an append
	zombie.signal(t, syscall.SIGSTOP)

	spawn(t, bin, pool)
	waitFor(t, "second worker claims", rescueWithin, func() bool { return leaseEpoch(t, pool, sid) == 2 })
	zombie.signal(t, syscall.SIGCONT)

	waitFor(t, "zombie to notice", rescueWithin, func() bool { return strings.Contains(zombie.log.String(), "lease lost") })
	waitSettled(t, pool, sid, eventlog.StatusAwaitingUser)

	firstNew := count(t, pool, `SELECT min(seq) FROM events WHERE session_id = $1 AND lease_epoch = 2`, sid)
	late := count(t, pool, `SELECT count(*) FROM events WHERE session_id = $1 AND lease_epoch = 1 AND seq > $2`, sid, firstNew)
	if late != 0 {
		t.Fatalf("%d events from the old lease_epoch after the new claim's first event (seq %d)", late, firstNew)
	}
	if got := count(t, pool, `SELECT turns FROM sessions WHERE id = $1`, sid); got != 1 {
		t.Fatalf("turns = %d, want 1", got)
	}
}

func TestCrashLoopFailsOnSixthClaim(t *testing.T) {
	pool := testdb.NewPool(t)
	bin := build(t)

	// Every claim dies before its first append: the append blocks on this
	// trigger until the test kills the worker.
	_, err := pool.Exec(t.Context(), `
		CREATE FUNCTION block_turn() RETURNS trigger LANGUAGE plpgsql AS $$
		BEGIN PERFORM pg_sleep(600); RETURN NEW; END $$;
		CREATE TRIGGER block_turn BEFORE INSERT ON events
		FOR EACH ROW WHEN (NEW.type = 'turn.started') EXECUTE FUNCTION block_turn()`)
	if err != nil {
		t.Fatal(err)
	}
	sid := newSession(t, pool, "hello")

	for claim := 1; claim <= 5; claim++ {
		w := spawn(t, bin, pool)
		waitFor(t, "claim", rescueWithin, func() bool { return leaseEpoch(t, pool, sid) == claim })
		waitFor(t, "append to block", rescueWithin, func() bool {
			return count(t, pool, `SELECT count(*) FROM pg_stat_activity WHERE datname = current_database() AND wait_event = 'PgSleep'`) == 1
		})
		w.kill(t)
		// The dead Worker's backend would sit in pg_sleep, holding the row lock.
		if _, err := pool.Exec(t.Context(), `SELECT pg_terminate_backend(pid) FROM pg_stat_activity WHERE datname = current_database() AND wait_event = 'PgSleep'`); err != nil {
			t.Fatal(err)
		}
	}

	spawn(t, bin, pool)
	waitSettled(t, pool, sid, eventlog.StatusFailed)

	got := types(t, pool, sid)
	want := []string{eventlog.TypeStatusChanged, eventlog.TypeSessionError, eventlog.TypeStatusChanged}
	if tail := got[len(got)-3:]; !slices.Equal(tail, want) || slices.Contains(got, eventlog.TypeTurnStarted) {
		t.Fatalf("events = %v, want no turn.started and a tail of %v", got, want)
	}
	if got := count(t, pool, `SELECT count(*) FROM events WHERE session_id = $1 AND type = 'session.error' AND payload->>'code' = 'crash_loop'`, sid); got != 1 {
		t.Fatalf("crash_loop errors = %d, want 1", got)
	}
}
