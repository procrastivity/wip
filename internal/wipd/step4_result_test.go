package wipd

import (
	"reflect"
	"strconv"
	"testing"

	"github.com/procrastivity/wip/internal/operation"
	"github.com/procrastivity/wip/internal/wipdjournal"
	"github.com/procrastivity/wip/internal/wipdwire"
)

func TestStep4TypedResultsRoundTripAndBindToReceipt(t *testing.T) {
	const (
		matter = "01KZ7XHAQT1S46NYPN1PW1DX3A"
		stage  = "01KZ7XHAQT1S46NYPN1PW1DX3B"
		stepA  = "01KZ7XHAQT1S46NYPN1PW1DX3C"
		stepB  = "01KZ7XHAQT1S46NYPN1PW1DX3D"
	)
	cases := []struct {
		definition operation.Definition
		output     operation.Output
	}{
		{operation.MatterCreateV2, operation.MatterCreateV2Output{ID: matter, Title: "Roadmap", RequestedLocator: "roadmap", AssignedLocator: "roadmap-01kz7x", LocatorRepairRequired: true}},
		{operation.StageCreateV1, operation.StageCreateOutput{ID: stage, MatterID: matter, Locator: "planning", Title: "Planning", SortKey: 2000, State: "planned"}},
		{operation.StepCreateV2, operation.StepCreateOutput{ID: stepA, ParentID: stage, MatterID: matter, Locator: "step-02", Title: "First", SortKey: 1000, State: "planned"}},
		{operation.StepInsertV1, operation.StepCreateOutput{ID: stepB, ParentID: stage, MatterID: matter, Locator: "step-03", Title: "Inserted", SortKey: 1500, State: "planned"}},
		{operation.StepReorderV1, operation.StepReorderOutput{ParentID: stage, Order: []string{stepB, stepA}}},
		{operation.StepReplaceV1, operation.StepReplaceOutput{RemovedStepID: stepA, Replacement: operation.StepCreateOutput{ID: stepB, ParentID: stage, MatterID: matter, Locator: "step-04", Title: "Replacement", SortKey: 2000, State: "planned"}}},
		{operation.StepRemoveV1, operation.StepRemoveOutput{StepID: stepA}},
		{operation.MatterLocatorRepairV1, operation.MatterLocatorRepairOutput{ID: matter, Action: "rename", RequestedLocator: "roadmap", PreviousLocator: "roadmap-01kz7x", AssignedLocator: "roadmap-renamed"}},
	}
	for _, test := range cases {
		t.Run(test.definition.Metadata().Operation.String(), func(t *testing.T) {
			id := test.definition.Metadata().Operation
			want := operation.Result{Code: operation.ResultSucceeded, Output: test.output}
			encoded, err := encodeOperationResultPayload(id, want)
			if err != nil {
				t.Fatalf("encode result: %v", err)
			}
			got, err := decodeOperationResultPayload(id, encoded)
			if err != nil || !reflect.DeepEqual(got, want) {
				t.Fatalf("round trip = %+v, %v; want %+v", got, err, want)
			}
		})
	}

	entry := wipdjournal.Entry{Command: operation.Command{
		Request: operation.Request{
			Operation: operation.MatterCreateV2.Metadata().Operation,
			Input:     operation.MatterCreateInput{Title: "Roadmap", Locator: "roadmap"},
		},
	}}
	encoded, err := encodeStep4Output(operation.MatterCreateV2.Metadata().Operation, cases[0].output)
	if err != nil {
		t.Fatal(err)
	}
	if err = validateStep4Receipt(entry, encoded, 2); err != nil {
		t.Fatalf("valid collision-repair receipt: %v", err)
	}
	if err = validateStep4Receipt(entry, encoded, 1); err == nil {
		t.Fatal("accepted locator-repair output with a one-event range")
	}
}

func TestStep4RejectedAndRefusedTerminalReceiptsDecodeWithTypedContract(t *testing.T) {
	const (
		matter = "01KZ7XHAQT1S46NYPN1PW1DX3A"
		stage  = "01KZ7XHAQT1S46NYPN1PW1DX3B"
		step   = "01KZ7XHAQT1S46NYPN1PW1DX3C"
		claim  = "01KZ7XHAQT1S46NYPN1PW1DX3D"
	)
	cases := []struct {
		name       string
		definition operation.Definition
		input      operation.Input
		code       operation.ResultCode
		problem    string
	}{
		{"absent Step parent", operation.StepCreateV2, operation.StepCreateInput{ParentID: step, Title: "Child"}, operation.ResultRejected, "not-found.step-parent"},
		{"empty Stage locator", operation.StageCreateV1, operation.StageCreateInput{MatterID: matter, Title: "!!!"}, operation.ResultRejected, "validation.empty-locator"},
		{"incomplete reorder", operation.StepReorderV1, operation.StepReorderInput{ParentID: stage, Order: []string{step}}, operation.ResultRejected, "validation.incomplete-order"},
		{"absent Step replacement", operation.StepReplaceV1, operation.StepReplaceInput{StepID: step, Title: "Replacement"}, operation.ResultRejected, "not-found.step"},
		{"absent Step removal", operation.StepRemoveV1, operation.StepRemoveInput{StepID: step, Reason: "obsolete"}, operation.ResultRejected, "not-found.step"},
		{"locator conflict refusal", operation.StageCreateV1, operation.StageCreateInput{MatterID: matter, Title: "Planning"}, operation.ResultRefused, "refusal.locator-conflict"},
	}
	for index, test := range cases {
		t.Run(test.name, func(t *testing.T) {
			command := commandStartCanonicalCommand(commandStartCommandPrefix+strconv.Itoa(70+index), uint64(index+1), "",
				commandStartCommandPrefix+strconv.Itoa(70+index), test.definition.Metadata().Operation, test.input,
				&operation.ClaimContext{ID: claim, Epoch: "1"})
			command.Request.Context.Clone = commandStartCommandPrefix + "47"
			command.Request.Context.Worktree = commandStartCommandPrefix + "48"
			entry := wipdjournal.Entry{
				Command: command, RequestHash: mustCommandStartHash(t, command), EnvironmentSeq: command.EnvironmentSequence,
				Delivery: operation.DeliveryClaim, State: wipdjournal.StateReturned,
			}
			receipt, err := encodeCommandStartProblemReceipt(entry, test.code, test.problem)
			if err != nil {
				t.Fatal(err)
			}
			got, err := commandReceiptResult(entry, receipt)
			if err != nil || got.Code != test.code || got.Problem == nil || got.Problem.Code != operation.ProblemCode(test.problem) {
				t.Fatalf("typed terminal result=%+v err=%v; want %s/%s", got, err, test.code, test.problem)
			}
		})
	}

	command := commandStartCanonicalCommand(commandStartCommandPrefix+"79", 9, "", commandStartCommandPrefix+"79",
		operation.StageCreateV1.Metadata().Operation, operation.StageCreateInput{MatterID: matter, Title: "!!!"},
		&operation.ClaimContext{ID: claim, Epoch: "1"})
	command.Request.Context.Clone = commandStartCommandPrefix + "47"
	command.Request.Context.Worktree = commandStartCommandPrefix + "48"
	entry := wipdjournal.Entry{
		Command: command, RequestHash: mustCommandStartHash(t, command), EnvironmentSeq: command.EnvironmentSequence,
		Delivery: operation.DeliveryClaim, State: wipdjournal.StateReturned,
	}
	wrongDisposition, err := encodeCommandStartProblemReceipt(entry, operation.ResultRefused, "validation.empty-locator")
	if err != nil {
		t.Fatal(err)
	}
	if _, err = commandReceiptResult(entry, wrongDisposition); err == nil {
		t.Fatal("typed client accepted validation.* in a refusal receipt")
	}
}

func encodeCommandStartProblemReceipt(entry wipdjournal.Entry, code operation.ResultCode, problem string) ([]byte, error) {
	return wipdwire.EncodeCanonical(map[string]any{
		"schema": "wipd.terminal-receipt/1", "domain_id": entry.Command.AuthorityDomainID,
		"authority_epoch": entry.Command.ExpectedAuthorityEpoch, "identity_schema": "wipd.command/1",
		"command_id": entry.Command.ID, "request_hash": entry.RequestHash,
		"operation":       map[string]any{"name": entry.Command.Request.Operation.Name, "version": uint64(entry.Command.Request.Operation.Version)},
		"environment":     map[string]any{"id": entry.Command.EnvironmentID, "sequence": entry.EnvironmentSeq},
		"result":          map[string]any{"code": string(code), "output": nil, "problem_code": problem},
		"accepted_events": nil,
	})
}
