package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/url"

	"github.com/prometheus/client_golang/prometheus"
	"golang.org/x/sync/errgroup"

	"github.com/bhanuprakaash/jelly-fish/internal/api"
	"github.com/bhanuprakaash/jelly-fish/internal/auth"
	"github.com/bhanuprakaash/jelly-fish/internal/config"
	"github.com/bhanuprakaash/jelly-fish/internal/eventlog"
	"github.com/bhanuprakaash/jelly-fish/internal/health"
	"github.com/bhanuprakaash/jelly-fish/internal/mail"
	"github.com/bhanuprakaash/jelly-fish/internal/pg"
	"github.com/bhanuprakaash/jelly-fish/internal/stream"
	"github.com/bhanuprakaash/jelly-fish/web"
)

func runAPI(ctx context.Context, cfg config.Config, logger *slog.Logger) error {
	if cfg.SMTPURL == "" || cfg.MailFrom == "" {
		return errors.New("JF_SMTP_URL and JF_MAIL_FROM are required")
	}
	if u, err := url.Parse(cfg.PublicURL); err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
		return errors.New("JF_PUBLIC_URL is required: want http(s)://host[:port]")
	}
	mailer, err := mail.NewSMTP(cfg.SMTPURL, cfg.MailFrom)
	if err != nil {
		return fmt.Errorf("JF_SMTP_URL: %w", err)
	}

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
	apiSrv := api.NewServer(cfg.APIAddr, logger, webFS, repo, hub, deltas, metrics, api.AuthConfig{
		Authenticator:  auth.NewStore(pool),
		Mailer:         mailer,
		PublicURL:      cfg.PublicURL,
		InsecureCookie: cfg.DevInsecureCookie,
	})

	g, gctx := errgroup.WithContext(ctx)
	g.Go(func() error { stream.Listen(gctx, pool, logger, hub, deltas, metrics); return nil })
	g.Go(func() error { return runHTTPServer(gctx, healthSrv, logger) })
	g.Go(func() error { return runHTTPServer(gctx, apiSrv, logger) })
	return g.Wait()
}
