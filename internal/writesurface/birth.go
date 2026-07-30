package writesurface

// Stage birth-and-amendment, step-02: the three birth verbs. Each creates a
// node shell and emits exactly one `matter.created`/`stage.created`/
// `step.created` (CONTRACT §B), the node entering Planned (MODEL §2.2).
// `subject` is the new node's ULID (identity, never a locator). No prose
// payload travels here — content is Stage content-prose.

import (
	"context"
	"fmt"

	"github.com/procrastivity/wip/internal/store"
	"github.com/procrastivity/wip/internal/wiperr"
)

// CreateMatter births a Matter — the addressable root, no parent (D2). Its
// own locator is slugify(title); a Matter's locator has to be unique across
// the whole Repo for addressing to resolve unambiguously, which the DB's own
// (matter, locator) unique index does not enforce for a row that is its own
// matter, so this is checked here rather than left to a constraint.
func CreateMatter(ctx context.Context, s *store.Store, actor store.Actor, repo, title string) (store.Node, error) {
	locator := slugify(title)
	if locator == "" {
		return store.Node{}, wiperr.New("validation.invalid-title", "a matter's title must contain at least one letter or digit")
	}
	if _, err := s.MatterByLocator(ctx, repo, locator); err == nil {
		return store.Node{}, wiperr.New("validation.locator-collision", fmt.Sprintf("a matter labeled %q already exists in this repo", locator))
	}

	req := store.Request{Actor: actor, Env: store.Env{Repo: repo}}
	var id string
	if _, err := s.Commit(ctx, req, func(_ context.Context, tx *store.Tx) ([]store.Draft, error) {
		id = tx.NewID()
		return []store.Draft{{
			Type:    store.TypeMatterCreated,
			Subject: id,
			Payload: store.NodeBirth{Title: title, Locator: locator, SortKey: 0},
		}}, nil
	}); err != nil {
		return store.Node{}, err
	}
	return s.Node(ctx, id)
}

// CreateStage births a Stage under a live Matter. Its own locator is
// slugify(title); uniqueness within the Matter is the DB's own (matter,
// locator) unique index (nodes_locator).
func CreateStage(ctx context.Context, s *store.Store, actor store.Actor, repo, parentLocator, title string) (store.Node, error) {
	parent, err := ResolveNode(ctx, s.View, repo, parentLocator)
	if err != nil {
		return store.Node{}, err
	}
	if parent.Kind != store.ScaleMatter {
		return store.Node{}, wiperr.New("validation.not-a-matter", fmt.Sprintf("%s is a %s; a stage's parent must be a matter", parentLocator, parent.Kind))
	}
	locator := slugify(title)
	if locator == "" {
		return store.Node{}, wiperr.New("validation.invalid-title", "a stage's title must contain at least one letter or digit")
	}
	if _, err := s.NodeByLocator(ctx, parent.ID, locator); err == nil {
		return store.Node{}, wiperr.New("validation.locator-collision", fmt.Sprintf("%s already has a node labeled %q", parentLocator, locator))
	}

	sortKey, err := s.NextSortKey(ctx, parent.ID)
	if err != nil {
		return store.Node{}, err
	}
	req := store.Request{Actor: actor, Env: store.Env{Repo: repo}}
	var id string
	if _, err := s.Commit(ctx, req, func(_ context.Context, tx *store.Tx) ([]store.Draft, error) {
		id = tx.NewID()
		return []store.Draft{{
			Type:    store.TypeStageCreated,
			Subject: id,
			Payload: store.NodeBirth{Title: title, Locator: locator, Parent: parent.ID, SortKey: sortKey},
		}}, nil
	}); err != nil {
		return store.Node{}, err
	}
	return s.Node(ctx, id)
}

// CreateStep births a Step under a live Matter or Stage — D2's "a Matter may
// be its own smallest node" means a Step may hang directly under a Matter.
// Its locator is never the title's: `step-NN`, globally sequential within the
// Matter and never renumbered (D16), which NextStepLocator computes.
func CreateStep(ctx context.Context, s *store.Store, actor store.Actor, repo, parentLocator, title string) (store.Node, error) {
	parent, err := ResolveNode(ctx, s.View, repo, parentLocator)
	if err != nil {
		return store.Node{}, err
	}
	if parent.Kind == store.ScaleStep {
		return store.Node{}, wiperr.New("validation.invalid-parent", fmt.Sprintf("%s is a step; a step's parent must be a matter or a stage", parentLocator))
	}

	locator, err := s.NextStepLocator(ctx, parent.Matter)
	if err != nil {
		return store.Node{}, err
	}
	sortKey, err := s.NextSortKey(ctx, parent.ID)
	if err != nil {
		return store.Node{}, err
	}
	req := store.Request{Actor: actor, Env: store.Env{Repo: repo}}
	var id string
	if _, err := s.Commit(ctx, req, func(_ context.Context, tx *store.Tx) ([]store.Draft, error) {
		id = tx.NewID()
		return []store.Draft{{
			Type:    store.TypeStepCreated,
			Subject: id,
			Payload: store.NodeBirth{Title: title, Locator: locator, Parent: parent.ID, SortKey: sortKey},
		}}, nil
	}); err != nil {
		return store.Node{}, err
	}
	return s.Node(ctx, id)
}
