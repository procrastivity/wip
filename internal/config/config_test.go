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
	// The shipped default (assets/config.default.yaml) declares no keys of
	// its own yet — chassis owns the mechanism, not any config key.
	if len(cfg) != 0 {
		t.Fatalf("cfg = %+v, want empty (no keys defined by the shipped default)", cfg)
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
