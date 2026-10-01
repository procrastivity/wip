package wipd

import (
	"reflect"
	"testing"

	"github.com/procrastivity/wip/internal/operation"
	"github.com/procrastivity/wip/internal/wipdjournal"
)

func TestStep5CancelResultCodecAndReceiptUseCanceledState(t *testing.T) {
	const (
		matterID = "01KZ7XHAQT1S46NYPN1PW1DX3A"
		stageID  = "01KZ7XHAQT1S46NYPN1PW1DX3B"
		stepID   = "01KZ7XHAQT1S46NYPN1PW1DX3C"
	)
	for _, test := range []struct {
		name       string
		definition operation.Definition
		input      operation.Input
		output     operation.Output
	}{
		{
			name:       "Matter",
			definition: operation.MatterCancelV1,
			input:      operation.NodeLifecycleInput{NodeID: matterID},
			output:     operation.MatterLifecycleOutput{MatterID: matterID, State: "canceled"},
		},
		{
			name:       "Stage",
			definition: operation.StageCancelV1,
			input:      operation.NodeLifecycleInput{NodeID: stageID},
			output:     operation.NodeLifecycleOutput{NodeID: stageID, MatterID: matterID, State: "canceled"},
		},
		{
			name:       "Step",
			definition: operation.StepCancelV1,
			input:      operation.StepCancelInput{StepID: stepID, Reason: "obsolete"},
			output:     operation.StepLifecycleOutput{StepID: stepID, MatterID: matterID, State: "canceled"},
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			id := test.definition.Metadata().Operation
			encoded, err := encodeStep5Output(id, test.output)
			if err != nil {
				t.Fatalf("encode authoritative canceled output: %v", err)
			}
			decoded, err := decodeStep5Output(id, encoded)
			if err != nil || !reflect.DeepEqual(decoded, test.output) {
				t.Fatalf("decode canceled output = %#v, %v; want %#v", decoded, err, test.output)
			}
			entry := wipdjournal.Entry{Command: operation.Command{Request: operation.Request{
				Operation: id,
				Input:     test.input,
			}}}
			if err = validateStep5Receipt(entry, encoded, 1); err != nil {
				t.Fatalf("validate one-event canceled receipt: %v", err)
			}
		})
	}
}
