package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"

	"github.com/bhanuprakaash/jelly-fish/internal/config"
	"github.com/bhanuprakaash/jelly-fish/internal/keyring"
	"github.com/bhanuprakaash/jelly-fish/internal/pg"
	"github.com/bhanuprakaash/jelly-fish/internal/worker"
)

// loadKeyring parses JF_MASTER_KEY, so a role that needs it refuses to start
// without a usable one.
func loadKeyring(cfg config.Config) (*keyring.Keyring, error) {
	if cfg.MasterKey == "" {
		return nil, errors.New("JF_MASTER_KEY is required: want comma-separated id:base64(32 bytes)")
	}
	kr, err := keyring.Parse(cfg.MasterKey)
	if err != nil {
		return nil, fmt.Errorf("JF_MASTER_KEY: %w", err)
	}
	return kr, nil
}

func runKeysRotate(ctx context.Context, cfg config.Config, logger *slog.Logger) error {
	kr, err := loadKeyring(cfg)
	if err != nil {
		return err
	}
	pool, err := pg.NewPool(ctx, cfg.DatabaseURL)
	if err != nil {
		return fmt.Errorf("connect db: %w", err)
	}
	defer pool.Close()
	n, err := worker.RotateKeys(ctx, pool, kr)
	logger.Info("keys rotated", "rows", n, "primary", kr.Primary())
	if err != nil {
		return fmt.Errorf("rotate keys: %w", err)
	}
	return nil
}
