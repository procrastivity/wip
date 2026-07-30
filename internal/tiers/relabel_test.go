package tiers

import (
	"strings"
	"testing"

	"github.com/procrastivity/wip/internal/store"
	"github.com/procrastivity/wip/internal/wiperr"
)

func TestSetLabel_ULIDShapedRefuses(t *testing.T) {
	s := newStore(t)
	dir := newRepo(t, "widget")
	addRemote(t, dir, "origin", "git@github.com:acme/widget.git")
	if _, err := Init(ctx, s, store.ActorHuman, dir, ""); err != nil {
		t.Fatalf("Init: %v", err)
	}

	_, err := SetLabel(ctx, s, store.ActorHuman, dir, "01ARZ3NDEKTSV4RRFFQ69G5FAV")
	wantWiperr(t, err, "validation.label-ulid-shaped")
}

func TestSetLabel_Rename(t *testing.T) {
	s := newStore(t)
	dir := newRepo(t, "widget")
	addRemote(t, dir, "origin", "git@github.com:acme/widget.git")
	result, err := Init(ctx, s, store.ActorHuman, dir, "")
	if err != nil {
		t.Fatalf("Init: %v", err)
	}

	renamed, err := SetLabel(ctx, s, store.ActorHuman, dir, "primary")
	if err != nil {
		t.Fatalf("SetLabel: %v", err)
	}
	if renamed.ID != result.Clone.ID {
		t.Error("SetLabel changed the Clone's identity")
	}
	if renamed.Label != "primary" {
		t.Errorf("Label = %q, want %q", renamed.Label, "primary")
	}
}

// TestSetLabel_CollisionRefusesAndSuggests exercises the Brief's own two
// collision tiers directly against `wip label`'s refusal path: the parent
// dir name when free, and <basename>-<parent> when the parent dir name is
// also taken.
func TestSetLabel_CollisionRefusesAndSuggests(t *testing.T) {
	s := newStore(t)
	root := t.TempDir()

	a := root + "/widget-a"
	run(t, root, "init", "-q", "widget-a")
	addRemote(t, a, "origin", "git@github.com:acme/widget.git")
	if _, err := Init(ctx, s, store.ActorHuman, a, ""); err != nil {
		t.Fatalf("Init(a): %v", err)
	}

	b := root + "/widget-b"
	run(t, root, "init", "-q", "widget-b")
	addRemote(t, b, "origin", "https://github.com/acme/widget.git")
	if _, err := Init(ctx, s, store.ActorHuman, b, ""); err != nil {
		t.Fatalf("Init(b): %v", err)
	}

	// b's own label is "widget-b"; asking it to take a's label "widget-a"
	// collides. The parent dir's own name is free ("root"'s basename), so
	// that should be the proposed alternative.
	_, err := SetLabel(ctx, s, store.ActorHuman, b, "widget-a")
	if err == nil {
		t.Fatal("SetLabel succeeded despite a same-repo label collision")
	}
	wantWiperr(t, err, "validation.label-collision")
	var werr *wiperr.Error
	asWiperr(err, &werr)
	if !strings.Contains(werr.Message, "try") {
		t.Errorf("collision message %q does not name a suggested alternative", werr.Message)
	}
}

func TestSetLabel_UnknownCloneRefuses(t *testing.T) {
	s := newStore(t)
	dir := newRepo(t, "never-init") // never `wip init`'d
	addRemote(t, dir, "origin", "git@github.com:acme/never-init.git")

	_, err := SetLabel(ctx, s, store.ActorHuman, dir, "whatever")
	wantWiperr(t, err, "refusal.unknown-clone")
}
