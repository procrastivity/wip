package render

import (
	"bytes"
	"os"
	"path/filepath"
	"sort"
	"testing"

	"github.com/procrastivity/wip/internal/store"
	"github.com/procrastivity/wip/internal/writesurface"
)

func step(t *testing.T, s *store.Store, repo, parent, title string) string {
	t.Helper()
	n, err := writesurface.CreateStep(ctx, s, store.ActorHuman, repo, parent, title)
	if err != nil {
		t.Fatalf("CreateStep: %v", err)
	}
	return n.Locator
}

func generatedNames(t *testing.T, dir string) []string {
	t.Helper()
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("read generated directory: %v", err)
	}
	names := make([]string, 0, len(entries))
	for _, entry := range entries {
		names = append(names, entry.Name())
	}
	sort.Strings(names)
	return names
}

func TestStepWorkplansRenderDirectAndGroupedVerbatim(t *testing.T) {
	s, _, cur := setup(t)
	matterLocator := matter(t, s, cur.Repo.ID, "Step projection")
	stageLocator := stage(t, s, cur.Repo.ID, matterLocator, "Investigation")
	directLocator := step(t, s, cur.Repo.ID, matterLocator, "Direct step")
	groupedLocator := step(t, s, cur.Repo.ID, matterLocator+"/"+stageLocator, "Grouped step")
	emptyLocator := step(t, s, cur.Repo.ID, matterLocator, "Empty step")
	noContentLocator := step(t, s, cur.Repo.ID, matterLocator, "No content")

	writeOnce(t, s, cur.Repo.ID, matterLocator, store.KindBrief, []byte("brief\r\n"))
	writeOnce(t, s, cur.Repo.ID, matterLocator, store.KindWorkplan, []byte("matter workplan"))
	writeOnce(t, s, cur.Repo.ID, matterLocator+"/"+stageLocator, store.KindWorkplan, []byte("stage workplan"))
	directBytes := []byte("  direct\r\nstep\x00bytes")
	groupedBytes := []byte("grouped\n  bytes  ")
	writeOnce(t, s, cur.Repo.ID, matterLocator+"/"+directLocator, store.KindWorkplan, directBytes)
	writeOnce(t, s, cur.Repo.ID, matterLocator+"/"+stageLocator+"/"+groupedLocator, store.KindWorkplan, groupedBytes)
	writeOnce(t, s, cur.Repo.ID, matterLocator+"/"+emptyLocator, store.KindWorkplan, []byte{})

	if _, err := Refresh(ctx, s, cur, store.ActorHuman, NoPrecondition); err != nil {
		t.Fatalf("Refresh: %v", err)
	}

	dir := filepath.Join(GeneratedDir(cur.Root), matterLocator)
	wantNames := []string{
		"brief.md",
		"matter.md",
		"roadmap.md",
		"workplan.md",
		"workplan-" + stageLocator + ".md",
		"workplan-" + directLocator + ".md",
		"workplan-" + groupedLocator + ".md",
		"workplan-" + emptyLocator + ".md",
	}
	sort.Strings(wantNames)
	if got := generatedNames(t, dir); !slicesEqual(got, wantNames) {
		t.Fatalf("generated names = %v, want %v", got, wantNames)
	}
	if fileExists(filepath.Join(dir, stageLocator)) {
		t.Fatal("render created a Stage directory")
	}

	for locator, want := range map[string][]byte{
		directLocator:  directBytes,
		groupedLocator: groupedBytes,
		emptyLocator:   {},
	} {
		path := filepath.Join(dir, "workplan-"+locator+".md")
		got, err := os.ReadFile(path)
		if err != nil {
			t.Fatalf("read %s: %v", path, err)
		}
		if !bytes.Equal(got, want) {
			t.Errorf("%s = %q, want exact stored bytes %q", path, got, want)
		}
		if mode := fileMode(t, path); mode != 0o444 {
			t.Errorf("%s mode = %o, want 0444", path, mode)
		}
	}
	if fileExists(filepath.Join(dir, "workplan-"+noContentLocator+".md")) {
		t.Error("Step without Workplan content rendered a file")
	}
	for _, name := range []string{"workplan.md", "workplan-" + stageLocator + ".md"} {
		if mode := fileMode(t, filepath.Join(dir, name)); mode != 0o444 {
			t.Errorf("existing path %s mode = %o, want 0444", name, mode)
		}
	}
}

func TestStepWorkplansRenderFromEveryLocatorAndBareRefresh(t *testing.T) {
	s, _, cur := setup(t)
	matterLocator := matter(t, s, cur.Repo.ID, "Locator routing")
	stageLocator := stage(t, s, cur.Repo.ID, matterLocator, "Grouped")
	directLocator := step(t, s, cur.Repo.ID, matterLocator, "Direct")
	groupedLocator := step(t, s, cur.Repo.ID, matterLocator+"/"+stageLocator, "Grouped step")
	directBytes := []byte("direct")
	groupedBytes := []byte("grouped")
	writeOnce(t, s, cur.Repo.ID, matterLocator+"/"+directLocator, store.KindWorkplan, directBytes)
	writeOnce(t, s, cur.Repo.ID, matterLocator+"/"+stageLocator+"/"+groupedLocator, store.KindWorkplan, groupedBytes)

	locators := []string{
		matterLocator,
		matterLocator + "/" + stageLocator,
		matterLocator + "/" + directLocator,
		matterLocator + "/" + stageLocator + "/" + groupedLocator,
	}
	for _, locator := range locators {
		if _, err := Render(ctx, s, cur, store.ActorHuman, locator, NoPrecondition); err != nil {
			t.Fatalf("Render(%s): %v", locator, err)
		}
		assertStepBytes(t, cur, matterLocator, directLocator, directBytes)
		assertStepBytes(t, cur, matterLocator, groupedLocator, groupedBytes)
	}
	if _, err := Refresh(ctx, s, cur, store.ActorHuman, NoPrecondition); err != nil {
		t.Fatalf("bare Refresh: %v", err)
	}
	assertStepBytes(t, cur, matterLocator, directLocator, directBytes)
	assertStepBytes(t, cur, matterLocator, groupedLocator, groupedBytes)
}

func assertStepBytes(t *testing.T, cur Current, matterLocator, stepLocator string, want []byte) {
	t.Helper()
	path := filepath.Join(GeneratedDir(cur.Root), matterLocator, "workplan-"+stepLocator+".md")
	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	if !bytes.Equal(got, want) {
		t.Errorf("%s = %q, want %q", path, got, want)
	}
	if mode := fileMode(t, path); mode != 0o444 {
		t.Errorf("%s mode = %o, want 0444", path, mode)
	}
}

func TestStepWorkplansReconstructAfterWipRemoval(t *testing.T) {
	s, _, cur := setup(t)
	matterLocator := matter(t, s, cur.Repo.ID, "Reconstruct steps")
	stageLocator := stage(t, s, cur.Repo.ID, matterLocator, "Stage")
	directLocator := step(t, s, cur.Repo.ID, matterLocator, "Direct")
	groupedLocator := step(t, s, cur.Repo.ID, matterLocator+"/"+stageLocator, "Grouped")
	directBytes := []byte("direct\r\n")
	groupedBytes := []byte("grouped\x00bytes")
	writeOnce(t, s, cur.Repo.ID, matterLocator+"/"+directLocator, store.KindWorkplan, directBytes)
	writeOnce(t, s, cur.Repo.ID, matterLocator+"/"+stageLocator+"/"+groupedLocator, store.KindWorkplan, groupedBytes)
	first, err := Refresh(ctx, s, cur, store.ActorHuman, NoPrecondition)
	if err != nil {
		t.Fatalf("first Refresh: %v", err)
	}
	if _, err := CloseExplicit(ctx, s, cur, store.ActorHuman); err != nil {
		t.Fatalf("CloseExplicit: %v", err)
	}
	if err := os.RemoveAll(WipDir(cur.Root)); err != nil {
		t.Fatalf("remove .wip: %v", err)
	}
	if _, err := Refresh(ctx, s, cur, store.ActorHuman, NoPrecondition); err != nil {
		t.Fatalf("Refresh after removing .wip: %v", err)
	}

	assertStepBytes(t, cur, matterLocator, directLocator, directBytes)
	assertStepBytes(t, cur, matterLocator, groupedLocator, groupedBytes)
	if first.DispatchID == "" {
		t.Fatal("first Refresh returned no dispatch")
	}
}

func TestStepWorkplansRemovalAndReplacementCleanup(t *testing.T) {
	s, _, cur := setup(t)
	matterLocator := matter(t, s, cur.Repo.ID, "Amend steps")
	oldLocator := step(t, s, cur.Repo.ID, matterLocator, "Old")
	writeOnce(t, s, cur.Repo.ID, matterLocator+"/"+oldLocator, store.KindWorkplan, []byte("old"))
	if _, err := Refresh(ctx, s, cur, store.ActorHuman, NoPrecondition); err != nil {
		t.Fatalf("initial Refresh: %v", err)
	}
	oldPath := filepath.Join(GeneratedDir(cur.Root), matterLocator, "workplan-"+oldLocator+".md")
	if err := writesurface.RemoveStep(ctx, s, store.ActorHuman, cur.Repo.ID, matterLocator+"/"+oldLocator, "obsolete"); err != nil {
		t.Fatalf("RemoveStep: %v", err)
	}
	if !fileExists(oldPath) {
		t.Fatal("RemoveStep changed generated files before refresh")
	}
	if _, err := Refresh(ctx, s, cur, store.ActorHuman, NoPrecondition); err != nil {
		t.Fatalf("Refresh after RemoveStep: %v", err)
	}
	if fileExists(oldPath) {
		t.Fatal("removed Step Workplan still exists after refresh")
	}

	replaced := step(t, s, cur.Repo.ID, matterLocator, "Replace me")
	writeOnce(t, s, cur.Repo.ID, matterLocator+"/"+replaced, store.KindWorkplan, []byte("not inherited"))
	if _, err := Refresh(ctx, s, cur, store.ActorHuman, NoPrecondition); err != nil {
		t.Fatalf("Refresh before ReplaceStep: %v", err)
	}
	replacement, err := writesurface.ReplaceStep(ctx, s, store.ActorHuman, cur.Repo.ID, matterLocator+"/"+replaced, "Replacement")
	if err != nil {
		t.Fatalf("ReplaceStep: %v", err)
	}
	replacedPath := filepath.Join(GeneratedDir(cur.Root), matterLocator, "workplan-"+replaced+".md")
	if !fileExists(replacedPath) {
		t.Fatal("ReplaceStep changed generated files before refresh")
	}
	if _, err := Refresh(ctx, s, cur, store.ActorHuman, NoPrecondition); err != nil {
		t.Fatalf("Refresh after ReplaceStep: %v", err)
	}
	if fileExists(replacedPath) || fileExists(filepath.Join(GeneratedDir(cur.Root), matterLocator, "workplan-"+replacement.Locator+".md")) {
		t.Fatal("replacement inherited or retained the old Step Workplan")
	}

	replacementBytes := []byte("new replacement")
	writeOnce(t, s, cur.Repo.ID, matterLocator+"/"+replacement.Locator, store.KindWorkplan, replacementBytes)
	if _, err := Refresh(ctx, s, cur, store.ActorHuman, NoPrecondition); err != nil {
		t.Fatalf("Refresh after replacement content: %v", err)
	}
	if fileExists(replacedPath) {
		t.Fatal("old replacement path still exists")
	}
	assertStepBytes(t, cur, matterLocator, replacement.Locator, replacementBytes)
}

func TestStepWorkplanCleanupOwnsOnlyCanonicalRegularFiles(t *testing.T) {
	s, _, cur := setup(t)
	matterLocator := matter(t, s, cur.Repo.ID, "Cleanup ownership")
	if _, err := Refresh(ctx, s, cur, store.ActorHuman, NoPrecondition); err != nil {
		t.Fatalf("initial Refresh: %v", err)
	}
	dir := filepath.Join(GeneratedDir(cur.Root), matterLocator)
	removed := filepath.Join(dir, "workplan-step-99.md")
	if err := os.WriteFile(removed, []byte("stale"), 0o644); err != nil {
		t.Fatal(err)
	}
	keepFiles := []string{
		"workplan-step-00.md",
		"workplan-step-1.md",
		"workplan-step-001.md",
		"workplan-step-alpha.md",
		"workplan-delivery.md",
		"other.md",
	}
	for _, name := range keepFiles {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(name), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	directory := filepath.Join(dir, "workplan-step-100.md")
	if err := os.Mkdir(directory, 0o755); err != nil {
		t.Fatal(err)
	}
	symlink := filepath.Join(dir, "workplan-step-101.md")
	if err := os.Symlink(filepath.Join(dir, keepFiles[0]), symlink); err != nil {
		t.Fatal(err)
	}

	if _, err := Refresh(ctx, s, cur, store.ActorHuman, NoPrecondition); err != nil {
		t.Fatalf("Refresh: %v", err)
	}
	if fileExists(removed) {
		t.Fatal("stale canonical regular Step Workplan was not removed")
	}
	for _, name := range keepFiles {
		if !fileExists(filepath.Join(dir, name)) {
			t.Errorf("non-owned file %s was removed", name)
		}
	}
	if !fileExists(directory) || !fileExists(symlink) {
		t.Error("canonical directory or symlink was removed")
	}
}

func TestStepWorkplanStageShapedCollision(t *testing.T) {
	t.Run("stage content protects path", func(t *testing.T) {
		s, _, cur := setup(t)
		matterLocator := matter(t, s, cur.Repo.ID, "Stage collision")
		stageLocator := stage(t, s, cur.Repo.ID, matterLocator, "step-01")
		want := []byte("Stage owns this path")
		writeOnce(t, s, cur.Repo.ID, matterLocator+"/"+stageLocator, store.KindWorkplan, want)
		if _, err := Refresh(ctx, s, cur, store.ActorHuman, NoPrecondition); err != nil {
			t.Fatalf("Refresh: %v", err)
		}
		assertStepBytes(t, cur, matterLocator, stageLocator, want)
	})

	t.Run("stage without content does not protect stale path", func(t *testing.T) {
		s, _, cur := setup(t)
		matterLocator := matter(t, s, cur.Repo.ID, "Stage without content")
		stageLocator := stage(t, s, cur.Repo.ID, matterLocator, "step-01")
		if _, err := Refresh(ctx, s, cur, store.ActorHuman, NoPrecondition); err != nil {
			t.Fatalf("initial Refresh: %v", err)
		}
		path := filepath.Join(GeneratedDir(cur.Root), matterLocator, "workplan-"+stageLocator+".md")
		if err := os.WriteFile(path, []byte("removed Step bytes"), 0o644); err != nil {
			t.Fatal(err)
		}
		if _, err := Refresh(ctx, s, cur, store.ActorHuman, NoPrecondition); err != nil {
			t.Fatalf("Refresh: %v", err)
		}
		if fileExists(path) {
			t.Fatal("stale Step-shaped path was protected by a Stage without Workplan content")
		}
	})
}

func TestStepWorkplanPruneWaitsForAllWrites(t *testing.T) {
	s, _, cur := setup(t)
	matterLocator := matter(t, s, cur.Repo.ID, "Prune boundary")
	stepLocator := step(t, s, cur.Repo.ID, matterLocator, "Expected")
	if _, err := Refresh(ctx, s, cur, store.ActorHuman, NoPrecondition); err != nil {
		t.Fatalf("initial Refresh: %v", err)
	}
	writeOnce(t, s, cur.Repo.ID, matterLocator+"/"+stepLocator, store.KindWorkplan, []byte("expected"))
	dir := filepath.Join(GeneratedDir(cur.Root), matterLocator)
	stale := filepath.Join(dir, "workplan-step-99.md")
	if err := os.WriteFile(stale, []byte("stale"), 0o644); err != nil {
		t.Fatal(err)
	}
	blocked := filepath.Join(dir, "workplan-"+stepLocator+".md")
	if err := os.Mkdir(blocked, 0o755); err != nil {
		t.Fatal(err)
	}
	if _, err := Render(ctx, s, cur, store.ActorHuman, matterLocator, NoPrecondition); err == nil {
		t.Fatal("Render succeeded with an obstructed expected Step path")
	}
	if !fileExists(stale) {
		t.Fatal("prune removed stale file before all writes succeeded")
	}
	if err := os.Remove(blocked); err != nil {
		t.Fatal(err)
	}
	if _, err := Render(ctx, s, cur, store.ActorHuman, matterLocator, NoPrecondition); err != nil {
		t.Fatalf("repaired Render: %v", err)
	}
	if fileExists(stale) {
		t.Fatal("successful retry did not remove stale Step path")
	}
}

func TestStepWorkplansCancelReorderAndExitSnapshot(t *testing.T) {
	s, _, cur := setup(t)
	matterLocator := matter(t, s, cur.Repo.ID, "Final Step snapshot")
	first := step(t, s, cur.Repo.ID, matterLocator, "First")
	second := step(t, s, cur.Repo.ID, matterLocator, "Second")
	firstBytes := []byte("first")
	secondBytes := []byte("second")
	writeOnce(t, s, cur.Repo.ID, matterLocator+"/"+first, store.KindWorkplan, firstBytes)
	writeOnce(t, s, cur.Repo.ID, matterLocator+"/"+second, store.KindWorkplan, secondBytes)
	if _, err := writesurface.Start(ctx, s, store.ActorHuman, cur.Repo.ID, matterLocator+"/"+first); err != nil {
		t.Fatalf("Start: %v", err)
	}
	if _, err := writesurface.Cancel(ctx, s, store.ActorHuman, cur.Repo.ID, matterLocator+"/"+first, "pause"); err != nil {
		t.Fatalf("Cancel: %v", err)
	}
	if _, err := writesurface.ReorderStep(ctx, s, store.ActorHuman, cur.Repo.ID, matterLocator, []string{matterLocator + "/" + second, matterLocator + "/" + first}); err != nil {
		t.Fatalf("ReorderStep: %v", err)
	}
	if _, err := Refresh(ctx, s, cur, store.ActorHuman, NoPrecondition); err != nil {
		t.Fatalf("Refresh after cancel and reorder: %v", err)
	}
	assertStepBytes(t, cur, matterLocator, first, firstBytes)
	assertStepBytes(t, cur, matterLocator, second, secondBytes)
	if _, err := CloseExplicit(ctx, s, cur, store.ActorHuman); err != nil {
		t.Fatalf("CloseExplicit: %v", err)
	}

	if err := writesurface.RemoveStep(ctx, s, store.ActorHuman, cur.Repo.ID, matterLocator+"/"+first, "finished"); err != nil {
		t.Fatalf("RemoveStep: %v", err)
	}
	before, err := s.Events(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if err := writesurface.DeclareGate(ctx, s, cur.Repo.ID, "reviewed-local", store.ScaleMatter); err != nil {
		t.Fatalf("DeclareGate: %v", err)
	}
	if _, err := writesurface.Finish(ctx, s, store.ActorHuman, cur.Repo.ID, matterLocator); err != nil {
		t.Fatalf("Finish: %v", err)
	}
	if _, err := writesurface.CloseGate(ctx, s, store.ActorHuman, cur.Repo.ID, "reviewed-local", matterLocator); err != nil {
		t.Fatalf("CloseGate: %v", err)
	}
	node, err := writesurface.ResolveNode(ctx, s.View, cur.Repo.ID, matterLocator)
	if err != nil {
		t.Fatal(err)
	}
	if err := Exit(ctx, s, cur, node, NoPrecondition); err != nil {
		t.Fatalf("Exit: %v", err)
	}
	if fileExists(filepath.Join(GeneratedDir(cur.Root), matterLocator, "workplan-"+first+".md")) {
		t.Fatal("Exit left the removed Step Workplan in the final snapshot")
	}
	assertStepBytes(t, cur, matterLocator, second, secondBytes)
	if _, found, err := s.OpenDispatch(ctx, cur.Worktree.ID); err != nil {
		t.Fatal(err)
	} else if found {
		t.Fatal("Exit opened a dispatch")
	}
	after, err := s.Events(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if countRenderEvents(after) != countRenderEvents(before) {
		t.Fatal("Exit emitted render.performed")
	}

	if _, err := Render(ctx, s, cur, store.ActorHuman, matterLocator+"/"+second, NoPrecondition); err != nil {
		t.Fatalf("explicit sealed Render: %v", err)
	}
	result, err := Refresh(ctx, s, cur, store.ActorHuman, NoPrecondition)
	if err != nil {
		t.Fatalf("bare Refresh after sealed explicit Render: %v", err)
	}
	for _, rendered := range result.Rendered {
		if rendered == matterLocator {
			t.Fatal("bare Refresh rendered sealed Matter")
		}
	}
}

func countRenderEvents(events []store.Event) int {
	var count int
	for _, event := range events {
		if event.Type == store.TypeRenderPerformed {
			count++
		}
	}
	return count
}

func slicesEqual(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}
