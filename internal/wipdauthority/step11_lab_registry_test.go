package wipdauthority

import (
	"context"
	"errors"
	"testing"

	"github.com/procrastivity/wip/internal/operation"
)

func TestM6LabServerAcceptsOnlyCompleteStep11Catalogue(t *testing.T) {
	fixture := newM5CommandFixture(t)
	complete, err := NewM6Step11Registry()
	if err != nil {
		t.Fatal(err)
	}
	config := fixture.config
	config.Registry = complete
	config.NamedBatchRead = true
	if _, err = NewM6LabServer(fixture.profile, fixture.serverCert, config); err != nil {
		t.Fatalf("accept complete fail-closed Step 11 registry: %v", err)
	}
	if _, err = NewM5LabServer(fixture.profile, fixture.serverCert, config); !errors.Is(err, ErrInvalidLabConfig) {
		t.Fatalf("M5 lab accepted the Step 11 registry: %v", err)
	}

	partial, err := NewM6Step10Registry()
	if err != nil {
		t.Fatal(err)
	}
	step11 := operation.Step11Catalogue()
	for _, definition := range step11[:len(step11)-1] {
		if err = partial.Register(definition, failClosedStep11Handler); err != nil {
			t.Fatalf("register partial Step 11 test definition %s: %v", definition.Metadata().Operation, err)
		}
	}
	config.Registry = partial
	if _, err = NewM6LabServer(fixture.profile, fixture.serverCert, config); !errors.Is(err, ErrInvalidLabConfig) {
		t.Fatalf("M6 lab accepted a partial Step 11 set: %v", err)
	}

	withUnknown, err := NewM6Step11Registry()
	if err != nil {
		t.Fatal(err)
	}
	if err = withUnknown.Register(operation.GateExemptionRepairV1, failClosedStep11Handler); err != nil {
		t.Fatalf("register unknown-to-M6 operation for negative control: %v", err)
	}
	config.Registry = withUnknown
	if _, err = NewM6LabServer(fixture.profile, fixture.serverCert, config); !errors.Is(err, ErrInvalidLabConfig) {
		t.Fatalf("M6 lab accepted an operation outside its closed allowlist: %v", err)
	}
}

func failClosedStep11Handler(context.Context, operation.Request) operation.Result {
	return operation.Result{Code: operation.ResultFailed, Problem: &operation.Problem{
		Code: operation.ProblemExecutionFailed, Message: "Step 11 route operation requires its authenticated route coordinator",
	}}
}
