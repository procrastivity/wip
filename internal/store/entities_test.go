package store

import (
	"context"
	"strings"
	"testing"
)

// Tests for the entity tables and the projection rules that maintain them (the
// `schema` Brief's entity list; step-03 of this Matter).
//
// Under D61 every table below is a *projection*: the row exists because an event
// said so, and applyEvent is the only thing that may put it there. So every
// assertion here is driven through Store.Commit — never by writing a row — and
// what it asserts is the row the event produced, column for column. harness.
// wantRow is strict about that on purpose: it fails on a column the test says
// nothing about, so a column a later migration adds cannot slip past unasserted.
//
// The raw-SQL assertions are the other half. A CHECK constraint, a unique index
// and a depth trigger are claims about the substrate, and a write path built to
// make them unreachable cannot demonstrate them — so those go around the API,
// which was the losing spike's single best technique (docs/store-fork).

// ---------------------------------------------------------------------------
// Helpers these tests share
// ---------------------------------------------------------------------------

// wantColumns asserts a table's shape in declaration order. It is how the tables
// with no P1 verb (Run, OutboxEntry) are held to existing "from event one"
// (MODEL §10) when there is no event to drive them with.
func wantColumns(h *harness, table string, want []string) {
	h.t.Helper()
	got := h.columnsOf(table)
	if strings.Join(got, ", ") != strings.Join(want, ", ") {
		h.t.Errorf("%s is (%s), want (%s)", table, strings.Join(got, ", "), strings.Join(want, ", "))
	}
}

// nodeAt creates a node at one scale, with whatever ancestors that scale needs,
// and returns it. The lifecycle is uniform at every scale (MODEL §2.2), and this
// is what lets one test body say so by running three times.
func (h *harness) nodeAt(scale Scale, locator, title string) string {
	h.t.Helper()
	switch scale {
	case ScaleMatter:
		return h.matter(locator, title)
	case ScaleStage:
		return h.stage(h.matter("host-"+locator, "Host"), locator, title)
	case ScaleStep:
		return h.step(h.matter("host-"+locator, "Host"), locator, title)
	default:
		h.t.Fatalf("%q is not a scale", scale)
		return ""
	}
}

// wantArchive asserts exactly which of a Repo's Matters are sealed.
func (h *harness) wantArchive(what string, want ...string) {
	h.t.Helper()
	sealed, err := h.ArchivedMatters(h.ctx, h.Repo)
	if err != nil {
		h.t.Fatalf("read the archive: %v", err)
	}
	got := make([]string, 0, len(sealed))
	for _, n := range sealed {
		got = append(got, n.ID)
	}
	if strings.Join(got, " ") != strings.Join(want, " ") {
		h.t.Errorf("%s: the archive holds [%s], want [%s]", what,
			strings.Join(got, " "), strings.Join(want, " "))
	}
}

// ---------------------------------------------------------------------------
// 1. The three tier rows (MODEL §7, D37)
// ---------------------------------------------------------------------------

// TestARepoNeedsNoRemoteAndTwoLocalOnesDifferByIdentityAlone is the nullable half
// of D37's natural key. A Repo needs no remote (D39), and the store therefore has
// to be able to hold two of them that are alike in every column but the one that
// is never a key: identity.
func TestARepoNeedsNoRemoteAndTwoLocalOnesDifferByIdentityAlone(t *testing.T) {
	h := newHarness(t)

	second := h.attachRepo(RepoAttached{Label: "fixture"})
	if second == h.Repo {
		t.Fatal("the second Repo reused the first one's identity")
	}

	for _, repo := range []string{h.Repo, second} {
		birth := h.birthEventOf(repo)
		h.wantRow("a local-only Repo", "repos", "id = ?", []any{repo}, map[string]any{
			"id": repo,
			// The natural key is absent, not empty: there is no remote, which is a
			// different fact from a remote whose URL is the empty string.
			"remote_url":      nil,
			"identity_remote": nil,
			"label":           "fixture",
			"birth_event":     birth.ID,
			"last_event":      birth.ID,
		})
	}

	// Two Repos with the same label and no remote at all, distinguished by nothing
	// but their ULIDs — which is exactly what "paths and labels are attributes,
	// never keys" has to mean if it means anything.
	h.wantRowCount("two local-only Repos", "repos", "remote_url IS NULL", nil, 2)
}

// TestARepoWithARemoteRefusesASecondClaimant is the other half: where the natural
// key is present it is unique, and a Repo adopting a remote another already claims
// is refused by the store rather than discovered later.
func TestARepoWithARemoteRefusesASecondClaimant(t *testing.T) {
	h := newHarness(t)

	const remote = "git@github.com:procrastivity/wip.git"
	owner := h.attachRepo(RepoAttached{RemoteURL: remote, IdentityRemote: "origin", Label: "wip"})
	birth := h.birthEventOf(owner)
	h.wantRow("a Repo with a remote", "repos", "id = ?", []any{owner}, map[string]any{
		"id":         owner,
		"remote_url": remote,
		// Which remote name the URL came from, so origin-is-my-fork has an explicit
		// answer rather than a default.
		"identity_remote": "origin",
		"label":           "wip",
		"birth_event":     birth.ID,
		"last_event":      birth.ID,
	})

	claimant := h.NewID()
	err := h.with(Env{Repo: claimant}).commitError(func(_ context.Context, _ *Tx) ([]Draft, error) {
		return []Draft{{
			Type:    TypeRepoAttached,
			Subject: claimant,
			Payload: RepoAttached{RemoteURL: remote, Label: "a second claimant"},
		}}, nil
	})
	refusalMentions(t, "a second Repo claiming one remote", err, "UNIQUE constraint failed: repos.remote_url")

	// The refusal took the event with it: the whole command is one transaction, so
	// there is no log entry claiming a Repo that does not exist.
	h.wantRowCount("after the refusal", "repos", "id = ?", []any{claimant}, 0)
	if events := h.eventsOf(claimant); len(events) != 0 {
		t.Errorf("the refused command left %d events behind", len(events))
	}

	got, ok, err := h.RepoByRemote(h.ctx, remote)
	if err != nil {
		t.Fatalf("resolve the Repo by its natural key: %v", err)
	}
	if !ok || got.ID != owner {
		t.Errorf("the remote resolves to %q (found=%v), want %s", got.ID, ok, owner)
	}
}

// TestAdoptingARemoteKeepsTheRepoAndItsHistory is remote adoption (D37, PLAN 1.1).
//
// The point of a ULID identity next to a mutable natural key is that gaining a
// remote is an *update*, not a new Repo: nothing downstream keys on the natural
// key, so the identity survives, and with it every event that ever named it.
func TestAdoptingARemoteKeepsTheRepoAndItsHistory(t *testing.T) {
	h := newHarness(t)

	before := h.eventsOf(h.Repo)
	birth := before[0]

	const remote = "https://github.com/procrastivity/wip.git"
	adopt := h.commit(Draft{
		Type:    TypeRepoKeyAdopted,
		Subject: h.Repo,
		Payload: RepoKeyAdopted{RemoteURL: remote, IdentityRemote: "origin"},
	})[0]

	h.wantRow("a Repo that adopted a remote", "repos", "id = ?", []any{h.Repo}, map[string]any{
		// The identity is the one it was born with.
		"id":              h.Repo,
		"remote_url":      remote,
		"identity_remote": "origin",
		"label":           "fixture",
		// Born by the same event it always was; only last_event moved.
		"birth_event": birth.ID,
		"last_event":  adopt.ID,
	})

	// Every event of that subject still resolves, because history is addressed by
	// identity and the identity did not change (MODEL §10).
	after := h.eventsOf(h.Repo)
	if len(after) != len(before)+1 {
		t.Fatalf("the Repo has %d events after adoption, want %d", len(after), len(before)+1)
	}
	for i, ev := range before {
		if after[i].ID != ev.ID || after[i].Type != ev.Type {
			t.Errorf("event %d of the Repo's history changed: %s (%s), was %s (%s)",
				i, after[i].ID, after[i].Type, ev.ID, ev.Type)
		}
	}

	// Adoption is for a Repo that has no remote yet. A Repo that already has one is
	// not adopting; it would be replacing a key, which this event does not mean.
	err := h.commitError(func(_ context.Context, _ *Tx) ([]Draft, error) {
		return []Draft{{
			Type:    TypeRepoKeyAdopted,
			Subject: h.Repo,
			Payload: RepoKeyAdopted{RemoteURL: "git@example.com:other/thing.git"},
		}}, nil
	})
	refusalMentions(t, "adopting a remote onto a Repo that has one", err, "touched 0 projection rows")

	// And an adoption that names no remote is not an adoption.
	err = h.commitError(func(_ context.Context, _ *Tx) ([]Draft, error) {
		return []Draft{{Type: TypeRepoKeyAdopted, Subject: h.Repo, Payload: RepoKeyAdopted{}}}, nil
	})
	refusalMentions(t, "an adoption naming no remote", err, "must name the remote being adopted")
}

// TestACloneKeysOnItsGitCommonDirAndLabelsAreUniquePerRepo holds the Clone's two
// uniqueness rules apart. The git-common-dir is the natural key and is unique
// store-wide, because it resolves identically from a subdirectory and from a
// linked worktree. The label is a mutable attribute and is unique only within its
// Repo (PLAN 1.1) — two Repos may each have a clone called `main`.
func TestACloneKeysOnItsGitCommonDirAndLabelsAreUniquePerRepo(t *testing.T) {
	h := newHarness(t)

	birth := h.birthEventOf(h.Clone)
	h.wantRow("a Clone", "clones", "id = ?", []any{h.Clone}, map[string]any{
		"id":             h.Clone,
		"repo":           h.Repo,
		"git_common_dir": "/tmp/fixture/.git",
		"label":          "main",
		"birth_event":    birth.ID,
		"last_event":     birth.ID,
	})

	err := h.commitError(func(_ context.Context, tx *Tx) ([]Draft, error) {
		return []Draft{{
			Type:    TypeCloneAttached,
			Subject: tx.NewID(),
			Payload: CloneAttached{Repo: h.Repo, GitCommonDir: "/tmp/fixture/.git", Label: "again"},
		}}, nil
	})
	refusalMentions(t, "a second Clone at one git-common-dir", err, "UNIQUE constraint failed: clones.git_common_dir")

	err = h.commitError(func(_ context.Context, tx *Tx) ([]Draft, error) {
		return []Draft{{
			Type:    TypeCloneAttached,
			Subject: tx.NewID(),
			Payload: CloneAttached{Repo: h.Repo, GitCommonDir: "/tmp/elsewhere/.git", Label: "main"},
		}}, nil
	})
	refusalMentions(t, "a second Clone labelled main in one Repo", err, "UNIQUE constraint failed: clones.repo, clones.label")

	// The same label under another Repo is not a collision: the label is per-repo.
	other := h.attachRepo(RepoAttached{Label: "other"})
	elsewhere := h.attachClone(CloneAttached{
		Repo: other, GitCommonDir: "/tmp/elsewhere/.git", Label: "main",
	})
	h.wantRowCount("two Repos with a clone called main", "clones", "label = 'main'", nil, 2)
	if elsewhere == h.Clone {
		t.Error("the second Repo's main clone reused the first one's identity")
	}
}

// TestACloneCanBeRelinkedAndRelabelled is the mutable half of the Clone row. Both
// are the same shape as remote adoption: the ULID and the history stay, one
// attribute moves.
func TestACloneCanBeRelinkedAndRelabelled(t *testing.T) {
	h := newHarness(t)

	birth := h.birthEventOf(h.Clone)
	relink := h.commit(Draft{
		Type:    TypeCloneRelinked,
		Subject: h.Clone,
		Payload: CloneRelinked{GitCommonDir: "/tmp/moved/.git"},
	})[0]
	h.wantRow("a relinked Clone", "clones", "id = ?", []any{h.Clone}, map[string]any{
		"id":             h.Clone,
		"repo":           h.Repo,
		"git_common_dir": "/tmp/moved/.git",
		"label":          "main",
		"birth_event":    birth.ID,
		"last_event":     relink.ID,
	})

	label := h.commit(Draft{
		Type:    TypeCloneLabeled,
		Subject: h.Clone,
		Payload: CloneLabeled{Label: "primary"},
	})[0]
	h.wantRow("a relabelled Clone", "clones", "id = ?", []any{h.Clone}, map[string]any{
		"id":             h.Clone,
		"repo":           h.Repo,
		"git_common_dir": "/tmp/moved/.git",
		"label":          "primary",
		"birth_event":    birth.ID,
		"last_event":     label.ID,
	})

	// The read surface resolves it at the new key and not the old one.
	if _, ok, err := h.CloneByCommonDir(h.ctx, "/tmp/fixture/.git"); err != nil || ok {
		t.Errorf("the old git-common-dir still resolves (found=%v, err=%v)", ok, err)
	}
	got, ok, err := h.CloneByCommonDir(h.ctx, "/tmp/moved/.git")
	if err != nil || !ok || got.ID != h.Clone {
		t.Errorf("the new git-common-dir resolves to %q (found=%v, err=%v), want %s", got.ID, ok, err, h.Clone)
	}

	// Relinking a Clone that does not exist moves nothing, and a projection that
	// silently agreed would be a projection that had stopped being one.
	err = h.commitError(func(_ context.Context, tx *Tx) ([]Draft, error) {
		return []Draft{{
			Type:    TypeCloneRelinked,
			Subject: tx.NewID(),
			Payload: CloneRelinked{GitCommonDir: "/tmp/nowhere/.git"},
		}}, nil
	})
	refusalMentions(t, "relinking a Clone that does not exist", err, "touched 0 projection rows")
}

// TestTheMainWorktreeHasNoNameAndACloneHasOnlyOne is D37's null case and the
// reason the unique index is over COALESCE(name, ”) rather than over name.
// SQLite treats NULLs as distinct, so a bare unique index would happily let one
// Clone have two main worktrees.
func TestTheMainWorktreeHasNoNameAndACloneHasOnlyOne(t *testing.T) {
	h := newHarness(t)

	birth := h.birthEventOf(h.Worktree)
	h.wantRow("the main worktree", "worktrees", "id = ?", []any{h.Worktree}, map[string]any{
		"id":    h.Worktree,
		"clone": h.Clone,
		// Absent, not empty: the main worktree has no name.
		"name":        nil,
		"birth_event": birth.ID,
		"last_event":  birth.ID,
	})

	named := h.attachWorktree(h.Repo, WorktreeAttached{Clone: h.Clone, Name: "feature"})
	namedBirth := h.birthEventOf(named)
	h.wantRow("a linked worktree", "worktrees", "id = ?", []any{named}, map[string]any{
		"id":          named,
		"clone":       h.Clone,
		"name":        "feature",
		"birth_event": namedBirth.ID,
		"last_event":  namedBirth.ID,
	})

	err := h.commitError(func(_ context.Context, tx *Tx) ([]Draft, error) {
		return []Draft{{
			Type:    TypeWorktreeAttached,
			Subject: tx.NewID(),
			Payload: WorktreeAttached{Clone: h.Clone},
		}}, nil
	})
	refusalMentions(t, "a second main worktree under one Clone", err, "UNIQUE constraint failed: index 'worktrees_name'")

	err = h.commitError(func(_ context.Context, tx *Tx) ([]Draft, error) {
		return []Draft{{
			Type:    TypeWorktreeAttached,
			Subject: tx.NewID(),
			Payload: WorktreeAttached{Clone: h.Clone, Name: "feature"},
		}}, nil
	})
	refusalMentions(t, "two worktrees of one name under one Clone", err, "UNIQUE constraint failed: index 'worktrees_name'")

	// Another Clone's main worktree is its own.
	other := h.attachRepo(RepoAttached{Label: "other"})
	otherClone := h.attachClone(CloneAttached{Repo: other, GitCommonDir: "/tmp/other/.git", Label: "main"})
	h.attachWorktree(other, WorktreeAttached{Clone: otherClone})
	h.wantRowCount("two Clones with a main worktree", "worktrees", "name IS NULL", nil, 2)
}

// ---------------------------------------------------------------------------
// 2. Nodes at all three scales
// ---------------------------------------------------------------------------

// TestANodeExistsAtEveryScaleAndKnowsItsMatter walks the whole tree the model
// allows: a Matter, a Stage under it, a Step under the Matter directly, and a Step
// under the Stage. One table holds all four, discriminated by kind, because
// `blocked-by` puts any node on either end of an edge and gate state is keyed by
// node — both of which need a single identity space.
//
// Stage's entity-hood is what the third row asserts: it is a row exactly like
// Step, with its own ULID, its own sort key, and a lifecycle of its own.
func TestANodeExistsAtEveryScaleAndKnowsItsMatter(t *testing.T) {
	h := newHarness(t)

	matter := h.matter("shape", "The shape of the tree")
	stage := h.stage(matter, "stage-1", "A grouping")
	underMatter := h.step(matter, "step-01", "A Step the Matter owns directly")
	underStage := h.step(stage, "step-02", "A Step the Stage groups")

	for _, want := range []struct {
		what    string
		id      string
		kind    Scale
		parent  any
		locator string
		title   string
		sortKey int64
	}{
		{"a Matter", matter, ScaleMatter, nil, "shape", "The shape of the tree", sortKeyGap},
		{"a Stage under a Matter", stage, ScaleStage, matter, "stage-1", "A grouping", sortKeyGap},
		{"a Step under a Matter", underMatter, ScaleStep, matter, "step-01", "A Step the Matter owns directly", 2 * sortKeyGap},
		{"a Step under a Stage", underStage, ScaleStep, stage, "step-02", "A Step the Stage groups", sortKeyGap},
	} {
		birth := h.birthEventOf(want.id)
		h.wantRow(want.what, "nodes", "id = ?", []any{want.id}, map[string]any{
			"id":   want.id,
			"kind": want.kind,
			"repo": h.Repo,
			// Derived, never carried: a Matter is its own Matter and everything else
			// inherits its parent's.
			"matter":    matter,
			"parent":    want.parent,
			"locator":   want.locator,
			"title":     want.title,
			"lifecycle": Planned,
			"sort_key":  want.sortKey,
			// Nothing binds a reference at birth; references are acquired (MODEL §5).
			"external_ref":    nil,
			"birth_event":     birth.ID,
			"last_event":      birth.ID,
			"tombstone_event": nil,
		})
	}

	// Every node in the Matter, the Matter included — which is what makes `matter`
	// usable as the addressing scope for the root as well as its descendants.
	nodes, err := h.MatterNodes(h.ctx, matter)
	if err != nil {
		t.Fatalf("read the Matter's nodes: %v", err)
	}
	if len(nodes) != 4 {
		t.Errorf("the Matter holds %d nodes, want 4 (itself, a Stage and two Steps)", len(nodes))
	}
}

// TestAPayloadCannotContradictTheDerivedMatter is the point of deriving the
// column rather than carrying it. `matter` is resolved from the parent inside the
// projection, so a payload that names one is not wrong — it is not consulted.
func TestAPayloadCannotContradictTheDerivedMatter(t *testing.T) {
	h := newHarness(t)

	host := h.matter("host", "The real Matter")
	stage := h.stage(host, "stage-1", "A grouping")
	impostor := h.matter("impostor", "Another Matter entirely")

	// A hand-built payload, because NodeBirth has no field for this: a verb cannot
	// express the contradiction at all, and going around the struct is the only way
	// to show the projection would ignore it if one could.
	step := h.commitOne(func(_ context.Context, tx *Tx) (Draft, error) {
		return Draft{
			Type:    TypeStepCreated,
			Subject: tx.NewID(),
			Payload: map[string]any{
				"title":    "A Step with opinions about its Matter",
				"locator":  "step-01",
				"parent":   stage,
				"sort_key": sortKeyGap,
				"matter":   impostor,
			},
		}, nil
	})
	node, err := h.Node(h.ctx, step)
	if err != nil {
		t.Fatalf("read the Step: %v", err)
	}
	if node.Matter != host {
		t.Errorf("the Step's Matter is %s, want %s (the payload named %s and must not be heard)",
			node.Matter, host, impostor)
	}

	// A Matter has no parent, and one that names something is a contradiction the
	// projection refuses outright rather than resolving.
	err = h.commitError(func(_ context.Context, tx *Tx) ([]Draft, error) {
		return []Draft{{
			Type:    TypeMatterCreated,
			Subject: tx.NewID(),
			Payload: NodeBirth{Title: "A nested Matter", Locator: "nested", Parent: host, SortKey: sortKeyGap},
		}}, nil
	})
	refusalMentions(t, "a Matter naming a parent", err, "a Matter has no parent")

	// And anything else needs one: there is no free-floating Stage or Step.
	for _, eventType := range []string{TypeStageCreated, TypeStepCreated} {
		err = h.commitError(func(_ context.Context, tx *Tx) ([]Draft, error) {
			return []Draft{{
				Type:    eventType,
				Subject: tx.NewID(),
				Payload: NodeBirth{Title: "An orphan", Locator: "orphan", SortKey: sortKeyGap},
			}}, nil
		})
		refusalMentions(t, eventType+" with no parent", err, "needs a parent")
	}

	// A parent that is not a node at all is not resolvable either.
	err = h.commitError(func(_ context.Context, tx *Tx) ([]Draft, error) {
		return []Draft{{
			Type:    TypeStepCreated,
			Subject: tx.NewID(),
			Payload: NodeBirth{Title: "A Step under nothing", Locator: "step-02", Parent: h.NewID(), SortKey: sortKeyGap},
		}}, nil
	})
	refusalMentions(t, "a Step under an identity that is not a node", err, "which is not a node")
}

// TestMaxDepthIsMatterStageStep holds D20 to the schema rather than to the verbs.
// The two illegal shapes are a Stage under a Stage and a Step under a Step; both
// resolve a Matter perfectly well, which is precisely why the depth rule cannot
// live in matterOf and has to be a trigger.
func TestMaxDepthIsMatterStageStep(t *testing.T) {
	h := newHarness(t)

	matter := h.matter("depth", "Depth")
	stage := h.stage(matter, "stage-1", "A grouping")
	step := h.step(stage, "step-01", "A Step")

	for _, bad := range []struct {
		what      string
		eventType string
		parent    string
	}{
		{"a Stage under a Stage", TypeStageCreated, stage},
		{"a Stage under a Step", TypeStageCreated, step},
		{"a Step under a Step", TypeStepCreated, step},
	} {
		err := h.commitError(func(_ context.Context, tx *Tx) ([]Draft, error) {
			return []Draft{{
				Type:    bad.eventType,
				Subject: tx.NewID(),
				Payload: NodeBirth{Title: bad.what, Locator: "too-deep", Parent: bad.parent, SortKey: sortKeyGap},
			}}, nil
		})
		refusalMentions(t, bad.what, err, "max depth is Matter -> Stage -> Step")
	}

	// The two CHECKs that keep the root exactly one row deep, held around the API:
	// only a Matter has no parent, and only a Matter is its own Matter.
	rawRefusedBy(h, "a Stage with no parent", "CHECK constraint failed",
		`INSERT INTO nodes (id, kind, repo, matter, parent, locator, title, lifecycle, sort_key, birth_event, last_event)
		 VALUES (?, 'stage', ?, ?, NULL, 'rootless', 'A rootless Stage', 'planned', 1000, ?, ?)`,
		h.NewID(), h.Repo, matter, h.birthEventOf(matter).ID, h.birthEventOf(matter).ID)

	orphanMatter := h.NewID()
	rawRefusedBy(h, "a Matter that is not its own Matter", "CHECK constraint failed",
		`INSERT INTO nodes (id, kind, repo, matter, parent, locator, title, lifecycle, sort_key, birth_event, last_event)
		 VALUES (?, 'matter', ?, ?, NULL, 'borrowed', 'A Matter belonging to another', 'planned', 1000, ?, ?)`,
		orphanMatter, h.Repo, matter, h.birthEventOf(matter).ID, h.birthEventOf(matter).ID)
}

// TestATombstonedNodeIsNotAPlaceToPutANode is the other half of what removal
// means.
//
// A removed node keeps its identity and its history, and every prior event that
// named it stays a valid reference (D44) — that is the whole point. What it is not
// is somewhere new work can go: a live row under a tombstoned parent would be a
// node Children never reaches and MatterNodes still lists, which is the projection
// disagreeing with itself. Before this was checked it was accepted; see the step-03
// report.
func TestATombstonedNodeIsNotAPlaceToPutANode(t *testing.T) {
	h := newHarness(t)

	matter := h.matter("removal", "Removal")
	stage := h.stage(matter, "stage-1", "A grouping that was taken out")
	h.commit(Draft{Type: TypeStepRemoved, Subject: stage, Payload: Removed{Reason: "the grouping was not earned"}})

	err := h.commitError(func(_ context.Context, tx *Tx) ([]Draft, error) {
		return []Draft{{
			Type:    TypeStepCreated,
			Subject: tx.NewID(),
			Payload: NodeBirth{Title: "An orphan", Locator: "step-01", Parent: stage, SortKey: sortKeyGap},
		}}, nil
	})
	refusalMentions(t, "a Step born under a tombstoned Stage", err, "which was removed")
	h.wantRowCount("under a tombstoned Stage", "nodes", "parent = ? AND tombstone_event IS NULL", []any{stage}, 0)

	// The same for a removed Matter, and for step.inserted, which shares the rule.
	removedMatter := h.matter("gone", "A Matter that was folded away")
	h.commit(Draft{Type: TypeStepRemoved, Subject: removedMatter, Payload: Removed{}})
	for _, eventType := range []string{TypeStageCreated, TypeStepCreated, TypeStepInserted} {
		err = h.commitError(func(_ context.Context, tx *Tx) ([]Draft, error) {
			return []Draft{{
				Type:    eventType,
				Subject: tx.NewID(),
				Payload: NodeBirth{Title: "An orphan", Locator: "orphan", Parent: removedMatter, SortKey: sortKeyGap},
			}}, nil
		})
		refusalMentions(t, eventType+" under a tombstoned Matter", err, "which was removed")
	}

	// A live parent still works, so the guard is about the tombstone and not about
	// parents in general.
	replacementStage := h.stage(matter, "stage-2", "A grouping that stayed")
	if _, err := h.Node(h.ctx, h.step(replacementStage, "step-01", "A Step under a live Stage")); err != nil {
		t.Errorf("a Step under a live Stage: %v", err)
	}
}

// TestALocatorIsUniqueWithinItsMatterAndNotBeyondIt is D16, which is the reason
// the unique index is over (matter, locator) and not over (parent, locator).
//
// `step-NN` is globally sequential within a Matter. A Stage grouping Steps is a
// grouping and not a namespace: it neither renames nor renumbers them, so two
// Steps called step-01 under two different Stages of one Matter is a collision,
// and the same locator in two different Matters is not.
func TestALocatorIsUniqueWithinItsMatterAndNotBeyondIt(t *testing.T) {
	h := newHarness(t)

	first := h.matter("first", "The first Matter")
	second := h.matter("second", "The second Matter")

	here := h.step(first, "step-01", "step-01 of the first Matter")
	there := h.step(second, "step-01", "step-01 of the second Matter")
	if here == there {
		t.Fatal("the two Steps share an identity")
	}
	// Both resolve, each within its own scope.
	for _, m := range []string{first, second} {
		node, err := h.NodeByLocator(h.ctx, m, "step-01")
		if err != nil {
			t.Errorf("step-01 does not resolve in %s: %v", m, err)
			continue
		}
		if node.Matter != m {
			t.Errorf("step-01 in %s resolved to a node of %s", m, node.Matter)
		}
	}

	err := h.commitError(func(_ context.Context, tx *Tx) ([]Draft, error) {
		return []Draft{{
			Type:    TypeStepCreated,
			Subject: tx.NewID(),
			Payload: NodeBirth{Title: "A second step-01", Locator: "step-01", Parent: first, SortKey: 2 * sortKeyGap},
		}}, nil
	})
	refusalMentions(t, "two Steps called step-01 in one Matter", err, "UNIQUE constraint failed: nodes.matter, nodes.locator")

	// The D16 case: a Stage boundary is not a namespace boundary.
	stage := h.stage(first, "stage-1", "A grouping")
	err = h.commitError(func(_ context.Context, tx *Tx) ([]Draft, error) {
		return []Draft{{
			Type:    TypeStepCreated,
			Subject: tx.NewID(),
			Payload: NodeBirth{Title: "step-01 again, under a Stage", Locator: "step-01", Parent: stage, SortKey: sortKeyGap},
		}}, nil
	})
	refusalMentions(t, "step-01 twice in one Matter across a Stage boundary", err, "UNIQUE constraint failed: nodes.matter, nodes.locator")

	// A Stage and a Step may not share a locator either: the scope is the Matter and
	// the index does not care about kind.
	err = h.commitError(func(_ context.Context, tx *Tx) ([]Draft, error) {
		return []Draft{{
			Type:    TypeStageCreated,
			Subject: tx.NewID(),
			Payload: NodeBirth{Title: "A Stage called step-01", Locator: "step-01", Parent: first, SortKey: 3 * sortKeyGap},
		}}, nil
	})
	refusalMentions(t, "a Stage taking a Step's locator in one Matter", err, "UNIQUE constraint failed: nodes.matter, nodes.locator")

	// The scope has an edge the index does not cover, and it is recorded here rather
	// than left to be discovered: a Matter is its own Matter, so `(matter, locator)`
	// constrains a Matter's own slug only against itself, which is vacuous. Two
	// Matters of one Repo may therefore share a slug. Nothing in the store resolves
	// a Matter by slug today (NodeByLocator is given the Matter it searches within),
	// so nothing here is ambiguous yet — but `wip work <slug>` would be, and that is
	// a call for the addressing Matter and not for this Step. See the step-03 report.
	sameSlug := h.commitOne(func(_ context.Context, tx *Tx) (Draft, error) {
		return Draft{
			Type:    TypeMatterCreated,
			Subject: tx.NewID(),
			Payload: NodeBirth{Title: "A second Matter slugged first", Locator: "first", SortKey: sortKeyGap},
		}, nil
	})
	h.wantRowCount("two Matters slugged first", "nodes", "locator = 'first' AND kind = 'matter'", nil, 2)
	if sameSlug == first {
		t.Error("the second Matter reused the first one's identity")
	}
}

// TestEveryLifecycleTransitionHappensAtEveryScale is MODEL §2.2's uniformity, run
// three times over one body. The five states are the whole of it: gates are not
// states and a tombstone is not one either.
func TestEveryLifecycleTransitionHappensAtEveryScale(t *testing.T) {
	for _, scale := range []Scale{ScaleMatter, ScaleStage, ScaleStep} {
		t.Run(string(scale), func(t *testing.T) {
			h := newHarness(t)
			node := h.nodeAt(scale, "lifecycle", "A node with a life")
			birth := h.birthEventOf(node)
			parent, err := h.Node(h.ctx, node)
			if err != nil {
				t.Fatalf("read the node: %v", err)
			}

			// The whole row, re-asserted after every move: the point is as much that
			// nothing else changed as that the lifecycle did.
			row := map[string]any{
				"id":              node,
				"kind":            scale,
				"repo":            h.Repo,
				"matter":          parent.Matter,
				"parent":          nullable(parent.Parent),
				"locator":         "lifecycle",
				"title":           "A node with a life",
				"lifecycle":       Planned,
				"sort_key":        parent.SortKey,
				"external_ref":    nil,
				"birth_event":     birth.ID,
				"last_event":      birth.ID,
				"tombstone_event": nil,
			}
			h.wantRow("at birth", "nodes", "id = ?", []any{node}, row)

			for _, move := range []struct {
				verb lifecycleVerb
				want Lifecycle
			}{
				{verbStart, InProgress},
				{verbPause, Paused},
				{verbResume, InProgress},
				{verbFinish, Done},
			} {
				ev := h.move(node, move.verb)
				row["lifecycle"] = move.want
				row["last_event"] = ev.ID
				h.wantRow("after "+string(move.verb), "nodes", "id = ?", []any{node}, row)
			}

			// Canceled is a terminal state reachable from Planned: work that ended
			// without sealing, and not a deletion.
			other := h.nodeAt(scale, "canceled", "A node that ended early")
			canceled := h.cancel(other)
			h.wantLifecycle(other, Canceled)
			if got := h.rowOf("nodes", "id = ?", other)["last_event"]; got != sqlLit(t, canceled.ID) {
				t.Errorf("the canceled node's last_event is %s, want %s", got, sqlLit(t, canceled.ID))
			}
			// A canceled node is not tombstoned: it stays addressable and keeps its
			// whole history.
			tombstoned, err := h.Tombstoned(h.ctx, other)
			if err != nil {
				t.Fatalf("read the tombstone: %v", err)
			}
			if tombstoned {
				t.Error("cancelling a node tombstoned it; those are two different mechanisms")
			}
		})
	}
}

// TestATransitionThatCouldNotHaveHappenedIsRefused is applyTransition's
// `AND lifecycle = ?`, which is the load-bearing half of the rule.
//
// Without it the store would append an event describing a move that did not
// happen and then quietly agree with it. With it, the log and the projection
// cannot disagree: the write fails, the transaction rolls back, and the event is
// not in the log either.
func TestATransitionThatCouldNotHaveHappenedIsRefused(t *testing.T) {
	h := newHarness(t)

	matter := h.matter("guarded", "A guarded transition")
	before := h.eventsOf(matter)

	err := h.commitError(func(_ context.Context, _ *Tx) ([]Draft, error) {
		return []Draft{h.transitionDraft(matter, ScaleMatter, verbCancel, Done)}, nil
	})
	refusalMentions(t, "a move from a state the node was never in", err, "touched 0 projection rows")
	h.wantLifecycle(matter, Planned)
	if after := h.eventsOf(matter); len(after) != len(before) {
		t.Errorf("the refused transition left %d events behind", len(after)-len(before))
	}

	// The same guard catches the replayed transition: started twice, the second
	// naming a From the node has already left.
	h.start(matter)
	err = h.commitError(func(_ context.Context, _ *Tx) ([]Draft, error) {
		return []Draft{h.transitionDraft(matter, ScaleMatter, verbStart, Planned)}, nil
	})
	refusalMentions(t, "starting a node that is already started", err, "touched 0 projection rows")

	// Both ends are required, because From is what the guard is made of.
	err = h.commitError(func(_ context.Context, _ *Tx) ([]Draft, error) {
		return []Draft{{Type: TypeMatterFinished, Subject: matter, Payload: Transition{From: InProgress}}}, nil
	})
	refusalMentions(t, "a transition with no To", err, "must record both ends")

	err = h.commitError(func(_ context.Context, _ *Tx) ([]Draft, error) {
		return []Draft{{Type: TypeMatterFinished, Subject: matter, Payload: Transition{To: Done}}}, nil
	})
	refusalMentions(t, "a transition with no From", err, "must record both ends")

	// A transition against an identity that is not a node moves nothing.
	err = h.commitError(func(_ context.Context, tx *Tx) ([]Draft, error) {
		return []Draft{h.transitionDraft(tx.NewID(), ScaleStep, verbStart, Planned)}, nil
	})
	refusalMentions(t, "starting something that is not a node", err, "touched 0 projection rows")
}

// TestAmendmentIsStepScopedEvenThoughRemovalIsNot draws the line the two
// amendment rules fall on either side of.
//
// Removal is kind-agnostic: a tombstone means the same thing at every scale, which
// is why a Matter can be removed (see the archive test above) with one rule.
// Replacement is not, because it *births* a row and has to know what kind of row —
// and a Stage cannot be replaced at all, since replacing a grouping would have to
// say what becomes of what it groups and no P1 event says that.
//
// Before this was checked, `step.replaced` against a Stage silently turned it into
// a Step and left its Steps parented to a tombstone. See the step-03 report.
func TestAmendmentIsStepScopedEvenThoughRemovalIsNot(t *testing.T) {
	h := newHarness(t)

	matter := h.matter("amendment", "Amendment")
	stage := h.stage(matter, "stage-1", "A grouping")
	step := h.step(stage, "step-01", "A Step the Stage groups")

	for _, bad := range []struct {
		what    string
		subject string
		kind    Scale
	}{
		{"replacing a Stage", stage, ScaleStage},
		{"replacing a Matter", matter, ScaleMatter},
	} {
		err := h.commitError(func(_ context.Context, tx *Tx) ([]Draft, error) {
			return []Draft{{
				Type:    TypeStepReplaced,
				Subject: bad.subject,
				Payload: Replaced{Replacement: tx.NewID(), Locator: "replacement", Title: "The replacement"},
			}}, nil
		})
		refusalMentions(t, bad.what, err, "which is a "+string(bad.kind)+"; replacement is step-scoped")
		// And the refusal left the node exactly as it was: still that kind, still
		// live, and still the parent of what it groups.
		node, readErr := h.Node(h.ctx, bad.subject)
		if readErr != nil {
			t.Errorf("%s: the node is no longer live: %v", bad.what, readErr)
			continue
		}
		if node.Kind != bad.kind {
			t.Errorf("%s: the node is now a %s, want a %s", bad.what, node.Kind, bad.kind)
		}
	}
	if node, err := h.Node(h.ctx, step); err != nil || node.Parent != stage {
		t.Errorf("the Step is parented to %q (err=%v), want the Stage %s", node.Parent, err, stage)
	}

	// A Step is replaced, keeps its predecessor's place, and the predecessor keeps
	// its history: the ULID stays spent and every event that named it still resolves.
	before := h.eventsOf(step)
	replacement := h.NewID()
	replaced := h.commit(Draft{
		Type:    TypeStepReplaced,
		Subject: step,
		Payload: Replaced{Replacement: replacement, Locator: "step-01", Title: "The Step that took its place"},
	})[0]
	h.wantRow("the replacement", "nodes", "id = ?", []any{replacement}, map[string]any{
		"id":     replacement,
		"kind":   ScaleStep,
		"repo":   h.Repo,
		"matter": matter,
		"parent": stage,
		// The locator its predecessor held is free again, because the predecessor was
		// tombstoned first and the unique index is partial.
		"locator":         "step-01",
		"title":           "The Step that took its place",
		"lifecycle":       Planned,
		"sort_key":        int64(sortKeyGap),
		"external_ref":    nil,
		"birth_event":     replaced.ID,
		"last_event":      replaced.ID,
		"tombstone_event": nil,
	})
	if after := h.eventsOf(step); len(after) != len(before)+1 {
		t.Errorf("the replaced Step has %d events, want %d: its history is not its replacement's",
			len(after), len(before)+1)
	}

	// A replacement that is not an identity is not a replacement, and neither is one
	// for a node that is not live.
	err := h.commitError(func(_ context.Context, _ *Tx) ([]Draft, error) {
		return []Draft{{Type: TypeStepReplaced, Subject: replacement, Payload: Replaced{Replacement: "step-02"}}}, nil
	})
	refusalMentions(t, "a replacement that is not an identity", err, "which is not an identity")
	err = h.commitError(func(_ context.Context, tx *Tx) ([]Draft, error) {
		return []Draft{{Type: TypeStepReplaced, Subject: step, Payload: Replaced{Replacement: tx.NewID(), Locator: "step-02"}}}, nil
	})
	refusalMentions(t, "replacing an already-replaced Step", err, "which is not a live node")

	// Reordering: the event carries the whole live sibling set, so anything in it
	// that is not a live Step under the subject is a reorder of something else.
	err = h.commitError(func(_ context.Context, _ *Tx) ([]Draft, error) {
		return []Draft{{Type: TypeStepReordered, Subject: stage, Payload: Reordered{}}}, nil
	})
	refusalMentions(t, "a reorder naming no order", err, "names no order")

	err = h.commitError(func(_ context.Context, _ *Tx) ([]Draft, error) {
		return []Draft{{Type: TypeStepReordered, Subject: matter, Payload: Reordered{Order: []string{stage}}}}, nil
	})
	refusalMentions(t, "a reorder naming a Stage", err, "which is not a live Step under")

	sibling := h.step(matter, "step-02", "A Step the Matter owns directly")
	err = h.commitError(func(_ context.Context, _ *Tx) ([]Draft, error) {
		return []Draft{{Type: TypeStepReordered, Subject: stage, Payload: Reordered{Order: []string{sibling}}}}, nil
	})
	refusalMentions(t, "a reorder naming another parent's Step", err, "which is not a live Step under")

	// One position, one Step: an order naming the same Step twice is caught by the
	// advance guard, because the second write would move a row to the event it
	// already names.
	err = h.commitError(func(_ context.Context, _ *Tx) ([]Draft, error) {
		return []Draft{{Type: TypeStepReordered, Subject: matter, Payload: Reordered{Order: []string{sibling, sibling}}}}, nil
	})
	refusalMentions(t, "a reorder naming one Step twice", err, "may only advance to a newer event")
}

// ---------------------------------------------------------------------------
// 3. Config and gate declarations (D42, D54, D4)
// ---------------------------------------------------------------------------

// TestProjectConfigIsKeyedAtTheRepoSoClonesCannotDiverge is D42 as a shape rather
// than a rule: the table has a repo column and no clone column, so two Clones of
// one Repo reading their config cannot get different answers — there is nowhere
// for the difference to live.
func TestProjectConfigIsKeyedAtTheRepoSoClonesCannotDiverge(t *testing.T) {
	h := newHarness(t)

	wantColumns(h, "config", []string{"repo", "key", "value"})

	// Presence, not truthiness. A strategy set to the empty string is set; the
	// second result is what tells a caller so.
	if err := h.SetConfig(h.ctx, h.Repo, "strategy", ""); err != nil {
		t.Fatalf("write config: %v", err)
	}
	value, present, err := h.Config(h.ctx, h.Repo, "strategy")
	if err != nil {
		t.Fatalf("read config: %v", err)
	}
	if !present || value != "" {
		t.Errorf(`config strategy = %q (present=%v), want "" and present`, value, present)
	}

	if _, present, err = h.Config(h.ctx, h.Repo, "never-written"); err != nil || present {
		t.Errorf("a key never written reads as present=%v (err=%v), want absent", present, err)
	}

	// The write is an upsert: config records the current setup, not its history.
	if err := h.SetConfig(h.ctx, h.Repo, "strategy", "trunk"); err != nil {
		t.Fatalf("rewrite config: %v", err)
	}
	value, present, err = h.Config(h.ctx, h.Repo, "strategy")
	if err != nil || !present || value != "trunk" {
		t.Errorf("config strategy = %q (present=%v, err=%v), want trunk", value, present, err)
	}
	h.wantRowCount("one key rewritten", "config", "repo = ? AND key = 'strategy'", []any{h.Repo}, 1)

	// Another Repo's config is its own.
	other := h.attachRepo(RepoAttached{Label: "other"})
	if err := h.SetConfig(h.ctx, other, "strategy", "release-branch"); err != nil {
		t.Fatalf("write the other Repo's config: %v", err)
	}
	value, _, err = h.Config(h.ctx, h.Repo, "strategy")
	if err != nil || value != "trunk" {
		t.Errorf("this Repo's strategy is now %q (err=%v), want trunk", value, err)
	}
	value, _, err = h.Config(h.ctx, other, "strategy")
	if err != nil || value != "release-branch" {
		t.Errorf("the other Repo's strategy is %q (err=%v), want release-branch", value, err)
	}
}

// TestDeclareGateValidatesItsScale holds the one storage capability the store
// offers gates. Declaring is configuration and emits no event (D4, D54); what the
// store checks is that a declaration binds to a scale that exists (D12).
func TestDeclareGateValidatesItsScale(t *testing.T) {
	h := newHarness(t)

	refusalMentions(t, "a gate bound to no scale",
		h.DeclareGate(h.ctx, h.Repo, "reviewed-local", Scale("repo")),
		"is not a scale a gate can bind to")
	refusalMentions(t, "a gate bound to the empty scale",
		h.DeclareGate(h.ctx, h.Repo, "reviewed-local", Scale("")),
		"is not a scale a gate can bind to")
	refusalMentions(t, "a nameless gate",
		h.DeclareGate(h.ctx, h.Repo, "", ScaleMatter),
		"needs a gate name")

	// MODEL §9 puts gate bindings at all three scales, so all three declare.
	for _, scale := range []Scale{ScaleMatter, ScaleStage, ScaleStep} {
		if err := h.DeclareGate(h.ctx, h.Repo, string(scale)+"-gate", scale); err != nil {
			t.Fatalf("declare a %s-scale gate: %v", scale, err)
		}
	}
	declarations, err := h.GateDeclarations(h.ctx, h.Repo)
	if err != nil {
		t.Fatalf("read the declarations: %v", err)
	}
	if len(declarations) != 3 {
		t.Fatalf("the Repo declares %d gates, want 3", len(declarations))
	}
	for _, d := range declarations {
		if d.Repo != h.Repo || d.Gate != string(d.Scale)+"-gate" {
			t.Errorf("declaration %+v is not the one that was written", d)
		}
	}

	// A gate is one row per (repo, gate): re-declaring moves its scale rather than
	// leaving two answers behind.
	if err := h.DeclareGate(h.ctx, h.Repo, "matter-gate", ScaleStep); err != nil {
		t.Fatalf("re-declare a gate at another scale: %v", err)
	}
	h.wantRow("a re-declared gate", "gate_declarations", "repo = ? AND gate = 'matter-gate'",
		[]any{h.Repo}, map[string]any{"repo": h.Repo, "gate": "matter-gate", "scale": ScaleStep})
}

// ---------------------------------------------------------------------------
// 4. The Archive, which is a view (D55)
// ---------------------------------------------------------------------------

// TestAMatterSealsWhenItIsDoneWithItsDeclaredGatesClosed is the sealing predicate.
//
// Archive is a view and not a table because D55 makes *sealed* a predicate over
// lifecycle and gates rather than a state, and there is no `*.sealed` event to
// project. At Matter scale there are no enclosing scales, so the predicate is:
// Done, with every matter-scale gate the Repo declares closed against it.
func TestAMatterSealsWhenItIsDoneWithItsDeclaredGatesClosed(t *testing.T) {
	h := newHarness(t)

	matter := h.matter("sealing", "A Matter that seals")
	h.wantArchive("a Matter still Planned")
	h.start(matter)
	h.wantArchive("a Matter In Progress")

	h.finish(matter)
	h.wantArchive("a Matter Done whose Repo declares no gate", matter)

	// A declaration is configuration and takes effect immediately, because sealing
	// is computed and not stored: declaring a gate un-seals a Matter that has not
	// closed it, which a stored state could not express.
	if err := h.DeclareGate(h.ctx, h.Repo, "reviewed-local", ScaleMatter); err != nil {
		t.Fatalf("declare the gate: %v", err)
	}
	h.wantArchive("a Matter Done with a matter-scale gate open")

	h.closeGate(matter, "reviewed-local", ScaleMatter)
	h.wantArchive("a Matter Done with its gate closed", matter)

	// A Canceled Matter is not sealed: it ended without sealing, which is a
	// different answer from sealed and from still running.
	canceled := h.matter("abandoned", "A Matter that ended without sealing")
	h.cancel(canceled)
	h.closeGate(canceled, "reviewed-local", ScaleMatter)
	h.wantArchive("a Canceled Matter with its gate closed", matter)
}

// TestFinishAndGateCloseAreOrderIndependent is why sealing is a predicate.
//
// A state would have to be entered by whichever of the two happened last, and
// something would have to notice. A predicate is simply true once both are, in
// either order — so this test writes them in both orders and expects one answer.
func TestFinishAndGateCloseAreOrderIndependent(t *testing.T) {
	h := newHarness(t)

	if err := h.DeclareGate(h.ctx, h.Repo, "reviewed-local", ScaleMatter); err != nil {
		t.Fatalf("declare the gate: %v", err)
	}

	closeFirst := h.matter("close-first", "Reviewed before it was finished")
	finishFirst := h.matter("finish-first", "Finished before it was reviewed")

	// Reviewed while still In Progress, which is legal: a gate is not a state.
	h.start(closeFirst)
	h.closeGate(closeFirst, "reviewed-local", ScaleMatter)
	h.wantArchive("a gate closed on a Matter that is not Done")

	h.start(finishFirst)
	h.finish(finishFirst)
	h.wantArchive("a Matter Done with its gate still open")

	h.finish(closeFirst)
	h.closeGate(finishFirst, "reviewed-local", ScaleMatter)

	// Both are sealed, in birth order, and nothing distinguishes them.
	h.wantArchive("close-then-finish and finish-then-close", closeFirst, finishFirst)
}

// TestAStepScaleGateDoesNotAffectMatterSealing keeps the predicate scoped. A gate
// binds to a scale (D12), and the Matter-scale predicate has no business consulting
// a gate declared for Steps.
func TestAStepScaleGateDoesNotAffectMatterSealing(t *testing.T) {
	h := newHarness(t)

	if err := h.DeclareGate(h.ctx, h.Repo, "verified", ScaleStep); err != nil {
		t.Fatalf("declare the gate: %v", err)
	}
	matter := h.matter("scoped", "A Matter with a step-scale gate declared")
	step := h.step(matter, "step-01", "A Step with a gate of its own to close")

	h.start(matter)
	h.finish(matter)
	h.wantArchive("a Matter whose Repo declares only a step-scale gate", matter)

	// The Step's own gate state is stored at the Step, keyed by node, and says
	// nothing about the Matter either way.
	closed := h.closeGate(step, "verified", ScaleStep)
	h.wantRow("a gate closed at Step scale", "gate_state", "node = ?", []any{step}, map[string]any{
		"node":       step,
		"gate":       "verified",
		"scale":      ScaleStep,
		"closed_at":  closed.OccurredAt.UTC().Format(timestampLayout),
		"last_event": closed.ID,
	})
	h.wantArchive("after the Step's gate closed", matter)

	// Gate state is one row per (node, gate): closing a gate that is already closed
	// is a close that did not happen, and is refused rather than folded in twice.
	// The refusal comes from the primary key rather than from a row-count check,
	// which is why it reads the way it does.
	err := h.commitError(func(_ context.Context, _ *Tx) ([]Draft, error) {
		return []Draft{{Type: TypeGateClosed, Subject: step, Payload: GateClosed{Gate: "verified", Scale: ScaleStep}}}, nil
	})
	refusalMentions(t, "closing a gate that is already closed", err,
		"UNIQUE constraint failed: gate_state.node, gate_state.gate")

	// A gate.closed naming no gate or no scale is not a gate close.
	err = h.commitError(func(_ context.Context, _ *Tx) ([]Draft, error) {
		return []Draft{{Type: TypeGateClosed, Subject: step, Payload: GateClosed{Scale: ScaleStep}}}, nil
	})
	refusalMentions(t, "a gate close naming no gate", err, "must name a gate and a scale")
	err = h.commitError(func(_ context.Context, _ *Tx) ([]Draft, error) {
		return []Draft{{Type: TypeGateClosed, Subject: step, Payload: GateClosed{Gate: "verified"}}}, nil
	})
	refusalMentions(t, "a gate close naming no scale", err, "must name a gate and a scale")
}

// TestATombstonedMatterNeverAppearsInTheArchive keeps removal and sealing apart.
// A removed node is soft-deleted and stays a valid identity reference (D44); what
// it is not is part of any answer.
func TestATombstonedMatterNeverAppearsInTheArchive(t *testing.T) {
	h := newHarness(t)

	matter := h.matter("folded", "A Matter that was folded into another")
	h.start(matter)
	h.finish(matter)
	h.wantArchive("Done and sealed", matter)

	// Amendment registers `step.*` only, and the projection rule is kind-agnostic,
	// so this is the only way P1 can express the removal of a node at Matter scale.
	removed := h.commit(Draft{
		Type:    TypeStepRemoved,
		Subject: matter,
		Payload: Removed{Reason: "folded into another Matter"},
	})[0]
	h.wantArchive("tombstoned")

	// The tombstone is not a lifecycle state: the row still says Done, and the
	// history still resolves by identity.
	h.wantRow("a tombstoned Matter", "nodes", "id = ?", []any{matter}, map[string]any{
		"id":              matter,
		"kind":            ScaleMatter,
		"repo":            h.Repo,
		"matter":          matter,
		"parent":          nil,
		"locator":         "folded",
		"title":           "A Matter that was folded into another",
		"lifecycle":       Done,
		"sort_key":        int64(sortKeyGap),
		"external_ref":    nil,
		"birth_event":     h.birthEventOf(matter).ID,
		"last_event":      removed.ID,
		"tombstone_event": removed.ID,
	})
	tombstoned, err := h.Tombstoned(h.ctx, matter)
	if err != nil {
		t.Fatalf("read the tombstone: %v", err)
	}
	if !tombstoned {
		t.Error("the removed Matter is not tombstoned")
	}
	if _, err := h.Node(h.ctx, matter); err == nil {
		t.Error("a tombstoned node still reads as live")
	}

	// Removing it twice is refused: the row is already gone and a projection that
	// agreed would have stopped being one.
	err = h.commitError(func(_ context.Context, _ *Tx) ([]Draft, error) {
		return []Draft{{Type: TypeStepRemoved, Subject: matter, Payload: Removed{}}}, nil
	})
	refusalMentions(t, "removing an already-removed node", err, "already tombstoned")
}

// ---------------------------------------------------------------------------
// 5. Batch, which keys at no tier (D39)
// ---------------------------------------------------------------------------

// TestABatchKeysAtNoTier is D39 and D56 together. A Batch may span Repos, so it
// binds to none — and its events therefore carry a null repo while still being
// execution events with a Clone and a Worktree. That is a row in `event_types`
// and not a special case in code, which is why the assertion is about the event's
// own columns as much as about the row.
func TestABatchKeysAtNoTier(t *testing.T) {
	h := newHarness(t)

	named := h.commitOne(func(_ context.Context, tx *Tx) (Draft, error) {
		return Draft{Type: TypeBatchCreated, Subject: tx.NewID(), Payload: BatchCreated{Name: "release-1"}}, nil
	})
	birth := h.birthEventOf(named)
	h.wantRow("a named Batch", "batches", "id = ?", []any{named}, map[string]any{
		"id":          named,
		"name":        "release-1",
		"birth_event": birth.ID,
		"last_event":  birth.ID,
	})
	h.wantDimensions(birth, "", h.Clone, h.Worktree)

	// The anonymous Batch a bare "work this Matter" wraps in, so there is exactly
	// one dispatch path (D23). Its name is absent, and absent names do not collide:
	// the unique index is partial for that reason.
	for i := 0; i < 2; i++ {
		anon := h.commitOne(func(_ context.Context, tx *Tx) (Draft, error) {
			return Draft{Type: TypeBatchCreated, Subject: tx.NewID(), Payload: BatchCreated{}}, nil
		})
		anonBirth := h.birthEventOf(anon)
		h.wantRow("an anonymous Batch", "batches", "id = ?", []any{anon}, map[string]any{
			"id":          anon,
			"name":        nil,
			"birth_event": anonBirth.ID,
			"last_event":  anonBirth.ID,
		})
	}
	h.wantRowCount("two anonymous Batches", "batches", "name IS NULL", nil, 2)

	// A name, where there is one, is unique.
	err := h.commitError(func(_ context.Context, tx *Tx) ([]Draft, error) {
		return []Draft{{Type: TypeBatchCreated, Subject: tx.NewID(), Payload: BatchCreated{Name: "release-1"}}}, nil
	})
	refusalMentions(t, "a second Batch called release-1", err, "UNIQUE constraint failed: batches.name")
}

// TestBatchMembershipIsIdempotentPerBatchAndMatter is D58: join, leave and rejoin
// is one membership row moving forward, not two rows and not a history. Membership
// implies nothing about ordering or structure (D18, D24), and an empty Batch is
// legal.
func TestBatchMembershipIsIdempotentPerBatchAndMatter(t *testing.T) {
	h := newHarness(t)

	batch := h.commitOne(func(_ context.Context, tx *Tx) (Draft, error) {
		return Draft{Type: TypeBatchCreated, Subject: tx.NewID(), Payload: BatchCreated{Name: "sprint"}}, nil
	})
	first := h.matter("one", "The first Matter")
	second := h.matter("two", "The second Matter")

	join := h.commit(Draft{Type: TypeBatchJoined, Subject: batch, Payload: BatchMembership{Matter: first}})[0]
	h.wantRow("a joined Matter", "batch_members", "batch = ? AND matter = ?", []any{batch, first},
		map[string]any{"batch": batch, "matter": first, "left_event": nil, "last_event": join.ID})

	left := h.commit(Draft{Type: TypeBatchLeft, Subject: batch, Payload: BatchMembership{Matter: first}})[0]
	h.wantRow("a Matter that left", "batch_members", "batch = ? AND matter = ?", []any{batch, first},
		map[string]any{"batch": batch, "matter": first, "left_event": left.ID, "last_event": left.ID})

	rejoin := h.commit(Draft{Type: TypeBatchJoined, Subject: batch, Payload: BatchMembership{Matter: first}})[0]
	h.wantRow("a Matter that rejoined", "batch_members", "batch = ? AND matter = ?", []any{batch, first},
		map[string]any{"batch": batch, "matter": first, "left_event": nil, "last_event": rejoin.ID})
	h.wantRowCount("join, leave, rejoin", "batch_members", "batch = ?", []any{batch}, 1)

	h.commit(Draft{Type: TypeBatchJoined, Subject: batch, Payload: BatchMembership{Matter: second}})
	h.wantMembers("two Matters in the Batch", batch, first, second)

	h.commit(Draft{Type: TypeBatchLeft, Subject: batch, Payload: BatchMembership{Matter: first}})
	h.wantMembers("one Matter left the Batch", batch, second)

	// Leaving a Batch a Matter is not in moves nothing.
	err := h.commitError(func(_ context.Context, _ *Tx) ([]Draft, error) {
		return []Draft{{Type: TypeBatchLeft, Subject: batch, Payload: BatchMembership{Matter: first}}}, nil
	})
	refusalMentions(t, "leaving a Batch twice", err, "touched 0 projection rows")

	// An empty Batch is legal (D58): the Batch outlives its membership.
	h.commit(Draft{Type: TypeBatchLeft, Subject: batch, Payload: BatchMembership{Matter: second}})
	h.wantMembers("an empty Batch", batch)
	h.wantRowCount("the Batch itself", "batches", "id = ?", []any{batch}, 1)
}

// wantMembers asserts a Batch's current membership, in the order BatchMembers
// returns it.
func (h *harness) wantMembers(what, batch string, want ...string) {
	h.t.Helper()
	got, err := h.BatchMembers(h.ctx, batch)
	if err != nil {
		h.t.Fatalf("read the members of %s: %v", batch, err)
	}
	if strings.Join(got, " ") != strings.Join(want, " ") {
		h.t.Errorf("%s: the Batch holds [%s], want [%s]", what,
			strings.Join(got, " "), strings.Join(want, " "))
	}
}

// ---------------------------------------------------------------------------
// 6. Run and OutboxEntry — shape only
// ---------------------------------------------------------------------------

// TestRunAndOutboxRowsExistFromEventOneWithNoVerbsYet is the whole of what this
// Step owes these two tables.
//
// Neither noun has a P1 event, so neither has a projection rule, and inventing one
// here would be inventing a verb. MODEL §9/§10 asks only that the rows exist from
// event one, so the single dispatch path and the outbox never need a retrofit —
// so what is asserted is the shape, the constraints, and membership of
// projectionTables, which is what makes the rule a later phase adds automatically
// covered by Rebuild.
func TestRunAndOutboxRowsExistFromEventOneWithNoVerbsYet(t *testing.T) {
	h := newHarness(t)

	wantColumns(h, "runs", []string{"id", "clone", "batch", "state", "birth_event", "last_event"})
	wantColumns(h, "outbox_entries", []string{
		"id", "state", "subject", "idempotency_key", "payload", "birth_event", "last_event",
	})

	for _, table := range []string{"runs", "outbox_entries"} {
		var found bool
		for _, p := range projectionTables {
			if p == table {
				found = true
				break
			}
		}
		if !found {
			t.Errorf("%s is not in projectionTables, so a later phase's rule would not be covered by Rebuild", table)
		}
		// And no P1 event lands a row in either: their verbs are not built here.
		h.wantRowCount("with no verb to drive it", table, "", nil, 0)
	}

	batch := h.commitOne(func(_ context.Context, tx *Tx) (Draft, error) {
		return Draft{Type: TypeBatchCreated, Subject: tx.NewID(), Payload: BatchCreated{Name: "provisional"}}, nil
	})
	ev := h.birthEventOf(batch)

	// A well-formed Run row is accepted around the API: the shape is real and only
	// the verb is missing (D48, provisional noun).
	if err := h.rawExec(
		`INSERT INTO runs (id, clone, batch, state, birth_event, last_event) VALUES (?, ?, ?, 'queued', ?, ?)`,
		h.NewID(), h.Clone, batch, ev.ID, ev.ID); err != nil {
		t.Fatalf("the substrate refused a well-formed Run row: %v", err)
	}
	// The generated guards apply to it like any other projection table.
	rawRefusedBy(h, "a Run born by two events", "a row is born by exactly one event",
		`INSERT INTO runs (id, clone, batch, state, birth_event, last_event) VALUES (?, ?, ?, 'queued', ?, ?)`,
		h.NewID(), h.Clone, batch, ev.ID, h.birthEventOf(h.Clone).ID)

	// The outbox's own constraints (D15, D50): three states, a JSON-object payload,
	// and an idempotency key that is delivered once.
	const outboxInsert = `INSERT INTO outbox_entries
		 (id, state, subject, idempotency_key, payload, birth_event, last_event)
		 VALUES (?, ?, NULL, ?, ?, ?, ?)`
	if err := h.rawExec(outboxInsert, h.NewID(), "pending", "one", `{"kind":"probe"}`, ev.ID, ev.ID); err != nil {
		t.Fatalf("the substrate refused a well-formed outbox row: %v", err)
	}
	rawRefusedBy(h, "an outbox entry in a state that is not a state", "CHECK constraint failed",
		outboxInsert, h.NewID(), "flushed", "two", `{}`, ev.ID, ev.ID)
	rawRefusedBy(h, "an outbox payload that is not a JSON object", "CHECK constraint failed",
		outboxInsert, h.NewID(), "pending", "three", `["not an object"]`, ev.ID, ev.ID)
	rawRefusedBy(h, "a second outbox entry under one idempotency key", "UNIQUE constraint failed: outbox_entries.idempotency_key",
		outboxInsert, h.NewID(), "pending", "one", `{}`, ev.ID, ev.ID)
}

// ---------------------------------------------------------------------------
// 7. Backlog (MODEL §4, D9)
// ---------------------------------------------------------------------------

// TestABacklogEntryRecordsHowItArrived is D9: one list, one noun, three
// provenances. Deferred is a provenance and not a second list, which is what the
// third case is for.
func TestABacklogEntryRecordsHowItArrived(t *testing.T) {
	h := newHarness(t)

	matter := h.matter("host", "The Matter work was found in")
	origin := h.step(matter, "step-01", "The Step it was noticed in")

	for _, want := range []struct {
		what    string
		payload BacklogEntered
		origin  any
	}{
		{"an entry that arrived by intake", BacklogEntered{
			Provenance: ProvenanceIntake, Title: "Rename the thing",
		}, nil},
		{"work found mid-flight", BacklogEntered{
			Provenance: ProvenanceFound, Title: "The parser mis-reads tabs",
			Detail: "noticed while doing step-01", OriginNode: origin,
		}, origin},
		{"work deferred out of a plan", BacklogEntered{
			Provenance: ProvenanceDeferred, Title: "Handle the 40 MB case",
			Detail: "out of scope for this Matter", OriginNode: origin,
		}, origin},
	} {
		payload := want.payload
		id := h.commitOne(func(_ context.Context, tx *Tx) (Draft, error) {
			return Draft{Type: TypeBacklogEntered, Subject: tx.NewID(), Payload: payload}, nil
		})
		birth := h.birthEventOf(id)
		h.wantRow(want.what, "backlog_entries", "id = ?", []any{id}, map[string]any{
			"id":          id,
			"repo":        h.Repo,
			"provenance":  payload.Provenance,
			"state":       "entered",
			"title":       payload.Title,
			"detail":      payload.Detail,
			"origin_node": want.origin,
			// An entry that has not been acted upon names no Matter, which is the
			// same CHECK that makes `planned` name one.
			"matter":      nil,
			"birth_event": birth.ID,
			"last_event":  birth.ID,
		})
	}

	entries, err := h.Backlog(h.ctx, h.Repo)
	if err != nil {
		t.Fatalf("read the backlog: %v", err)
	}
	if len(entries) != 3 {
		t.Errorf("the backlog holds %d entries, want 3 — one list, not one per provenance", len(entries))
	}
}

// TestABacklogEntryLeavesByPlanningOrDeclining is the exit set. Both exits are
// from `entered` and nowhere else, and declined has to be distinguishable from
// not-yet-acted-upon — which is why a reason is mandatory at the point of the
// decision rather than reconstructed in a retro.
func TestABacklogEntryLeavesByPlanningOrDeclining(t *testing.T) {
	h := newHarness(t)

	enter := func(title string) string {
		h.t.Helper()
		return h.commitOne(func(_ context.Context, tx *Tx) (Draft, error) {
			return Draft{
				Type:    TypeBacklogEntered,
				Subject: tx.NewID(),
				Payload: BacklogEntered{Provenance: ProvenanceIntake, Title: title, Detail: "why it arrived"},
			}, nil
		})
	}

	planned := enter("Becomes a Matter")
	matter := h.matter("planned", "What it became")
	plan := h.commit(Draft{Type: TypeBacklogPlanned, Subject: planned, Payload: BacklogPlanned{Matter: matter}})[0]
	h.wantRow("a planned entry", "backlog_entries", "id = ?", []any{planned}, map[string]any{
		"id":          planned,
		"repo":        h.Repo,
		"provenance":  ProvenanceIntake,
		"state":       "planned",
		"title":       "Becomes a Matter",
		"detail":      "why it arrived",
		"origin_node": nil,
		// Planned names what it became.
		"matter":      matter,
		"birth_event": h.birthEventOf(planned).ID,
		"last_event":  plan.ID,
	})

	declined := enter("Will not be done")
	decline := h.commit(Draft{
		Type:    TypeBacklogDeclined,
		Subject: declined,
		Payload: BacklogDeclined{Reason: "the premise stopped being true"},
	})[0]
	h.wantRow("a declined entry", "backlog_entries", "id = ?", []any{declined}, map[string]any{
		"id":         declined,
		"repo":       h.Repo,
		"provenance": ProvenanceIntake,
		"state":      "declined",
		"title":      "Will not be done",
		// The reason lands in `detail`, which is the row's one free-text column: the
		// decision's reason replaces the arrival's. See the note in the step-03
		// report — the log keeps both, the projection keeps the later one.
		"detail":      "the premise stopped being true",
		"origin_node": nil,
		"matter":      nil,
		"birth_event": h.birthEventOf(declined).ID,
		"last_event":  decline.ID,
	})

	// A decline with no reason is not a decline. (The entry is entered first: a
	// decide function runs inside the open transaction and may not start another.)
	reasonless := enter("Reasonless")
	err := h.commitError(func(_ context.Context, _ *Tx) ([]Draft, error) {
		return []Draft{{Type: TypeBacklogDeclined, Subject: reasonless, Payload: BacklogDeclined{}}}, nil
	})
	refusalMentions(t, "a decline with no reason", err, "must carry a reason")

	// Both exits are from `entered`: an entry that has already left does not leave
	// again, in either direction.
	for _, already := range []struct {
		what    string
		subject string
	}{{"a planned entry", planned}, {"a declined entry", declined}} {
		err = h.commitError(func(_ context.Context, _ *Tx) ([]Draft, error) {
			return []Draft{{Type: TypeBacklogPlanned, Subject: already.subject, Payload: BacklogPlanned{Matter: matter}}}, nil
		})
		refusalMentions(t, "planning "+already.what, err, "touched 0 projection rows")

		err = h.commitError(func(_ context.Context, _ *Tx) ([]Draft, error) {
			return []Draft{{Type: TypeBacklogDeclined, Subject: already.subject, Payload: BacklogDeclined{Reason: "again"}}}, nil
		})
		refusalMentions(t, "declining "+already.what, err, "touched 0 projection rows")
	}

	// The CHECK behind it, held around the API in both directions: planned names a
	// Matter and nothing else does.
	birth := h.birthEventOf(planned).ID
	const entryInsert = `INSERT INTO backlog_entries
		 (id, repo, provenance, state, title, detail, origin_node, matter, birth_event, last_event)
		 VALUES (?, ?, 'intake', ?, 'A hand-built entry', '', NULL, ?, ?, ?)`
	rawRefusedBy(h, "a planned entry naming no Matter", "CHECK constraint failed",
		entryInsert, h.NewID(), h.Repo, "planned", nil, birth, birth)
	rawRefusedBy(h, "an entered entry naming a Matter", "CHECK constraint failed",
		entryInsert, h.NewID(), h.Repo, "entered", matter, birth, birth)
	rawRefusedBy(h, "a declined entry naming a Matter", "CHECK constraint failed",
		entryInsert, h.NewID(), h.Repo, "declined", matter, birth, birth)
	rawRefusedBy(h, "an entry in a state that is not a state", "CHECK constraint failed",
		entryInsert, h.NewID(), h.Repo, "shelved", nil, birth, birth)
}

// ---------------------------------------------------------------------------
// 8. The cursor (D38)
// ---------------------------------------------------------------------------

// TestTheCursorIsKeyedAtCloneAndWorktreeAndMayBeNowhere is D38 twice over.
//
// The cursor is attention and never a fact about the work, so it is keyed at
// Clone + Worktree rather than owned by a node, and a cleared cursor is a
// first-class answer rather than an error: attention is allowed to be nowhere.
func TestTheCursorIsKeyedAtCloneAndWorktreeAndMayBeNowhere(t *testing.T) {
	h := newHarness(t)

	matter := h.matter("attention", "Where attention is")
	step := h.step(matter, "step-01", "A Step to look at")

	// The subject is the Worktree the cursor belongs to; the Clone is the event's
	// own dimension, which is what makes the row keyed at both.
	move := h.commit(Draft{Type: TypeCursorMoved, Subject: h.Worktree, Payload: CursorMoved{Node: matter}})[0]
	h.wantRow("a cursor pointing at a Matter", "cursors", "clone = ? AND worktree = ?",
		[]any{h.Clone, h.Worktree}, map[string]any{
			"clone": h.Clone, "worktree": h.Worktree, "node": matter, "last_event": move.ID,
		})

	// Moving it is an upsert, not a second row: there is one cursor per place to
	// stand, and reading it is one keyed lookup.
	again := h.commit(Draft{
		Type:    TypeCursorMoved,
		Subject: h.Worktree,
		Payload: CursorMoved{Node: step, Previous: matter},
	})[0]
	h.wantRow("a cursor that moved", "cursors", "clone = ? AND worktree = ?",
		[]any{h.Clone, h.Worktree}, map[string]any{
			"clone": h.Clone, "worktree": h.Worktree, "node": step, "last_event": again.ID,
		})
	h.wantRowCount("two moves in one worktree", "cursors", "", nil, 1)

	node, ok, err := h.Cursor(h.ctx, h.Clone, h.Worktree)
	if err != nil || !ok || node != step {
		t.Errorf("the cursor reads %q (set=%v, err=%v), want %s", node, ok, err, step)
	}

	// Cleared: the row stays and the node is absent, because "nowhere" is an answer
	// the store has to be able to hold rather than the absence of one.
	cleared := h.commit(Draft{Type: TypeCursorMoved, Subject: h.Worktree, Payload: CursorMoved{Previous: step}})[0]
	h.wantRow("a cleared cursor", "cursors", "clone = ? AND worktree = ?",
		[]any{h.Clone, h.Worktree}, map[string]any{
			"clone": h.Clone, "worktree": h.Worktree, "node": nil, "last_event": cleared.ID,
		})
	node, ok, err = h.Cursor(h.ctx, h.Clone, h.Worktree)
	if err != nil {
		t.Fatalf("read a cleared cursor: %v", err)
	}
	if ok || node != "" {
		t.Errorf("a cleared cursor reads %q (set=%v), want nowhere and no error", node, ok)
	}

	// Another Worktree of the same Clone has its own cursor, and this one is still
	// cleared.
	other := h.attachWorktree(h.Repo, WorktreeAttached{Clone: h.Clone, Name: "feature"})
	elsewhere := h.with(Env{Repo: h.Repo, Clone: h.Clone, Worktree: other})
	elsewhere.commit(Draft{Type: TypeCursorMoved, Subject: other, Payload: CursorMoved{Node: matter}})
	h.wantRowCount("two worktrees with cursors", "cursors", "", nil, 2)
	if _, ok, _ := h.Cursor(h.ctx, h.Clone, h.Worktree); ok {
		t.Error("moving another worktree's cursor moved this one")
	}
	if node, ok, _ := h.Cursor(h.ctx, h.Clone, other); !ok || node != matter {
		t.Errorf("the other worktree's cursor reads %q (set=%v), want %s", node, ok, matter)
	}

	// A cursor.moved whose subject is not the Worktree it moves in is not a cursor
	// move; the subject is the entity the event is about.
	err = h.commitError(func(_ context.Context, _ *Tx) ([]Draft, error) {
		return []Draft{{Type: TypeCursorMoved, Subject: matter, Payload: CursorMoved{Node: matter}}}, nil
	})
	refusalMentions(t, "a cursor move whose subject is a node", err, "must have the Worktree it moves in as its subject")
}

// ---------------------------------------------------------------------------
// 9. Dispatch (D59)
// ---------------------------------------------------------------------------

// TestADispatchIsBracketedAndAlwaysClosesWithAReason is D59. A Dispatch is a
// bracketed working period, and every close carries why — because Session
// discounts every reason but `completed`, whose occurred_at is the only one that is
// evidence of work rather than administration. A reason that could be absent would
// make that distinction unaskable.
func TestADispatchIsBracketedAndAlwaysClosesWithAReason(t *testing.T) {
	h := newHarness(t)

	for _, reason := range []CloseReason{CloseCompleted, CloseSuperseded, CloseReaped} {
		dispatch := h.commitOne(func(_ context.Context, tx *Tx) (Draft, error) {
			return Draft{Type: TypeDispatchOpened, Subject: tx.NewID()}, nil
		})
		opened := h.birthEventOf(dispatch)
		h.wantRow("an open dispatch", "dispatches", "id = ?", []any{dispatch}, map[string]any{
			"id":       dispatch,
			"clone":    h.Clone,
			"worktree": h.Worktree,
			"state":    "open",
			// Open means no reason and no close time; the CHECKs tie all three
			// together so a half-closed dispatch is not expressible.
			"close_reason": nil,
			"opened_at":    opened.OccurredAt.UTC().Format(timestampLayout),
			"closed_at":    nil,
			"birth_event":  opened.ID,
			"last_event":   opened.ID,
		})

		got, ok, err := h.OpenDispatch(h.ctx, h.Worktree)
		if err != nil || !ok || got.ID != dispatch {
			t.Fatalf("the open dispatch reads %q (found=%v, err=%v), want %s", got.ID, ok, err, dispatch)
		}

		closed := h.commit(Draft{
			Type:    TypeDispatchClosed,
			Subject: dispatch,
			Payload: DispatchClosed{Reason: reason},
		})[0]
		h.wantRow("a dispatch closed as "+string(reason), "dispatches", "id = ?", []any{dispatch}, map[string]any{
			"id":           dispatch,
			"clone":        h.Clone,
			"worktree":     h.Worktree,
			"state":        "closed",
			"close_reason": reason,
			"opened_at":    opened.OccurredAt.UTC().Format(timestampLayout),
			"closed_at":    closed.OccurredAt.UTC().Format(timestampLayout),
			"birth_event":  opened.ID,
			"last_event":   closed.ID,
		})

		read, err := h.Dispatch(h.ctx, dispatch)
		if err != nil {
			t.Fatalf("read the dispatch: %v", err)
		}
		if read.Open || read.CloseReason != reason {
			t.Errorf("the dispatch reads open=%v reason=%q, want closed as %s", read.Open, read.CloseReason, reason)
		}
		if _, ok, _ := h.OpenDispatch(h.ctx, h.Worktree); ok {
			t.Error("the worktree still has an open dispatch after closing it")
		}

		// Closing it again moves nothing: the bracket is already closed.
		err = h.commitError(func(_ context.Context, _ *Tx) ([]Draft, error) {
			return []Draft{{Type: TypeDispatchClosed, Subject: dispatch, Payload: DispatchClosed{Reason: reason}}}, nil
		})
		refusalMentions(t, "closing a closed dispatch", err, "touched 0 projection rows")
	}

	// The reason is mandatory, checked before the row is ever reached.
	open := h.commitOne(func(_ context.Context, tx *Tx) (Draft, error) {
		return Draft{Type: TypeDispatchOpened, Subject: tx.NewID()}, nil
	})
	err := h.commitError(func(_ context.Context, _ *Tx) ([]Draft, error) {
		return []Draft{{Type: TypeDispatchClosed, Subject: open, Payload: DispatchClosed{}}}, nil
	})
	refusalMentions(t, "a close with no reason", err, "must carry a reason (D59)")

	// And a reason outside the three is refused by the substrate too.
	rawRefusedBy(h, "a dispatch closed for a reason that is not one", "CHECK constraint failed",
		`UPDATE dispatches SET state = 'closed', close_reason = 'abandoned', closed_at = ?, last_event = ?
		 WHERE id = ?`,
		h.birthEventOf(open).OccurredAt.UTC().Format(timestampLayout), h.NewID(), open)
}

// ---------------------------------------------------------------------------
// 10. reference.bound (MODEL §5)
// ---------------------------------------------------------------------------

// TestAReferenceBindsToALiveNode is MODEL §5's "acquired, not assigned": a late,
// mutable update to a nullable column keyed on identity. It ships in P1 and is
// inert until P3, so what the store owes it now is that the column moves and that
// nothing else does.
func TestAReferenceBindsToALiveNode(t *testing.T) {
	h := newHarness(t)

	matter := h.matter("refs", "References")
	step := h.step(matter, "step-01", "A Step with an issue somewhere")
	birth := h.birthEventOf(step)

	row := map[string]any{
		"id":              step,
		"kind":            ScaleStep,
		"repo":            h.Repo,
		"matter":          matter,
		"parent":          matter,
		"locator":         "step-01",
		"title":           "A Step with an issue somewhere",
		"lifecycle":       Planned,
		"sort_key":        int64(sortKeyGap),
		"external_ref":    nil,
		"birth_event":     birth.ID,
		"last_event":      birth.ID,
		"tombstone_event": nil,
	}

	bound := h.commit(Draft{Type: TypeReferenceBound, Subject: step, Payload: ReferenceBound{Ref: "GH-17"}})[0]
	row["external_ref"] = "GH-17"
	row["last_event"] = bound.ID
	h.wantRow("a bound reference", "nodes", "id = ?", []any{step}, row)

	node, err := h.Node(h.ctx, step)
	if err != nil {
		t.Fatalf("read the Step: %v", err)
	}
	if node.ExternalRef != "GH-17" {
		t.Errorf("the Step's external ref is %q, want GH-17", node.ExternalRef)
	}

	// Acquired late and mutable: binding again replaces it.
	rebound := h.commit(Draft{Type: TypeReferenceBound, Subject: step, Payload: ReferenceBound{Ref: "GH-18"}})[0]
	row["external_ref"] = "GH-18"
	row["last_event"] = rebound.ID
	h.wantRow("a rebound reference", "nodes", "id = ?", []any{step}, row)

	// A tombstoned node has nothing to bind to.
	h.commit(Draft{Type: TypeStepRemoved, Subject: step, Payload: Removed{Reason: "no longer in the plan"}})
	err = h.commitError(func(_ context.Context, _ *Tx) ([]Draft, error) {
		return []Draft{{Type: TypeReferenceBound, Subject: step, Payload: ReferenceBound{Ref: "GH-19"}}}, nil
	})
	refusalMentions(t, "binding a reference to a tombstoned node", err, "touched 0 projection rows")
}
