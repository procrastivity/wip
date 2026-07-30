package readsurface

// Address rendering and locator resolution — the small addressing surface
// this package owns for itself rather than importing internal/writesurface
// for (this Matter carries no edge to write-surface; ResolveNode there is
// ~25 lines of ULID-vs-`/`-segments dispatch this package reproduces rather
// than depends on).

import (
	"context"
	"fmt"
	"strings"

	"github.com/procrastivity/wip/internal/store"
	"github.com/procrastivity/wip/internal/wiperr"
)

// StagePosition is where a Step sits within the Stage that groups it —
// vocabulary output 2's "Stage: tier-verbs (2 of 3)".
type StagePosition struct {
	StageLocator string
	Index, Total int
}

// Address renders a node's display locator: a Matter is its own path; a
// Stage is `<matter>/<stage>`; a Step is `<matter> · <step>` when it hangs
// directly off its Matter, or `<matter>/<stage> · <step>` when a Stage
// groups it — the "/" marks structural containment, the " · " marks "and
// specifically this node within it" (vocabulary's own drafted outputs use
// both forms). The second return is non-nil only for a Step grouped by a
// Stage, carrying that Stage's own position among its live siblings.
func Address(ctx context.Context, v store.View, n store.Node) (string, *StagePosition, error) {
	if n.Kind == store.ScaleMatter {
		return n.Locator, nil, nil
	}
	matter, err := v.Node(ctx, n.Matter)
	if err != nil {
		return "", nil, err
	}
	if n.Kind == store.ScaleStage {
		return matter.Locator + "/" + n.Locator, nil, nil
	}
	// A Step. Its parent is either the Matter itself (no Stage groups it) or
	// a Stage — never anything else (MODEL §9's max depth, D20).
	if n.Parent == matter.ID {
		return matter.Locator + " · " + n.Locator, nil, nil
	}
	stage, err := v.Node(ctx, n.Parent)
	if err != nil {
		return "", nil, err
	}
	siblings, err := v.Children(ctx, stage.ID)
	if err != nil {
		return "", nil, err
	}
	pos := &StagePosition{StageLocator: stage.Locator, Total: len(siblings)}
	for i, sib := range siblings {
		if sib.ID == n.ID {
			pos.Index = i + 1
			break
		}
	}
	return matter.Locator + "/" + stage.Locator + " · " + n.Locator, pos, nil
}

// resolveLocator resolves a `next --set` argument to a live node, scoped to
// repo: a ULID resolves by identity; anything else dispatches through
// MatterByLocator and, for a multi-segment locator, NodeByLocator on the
// last segment — write-surface's own addressing convention (its
// resolve.go), reproduced rather than imported (see this file's doc note).
func resolveLocator(ctx context.Context, v store.View, repo, locator string) (store.Node, error) {
	if store.IsIdentityShaped(locator) {
		n, err := v.Node(ctx, locator)
		if err != nil {
			return store.Node{}, wiperr.New("validation.unknown-locator", fmt.Sprintf("no node %s", locator))
		}
		return n, nil
	}

	segs := strings.Split(locator, "/")
	matter, err := v.MatterByLocator(ctx, repo, segs[0])
	if err != nil {
		return store.Node{}, wiperr.New("validation.unknown-locator", fmt.Sprintf("no matter labeled %q", segs[0]))
	}
	if len(segs) == 1 {
		return matter, nil
	}
	n, err := v.NodeByLocator(ctx, matter.ID, segs[len(segs)-1])
	if err != nil {
		return store.Node{}, wiperr.New("validation.unknown-locator",
			fmt.Sprintf("%s addresses no live node in %s", locator, matter.Locator))
	}
	return n, nil
}
