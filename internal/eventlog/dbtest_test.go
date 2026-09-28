package eventlog_test

import (
	"context"
	"database/sql"
	"fmt"
	"log/slog"
	"net/url"
	"os"
	"testing"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	_ "github.com/jackc/pgx/v5/stdlib"

	"github.com/bhanuprakaash/jelly-fish/internal/migrate"
)

// newTestPool creates a fresh, migrated database on the server pointed to by
// TEST_DATABASE_URL, isolated to this test, and returns a pool for it. It
// skips the test if TEST_DATABASE_URL is unset (event-log.md "Tests: real
// Postgres").
func newTestPool(t *testing.T) *pgxpool.Pool {
	t.Helper()

	base := os.Getenv("TEST_DATABASE_URL")
	if base == "" {
		t.Skip("TEST_DATABASE_URL not set")
	}

	ctx := context.Background()
	admin, err := pgxpool.New(ctx, base)
	if err != nil {
		t.Fatalf("connect admin pool: %v", err)
	}
	defer admin.Close()

	dbName := "test_" + uuidHex()
	if _, err := admin.Exec(ctx, "CREATE DATABASE "+pgx.Identifier{dbName}.Sanitize()); err != nil {
		t.Fatalf("create test database: %v", err)
	}
	t.Cleanup(func() {
		_, _ = admin.Exec(context.Background(), "DROP DATABASE "+pgx.Identifier{dbName}.Sanitize()+" WITH (FORCE)")
	})

	dbURL, err := withDatabase(base, dbName)
	if err != nil {
		t.Fatalf("build test database url: %v", err)
	}

	db, err := sql.Open("pgx", dbURL)
	if err != nil {
		t.Fatalf("open test database: %v", err)
	}
	if err := migrate.Up(ctx, db, slog.New(slog.DiscardHandler)); err != nil {
		_ = db.Close()
		t.Fatalf("migrate test database: %v", err)
	}
	if err := db.Close(); err != nil {
		t.Fatalf("close migration db handle: %v", err)
	}

	pool, err := pgxpool.New(ctx, dbURL)
	if err != nil {
		t.Fatalf("connect test pool: %v", err)
	}
	t.Cleanup(pool.Close)
	return pool
}

func withDatabase(rawURL, dbName string) (string, error) {
	u, err := url.Parse(rawURL)
	if err != nil {
		return "", fmt.Errorf("parse %q: %w", rawURL, err)
	}
	u.Path = "/" + dbName
	return u.String(), nil
}

func uuidHex() string {
	id := uuid.New()
	return fmt.Sprintf("%x", id[:])
}
