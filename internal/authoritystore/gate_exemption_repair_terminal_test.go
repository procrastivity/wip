package authoritystore

import (
	"bytes"
	"context"
	"database/sql"
	"errors"
	"fmt"
	"path/filepath"
	"testing"
	"time"

	"github.com/fxamacker/cbor/v2"
	"github.com/procrastivity/wip/internal/operation"
)

type repairTerminalFixture struct {
	f         *claimTestFixture
	claim     AcquireAllocation
	command   gateExemptionRepairCommand
	canonical []byte
	hash      string
	proof     []byte
}

func sameGateExemptionRepairTerminal(left, right gateExemptionRepairTerminal) bool {
	return left.DomainID == right.DomainID && left.CommandID == right.CommandID && left.RequestHash == right.RequestHash &&
		left.ResultCode == right.ResultCode && left.RefusalCode == right.RefusalCode && left.RefusalMessage == right.RefusalMessage &&
		left.ObservedPosition == right.ObservedPosition && left.ObservedEventID == right.ObservedEventID &&
		left.ObservedPrefixDigest == right.ObservedPrefixDigest && left.EventID == right.EventID && left.OccurredAt == right.OccurredAt &&
		bytes.Equal(left.BoundaryWitness, right.BoundaryWitness)
}

func completeRepairGateDeclaration(t *testing.T, f *claimTestFixture, claim AcquireAllocation, id int, sequence uint64, gate, scale string, event, journal int) {
	t.Helper()
	command := step12Command(f, id, sequence, operation.GateDeclareV1,
		operation.GateDeclareInput{Gate: gate, Scale: scale}, claim.ClaimID)
	owner := submitGateOperation(t, f, command)
	completed, err := f.s.CompleteCommand(context.Background(), owner, operation.Result{Code: operation.ResultSucceeded},
		repoA, claimTestID(event), f.now, signWith(f.key))
	if err != nil {
		t.Fatalf("complete %s declaration: %v", gate, err)
	}
	claimTestAcknowledge(t, f, claim.JournalID, uint64(journal), completed)
}

func completeRepairGateClose(t *testing.T, f *claimTestFixture, claim AcquireAllocation, id int, sequence uint64, gate, node string, event, journal int) {
	t.Helper()
	command := step12Command(f, id, sequence, operation.GateCloseV1,
		operation.GateCloseInput{Gate: gate, NodeID: node}, claim.ClaimID)
	owner := submitGateOperation(t, f, command)
	completed, err := f.s.CompleteCommand(context.Background(), owner, operation.Result{Code: operation.ResultSucceeded},
		node, claimTestID(event), f.now, signWith(f.key))
	if err != nil {
		t.Fatalf("close %s on %s: %v", gate, node, err)
	}
	claimTestAcknowledge(t, f, claim.JournalID, uint64(journal), completed)
}

func completeRepairLifecycle(t *testing.T, f *claimTestFixture, claim AcquireAllocation, id int, sequence uint64,
	definition operation.Definition, input operation.Input, events []int, journal int,
) CommandStatus {
	t.Helper()
	command := step12Command(f, id, sequence, definition, input, claim.ClaimID)
	ids := make([]string, len(events))
	for index, event := range events {
		ids[index] = claimTestID(event)
	}
	completed := completeLifecycleCommand(t, f, command, ids)
	if definition.Metadata().Delivery == operation.DeliveryClaim {
		claimTestAcknowledge(t, f, claim.JournalID, uint64(journal), completed)
	}
	return completed
}

func prepareRepairTerminalFixture(t *testing.T, scenario string, nonceByte byte) repairTerminalFixture {
	t.Helper()
	f, claim := gateClaimedFixture(t)
	var boundary PrefixAnchor
	var target string
	var commandID, nextSequence int
	switch scenario {
	case "success-missing-snapshot":
		completeRepairGateDeclaration(t, f, claim, 12, 3, "own-pre", "matter", 104, 1)
		completeRepairLifecycle(t, f, claim, 13, 4, operation.MatterStartV1,
			operation.NodeLifecycleInput{NodeID: f.matter}, []int{105}, 2)
		completeRepairLifecycle(t, f, claim, 14, 5, operation.MatterFinishV1,
			operation.MatterFinishInput{MatterID: f.matter}, []int{106}, 3)
		completeRepairGateClose(t, f, claim, 15, 6, "own-pre", f.matter, 107, 3)
		completeRepairGateDeclaration(t, f, claim, 16, 7, "repair-target", "matter", 108, 4)
		boundary = f.anchor(t)
		target, commandID, nextSequence = f.matter, 20, 8
	case "done-after-boundary":
		completeRepairGateDeclaration(t, f, claim, 12, 3, "repair-target", "matter", 104, 1)
		boundary = f.anchor(t)
		completeRepairLifecycle(t, f, claim, 13, 4, operation.MatterStartV1,
			operation.NodeLifecycleInput{NodeID: f.matter}, []int{105}, 2)
		completeRepairLifecycle(t, f, claim, 14, 5, operation.MatterFinishV1,
			operation.MatterFinishInput{MatterID: f.matter}, []int{106}, 3)
		target, commandID, nextSequence = f.matter, 20, 6
	case "own-open-current", "own-closed-after-boundary":
		completeRepairGateDeclaration(t, f, claim, 12, 3, "own-pre", "matter", 104, 1)
		completeRepairLifecycle(t, f, claim, 13, 4, operation.MatterStartV1,
			operation.NodeLifecycleInput{NodeID: f.matter}, []int{105}, 2)
		completeRepairLifecycle(t, f, claim, 14, 5, operation.MatterFinishV1,
			operation.MatterFinishInput{MatterID: f.matter}, []int{106}, 3)
		completeRepairGateDeclaration(t, f, claim, 15, 6, "repair-target", "matter", 107, 3)
		boundary = f.anchor(t)
		if scenario == "own-closed-after-boundary" {
			completeRepairGateClose(t, f, claim, 16, 7, "own-pre", f.matter, 108, 4)
			nextSequence = 8
		} else {
			nextSequence = 7
		}
		target, commandID = f.matter, 20
	case "lifecycle-open-at-boundary":
		completeRepairGateDeclaration(t, f, claim, 12, 3, "own-pre", "matter", 104, 1)
		completeRepairLifecycle(t, f, claim, 13, 4, operation.MatterStartV1,
			operation.NodeLifecycleInput{NodeID: f.matter}, []int{105}, 2)
		completeRepairGateDeclaration(t, f, claim, 14, 5, "repair-target", "matter", 106, 3)
		boundary = f.anchor(t)
		target, commandID, nextSequence = f.matter, 20, 6
	case "two-open-own-gates":
		completeRepairGateDeclaration(t, f, claim, 12, 3, "own-z", "matter", 104, 1)
		completeRepairGateDeclaration(t, f, claim, 13, 4, "own-a", "matter", 105, 2)
		completeRepairLifecycle(t, f, claim, 14, 5, operation.MatterStartV1,
			operation.NodeLifecycleInput{NodeID: f.matter}, []int{106}, 3)
		completeRepairLifecycle(t, f, claim, 15, 6, operation.MatterFinishV1,
			operation.MatterFinishInput{MatterID: f.matter}, []int{107}, 4)
		completeRepairGateDeclaration(t, f, claim, 16, 7, "repair-target", "matter", 108, 4)
		boundary = f.anchor(t)
		target, commandID, nextSequence = f.matter, 20, 8
	case "enclosing-open-at-boundary":
		completeRepairGateDeclaration(t, f, claim, 12, 3, "parent-pre", "matter", 104, 1)
		completeRepairLifecycle(t, f, claim, 13, 4, operation.MatterStartV1,
			operation.NodeLifecycleInput{NodeID: f.matter}, []int{105}, 2)
		stageID := claimTestID(201)
		create := step12Command(f, 14, 5, operation.StageCreateV1,
			operation.StageCreateInput{MatterID: f.matter, Title: "Repair target"}, claim.ClaimID)
		_, created := completeStep12ClaimCommand(t, f, create, claim.JournalID, 3, 201, 106)
		if created.Pending {
			t.Fatal("stage creation remained pending")
		}
		completeRepairLifecycle(t, f, claim, 15, 6, operation.StageStartV1,
			operation.NodeLifecycleInput{NodeID: stageID}, []int{107}, 4)
		completeRepairLifecycle(t, f, claim, 16, 7, operation.StageFinishV1,
			operation.NodeLifecycleInput{NodeID: stageID}, []int{108}, 5)
		completeRepairGateDeclaration(t, f, claim, 17, 8, "repair-target", "stage", 109, 6)
		boundary = f.anchor(t)
		completeRepairLifecycle(t, f, claim, 18, 9, operation.MatterFinishV1,
			operation.MatterFinishInput{MatterID: f.matter}, []int{110}, 0)
		completeRepairGateClose(t, f, claim, 19, 10, "parent-pre", f.matter, 111, 7)
		target, commandID, nextSequence = stageID, 22, 11
	default:
		t.Fatalf("unknown repair terminal scenario %q", scenario)
	}
	if scenario == "success-missing-snapshot" {
		if err := f.s.Close(); err != nil {
			t.Fatal(err)
		}
		rewriteGateAuthorityEvent(t, f.root, boundary.EventID, func(fields map[string]cbor.RawMessage) error {
			var payload map[string]cbor.RawMessage
			if err := canonicalDecode(fields["payload"], &payload); err != nil {
				return err
			}
			delete(payload, "exempt")
			encoded, err := artifactEncoder.Marshal(payload)
			if err == nil {
				fields["payload"] = encoded
			}
			return err
		})
		db, err := connect(filepath.Join(f.root, "authority.db"), "rw", false)
		if err != nil {
			t.Fatal(err)
		}
		if err = rebuildStep13Projection(db); err != nil {
			_ = db.Close()
			t.Fatalf("rebuild projection for historical missing exemption: %v", err)
		}
		if err = db.Close(); err != nil {
			t.Fatal(err)
		}
		reopened, err := OpenExisting(f.root)
		if err != nil {
			t.Fatalf("open historical missing-exemption fixture: %v", err)
		}
		f.s = reopened
		t.Cleanup(func() { _ = f.s.Close() })
		boundary = f.anchor(t)
	}
	if target == "" {
		target = f.matter
	}
	command := gateExemptionRepairCommand{
		ID: claimTestID(commandID), DomainID: domainA, AuthorityEpoch: 7,
		EnvironmentID: envA, EnvironmentSequence: uint64(nextSequence), ActedAt: "2026-09-23T11:59:00Z",
		Actor: "human", CorrelationID: claimTestID(commandID), RepoID: repoA,
		CloneID: f.clone, WorktreeID: f.worktree, ClaimID: claim.ClaimID, ClaimEpoch: 1,
		NodeID: target, Gate: "repair-target", Boundary: gateExemptionRepairBoundary{
			EventCount: boundary.EventCount, HighWaterEvent: boundary.EventID, PrefixDigest: boundary.Digest,
		},
		IncidentRef: "urn:example:repair-target", Reason: "Restore the omitted prospective exemption after review",
		Evidence: []string{"sha256:" + string(bytes.Repeat([]byte{'a'}, 64))},
	}
	canonical, hash, proof := signRepairAdmissionProof(t, command, false,
		f.now.Add(-time.Minute), f.now.Add(time.Minute), bytes.Repeat([]byte{nonceByte}, 16), nil)
	return repairTerminalFixture{f: f, claim: claim, command: command, canonical: canonical, hash: hash, proof: proof}
}

func admitRepairTerminalFixture(t *testing.T, fixture repairTerminalFixture) {
	t.Helper()
	if _, err := fixture.f.s.admitGateExemptionRepair(context.Background(), fixture.command, fixture.proof, fixture.f.peer, fixture.f.now); err != nil {
		t.Fatalf("admit private repair proof: %v", err)
	}
}

func releaseRepairFixtureClaim(t *testing.T, fixture repairTerminalFixture, sequence uint64, commandID, firstEvent, secondEvent int, at time.Time) {
	t.Helper()
	ctx := context.Background()
	current, err := fixture.f.s.GetCurrentClaimJournal(ctx, domainA, 7, envA, fixture.claim.ClaimID, 1,
		fixture.f.matter, claimTestID(51))
	if err != nil {
		t.Fatalf("read claim journal before release: %v", err)
	}
	if current.State == "open" {
		if _, _, err = fixture.f.s.SealOwnedClaimJournal(ctx, current); err != nil {
			t.Fatalf("seal journal before release: %v", err)
		}
	}
	tx, err := fixture.f.s.db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	digest, count, digestErr := barrierDigest(ctx, tx, domainA, current.JournalID)
	_ = tx.Rollback()
	if digestErr != nil {
		t.Fatalf("compute release barrier: %v", digestErr)
	}
	barrier := map[string]any{
		"schema": "wipd.journal-barrier/1", "journal_id": current.JournalID,
		"claim":       map[string]any{"id": fixture.claim.ClaimID, "epoch": uint64(1)},
		"entry_count": count, "last_position": count, "terminal_receipt_count": count,
		"entries_digest": digest, "sealed": true, "unresolved_count": uint64(0), "quarantined_count": uint64(0),
	}
	canonical, hash := fixture.f.command(t, commandID, sequence, "claim.release",
		map[string]any{"id": fixture.claim.ClaimID, "epoch": uint64(1)}, map[string]any{"barrier": barrier})
	status, err := fixture.f.s.SubmitClaimLifecycle(ctx, canonical, hash, fixture.f.peer, at, nil)
	if err != nil || status.Owner == nil {
		t.Fatalf("submit claim release after repair terminal: %+v err=%v", status, err)
	}
	if _, err = fixture.f.s.CompleteClaimLifecycle(ctx, status.Owner, "",
		[]string{claimTestID(firstEvent), claimTestID(secondEvent)}, at, signWith(fixture.f.key)); err != nil {
		t.Fatalf("complete claim release after repair terminal: %v", err)
	}
}

func TestGateExemptionRepairTerminalSuccessIsAtomicDetachedAndRecoverable(t *testing.T) {
	fixture := prepareRepairTerminalFixture(t, "success-missing-snapshot", 0x51)
	ctx := context.Background()
	admitRepairTerminalFixture(t, fixture)
	if _, err := fixture.f.s.db.Exec(`CREATE TRIGGER fail_repair_terminal BEFORE INSERT ON gate_exemption_repair_terminals BEGIN SELECT RAISE(ABORT,'injected terminal failure'); END`); err != nil {
		t.Fatal(err)
	}
	if _, err := fixture.f.s.completeGateExemptionRepair(ctx, fixture.canonical, fixture.hash, fixture.proof, []byte(claimTestID(130)), fixture.f.now); err == nil {
		t.Fatal("injected terminal transaction failure was ignored")
	}
	var terminals, submissions, effects, receipts, nonces int
	for query, destination := range map[string]*int{
		`SELECT count(*) FROM gate_exemption_repair_terminals`:                          &terminals,
		`SELECT count(*) FROM submissions WHERE operation_name='gate.exemption.repair'`: &submissions,
		`SELECT count(*) FROM authority_events WHERE command_id=?`:                      &effects,
		`SELECT count(*) FROM terminal_receipts WHERE command_id=?`:                     &receipts,
		`SELECT count(*) FROM gate_exemption_repair_nonces WHERE domain_id=?`:           &nonces,
	} {
		switch destination {
		case &effects, &receipts:
			err := fixture.f.s.db.QueryRow(query, fixture.command.ID).Scan(destination)
			if err != nil {
				t.Fatal(err)
			}
		case &nonces:
			if err := fixture.f.s.db.QueryRow(query, domainA).Scan(destination); err != nil {
				t.Fatal(err)
			}
		default:
			if err := fixture.f.s.db.QueryRow(query).Scan(destination); err != nil {
				t.Fatal(err)
			}
		}
	}
	if terminals != 0 || submissions != 0 || effects != 0 || receipts != 0 || nonces != 1 {
		t.Fatalf("terminal rollback leaked partial state: terminals=%d submissions=%d effects=%d receipts=%d nonces=%d", terminals, submissions, effects, receipts, nonces)
	}
	if _, err := fixture.f.s.db.Exec(`DROP TRIGGER fail_repair_terminal`); err != nil {
		t.Fatal(err)
	}
	terminal, err := fixture.f.s.completeGateExemptionRepair(ctx, fixture.canonical, fixture.hash, fixture.proof,
		[]byte(claimTestID(130)), fixture.f.now)
	if err != nil || terminal.ResultCode != "result.succeeded" || terminal.EventID != claimTestID(130) || terminal.RefusalCode != "" {
		t.Fatalf("complete eligible private repair: %+v err=%v", terminal, err)
	}
	if err = validatePrivateGateRepairEvent(ctx, fixture.f.s.db, fixture.command, terminal.EventID, terminal.ObservedPosition+1, terminal.OccurredAt); err != nil {
		t.Fatalf("validate exact legacy repair effect: %v", err)
	}
	var state string
	if err = fixture.f.s.db.QueryRow(`SELECT state FROM m6_gate_states WHERE domain_id=? AND node_id=? AND gate=?`,
		domainA, fixture.command.NodeID, fixture.command.Gate).Scan(&state); err != nil || state != "exempt" {
		t.Fatalf("repair projection state=%q err=%v", state, err)
	}
	var repairRecord []byte
	if err = fixture.f.s.db.QueryRow(`SELECT record FROM authority_events WHERE domain_id=? AND command_id=?`, domainA, fixture.command.ID).Scan(&repairRecord); err != nil {
		t.Fatal(err)
	}
	var repairEvent step12Event
	if repairEvent, err = parseStep12Event(repairRecord, domainA, terminal.ObservedPosition+1, terminal.EventID, fixture.command.ID); err != nil || repairEvent.kind != "gate.exemption-repaired" {
		t.Fatalf("repair emitted non-repair or inline sweep event: kind=%q err=%v", repairEvent.kind, err)
	}
	if repairEvent.occurred != terminal.OccurredAt {
		t.Fatalf("terminal time %q differs from repair event time %q", terminal.OccurredAt, repairEvent.occurred)
	}
	if _, err = fixture.f.s.QueryCommand(ctx, domainA, fixture.command.ID, fixture.hash, 7, fixture.f.peer, envA, fixture.f.now); !errors.Is(err, ErrNotFound) {
		t.Fatalf("private terminal appeared on public receipt query: %v", err)
	}
	if err = fixture.f.s.Close(); err != nil {
		t.Fatal(err)
	}
	reopened, err := OpenExisting(fixture.f.root)
	if err != nil {
		t.Fatalf("reopen successful terminal repair: %v", err)
	}
	fixture.f.s = reopened
	t.Cleanup(func() { _ = reopened.Close() })
	recovered, err := reopened.recoverGateExemptionRepair(ctx, fixture.canonical, fixture.hash, nil)
	if err != nil || recovered.Terminal == nil || recovered.Terminal.EventID != terminal.EventID || !bytes.Equal(recovered.Proof, fixture.proof) {
		t.Fatalf("recover exact detached terminal identity: %+v err=%v", recovered, err)
	}
	if _, err = reopened.recoverGateExemptionRepair(ctx, fixture.canonical, fixture.hash, fixture.proof); err != nil {
		t.Fatalf("recover terminal with byte-equal proof: %v", err)
	}
	substituted := append([]byte(nil), fixture.proof...)
	substituted[len(substituted)-1] ^= 1
	if _, err = reopened.recoverGateExemptionRepair(ctx, fixture.canonical, fixture.hash, substituted); !errors.Is(err, ErrConflict) {
		t.Fatalf("recover terminal with substituted proof: %v", err)
	}
	if err = rebuildStep13Projection(reopened.db); err != nil {
		t.Fatalf("rebuild Step 13 after private success: %v", err)
	}
	if err = checkStep15State(reopened.db); err != nil {
		t.Fatalf("validate terminal after projection rebuild: %v", err)
	}
	if _, err = reopened.completeGateExemptionRepair(ctx, fixture.canonical, fixture.hash, nil,
		[]byte(claimTestID(131)), fixture.f.now.Add(time.Minute)); err != nil {
		t.Fatalf("idempotent terminal retry with omitted proof: %v", err)
	}
}

func rewriteGateRepairTerminalForRecoveryTest(t *testing.T, db *sql.DB, statement string, args ...any) {
	t.Helper()
	var immutableTrigger string
	for _, object := range step15Schema {
		if object.name == "gate_exemption_repair_terminals_immutable" {
			immutableTrigger = object.sql
			break
		}
	}
	if immutableTrigger == "" {
		t.Fatal("immutable private repair terminal trigger missing from schema")
	}
	if _, err := db.Exec(`DROP TRIGGER gate_exemption_repair_terminals_immutable`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(statement, args...); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(immutableTrigger); err != nil {
		t.Fatal(err)
	}
}

func downgradeStep16ForMigrationTest(t *testing.T, db *sql.DB) {
	t.Helper()
	tx, err := db.Begin()
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = tx.Rollback() }()
	if _, err = tx.Exec(`DROP TRIGGER gate_exemption_repair_terminals_immutable`); err != nil {
		t.Fatal(err)
	}
	if _, err = tx.Exec(`ALTER TABLE gate_exemption_repair_terminals DROP COLUMN boundary_witness`); err != nil {
		t.Fatalf("remove Step 16 terminal witness column: %v", err)
	}
	for _, object := range step15Schema {
		if object.name == "gate_exemption_repair_terminals_immutable" {
			if _, err = tx.Exec(object.sql); err != nil {
				t.Fatal(err)
			}
		}
	}
	for _, statement := range []string{
		`DROP TABLE schema_migrations`, step15MigrationMarker.sql,
		`INSERT INTO schema_migrations VALUES (1,'baseline'),(2,'environment-and-artifacts'),(3,'submissions-and-receipts'),(4,'prefix-snapshot-blob-transfer'),(5,'claims-grants-journals-close'),(6,'m5-lab-genesis-grant-consumption'),(7,'step-8-provisional-birth-projection'),(8,'step-9-birth-journal-receipt-barrier'),(9,'step-10-content-and-findings'),(10,'step-16-claim-journal-sequence-order'),(11,'step-4-stage-step-operations'),(12,'step-7-authority-gate-config-projections'),(13,'step-7-detached-gate-repair-admissions'),(14,'step-7-private-gate-repair-terminals')`,
		`PRAGMA user_version=14`,
	} {
		if _, err = tx.Exec(statement); err != nil {
			t.Fatalf("restore v14 schema marker: %v", err)
		}
	}
	if err = tx.Commit(); err != nil {
		t.Fatal(err)
	}
}

func TestGateExemptionRepairMultiplePrerequisiteRefusalRecoversDeterministically(t *testing.T) {
	for run := range 16 {
		t.Run(fmt.Sprintf("run-%02d", run), func(t *testing.T) {
			fixture := prepareRepairTerminalFixture(t, "two-open-own-gates", byte(0x80+run))
			admitRepairTerminalFixture(t, fixture)
			terminal, err := fixture.f.s.completeGateExemptionRepair(context.Background(), fixture.canonical, fixture.hash,
				fixture.proof, nil, fixture.f.now)
			if err != nil || terminal.ResultCode != "result.refused" || terminal.RefusalCode != "refusal.gate-repair-prerequisite" {
				t.Fatalf("persist deterministic prerequisite refusal: %+v err=%v", terminal, err)
			}
			want := fmt.Sprintf("%s was not sealed before repair-target became operative: own-a is open on %s", fixture.command.NodeID, fixture.command.NodeID)
			if terminal.RefusalMessage != want {
				t.Fatalf("refusal witness=%q, want stable lexical witness %q", terminal.RefusalMessage, want)
			}
			if err = fixture.f.s.Close(); err != nil {
				t.Fatal(err)
			}
			reopened, err := OpenExisting(fixture.f.root)
			if err != nil {
				t.Fatalf("reopen deterministic refusal: %v", err)
			}
			defer func() { _ = reopened.Close() }()
			recovered, err := reopened.recoverGateExemptionRepair(context.Background(), fixture.canonical, fixture.hash, nil)
			if err != nil || recovered.Terminal == nil || !sameGateExemptionRepairTerminal(*recovered.Terminal, terminal) || !bytes.Equal(recovered.Proof, fixture.proof) {
				t.Fatalf("recover identical deterministic refusal: %+v err=%v", recovered, err)
			}
		})
	}
}

func TestUpgradeV14BackfillsLegacyRepairTerminalBoundaryWitness(t *testing.T) {
	fixture := prepareRepairTerminalFixture(t, "lifecycle-open-at-boundary", 0x96)
	admitRepairTerminalFixture(t, fixture)
	terminal, err := fixture.f.s.completeGateExemptionRepair(context.Background(), fixture.canonical, fixture.hash,
		fixture.proof, nil, fixture.f.now)
	if err != nil || terminal.ResultCode != "result.refused" {
		t.Fatalf("create v14-compatible terminal: %+v err=%v", terminal, err)
	}
	downgradeStep16ForMigrationTest(t, fixture.f.s.db)
	if err = checkSchemaVersion(fixture.f.s.db, 14); err != nil {
		t.Fatalf("validate reconstructed v14 store: %v", err)
	}
	if err = fixture.f.s.Close(); err != nil {
		t.Fatal(err)
	}
	if err = UpgradeV14(fixture.f.root); err != nil {
		t.Fatalf("upgrade v14 terminal history with boundary backfill: %v", err)
	}
	reopened, err := OpenExisting(fixture.f.root)
	if err != nil {
		t.Fatalf("open upgraded v15 terminal history: %v", err)
	}
	fixture.f.s = reopened
	defer func() { _ = reopened.Close() }()
	stored, err := readGateExemptionRepairTerminal(context.Background(), reopened.db, domainA, fixture.command.ID)
	if err != nil {
		t.Fatal(err)
	}
	witness, err := decodeGateRepairBoundaryWitness(stored.BoundaryWitness)
	if err != nil || witness.Fence != "legacy" || witness.ResultCode != terminal.ResultCode || witness.RefusalMessage != terminal.RefusalMessage {
		t.Fatalf("legacy terminal backfill witness=%+v err=%v", witness, err)
	}
	recovered, err := reopened.recoverGateExemptionRepair(context.Background(), fixture.canonical, fixture.hash, nil)
	if err != nil || recovered.Terminal == nil || recovered.Terminal.DomainID != terminal.DomainID ||
		recovered.Terminal.CommandID != terminal.CommandID || recovered.Terminal.RequestHash != terminal.RequestHash ||
		recovered.Terminal.ResultCode != terminal.ResultCode || recovered.Terminal.RefusalCode != terminal.RefusalCode ||
		recovered.Terminal.RefusalMessage != terminal.RefusalMessage || recovered.Terminal.ObservedPosition != terminal.ObservedPosition ||
		recovered.Terminal.ObservedEventID != terminal.ObservedEventID || recovered.Terminal.ObservedPrefixDigest != terminal.ObservedPrefixDigest ||
		recovered.Terminal.OccurredAt != terminal.OccurredAt || len(recovered.Terminal.BoundaryWitness) == 0 {
		t.Fatalf("recover terminal after v14 upgrade: %+v err=%v", recovered, err)
	}
}

func TestUpgradeV14RejectsClaimFenceThatOnlyExistsAfterTerminalPrefix(t *testing.T) {
	fixture := prepareRepairTerminalFixture(t, "lifecycle-open-at-boundary", 0x97)
	ctx := context.Background()
	admitRepairTerminalFixture(t, fixture)
	terminal, err := fixture.f.s.completeGateExemptionRepair(ctx, fixture.canonical, fixture.hash,
		fixture.proof, nil, fixture.f.now)
	if err != nil || terminal.RefusalCode != "refusal.gate-repair-lifecycle" || terminal.ObservedPosition != 7 {
		t.Fatalf("create original prefix-7 lifecycle refusal: %+v err=%v", terminal, err)
	}
	releaseRepairFixtureClaim(t, fixture, 7, 32, 144, 145, fixture.f.now.Add(time.Minute))
	downgradeStep16ForMigrationTest(t, fixture.f.s.db)
	rewriteGateRepairTerminalForRecoveryTest(t, fixture.f.s.db,
		`UPDATE gate_exemption_repair_terminals SET refusal_code=?,refusal_message=? WHERE domain_id=? AND command_id=?`,
		"refusal.claim-fenced", gateRepairClaimFencedMessage, domainA, fixture.command.ID)
	if err = checkSchemaVersion(fixture.f.s.db, 14); err != nil {
		t.Fatalf("v14 legacy validator control no longer reproduces old claim-fence interpretation: %v", err)
	}
	if err = fixture.f.s.Close(); err != nil {
		t.Fatal(err)
	}
	if err = UpgradeV14(fixture.f.root); !errors.Is(err, ErrInvalidStore) {
		t.Fatalf("v14 upgrade accepted claim fence lacking evidence at the terminal prefix: %v", err)
	}
}

func TestGateExemptionRepairRecoveryRejectsClaimFenceStatusForgery(t *testing.T) {
	fixture := prepareRepairTerminalFixture(t, "own-open-current", 0x91)
	admitRepairTerminalFixture(t, fixture)
	terminal, err := fixture.f.s.completeGateExemptionRepair(context.Background(), fixture.canonical, fixture.hash,
		fixture.proof, nil, fixture.f.now)
	if err != nil || terminal.ResultCode != "result.refused" || terminal.RefusalCode != "refusal.gate-repair-prerequisite" {
		t.Fatalf("create genuine lifecycle refusal: %+v err=%v", terminal, err)
	}
	rewriteGateRepairTerminalForRecoveryTest(t, fixture.f.s.db,
		`UPDATE gate_exemption_repair_terminals SET refusal_code=?,refusal_message=? WHERE domain_id=? AND command_id=?`,
		"refusal.claim-fenced", gateRepairClaimFencedMessage, domainA, fixture.command.ID)
	if _, err = fixture.f.s.recoverGateExemptionRepair(context.Background(), fixture.canonical, fixture.hash, fixture.proof); !errors.Is(err, ErrInvalidStore) {
		t.Fatalf("recovery accepted invented claim-fenced refusal with active claim/journal: %v", err)
	}
	if err = checkStep15State(fixture.f.s.db); !errors.Is(err, ErrInvalidStore) {
		t.Fatalf("terminal history validator accepted invented claim-fenced refusal: %v", err)
	}
	if err = fixture.f.s.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err = OpenExisting(fixture.f.root); !errors.Is(err, ErrInvalidStore) {
		t.Fatalf("OpenExisting accepted invented claim-fenced terminal: %v", err)
	}
}

func TestGateExemptionRepairGenuineClaimFenceRefusalRecovers(t *testing.T) {
	fixture := prepareRepairTerminalFixture(t, "success-missing-snapshot", 0x93)
	ctx := context.Background()
	admitRepairTerminalFixture(t, fixture)
	current, err := fixture.f.s.GetCurrentClaimJournal(ctx, domainA, 7, envA, fixture.claim.ClaimID, 1,
		fixture.f.matter, claimTestID(51))
	if err != nil {
		t.Fatalf("read current Matter claim journal: %v", err)
	}
	if _, _, err = fixture.f.s.SealOwnedClaimJournal(ctx, current); err != nil {
		t.Fatalf("seal claim journal after repair admission: %v", err)
	}
	terminal, err := fixture.f.s.completeGateExemptionRepair(ctx, fixture.canonical, fixture.hash,
		fixture.proof, nil, fixture.f.now)
	if err != nil || terminal.ResultCode != "result.refused" || terminal.RefusalCode != "refusal.claim-fenced" ||
		terminal.RefusalMessage != gateRepairClaimFencedMessage || terminal.EventID != "" {
		t.Fatalf("persist factual claim-fenced refusal: %+v err=%v", terminal, err)
	}
	if err = fixture.f.s.Close(); err != nil {
		t.Fatal(err)
	}
	reopened, err := OpenExisting(fixture.f.root)
	if err != nil {
		t.Fatalf("reopen factual claim-fenced refusal: %v", err)
	}
	defer func() { _ = reopened.Close() }()
	recovered, err := reopened.recoverGateExemptionRepair(ctx, fixture.canonical, fixture.hash, nil)
	if err != nil || recovered.Terminal == nil || !sameGateExemptionRepairTerminal(*recovered.Terminal, terminal) || !bytes.Equal(recovered.Proof, fixture.proof) {
		t.Fatalf("recover factual claim-fenced refusal: %+v err=%v", recovered, err)
	}
}

func TestGateExemptionRepairClaimFenceForgeryIsBoundToTerminalPrefix(t *testing.T) {
	fixture := prepareRepairTerminalFixture(t, "lifecycle-open-at-boundary", 0x94)
	ctx := context.Background()
	admitRepairTerminalFixture(t, fixture)
	terminal, err := fixture.f.s.completeGateExemptionRepair(ctx, fixture.canonical, fixture.hash,
		fixture.proof, nil, fixture.f.now)
	if err != nil || terminal.ResultCode != "result.refused" || terminal.RefusalCode != "refusal.gate-repair-lifecycle" ||
		terminal.ObservedPosition != 7 || terminal.ObservedEventID != claimTestID(106) || terminal.OccurredAt != "2026-09-23T12:00:00Z" {
		t.Fatalf("record exact prefix-7 lifecycle refusal: %+v err=%v", terminal, err)
	}
	if err = fixture.f.s.Close(); err != nil {
		t.Fatal(err)
	}
	fixture.f.s, err = OpenExisting(fixture.f.root)
	if err != nil {
		t.Fatalf("reopen prefix-7 lifecycle refusal: %v", err)
	}
	if _, err = fixture.f.s.recoverGateExemptionRepair(ctx, fixture.canonical, fixture.hash, nil); err != nil {
		t.Fatalf("recover genuine refusal before later claim close: %v", err)
	}
	releaseRepairFixtureClaim(t, fixture, 7, 30, 140, 141, fixture.f.now.Add(time.Minute))
	postRelease := fixture.f.anchor(t)
	if postRelease.EventCount != 9 || postRelease.EventID != claimTestID(141) {
		t.Fatalf("release did not grow history from terminal prefix 7 to 9: %+v", postRelease)
	}
	if err = fixture.f.s.Close(); err != nil {
		t.Fatal(err)
	}
	reopened, err := OpenExisting(fixture.f.root)
	if err != nil {
		t.Fatalf("reopen unchanged prefix-7 refusal after prefix-9 release: %v", err)
	}
	fixture.f.s = reopened
	recovered, err := reopened.recoverGateExemptionRepair(ctx, fixture.canonical, fixture.hash, nil)
	if err != nil || recovered.Terminal == nil || !sameGateExemptionRepairTerminal(*recovered.Terminal, terminal) {
		t.Fatalf("later release changed original terminal result: %+v err=%v", recovered, err)
	}
	rewriteGateRepairTerminalForRecoveryTest(t, reopened.db,
		`UPDATE gate_exemption_repair_terminals SET refusal_code=?,refusal_message=? WHERE domain_id=? AND command_id=?`,
		"refusal.claim-fenced", gateRepairClaimFencedMessage, domainA, fixture.command.ID)
	if _, err = reopened.recoverGateExemptionRepair(ctx, fixture.canonical, fixture.hash, fixture.proof); !errors.Is(err, ErrInvalidStore) {
		t.Fatalf("recovery accepted invented earlier claim-fence refusal after later release: %v", err)
	}
	if err = checkStep15State(reopened.db); !errors.Is(err, ErrInvalidStore) {
		t.Fatalf("terminal validator accepted forged prefix-7 claim fence: %v", err)
	}
	if err = reopened.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err = OpenExisting(fixture.f.root); !errors.Is(err, ErrInvalidStore) {
		t.Fatalf("OpenExisting accepted forged prefix-7 claim fence after prefix-9 release: %v", err)
	}
}

func TestGateExemptionRepairGenuineClaimFenceSurvivesLaterClaimRelease(t *testing.T) {
	fixture := prepareRepairTerminalFixture(t, "success-missing-snapshot", 0x95)
	ctx := context.Background()
	admitRepairTerminalFixture(t, fixture)
	current, err := fixture.f.s.GetCurrentClaimJournal(ctx, domainA, 7, envA, fixture.claim.ClaimID, 1,
		fixture.f.matter, claimTestID(51))
	if err != nil {
		t.Fatalf("read claim journal before terminal refusal: %v", err)
	}
	if _, _, err = fixture.f.s.SealOwnedClaimJournal(ctx, current); err != nil {
		t.Fatalf("seal journal before factual fence: %v", err)
	}
	terminal, err := fixture.f.s.completeGateExemptionRepair(ctx, fixture.canonical, fixture.hash,
		fixture.proof, nil, fixture.f.now)
	if err != nil || terminal.ResultCode != "result.refused" || terminal.RefusalCode != "refusal.claim-fenced" || terminal.ObservedPosition == 0 {
		t.Fatalf("record factual journal-fenced refusal: %+v err=%v", terminal, err)
	}
	if err = fixture.f.s.Close(); err != nil {
		t.Fatal(err)
	}
	fixture.f.s, err = OpenExisting(fixture.f.root)
	if err != nil {
		t.Fatalf("reopen factual journal-fenced terminal: %v", err)
	}
	releaseRepairFixtureClaim(t, fixture, 9, 31, 142, 143, fixture.f.now.Add(time.Minute))
	postRelease := fixture.f.anchor(t)
	if postRelease.EventCount != terminal.ObservedPosition+2 {
		t.Fatalf("claim release did not append two events after the terminal boundary: terminal=%d high-water=%d", terminal.ObservedPosition, postRelease.EventCount)
	}
	if err = fixture.f.s.Close(); err != nil {
		t.Fatal(err)
	}
	reopened, err := OpenExisting(fixture.f.root)
	if err != nil {
		t.Fatalf("reopen genuine journal fence after later claim release: %v", err)
	}
	defer func() { _ = reopened.Close() }()
	recovered, err := reopened.recoverGateExemptionRepair(ctx, fixture.canonical, fixture.hash, nil)
	if err != nil || recovered.Terminal == nil || !sameGateExemptionRepairTerminal(*recovered.Terminal, terminal) {
		t.Fatalf("later release invalidated genuine earlier journal fence: %+v err=%v", recovered, err)
	}
}

func TestGateExemptionRepairRecoveryRejectsTerminalEventTimeMismatch(t *testing.T) {
	fixture := prepareRepairTerminalFixture(t, "success-missing-snapshot", 0x92)
	admitRepairTerminalFixture(t, fixture)
	terminal, err := fixture.f.s.completeGateExemptionRepair(context.Background(), fixture.canonical, fixture.hash,
		fixture.proof, []byte(claimTestID(130)), fixture.f.now)
	if err != nil || terminal.ResultCode != "result.succeeded" {
		t.Fatalf("complete repair with event time: %+v err=%v", terminal, err)
	}
	var eventRecord []byte
	if err = fixture.f.s.db.QueryRow(`SELECT record FROM authority_events WHERE domain_id=? AND event_id=?`, domainA, terminal.EventID).Scan(&eventRecord); err != nil {
		t.Fatal(err)
	}
	event, err := parseStep12Event(eventRecord, domainA, terminal.ObservedPosition+1, terminal.EventID, fixture.command.ID)
	if err != nil || event.occurred != terminal.OccurredAt {
		t.Fatalf("normal terminal/event times differ: terminal=%q event=%q err=%v", terminal.OccurredAt, event.occurred, err)
	}
	wrongTime := "2099-01-01T00:00:00Z"
	rewriteGateRepairTerminalForRecoveryTest(t, fixture.f.s.db,
		`UPDATE gate_exemption_repair_terminals SET occurred_at=? WHERE domain_id=? AND command_id=?`,
		wrongTime, domainA, fixture.command.ID)
	if _, err = fixture.f.s.recoverGateExemptionRepair(context.Background(), fixture.canonical, fixture.hash, fixture.proof); !errors.Is(err, ErrInvalidStore) {
		t.Fatalf("recovery accepted terminal time %q distinct from event time %q: %v", wrongTime, event.occurred, err)
	}
	if err = checkStep15State(fixture.f.s.db); !errors.Is(err, ErrInvalidStore) {
		t.Fatalf("terminal history validator accepted event-time mismatch: %v", err)
	}
	if err = fixture.f.s.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err = OpenExisting(fixture.f.root); !errors.Is(err, ErrInvalidStore) {
		t.Fatalf("OpenExisting accepted terminal/event time mismatch: %v", err)
	}
}

func TestGateExemptionRepairTerminalRefusesHistoricalBoundaryFailures(t *testing.T) {
	for index, scenario := range []string{"done-after-boundary", "own-open-current", "own-closed-after-boundary", "enclosing-open-at-boundary"} {
		t.Run(scenario, func(t *testing.T) {
			fixture := prepareRepairTerminalFixture(t, scenario, byte(0x60+index))
			admitRepairTerminalFixture(t, fixture)
			terminal, err := fixture.f.s.completeGateExemptionRepair(context.Background(), fixture.canonical, fixture.hash,
				fixture.proof, nil, fixture.f.now)
			if err != nil || terminal.ResultCode != "result.refused" || terminal.RefusalCode == "" || terminal.RefusalMessage == "" || terminal.EventID != "" {
				t.Fatalf("persist terminal refusal for %s: %+v err=%v", scenario, terminal, err)
			}
			var events, receipts, reservations int
			if err = fixture.f.s.db.QueryRow(`SELECT count(*) FROM authority_events WHERE domain_id=? AND command_id=?`, domainA, fixture.command.ID).Scan(&events); err != nil {
				t.Fatal(err)
			}
			if err = fixture.f.s.db.QueryRow(`SELECT count(*) FROM terminal_receipts WHERE domain_id=? AND command_id=?`, domainA, fixture.command.ID).Scan(&receipts); err != nil {
				t.Fatal(err)
			}
			if err = fixture.f.s.db.QueryRow(`SELECT count(*) FROM gate_exemption_repair_nonces WHERE domain_id=? AND command_id=?`, domainA, fixture.command.ID).Scan(&reservations); err != nil {
				t.Fatal(err)
			}
			if events != 0 || receipts != 0 || reservations != 1 {
				t.Fatalf("refusal effects=%d receipts=%d retained nonces=%d", events, receipts, reservations)
			}
			if err = fixture.f.s.Close(); err != nil {
				t.Fatal(err)
			}
			reopened, err := OpenExisting(fixture.f.root)
			if err != nil {
				t.Fatalf("reopen refused terminal repair: %v", err)
			}
			fixture.f.s = reopened
			defer func() { _ = reopened.Close() }()
			recovered, err := reopened.recoverGateExemptionRepair(context.Background(), fixture.canonical, fixture.hash, nil)
			if err != nil || recovered.Terminal == nil || recovered.Terminal.RefusalCode != terminal.RefusalCode || !bytes.Equal(recovered.Proof, fixture.proof) {
				t.Fatalf("recover persisted refusal and detached proof: %+v err=%v", recovered, err)
			}
		})
	}
}

func TestGateExemptionRepairTerminalNoopsForExistingExemption(t *testing.T) {
	fixture := prepareRepairTerminalFixture(t, "success-missing-snapshot", 0x70)
	admitRepairTerminalFixture(t, fixture)
	first, err := fixture.f.s.completeGateExemptionRepair(context.Background(), fixture.canonical, fixture.hash,
		fixture.proof, []byte(claimTestID(130)), fixture.f.now)
	if err != nil {
		t.Fatalf("establish existing exemption: %v", err)
	}
	command := fixture.command
	command.ID, command.CorrelationID = claimTestID(21), claimTestID(21)
	command.EnvironmentSequence++
	canonical, hash, proof := signRepairAdmissionProof(t, command, false,
		fixture.f.now.Add(-time.Minute), fixture.f.now.Add(time.Minute), bytes.Repeat([]byte{0x71}, 16), nil)
	if _, err = fixture.f.s.admitGateExemptionRepair(context.Background(), command, proof, fixture.f.peer, fixture.f.now); err != nil {
		t.Fatalf("admit already-satisfied exemption no-op: %v", err)
	}
	terminal, err := fixture.f.s.completeGateExemptionRepair(context.Background(), canonical, hash, proof, nil, fixture.f.now)
	if err != nil || terminal.ResultCode != "result.succeeded" || terminal.EventID != "" {
		t.Fatalf("already-present exemption result=%+v err=%v", terminal, err)
	}
	var events int
	if err = fixture.f.s.db.QueryRow(`SELECT count(*) FROM authority_events WHERE domain_id=? AND command_id=?`, domainA, command.ID).Scan(&events); err != nil || events != 0 {
		t.Fatalf("no-op repair emitted %d events: %v", events, err)
	}
	if first.EventID == terminal.EventID {
		t.Fatalf("unexpected event identity reuse: first=%+v second=%+v", first, terminal)
	}
}

func TestAssessGateExemptionRepairRejectsCurrentSubjectAndSatisfactionFailures(t *testing.T) {
	command := gateExemptionRepairCommand{DomainID: domainA, RepoID: repoA, NodeID: claimTestID(202), Gate: "repair-target"}
	matterID := claimTestID(200)
	base := gateRepairSnapshot{
		nodes: map[string]step13Node{
			ownerKey(domainA, command.NodeID):   {node: step12Node{domain: domainA, id: command.NodeID, repo: repoA, kind: "step", parent: claimTestID(201)}, birthPos: 1},
			ownerKey(domainA, claimTestID(201)): {node: step12Node{domain: domainA, id: claimTestID(201), repo: repoA, kind: "stage", parent: matterID}, birthPos: 1},
			ownerKey(domainA, matterID):         {node: step12Node{domain: domainA, id: matterID, repo: repoA, kind: "matter"}, birthPos: 1},
		},
		lifecycle: map[string]string{ownerKey(domainA, command.NodeID): "done", ownerKey(domainA, claimTestID(201)): "done", ownerKey(domainA, matterID): "done"},
		declarations: map[string]step13Declaration{
			gateRepairDeclarationKey(domainA, repoA, command.Gate):  {domain: domainA, repo: repoA, gate: command.Gate, scale: "step"},
			gateRepairDeclarationKey(domainA, repoA, "own-open"):    {domain: domainA, repo: repoA, gate: "own-open", scale: "step"},
			gateRepairDeclarationKey(domainA, repoA, "parent-open"): {domain: domainA, repo: repoA, gate: "parent-open", scale: "matter"},
		},
		states: map[string]step13GateState{
			gateRepairStateKey(domainA, command.NodeID, command.Gate): {domain: domainA, repo: repoA, node: command.NodeID, gate: command.Gate, scale: "step", state: "closed"},
		},
	}
	if refusal := assessGateExemptionRepair(base, command, 10, false); refusal == nil || refusal.code != "refusal.gate-already-satisfied" {
		t.Fatalf("already closed target refusal=%+v", refusal)
	}
	base.states[gateRepairStateKey(domainA, command.NodeID, command.Gate)] = step13GateState{domain: domainA, repo: repoA, node: command.NodeID, gate: command.Gate, scale: "step", state: "dismissed"}
	if refusal := assessGateExemptionRepair(base, command, 10, false); refusal == nil || refusal.code != "refusal.gate-already-satisfied" {
		t.Fatalf("already dismissed target refusal=%+v", refusal)
	}
	delete(base.states, gateRepairStateKey(domainA, command.NodeID, command.Gate))
	base.declarations[gateRepairDeclarationKey(domainA, repoA, command.Gate)] = step13Declaration{domain: domainA, repo: repoA, gate: command.Gate, scale: "matter"}
	if refusal := assessGateExemptionRepair(base, command, 10, false); refusal == nil || refusal.code != "refusal.gate-repair-scale" {
		t.Fatalf("wrong-scale target refusal=%+v", refusal)
	}
	base.declarations = map[string]step13Declaration{
		gateRepairDeclarationKey(domainA, repoA, command.Gate): {domain: domainA, repo: repoA, gate: command.Gate, scale: "step"},
	}
	target := base.nodes[ownerKey(domainA, command.NodeID)]
	target.node.repo = repoB
	base.nodes[ownerKey(domainA, command.NodeID)] = target
	if refusal := assessGateExemptionRepair(base, command, 10, false); refusal == nil || refusal.code != "refusal.gate-repair-subject" {
		t.Fatalf("wrong-Repo target refusal=%+v", refusal)
	}
	target.node.repo = repoA
	target.tombstonePos = 5
	base.nodes[ownerKey(domainA, command.NodeID)] = target
	if refusal := assessGateExemptionRepair(base, command, 10, false); refusal == nil || refusal.code != "refusal.gate-repair-subject" {
		t.Fatalf("tombstoned target refusal=%+v", refusal)
	}
}

func TestGateExemptionRepairAdmissionRejectsClosedAndDismissedTargets(t *testing.T) {
	for _, operationCase := range []struct {
		name    string
		def     operation.Definition
		dismiss bool
	}{
		{name: "closed", def: operation.GateCloseV1},
		{name: "dismissed", def: operation.GateDismissV1, dismiss: true},
	} {
		t.Run(operationCase.name, func(t *testing.T) {
			fixture := prepareRepairTerminalFixture(t, "success-missing-snapshot", byte(0x78))
			// The matching gate declaration exists and the target is Done. A
			// satisfied target is refused before nonce admission, not converted
			// into a repair terminal outcome.
			var input operation.Input = operation.GateCloseInput{Gate: "repair-target", NodeID: fixture.command.NodeID}
			if operationCase.dismiss {
				input = operation.GateDismissInput{Gate: "repair-target", NodeID: fixture.command.NodeID, Reason: "not required"}
			}
			gateCommand := step12Command(fixture.f, 22, fixture.command.EnvironmentSequence,
				operationCase.def, input, fixture.claim.ClaimID)
			owner := submitGateOperation(t, fixture.f, gateCommand)
			completed, err := fixture.f.s.CompleteCommand(context.Background(), owner, operation.Result{Code: operation.ResultSucceeded},
				fixture.command.NodeID, claimTestID(131), fixture.f.now, signWith(fixture.f.key))
			if err != nil {
				t.Fatalf("complete prior %s: %v", operationCase.name, err)
			}
			claimTestAcknowledge(t, fixture.f, fixture.claim.JournalID, 5, completed)
			command := fixture.command
			command.EnvironmentSequence++
			canonical, hash, proof := signRepairAdmissionProof(t, command, false,
				fixture.f.now.Add(-time.Minute), fixture.f.now.Add(time.Minute), bytes.Repeat([]byte{0x79}, 16), nil)
			if _, err = fixture.f.s.admitGateExemptionRepair(context.Background(), command, proof, fixture.f.peer, fixture.f.now); err == nil {
				t.Fatal("already-satisfied target consumed a repair nonce")
			}
			var admissions, nonces int
			if err = fixture.f.s.db.QueryRow(`SELECT count(*) FROM gate_exemption_repair_admissions WHERE command_id=?`, command.ID).Scan(&admissions); err != nil {
				t.Fatal(err)
			}
			if err = fixture.f.s.db.QueryRow(`SELECT count(*) FROM gate_exemption_repair_nonces WHERE nonce=?`, bytes.Repeat([]byte{0x79}, 16)).Scan(&nonces); err != nil || admissions != 0 || nonces != 0 {
				t.Fatalf("pre-admission satisfied-target rejection persisted state: admissions=%d nonces=%d err=%v (%d bytes/%d hash)", admissions, nonces, err, len(canonical), len(hash))
			}
		})
	}
}
