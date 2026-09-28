// Package worker runs the background worker role.
package worker

import (
	"context"
	"log/slog"
)

// Run blocks until ctx is cancelled (SIGTERM).
func Run(ctx context.Context, logger *slog.Logger) {
	logger.Info("worker idle")
	<-ctx.Done()
	logger.Info("worker stopping")
}
