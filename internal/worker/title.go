package worker

import (
	"context"
	"errors"
	"sync"
	"time"

	"github.com/bhanuprakaash/jelly-fish/internal/eventlog"
	"github.com/bhanuprakaash/jelly-fish/internal/msg"
	"github.com/bhanuprakaash/jelly-fish/internal/provider"
	"github.com/bhanuprakaash/jelly-fish/internal/provider/tracing"
)

// defaultTitleTimeout bounds a Title call, so a slow one delays the park by
// at most this long (event-log.md D45).
const defaultTitleTimeout = 30 * time.Second

// titleCall is the Title side call of a session's first Turn. Its methods
// accept nil, which stands for a session that wants no Title.
type titleCall struct {
	cancel context.CancelFunc
	wg     sync.WaitGroup

	mu   sync.Mutex
	seqs []int64
}

// startTitle begins the Title call in parallel with the Turn that just
// started, or returns nil if the session wants none. The call runs on the
// session's own model with no retry; a failure leaves the placeholder
// (event-log.md D45).
func (w *Worker) startTitle(ctx context.Context, c eventlog.Claim, st State) *titleCall {
	if w.titleTimeout <= 0 || !st.wantsTitle() {
		return nil
	}
	ctx, cancel := context.WithTimeout(ctx, w.titleTimeout)
	t := &titleCall{cancel: cancel}
	t.wg.Go(func() { w.runTitle(ctx, c, st, t) })
	return t
}

func (w *Worker) runTitle(ctx context.Context, c eventlog.Claim, st State, t *titleCall) {
	logger := w.logger.With("session_id", c.SessionID)
	prov, err := w.gateway.forSideCall(ctx, c.UserID, st.Model, tracing.IDs{SessionID: c.SessionID.String()})
	if err != nil {
		logger.Warn("title call not started", "error", err)
		return
	}
	resp, err := prov.Stream(ctx, provider.Request{
		Model:      st.Model,
		Messages:   []msg.Message{msg.UserText(provider.TitlePrompt(st.Messages[0].Text()))},
		NoThinking: true,
	}, func(provider.Delta) {})
	if ctx.Err() != nil {
		// Timed out, or the Turn ended without waiting: nothing may be
		// written, as the Lease may be gone.
		if errors.Is(ctx.Err(), context.DeadlineExceeded) {
			logger.Warn("title call timed out", "timeout", w.titleTimeout)
		}
		return
	}
	usage := resp.Usage
	var title string
	var pe *provider.Error
	switch {
	case errors.As(err, &pe):
		usage = pe.Usage
		logger.Warn("title call failed", "code", pe.Kind, "request_id", pe.RequestID, "http_status", pe.HTTPStatus)
	case err != nil:
		logger.Warn("title call failed", "error", err)
	default:
		title = eventlog.CleanTitle(resp.Message.Text())
	}

	// No correlation id: the call is not a Turn, and its tokens still count
	// toward the session.
	evs := w.usageEvents(usageClasses(usage, w.gateway.price(st.Model)), "", prov.Name(), st.Model)
	if title != "" {
		evs = append(evs, eventlog.NewEvent{
			Type:    eventlog.TypeSessionRenamed,
			Actor:   w.actor(),
			Payload: eventlog.SessionRenamed{Title: title, By: eventlog.RenamedByAuto},
		})
	}
	if len(evs) == 0 {
		return
	}
	f := c.Fence
	f.ExpectSeq = nil
	seqs, err := w.store.AppendFenced(ctx, c.SessionID, f, evs, nil)
	if err != nil {
		if !errors.Is(err, eventlog.ErrLeaseLost) && ctx.Err() == nil {
			logger.Warn("write title", "error", err)
		}
		return
	}
	t.mu.Lock()
	t.seqs = seqs
	t.mu.Unlock()
}

// settle waits for the call to finish or time out, so its write lands before
// the Worker gives up the Lease. The heartbeat keeps the Lease meanwhile.
func (t *titleCall) settle() {
	if t == nil {
		return
	}
	t.wg.Wait()
	t.cancel()
}

// abandon stops the call and waits for it, for a Turn that ends abnormally:
// no Title then, and no write after the Turn's own.
func (t *titleCall) abandon() {
	if t == nil {
		return
	}
	t.cancel()
	t.wg.Wait()
}

// contiguousAfter is the log head a park may expect once the call has
// settled: the call's last seq if its write directly followed seq, else seq
// itself, so anything else that landed in between still makes the park stale.
func (t *titleCall) contiguousAfter(seq int64) int64 {
	if t == nil {
		return seq
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	if len(t.seqs) > 0 && t.seqs[0] == seq+1 {
		return t.seqs[len(t.seqs)-1]
	}
	return seq
}
