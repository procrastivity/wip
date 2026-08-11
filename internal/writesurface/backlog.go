package writesurface

// Stage birth-and-amendment, step-05: backlog intake. `wip backlog add`
// emits `backlog.entered`; `wip backlog plan` emits `backlog.planned`; `wip
// backlog decline` emits `backlog.declined` — the P1 decline exit, which
// must stay distinguishable from not-yet-acted-upon (MODEL §4). `wip backlog
// list` is read-only and emits no event; it is a straight store read and has
// no business-logic function here.

import (
	"context"
	"fmt"

	"github.com/procrastivity/wip/internal/store"
	"github.com/procrastivity/wip/internal/wiperr"
)

// BacklogAdd enters a new entry. `deferred` provenance records origin node +
// why (MODEL §4), so origin is required for it; `found`'s origin is optional
// evidence, and `intake` carries none.
func BacklogAdd(ctx context.Context, s *store.Store, actor store.Actor, repo string, provenance store.Provenance, title, detail, originLocator string) (store.BacklogEntry, error) {
	switch provenance {
	case store.ProvenanceIntake, store.ProvenanceFound, store.ProvenanceDeferred:
	default:
		return store.BacklogEntry{}, wiperr.New("validation.invalid-provenance", fmt.Sprintf("%q is not intake, found or deferred", provenance))
	}
	if title == "" {
		return store.BacklogEntry{}, wiperr.New("validation.missing-title", "a backlog entry needs a title")
	}
	var originNode string
	if originLocator != "" {
		n, err := ResolveNode(ctx, s.View, repo, originLocator)
		if err != nil {
			return store.BacklogEntry{}, err
		}
		originNode = n.ID
	} else if provenance == store.ProvenanceDeferred {
		return store.BacklogEntry{}, wiperr.New("validation.missing-origin", "a deferred entry must name the origin node it was pushed out of (--origin)")
	}

	req := store.Request{Actor: actor, Env: store.Env{Repo: repo}}
	var id string
	if _, err := s.Commit(ctx, req, func(_ context.Context, tx *store.Tx) ([]store.Draft, error) {
		id = tx.NewID()
		return []store.Draft{{
			Type:    store.TypeBacklogEntered,
			Subject: id,
			Payload: store.BacklogEntered{Provenance: provenance, Title: title, Detail: detail, OriginNode: originNode},
		}}, nil
	}); err != nil {
		return store.BacklogEntry{}, err
	}
	return backlogEntryByID(ctx, s, repo, id)
}

// BacklogPlan promotes an entry into a Matter — the exit that consumes it.
func BacklogPlan(ctx context.Context, s *store.Store, actor store.Actor, repo, entryID, matterLocator string) (store.BacklogEntry, error) {
	matter, err := ResolveMatter(ctx, s.View, repo, matterLocator)
	if err != nil {
		return store.BacklogEntry{}, err
	}

	req := store.Request{Actor: actor, Env: store.Env{Repo: repo}}
	if _, err := s.Commit(ctx, req, func(_ context.Context, _ *store.Tx) ([]store.Draft, error) {
		return []store.Draft{{
			Type:    store.TypeBacklogPlanned,
			Subject: entryID,
			Payload: store.BacklogPlanned{Matter: matter.ID},
		}}, nil
	}); err != nil {
		return store.BacklogEntry{}, err
	}
	return backlogEntryByID(ctx, s, repo, entryID)
}

// BacklogDecline is the P1 decline exit — it must carry a reason, or a
// declined entry could not be told apart from one nobody has acted on yet
// (MODEL §4).
func BacklogDecline(ctx context.Context, s *store.Store, actor store.Actor, repo, entryID, reason string) (store.BacklogEntry, error) {
	if reason == "" {
		return store.BacklogEntry{}, wiperr.New("validation.missing-reason", "declining a backlog entry needs a reason")
	}

	req := store.Request{Actor: actor, Env: store.Env{Repo: repo}}
	if _, err := s.Commit(ctx, req, func(_ context.Context, _ *store.Tx) ([]store.Draft, error) {
		return []store.Draft{{
			Type:    store.TypeBacklogDeclined,
			Subject: entryID,
			Payload: store.BacklogDeclined{Reason: reason},
		}}, nil
	}); err != nil {
		return store.BacklogEntry{}, err
	}
	return backlogEntryByID(ctx, s, repo, entryID)
}

// BacklogDelegate moves one entered item to provider-neutral delivery. The
// entry identity is the stable idempotency boundary across all later retries.
func BacklogDelegate(ctx context.Context, s *store.Store, actor store.Actor, repo, entryID string) (store.BacklogEntry, error) {
	req := store.Request{Actor: actor, Env: store.Env{Repo: repo}}
	if _, err := s.Commit(ctx, req, func(_ context.Context, tx *store.Tx) ([]store.Draft, error) {
		return []store.Draft{{
			Type:    store.TypeBacklogDelegated,
			Subject: entryID,
			Payload: store.BacklogDelegated{Outbox: tx.NewID(), IdempotencyKey: "backlog:" + entryID},
		}}, nil
	}); err != nil {
		return store.BacklogEntry{}, err
	}
	return backlogEntryByID(ctx, s, repo, entryID)
}

// ConfirmBacklogDelegation records the provider seam's successful creation
// response. Provider adapters call this only after they have an unambiguous ref.
func ConfirmBacklogDelegation(ctx context.Context, s *store.Store, actor store.Actor, repo, outbox, ref string) error {
	if ref == "" {
		return wiperr.New("validation.missing-reference", "a creation confirmation needs a reference")
	}
	req := store.Request{Actor: actor, Env: store.Env{Repo: repo}}
	_, err := s.Commit(ctx, req, func(context.Context, *store.Tx) ([]store.Draft, error) {
		return []store.Draft{{
			Type: store.TypeTrackerItemCreated, Subject: outbox,
			Payload: store.TrackerItemCreated{Ref: ref},
		}}, nil
	})
	return err
}

func backlogEntryByID(ctx context.Context, s *store.Store, repo, id string) (store.BacklogEntry, error) {
	entries, err := s.Backlog(ctx, repo)
	if err != nil {
		return store.BacklogEntry{}, err
	}
	for _, e := range entries {
		if e.ID == id {
			return e, nil
		}
	}
	return store.BacklogEntry{}, fmt.Errorf("writesurface: no backlog entry %s in %s", id, repo)
}
