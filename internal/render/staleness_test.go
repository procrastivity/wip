package render

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/procrastivity/wip/internal/store"
	"github.com/procrastivity/wip/internal/tiers"
)

// TestD43_StalenessWindowWithinOneClone is step-11's second test: this
// package accepts the staleness window a render leaves open within one
// Clone — a render is not instantly refreshed by another write, in this
// Clone or any other, because rendering happens only when this Clone's own
// `wip refresh` runs (D33: filenames are render policy, not a live view).
// D43 (a Matter is worked from one clone at a time) is what bounds this: no
// cross-Clone staleness scenario is exercised here, because D43 makes it
// structurally not arise (unenforced in P1 per D69, but the two clones this
// test opens never touch the same Matter concurrently either).
func TestD43_StalenessWindowWithinOneClone(t *testing.T) {
	s := newStore(t)

	const remote = "git@example.com:acme/widget.git"
	dirA := newRepo(t)
	addRemote(t, dirA, "origin", remote)
	if _, err := tiers.Init(ctx, s, store.ActorHuman, dirA, ""); err != nil {
		t.Fatalf("tiers.Init clone A: %v", err)
	}
	curA, err := ResolveCurrent(ctx, s, store.ActorHuman, dirA)
	if err != nil {
		t.Fatalf("ResolveCurrent clone A: %v", err)
	}

	dirB := newRepo(t)
	addRemote(t, dirB, "origin", remote)
	if _, err := tiers.Init(ctx, s, store.ActorHuman, dirB, ""); err != nil {
		t.Fatalf("tiers.Init clone B: %v", err)
	}
	curB, err := ResolveCurrent(ctx, s, store.ActorHuman, dirB)
	if err != nil {
		t.Fatalf("ResolveCurrent clone B: %v", err)
	}

	if curA.Repo.ID != curB.Repo.ID {
		t.Fatalf("clone A and clone B resolved to different Repos (%s vs %s); same remote should be one Repo", curA.Repo.ID, curB.Repo.ID)
	}
	if curA.Clone.ID == curB.Clone.ID {
		t.Fatal("clone A and clone B resolved to the same Clone row")
	}

	locator := matter(t, s, curA.Repo.ID, "Written From Clone A")
	writeOnce(t, s, curA.Repo.ID, locator, store.KindBody, []byte("v1"))

	if _, err := Refresh(ctx, s, curA, store.ActorHuman, NoPrecondition); err != nil {
		t.Fatalf("Refresh from clone A: %v", err)
	}
	aFile := filepath.Join(GeneratedDir(curA.Root), locator, "matter.md")
	v1, err := os.ReadFile(aFile)
	if err != nil {
		t.Fatal(err)
	}
	if !contains(string(v1), "v1") {
		t.Fatalf("clone A's own render does not carry its own just-written content: %q", v1)
	}

	// The store is one database, so clone B's own first render already
	// sees the durable fact clone A wrote — there is no cross-clone
	// replication lag to accept here; the staleness this test accepts is
	// narrower: clone A's own already-rendered file is what could go stale
	// after clone B (or clone A itself) writes again, and only clone A's
	// own next `wip refresh` would notice.
	if _, err := Refresh(ctx, s, curB, store.ActorHuman, NoPrecondition); err != nil {
		t.Fatalf("Refresh from clone B: %v", err)
	}
	bFile := filepath.Join(GeneratedDir(curB.Root), locator, "matter.md")
	got, err := os.ReadFile(bFile)
	if err != nil {
		t.Fatal(err)
	}
	if !contains(string(got), "v1") {
		t.Errorf("clone B's render does not see the durable content clone A wrote: %q", got)
	}

	// A later write, from either clone, does not retroactively update
	// clone A's already-rendered file — that is the accepted window.
	writeOnce(t, s, curA.Repo.ID, locator, store.KindFindings, []byte("v2 finding"))
	staleA, err := os.ReadFile(aFile)
	if err != nil {
		t.Fatal(err)
	}
	if contains(string(staleA), "v2 finding") {
		t.Error("clone A's rendered file changed without clone A calling refresh again — there is no live view to update it")
	}

	if _, err := Refresh(ctx, s, curA, store.ActorHuman, NoPrecondition); err != nil {
		t.Fatalf("second Refresh from clone A: %v", err)
	}
	freshA, err := os.ReadFile(aFile)
	if err != nil {
		t.Fatal(err)
	}
	if !contains(string(freshA), "v2 finding") {
		t.Error("clone A's own next refresh should pick up the new finding")
	}
}
