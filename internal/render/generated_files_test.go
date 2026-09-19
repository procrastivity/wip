package render

import (
	"encoding/json"
	"path/filepath"
	"testing"

	"github.com/procrastivity/wip/internal/store"
	"github.com/procrastivity/wip/internal/writesurface"
)

// Tests for the navigation contract's §4 (generatedFiles): renderMatterTree
// reports the absolute paths it actually wrote, in deterministic write
// order, written-only (pruned files never reported).

// TestRenderMatterTree_FullVocabularyReportsOrderedWrittenPaths is §9 4.1:
// matter.md, brief.md, workplan.md, roadmap.md first (always this order),
// then per Stage/Step node in MatterNodes order — which sorts by sort_key,
// birth_event across Stages and Steps together — each node contributing
// workplan-<locator>.md then findings-<locator>.md when earned. The
// expected tail is derived from the fixture's own creation order via
// s.MatterNodes, never hard-coded as Stages-before-Steps.
func TestRenderMatterTree_FullVocabularyReportsOrderedWrittenPaths(t *testing.T) {
	s, _, cur := setup(t)
	locator := matter(t, s, cur.Repo.ID, "Full vocabulary")
	writeOnce(t, s, cur.Repo.ID, locator, store.KindBrief, []byte("brief"))
	writeOnce(t, s, cur.Repo.ID, locator, store.KindWorkplan, []byte("matter workplan"))

	stageLocator := stage(t, s, cur.Repo.ID, locator, "Investigation")
	directLocator := step(t, s, cur.Repo.ID, locator, "Direct step")
	groupedLocator := step(t, s, cur.Repo.ID, locator+"/"+stageLocator, "Grouped step")
	quietLocator := step(t, s, cur.Repo.ID, locator, "No content at all")
	_ = quietLocator

	writeOnce(t, s, cur.Repo.ID, locator+"/"+stageLocator, store.KindWorkplan, []byte("stage workplan"))
	appendFinding(t, s, cur.Repo.ID, locator+"/"+stageLocator, []byte("stage finding"))
	writeOnce(t, s, cur.Repo.ID, locator+"/"+directLocator, store.KindWorkplan, []byte("direct step workplan"))
	appendFinding(t, s, cur.Repo.ID, locator+"/"+stageLocator+"/"+groupedLocator, []byte("grouped step finding"))

	matterNode, err := s.MatterByLocator(ctx, cur.Repo.ID, locator)
	if err != nil {
		t.Fatalf("MatterByLocator: %v", err)
	}
	got, err := renderMatterTree(ctx, s, cur.Root, matterNode)
	if err != nil {
		t.Fatalf("renderMatterTree: %v", err)
	}

	dir := filepath.Join(GeneratedDir(cur.Root), locator)
	want := []string{
		filepath.Join(dir, "matter.md"),
		filepath.Join(dir, "brief.md"),
		filepath.Join(dir, "workplan.md"),
		filepath.Join(dir, "roadmap.md"),
	}
	nodes, err := s.MatterNodes(ctx, matterNode.ID)
	if err != nil {
		t.Fatalf("MatterNodes: %v", err)
	}
	for _, node := range nodes {
		if node.Kind != store.ScaleStage && node.Kind != store.ScaleStep {
			continue
		}
		if hasContent(t, s, node.ID, store.KindWorkplan) {
			want = append(want, filepath.Join(dir, "workplan-"+node.Locator+".md"))
		}
		if hasContent(t, s, node.ID, store.KindFindings) {
			want = append(want, filepath.Join(dir, "findings-"+node.Locator+".md"))
		}
	}

	if !slicesEqual(got, want) {
		t.Errorf("renderMatterTree written paths =\n%v\nwant\n%v", got, want)
	}
}

func hasContent(t *testing.T, s *store.Store, node string, kind store.ContentKind) bool {
	t.Helper()
	segs, err := s.ContentSegments(ctx, node, kind)
	if err != nil {
		t.Fatalf("ContentSegments(%s, %s): %v", node, kind, err)
	}
	return len(segs) > 0
}

// TestRenderMatterTree_MinimalMatterReportsOnlyMatterFile is §9 4.2.
func TestRenderMatterTree_MinimalMatterReportsOnlyMatterFile(t *testing.T) {
	s, _, cur := setup(t)
	locator := matter(t, s, cur.Repo.ID, "Minimal")
	matterNode, err := s.MatterByLocator(ctx, cur.Repo.ID, locator)
	if err != nil {
		t.Fatal(err)
	}
	got, err := renderMatterTree(ctx, s, cur.Root, matterNode)
	if err != nil {
		t.Fatalf("renderMatterTree: %v", err)
	}
	want := []string{filepath.Join(GeneratedDir(cur.Root), locator, "matter.md")}
	if !slicesEqual(got, want) {
		t.Errorf("renderMatterTree = %v, want exactly %v", got, want)
	}
}

// TestRefresh_EagerConcatenatesGeneratedFilesInRenderedOrder is §9 4.3: the
// eager path's GeneratedFiles is the concatenation of each rendered
// Matter's own written files, in `rendered`'s own (EagerScope) order.
func TestRefresh_EagerConcatenatesGeneratedFilesInRenderedOrder(t *testing.T) {
	s, _, cur := setup(t)
	first := matter(t, s, cur.Repo.ID, "First Matter")
	writeOnce(t, s, cur.Repo.ID, first, store.KindBrief, []byte("brief"))
	second := matter(t, s, cur.Repo.ID, "Second Matter")

	result, err := Refresh(ctx, s, cur, store.ActorHuman, NoPrecondition)
	if err != nil {
		t.Fatalf("Refresh: %v", err)
	}
	if len(result.Rendered) != 2 {
		t.Fatalf("rendered = %v, want 2 Matters", result.Rendered)
	}

	var want []string
	for _, loc := range result.Rendered {
		want = append(want, filepath.Join(GeneratedDir(cur.Root), loc, "matter.md"))
		if loc == first {
			want = append(want, filepath.Join(GeneratedDir(cur.Root), loc, "brief.md"))
		}
	}
	_ = second
	if !slicesEqual(result.GeneratedFiles, want) {
		t.Errorf("GeneratedFiles = %v, want %v", result.GeneratedFiles, want)
	}
}

// TestRefresh_PruneCase is §9 4.4: a stale canonical Step file removed by
// pruneStepFiles is absent from GeneratedFiles.
func TestRefresh_PruneCase(t *testing.T) {
	s, _, cur := setup(t)
	locator := matter(t, s, cur.Repo.ID, "Prune case")
	stepLocator := step(t, s, cur.Repo.ID, locator, "Will be removed")
	writeOnce(t, s, cur.Repo.ID, locator+"/"+stepLocator, store.KindWorkplan, []byte("workplan"))

	if _, err := Refresh(ctx, s, cur, store.ActorHuman, NoPrecondition); err != nil {
		t.Fatalf("initial Refresh: %v", err)
	}
	stalePath := filepath.Join(GeneratedDir(cur.Root), locator, "workplan-"+stepLocator+".md")
	if !fileExists(stalePath) {
		t.Fatalf("fixture did not render %s", stalePath)
	}

	if err := writesurface.RemoveStep(ctx, s, store.ActorHuman, cur.Repo.ID, locator+"/"+stepLocator, "obsolete"); err != nil {
		t.Fatalf("RemoveStep: %v", err)
	}

	result, err := Refresh(ctx, s, cur, store.ActorHuman, NoPrecondition)
	if err != nil {
		t.Fatalf("Refresh after RemoveStep: %v", err)
	}
	if fileExists(stalePath) {
		t.Fatal("stale Step workplan file was not pruned")
	}
	for _, path := range result.GeneratedFiles {
		if path == stalePath {
			t.Errorf("GeneratedFiles reported the pruned path %s", path)
		}
	}
}

// TestRefresh_EmptyEagerScopeReportsEmptyNotNil is §9 4.5.
func TestRefresh_EmptyEagerScopeReportsEmptyNotNil(t *testing.T) {
	s, _, cur := setup(t)
	result, err := Refresh(ctx, s, cur, store.ActorHuman, NoPrecondition)
	if err != nil {
		t.Fatalf("Refresh: %v", err)
	}
	if result.GeneratedFiles == nil {
		t.Error("GeneratedFiles is nil, want an empty non-nil slice")
	}
	if len(result.GeneratedFiles) != 0 {
		t.Errorf("GeneratedFiles = %v, want empty", result.GeneratedFiles)
	}
}

// TestExit_SignatureAndBehaviorUnchanged is §9 4.6: Exit stays outside the
// contract — error-only signature — while renderMatterTree's now-([]string,
// error) internal signature still lets Exit write the final snapshot.
// (Exit's own seal-write behavior is separately covered by
// TestExit_WritesSealedSnapshotWithoutDispatch / TestExit_OverwritesPreSealSnapshot
// in refresh_test.go, which keep passing unmodified.)
func TestExit_SignatureAndBehaviorUnchanged(t *testing.T) {
	s, _, cur := setup(t)
	locator := matter(t, s, cur.Repo.ID, "Exit signature")
	seal(t, s, cur.Repo.ID, locator)
	node, err := s.MatterByLocator(ctx, cur.Repo.ID, locator)
	if err != nil {
		t.Fatal(err)
	}
	// Exit's signature is `func(...) error` — a single-value call site like
	// this one does not compile otherwise, which is the regression this
	// test guards against.
	if err := Exit(ctx, s, cur, node, NoPrecondition); err != nil {
		t.Fatalf("Exit: %v", err)
	}
	if !fileExists(filepath.Join(GeneratedDir(cur.Root), locator, "matter.md")) {
		t.Error("Exit did not write the sealed snapshot")
	}
}

// TestRecordRenderPerformed_PayloadUnchangedAcrossBothPaths is §9 4.12: the
// file-list addition is verb/Result output only, never an event field —
// exactly one render.performed per pass, on both the eager and locator
// paths, with its Target payload untouched by this Matter.
func TestRecordRenderPerformed_PayloadUnchangedAcrossBothPaths(t *testing.T) {
	s, _, cur := setup(t)
	locator := matter(t, s, cur.Repo.ID, "Event payload")

	before, err := s.Events(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := Refresh(ctx, s, cur, store.ActorHuman, NoPrecondition); err != nil {
		t.Fatalf("Refresh: %v", err)
	}
	afterRefresh, err := s.Events(ctx)
	if err != nil {
		t.Fatal(err)
	}
	assertExactlyOneRenderPerformed(t, afterRefresh[len(before):], "")

	if _, err := Render(ctx, s, cur, store.ActorHuman, locator, NoPrecondition); err != nil {
		t.Fatalf("Render: %v", err)
	}
	afterRender, err := s.Events(ctx)
	if err != nil {
		t.Fatal(err)
	}
	assertExactlyOneRenderPerformed(t, afterRender[len(afterRefresh):], locator)
}

func assertExactlyOneRenderPerformed(t *testing.T, newEvents []store.Event, wantTarget string) {
	t.Helper()
	var payloads []store.RenderPerformed
	for _, ev := range newEvents {
		if ev.Type != store.TypeRenderPerformed {
			continue
		}
		var p store.RenderPerformed
		if err := json.Unmarshal(ev.Payload, &p); err != nil {
			t.Fatalf("decode render.performed payload: %v", err)
		}
		payloads = append(payloads, p)
	}
	if len(payloads) != 1 {
		t.Fatalf("this pass produced %d render.performed events, want exactly 1", len(payloads))
	}
	if payloads[0].Target != wantTarget {
		t.Errorf("render.performed Target = %q, want %q", payloads[0].Target, wantTarget)
	}
}
