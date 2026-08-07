package render

import (
	"context"
	"errors"
	"path/filepath"
	"testing"
	"time"

	"github.com/procrastivity/wip/internal/store"
	"github.com/procrastivity/wip/internal/tiers"
)

var errPreconditionRefused = errors.New("refused for test")

// TestRefresh_OpensDispatchThenReuses is step-02's Done: the first `wip
// refresh` in a Clone with no currently-open dispatch mints one; a second
// call re-renders without minting a new id.
func TestRefresh_OpensDispatchThenReuses(t *testing.T) {
	s, _, cur := setup(t)
	matter(t, s, cur.Repo.ID, "A Bugfix")

	first, err := Refresh(ctx, s, cur, store.ActorHuman, NoPrecondition)
	if err != nil {
		t.Fatalf("first Refresh: %v", err)
	}
	if !first.Opened {
		t.Error("first Refresh did not open a dispatch")
	}
	if first.DispatchID == "" {
		t.Error("first Refresh returned no dispatch id")
	}
	if !fileExists(first.ScratchDir) {
		t.Errorf("scratch dir %s was not created", first.ScratchDir)
	}

	second, err := Refresh(ctx, s, cur, store.ActorHuman, NoPrecondition)
	if err != nil {
		t.Fatalf("second Refresh: %v", err)
	}
	if second.Opened {
		t.Error("second Refresh minted a new dispatch instead of reusing the open one")
	}
	if second.DispatchID != first.DispatchID {
		t.Errorf("second Refresh dispatch = %s, want the same id %s", second.DispatchID, first.DispatchID)
	}
}

// TestRefresh_SupersedesStaleDispatch is D59 path (b): a stale open bracket
// is closed with reason superseded and swept before a new one opens.
//
// A dispatch's opened_at is derived from its own event id (timeOfID), which
// is derived from the store's own clock at mint time — so backdating it for
// this test means minting the first dispatch under a backdated clock, via
// store.OpenWithClock's injectable seam, rather than backdating the
// staleness check's `now` argument alone (which affects only the
// comparison, not what actually got recorded).
func TestRefresh_SupersedesStaleDispatch(t *testing.T) {
	current := time.Now().Add(-48 * time.Hour)
	s, err := store.OpenWithClock(filepath.Join(t.TempDir(), "wip.db"), func() time.Time { return current })
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
	matter(t, s, cur.Repo.ID, "A Bugfix")

	dispatch, opened, _, err := OpenOrReuse(ctx, s, cur, store.ActorHuman, current, time.Hour)
	if err != nil {
		t.Fatalf("OpenOrReuse (backdated): %v", err)
	}
	if !opened {
		t.Fatal("expected the first open to mint a dispatch")
	}
	staleScratch := ScratchDir(cur.Root, dispatch.ID)
	if !fileExists(staleScratch) {
		t.Fatalf("scratch dir %s was not created", staleScratch)
	}

	current = time.Now() // advance the store's own clock before the second call
	second, opened, superseded, err := OpenOrReuse(ctx, s, cur, store.ActorHuman, current, time.Hour)
	if err != nil {
		t.Fatalf("OpenOrReuse (fresh now): %v", err)
	}
	if !opened {
		t.Error("a stale open dispatch should have been superseded, minting a new one")
	}
	if superseded != dispatch.ID {
		t.Errorf("superseded = %q, want %q", superseded, dispatch.ID)
	}
	if second.ID == dispatch.ID {
		t.Error("the new dispatch has the same id as the superseded one")
	}
	closedOld, err := s.Dispatch(ctx, dispatch.ID)
	if err != nil {
		t.Fatal(err)
	}
	if closedOld.Open {
		t.Error("the stale dispatch is still open")
	}
	if closedOld.CloseReason != store.CloseSuperseded {
		t.Errorf("close reason = %q, want %q", closedOld.CloseReason, store.CloseSuperseded)
	}
	if fileExists(staleScratch) {
		t.Errorf("superseded dispatch's scratch dir %s was not swept", staleScratch)
	}
}

// TestDispatch_OpensWithoutBatchCreation preserves the P1 dispatch bracket;
// Batch creation belongs to the later scheduler path.
func TestDispatch_OpensWithoutBatchCreation(t *testing.T) {
	s, _, cur := setup(t)

	before, err := s.Events(ctx)
	if err != nil {
		t.Fatal(err)
	}

	dispatch, opened, _, err := OpenOrReuse(ctx, s, cur, store.ActorHuman, time.Now(), DefaultStaleBound)
	if err != nil {
		t.Fatalf("OpenOrReuse: %v", err)
	}
	if !opened {
		t.Fatal("expected a fresh dispatch to be opened")
	}

	after, err := s.Events(ctx)
	if err != nil {
		t.Fatal(err)
	}
	newEvents := after[len(before):]
	if len(newEvents) != 1 {
		t.Fatalf("dispatch open produced %d events, want exactly 1 (dispatch.opened)", len(newEvents))
	}
	if newEvents[0].Type != store.TypeDispatchOpened {
		t.Errorf("event = %s, want %s", newEvents[0].Type, store.TypeDispatchOpened)
	}
	if newEvents[0].Subject != dispatch.ID {
		t.Errorf("dispatch.opened subject = %s, want %s", newEvents[0].Subject, dispatch.ID)
	}
}

// TestRefresh_EmitsExactlyOneRenderPerformed is step-03's Done: each render
// pass emits exactly one render.performed execution event, attributed to
// the dispatch it belongs to.
func TestRefresh_EmitsExactlyOneRenderPerformed(t *testing.T) {
	s, _, cur := setup(t)
	matter(t, s, cur.Repo.ID, "A Bugfix")

	result, err := Refresh(ctx, s, cur, store.ActorHuman, NoPrecondition)
	if err != nil {
		t.Fatalf("Refresh: %v", err)
	}

	renderEvents, err := s.EventsOfSubject(ctx, result.DispatchID)
	if err != nil {
		t.Fatal(err)
	}
	var count int
	for _, ev := range renderEvents {
		if ev.Type == store.TypeRenderPerformed {
			count++
		}
	}
	if count != 1 {
		t.Errorf("render.performed fired %d times, want exactly 1", count)
	}
}

// TestClose_ExplicitSweepsAndRefusesWithNoOpenDispatch covers D59 path (a):
// the explicit dispatch-close verb, reason completed, and step-07's sweep.
func TestClose_ExplicitSweepsAndRefusesWithNoOpenDispatch(t *testing.T) {
	s, _, cur := setup(t)
	matter(t, s, cur.Repo.ID, "A Bugfix")

	result, err := Refresh(ctx, s, cur, store.ActorHuman, NoPrecondition)
	if err != nil {
		t.Fatalf("Refresh: %v", err)
	}
	if !fileExists(result.ScratchDir) {
		t.Fatalf("scratch dir %s should exist before close", result.ScratchDir)
	}

	closed, err := CloseExplicit(ctx, s, cur, store.ActorHuman)
	if err != nil {
		t.Fatalf("CloseExplicit: %v", err)
	}
	if closed.CloseReason != store.CloseCompleted {
		t.Errorf("close reason = %q, want %q", closed.CloseReason, store.CloseCompleted)
	}
	if fileExists(result.ScratchDir) {
		t.Errorf("scratch dir %s was not swept on explicit close", result.ScratchDir)
	}

	if _, err := CloseExplicit(ctx, s, cur, store.ActorHuman); err == nil {
		t.Error("closing again with no open dispatch should refuse, not succeed")
	}
}

// TestEagerScope_SkipsSealedMattersUnlessNamed is step-05's Done: eager
// coverage renders every not-sealed Matter and skips sealed ones; a sealed
// Matter renders only via an explicit locator.
func TestEagerScope_SkipsSealedMattersUnlessNamed(t *testing.T) {
	s, _, cur := setup(t)
	active := matter(t, s, cur.Repo.ID, "Still Working")
	sealedLocator := matter(t, s, cur.Repo.ID, "Long Done")
	seal(t, s, cur.Repo.ID, sealedLocator)

	scope, err := EagerScope(ctx, s, cur.Repo.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(scope) != 1 || scope[0].Locator != active {
		t.Fatalf("eager scope = %v, want exactly [%s]", scope, active)
	}

	result, err := Refresh(ctx, s, cur, store.ActorHuman, NoPrecondition)
	if err != nil {
		t.Fatalf("Refresh: %v", err)
	}
	sealedFile := filepath.Join(GeneratedDir(cur.Root), sealedLocator, "matter.md")
	if fileExists(sealedFile) {
		t.Errorf("eager Refresh rendered the sealed matter at %s", sealedFile)
	}
	activeFile := filepath.Join(GeneratedDir(cur.Root), active, "matter.md")
	if !fileExists(activeFile) {
		t.Errorf("eager Refresh did not render the active matter at %s", activeFile)
	}
	if len(result.Rendered) != 1 {
		t.Errorf("Refresh reported %d rendered matter(s), want 1", len(result.Rendered))
	}

	// Named explicitly, the sealed Matter renders on demand.
	if _, err := Render(ctx, s, cur, store.ActorHuman, sealedLocator, NoPrecondition); err != nil {
		t.Fatalf("Render(%s): %v", sealedLocator, err)
	}
	if !fileExists(sealedFile) {
		t.Errorf("Render(%s) did not produce %s", sealedLocator, sealedFile)
	}
}

// TestPrecondition_RunsBeforeAnyWrite is step-09's Done: the precondition
// hook is reached unconditionally, before any write to .wip/generated/ or
// .wip/work/ — a failing precondition leaves neither half touched.
func TestPrecondition_RunsBeforeAnyWrite(t *testing.T) {
	s, _, cur := setup(t)
	matter(t, s, cur.Repo.ID, "A Bugfix")

	refused := func(_ context.Context, _ Current) error { return errPreconditionRefused }
	if _, err := Refresh(ctx, s, cur, store.ActorHuman, refused); err == nil {
		t.Fatal("expected the precondition's refusal to propagate")
	}
	if fileExists(WipDir(cur.Root)) {
		t.Errorf(".wip/ was created despite a failing precondition")
	}
}
