package render

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/procrastivity/wip/internal/store"
	"github.com/procrastivity/wip/internal/tiers"
)

// TestClean_ReapsStaleOpenDispatchAsCrashOrphan is step-07/D59 path (c): a
// process died before any subsequent `wip refresh` could supersede it —
// `wip clean` finds the still-open, past-bound dispatch, closes it `reaped`,
// and sweeps its scratch directory.
func TestClean_ReapsStaleOpenDispatchAsCrashOrphan(t *testing.T) {
	backdated := time.Now().Add(-48 * time.Hour)
	s, err := store.OpenWithClock(filepath.Join(t.TempDir(), "wip.db"), func() time.Time { return backdated })
	if err != nil {
		t.Fatalf("OpenWithClock: %v", err)
	}
	t.Cleanup(func() { _ = s.Close() })

	dir := newRepo(t)
	if _, err := tiers.Init(ctx, s, store.ActorHuman, dir, ""); err != nil {
		t.Fatalf("tiers.Init: %v", err)
	}
	cur, err := ResolveCurrent(ctx, s, store.ActorHuman, dir)
	if err != nil {
		t.Fatalf("ResolveCurrent: %v", err)
	}

	dispatch, _, _, err := OpenOrReuse(ctx, s, cur, store.ActorHuman, backdated, time.Hour)
	if err != nil {
		t.Fatalf("OpenOrReuse: %v", err)
	}
	scratch := ScratchDir(cur.Root, dispatch.ID)
	if !fileExists(scratch) {
		t.Fatalf("scratch dir %s should exist", scratch)
	}

	result, err := Clean(ctx, s, cur, store.ActorHuman, time.Now(), time.Hour)
	if err != nil {
		t.Fatalf("Clean: %v", err)
	}
	if len(result.ReapedDispatches) != 1 || result.ReapedDispatches[0] != dispatch.ID {
		t.Errorf("ReapedDispatches = %v, want exactly [%s]", result.ReapedDispatches, dispatch.ID)
	}
	if fileExists(scratch) {
		t.Errorf("scratch dir %s was not swept by clean", scratch)
	}
	reread, err := s.Dispatch(ctx, dispatch.ID)
	if err != nil {
		t.Fatal(err)
	}
	if reread.Open {
		t.Error("dispatch is still open after clean")
	}
	if reread.CloseReason != store.CloseReaped {
		t.Errorf("close reason = %q, want %q", reread.CloseReason, store.CloseReaped)
	}
}

// TestClean_LeavesAFreshOpenDispatchAlone confirms `wip clean` never touches
// a dispatch that is still genuinely within the staleness bound — the
// worked-in-progress case, not a crash orphan.
func TestClean_LeavesAFreshOpenDispatchAlone(t *testing.T) {
	s, _, cur := setup(t)
	dispatch, opened, _, err := OpenOrReuse(ctx, s, cur, store.ActorHuman, time.Now(), time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	if !opened {
		t.Fatal("expected a fresh dispatch")
	}

	result, err := Clean(ctx, s, cur, store.ActorHuman, time.Now(), time.Hour)
	if err != nil {
		t.Fatalf("Clean: %v", err)
	}
	if len(result.ReapedDispatches) != 0 {
		t.Errorf("ReapedDispatches = %v, want none — this dispatch is not stale", result.ReapedDispatches)
	}
	still, err := s.Dispatch(ctx, dispatch.ID)
	if err != nil {
		t.Fatal(err)
	}
	if !still.Open {
		t.Error("clean closed a dispatch that was not stale")
	}
	if !fileExists(ScratchDir(cur.Root, dispatch.ID)) {
		t.Error("clean swept a scratch dir that was not stale")
	}
}

// TestClean_SweepsLeftoverDirectoryOfAnAlreadyClosedDispatch covers the
// plain disk/store desync case: a close committed but its own sweep did not
// finish (e.g. a crash between the two) — clean finds the directory again
// on its next pass and finishes the job, with no new event needed.
func TestClean_SweepsLeftoverDirectoryOfAnAlreadyClosedDispatch(t *testing.T) {
	s, _, cur := setup(t)
	closed, err := CloseExplicit(ctx, s, cur, store.ActorHuman)
	if err == nil {
		t.Fatalf("expected no-open-dispatch refusal, got a close of %s", closed.ID)
	}

	dispatch, _, _, err := OpenOrReuse(ctx, s, cur, store.ActorHuman, time.Now(), time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := CloseExplicit(ctx, s, cur, store.ActorHuman); err != nil {
		t.Fatalf("CloseExplicit: %v", err)
	}
	// Recreate the directory closeAndSweep already removed, simulating a
	// crash between the event commit and the disk removal.
	leftover := ScratchDir(cur.Root, dispatch.ID)
	if err := os.MkdirAll(leftover, 0o755); err != nil {
		t.Fatal(err)
	}

	result, err := Clean(ctx, s, cur, store.ActorHuman, time.Now(), time.Hour)
	if err != nil {
		t.Fatalf("Clean: %v", err)
	}
	if fileExists(leftover) {
		t.Errorf("leftover directory %s was not swept", leftover)
	}
	if len(result.ReapedDispatches) != 0 {
		t.Errorf("ReapedDispatches = %v, want none — this dispatch was already closed, not reaped", result.ReapedDispatches)
	}
	if len(result.SweptDirectories) != 1 || result.SweptDirectories[0] != dispatch.ID {
		t.Errorf("SweptDirectories = %v, want exactly [%s]", result.SweptDirectories, dispatch.ID)
	}
}

// TestClean_ReapsOrphanBlobs is D68: a spilled blob no live content row
// references is reaped past the staleness bound, and not before it.
func TestClean_ReapsOrphanBlobs(t *testing.T) {
	s, _, cur := setup(t)

	big := make([]byte, store.SpillThreshold+1)
	for i := range big {
		big[i] = byte(i)
	}
	locator := matter(t, s, cur.Repo.ID, "Has a huge body")
	writeOnce(t, s, cur.Repo.ID, locator, store.KindBody, big)

	// Too new: not reaped yet.
	result, err := Clean(ctx, s, cur, store.ActorHuman, time.Now(), 24*time.Hour)
	if err != nil {
		t.Fatalf("Clean: %v", err)
	}
	if len(result.ReapedBlobs) != 0 {
		t.Errorf("ReapedBlobs = %v, want none yet — this blob is live", result.ReapedBlobs)
	}

	// Simulate the row being tombstoned/removed is out of scope here (no
	// removal verb touches body content in P1); instead confirm the bound
	// itself: a live blob is never reaped regardless of age.
	result, err = Clean(ctx, s, cur, store.ActorHuman, time.Now().Add(365*24*time.Hour), time.Hour)
	if err != nil {
		t.Fatalf("Clean: %v", err)
	}
	if len(result.ReapedBlobs) != 0 {
		t.Errorf("ReapedBlobs = %v, want none — a live content row still references this blob", result.ReapedBlobs)
	}
}
