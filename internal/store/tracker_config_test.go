package store

import "testing"

func TestTrackerPushLevelResolutionAndValidation(t *testing.T) {
	h := newHarness(t)
	want := func(expected TrackerPushLevel) {
		t.Helper()
		got, err := h.EffectiveTrackerPushLevel(h.ctx, h.Repo)
		if err != nil || got != expected {
			t.Fatalf("effective tracker level = %q, err=%v, want %q", got, err, expected)
		}
	}
	want(TrackerPushOff)
	if err := h.SetConfig(h.ctx, h.Repo, TrackerBackendKey, "configured"); err != nil {
		t.Fatal(err)
	}
	want(TrackerPushBoundary)
	if _, err := h.SetTrackerPushLevel(h.ctx, h.Repo, "off"); err != nil {
		t.Fatal(err)
	}
	want(TrackerPushOff)
	if _, err := h.SetTrackerPushLevel(h.ctx, h.Repo, "narrated"); err != nil {
		t.Fatal(err)
	}
	want(TrackerPushNarrated)
	if _, err := h.SetTrackerPushLevel(h.ctx, h.Repo, "loud"); err == nil {
		t.Fatal("invalid tracker push level succeeded")
	}
}

func TestTrackerPushLevelIsRepoTierConfigAndEmitsNoEvent(t *testing.T) {
	h := newHarness(t)
	before, err := h.Events(h.ctx)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := h.SetTrackerPushLevel(h.ctx, h.Repo, "boundary"); err != nil {
		t.Fatal(err)
	}
	after, err := h.Events(h.ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(after) != len(before) {
		t.Fatalf("config change appended %d event(s)", len(after)-len(before))
	}
	clone := h.attachClone(CloneAttached{Repo: h.Repo, GitCommonDir: "/tmp/other/.git", Label: "other"})
	_ = clone
	got, err := h.EffectiveTrackerPushLevel(h.ctx, h.Repo)
	if err != nil || got != TrackerPushBoundary {
		t.Fatalf("other clone's Repo level = %q, err=%v", got, err)
	}
	entries, err := h.Outbox(h.ctx, h.Repo)
	if err != nil || len(entries) != 0 {
		t.Fatalf("config change queued outbox = %+v, err=%v", entries, err)
	}
}
