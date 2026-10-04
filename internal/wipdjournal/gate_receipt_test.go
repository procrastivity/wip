package wipdjournal

import (
	"context"
	"testing"

	"github.com/procrastivity/wip/internal/operation"
	"github.com/procrastivity/wip/internal/wipdwire"
)

func TestGateDeclarationNoEventReceiptIsExactAndExclusive(t *testing.T) {
	id := testCommandPrefix + "73"
	entry := Entry{Command: operation.Command{
		ID: id, AuthorityDomainID: testDomainID, ExpectedAuthorityEpoch: 7, EnvironmentID: testEnvironmentID,
		Request: operation.Request{
			Operation: operation.GateDeclareV1.Metadata().Operation, Actor: "human",
			Context: operation.Context{Repo: testRepoID, Clone: id, Worktree: id}, Claim: &operation.ClaimContext{ID: id, Epoch: "1"},
			Input: operation.GateDeclareInput{Gate: "local-check", Scale: "stage"},
		},
	}, RequestHash: hydrationDigest([]byte("declaration")), EnvironmentSeq: 9}
	for _, mutation := range []string{"valid", "wrong-gate", "wrong-scale", "extra-output", "output-type", "non-success", "problem", "event-range", "close-operation"} {
		t.Run(mutation, func(t *testing.T) {
			candidate := entry
			output := map[string]any{"gate": "local-check", "scale": "stage"}
			switch mutation {
			case "wrong-gate":
				output["gate"] = "substituted"
			case "wrong-scale":
				output["scale"] = "matter"
			case "extra-output":
				output["node_id"] = id
			case "output-type":
				output["scale"] = uint64(1)
			case "close-operation":
				candidate.Command.Request.Operation = operation.GateCloseV1.Metadata().Operation
			}
			encodedOutput, _ := wipdwire.EncodeCanonical(output)
			result := map[string]any{"code": "result.succeeded", "output": encodedOutput, "problem_code": nil}
			fields := map[string]any{
				"schema": "wipd.terminal-receipt/1", "domain_id": testDomainID, "authority_epoch": uint64(7), "identity_schema": "wipd.command/1",
				"command_id": id, "request_hash": entry.RequestHash,
				"operation":   map[string]any{"name": candidate.Command.Request.Operation.Name, "version": uint64(1)},
				"environment": map[string]any{"id": testEnvironmentID, "sequence": uint64(9)}, "result": result, "accepted_events": nil,
			}
			switch mutation {
			case "non-success":
				result["code"] = "result.refused"
			case "problem":
				result["problem_code"] = "refusal.gate-owner"
			case "event-range":
				fields["accepted_events"] = map[string]any{"first_event_id": id, "last_event_id": id, "event_count": uint64(1)}
			}
			raw, _ := wipdwire.EncodeCanonical(fields)
			allowed := mutation == "valid"
			if got := gateDeclarationReceiptNoEvent(candidate, raw); got != allowed {
				t.Fatalf("no-event permission=%t; want=%t", got, allowed)
			}
			if err := validateTerminalReceipt(candidate, operation.ResultSucceeded, raw, nil); (err == nil) != allowed {
				t.Fatalf("terminal receipt=%v; allowed=%t", err, allowed)
			}
			if mutation != "event-range" {
				anchor := emptyTransferAnchor()
				_, _, _, err := receiptEventRange(context.Background(), nil, anchor, emptyInstallTestTransfer(t, anchor), candidate, operation.ResultSucceeded, raw)
				if (err == nil) != allowed {
					t.Fatalf("receipt event range=%v; allowed=%t", err, allowed)
				}
			}
		})
	}
}
