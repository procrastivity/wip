package readsurface

// Tests for step-03's recency policy on top of Content: sealed Matters older
// than recentSealedWindow are hidden from the default finished listing and
// counted into RepoContent.HiddenSealedMatters, and ContentOptions.All
// restores everything (D51-adjacent: presentation-only, no state change).

import (
	"testing"
	"time"

	"github.com/procrastivity/wip/internal/store"
)

// sealMatter births, starts and finishes a bare Matter (no gates declared,
// so Done alone seals it — D62) and returns its ID.
func (f *fixture) sealMatter(locator, title string) string {
	m := f.matter(locator, title)
	f.start(m)
	f.finish(m)
	return m
}

func TestContent_HidesSealedMatterOlderThanTheRecencyWindow(t *testing.T) {
	f := newFixture(t)
	old := f.sealMatter("old", "Sealed long ago")
	now := time.Now()

	c, err := Content(ctx, f.View, f.Repo, ContentOptions{Now: now.Add(15 * 24 * time.Hour)})
	if err != nil {
		t.Fatal(err)
	}
	for _, fin := range c.Finished {
		if fin.Node.ID == old {
			t.Errorf("finished = %+v, want the old sealed Matter %s hidden", c.Finished, old)
		}
	}
	if c.HiddenSealedMatters != 1 {
		t.Errorf("HiddenSealedMatters = %d, want 1", c.HiddenSealedMatters)
	}
}

func TestContent_ShowsSealedMatterWithinTheRecencyWindow(t *testing.T) {
	f := newFixture(t)
	recent := f.sealMatter("recent", "Sealed a moment ago")
	now := time.Now()

	c, err := Content(ctx, f.View, f.Repo, ContentOptions{Now: now.Add(13 * 24 * time.Hour)})
	if err != nil {
		t.Fatal(err)
	}
	var found bool
	for _, fin := range c.Finished {
		if fin.Node.ID == recent {
			found = true
		}
	}
	if !found {
		t.Errorf("finished = %+v, want the recently sealed Matter %s present", c.Finished, recent)
	}
	if c.HiddenSealedMatters != 0 {
		t.Errorf("HiddenSealedMatters = %d, want 0", c.HiddenSealedMatters)
	}
}

func TestContentTreatsAnAllDismissedMatterAsSealed(t *testing.T) {
	f := newFixture(t)
	f.declareGate("reviewed-local", store.ScaleMatter)
	m := f.matter("dismissed", "Dismissed matter")
	f.start(m)
	f.finish(m)
	f.dismissGate(m, "reviewed-local", store.ScaleMatter, "the verifier is unavailable")

	before, err := f.Events(ctx)
	if err != nil {
		t.Fatal(err)
	}
	c, err := Content(ctx, f.View, f.Repo, ContentOptions{All: true})
	if err != nil {
		t.Fatal(err)
	}
	if len(c.Finished) != 1 || c.Finished[0].Node.ID != m || !c.Finished[0].Sealed || len(c.Finished[0].Pending) != 0 {
		t.Fatalf("content = %+v, want dismissed Matter in the sealed finished set with no pending gates", c)
	}
	after, err := f.Events(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(after) != len(before) {
		t.Fatalf("Content changed the event log from %d to %d events", len(before), len(after))
	}
}

func TestContent_AllRestoresEverythingAndZeroesTheCount(t *testing.T) {
	f := newFixture(t)
	old := f.sealMatter("old", "Sealed long ago")
	now := time.Now()

	c, err := Content(ctx, f.View, f.Repo, ContentOptions{All: true, Now: now.Add(15 * 24 * time.Hour)})
	if err != nil {
		t.Fatal(err)
	}
	var found bool
	for _, fin := range c.Finished {
		if fin.Node.ID == old {
			found = true
		}
	}
	if !found {
		t.Errorf("finished = %+v, want the old sealed Matter %s present under All", c.Finished, old)
	}
	if c.HiddenSealedMatters != 0 {
		t.Errorf("HiddenSealedMatters = %d, want 0 under All", c.HiddenSealedMatters)
	}
}

// TestContent_CursorExemptFromCollapseAndRecency: the cursor node and its
// ancestors are never collapsed away or recency-hidden — the one orientation
// mark status draws stays visible, however old the sealed Matter around it.
func TestContent_CursorExemptFromCollapseAndRecency(t *testing.T) {
	f := newFixture(t)
	m := f.matter("m", "Sealed long ago")
	step := f.step(m, "step-01", "Its step")
	f.start(m)
	f.start(step)
	f.finish(step)
	f.finish(m)
	now := time.Now()

	c, err := Content(ctx, f.View, f.Repo, ContentOptions{Now: now.Add(365 * 24 * time.Hour), Cursor: step})
	if err != nil {
		t.Fatal(err)
	}
	var foundStep, foundMatter bool
	for _, fin := range c.Finished {
		if fin.Node.ID == step {
			foundStep = true
		}
		if fin.Node.ID == m {
			foundMatter = true
		}
	}
	if !foundStep || !foundMatter {
		t.Errorf("finished = %+v, want both cursor step %s and its Matter %s present", c.Finished, step, m)
	}
	if c.HiddenSealedMatters != 0 {
		t.Errorf("HiddenSealedMatters = %d, want 0 — the cursor's Matter is exempt", c.HiddenSealedMatters)
	}
}

// TestContent_SealedStageInOpenMatterNeverHiddenByRecency: recency hiding
// applies to sealed Matters only — a sealed Stage inside a still-open Matter
// is current context and stays, however old.
func TestContent_SealedStageInOpenMatterNeverHiddenByRecency(t *testing.T) {
	f := newFixture(t)
	m := f.matter("m", "Still open")
	stage := f.stage(m, "stage-a", "A Stage")
	f.start(m)
	f.start(stage)
	f.finish(stage)
	now := time.Now()

	c, err := Content(ctx, f.View, f.Repo, ContentOptions{Now: now.Add(365 * 24 * time.Hour)})
	if err != nil {
		t.Fatal(err)
	}
	var found bool
	for _, fin := range c.Finished {
		if fin.Node.ID == stage {
			found = true
		}
	}
	if !found {
		t.Errorf("finished = %+v, want the sealed Stage %s present regardless of age", c.Finished, stage)
	}
	if c.HiddenSealedMatters != 0 {
		t.Errorf("HiddenSealedMatters = %d, want 0 — only sealed Matters are hidden", c.HiddenSealedMatters)
	}
}
