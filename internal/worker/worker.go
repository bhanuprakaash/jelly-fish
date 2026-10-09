// Package worker claims runnable sessions and drives them: load, fold,
// decide, exec (docs/design/event-log.md §5.3).
package worker

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"math"
	"os"
	"slices"
	"sync"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/bhanuprakaash/jelly-fish/internal/eventlog"
	"github.com/bhanuprakaash/jelly-fish/internal/memory"
	"github.com/bhanuprakaash/jelly-fish/internal/msg"
	"github.com/bhanuprakaash/jelly-fish/internal/provider"
	"github.com/bhanuprakaash/jelly-fish/internal/provider/tracing"
	"github.com/bhanuprakaash/jelly-fish/internal/stream"
	"github.com/bhanuprakaash/jelly-fish/internal/tool"
)

const (
	maxSessions     = 50
	pollInterval    = 3 * time.Second
	runnableChannel = "jf_runnable"
	// maxRecoveryAttempts is how many claims in a row may make no fenced
	// append before the session is failed (event-log.md §5.6).
	maxRecoveryAttempts = 5
)

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
	tools    *tool.Registry
	held     held
	deltas   DeltaPublisher
	lease    Lease
	upcast   eventlog.Upcasters
	released released
	id       string
	logger   *slog.Logger
	slots    chan struct{}
	// titleTimeout bounds each Title call; zero or less turns Titles off.
	titleTimeout time.Duration
}

// New builds a Worker that runs each turn on the Provider gw picks, offers
// the model tools (nil for none), streams reply text to deltas and leases
// sessions as lease says. It reads stored events through upcast, and releases
// any session holding an event it cannot read.
func New(pool *pgxpool.Pool, gw Gateway, tools *tool.Registry, deltas DeltaPublisher, lease Lease, upcast eventlog.Upcasters, logger *slog.Logger) *Worker {
	host, err := os.Hostname()
	if err != nil {
		host = "unknown"
	}
	return &Worker{
		pool:    pool,
		store:   eventlog.NewStore(pool),
		gateway: gw,
		tools:   tools,
		deltas:  deltas,
		lease:   lease,
		upcast:  upcast,
		id:      fmt.Sprintf("%s:%d", host, os.Getpid()),
		logger:  logger,
		slots:   make(chan struct{}, maxSessions),

		titleTimeout: defaultTitleTimeout,
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
	ctx, cancel := context.WithCancelCause(parent)
	var hb sync.WaitGroup
	defer hb.Wait()
	defer cancel(nil)
	defer w.held.drop(c.SessionID)
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
		case errors.Is(err, errParked):
			logger.Info("session parked after provider error")
			return
		case err != nil:
			w.stop(ctx, logger, c, err)
			return
		case step.Kind.Parks():
			logger.Info("session parked", "status", eventlog.StatusAwaitingUser)
			return
		}
	}
}

// errParked is what a step returns after its own write took the session out
// of running, so the drive stops without reading the log again.
var errParked = errors.New("session parked")

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
		if errors.Is(context.Cause(ctx), errInterrupted) {
			w.finishInterrupt(ctx, logger, c)
		}
		return
	}
	w.fail(ctx, logger, c, err)
}

// errInterrupted is the cancel cause when the User asked to stop the running
// session, as opposed to losing the Lease or shutting down.
var errInterrupted = errors.New("interrupted by user")

// interruptTimeout bounds finishInterrupt, which runs after its session's
// context is already cancelled.
const interruptTimeout = 10 * time.Second

// finishInterrupt closes the open turn, if any, and parks the session
// awaiting the User (event-log.md §5.8).
func (w *Worker) finishInterrupt(ctx context.Context, logger *slog.Logger, c eventlog.Claim) {
	ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), interruptTimeout)
	defer cancel()
	evs, err := w.store.Load(ctx, c.SessionID)
	var st State
	if err == nil {
		st, err = w.fold(evs)
	}
	if err != nil {
		logger.Error("finish interrupt", "error", err)
		return
	}
	var closing []eventlog.NewEvent
	if st.OpenTurn != nil {
		closing = append(closing, eventlog.NewEvent{
			Type:          eventlog.TypeTurnInterrupted,
			Actor:         w.actor(),
			CorrelationID: st.OpenTurn.ID,
			Payload:       map[string]string{"turn_id": st.OpenTurn.ID, "reason": "user_interrupt"},
		})
	}
	closing = append(closing, w.userStopEvents(st)...)
	_, err = w.store.AppendFenced(ctx, c.SessionID, c.Fence, closing, &eventlog.StatusChange{To: eventlog.StatusAwaitingUser, Reason: "interrupted"})
	if err != nil && !errors.Is(err, eventlog.ErrLeaseLost) {
		logger.Error("finish interrupt", "error", err)
	}
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

func (w *Worker) heartbeat(ctx context.Context, cancel context.CancelCauseFunc, c eventlog.Claim) {
	t := time.NewTicker(w.lease.Heartbeat)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			cancelRequested, err := w.store.Heartbeat(ctx, c.SessionID, c.Fence, w.lease.TTL)
			switch {
			case errors.Is(err, eventlog.ErrLeaseLost):
				w.logger.Warn("lease lost, cancelling session", "session_id", c.SessionID)
				cancel(err)
				return
			case err != nil:
				if ctx.Err() == nil {
					w.logger.Error("heartbeat", "session_id", c.SessionID, "error", err)
				}
			case cancelRequested:
				w.logger.Info("interrupt requested, cancelling session", "session_id", c.SessionID)
				cancel(errInterrupted)
				return
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
	case StepMarkToolsInterrupted:
		return w.markToolsInterrupted(ctx, c, f, st)
	case StepRequestTools:
		return w.requestTools(ctx, c, f, st)
	case StepStartTool:
		return w.startTools(ctx, c, f, st)
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
	turnID := uuid.NewString()
	// The full reply lands with llm.response; deltas only preview its text and thinking.
	batch := stream.NewBatcher(w.deltas.Publish, sid, turnID, stream.CoalesceInterval, w.logger)
	prov, err := w.gateway.forTurn(ctx, c.UserID, st.Model, tracing.IDs{SessionID: sid.String(), TurnID: turnID}, func() { batch.Reset(ctx) })
	var pe *provider.Error
	switch {
	case errors.As(err, &pe):
		return w.parkOnProviderError(ctx, c, f, st, "", "", st.LastSeq, pe)
	case err != nil:
		return fmt.Errorf("pick provider: %w", err)
	}
	defs := w.tools.Defs()
	if c.Incognito {
		defs = slices.DeleteFunc(defs, func(d tool.Def) bool { return d.Name == memory.ToolName })
	}
	toolsHash, err := tool.Hash(defs)
	if err != nil {
		return err
	}
	messages, err := memory.Resolve(ctx, w.pool, st.Messages)
	if err != nil {
		return err
	}
	system, err := w.system(ctx, c)
	if err != nil {
		return err
	}
	seqs, err := w.store.AppendFenced(ctx, sid, f, []eventlog.NewEvent{{
		Type:          eventlog.TypeTurnStarted,
		Actor:         w.actor(),
		CorrelationID: turnID,
		Payload: map[string]any{
			"turn_id":           turnID,
			"model":             st.Model,
			"provider":          prov.Name(),
			"input_through_seq": st.LastSeq,
			"tools_hash":        toolsHash,
		},
	}}, nil)
	if err != nil {
		return err
	}
	title := w.startTitle(ctx, c, st)
	defer title.abandon()

	stopBatch := batch.Start(ctx)
	resp, err := prov.Stream(ctx, provider.Request{Model: st.Model, System: system, Messages: messages, Tools: toolSpecs(defs), CacheKey: c.SessionID.String()}, func(d provider.Delta) {
		switch d.Kind {
		case provider.DeltaText:
			batch.Add(d.Text)
		case provider.DeltaThinking:
			batch.AddThinking(d.Text)
		}
	})
	stopBatch()
	if errors.As(err, &pe) {
		// The park gives up the Lease, so the Title has to land first.
		title.settle()
		err := w.parkOnProviderError(ctx, c, f, st, turnID, prov.Name(), title.contiguousAfter(seqs[0]), pe)
		if errors.Is(err, errParked) {
			batch.Reset(ctx)
		}
		return err
	}
	if err != nil {
		return fmt.Errorf("call provider: %w", err)
	}

	if resp.StopReason == provider.StopReasonMaxTokens {
		// A call cut off mid-way can't be run, and no result could answer it.
		resp.Message.Parts = slices.DeleteFunc(resp.Message.Parts, func(p msg.Part) bool { return p.Kind == msg.KindToolUse })
	}
	resp.Message.Parts = w.redactMemoryCalls(sid, resp.Message.Parts)

	classes := usageClasses(resp.Usage, w.gateway.price(st.Model))
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
	evs = append(evs, w.usageEvents(classes, turnID, prov.Name(), st.Model)...)
	if _, err := w.store.AppendFenced(ctx, sid, f, evs, nil); err != nil {
		return err
	}
	// The next loop iteration parks, which gives up the Lease.
	title.settle()
	return nil
}

// platformPrompt is the first system block; it must stay byte-stable.
const platformPrompt = "You are Jelly Fish, a helpful assistant."

// system is the system prompt: platform text, the Project's instructions,
// then the memory blocks (context.md §5.2).
func (w *Worker) system(ctx context.Context, c eventlog.Claim) ([]string, error) {
	system := []string{platformPrompt}
	var instructions string
	if err := w.pool.QueryRow(ctx, `SELECT instructions FROM projects WHERE id = $1`, c.ProjectID).Scan(&instructions); err != nil {
		return nil, fmt.Errorf("load project instructions: %w", err)
	}
	if instructions != "" {
		system = append(system, instructions)
	}
	if c.Incognito {
		return system, nil
	}
	blocks, err := memory.Prompt(ctx, w.pool, c.SessionID, c.UserID, c.ProjectID)
	if err != nil {
		return nil, err
	}
	if blocks != "" {
		system = append(system, blocks)
	}
	return system, nil
}

func toolSpecs(defs []tool.Def) []provider.ToolSpec {
	specs := make([]provider.ToolSpec, len(defs))
	for i, d := range defs {
		specs[i] = provider.ToolSpec{Name: d.Name, Description: d.Description, Schema: d.Schema, Strict: d.Strict}
	}
	return specs
}

type usageClass struct {
	unit string
	n    int64
	// price is USD per million tokens, so n × price is micro-dollars.
	price float64
}

// usageClasses lists u's billing classes, priced by p; a nil p prices
// them at 0. Reasoning is part of output_tokens, so it gets none of its own.
func usageClasses(u provider.Usage, p *provider.Prices) []usageClass {
	if p == nil {
		p = &provider.Prices{}
	}
	return []usageClass{
		{"input_tokens", u.Input, p.Input},
		{"cache_read_tokens", u.CacheRead, p.CacheRead},
		{"cache_write_5m_tokens", u.CacheWrite5m, p.CacheWrite5m},
		{"cache_write_1h_tokens", u.CacheWrite1h, p.CacheWrite1h},
		{"output_tokens", u.Output, p.Output},
	}
}

// usageEvents is one usage.recorded per non-zero class. cost_micros is
// fixed at record time
// from the catalog (provider-gateway.md D12).
func (w *Worker) usageEvents(classes []usageClass, turnID, providerName, model string) []eventlog.NewEvent {
	var evs []eventlog.NewEvent
	for _, u := range classes {
		if u.n == 0 {
			continue
		}
		evs = append(evs, eventlog.NewEvent{
			Type:          eventlog.TypeUsageRecorded,
			Actor:         w.actor(),
			CorrelationID: turnID,
			Payload: eventlog.UsageRecorded{
				Kind: eventlog.KindLLM, Provider: providerName, Model: model,
				Quantity: u.n, Unit: u.unit, CostMicros: int64(math.Round(float64(u.n) * u.price)),
			},
		})
	}
	return evs
}

// sleepFor is how long a session sleeps after its step-th retryable error
// outlasted the quick retries: 1, 5 then 15 minutes, after which it fails
// (agent-loop.md §5.3).
func sleepFor(step int) (time.Duration, bool) {
	switch step {
	case 0:
		return time.Minute, true
	case 1:
		return 5 * time.Minute, true
	case 2:
		return 15 * time.Minute, true
	}
	return 0, false
}

// errorMessage is the fixed text for a session.error: Provider text can echo
// the request, so none of it is stored.
func errorMessage(kind provider.ErrorKind) string {
	switch kind {
	case provider.KindKeyInvalid:
		return "Provider key rejected"
	case provider.KindBilling:
		return "Provider account out of credit or limit reached"
	case provider.KindModelUnavailable:
		return "Key can't use this model"
	case provider.KindTooLarge:
		return "File too large"
	case provider.KindBug:
		return "Internal error"
	}
	return "Provider having trouble"
}

// parkOnProviderError writes what a failed call leaves behind in one tx: its
// usage, then the session.error and the status that follows from its class
// (agent-loop.md §5.3). It returns errParked on success. lastSeq is the log head the call began from, so a
// message that arrived meanwhile makes the park stale instead of stranding it.
func (w *Worker) parkOnProviderError(ctx context.Context, c eventlog.Claim, f eventlog.Fence, st State, turnID, providerName string, lastSeq int64, pe *provider.Error) error {
	usage := w.usageEvents(usageClasses(pe.Usage, w.gateway.price(st.Model)), turnID, providerName, st.Model)
	evs := slices.Clone(usage)
	sessionError := func(retryable bool) eventlog.NewEvent {
		payload := map[string]any{
			"code":       pe.Kind,
			"message":    errorMessage(pe.Kind),
			"retryable":  retryable,
			"request_id": pe.RequestID,
		}
		if turnID != "" {
			payload["turn_id"] = turnID
		}
		return eventlog.NewEvent{Type: eventlog.TypeSessionError, Actor: w.actor(), CorrelationID: turnID, Payload: payload}
	}

	var next eventlog.StatusChange
	sleep, canSleep := sleepFor(st.RetryStep)
	switch {
	case pe.Kind == provider.KindLongWait:
		// No session.error: the Provider asked for a pause, nothing broke. The
		// turn is closed here because no error event will.
		evs = append(evs, eventlog.NewEvent{
			Type:          eventlog.TypeTurnInterrupted,
			Actor:         w.actor(),
			CorrelationID: turnID,
			Payload:       map[string]string{"turn_id": turnID, "reason": "provider_wait"},
		})
		next = eventlog.StatusChange{To: eventlog.StatusSleeping, Reason: string(pe.Kind), WakeIn: pe.RetryAfter}
	case pe.Kind.Retryable() && canSleep:
		evs = append(evs, sessionError(true))
		next = eventlog.StatusChange{To: eventlog.StatusSleeping, Reason: string(pe.Kind), WakeIn: sleep}
	case pe.Kind.Retryable():
		evs = append(evs, sessionError(false))
		next = eventlog.StatusChange{To: eventlog.StatusFailed, Reason: "retries_exhausted"}
	default:
		evs = append(evs, sessionError(false))
		next = eventlog.StatusChange{To: eventlog.StatusAwaitingUser, Reason: "error"}
	}
	w.logger.Warn("provider call failed", "session_id", c.SessionID, "code", pe.Kind, "request_id", pe.RequestID, "status", next.To)
	f.ExpectSeq = &lastSeq
	_, err := w.store.AppendFenced(ctx, c.SessionID, f, evs, &next)
	if errors.Is(err, eventlog.ErrStale) && turnID != "" {
		// The new events win and the turn is re-run, but what the failed
		// attempts consumed is still billed.
		usage = append(usage, eventlog.NewEvent{
			Type:          eventlog.TypeTurnInterrupted,
			Actor:         w.actor(),
			CorrelationID: turnID,
			Payload:       map[string]string{"turn_id": turnID, "reason": "provider_error"},
		})
		f.ExpectSeq = nil
		if _, err := w.store.AppendFenced(ctx, c.SessionID, f, usage, nil); err != nil {
			return err
		}
		return eventlog.ErrStale
	}
	if err != nil {
		return err
	}
	return errParked
}
