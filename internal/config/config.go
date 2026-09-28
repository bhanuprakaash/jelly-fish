// Package config loads process configuration from the environment.
package config

import (
	"errors"
	"os"
)

// Config holds env-derived settings shared by all roles.
type Config struct {
	DatabaseURL string
	APIAddr     string
	HealthAddr  string
}

// Load reads Config from the environment.
func Load() (Config, error) {
	databaseURL := os.Getenv("DATABASE_URL")
	if databaseURL == "" {
		return Config{}, errors.New("DATABASE_URL is required")
	}
	return Config{
		DatabaseURL: databaseURL,
		APIAddr:     envOr("API_ADDR", ":8080"),
		HealthAddr:  envOr("HEALTH_ADDR", ":9090"),
	}, nil
}

func envOr(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}
