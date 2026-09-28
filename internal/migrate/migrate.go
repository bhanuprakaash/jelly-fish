// Package migrate applies goose SQL migrations using a Postgres advisory lock.
package migrate

import (
	"context"
	"database/sql"
	"fmt"
	"log/slog"

	"github.com/pressly/goose/v3"
	"github.com/pressly/goose/v3/lock"

	"github.com/bhanuprakaash/jelly-fish/migrations"
)

// Up applies all pending migrations, serialized across concurrent callers via
// a Postgres session-level advisory lock.
func Up(ctx context.Context, db *sql.DB, logger *slog.Logger) error {
	locker, err := lock.NewPostgresSessionLocker()
	if err != nil {
		return fmt.Errorf("create session locker: %w", err)
	}

	provider, err := goose.NewProvider(goose.DialectPostgres, db, migrations.FS, goose.WithSessionLocker(locker))
	if err != nil {
		return fmt.Errorf("create migration provider: %w", err)
	}

	results, err := provider.Up(ctx)
	if err != nil {
		return fmt.Errorf("apply migrations: %w", err)
	}

	for _, r := range results {
		logger.Info("applied migration", "version", r.Source.Version, "duration", r.Duration)
	}
	if len(results) == 0 {
		logger.Info("no pending migrations")
	}
	return nil
}
