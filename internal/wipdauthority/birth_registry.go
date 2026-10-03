package wipdauthority

import (
	"context"
	"time"

	"github.com/procrastivity/wip/internal/authoritystore"
	"github.com/procrastivity/wip/internal/operation"
)

// NewM5BirthRegistry returns the pure semantic handlers shared by the M5
// authority adapter and the connected daemon's negotiated capabilities.
// Authority IDs and projections are still committed only by CompleteCommand.
func NewM5BirthRegistry() (*operation.Registry, error) {
	registry := operation.NewRegistry()
	if err := registerM5BirthOperations(registry); err != nil {
		return nil, err
	}
	return registry, nil
}

// NewM6Step4Registry is a separate closed acceptance capability set: it keeps
// the complete M5 compatibility registry and adds only Step 4 definitions.
// Every M6 mutation is completed by one authority transaction, not by the
// transport-neutral placeholder handler registered here.
func NewM6Step4Registry() (*operation.Registry, error) {
	return newM6Registry(false)
}

// NewM6Step5Registry returns the full M6 acceptance capability set through
// Step 5, including the authority-bound lifecycle variants.
func NewM6Step5Registry() (*operation.Registry, error) {
	return newM6Registry(true)
}

// NewM6Step7Registry returns the explicit M6 capability set through the
// post-close anonymous Batch sweep operation. M5 and earlier M6 registries do
// not include this candidate.
func NewM6Step7Registry() (*operation.Registry, error) {
	registry, err := NewM6Step5Registry()
	if err != nil {
		return nil, err
	}
	if err = registry.Register(operation.BatchSweepAnonymousV1, func(context.Context, operation.Request) operation.Result {
		return operation.Result{Code: operation.ResultFailed, Problem: &operation.Problem{
			Code: operation.ProblemExecutionFailed, Message: "anonymous Batch sweep requires its authority transaction",
		}}
	}); err != nil {
		return nil, err
	}
	return registry, nil
}

func newM6Registry(step5 bool) (*operation.Registry, error) {
	registry, err := NewM5BirthRegistry()
	if err != nil {
		return nil, err
	}
	for _, definition := range operation.Step4Catalogue() {
		if err := registry.Register(definition, func(context.Context, operation.Request) operation.Result {
			return operation.Result{Code: operation.ResultFailed, Problem: &operation.Problem{
				Code: operation.ProblemExecutionFailed, Message: "Step 4 operation requires an authority transaction",
			}}
		}); err != nil {
			return nil, err
		}
	}
	if step5 {
		for _, definition := range operation.Step5Catalogue() {
			if err := registry.Register(definition, func(context.Context, operation.Request) operation.Result {
				return operation.Result{Code: operation.ResultFailed, Problem: &operation.Problem{
					Code: operation.ProblemExecutionFailed, Message: "Step 5 operation requires an authority transaction",
				}}
			}); err != nil {
				return nil, err
			}
		}
	}
	return registry, nil
}

func registerM5BirthOperations(registry *operation.Registry) error {
	if err := registry.Register(operation.MatterCreateV1, func(_ context.Context, request operation.Request) operation.Result {
		input, ok := request.Input.(operation.MatterCreateInput)
		if !ok {
			return operation.Result{Code: operation.ResultFailed, Problem: &operation.Problem{
				Code: operation.ProblemExecutionFailed, Message: "invalid matter.create input",
			}}
		}
		locator := input.Locator
		if locator == "" {
			locator = authoritystore.MatterLocator(input.Title)
		}
		id, err := randomULID(time.Now().UTC())
		if err != nil {
			return operation.Result{Code: operation.ResultFailed, Problem: &operation.Problem{
				Code: operation.ProblemExecutionFailed, Message: "could not allocate Matter identity",
			}}
		}
		return operation.Result{Code: operation.ResultSucceeded, Output: operation.MatterCreateOutput{
			ID: id, Locator: locator, Title: input.Title,
		}}
	}); err != nil {
		return err
	}
	if err := registry.Register(operation.StepCreateV1, func(_ context.Context, request operation.Request) operation.Result {
		input, ok := request.Input.(operation.StepCreateInput)
		if !ok {
			return operation.Result{Code: operation.ResultFailed, Problem: &operation.Problem{
				Code: operation.ProblemExecutionFailed, Message: "invalid step.create input",
			}}
		}
		return operation.Result{Code: operation.ResultSucceeded, Output: operation.StepCreateOutput{
			ParentID: input.ParentID, Title: input.Title,
		}}
	}); err != nil {
		return err
	}
	for _, definition := range []operation.Definition{
		operation.StepStartV1, operation.StepFinishV1, operation.MatterFinishV1,
	} {
		if err := registry.Register(definition, func(context.Context, operation.Request) operation.Result {
			return operation.Result{Code: operation.ResultFailed, Problem: &operation.Problem{
				Code: operation.ProblemExecutionFailed, Message: "lifecycle operation requires an authority transaction",
			}}
		}); err != nil {
			return err
		}
	}
	for _, definition := range []operation.Definition{operation.ContentWriteOnceV1, operation.FindingAppendV1} {
		if err := registry.Register(definition, func(_ context.Context, request operation.Request) operation.Result {
			var subject, kind string
			switch input := request.Input.(type) {
			case operation.ContentWriteInput:
				subject, kind = input.SubjectID, input.Kind
			case operation.FindingAppendInput:
				subject, kind = input.SubjectID, "findings"
			default:
				return operation.Result{Code: operation.ResultFailed, Problem: &operation.Problem{
					Code: operation.ProblemExecutionFailed, Message: "invalid content operation input",
				}}
			}
			id, err := randomULID(time.Now().UTC())
			if err != nil {
				return operation.Result{Code: operation.ResultFailed, Problem: &operation.Problem{
					Code: operation.ProblemExecutionFailed, Message: "could not allocate content identity",
				}}
			}
			blob := request.Blobs[0]
			return operation.Result{Code: operation.ResultSucceeded, Output: operation.ContentSegmentOutput{
				ID: id, SubjectID: subject, Kind: kind, BlobDigest: blob.Digest, ByteLength: blob.Size,
			}}
		}); err != nil {
			return err
		}
	}
	return nil
}
