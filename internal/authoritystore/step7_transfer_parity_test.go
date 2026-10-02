package authoritystore

import (
	"bytes"
	"context"
	"reflect"
	"testing"
	"time"
)

type step7ReceiptSnapshot struct {
	domain, command, requestHash, state string
	receipt                             []byte
}

func readStep7ReceiptSnapshot(t *testing.T, s *Store) []step7ReceiptSnapshot {
	t.Helper()
	rows, err := s.db.Query(`SELECT s.domain_id,s.command_id,s.request_hash,s.state,coalesce(r.receipt,X'')
		FROM submissions s LEFT JOIN terminal_receipts r USING(domain_id,command_id)
		ORDER BY s.domain_id,s.command_id`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var out []step7ReceiptSnapshot
	for rows.Next() {
		var row step7ReceiptSnapshot
		if err = rows.Scan(&row.domain, &row.command, &row.requestHash, &row.state, &row.receipt); err != nil {
			t.Fatal(err)
		}
		out = append(out, row)
	}
	if err = rows.Err(); err != nil {
		t.Fatal(err)
	}
	return out
}

func assertStep7DetachedRepairState(t *testing.T, s *Store, fixture repairTerminalFixture) gateExemptionRepairTerminal {
	t.Helper()
	var proof, nonce, reservedNonce []byte
	var verifiedAt, reservedCommand string
	if err := s.db.QueryRow(`SELECT a.proof,a.nonce,a.verified_at,n.nonce,n.command_id
		FROM gate_exemption_repair_admissions a JOIN gate_exemption_repair_nonces n
		USING(domain_id,command_id) WHERE a.domain_id=? AND a.command_id=?`,
		fixture.command.DomainID, fixture.command.ID).Scan(&proof, &nonce, &verifiedAt, &reservedNonce, &reservedCommand); err != nil {
		t.Fatalf("read detached repair admission and nonce: %v", err)
	}
	if !bytes.Equal(proof, fixture.proof) || !bytes.Equal(nonce, reservedNonce) || len(nonce) != 16 ||
		verifiedAt != fixture.f.now.UTC().Format(time.RFC3339Nano) || reservedCommand != fixture.command.ID {
		t.Fatalf("detached repair admission changed across transfer/recovery: proof=%t nonce=%x reserved=%x verified_at=%q command=%q",
			bytes.Equal(proof, fixture.proof), nonce, reservedNonce, verifiedAt, reservedCommand)
	}
	terminal, err := readGateExemptionRepairTerminal(context.Background(), s.db, fixture.command.DomainID, fixture.command.ID)
	if err != nil {
		t.Fatalf("read private repair terminal: %v", err)
	}
	recovered, err := s.recoverGateExemptionRepair(context.Background(), fixture.canonical, fixture.hash, nil)
	if err != nil || recovered.Terminal == nil || !sameGateExemptionRepairTerminal(terminal, *recovered.Terminal) ||
		!bytes.Equal(recovered.Proof, fixture.proof) || recovered.VerifiedAt != fixture.f.now.UTC().Format(time.RFC3339Nano) {
		t.Fatalf("recover detached repair admission: terminal=%+v recovered=%+v err=%v", terminal, recovered, err)
	}
	return terminal
}

func TestStep7SeedPullReopenPreservesHistoryReceiptsAndDetachedRepair(t *testing.T) {
	fixture := prepareRepairTerminalFixture(t, "success-missing-snapshot", 0x63)
	admitRepairTerminalFixture(t, fixture)
	terminal, err := fixture.f.s.completeGateExemptionRepair(context.Background(), fixture.canonical, fixture.hash,
		fixture.proof, []byte(claimTestID(705)), fixture.f.now)
	if err != nil || terminal.ResultCode != "result.succeeded" || terminal.EventID == "" {
		t.Fatalf("complete repair before seed/pull: terminal=%+v err=%v", terminal, err)
	}
	start := PrefixAnchor{EventCount: fixture.command.Boundary.EventCount,
		EventID: fixture.command.Boundary.HighWaterEvent, Digest: fixture.command.Boundary.PrefixDigest}
	end := fixture.f.anchor(t)
	projection, err := readStep13Projection(fixture.f.s.db)
	if err != nil {
		t.Fatal(err)
	}
	receipts := readStep7ReceiptSnapshot(t, fixture.f.s)
	storedTerminal := assertStep7DetachedRepairState(t, fixture.f.s, fixture)
	if !sameGateExemptionRepairTerminal(terminal, storedTerminal) || end.EventCount <= start.EventCount {
		t.Fatalf("unexpected pre-transfer terminal/prefix: terminal=%+v stored=%+v start=%+v end=%+v",
			terminal, storedTerminal, start, end)
	}

	ctx := context.Background()
	seedID, seedTransferID := claimTestID(700), claimTestID(701)
	seed, err := fixture.f.s.StartTransfer(ctx, fixture.command.DomainID, fixture.command.AuthorityEpoch,
		"seed", "wipd.store/1", emptyAnchor(), seedID, seedTransferID, fixture.f.now)
	if err != nil || seed.Snapshot.Delta.End != end || seed.EventCount != uint64(len(seed.Snapshot.Delta.Events)) ||
		seed.EventCount == 0 {
		t.Fatalf("pin seed at exact repair terminal prefix: snapshot=%+v end=%+v err=%v", seed.Snapshot.Delta, end, err)
	}
	for _, record := range seed.Snapshot.Delta.Events {
		if bytes.Contains(record.Record, fixture.proof) {
			t.Fatalf("detached proof bytes were embedded in transferred authority event %s", record.EventID)
		}
	}

	// Consume and acknowledge one exact event, then restart mid-transfer. The
	// remaining event and manifest boundaries must resume without loss or
	// replaying a second receipt/candidate projection.
	first, err := fixture.f.s.NextTransfer(ctx, seed.Token, 1<<20, fixture.f.now)
	if err != nil || first.Event == nil || len(seed.Snapshot.Delta.Events) == 0 ||
		first.Event.EventID != seed.Snapshot.Delta.Events[0].EventID ||
		!bytes.Equal(first.Event.Record, seed.Snapshot.Delta.Events[0].Record) {
		t.Fatalf("read exact first seed event: boundary=%+v err=%v", first, err)
	}
	token, err := fixture.f.s.AcknowledgeTransfer(ctx, seed.Token, first.Anchor.Digest, fixture.f.now)
	if err != nil {
		t.Fatalf("acknowledge first seed event: %v", err)
	}
	root := fixture.f.root
	if err = fixture.f.s.Close(); err != nil {
		t.Fatal(err)
	}
	reopened, err := OpenExisting(root)
	if err != nil {
		t.Fatalf("reopen during seed transfer: %v", err)
	}
	fixture.f.s = reopened
	seen := []PrefixRecord{*first.Event}
	for steps := 0; steps < 1000; steps++ {
		boundary, nextErr := reopened.NextTransfer(ctx, token, 1<<20, fixture.f.now)
		if nextErr != nil {
			t.Fatalf("continue seed transfer: %v", nextErr)
		}
		if boundary.Event != nil {
			seen = append(seen, *boundary.Event)
			token, nextErr = reopened.AcknowledgeTransfer(ctx, token, boundary.Anchor.Digest, fixture.f.now)
		} else if boundary.Entry != nil {
			token, nextErr = reopened.AcknowledgeTransfer(ctx, token, boundary.Entry.Digest, fixture.f.now)
		} else if boundary.Complete {
			if len(seen) != len(seed.Snapshot.Delta.Events) {
				t.Fatalf("seed omitted events: got=%d want=%d", len(seen), len(seed.Snapshot.Delta.Events))
			}
			for index := range seen {
				if seen[index].EventID != seed.Snapshot.Delta.Events[index].EventID ||
					!bytes.Equal(seen[index].Record, seed.Snapshot.Delta.Events[index].Record) {
					t.Fatalf("seed event %d changed: got=%+v want=%+v", index, seen[index], seed.Snapshot.Delta.Events[index])
				}
			}
			break
		} else {
			t.Fatalf("seed produced neither a boundary nor completion: %+v", boundary)
		}
		if nextErr != nil {
			t.Fatalf("acknowledge continued seed boundary: %v", nextErr)
		}
		if steps == 999 {
			t.Fatal("seed transfer exceeded the event/manifest boundary limit")
		}
	}

	if got := readStep7ReceiptSnapshot(t, reopened); !reflect.DeepEqual(got, receipts) {
		t.Fatalf("seed/reopen duplicated or lost command/receipt status: got=%+v want=%+v", got, receipts)
	}
	if got := assertStep7DetachedRepairState(t, reopened, fixture); !sameGateExemptionRepairTerminal(got, storedTerminal) {
		t.Fatalf("reopen changed private repair terminal: got=%+v want=%+v", got, storedTerminal)
	}
	if err = rebuildStep13Projection(reopened.db); err != nil {
		t.Fatalf("rebuild Step 13 projections after seed/reopen: %v", err)
	}
	after, err := readStep13Projection(reopened.db)
	if err != nil || !sameStep13Projection(after, projection) || checkStep13State(reopened.db) != nil {
		t.Fatalf("seed/reopen/rebuild changed Step 13 projections: before=%+v after=%+v err=%v", projection, after, err)
	}

	pull, err := reopened.StartTransfer(ctx, fixture.command.DomainID, fixture.command.AuthorityEpoch,
		"pull", "wipd.store/1", start, claimTestID(702), claimTestID(703), fixture.f.now)
	if err != nil || pull.EventCount != 1 || len(pull.Snapshot.Delta.Events) != 1 ||
		pull.Snapshot.Delta.Events[0].EventID != terminal.EventID ||
		!bytes.Equal(pull.Snapshot.Delta.Events[0].Record, seed.Snapshot.Delta.Events[len(seed.Snapshot.Delta.Events)-1].Record) {
		t.Fatalf("pull from signed declaration boundary did not preserve the repair event: snapshot=%+v err=%v", pull.Snapshot.Delta, err)
	}
	pulled, err := reopened.NextTransfer(ctx, pull.Token, 1<<20, fixture.f.now)
	if err != nil || pulled.Event == nil || pulled.Event.EventID != terminal.EventID ||
		!bytes.Equal(pulled.Event.Record, pull.Snapshot.Delta.Events[0].Record) {
		t.Fatalf("read exact pulled repair event: boundary=%+v err=%v", pulled, err)
	}
	if _, err = reopened.AcknowledgeTransfer(ctx, pull.Token, pulled.Anchor.Digest, fixture.f.now); err != nil {
		t.Fatalf("acknowledge pulled repair event: %v", err)
	}
	if got := readStep7ReceiptSnapshot(t, reopened); !reflect.DeepEqual(got, receipts) {
		t.Fatalf("pull duplicated or lost command/receipt status: got=%+v want=%+v", got, receipts)
	}

	// A duplicate transfer identity must roll back the snapshot pin created
	// earlier in StartTransfer's transaction.
	failedSnapshotID := claimTestID(704)
	if _, err = reopened.StartTransfer(ctx, fixture.command.DomainID, fixture.command.AuthorityEpoch,
		"seed", "wipd.store/1", emptyAnchor(), failedSnapshotID, seedTransferID, fixture.f.now); err == nil {
		t.Fatal("duplicate transfer identity unexpectedly succeeded")
	}
	var failedSnapshotCount int
	if err = reopened.db.QueryRow(`SELECT count(*) FROM snapshots WHERE snapshot_id=?`, failedSnapshotID).Scan(&failedSnapshotCount); err != nil || failedSnapshotCount != 0 {
		t.Fatalf("failed transfer did not atomically roll back its snapshot: count=%d err=%v", failedSnapshotCount, err)
	}
}
