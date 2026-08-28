package manifest_test

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/procrastivity/wip/internal/buildinfo"
	"github.com/procrastivity/wip/internal/manifest"
	"github.com/procrastivity/wip/internal/tracker"
)

func TestBuild_TrackersDefaultsToEmptyList(t *testing.T) {
	m, err := manifest.Build(fakeRoot(t), buildinfo.Info{})
	if err != nil {
		t.Fatalf("Build: %v", err)
	}

	if m.Trackers == nil || len(m.Trackers) != 0 {
		t.Fatalf("Trackers = %#v, want a non-nil empty slice", m.Trackers)
	}

	b, err := json.Marshal(m)
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}
	if !strings.Contains(string(b), `"trackers":[]`) {
		t.Errorf("marshaled manifest = %s, want it to contain %q", b, `"trackers":[]`)
	}
}

func TestBuild_WithTrackersRecordsNamesFromRegistry(t *testing.T) {
	fakeFactory := func(tracker.FactoryInput) (tracker.Seam, error) { return nil, nil }

	providers := tracker.NewRegistry()
	providers.Register("zeta-fixture", fakeFactory)
	providers.Register("alpha-fixture", fakeFactory)

	m, err := manifest.Build(fakeRoot(t), buildinfo.Info{}, manifest.WithTrackers(providers.Names()))
	if err != nil {
		t.Fatalf("Build: %v", err)
	}

	want := []string{"alpha-fixture", "zeta-fixture"}
	if len(m.Trackers) != len(want) {
		t.Fatalf("Trackers = %#v, want %#v", m.Trackers, want)
	}
	for i, name := range want {
		if m.Trackers[i] != name {
			t.Errorf("Trackers[%d] = %q, want %q", i, m.Trackers[i], name)
		}
	}
}

func TestBuild_WithTrackersCopiesTheSlice(t *testing.T) {
	names := []string{"a"}

	m, err := manifest.Build(fakeRoot(t), buildinfo.Info{}, manifest.WithTrackers(names))
	if err != nil {
		t.Fatalf("Build: %v", err)
	}

	names[0] = "mutated"

	if m.Trackers[0] != "a" {
		t.Errorf("Trackers[0] = %q after caller mutation, want %q (Build must copy)", m.Trackers[0], "a")
	}
}
