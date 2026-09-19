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

// FindingSubject is what AppendFinding writes to: a live node, or — findings
// being the one content kind backlog entries carry (v12) — a backlog entry.
type FindingSubject struct {
	ID      string
	Backlog bool
}

// ResolveFindingSubject is ResolveNode widened for the findings write path
// only. A ULID that names no live node is tried against the Repo's backlog
// before it is refused; every other verb keeps ResolveNode's node-only answer.
func ResolveFindingSubject(ctx context.Context, v store.View, repo, locator string) (FindingSubject, error) {
	if store.IsIdentityShaped(locator) {
		if n, err := v.Node(ctx, locator); err == nil {
			return FindingSubject{ID: n.ID}, nil
		}
		e, ok, err := v.BacklogEntry(ctx, repo, locator)
		if err != nil {
			return FindingSubject{}, err
		}
		if ok {
			return FindingSubject{ID: e.ID, Backlog: true}, nil
		}
		return FindingSubject{}, wiperr.New("validation.unknown-locator",
			fmt.Sprintf("no node or backlog entry %s", locator))
	}
	n, err := ResolveNode(ctx, v, repo, locator)
	if err != nil {
		return FindingSubject{}, err
	}
	return FindingSubject{ID: n.ID}, nil
}

// ResolveMatter is ResolveNode narrowed to a Matter — the shape `wip plumbing gate
// declare`, `wip plumbing backlog plan` and the bind verb's locator argument need.
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
