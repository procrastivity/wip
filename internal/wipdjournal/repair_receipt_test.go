package wipdjournal

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"testing"

	"github.com/procrastivity/wip/internal/operation"
	"github.com/procrastivity/wip/internal/wipdwire"
)

func TestRepairV1PrepareDeniedBeforeJournalWrite(t *testing.T) {
	j, err := Open(filepath.Join(privateTempDir(t), "journal"), testIdentity)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = j.Close() }()
	input := CommandInput{ID: testCommandPrefix + "71", Request: operation.Request{
		Operation: operation.GateExemptionRepairV1.Metadata().Operation,
	}}
	if _, err = j.PrepareCommand(input); !errors.Is(err, ErrInvalidCommand) {
		t.Fatalf("v1 prepare: %v", err)
	}
	command := operation.Command{Request: input.Request}
	if _, err = j.PrepareCanonicalSubmission(command, "wipd.command-submit/1", nil); !errors.Is(err, ErrInvalidCommand) {
		t.Fatalf("v1 canonical prepare: %v", err)
	}
	entries, err := j.Entries()
	if err != nil || len(entries) != 0 {
		t.Fatalf("v1 denial wrote entries: %v, %v", entries, err)
	}
}

func TestRepairV2RetainedProofRetryCannotDowngrade(t *testing.T) {
	root := filepath.Join(privateTempDir(t), "journal")
	j, err := Open(root, testIdentity)
	if err != nil {
		t.Fatal(err)
	}
	id := testCommandPrefix + "74"
	command := operation.Command{
		ID: id, AuthorityDomainID: testDomainID, ExpectedAuthorityEpoch: 7, EnvironmentID: testEnvironmentID,
		EnvironmentSequence: 1, ActedAt: "2026-10-02T00:00:00Z", CorrelationCommandID: id,
		Request: operation.Request{
			Operation: operation.GateExemptionRepairV1.Metadata().Operation, Actor: "human",
			Context: operation.Context{Repo: testRepoID, Clone: id, Worktree: id},
			Claim:   &operation.ClaimContext{ID: id, Epoch: "1"},
			Input: operation.GateExemptionRepairInput{
				NodeID: id, Gate: "reviewed", EventCount: 1,
				HighWaterEventID: id, PrefixDigest: fmt.Sprintf("sha256:%064x", 0),
				IncidentRef: "urn:example:incident", Reason: "Owner-approved repair",
				EvidenceRefs: []string{fmt.Sprintf("sha256:%064x", 0)},
			},
		},
	}
	proof := []byte{0x01, 0x02, 0x03}
	first, err := j.PrepareCanonicalSubmission(command, wipdwire.CommandSubmitV2Feature, proof)
	if err != nil || first.JournalPosition != 0 || first.State != StateAttemptPrepared {
		t.Fatalf("repair v2 prepare: %+v %v", first, err)
	}
	if err = j.Close(); err != nil {
		t.Fatal(err)
	}
	j, err = Open(root, testIdentity)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = j.Close() }()
	for _, supplied := range [][]byte{nil, proof} {
		retry, err := j.PrepareCanonicalSubmission(command, wipdwire.CommandSubmitV2Feature, supplied)
		if err != nil || retry.Created || !bytes.Equal(retry.DetachedProof, proof) || retry.RequestHash != first.RequestHash {
			t.Fatalf("repair retry proof=%v: %+v %v", supplied, retry, err)
		}
		payload, err := retry.SubmissionPayload()
		if err != nil {
			t.Fatal(err)
		}
		encoded, _ := wipdwire.EncodeCanonical(payload)
		decoded, v2, err := wipdwire.DecodeCommandSubmit(encoded)
		if err != nil || !v2 || !bytes.Equal(decoded.DetachedProof, proof) {
			t.Fatalf("repair retry transport: %+v %t %v", decoded, v2, err)
		}
	}
	if _, err = j.PrepareCanonicalSubmission(command, wipdwire.CommandSubmitV2Feature, []byte{0x04}); !errors.Is(err, ErrCommandIDConflict) {
		t.Fatalf("substituted proof: %v", err)
	}
	if _, err = j.PrepareCanonicalSubmission(command, "wipd.command-submit/1", nil); !errors.Is(err, ErrInvalidCommand) {
		t.Fatalf("v1 downgrade: %v", err)
	}
	entries, err := j.Entries()
	if err != nil || len(entries) != 1 {
		t.Fatalf("repair retries changed durable count: %v %v", entries, err)
	}
	output, _ := wipdwire.EncodeCanonical(map[string]any{"gate": "reviewed", "node_id": id, "already_exempt": true})
	receipt, _ := wipdwire.EncodeCanonical(map[string]any{
		"schema": "wipd.terminal-receipt/1", "domain_id": testDomainID, "authority_epoch": uint64(7),
		"identity_schema": "wipd.command/1", "command_id": id, "request_hash": first.RequestHash,
		"operation":       map[string]any{"name": "gate.exemption.repair", "version": uint64(1)},
		"environment":     map[string]any{"id": testEnvironmentID, "sequence": uint64(1)},
		"result":          map[string]any{"code": string(operation.ResultSucceeded), "output": output, "problem_code": nil},
		"accepted_events": nil,
	})
	installed, err := j.InstallSnapshot(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	installed, err = j.InstallAuthorityOutcome(context.Background(), installed.Expectation(), first,
		operation.ResultSucceeded, receipt, emptyInstallTestTransfer(t, installed.Anchor))
	if err != nil || installed.Anchor.EventCount != 0 || len(installed.Receipts) != 1 {
		t.Fatalf("no-event repair install: %+v %v", installed, err)
	}
	if err = j.Close(); err != nil {
		t.Fatal(err)
	}
	j, err = Open(root, testIdentity)
	if err != nil {
		t.Fatalf("no-event repair reopen: %v", err)
	}
	defer func() { _ = j.Close() }()
	if snapshot, snapshotErr := j.InstallSnapshot(context.Background()); snapshotErr != nil || snapshot.Anchor.EventCount != 0 || len(snapshot.Receipts) != 1 {
		t.Fatalf("no-event repair persisted state: %+v %v", snapshot, snapshotErr)
	}
}

func TestRepairReceiptNoEventIsExactAndExclusive(t *testing.T) {
	entry := Entry{Command: operation.Command{Request: operation.Request{
		Operation: operation.GateExemptionRepairV1.Metadata().Operation,
		Input:     operation.GateExemptionRepairInput{NodeID: testCommandPrefix + "72", Gate: "reviewed"},
	}}}
	for _, test := range []struct {
		name, gate string
		already    bool
		allowed    bool
	}{
		{"no-op", "reviewed", true, true},
		{"effectful", "reviewed", false, false},
		{"substituted-gate", "other", true, false},
	} {
		t.Run(test.name, func(t *testing.T) {
			output, _ := wipdwire.EncodeCanonical(map[string]any{"gate": test.gate, "node_id": testCommandPrefix + "72", "already_exempt": test.already})
			receipt, _ := wipdwire.EncodeCanonical(map[string]any{
				"schema": "wipd.terminal-receipt/1", "domain_id": testDomainID, "authority_epoch": uint64(7),
				"identity_schema": "wipd.command/1", "command_id": testCommandPrefix + "73", "request_hash": "sha256:" + testDomainID,
				"operation":   map[string]any{"name": "gate.exemption.repair", "version": uint64(1)},
				"environment": map[string]any{"id": testEnvironmentID, "sequence": uint64(1)},
				"result":      map[string]any{"code": string(operation.ResultSucceeded), "output": output, "problem_code": nil}, "accepted_events": nil,
			})
			if got := repairReceiptNoEvent(entry, receipt); got != test.allowed {
				t.Fatalf("no-event accepted = %t, want %t", got, test.allowed)
			}
			anchor := emptyTransferAnchor()
			transfer := emptyInstallTestTransfer(t, anchor)
			_, _, _, err := receiptEventRange(context.Background(), nil, anchor, transfer, entry, operation.ResultSucceeded, receipt)
			if (err == nil) != test.allowed {
				t.Fatalf("no-event installation = %v, allowed=%t", err, test.allowed)
			}
		})
	}
}

func TestGateHistoryTransferShapesStayClosed(t *testing.T) {
	for _, test := range []struct {
		name, kind string
		payload    map[string]any
		allowed    bool
	}{
		{"repair", "gate.exemption-repaired", map[string]any{"gate": "reviewed"}, true},
		{"repair-empty-gate", "gate.exemption-repaired", map[string]any{"gate": ""}, false},
		{"repair-proof-leak", "gate.exemption-repaired", map[string]any{"gate": "reviewed", "proof": []byte{1}}, false},
		{"declaration", "gate.declared", map[string]any{"gate": "reviewed", "scale": "matter"}, true},
		{"prospective", "gate.declared", map[string]any{"gate": "reviewed", "scale": "matter", "exempt": []any{testCommandPrefix + "72"}}, true},
		{"duplicate-exemption", "gate.declared", map[string]any{"gate": "reviewed", "scale": "matter", "exempt": []any{testCommandPrefix + "72", testCommandPrefix + "72"}}, false},
		{"bad-scale", "gate.declared", map[string]any{"gate": "reviewed", "scale": "unknown"}, false},
		{"closed", "gate.closed", map[string]any{"gate": "reviewed", "scale": "matter"}, true},
		{"dismissed", "gate.dismissed", map[string]any{"gate": "reviewed", "scale": "matter", "reason": "obsolete"}, true},
		{"dismiss-empty-reason", "gate.dismissed", map[string]any{"gate": "reviewed", "scale": "matter", "reason": " "}, false},
		{"tracker-off", "gate.closed", map[string]any{"gate": "reviewed", "scale": "matter", "tracker_push_level": "off"}, true},
		{"tracker-boundary", "gate.closed", map[string]any{"gate": "reviewed", "scale": "matter", "tracker_push_level": "boundary"}, true},
		{"tracker-invalid", "gate.closed", map[string]any{"gate": "reviewed", "scale": "matter", "tracker_push_level": "unknown"}, false},
	} {
		t.Run(test.name, func(t *testing.T) {
			id := testCommandPrefix + "73"
			subject := testCommandPrefix + "72"
			if test.kind == "gate.declared" {
				subject = testRepoID
			}
			record, err := wipdwire.EncodeCanonical(map[string]any{
				"schema": "wipd.event/1", "event_id": id, "domain_id": testDomainID,
				"command_id": testCommandPrefix + "74", "request_hash": hydrationDigest([]byte("gate history")),
				"environment": map[string]any{"id": testEnvironmentID, "sequence": uint64(1)},
				"acted_at":    "2026-10-02T00:00:00Z", "occurred_at": "2026-10-02T00:00:00Z",
				"kind": test.kind, "subject_id": subject, "repo_id": testRepoID, "payload": test.payload,
			})
			if err != nil {
				t.Fatal(err)
			}
			if got := validAuthorityEvent(record, testDomainID, id); got != test.allowed {
				t.Fatalf("gate history accepted=%t want=%t", got, test.allowed)
			}
		})
	}
}
