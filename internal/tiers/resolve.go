package tiers

// This file is the addressing resolver the tiers Brief's "Labels and
// addressing" section documents: `--repo`/`--clone` resolution, dispatched
// on shape — a 26-character Crockford-Base32 string is looked up as a ULID,
// anything else as a label. Every verb in this Stage onward, and every later
// Matter's verb that accepts a tier locator, uses this rather than
// reimplementing the dispatch.

import (
	"context"
	"fmt"

	"github.com/procrastivity/wip/internal/store"
	"github.com/procrastivity/wip/internal/wiperr"
)

// ResolveClone resolves --clone <locator>: a ULID-shaped string is looked up
// by identity, anything else by label. A Clone's label is unique only within
// its Repo (two clones of different Repos may share one), so a label lookup
// is scoped to repo when it is given, and searches every Repo when it is
// not — erroring on ambiguity rather than guessing.
func ResolveClone(ctx context.Context, v store.View, repo, locator string) (store.Clone, error) {
	if store.IsIdentityShaped(locator) {
		c, err := v.Clone(ctx, locator)
		if err != nil {
			return store.Clone{}, wiperr.New("validation.unknown-locator", fmt.Sprintf("no clone %s", locator))
		}
		return c, nil
	}

	var repos []string
	if repo != "" {
		repos = []string{repo}
	} else {
		all, err := v.Repos(ctx)
		if err != nil {
			return store.Clone{}, err
		}
		for _, r := range all {
			repos = append(repos, r.ID)
		}
	}

	var candidates []store.Clone
	for _, r := range repos {
		clones, err := v.ClonesOfRepo(ctx, r)
		if err != nil {
			return store.Clone{}, err
		}
		for _, c := range clones {
			if c.Label == locator {
				candidates = append(candidates, c)
			}
		}
	}
	switch len(candidates) {
	case 0:
		return store.Clone{}, wiperr.New("validation.unknown-locator", fmt.Sprintf("no clone labeled %q", locator))
	case 1:
		return candidates[0], nil
	default:
		return store.Clone{}, wiperr.New("validation.ambiguous-locator",
			fmt.Sprintf("%q labels a clone in more than one repo; scope with --repo", locator))
	}
}

// ResolveRepo resolves --repo <locator>: ULID-shaped dispatches to identity;
// anything else looks up a Repo by its own `label` column, per the same
// shape rule. In P1 that column is never written by this Matter — only
// Clone labels are addressable, since that is all the Brief's "Labels and
// addressing" section calls for — so a non-ULID --repo currently always
// misses. The dispatch rule is written generally regardless, so a later
// Matter that starts writing repos.label needs no resolver change.
func ResolveRepo(ctx context.Context, v store.View, locator string) (store.Repo, error) {
	if store.IsIdentityShaped(locator) {
		r, err := v.Repo(ctx, locator)
		if err != nil {
			return store.Repo{}, wiperr.New("validation.unknown-locator", fmt.Sprintf("no repo %s", locator))
		}
		return r, nil
	}

	all, err := v.Repos(ctx)
	if err != nil {
		return store.Repo{}, err
	}
	var candidates []store.Repo
	for _, r := range all {
		if r.Label == locator {
			candidates = append(candidates, r)
		}
	}
	switch len(candidates) {
	case 0:
		return store.Repo{}, wiperr.New("validation.unknown-locator", fmt.Sprintf("no repo labeled %q", locator))
	case 1:
		return candidates[0], nil
	default:
		return store.Repo{}, wiperr.New("validation.ambiguous-locator", fmt.Sprintf("%q labels more than one repo", locator))
	}
}
