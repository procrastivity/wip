package writesurface

// Stage gates-and-dependencies, step-04: `wip bind` — ships inert (D25, §5).
// The verb and its event ship now, in P1; nothing consumes the reference
// until Phase 3.

import (
	"context"

	"github.com/procrastivity/wip/internal/store"
)

// Bind sets a node's external reference — a late, mutable update to a
// nullable column keyed on identity (MODEL §5: "references are acquired, not
// assigned"), never a rewrite.
func Bind(ctx context.Context, s *store.Store, actor store.Actor, repo, locator, ref string) (store.Node, error) {
	n, err := ResolveNode(ctx, s.View, repo, locator)
	if err != nil {
		return store.Node{}, err
	}

	req := store.Request{Actor: actor, Env: store.Env{Repo: repo}}
	if _, err := s.Commit(ctx, req, func(_ context.Context, _ *store.Tx) ([]store.Draft, error) {
		return []store.Draft{{
			Type:    store.TypeReferenceBound,
			Subject: n.ID,
			Payload: store.ReferenceBound{Ref: ref},
		}}, nil
	}); err != nil {
		return store.Node{}, err
	}
	return s.Node(ctx, n.ID)
}
