package writesurface

import (
	"context"
	"fmt"

	"github.com/procrastivity/wip/internal/runlock"
	"github.com/procrastivity/wip/internal/store"
	"github.com/procrastivity/wip/internal/wiperr"
)

// StandDownRun closes an interrupted open Run and reaps all of its open
// Dispatch claims in one event transaction. The advisory lock is held only
// across the read/commit bracket and is released before this function returns.
func StandDownRun(ctx context.Context, s *store.Store, actor store.Actor, env store.Env, runID string) (store.Run, []store.Event, error) {
	if !store.IsIdentityShaped(runID) {
		return store.Run{}, nil, wiperr.New("validation.invalid-run-identity", fmt.Sprintf("Run %q must be a full ULID", runID))
	}
	r, err := s.Run(ctx, runID)
	if err != nil {
		return store.Run{}, nil, wiperr.New("validation.unknown-run", fmt.Sprintf("no Run %s", runID))
	}
	if !r.Open {
		return store.Run{}, nil, wiperr.New("refusal.run-closed", fmt.Sprintf("Run %s is closed", runID))
	}
	lock, err := runlock.Acquire(runID)
	if err != nil {
		if err == runlock.ErrHeld {
			return store.Run{}, nil, wiperr.New("refusal.run-live", fmt.Sprintf("Run %s is live", runID))
		}
		if le, ok := err.(*runlock.Error); ok {
			return store.Run{}, nil, wiperr.New("refusal.run-liveness-unknown", fmt.Sprintf("cannot stand down Run %s: %s", runID, le.Cause))
		}
		return store.Run{}, nil, wiperr.New("refusal.run-liveness-unknown", fmt.Sprintf("cannot stand down Run %s: %v", runID, err))
	}
	defer func() { _ = lock.Release() }()

	req := store.Request{Actor: actor, Env: env}
	events, err := s.Commit(ctx, req, func(ctx context.Context, tx *store.Tx) ([]store.Draft, error) {
		fresh, err := tx.Run(ctx, runID)
		if err != nil {
			return nil, wiperr.New("validation.unknown-run", fmt.Sprintf("no Run %s", runID))
		}
		if !fresh.Open {
			return nil, wiperr.New("refusal.run-closed", fmt.Sprintf("Run %s is closed", runID))
		}
		dispatches, err := tx.OpenDispatchesForRun(ctx, runID)
		if err != nil {
			return nil, err
		}
		drafts := []store.Draft{{
			Type:    store.TypeRunStoodDown,
			Subject: runID,
			Payload: store.RunStoodDown{ActingClone: env.Clone, OwningClone: fresh.Clone},
		}}
		for _, dispatch := range dispatches {
			clone, err := tx.Clone(ctx, dispatch.Clone)
			if err != nil {
				return nil, err
			}
			dispatchEnv := &store.Env{Repo: clone.Repo, Clone: dispatch.Clone, Worktree: dispatch.Worktree}
			drafts = append(drafts, store.Draft{
				Type:    store.TypeDispatchClosed,
				Subject: dispatch.ID,
				Payload: store.DispatchClosed{Reason: store.CloseReaped},
				Env:     dispatchEnv,
				Cause:   0,
			})
		}
		return drafts, nil
	})
	if err != nil {
		return store.Run{}, nil, err
	}
	closed, err := s.Run(ctx, runID)
	if err != nil {
		return store.Run{}, nil, err
	}
	return closed, events, nil
}
