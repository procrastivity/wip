package authoritystore

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/tls"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/fxamacker/cbor/v2"
	"github.com/procrastivity/wip/internal/operation"
)

type sweepFixture struct {
	f          *claimTestFixture
	allocation AcquireAllocation
	next       uint64
	acquired   operation.ClaimCloseReference
	birth      operation.ClaimCloseReference
}

func newSweepFixture(t *testing.T, openGate, legacy, leaveBirthOpen bool) sweepFixture {
	t.Helper()
	f, allocation := gateClaimedFixture(t)
	x := sweepFixture{f: f, allocation: allocation, next: 3}
	ctx := context.Background()
	if legacy {
		createLegacyInlineSweepFinish(t, f, allocation)
		x.next = 5
	} else {
		start := step12Command(f, 12, x.next, operation.MatterStartV1, operation.NodeLifecycleInput{NodeID: f.matter}, allocation.ClaimID)
		started := completeLifecycleCommand(t, f, start, []string{claimTestID(105)})
		claimTestAcknowledge(t, f, allocation.JournalID, 1, started)
		x.next++
		if openGate {
			declare := step12Command(f, 14, x.next, operation.GateDeclareV1, operation.GateDeclareInput{Gate: "quality", Scale: "matter"}, allocation.ClaimID)
			owner := submitGateOperation(t, f, declare)
			declared, err := f.s.CompleteCommand(ctx, owner, operation.Result{Code: operation.ResultSucceeded}, repoA, claimTestID(106), f.now, signWith(f.key))
			if err != nil {
				t.Fatal(err)
			}
			claimTestAcknowledge(t, f, allocation.JournalID, 2, declared)
			x.next++
		}
		finish := step12Command(f, 13, x.next, operation.MatterFinishV1, operation.MatterFinishInput{MatterID: f.matter}, allocation.ClaimID)
		// FINISH-A is authority delivered, not a claim-journal append/ACK.
		completeLifecycleCommand(t, f, finish, []string{claimTestID(107)})
		x.next++
	}
	if err := f.s.SealClaimJournal(ctx, domainA, allocation.JournalID); err != nil {
		t.Fatal(err)
	}
	tx, err := f.s.db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	digest, count, err := barrierDigest(ctx, tx, domainA, allocation.JournalID)
	_ = tx.Rollback()
	if err != nil {
		t.Fatal(err)
	}
	barrier := map[string]any{"schema": "wipd.journal-barrier/1", "journal_id": allocation.JournalID,
		"claim": map[string]any{"id": allocation.ClaimID, "epoch": uint64(1)}, "entry_count": count, "last_position": count,
		"terminal_receipt_count": count, "entries_digest": digest, "sealed": true, "unresolved_count": uint64(0), "quarantined_count": uint64(0)}
	raw, hash := f.command(t, 15, x.next, "claim.release", map[string]any{"id": allocation.ClaimID, "epoch": uint64(1)}, map[string]any{"barrier": barrier})
	pending, err := f.s.SubmitClaimLifecycle(ctx, raw, hash, f.peer, f.now, nil)
	if err != nil {
		t.Fatal(err)
	}
	released, err := f.s.CompleteClaimLifecycle(ctx, pending.Owner, "", []string{claimTestID(110), claimTestID(111)}, f.now, signWith(f.key))
	if err != nil {
		t.Fatal(err)
	}
	x.acquired = sweepReference(t, f, claimTestID(15), hash, allocation.ClaimID, released)
	x.next++
	if !leaveBirthOpen {
		x.closeBirth(t)
	}
	return x
}

func (x *sweepFixture) closeBirth(t *testing.T) {
	t.Helper()
	f, ctx := x.f, context.Background()
	var hash string
	var receipt []byte
	if err := f.s.db.QueryRow(`SELECT s.request_hash,r.receipt FROM submissions s JOIN terminal_receipts r USING(domain_id,command_id) WHERE s.command_id=?`, claimTestID(10)).Scan(&hash, &receipt); err != nil {
		t.Fatal(err)
	}
	if err := f.s.AcknowledgeBirthJournalEntry(ctx, claimTestBirthAck(t, f, claimTestID(10), hash, receipt, f.anchor(t)), f.peer, envA, f.now); err != nil {
		t.Fatal(err)
	}
	tx, err := f.s.db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	digest, count, receipts, unresolved, quarantined, err := birthBarrierStatus(ctx, tx, domainA, f.matter)
	_ = tx.Rollback()
	if err != nil {
		t.Fatal(err)
	}
	barrier := map[string]any{"schema": "wipd.journal-barrier/1", "journal_id": f.matter,
		"claim": map[string]any{"id": f.matter, "epoch": uint64(1)}, "entry_count": count, "last_position": count,
		"terminal_receipt_count": receipts, "entries_digest": digest, "sealed": true, "unresolved_count": unresolved, "quarantined_count": quarantined}
	raw, hash := claimTestBirthRelease(t, f, 16, x.next, barrier)
	pending, err := f.s.SubmitClaimLifecycle(ctx, raw, hash, f.peer, f.now, nil)
	if err != nil {
		t.Fatal(err)
	}
	released, err := f.s.CompleteClaimLifecycle(ctx, pending.Owner, "", []string{claimTestID(112)}, f.now, signWith(f.key))
	if err != nil {
		t.Fatal(err)
	}
	x.birth = sweepReference(t, f, claimTestID(16), hash, f.matter, released)
	x.next++
}

func sweepReference(t *testing.T, f *claimTestFixture, id, hash, claim string, status CommandStatus) operation.ClaimCloseReference {
	t.Helper()
	r, err := readReceipt(status.Receipt)
	if err != nil || r.Result.Code != "result.succeeded" {
		t.Fatalf("fixture release = %+v, %v", r, err)
	}
	anchor := f.anchor(t)
	return operation.ClaimCloseReference{ClaimID: claim, ClaimEpoch: 1, ReleaseCommandID: id, ReleaseRequestHash: hash,
		TerminalReceiptDigest: digestBytes(status.Receipt), InstalledPrefixAnchor: operation.ClaimClosePrefix{
			EventCount: anchor.EventCount, EventID: &anchor.EventID, Digest: anchor.Digest}}
}

func (x sweepFixture) command(id int, ref operation.ClaimCloseReference) operation.Command {
	return step12Command(x.f, id, x.next, operation.BatchSweepAnonymousV1,
		operation.BatchSweepAnonymousInput{MatterID: x.f.matter, BatchID: x.allocation.BatchID, ClaimClose: ref}, "")
}

func sweepTestResult(t *testing.T, status CommandStatus, outcome string, problem operation.ProblemCode) {
	t.Helper()
	r, err := readReceipt(status.Receipt)
	if err != nil || status.Pending || status.Owner != nil || len(status.SignedReceipt) == 0 {
		t.Fatalf("terminal sweep = %+v, %v", r, err)
	}
	if problem != "" {
		if r.Result.Code != "result.refused" || r.Result.Output != nil || r.Result.Problem == nil || *r.Result.Problem != string(problem) || r.Range != nil {
			t.Fatalf("refusal = %+v, want %s", r, problem)
		}
		return
	}
	want := encodeTest(t, map[string]any{"outcome": outcome})
	if r.Result.Code != "result.succeeded" || !bytes.Equal(r.Result.Output, want) || r.Result.Problem != nil ||
		(outcome == "swept" && (r.Range == nil || r.Range.Count != 1 || r.Range.First != r.Range.Last)) ||
		(outcome == "already-swept" && r.Range != nil) {
		t.Fatalf("sweep output = %+v, want %s", r, outcome)
	}
}

func TestBatchSweepNormalReleasesReplayAndReopen(t *testing.T) {
	for _, kind := range []string{"acquired", "implicit birth"} {
		t.Run(kind, func(t *testing.T) {
			x := newSweepFixture(t, false, false, false)
			ref := x.acquired
			if kind == "implicit birth" {
				ref = x.birth
			}
			command := x.command(30, ref)
			hash := hashCommand(t, command)
			before := x.f.anchor(t)
			// The candidate remains outside every public store submission route.
			if _, err := x.f.s.SubmitCommand(context.Background(), command, hash, x.f.peer, x.f.now); !errors.Is(err, ErrInvalidProof) {
				t.Fatalf("public submission = %v", err)
			}
			status, err := x.f.s.sweepAnonymousBatch(context.Background(), command, hash, x.f.peer, x.f.now, claimTestID(120), signWith(x.f.key))
			if err != nil {
				t.Fatal(err)
			}
			sweepTestResult(t, status, "swept", "")
			if after := x.f.anchor(t); after.EventCount != before.EventCount+1 {
				t.Fatalf("first sweep prefix = %+v; before %+v", after, before)
			}
			claimTestEvent(t, x.f, int(before.EventCount+1), 120, 30, hash, "batch.swept", x.allocation.BatchID, x.next, map[string]any{})
			x.next++
			fresh := x.command(31, ref)
			noop, err := x.f.s.sweepAnonymousBatch(context.Background(), fresh, hashCommand(t, fresh), x.f.peer, x.f.now, "", signWith(x.f.key))
			if err != nil {
				t.Fatal(err)
			}
			sweepTestResult(t, noop, "already-swept", "")
			if x.f.anchor(t).EventCount != before.EventCount+1 {
				t.Fatal("fresh no-op appended an event")
			}
			if err = x.f.s.Close(); err != nil {
				t.Fatal(err)
			}
			x.f.s, err = OpenExisting(x.f.root)
			if err != nil {
				t.Fatalf("reopen: %v", err)
			}
			replay, err := x.f.s.sweepAnonymousBatch(context.Background(), command, hash, x.f.peer, x.f.now.Add(time.Minute), "unused", signWith(key("different signer")))
			if err != nil || !bytes.Equal(replay.Receipt, status.Receipt) || !bytes.Equal(replay.SignedReceipt, status.SignedReceipt) {
				t.Fatalf("exact replay = %+v, %v", replay, err)
			}
			changed := command
			changed.Request.Actor = "system:changed"
			if _, err = x.f.s.sweepAnonymousBatch(context.Background(), changed, hashCommand(t, changed), x.f.peer, x.f.now, "", signWith(x.f.key)); !errors.Is(err, ErrConflict) {
				t.Fatalf("ID/hash conflict = %v", err)
			}
		})
	}
}

func TestBatchSweepReferenceSubstitutions(t *testing.T) {
	mutations := []struct {
		name string
		edit func(*operation.ClaimCloseReference)
	}{
		{"missing release", func(r *operation.ClaimCloseReference) { r.ReleaseCommandID = claimTestID(999) }},
		{"request hash", func(r *operation.ClaimCloseReference) { r.ReleaseRequestHash = digestBytes([]byte("different")) }},
		{"receipt digest", func(r *operation.ClaimCloseReference) { r.TerminalReceiptDigest = digestBytes([]byte("different")) }},
		{"claim ID", func(r *operation.ClaimCloseReference) { r.ClaimID = claimTestID(998) }},
		{"claim epoch", func(r *operation.ClaimCloseReference) { r.ClaimEpoch = 2 }},
		{"prefix before release", func(r *operation.ClaimCloseReference) { r.InstalledPrefixAnchor.EventCount-- }},
		{"prefix future", func(r *operation.ClaimCloseReference) { r.InstalledPrefixAnchor.EventCount += 100 }},
		{"prefix event ID", func(r *operation.ClaimCloseReference) { id := claimTestID(997); r.InstalledPrefixAnchor.EventID = &id }},
		{"prefix digest", func(r *operation.ClaimCloseReference) {
			r.InstalledPrefixAnchor.Digest = digestBytes([]byte("different"))
		}},
	}
	for _, kind := range []string{"acquired", "implicit birth"} {
		t.Run(kind, func(t *testing.T) {
			x := newSweepFixture(t, false, false, false)
			ref := x.acquired
			if kind == "implicit birth" {
				ref = x.birth
			}
			for i, test := range mutations {
				t.Run(test.name, func(t *testing.T) {
					mutated := ref
					test.edit(&mutated)
					command := x.command(40+i, mutated)
					status, err := x.f.s.sweepAnonymousBatch(context.Background(), command, hashCommand(t, command), x.f.peer, x.f.now, "", signWith(x.f.key))
					if err != nil {
						t.Fatal(err)
					}
					sweepTestResult(t, status, "", operation.ProblemBatchSweepClaimClose)
					x.next++
				})
			}
			if err := checkStep4State(x.f.s.db); err != nil {
				t.Fatalf("refusal history: %v", err)
			}
		})
	}
}

func TestBatchSweepRefusalsAndHistoricalBoundary(t *testing.T) {
	t.Run("missing targets", func(t *testing.T) {
		x := newSweepFixture(t, false, false, false)
		for i, matter := range []bool{true, false} {
			command := x.command(60+i, x.acquired)
			input := command.Request.Input.(operation.BatchSweepAnonymousInput)
			if matter {
				input.MatterID = claimTestID(999)
			} else {
				input.BatchID = claimTestID(999)
			}
			command.Request.Input = input
			status, err := x.f.s.sweepAnonymousBatch(context.Background(), command, hashCommand(t, command), x.f.peer, x.f.now, "", signWith(x.f.key))
			if err != nil {
				t.Fatal(err)
			}
			sweepTestResult(t, status, "", operation.ProblemBatchSweepTargetMissing)
			x.next++
		}
	})
	t.Run("Done with open gate", func(t *testing.T) {
		x := newSweepFixture(t, true, false, false)
		command := x.command(60, x.acquired)
		status, err := x.f.s.sweepAnonymousBatch(context.Background(), command, hashCommand(t, command), x.f.peer, x.f.now, "", signWith(x.f.key))
		if err != nil {
			t.Fatal(err)
		}
		sweepTestResult(t, status, "", operation.ProblemBatchSweepNotEligible)
		if err = checkStep4State(x.f.s.db); err != nil {
			t.Fatal(err)
		}
	})
	t.Run("current birth claim and later close", func(t *testing.T) {
		x := newSweepFixture(t, false, false, true)
		command := x.command(60, x.acquired)
		status, err := x.f.s.sweepAnonymousBatch(context.Background(), command, hashCommand(t, command), x.f.peer, x.f.now, "", signWith(x.f.key))
		if err != nil {
			t.Fatal(err)
		}
		sweepTestResult(t, status, "", operation.ProblemBatchSweepNotEligible)
		x.next++
		x.closeBirth(t)
		if err = checkStep4State(x.f.s.db); err != nil {
			t.Fatalf("later close changed historical refusal: %v", err)
		}
	})
	t.Run("role actor unsupported", func(t *testing.T) {
		x := newSweepFixture(t, false, false, false)
		command := x.command(60, x.acquired)
		command.Request.Actor = "role:worker"
		status, err := x.f.s.sweepAnonymousBatch(context.Background(), command, hashCommand(t, command), x.f.peer, x.f.now, "", signWith(x.f.key))
		if err != nil {
			t.Fatal(err)
		}
		sweepTestResult(t, status, "", operation.ProblemBatchSweepUnsupported)
	})
	t.Run("legacy inline sweep", func(t *testing.T) {
		x := newSweepFixture(t, false, true, false)
		before := x.f.anchor(t)
		command := x.command(60, x.acquired)
		status, err := x.f.s.sweepAnonymousBatch(context.Background(), command, hashCommand(t, command), x.f.peer, x.f.now, "", signWith(x.f.key))
		if err != nil {
			t.Fatal(err)
		}
		sweepTestResult(t, status, "already-swept", "")
		if x.f.anchor(t) != before {
			t.Fatal("legacy sweep repeated")
		}
		if err = checkStep4State(x.f.s.db); err != nil {
			t.Fatal(err)
		}
	})
}

func TestBatchSweepConcurrentReplayAndFreshIDs(t *testing.T) {
	x := newSweepFixture(t, false, false, false)
	command := x.command(70, x.acquired)
	hash := hashCommand(t, command)
	var wg sync.WaitGroup
	statuses := make([]CommandStatus, 8)
	errorsFound := make([]error, len(statuses))
	for i := range statuses {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			statuses[i], errorsFound[i] = x.f.s.sweepAnonymousBatch(context.Background(), command, hash, x.f.peer, x.f.now, claimTestID(120+i), signWith(x.f.key))
		}(i)
	}
	wg.Wait()
	for i := range statuses {
		if errorsFound[i] != nil || !bytes.Equal(statuses[i].Receipt, statuses[0].Receipt) || !bytes.Equal(statuses[i].SignedReceipt, statuses[0].SignedReceipt) {
			t.Fatalf("concurrent replay %d: %v", i, errorsFound[i])
		}
	}
	x.next++
	for i := range statuses {
		fresh := x.command(71+i, x.acquired)
		fresh.EnvironmentSequence += uint64(i)
		freshHash := hashCommand(t, fresh)
		wg.Add(1)
		go func(i int, fresh operation.Command, hash string) {
			defer wg.Done()
			deadline := time.Now().Add(5 * time.Second)
			for {
				status, err := x.f.s.sweepAnonymousBatch(context.Background(), fresh, hash, x.f.peer, x.f.now, "", signWith(x.f.key))
				if errors.Is(err, ErrPending) && time.Now().Before(deadline) {
					time.Sleep(time.Millisecond)
					continue
				}
				statuses[i], errorsFound[i] = status, err
				return
			}
		}(i, fresh, freshHash)
	}
	wg.Wait()
	for i := range statuses {
		if errorsFound[i] != nil {
			t.Fatal(errorsFound[i])
		}
		sweepTestResult(t, statuses[i], "already-swept", "")
	}
	var count int
	if err := x.f.s.db.QueryRow(`SELECT count(*) FROM authority_events WHERE command_id IN (SELECT command_id FROM submissions WHERE operation_name='batch.sweep-anonymous')`).Scan(&count); err != nil || count != 1 {
		t.Fatalf("concurrent event count = %d, %v", count, err)
	}
}

func TestBatchSweepConcurrentFreshIDsAtMostOnce(t *testing.T) {
	x := newSweepFixture(t, false, false, false)
	before := x.f.anchor(t)
	var wg sync.WaitGroup
	statuses, failures := make([]CommandStatus, 8), make([]error, 8)
	for i := range statuses {
		command := x.command(70+i, x.birth)
		command.EnvironmentSequence += uint64(i)
		hash := hashCommand(t, command)
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			deadline := time.Now().Add(5 * time.Second)
			for {
				status, err := x.f.s.sweepAnonymousBatch(context.Background(), command, hash, x.f.peer, x.f.now, claimTestID(120+i), signWith(x.f.key))
				if errors.Is(err, ErrPending) && time.Now().Before(deadline) {
					time.Sleep(time.Millisecond)
					continue
				}
				statuses[i], failures[i] = status, err
				return
			}
		}(i)
	}
	wg.Wait()
	for i, status := range statuses {
		if failures[i] != nil {
			t.Fatal(failures[i])
		}
		outcome := "already-swept"
		if i == 0 {
			outcome = "swept"
		}
		sweepTestResult(t, status, outcome, "")
	}
	if x.f.anchor(t).EventCount != before.EventCount+1 {
		t.Fatal("concurrent fresh IDs duplicated the sweep")
	}
	if err := checkSchema(x.f.s.db); err != nil {
		t.Fatal(err)
	}
}

func TestBatchSweepRollbackAndAuthentication(t *testing.T) {
	x := newSweepFixture(t, false, false, false)
	command := x.command(80, x.acquired)
	hash := hashCommand(t, command)
	before := x.f.anchor(t)
	if _, err := x.f.s.sweepAnonymousBatch(context.Background(), command, hash, tls.ConnectionState{}, x.f.now, claimTestID(120), signWith(x.f.key)); err == nil {
		t.Fatal("unauthenticated sweep accepted")
	}
	failure := errors.New("signing failed")
	for _, signer := range []Signer{
		func(context.Context, []byte) ([]byte, error) { return nil, failure },
		func(_ context.Context, raw []byte) ([]byte, error) { return ed25519.Sign(key("wrong"), raw), nil },
	} {
		if _, err := x.f.s.sweepAnonymousBatch(context.Background(), command, hash, x.f.peer, x.f.now, claimTestID(120), signer); err == nil {
			t.Fatal("bad signer committed")
		}
		if x.f.anchor(t) != before {
			t.Fatal("signing failure leaked event")
		}
	}
	if _, err := x.f.s.db.Exec(`CREATE TRIGGER sweep_test_failure BEFORE UPDATE OF sequence_head ON environments BEGIN SELECT RAISE(ABORT,'injected failure'); END`); err != nil {
		t.Fatal(err)
	}
	if _, err := x.f.s.sweepAnonymousBatch(context.Background(), command, hash, x.f.peer, x.f.now, claimTestID(120), signWith(x.f.key)); err == nil {
		t.Fatal("transaction failure committed")
	}
	if _, err := x.f.s.db.Exec(`DROP TRIGGER sweep_test_failure`); err != nil {
		t.Fatal(err)
	}
	var count int
	if err := x.f.s.db.QueryRow(`SELECT count(*) FROM submissions WHERE command_id=?`, command.ID).Scan(&count); err != nil || count != 0 || x.f.anchor(t) != before {
		t.Fatalf("rollback retained identity/effect: %d, %v", count, err)
	}
	if _, err := x.f.s.db.Exec(`CREATE TRIGGER sweep_boundary_failure BEFORE INSERT ON batch_sweep_boundaries BEGIN SELECT RAISE(ABORT,'injected boundary failure'); END`); err != nil {
		t.Fatal(err)
	}
	if _, err := x.f.s.sweepAnonymousBatch(context.Background(), command, hash, x.f.peer, x.f.now, claimTestID(120), signWith(x.f.key)); err == nil {
		t.Fatal("boundary failure committed signed receipt/effect")
	}
	if _, err := x.f.s.db.Exec(`DROP TRIGGER sweep_boundary_failure`); err != nil {
		t.Fatal(err)
	}
	for _, table := range []string{"submissions", "terminal_receipts", "batch_sweep_boundaries"} {
		if err := x.f.s.db.QueryRow(`SELECT count(*) FROM `+table+` WHERE command_id=?`, command.ID).Scan(&count); err != nil || count != 0 {
			t.Fatalf("rollback retained %s: %d, %v", table, count, err)
		}
	}
	if err := checkSchema(x.f.s.db); err != nil || x.f.anchor(t) != before {
		t.Fatalf("rollback left invalid artifacts/sequence/prefix: %v", err)
	}
	status, err := x.f.s.sweepAnonymousBatch(context.Background(), command, hash, x.f.peer, x.f.now, claimTestID(120), signWith(x.f.key))
	if err != nil {
		t.Fatal(err)
	}
	sweepTestResult(t, status, "swept", "")
}

// Rewrite only the candidate event, preserving deterministic CBOR and prefix
// hashing. Reopen must reject semantic corruption, not merely a stale digest.
func TestBatchSweepHistoryRejectsCorruptEvent(t *testing.T) {
	for _, field := range []string{"subject_id", "payload", "request_hash", "kind"} {
		t.Run(field, func(t *testing.T) {
			x := newSweepFixture(t, false, false, false)
			command := x.command(90, x.acquired)
			if _, err := x.f.s.sweepAnonymousBatch(context.Background(), command, hashCommand(t, command), x.f.peer, x.f.now, claimTestID(120), signWith(x.f.key)); err != nil {
				t.Fatal(err)
			}
			if err := x.f.s.Close(); err != nil {
				t.Fatal(err)
			}
			rewriteGateAuthorityEvent(t, x.f.root, claimTestID(120), func(fields map[string]cbor.RawMessage) error {
				value := any(claimTestID(999))
				if field == "payload" {
					value = map[string]any{"unexpected": true}
				} else if field == "request_hash" {
					value = digestBytes([]byte("other"))
				} else if field == "kind" {
					value = "batch.dismissed"
				}
				return setGateEventField(fields, field, value)
			})
			store, err := OpenExisting(x.f.root)
			if store != nil {
				_ = store.Close()
			}
			if !errors.Is(err, ErrInvalidStore) {
				t.Fatalf("corrupt sweep reopened: %v", err)
			}
		})
	}
}
