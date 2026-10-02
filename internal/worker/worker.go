// Package worker claims runnable sessions and drives them: load, fold,
// decide, exec (docs/design/event-log.md §5.3).
package worker

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"sync"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/bhanuprakaash/jelly-fish/internal/eventlog"
	"github.com/bhanuprakaash/jelly-fish/internal/msg"
	"github.com/bhanuprakaash/jelly-fish/internal/provider"
	"github.com/bhanuprakaash/jelly-fish/internal/stream"
)

const (
	maxSessions     = 50
	pollInterval    = 3 * time.Second
	runnableChannel = "jf_runnable"
	// maxRecoveryAttempts is how many claims in a row may make no fenced
	// append before the session is failed (event-log.md §5.6).
	maxRecoveryAttempts = 5
)

// emptyToolsHash is the sha256 of the empty tool list, the only one this
// slice has.
const emptyToolsHash = "4f53cda18c2baa0c0354bb5f9a3ecbe5ed12ab4d8e11ba873c2f11161202b945"

// DeltaPublisher sends a turn's ephemeral text deltas to the Session streams
// watching it.
type DeltaPublisher interface {
	Publish(ctx context.Context, d stream.Delta) error
}

var _ DeltaPublisher = (*stream.PGDeltaBus)(nil)

// Lease sets how long a claimed session stays leased without a heartbeat,
// and how often the Worker renews it.
type Lease struct {
	TTL       time.Duration
	Heartbeat time.Duration
}

// Worker drives sessions to completion.
type Worker struct {
	pool     *pgxpool.Pool
	store    *eventlog.Store
	gateway  Gateway
	deltas   DeltaPublisher
	lease    Lease
	upcast   eventlog.Upcasters
	released released
	id       string
	logger   *slog.Logger
	slots    chan struct{}
}

// New builds a Worker that runs each turn on the Provider gw picks, streams
// reply text to deltas and leases sessions as lease says. It reads stored
// events through upcast, and releases any session holding an event it cannot
// read.
func New(pool *pgxpool.Pool, gw Gateway, deltas DeltaPublisher, lease Lease, upcast eventlog.Upcasters, logger *slog.Logger) *Worker {
	host, err := os.Hostname()
	if err != nil {
		host = "unknown"
	}
	return &Worker{
		pool:    pool,
		store:   eventlog.NewStore(pool),
		gateway: gw,
		deltas:  deltas,
		lease:   lease,
		upcast:  upcast,
		id:      fmt.Sprintf("%s:%d", host, os.Getpid()),
		logger:  logger,
		slots:   make(chan struct{}, maxSessions),
	}
}

// Run claims and drives sessions until ctx is cancelled (SIGTERM), then waits
// for in-flight sessions to stop. Their Leases expire and another Worker
// rescues them.
func (w *Worker) Run(ctx context.Context) {
	var wg sync.WaitGroup
	defer wg.Wait()

	wake := make(chan struct{}, 1)
	wg.Go(func() { w.listen(ctx, wake) })

	for {
		w.claimAll(ctx, &wg)
		select {
		case <-wake:
		case <-time.After(pollInterval):
		case <-ctx.Done():
			w.logger.Info("worker stopping")
			return
		}
	}
}

func (w *Worker) claimAll(ctx context.Context, wg *sync.WaitGroup) {
	for {
		select {
		case w.slots <- struct{}{}:
		default:
			return
		}
		c, ok, err := w.store.Claim(ctx, w.id, w.lease.TTL, w.released.ids()...)
		if err != nil || !ok {
			<-w.slots
			if err != nil && ctx.Err() == nil {
				w.logger.Error("claim session", "error", err)
			}
			return
		}
		w.logger.Info("session claimed", "session_id", c.SessionID, "epoch", c.Fence.Epoch)
		wg.Go(func() {
			defer func() { <-w.slots }()
			w.drive(ctx, c)
		})
	}
}

// listen wakes the claim loop on every jf_runnable NOTIFY, reconnecting when
// the connection drops. The poll covers anything missed meanwhile.
func (w *Worker) listen(ctx context.Context, wake chan<- struct{}) {
	for ctx.Err() == nil {
		if err := w.listenOnce(ctx, wake); err != nil && ctx.Err() == nil {
			w.logger.Error("listen", "error", err)
		}
		select {
		case <-time.After(time.Second):
		case <-ctx.Done():
		}
	}
}

func (w *Worker) listenOnce(ctx context.Context, wake chan<- struct{}) error {
	conn, err := w.pool.Acquire(ctx)
	if err != nil {
		return fmt.Errorf("acquire conn: %w", err)
	}
	defer conn.Release()
	if _, err := conn.Exec(ctx, "LISTEN "+runnableChannel); err != nil {
		return fmt.Errorf("listen: %w", err)
	}
	for {
		if _, err := conn.Conn().WaitForNotification(ctx); err != nil {
			return fmt.Errorf("wait for notification: %w", err)
		}
		select {
		case wake <- struct{}{}:
		default:
		}
	}
}

func (w *Worker) drive(parent context.Context, c eventlog.Claim) {
	ctx, cancel := context.WithCancel(parent)
	var hb sync.WaitGroup
	defer hb.Wait()
	defer cancel()
	hb.Go(func() { w.heartbeat(ctx, cancel, c) })

	logger := w.logger.With("session_id", c.SessionID)
	if c.RecoveryAttempts > maxRecoveryAttempts {
		w.fail(ctx, logger, c, errCrashLoop)
		return
	}
	for {
		evs, err := w.store.Load(ctx, c.SessionID)
		if err != nil {
			w.stop(ctx, logger, c, err)
			return
		}
		st, err := w.fold(evs)
		switch {
		case isUnreadable(err):
			w.release(ctx, logger, c, err)
			return
		case err != nil:
			w.fail(ctx, logger, c, err)
			return
		}
		step := Decide(st)

		f := c.Fence
		if step.Kind.Parks() {
			f.ExpectSeq = &st.LastSeq
		}
		err = w.exec(ctx, c, f, st, step)
		switch {
		case errors.Is(err, eventlog.ErrStale):
			continue
		case err != nil:
			w.stop(ctx, logger, c, err)
			return
		case step.Kind.Parks():
			logger.Info("session parked", "status", eventlog.StatusAwaitingUser)
			return
		}
	}
}

func (w *Worker) fold(evs []eventlog.Event) (State, error) {
	evs, err := w.upcast.Events(evs)
	if err != nil {
		return State{}, err
	}
	return Fold(evs)
}

// isUnreadable reports whether err means a newer binary wrote something this
// one cannot read, which is not the session's fault (event-log.md §5.19).
func isUnreadable(err error) bool {
	return errors.Is(err, eventlog.ErrSchemaTooNew) || errors.Is(err, msg.ErrUnsupported)
}

// release gives the Lease back without deciding or writing an event, so a
// compatible Worker can claim the session.
func (w *Worker) release(ctx context.Context, logger *slog.Logger, c eventlog.Claim, cause error) {
	logger.Warn("session needs a newer worker, releasing lease", "error", cause)
	w.released.add(c.SessionID)
	err := w.store.Release(ctx, c.SessionID, c.Fence)
	if err != nil && !errors.Is(err, eventlog.ErrLeaseLost) && ctx.Err() == nil {
		logger.Error("release lease", "error", err)
	}
}

// stop ends a drive on err: lease loss and shutdown write nothing more,
// anything else fails the session.
func (w *Worker) stop(ctx context.Context, logger *slog.Logger, c eventlog.Claim, err error) {
	if errors.Is(err, eventlog.ErrLeaseLost) {
		logger.Warn("lease lost, dropping session")
		return
	}
	if ctx.Err() != nil {
		return
	}
	w.fail(ctx, logger, c, err)
}

// errCrashLoop fails a session whose last claims all died before making
// progress.
var errCrashLoop = errors.New("worker kept dying on this session")

func (w *Worker) fail(ctx context.Context, logger *slog.Logger, c eventlog.Claim, cause error) {
	code := "error"
	if errors.Is(cause, errCrashLoop) {
		code = "crash_loop"
	}
	logger.Error("session failed", "code", code, "error", cause)
	_, err := w.store.AppendFenced(ctx, c.SessionID, c.Fence, []eventlog.NewEvent{{
		Type:    eventlog.TypeSessionError,
		Actor:   w.actor(),
		Payload: map[string]any{"code": code, "message": cause.Error(), "retryable": false},
	}}, &eventlog.StatusChange{To: eventlog.StatusFailed, Reason: code})
	if err != nil && !errors.Is(err, eventlog.ErrLeaseLost) && ctx.Err() == nil {
		logger.Error("record session error", "error", err)
	}
}

func (w *Worker) heartbeat(ctx context.Context, cancel context.CancelFunc, c eventlog.Claim) {
	t := time.NewTicker(w.lease.Heartbeat)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			err := w.store.Heartbeat(ctx, c.SessionID, c.Fence, w.lease.TTL)
			if errors.Is(err, eventlog.ErrLeaseLost) {
				w.logger.Warn("lease lost, cancelling session", "session_id", c.SessionID)
				cancel()
				return
			}
			if err != nil && ctx.Err() == nil {
				w.logger.Error("heartbeat", "session_id", c.SessionID, "error", err)
			}
		}
	}
}

func (w *Worker) actor() string { return "worker:" + w.id }

func (w *Worker) exec(ctx context.Context, c eventlog.Claim, f eventlog.Fence, st State, step Step) error {
	sid := c.SessionID
	switch step.Kind {
	case StepMarkInterrupted:
		_, err := w.store.AppendFenced(ctx, sid, f, []eventlog.NewEvent{{
			Type:          eventlog.TypeTurnInterrupted,
			Actor:         w.actor(),
			CorrelationID: step.TurnID,
			Payload:       map[string]string{"turn_id": step.TurnID, "reason": "worker_lost"},
		}}, nil)
		return err
	case StepStartTurn:
		return w.startTurn(ctx, c, f, st)
	case StepComplete:
		_, err := w.store.AppendFenced(ctx, sid, f, []eventlog.NewEvent{{
			Type:    eventlog.TypeSessionCompleted,
			Actor:   w.actor(),
			Payload: map[string]string{"outcome": string(provider.StopReasonEndTurn)},
		}}, &eventlog.StatusChange{To: eventlog.StatusAwaitingUser, Reason: string(provider.StopReasonEndTurn)})
		return err
	}
	return fmt.Errorf("unknown step %d", step.Kind)
}

func (w *Worker) startTurn(ctx context.Context, c eventlog.Claim, f eventlog.Fence, st State) error {
	sid := c.SessionID
	prov, err := w.gateway.forTurn(ctx, c.UserID, st.Model)
	if err != nil {
		return fmt.Errorf("pick provider: %w", err)
	}
	turnID := uuid.NewString()
	_, err = w.store.AppendFenced(ctx, sid, f, []eventlog.NewEvent{{
		Type:          eventlog.TypeTurnStarted,
		Actor:         w.actor(),
		CorrelationID: turnID,
		Payload: map[string]any{
			"turn_id":           turnID,
			"model":             st.Model,
			"provider":          prov.Name(),
			"input_through_seq": st.LastSeq,
			"tools_hash":        emptyToolsHash,
		},
	}}, nil)
	if err != nil {
		return err
	}

	// The full reply lands with llm.response; deltas only preview its text.
	batch := stream.NewBatcher(w.deltas.Publish, sid, turnID, stream.CoalesceInterval, w.logger)
	stopBatch := batch.Start(ctx)
	resp, err := prov.Stream(ctx, provider.Request{Model: st.Model, Messages: st.Messages}, func(d provider.Delta) {
		if d.Kind == provider.DeltaText {
			batch.Add(d.Text)
		}
	})
	stopBatch()
	if err != nil {
		return fmt.Errorf("call provider: %w", err)
	}

	classes := []struct {
		unit string
		n    int64
	}{
		{"input_tokens", resp.Usage.Input},
		{"cache_read_tokens", resp.Usage.CacheRead},
		{"cache_write_5m_tokens", resp.Usage.CacheWrite5m},
		{"cache_write_1h_tokens", resp.Usage.CacheWrite1h},
		{"output_tokens", resp.Usage.Output},
	}
	usage := map[string]int64{}
	for _, u := range classes {
		usage[u.unit] = u.n
	}
	evs := []eventlog.NewEvent{{
		Type:          eventlog.TypeLLMResponse,
		Actor:         w.actor(),
		CorrelationID: turnID,
		Payload: map[string]any{
			"turn_id":     turnID,
			"message":     resp.Message,
			"stop_reason": resp.StopReason,
			"usage":       usage,
		},
	}}
	// Reasoning is part of output_tokens, so it gets no row of its own.
	for _, u := range classes {
		if u.n == 0 {
			continue
		}
		evs = append(evs, eventlog.NewEvent{
			Type:          eventlog.TypeUsageRecorded,
			Actor:         w.actor(),
			CorrelationID: turnID,
			Payload: eventlog.UsageRecorded{
				Kind: eventlog.KindLLM, Provider: prov.Name(), Model: st.Model,
				Quantity: u.n, Unit: u.unit,
			},
		})
	}
	_, err = w.store.AppendFenced(ctx, sid, f, evs, nil)
	return err
}
