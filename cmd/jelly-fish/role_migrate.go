package main

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"log/slog"

	_ "github.com/jackc/pgx/v5/stdlib"

	"github.com/bhanuprakaash/jelly-fish/internal/auth"
	"github.com/bhanuprakaash/jelly-fish/internal/config"
	"github.com/bhanuprakaash/jelly-fish/internal/migrate"
	"github.com/bhanuprakaash/jelly-fish/internal/pg"
)

func runMigrate(ctx context.Context, cfg config.Config, logger *slog.Logger) (err error) {
	db, err := sql.Open("pgx", cfg.DatabaseURL)
	if err != nil {
		return fmt.Errorf("open db: %w", err)
	}
	defer func() { err = errors.Join(err, db.Close()) }()

	if err := migrate.Up(ctx, db, logger); err != nil {
		return fmt.Errorf("migrate up: %w", err)
	}

	if cfg.AdminEmail == "" {
		return nil
	}
	pool, err := pg.NewPool(ctx, cfg.DatabaseURL)
	if err != nil {
		return fmt.Errorf("connect db: %w", err)
	}
	defer pool.Close()
	created, err := auth.BootstrapAdmin(ctx, pool, cfg.AdminEmail)
	if err != nil {
		return fmt.Errorf("bootstrap admin: %w", err)
	}
	logger.Info("admin bootstrap", "created", created)
	return nil
}
