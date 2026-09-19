package render

import (
	"bytes"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	"github.com/procrastivity/wip/internal/store"
	"github.com/procrastivity/wip/internal/writesurface"
)

// Tests for the Stage/Step findings read surface: findings on a Stage or Step
// render as a standalone `findings-<locator>.md` beside the Workplan files,
// with the same timestamped entry grammar matter.md's Findings section uses,
// while matter.md itself stays the Matter's file alone.

// expectedFindingsFile builds the exact bytes findings-<locator>.md must
// carry for a node with one finding, from the stored segment's own timestamp.
func expectedFindingsFile(t *testing.T, s *store.Store, matterLocator, nodeLocator, nodeID string, payload []byte) []byte {
	t.Helper()
	segs, err := s.ContentSegments(ctx, nodeID, store.KindFindings)
	if err != nil {
		t.Fatalf("read findings segments of %s: %v", nodeID, err)
	}
	if len(segs) != 1 {
		t.Fatalf("fixture has %d findings segments on %s, want 1", len(segs), nodeID)
	}
	var b strings.Builder
	b.WriteString("# Findings — " + nodeLocator + "\n\n")
	b.WriteString(snapshotNotice(matterLocator))
	b.WriteString("\n## Findings\n\n")
	b.WriteString("### " + segs[0].OccurredAt.UTC().Format("2006-01-02T15:04:05.000Z") + "\n\n")
	b.Write(payload)
	if len(payload) > 0 && payload[len(payload)-1] != '\n' {
		b.WriteByte('\n')
	}
	return []byte(b.String())
}

func resolveID(t *testing.T, s *store.Store, repo, locator string) string {
	t.Helper()
	n, err := writesurface.ResolveNode(ctx, s.View, repo, locator)
	if err != nil {
		t.Fatalf("ResolveNode(%s): %v", locator, err)
	}
	return n.ID
}

func TestStepAndStageFindingsRenderStandaloneFiles(t *testing.T) {
	s, _, cur := setup(t)
	matterLocator := matter(t, s, cur.Repo.ID, "Findings projection")
	stageLocator := stage(t, s, cur.Repo.ID, matterLocator, "Investigation")
	directLocator := step(t, s, cur.Repo.ID, matterLocator, "Direct step")
	groupedLocator := step(t, s, cur.Repo.ID, matterLocator+"/"+stageLocator, "Grouped step")
	quietLocator := step(t, s, cur.Repo.ID, matterLocator, "No findings")

	matterNote := []byte("a Matter finding\n")
	stageNote := []byte("a Stage finding\n")
	directNote := []byte("a direct Step finding, no final newline")
	groupedNote := []byte("a grouped Step finding\n")
	appendFinding(t, s, cur.Repo.ID, matterLocator, matterNote)
	appendFinding(t, s, cur.Repo.ID, matterLocator+"/"+stageLocator, stageNote)
	appendFinding(t, s, cur.Repo.ID, matterLocator+"/"+directLocator, directNote)
	appendFinding(t, s, cur.Repo.ID, matterLocator+"/"+stageLocator+"/"+groupedLocator, groupedNote)

	if _, err := Refresh(ctx, s, cur, store.ActorHuman, NoPrecondition); err != nil {
		t.Fatalf("Refresh: %v", err)
	}

	dir := filepath.Join(GeneratedDir(cur.Root), matterLocator)
	wantNames := []string{
		"matter.md",
		"roadmap.md",
		"findings-" + stageLocator + ".md",
		"findings-" + directLocator + ".md",
		"findings-" + groupedLocator + ".md",
	}
	sort.Strings(wantNames)
	if got := generatedNames(t, dir); !slicesEqual(got, wantNames) {
		t.Fatalf("generated names = %v, want %v", got, wantNames)
	}
	if fileExists(filepath.Join(dir, "findings-"+quietLocator+".md")) {
		t.Error("a Step without findings rendered a findings file")
	}
	if fileExists(filepath.Join(dir, "findings-"+matterLocator+".md")) {
		t.Error("the Matter's findings rendered a standalone file; they belong in matter.md")
	}
	if md := matterMarkdown(t, cur, matterLocator); !strings.Contains(md, "## Findings") ||
		!strings.Contains(md, string(matterNote)) {
		t.Errorf("matter.md lost its own Findings section:\n%s", md)
	}

	for locator, note := range map[string][]byte{
		stageLocator:   stageNote,
		directLocator:  directNote,
		groupedLocator: groupedNote,
	} {
		path := filepath.Join(dir, "findings-"+locator+".md")
		want := expectedFindingsFile(t, s, matterLocator, locator,
			resolveID(t, s, cur.Repo.ID, matterLocator+"/"+locator), note)
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

	// A second refresh rewrites in place and restores 0444.
	if _, err := Refresh(ctx, s, cur, store.ActorHuman, NoPrecondition); err != nil {
		t.Fatalf("second Refresh: %v", err)
	}
	if mode := fileMode(t, filepath.Join(dir, "findings-"+directLocator+".md")); mode != 0o444 {
		t.Errorf("findings file mode after second refresh = %o, want 0444", mode)
	}
}

func TestStepFindingsRemovalCleanupAndOwnership(t *testing.T) {
	s, _, cur := setup(t)
	matterLocator := matter(t, s, cur.Repo.ID, "Findings cleanup")
	stepLocator := step(t, s, cur.Repo.ID, matterLocator, "Removed later")
	appendFinding(t, s, cur.Repo.ID, matterLocator+"/"+stepLocator, []byte("kept until removal\n"))
	if _, err := Refresh(ctx, s, cur, store.ActorHuman, NoPrecondition); err != nil {
		t.Fatalf("initial Refresh: %v", err)
	}
	dir := filepath.Join(GeneratedDir(cur.Root), matterLocator)
	path := filepath.Join(dir, "findings-"+stepLocator+".md")
	if !fileExists(path) {
		t.Fatalf("fixture rendered no %s", path)
	}

	// Cleanup owns only the canonical namespace: a stale canonical file goes,
	// noncanonical neighbors stay.
	stale := filepath.Join(dir, "findings-step-99.md")
	if err := os.WriteFile(stale, []byte("stale"), 0o644); err != nil {
		t.Fatal(err)
	}
	keep := []string{"findings-step-00.md", "findings-step-1.md", "findings-delivery.md"}
	for _, name := range keep {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(name), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := Refresh(ctx, s, cur, store.ActorHuman, NoPrecondition); err != nil {
		t.Fatalf("Refresh: %v", err)
	}
	if fileExists(stale) {
		t.Fatal("stale canonical Step findings file was not removed")
	}
	for _, name := range keep {
		if !fileExists(filepath.Join(dir, name)) {
			t.Errorf("non-owned file %s was removed", name)
		}
	}

	// A removed Step's findings file disappears on the next refresh.
	if err := writesurface.RemoveStep(ctx, s, store.ActorHuman, cur.Repo.ID, matterLocator+"/"+stepLocator, "obsolete"); err != nil {
		t.Fatalf("RemoveStep: %v", err)
	}
	if !fileExists(path) {
		t.Fatal("RemoveStep changed generated files before refresh")
	}
	if _, err := Refresh(ctx, s, cur, store.ActorHuman, NoPrecondition); err != nil {
		t.Fatalf("Refresh after RemoveStep: %v", err)
	}
	if fileExists(path) {
		t.Fatal("removed Step findings file still exists after refresh")
	}
}

func TestStepFindingsStageShapedCollision(t *testing.T) {
	t.Run("stage findings protect the path", func(t *testing.T) {
		s, _, cur := setup(t)
		matterLocator := matter(t, s, cur.Repo.ID, "Findings stage collision")
		stageLocator := stage(t, s, cur.Repo.ID, matterLocator, "step-01")
		appendFinding(t, s, cur.Repo.ID, matterLocator+"/"+stageLocator, []byte("Stage owns this path\n"))
		if _, err := Refresh(ctx, s, cur, store.ActorHuman, NoPrecondition); err != nil {
			t.Fatalf("Refresh: %v", err)
		}
		path := filepath.Join(GeneratedDir(cur.Root), matterLocator, "findings-"+stageLocator+".md")
		if !fileExists(path) {
			t.Fatal("a Stage's findings at a Step-shaped locator did not render")
		}
	})

	t.Run("stage without findings does not protect a stale path", func(t *testing.T) {
		s, _, cur := setup(t)
		matterLocator := matter(t, s, cur.Repo.ID, "Findings stage without content")
		stageLocator := stage(t, s, cur.Repo.ID, matterLocator, "step-01")
		if _, err := Refresh(ctx, s, cur, store.ActorHuman, NoPrecondition); err != nil {
			t.Fatalf("initial Refresh: %v", err)
		}
		path := filepath.Join(GeneratedDir(cur.Root), matterLocator, "findings-"+stageLocator+".md")
		if err := os.WriteFile(path, []byte("removed Step bytes"), 0o644); err != nil {
			t.Fatal(err)
		}
		if _, err := Refresh(ctx, s, cur, store.ActorHuman, NoPrecondition); err != nil {
			t.Fatalf("Refresh: %v", err)
		}
		if fileExists(path) {
			t.Fatal("a stale Step-shaped findings path was protected by a Stage without findings")
		}
	})
}
