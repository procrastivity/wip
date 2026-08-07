package writesurface

import (
	"context"
	"fmt"
	"strings"

	"github.com/procrastivity/wip/internal/store"
	"github.com/procrastivity/wip/internal/wiperr"
)

// ResolveBatch accepts a full Batch ULID or an exact live named-Batch name.
// Partial identities and closed names are never resolved.
func ResolveBatch(ctx context.Context, v store.View, locator string) (store.Batch, error) {
	if store.IsIdentityShaped(locator) {
		b, err := v.Batch(ctx, locator)
		if err != nil {
			return store.Batch{}, wiperr.New("validation.unknown-batch", fmt.Sprintf("no Batch %s", locator))
		}
		return b, nil
	}
	b, err := v.BatchByName(ctx, locator)
	if err != nil {
		return store.Batch{}, wiperr.New("validation.unknown-batch", fmt.Sprintf("no live named Batch %q", locator))
	}
	return b, nil
}

func CreateBatch(ctx context.Context, s *store.Store, actor store.Actor, env store.Env, name string) (store.Batch, error) {
	if strings.TrimSpace(name) == "" {
		return store.Batch{}, wiperr.New("validation.empty-batch-name", "a named Batch needs a non-empty name")
	}
	req := store.Request{Actor: actor, Env: env}
	var id string
	if _, err := s.Commit(ctx, req, func(ctx context.Context, tx *store.Tx) ([]store.Draft, error) {
		if exists, err := tx.BatchNameExists(ctx, name); err != nil {
			return nil, err
		} else if exists {
			return nil, wiperr.New("refusal.batch-name-collision", fmt.Sprintf("a Batch named %q already exists", name))
		}
		id = tx.NewID()
		return []store.Draft{{Type: store.TypeBatchCreated, Subject: id, Payload: store.BatchCreated{Name: name}}}, nil
	}); err != nil {
		return store.Batch{}, err
	}
	return s.Batch(ctx, id)
}

// CreateAnonymousBatch is the scheduler-facing anonymous creation path. The
// caller supplies the resolved Matter identity; no user-facing name exists.
func CreateAnonymousBatch(ctx context.Context, s *store.Store, actor store.Actor, env store.Env, matter string) (store.Batch, error) {
	req := store.Request{Actor: actor, Env: env}
	var id string
	if _, err := s.Commit(ctx, req, func(ctx context.Context, tx *store.Tx) ([]store.Draft, error) {
		n, err := tx.Node(ctx, matter)
		if err != nil || n.Kind != store.ScaleMatter {
			return nil, wiperr.New("validation.not-a-matter", fmt.Sprintf("%s is not a live Matter", matter))
		}
		if existing, ok, err := tx.AnonymousBatchForMatter(ctx, matter); err != nil {
			return nil, err
		} else if ok {
			return nil, wiperr.New("refusal.anonymous-batch-exists", fmt.Sprintf("Matter %s already has anonymous Batch %s", matter, existing.ID))
		}
		id = tx.NewID()
		return []store.Draft{{Type: store.TypeBatchCreated, Subject: id, Payload: store.BatchCreated{Matter: matter}}}, nil
	}); err != nil {
		return store.Batch{}, err
	}
	return s.Batch(ctx, id)
}

func JoinBatch(ctx context.Context, s *store.Store, actor store.Actor, env store.Env, batchLocator, matterLocator string) (store.Batch, string, error) {
	repo := env.Repo
	batch, err := ResolveBatch(ctx, s.View, batchLocator)
	if err != nil {
		return store.Batch{}, "", err
	}
	matter, err := ResolveMatter(ctx, s.View, repo, matterLocator)
	if err != nil {
		return store.Batch{}, "", err
	}
	req := store.Request{Actor: actor, Env: env}
	if _, err := s.Commit(ctx, req, func(ctx context.Context, tx *store.Tx) ([]store.Draft, error) {
		fresh, err := tx.Batch(ctx, batch.ID)
		if err != nil {
			return nil, err
		}
		if fresh.State != "live" {
			return nil, wiperr.New("refusal.batch-closed", fmt.Sprintf("Batch %s is closed", batchLocator))
		}
		if fresh.Matter != "" {
			return nil, wiperr.New("refusal.anonymous-batch-membership", "anonymous Batch membership is scheduler-owned")
		}
		return []store.Draft{{Type: store.TypeBatchJoined, Subject: fresh.ID, Payload: store.BatchMembership{Matter: matter.ID}}}, nil
	}); err != nil {
		return store.Batch{}, "", err
	}
	result, err := s.Batch(ctx, batch.ID)
	return result, matter.ID, err
}

func LeaveBatch(ctx context.Context, s *store.Store, actor store.Actor, env store.Env, batchLocator, matterLocator string) (store.Batch, string, error) {
	repo := env.Repo
	batch, err := ResolveBatch(ctx, s.View, batchLocator)
	if err != nil {
		return store.Batch{}, "", err
	}
	matter, err := ResolveMatter(ctx, s.View, repo, matterLocator)
	if err != nil {
		return store.Batch{}, "", err
	}
	req := store.Request{Actor: actor, Env: env}
	if _, err := s.Commit(ctx, req, func(ctx context.Context, tx *store.Tx) ([]store.Draft, error) {
		fresh, err := tx.Batch(ctx, batch.ID)
		if err != nil {
			return nil, err
		}
		if fresh.State != "live" {
			return nil, wiperr.New("refusal.batch-closed", fmt.Sprintf("Batch %s is closed", batchLocator))
		}
		if fresh.Matter != "" {
			return nil, wiperr.New("refusal.anonymous-batch-membership", "an anonymous Batch membership cannot be removed")
		}
		member, err := tx.BatchMember(ctx, fresh.ID, matter.ID)
		if err != nil {
			return nil, err
		}
		if !member {
			return nil, wiperr.New("refusal.batch-not-member", fmt.Sprintf("Matter %s is not a member of Batch %s", matterLocator, batchLocator))
		}
		return []store.Draft{{Type: store.TypeBatchLeft, Subject: fresh.ID, Payload: store.BatchMembership{Matter: matter.ID}}}, nil
	}); err != nil {
		return store.Batch{}, "", err
	}
	result, err := s.Batch(ctx, batch.ID)
	return result, matter.ID, err
}

func DismissBatch(ctx context.Context, s *store.Store, actor store.Actor, env store.Env, locator string) (store.Batch, error) {
	batch, err := ResolveBatch(ctx, s.View, locator)
	if err != nil {
		return store.Batch{}, err
	}
	if batch.Matter != "" {
		return store.Batch{}, wiperr.New("refusal.anonymous-batch-dismissal", "an anonymous Batch is swept when its Matter seals")
	}
	req := store.Request{Actor: actor, Env: env}
	if _, err := s.Commit(ctx, req, func(ctx context.Context, tx *store.Tx) ([]store.Draft, error) {
		fresh, err := tx.Batch(ctx, batch.ID)
		if err != nil {
			return nil, err
		}
		if fresh.State != "live" {
			return nil, wiperr.New("refusal.batch-closed", fmt.Sprintf("Batch %s is closed", locator))
		}
		if fresh.Matter != "" {
			return nil, wiperr.New("refusal.anonymous-batch-dismissal", "an anonymous Batch is swept when its Matter seals")
		}
		return []store.Draft{{Type: store.TypeBatchDismissed, Subject: fresh.ID, Payload: store.BatchDismissedPayload{}}}, nil
	}); err != nil {
		return store.Batch{}, err
	}
	return s.Batch(ctx, batch.ID)
}
