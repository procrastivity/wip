package store

import (
	"strings"
	"testing"
)

func TestTrackerBacklogPushDefaultsToManualEvenWithABackend(t *testing.T) {
	h := newHarness(t)
	want := func(expected TrackerBacklogPush) {
		t.Helper()
		got, err := h.EffectiveTrackerBacklogPush(h.ctx, h.Repo)
		if err != nil || got != expected {
			t.Fatalf("effective tracker backlog-push = %q, err=%v, want %q", got, err, expected)
		}
	}
	want(TrackerBacklogPushManual)
	if err := h.SetConfig(h.ctx, h.Repo, TrackerBackendKey, "configured"); err != nil {
		t.Fatal(err)
	}
	want(TrackerBacklogPushManual)
	if _, err := h.SetTrackerBacklogPush(h.ctx, h.Repo, "auto"); err != nil {
		t.Fatal(err)
	}
	want(TrackerBacklogPushAuto)
	if _, err := h.SetTrackerBacklogPush(h.ctx, h.Repo, "manual"); err != nil {
		t.Fatal(err)
	}
	want(TrackerBacklogPushManual)
	if _, err := h.SetTrackerBacklogPush(h.ctx, h.Repo, "always"); err == nil {
		t.Fatal("invalid tracker backlog-push mode succeeded")
	} else if !strings.Contains(err.Error(), "expected manual or auto") {
		t.Fatalf("error = %q, want it to contain %q", err.Error(), "expected manual or auto")
	}
	want(TrackerBacklogPushManual)
}

func TestTrackerBacklogPushIsRepoTierConfigAndQueuesNothing(t *testing.T) {
	h := newHarness(t)
	before, err := h.Events(h.ctx)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := h.SetTrackerBacklogPush(h.ctx, h.Repo, "auto"); err != nil {
		t.Fatal(err)
	}
	after, err := h.Events(h.ctx)
	if err != nil {
		t.Fatal(err)
	}
	// SetTrackerBacklogPush delegates to SetConfig, an ordinary verb since v13:
	// exactly one config.set, and nothing beyond it (no candidate queued).
	if len(after) != len(before)+1 {
		t.Fatalf("config change appended %d event(s), want exactly one config.set", len(after)-len(before))
	}
	clone := h.attachClone(CloneAttached{Repo: h.Repo, GitCommonDir: "/tmp/other/.git", Label: "other"})
	_ = clone
	got, err := h.EffectiveTrackerBacklogPush(h.ctx, h.Repo)
	if err != nil || got != TrackerBacklogPushAuto {
		t.Fatalf("other clone's Repo backlog-push = %q, err=%v", got, err)
	}
	entries, err := h.Outbox(h.ctx, h.Repo)
	if err != nil || len(entries) != 0 {
		t.Fatalf("config change queued outbox = %+v, err=%v", entries, err)
	}
	backlog, err := h.Backlog(h.ctx, h.Repo)
	if err != nil || len(backlog) != 0 {
		t.Fatalf("config change entered backlog = %+v, err=%v", backlog, err)
	}
}
