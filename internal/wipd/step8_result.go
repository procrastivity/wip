package wipd

import (
	"github.com/procrastivity/wip/internal/operation"
	"github.com/procrastivity/wip/internal/wipdjournal"
	"github.com/procrastivity/wip/internal/wipdwire"
)

func encodeStep8Output(id operation.ID, output operation.Output) ([]byte, error) {
	var fields map[string]any
	switch id {
	case operation.DependencyAddV1.Metadata().Operation, operation.DependencyRemoveV1.Metadata().Operation:
		value, ok := output.(operation.DependencyOutput)
		if !ok {
			return nil, errMalformedMessage
		}
		fields = map[string]any{"blocked_id": value.BlockedID, "blocker_id": value.BlockerID}
		if id == operation.DependencyAddV1.Metadata().Operation {
			fields["edge"] = value.EdgeID
		}
	case operation.ReferenceBindV1.Metadata().Operation, operation.ReferenceUnbindV1.Metadata().Operation, operation.ReferenceRebindV1.Metadata().Operation:
		value, ok := output.(operation.ReferenceOutput)
		if !ok {
			return nil, errMalformedMessage
		}
		fields = map[string]any{"matter_id": value.MatterID, "reference": value.Reference}
		if id == operation.ReferenceRebindV1.Metadata().Operation {
			fields["previous_reference"] = value.PreviousReference
		}
	default:
		return nil, errMalformedMessage
	}
	return wipdwire.EncodeCanonical(fields)
}

func decodeStep8Output(id operation.ID, raw []byte) (operation.Output, error) {
	var output operation.Output
	var fields map[string]any
	var err error
	switch id {
	case operation.DependencyAddV1.Metadata().Operation, operation.DependencyRemoveV1.Metadata().Operation:
		keys := []string{"blocked_id", "blocker_id"}
		if id == operation.DependencyAddV1.Metadata().Operation {
			keys = append(keys, "edge")
		}
		fields, err = wipdwire.DecodeCanonicalMap(raw, keys...)
		output = operation.DependencyOutput{
			EdgeID: asCommandStartString(fields["edge"]), BlockedID: asCommandStartString(fields["blocked_id"]), BlockerID: asCommandStartString(fields["blocker_id"]),
		}
	case operation.ReferenceBindV1.Metadata().Operation, operation.ReferenceUnbindV1.Metadata().Operation, operation.ReferenceRebindV1.Metadata().Operation:
		keys := []string{"matter_id", "reference"}
		if id == operation.ReferenceRebindV1.Metadata().Operation {
			keys = append(keys, "previous_reference")
		}
		fields, err = wipdwire.DecodeCanonicalMap(raw, keys...)
		output = operation.ReferenceOutput{
			MatterID: asCommandStartString(fields["matter_id"]), Reference: asCommandStartString(fields["reference"]), PreviousReference: asCommandStartString(fields["previous_reference"]),
		}
	default:
		return nil, errMalformedMessage
	}
	definition, found := connectedOperationDefinition(id)
	if err != nil || !found || definition.ValidateResult(operation.Result{Code: operation.ResultSucceeded, Output: output}) != nil {
		return nil, errMalformedMessage
	}
	return output, nil
}

func validateStep8Receipt(entry wipdjournal.Entry, raw []byte, count uint64) error {
	if count != 1 || entry.Delivery != operation.DeliveryAuthority || entry.JournalPosition != 0 || entry.Command.Request.Claim != nil {
		return ErrCommandStartIdentity
	}
	output, err := decodeStep8Output(entry.Command.Request.Operation, raw)
	if err != nil {
		return err
	}
	var want operation.Output
	switch input := entry.Command.Request.Input.(type) {
	case operation.DependencyAddInput:
		value, ok := output.(operation.DependencyOutput)
		if !ok {
			return ErrCommandStartIdentity
		}
		want = operation.DependencyOutput{EdgeID: value.EdgeID, BlockedID: input.BlockedID, BlockerID: input.BlockerID}
	case operation.DependencyRemoveInput:
		want = operation.DependencyOutput{BlockedID: input.BlockedID, BlockerID: input.BlockerID}
	case operation.ReferenceBindInput:
		want = operation.ReferenceOutput{MatterID: input.MatterID, Reference: input.Reference}
	case operation.ReferenceUnbindInput:
		want = operation.ReferenceOutput{MatterID: input.MatterID, Reference: input.Reference}
	case operation.ReferenceRebindInput:
		want = operation.ReferenceOutput{MatterID: input.MatterID, Reference: input.To, PreviousReference: input.From}
	default:
		return ErrCommandStartIdentity
	}
	if output != want {
		return ErrCommandStartIdentity
	}
	return nil
}
