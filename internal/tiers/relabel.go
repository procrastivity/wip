package tiers

import (
	"context"
	"fmt"

	"github.com/procrastivity/wip/internal/store"
	"github.com/procrastivity/wip/internal/wiperr"
)

// SetLabel implements `wip label <new-label>`, operating on the Clone at
// dir's git-common-dir (tiers Brief, "Labels and addressing"): per-Repo
// uniqueness enforced, a ULID-shaped label refused outright (closing the
// addressing ambiguity at the source), and — on an ordinary collision — a
// hard error naming the one alternative the collision-suggestion algorithm
// proposes, never an auto-suffix.
func SetLabel(ctx context.Context, s *store.Store, actor store.Actor, dir, newLabel string) (store.Clone, error) {
	if store.IsIdentityShaped(newLabel) {
		return store.Clone{}, wiperr.New("validation.label-ulid-shaped",
			fmt.Sprintf("%q has the shape of a ULID; a label may never take that shape, "+
				"so addressing can dispatch on shape alone", newLabel))
	}

	clone, found, err := ResolveCurrentClone(ctx, s, actor, dir)
	if err != nil {
		return store.Clone{}, err
	}
	if !found {
		return store.Clone{}, unknownClone()
	}
	if clone.Label == newLabel {
		return clone, nil
	}

	clones, err := s.ClonesOfRepo(ctx, clone.Repo)
	if err != nil {
		return store.Clone{}, err
	}
	taken := map[string]bool{}
	for _, c := range clones {
		if c.ID != clone.ID {
			taken[c.Label] = true
		}
	}
	if taken[newLabel] {
		msg := fmt.Sprintf("label %q is already used by another clone of this repo", newLabel)
		if suggestion, ok := SuggestLabel(dir, taken); ok {
			msg += fmt.Sprintf("; try %q", suggestion)
		}
		return store.Clone{}, wiperr.New("validation.label-collision", msg)
	}

	req := store.Request{Actor: actor, Env: store.Env{Repo: clone.Repo}}
	if _, err := s.Commit(ctx, req, func(_ context.Context, _ *store.Tx) ([]store.Draft, error) {
		return []store.Draft{{
			Type:    store.TypeCloneLabeled,
			Subject: clone.ID,
			Payload: store.CloneLabeled{Label: newLabel},
		}}, nil
	}); err != nil {
		return store.Clone{}, err
	}
	return s.Clone(ctx, clone.ID)
}
