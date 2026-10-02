package operation

import (
	"bytes"
	"encoding/json"
	"reflect"
	"strings"
	"testing"

	"github.com/fxamacker/cbor/v2"
)

func repairTestCommand() Command {
	return Command{
		ID: testCommandID, AuthorityDomainID: testDomainID, ExpectedAuthorityEpoch: 7,
		EnvironmentID: testEnvironmentID, EnvironmentSequence: 42,
		ActedAt: "2026-09-22T17:31:42.123456789Z", CorrelationCommandID: testCommandID,
		Request: Request{
			Operation: GateExemptionRepairV1.Metadata().Operation, Actor: "human",
			Context: Context{Repo: testRepoID, Clone: "01K5V8KGD3F6H9J2M4N7Q0R5TX", Worktree: "01K5V8KGD3F6H9J2M4N7Q0R5TY"},
			Claim:   &ClaimContext{ID: "01K5V8KGD3F6H9J2M4N7Q0R5TZ", Epoch: "9"},
			Input: GateExemptionRepairInput{
				NodeID: "01K5V8KGD3F6H9J2M4N7Q0R5TV", Gate: "review", EventCount: 17,
				HighWaterEventID: "01K5V8KGD3F6H9J2M4N7Q0R5TS",
				PrefixDigest:     "sha256:" + strings.Repeat("a", 64),
				IncidentRef:      "urn:incident:asymmetric-42", Reason: "Café\nowner review",
				EvidenceRefs: []string{"sha256:" + strings.Repeat("b", 64), "sha256:" + strings.Repeat("c", 64)},
			}, Blobs: []BlobInput{},
		},
	}
}

func TestGateExemptionRepairCanonicalMatchesPrivateInputMap(t *testing.T) {
	command := repairTestCommand()
	encoded, err := command.CanonicalBytes()
	if err != nil {
		t.Fatal(err)
	}
	decoded, err := DecodeCanonicalCommand(encoded)
	if err != nil || !reflect.DeepEqual(decoded, command) {
		t.Fatalf("roundtrip = %+v, %v", decoded, err)
	}
	input := command.Request.Input.(GateExemptionRepairInput)
	mode, err := cbor.CanonicalEncOptions().EncMode()
	if err != nil {
		t.Fatal(err)
	}
	// Independent mirror of the private admission encoder's complete command
	// map; a missing boundary field, renamed key or nonempty blobs changes bytes.
	want, err := mode.Marshal(map[string]any{
		"schema": "wipd.command/1", "command_id": command.ID,
		"authority":   map[string]any{"domain_id": command.AuthorityDomainID, "expected_epoch": command.ExpectedAuthorityEpoch},
		"environment": map[string]any{"id": command.EnvironmentID, "sequence": command.EnvironmentSequence},
		"acted_at":    command.ActedAt, "actor": "human", "causation_command_id": nil,
		"correlation_command_id": command.CorrelationCommandID,
		"operation":              map[string]any{"name": "gate.exemption.repair", "version": uint64(1)},
		"context":                map[string]any{"repo_id": command.Request.Context.Repo, "clone_id": command.Request.Context.Clone, "worktree_id": command.Request.Context.Worktree},
		"claim":                  map[string]any{"id": command.Request.Claim.ID, "epoch": uint64(9)},
		"input": map[string]any{
			"node_id": input.NodeID, "gate": input.Gate, "event_count": input.EventCount,
			"high_water_event_id": input.HighWaterEventID, "prefix_digest": input.PrefixDigest,
			"incident_ref": input.IncidentRef, "reason": input.Reason, "evidence_refs": input.EvidenceRefs,
		}, "blobs": []any{},
	})
	if err != nil || !bytes.Equal(encoded, want) {
		t.Fatalf("private admission CBOR parity = %v; got %x, want %x", err, encoded, want)
	}
	if bytes.Contains(encoded, []byte("proof")) || bytes.Contains(encoded, []byte("reason_digest")) || bytes.Contains(encoded, []byte("authorization")) {
		t.Fatal("detached proof leaked into command")
	}
	fields := map[string]any{}
	if err := cbor.Unmarshal(encoded, &fields); err != nil {
		t.Fatal(err)
	}
	if blobs, ok := fields["blobs"].([]any); !ok || len(blobs) != 0 {
		t.Fatalf("blobs = %v", fields["blobs"])
	}
	if _, err := command.RequestHash(); err != nil {
		t.Fatal(err)
	}
}

func TestGateExemptionRepairClosedInputAndClaim(t *testing.T) {
	command := repairTestCommand()
	for _, edit := range []func(*Command){
		func(c *Command) { c.Request.Claim = nil },
		func(c *Command) { c.Request.Actor = "role:worker" },
		func(c *Command) { c.Request.Context.Clone = "" },
		func(c *Command) { c.Request.Context.Worktree = "" },
		func(c *Command) { c.Request.Blobs = []BlobInput{{Name: "proof"}} },
		func(c *Command) {
			v := c.Request.Input.(GateExemptionRepairInput)
			v.EventCount = 0
			c.Request.Input = v
		},
		func(c *Command) {
			v := c.Request.Input.(GateExemptionRepairInput)
			v.EvidenceRefs = []string{}
			c.Request.Input = v
		},
		func(c *Command) {
			v := c.Request.Input.(GateExemptionRepairInput)
			v.IncidentRef = "http://insecure.example"
			c.Request.Input = v
		},
		func(c *Command) {
			v := c.Request.Input.(GateExemptionRepairInput)
			v.Reason = strings.Repeat("x", 4097)
			c.Request.Input = v
		},
	} {
		bad := command
		edit(&bad)
		if _, err := bad.CanonicalBytes(); err == nil {
			t.Fatalf("accepted invalid repair request %+v", bad.Request)
		}
	}
	encoded, _ := command.CanonicalBytes()
	var fields map[string]any
	if err := canonicalCommandDecoder.Unmarshal(encoded, &fields); err != nil {
		t.Fatal(err)
	}
	input := fields["input"].(map[string]any)
	input["proof"] = "forbidden"
	mode, _ := cbor.CanonicalEncOptions().EncMode()
	bad, _ := mode.Marshal(fields)
	if _, err := DecodeCanonicalCommand(bad); err == nil {
		t.Fatal("decoded an unknown proof field")
	}
	if len(GateCatalogue()) != 3 || len(Catalogue()) != 31 || Catalogue()[30].Metadata().Operation != GateExemptionRepairV1.Metadata().Operation {
		t.Fatal("repair must be in the full catalogue, not ordinary GateCatalogue")
	}
	for _, already := range []bool{false, true} {
		output := GateExemptionRepairOutput{Gate: "review", NodeID: command.Request.Input.(GateExemptionRepairInput).NodeID, AlreadyExempt: already}
		if err := GateExemptionRepairV1.ValidateResult(Result{Code: ResultSucceeded, Output: output}); err != nil {
			t.Fatal(err)
		}
		encoded, err := json.Marshal(output)
		if err != nil || !bytes.Equal(encoded, []byte(`{"gate":"review","node_id":"`+output.NodeID+`","already_exempt":`+map[bool]string{true: "true", false: "false"}[already]+`}`)) {
			t.Fatalf("output keys = %s, %v", encoded, err)
		}
		var cborFields map[string]any
		encoded, err = cbor.Marshal(output)
		if err != nil || cbor.Unmarshal(encoded, &cborFields) != nil || len(cborFields) != 3 {
			t.Fatalf("output CBOR fields = %v, %v", cborFields, err)
		}
		for _, key := range []string{"gate", "node_id", "already_exempt"} {
			if _, ok := cborFields[key]; !ok {
				t.Fatalf("missing output key %q: %v", key, cborFields)
			}
		}
	}
}

func TestGateExemptionRepairMetadata(t *testing.T) {
	metadata := GateExemptionRepairV1.Metadata()
	if metadata.Operation != (ID{Name: "gate.exemption.repair", Version: 1}) || metadata.Delivery != DeliveryAuthority || metadata.Access != AccessMutation || metadata.Claim != ClaimExact ||
		!reflect.DeepEqual(metadata.RequiredContext, []ContextDimension{ContextRepo, ContextClone, ContextWorktree}) ||
		!reflect.DeepEqual(metadata.Guards, []Footprint{
			FootprintMatterActiveClaim, FootprintGateSubject, FootprintAncestorLifecycle,
			FootprintGateDeclaration, FootprintGateState, FootprintGateExemptionSnapshot,
			FootprintGateRepairBoundary, FootprintGateRepairOwnerAuthorization,
		}) || !reflect.DeepEqual(metadata.Writes, []Footprint{FootprintGateState, FootprintGateExemptionSnapshot}) ||
		metadata.BlobInputs == nil || len(metadata.BlobInputs) != 0 || metadata.ExternalEffects == nil || len(metadata.ExternalEffects) != 0 {
		t.Fatalf("repair metadata = %+v", metadata)
	}
}
