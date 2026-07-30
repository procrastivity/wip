package tiers

import (
	"path/filepath"
	"testing"

	"github.com/procrastivity/wip/internal/store"
)

// TestCheckUnknownClone_Known confirms a clone wip already knows reports
// clean, and runs adoption opportunistically along the way.
func TestCheckUnknownClone_Known(t *testing.T) {
	s := newStore(t)
	dir := newRepo(t, "widget")
	addRemote(t, dir, "origin", "git@github.com:acme/widget.git")
	result, err := Init(ctx, s, store.ActorHuman, dir, "")
	if err != nil {
		t.Fatalf("Init: %v", err)
	}

	report, err := CheckUnknownClone(ctx, s, store.ActorHuman, dir)
	if err != nil {
		t.Fatalf("CheckUnknownClone: %v", err)
	}
	if !report.Known {
		t.Fatal("Known = false, want true")
	}
	if report.Clone.ID != result.Clone.ID {
		t.Errorf("reported clone %s, want %s", report.Clone.ID, result.Clone.ID)
	}
}

// TestCheckUnknownClone_RemoteKnownOffersRelinkOrNew is example 6a: an
// un-init'd clone whose remote matches a known Repo gets both offers named,
// never a silent choice.
func TestCheckUnknownClone_RemoteKnownOffersRelinkOrNew(t *testing.T) {
	s := newStore(t)
	known := newRepo(t, "widget-known")
	addRemote(t, known, "origin", "git@github.com:acme/widget.git")
	first, err := Init(ctx, s, store.ActorHuman, known, "")
	if err != nil {
		t.Fatalf("Init(known): %v", err)
	}

	unregistered := newRepo(t, "widget-new-clone")
	addRemote(t, unregistered, "origin", "https://github.com/acme/widget.git")

	report, err := CheckUnknownClone(ctx, s, store.ActorHuman, unregistered)
	if err != nil {
		t.Fatalf("CheckUnknownClone: %v", err)
	}
	if report.Known {
		t.Fatal("Known = true, want false (this common-dir has never been init'd)")
	}
	if report.OfferRepo.ID != first.Repo.ID {
		t.Errorf("OfferRepo = %s, want %s", report.OfferRepo.ID, first.Repo.ID)
	}
	if len(report.OfferClones) != 1 || report.OfferClones[0].ID != first.Clone.ID {
		t.Errorf("OfferClones = %+v, want just the known clone %s", report.OfferClones, first.Clone.ID)
	}
}

// TestCheckUnknownClone_RemoteAlsoUnknownRefuses is example 6b: no known
// Repo to relink against, so this falls through to the standard
// unknown-clone hard failure.
func TestCheckUnknownClone_RemoteAlsoUnknownRefuses(t *testing.T) {
	s := newStore(t)
	dir := newRepo(t, "totally-unknown")
	addRemote(t, dir, "origin", "git@github.com:nobody/unknown.git")

	_, err := CheckUnknownClone(ctx, s, store.ActorHuman, dir)
	wantWiperr(t, err, "refusal.unknown-clone")
}

// TestCheckUnknownClone_NoRemoteAtAllRefuses is the same fallthrough with no
// remote configured at all.
func TestCheckUnknownClone_NoRemoteAtAllRefuses(t *testing.T) {
	s := newStore(t)
	dir := newRepo(t, "no-remote-at-all")

	_, err := CheckUnknownClone(ctx, s, store.ActorHuman, dir)
	wantWiperr(t, err, "refusal.unknown-clone")
}

// TestRelink_ByULID_AcceptsTheOffer runs the accepted half of the
// move-detection offer: a clone directory renamed (its common-dir changes),
// then relinked by ULID from the new location.
func TestRelink_ByULID_AcceptsTheOffer(t *testing.T) {
	s := newStore(t)
	root := t.TempDir()
	original := filepath.Join(root, "widget-b")
	run(t, root, "init", "-q", "widget-b")
	addRemote(t, original, "origin", "git@github.com:acme/widget.git")
	result, err := Init(ctx, s, store.ActorHuman, original, "")
	if err != nil {
		t.Fatalf("Init: %v", err)
	}

	moved := filepath.Join(root, "widget-b-moved")
	if err := moveDir(original, moved); err != nil {
		t.Fatal(err)
	}

	relinked, err := Relink(ctx, s, store.ActorHuman, moved, result.Clone.ID)
	if err != nil {
		t.Fatalf("Relink: %v", err)
	}
	if relinked.ID != result.Clone.ID {
		t.Errorf("relink produced a different Clone identity: %s vs %s", relinked.ID, result.Clone.ID)
	}
	wantCommonDir, err := gitCommonDir(ctx, moved)
	if err != nil {
		t.Fatal(err)
	}
	if relinked.GitCommonDir != wantCommonDir {
		t.Errorf("GitCommonDir after relink = %q, want %q", relinked.GitCommonDir, wantCommonDir)
	}

	// The relinked clone is now known at its new location.
	report, err := CheckUnknownClone(ctx, s, store.ActorHuman, moved)
	if err != nil {
		t.Fatalf("CheckUnknownClone after relink: %v", err)
	}
	if !report.Known {
		t.Error("the relinked clone is still unknown at its new location")
	}
}

// TestRelink_ByLabel_ScopedToCurrentRemotesRepo confirms label-form relink
// resolves scoped to whichever Repo the current directory's own remote
// matches — needed because a label is only unique within its Repo.
func TestRelink_ByLabel_ScopedToCurrentRemotesRepo(t *testing.T) {
	s := newStore(t)
	root := t.TempDir()
	original := filepath.Join(root, "widget-b")
	run(t, root, "init", "-q", "widget-b")
	addRemote(t, original, "origin", "git@github.com:acme/widget.git")
	if _, err := Init(ctx, s, store.ActorHuman, original, ""); err != nil {
		t.Fatalf("Init: %v", err)
	}

	moved := filepath.Join(root, "widget-b-moved")
	if err := moveDir(original, moved); err != nil {
		t.Fatal(err)
	}

	relinked, err := Relink(ctx, s, store.ActorHuman, moved, "widget-b")
	if err != nil {
		t.Fatalf("Relink by label: %v", err)
	}
	if relinked.Label != "widget-b" {
		t.Errorf("relinked.Label = %q, want %q", relinked.Label, "widget-b")
	}
}

// TestCheckUnknownClone_MovedSameCommonDirIsANoOp is worked example 3: a
// clone directory moved such that git-common-dir itself is unaffected (a
// separate git-dir, e.g. via --separate-git-dir) is not a move wip needs to
// know about at all — the natural key genuinely has not changed.
func TestCheckUnknownClone_MovedSameCommonDirIsANoOp(t *testing.T) {
	s := newStore(t)
	root := t.TempDir()
	externalGitDir := filepath.Join(root, "external-git")
	original := filepath.Join(root, "detached")
	run(t, root, "init", "-q", "--separate-git-dir="+externalGitDir, "detached")
	result, err := Init(ctx, s, store.ActorHuman, original, "")
	if err != nil {
		t.Fatalf("Init: %v", err)
	}

	moved := filepath.Join(root, "detached-renamed")
	if err := moveDir(original, moved); err != nil {
		t.Fatal(err)
	}

	commonDirAfter, err := gitCommonDir(ctx, moved)
	if err != nil {
		t.Fatal(err)
	}
	if commonDirAfter != result.Clone.GitCommonDir {
		t.Fatalf("test setup is broken: common-dir changed after the move (%q vs %q); this test needs it to stay fixed",
			commonDirAfter, result.Clone.GitCommonDir)
	}

	report, err := CheckUnknownClone(ctx, s, store.ActorHuman, moved)
	if err != nil {
		t.Fatalf("CheckUnknownClone after a same-common-dir move: %v", err)
	}
	if !report.Known {
		t.Error("Known = false after a move that left common-dir unchanged; want a true no-op")
	}
	if report.Clone.ID != result.Clone.ID {
		t.Error("a same-common-dir move produced a different Clone identity")
	}
}
