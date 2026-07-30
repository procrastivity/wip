package render

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/procrastivity/wip/internal/store"
)

// TestD33_DeleteWipBetweenDispatchesLosesNothing is the seal condition's
// centerpiece and MODEL §3.1's own test, passed literally: a dispatch opens,
// renders, and an agent writes to `.wip/work/`; `.wip/` is deleted wholesale
// between dispatches (simulating a fresh clone or a wiped worktree); the
// next dispatch opens and renders cleanly with no loss — every fact
// `status`/`next` could report is unaffected, because none of it lived in
// `.wip/` (D33 as amended).
func TestD33_DeleteWipBetweenDispatchesLosesNothing(t *testing.T) {
	s, _, cur := setup(t)

	locator := matter(t, s, cur.Repo.ID, "Durable Work")
	writeOnce(t, s, cur.Repo.ID, locator, store.KindBrief, []byte("# Brief\n\nthe durable why\n"))
	stageLocator := stage(t, s, cur.Repo.ID, locator, "Only Stage")
	writeOnce(t, s, cur.Repo.ID, locator+"/"+stageLocator, store.KindWorkplan, []byte("# Workplan\n\nthe durable how\n"))

	first, err := Refresh(ctx, s, cur, store.ActorHuman, NoPrecondition)
	if err != nil {
		t.Fatalf("first Refresh: %v", err)
	}

	// The agent drops scratch content — inert, per D45, but present.
	scratchFile := filepath.Join(first.ScratchDir, "draft.md")
	if err := os.WriteFile(scratchFile, []byte("an agent's draft, never durable"), 0o644); err != nil {
		t.Fatal(err)
	}

	if _, err := CloseExplicit(ctx, s, cur, store.ActorHuman); err != nil {
		t.Fatalf("CloseExplicit: %v", err)
	}

	// Simulate a fresh clone or a wiped worktree: delete .wip/ wholesale.
	if err := os.RemoveAll(WipDir(cur.Root)); err != nil {
		t.Fatalf("removing %s: %v", WipDir(cur.Root), err)
	}
	if fileExists(WipDir(cur.Root)) {
		t.Fatal(".wip/ still exists after RemoveAll — test setup is broken")
	}

	// The next dispatch opens and renders cleanly, with no loss.
	second, err := Refresh(ctx, s, cur, store.ActorHuman, NoPrecondition)
	if err != nil {
		t.Fatalf("second Refresh after deleting .wip/: %v", err)
	}
	if !second.Opened {
		t.Error("expected a fresh dispatch to open — the prior one was explicitly closed")
	}
	if second.DispatchID == first.DispatchID {
		t.Error("second dispatch reused the first's id; it should have been closed and gone")
	}

	dir := filepath.Join(GeneratedDir(cur.Root), locator)
	brief, err := os.ReadFile(filepath.Join(dir, "brief.md"))
	if err != nil {
		t.Fatalf("brief.md did not survive the delete-and-refresh cycle: %v", err)
	}
	if string(brief) != "# Brief\n\nthe durable why\n" {
		t.Errorf("brief.md = %q, want the durable Brief content unchanged", brief)
	}
	workplan, err := os.ReadFile(filepath.Join(dir, "workplan-"+stageLocator+".md"))
	if err != nil {
		t.Fatalf("the Stage's workplan file did not survive: %v", err)
	}
	if string(workplan) != "# Workplan\n\nthe durable how\n" {
		t.Errorf("workplan file = %q, want the durable Workplan content unchanged", workplan)
	}
	if !fileExists(filepath.Join(dir, "roadmap.md")) {
		t.Error("roadmap.md did not survive the delete-and-refresh cycle")
	}

	// The scratch draft is gone — it was never durable, and nothing lost by
	// its absence: it was inert from the moment it was written (D45), never
	// read as state, never a source anything else depended on.
	if fileExists(scratchFile) {
		t.Error("the old scratch file exists after the .wip/ delete — it was supposed to be gone")
	}

	// The store's own account of this Matter is untouched by any of this —
	// exactly the property that makes the delete safe.
	node, err := s.MatterByLocator(ctx, cur.Repo.ID, locator)
	if err != nil {
		t.Fatalf("the Matter itself did not survive in the store: %v", err)
	}
	if node.Title != "Durable Work" {
		t.Errorf("Matter title = %q, want %q", node.Title, "Durable Work")
	}
	storedBrief, err := s.Content(ctx, node.ID, store.KindBrief)
	if err != nil {
		t.Fatalf("reading the Brief back from the store: %v", err)
	}
	if string(storedBrief) != "# Brief\n\nthe durable why\n" {
		t.Errorf("store's own Brief = %q, want it unchanged by any of this", storedBrief)
	}
}
