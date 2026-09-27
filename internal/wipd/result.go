package wipd

import (
	"fmt"

	"github.com/procrastivity/wip/internal/operation"
)

// encodeM1ResultPayload maps one validated M1 semantic Result into the
// already-defined terminal-result CBOR map. Step 7 uses this map only as the
// payload of response.end for an explicitly composed local fixture Handler;
// it is not a terminal receipt or an authority outcome.
func encodeM1ResultPayload(result operation.Result) ([]byte, error) {
	if err := operation.MatterCreateV1.ValidateResult(result); err != nil {
		return nil, fmt.Errorf("wipd: invalid local M1 result: %w", err)
	}
	fields := map[string]any{
		"code":         string(result.Code),
		"output":       nil,
		"problem_code": nil,
	}
	if result.Code == operation.ResultSucceeded {
		output, ok := result.Output.(operation.MatterCreateOutput)
		if !ok || !validRequestID(output.ID) {
			return nil, errMalformedMessage
		}
		encodedOutput, err := encodePayload(map[string]any{
			"id":      output.ID,
			"locator": output.Locator,
			"title":   output.Title,
		})
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
		if !ok || !exactFields(outputFields, "id", "locator", "title") {
			return operation.Result{}, errMalformedMessage
		}
		id, idOK := outputFields["id"].(string)
		locator, locatorOK := outputFields["locator"].(string)
		title, titleOK := outputFields["title"].(string)
		if !idOK || !validRequestID(id) || !locatorOK || !titleOK {
			return operation.Result{}, errMalformedMessage
		}
		result.Output = operation.MatterCreateOutput{ID: id, Locator: locator, Title: title}
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
	if err := operation.MatterCreateV1.ValidateResult(result); err != nil {
		return operation.Result{}, fmt.Errorf("%w: invalid M1 result: %v", errMalformedMessage, err)
	}
	return result, nil
}
