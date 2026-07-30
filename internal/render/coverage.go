package render

import (
	"context"

	"github.com/procrastivity/wip/internal/store"
)

// EagerScope implements step-05's resolved coverage policy: every render
// trigger (dispatch-open, or an explicit `wip refresh` with no locator)
// eagerly covers every not-sealed Matter in this Repo. A sealed Matter is
// archival, not being worked — auto-rendering it on every dispatch would
// grow `.wip/generated/` without bound for no reader — so it renders only
// when named explicitly via `wip refresh <sealed-locator>` (renderMatterTree
// called directly on it, bypassing this scope). This is a coverage policy,
// not a storage decision (MODEL §3.1).
//
// A node crossing into Done+sealed between two eager passes simply stops
// appearing here on the next pass; its last-rendered file is left in place
// — stale but harmless, per D33, until explicitly refreshed again or swept.
func EagerScope(ctx context.Context, s *store.Store, repo string) ([]store.Node, error) {
	matters, err := s.Matters(ctx, repo)
	if err != nil {
		return nil, err
	}
	sealed, err := s.ArchivedMatters(ctx, repo)
	if err != nil {
		return nil, err
	}
	isSealed := make(map[string]bool, len(sealed))
	for _, m := range sealed {
		isSealed[m.ID] = true
	}

	out := make([]store.Node, 0, len(matters))
	for _, m := range matters {
		if !isSealed[m.ID] {
			out = append(out, m)
		}
	}
	return out, nil
}
