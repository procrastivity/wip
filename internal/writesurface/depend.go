package writesurface

// Stage gates-and-dependencies, step-03: `blocked-by` edges. The static
// cycle check lives in `schema` (store.WouldCycle) and is *called* here as a
// precondition — this Matter does not re-implement it, it invokes it before
// the write commits so a cycle-creating edge never reaches the store (D29).

import (
	"context"
	"fmt"

	"github.com/procrastivity/wip/internal/store"
	"github.com/procrastivity/wip/internal/wiperr"
)

// DependAdd adds a `blocked-by` edge: blockedLocator waits for
// blockerLocator. Any node may block any node (D28), within or across
// Matters — the verb accepts what the model allows; keeping cross-Matter
// edges at Matter/Stage grain is this dogfood's own register discipline
// (HANDOFF §1.4), not a rule this verb enforces.
func DependAdd(ctx context.Context, s *store.Store, actor store.Actor, repo, blockedLocator, blockerLocator string) (store.Edge, error) {
	blocked, err := ResolveNode(ctx, s.View, repo, blockedLocator)
	if err != nil {
		return store.Edge{}, err
	}
	blocker, err := ResolveNode(ctx, s.View, repo, blockerLocator)
	if err != nil {
		return store.Edge{}, err
	}

	cycle, err := s.WouldCycle(ctx, blocked.ID, blocker.ID)
	if err != nil {
		return store.Edge{}, err
	}
	if cycle {
		return store.Edge{}, wiperr.New("refusal.blocked-by-cycle",
			fmt.Sprintf("refused — %s is already blocked-by %s; this edge would create a cycle", blockerLocator, blockedLocator))
	}

	req := store.Request{Actor: actor, Env: store.Env{Repo: repo}}
	var edgeID string
	if _, err := s.Commit(ctx, req, func(_ context.Context, tx *store.Tx) ([]store.Draft, error) {
		edgeID = tx.NewID()
		return []store.Draft{{
			Type:    store.TypeDependencyAdded,
			Subject: blocked.ID,
			Payload: store.DependencyChange{Edge: edgeID, Blocker: blocker.ID},
		}}, nil
	}); err != nil {
		return store.Edge{}, err
	}
	return store.Edge{ID: edgeID, Blocked: blocked.ID, Blocker: blocker.ID}, nil
}

// DependRemove tombstones a live `blocked-by` edge (D44) — cancel keeps the
// question alive; remove answers it (D64).
func DependRemove(ctx context.Context, s *store.Store, actor store.Actor, repo, blockedLocator, blockerLocator string) error {
	blocked, err := ResolveNode(ctx, s.View, repo, blockedLocator)
	if err != nil {
		return err
	}
	blocker, err := ResolveNode(ctx, s.View, repo, blockerLocator)
	if err != nil {
		return err
	}
	edge, found, err := s.EdgeBetween(ctx, blocked.ID, blocker.ID)
	if err != nil {
		return err
	}
	if !found {
		return wiperr.New("validation.no-such-edge", fmt.Sprintf("%s is not blocked-by %s", blockedLocator, blockerLocator))
	}

	req := store.Request{Actor: actor, Env: store.Env{Repo: repo}}
	_, err = s.Commit(ctx, req, func(_ context.Context, _ *store.Tx) ([]store.Draft, error) {
		return []store.Draft{{
			Type:    store.TypeDependencyRemoved,
			Subject: blocked.ID,
			Payload: store.DependencyChange{Edge: edge.ID, Blocker: blocker.ID},
		}}, nil
	})
	return err
}
