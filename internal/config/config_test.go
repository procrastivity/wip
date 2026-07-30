package config_test

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/procrastivity/wip/internal/config"
)

func TestLoad_NoOverride_ReturnsShippedDefault(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())

	cfg, err := config.Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	// chassis owns the mechanism, not any config key; read-surface is the
	// first Matter to earn one — the default idle gap for `wip session`
	// (MODEL §2.4), documented next to the key in config.default.yaml.
	if got := cfg["idle_gap"]; got != "6h" {
		t.Fatalf(`cfg["idle_gap"] = %v, want "6h"`, got)
	}
	if len(cfg) != 1 {
		t.Fatalf("cfg = %+v, want exactly the one key the shipped default declares", cfg)
	}
}

func TestLoad_OverridePresent_MergesOverDefault(t *testing.T) {
	configHome := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", configHome)

	overrideDir := filepath.Join(configHome, "wip")
	if err := os.MkdirAll(overrideDir, 0o755); err != nil {
		t.Fatal(err)
	}
	overrideYAML := "example-key: from-override\n"
	if err := os.WriteFile(filepath.Join(overrideDir, "config.yaml"), []byte(overrideYAML), 0o644); err != nil {
		t.Fatal(err)
	}

	cfg, err := config.Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if got := cfg["example-key"]; got != "from-override" {
		t.Fatalf("cfg[%q] = %v, want %q", "example-key", got, "from-override")
	}
}
