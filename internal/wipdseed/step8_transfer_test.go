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

// Real authority commands produce the terminal receipts and exact records;
// the Environment journal allocates their immutable command identities.
func TestStep8GenuineAuthorityReceiptsSeedPullTerminalReopen(t *testing.T) {
	ctx := context.Background()
	f := newClientFixture(t)
	key := registerClientFixtureArtifactKey(t, f)
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
	// Retain birth intents locally so the next sequence is shared by both stores.
	for i, matter := range []string{sweepFoldMatter, sweepFoldOther} {
		entry, err := journal.PrepareCommand(wipdjournal.CommandInput{ID: fmt.Sprintf("%026d", 400+i), Request: operation.Request{Operation: operation.MatterCreateV1.Metadata().Operation, Actor: "human", Context: operation.Context{Repo: testRepoID}, Input: operation.MatterCreateInput{Title: fmt.Sprintf("Matter %d", i)}}})
		if err != nil {
			t.Fatal(err)
		}
		owner := submitM6PullCommand(t, f.store, peer, entry.Command)
		_, err = f.store.CompleteCommand(ctx, owner, operation.Result{Code: operation.ResultSucceeded, Output: operation.MatterCreateOutput{ID: matter, Title: fmt.Sprintf("Matter %d", i), Locator: fmt.Sprintf("matter-%d", i)}}, matter, m6PullEventID(10+i), time.Now().UTC(), sign)
		if err != nil {
			t.Fatal(err)
		}
	}
	state, err = PullAndInstall(ctx, f.profile, f.roots, directory)
	if err != nil {
		t.Fatal(err)
	}
	manifest := wipdwire.BlobManifest{Schema: "wipd.blob-manifest/1", DomainID: testDomainID, Epoch: 1, AsOf: state.Prefix, Entries: state.ManifestEntries, Digest: state.ManifestDigest}
	prior, err := wipdjournal.VerifyTransfer(testDomainID, 1, emptyWireAnchor(), state.Prefix, state.EventRecords, manifest)
	if err != nil {
		t.Fatal(err)
	}
	base, err := journal.InstallPull(ctx, installSnapshotExpectation(t, journal), prior)
	if err != nil {
		t.Fatal(err)
	}
	ops := []struct {
		def     operation.Definition
		input   operation.Input
		refused bool
	}{
		{operation.DependencyAddV1, operation.DependencyAddInput{BlockedID: sweepFoldMatter, BlockerID: sweepFoldOther}, false},
		{operation.DependencyAddV1, operation.DependencyAddInput{BlockedID: sweepFoldOther, BlockerID: sweepFoldMatter}, true},
		{operation.DependencyRemoveV1, operation.DependencyRemoveInput{BlockedID: sweepFoldMatter, BlockerID: sweepFoldOther}, false},
		{operation.ReferenceBindV1, operation.ReferenceBindInput{MatterID: sweepFoldMatter, Reference: "OLD"}, false},
		{operation.ReferenceBindV1, operation.ReferenceBindInput{MatterID: sweepFoldMatter, Reference: "OLD"}, true},
		{operation.ReferenceRebindV1, operation.ReferenceRebindInput{MatterID: sweepFoldMatter, From: "OLD", To: "NEW"}, false},
		{operation.ReferenceUnbindV1, operation.ReferenceUnbindInput{MatterID: sweepFoldMatter, Reference: "NEW"}, false},
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
	seeded, err := EnrollAndSeed(ctx, f.profile, f.roots, f.ownerRoot, f.delegation, testRepoID, f.identity, f.grant, t.TempDir())
	if err != nil || !reflect.DeepEqual(seeded.EventRecords, state.EventRecords) || !reflect.DeepEqual(seeded.Step8Projection, state.Step8Projection) {
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
