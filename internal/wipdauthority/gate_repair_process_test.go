package wipdauthority

import (
	"bytes"
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/fxamacker/cbor/v2"
	"github.com/procrastivity/wip/internal/authoritystore"
	"github.com/procrastivity/wip/internal/operation"
	"github.com/procrastivity/wip/internal/wipd"
	"github.com/procrastivity/wip/internal/wipdjournal"
	"github.com/procrastivity/wip/internal/wipdwire"
	_ "modernc.org/sqlite"
)

func TestGateRepairThroughAuthenticatedWipdProcess(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("authenticated local wipd IPC is Linux-only")
	}
	for _, scenario := range []struct {
		name    string
		success bool
		ackLoss string
	}{
		{"factual-refusal", false, ""},
		{"refusal-ack-before-commit", false, "before"},
		{"refusal-ack-after-commit", false, "after"},
		{"missing-prospective-exemption", true, ""},
	} {
		t.Run(scenario.name, func(t *testing.T) {
			runGateRepairThroughAuthenticatedWipdProcess(t, scenario.success, scenario.ackLoss)
		})
	}
}

func runGateRepairThroughAuthenticatedWipdProcess(t *testing.T, success bool, ackLoss string) {
	ctx := context.Background()
	fixture := newM5CommandFixture(t)
	registry, err := NewM6Step5Registry()
	if err != nil {
		t.Fatal(err)
	}
	config := fixture.config
	config.Registry = registry
	fixture.server, err = NewM6LabServer(fixture.profile, fixture.serverCert, config)
	if err != nil {
		t.Fatal(err)
	}
	var trace m6AuthorityHTTPTrace
	if success {
		fixture.server.http.Handler = dropFirstProcessRepairTerminal(fixture.server.http.Handler, repairTransportID(67))
	}
	if ackLoss != "" {
		fixture.server.http.Handler = dropFirstProcessRepairAck(fixture.server.http.Handler, ackLoss)
	}
	fixture.server.http.Handler = traceM6AuthorityHTTP(fixture.server.http.Handler, &trace)
	authorityCtx, stopAuthority := context.WithCancel(ctx)
	served := make(chan error, 1)
	go func() { served <- fixture.server.Serve(authorityCtx, fixture.listener) }()
	t.Cleanup(func() {
		stopAuthority()
		select {
		case err := <-served:
			if err != nil {
				t.Errorf("authority shutdown: %v", err)
			}
		case <-time.After(3 * time.Second):
			t.Error("authority did not shut down")
		}
	})
	root, err := os.MkdirTemp("", "repair-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(root) })
	environment, err := prepareM5ProcessEnvironment(t, fixture, root, "env", m5TestEnv,
		fixture.environment, fixture.clientCert.Certificate[0], fixture.clientCert.Certificate[1], "m6-step5")
	if err != nil {
		t.Fatal(err)
	}
	binary := m5WipdBinary(t)
	client, stopDaemon, output := startWipdForBirthReleaseRecovery(t, binary, environment.profileRoot, true)
	defer func() { _ = client.Close(); stopDaemon() }()
	command := func(id string, seq uint64, request operation.Request) operation.Command {
		return operation.Command{
			ID: id, AuthorityDomainID: m5TestDomain, ExpectedAuthorityEpoch: 1,
			EnvironmentID: m5TestEnv, EnvironmentSequence: seq,
			ActedAt: time.Now().UTC().Format(time.RFC3339Nano), CorrelationCommandID: id, Request: request,
		}
	}
	create := command(repairTransportID(60), 1, operation.Request{
		Operation: operation.MatterCreateV1.Metadata().Operation, Actor: "human",
		Context: operation.Context{Repo: m5TestRepo},
		Input:   operation.MatterCreateInput{Title: "Repair target", Locator: "process-repair-target"},
	})
	created, err := client.ExecuteCommand(ctx, create)
	matter, ok := created.Output.(operation.MatterCreateOutput)
	if err != nil || created.Code != operation.ResultSucceeded || !ok {
		t.Fatalf("create: %+v %v daemon=%s", created, err, output.String())
	}
	if _, err = client.ReleaseBirthClaim(ctx, matter.ID, repairTransportID(61), operation.Actor("human")); err != nil {
		t.Fatalf("birth release: %v", err)
	}
	clone, worktree := repairTransportID(63), repairTransportID(64)
	acquired, err := client.AcquireClaim(ctx, repairTransportID(62), matter.ID,
		clone, worktree, repairTransportID(65), operation.Actor("human"))
	if err != nil || acquired.Code != operation.ResultSucceeded || acquired.Grant == nil {
		t.Fatalf("acquire: %+v %v daemon=%s", acquired, err, output.String())
	}
	claim := &operation.ClaimContext{ID: acquired.Grant.ClaimID, Epoch: fmt.Sprint(acquired.Grant.ClaimEpoch)}
	claimContext := operation.Context{Repo: m5TestRepo, Clone: clone, Worktree: worktree}
	declarationSequence := uint64(4)
	if success {
		for _, step := range []struct {
			id    int
			def   operation.Definition
			input operation.Input
		}{
			{68, operation.MatterStartV1, operation.NodeLifecycleInput{NodeID: matter.ID}},
			{69, operation.MatterFinishV1, operation.MatterFinishInput{MatterID: matter.ID}},
		} {
			lifecycle := command(repairTransportID(step.id), declarationSequence, operation.Request{
				Operation: step.def.Metadata().Operation, Actor: "human", Context: claimContext, Claim: claim, Input: step.input,
			})
			got, runErr := client.ExecuteCommand(ctx, lifecycle)
			if runErr != nil || got.Code != operation.ResultSucceeded {
				t.Fatalf("lifecycle %s: %+v %v daemon=%s", step.def.Metadata().Operation.Name, got, runErr, output.String())
			}
			declarationSequence++
		}
	}
	declare := command(repairTransportID(66), declarationSequence, operation.Request{
		Operation: operation.GateDeclareV1.Metadata().Operation, Actor: "human",
		Context: claimContext, Claim: claim,
		Input: operation.GateDeclareInput{Gate: "repair-target", Scale: "matter"},
	})
	// Gate declaration is a store foundation, not part of the production M6
	// daemon catalogue. Seed that accepted historical command and exact fold
	// through the existing store/journal APIs without exposing another operation.
	if err = client.Close(); err != nil {
		t.Fatal(err)
	}
	stopDaemon()
	identity := wipdjournal.Identity{
		RepoID: m5TestRepo, DomainID: m5TestDomain, AuthorityEpoch: 1,
		EnvironmentID: m5TestEnv, OwnerRootSPKI: fixture.profile.OwnerRootSPKI(),
	}
	seedJournal, err := wipdjournal.Open(filepath.Join(environment.profileRoot, "environment-journal"), identity)
	if err != nil {
		t.Fatal(err)
	}
	seedEntry, err := seedJournal.PrepareCanonicalSubmission(declare, "wipd.command-submit/1", nil)
	if err != nil {
		t.Fatal(err)
	}
	seedSnapshot, err := seedJournal.InstallSnapshot(ctx)
	if err != nil {
		t.Fatal(err)
	}
	seedSnapshot, err = seedJournal.AdmitPending(ctx, seedSnapshot.Expectation(), declare.ID)
	if err != nil {
		t.Fatal(err)
	}
	declarationStatus, err := fixture.store.SubmitCommand(ctx, declare, seedEntry.RequestHash, fixture.peer, time.Now().UTC())
	if err != nil || declarationStatus.Owner == nil {
		t.Fatalf("fixture declaration admission: %+v %v", declarationStatus, err)
	}
	declarationEventID, err := randomULID(time.Now().UTC())
	if err != nil {
		t.Fatal(err)
	}
	declarationStatus, err = fixture.store.CompleteCommand(ctx, declarationStatus.Owner, operation.Result{Code: operation.ResultSucceeded}, m5TestRepo,
		declarationEventID, time.Now().UTC(), fixture.config.SignArtifact)
	if err != nil {
		t.Fatal(err)
	}
	anchor, err := fixture.store.CurrentPrefixAnchor(ctx, m5TestDomain)
	if err != nil || anchor.EventCount == 0 || anchor.EventID == "" {
		t.Fatalf("declaration anchor: %+v %v", anchor, err)
	}
	if success {
		rewriteProcessGateDeclaration(t, filepath.Join(fixture.root, "authority.db"), anchor.EventID, matter.ID)
		anchor, err = fixture.store.CurrentPrefixAnchor(ctx, m5TestDomain)
		if err != nil {
			t.Fatal(err)
		}
	}
	db, err := sql.Open("sqlite", filepath.Join(fixture.root, "authority.db"))
	if err != nil {
		t.Fatal(err)
	}
	var record []byte
	err = db.QueryRow(`SELECT record FROM authority_events WHERE domain_id=? AND event_id=?`, m5TestDomain, anchor.EventID).Scan(&record)
	if closeErr := db.Close(); err != nil || closeErr != nil {
		t.Fatalf("read declaration fixture: %v %v", err, closeErr)
	}
	endID := anchor.EventID
	end := wipdwire.PrefixAnchor{EventCount: anchor.EventCount, EventID: &endID, Digest: anchor.Digest}
	transfer, err := wipdjournal.VerifyTransfer(m5TestDomain, 1, seedSnapshot.Anchor, end,
		[]wipdwire.EventRecord{{EventID: endID, Record: record}}, wipdwire.BlobManifest{
			Schema: "wipd.blob-manifest/1", DomainID: m5TestDomain, Epoch: 1, AsOf: end,
			Entries: []wipdwire.BlobManifestEntry{}, Digest: emptyManifestDigest(),
		})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = seedJournal.InstallFold(ctx, seedSnapshot.Expectation(), seedEntry, operation.ResultSucceeded, declarationStatus.Receipt, transfer); err != nil {
		t.Fatalf("install declaration fixture: %v", err)
	}
	seedBinding, err := fixture.store.GetCurrentClaimJournal(ctx, m5TestDomain, 1, m5TestEnv, claim.ID,
		acquired.Grant.ClaimEpoch, matter.ID, acquired.Grant.DispatchID)
	if err != nil {
		t.Fatal(err)
	}
	declarationPosition := uint64(1)
	if success {
		declarationPosition = 2 // Matter start is claim-delivered; finish is authority-delivered.
	}
	if err = fixture.store.AcknowledgeClaimJournalEntry(ctx, m5TestDomain, seedBinding.JournalID, declarationPosition, declarationStatus.Receipt, anchor); err != nil {
		t.Fatal(err)
	}
	if err = seedJournal.Close(); err != nil {
		t.Fatal(err)
	}
	client, stopDaemon, output = startWipdForBirthReleaseRecovery(t, binary, environment.profileRoot, true)
	repair := command(repairTransportID(67), declarationSequence+1, operation.Request{
		Operation: operation.GateExemptionRepairV1.Metadata().Operation, Actor: "human",
		Context: claimContext, Claim: claim,
		Input: operation.GateExemptionRepairInput{
			NodeID: matter.ID, Gate: "repair-target", EventCount: anchor.EventCount,
			HighWaterEventID: anchor.EventID, PrefixDigest: anchor.Digest,
			IncidentRef: "urn:example:repair-process", Reason: "Owner-approved omitted prospective exemption",
			EvidenceRefs: []string{"sha256:" + strings.Repeat("a", 64)},
		},
	})
	hash := m5CommandHash(t, repair)
	proof := processRepairProof(t, fixture, repair, 0x7a)
	if _, err = client.ExecuteCommand(ctx, repair); err == nil || !strings.Contains(err.Error(), "protocol.unsupported-extension") {
		t.Fatalf("v1 repair must be denied before journal/authority: %v", err)
	}
	before, err := fixture.store.CurrentPrefixAnchor(ctx, m5TestDomain)
	if err != nil || before != anchor {
		t.Fatalf("v1 changed prefix: %+v %v", before, err)
	}
	if _, err = fixture.store.QueryCommand(ctx, m5TestDomain, repair.ID, hash, 1, fixture.peer, m5TestEnv, time.Now()); !errors.Is(err, authoritystore.ErrNotFound) {
		t.Fatalf("v1 reached authority: %v", err)
	}
	authorityDB, err := sql.Open("sqlite", filepath.Join(fixture.root, "authority.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if closeErr := authorityDB.Close(); closeErr != nil {
			t.Error(closeErr)
		}
	}()
	var count int
	for _, table := range []string{"gate_exemption_repair_admissions", "gate_exemption_repair_nonces", "claim_journal_entries"} {
		if err = authorityDB.QueryRow("SELECT count(*) FROM "+table+" WHERE domain_id=? AND command_id=?",
			m5TestDomain, repair.ID).Scan(&count); err != nil || count != 0 {
			t.Fatalf("v1 reserved %s: %d %v", table, count, err)
		}
	}
	// Read the local DB while the daemon owns its writer lease: absence must be
	// established before v2 reuses the same immutable ID and sequence.
	localDB, err := sql.Open("sqlite", filepath.Join(environment.profileRoot, "environment-journal", "command-journal.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if closeErr := localDB.Close(); closeErr != nil {
			t.Error(closeErr)
		}
	}()
	if err = localDB.QueryRow("SELECT count(*) FROM commands WHERE command_id=?", repair.ID).Scan(&count); err != nil || count != 0 {
		t.Fatalf("v1 persisted local command: %d %v", count, err)
	}
	if success {
		checkProcessRepairSuccess(ctx, t, client, stopDaemon, output, fixture, environment.profileRoot,
			identity, binary, &trace, command, claimContext, claim, matter.ID, anchor, repair, proof, declarationSequence)
		return
	}
	result, err := client.ExecuteCommandV2(ctx, repair, proof)
	if ackLoss != "" {
		if err == nil {
			t.Fatalf("lost refusal ACK unexpectedly returned success: %+v", result)
		}
		if err = client.Close(); err != nil {
			t.Fatal(err)
		}
		stopDaemon()
		client, stopDaemon, output = startWipdForBirthReleaseRecovery(t, binary, environment.profileRoot, true)
		result, err = client.ExecuteCommandV2(ctx, repair, nil)
	}
	if err != nil || result.Code != operation.ResultRefused || result.Problem == nil ||
		string(result.Problem.Code) != "refusal.gate-repair-lifecycle" || result.Output != nil {
		t.Fatalf("factual v2 refusal: %+v %v daemon=%s trace=%v", result, err, output.String(), trace.snapshot())
	}
	if !traceContains(trace.snapshot(), "/wipd/v1/exchange request=command.submit response=submission.accepted,command.terminal") ||
		!traceContains(trace.snapshot(), "/wipd/v1/exchange request=claim-journal.ack response=claim-journal.acked") {
		t.Fatalf("missing authority submission/ACK hop: %v", trace.snapshot())
	}
	if err = client.Close(); err != nil {
		t.Fatal(err)
	}
	stopDaemon()
	journal, err := wipdjournal.Open(filepath.Join(environment.profileRoot, "environment-journal"), identity)
	if err != nil {
		t.Fatal(err)
	}
	birthRelease, birthErr := journal.BirthReleaseAttempt(repairTransportID(61))
	acquireAttempt, acquireErr := journal.ClaimAcquireAttempt(repairTransportID(62))
	if birthErr != nil || acquireErr != nil || birthRelease.EnvironmentSeq != 2 || acquireAttempt.EnvironmentSeq != 3 {
		t.Fatalf("serialized birth release/acquire: %+v %v; %+v %v", birthRelease, birthErr, acquireAttempt, acquireErr)
	}
	entries, err := journal.Entries()
	if err != nil || len(entries) != 3 || entries[0].EnvironmentSeq != 1 || entries[1].EnvironmentSeq != 4 {
		t.Fatalf("create/declaration/repair journal: %d %v", len(entries), err)
	}
	entry := entries[2]
	if entry.Command.ID != repair.ID || entry.EnvironmentSeq != 5 || entry.SubmissionSchema != wipdwire.CommandSubmitV2Feature ||
		!bytes.Equal(entry.DetachedProof, proof) || entry.State != wipdjournal.StateAttemptPrepared || entry.JournalPosition != 0 {
		t.Fatalf("retained v2 proof/identity: %+v", entry)
	}
	installed, err := journal.InstallSnapshot(ctx)
	if err != nil {
		t.Fatal(err)
	}
	localReceipt, ok := installed.Receipts[repair.ID]
	if !ok || localReceipt.ResultCode != operation.ResultRefused || localReceipt.RequestHash != hash || localReceipt.EnvironmentSeq != 5 ||
		installed.Anchor.EventCount != anchor.EventCount || installed.Anchor.Digest != anchor.Digest ||
		installed.Anchor.EventID == nil || *installed.Anchor.EventID != anchor.EventID {
		t.Fatalf("installed refusal/prefix: %+v found=%t anchor=%+v", localReceipt, ok, installed.Anchor)
	}
	fields, err := wipdwire.DecodeCanonicalMap(localReceipt.CanonicalReceipt,
		"schema", "domain_id", "authority_epoch", "identity_schema", "command_id", "request_hash", "operation", "environment", "result", "accepted_events")
	if err != nil {
		t.Fatal(err)
	}
	receiptResult, ok := fields["result"].(map[string]any)
	if !ok || fields["schema"] != "wipd.terminal-receipt/1" || fields["command_id"] != repair.ID ||
		fields["request_hash"] != hash || fields["accepted_events"] != nil || receiptResult["code"] != "result.refused" ||
		receiptResult["problem_code"] != "refusal.gate-repair-lifecycle" || receiptResult["output"] != nil {
		t.Fatalf("public installed receipt: %#v", fields)
	}
	if err = journal.Close(); err != nil {
		t.Fatal(err)
	}
	status, err := fixture.store.QueryCommand(ctx, m5TestDomain, repair.ID, hash, 1, fixture.peer, m5TestEnv, time.Now())
	if err != nil || status.Pending || !bytes.Equal(status.Receipt, localReceipt.CanonicalReceipt) || len(status.SignedReceipt) == 0 {
		t.Fatalf("public QueryCommand vs local receipt: %+v %v", status, err)
	}
	var state string
	var position uint64
	if err = authorityDB.QueryRow("SELECT state,position FROM claim_journal_entries WHERE domain_id=? AND command_id=?",
		m5TestDomain, repair.ID).Scan(&state, &position); err != nil || state != "quarantined" || position != 2 {
		t.Fatalf("exact refusal ACK: state=%s position=%d err=%v", state, position, err)
	}
	if err = authorityDB.QueryRow("SELECT count(*) FROM authority_events WHERE domain_id=? AND command_id=?", m5TestDomain, repair.ID).Scan(&count); err != nil || count != 0 {
		t.Fatalf("refusal emitted event: %d %v", count, err)
	}
	client, stopDaemon, output = startWipdForBirthReleaseRecovery(t, binary, environment.profileRoot, true)
	for _, replay := range [][]byte{nil, proof} {
		got, replayErr := client.ExecuteCommandV2(ctx, repair, replay)
		if replayErr != nil || got.Code != operation.ResultRefused || got.Problem == nil || got.Problem.Code != result.Problem.Code {
			t.Fatalf("v2 replay proof omitted=%t: %+v %v daemon=%s", replay == nil, got, replayErr, output.String())
		}
	}
	changed := bytes.Clone(proof)
	changed[len(changed)-1] ^= 1
	if _, err = client.ExecuteCommandV2(ctx, repair, changed); err == nil || !strings.Contains(err.Error(), "command.id-conflict") {
		t.Fatalf("substituted proof did not conflict: %v", err)
	}
	if _, err = client.ExecuteCommand(ctx, repair); err == nil || !strings.Contains(err.Error(), "protocol.unsupported-extension") {
		t.Fatalf("retry downgraded to v1: %v", err)
	}
	after, err := fixture.store.CurrentPrefixAnchor(ctx, m5TestDomain)
	if err != nil || after != before {
		t.Fatalf("replay changed prefix: before=%+v after=%+v err=%v", before, after, err)
	}
}

// Only this disposable fixture's final historical declaration is rewritten.
// Its prospective exemption is absent from both the event and mutable projection.
func rewriteProcessGateDeclaration(t *testing.T, path, eventID, matterID string) {
	t.Helper()
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if err := db.Close(); err != nil {
			t.Error(err)
		}
	}()
	tx, err := db.Begin()
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = tx.Rollback() }()
	var record []byte
	var position uint64
	if err = tx.QueryRow(`SELECT record,position FROM authority_events WHERE domain_id=? AND event_id=?`, m5TestDomain, eventID).Scan(&record, &position); err != nil || position < 2 {
		t.Fatalf("last declaration: position=%d err=%v", position, err)
	}
	var fields map[string]cbor.RawMessage
	if err = cbor.Unmarshal(record, &fields); err != nil {
		t.Fatal(err)
	}
	var payload map[string]cbor.RawMessage
	if err = cbor.Unmarshal(fields["payload"], &payload); err != nil {
		t.Fatal(err)
	}
	if _, ok := payload["exempt"]; !ok {
		t.Fatal("fixture declaration did not produce prospective exemption")
	}
	delete(payload, "exempt")
	fields["payload"], err = wipdwire.EncodeCanonical(payload)
	if err != nil {
		t.Fatal(err)
	}
	record, err = wipdwire.EncodeCanonical(fields)
	if err != nil {
		t.Fatal(err)
	}
	var previous, trigger string
	if err = tx.QueryRow(`SELECT prefix_digest FROM authority_events WHERE domain_id=? AND position=?`, m5TestDomain, position-1).Scan(&previous); err != nil {
		t.Fatal(err)
	}
	if err = tx.QueryRow(`SELECT sql FROM sqlite_master WHERE type='trigger' AND name='authority_events_immutable'`).Scan(&trigger); err != nil {
		t.Fatal(err)
	}
	prior, err := hex.DecodeString(strings.TrimPrefix(previous, "sha256:"))
	if err != nil || len(prior) != sha256.Size {
		t.Fatalf("previous prefix digest %q: %v", previous, err)
	}
	var length [8]byte
	binary.BigEndian.PutUint64(length[:], uint64(len(record)))
	h := sha256.New()
	_, _ = h.Write([]byte("wipd/event-prefix-step/v1\x00"))
	_, _ = h.Write(prior)
	_, _ = h.Write(length[:])
	_, _ = h.Write(record)
	if _, err = tx.Exec(`DROP TRIGGER authority_events_immutable`); err != nil {
		t.Fatal(err)
	}
	if _, err = tx.Exec(`UPDATE authority_events SET record=?,prefix_digest=? WHERE domain_id=? AND position=?`,
		record, "sha256:"+hex.EncodeToString(h.Sum(nil)), m5TestDomain, position); err != nil {
		t.Fatal(err)
	}
	if _, err = tx.Exec(trigger); err != nil {
		t.Fatal(err)
	}
	result, err := tx.Exec(`DELETE FROM m6_gate_states WHERE domain_id=? AND node_id=? AND gate=? AND state='exempt'`,
		m5TestDomain, matterID, "repair-target")
	if err != nil {
		t.Fatal(err)
	}
	if removed, rowsErr := result.RowsAffected(); rowsErr != nil || removed != 1 {
		t.Fatalf("remove declaration exemption: %d %v", removed, rowsErr)
	}
	if err = tx.Commit(); err != nil {
		t.Fatal(err)
	}
}

func processRepairProof(t *testing.T, fixture *m5CommandFixture, repair operation.Command, nonce byte) []byte {
	t.Helper()
	input := repair.Request.Input.(operation.GateExemptionRepairInput)
	subject, err := wipdwire.EncodeCanonical(map[string]any{
		"schema": "wipd.gate-exemption-repair-subject/1", "command_id": repair.ID,
		"request_hash": m5CommandHash(t, repair), "repo_id": m5TestRepo, "node_id": input.NodeID, "gate": input.Gate,
		"event_count": input.EventCount, "high_water_event_id": input.HighWaterEventID,
		"prefix_digest": input.PrefixDigest, "incident_ref": input.IncidentRef,
		"reason_digest": repairTransportHashBytes([]byte(input.Reason)), "evidence_refs": input.EvidenceRefs,
	})
	if err != nil {
		t.Fatal(err)
	}
	issued := time.Now().UTC().Add(-time.Minute)
	return m5TestOwnerArtifact(t, m5TestKey("owner-root"), m5TestPublicDigest(fixture.ownerRoot), m5TestDomain,
		"owner-attestation", "wipd.owner-attestation/1", map[string]any{
			"schema": "wipd.owner-attestation/1", "action": "gate-exemption-repair",
			"domain_id": m5TestDomain, "current_epoch": uint64(1), "next_epoch": nil,
			"subject_schema": "wipd.gate-exemption-repair-subject/1", "subject_digest": repairTransportHashBytes(subject),
			"subject": subject, "nonce": bytes.Repeat([]byte{nonce}, 16),
			"issued_at": issued.Format(time.RFC3339Nano), "expires_at": issued.Add(2 * time.Minute).Format(time.RFC3339Nano),
			"loss_accepted": false,
		}, issued)
}

func checkProcessRepairSuccess(ctx context.Context, t *testing.T, client *wipd.Client, stopDaemon func(), output *bytes.Buffer,
	fixture *m5CommandFixture, profileRoot string, identity wipdjournal.Identity, binary string, trace *m6AuthorityHTTPTrace,
	command func(string, uint64, operation.Request) operation.Command, claimContext operation.Context, claim *operation.ClaimContext,
	matterID string, boundary authoritystore.PrefixAnchor, repair operation.Command, proof []byte, declarationSequence uint64,
) {
	t.Helper()
	result, err := client.ExecuteCommandV2(ctx, repair, proof)
	got, ok := result.Output.(operation.GateExemptionRepairOutput)
	if err != nil || result.Code != operation.ResultSucceeded || !ok || got.AlreadyExempt || got.Gate != "repair-target" || got.NodeID != matterID {
		observed := trace.snapshot()
		if len(observed) > 12 {
			observed = observed[:12]
		}
		t.Fatalf("effectful repair: %+v %v daemon=%s first trace=%v", result, err, output.String(), observed)
	}
	if !traceContains(trace.snapshot(), "/wipd/v1/exchange request=command.submit response=submission.accepted status=200") ||
		!traceContains(trace.snapshot(), "/wipd/v1/exchange request=receipt.query response=command.terminal") ||
		!traceContains(trace.snapshot(), "/wipd/v1/exchange request=claim-journal.ack response=claim-journal.acked") {
		t.Fatalf("missing success HTTP hops: %v", trace.snapshot())
	}
	db, err := sql.Open("sqlite", filepath.Join(fixture.root, "authority.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if err := db.Close(); err != nil {
			t.Error(err)
		}
	}()
	var count int
	var eventID, state string
	if err = db.QueryRow(`SELECT event_id FROM authority_events WHERE domain_id=? AND command_id=?`, m5TestDomain, repair.ID).Scan(&eventID); err != nil {
		t.Fatalf("single repair event: %v", err)
	}
	var record []byte
	if err = db.QueryRow(`SELECT record FROM authority_events WHERE domain_id=? AND event_id=?`, m5TestDomain, eventID).Scan(&record); err != nil {
		t.Fatal(err)
	}
	fields, err := wipdwire.DecodeCanonicalMap(record, "schema", "event_id", "domain_id", "command_id", "request_hash", "environment", "acted_at", "occurred_at", "kind", "subject_id", "repo_id", "payload")
	if err != nil || fields["kind"] != "gate.exemption-repaired" || fields["subject_id"] != matterID || fields["command_id"] != repair.ID {
		t.Fatalf("repair event: %#v %v", fields, err)
	}
	if err = db.QueryRow(`SELECT state FROM m6_gate_states WHERE domain_id=? AND node_id=? AND gate=?`, m5TestDomain, matterID, "repair-target").Scan(&state); err != nil || state != "exempt" {
		t.Fatalf("repaired state=%s err=%v", state, err)
	}
	if err = client.Close(); err != nil {
		t.Fatal(err)
	}
	stopDaemon()
	journal, err := wipdjournal.Open(filepath.Join(profileRoot, "environment-journal"), identity)
	if err != nil {
		t.Fatal(err)
	}
	entries, err := journal.Entries()
	if err != nil || len(entries) != 5 || entries[3].EnvironmentSeq != declarationSequence || entries[4].EnvironmentSeq != declarationSequence+1 ||
		entries[4].SubmissionSchema != wipdwire.CommandSubmitV2Feature || !bytes.Equal(entries[4].DetachedProof, proof) {
		t.Fatalf("success journal: %+v %v", entries, err)
	}
	assertProcessRetainedProof(t, entries[4], proof)
	installed, err := journal.InstallSnapshot(ctx)
	if err != nil {
		t.Fatal(err)
	}
	receipt := installed.Receipts[repair.ID]
	if receipt.RequestHash != m5CommandHash(t, repair) || receipt.ResultCode != operation.ResultSucceeded || installed.Anchor.EventCount != boundary.EventCount+1 || installed.Anchor.EventID == nil || *installed.Anchor.EventID != eventID {
		t.Fatalf("installed effectful receipt: %+v anchor=%+v", receipt, installed.Anchor)
	}
	assertRepairReceipt(t, receipt.CanonicalReceipt, repair, false, eventID)
	if err = journal.Close(); err != nil {
		t.Fatal(err)
	}
	status, err := fixture.store.QueryCommand(ctx, m5TestDomain, repair.ID, m5CommandHash(t, repair), 1, fixture.peer, m5TestEnv, time.Now())
	if err != nil || status.Pending || !bytes.Equal(status.Receipt, receipt.CanonicalReceipt) || len(status.SignedReceipt) == 0 {
		t.Fatalf("retained public receipt: %+v %v", status, err)
	}
	if err = db.QueryRow(`SELECT state,position FROM claim_journal_entries WHERE domain_id=? AND command_id=?`, m5TestDomain, repair.ID).Scan(&state, &count); err != nil || state != "terminal" || count != 3 {
		t.Fatalf("effectful exact ACK: state=%s position=%d err=%v", state, count, err)
	}
	client, stopDaemon, output = startWipdForBirthReleaseRecovery(t, binary, profileRoot, true)
	defer func() { _ = client.Close(); stopDaemon() }()
	for _, replay := range [][]byte{nil, proof} {
		got, replayErr := client.ExecuteCommandV2(ctx, repair, replay)
		out, valid := got.Output.(operation.GateExemptionRepairOutput)
		if replayErr != nil || got.Code != operation.ResultSucceeded || !valid || out.AlreadyExempt {
			t.Fatalf("retained success proof omitted=%t: %+v %v daemon=%s", replay == nil, got, replayErr, output.String())
		}
	}
	changed := bytes.Clone(proof)
	changed[len(changed)-1] ^= 1
	if _, err = client.ExecuteCommandV2(ctx, repair, changed); err == nil || !strings.Contains(err.Error(), "command.id-conflict") {
		t.Fatalf("substituted proof: %v", err)
	}
	// A distinct owner-attested identity over the original declaration boundary
	// succeeds without an event now that the projection is already exempt.
	second := command(repairTransportID(70), declarationSequence+2, operation.Request{
		Operation: operation.GateExemptionRepairV1.Metadata().Operation, Actor: "human", Context: claimContext, Claim: claim,
		Input: repair.Request.Input,
	})
	secondProof := processRepairProof(t, fixture, second, 0x7b)
	secondResult, err := client.ExecuteCommandV2(ctx, second, secondProof)
	secondOutput, ok := secondResult.Output.(operation.GateExemptionRepairOutput)
	if err != nil || secondResult.Code != operation.ResultSucceeded || !ok || !secondOutput.AlreadyExempt || secondOutput.NodeID != matterID || secondOutput.Gate != "repair-target" {
		t.Fatalf("already-exempt repair: %+v %v daemon=%s trace=%v", secondResult, err, output.String(), trace.snapshot())
	}
	if !traceContains(trace.snapshot(), "/wipd/v1/exchange request=command.submit response=submission.accepted,command.terminal") {
		t.Fatalf("no-event second HTTP submission missing: %v", trace.snapshot())
	}
	secondStatus, err := fixture.store.QueryCommand(ctx, m5TestDomain, second.ID, m5CommandHash(t, second), 1, fixture.peer, m5TestEnv, time.Now())
	if err != nil || secondStatus.Pending || len(secondStatus.SignedReceipt) == 0 {
		t.Fatalf("no-event public receipt: %+v %v", secondStatus, err)
	}
	assertRepairReceipt(t, secondStatus.Receipt, second, true, "")
	if err = db.QueryRow(`SELECT count(*) FROM authority_events WHERE domain_id=? AND command_id IN (?,?)`, m5TestDomain, repair.ID, second.ID).Scan(&count); err != nil || count != 1 {
		t.Fatalf("repairs emitted %d events: %v", count, err)
	}
	if err = db.QueryRow(`SELECT state,position FROM claim_journal_entries WHERE domain_id=? AND command_id=?`, m5TestDomain, second.ID).Scan(&state, &count); err != nil || state != "terminal" || count != 4 {
		t.Fatalf("no-event exact ACK: state=%s position=%d err=%v", state, count, err)
	}
	last, err := fixture.store.CurrentPrefixAnchor(ctx, m5TestDomain)
	if err != nil || last.EventCount != boundary.EventCount+1 || last.EventID != eventID {
		t.Fatalf("no-event prefix: %+v %v", last, err)
	}
	if err = client.Close(); err != nil {
		t.Fatal(err)
	}
	stopDaemon()
	journal, err = wipdjournal.Open(filepath.Join(profileRoot, "environment-journal"), identity)
	if err != nil {
		t.Fatal(err)
	}
	entries, err = journal.Entries()
	if err != nil || len(entries) != 6 || entries[5].Command.ID != second.ID ||
		entries[5].EnvironmentSeq != second.EnvironmentSequence || entries[5].SubmissionSchema != wipdwire.CommandSubmitV2Feature ||
		!bytes.Equal(entries[5].DetachedProof, secondProof) {
		t.Fatalf("no-event retained proof and sequence: %+v %v", entries, err)
	}
	assertProcessRetainedProof(t, entries[5], secondProof)
	installed, err = journal.InstallSnapshot(ctx)
	if err != nil {
		t.Fatal(err)
	}
	noEventReceipt := installed.Receipts[second.ID]
	if installed.Anchor.EventCount != last.EventCount || installed.Anchor.Digest != last.Digest ||
		noEventReceipt.ResultCode != operation.ResultSucceeded || !bytes.Equal(noEventReceipt.CanonicalReceipt, secondStatus.Receipt) {
		t.Fatalf("installed no-event receipt/prefix: %+v anchor=%+v", noEventReceipt, installed.Anchor)
	}
	assertRepairReceipt(t, noEventReceipt.CanonicalReceipt, second, true, "")
	if err = journal.Close(); err != nil {
		t.Fatal(err)
	}
	client, stopDaemon, output = startWipdForBirthReleaseRecovery(t, binary, profileRoot, true)
	gotSecond, replayErr := client.ExecuteCommandV2(ctx, second, nil)
	outSecond, valid := gotSecond.Output.(operation.GateExemptionRepairOutput)
	if replayErr != nil || gotSecond.Code != operation.ResultSucceeded || !valid || !outSecond.AlreadyExempt {
		t.Fatalf("restarted no-event replay: %+v %v daemon=%s", gotSecond, replayErr, output.String())
	}
}

func assertRepairReceipt(t *testing.T, raw []byte, command operation.Command, already bool, eventID string) {
	t.Helper()
	fields, err := wipdwire.DecodeCanonicalMap(raw, "schema", "domain_id", "authority_epoch", "identity_schema", "command_id", "request_hash", "operation", "environment", "result", "accepted_events")
	if err != nil {
		t.Fatal(err)
	}
	result, ok := fields["result"].(map[string]any)
	if !ok || fields["command_id"] != command.ID || fields["request_hash"] != m5CommandHash(t, command) || result["code"] != "result.succeeded" || result["problem_code"] != nil {
		t.Fatalf("repair receipt: %#v", fields)
	}
	output, ok := result["output"].([]byte)
	if !ok {
		t.Fatalf("receipt output: %#v", result)
	}
	decoded, err := wipdwire.DecodeCanonicalMap(output, "gate", "node_id", "already_exempt")
	if err != nil || decoded["already_exempt"] != already || decoded["gate"] != "repair-target" || decoded["node_id"] != command.Request.Input.(operation.GateExemptionRepairInput).NodeID {
		t.Fatalf("receipt output: %#v %v", decoded, err)
	}
	if already && fields["accepted_events"] != nil {
		t.Fatalf("no-event receipt accepted events: %#v", fields["accepted_events"])
	}
	if !already {
		accepted, ok := fields["accepted_events"].(map[string]any)
		if !ok || accepted["first_event_id"] != eventID || accepted["last_event_id"] != eventID || accepted["event_count"] != uint64(1) {
			t.Fatalf("effectful accepted events: %#v", fields["accepted_events"])
		}
	}
}

func assertProcessRetainedProof(t *testing.T, entry wipdjournal.Entry, original []byte) {
	t.Helper()
	fields, err := wipdwire.DecodeCanonicalMap(entry.DetachedProof, "schema", "kind", "domain_id", "authority_epoch",
		"signer_role", "signer_key_id", "key_generation", "artifact_sequence", "previous_artifact_digest",
		"issued_at", "payload_schema", "payload_digest", "payload", "signature")
	proofPayload, payloadOK := fields["payload"].([]byte)
	if err != nil || !bytes.Equal(entry.DetachedProof, original) || fields["schema"] != "wipd.signed-artifact/1" ||
		fields["payload_schema"] != "wipd.owner-attestation/1" || !payloadOK ||
		fields["payload_digest"] != repairTransportHashBytes(proofPayload) {
		t.Fatalf("retained signed proof schema/digest: %#v %v", fields, err)
	}
	payload, err := wipdwire.DecodeCanonicalMap(proofPayload, "schema", "action", "domain_id", "current_epoch",
		"next_epoch", "subject_schema", "subject_digest", "subject", "nonce", "issued_at", "expires_at", "loss_accepted")
	subject, subjectOK := payload["subject"].([]byte)
	issued, issuedOK := payload["issued_at"].(string)
	expires, expiresOK := payload["expires_at"].(string)
	issuedAt, issuedErr := time.Parse(time.RFC3339Nano, issued)
	expiresAt, expiresErr := time.Parse(time.RFC3339Nano, expires)
	if err != nil || payload["schema"] != "wipd.owner-attestation/1" ||
		!subjectOK || payload["subject_digest"] != repairTransportHashBytes(subject) || !issuedOK || !expiresOK ||
		fields["issued_at"] != issued || issuedErr != nil || expiresErr != nil || !expiresAt.After(issuedAt) {
		t.Fatalf("retained attestation hash/time: %#v %v", payload, err)
	}
}

// Allow authority completion, but lose exactly the first repair's terminal
// frame after its accepted frame has reached the daemon. Receipt query must
// recover the committed terminal instead of re-executing the repair.
type processRepairLossWriter struct {
	http.ResponseWriter
	id       string
	used     *atomic.Bool
	suppress bool
}

func (w *processRepairLossWriter) Write(p []byte) (int, error) {
	frames, err := wipdwire.ReadFrames(p, 2)
	if err == nil && len(frames) == 1 {
		if frames[0].Kind == "submission.accepted" {
			var accepted wipdwire.SubmissionAccepted
			if wipdwire.DecodeCanonical(frames[0].Payload, &accepted,
				"schema", "domain_id", "authority_epoch", "command_id", "request_hash") == nil &&
				accepted.CommandID == w.id && w.used.CompareAndSwap(false, true) {
				w.suppress = true
			}
		} else if w.suppress && frames[0].Kind == "command.terminal" {
			return len(p), nil
		}
	}
	return w.ResponseWriter.Write(p)
}

func (w *processRepairLossWriter) Flush() {
	if flusher, ok := w.ResponseWriter.(http.Flusher); ok {
		flusher.Flush()
	}
}

func dropFirstProcessRepairTerminal(next http.Handler, commandID string) http.Handler {
	var used atomic.Bool
	return http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		next.ServeHTTP(&processRepairLossWriter{ResponseWriter: writer, id: commandID, used: &used}, request)
	})
}

// Lose only the second journal entry's first ACK, either before committing it
// or after committing it but before delivering its response. A restarted
// Environment must retry the exact installed refusal receipt in both cases.
func dropFirstProcessRepairAck(next http.Handler, mode string) http.Handler {
	var used atomic.Bool
	return http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if request.URL.Path == "/wipd/v1/exchange" {
			var consumed bytes.Buffer
			frame, err := wipdwire.ReadFrame(io.TeeReader(request.Body, &consumed))
			request.Body = io.NopCloser(io.MultiReader(&consumed, request.Body))
			if err == nil && frame.Kind == "claim-journal.ack" {
				var ack wipdwire.ClaimJournalReceiptAck
				if wipdwire.DecodeCanonical(frame.Payload, &ack, "schema", "domain_id", "authority_epoch",
					"environment_id", "claim_id", "claim_epoch", "matter_id", "dispatch_id", "journal_id",
					"generation", "position", "terminal_receipt", "installed_prefix") == nil && ack.Position == 2 && used.CompareAndSwap(false, true) {
					if mode == "before" {
						writeLabProblem(writer, frame.RequestID, "authority.unavailable")
					} else {
						next.ServeHTTP(&processRepairAckLossWriter{ResponseWriter: writer}, request)
					}
					return
				}
			}
		}
		next.ServeHTTP(writer, request)
	})
}

type processRepairAckLossWriter struct{ http.ResponseWriter }

func (w *processRepairAckLossWriter) Write(p []byte) (int, error) { return len(p), nil }

func (w *processRepairAckLossWriter) Flush() {}
