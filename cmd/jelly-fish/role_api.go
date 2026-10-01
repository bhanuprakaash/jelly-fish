package main

import (
	"context"
	"fmt"
	"log/slog"

	"github.com/prometheus/client_golang/prometheus"
	"golang.org/x/sync/errgroup"

	"github.com/bhanuprakaash/jelly-fish/internal/api"
	"github.com/bhanuprakaash/jelly-fish/internal/config"
	"github.com/bhanuprakaash/jelly-fish/internal/eventlog"
	"github.com/bhanuprakaash/jelly-fish/internal/health"
	"github.com/bhanuprakaash/jelly-fish/internal/pg"
	"github.com/bhanuprakaash/jelly-fish/internal/stream"
	"github.com/bhanuprakaash/jelly-fish/web"
)

func runAPI(ctx context.Context, cfg config.Config, logger *slog.Logger) error {
	pool, err := pg.NewPool(ctx, cfg.DatabaseURL)
	if err != nil {
		return fmt.Errorf("connect db: %w", err)
	}
	defer pool.Close()

	webFS, err := web.FS()
	if err != nil {
		return fmt.Errorf("load web assets: %w", err)
	}

	repo := eventlog.NewRepo(pool)
	hub := stream.NewHub()
	deltas := stream.NewPGDeltaBus(pool)

	reg := prometheus.NewRegistry()
	metrics, err := stream.NewMetrics(reg)
	if err != nil {
		return err
	}
	if err := stream.RegisterQueueUsage(ctx, reg, pool); err != nil {
		return err
	}

	healthSrv := health.NewServer(cfg.HealthAddr, pg.NewReadinessChecker(pool), logger)
	mountMetrics(healthSrv, reg)
	apiSrv := api.NewServer(cfg.APIAddr, logger, webFS, repo, hub, deltas, metrics)

	g, gctx := errgroup.WithContext(ctx)
	g.Go(func() error { stream.Listen(gctx, pool, logger, hub, deltas, metrics); return nil })
	g.Go(func() error { return runHTTPServer(gctx, healthSrv, logger) })
	g.Go(func() error { return runHTTPServer(gctx, apiSrv, logger) })
	return g.Wait()
}
