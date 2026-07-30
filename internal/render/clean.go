package render

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/procrastivity/wip/internal/store"
)

// CleanResult reports what `wip clean` reaped (step-07/step-12).
type CleanResult struct {
	// ReapedDispatches is every dispatch this call closed with reason
	// `reaped` — a crash orphan: it was still open past staleBound with no
	// subsequent `wip refresh` ever superseding it.
	ReapedDispatches []string
	// SweptDirectories is every `.wip/work/<dispatch-id>/` directory this
	// call removed — including plain disk/store desync leftovers (a dispatch
	// already closed whose sweep did not finish) alongside the reaped ones.
	SweptDirectories []string
	// ReapedBlobs is every orphaned blob (D68) this call removed from the
	// store's sidecar directory.
	ReapedBlobs []string
}

// Clean implements step-07's `wip clean`: it scans this worktree's own
// `.wip/work/` for scratch directories whose dispatch is not open (a leftover
// from a close whose sweep did not finish — pure disk/store desync, reaped
// immediately, no new event) or is open but stale past staleBound (the crash
// case PLAN 1.4 names: a process died before any subsequent `wip refresh`
// could supersede it) — closed here with reason `reaped` and swept. It also
// reaps orphaned blobs (D68) past the same bound.
func Clean(ctx context.Context, s *store.Store, cur Current, actor store.Actor, now time.Time, staleBound time.Duration) (CleanResult, error) {
	var result CleanResult

	entries, err := os.ReadDir(WorkDir(cur.Root))
	if err != nil {
		if !os.IsNotExist(err) {
			return CleanResult{}, fmt.Errorf("render: reading %s: %w", WorkDir(cur.Root), err)
		}
		entries = nil
	}

	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		id := entry.Name()
		dispatch, err := s.Dispatch(ctx, id)
		if err != nil {
			// A directory named after no known dispatch is not a state
			// `.wip/work/` is ever read as (D45) — it is disposable debris,
			// same as anything else found here.
			if err := os.RemoveAll(filepath.Join(WorkDir(cur.Root), id)); err != nil {
				return CleanResult{}, err
			}
			result.SweptDirectories = append(result.SweptDirectories, id)
			continue
		}

		if !dispatch.Open {
			if err := os.RemoveAll(filepath.Join(WorkDir(cur.Root), id)); err != nil {
				return CleanResult{}, err
			}
			result.SweptDirectories = append(result.SweptDirectories, id)
			continue
		}

		if now.Sub(dispatch.OpenedAt) < staleBound {
			continue // genuinely still open and not yet stale — leave it
		}

		if err := closeAndSweep(ctx, s, cur, actor, id, store.CloseReaped); err != nil {
			return CleanResult{}, err
		}
		result.ReapedDispatches = append(result.ReapedDispatches, id)
		result.SweptDirectories = append(result.SweptDirectories, id)
	}

	cutoff := now.Add(-staleBound)
	blobs, err := s.ReapOrphanBlobs(ctx, cutoff)
	if err != nil {
		return CleanResult{}, err
	}
	result.ReapedBlobs = blobs

	return result, nil
}
