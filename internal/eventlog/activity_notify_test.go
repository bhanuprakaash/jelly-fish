package eventlog_test

import (
	"context"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/bhanuprakaash/jelly-fish/internal/eventlog"
	"github.com/bhanuprakaash/jelly-fish/internal/testdb"
)

// activityListener LISTENs on jf_activity over its own connection. Notifications
// arrive in commit order, so a sentinel sent after an operation marks where
// that operation's notifications end.
type activityListener struct {
	t    *testing.T
	pool *pgxpool.Pool
	conn *pgxpool.Conn
}

func listenActivity(t *testing.T, pool *pgxpool.Pool) *activityListener {
	t.Helper()
	conn, err := pool.Acquire(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(conn.Release)
	if _, err := conn.Exec(t.Context(), "LISTEN jf_activity"); err != nil {
		t.Fatal(err)
	}
	return &activityListener{t: t, pool: pool, conn: conn}
}

// drain returns every jf_activity payload committed so far, in order.
func (l *activityListener) drain() []string {
	l.t.Helper()
	ctx, cancel := context.WithTimeout(l.t.Context(), 5*time.Second)
	defer cancel()
	if _, err := l.pool.Exec(ctx, `SELECT pg_notify('jf_activity', 'sentinel')`); err != nil {
		l.t.Fatal(err)
	}
	var got []string
	for {
		n, err := l.conn.Conn().WaitForNotification(ctx)
		if err != nil {
			l.t.Fatalf("wait for sentinel: %v", err)
		}
		if n.Payload == "sentinel" {
			return got
		}
		got = append(got, n.Payload)
	}
}

type activityEnv struct {
	pool  *pgxpool.Pool
	repo  *eventlog.Repo
	store *eventlog.Store
	l     *activityListener
}

// newSession creates a runnable session for a fresh User.
func (e *activityEnv) newSession(t *testing.T) (eventlog.TenantScope, uuid.UUID) {
	t.Helper()
	scope := testdb.NewUser(t, e.pool).Scope()
	sid := uuid.New()
	if _, err := e.repo.CreateSession(t.Context(), scope, sid, uuid.New(), "hi", "", false); err != nil {
		t.Fatal(err)
	}
	return scope, sid
}

// claimed is newSession, claimed by a Worker.
func (e *activityEnv) claimed(t *testing.T) (eventlog.TenantScope, uuid.UUID, eventlog.Fence) {
	t.Helper()
	scope, sid := e.newSession(t)
	c, ok, err := e.store.Claim(t.Context(), "w1", 30*time.Second)
	if err != nil || !ok || c.SessionID != sid {
		t.Fatalf("Claim: ok=%v err=%v", ok, err)
	}
	return scope, sid, c.Fence
}

func TestActivityNotify(t *testing.T) {
	turn := []eventlog.NewEvent{{Type: eventlog.TypeTurnStarted, Actor: "worker:w1", Payload: map[string]any{}}}

	// Each case does its setup, drains it, then runs the one operation under
	// test and returns the session that operation should have notified. Cases
	// get their own database: Claim takes any runnable session.
	tests := []struct {
		name string
		want int
		run  func(t *testing.T, e *activityEnv) (eventlog.TenantScope, uuid.UUID)
	}{
		{"create session", 1, func(t *testing.T, e *activityEnv) (eventlog.TenantScope, uuid.UUID) {
			scope := testdb.NewUser(t, e.pool).Scope()
			sid := uuid.New()
			e.l.drain()
			if _, err := e.repo.CreateSession(t.Context(), scope, sid, uuid.New(), "hi", "", false); err != nil {
				t.Fatal(err)
			}
			return scope, sid
		}},
		{"repeated create session", 0, func(t *testing.T, e *activityEnv) (eventlog.TenantScope, uuid.UUID) {
			scope, sid := e.newSession(t)
			e.l.drain()
			if _, err := e.repo.CreateSession(t.Context(), scope, sid, uuid.New(), "hi", "", false); err != nil {
				t.Fatal(err)
			}
			return scope, sid
		}},
		{"claim", 1, func(t *testing.T, e *activityEnv) (eventlog.TenantScope, uuid.UUID) {
			scope, sid := e.newSession(t)
			e.l.drain()
			if _, ok, err := e.store.Claim(t.Context(), "w1", 30*time.Second); err != nil || !ok {
				t.Fatalf("Claim: ok=%v err=%v", ok, err)
			}
			return scope, sid
		}},
		{"claim of a sleeping session", 1, func(t *testing.T, e *activityEnv) (eventlog.TenantScope, uuid.UUID) {
			sid, scope := sleepingSession(t, e.pool, time.Minute)
			if _, err := e.pool.Exec(t.Context(), `UPDATE sessions SET wake_at = now() - interval '1 second' WHERE id = $1`, sid); err != nil {
				t.Fatal(err)
			}
			e.l.drain()
			if _, ok, err := e.store.Claim(t.Context(), "w2", 30*time.Second); err != nil || !ok {
				t.Fatalf("Claim: ok=%v err=%v", ok, err)
			}
			return scope, sid
		}},
		{"append with a status change", 1, func(t *testing.T, e *activityEnv) (eventlog.TenantScope, uuid.UUID) {
			scope, sid := e.newSession(t)
			e.l.drain()
			if _, err := e.store.Append(t.Context(), sid, nil, nil, &eventlog.StatusChange{To: eventlog.StatusAwaitingUser, Reason: "end_turn"}); err != nil {
				t.Fatal(err)
			}
			return scope, sid
		}},
		{"fenced park", 1, func(t *testing.T, e *activityEnv) (eventlog.TenantScope, uuid.UUID) {
			scope, sid, f := e.claimed(t)
			e.l.drain()
			if _, err := e.store.AppendFenced(t.Context(), sid, f, turn, &eventlog.StatusChange{To: eventlog.StatusAwaitingUser, Reason: "end_turn"}); err != nil {
				t.Fatal(err)
			}
			return scope, sid
		}},
		{"post message resuming the session", 1, func(t *testing.T, e *activityEnv) (eventlog.TenantScope, uuid.UUID) {
			scope, sid := e.newSession(t)
			if _, err := e.store.Append(t.Context(), sid, nil, nil, &eventlog.StatusChange{To: eventlog.StatusAwaitingUser}); err != nil {
				t.Fatal(err)
			}
			e.l.drain()
			if _, err := e.repo.PostMessage(t.Context(), scope, sid, uuid.New(), "more"); err != nil {
				t.Fatal(err)
			}
			return scope, sid
		}},
		{"steering a running session", 0, func(t *testing.T, e *activityEnv) (eventlog.TenantScope, uuid.UUID) {
			scope, sid, _ := e.claimed(t)
			e.l.drain()
			if _, err := e.repo.PostMessage(t.Context(), scope, sid, uuid.New(), "steer"); err != nil {
				t.Fatal(err)
			}
			return scope, sid
		}},
		{"interrupting a running session", 0, func(t *testing.T, e *activityEnv) (eventlog.TenantScope, uuid.UUID) {
			scope, sid, _ := e.claimed(t)
			e.l.drain()
			if err := e.repo.Interrupt(t.Context(), scope, sid); err != nil {
				t.Fatal(err)
			}
			return scope, sid
		}},
		{"fenced events without a status change", 0, func(t *testing.T, e *activityEnv) (eventlog.TenantScope, uuid.UUID) {
			scope, sid, f := e.claimed(t)
			e.l.drain()
			if _, err := e.store.AppendFenced(t.Context(), sid, f, turn, nil); err != nil {
				t.Fatal(err)
			}
			return scope, sid
		}},
		{"conditional status change that does not apply", 0, func(t *testing.T, e *activityEnv) (eventlog.TenantScope, uuid.UUID) {
			scope, sid := e.newSession(t)
			e.l.drain()
			if _, err := e.store.Append(t.Context(), sid, nil, turn, &eventlog.StatusChange{
				To: eventlog.StatusRunnable, From: []string{eventlog.StatusAwaitingUser},
			}); err != nil {
				t.Fatal(err)
			}
			return scope, sid
		}},
		{"heartbeat and release", 0, func(t *testing.T, e *activityEnv) (eventlog.TenantScope, uuid.UUID) {
			scope, sid, f := e.claimed(t)
			e.l.drain()
			if _, err := e.store.Heartbeat(t.Context(), sid, f, 30*time.Second); err != nil {
				t.Fatal(err)
			}
			if err := e.store.Release(t.Context(), sid, f); err != nil {
				t.Fatal(err)
			}
			return scope, sid
		}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			pool := testdb.NewPool(t)
			e := &activityEnv{pool: pool, repo: eventlog.NewRepo(pool, "fake"), store: eventlog.NewStore(pool)}
			e.l = listenActivity(t, pool)
			scope, sid := tt.run(t, e)
			got := e.l.drain()
			if len(got) != tt.want {
				t.Fatalf("jf_activity payloads = %v, want %d", got, tt.want)
			}
			for _, p := range got {
				if want := scope.UserID.String() + ":" + sid.String(); p != want {
					t.Errorf("payload = %q, want %q", p, want)
				}
			}
		})
	}
}

func TestActivityNotify_RolledBackTxSendsNone(t *testing.T) {
	pool := testdb.NewPool(t)
	e := &activityEnv{pool: pool, repo: eventlog.NewRepo(pool, "fake"), store: eventlog.NewStore(pool)}
	l := listenActivity(t, pool)
	_, sid := e.newSession(t)

	// A deferred trigger fails the commit after appendTx has issued its NOTIFYs.
	// Each test has its own database, so nothing else sees it.
	for _, stmt := range []string{
		`CREATE FUNCTION fail_commit() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN RAISE EXCEPTION 'commit refused'; END $$`,
		`CREATE CONSTRAINT TRIGGER fail_commit AFTER INSERT ON events DEFERRABLE INITIALLY DEFERRED FOR EACH ROW EXECUTE FUNCTION fail_commit()`,
	} {
		if _, err := pool.Exec(t.Context(), stmt); err != nil {
			t.Fatal(err)
		}
	}
	l.drain()

	if _, err := e.store.Append(t.Context(), sid, nil, nil, &eventlog.StatusChange{To: eventlog.StatusAwaitingUser}); err == nil {
		t.Fatal("Append succeeded, want the commit to fail")
	}
	if got := l.drain(); len(got) != 0 {
		t.Errorf("jf_activity payloads after a rolled-back tx = %v, want none", got)
	}
	var status string
	if err := pool.QueryRow(t.Context(), `SELECT status FROM sessions WHERE id = $1`, sid).Scan(&status); err != nil {
		t.Fatal(err)
	}
	if status != eventlog.StatusRunnable {
		t.Errorf("status = %q, want runnable", status)
	}
}
