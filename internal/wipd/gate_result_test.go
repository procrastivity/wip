package wipd

import (
	"bytes"
	"reflect"
	"strings"
	"testing"

	"github.com/procrastivity/wip/internal/operation"
	"github.com/procrastivity/wip/internal/wipdjournal"
	"github.com/procrastivity/wip/internal/wipdwire"
)

func TestOrdinaryGateResultPayloadAndReceipt(t *testing.T) {
	node := "01KZ7XHAQT1S46NYPN1PW1DX71"
	for _, test := range []struct {
		definition operation.Definition
		input      operation.Input
		output     operation.Output
		fields     map[string]any
	}{
		{operation.GateDeclareV1, operation.GateDeclareInput{Gate: "local-check", Scale: "stage"}, operation.GateDeclareOutput{Gate: "local-check", Scale: "stage"}, map[string]any{"gate": "local-check", "scale": "stage"}},
		{operation.GateCloseV1, operation.GateCloseInput{Gate: "reviewed-local", NodeID: node}, operation.GateCloseOutput{Gate: "reviewed-local", NodeID: node, Scale: "matter"}, map[string]any{"gate": "reviewed-local", "node_id": node, "scale": "matter"}},
		{operation.GateDismissV1, operation.GateDismissInput{Gate: "local-check", NodeID: node, Reason: "Not applicable"}, operation.GateDismissOutput{Gate: "local-check", NodeID: node, Scale: "step"}, map[string]any{"gate": "local-check", "node_id": node, "scale": "step"}},
	} {
		id := test.definition.Metadata().Operation
		t.Run(id.Name, func(t *testing.T) {
			want := operation.Result{Code: operation.ResultSucceeded, Output: test.output}
			output, _ := wipdwire.EncodeCanonical(test.fields)
			resultFields := map[string]any{"code": "result.succeeded", "output": output, "problem_code": nil}
			wire, _ := wipdwire.EncodeCanonical(resultFields)
			encoded, err := encodeOperationResultPayload(id, want)
			if err != nil || !bytes.Equal(encoded, wire) {
				t.Fatalf("encode: %x %v; want %x", encoded, err, wire)
			}
			decoded, err := decodeOperationResultPayload(id, wire)
			if err != nil || !reflect.DeepEqual(decoded, want) {
				t.Fatalf("decode: %+v %v; want %+v", decoded, err, want)
			}
			entry := wipdjournal.Entry{Delivery: operation.DeliveryClaim, RequestHash: "sha256:" + strings.Repeat("a", 64), EnvironmentSeq: 7,
				Command: operation.Command{ID: node, AuthorityDomainID: strings.Repeat("b", 64), ExpectedAuthorityEpoch: 1, EnvironmentID: node,
					Request: operation.Request{Operation: id, Input: test.input}},
			}
			accepted := map[string]any{"first_event_id": node, "last_event_id": node, "event_count": uint64(1)}
			fields := map[string]any{
				"schema": "wipd.terminal-receipt/1", "domain_id": entry.Command.AuthorityDomainID, "authority_epoch": uint64(1),
				"identity_schema": "wipd.command/1", "command_id": node, "request_hash": entry.RequestHash,
				"operation": map[string]any{"name": id.Name, "version": uint64(1)}, "environment": map[string]any{"id": node, "sequence": uint64(7)},
				"result": resultFields, "accepted_events": accepted,
			}
			for _, noEvent := range []bool{false, true} {
				fields["accepted_events"] = accepted
				if noEvent {
					fields["accepted_events"] = nil
				}
				raw, _ := wipdwire.EncodeCanonical(fields)
				got, err := commandReceiptResult(entry, raw)
				allowed := !noEvent || id == operation.GateDeclareV1.Metadata().Operation
				if (err == nil) != allowed || allowed && !reflect.DeepEqual(got, want) {
					t.Fatalf("no-event=%t receipt: %+v %v; allowed=%t", noEvent, got, err, allowed)
				}
			}
			fields["accepted_events"] = accepted
			for _, mutation := range []string{"wrong-gate", "wrong-node", "invalid-scale", "extra-key", "wrong-type"} {
				if mutation == "wrong-node" && id == operation.GateDeclareV1.Metadata().Operation {
					continue
				}
				bad := make(map[string]any)
				for k, v := range test.fields {
					bad[k] = v
				}
				switch mutation {
				case "wrong-gate":
					bad["gate"] = "substituted"
				case "wrong-node":
					bad["node_id"] = "01KZ7XHAQT1S46NYPN1PW1DX72"
				case "invalid-scale":
					bad["scale"] = "repo"
				case "extra-key":
					bad["extra"] = true
				case "wrong-type":
					bad["gate"] = uint64(1)
				}
				resultFields["output"], _ = wipdwire.EncodeCanonical(bad)
				raw, _ := wipdwire.EncodeCanonical(fields)
				if _, err := commandReceiptResult(entry, raw); err == nil {
					t.Fatalf("receipt accepted %s", mutation)
				}
				if mutation != "wrong-gate" && mutation != "wrong-node" {
					payload, _ := wipdwire.EncodeCanonical(resultFields)
					if _, err := decodeOperationResultPayload(id, payload); err == nil {
						t.Fatalf("result codec accepted %s", mutation)
					}
				}
			}
		})
	}
}
