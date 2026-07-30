package readsurface

// Step-04: `wip next` — the cursor fused with the frontier, producing
// exactly vocabulary's five drafted outputs plus the dangling-cursor case
// D67 makes first-class. `next` needs a resolved current Clone + Worktree
// (unlike `status`, it does not inherit `tiers`'s host-wide carve-out) and
// composes over Frontier/FinishedNodes/LocallyComplete/Sealed — the same
// logic `status` (step-03) consumes, so the two verbs never diverge on what
// "unblocked" means.

import (
	"context"

	"github.com/procrastivity/wip/internal/store"
)

// Kind discriminates NextView's five ratified shapes plus the dangling-
// cursor extension D67 makes first-class — six results, never a guess among
// them.
type Kind int

const (
	// BareMatter is vocabulary output 1: the cursor sits on a Matter that is
	// its own smallest node (no Stages, no Steps, D2).
	BareMatter Kind = iota
	// Positioned is vocabulary output 2: the cursor sits on a Stage, a Step,
	// or a Matter that has children — its address and blocked-by state are
	// shown.
	Positioned
	// NothingUnblocked is vocabulary output 3: no cursor, and every Planned
	// node in scope is still waiting on something.
	NothingUnblocked
	// NoCursor is vocabulary output 4: no cursor, and the frontier is
	// non-empty — candidates are listed, never guessed among.
	NoCursor
	// EverythingSealed is vocabulary output 5: no cursor, nothing Planned is
	// outstanding at all.
	EverythingSealed
	// Dangling is D67's first-class result: the cursor's target is
	// tombstoned, Canceled, or sealed. next reports the fact and the
	// unblocked candidates, and never moves the cursor itself.
	Dangling
)

// View is what `next` renders. Only the fields its Kind calls for are
// populated; the rest are the zero value.
type View struct {
	Kind Kind

	// BareMatter / Positioned / Dangling's own target.
	Node    store.Node
	Address string
	Stage   *StagePosition // Positioned only, and only when a Stage groups the Step

	// Positioned's own blocked-by state — informational, since nothing in P1
	// enforces blocked-by satisfaction at start time.
	Unmet []store.Node
	Met   []store.Node

	// Dangling's own reason: "sealed", "canceled", or "removed" — the last
	// naming a tombstoned target, since a removed node cannot be resolved by
	// identity into anything more specific than "gone".
	DanglingReason string

	// NoCursor / Dangling's candidate list — the ready frontier, repo-scoped.
	Candidates []store.Node

	// NothingUnblocked's list: what's still waiting, and on what.
	Blocked []Blocked

	// EverythingSealed's nudge.
	BacklogCount int
}

// Next computes the fused cursor+frontier answer for the current Clone +
// Worktree resolved from dir.
func Next(ctx context.Context, s *store.Store, actor store.Actor, dir string) (View, error) {
	cur, err := ResolveCurrent(ctx, s, actor, dir)
	if err != nil {
		return View{}, err
	}
	return next(ctx, s.View, cur)
}

// next is Next's composition against an already-resolved Current — factored
// out so package tests can exercise every branch without a real git clone
// for ResolveCurrent to shell out to (the e2e suite in internal/cli covers
// that resolution end to end, including the real `wip next --set` write
// path — step-07(b)).
func next(ctx context.Context, v store.View, cur Current) (View, error) {
	repo := cur.Repo.ID

	node, set, err := Cursor(ctx, v, cur)
	if err != nil {
		return View{}, err
	}

	ready, blocked, err := Frontier(ctx, v)
	if err != nil {
		return View{}, err
	}
	readyInRepo := filterByRepo(ready, repo)
	blockedInRepo := filterBlockedByRepo(blocked, repo)

	if !set {
		return noCursorView(ctx, v, repo, readyInRepo, blockedInRepo)
	}

	target, err := v.Node(ctx, node)
	if err != nil {
		// Tombstoned or otherwise unresolvable: the target is gone (D44), and
		// that is exactly the dangling case, not an error next propagates.
		return View{Kind: Dangling, DanglingReason: "removed", Candidates: readyInRepo}, nil
	}
	if target.Lifecycle == store.Canceled {
		return View{Kind: Dangling, Node: target, DanglingReason: "canceled", Candidates: readyInRepo}, nil
	}
	sealed, err := Sealed(ctx, v, target)
	if err != nil {
		return View{}, err
	}
	if sealed {
		return View{Kind: Dangling, Node: target, DanglingReason: "sealed", Candidates: readyInRepo}, nil
	}

	return positionedView(ctx, v, target)
}

func noCursorView(ctx context.Context, v store.View, repo string, ready []store.Node, blocked []Blocked) (View, error) {
	if len(ready) > 0 {
		return View{Kind: NoCursor, Candidates: ready}, nil
	}
	if len(blocked) > 0 {
		return View{Kind: NothingUnblocked, Blocked: blocked}, nil
	}
	entries, err := v.Backlog(ctx, repo)
	if err != nil {
		return View{}, err
	}
	unprocessed := 0
	for _, e := range entries {
		if e.State == "entered" {
			unprocessed++
		}
	}
	return View{Kind: EverythingSealed, BacklogCount: unprocessed}, nil
}

func positionedView(ctx context.Context, v store.View, target store.Node) (View, error) {
	address, stagePos, err := Address(ctx, v, target)
	if err != nil {
		return View{}, err
	}

	edges, err := v.BlockedBy(ctx, target.ID)
	if err != nil {
		return View{}, err
	}
	var unmet, met []store.Node
	for _, e := range edges {
		blocker, err := v.Node(ctx, e.Blocker)
		if err != nil {
			return View{}, err
		}
		ok, err := LocallyComplete(ctx, v, blocker)
		if err != nil {
			return View{}, err
		}
		if ok {
			met = append(met, blocker)
		} else {
			unmet = append(unmet, blocker)
		}
	}

	kind := Positioned
	if target.Kind == store.ScaleMatter {
		children, err := v.Children(ctx, target.ID)
		if err != nil {
			return View{}, err
		}
		if len(children) == 0 {
			kind = BareMatter
		}
	}

	return View{
		Kind: kind, Node: target, Address: address, Stage: stagePos,
		Unmet: unmet, Met: met,
	}, nil
}

func filterByRepo(nodes []store.Node, repo string) []store.Node {
	var out []store.Node
	for _, n := range nodes {
		if n.Repo == repo {
			out = append(out, n)
		}
	}
	return out
}

func filterBlockedByRepo(blocked []Blocked, repo string) []Blocked {
	var out []Blocked
	for _, b := range blocked {
		if b.Node.Repo == repo {
			out = append(out, b)
		}
	}
	return out
}

// SetCursor implements `wip next --set <locator>`: the one write path this
// whole Matter has. It resolves locator against the current Repo, moves the
// cursor for the current Clone + Worktree, and emits the one event this
// Matter produces — cursor.moved (an execution event carrying repo, clone
// and worktree, D56), subject = the target's own ULID (identity, never a
// locator), payload recording the prior target for narratability. A read
// verb repairs nothing (D67); this is not one — `next` without `--set`
// never reaches this function.
func SetCursor(ctx context.Context, s *store.Store, actor store.Actor, dir, locator string) (store.Node, error) {
	cur, err := ResolveCurrent(ctx, s, actor, dir)
	if err != nil {
		return store.Node{}, err
	}
	return setCursor(ctx, s, actor, cur, locator)
}

// setCursor is SetCursor's write path against an already-resolved Current —
// factored out so package tests can exercise the real write (and D67's "the
// cursor is never moved by a read" assertion) without needing a real git
// clone for ResolveCurrent to shell out to.
func setCursor(ctx context.Context, s *store.Store, actor store.Actor, cur Current, locator string) (store.Node, error) {
	target, err := resolveLocator(ctx, s.View, cur.Repo.ID, locator)
	if err != nil {
		return store.Node{}, err
	}

	req := store.Request{Actor: actor, Env: cur.Env()}
	if _, err := s.Commit(ctx, req, func(ctx context.Context, tx *store.Tx) ([]store.Draft, error) {
		previous, _, err := tx.Cursor(ctx, cur.Clone.ID, cur.Worktree.ID)
		if err != nil {
			return nil, err
		}
		return []store.Draft{{
			Type:    store.TypeCursorMoved,
			Subject: cur.Worktree.ID,
			Payload: store.CursorMoved{Node: target.ID, Previous: previous},
		}}, nil
	}); err != nil {
		return store.Node{}, err
	}
	return target, nil
}
