package config_test

import (
	"encoding/json"
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
	// chassis owns the mechanism, not any config key; read-surface earned
	// the first one — the default idle gap for `wip session` (MODEL
	// §2.4) — and scheduler added the second — the default Run cap `wip
	// next`'s frontier display uses (G4/D31). Explicit sync adds only empty
	// optional connection/routing definitions, never an implicit destination.
	if got := cfg["idle_gap"]; got != "6h" {
		t.Fatalf(`cfg["idle_gap"] = %v, want "6h"`, got)
	}
	if got := cfg["run_cap"]; got != 1 {
		t.Fatalf(`cfg["run_cap"] = %v, want 1`, got)
	}
	var sync struct {
		Connections map[string]any
		Routes      []any
	}
	raw, err := json.Marshal(cfg["sync"])
	if err != nil || json.Unmarshal(raw, &sync) != nil || sync.Connections == nil || sync.Routes == nil || len(sync.Connections) != 0 || len(sync.Routes) != 0 {
		t.Fatal("sync defaults must not select or route to any server")
	}
	if len(cfg) != 3 {
		t.Fatalf("cfg = %+v, want exactly the three keys the shipped default declares", cfg)
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
