package writesurface

// Stage birth-and-amendment, step-04: the five scale-polymorphic lifecycle
// verbs. The transitions are exactly MODEL §2.2's graph — Planned->(start)
// InProgress->(finish) Done, with InProgress->(cancel) Canceled and
// InProgress->(pause) Paused, and Paused->(resume) InProgress. A lifecycle
// payload always carries both ends (schema's Brief), so a transition that
// could not have happened is refused at write time rather than defaulted.
//
// Documented gap (D58): the workplan also calls for Start to emit
// `batch.joined` on a node's first start inside an open dispatch. Not
// implemented here — there is no verb yet that opens a dispatch
// (`render-scratch`'s `wip refresh`), and the `dispatches` table `schema`
// shipped carries no `batch` column at all, so "the dispatch's batch" has no
// answer to give. See docs/write-surface/decisions.md; `render-scratch` or
// `orchestration`, whichever wires dispatch-opening first, is the Matter
// that should add this.

import (
	"context"
	"fmt"

	"github.com/procrastivity/wip/internal/store"
	"github.com/procrastivity/wip/internal/wiperr"
)

// lifecycleEvents maps a verb's action to the scale-correct event type — the
// lifecycle being uniform at every scale (MODEL §2.2) is what makes one table
// serve all three.
var lifecycleEvents = map[string]map[store.Scale]string{
	"started": {
		store.ScaleMatter: store.TypeMatterStarted,
		store.ScaleStage:  store.TypeStageStarted,
		store.ScaleStep:   store.TypeStepStarted,
	},
	"finished": {
		store.ScaleMatter: store.TypeMatterFinished,
		store.ScaleStage:  store.TypeStageFinished,
		store.ScaleStep:   store.TypeStepFinished,
	},
	"canceled": {
		store.ScaleMatter: store.TypeMatterCanceled,
		store.ScaleStage:  store.TypeStageCanceled,
		store.ScaleStep:   store.TypeStepCanceled,
	},
	"paused": {
		store.ScaleMatter: store.TypeMatterPaused,
		store.ScaleStage:  store.TypeStagePaused,
		store.ScaleStep:   store.TypeStepPaused,
	},
	"resumed": {
		store.ScaleMatter: store.TypeMatterResumed,
		store.ScaleStage:  store.TypeStageResumed,
		store.ScaleStep:   store.TypeStepResumed,
	},
}

// ancestorsRootFirst walks a node's Parent chain and returns its live
// ancestors root-first (Matter first, then any Stage) — the order the D57
// cascade must emit in, since the origin event is the first one emitted.
func ancestorsRootFirst(ctx context.Context, v store.View, n store.Node) ([]store.Node, error) {
	var chain []store.Node
	cur := n
	for cur.Parent != "" {
		p, err := v.Node(ctx, cur.Parent)
		if err != nil {
			return nil, err
		}
		chain = append(chain, p)
		cur = p
	}
	for i, j := 0, len(chain)-1; i < j; i, j = i+1, j-1 {
		chain[i], chain[j] = chain[j], chain[i]
	}
	return chain, nil
}

// Start moves a node from Planned to InProgress, auto-starting any Planned
// ancestor first (D57) — one command, one event per node it actually moves,
// chained by causation: the first-emitted event is the origin, and every
// ancestor wip started on its own initiative (never the node the caller
// named) carries `payload.cascade: true`. The whole chain carries the
// caller's own actor throughout (`schema` Brief §A) — wip auto-starting an
// ancestor acts within the authority the command granted it.
func Start(ctx context.Context, s *store.Store, actor store.Actor, repo, locator string) ([]store.Event, error) {
	target, err := ResolveNode(ctx, s.View, repo, locator)
	if err != nil {
		return nil, err
	}
	ancestors, err := ancestorsRootFirst(ctx, s.View, target)
	if err != nil {
		return nil, err
	}
	chain := append(ancestors, target)

	req := store.Request{Actor: actor, Env: store.Env{Repo: repo}}
	return s.Commit(ctx, req, func(ctx context.Context, tx *store.Tx) ([]store.Draft, error) {
		level, err := tx.EffectiveTrackerPushLevel(ctx, repo)
		if err != nil {
			return nil, err
		}
		var drafts []store.Draft
		for _, node := range chain {
			isTarget := node.ID == target.ID
			fresh, err := tx.Node(ctx, node.ID)
			if err != nil {
				return nil, err
			}
			if fresh.Lifecycle != store.Planned {
				if isTarget {
					return nil, wiperr.New("refusal.invalid-transition",
						fmt.Sprintf("%s is %s; start requires planned", locator, fresh.Lifecycle))
				}
				continue // this ancestor is already under way; nothing to cascade here
			}
			d := store.Draft{
				Type:    lifecycleEvents["started"][fresh.Kind],
				Subject: fresh.ID,
				Payload: store.Transition{From: store.Planned, To: store.InProgress, Cascade: !isTarget, TrackerPushLevel: level},
			}
			if len(drafts) > 0 {
				d.Cause = len(drafts) - 1
			}
			drafts = append(drafts, d)
		}
		if len(drafts) == 0 {
			return nil, store.ErrNoEvent
		}
		return drafts, nil
	})
}

// simpleTransition is Finish/Cancel/Pause/Resume's shared shape: one node,
// one event, no cascade.
func simpleTransition(ctx context.Context, s *store.Store, actor store.Actor, repo, locator, action string, from, to store.Lifecycle) (store.Node, error) {
	return simpleTransitionEnv(ctx, s, actor, store.Env{Repo: repo}, locator, action, from, to)
}

func simpleTransitionEnv(ctx context.Context, s *store.Store, actor store.Actor, env store.Env, locator, action string, from, to store.Lifecycle) (store.Node, error) {
	repo := env.Repo
	n, err := ResolveNode(ctx, s.View, repo, locator)
	if err != nil {
		return store.Node{}, err
	}
	if n.Lifecycle != from {
		return store.Node{}, wiperr.New("refusal.invalid-transition",
			fmt.Sprintf("%s is %s; %s requires %s", locator, n.Lifecycle, action, from))
	}

	req := store.Request{Actor: actor, Env: env}
	if _, err := s.Commit(ctx, req, func(ctx context.Context, tx *store.Tx) ([]store.Draft, error) {
		level, err := tx.EffectiveTrackerPushLevel(ctx, repo)
		if err != nil {
			return nil, err
		}
		fresh, err := tx.Node(ctx, n.ID)
		if err != nil {
			return nil, err
		}
		if fresh.Lifecycle != from {
			return nil, wiperr.New("refusal.invalid-transition",
				fmt.Sprintf("%s is %s; %s requires %s", locator, fresh.Lifecycle, action, from))
		}
		drafts := []store.Draft{{
			Type:    lifecycleEvents[action][fresh.Kind],
			Subject: fresh.ID,
			Payload: store.Transition{From: from, To: to, TrackerPushLevel: level},
		}}
		if action == "finished" && fresh.Kind == store.ScaleMatter {
			if sweep, found, err := sealSweepDraft(ctx, tx, fresh.ID, true, ""); err != nil {
				return nil, err
			} else if found {
				sweep.Cause = 0
				drafts = append(drafts, sweep)
			}
		}
		return drafts, nil
	}); err != nil {
		return store.Node{}, err
	}
	return s.Node(ctx, n.ID)
}

// Finish moves a node from InProgress to Done.
func Finish(ctx context.Context, s *store.Store, actor store.Actor, repo, locator string) (store.Node, error) {
	return simpleTransition(ctx, s, actor, repo, locator, "finished", store.InProgress, store.Done)
}

// FinishWithEnv is Finish with the caller's full Env, so a sealing Matter
// finish can sweep its anonymous Batch with correct event dimensions.
func FinishWithEnv(ctx context.Context, s *store.Store, actor store.Actor, env store.Env, locator string) (store.Node, error) {
	return simpleTransitionEnv(ctx, s, actor, env, locator, "finished", store.InProgress, store.Done)
}

// Cancel moves a node from InProgress to Canceled.
func Cancel(ctx context.Context, s *store.Store, actor store.Actor, repo, locator string) (store.Node, error) {
	return simpleTransition(ctx, s, actor, repo, locator, "canceled", store.InProgress, store.Canceled)
}

// Pause moves a node from InProgress to Paused.
func Pause(ctx context.Context, s *store.Store, actor store.Actor, repo, locator string) (store.Node, error) {
	return simpleTransition(ctx, s, actor, repo, locator, "paused", store.InProgress, store.Paused)
}

// Resume moves a node from Paused back to InProgress.
func Resume(ctx context.Context, s *store.Store, actor store.Actor, repo, locator string) (store.Node, error) {
	return simpleTransition(ctx, s, actor, repo, locator, "resumed", store.Paused, store.InProgress)
}
