package readsurface

// The shared test fixture for this package's tests. Fixtures are seeded
// directly through store's exported data-access layer rather than through
// write-surface's verbs: this Matter carries no edge to write-surface
// (workplan step-07), and this is the only fixture path available at this
// point in the register — the same posture `tiers`'s worked-examples Stage
// and `store-fork`'s throwaway code both took before it.

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/procrastivity/wip/internal/store"
)

var ctx = context.Background()

// fixture is a fresh store plus one bootstrapped Repo/Clone/Worktree — the
// tier context every event but a Batch-subject one requires (D56).
type fixture struct {
	*store.Store

	t testing.TB

	Repo     string
	Clone    string
	Worktree string
}

func newFixture(t *testing.T) *fixture {
	t.Helper()
	s, err := store.Open(filepath.Join(t.TempDir(), "wip.db"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { _ = s.Close() })

	f := &fixture{Store: s, t: t}
	f.Repo = f.attachRepo()
	f.Clone = f.attachClone(f.Repo)
	f.Worktree = f.attachWorktree(f.Repo, f.Clone, "")
	return f
}

func (f *fixture) commit(env store.Env, decide func(context.Context, *store.Tx) ([]store.Draft, error)) []store.Event {
	f.t.Helper()
	events, err := f.Commit(ctx, store.Request{Actor: store.ActorHuman, Env: env}, decide)
	if err != nil {
		f.t.Fatalf("commit: %v", err)
	}
	return events
}

func (f *fixture) attachRepo() string {
	f.t.Helper()
	id := f.NewID()
	f.commit(store.Env{Repo: id}, func(_ context.Context, _ *store.Tx) ([]store.Draft, error) {
		return []store.Draft{{Type: store.TypeRepoAttached, Subject: id, Payload: store.RepoAttached{Label: "fixture"}}}, nil
	})
	return id
}

func (f *fixture) attachClone(repo string) string {
	f.t.Helper()
	var id string
	f.commit(store.Env{Repo: repo}, func(_ context.Context, tx *store.Tx) ([]store.Draft, error) {
		id = tx.NewID()
		return []store.Draft{{
			Type: store.TypeCloneAttached, Subject: id,
			Payload: store.CloneAttached{Repo: repo, GitCommonDir: "/tmp/fixture-" + id + "/.git", Label: "main"},
		}}, nil
	})
	return id
}

func (f *fixture) attachWorktree(repo, clone, name string) string {
	f.t.Helper()
	var id string
	f.commit(store.Env{Repo: repo}, func(_ context.Context, tx *store.Tx) ([]store.Draft, error) {
		id = tx.NewID()
		return []store.Draft{{
			Type: store.TypeWorktreeAttached, Subject: id,
			Payload: store.WorktreeAttached{Clone: clone, Name: name},
		}}, nil
	})
	return id
}

// node births a Matter, Stage or Step, appending it after every live sibling.
func (f *fixture) node(eventType, parent, locator, title string) string {
	f.t.Helper()
	var id string
	f.commit(store.Env{Repo: f.Repo}, func(ctx context.Context, tx *store.Tx) ([]store.Draft, error) {
		sortKey := int64(1000)
		if parent != "" {
			k, err := tx.NextSortKey(ctx, parent)
			if err != nil {
				return nil, err
			}
			sortKey = k
		}
		id = tx.NewID()
		return []store.Draft{{
			Type: eventType, Subject: id,
			Payload: store.NodeBirth{Title: title, Locator: locator, Parent: parent, SortKey: sortKey},
		}}, nil
	})
	return id
}

func (f *fixture) matter(locator, title string) string {
	return f.node(store.TypeMatterCreated, "", locator, title)
}

func (f *fixture) stage(parent, locator, title string) string {
	return f.node(store.TypeStageCreated, parent, locator, title)
}

func (f *fixture) step(parent, locator, title string) string {
	return f.node(store.TypeStepCreated, parent, locator, title)
}

// transition drives one lifecycle move, picking the scale-correct event type
// itself, so a test can say "start" without naming Matter/Stage/Step.
func (f *fixture) transition(node string, from, to store.Lifecycle, verb string) store.Event {
	f.t.Helper()
	n, err := f.Node(ctx, node)
	if err != nil {
		f.t.Fatalf("read %s before %s: %v", node, verb, err)
	}
	events := map[store.Scale]map[string]string{
		store.ScaleMatter: {
			"start": store.TypeMatterStarted, "finish": store.TypeMatterFinished,
			"cancel": store.TypeMatterCanceled, "pause": store.TypeMatterPaused, "resume": store.TypeMatterResumed,
		},
		store.ScaleStage: {
			"start": store.TypeStageStarted, "finish": store.TypeStageFinished,
			"cancel": store.TypeStageCanceled, "pause": store.TypeStagePaused, "resume": store.TypeStageResumed,
		},
		store.ScaleStep: {
			"start": store.TypeStepStarted, "finish": store.TypeStepFinished,
			"cancel": store.TypeStepCanceled, "pause": store.TypeStepPaused, "resume": store.TypeStepResumed,
		},
	}
	evs := f.commit(store.Env{Repo: f.Repo}, func(_ context.Context, _ *store.Tx) ([]store.Draft, error) {
		return []store.Draft{{
			Type: events[n.Kind][verb], Subject: node,
			Payload: store.Transition{From: from, To: to},
		}}, nil
	})
	return evs[0]
}

func (f *fixture) start(node string) store.Event {
	return f.transition(node, store.Planned, store.InProgress, "start")
}

func (f *fixture) finish(node string) store.Event {
	return f.transition(node, store.InProgress, store.Done, "finish")
}

func (f *fixture) cancel(node string) store.Event {
	return f.transition(node, store.InProgress, store.Canceled, "cancel")
}

func (f *fixture) pause(node string) store.Event {
	return f.transition(node, store.InProgress, store.Paused, "pause")
}

func (f *fixture) resume(node string) store.Event {
	return f.transition(node, store.Paused, store.InProgress, "resume")
}

// depend adds a `blocked-by` edge: blocked waits for blocker.
func (f *fixture) depend(blocked, blocker string) string {
	f.t.Helper()
	var edge string
	f.commit(store.Env{Repo: f.Repo}, func(_ context.Context, tx *store.Tx) ([]store.Draft, error) {
		edge = tx.NewID()
		return []store.Draft{{
			Type: store.TypeDependencyAdded, Subject: blocked,
			Payload: store.DependencyChange{Edge: edge, Blocker: blocker},
		}}, nil
	})
	return edge
}

func (f *fixture) removeEdge(blocked, edge, blocker string) {
	f.t.Helper()
	f.commit(store.Env{Repo: f.Repo}, func(_ context.Context, _ *store.Tx) ([]store.Draft, error) {
		return []store.Draft{{
			Type: store.TypeDependencyRemoved, Subject: blocked,
			Payload: store.DependencyChange{Edge: edge, Blocker: blocker},
		}}, nil
	})
}

func (f *fixture) remove(node string) {
	f.t.Helper()
	f.commit(store.Env{Repo: f.Repo}, func(_ context.Context, _ *store.Tx) ([]store.Draft, error) {
		return []store.Draft{{Type: store.TypeStepRemoved, Subject: node, Payload: store.Removed{Reason: "fixture"}}}, nil
	})
}

func (f *fixture) closeGate(node, gate string, scale store.Scale) store.Event {
	f.t.Helper()
	evs := f.commit(store.Env{Repo: f.Repo}, func(_ context.Context, _ *store.Tx) ([]store.Draft, error) {
		return []store.Draft{{Type: store.TypeGateClosed, Subject: node, Payload: store.GateClosed{Gate: gate, Scale: scale}}}, nil
	})
	return evs[0]
}

// seedBacklogEntry births an unprocessed Backlog entry (MODEL §4), the
// fixture NextView's EverythingSealed branch nudges toward.
func (f *fixture) seedBacklogEntry(title string) string {
	f.t.Helper()
	var id string
	f.commit(store.Env{Repo: f.Repo}, func(_ context.Context, tx *store.Tx) ([]store.Draft, error) {
		id = tx.NewID()
		return []store.Draft{{
			Type: store.TypeBacklogEntered, Subject: id,
			Payload: store.BacklogEntered{Provenance: store.ProvenanceFound, Title: title},
		}}, nil
	})
	return id
}

func (f *fixture) declareGate(gate string, scale store.Scale) {
	f.t.Helper()
	if err := f.DeclareGate(ctx, f.Repo, gate, scale); err != nil {
		f.t.Fatalf("declare gate %s: %v", gate, err)
	}
}

// current is the Current a resolved Clone/Worktree composes to, for tests
// that call package functions taking one directly rather than going through
// ResolveCurrent's git shell-outs.
func (f *fixture) current() Current {
	f.t.Helper()
	// f.Repo/f.Clone/f.Worktree are this fixture's own string-ID fields,
	// which shadow the promoted View.Repo/Clone/Worktree row accessors on
	// the embedded *store.Store — hence the explicit f.Store. qualification
	// below to reach the row rather than the identity.
	repo, err := f.Store.Repo(ctx, f.Repo)
	if err != nil {
		f.t.Fatalf("read repo: %v", err)
	}
	clone, err := f.Store.Clone(ctx, f.Clone)
	if err != nil {
		f.t.Fatalf("read clone: %v", err)
	}
	wt, err := f.Store.Worktree(ctx, f.Worktree)
	if err != nil {
		f.t.Fatalf("read worktree: %v", err)
	}
	return Current{Repo: repo, Clone: clone, Worktree: wt}
}
