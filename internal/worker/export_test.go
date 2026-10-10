package worker

import (
	"context"
	"time"

	"github.com/bhanuprakaash/jelly-fish/internal/eventlog"
)

// SetTitleTimeout bounds each Title call to d; zero or less turns Titles off,
// which keeps tests that count provider calls or events independent of them.
func SetTitleTimeout(w *Worker, d time.Duration) { w.titleTimeout = d }

// Exec runs step against st, as the drive loop does after folding it.
func Exec(ctx context.Context, w *Worker, c eventlog.Claim, st State, step Step) error {
	return w.exec(ctx, c, c.Fence, st, step)
}

// SetLegacyHold bounds a legacy elicitation to d.
func SetLegacyHold(w *Worker, d time.Duration) { w.legacyHold = d }

// SetDeltas makes the Worker publish its deltas to p.
func SetDeltas(w *Worker, p DeltaPublisher) { w.deltas = p }
