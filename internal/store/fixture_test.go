package store

import (
	"context"
	"path/filepath"
	"testing"
)

// The shared test harness.
//
// Almost nothing in this store can be written without a tier context: D56 makes
// the repo dimension mandatory on every event but `batch.*`, and the projection
// tables carry real foreign keys into repos/clones/worktrees. So a test that
// wants to say anything at all first needs a Repo, a Clone and a Worktree — and
// the only way to get them is through the same write path everything else uses,
// which is the point.
//
// harness is therefore not a convenience: it is the bootstrap every Step's tests
// share, so a change to the write path breaks one file rather than ten.

// harness is a fresh store plus the tier context and actor a command runs under.
type harness struct {
	*Store

	t   *testing.T
	ctx context.Context

	// Repo, Clone and Worktree are the bootstrapped tier rows, which are also
	// this harness's Env.
	Repo     string
	Clone    string
	Worktree string
}

// newHarness opens a store under t.TempDir() and attaches one Repo, one Clone
// and one main Worktree, so every dimension a P1 event can require is available.
func newHarness(t *testing.T) *harness {
	t.Helper()
	ctx := context.Background()

	s, err := Open(filepath.Join(t.TempDir(), "wip.db"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { _ = s.Close() })

	h := &harness{Store: s, t: t, ctx: ctx}

	// The Repo's identity has to exist before the command that creates it, because
	// repo.attached carries its own Repo as its repo dimension. This is the one
	// case Store.NewID is exported for.
	h.Repo = s.NewID()
	h.commit(Draft{
		Type:    TypeRepoAttached,
		Subject: h.Repo,
		Payload: RepoAttached{Label: "fixture"},
	})

	h.Clone = h.commitOne(func(_ context.Context, tx *Tx) (Draft, error) {
		return Draft{
			Type:    TypeCloneAttached,
			Subject: tx.NewID(),
			Payload: CloneAttached{Repo: h.Repo, GitCommonDir: "/tmp/fixture/.git", Label: "main"},
		}, nil
	})
	h.Worktree = h.commitOne(func(_ context.Context, tx *Tx) (Draft, error) {
		return Draft{
			Type:    TypeWorktreeAttached,
			Subject: tx.NewID(),
			Payload: WorktreeAttached{Clone: h.Clone},
		}, nil
	})
	return h
}

// req is the Request every harness command runs under: a human, in the
// bootstrapped tier context.
func (h *harness) req() Request {
	return Request{
		Actor: ActorHuman,
		Env:   Env{Repo: h.Repo, Clone: h.Clone, Worktree: h.Worktree},
	}
}

// commit writes a fixed set of drafts and fails the test if it does not land.
func (h *harness) commit(drafts ...Draft) []Event {
	h.t.Helper()
	return h.commitWith(func(context.Context, *Tx) ([]Draft, error) { return drafts, nil })
}

// commitWith runs a decide function through the one write path.
func (h *harness) commitWith(decide func(context.Context, *Tx) ([]Draft, error)) []Event {
	h.t.Helper()
	events, err := h.Commit(h.ctx, h.req(), decide)
	if err != nil {
		h.t.Fatalf("commit: %v", err)
	}
	return events
}

// commitError runs a decide function expecting it to be refused, and returns the
// error. It fails the test if the command succeeded.
func (h *harness) commitError(decide func(context.Context, *Tx) ([]Draft, error)) error {
	h.t.Helper()
	if _, err := h.Commit(h.ctx, h.req(), decide); err != nil {
		return err
	}
	h.t.Fatal("commit succeeded, want a refusal")
	return nil
}

// commitOne commits a single draft whose subject the decide function mints, and
// returns that subject — the common "birth one entity" shape.
func (h *harness) commitOne(decide func(context.Context, *Tx) (Draft, error)) string {
	h.t.Helper()
	var subject string
	h.commitWith(func(ctx context.Context, tx *Tx) ([]Draft, error) {
		d, err := decide(ctx, tx)
		if err != nil {
			return nil, err
		}
		subject = d.Subject
		return []Draft{d}, nil
	})
	return subject
}

// ---------------------------------------------------------------------------
// Nodes
// ---------------------------------------------------------------------------

// matter creates a Matter and returns its identity.
func (h *harness) matter(locator, title string) string {
	h.t.Helper()
	return h.node(TypeMatterCreated, "", locator, title)
}

// stage creates a Stage under a Matter.
func (h *harness) stage(parent, locator, title string) string {
	h.t.Helper()
	return h.node(TypeStageCreated, parent, locator, title)
}

// step creates a Step under a Matter or a Stage.
func (h *harness) step(parent, locator, title string) string {
	h.t.Helper()
	return h.node(TypeStepCreated, parent, locator, title)
}

// node is the shared birth path: one event, sort key after every live sibling.
func (h *harness) node(eventType, parent, locator, title string) string {
	h.t.Helper()
	return h.commitOne(func(ctx context.Context, tx *Tx) (Draft, error) {
		sortKey := int64(sortKeyGap)
		if parent != "" {
			k, err := tx.NextSortKey(ctx, parent)
			if err != nil {
				return Draft{}, err
			}
			sortKey = k
		}
		return Draft{
			Type:    eventType,
			Subject: tx.NewID(),
			Payload: NodeBirth{Title: title, Locator: locator, Parent: parent, SortKey: sortKey},
		}, nil
	})
}

// ---------------------------------------------------------------------------
// Assertions shared across Steps
// ---------------------------------------------------------------------------

// rawExec runs SQL straight at the database, around the API. Several Steps'
// assertions are about what the *substrate* refuses — append-only, the
// projection guards — and those cannot be demonstrated through a write path
// designed to make them unreachable.
func (h *harness) rawExec(query string, args ...any) error {
	h.t.Helper()
	_, err := h.db.ExecContext(h.ctx, query, args...)
	return err
}
