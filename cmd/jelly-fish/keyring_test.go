package main

import (
	"log/slog"
	"strings"
	"testing"

	"github.com/bhanuprakaash/jelly-fish/internal/config"
)

func TestRolesRefuseToStartWithoutAUsableMasterKey(t *testing.T) {
	tests := []struct {
		name, masterKey, want string
	}{
		{"missing", "", "JF_MASTER_KEY is required"},
		{"malformed", "m1:not-base64!", "JF_MASTER_KEY: "},
		{"wrong length", "m1:c2hvcnQ=", "JF_MASTER_KEY: "},
	}
	for _, role := range []string{"api", "worker", "keys"} {
		for _, tt := range tests {
			t.Run(role+" "+tt.name, func(t *testing.T) {
				args := []string{}
				if role == "keys" {
					args = []string{"rotate"}
				}
				err := run(role, args, config.Config{MasterKey: tt.masterKey}, slog.New(slog.DiscardHandler))
				if err == nil || !strings.Contains(err.Error(), tt.want) {
					t.Fatalf("run error = %v, want it to contain %q", err, tt.want)
				}
			})
		}
	}
}

func TestKeysRequiresRotate(t *testing.T) {
	if err := run("keys", nil, config.Config{}, slog.New(slog.DiscardHandler)); err == nil || !strings.Contains(err.Error(), "usage") {
		t.Fatalf("error = %v, want usage", err)
	}
}
