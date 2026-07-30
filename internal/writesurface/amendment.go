package writesurface

// Stage birth-and-amendment, step-03: the four amendment verbs — insert,
// reorder, replace, remove — complete because identity (D44) removed the old
// obstruction: with an opaque ULID per node, a Step can be amended without
// breaking any reference, since nothing points at a position. Registered for
// `step.*` only (the schema Brief: "Stage equivalents exist where earned;
// Steps are the primary case" — no stage.* amendment type is in the P1
// taxonomy at all).

import (
	"context"
	"fmt"

	"github.com/procrastivity/wip/internal/store"
	"github.com/procrastivity/wip/internal/wiperr"
)

// liveSteps returns parent's live Step children, in sibling order — the
// amendment target set. Amendment concerns Steps only; a Matter or Stage
// hanging directly under the same parent (D2) is not part of this ordering.
func liveSteps(ctx context.Context, s *store.Store, parent string) ([]store.Node, error) {
	children, err := s.Children(ctx, parent)
	if err != nil {
		return nil, err
	}
	steps := children[:0:0]
	for _, c := range children {
		if c.Kind == store.ScaleStep {
			steps = append(steps, c)
		}
	}
	return steps, nil
}

// InsertStep births a Step between siblings (or at either end) rather than
// after all of them. The common case computes a single sort key with a
// midpoint (SortKeyBetween) and commits one event, `step.inserted` — exactly
// like CreateStep but positioned. When the gap between the two neighbours is
// exhausted, the schema Brief's own anticipated fallback fires: the existing
// live Steps are renumbered to dense sort keys first (`step.reordered`, this
// command's origin draft) and the new Step is inserted into the resulting gap
// (`step.inserted`, caused by the reorder) — one command emitting the two
// events the rebalance genuinely requires, the same "one command, several
// verbs, each emitting exactly one event" shape D57's start-cascade uses.
func InsertStep(ctx context.Context, s *store.Store, actor store.Actor, repo, parentLocator, title, after, before string) (store.Node, error) {
	if after != "" && before != "" {
		return store.Node{}, wiperr.New("validation.conflicting-flags", "pass at most one of --after or --before")
	}
	parent, err := ResolveNode(ctx, s.View, repo, parentLocator)
	if err != nil {
		return store.Node{}, err
	}
	if parent.Kind == store.ScaleStep {
		return store.Node{}, wiperr.New("validation.invalid-parent", fmt.Sprintf("%s is a step; a step's parent must be a matter or a stage", parentLocator))
	}
	siblings, err := liveSteps(ctx, s, parent.ID)
	if err != nil {
		return store.Node{}, err
	}

	position := len(siblings)
	switch {
	case after != "":
		position, err = siblingIndex(ctx, s, repo, siblings, after)
		if err != nil {
			return store.Node{}, err
		}
		position++
	case before != "":
		position, err = siblingIndex(ctx, s, repo, siblings, before)
		if err != nil {
			return store.Node{}, err
		}
	}

	locator, err := s.NextStepLocator(ctx, parent.Matter)
	if err != nil {
		return store.Node{}, err
	}

	var newKey int64
	fit := true
	switch {
	case len(siblings) == 0 || position == len(siblings):
		newKey, err = s.NextSortKey(ctx, parent.ID)
		if err != nil {
			return store.Node{}, err
		}
	case position == 0:
		newKey, fit = store.SortKeyBetween(0, siblings[0].SortKey)
	default:
		newKey, fit = store.SortKeyBetween(siblings[position-1].SortKey, siblings[position].SortKey)
	}

	req := store.Request{Actor: actor, Env: store.Env{Repo: repo}}
	var id string
	if _, err := s.Commit(ctx, req, func(_ context.Context, tx *store.Tx) ([]store.Draft, error) {
		var drafts []store.Draft
		key := newKey
		if !fit {
			// The gap ran out: renumber the existing live Steps to dense
			// multiples of 1000, in their existing order, then place the new
			// Step at position*1000+500 — a slot that sits strictly between
			// any two neighbours the renumbering produced, whatever position
			// is (D51: the magnitude is meaningless, only the order is real).
			order := make([]string, len(siblings))
			for i, sib := range siblings {
				order[i] = sib.ID
			}
			drafts = append(drafts, store.Draft{
				Type:    store.TypeStepReordered,
				Subject: parent.ID,
				Payload: store.Reordered{Order: order},
			})
			key = int64(position)*1000 + 500
		}
		id = tx.NewID()
		insert := store.Draft{
			Type:    store.TypeStepInserted,
			Subject: id,
			Payload: store.NodeBirth{Title: title, Locator: locator, Parent: parent.ID, SortKey: key},
		}
		if len(drafts) > 0 {
			insert.Cause = 0
		}
		drafts = append(drafts, insert)
		return drafts, nil
	}); err != nil {
		return store.Node{}, err
	}
	return s.Node(ctx, id)
}

// siblingIndex finds locator's position among siblings, refusing if it names
// something other than a live Step child of the same parent.
func siblingIndex(ctx context.Context, s *store.Store, repo string, siblings []store.Node, locator string) (int, error) {
	n, err := ResolveNode(ctx, s.View, repo, locator)
	if err != nil {
		return 0, err
	}
	for i, sib := range siblings {
		if sib.ID == n.ID {
			return i, nil
		}
	}
	return 0, wiperr.New("validation.not-a-sibling", fmt.Sprintf("%s is not a live step among these siblings", locator))
}

// ReorderStep rewrites the whole sibling order of parent's live Steps.
// order must name exactly that set — a partial reorder is refused rather
// than silently dropping a sibling.
func ReorderStep(ctx context.Context, s *store.Store, actor store.Actor, repo, parentLocator string, order []string) (store.Node, error) {
	parent, err := ResolveNode(ctx, s.View, repo, parentLocator)
	if err != nil {
		return store.Node{}, err
	}
	siblings, err := liveSteps(ctx, s, parent.ID)
	if err != nil {
		return store.Node{}, err
	}
	if len(order) != len(siblings) {
		return store.Node{}, wiperr.New("validation.incomplete-order",
			fmt.Sprintf("%s has %d live steps; the reorder named %d", parentLocator, len(siblings), len(order)))
	}
	live := make(map[string]bool, len(siblings))
	for _, sib := range siblings {
		live[sib.ID] = true
	}
	resolved := make([]string, len(order))
	for i, locator := range order {
		n, err := ResolveNode(ctx, s.View, repo, locator)
		if err != nil {
			return store.Node{}, err
		}
		if !live[n.ID] {
			return store.Node{}, wiperr.New("validation.not-a-sibling", fmt.Sprintf("%s is not a live step of %s", locator, parentLocator))
		}
		resolved[i] = n.ID
	}

	req := store.Request{Actor: actor, Env: store.Env{Repo: repo}}
	if _, err := s.Commit(ctx, req, func(_ context.Context, _ *store.Tx) ([]store.Draft, error) {
		return []store.Draft{{
			Type:    store.TypeStepReordered,
			Subject: parent.ID,
			Payload: store.Reordered{Order: resolved},
		}}, nil
	}); err != nil {
		return store.Node{}, err
	}
	return s.Node(ctx, parent.ID)
}

// ReplaceStep tombstones a Step and births its replacement in the same
// sibling position. The replacement gets the next sequential step-NN, never
// the old one: locators are never renamed or reused (D16, D44).
func ReplaceStep(ctx context.Context, s *store.Store, actor store.Actor, repo, stepLocator, title string) (store.Node, error) {
	n, err := ResolveNode(ctx, s.View, repo, stepLocator)
	if err != nil {
		return store.Node{}, err
	}
	if n.Kind != store.ScaleStep {
		return store.Node{}, wiperr.New("validation.not-a-step", fmt.Sprintf("%s is a %s; only a step may be replaced", stepLocator, n.Kind))
	}
	locator, err := s.NextStepLocator(ctx, n.Matter)
	if err != nil {
		return store.Node{}, err
	}

	req := store.Request{Actor: actor, Env: store.Env{Repo: repo}}
	var replacementID string
	if _, err := s.Commit(ctx, req, func(_ context.Context, tx *store.Tx) ([]store.Draft, error) {
		replacementID = tx.NewID()
		return []store.Draft{{
			Type:    store.TypeStepReplaced,
			Subject: n.ID,
			Payload: store.Replaced{Replacement: replacementID, Title: title, Locator: locator},
		}}, nil
	}); err != nil {
		return store.Node{}, err
	}
	return s.Node(ctx, replacementID)
}

// RemoveStep tombstones a Step. The ULID is never reissued and every prior
// event referencing it stays a valid identity reference (D44).
func RemoveStep(ctx context.Context, s *store.Store, actor store.Actor, repo, stepLocator, reason string) error {
	n, err := ResolveNode(ctx, s.View, repo, stepLocator)
	if err != nil {
		return err
	}
	if n.Kind != store.ScaleStep {
		return wiperr.New("validation.not-a-step", fmt.Sprintf("%s is a %s; only a step may be removed", stepLocator, n.Kind))
	}

	req := store.Request{Actor: actor, Env: store.Env{Repo: repo}}
	_, err = s.Commit(ctx, req, func(_ context.Context, _ *store.Tx) ([]store.Draft, error) {
		return []store.Draft{{
			Type:    store.TypeStepRemoved,
			Subject: n.ID,
			Payload: store.Removed{Reason: reason},
		}}, nil
	})
	return err
}
