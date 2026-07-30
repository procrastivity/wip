package tiers

import (
	"testing"

	"github.com/procrastivity/wip/internal/store"
)

func TestIsIdentityShaped(t *testing.T) {
	if !store.IsIdentityShaped("01ARZ3NDEKTSV4RRFFQ69G5FAV") {
		t.Error("a real 26-char Crockford ULID was not recognized as identity-shaped")
	}
	for _, s := range []string{"widget-a", "", "01ARZ3NDEKTSV4RRFFQ69G5FA", "01ARZ3NDEKTSV4RRFFQ69G5FAVX", "0lARZ3NDEKTSV4RRFFQ69G5FAV"} {
		if store.IsIdentityShaped(s) {
			t.Errorf("%q was recognized as identity-shaped, want not", s)
		}
	}
}

// TestResolveClone_ULIDDispatch confirms a ULID-shaped locator resolves by
// identity, never by label lookup.
func TestResolveClone_ULIDDispatch(t *testing.T) {
	s := newStore(t)
	dir := newRepo(t, "widget")
	addRemote(t, dir, "origin", "git@github.com:acme/widget.git")
	result, err := Init(ctx, s, store.ActorHuman, dir, "")
	if err != nil {
		t.Fatalf("Init: %v", err)
	}

	got, err := ResolveClone(ctx, s.View, "", result.Clone.ID)
	if err != nil {
		t.Fatalf("ResolveClone by ULID: %v", err)
	}
	if got.ID != result.Clone.ID {
		t.Errorf("resolved %s, want %s", got.ID, result.Clone.ID)
	}
}

// TestResolveClone_LabelDispatch_ScopedAndUnscoped confirms label dispatch,
// scoped to a repo when given and searched globally otherwise, erroring on
// cross-repo ambiguity rather than guessing.
func TestResolveClone_LabelDispatch_ScopedAndUnscoped(t *testing.T) {
	s := newStore(t)
	a := newRepo(t, "widget")
	addRemote(t, a, "origin", "git@github.com:acme/widget.git")
	resultA, err := Init(ctx, s, store.ActorHuman, a, "")
	if err != nil {
		t.Fatalf("Init(a): %v", err)
	}

	got, err := ResolveClone(ctx, s.View, "", "widget")
	if err != nil {
		t.Fatalf("ResolveClone by label, unscoped: %v", err)
	}
	if got.ID != resultA.Clone.ID {
		t.Errorf("resolved %s, want %s", got.ID, resultA.Clone.ID)
	}

	// A second Repo whose clone happens to share the same label — legal,
	// since labels are unique only within their Repo.
	b := newRepo(t, "widget-elsewhere")
	addRemote(t, b, "origin", "git@github.com:other/other-repo.git")
	resultB, err := Init(ctx, s, store.ActorHuman, b, "")
	if err != nil {
		t.Fatalf("Init(b): %v", err)
	}
	if _, err := SetLabel(ctx, s, store.ActorHuman, b, "widget"); err != nil {
		t.Fatalf("SetLabel(b, widget): %v", err)
	}

	if _, err := ResolveClone(ctx, s.View, "", "widget"); err == nil {
		t.Fatal("unscoped ResolveClone succeeded despite a cross-repo label collision, want validation.ambiguous-locator")
	} else {
		wantWiperr(t, err, "validation.ambiguous-locator")
	}

	// Scoped to the right repo, it's unambiguous.
	scoped, err := ResolveClone(ctx, s.View, resultB.Repo.ID, "widget")
	if err != nil {
		t.Fatalf("ResolveClone scoped to repo b: %v", err)
	}
	if scoped.ID != resultB.Clone.ID {
		t.Errorf("resolved %s, want %s", scoped.ID, resultB.Clone.ID)
	}
}

func TestResolveClone_UnknownLocator(t *testing.T) {
	s := newStore(t)
	if _, err := ResolveClone(ctx, s.View, "", "nope"); err == nil {
		t.Fatal("resolved a locator that names nothing, want an error")
	} else {
		wantWiperr(t, err, "validation.unknown-locator")
	}
}
