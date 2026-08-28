package cli_test

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"

	"github.com/procrastivity/wip/internal/tracker"
)

// The three factories root.go registers in production — the manifest's
// trackers list must name exactly them, in Registry.Names() (sorted) order.
func TestManifestListsRegisteredTrackers(t *testing.T) {
	r := run(t, nil, "manifest", "--json")
	if r.exitCode != 0 {
		t.Fatalf("exit code = %d, want 0; stderr=%q", r.exitCode, r.stderr)
	}

	var m struct {
		Trackers []string `json:"trackers"`
	}
	if err := json.Unmarshal([]byte(r.stdout), &m); err != nil {
		t.Fatalf("unmarshal: %v\nstdout=%s", err, r.stdout)
	}

	want := []string{"github", "gitlab", "linear"}
	if !reflect.DeepEqual(m.Trackers, want) {
		t.Errorf("Trackers = %#v, want %#v", m.Trackers, want)
	}
}

// The list is sourced from the injected registry, not a literal: a registry
// with only "fake" registered must produce a manifest naming only "fake".
func TestManifestTrackersComeFromTheInjectedRegistry(t *testing.T) {
	providers := tracker.NewRegistry()
	providers.Register("fake", func(tracker.FactoryInput) (tracker.Seam, error) { return nil, nil })

	r := runWithProviders(t, providers, "manifest", "--json")
	if r.exitCode != 0 {
		t.Fatalf("exit code = %d, want 0; stderr=%q", r.exitCode, r.stderr)
	}

	var m struct {
		Trackers []string `json:"trackers"`
	}
	if err := json.Unmarshal([]byte(r.stdout), &m); err != nil {
		t.Fatalf("unmarshal: %v\nstdout=%s", err, r.stdout)
	}

	want := []string{"fake"}
	if !reflect.DeepEqual(m.Trackers, want) {
		t.Errorf("Trackers = %#v, want %#v", m.Trackers, want)
	}
	for _, name := range m.Trackers {
		if name == "gitlab" {
			t.Errorf("Trackers = %#v, want it not to contain the production-only %q", m.Trackers, "gitlab")
		}
	}
}

func TestManifestHumanLineCountsTrackers(t *testing.T) {
	r := run(t, nil, "manifest")
	if r.exitCode != 0 {
		t.Fatalf("exit code = %d, want 0; stderr=%q", r.exitCode, r.stderr)
	}
	if !strings.Contains(r.stdout, "3 tracker(s)") {
		t.Fatalf("stdout = %q, want it to contain %q", r.stdout, "3 tracker(s)")
	}
}
