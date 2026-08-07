package writesurface

import (
	"context"

	"github.com/procrastivity/wip/internal/store"
)

// sealSweepDraft decides the seal consequence inside the same transaction as
// the lifecycle or gate event that can make a Matter sealed. closingGate is
// included because the gate event has not projected yet when decide runs.
func sealSweepDraft(ctx context.Context, tx *store.Tx, matterID string, willFinish bool, closingGate string) (store.Draft, bool, error) {
	matter, err := tx.Node(ctx, matterID)
	if err != nil || matter.Kind != store.ScaleMatter {
		return store.Draft{}, false, err
	}
	if !willFinish && matter.Lifecycle != store.Done {
		return store.Draft{}, false, nil
	}
	declarations, err := tx.GateDeclarations(ctx, matter.Repo)
	if err != nil {
		return store.Draft{}, false, err
	}
	closed, err := tx.ClosedGates(ctx, matter.ID)
	if err != nil {
		return store.Draft{}, false, err
	}
	closedNames := make(map[string]bool, len(closed)+1)
	for _, gate := range closed {
		closedNames[gate.Gate] = true
	}
	if closingGate != "" {
		closedNames[closingGate] = true
	}
	for _, declaration := range declarations {
		if declaration.Scale == store.ScaleMatter && !closedNames[declaration.Gate] {
			return store.Draft{}, false, nil
		}
	}
	batch, found, err := tx.AnonymousBatchForMatter(ctx, matter.ID)
	if err != nil || !found || batch.State != "live" {
		return store.Draft{}, false, err
	}
	return store.Draft{Type: store.TypeBatchSwept, Subject: batch.ID, Payload: store.BatchSweptPayload{}}, true, nil
}
