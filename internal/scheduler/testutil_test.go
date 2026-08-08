package scheduler

// The shared test fixture, following readsurface's posture: seeded directly
// through store's exported data-access layer, with Batch/Run helpers for the
// Run-scoped derivations this package adds.

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
			Payload: store.CloneAttached{Repo: repo, GitCommonDir: "/tmp/fixture-" + id + "/.git", Label: "clone-" + id},
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

// namedBatch creates a named Batch and joins the given Matters.
func (f *fixture) namedBatch(name string, matters ...string) string {
	f.t.Helper()
	var id string
	env := store.Env{Clone: f.Clone, Worktree: f.Worktree}
	f.commit(env, func(_ context.Context, tx *store.Tx) ([]store.Draft, error) {
		id = tx.NewID()
		return []store.Draft{{Type: store.TypeBatchCreated, Subject: id, Payload: store.BatchCreated{Name: name}}}, nil
	})
	for _, m := range matters {
		matter := m
		f.commit(env, func(_ context.Context, _ *store.Tx) ([]store.Draft, error) {
			return []store.Draft{{Type: store.TypeBatchJoined, Subject: id, Payload: store.BatchMembership{Matter: matter}}}, nil
		})
	}
	return id
}

// startRun starts a Run over a Batch with the given frozen Matter set.
func (f *fixture) startRun(batch, locator string, matters ...string) store.Run {
	f.t.Helper()
	var id string
	f.commit(store.Env{Repo: f.Repo, Clone: f.Clone, Worktree: f.Worktree}, func(_ context.Context, tx *store.Tx) ([]store.Draft, error) {
		id = tx.NewID()
		return []store.Draft{{Type: store.TypeRunStarted, Subject: id, Payload: store.RunStarted{
			Batch: batch, Locator: locator, Matters: matters,
		}}}, nil
	})
	run, err := f.Run(ctx, id)
	if err != nil {
		f.t.Fatalf("read run: %v", err)
	}
	return run
}
