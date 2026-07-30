package writesurface

// The addressing convention every write-surface verb's locator argument goes
// through: a ULID resolves by identity; anything else is a `/`-separated
// path whose first segment names a Matter (a Matter is its own Matter, D2)
// and whose *last* segment — if there is more than one — names the live node
// within it. A middle segment (a Stage's own locator, in
// "<matter>/<stage>/<step>") is presentational only and is never checked: a
// grouping is not a namespace (D16), so `step-04`'s locator is already
// globally unique within its Matter regardless of which Stage groups it, and
// `<matter>/<stage>` and `<matter>/step-04` both resolve the same way this
// does — by the last segment.

import (
	"context"
	"fmt"
	"strings"

	"github.com/procrastivity/wip/internal/store"
	"github.com/procrastivity/wip/internal/wiperr"
)

// ResolveNode resolves a locator to a live node, scoped to repo. A ULID
// resolves by identity; anything else dispatches through MatterByLocator and,
// for a multi-segment locator, NodeByLocator on the last segment.
func ResolveNode(ctx context.Context, v store.View, repo, locator string) (store.Node, error) {
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

// ResolveMatter is ResolveNode narrowed to a Matter — the shape `wip gate
// declare`, `wip backlog plan` and the bind verb's locator argument need.
func ResolveMatter(ctx context.Context, v store.View, repo, locator string) (store.Node, error) {
	n, err := ResolveNode(ctx, v, repo, locator)
	if err != nil {
		return store.Node{}, err
	}
	if n.Kind != store.ScaleMatter {
		return store.Node{}, wiperr.New("validation.not-a-matter", fmt.Sprintf("%s is a %s, not a matter", locator, n.Kind))
	}
	return n, nil
}
