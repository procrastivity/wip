package writesurface

import (
	"context"

	"github.com/procrastivity/wip/internal/store"
)

// SealTransition is the result of a write that can seal a Matter.
// BecameSealed is true only when the write crossed the Matter's seal boundary.
type SealTransition struct {
	Node         store.Node
	BecameSealed bool
}

// sealSweepDraft decides the anonymous-Batch consequence after the caller has
// established that its event crosses the Matter's seal boundary.
func sealSweepDraft(ctx context.Context, tx *store.Tx, matterID string) (store.Draft, bool, error) {
	batch, found, err := tx.AnonymousBatchForMatter(ctx, matterID)
	if err != nil || !found || batch.State != "live" {
		return store.Draft{}, false, err
	}
	return store.Draft{Type: store.TypeBatchSwept, Subject: batch.ID, Payload: store.BatchSweptPayload{}}, true, nil
}
