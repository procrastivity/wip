package authoritystore

import (
	"bytes"
	"context"
	"errors"
	"testing"
	"time"

	"github.com/procrastivity/wip/internal/operation"
)

func publicRepairCommand(t *testing.T, fixture repairTerminalFixture) operation.Command {
	t.Helper()
	command, err := operation.DecodeCanonicalCommand(fixture.canonical)
	if err != nil {
		t.Fatal(err)
	}
	canonical, err := command.CanonicalBytes()
	hash, hashErr := command.RequestHash()
	if err != nil || hashErr != nil || !bytes.Equal(canonical, fixture.canonical) || hash != fixture.hash {
		t.Fatalf("public/private canonical identity differs: %v %v %s", err, hashErr, hash)
	}
	return command
}

func TestRepairPublicReceiptQueryRestartAndExactAcknowledgement(t *testing.T) {
	for _, scenario := range []string{"success-missing-snapshot", "done-after-boundary"} {
		t.Run(scenario, func(t *testing.T) {
			fixture := prepareRepairTerminalFixture(t, scenario, 0x81)
			f := fixture.f
			command := publicRepairCommand(t, fixture)
			ctx := context.Background()
			before := f.anchor(t)
			if _, err := f.s.SubmitCommand(ctx, command, fixture.hash, f.peer, f.now); !errors.Is(err, ErrInvalidProof) {
				t.Fatalf("generic/v1 repair admission: %v", err)
			}
			if _, err := f.s.SubmitGateExemptionRepairV2(ctx, command, fixture.hash, nil, f.peer, f.now, time.Time{}); !errors.Is(err, ErrInvalidProof) {
				t.Fatalf("fresh v2 admission without proof: %v", err)
			}
			var reservations int
			if err := f.s.db.QueryRow(`SELECT count(*) FROM gate_exemption_repair_admissions`).Scan(&reservations); err != nil || reservations != 0 {
				t.Fatalf("v1 reserved repair authorization: %d %v", reservations, err)
			}
			pending, err := f.s.SubmitGateExemptionRepairV2(ctx, command, fixture.hash, fixture.proof, f.peer, f.now, time.Time{})
			if err != nil || !pending.Pending || pending.Owner != nil {
				t.Fatalf("public repair admission: %+v %v", pending, err)
			}
			var journalPosition uint64
			if err = f.s.db.QueryRow(`SELECT position FROM claim_journal_entries WHERE domain_id=? AND command_id=?`, domainA, command.ID).Scan(&journalPosition); err != nil || journalPosition == 0 {
				t.Fatalf("atomic public journal linkage: %d %v", journalPosition, err)
			}
			// Lose the admission response and restart before terminalization.
			if err = f.s.Close(); err != nil {
				t.Fatal(err)
			}
			f.s, err = OpenExisting(f.root)
			if err != nil {
				t.Fatalf("pending public repair reopen: %v", err)
			}
			t.Cleanup(func() { _ = f.s.Close() })
			later := f.now.Add(5 * time.Minute)
			for _, proof := range [][]byte{nil, fixture.proof} {
				replay, replayErr := f.s.SubmitGateExemptionRepairV2(ctx, command, fixture.hash, proof, f.peer, later, time.Time{})
				if replayErr != nil || !replay.Pending {
					t.Fatalf("admitted proof expiry replaced pending status: %+v %v", replay, replayErr)
				}
			}
			if _, err = f.s.SubmitGateExemptionRepairV2(ctx, command, fixture.hash, []byte{1, 2, 3}, f.peer, later, time.Time{}); !errors.Is(err, ErrConflict) {
				t.Fatalf("substituted proof replay: %v", err)
			}
			changed := command
			input := changed.Request.Input.(operation.GateExemptionRepairInput)
			input.Reason += " changed"
			changed.Request.Input = input
			changedHash, err := changed.RequestHash()
			if err != nil {
				t.Fatal(err)
			}
			if _, err = f.s.SubmitGateExemptionRepairV2(ctx, changed, changedHash, nil, f.peer, later, time.Time{}); !errors.Is(err, ErrConflict) {
				t.Fatalf("substituted canonical identity replay: %v", err)
			}
			changed.Request.Operation.Version = 2
			if _, err = f.s.SubmitGateExemptionRepairV2(ctx, changed, changedHash, nil, f.peer, later, time.Time{}); !errors.Is(err, ErrInvalidProof) {
				t.Fatalf("unnegotiated repair version replay: %v", err)
			}
			// Signing failure rolls back the event, witness, receipt and sequence,
			// but never frees or replaces the already admitted authorization.
			signingFailure := errors.New("lost signer")
			if _, err = f.s.CompleteGateExemptionRepairV2(ctx, command, fixture.hash, nil, claimTestID(120), later,
				func(context.Context, []byte) ([]byte, error) { return nil, signingFailure }); !errors.Is(err, signingFailure) {
				t.Fatalf("signing failure: %v", err)
			}
			if anchor := f.anchor(t); anchor != before {
				t.Fatal("signing failure retained a partial event")
			}
			pending, err = f.s.QueryCommand(ctx, domainA, command.ID, fixture.hash, 7, f.peer, envA, later)
			if err != nil || !pending.Pending {
				t.Fatalf("signing failure destroyed durable retry: %+v %v", pending, err)
			}
			terminal, err := f.s.CompleteGateExemptionRepairV2(ctx, command, fixture.hash, nil, claimTestID(120), later, signWith(f.key))
			if err != nil || terminal.Pending || len(terminal.SignedReceipt) == 0 {
				t.Fatalf("public repair completion: %+v %v", terminal, err)
			}
			receipt, err := readReceipt(terminal.Receipt)
			if err != nil {
				t.Fatal(err)
			}
			if scenario == "success-missing-snapshot" {
				claimTestReceipt(t, terminal, "result.succeeded", map[string]any{"gate": "repair-target", "node_id": fixture.command.NodeID, "already_exempt": false}, 120)
			} else if receipt.Result.Code != "result.refused" || receipt.Result.Problem == nil || *receipt.Result.Problem != "refusal.gate-repair-lifecycle" || receipt.Range != nil {
				t.Fatalf("factual refusal receipt: %+v", receipt)
			}
			// A well-formed substituted receipt must not acknowledge the journal.
			var fields map[string]any
			if err = artifactDecoder.Unmarshal(terminal.Receipt, &fields); err != nil {
				t.Fatal(err)
			}
			fields["command_id"] = claimTestID(121)
			anchor := f.anchor(t)
			if err = f.s.AcknowledgeClaimJournalEntry(ctx, domainA, fixture.claim.JournalID, journalPosition, encodeTest(t, fields), anchor); !errors.Is(err, ErrInvalidProof) {
				t.Fatalf("substituted receipt ACK: %v", err)
			}
			identity, err := f.s.GetCurrentClaimJournal(ctx, domainA, 7, envA, fixture.claim.ClaimID, 1, f.matter, claimTestID(51))
			if err != nil {
				t.Fatal(err)
			}
			for attempt := 0; attempt < 2; attempt++ {
				if err = f.s.AcknowledgeOwnedClaimJournalEntry(ctx, identity, journalPosition, terminal.Receipt, anchor); err != nil {
					t.Fatalf("exact owned receipt ACK attempt %d: %v", attempt, err)
				}
				if err = f.s.AcknowledgeOwnedClaimJournalEntry(ctx, identity, journalPosition, encodeTest(t, fields), anchor); !errors.Is(err, ErrInvalidProof) {
					t.Fatalf("substituted owned receipt ACK attempt %d: %v", attempt, err)
				}
			}
			var state string
			if err = f.s.db.QueryRow(`SELECT state FROM claim_journal_entries WHERE domain_id=? AND command_id=?`, domainA, command.ID).Scan(&state); err != nil || state != map[bool]string{true: "terminal", false: "quarantined"}[receipt.Result.Code == "result.succeeded"] {
				t.Fatalf("exact receipt ACK state: %s %v", state, err)
			}
			// Lose the terminal response and restart; query and both replay forms
			// must return the original exact signed receipt and authorization time.
			if err = f.s.Close(); err != nil {
				t.Fatal(err)
			}
			f.s, err = OpenExisting(f.root)
			if err != nil {
				t.Fatalf("terminal public repair reopen: %v", err)
			}
			query, err := f.s.QueryCommand(ctx, domainA, command.ID, fixture.hash, 7, f.peer, envA, later)
			if err != nil || query.Pending || !bytes.Equal(query.Receipt, terminal.Receipt) || !bytes.Equal(query.SignedReceipt, terminal.SignedReceipt) {
				t.Fatalf("public query/restart changed terminal: %+v %v", query, err)
			}
			for _, proof := range [][]byte{nil, fixture.proof} {
				replay, replayErr := f.s.SubmitGateExemptionRepairV2(ctx, command, fixture.hash, proof, f.peer, later, time.Time{})
				if replayErr != nil || replay.Pending || !bytes.Equal(replay.Receipt, terminal.Receipt) {
					t.Fatalf("terminal exact replay: %+v %v", replay, replayErr)
				}
			}
			admitted, err := readGateExemptionRepairAdmission(ctx, f.s.db, domainA, command.ID)
			if err != nil || !bytes.Equal(admitted.Proof, fixture.proof) || admitted.VerifiedAt != f.now.UTC().Format(time.RFC3339Nano) || !bytes.Equal(admitted.Command, fixture.canonical) {
				t.Fatalf("retained authorization differs: %+v %v", admitted, err)
			}
		})
	}
}

func TestRepairPrivateHistoryCannotBecomePublicByReplay(t *testing.T) {
	fixture := prepareRepairTerminalFixture(t, "done-after-boundary", 0x82)
	admitRepairTerminalFixture(t, fixture)
	command := publicRepairCommand(t, fixture)
	if _, err := fixture.f.s.SubmitGateExemptionRepairV2(context.Background(), command, fixture.hash, nil, fixture.f.peer, fixture.f.now, time.Time{}); !errors.Is(err, ErrConflict) {
		t.Fatalf("private admission minted public identity: %v", err)
	}
	if _, err := fixture.f.s.QueryCommand(context.Background(), domainA, command.ID, fixture.hash, 7, fixture.f.peer, envA, fixture.f.now); !errors.Is(err, ErrNotFound) {
		t.Fatalf("private admission became public queryable: %v", err)
	}
}

func TestRepairPublicAlreadyExemptNoEventReceiptAndJournal(t *testing.T) {
	fixture := prepareRepairTerminalFixture(t, "success-missing-snapshot", 0x83)
	admitRepairTerminalFixture(t, fixture)
	f := fixture.f
	ctx := context.Background()
	if _, err := f.s.completeGateExemptionRepair(ctx, fixture.canonical, fixture.hash, nil, []byte(claimTestID(120)), f.now); err != nil {
		t.Fatal(err)
	}
	// A different authorized identity sees the repaired exemption. Its
	// declaration boundary remains the original missing-snapshot declaration.
	fixture.command.ID, fixture.command.CorrelationID = claimTestID(21), claimTestID(21)
	fixture.command.EnvironmentSequence++
	fixture.canonical, fixture.hash, fixture.proof = signRepairAdmissionProof(t, fixture.command, false,
		f.now.Add(-time.Minute), f.now.Add(time.Minute), bytes.Repeat([]byte{0x84}, 16), nil)
	command := publicRepairCommand(t, fixture)
	before := f.anchor(t)
	if pending, err := f.s.SubmitGateExemptionRepairV2(ctx, command, fixture.hash, fixture.proof, f.peer, f.now, time.Time{}); err != nil || !pending.Pending {
		t.Fatalf("already-exempt admission: %+v %v", pending, err)
	}
	terminal, err := f.s.CompleteGateExemptionRepairV2(ctx, command, fixture.hash, nil, "", f.now, signWith(f.key))
	if err != nil || terminal.Pending {
		t.Fatalf("already-exempt completion: %+v %v", terminal, err)
	}
	receipt, err := readReceipt(terminal.Receipt)
	if err != nil || receipt.Range != nil || receipt.Result.Code != "result.succeeded" || receipt.Result.Problem != nil ||
		!bytes.Equal(receipt.Result.Output, encodeTest(t, map[string]any{"gate": "repair-target", "node_id": fixture.command.NodeID, "already_exempt": true})) || f.anchor(t) != before {
		t.Fatalf("already-exempt changed closed receipt or event prefix: %+v %v", receipt, err)
	}
	var position uint64
	if err = f.s.db.QueryRow(`SELECT position FROM claim_journal_entries WHERE domain_id=? AND command_id=?`, domainA, command.ID).Scan(&position); err != nil {
		t.Fatal(err)
	}
	claimTestAcknowledge(t, f, fixture.claim.JournalID, position, terminal)
	if err = f.s.Close(); err != nil {
		t.Fatal(err)
	}
	f.s, err = OpenExisting(f.root)
	if err != nil {
		t.Fatalf("public no-event receipt reopen: %v", err)
	}
	t.Cleanup(func() { _ = f.s.Close() })
	query, err := f.s.QueryCommand(ctx, domainA, command.ID, fixture.hash, 7, f.peer, envA, f.now.Add(5*time.Minute))
	if err != nil || query.Pending || !bytes.Equal(query.Receipt, terminal.Receipt) || !bytes.Equal(query.SignedReceipt, terminal.SignedReceipt) {
		t.Fatalf("public no-event query/restart: %+v %v", query, err)
	}
}
