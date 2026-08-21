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

// matterSealedProspectively evaluates a Matter's seal predicate from the
// transaction's pre-event state. willFinish and closingGate supply the parts
// of the event being decided that have not projected yet.
func matterSealedProspectively(ctx context.Context, tx *store.Tx, matterID string, willFinish bool, closingGate string) (bool, error) {
	matter, err := tx.Node(ctx, matterID)
	if err != nil || matter.Kind != store.ScaleMatter {
		return false, err
	}
	if !willFinish && matter.Lifecycle != store.Done {
		return false, nil
	}
	declarations, err := tx.GateDeclarations(ctx, matter.Repo)
	if err != nil {
		return false, err
	}
	for _, declaration := range declarations {
		if declaration.Scale != store.ScaleMatter || declaration.Gate == closingGate {
			continue
		}
		satisfied, err := tx.GateSatisfied(ctx, matter.Repo, matter.ID, declaration.Gate)
		if err != nil {
			return false, err
		}
		if !satisfied {
			return false, nil
		}
	}
	return true, nil
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
