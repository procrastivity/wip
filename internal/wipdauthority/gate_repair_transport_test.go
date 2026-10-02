package wipdauthority

import (
	"bytes"
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"errors"
	"net/http"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/procrastivity/wip/internal/authoritystore"
	"github.com/procrastivity/wip/internal/operation"
	"github.com/procrastivity/wip/internal/wipdwire"
	_ "modernc.org/sqlite"
)

func repairTransportID(n int) string { return claimTestIDForM6(n) }

func repairTransportCommand(t *testing.T, f *m5CommandFixture) (operation.Command, []byte) {
	t.Helper()
	ctx := context.Background()
	created := m5Command(repairTransportID(410), 1, "repair-transport")
	status, err := f.store.SubmitCommand(ctx, created, m5CommandHash(t, created), f.peer, f.now)
	if err != nil || status.Owner == nil {
		t.Fatalf("create matter admission: %+v %v", status, err)
	}
	_, err = f.store.CompleteCommand(ctx, status.Owner, operation.Result{
		Code:   operation.ResultSucceeded,
		Output: operation.MatterCreateOutput{ID: m5TestMatter, Locator: "repair-transport", Title: "A title"},
	},
		m5TestMatter, repairTransportID(510), f.now, f.config.SignArtifact)
	if err != nil {
		t.Fatalf("create matter: %v", err)
	}
	anchor, err := f.store.CurrentPrefixAnchor(ctx, m5TestDomain)
	if err != nil {
		t.Fatal(err)
	}
	clone, worktree, claim := repairTransportID(411), repairTransportID(412), repairTransportID(413)
	acquireID := repairTransportID(414)
	raw, err := wipdwire.EncodeCanonical(map[string]any{
		"schema": "wipd.command/1", "command_id": acquireID,
		"authority":   map[string]any{"domain_id": m5TestDomain, "expected_epoch": uint64(1)},
		"environment": map[string]any{"id": m5TestEnv, "sequence": uint64(2)},
		"acted_at":    f.now.Format(time.RFC3339Nano), "actor": "human",
		"causation_command_id": nil, "correlation_command_id": acquireID,
		"operation": map[string]any{"name": "claim.acquire", "version": uint64(1)},
		"context":   map[string]any{"repo_id": m5TestRepo, "clone_id": clone, "worktree_id": worktree},
		"claim":     nil, "input": map[string]any{
			"matter_id": m5TestMatter, "worktree_id": worktree,
			"dispatch_mode": "anonymous-matter", "requested_dispatch_id": repairTransportID(415),
		}, "blobs": []any{},
	})
	if err != nil {
		t.Fatal(err)
	}
	acquireHash := repairTransportHash(raw)
	status, err = f.store.SubmitClaimAcquire(ctx, raw, acquireHash, anchor, f.peer, f.now)
	if err != nil || status.Owner == nil {
		t.Fatalf("acquire claim admission: %+v %v", status, err)
	}
	_, _, err = f.store.CompleteClaimAcquire(ctx, status.Owner, authoritystore.AcquireAllocation{
		ClaimID: claim, BatchID: repairTransportID(416), GrantID: repairTransportID(417),
		SnapshotID: repairTransportID(418), JournalID: repairTransportID(419), Installed: anchor,
		EventIDs: []string{repairTransportID(511), repairTransportID(512), repairTransportID(513)},
	}, f.now, f.config.SignArtifact)
	if err != nil {
		t.Fatalf("acquire claim: %v", err)
	}
	declare := operation.Command{
		ID: repairTransportID(420), AuthorityDomainID: m5TestDomain, ExpectedAuthorityEpoch: 1,
		EnvironmentID: m5TestEnv, EnvironmentSequence: 3, ActedAt: f.now.Format(time.RFC3339Nano),
		CorrelationCommandID: repairTransportID(420), Request: operation.Request{
			Operation: operation.GateDeclareV1.Metadata().Operation, Actor: "human",
			Context: operation.Context{Repo: m5TestRepo, Clone: clone, Worktree: worktree},
			Claim:   &operation.ClaimContext{ID: claim, Epoch: "1"},
			Input:   operation.GateDeclareInput{Gate: "repair-target", Scale: "matter"},
		},
	}
	status, err = f.store.SubmitCommand(ctx, declare, m5CommandHash(t, declare), f.peer, f.now)
	if err != nil || status.Owner == nil {
		t.Fatalf("declare gate admission: %+v %v", status, err)
	}
	status, err = f.store.CompleteCommand(ctx, status.Owner, operation.Result{Code: operation.ResultSucceeded},
		m5TestRepo, repairTransportID(514), f.now, f.config.SignArtifact)
	if err != nil {
		t.Fatalf("declare gate: %v", err)
	}
	anchor, err = f.store.CurrentPrefixAnchor(ctx, m5TestDomain)
	if err != nil {
		t.Fatal(err)
	}
	if err = f.store.AcknowledgeClaimJournalEntry(ctx, m5TestDomain, repairTransportID(419), 1, status.Receipt, anchor); err != nil {
		t.Fatalf("acknowledge declaration journal entry: %v", err)
	}
	command := operation.Command{
		ID: repairTransportID(421), AuthorityDomainID: m5TestDomain, ExpectedAuthorityEpoch: 1,
		EnvironmentID: m5TestEnv, EnvironmentSequence: 4, ActedAt: f.now.Format(time.RFC3339Nano),
		CorrelationCommandID: repairTransportID(421), Request: operation.Request{
			Operation: operation.GateExemptionRepairV1.Metadata().Operation, Actor: "human",
			Context: operation.Context{Repo: m5TestRepo, Clone: clone, Worktree: worktree},
			Claim:   &operation.ClaimContext{ID: claim, Epoch: "1"},
			Input: operation.GateExemptionRepairInput{
				NodeID: m5TestMatter, Gate: "repair-target", EventCount: anchor.EventCount,
				HighWaterEventID: anchor.EventID, PrefixDigest: anchor.Digest,
				IncidentRef: "urn:example:repair-transport", Reason: "Owner-approved omitted prospective exemption",
				EvidenceRefs: []string{"sha256:" + strings.Repeat("a", 64)},
			},
		},
	}
	hash := m5CommandHash(t, command)
	input := command.Request.Input.(operation.GateExemptionRepairInput)
	subject, err := wipdwire.EncodeCanonical(map[string]any{
		"schema": "wipd.gate-exemption-repair-subject/1", "command_id": command.ID,
		"request_hash": hash, "repo_id": m5TestRepo, "node_id": input.NodeID, "gate": input.Gate,
		"event_count": input.EventCount, "high_water_event_id": input.HighWaterEventID,
		"prefix_digest": input.PrefixDigest, "incident_ref": input.IncidentRef,
		"reason_digest": repairTransportHashBytes([]byte(input.Reason)), "evidence_refs": input.EvidenceRefs,
	})
	if err != nil {
		t.Fatal(err)
	}
	issued := time.Now().UTC().Add(-time.Minute)
	proof := m5TestOwnerArtifact(t, m5TestKey("owner-root"), m5TestPublicDigest(f.ownerRoot), m5TestDomain,
		"owner-attestation", "wipd.owner-attestation/1", map[string]any{
			"schema": "wipd.owner-attestation/1", "action": "gate-exemption-repair",
			"domain_id": m5TestDomain, "current_epoch": uint64(1), "next_epoch": nil,
			"subject_schema": "wipd.gate-exemption-repair-subject/1", "subject_digest": repairTransportHashBytes(subject),
			"subject": subject, "nonce": bytes.Repeat([]byte{0x79}, 16),
			"issued_at": issued.Format(time.RFC3339Nano), "expires_at": issued.Add(2 * time.Minute).Format(time.RFC3339Nano),
			"loss_accepted": false,
		}, issued)
	return command, proof
}

func repairTransportHashBytes(raw []byte) string {
	sum := sha256.Sum256(raw)
	return "sha256:" + hex.EncodeToString(sum[:])
}

func repairTransportHash(raw []byte) string {
	return repairTransportHashBytes(append([]byte("wipd/request-hash/v1\x00"), raw...))
}

func repairTransportSession(t *testing.T, f *m5CommandFixture, v2 bool) *labConnectionSession {
	t.Helper()
	features := []any{"wipd.frame/1"}
	if v2 {
		features = []any{wipdwire.CommandSubmitV2Feature, "wipd.frame/1"}
	}
	hello, err := wipdwire.EncodeCanonical(map[string]any{
		"protocol_min": []any{uint64(1), uint64(0)}, "protocol_max": []any{uint64(1), uint64(0)},
		"identity_schemas": []any{"wipd.command/1"}, "store_schemas": []any{"wipd.store/1"},
		"operations": []any{map[string]any{"name": "gate.exemption.repair", "versions": []any{uint64(1)}, "identity_schemas": []any{"wipd.command/1"}}},
		"features":   features,
	})
	if err != nil {
		t.Fatal(err)
	}
	session := &labConnectionSession{}
	recorder, request := m5Request(t, session, f.peer, "client.hello", hello, http.MethodPost, labNegotiatePath)
	f.handler.ServeHTTP(recorder, request)
	frames := m5ResponseFrames(t, recorder, 2)
	if len(frames) != 2 || !session.negotiated || session.commandSubmitV2 != v2 {
		t.Fatalf("repair negotiation: %+v session=%+v", frames, session)
	}
	_, selected := session.operations[operation.GateExemptionRepairV1.Metadata().Operation]
	if selected != v2 {
		t.Fatalf("repair capability selected=%t, v2=%t", selected, v2)
	}
	return session
}

func TestRepairM6TransportV1DenialAndV2FactualRefusalReplay(t *testing.T) {
	f := newM5CommandFixture(t)
	command, proof := repairTransportCommand(t, f)
	registry, err := NewM6Step5Registry()
	if err != nil {
		t.Fatal(err)
	}
	for _, definition := range registry.Definitions() {
		if definition.Metadata().Operation == operation.GateExemptionRepairV1.Metadata().Operation {
			t.Fatal("repair unexpectedly has a generic Registry.Dispatch handler")
		}
	}
	config := f.config
	config.Registry = registry
	f.server, err = NewM6LabServer(f.profile, f.serverCert, config)
	if err != nil {
		t.Fatal(err)
	}
	f.handler = f.server.http.Handler
	raw, err := command.CanonicalBytes()
	if err != nil {
		t.Fatal(err)
	}
	hash := m5CommandHash(t, command)
	v1 := repairTransportSession(t, f, false)
	assertSubmitV2Problem(t, m5Exchange(t, f, v1, "command.submit", wipdwire.CommandSubmit{
		Schema: "wipd.command-submit/1", CanonicalCommand: raw, RequestHash: hash,
	}), "protocol.unsupported-extension")
	assertSubmitV2Problem(t, m5Exchange(t, f, v1, "command.submit", wipdwire.CommandSubmitV2{
		Schema: wipdwire.CommandSubmitV2Feature, CanonicalCommand: raw, RequestHash: hash, DetachedProof: proof,
	}), "protocol.unsupported-extension")
	if _, err = f.store.QueryCommand(context.Background(), m5TestDomain, command.ID, hash, 1, f.peer, m5TestEnv, time.Now()); !errors.Is(err, authoritystore.ErrNotFound) {
		t.Fatalf("v1 denial reached submission: %v", err)
	}
	db, err := sql.Open("sqlite", filepath.Join(f.root, "authority.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = db.Close() }()
	for _, table := range []string{"gate_exemption_repair_admissions", "gate_exemption_repair_nonces", "claim_journal_entries"} {
		var count int
		if err = db.QueryRow("SELECT count(*) FROM "+table+" WHERE domain_id=? AND command_id=?", m5TestDomain, command.ID).Scan(&count); err != nil || count != 0 {
			t.Fatalf("v1 denial persisted %s count=%d err=%v", table, count, err)
		}
	}
	// The exact nonce/sequence remains available for the subsequent valid v2
	// admission; neither rejection can silently reserve them.
	v2 := repairTransportSession(t, f, true)
	submit := wipdwire.CommandSubmitV2{
		Schema:           wipdwire.CommandSubmitV2Feature,
		CanonicalCommand: raw, RequestHash: hash, DetachedProof: proof,
	}
	frames := m5Exchange(t, f, v2, "command.submit", submit)
	if len(frames) != 2 || frames[0].Kind != "submission.accepted" || frames[1].Kind != "command.terminal" {
		t.Fatalf("v2 repair accepted/terminal frames: %+v", frames)
	}
	receipt := bytes.Clone(frames[1].Payload)
	fields, err := wipdwire.DecodeCanonicalMap(receipt, "schema", "domain_id", "authority_epoch", "identity_schema", "command_id", "request_hash", "operation", "environment", "result", "accepted_events")
	if err != nil {
		t.Fatal(err)
	}
	result, ok := fields["result"].(map[string]any)
	op, opOK := fields["operation"].(map[string]any)
	if !ok || !opOK || fields["schema"] != "wipd.terminal-receipt/1" ||
		fields["command_id"] != command.ID || fields["request_hash"] != hash ||
		op["name"] != "gate.exemption.repair" || op["version"] != uint64(1) ||
		result["code"] != "result.refused" || result["problem_code"] != "refusal.gate-repair-lifecycle" ||
		result["output"] != nil || fields["accepted_events"] != nil {
		t.Fatalf("declaration-before-Done factual refusal receipt: %#v", fields)
	}
	before, err := f.store.CurrentPrefixAnchor(context.Background(), m5TestDomain)
	if err != nil || before.EventCount != 5 || before.EventID != repairTransportID(514) {
		t.Fatalf("unexpected independent five-event fixture prefix: %+v %v", before, err)
	}
	for table, want := range map[string]int{
		"gate_exemption_repair_admissions": 1, "gate_exemption_repair_nonces": 1,
		"claim_journal_entries": 1, "gate_exemption_repair_terminals": 1,
		"terminal_receipts": 1, "authority_events": 0,
	} {
		var count int
		if err = db.QueryRow("SELECT count(*) FROM "+table+" WHERE domain_id=? AND command_id=?", m5TestDomain, command.ID).Scan(&count); err != nil || count != want {
			t.Fatalf("repair refusal %s count=%d want=%d err=%v", table, count, want, err)
		}
	}
	if queried := m5Query(t, v2, f, command.ID, hash); len(queried) != 1 || queried[0].Kind != "command.terminal" || !bytes.Equal(queried[0].Payload, receipt) {
		t.Fatalf("receipt.query mismatch: %+v", queried)
	}
	if err = f.store.Close(); err != nil {
		t.Fatal(err)
	}
	f.store, err = authoritystore.OpenExisting(f.root)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = f.store.Close() })
	config.Store = f.store
	f.server, err = NewM6LabServer(f.profile, f.serverCert, config)
	if err != nil {
		t.Fatal(err)
	}
	f.handler = f.server.http.Handler
	v2 = repairTransportSession(t, f, true)
	status, err := f.store.QueryCommand(context.Background(), m5TestDomain, command.ID, hash, 1, f.peer, m5TestEnv, time.Now())
	if err != nil || status.Pending || !bytes.Equal(status.Receipt, receipt) || len(status.SignedReceipt) == 0 {
		t.Fatalf("public QueryCommand restart status: %+v %v", status, err)
	}
	if queried := m5Query(t, v2, f, command.ID, hash); len(queried) != 1 || queried[0].Kind != "command.terminal" || !bytes.Equal(queried[0].Payload, receipt) {
		t.Fatalf("restarted receipt.query: %+v", queried)
	}
	for _, replay := range []struct {
		name  string
		proof []byte
	}{{"omitted", nil}, {"equal", proof}} {
		t.Run(replay.name, func(t *testing.T) {
			payload := submit
			payload.DetachedProof = replay.proof
			got := m5Exchange(t, f, v2, "command.submit", payload)
			if len(got) != 1 || got[0].Kind != "command.terminal" || !bytes.Equal(got[0].Payload, receipt) {
				t.Fatalf("exact replay: %+v", got)
			}
		})
	}
	changed := submit
	changed.DetachedProof = bytes.Clone(proof)
	changed.DetachedProof[len(changed.DetachedProof)-1] ^= 1
	conflict := m5Exchange(t, f, v2, "command.submit", changed)
	assertSubmitV2Problem(t, conflict, "command.id-conflict")
	after, err := f.store.CurrentPrefixAnchor(context.Background(), m5TestDomain)
	if err != nil || after != before {
		t.Fatalf("refusal/replay changed event prefix: before=%+v after=%+v err=%v", before, after, err)
	}
}
