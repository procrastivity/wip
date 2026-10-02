package wipd

import (
	"github.com/procrastivity/wip/internal/operation"
	"github.com/procrastivity/wip/internal/wipdwire"
)

func encodeRepairOutput(output operation.Output) ([]byte, error) {
	repair, ok := output.(operation.GateExemptionRepairOutput)
	if !ok || repair.Gate == "" || !validRequestID(repair.NodeID) {
		return nil, errMalformedMessage
	}
	return wipdwire.EncodeCanonical(map[string]any{
		"gate": repair.Gate, "node_id": repair.NodeID, "already_exempt": repair.AlreadyExempt,
	})
}

func decodeRepairOutput(raw []byte) (operation.GateExemptionRepairOutput, error) {
	fields, err := wipdwire.DecodeCanonicalMap(raw, "gate", "node_id", "already_exempt")
	gate, gateOK := fields["gate"].(string)
	node, nodeOK := fields["node_id"].(string)
	already, alreadyOK := fields["already_exempt"].(bool)
	if err != nil || !gateOK || gate == "" || !nodeOK || !validRequestID(node) || !alreadyOK {
		return operation.GateExemptionRepairOutput{}, errMalformedMessage
	}
	return operation.GateExemptionRepairOutput{Gate: gate, NodeID: node, AlreadyExempt: already}, nil
}
