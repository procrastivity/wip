package wipdseed

import (
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/procrastivity/wip/internal/wipdjournal"
	"github.com/procrastivity/wip/internal/wipdwire"
)

const (
	sweepFoldMatter   = "00000000000000000000000041"
	sweepFoldBatch    = "00000000000000000000000042"
	sweepFoldClaim    = "00000000000000000000000043"
	sweepFoldDispatch = "00000000000000000000000044"
	sweepFoldWorktree = "00000000000000000000000045"
	sweepFoldOther    = "00000000000000000000000046"
	sweepFoldEnv      = "01KZ7XHAQT1S46NYPN1PW1DX3A"
	sweepFoldOtherEnv = "01KZ7XHAQT1S46NYPN1PW1DX3B"
)

// Swap the two normal close paths' order so either can be the latest close.
func postCloseSweepHistory(t *testing.T, birthLast bool) []wipdwire.EventRecord {
	t.Helper()
	event := func(number, command int, seq uint64, kind, subject string, payload map[string]any) wipdwire.EventRecord {
		return step7ClientTestEvent(t, number, command, seq, kind, subject, payload)
	}
	created := event(500, 400, 1, "matter.created", sweepFoldMatter,
		map[string]any{"id": sweepFoldMatter, "locator": "sweep-target", "title": "Sweep target"})
	barrier, err := wipdwire.JournalBarrierDigest([]wipdwire.JournalBarrierEntry{{
		Position: 1, CommandID: fmt.Sprintf("%026d", 400), RequestHash: testDigest([]byte("client-step7:" + fmt.Sprintf("%026d", 400))),
		ResultCode: "result.succeeded", Range: &wipdwire.JournalBarrierRange{First: created.EventID, Last: created.EventID, Count: 1},
	}})
	if err != nil {
		t.Fatal(err)
	}
	birthRelease := func(number int, seq uint64) wipdwire.EventRecord {
		return event(number, 406, seq, "claim.released", sweepFoldMatter, map[string]any{
			"claim_id": sweepFoldMatter, "claim_epoch": uint64(1), "dispatch_id": nil, "barrier_digest": barrier,
		})
	}
	records := []wipdwire.EventRecord{created}
	if !birthLast {
		records = append(records, birthRelease(501, 2))
	}
	records = append(records,
		event(502, 402, 3, "batch.anonymous-created", sweepFoldBatch, map[string]any{"batch_id": sweepFoldBatch, "matter_id": sweepFoldMatter}),
		event(503, 402, 3, "claim.acquired", sweepFoldClaim, map[string]any{
			"claim_id": sweepFoldClaim, "claim_epoch": uint64(1), "matter_id": sweepFoldMatter,
			"batch_id": sweepFoldBatch, "dispatch_id": sweepFoldDispatch, "owner_environment_id": sweepFoldEnv, "worktree_id": sweepFoldWorktree,
		}),
		event(504, 402, 3, "dispatch.opened", sweepFoldDispatch, map[string]any{
			"dispatch_id": sweepFoldDispatch, "matter_id": sweepFoldMatter, "batch_id": sweepFoldBatch,
			"claim_id": sweepFoldClaim, "worktree_id": sweepFoldWorktree,
		}),
		event(505, 403, 4, "matter.started", sweepFoldMatter, map[string]any{"from": "planned", "to": "in-progress"}),
		event(506, 404, 5, "matter.finished", sweepFoldMatter, map[string]any{"from": "in-progress", "to": "done"}),
		event(508, 405, 6, "dispatch.closed", sweepFoldDispatch, map[string]any{
			"dispatch_id": sweepFoldDispatch, "claim_id": sweepFoldClaim, "claim_epoch": uint64(1),
		}),
		event(509, 405, 6, "claim.released", sweepFoldClaim, map[string]any{
			"claim_id": sweepFoldClaim, "claim_epoch": uint64(1), "dispatch_id": sweepFoldDispatch,
			"barrier_digest": testDigest([]byte("acquired close barrier")),
		}))
	if birthLast {
		records = append(records, birthRelease(510, 8))
	}
	// Unrelated history from another Environment may interleave before sweep.
	unrelated := event(511, 407, 1, "matter.created", sweepFoldOther,
		map[string]any{"id": sweepFoldOther, "locator": "unrelated", "title": "Unrelated Matter"})
	unrelated = changeSweepFoldEvent(t, unrelated, func(fields map[string]any) {
		fields["environment"].(map[string]any)["id"] = sweepFoldOtherEnv
	})
	return append(records, unrelated, event(512, 410, 12, "batch.swept", sweepFoldBatch, map[string]any{}))
}

func changeSweepFoldEvent(t *testing.T, record wipdwire.EventRecord, mutate func(map[string]any)) wipdwire.EventRecord {
	t.Helper()
	fields, err := wipdwire.DecodeCanonicalMap(record.Record,
		"schema", "event_id", "domain_id", "command_id", "request_hash", "environment", "acted_at", "occurred_at", "kind", "subject_id", "repo_id", "payload")
	if err != nil {
		t.Fatal(err)
	}
	mutate(fields)
	record.Record, err = wipdwire.EncodeCanonical(fields)
	if err != nil {
		t.Fatal(err)
	}
	return record
}

func TestBatchSweepTransferFoldsStandaloneAfterEitherNormalClose(t *testing.T) {
	fixture := newClientFixture(t)
	for _, birthLast := range []bool{false, true} {
		name := "acquired"
		if birthLast {
			name = "implicit-birth"
		}
		t.Run(name, func(t *testing.T) {
			records := postCloseSweepHistory(t, birthLast)
			want := step7IndependentPrefixAnchor(records)
			manifest := wipdwire.BlobManifest{
				Schema: "wipd.blob-manifest/1", DomainID: testDomainID, Epoch: 1,
				AsOf: want, Entries: []wipdwire.BlobManifestEntry{}, Digest: emptyManifestDigest(),
			}
			// Journal transfer validation already accepts this distinct command.
			if transfer, err := wipdjournal.VerifyTransfer(testDomainID, 1, emptyWireAnchor(), want, records, manifest); err != nil || !transfer.Valid() {
				t.Fatalf("journal structural verification: %v", err)
			}
			for _, test := range []struct {
				name       string
				priorCount int
			}{
				{"seed", 0},
				{"pull through close", 6},
				{"pull after close", len(records) - 1},
			} {
				t.Run(test.name, func(t *testing.T) {
					kind := "pull"
					if test.priorCount == 0 {
						kind = "seed"
					}
					prior := records[:test.priorCount]
					frames, installed := step7ClientRepairTransferFrames(t, kind, records, prior)
					state, err := verifyTransferFrames(frames, kind, fixture.profile, testRepoID, sweepFoldEnv,
						"sha256:"+strings.Repeat("a", 64), installed, prior)
					if err != nil || !anchorEqual(state.Prefix, want) || len(state.Projections) != 2 || len(state.EventRecords) != len(records) {
						t.Fatalf("%s post-close sweep: prefix=%+v projections=%d records=%d err=%v", kind, state.Prefix, len(state.Projections), len(state.EventRecords), err)
					}
					var matter eventProjection
					if json.Unmarshal(state.Projections[0], &matter) != nil || matter.ID != sweepFoldMatter || matter.State != "done" {
						t.Fatalf("post-close target projection: %+v", matter)
					}
				})
			}
		})
	}
}

func TestBatchSweepTransferRejectsPriorCommandIDReuse(t *testing.T) {
	fixture := newClientFixture(t)
	for _, test := range []struct {
		name    string
		command int
	}{
		{"earlier implicit-birth release", 406},
		{"FINISH-A", 404},
	} {
		t.Run(test.name, func(t *testing.T) {
			records := postCloseSweepHistory(t, false)
			records[len(records)-1] = changeSweepFoldEvent(t, records[len(records)-1], func(fields map[string]any) {
				id := fmt.Sprintf("%026d", test.command)
				fields["command_id"] = id
				fields["request_hash"] = testDigest([]byte("client-step7:" + id))
			})
			for _, kind := range []string{"seed", "pull"} {
				t.Run(kind, func(t *testing.T) {
					var prior []wipdwire.EventRecord
					if kind == "pull" {
						// Only the sweep is new; the reused ID is already installed.
						prior = records[:len(records)-1]
					}
					frames, installed := step7ClientRepairTransferFrames(t, kind, records, prior)
					state, err := verifyTransferFrames(frames, kind, fixture.profile, testRepoID, sweepFoldEnv,
						"sha256:"+strings.Repeat("a", 64), installed, prior)
					if !errors.Is(err, ErrInvalidClientState) || state.Prefix.EventCount != 0 || state.EventRecords != nil {
						t.Fatalf("%s accepted standalone sweep reusing command ID %d: prefix=%+v err=%v", kind, test.command, state.Prefix, err)
					}
				})
			}
		})
	}
}

func TestBatchSweepTransferRejectsInvalidStandalone(t *testing.T) {
	for _, birthLast := range []bool{false, true} {
		name, closeSequence, closeCommand := "acquired", uint64(6), 405
		if birthLast {
			name, closeSequence, closeCommand = "implicit-birth", 8, 406
		}
		t.Run(name, func(t *testing.T) {
			for _, test := range []struct {
				name   string
				mutate func(map[string]any)
			}{
				{"wrong Environment", func(f map[string]any) { f["environment"].(map[string]any)["id"] = sweepFoldOtherEnv }},
				{"same sequence", func(f map[string]any) { f["environment"].(map[string]any)["sequence"] = closeSequence }},
				{"earlier sequence", func(f map[string]any) { f["environment"].(map[string]any)["sequence"] = closeSequence - 1 }},
				{"same close command", func(f map[string]any) { f["command_id"] = fmt.Sprintf("%026d", closeCommand) }},
				{"wrong Batch", func(f map[string]any) { f["subject_id"] = sweepFoldOther }},
				{"wrong Repo", func(f map[string]any) { f["repo_id"] = sweepFoldOther }},
				{"nonempty payload", func(f map[string]any) { f["payload"] = map[string]any{"unexpected": true} }},
			} {
				t.Run(test.name, func(t *testing.T) {
					records := postCloseSweepHistory(t, birthLast)
					records[len(records)-1] = changeSweepFoldEvent(t, records[len(records)-1], test.mutate)
					if _, _, _, err := foldEventRecords(records, testDomainID); !errors.Is(err, ErrInvalidClientState) {
						t.Fatalf("invalid standalone sweep folded: %v", err)
					}
				})
			}
			for _, test := range []struct {
				name string
				drop []int
			}{
				{"no normal close", []int{501, 508, 509, 510}},
				{"birth still live", []int{501, 510}},
				{"acquired still live", []int{508, 509}},
				{"incomplete acquired close", []int{509}},
				{"Matter not Done", []int{506}},
			} {
				t.Run(test.name, func(t *testing.T) {
					var records []wipdwire.EventRecord
					for _, record := range postCloseSweepHistory(t, birthLast) {
						drop := false
						for _, number := range test.drop {
							drop = drop || record.EventID == fmt.Sprintf("%026d", number)
						}
						if !drop {
							records = append(records, record)
						}
					}
					if _, _, _, err := foldEventRecords(records, testDomainID); !errors.Is(err, ErrInvalidClientState) {
						t.Fatalf("ineligible standalone sweep folded: %v", err)
					}
				})
			}
			t.Run("duplicate", func(t *testing.T) {
				records := postCloseSweepHistory(t, birthLast)
				records = append(records, step7ClientTestEvent(t, 513, 411, 13, "batch.swept", sweepFoldBatch, map[string]any{}))
				if _, _, _, err := foldEventRecords(records, testDomainID); !errors.Is(err, ErrInvalidClientState) {
					t.Fatalf("duplicate standalone sweep folded: %v", err)
				}
			})
		})
	}
}

func TestBatchSweepTransferPreservesLegacyInlineSweep(t *testing.T) {
	records := postCloseSweepHistory(t, true)
	// Legacy inline sweep still folds with both claims live and no release.
	legacy := append(cloneEventRecords(records[:6]),
		step7ClientTestEvent(t, 507, 404, 5, "batch.swept", sweepFoldBatch, map[string]any{}))
	if _, _, _, err := foldEventRecords(legacy, testDomainID); err != nil {
		t.Fatalf("legacy same-command inline sweep: %v", err)
	}
	declaration := step7ClientTestEvent(t, 501, 401, 2, "gate.declared", testRepoID, map[string]any{"gate": "historical-open", "scale": "matter"})
	legacyWithGate := append(append(cloneEventRecords(legacy[:1]), declaration), legacy[1:]...)
	if _, _, _, err := foldEventRecords(legacyWithGate, testDomainID); err != nil {
		t.Fatalf("legacy inline sweep acquired a new D55 requirement: %v", err)
	}
	if _, _, _, err := foldEventRecords(append(legacy, records[6:]...), testDomainID); !errors.Is(err, ErrInvalidClientState) {
		t.Fatalf("standalone sweep duplicated a legacy inline sweep: %v", err)
	}
}

func TestBatchSweepTransferAcceptsIncompleteSubtree(t *testing.T) {
	records := postCloseSweepHistory(t, true)
	stage := step7ClientTestEvent(t, 501, 401, 2, "stage.created", "00000000000000000000000049", map[string]any{
		"matter_id": sweepFoldMatter, "locator": "unfinished", "title": "Unfinished Stage", "sort_key": uint64(1000),
	})
	records = append(append(cloneEventRecords(records[:1]), stage), records[1:]...)
	if _, _, _, err := foldEventRecords(records[:len(records)-1], testDomainID); err != nil {
		t.Fatalf("incomplete subtree before standalone sweep: %v", err)
	}
	if _, _, _, err := foldEventRecords(records, testDomainID); err != nil {
		t.Fatalf("standalone sweep with unfinished descendant: %v", err)
	}
}

func TestBatchSweepTransferRequiresD55AtSweepBoundary(t *testing.T) {
	for _, satisfaction := range []string{"open", "closed", "dismissed", "exempt", "closed after sweep"} {
		t.Run(satisfaction, func(t *testing.T) {
			records := postCloseSweepHistory(t, true)
			records = records[:len(records)-1]
			declaration := map[string]any{"gate": "own-review", "scale": "matter"}
			if satisfaction == "exempt" {
				declaration["exempt"] = []any{sweepFoldMatter}
			}
			records = append(records, step7ClientTestEvent(t, 520, 420, 13, "gate.declared", testRepoID, declaration))
			if satisfaction == "closed" || satisfaction == "dismissed" {
				payload := map[string]any{"gate": "own-review", "scale": "matter"}
				if satisfaction == "dismissed" {
					payload["reason"] = "review waived"
				}
				records = append(records, step7ClientTestEvent(t, 521, 421, 14, "gate."+satisfaction, sweepFoldMatter, payload))
			}
			records = append(records, step7ClientTestEvent(t, 522, 422, 15, "batch.swept", sweepFoldBatch, map[string]any{}))
			if satisfaction == "closed after sweep" {
				records = append(records, step7ClientTestEvent(t, 523, 423, 16, "gate.closed", sweepFoldMatter,
					map[string]any{"gate": "own-review", "scale": "matter"}))
			}
			_, _, _, err := foldEventRecords(records, testDomainID)
			if satisfaction == "open" || satisfaction == "closed after sweep" {
				if !errors.Is(err, ErrInvalidClientState) {
					t.Fatalf("sweep without D55 obligations satisfied at its boundary: %v", err)
				}
			} else if err != nil {
				t.Fatalf("sweep with %s own gate: %v", satisfaction, err)
			}
		})
	}
}

func TestBatchSweepTransferReacquisitionReplacesClosingEnvironment(t *testing.T) {
	const claim, dispatch = "00000000000000000000000047", "00000000000000000000000048"
	records := postCloseSweepHistory(t, true)
	records = records[:len(records)-1]
	event := func(number, command int, seq uint64, kind, subject string, payload map[string]any) wipdwire.EventRecord {
		return changeSweepFoldEvent(t, step7ClientTestEvent(t, number, command, seq, kind, subject, payload), func(f map[string]any) {
			f["environment"].(map[string]any)["id"] = sweepFoldOtherEnv
		})
	}
	records = append(records,
		event(520, 420, 2, "claim.acquired", claim, map[string]any{
			"claim_id": claim, "claim_epoch": uint64(2), "matter_id": sweepFoldMatter,
			"batch_id": sweepFoldBatch, "dispatch_id": dispatch, "owner_environment_id": sweepFoldOtherEnv, "worktree_id": sweepFoldWorktree,
		}),
		event(521, 420, 2, "dispatch.opened", dispatch, map[string]any{
			"dispatch_id": dispatch, "matter_id": sweepFoldMatter, "batch_id": sweepFoldBatch, "claim_id": claim, "worktree_id": sweepFoldWorktree,
		}))
	sweep := step7ClientTestEvent(t, 524, 422, 12, "batch.swept", sweepFoldBatch, map[string]any{})
	if _, _, _, err := foldEventRecords(append(cloneEventRecords(records), sweep), testDomainID); !errors.Is(err, ErrInvalidClientState) {
		t.Fatalf("sweep using stale close during reacquisition: %v", err)
	}
	records = append(records,
		event(522, 421, 3, "dispatch.closed", dispatch, map[string]any{"dispatch_id": dispatch, "claim_id": claim, "claim_epoch": uint64(2)}),
		event(523, 421, 3, "claim.released", claim, map[string]any{
			"claim_id": claim, "claim_epoch": uint64(2), "dispatch_id": dispatch, "barrier_digest": testDigest([]byte("new close")),
		}))
	if _, _, _, err := foldEventRecords(append(cloneEventRecords(records), sweep), testDomainID); !errors.Is(err, ErrInvalidClientState) {
		t.Fatalf("sweep from old closing Environment: %v", err)
	}
	sweep = event(524, 422, 4, "batch.swept", sweepFoldBatch, map[string]any{})
	if _, _, _, err := foldEventRecords(append(records, sweep), testDomainID); err != nil {
		t.Fatalf("sweep from latest closing Environment with its own later sequence: %v", err)
	}
}
