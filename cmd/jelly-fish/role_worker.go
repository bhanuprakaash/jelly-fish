package main

import (
	"context"
	"fmt"
	"log/slog"

	"golang.org/x/sync/errgroup"

	"github.com/bhanuprakaash/jelly-fish/internal/config"
	"github.com/bhanuprakaash/jelly-fish/internal/eventlog"
	"github.com/bhanuprakaash/jelly-fish/internal/health"
	"github.com/bhanuprakaash/jelly-fish/internal/pg"
	"github.com/bhanuprakaash/jelly-fish/internal/provider/anthropic"
	"github.com/bhanuprakaash/jelly-fish/internal/provider/catalog"
	"github.com/bhanuprakaash/jelly-fish/internal/providerkeys"
	"github.com/bhanuprakaash/jelly-fish/internal/stream"
	"github.com/bhanuprakaash/jelly-fish/internal/worker"
)

func runWorker(ctx context.Context, cfg config.Config, logger *slog.Logger) error {
	kr, err := loadKeyring(cfg)
	if err != nil {
		return err
	}
	cat, err := catalog.Load()
	if err != nil {
		return err
	}
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
	claude := anthropic.Client{Catalog: cat}
	gw := worker.Gateway{Keyring: kr, Keys: providerkeys.NewStore(pool), Catalog: cat, Anthropic: claude, Fake: devFake()}
	g.Go(func() error { worker.RunModelRefresh(gctx, pool, kr, claude, logger); return nil })
	g.Go(func() error {
		worker.New(pool, gw, stream.NewPGDeltaBus(pool), worker.Lease{TTL: cfg.LeaseTTL, Heartbeat: cfg.Heartbeat}, eventlog.Upcasters{}, logger).Run(gctx)
		return nil
	})
	return g.Wait()
}
