package wipd

import (
	"github.com/procrastivity/wip/internal/operation"
	"github.com/procrastivity/wip/internal/wipdwire"
)

func encodeGateOutput(output operation.Output) ([]byte, error) {
	switch value := output.(type) {
	case operation.GateDeclareOutput:
		return wipdwire.EncodeCanonical(map[string]any{"gate": value.Gate, "scale": value.Scale})
	case operation.GateCloseOutput:
		return wipdwire.EncodeCanonical(map[string]any{"gate": value.Gate, "node_id": value.NodeID, "scale": value.Scale})
	case operation.GateDismissOutput:
		return wipdwire.EncodeCanonical(map[string]any{"gate": value.Gate, "node_id": value.NodeID, "scale": value.Scale})
	default:
		return nil, errMalformedMessage
	}
}

func decodeGateOutput(id operation.ID, raw []byte) (operation.Output, error) {
	keys := []string{"gate", "node_id", "scale"}
	if id == operation.GateDeclareV1.Metadata().Operation {
		keys = []string{"gate", "scale"}
	}
	fields, err := wipdwire.DecodeCanonicalMap(raw, keys...)
	if err != nil {
		return nil, errMalformedMessage
	}
	gate, gateOK := fields["gate"].(string)
	scale, scaleOK := fields["scale"].(string)
	if !gateOK || !scaleOK {
		return nil, errMalformedMessage
	}
	var output operation.Output
	switch id {
	case operation.GateDeclareV1.Metadata().Operation:
		output = operation.GateDeclareOutput{Gate: gate, Scale: scale}
	case operation.GateCloseV1.Metadata().Operation, operation.GateDismissV1.Metadata().Operation:
		node, ok := fields["node_id"].(string)
		if !ok {
			return nil, errMalformedMessage
		}
		if id == operation.GateCloseV1.Metadata().Operation {
			output = operation.GateCloseOutput{Gate: gate, NodeID: node, Scale: scale}
		} else {
			output = operation.GateDismissOutput{Gate: gate, NodeID: node, Scale: scale}
		}
	default:
		return nil, errMalformedMessage
	}
	definition, found := operationDefinition(id)
	if !found || definition.ValidateResult(operation.Result{Code: operation.ResultSucceeded, Output: output}) != nil {
		return nil, errMalformedMessage
	}
	return output, nil
}
