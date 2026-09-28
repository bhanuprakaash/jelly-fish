package main

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"log/slog"

	_ "github.com/jackc/pgx/v5/stdlib"

	"github.com/bhanuprakaash/jelly-fish/internal/config"
	"github.com/bhanuprakaash/jelly-fish/internal/migrate"
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
	return nil
}
