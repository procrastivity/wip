package render

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/procrastivity/wip/internal/store"
)

// TestDepthPolicy_NoStagesRendersOneMatterFile is MODEL §3.1's first example:
// "a bugfix Matter with no Stages renders as one matter.md".
func TestDepthPolicy_NoStagesRendersOneMatterFile(t *testing.T) {
	s, _, cur := setup(t)
	locator := matter(t, s, cur.Repo.ID, "Fix the thing")

	if _, err := Refresh(ctx, s, cur, store.ActorHuman, NoPrecondition); err != nil {
		t.Fatalf("Refresh: %v", err)
	}

	dir := filepath.Join(GeneratedDir(cur.Root), locator)
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("read %s: %v", dir, err)
	}
	if len(entries) != 1 || entries[0].Name() != "matter.md" {
		var names []string
		for _, e := range entries {
			names = append(names, e.Name())
		}
		t.Errorf("generated files = %v, want exactly [matter.md]", names)
	}
}

// TestDepthPolicy_BriefStagesAndWorkplansRenderSeparateFiles is MODEL §3.1's
// second example: "a Matter with a Brief, Stages, and per-Stage Workplans
// renders as brief + roadmap + per-Stage workplan files."
func TestDepthPolicy_BriefStagesAndWorkplansRenderSeparateFiles(t *testing.T) {
	s, _, cur := setup(t)
	locator := matter(t, s, cur.Repo.ID, "A Feature")
	writeOnce(t, s, cur.Repo.ID, locator, store.KindBrief, []byte("# Brief\n\nwhy this exists\n"))
	stageLocator := stage(t, s, cur.Repo.ID, locator, "First Stage")
	writeOnce(t, s, cur.Repo.ID, locator+"/"+stageLocator, store.KindWorkplan, []byte("# Workplan\n\nsteps go here\n"))

	if _, err := Refresh(ctx, s, cur, store.ActorHuman, NoPrecondition); err != nil {
		t.Fatalf("Refresh: %v", err)
	}

	dir := filepath.Join(GeneratedDir(cur.Root), locator)
	for _, want := range []string{"matter.md", "brief.md", "roadmap.md", "workplan-" + stageLocator + ".md"} {
		if !fileExists(filepath.Join(dir, want)) {
			t.Errorf("expected generated file %s not found", want)
		}
	}

	brief, err := os.ReadFile(filepath.Join(dir, "brief.md"))
	if err != nil {
		t.Fatal(err)
	}
	if string(brief) != "# Brief\n\nwhy this exists\n" {
		t.Errorf("brief.md = %q, want the stored Brief content verbatim", brief)
	}
}

// TestWrite_GeneratedFilesAre0444 is step-04's Done: every file under
// `.wip/generated/` is 0444 immediately after render, and a direct write
// attempt against one fails at the OS level.
func TestWrite_GeneratedFilesAre0444(t *testing.T) {
	s, _, cur := setup(t)
	locator := matter(t, s, cur.Repo.ID, "Fix the thing")

	if _, err := Refresh(ctx, s, cur, store.ActorHuman, NoPrecondition); err != nil {
		t.Fatalf("Refresh: %v", err)
	}

	path := filepath.Join(GeneratedDir(cur.Root), locator, "matter.md")
	if mode := fileMode(t, path); mode != 0o444 {
		t.Errorf("mode of %s = %o, want 0444", path, mode)
	}
	if err := os.WriteFile(path, []byte("tampered"), 0o644); err == nil {
		t.Errorf("a direct write against %s succeeded; want an OS-level permission refusal", path)
	} else if !os.IsPermission(err) {
		t.Errorf("write against %s failed with %v, want a permission error", path, err)
	}
}

// TestWrite_RerenderReopensAndRestores0444 confirms this package's own
// render pass — the one legitimate writer — can still regenerate a file it
// previously wrote 0444, restoring 0444 afterward.
func TestWrite_RerenderReopensAndRestores0444(t *testing.T) {
	s, _, cur := setup(t)
	locator := matter(t, s, cur.Repo.ID, "Fix the thing")

	if _, err := Refresh(ctx, s, cur, store.ActorHuman, NoPrecondition); err != nil {
		t.Fatalf("first Refresh: %v", err)
	}
	if _, err := Refresh(ctx, s, cur, store.ActorHuman, NoPrecondition); err != nil {
		t.Fatalf("second Refresh: %v", err)
	}

	path := filepath.Join(GeneratedDir(cur.Root), locator, "matter.md")
	if mode := fileMode(t, path); mode != 0o444 {
		t.Errorf("mode of %s after re-render = %o, want 0444", path, mode)
	}
}
