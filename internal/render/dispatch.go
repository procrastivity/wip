package render

import (
	"context"
	"os"
	"time"

	"github.com/procrastivity/wip/internal/store"
	"github.com/procrastivity/wip/internal/wiperr"
)

// DefaultStaleBound is the staleness window past which an open dispatch is no
// longer reused: a `wip refresh` that finds one this old supersedes it rather
// than treating it as still live (step-02/step-07, D59's resolved "closed"
// call, path (b)), and `wip clean` reaps one this old as a crash orphan
// (path (c)) rather than leaving it parked indefinitely. One value serves
// both, and the blob-orphan reap in `wip clean` (D68) reuses it too — a
// single documented bound rather than three independent guesses.
const DefaultStaleBound = 24 * time.Hour

// OpenOrReuse implements step-02's dual-purpose `wip refresh`: mint a fresh
// dispatch when this worktree has none open, or when the one it has has gone
// stale (supersede it first, D59 path (b)); otherwise reuse the open one
// without minting a new id.
//
// Opened reports whether this call minted a new dispatch. Superseded is the
// id of a stale dispatch this call closed first, or empty when there was
// none.
func OpenOrReuse(ctx context.Context, s *store.Store, cur Current, actor store.Actor, now time.Time, staleBound time.Duration) (dispatch store.Dispatch, opened bool, superseded string, err error) {
	existing, found, err := s.OpenDispatch(ctx, cur.Worktree.ID)
	if err != nil {
		return store.Dispatch{}, false, "", err
	}
	if found {
		if now.Sub(existing.OpenedAt) < staleBound {
			if err := os.MkdirAll(ScratchDir(cur.Root, existing.ID), 0o755); err != nil {
				return store.Dispatch{}, false, "", err
			}
			return existing, false, "", nil
		}
		if err := closeAndSweep(ctx, s, cur, actor, existing.ID, store.CloseSuperseded); err != nil {
			return store.Dispatch{}, false, "", err
		}
		superseded = existing.ID
	}

	d, err := openNewDispatch(ctx, s, cur, actor)
	if err != nil {
		return store.Dispatch{}, false, "", err
	}
	if err := os.MkdirAll(ScratchDir(cur.Root, d.ID), 0o755); err != nil {
		return store.Dispatch{}, false, "", err
	}
	return d, true, superseded, nil
}

// openNewDispatch opens the P1 dispatch bracket. Batch creation belongs to the
// later scheduler path; P1 dispatch rows remain valid with null Run and Matter.
func openNewDispatch(ctx context.Context, s *store.Store, cur Current, actor store.Actor) (store.Dispatch, error) {
	req := store.Request{Actor: actor, Env: cur.Env()}
	var dispatchID string
	if _, err := s.Commit(ctx, req, func(_ context.Context, tx *store.Tx) ([]store.Draft, error) {
		dispatchID = tx.NewID()
		return []store.Draft{{Type: store.TypeDispatchOpened, Subject: dispatchID}}, nil
	}); err != nil {
		return store.Dispatch{}, err
	}
	return s.Dispatch(ctx, dispatchID)
}

// CloseExplicit implements D59's path (a): the explicit `wip dispatch close`
// verb, `reason = completed` — the agent-porcelain contract's last act
// (agent-path).
func CloseExplicit(ctx context.Context, s *store.Store, cur Current, actor store.Actor) (store.Dispatch, error) {
	existing, found, err := s.OpenDispatch(ctx, cur.Worktree.ID)
	if err != nil {
		return store.Dispatch{}, err
	}
	if !found {
		return store.Dispatch{}, wiperr.New("validation.no-open-dispatch",
			"no open dispatch on this worktree; run `wip refresh` first")
	}
	if err := closeAndSweep(ctx, s, cur, actor, existing.ID, store.CloseCompleted); err != nil {
		return store.Dispatch{}, err
	}
	return s.Dispatch(ctx, existing.ID)
}

// closeAndSweep emits dispatch.closed and removes the dispatch's scratch
// directory — deleted, not just marked closed, since nothing durable may
// live there (D45, step-07).
func closeAndSweep(ctx context.Context, s *store.Store, cur Current, actor store.Actor, dispatchID string, reason store.CloseReason) error {
	req := store.Request{Actor: actor, Env: cur.Env()}
	if _, err := s.Commit(ctx, req, func(_ context.Context, _ *store.Tx) ([]store.Draft, error) {
		return []store.Draft{{
			Type:    store.TypeDispatchClosed,
			Subject: dispatchID,
			Payload: store.DispatchClosed{Reason: reason},
		}}, nil
	}); err != nil {
		return err
	}
	return sweepScratch(cur.Root, dispatchID)
}

// sweepScratch removes a dispatch's scratch directory. Removing an
// already-absent directory is not an error — sweeping is idempotent, the way
// every disposal in this package is.
func sweepScratch(root, dispatchID string) error {
	if err := os.RemoveAll(ScratchDir(root, dispatchID)); err != nil {
		return err
	}
	return nil
}
