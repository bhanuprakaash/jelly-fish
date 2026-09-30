// Package config loads process configuration from the environment.
package config

import (
	"errors"
	"fmt"
	"os"
	"time"
)

// Config holds env-derived settings shared by all roles.
type Config struct {
	DatabaseURL string
	APIAddr     string
	HealthAddr  string
	// LeaseTTL is how long a Worker's Lease on a session lasts without a
	// heartbeat; Heartbeat is how often it renews (docs/design/event-log.md
	// §5.2).
	LeaseTTL  time.Duration
	Heartbeat time.Duration
}

// Load reads Config from the environment.
func Load() (Config, error) {
	databaseURL := os.Getenv("DATABASE_URL")
	if databaseURL == "" {
		return Config{}, errors.New("DATABASE_URL is required")
	}
	leaseTTL, err := envDuration("JF_LEASE_TTL", 30*time.Second)
	if err != nil {
		return Config{}, err
	}
	heartbeat, err := envDuration("JF_HEARTBEAT", 10*time.Second)
	if err != nil {
		return Config{}, err
	}
	if heartbeat >= leaseTTL {
		return Config{}, fmt.Errorf("JF_HEARTBEAT %s must be shorter than JF_LEASE_TTL %s", heartbeat, leaseTTL)
	}
	return Config{
		DatabaseURL: databaseURL,
		APIAddr:     envOr("API_ADDR", ":8080"),
		HealthAddr:  envOr("HEALTH_ADDR", ":9090"),
		LeaseTTL:    leaseTTL,
		Heartbeat:   heartbeat,
	}, nil
}

func envDuration(key string, fallback time.Duration) (time.Duration, error) {
	v := os.Getenv(key)
	if v == "" {
		return fallback, nil
	}
	d, err := time.ParseDuration(v)
	if err != nil || d <= 0 {
		return 0, fmt.Errorf("%s %q: want a positive duration like 30s", key, v)
	}
	return d, nil
}

func envOr(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}
