// Package pg wires up the pgx pool used by the api and worker roles.
package pg

import (
	"context"
	"fmt"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/pressly/goose/v3"

	"github.com/bhanuprakaash/jelly-fish/internal/health"
)

// NewPool creates a pgx connection pool for databaseURL.
func NewPool(ctx context.Context, databaseURL string) (*pgxpool.Pool, error) {
	pool, err := pgxpool.New(ctx, databaseURL)
	if err != nil {
		return nil, fmt.Errorf("create pool: %w", err)
	}
	return pool, nil
}

// ReadinessChecker reports whether the database is reachable and migrated.
type ReadinessChecker struct {
	pool *pgxpool.Pool
}

var _ health.Checker = (*ReadinessChecker)(nil)

// NewReadinessChecker builds a ReadinessChecker backed by pool.
func NewReadinessChecker(pool *pgxpool.Pool) *ReadinessChecker {
	return &ReadinessChecker{pool: pool}
}

// Ready pings the database and confirms the goose version table exists.
func (r *ReadinessChecker) Ready(ctx context.Context) error {
	if err := r.pool.Ping(ctx); err != nil {
		return fmt.Errorf("ping: %w", err)
	}
	tableName := "public." + goose.TableName()
	var exists bool
	err := r.pool.QueryRow(ctx, "select to_regclass($1) is not null", tableName).Scan(&exists)
	if err != nil {
		return fmt.Errorf("check %s: %w", tableName, err)
	}
	if !exists {
		return fmt.Errorf("%s table not present: migrations not applied", tableName)
	}
	return nil
}
