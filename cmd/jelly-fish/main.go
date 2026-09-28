// Command jelly-fish is the single binary running the api, worker and migrate roles.
package main

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"syscall"

	"github.com/bhanuprakaash/jelly-fish/internal/config"
)

func main() {
	logger := slog.New(slog.NewJSONHandler(os.Stdout, nil))

	if len(os.Args) < 2 {
		logger.Error("missing role", "usage", "jelly-fish [api|worker|migrate]")
		os.Exit(1)
	}
	role := os.Args[1]

	cfg, err := config.Load()
	if err != nil {
		logger.Error("load config", "error", err)
		os.Exit(1)
	}

	if err := run(role, cfg, logger); err != nil {
		logger.Error("exit", "role", role, "error", err)
		os.Exit(1)
	}
}

func run(role string, cfg config.Config, logger *slog.Logger) error {
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	switch role {
	case "api":
		return runAPI(ctx, cfg, logger)
	case "worker":
		return runWorker(ctx, cfg, logger)
	case "migrate":
		return runMigrate(ctx, cfg, logger)
	default:
		return fmt.Errorf("unknown role %q: want api, worker or migrate", role)
	}
}
