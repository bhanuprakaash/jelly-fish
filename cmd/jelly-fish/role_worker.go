package main

import (
	"context"
	"fmt"
	"log/slog"

	"golang.org/x/sync/errgroup"

	"github.com/bhanuprakaash/jelly-fish/internal/config"
	"github.com/bhanuprakaash/jelly-fish/internal/health"
	"github.com/bhanuprakaash/jelly-fish/internal/pg"
	"github.com/bhanuprakaash/jelly-fish/internal/worker"
)

func runWorker(ctx context.Context, cfg config.Config, logger *slog.Logger) error {
	pool, err := pg.NewPool(ctx, cfg.DatabaseURL)
	if err != nil {
		return fmt.Errorf("connect db: %w", err)
	}
	defer pool.Close()
	if err := pool.Ping(ctx); err != nil {
		return fmt.Errorf("ping db: %w", err)
	}

	healthSrv := health.NewServer(cfg.HealthAddr, pg.NewReadinessChecker(pool), logger)

	g, gctx := errgroup.WithContext(ctx)
	g.Go(func() error { return runHTTPServer(gctx, healthSrv, logger) })
	g.Go(func() error { worker.Run(gctx, logger); return nil })
	return g.Wait()
}
