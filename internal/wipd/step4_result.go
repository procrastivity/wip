package wipd

import (
	"fmt"

	"github.com/procrastivity/wip/internal/operation"
	"github.com/procrastivity/wip/internal/wipdjournal"
	"github.com/procrastivity/wip/internal/wipdwire"
)

func encodeStep4Output(id operation.ID, output operation.Output) ([]byte, error) {
	var value map[string]any
	switch result := output.(type) {
	case operation.MatterCreateV2Output:
		if id != operation.MatterCreateV2.Metadata().Operation {
			return nil, errMalformedMessage
		}
		value = map[string]any{
			"id": result.ID, "title": result.Title, "requested_locator": result.RequestedLocator,
			"assigned_locator": result.AssignedLocator, "locator_repair_required": result.LocatorRepairRequired,
		}
	case operation.StageCreateOutput:
		if id != operation.StageCreateV1.Metadata().Operation {
			return nil, errMalformedMessage
		}
		value = map[string]any{
			"id": result.ID, "matter_id": result.MatterID, "locator": result.Locator,
			"title": result.Title, "sort_key": result.SortKey, "state": result.State,
		}
	case operation.StepCreateOutput:
		if id != operation.StepCreateV2.Metadata().Operation && id != operation.StepInsertV1.Metadata().Operation {
			return nil, errMalformedMessage
		}
		value = step4CreateOutputMap(result)
	case operation.StepReorderOutput:
		if id != operation.StepReorderV1.Metadata().Operation {
			return nil, errMalformedMessage
		}
		value = map[string]any{"parent_id": result.ParentID, "order": step4StringValues(result.Order)}
	case operation.StepReplaceOutput:
		if id != operation.StepReplaceV1.Metadata().Operation {
			return nil, errMalformedMessage
		}
		value = map[string]any{"removed_step_id": result.RemovedStepID, "replacement": step4CreateOutputMap(result.Replacement)}
	case operation.StepRemoveOutput:
		if id != operation.StepRemoveV1.Metadata().Operation {
			return nil, errMalformedMessage
		}
		value = map[string]any{"step_id": result.StepID}
	case operation.MatterLocatorRepairOutput:
		if id != operation.MatterLocatorRepairV1.Metadata().Operation {
			return nil, errMalformedMessage
		}
		value = map[string]any{
			"id": result.ID, "action": result.Action, "requested_locator": result.RequestedLocator,
			"previous_locator": result.PreviousLocator, "assigned_locator": result.AssignedLocator,
		}
	default:
		return nil, errMalformedMessage
	}
	if definition, ok := operationDefinition(id); !ok || definition.ValidateResult(operation.Result{Code: operation.ResultSucceeded, Output: output}) != nil {
		return nil, errMalformedMessage
	}
	return wipdwire.EncodeCanonical(value)
}

func decodeStep4Output(id operation.ID, raw []byte) (operation.Output, error) {
	var output operation.Output
	switch id {
	case operation.MatterCreateV2.Metadata().Operation:
		fields, err := wipdwire.DecodeCanonicalMap(raw, "id", "title", "requested_locator", "assigned_locator", "locator_repair_required")
		if err != nil {
			return nil, errMalformedMessage
		}
		repair, ok := fields["locator_repair_required"].(bool)
		if !ok {
			return nil, errMalformedMessage
		}
		output = operation.MatterCreateV2Output{
			ID: step4String(fields["id"]), Title: step4String(fields["title"]),
			RequestedLocator: step4String(fields["requested_locator"]), AssignedLocator: step4String(fields["assigned_locator"]), LocatorRepairRequired: repair,
		}
	case operation.StageCreateV1.Metadata().Operation:
		fields, err := wipdwire.DecodeCanonicalMap(raw, "id", "matter_id", "locator", "title", "sort_key", "state")
		if err != nil {
			return nil, errMalformedMessage
		}
		sortKey, ok := step4Int(fields["sort_key"])
		if !ok {
			return nil, errMalformedMessage
		}
		output = operation.StageCreateOutput{
			ID: step4String(fields["id"]), MatterID: step4String(fields["matter_id"]),
			Locator: step4String(fields["locator"]), Title: step4String(fields["title"]), SortKey: sortKey, State: step4String(fields["state"]),
		}
	case operation.StepCreateV2.Metadata().Operation, operation.StepInsertV1.Metadata().Operation:
		fields, err := wipdwire.DecodeCanonicalMap(raw, "id", "parent_id", "matter_id", "locator", "title", "sort_key", "state")
		if err != nil {
			return nil, errMalformedMessage
		}
		step, ok := decodeStep4CreateOutput(fields)
		if !ok {
			return nil, errMalformedMessage
		}
		output = step
	case operation.StepReorderV1.Metadata().Operation:
		fields, err := wipdwire.DecodeCanonicalMap(raw, "parent_id", "order")
		if err != nil {
			return nil, errMalformedMessage
		}
		order, ok := step4Strings(fields["order"])
		if !ok {
			return nil, errMalformedMessage
		}
		output = operation.StepReorderOutput{ParentID: step4String(fields["parent_id"]), Order: order}
	case operation.StepReplaceV1.Metadata().Operation:
		fields, err := wipdwire.DecodeCanonicalMap(raw, "removed_step_id", "replacement")
		if err != nil {
			return nil, errMalformedMessage
		}
		replacement, ok := fields["replacement"].(map[string]any)
		if !ok || !wipdwire.ExactMapKeys(replacement, "id", "parent_id", "matter_id", "locator", "title", "sort_key", "state") {
			return nil, errMalformedMessage
		}
		step, ok := decodeStep4CreateOutput(replacement)
		if !ok {
			return nil, errMalformedMessage
		}
		output = operation.StepReplaceOutput{RemovedStepID: step4String(fields["removed_step_id"]), Replacement: step}
	case operation.StepRemoveV1.Metadata().Operation:
		fields, err := wipdwire.DecodeCanonicalMap(raw, "step_id")
		if err != nil {
			return nil, errMalformedMessage
		}
		output = operation.StepRemoveOutput{StepID: step4String(fields["step_id"])}
	case operation.MatterLocatorRepairV1.Metadata().Operation:
		fields, err := wipdwire.DecodeCanonicalMap(raw, "id", "action", "requested_locator", "previous_locator", "assigned_locator")
		if err != nil {
			return nil, errMalformedMessage
		}
		output = operation.MatterLocatorRepairOutput{
			ID: step4String(fields["id"]), Action: step4String(fields["action"]),
			RequestedLocator: step4String(fields["requested_locator"]), PreviousLocator: step4String(fields["previous_locator"]),
			AssignedLocator: step4String(fields["assigned_locator"]),
		}
	default:
		return nil, errMalformedMessage
	}
	definition, ok := operationDefinition(id)
	if !ok || definition.ValidateResult(operation.Result{Code: operation.ResultSucceeded, Output: output}) != nil {
		return nil, errMalformedMessage
	}
	return output, nil
}

func validateStep4Receipt(entry wipdjournal.Entry, raw []byte, eventCount uint64) error {
	output, err := decodeStep4Output(entry.Command.Request.Operation, raw)
	if err != nil {
		return err
	}
	valid := false
	switch input := entry.Command.Request.Input.(type) {
	case operation.MatterCreateInput:
		got := output.(operation.MatterCreateV2Output)
		valid = got.Title == input.Title && (input.Locator == "" || got.RequestedLocator == input.Locator)
		wantEvents := uint64(1)
		if got.LocatorRepairRequired {
			wantEvents = 2
		}
		valid = valid && eventCount == wantEvents
	case operation.StageCreateInput:
		got := output.(operation.StageCreateOutput)
		valid = got.MatterID == input.MatterID && got.Title == input.Title && eventCount == 1
	case operation.StepCreateInput:
		got := output.(operation.StepCreateOutput)
		valid = got.ParentID == input.ParentID && got.Title == input.Title && eventCount == 1
	case operation.StepInsertInput:
		got := output.(operation.StepCreateOutput)
		valid = got.ParentID == input.ParentID && got.Title == input.Title && (eventCount == 1 || eventCount == 2)
	case operation.StepReorderInput:
		got := output.(operation.StepReorderOutput)
		valid = got.ParentID == input.ParentID && equalStep4Strings(got.Order, input.Order) && eventCount == 1
	case operation.StepReplaceInput:
		got := output.(operation.StepReplaceOutput)
		valid = got.RemovedStepID == input.StepID && got.Replacement.Title == input.Title && eventCount == 1
	case operation.StepRemoveInput:
		got := output.(operation.StepRemoveOutput)
		valid = got.StepID == input.StepID && eventCount == 1
	case operation.MatterLocatorRepairInput:
		got := output.(operation.MatterLocatorRepairOutput)
		valid = got.ID == input.MatterID && got.Action == input.Action && got.AssignedLocator == input.AssignedLocator && eventCount == 1
	}
	if !valid {
		return fmt.Errorf("M6 Step 4 receipt output does not match the submitted command")
	}
	return nil
}

func step4CreateOutputMap(output operation.StepCreateOutput) map[string]any {
	return map[string]any{
		"id": output.ID, "parent_id": output.ParentID, "matter_id": output.MatterID, "locator": output.Locator,
		"title": output.Title, "sort_key": output.SortKey, "state": output.State,
	}
}

func decodeStep4CreateOutput(fields map[string]any) (operation.StepCreateOutput, bool) {
	sortKey, ok := step4Int(fields["sort_key"])
	if !ok {
		return operation.StepCreateOutput{}, false
	}
	return operation.StepCreateOutput{
		ID: step4String(fields["id"]), ParentID: step4String(fields["parent_id"]),
		MatterID: step4String(fields["matter_id"]), Locator: step4String(fields["locator"]), Title: step4String(fields["title"]),
		SortKey: sortKey, State: step4String(fields["state"]),
	}, true
}

func step4Int(value any) (int64, bool) {
	unsigned, ok := value.(uint64)
	if !ok || unsigned > uint64(^uint64(0)>>1) {
		return 0, false
	}
	return int64(unsigned), true
}

func step4String(value any) string {
	text, _ := value.(string)
	return text
}

func step4Strings(value any) ([]string, bool) {
	values, ok := value.([]any)
	if !ok {
		return nil, false
	}
	result := make([]string, len(values))
	for index, value := range values {
		text, valid := value.(string)
		if !valid {
			return nil, false
		}
		result[index] = text
	}
	return result, true
}

func step4StringValues(values []string) []any {
	result := make([]any, len(values))
	for index, value := range values {
		result[index] = value
	}
	return result
}

func equalStep4Strings(left, right []string) bool {
	if len(left) != len(right) {
		return false
	}
	for index := range left {
		if left[index] != right[index] {
			return false
		}
	}
	return true
}
