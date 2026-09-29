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
		return nil, err
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
		return nil, err
	}
	for _, definition := range []operation.Definition{
		operation.StepStartV1, operation.StepFinishV1, operation.MatterFinishV1,
	} {
		if err := registry.Register(definition, func(context.Context, operation.Request) operation.Result {
			return operation.Result{Code: operation.ResultFailed, Problem: &operation.Problem{
				Code: operation.ProblemExecutionFailed, Message: "lifecycle operation requires an authority transaction",
			}}
		}); err != nil {
			return nil, err
		}
	}
	return registry, nil
}
