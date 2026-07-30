package tiers

import (
	"testing"

	"github.com/procrastivity/wip/internal/store"
)

// TestAdopt_HappyPath is worked example 4: a local-only Repo gains a remote
// under its recorded identity_remote name, and the next resolution adopts
// the key in place — same ULID.
func TestAdopt_HappyPath(t *testing.T) {
	s := newStore(t)
	dir := newRepo(t, "widget")

	before, err := Init(ctx, s, store.ActorHuman, dir, "")
	if err != nil {
		t.Fatalf("Init: %v", err)
	}
	if before.Repo.RemoteURL != "" {
		t.Fatal("expected a local-only Repo before adoption")
	}

	addRemote(t, dir, "origin", "git@github.com:acme/widget.git")

	clone, found, err := ResolveCurrentClone(ctx, s, store.ActorHuman, dir)
	if err != nil {
		t.Fatalf("ResolveCurrentClone: %v", err)
	}
	if !found {
		t.Fatal("clone not found after adoption should have run")
	}
	after, err := s.Repo(ctx, clone.Repo)
	if err != nil {
		t.Fatal(err)
	}
	if after.ID != before.Repo.ID {
		t.Fatalf("adoption changed the Repo's identity: %s -> %s, want the same ULID", before.Repo.ID, after.ID)
	}
	if after.RemoteURL != "github.com/acme/widget" {
		t.Errorf("RemoteURL after adoption = %q, want the normalized origin", after.RemoteURL)
	}
}

// TestAdopt_DifferentNamedRemoteDoesNotFire confirms a remote added under a
// name other than the Repo's recorded identity_remote is not adoption and
// leaves the natural key untouched, no matter how plausible it looks.
func TestAdopt_DifferentNamedRemoteDoesNotFire(t *testing.T) {
	s := newStore(t)
	dir := newRepo(t, "widget")

	// init'd with an override naming "upstream" as the identity remote, and
	// no remotes at all yet — stays null-keyed.
	before, err := Init(ctx, s, store.ActorHuman, dir, "upstream")
	if err != nil {
		t.Fatalf("Init: %v", err)
	}

	// Only "origin" shows up later — a different name, not adoption.
	addRemote(t, dir, "origin", "git@github.com:acme/widget.git")

	clone, found, err := ResolveCurrentClone(ctx, s, store.ActorHuman, dir)
	if err != nil {
		t.Fatalf("ResolveCurrentClone: %v", err)
	}
	if !found {
		t.Fatal("clone not found")
	}
	after, err := s.Repo(ctx, clone.Repo)
	if err != nil {
		t.Fatal(err)
	}
	if after.RemoteURL != "" {
		t.Errorf("RemoteURL = %q after a differently-named remote appeared, want still empty (no adoption)", after.RemoteURL)
	}
	if after.ID != before.Repo.ID {
		t.Fatal("Repo identity changed unexpectedly")
	}
}

// TestAdopt_ConflictRefuses covers the unhappy path: the URL being adopted
// already belongs to a different, existing Repo. Adoption refuses rather
// than merging, and names both ULIDs.
func TestAdopt_ConflictRefuses(t *testing.T) {
	s := newStore(t)

	// A Repo already claims this normalized URL.
	claimed := newRepo(t, "widget-claimed")
	addRemote(t, claimed, "origin", "git@github.com:acme/widget.git")
	claimedResult, err := Init(ctx, s, store.ActorHuman, claimed, "")
	if err != nil {
		t.Fatalf("Init(claimed): %v", err)
	}

	// A second, local-only Repo whose clone later gains the *same* remote
	// URL under its own recorded identity_remote name.
	localOnly := newRepo(t, "widget-local-only")
	localResult, err := Init(ctx, s, store.ActorHuman, localOnly, "")
	if err != nil {
		t.Fatalf("Init(local-only): %v", err)
	}
	addRemote(t, localOnly, "origin", "https://github.com/acme/widget.git") // same normal form

	_, _, err = ResolveCurrentClone(ctx, s, store.ActorHuman, localOnly)
	wantWiperr(t, err, "refusal.repo-key-conflict")

	// The refused Repo stays local-only, and the claimant is untouched.
	stillLocal, err := s.Repo(ctx, localResult.Repo.ID)
	if err != nil {
		t.Fatal(err)
	}
	if stillLocal.RemoteURL != "" {
		t.Errorf("the refused Repo's RemoteURL = %q, want still empty", stillLocal.RemoteURL)
	}
	claimedAfter, err := s.Repo(ctx, claimedResult.Repo.ID)
	if err != nil {
		t.Fatal(err)
	}
	if claimedAfter.RemoteURL != "github.com/acme/widget" {
		t.Errorf("the claimant Repo's RemoteURL changed to %q", claimedAfter.RemoteURL)
	}
}

// TestAdopt_AlreadyKeyedIsANoOp confirms a Repo that already has a remote
// never re-runs adoption logic against a second identity_remote match.
func TestAdopt_AlreadyKeyedIsANoOp(t *testing.T) {
	s := newStore(t)
	dir := newRepo(t, "widget")
	addRemote(t, dir, "origin", "git@github.com:acme/widget.git")

	result, err := Init(ctx, s, store.ActorHuman, dir, "")
	if err != nil {
		t.Fatalf("Init: %v", err)
	}

	repo, err := AdoptIfPossible(ctx, s, store.ActorHuman, result.Clone, dir)
	if err != nil {
		t.Fatalf("AdoptIfPossible on an already-keyed Repo: %v", err)
	}
	if repo.RemoteURL != result.Repo.RemoteURL {
		t.Errorf("RemoteURL changed on an already-keyed Repo: %q -> %q", result.Repo.RemoteURL, repo.RemoteURL)
	}
}
