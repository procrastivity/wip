package wipd

import (
	"fmt"

	"github.com/procrastivity/wip/internal/operation"
)

func encodeOperationResultPayload(id operation.ID, result operation.Result) ([]byte, error) {
	definition, found := operationDefinition(id)
	if !found || definition.ValidateResult(result) != nil {
		return nil, fmt.Errorf("wipd: invalid %s result", id)
	}
	fields := map[string]any{
		"code":         string(result.Code),
		"output":       nil,
		"problem_code": nil,
	}
	if result.Code == operation.ResultSucceeded {
		var output map[string]any
		switch id {
		case operation.MatterCreateV1.Metadata().Operation:
			matter, ok := result.Output.(operation.MatterCreateOutput)
			if !ok || !validRequestID(matter.ID) {
				return nil, errMalformedMessage
			}
			output = map[string]any{"id": matter.ID, "locator": matter.Locator, "title": matter.Title}
		case operation.StepCreateV1.Metadata().Operation:
			step, ok := result.Output.(operation.StepCreateOutput)
			if !ok || !validRequestID(step.ID) || !validRequestID(step.ParentID) || !validRequestID(step.MatterID) || step.SortKey <= 0 {
				return nil, errMalformedMessage
			}
			output = map[string]any{
				"id": step.ID, "parent_id": step.ParentID, "matter_id": step.MatterID,
				"locator": step.Locator, "title": step.Title, "sort_key": step.SortKey, "state": step.State,
			}
		case operation.StepStartV1.Metadata().Operation, operation.StepFinishV1.Metadata().Operation:
			step, ok := result.Output.(operation.StepLifecycleOutput)
			if !ok || !validRequestID(step.StepID) || !validRequestID(step.MatterID) {
				return nil, errMalformedMessage
			}
			output = map[string]any{"step_id": step.StepID, "matter_id": step.MatterID, "state": step.State}
		case operation.MatterFinishV1.Metadata().Operation:
			matter, ok := result.Output.(operation.MatterFinishOutput)
			if !ok || !validRequestID(matter.MatterID) {
				return nil, errMalformedMessage
			}
			output = map[string]any{"matter_id": matter.MatterID, "state": matter.State, "became_sealed": matter.BecameSealed}
		default:
			return nil, errMalformedMessage
		}
		encodedOutput, err := encodePayload(output)
		if err != nil {
			return nil, err
		}
		fields["output"] = encodedOutput
	} else {
		fields["problem_code"] = string(result.Problem.Code)
	}
	return encodePayload(fields)
}

// decodeM1ResultPayload accepts only the bare three-field M1 result map. The
// message is intentionally absent on the wire, so the local semantic value
// retains only the stable problem code and uses a non-authoritative
// presentation message.
func decodeM1ResultPayload(payload []byte) (operation.Result, error) {
	return decodeOperationResultPayload(operation.MatterCreateV1.Metadata().Operation, payload)
}

func decodeOperationResultPayload(id operation.ID, payload []byte) (operation.Result, error) {
	definition, found := operationDefinition(id)
	if !found {
		return operation.Result{}, errMalformedMessage
	}
	value, err := decodePayload(payload)
	if err != nil {
		return operation.Result{}, err
	}
	fields, ok := value.(map[string]any)
	if !ok || !exactFields(fields, "code", "output", "problem_code") {
		return operation.Result{}, errMalformedMessage
	}
	codeText, ok := fields["code"].(string)
	if !ok {
		return operation.Result{}, errMalformedMessage
	}
	result := operation.Result{Code: operation.ResultCode(codeText)}
	if result.Code == operation.ResultSucceeded {
		if fields["problem_code"] != nil {
			return operation.Result{}, errMalformedMessage
		}
		encodedOutput, ok := fields["output"].([]byte)
		if !ok {
			return operation.Result{}, errMalformedMessage
		}
		outputValue, err := decodePayload(encodedOutput)
		if err != nil {
			return operation.Result{}, err
		}
		outputFields, ok := outputValue.(map[string]any)
		if !ok {
			return operation.Result{}, errMalformedMessage
		}
		switch id {
		case operation.MatterCreateV1.Metadata().Operation:
			if !exactFields(outputFields, "id", "locator", "title") {
				return operation.Result{}, errMalformedMessage
			}
			matterID, idOK := outputFields["id"].(string)
			locator, locatorOK := outputFields["locator"].(string)
			title, titleOK := outputFields["title"].(string)
			if !idOK || !validRequestID(matterID) || !locatorOK || !titleOK {
				return operation.Result{}, errMalformedMessage
			}
			result.Output = operation.MatterCreateOutput{ID: matterID, Locator: locator, Title: title}
		case operation.StepCreateV1.Metadata().Operation:
			if !exactFields(outputFields, "id", "parent_id", "matter_id", "locator", "title", "sort_key", "state") {
				return operation.Result{}, errMalformedMessage
			}
			stepID, idOK := outputFields["id"].(string)
			parentID, parentOK := outputFields["parent_id"].(string)
			matterID, matterOK := outputFields["matter_id"].(string)
			locator, locatorOK := outputFields["locator"].(string)
			title, titleOK := outputFields["title"].(string)
			sortKey, sortOK := outputFields["sort_key"].(uint64)
			state, stateOK := outputFields["state"].(string)
			if !idOK || !validRequestID(stepID) || !parentOK || !validRequestID(parentID) || !matterOK || !validRequestID(matterID) ||
				!locatorOK || !titleOK || !sortOK || sortKey == 0 || sortKey > uint64(^uint64(0)>>1) || !stateOK {
				return operation.Result{}, errMalformedMessage
			}
			result.Output = operation.StepCreateOutput{
				ID: stepID, ParentID: parentID, MatterID: matterID, Locator: locator, Title: title,
				SortKey: int64(sortKey), State: state,
			}
		case operation.StepStartV1.Metadata().Operation, operation.StepFinishV1.Metadata().Operation:
			if !exactFields(outputFields, "step_id", "matter_id", "state") {
				return operation.Result{}, errMalformedMessage
			}
			stepID, stepOK := outputFields["step_id"].(string)
			matterID, matterOK := outputFields["matter_id"].(string)
			state, stateOK := outputFields["state"].(string)
			if !stepOK || !validRequestID(stepID) || !matterOK || !validRequestID(matterID) || !stateOK {
				return operation.Result{}, errMalformedMessage
			}
			result.Output = operation.StepLifecycleOutput{StepID: stepID, MatterID: matterID, State: state}
		case operation.MatterFinishV1.Metadata().Operation:
			if !exactFields(outputFields, "matter_id", "state", "became_sealed") {
				return operation.Result{}, errMalformedMessage
			}
			matterID, matterOK := outputFields["matter_id"].(string)
			state, stateOK := outputFields["state"].(string)
			sealed, sealedOK := outputFields["became_sealed"].(bool)
			if !matterOK || !validRequestID(matterID) || !stateOK || !sealedOK {
				return operation.Result{}, errMalformedMessage
			}
			result.Output = operation.MatterFinishOutput{MatterID: matterID, State: state, BecameSealed: sealed}
		default:
			return operation.Result{}, errMalformedMessage
		}
	} else {
		if fields["output"] != nil {
			return operation.Result{}, errMalformedMessage
		}
		problemCode, ok := fields["problem_code"].(string)
		if !ok || problemCode == "" {
			return operation.Result{}, errMalformedMessage
		}
		result.Problem = &operation.Problem{
			Code:    operation.ProblemCode(problemCode),
			Message: "local Handler returned " + problemCode,
		}
	}
	if err := definition.ValidateResult(result); err != nil {
		return operation.Result{}, fmt.Errorf("%w: invalid M1 result: %v", errMalformedMessage, err)
	}
	return result, nil
}
