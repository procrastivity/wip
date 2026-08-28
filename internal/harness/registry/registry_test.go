package registry_test

import (
	"testing"

	"github.com/procrastivity/wip/internal/harness/claudecode"
	"github.com/procrastivity/wip/internal/harness/codex"
	"github.com/procrastivity/wip/internal/harness/devin"
	"github.com/procrastivity/wip/internal/harness/opencode"
	"github.com/procrastivity/wip/internal/harness/pi"
	"github.com/procrastivity/wip/internal/harness/registry"
)

func TestAll_OrderAndCompleteness(t *testing.T) {
	want := []string{claudecode.Name, codex.Name, devin.Name, pi.Name, opencode.Name}
	if len(registry.All) != len(want) {
		t.Fatalf("len(All) = %d, want %d", len(registry.All), len(want))
	}
	for i, name := range want {
		if registry.All[i].Name != name {
			t.Errorf("All[%d].Name = %q, want %q", i, registry.All[i].Name, name)
		}
	}
}

func TestNames_MatchesAll(t *testing.T) {
	if len(registry.Names) != len(registry.All) {
		t.Fatalf("len(Names) = %d, len(All) = %d", len(registry.Names), len(registry.All))
	}
	for i, h := range registry.All {
		if registry.Names[i] != h.Name {
			t.Errorf("Names[%d] = %q, want %q (All[%d].Name)", i, registry.Names[i], h.Name, i)
		}
	}
}

func TestLookup_FindsEachRegisteredHarness(t *testing.T) {
	for _, name := range registry.Names {
		h, ok := registry.Lookup(name)
		if !ok {
			t.Errorf("Lookup(%q) ok = false, want true", name)
			continue
		}
		if h.Name != name {
			t.Errorf("Lookup(%q).Name = %q, want %q", name, h.Name, name)
		}
	}
}

func TestLookup_MissesUnknownName(t *testing.T) {
	if h, ok := registry.Lookup("no-such-harness"); ok {
		t.Errorf("Lookup(unknown) = %+v, ok = true, want ok = false", h)
	}
}

func TestAll_NoNilFunctionFields(t *testing.T) {
	for _, h := range registry.All {
		if h.InstallDir == nil {
			t.Errorf("%s: InstallDir is nil", h.Name)
		}
		if h.Generate == nil {
			t.Errorf("%s: Generate is nil", h.Name)
		}
		if h.Install == nil {
			t.Errorf("%s: Install is nil", h.Name)
		}
		if h.Uninstall == nil {
			t.Errorf("%s: Uninstall is nil", h.Name)
		}
		if h.Available == nil {
			t.Errorf("%s: Available is nil", h.Name)
		}
	}
}
