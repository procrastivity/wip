package wipd

import (
	"reflect"
	"testing"

	"github.com/procrastivity/wip/internal/operation"
	"github.com/procrastivity/wip/internal/wipdjournal"
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
