package wipdseed

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/procrastivity/wip/internal/authoritystore"
	"github.com/procrastivity/wip/internal/operation"
	"github.com/procrastivity/wip/internal/wipdjournal"
	"github.com/procrastivity/wip/internal/wipdwire"
)

// Asymmetric states and shared membership distinguish domain-wide aggregation
// from a per-Repo/per-Matter cache, and rebind from a remove-then-add narration.
func step8TransferHistory(t *testing.T, level string) []wipdwire.EventRecord {
	t.Helper()
	records := postCloseSweepHistory(t, false)
	records = records[:len(records)-1] // both Matters exist; first is Done, second Planned
	next := 520
	add := func(kind, subject string, payload map[string]any) {
		records = append(records, step7ClientTestEvent(t, next, next+100, uint64(next), kind, subject, payload))
		next++
	}
	add("config.set", testRepoID, map[string]any{"key": "tracker.push-level", "value": level})
	add("reference.added", sweepFoldMatter, map[string]any{"ref": "SHARED", "tracker_push_level": level})
	add("reference.added", sweepFoldOther, map[string]any{"ref": "SHARED", "tracker_push_level": level})
	add("reference.rebound", sweepFoldMatter, map[string]any{"from": "SHARED", "to": "SOLO", "tracker_push_level": level})
	add("reference.removed", sweepFoldMatter, map[string]any{"ref": "SOLO", "tracker_push_level": level})
	// Later config cannot rewrite the snapshots or candidates above.
	add("config.set", testRepoID, map[string]any{"key": "tracker.push-level", "value": "off"})
	add("dependency.added", sweepFoldOther, map[string]any{"edge": "00000000000000000000000060", "blocker": sweepFoldMatter})
	add("dependency.removed", sweepFoldOther, map[string]any{"edge": "00000000000000000000000060", "blocker": sweepFoldMatter})
	return records
}

func TestStep8SeedPrefixPullParityAndCacheRefold(t *testing.T) {
	fixture := newClientFixture(t)
	for _, level := range []string{"off", "boundary", "narrated"} {
		t.Run(level, func(t *testing.T) {
			records := step8TransferHistory(t, level)
			var want ClientState
			for _, split := range []int{0, len(records) - 8, len(records) - 4, len(records) - 1} {
				kind := "pull"
				if split == 0 {
					kind = "seed"
				}
				frames, anchor := step7ClientRepairTransferFrames(t, kind, records, records[:split])
				got, err := verifyTransferFrames(frames, kind, fixture.profile, testRepoID, sweepFoldEnv, "sha256:"+strings.Repeat("a", 64), anchor, records[:split])
				if err != nil {
					t.Fatalf("split %d: %v", split, err)
				}
				if split == 0 {
					want = got
				} else if !reflect.DeepEqual(got, want) {
					t.Fatalf("seed/prefix pull differs at split %d", split)
				}
				if !reflect.DeepEqual(got.EventRecords, records) {
					t.Fatal("transfer rewrote canonical event bytes")
				}
			}
			p := want.Step8Projection
			if p == nil || len(p.References) != 3 || len(p.Aggregates) != 1 || p.Aggregates[0].Reference != "SHARED" || p.Aggregates[0].Members != 1 || p.Aggregates[0].Disposition != "" ||
				len(p.Dependencies) != 1 || p.Dependencies[0].Live || p.Dependencies[0].BlockedID != sweepFoldOther || p.Dependencies[0].BlockerID != sweepFoldMatter || p.Dependencies[0].TombstoneEventID != records[len(records)-1].EventID || len(p.ConfigHistory) != 2 {
				t.Fatalf("wrong retained graph/shared aggregation/config: %+v", p)
			}
			wantCount := 3
			if level == "off" {
				wantCount = 0
			}
			if len(p.Candidates) != wantCount {
				t.Fatalf("candidate count=%d want=%d: %+v", len(p.Candidates), wantCount, p.Candidates)
			}
			if level != "off" {
				// First bind emits Completed, shared bind Active; rebind emits only
				// Completed for SOLO. Final unbind must not emit a deletion candidate.
				byEvent := map[string]wipdjournal.TrackerCandidate{}
				for _, c := range p.Candidates {
					byEvent[c.EventID] = c
				}
				if byEvent[records[len(records)-7].EventID].Payload != `{"disposition":"completed"}` || byEvent[records[len(records)-6].EventID].Payload != `{"disposition":"active"}` || byEvent[records[len(records)-5].EventID].Reference != "SOLO" {
					t.Fatalf("wrong event-time candidates: %+v", p.Candidates)
				}
			}
			if err := validateInstalledState(want, fixture.profile); err != nil {
				t.Fatal(err)
			}
			for _, mutate := range []func(*wipdjournal.Step8Projection){
				func(p *wipdjournal.Step8Projection) { p.Dependencies[0].BlockerID = sweepFoldOther },
				func(p *wipdjournal.Step8Projection) { p.References[0].BirthEventID = records[len(records)-1].EventID },
				func(p *wipdjournal.Step8Projection) { p.Aggregates[0].Members++ },
				func(p *wipdjournal.Step8Projection) { p.ConfigHistory[0].Value = "tampered" },
				func(p *wipdjournal.Step8Projection) {
					p.Candidates = append(p.Candidates, wipdjournal.TrackerCandidate{ID: sweepFoldMatter})
				},
			} {
				encoded, _ := json.Marshal(want)
				var forged ClientState
				_ = json.Unmarshal(encoded, &forged)
				mutate(forged.Step8Projection)
				if !errors.Is(validateInstalledState(forged, fixture.profile), ErrInvalidClientState) {
					t.Fatal("tampered derived cache passed refold")
				}
			}
			dir := t.TempDir()
			if err := installState(dir, want); err != nil {
				t.Fatal(err)
			}
			if reopened, err := loadInstalledClientState(dir, fixture.profile); err != nil || !reflect.DeepEqual(reopened.Step8Projection, p) {
				t.Fatalf("cache reopen differs: %v", err)
			}
		})
	}
}

func TestStep8ForgedTransferRecomputedPrefixRejected(t *testing.T) {
	fixture := newClientFixture(t)
	for _, mutation := range []string{"snapshot", "subject", "domain", "unknown-endpoint", "cycle", "edge-substitution", "extra-payload"} {
		t.Run(mutation, func(t *testing.T) {
			records := step8TransferHistory(t, "boundary")
			index := len(records) - 7
			if mutation == "unknown-endpoint" || mutation == "cycle" || mutation == "edge-substitution" {
				index = len(records) - 1
			}
			records[index] = changeSweepFoldEvent(t, records[index], func(f map[string]any) {
				p := f["payload"].(map[string]any)
				switch mutation {
				case "snapshot":
					p["tracker_push_level"] = "off"
				case "subject":
					f["subject_id"] = sweepFoldBatch
				case "domain":
					f["domain_id"] = sweepFoldOther
				case "unknown-endpoint":
					p["blocker"] = sweepFoldBatch
				case "cycle":
					f["kind"] = "dependency.added"
					f["subject_id"] = sweepFoldMatter
					p["blocker"] = sweepFoldOther
					p["edge"] = "00000000000000000000000061"
				case "edge-substitution":
					p["edge"] = "00000000000000000000000061"
				case "extra-payload":
					p["secret"] = true
				}
			})
			// The helper recomputes every advertised prefix, not just the event.
			frames, anchor := step7ClientRepairTransferFrames(t, "pull", records, records[:index])
			if _, err := verifyTransferFrames(frames, "pull", fixture.profile, testRepoID, sweepFoldEnv, "sha256:"+strings.Repeat("a", 64), anchor, records[:index]); !errors.Is(err, ErrInvalidClientState) {
				t.Fatalf("forged %s passed: %v", mutation, err)
			}
		})
	}
}

// Fresh v1 authority commands produce retained refusal receipts without model
// effects; the Environment journal allocates their immutable identities.
func TestStep8FreshV1RefusalsSeedPullTerminalReopen(t *testing.T) {
	ctx := context.Background()
	f := newClientFixture(t)
	t.Cleanup(func() {
		if f.store != nil {
			_ = f.store.Close()
		}
	})
	key, artifactCertificate := registerClientFixtureArtifactKeyWithCertificate(t, f)
	sign := func(_ context.Context, message []byte) ([]byte, error) { return ed25519.Sign(key, message), nil }
	directory := t.TempDir()
	state := enrollFixtureClient(t, f, directory)
	peer := peerStateFromClient(t, state)
	root := filepath.Join(privateTempDir(t), "journal")
	identity := wipdjournal.Identity{RepoID: testRepoID, DomainID: testDomainID, AuthorityEpoch: 1, EnvironmentID: state.EnvironmentID, OwnerRootSPKI: state.OwnerKeyID}
	journal, err := wipdjournal.Open(root, identity)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = journal.Close() }()
	base, err := journal.InstallSnapshot(ctx)
	if err != nil {
		t.Fatal(err)
	}
	// Retain birth intents locally so the next sequence is shared by both stores.
	for i, matter := range []string{sweepFoldMatter, sweepFoldOther} {
		entry, err := journal.PrepareCommand(wipdjournal.CommandInput{ID: fmt.Sprintf("%026d", 400+i), Request: operation.Request{Operation: operation.MatterCreateV1.Metadata().Operation, Actor: "human", Context: operation.Context{Repo: testRepoID}, Input: operation.MatterCreateInput{Title: fmt.Sprintf("Matter %d", i)}}})
		if err != nil {
			t.Fatal(err)
		}
		base, err = journal.AdmitPending(ctx, base.Expectation(), entry.Command.ID)
		if err != nil {
			t.Fatalf("admit birth %d local overlay: %v", i, err)
		}
		owner := submitM6PullCommand(t, f.store, peer, entry.Command)
		status, err := f.store.CompleteCommand(ctx, owner, operation.Result{Code: operation.ResultSucceeded, Output: operation.MatterCreateOutput{ID: matter, Title: fmt.Sprintf("Matter %d", i), Locator: fmt.Sprintf("matter-%d", i)}}, matter, m6PullEventID(10+i), time.Now().UTC(), sign)
		if err != nil {
			t.Fatal(err)
		}
		nextState, pullErr := PullAndInstall(ctx, f.profile, f.roots, directory)
		if pullErr != nil {
			t.Fatalf("pull birth %d: %v", i, pullErr)
		}
		transfer, _, verifyErr := VerifyPullTransfer(f.profile, state, state.Prefix, authenticatedPullFrames(t, f, state))
		if verifyErr != nil || len(transfer.Records()) != 1 {
			t.Fatalf("verify birth %d incremental outcome transfer: records=%d error=%v", i, len(transfer.Records()), verifyErr)
		}
		base, err = journal.InstallFold(ctx, base.Expectation(), entry, operation.ResultSucceeded, status.Receipt, transfer)
		if err != nil {
			t.Fatalf("install birth %d authority outcome: %v", i, err)
		}
		state = nextState
	}
	ops := []struct {
		def     operation.Definition
		input   operation.Input
		refused bool
	}{
		{operation.DependencyAddV1, operation.DependencyAddInput{BlockedID: sweepFoldMatter, BlockerID: sweepFoldOther}, true},
		{operation.DependencyAddV1, operation.DependencyAddInput{BlockedID: sweepFoldOther, BlockerID: sweepFoldMatter}, true},
		{operation.DependencyRemoveV1, operation.DependencyRemoveInput{BlockedID: sweepFoldMatter, BlockerID: sweepFoldOther}, true},
		{operation.ReferenceBindV1, operation.ReferenceBindInput{MatterID: sweepFoldMatter, Reference: "OLD"}, true},
		{operation.ReferenceBindV1, operation.ReferenceBindInput{MatterID: sweepFoldMatter, Reference: "OLD"}, true},
		{operation.ReferenceRebindV1, operation.ReferenceRebindInput{MatterID: sweepFoldMatter, From: "OLD", To: "NEW"}, true},
		{operation.ReferenceUnbindV1, operation.ReferenceUnbindInput{MatterID: sweepFoldMatter, Reference: "NEW"}, true},
	}
	for i, op := range ops {
		entry, err := journal.PrepareCommand(wipdjournal.CommandInput{ID: fmt.Sprintf("%026d", 410+i), Request: operation.Request{Operation: op.def.Metadata().Operation, Actor: "human", Context: operation.Context{Repo: testRepoID}, Input: op.input}})
		if err != nil {
			t.Fatal(err)
		}
		owner := submitM6PullCommand(t, f.store, peer, entry.Command)
		status, err := f.store.CompleteCommand(ctx, owner, operation.Result{Code: operation.ResultSucceeded}, "", m6PullEventID(20+i), time.Now().UTC(), sign)
		if err != nil {
			t.Fatal(err)
		}
		transfer, _, err := VerifyPullTransfer(f.profile, state, state.Prefix, authenticatedPullFrames(t, f, state))
		if err != nil {
			t.Fatal(err)
		}
		code := operation.ResultSucceeded
		if op.refused {
			code = operation.ResultRefused
		}
		before := base
		base, err = journal.InstallAuthorityOutcome(ctx, base.Expectation(), entry, code, status.Receipt, transfer)
		if err != nil {
			t.Fatalf("install %s: %v", op.def.Metadata().Operation, err)
		}
		if op.refused && (!anchorEqual(before.Anchor, base.Anchor) || !reflect.DeepEqual(before.Step8Projection, base.Step8Projection) || len(transfer.Records()) != 0) {
			t.Fatal("refusal changed projection/prefix")
		}
		if !bytes.Equal(base.Receipts[entry.Command.ID].CanonicalReceipt, status.Receipt) || base.Receipts[entry.Command.ID].JournalPosition != 0 {
			t.Fatal("authority receipt identity or delivery changed")
		}
		state, err = PullAndInstall(ctx, f.profile, f.roots, directory)
		if err != nil {
			t.Fatal(err)
		}
		if !reflect.DeepEqual(state.Step8Projection, base.Step8Projection) {
			t.Fatal("terminal projection differs from pull")
		}
	}
	cloneID, worktreeID, dispatchID := "01KZ7XHAQT1S46NYPN1PW1DYA6", "01KZ7XHAQT1S46NYPN1PW1DYA5", "01KZ7XHAQT1S46NYPN1PW1DYA7"
	acquireID := fmt.Sprintf("%026d", 500)
	attempt, err := journal.PrepareClaimAcquire(acquireID, sweepFoldMatter, cloneID, worktreeID, dispatchID, "human", base.Anchor)
	if err != nil {
		t.Fatalf("prepare real claim-acquire attempt for v2 write: %v", err)
	}
	authorityAnchor, err := f.store.CurrentPrefixAnchor(ctx, state.DomainID)
	if err != nil || authorityAnchor.EventCount != base.Anchor.EventCount || authorityAnchor.Digest != base.Anchor.Digest {
		t.Fatalf("authority/journal prefix before real claim acquisition differs: authority=%+v journal=%+v error=%v", authorityAnchor, base.Anchor, err)
	}
	acquirePending, err := f.store.SubmitClaimAcquire(ctx, attempt.CanonicalBytes, attempt.RequestHash, authorityAnchor, peer, time.Now().UTC())
	if err != nil || acquirePending.Owner == nil || !acquirePending.Pending {
		t.Fatalf("submit genuine claim acquisition for v2 write: status=%+v error=%v", acquirePending, err)
	}
	claimID, batchID, grantID := fmt.Sprintf("%026d", 501), fmt.Sprintf("%026d", 502), fmt.Sprintf("%026d", 503)
	snapshotID, claimJournalID := fmt.Sprintf("%026d", 504), fmt.Sprintf("%026d", 505)
	acquired, authorityGrant, err := f.store.CompleteClaimAcquire(ctx, acquirePending.Owner, authoritystore.AcquireAllocation{
		ClaimID: claimID, BatchID: batchID, GrantID: grantID, SnapshotID: snapshotID, JournalID: claimJournalID,
		Installed: authorityAnchor, EventIDs: []string{m6PullEventID(30), m6PullEventID(31), m6PullEventID(32)},
	}, time.Now().UTC(), sign)
	if err != nil || acquired.Pending || len(acquired.Receipt) == 0 {
		t.Fatalf("complete genuine claim acquisition for v2 write: status=%+v error=%v", acquired, err)
	}
	claimTransfer, _, err := VerifyPullTransfer(f.profile, state, state.Prefix, authenticatedPullFrames(t, f, state))
	if err != nil || len(claimTransfer.Records()) != 3 {
		t.Fatalf("verify genuine claim-acquisition transfer: records=%d error=%v", len(claimTransfer.Records()), err)
	}
	verifiedGrant, err := wipdjournal.VerifyClaimGrant(identity, wipdjournal.ClaimGrantTrust{
		OwnerRootPublicKey: f.ownerRoot, OwnerRootSPKI: f.ownerKeyID, VerifiedAt: time.Now().UTC(),
	}, wipdjournal.ClaimGrantEvidence{
		ArtifactKeyCertificate: artifactCertificate, Wrapper: authorityGrant.Wrapper,
		Start: authorityGrant.Start, End: authorityGrant.End, Transfer: claimTransfer,
	})
	if err != nil {
		t.Fatalf("verify genuine claim-acquisition grant: %v", err)
	}
	base, err = journal.InstallClaimAcquireGrant(ctx, base.Expectation(), attempt.ID, verifiedGrant)
	if err != nil {
		t.Fatalf("install genuine claim-acquisition grant: %v", err)
	}
	state, err = PullAndInstall(ctx, f.profile, f.roots, directory)
	if err != nil || !anchorEqual(state.Prefix, base.Anchor) || !reflect.DeepEqual(state.Step8Projection, base.Step8Projection) {
		t.Fatalf("claim-grant pull/install/refold parity: %v", err)
	}
	claimProof := []operation.TargetClaim{{MatterID: sweepFoldMatter, ClaimID: claimID, ClaimEpoch: 1}}
	v2Input := operation.ReferenceBindV2Input{MatterID: sweepFoldMatter, Reference: "V2-BOUND", TargetClaims: claimProof}
	v2Entry, err := journal.PrepareCommand(wipdjournal.CommandInput{ID: fmt.Sprintf("%026d", 510), Request: operation.Request{
		Operation: operation.ReferenceBindV2.Metadata().Operation, Actor: "human", Context: operation.Context{Repo: testRepoID}, Input: v2Input,
	}})
	if err != nil {
		t.Fatalf("prepare v2 reference write: %v", err)
	}
	v2Owner := submitM6PullCommand(t, f.store, peer, v2Entry.Command)
	v2Status, err := f.store.CompleteCommand(ctx, v2Owner, operation.Result{Code: operation.ResultSucceeded}, "",
		m6PullEventID(40), time.Now().UTC(), sign)
	if err != nil || v2Status.Pending || len(v2Status.Receipt) == 0 || len(v2Status.SignedReceipt) == 0 {
		t.Fatalf("complete genuine v2 reference write: status=%+v error=%v", v2Status, err)
	}
	v2Transfer, _, err := VerifyPullTransfer(f.profile, state, state.Prefix, authenticatedPullFrames(t, f, state))
	if err != nil || len(v2Transfer.Records()) != 1 {
		t.Fatalf("verify genuine v2 write transfer: records=%d error=%v", len(v2Transfer.Records()), err)
	}
	base, err = journal.InstallAuthorityOutcome(ctx, base.Expectation(), v2Entry, operation.ResultSucceeded, v2Status.Receipt, v2Transfer)
	if err != nil {
		t.Fatalf("install genuine v2 write receipt and transfer: %v", err)
	}
	state, err = PullAndInstall(ctx, f.profile, f.roots, directory)
	if err != nil || !anchorEqual(state.Prefix, base.Anchor) || !reflect.DeepEqual(state.Step8Projection, base.Step8Projection) {
		t.Fatalf("successful v2 seed/pull/install/refold parity: %v", err)
	}
	if state.Step8Projection == nil || len(state.Step8Projection.References) != 1 ||
		state.Step8Projection.References[0].MatterID != sweepFoldMatter || state.Step8Projection.References[0].Reference != "V2-BOUND" ||
		state.Step8Projection.References[0].RemovedEventID != "" {
		t.Fatalf("successful v2 reference was not retained by client refold: %+v", state.Step8Projection)
	}
	v2Replay, err := f.store.QueryCommand(ctx, state.DomainID, v2Entry.Command.ID, v2Entry.RequestHash,
		state.Epoch, peer, state.EnvironmentID, time.Now().UTC())
	if err != nil || !bytes.Equal(v2Replay.Receipt, v2Status.Receipt) || !bytes.Equal(v2Replay.SignedReceipt, v2Status.SignedReceipt) {
		t.Fatalf("successful v2 terminal replay changed: status=%+v error=%v", v2Replay, err)
	}
	seeded, err := EnrollAndSeed(ctx, f.profile, f.roots, f.ownerRoot, f.delegation, testRepoID, f.identity, f.grant, t.TempDir())
	if err != nil || !reflect.DeepEqual(seeded.EventRecords, state.EventRecords) || !reflect.DeepEqual(seeded.Step8Projection, state.Step8Projection) ||
		!anchorEqual(seeded.Prefix, state.Prefix) {
		t.Fatalf("genuine seed/pull parity: %v", err)
	}
	if err := journal.Close(); err != nil {
		t.Fatal(err)
	}
	journal, err = wipdjournal.Open(root, identity)
	if err != nil {
		t.Fatal(err)
	}
	reopened, err := journal.InstallSnapshot(ctx)
	if err != nil || !reflect.DeepEqual(reopened, base) {
		t.Fatalf("terminal reopen differs: %v", err)
	}
	// A failed client-state replacement must retain the exact previous bytes.
	before, err := os.ReadFile(filepath.Join(directory, stateName))
	if err != nil {
		t.Fatal(err)
	}
	if err := replaceInstalledState(directory, []byte("stale expected state"), state); err == nil {
		t.Fatal("stale expected install state accepted")
	}
	after, _ := os.ReadFile(filepath.Join(directory, stateName))
	if !bytes.Equal(before, after) {
		t.Fatal("failed client install changed bytes")
	}
}

func TestStep8PendingFreshV1RefusalRecoversAfterStoreReopenWithoutEffects(t *testing.T) {
	ctx := context.Background()
	f := newClientFixture(t)
	t.Cleanup(func() {
		if f.store != nil {
			_ = f.store.Close()
		}
	})
	key := registerClientFixtureArtifactKey(t, f)
	signer := func(_ context.Context, message []byte) ([]byte, error) { return ed25519.Sign(key, message), nil }
	directory := t.TempDir()
	state := enrollFixtureClient(t, f, directory)
	peer := peerStateFromClient(t, state)
	matterID := "01KZ7XHAQT1S46NYPN1PW1DX90"
	createFixtureMatter(t, f.store, peer, key, state, "01KZ7XHAQT1S46NYPN1PW1DYA9",
		matterID, m6PullEventID(10), "Pending v1 target", "pending-v1", 1)
	beforeState, err := PullAndInstall(ctx, f.profile, f.roots, directory)
	if err != nil {
		t.Fatal(err)
	}
	command := operation.Command{
		ID: "01KZ7XHAQT1S46NYPN1PW1WY00", AuthorityDomainID: state.DomainID, ExpectedAuthorityEpoch: state.Epoch,
		EnvironmentID: state.EnvironmentID, EnvironmentSequence: 2, ActedAt: time.Now().UTC().Truncate(time.Second).Format(time.RFC3339Nano),
		CorrelationCommandID: "01KZ7XHAQT1S46NYPN1PW1WY00",
		Request: operation.Request{
			Operation: operation.ReferenceBindV1.Metadata().Operation, Actor: "human",
			Context: operation.Context{Repo: state.RepoID}, Input: operation.ReferenceBindInput{MatterID: matterID, Reference: "FRESH-V1"},
			Blobs: []operation.BlobInput{},
		},
	}
	hash, err := command.RequestHash()
	if err != nil {
		t.Fatal(err)
	}
	pending, err := f.store.SubmitCommand(ctx, command, hash, peer, time.Now().UTC())
	if err != nil || !pending.Pending || pending.Owner == nil {
		t.Fatalf("submit fresh pending v1 reference write: status=%+v error=%v", pending, err)
	}
	if err = f.store.Close(); err != nil {
		t.Fatal(err)
	}
	f.store, err = authoritystore.OpenExisting(f.storeRoot)
	if err != nil {
		t.Fatalf("reopen authority with pending fresh v1 write: %v", err)
	}
	recovered, err := f.store.RecoverCommand(ctx, command, hash)
	if err != nil {
		t.Fatalf("recover pending fresh v1 write: %v", err)
	}
	terminal, err := f.store.CompleteCommand(ctx, recovered, operation.Result{Code: operation.ResultSucceeded},
		"", m6PullEventID(11), time.Now().UTC(), signer)
	if err != nil || terminal.Pending || len(terminal.Receipt) == 0 || len(terminal.SignedReceipt) == 0 {
		t.Fatalf("complete recovered fresh v1 refusal: status=%+v error=%v", terminal, err)
	}
	receipt, err := wipdwire.DecodeCanonicalMap(terminal.Receipt,
		"schema", "domain_id", "authority_epoch", "identity_schema", "command_id", "request_hash", "operation", "environment", "result", "accepted_events")
	if err != nil {
		t.Fatalf("decode fresh v1 refusal receipt: %v", err)
	}
	result, ok := receipt["result"].(map[string]any)
	if !ok || result["code"] != string(operation.ResultRefused) || result["problem_code"] != "refusal.step8-v1-claim-required" ||
		result["output"] != nil || receipt["accepted_events"] != nil {
		t.Fatalf("recovered fresh v1 receipt has effects or wrong refusal: %+v", receipt)
	}
	afterAnchor, err := f.store.CurrentPrefixAnchor(ctx, state.DomainID)
	if err != nil {
		t.Fatal(err)
	}
	beforeEventID := ""
	if beforeState.Prefix.EventID != nil {
		beforeEventID = *beforeState.Prefix.EventID
	}
	if afterAnchor.EventCount != beforeState.Prefix.EventCount || afterAnchor.EventID != beforeEventID || afterAnchor.Digest != beforeState.Prefix.Digest {
		t.Fatalf("recovered fresh v1 refusal changed model prefix: before=%+v after=%+v", beforeState.Prefix, afterAnchor)
	}
	replay, err := f.store.QueryCommand(ctx, state.DomainID, command.ID, hash, state.Epoch, peer, state.EnvironmentID, time.Now().UTC())
	if err != nil || !bytes.Equal(replay.Receipt, terminal.Receipt) || !bytes.Equal(replay.SignedReceipt, terminal.SignedReceipt) {
		t.Fatalf("recovered fresh v1 terminal replay changed: status=%+v error=%v", replay, err)
	}
}

func TestStep8LegacyOptionalCacheReopensReconstructsAndUpgrades(t *testing.T) {
	f := newClientFixture(t)
	records := append(step7ClientGateHistory(t), step7ClientTestEvent(t, 513, 411, 12, "config.set", testRepoID,
		map[string]any{"key": "tracker.push-level", "value": "narrated"}))
	frames, anchor := step7ClientRepairTransferFrames(t, "seed", records, nil)
	state, err := verifyTransferFrames(frames, "seed", f.profile, testRepoID, sweepFoldEnv, "sha256:"+strings.Repeat("a", 64), anchor, nil)
	if err != nil {
		t.Fatal(err)
	}
	want := []wipdjournal.ConfigHistoryEntry{{RepoID: testRepoID, Key: "tracker.push-level", Value: "narrated", EventID: records[len(records)-1].EventID}}
	// Serialize the old schema with every prior derived cache intact and no
	// step8_projection field. Loading must refold, not trust a missing cache.
	state.Step8Projection = nil
	directory := t.TempDir()
	if err := installState(directory, state); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(directory, stateName)
	original, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var fields map[string]json.RawMessage
	if json.Unmarshal(original, &fields) != nil {
		t.Fatal("invalid legacy JSON")
	}
	if _, present := fields["step8_projection"]; present {
		t.Fatal("regression did not omit optional cache")
	}
	if state.GateProjection == nil || state.StepProjections == nil || state.ContentProjections == nil {
		t.Fatal("regression lost previous caches")
	}
	loaded, err := loadInstalledClientState(directory, f.profile)
	if err != nil || loaded.Step8Projection == nil || !reflect.DeepEqual(loaded.Step8Projection.ConfigHistory, want) {
		t.Fatalf("legacy reopen did not reconstruct config: %+v %v", loaded.Step8Projection, err)
	}
	if err := validateInstalledState(loaded, f.profile); err != nil {
		t.Fatalf("reconstructed state failed refold: %v", err)
	}
	// The next empty tail uses the same load/verify/replace seam as pull and
	// persists the rebuilt cache without rewriting retained event identity.
	legacy, prior, err := loadInstalledState(directory)
	if err != nil || validateInstalledState(legacy, f.profile) != nil {
		t.Fatalf("legacy pull loader: %v", err)
	}
	pullFrames, start := step7ClientRepairTransferFrames(t, "pull", records, records)
	upgraded, err := verifyTransferFrames(pullFrames, "pull", f.profile, legacy.RepoID, legacy.EnvironmentID, legacy.SPKIDigest, start, legacy.EventRecords)
	if err != nil || !reflect.DeepEqual(upgraded.EventRecords, records) || upgraded.Step8Projection == nil || !reflect.DeepEqual(upgraded.Step8Projection.ConfigHistory, want) {
		t.Fatalf("empty pull upgrade: %v", err)
	}
	if err := replaceInstalledState(directory, prior, upgraded); err != nil {
		t.Fatal(err)
	}
	reopened, err := loadInstalledClientState(directory, f.profile)
	if err != nil || !reflect.DeepEqual(reopened.Step8Projection, upgraded.Step8Projection) {
		t.Fatalf("persisted upgraded cache reopen: %v", err)
	}
	// A present cache remains integrity evidence, not a hint to repair.
	reopened.Step8Projection.ConfigHistory[0].Value = "tampered"
	corrupt, _ := json.Marshal(reopened)
	if err := os.WriteFile(path, corrupt, 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := loadInstalledClientState(directory, f.profile); !errors.Is(err, ErrInvalidClientState) {
		t.Fatalf("tampered present config cache accepted: %v", err)
	}
}

func TestStep8MissingCacheCannotBypassEffectValidation(t *testing.T) {
	f := newClientFixture(t)
	records := step8TransferHistory(t, "boundary")
	frames, anchor := step7ClientRepairTransferFrames(t, "seed", records, nil)
	state, err := verifyTransferFrames(frames, "seed", f.profile, testRepoID, sweepFoldEnv, "sha256:"+strings.Repeat("a", 64), anchor, nil)
	if err != nil {
		t.Fatal(err)
	}
	state.Step8Projection = nil
	// Discarding a disposable cache is safe only because all effects still
	// undergo the same strict full-history fold.
	directory := t.TempDir()
	if err := installState(directory, state); err != nil {
		t.Fatal(err)
	}
	if loaded, err := loadInstalledClientState(directory, f.profile); err != nil || loaded.Step8Projection == nil || len(loaded.Step8Projection.Candidates) != 3 {
		t.Fatalf("missing effect cache not reconstructed: %v", err)
	}
	state.EventRecords[len(records)-7] = changeSweepFoldEvent(t, state.EventRecords[len(records)-7], func(f map[string]any) { f["payload"].(map[string]any)["tracker_push_level"] = "off" })
	state.Prefix = step7IndependentPrefixAnchor(state.EventRecords)
	raw, _ := json.Marshal(state)
	if err := os.WriteFile(filepath.Join(directory, stateName), raw, 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := loadInstalledClientState(directory, f.profile); !errors.Is(err, ErrInvalidClientState) {
		t.Fatalf("missing cache bypassed forged event-time snapshot validation: %v", err)
	}
}
