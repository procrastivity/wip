package readsurface

// The hand-off a write verb that ends the cursor's work (finish, gate close,
// cancel) prints after a successful commit — strictly post-commit, read-only
// and best-effort (D67: a write verb never moves the cursor itself, and a
// read never gates a write's success). EndedCursor decides *whether* the
// cursor's target has stopped being live and, when it has, what — if
// anything — is the obvious next step; HandoffLine and Handoff turn that
// into the sentence lifecycle and gate both print, so the two verbs never
// diverge on the wording.

import (
	"context"
	"fmt"

	"github.com/procrastivity/wip/internal/store"
)

// CursorEnded is EndedCursor's answer: the cursor was set, and its target's
// own work has ended. Reason is "sealed", "canceled", or "removed" — the
// same three ChooseNext reports. Target is the zero Node when Reason is
// "removed" (nothing left to resolve). Suggested is the first ready sibling
// step of a Step target, nil when there isn't one or the target isn't a Step
// — this is a hand-off, not the frontier: it names one obvious next step,
// never a list.
type CursorEnded struct {
	Reason    string
	Target    store.Node
	Suggested *store.Node
}

// EndedCursor reports whether the cursor at clone/worktree has stopped
// pointing at live work — nil, nil when there is no cursor or its target is
// still live, exactly mirroring next()'s own three ended-cursor branches
// (removed/canceled/sealed) but without the candidate-listing ChooseNext
// otherwise builds, since a hand-off names at most one successor.
func EndedCursor(ctx context.Context, v store.View, clone, worktree string) (*CursorEnded, error) {
	node, set, err := v.Cursor(ctx, clone, worktree)
	if err != nil {
		return nil, err
	}
	if !set {
		return nil, nil
	}

	target, err := v.Node(ctx, node)
	if err != nil {
		// Tombstoned or otherwise unresolvable (D44) — the same "removed"
		// case next() reports, not an error this function propagates.
		return &CursorEnded{Reason: "removed"}, nil
	}
	if target.Lifecycle == store.Canceled {
		return &CursorEnded{Reason: "canceled", Target: target}, nil
	}
	sealed, err := Sealed(ctx, v, target)
	if err != nil {
		return nil, err
	}
	if !sealed {
		return nil, nil
	}

	ended := &CursorEnded{Reason: "sealed", Target: target}
	if target.Kind == store.ScaleStep && target.Parent != "" {
		suggested, err := readySiblingStep(ctx, v, target)
		if err != nil {
			return nil, err
		}
		ended.Suggested = suggested
	}
	return ended, nil
}

// readySiblingStep finds the first ready step sharing target's Parent, in
// creation order (D51) — Frontier already returns Ready that way. No
// CollapseReady here: a hand-off wants the step grain specifically, never a
// Matter or Stage standing in for its own interior.
func readySiblingStep(ctx context.Context, v store.View, target store.Node) (*store.Node, error) {
	ready, _, err := Frontier(ctx, v)
	if err != nil {
		return nil, err
	}
	for _, n := range ready {
		if n.Kind == store.ScaleStep && n.Parent == target.Parent {
			n := n
			return &n, nil
		}
	}
	return nil, nil
}

// HandoffLine renders CursorEnded as the one sentence lifecycle and gate
// both print — generic when there's no named successor, naming the
// successor's own locator and its Matter's locator when there is one.
// ended == nil renders "" (no hand-off), the same case its caller — Handoff
// — folds into a bool rather than an error.
func HandoffLine(ctx context.Context, v store.View, ended *CursorEnded) (string, error) {
	if ended == nil {
		return "", nil
	}
	if ended.Suggested == nil {
		return "the cursor's work is done — wip next to choose what's next, or wip next --clear", nil
	}
	matter, err := v.Node(ctx, ended.Suggested.Matter)
	if err != nil {
		return "", err
	}
	return fmt.Sprintf("%s is next in %s: wip next --set %s — or wip next / wip next --clear",
		ended.Suggested.Locator, matter.Locator, ended.Suggested.Locator), nil
}

// Handoff composes EndedCursor and HandoffLine into the one call a write
// verb needs after its commit succeeds, plus the target's own composed
// address for a JSON payload's "target" field. ok is false whenever there is
// nothing to report — no cursor, a still-live target, or any read along the
// way failing — which is exactly "skip the hand-off silently," the
// best-effort contract every caller of this function holds to.
func Handoff(ctx context.Context, v store.View, clone, worktree string) (ended *CursorEnded, line, targetAddress string, ok bool) {
	e, err := EndedCursor(ctx, v, clone, worktree)
	if err != nil || e == nil {
		return nil, "", "", false
	}
	l, err := HandoffLine(ctx, v, e)
	if err != nil {
		return nil, "", "", false
	}
	addr := ""
	if e.Target.ID != "" {
		a, _, err := Address(ctx, v, e.Target)
		if err != nil {
			return nil, "", "", false
		}
		addr = a
	}
	return e, l, addr, true
}
