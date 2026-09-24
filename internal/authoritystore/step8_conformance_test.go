package authoritystore

import (
	"context"
	"crypto/sha256"
	"errors"
	"testing"
)

func TestStep8ClosedClaimIsIncludedInVerifiedHandoffBundle(t *testing.T) {
	f := newClaimTestFixture(t)
	ctx := context.Background()
	installed := f.anchor(t)
	allocation := claimTestAllocation(91, installed, 101, 102, 103)
	_, acquireHash, acquired, _ := f.acquire(t, 11, 2, installed, allocation)
	claimTestReceipt(t, acquired, "result.succeeded", map[string]any{
		"claim":     map[string]any{"id": allocation.ClaimID, "epoch": uint64(1)},
		"matter_id": f.matter, "batch_id": allocation.BatchID, "dispatch_id": claimTestID(51),
	}, 101, 102, 103)
	claimTestEvent(t, f, 2, 101, 11, acquireHash, "batch.anonymous-created", allocation.BatchID, 2,
		map[string]any{"batch_id": allocation.BatchID, "matter_id": f.matter})
	claimTestEvent(t, f, 3, 102, 11, acquireHash, "claim.acquired", allocation.ClaimID, 2,
		map[string]any{
			"claim_id": allocation.ClaimID, "claim_epoch": uint64(1), "matter_id": f.matter,
			"batch_id": allocation.BatchID, "dispatch_id": claimTestID(51), "owner_environment_id": envA, "worktree_id": f.worktree,
		})
	claimTestEvent(t, f, 4, 103, 11, acquireHash, "dispatch.opened", claimTestID(51), 2,
		map[string]any{
			"dispatch_id": claimTestID(51), "matter_id": f.matter, "batch_id": allocation.BatchID,
			"claim_id": allocation.ClaimID, "worktree_id": f.worktree,
		})
	if err := f.s.SealClaimJournal(ctx, domainA, allocation.JournalID); err != nil {
		t.Fatalf("seal empty claim journal: %v", err)
	}
	root := sha256.Sum256([]byte("wipd/journal-barrier/v1\x00"))
	barrier := map[string]any{
		"schema": "wipd.journal-barrier/1", "journal_id": allocation.JournalID,
		"claim":       map[string]any{"id": allocation.ClaimID, "epoch": uint64(1)},
		"entry_count": uint64(0), "last_position": uint64(0), "terminal_receipt_count": uint64(0),
		"entries_digest": digestRawBytes(root[:]), "sealed": true, "unresolved_count": uint64(0), "quarantined_count": uint64(0),
	}
	release, releaseHash := f.command(t, 12, 3, "claim.release", map[string]any{"id": allocation.ClaimID, "epoch": uint64(1)}, map[string]any{"barrier": barrier})
	pending, err := f.s.SubmitClaimLifecycle(ctx, release, releaseHash, f.peer, f.now, nil)
	if err != nil || pending.Owner == nil {
		t.Fatalf("release submission: %+v %v", pending, err)
	}
	closed, err := f.s.CompleteClaimLifecycle(ctx, pending.Owner, "", []string{claimTestID(104), claimTestID(105)}, f.now, signWith(f.key))
	if err != nil {
		t.Fatalf("release completion: %v", err)
	}
	claimTestReceipt(t, closed, "result.succeeded", map[string]any{
		"claim_id": allocation.ClaimID, "claim_epoch": uint64(1), "dispatch_id": claimTestID(51), "barrier_digest": digestRawBytes(root[:]),
	}, 104, 105)
	claimTestEvent(t, f, 5, 104, 12, releaseHash, "dispatch.closed", claimTestID(51), 3,
		map[string]any{"dispatch_id": claimTestID(51), "claim_id": allocation.ClaimID, "claim_epoch": uint64(1)})
	claimTestEvent(t, f, 6, 105, 12, releaseHash, "claim.released", allocation.ClaimID, 3,
		map[string]any{"claim_id": allocation.ClaimID, "claim_epoch": uint64(1), "dispatch_id": claimTestID(51), "barrier_digest": digestRawBytes(root[:])})

	d, owner := identity(domainA, 7)
	destinationKey := key("step8-handoff-destination-artifact")
	certificate, keyID := destinationKeyFixture(t, owner, d, destinationKey, 8, f.now)
	binding := ContinuityBinding{digest('a'), "https://step8-destination.example", digest('b'), keyID}
	intent := continuityFixture(t, owner, d, f.now, binding, 51)
	products, err := f.s.QuiesceAndRelinquish(ctx, domainA, intent, binding, f.now, signWith(f.key))
	if err != nil {
		t.Fatalf("quiesce after terminal claim release: %v", err)
	}
	var bundle signedArtifact
	if err = artifactDecoder.Unmarshal(products.BundleManifest, &bundle); err != nil || bundle.Kind != "bundle-manifest" {
		t.Fatalf("bundle wrapper: %v", err)
	}
	var payload bundlePayload
	if err = closedPayload(bundle.Payload, &payload, "schema", "domain_id", "authority_epoch", "store_schema", "prefix", "blob_manifest_digest", "artifact_chain_head", "entries"); err != nil {
		t.Fatalf("bundle payload: %v", err)
	}
	if payload.Domain != domainA || payload.Epoch != 7 || payload.Prefix.Count != 6 {
		t.Fatalf("bundle identity/prefix: %+v", payload)
	}
	counts := make(map[string]uint64, len(payload.Entries))
	for _, entry := range payload.Entries {
		counts[entry.LogicalName] = entry.RecordCount
	}
	for table, want := range map[string]uint64{
		"terminal_receipts": 3, "authority_events": 6, "claims": 1,
		"claim_grants": 1, "claim_journals": 1,
	} {
		if counts[table] != want {
			t.Errorf("bundle %s record count = %d, want %d", table, counts[table], want)
		}
	}
	if err = f.s.Close(); err != nil {
		t.Fatal(err)
	}
	f.s, err = OpenExisting(f.root)
	if err != nil {
		t.Fatalf("reopen relinquished store with claim closure: %v", err)
	}
	if err = checkStep7Closure(f.s.db, f.root); err != nil {
		t.Fatalf("reopen bundle closure: %v", err)
	}
	final := handoffFinalFixture(t, owner, d, binding, intent, products, f.now, 52)
	activation, err := f.s.ActivateVerifiedBundle(ctx, domainA, final, certificate, binding, f.now, signWith(destinationKey))
	if err != nil || activation.Epoch != 8 || activation.Prefix.EventCount != 6 {
		t.Fatalf("activate verified cross-boundary bundle: %+v %v", activation, err)
	}
	if _, _, err = f.s.QueryClaimGrant(ctx, domainA, claimTestID(11), acquireHash, 7, f.peer, envA, f.now); !errors.Is(err, ErrFenced) {
		t.Fatalf("served old-epoch grant after activation: %v", err)
	}
	if err = f.s.Close(); err != nil {
		t.Fatal(err)
	}
	if reopened, openErr := OpenExisting(f.root); openErr != nil {
		t.Fatalf("reopen activated claim bundle: %v", openErr)
	} else if err = reopened.Close(); err != nil {
		t.Fatal(err)
	}
}
