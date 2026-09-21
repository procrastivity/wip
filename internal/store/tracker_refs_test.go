package store

import (
	"path/filepath"
	"strings"
	"testing"
)

// The Repo-scoped read of tracker memberships: the (matter, ref) pairs
// `wip plumbing tracker refs` lists.
//
// tracker_substrate_test.go owns the write side of this table — what each
// reference event projects, and what it refuses. What this file owns is the
// one thing a widened read can get wrong that a matter-scoped one cannot: a
// row is live only when *both* its own removal column and the holding Matter's
// tombstone are null, and the fold writes those two through entirely different
// events.

// refPicture renders a listing as "<locator> <ref>" entries — the shape the
// verb prints, and the only part of a row a failure message can be read
// against. Identity is asserted separately and once; a picture of ULIDs would
// be unreadable.
func refPicture(refs []MatterReference) string {
	out := make([]string, 0, len(refs))
	for _, r := range refs {
		out = append(out, r.Locator+" "+r.Ref)
	}
	return strings.Join(out, ", ")
}

// wantRepoReferences asserts a Repo's whole listing, in order, and returns it
// so a caller can go on to assert identity.
func (h *harness) wantRepoReferences(what, repo string, want ...string) []MatterReference {
	h.t.Helper()
	got, err := h.RepoTrackerReferences(h.ctx, repo)
	if err != nil {
		h.t.Fatalf("%s: read the tracker references: %v", what, err)
	}
	if refPicture(got) != strings.Join(want, ", ") {
		h.t.Fatalf("%s reads [%s], want [%s]", what, refPicture(got), strings.Join(want, ", "))
	}
	return got
}

// TestRepoTrackerReferencesListEveryLiveBindingInBirthOrder is the central
// claim: one row per live membership, ordered by the event that made it, with
// both names of the Matter on it.
func TestRepoTrackerReferencesListEveryLiveBindingInBirthOrder(t *testing.T) {
	h := newHarness(t)

	first := h.matter("first-matter", "First matter")
	second := h.matter("second-matter", "Second matter")

	// The second Matter binds first, so birth order is neither locator order
	// nor Matter creation order — which is the only way to tell the ORDER BY
	// is doing something.
	h.commit(Draft{Type: TypeReferenceAdded, Subject: second, Payload: ReferenceAdded{Ref: "GH-2"}})
	h.commit(Draft{Type: TypeReferenceAdded, Subject: first, Payload: ReferenceAdded{Ref: "GH-1"}})
	h.commit(Draft{Type: TypeReferenceAdded, Subject: first, Payload: ReferenceAdded{Ref: "GL-9"}})

	got := h.wantRepoReferences("three live bindings", h.Repo,
		"second-matter GH-2", "first-matter GH-1", "first-matter GL-9")

	// The locator is for a person; the identity is what every consumer of this
	// read addresses the Matter by.
	wantMatters := []string{second, first, first}
	for i, r := range got {
		if r.Matter != wantMatters[i] {
			t.Errorf("row %d (%s) names matter %s, want %s", i, r.Ref, r.Matter, wantMatters[i])
		}
	}

	// A removed membership leaves the listing. The row itself survives (D44),
	// which is exactly what the removed_event filter is for.
	h.commit(Draft{Type: TypeReferenceRemoved, Subject: first, Payload: ReferenceRemoved{Ref: "GH-1"}})
	h.wantRepoReferences("after unbinding GH-1", h.Repo,
		"second-matter GH-2", "first-matter GL-9")
	h.wantRowCount("the unbound membership survives as a row", "tracker_references",
		"matter=? AND ref=? AND removed_event IS NOT NULL", []any{first, "GH-1"}, 1)

	// A rebind reads as one row carrying the new ref, at the rebind's own
	// birth position: the source is retired and the destination is born by the
	// same event.
	h.commit(Draft{
		Type: TypeReferenceRebound, Subject: second,
		Payload: ReferenceRebound{From: "GH-2", To: "BB-7"},
	})
	rebound := h.wantRepoReferences("after rebinding GH-2 to BB-7", h.Repo,
		"first-matter GL-9", "second-matter BB-7")
	if len(rebound) != 2 {
		t.Fatalf("a rebind left %d rows, want 2", len(rebound))
	}
	h.wantRowCount("the rebound Matter holds exactly one live membership", "tracker_references",
		"matter=? AND removed_event IS NULL", []any{second}, 1)
}

// TestRepoTrackerReferencesDropARemovedMattersBindings is the condition a
// reference-row-only read would miss: removing a Matter appends no reference
// event, so nothing about the membership row changes.
func TestRepoTrackerReferencesDropARemovedMattersBindings(t *testing.T) {
	h := newHarness(t)

	kept := h.matter("kept", "A Matter that stays")
	folded := h.matter("folded", "A Matter that is folded away")
	h.commit(Draft{Type: TypeReferenceAdded, Subject: kept, Payload: ReferenceAdded{Ref: "GH-kept"}})
	h.commit(Draft{Type: TypeReferenceAdded, Subject: folded, Payload: ReferenceAdded{Ref: "GH-folded"}})
	h.wantRepoReferences("before the removal", h.Repo, "kept GH-kept", "folded GH-folded")

	h.commit(Draft{
		Type: TypeStepRemoved, Subject: folded,
		Payload: Removed{Reason: "folded into another Matter"},
	})

	// The membership row is untouched — still live by its own column — so the
	// node's tombstone is the only thing that can drop it from the listing.
	h.wantRowCount("the folded Matter's membership is still live by its own column",
		"tracker_references", "matter=? AND ref=? AND removed_event IS NULL",
		[]any{folded, "GH-folded"}, 1)
	h.wantRepoReferences("after the removal", h.Repo, "kept GH-kept")

	// The reference is readable as history, which is the point of a tombstone
	// rather than a delete: the Matter is gone and the ref still resolves.
	if refs, err := h.TrackerReferences(h.ctx, folded); err != nil || len(refs) != 1 || refs[0] != "GH-folded" {
		t.Fatalf("the removed Matter's own references read %v (err=%v), want [GH-folded]", refs, err)
	}
}

// TestRepoTrackerReferencesAreScopedToOneRepo covers the join's Repo predicate
// and the empty answer, which is a legitimate steady state rather than an
// absence.
func TestRepoTrackerReferencesAreScopedToOneRepo(t *testing.T) {
	h := newHarness(t)

	otherRepo := h.attachRepo(RepoAttached{Label: "other"})
	otherClone := h.attachClone(CloneAttached{Repo: otherRepo, GitCommonDir: "/tmp/other/.git", Label: "main"})
	other := h.with(Env{
		Repo:     otherRepo,
		Clone:    otherClone,
		Worktree: h.attachWorktree(otherRepo, WorktreeAttached{Clone: otherClone}),
	})

	mine := h.matter("mine", "Mine")
	h.commit(Draft{Type: TypeReferenceAdded, Subject: mine, Payload: ReferenceAdded{Ref: "GH-mine"}})

	theirs := other.matter("theirs", "Theirs")
	other.commit(Draft{Type: TypeReferenceAdded, Subject: theirs, Payload: ReferenceAdded{Ref: "GH-theirs"}})

	h.wantRepoReferences("this Repo", h.Repo, "mine GH-mine")
	h.wantRepoReferences("the other Repo", otherRepo, "theirs GH-theirs")

	// A Repo whose Matters bind nothing reads empty, not as an error and not
	// as somebody else's rows.
	bare := h.attachRepo(RepoAttached{Label: "bare"})
	bareClone := h.attachClone(CloneAttached{Repo: bare, GitCommonDir: "/tmp/bare/.git", Label: "main"})
	h.with(Env{
		Repo:     bare,
		Clone:    bareClone,
		Worktree: h.attachWorktree(bare, WorktreeAttached{Clone: bareClone}),
	}).matter("unbound", "A Matter that binds nothing")

	got, err := h.RepoTrackerReferences(h.ctx, bare)
	if err != nil {
		t.Fatalf("a Repo with no bindings: %v", err)
	}
	if len(got) != 0 {
		t.Fatalf("a Repo with no bindings reads [%s], want nothing", refPicture(got))
	}
}

// TestRepoTrackerReferencesAreEmptyBeforeTheTrackerSubstrate is the schema
// gate. A v4 store has no tracker_references table, so the read answers empty
// instead of failing on a missing table — the same contract Outbox keeps.
func TestRepoTrackerReferencesAreEmptyBeforeTheTrackerSubstrate(t *testing.T) {
	h := newHarnessAt(t, filepath.Join(t.TempDir(), "wip.db"), register[:4], 4)
	h.matter("pre-tracker", "A Matter older than the substrate")

	// Read the absence from the database, not from a comment: if v4 ever grew
	// the table, the gate below would be testing nothing.
	var tables int
	if err := h.db.QueryRowContext(h.ctx,
		`SELECT count(*) FROM sqlite_master WHERE type='table' AND name='tracker_references'`).
		Scan(&tables); err != nil {
		t.Fatalf("read the v4 schema: %v", err)
	}
	if tables != 0 {
		t.Fatal("v4 already has a tracker_references table; this gate asserts nothing")
	}

	got, err := h.RepoTrackerReferences(h.ctx, h.Repo)
	if err != nil {
		t.Fatalf("a v4 store must answer the read, not fail it: %v", err)
	}
	if len(got) != 0 {
		t.Fatalf("a v4 store reads %d bindings, want none", len(got))
	}
}
