package authoritystore

import (
	"bytes"
	"context"
	"errors"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/procrastivity/wip/internal/operation"
)

type gateRepairAdmissionFixture struct {
	store      *claimTestFixture
	allocation AcquireAllocation
	command    gateExemptionRepairCommand
}

func newGateRepairAdmissionFixture(t *testing.T) gateRepairAdmissionFixture {
	t.Helper()
	f, allocation := gateClaimedFixture(t)
	declaration := step12Command(f, 12, 3, operation.GateDeclareV1,
		operation.GateDeclareInput{Gate: "reviewed-local", Scale: "matter"}, allocation.ClaimID)
	owner := submitGateOperation(t, f, declaration)
	declared, err := f.s.CompleteCommand(context.Background(), owner, operation.Result{Code: operation.ResultSucceeded},
		repoA, claimTestID(104), f.now, signWith(f.key))
	if err != nil {
		t.Fatalf("complete repair-boundary declaration: %v", err)
	}
	claimTestAcknowledge(t, f, allocation.JournalID, 1, declared)
	boundary := f.anchor(t)
	return gateRepairAdmissionFixture{
		store: f, allocation: allocation,
		command: gateExemptionRepairCommand{
			ID: claimTestID(13), DomainID: domainA, AuthorityEpoch: 7,
			EnvironmentID: envA, EnvironmentSequence: 4, ActedAt: "2026-09-23T11:59:00Z",
			Actor: "human", CorrelationID: claimTestID(13), RepoID: repoA,
			CloneID: f.clone, WorktreeID: f.worktree, ClaimID: allocation.ClaimID, ClaimEpoch: 1,
			NodeID: f.matter, Gate: "reviewed-local", Boundary: gateExemptionRepairBoundary{
				EventCount: boundary.EventCount, HighWaterEvent: boundary.EventID, PrefixDigest: boundary.Digest,
			},
			IncidentRef: "urn:example:incident%2F123", Reason: "Owner-approved repair after the prospective exemption was omitted",
			Evidence: []string{"sha256:" + strings.Repeat("0", 64), "sha256:" + strings.Repeat("f", 64)},
		},
	}
}

func signRepairAdmissionProof(t *testing.T, command gateExemptionRepairCommand, ownerKeyOverride bool,
	issuedAt, expiresAt time.Time, nonce []byte, mutateBinding func(*gateExemptionRepairBinding),
) ([]byte, string, []byte) {
	t.Helper()
	canonical, hash, err := command.canonicalBytes()
	if err != nil {
		t.Fatalf("canonical private repair command: %v", err)
	}
	fixture := gateRepairFixture(t)
	binding := command.binding(hash)
	if mutateBinding != nil {
		mutateBinding(&binding)
	}
	if ownerKeyOverride {
		fixture.owner = key("untrusted repair owner")
	}
	proof := signGateRepairBinding(t, fixture, binding, issuedAt, expiresAt, nil, func(fields map[string]any) {
		fields["nonce"] = append([]byte(nil), nonce...)
	}, nil)
	return canonical, hash, proof
}

func repairNonce() []byte { return bytes.Repeat([]byte{0x37}, 16) }

func TestGateExemptionRepairAdmissionPersistsAndRecoversDetachedIdentity(t *testing.T) {
	fixture := newGateRepairAdmissionFixture(t)
	command := fixture.command
	canonical, hash, proof := signRepairAdmissionProof(t, command, false, fixture.store.now, fixture.store.now.Add(10*time.Minute), repairNonce(), nil)
	beforeEvents, beforeReceipts, beforeSubmissions := 0, 0, 0
	for query, destination := range map[string]*int{
		`SELECT count(*) FROM authority_events WHERE domain_id=?`:  &beforeEvents,
		`SELECT count(*) FROM terminal_receipts WHERE domain_id=?`: &beforeReceipts,
		`SELECT count(*) FROM submissions WHERE domain_id=?`:       &beforeSubmissions,
	} {
		if err := fixture.store.s.db.QueryRow(query, domainA).Scan(destination); err != nil {
			t.Fatal(err)
		}
	}
	admitted, err := fixture.store.s.admitGateExemptionRepair(context.Background(), command, proof, fixture.store.peer, fixture.store.now)
	if err != nil {
		t.Fatalf("admit valid detached repair proof: %v", err)
	}
	if !bytes.Equal(admitted.Command, canonical) || admitted.RequestHash != hash ||
		!bytes.Equal(admitted.Proof, proof) || !bytes.Equal(admitted.Nonce, repairNonce()) ||
		admitted.VerifiedAt != fixture.store.now.Format(time.RFC3339Nano) || bytes.Contains(admitted.Command, proof) {
		t.Fatalf("stored repair admission changed identity or proof: %+v", admitted)
	}
	var storedProof, storedCommand []byte
	var storedHash, verifiedAt string
	if err = fixture.store.s.db.QueryRow(`SELECT command,request_hash,proof,verified_at FROM gate_exemption_repair_admissions WHERE domain_id=? AND command_id=?`, domainA, command.ID).
		Scan(&storedCommand, &storedHash, &storedProof, &verifiedAt); err != nil || !bytes.Equal(storedCommand, canonical) || storedHash != hash ||
		!bytes.Equal(storedProof, proof) || verifiedAt != fixture.store.now.Format(time.RFC3339Nano) {
		t.Fatalf("persisted canonical retry identity/proof/time: %v", err)
	}
	var afterEvents, afterReceipts, afterSubmissions, reservations, sequenceHead int
	for query, destination := range map[string]*int{
		`SELECT count(*) FROM authority_events WHERE domain_id=?`:                       &afterEvents,
		`SELECT count(*) FROM terminal_receipts WHERE domain_id=?`:                      &afterReceipts,
		`SELECT count(*) FROM submissions WHERE domain_id=?`:                            &afterSubmissions,
		`SELECT count(*) FROM gate_exemption_repair_nonces WHERE domain_id=?`:           &reservations,
		`SELECT sequence_head FROM environments WHERE domain_id=? AND environment_id=?`: &sequenceHead,
	} {
		if query == `SELECT sequence_head FROM environments WHERE domain_id=? AND environment_id=?` {
			err = fixture.store.s.db.QueryRow(query, domainA, envA).Scan(destination)
		} else {
			err = fixture.store.s.db.QueryRow(query, domainA).Scan(destination)
		}
		if err != nil {
			t.Fatal(err)
		}
	}
	if afterEvents != beforeEvents || afterReceipts != beforeReceipts || afterSubmissions != beforeSubmissions || reservations != 1 || sequenceHead != 3 {
		t.Fatalf("admission leaked into command effects or failed to reserve identity: events %d/%d receipts %d/%d submissions %d/%d nonces=%d head=%d",
			beforeEvents, afterEvents, beforeReceipts, afterReceipts, beforeSubmissions, afterSubmissions, reservations, sequenceHead)
	}
	for _, definition := range operation.Catalogue() {
		if definition.Metadata().Operation.Name == gateExemptionRepairOperationName {
			t.Fatal("repair admission unexpectedly registered a public operation")
		}
	}
	if err = fixture.store.s.Close(); err != nil {
		t.Fatal(err)
	}
	reopened, err := OpenExisting(fixture.store.root)
	if err != nil {
		t.Fatalf("reopen durable proof admission: %v", err)
	}
	fixture.store.s = reopened
	defer func() { _ = reopened.Close() }()
	recovered, err := reopened.recoverGateExemptionRepair(context.Background(), canonical, hash, nil)
	if err != nil || !bytes.Equal(recovered.Proof, proof) || recovered.VerifiedAt != admitted.VerifiedAt {
		t.Fatalf("recover with omitted detached proof using stored verification time: %+v %v", recovered, err)
	}
	if equalProof, recoverErr := reopened.recoverGateExemptionRepair(context.Background(), canonical, hash, proof); recoverErr != nil || !bytes.Equal(equalProof.Proof, proof) {
		t.Fatalf("recover with byte-equal proof: %+v %v", equalProof, recoverErr)
	}
	substituted := append([]byte(nil), proof...)
	substituted[len(substituted)-1] ^= 1
	if _, recoverErr := reopened.recoverGateExemptionRepair(context.Background(), canonical, hash, substituted); !errors.Is(recoverErr, ErrConflict) {
		t.Fatalf("recovery accepted substituted proof: %v", recoverErr)
	}
	if _, replayErr := reopened.admitGateExemptionRepair(context.Background(), command, nil, fixture.store.peer, fixture.store.now.Add(30*time.Minute)); replayErr != nil {
		t.Fatalf("late authenticated replay without expired proof: %v", replayErr)
	}
	if _, replayErr := reopened.admitGateExemptionRepair(context.Background(), command, proof, fixture.store.peer, fixture.store.now.Add(30*time.Minute)); replayErr != nil {
		t.Fatalf("late authenticated replay with exact expired proof bytes: %v", replayErr)
	}
	if _, replayErr := reopened.admitGateExemptionRepair(context.Background(), command, substituted, fixture.store.peer, fixture.store.now.Add(30*time.Minute)); !errors.Is(replayErr, ErrConflict) {
		t.Fatalf("late authenticated replay replaced stored proof: %v", replayErr)
	}
	if _, err = reopened.db.Exec(`DELETE FROM gate_exemption_repair_nonces WHERE domain_id=? AND nonce=?`, domainA, repairNonce()); err == nil {
		t.Fatal("post-admission refusal released the immutable repair nonce")
	}
	if err = rebuildStep13Projection(reopened.db); err != nil {
		t.Fatalf("rebuild Step 13 projections with pending repair admission: %v", err)
	}
	if err = checkStep13State(reopened.db); err != nil {
		t.Fatalf("rebuilt gate history validation: %v", err)
	}
	if err = checkStep14State(reopened.db); err != nil {
		t.Fatalf("recovered proof admission after projection rebuild: %v", err)
	}
	if _, err = reopened.recoverGateExemptionRepair(context.Background(), canonical, hash, nil); err != nil {
		t.Fatalf("recover after Step 13 rebuild: %v", err)
	}
}

func TestGateExemptionRepairAdmissionRejectsInvalidProofWithoutBurningNonce(t *testing.T) {
	tests := []struct {
		name          string
		changeCommand func(*gateExemptionRepairCommand)
		changeBinding func(*gateExemptionRepairBinding)
		changeProof   func(t *testing.T, proof []byte) []byte
		wrongRoot     bool
		issuedOffset  time.Duration
		expiresOffset time.Duration
		missing       bool
	}{
		{name: "missing proof", missing: true},
		{name: "bad signature", changeProof: func(t *testing.T, proof []byte) []byte {
			t.Helper()
			var fields map[string]any
			if err := artifactDecoder.Unmarshal(proof, &fields); err != nil {
				t.Fatal(err)
			}
			signature := fields["signature"].([]byte)
			signature[0] ^= 1
			fields["signature"] = signature
			encoded, err := artifactEncoder.Marshal(fields)
			if err != nil {
				t.Fatal(err)
			}
			return encoded
		}},
		{name: "wrong root", wrongRoot: true},
		{name: "subject Repo mismatch", changeBinding: func(binding *gateExemptionRepairBinding) { binding.RepoID = repoB }},
		{name: "subject node mismatch", changeBinding: func(binding *gateExemptionRepairBinding) { binding.NodeID = claimTestID(999) }},
		{name: "subject gate mismatch", changeBinding: func(binding *gateExemptionRepairBinding) { binding.Gate = "other-gate" }},
		{name: "command Repo not in claim scope", changeCommand: func(command *gateExemptionRepairCommand) { command.RepoID = repoB }},
		{name: "unknown target node", changeCommand: func(command *gateExemptionRepairCommand) { command.NodeID = claimTestID(999) }},
		{name: "unknown gate", changeCommand: func(command *gateExemptionRepairCommand) { command.Gate = "other-gate" }},
		{name: "wrong declaration prefix", changeCommand: func(command *gateExemptionRepairCommand) {
			command.Boundary.PrefixDigest = digestBytes([]byte("not the declaration prefix"))
		}},
		{name: "future issue time without skew", issuedOffset: time.Second, expiresOffset: 2 * time.Minute},
		{name: "expiry at verification instant", issuedOffset: -time.Minute, expiresOffset: 0},
		{name: "expired proof", issuedOffset: -11 * time.Minute, expiresOffset: -time.Minute},
		{name: "lifetime exceeds ten minutes", issuedOffset: 0, expiresOffset: 11 * time.Minute},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			fixture := newGateRepairAdmissionFixture(t)
			badCommand := fixture.command
			if test.changeCommand != nil {
				test.changeCommand(&badCommand)
			}
			var proof []byte
			if !test.missing {
				issuedOffset, expiresOffset := test.issuedOffset, test.expiresOffset
				if issuedOffset == 0 && expiresOffset == 0 {
					issuedOffset, expiresOffset = -time.Minute, time.Minute
				}
				_, _, proof = signRepairAdmissionProof(t, badCommand, test.wrongRoot,
					fixture.store.now.Add(issuedOffset), fixture.store.now.Add(expiresOffset), repairNonce(), test.changeBinding)
				if test.changeProof != nil {
					proof = test.changeProof(t, proof)
				}
			}
			if _, err := fixture.store.s.admitGateExemptionRepair(context.Background(), badCommand, proof, fixture.store.peer, fixture.store.now); err == nil {
				t.Fatal("invalid or inapplicable repair proof was admitted")
			}
			var admissions, nonces int
			if err := fixture.store.s.db.QueryRow(`SELECT count(*) FROM gate_exemption_repair_admissions WHERE domain_id=?`, domainA).Scan(&admissions); err != nil {
				t.Fatal(err)
			}
			if err := fixture.store.s.db.QueryRow(`SELECT count(*) FROM gate_exemption_repair_nonces WHERE domain_id=?`, domainA).Scan(&nonces); err != nil || admissions != 0 || nonces != 0 {
				t.Fatalf("pre-admission rejection burned state: admissions=%d nonces=%d err=%v", admissions, nonces, err)
			}
			canonical, hash, validProof := signRepairAdmissionProof(t, fixture.command, false,
				fixture.store.now.Add(-time.Minute), fixture.store.now.Add(time.Minute), repairNonce(), nil)
			if _, err := fixture.store.s.admitGateExemptionRepair(context.Background(), fixture.command, validProof, fixture.store.peer, fixture.store.now); err != nil {
				t.Fatalf("same nonce was burned before admission (%s / %s): %v", canonical, hash, err)
			}
		})
	}
}

func TestGateExemptionRepairNonceIsScopedOnlyToDomain(t *testing.T) {
	fixture := newGateRepairAdmissionFixture(t)
	_, _, proof := signRepairAdmissionProof(t, fixture.command, false, fixture.store.now.Add(-time.Minute), fixture.store.now.Add(time.Minute), repairNonce(), nil)
	if _, err := fixture.store.s.admitGateExemptionRepair(context.Background(), fixture.command, proof, fixture.store.peer, fixture.store.now); err != nil {
		t.Fatalf("admit first nonce: %v", err)
	}
	domain, _ := identity(domainB, 7)
	if err := fixture.store.s.BootstrapDomain(context.Background(), domain, repoB); err != nil {
		t.Fatalf("bootstrap second nonce scope: %v", err)
	}
	tx, err := fixture.store.s.db.BeginTx(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = tx.Rollback() }()
	if _, err = tx.Exec(`INSERT INTO gate_exemption_repair_nonces(domain_id,nonce,command_id) VALUES(?,?,?)`, domainB, repairNonce(), claimTestID(901)); err != nil {
		t.Fatalf("same nonce was not available in another domain: %v", err)
	}
	if _, err = tx.Exec(`INSERT INTO gate_exemption_repair_nonces(domain_id,nonce,command_id) VALUES(?,?,?)`, domainA, repairNonce(), claimTestID(902)); err == nil {
		t.Fatal("same-domain nonce reuse was accepted")
	}
	if _, err = tx.Exec(`INSERT INTO gate_exemption_repair_nonces(domain_id,nonce,command_id) VALUES(?,?,?)`, domainB, repairNonce(), claimTestID(903)); err == nil {
		t.Fatal("duplicate nonce in second domain was accepted")
	}
}

func TestGateExemptionRepairAdmissionStorageFailureRollsBackNonce(t *testing.T) {
	fixture := newGateRepairAdmissionFixture(t)
	_, _, proof := signRepairAdmissionProof(t, fixture.command, false, fixture.store.now.Add(-time.Minute), fixture.store.now.Add(time.Minute), repairNonce(), nil)
	if _, err := fixture.store.s.db.Exec(`CREATE TRIGGER test_fail_repair_admission BEFORE INSERT ON gate_exemption_repair_admissions BEGIN SELECT RAISE(ABORT,'injected storage failure'); END`); err != nil {
		t.Fatal(err)
	}
	if _, err := fixture.store.s.admitGateExemptionRepair(context.Background(), fixture.command, proof, fixture.store.peer, fixture.store.now); err == nil {
		t.Fatal("injected durable admission failure was ignored")
	}
	if _, err := fixture.store.s.db.Exec(`DROP TRIGGER test_fail_repair_admission`); err != nil {
		t.Fatal(err)
	}
	var admissions, nonces int
	if err := fixture.store.s.db.QueryRow(`SELECT count(*) FROM gate_exemption_repair_admissions WHERE domain_id=?`, domainA).Scan(&admissions); err != nil {
		t.Fatal(err)
	}
	if err := fixture.store.s.db.QueryRow(`SELECT count(*) FROM gate_exemption_repair_nonces WHERE domain_id=?`, domainA).Scan(&nonces); err != nil || admissions != 0 || nonces != 0 {
		t.Fatalf("storage failure partially committed proof or nonce: admissions=%d nonces=%d err=%v", admissions, nonces, err)
	}
	if _, err := fixture.store.s.admitGateExemptionRepair(context.Background(), fixture.command, proof, fixture.store.peer, fixture.store.now); err != nil {
		t.Fatalf("retry after atomic rollback: %v", err)
	}
}

func downgradeStep14ToV12(t *testing.T, root string) {
	t.Helper()
	db, err := connect(filepath.Join(root, "authority.db"), "rw", false)
	if err != nil {
		t.Fatal(err)
	}
	for index := len(step14Schema) - 1; index >= 0; index-- {
		object := step14Schema[index]
		if _, err = db.Exec("DROP " + object.kind + " IF EXISTS " + object.name); err != nil {
			_ = db.Close()
			t.Fatalf("drop v13 object %s: %v", object.name, err)
		}
	}
	if _, err = db.Exec(`DROP TABLE schema_migrations`); err != nil {
		_ = db.Close()
		t.Fatal(err)
	}
	if _, err = db.Exec(step13MigrationMarker.sql); err != nil {
		_ = db.Close()
		t.Fatal(err)
	}
	markers := []string{
		"baseline", "environment-and-artifacts", "submissions-and-receipts", "prefix-snapshot-blob-transfer",
		"claims-grants-journals-close", "m5-lab-genesis-grant-consumption", "step-8-provisional-birth-projection",
		"step-9-birth-journal-receipt-barrier", "step-10-content-and-findings", "step-16-claim-journal-sequence-order",
		"step-4-stage-step-operations", "step-7-authority-gate-config-projections",
	}
	for index, marker := range markers {
		if _, err = db.Exec(`INSERT INTO schema_migrations(version,name) VALUES(?,?)`, index+1, marker); err != nil {
			_ = db.Close()
			t.Fatal(err)
		}
	}
	if _, err = db.Exec(`PRAGMA user_version=12`); err != nil {
		_ = db.Close()
		t.Fatal(err)
	}
	if err = checkSchemaVersion(db, 12); err != nil {
		_ = db.Close()
		t.Fatalf("downgraded v12 fixture validation: %v", err)
	}
	if err = db.Close(); err != nil {
		t.Fatal(err)
	}
}

func TestUpgradeV12InstallsPrivateRepairAdmissionSchema(t *testing.T) {
	fixture := newGateRepairAdmissionFixture(t)
	root := fixture.store.root
	var beforeEvents int
	if err := fixture.store.s.db.QueryRow(`SELECT count(*) FROM authority_events WHERE domain_id=?`, domainA).Scan(&beforeEvents); err != nil {
		t.Fatal(err)
	}
	if err := fixture.store.s.Close(); err != nil {
		t.Fatal(err)
	}
	downgradeStep14ToV12(t, root)
	if _, err := OpenExisting(root); !errors.Is(err, ErrInvalidStore) {
		t.Fatalf("ordinary open silently accepted v12 store: %v", err)
	}
	if err := UpgradeV12(root); err != nil {
		t.Fatalf("explicit v12 migration: %v", err)
	}
	reopened, err := OpenExisting(root)
	if err != nil {
		t.Fatalf("open migrated v13 store: %v", err)
	}
	defer func() { _ = reopened.Close() }()
	if err = checkStep13State(reopened.db); err != nil {
		t.Fatalf("v12 gate history changed during migration: %v", err)
	}
	if err = checkStep14State(reopened.db); err != nil {
		t.Fatalf("validate private admission schema: %v", err)
	}
	var afterEvents, declarations int
	if err = reopened.db.QueryRow(`SELECT count(*) FROM authority_events WHERE domain_id=?`, domainA).Scan(&afterEvents); err != nil {
		t.Fatal(err)
	}
	if err = reopened.db.QueryRow(`SELECT count(*) FROM m6_gate_declarations WHERE domain_id=? AND repo_id=? AND gate=?`, domainA, repoA, "reviewed-local").Scan(&declarations); err != nil {
		t.Fatal(err)
	}
	if afterEvents != beforeEvents || declarations != 1 {
		t.Fatalf("v12 migration changed signed gate history: event count %d/%d declarations=%d", beforeEvents, afterEvents, declarations)
	}
	backup, err := connect(filepath.Join(root, "authority-v12.backup.db"), "rw", true)
	if err != nil {
		t.Fatalf("open retained v12 backup: %v", err)
	}
	if err = errors.Join(checkSchemaVersion(backup, 12), checkStep13State(backup), backup.Close()); err != nil {
		t.Fatalf("retained v12 backup validation: %v", err)
	}
}
