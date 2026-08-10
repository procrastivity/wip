package writesurface

// Matter-owned tracker-reference membership. The provider-neutral relation is
// additive; unbind removes one member and rebind atomically replaces one.

import (
	"context"

	"github.com/procrastivity/wip/internal/store"
)

// Bind adds one reference to a Matter's active set.
func Bind(ctx context.Context, s *store.Store, actor store.Actor, repo, locator, ref string) (store.Node, error) {
	n, err := ResolveMatter(ctx, s.View, repo, locator)
	if err != nil {
		return store.Node{}, err
	}

	req := store.Request{Actor: actor, Env: store.Env{Repo: repo}}
	if _, err := s.Commit(ctx, req, func(_ context.Context, _ *store.Tx) ([]store.Draft, error) {
		return []store.Draft{{
			Type:    store.TypeReferenceAdded,
			Subject: n.ID,
			Payload: store.ReferenceAdded{Ref: ref},
		}}, nil
	}); err != nil {
		return store.Node{}, err
	}
	return s.Node(ctx, n.ID)
}

// Unbind removes one reference from a Matter's active set.
func Unbind(ctx context.Context, s *store.Store, actor store.Actor, repo, locator, ref string) (store.Node, error) {
	n, err := ResolveMatter(ctx, s.View, repo, locator)
	if err != nil {
		return store.Node{}, err
	}
	req := store.Request{Actor: actor, Env: store.Env{Repo: repo}}
	if _, err := s.Commit(ctx, req, func(context.Context, *store.Tx) ([]store.Draft, error) {
		return []store.Draft{{Type: store.TypeReferenceRemoved, Subject: n.ID, Payload: store.ReferenceRemoved{Ref: ref}}}, nil
	}); err != nil {
		return store.Node{}, err
	}
	return s.Node(ctx, n.ID)
}

// Rebind atomically replaces one reference in a Matter's active set.
func Rebind(ctx context.Context, s *store.Store, actor store.Actor, repo, locator, from, to string) (store.Node, error) {
	n, err := ResolveMatter(ctx, s.View, repo, locator)
	if err != nil {
		return store.Node{}, err
	}
	req := store.Request{Actor: actor, Env: store.Env{Repo: repo}}
	if _, err := s.Commit(ctx, req, func(context.Context, *store.Tx) ([]store.Draft, error) {
		return []store.Draft{{Type: store.TypeReferenceRebound, Subject: n.ID, Payload: store.ReferenceRebound{From: from, To: to}}}, nil
	}); err != nil {
		return store.Node{}, err
	}
	return s.Node(ctx, n.ID)
}
