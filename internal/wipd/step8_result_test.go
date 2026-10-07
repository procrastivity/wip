package wipd

import (
	"bytes"
	"context"
	"testing"

	"github.com/procrastivity/wip/internal/operation"
	"github.com/procrastivity/wip/internal/wipdjournal"
	"github.com/procrastivity/wip/internal/wipdwire"
)

func TestStep8TypedResultsAndReceiptBindings(t *testing.T) {
	a, b, edge := commandStartCommandPrefix+"43", commandStartCommandPrefix+"44", commandStartCommandPrefix+"45"
	cases := []struct {
		definition operation.Definition
		input      operation.Input
		output     operation.Output
		fields     map[string]any
		problem    string
	}{
		{operation.DependencyAddV1, operation.DependencyAddInput{BlockedID: a, BlockerID: b}, operation.DependencyOutput{EdgeID: edge, BlockedID: a, BlockerID: b}, map[string]any{"edge": edge, "blocked_id": a, "blocker_id": b}, "refusal.dependency-cycle"},
		{operation.DependencyRemoveV1, operation.DependencyRemoveInput{BlockedID: a, BlockerID: b}, operation.DependencyOutput{BlockedID: a, BlockerID: b}, map[string]any{"blocked_id": a, "blocker_id": b}, "refusal.dependency-missing"},
		{operation.ReferenceBindV1, operation.ReferenceBindInput{MatterID: a, Reference: "OLD"}, operation.ReferenceOutput{MatterID: a, Reference: "OLD"}, map[string]any{"matter_id": a, "reference": "OLD"}, "refusal.reference-exists"},
		{operation.ReferenceUnbindV1, operation.ReferenceUnbindInput{MatterID: a, Reference: "OLD"}, operation.ReferenceOutput{MatterID: a, Reference: "OLD"}, map[string]any{"matter_id": a, "reference": "OLD"}, "refusal.reference-missing"},
		{operation.ReferenceRebindV1, operation.ReferenceRebindInput{MatterID: a, From: "OLD", To: "NEW"}, operation.ReferenceOutput{MatterID: a, Reference: "NEW", PreviousReference: "OLD"}, map[string]any{"matter_id": a, "reference": "NEW", "previous_reference": "OLD"}, "refusal.reference-destination-exists"},
	}
	for _, tc := range cases {
		t.Run(tc.definition.Metadata().Operation.Name, func(t *testing.T) {
			id := tc.definition.Metadata().Operation
			if _, defaulted := operationDefinition(id); defaulted {
				t.Fatal("Step 8 entered default catalogue")
			}
			commandID := commandStartCommandPrefix + "50"
			command := commandStartCanonicalCommand(commandID, 3, "", commandID, id, tc.input, nil)
			entry := wipdjournal.Entry{Command: command, RequestHash: mustCommandStartHash(t, command), EnvironmentSeq: 3, Delivery: operation.DeliveryAuthority, State: wipdjournal.StateAttemptPrepared}
			if validateConnectedCommand(entry) != nil || claimJournalMember(entry) {
				t.Fatal("authority/claim-free eligibility changed")
			}
			claimed := entry
			claimed.Command.Request.Claim = &operation.ClaimContext{ID: a, Epoch: "1"}
			if validateConnectedCommand(claimed) == nil {
				t.Fatal("Step 8 accepted claim context")
			}
			output := batchSweepResultTestPayload(t, tc.fields)
			result := operation.Result{Code: operation.ResultSucceeded, Output: tc.output}
			wire := batchSweepResultTestPayload(t, map[string]any{"code": string(result.Code), "output": output, "problem_code": nil})
			encoded, err := encodeOperationResultPayload(id, result)
			decoded, decodeErr := decodeOperationResultPayload(id, wire)
			if err != nil || decodeErr != nil || !bytes.Equal(encoded, wire) || decoded.Output != tc.output {
				t.Fatalf("exact typed result: %+v %v %v", decoded, err, decodeErr)
			}
			// Use an independent canonical output map, not the codec's output,
			// as the receipt's expected authority success shape.
			raw, err := commandStartReceipt(entry, operation.ResultRefused, nil)
			if err != nil {
				t.Fatal(err)
			}
			fields, err := wipdwire.DecodeCanonicalMap(raw, "schema", "domain_id", "authority_epoch", "identity_schema", "command_id", "request_hash", "operation", "environment", "result", "accepted_events")
			if err != nil {
				t.Fatal(err)
			}
			fields["result"] = map[string]any{"code": "result.succeeded", "output": output, "problem_code": nil}
			fields["accepted_events"] = map[string]any{"first_event_id": edge, "last_event_id": edge, "event_count": uint64(1)}
			receipt := batchSweepResultTestPayload(t, fields)
			got, err := commandReceiptResult(entry, receipt)
			if err != nil || got.Output != tc.output {
				t.Fatalf("typed receipt: %+v %v", got, err)
			}
			for key := range tc.fields {
				bad := make(map[string]any, len(tc.fields))
				for k, v := range tc.fields {
					bad[k] = v
				}
				bad[key] = "WRONG"
				if key == "matter_id" || key == "blocked_id" || key == "blocker_id" {
					bad[key] = commandStartCommandPrefix + "46"
				}
				fields["result"].(map[string]any)["output"] = batchSweepResultTestPayload(t, bad)
				if _, err := commandReceiptResult(entry, batchSweepResultTestPayload(t, fields)); err == nil {
					t.Fatalf("accepted substituted output %s", key)
				}
				delete(bad, key)
				if _, err := decodeStep8Output(id, batchSweepResultTestPayload(t, bad)); err == nil {
					t.Fatalf("accepted missing output field %s", key)
				}
			}
			fields["result"].(map[string]any)["output"] = output
			for _, count := range []uint64{0, 2} {
				fields["accepted_events"].(map[string]any)["event_count"] = count
				if _, err := commandReceiptResult(entry, batchSweepResultTestPayload(t, fields)); err == nil {
					t.Fatalf("accepted event count %d", count)
				}
			}
			fields["accepted_events"].(map[string]any)["event_count"] = uint64(1)
			fields["accepted_events"].(map[string]any)["last_event_id"] = commandStartCommandPrefix + "46"
			if _, err := commandReceiptResult(entry, batchSweepResultTestPayload(t, fields)); err == nil {
				t.Fatal("accepted two endpoints for single-event range")
			}
			fields["accepted_events"] = nil
			fields["result"] = map[string]any{"code": "result.refused", "output": nil, "problem_code": tc.problem}
			got, err = commandReceiptResult(entry, batchSweepResultTestPayload(t, fields))
			if err != nil || got.Code != operation.ResultRefused || got.Problem.Code != operation.ProblemCode(tc.problem) {
				t.Fatalf("typed refusal: %+v %v", got, err)
			}
			fields["accepted_events"] = map[string]any{"first_event_id": edge, "last_event_id": edge, "event_count": uint64(1)}
			if _, err := commandReceiptResult(entry, batchSweepResultTestPayload(t, fields)); err == nil {
				t.Fatal("refusal accepted an effect")
			}
			// Unknown operation versions cannot inherit a known output codec.
			if _, err := decodeOperationResultPayload(operation.ID{Name: id.Name, Version: 3}, wire); err == nil {
				t.Fatal("accepted unknown version")
			}
		})
	}
}

func TestStep8CapabilityVersionAndIdentityIntersection(t *testing.T) {
	registry := operation.NewRegistry()
	for _, definition := range operation.Step8Catalogue() {
		if err := registry.Register(definition, func(context.Context, operation.Request) operation.Result { return operation.Result{} }); err != nil {
			t.Fatal(err)
		}
	}
	for _, definition := range operation.Step8Catalogue() {
		id := definition.Metadata().Operation
		for _, test := range []struct {
			version  uint16
			schema   string
			selected bool
		}{{2, identitySchemaV1, true}, {1, identitySchemaV1, false}, {2, "wipd.command/2", false}} {
			hello := capabilityHello{
				protocolMin: protocolVersion{major: 1}, protocolMax: protocolVersion{major: 1},
				identitySchemas: []string{identitySchemaV1}, storeSchemas: []string{storeSchemaV1}, features: []string{frameSchema},
				operations: []operationCapability{{name: id.Name, versions: []uint16{test.version}, identitySchemas: []string{test.schema}}},
			}
			selected, _, err := negotiateCapabilities(hello, registry, false, false, false, false)
			if err != nil || operationCapabilityContains(selected.operations, id, identitySchemaV1) != test.selected {
				t.Fatalf("%s version=%d schema=%s: %+v %v", id, test.version, test.schema, selected, err)
			}
			if len(selected.operations) > 1 {
				t.Fatal("server selected unoffered Step 8 operations")
			}
		}
	}
}
