package wipd

import (
	"github.com/procrastivity/wip/internal/operation"
	"github.com/procrastivity/wip/internal/wipdjournal"
	"github.com/procrastivity/wip/internal/wipdwire"
)

func encodeStep5Output(id operation.ID, output operation.Output) ([]byte, error) {
	switch id {
	case operation.MatterStartV1.Metadata().Operation, operation.MatterPauseV1.Metadata().Operation,
		operation.MatterResumeV1.Metadata().Operation, operation.MatterCancelV1.Metadata().Operation:
		matter, ok := output.(operation.MatterLifecycleOutput)
		if !ok || !validRequestID(matter.MatterID) || matter.State != step5State(id) {
			return nil, errMalformedMessage
		}
		return wipdwire.EncodeCanonical(map[string]any{"matter_id": matter.MatterID, "state": matter.State})
	case operation.StageStartV1.Metadata().Operation, operation.StagePauseV1.Metadata().Operation,
		operation.StageResumeV1.Metadata().Operation, operation.StageCancelV1.Metadata().Operation,
		operation.StageFinishV1.Metadata().Operation:
		node, ok := output.(operation.NodeLifecycleOutput)
		if !ok || !validRequestID(node.NodeID) || !validRequestID(node.MatterID) || node.State != step5State(id) {
			return nil, errMalformedMessage
		}
		return wipdwire.EncodeCanonical(map[string]any{"node_id": node.NodeID, "matter_id": node.MatterID, "state": node.State})
	case operation.StepPauseV1.Metadata().Operation, operation.StepResumeV1.Metadata().Operation,
		operation.StepCancelV1.Metadata().Operation:
		step, ok := output.(operation.StepLifecycleOutput)
		if !ok || !validRequestID(step.StepID) || !validRequestID(step.MatterID) || step.State != step5State(id) {
			return nil, errMalformedMessage
		}
		return wipdwire.EncodeCanonical(map[string]any{"step_id": step.StepID, "matter_id": step.MatterID, "state": step.State})
	default:
		return nil, errMalformedMessage
	}
}

func decodeStep5Output(id operation.ID, raw []byte) (operation.Output, error) {
	switch id {
	case operation.MatterStartV1.Metadata().Operation, operation.MatterPauseV1.Metadata().Operation,
		operation.MatterResumeV1.Metadata().Operation, operation.MatterCancelV1.Metadata().Operation:
		fields, err := wipdwire.DecodeCanonicalMap(raw, "matter_id", "state")
		matterID, idOK := fields["matter_id"].(string)
		state, stateOK := fields["state"].(string)
		if err != nil || !idOK || !validRequestID(matterID) || !stateOK || state != step5State(id) {
			return nil, errMalformedMessage
		}
		return operation.MatterLifecycleOutput{MatterID: matterID, State: state}, nil
	case operation.StageStartV1.Metadata().Operation, operation.StagePauseV1.Metadata().Operation,
		operation.StageResumeV1.Metadata().Operation, operation.StageCancelV1.Metadata().Operation,
		operation.StageFinishV1.Metadata().Operation:
		fields, err := wipdwire.DecodeCanonicalMap(raw, "node_id", "matter_id", "state")
		nodeID, nodeOK := fields["node_id"].(string)
		matterID, matterOK := fields["matter_id"].(string)
		state, stateOK := fields["state"].(string)
		if err != nil || !nodeOK || !validRequestID(nodeID) || !matterOK || !validRequestID(matterID) ||
			!stateOK || state != step5State(id) {
			return nil, errMalformedMessage
		}
		return operation.NodeLifecycleOutput{NodeID: nodeID, MatterID: matterID, State: state}, nil
	case operation.StepPauseV1.Metadata().Operation, operation.StepResumeV1.Metadata().Operation,
		operation.StepCancelV1.Metadata().Operation:
		fields, err := wipdwire.DecodeCanonicalMap(raw, "step_id", "matter_id", "state")
		stepID, stepOK := fields["step_id"].(string)
		matterID, matterOK := fields["matter_id"].(string)
		state, stateOK := fields["state"].(string)
		if err != nil || !stepOK || !validRequestID(stepID) || !matterOK || !validRequestID(matterID) ||
			!stateOK || state != step5State(id) {
			return nil, errMalformedMessage
		}
		return operation.StepLifecycleOutput{StepID: stepID, MatterID: matterID, State: state}, nil
	default:
		return nil, errMalformedMessage
	}
}

func step5State(id operation.ID) string {
	switch id {
	case operation.MatterStartV1.Metadata().Operation, operation.MatterResumeV1.Metadata().Operation,
		operation.StageStartV1.Metadata().Operation, operation.StageResumeV1.Metadata().Operation,
		operation.StepResumeV1.Metadata().Operation:
		return "in-progress"
	case operation.MatterPauseV1.Metadata().Operation, operation.StagePauseV1.Metadata().Operation,
		operation.StepPauseV1.Metadata().Operation:
		return "paused"
	case operation.MatterCancelV1.Metadata().Operation, operation.StageCancelV1.Metadata().Operation,
		operation.StepCancelV1.Metadata().Operation:
		return "canceled"
	case operation.StageFinishV1.Metadata().Operation:
		return "done"
	default:
		return ""
	}
}

func validateStep5Receipt(entry wipdjournal.Entry, raw []byte, eventCount uint64) error {
	if eventCount == 0 || eventCount > 2 {
		return errMalformedMessage
	}
	if entry.Command.Request.Operation != operation.StageStartV1.Metadata().Operation && eventCount != 1 {
		return errMalformedMessage
	}
	output, err := decodeStep5Output(entry.Command.Request.Operation, raw)
	if err != nil {
		return err
	}
	switch got := output.(type) {
	case operation.MatterLifecycleOutput:
		input, ok := entry.Command.Request.Input.(operation.NodeLifecycleInput)
		if !ok || input.NodeID != got.MatterID {
			return errMalformedMessage
		}
	case operation.NodeLifecycleOutput:
		input, ok := entry.Command.Request.Input.(operation.NodeLifecycleInput)
		if !ok || input.NodeID != got.NodeID {
			return errMalformedMessage
		}
	case operation.StepLifecycleOutput:
		var stepID string
		switch input := entry.Command.Request.Input.(type) {
		case operation.StepLifecycleInput:
			stepID = input.StepID
		case operation.StepCancelInput:
			stepID = input.StepID
		}
		if stepID == "" || stepID != got.StepID {
			return errMalformedMessage
		}
	default:
		return errMalformedMessage
	}
	return nil
}
