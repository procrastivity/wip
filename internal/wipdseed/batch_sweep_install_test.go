package wipdseed

import (
	"context"
	"crypto/ed25519"
	"fmt"
	"path/filepath"
	"reflect"
	"testing"
	"time"

	"github.com/procrastivity/wip/internal/authoritystore"
	"github.com/procrastivity/wip/internal/operation"
	"github.com/procrastivity/wip/internal/wipdjournal"
	"github.com/procrastivity/wip/internal/wipdwire"
)

func TestBatchSweepD55AuthoritySeedPullAndInstall(t *testing.T) {
	for _, openGate := range []bool{false, true} {
		t.Run(fmt.Sprintf("open-own-gate=%t", openGate), func(t *testing.T) {
			ctx := context.Background()
			fixture := newClientFixture(t)
			key := registerClientFixtureArtifactKey(t, fixture)
			sign := func(_ context.Context, message []byte) ([]byte, error) { return ed25519.Sign(key, message), nil }
			directory := t.TempDir()
			state := enrollFixtureClient(t, fixture, directory)
			peer := peerStateFromClient(t, state)
			const stage, grant, snapshot, journalID, clone = "00000000000000000000000049", "00000000000000000000000050", "00000000000000000000000051", "00000000000000000000000052", "01KZ7XHAQT1S46NYPN1PW1DYA6"
			id := func(n int) string { return fmt.Sprintf("%026d", n) }
			createFixtureMatter(t, fixture.store, peer, key, state, id(400), sweepFoldMatter, m6PullEventID(10), "D55 target", "d55-target", 1)
			state, err := PullAndInstall(ctx, fixture.profile, fixture.roots, directory)
			if err != nil {
				t.Fatal(err)
			}
			completeFixtureBirthRelease(t, fixture, peer, state, sweepFoldMatter, id(401), m6PullEventID(11), key)
			completeM6ClaimForPull(t, fixture, state, peer, key, sweepFoldMatter, sweepFoldClaim, sweepFoldBatch,
				grant, snapshot, journalID, sweepFoldWorktree, sweepFoldDispatch, 3, 12)
			claim := &operation.ClaimContext{ID: sweepFoldClaim, Epoch: "1"}
			sequence, journalPosition, eventNumber := uint64(4), uint64(1), 15
			complete := func(def operation.Definition, input operation.Input, subject string, output operation.Output) {
				t.Helper()
				command := m6PullCommand(state, id(400+int(sequence)), sequence, clone, sweepFoldWorktree, claim, def, input)
				owner := submitM6PullCommand(t, fixture.store, peer, command)
				var status authoritystore.CommandStatus
				if def.Metadata().Operation == operation.MatterStartV1.Metadata().Operation || def.Metadata().Operation == operation.MatterFinishV1.Metadata().Operation {
					status, err = fixture.store.CompleteConnectedLifecycle(ctx, owner, []string{m6PullEventID(eventNumber)}, time.Now().UTC(), sign)
				} else {
					status, err = fixture.store.CompleteCommand(ctx, owner, operation.Result{Code: operation.ResultSucceeded, Output: output}, subject, m6PullEventID(eventNumber), time.Now().UTC(), sign)
				}
				if err != nil {
					t.Fatalf("complete %s: %v", def.Metadata().Operation, err)
				}
				if def.Metadata().Delivery == operation.DeliveryClaim {
					installAndAcknowledgeM6Claim(t, fixture, state, directory, journalID, journalPosition, status.Receipt)
					journalPosition++
				}
				sequence++
				eventNumber++
			}
			complete(operation.MatterStartV1, operation.NodeLifecycleInput{NodeID: sweepFoldMatter}, sweepFoldMatter, nil)
			complete(operation.StageCreateV1, operation.StageCreateInput{MatterID: sweepFoldMatter, Title: "Unfinished Stage"}, stage,
				operation.StageCreateOutput{ID: stage, MatterID: sweepFoldMatter, Locator: "unfinished-stage", Title: "Unfinished Stage", SortKey: 1000, State: "planned"})
			// Seed ordinary gates through existing store APIs, not runtime wiring.
			complete(operation.GateDeclareV1, operation.GateDeclareInput{Gate: "own-review", Scale: "matter"}, testRepoID, nil)
			complete(operation.GateDeclareV1, operation.GateDeclareInput{Gate: "child-review", Scale: "stage"}, testRepoID, nil)
			if !openGate {
				complete(operation.GateCloseV1, operation.GateCloseInput{Gate: "own-review", NodeID: sweepFoldMatter}, sweepFoldMatter, nil)
			}
			complete(operation.MatterFinishV1, operation.MatterFinishInput{MatterID: sweepFoldMatter}, sweepFoldMatter, nil)
			current, err := fixture.store.GetCurrentClaimJournal(ctx, testDomainID, 1, state.EnvironmentID, sweepFoldClaim, 1, sweepFoldMatter, sweepFoldDispatch)
			if err != nil {
				t.Fatal(err)
			}
			barrierDigest, count, err := fixture.store.SealOwnedClaimJournal(ctx, current)
			if err != nil {
				t.Fatal(err)
			}
			closeID := id(430)
			raw, err := wipdwire.EncodeCanonical(map[string]any{
				"schema": "wipd.command/1", "command_id": closeID,
				"authority":   map[string]any{"domain_id": testDomainID, "expected_epoch": uint64(1)},
				"environment": map[string]any{"id": state.EnvironmentID, "sequence": sequence},
				"acted_at":    time.Now().UTC().Format(time.RFC3339Nano), "actor": "human", "causation_command_id": nil, "correlation_command_id": closeID,
				"operation": map[string]any{"name": "claim.release", "version": uint64(1)},
				"context":   map[string]any{"repo_id": testRepoID, "clone_id": clone, "worktree_id": sweepFoldWorktree},
				"claim":     map[string]any{"id": sweepFoldClaim, "epoch": uint64(1)}, "blobs": []any{},
				"input": map[string]any{"barrier": map[string]any{
					"schema": "wipd.journal-barrier/1", "journal_id": journalID, "claim": map[string]any{"id": sweepFoldClaim, "epoch": uint64(1)},
					"entry_count": count, "last_position": count, "terminal_receipt_count": count, "entries_digest": barrierDigest,
					"sealed": true, "unresolved_count": uint64(0), "quarantined_count": uint64(0),
				}},
			})
			if err != nil {
				t.Fatal(err)
			}
			closeHash := testDigest(append([]byte("wipd/request-hash/v1\x00"), raw...))
			pending, err := fixture.store.SubmitClaimLifecycle(ctx, raw, closeHash, peer, time.Now().UTC(), nil)
			if err != nil || pending.Owner == nil {
				t.Fatalf("normal close admission: %+v %v", pending, err)
			}
			closed, err := fixture.store.CompleteClaimLifecycle(ctx, pending.Owner, "", []string{m6PullEventID(eventNumber), m6PullEventID(eventNumber + 1)}, time.Now().UTC(), sign)
			if err != nil {
				t.Fatal(err)
			}
			before, err := PullAndInstall(ctx, fixture.profile, fixture.roots, directory)
			if err != nil {
				t.Fatal(err)
			}
			command := m6PullCommand(before, id(431), sequence+1, "", "", nil, operation.BatchSweepAnonymousV1, operation.BatchSweepAnonymousInput{
				MatterID: sweepFoldMatter, BatchID: sweepFoldBatch, ClaimClose: operation.ClaimCloseReference{
					ClaimID: sweepFoldClaim, ClaimEpoch: 1, ReleaseCommandID: closeID, ReleaseRequestHash: closeHash, TerminalReceiptDigest: testDigest(closed.Receipt),
					InstalledPrefixAnchor: operation.ClaimClosePrefix{EventCount: before.Prefix.EventCount, EventID: before.Prefix.EventID, Digest: before.Prefix.Digest},
				},
			})
			hash, err := command.RequestHash()
			if err != nil {
				t.Fatal(err)
			}
			status, err := fixture.store.SweepAnonymousBatchWithDeadline(ctx, command, hash, peer, time.Now().UTC(), m6PullEventID(eventNumber+2), sign, time.Time{})
			if err != nil {
				t.Fatal(err)
			}
			receipt, err := wipdwire.DecodeCanonicalMap(status.Receipt, "schema", "domain_id", "authority_epoch", "identity_schema", "command_id", "request_hash", "operation", "environment", "result", "accepted_events")
			if err != nil {
				t.Fatal(err)
			}
			result := receipt["result"].(map[string]any)
			if openGate {
				if result["code"] != "result.refused" || result["problem_code"] != string(operation.ProblemBatchSweepNotEligible) || receipt["accepted_events"] != nil {
					t.Fatalf("open applicable gate sweep: %+v", receipt)
				}
			} else {
				output, decodeErr := wipdwire.DecodeCanonicalMap(result["output"].([]byte), "outcome")
				if result["code"] != "result.succeeded" || decodeErr != nil || output["outcome"] != "swept" {
					t.Fatalf("D55-satisfied sweep: %+v %v", receipt, decodeErr)
				}
			}
			transfer, _, err := VerifyPullTransfer(fixture.profile, before, before.Prefix, authenticatedPullFrames(t, fixture, before))
			if err != nil {
				t.Fatalf("authority-to-pull verification: %v", err)
			}
			installed, err := PullAndInstall(ctx, fixture.profile, fixture.roots, directory)
			if err != nil {
				t.Fatalf("authority-to-client install: %v", err)
			}
			seeded, err := EnrollAndSeed(ctx, fixture.profile, fixture.roots, fixture.ownerRoot, fixture.delegation, testRepoID, fixture.identity, fixture.grant, t.TempDir())
			if err != nil || !anchorEqual(seeded.Prefix, installed.Prefix) || projectedMatterState(t, seeded, sweepFoldMatter) != "done" {
				t.Fatalf("authority-to-seed install: prefix=%+v err=%v", seeded.Prefix, err)
			}
			if openGate {
				if !anchorEqual(installed.Prefix, before.Prefix) || !reflect.DeepEqual(installed.EventRecords, before.EventRecords) || len(transfer.Records()) != 0 {
					t.Fatal("refused sweep advanced or installed an event prefix")
				}
				return
			}
			if installed.Prefix.EventCount != before.Prefix.EventCount+1 || len(transfer.Records()) != 1 {
				t.Fatalf("first sweep did not install exactly one event: %+v", installed.Prefix)
			}
			journal, err := wipdjournal.Open(filepath.Join(t.TempDir(), "journal"), wipdjournal.Identity{
				RepoID: testRepoID, DomainID: testDomainID, AuthorityEpoch: 1, EnvironmentID: before.EnvironmentID, OwnerRootSPKI: before.OwnerKeyID,
			})
			if err != nil {
				t.Fatal(err)
			}
			defer func() { _ = journal.Close() }()
			manifest := wipdwire.BlobManifest{Schema: "wipd.blob-manifest/1", DomainID: testDomainID, Epoch: 1, AsOf: before.Prefix, Entries: before.ManifestEntries, Digest: before.ManifestDigest}
			prior, err := wipdjournal.VerifyTransfer(testDomainID, 1, emptyWireAnchor(), before.Prefix, before.EventRecords, manifest)
			if err != nil {
				t.Fatal(err)
			}
			base, err := journal.InstallPull(ctx, installSnapshotExpectation(t, journal), prior)
			if err != nil {
				t.Fatal(err)
			}
			final, err := journal.InstallPull(ctx, base.Expectation(), transfer)
			if err != nil || !anchorEqual(final.Anchor, installed.Prefix) {
				t.Fatalf("verified sweep journal install: %+v %v", final.Anchor, err)
			}
		})
	}
}
