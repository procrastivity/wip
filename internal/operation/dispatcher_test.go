package operation

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"testing"
)

func TestRegistryRegistersAndResolvesVersionedOperation(t *testing.T) {
	registry := NewRegistry()
	handler := func(context.Context, Request) Result {
		return Result{Code: ResultSucceeded, Output: MatterCreateOutput{ID: "01MATTER"}}
	}
	if err := registry.Register(MatterCreateV1, handler); err != nil {
		t.Fatalf("Register() error = %v", err)
	}

	registration, err := registry.Resolve(MatterCreateV1.Metadata().Operation)
	if err != nil {
		t.Fatalf("Resolve() error = %v", err)
	}
	if got := registration.Definition.Metadata().Operation; got != MatterCreateV1.Metadata().Operation {
		t.Fatalf("resolved operation = %s, want %s", got, MatterCreateV1.Metadata().Operation)
	}
	if registration.Handler == nil {
		t.Fatal("resolved registration has no handler")
	}

	_, err = registry.Resolve(ID{Name: "matter.remove", Version: 1})
	assertResolutionCode(t, err, ProblemUnknownOperation)
	_, err = registry.Resolve(ID{Name: "matter.create", Version: 2})
	assertResolutionCode(t, err, ProblemUnsupportedVersion)
}

func TestRegistryRejectsInvalidAndDuplicateRegistrations(t *testing.T) {
	registry := NewRegistry()
	handler := func(context.Context, Request) Result { return Result{} }

	if err := registry.Register(MatterCreateV1, nil); err == nil || !strings.Contains(err.Error(), "nil handler") {
		t.Fatalf("nil handler registration error = %v", err)
	}
	if err := registry.Register(Definition{}, handler); err == nil || !strings.Contains(err.Error(), "invalid definition") {
		t.Fatalf("zero definition registration error = %v", err)
	}
	if err := registry.Register(MatterCreateV1, handler); err != nil {
		t.Fatalf("first registration error = %v", err)
	}
	if err := registry.Register(MatterCreateV1, handler); err == nil || !strings.Contains(err.Error(), "duplicate registration") {
		t.Fatalf("duplicate registration error = %v", err)
	}
}

func TestDispatchValidatesAndInvokesRegisteredHandler(t *testing.T) {
	registry := NewRegistry()
	ctx := context.WithValue(context.Background(), executionContextKey{}, "execution-only")
	request := canonicalMatterCreateRequest()
	var gotContext context.Context
	var gotRequest Request
	if err := registry.Register(MatterCreateV1, func(handlerContext context.Context, handlerRequest Request) Result {
		gotContext = handlerContext
		gotRequest = handlerRequest
		return Result{
			Code: ResultSucceeded,
			Output: MatterCreateOutput{
				ID: "01MATTER", Locator: request.Input.(MatterCreateInput).Locator, Title: "Created",
			},
		}
	}); err != nil {
		t.Fatalf("Register() error = %v", err)
	}

	result := registry.Dispatch(ctx, request)
	if err := MatterCreateV1.ValidateResult(result); err != nil {
		t.Fatalf("Dispatch() result is invalid: %v", err)
	}
	if result.Code != ResultSucceeded {
		t.Fatalf("Dispatch() code = %s, want %s", result.Code, ResultSucceeded)
	}
	if gotContext != ctx {
		t.Fatal("handler did not receive the execution context")
	}
	if !reflect.DeepEqual(gotRequest, request) {
		t.Fatalf("handler request = %+v, want %+v", gotRequest, request)
	}
}

func TestDispatchPreservesAsymmetricTypedFixtureSemantics(t *testing.T) {
	registry := NewRegistry()
	request := canonicalMatterCreateRequest()
	request.Input = MatterCreateInput{Title: "M4 Fixture", Locator: "fixture-17"}
	want := MatterCreateOutput{ID: "01M4FIXTURE0000000000000001", Locator: "fixture-17", Title: "M4 Fixture"}
	if err := registry.Register(MatterCreateV1, func(ctx context.Context, got Request) Result {
		if ctx.Value(executionContextKey{}) != "fixture-context" {
			t.Errorf("handler context value = %v, want fixture-context", ctx.Value(executionContextKey{}))
		}
		if got.Input != request.Input {
			t.Errorf("handler input = %#v, want %#v", got.Input, request.Input)
		}
		return Result{Code: ResultSucceeded, Output: want}
	}); err != nil {
		t.Fatalf("Register() error = %v", err)
	}

	result := registry.Dispatch(context.WithValue(context.Background(), executionContextKey{}, "fixture-context"), request)
	if result.Code != ResultSucceeded || result.Problem != nil {
		t.Fatalf("Dispatch() result = %+v, want typed success", result)
	}
	got, ok := result.Output.(MatterCreateOutput)
	if !ok || got != want {
		t.Fatalf("Dispatch() output = %#v, want typed asymmetric output %#v", result.Output, want)
	}
}

func TestDispatchRejectsUnknownVersionAndInvalidRequestBeforeHandler(t *testing.T) {
	registry := NewRegistry()
	called := 0
	if err := registry.Register(MatterCreateV1, func(context.Context, Request) Result {
		called++
		return Result{Code: ResultSucceeded, Output: MatterCreateOutput{ID: "01MATTER"}}
	}); err != nil {
		t.Fatalf("Register() error = %v", err)
	}

	unknown := registry.Dispatch(context.Background(), Request{
		Operation: ID{Name: "matter.remove", Version: 1},
	})
	assertResultProblem(t, unknown, ResultRejected, ProblemUnknownOperation)

	wrongVersion := canonicalMatterCreateRequest()
	wrongVersion.Operation.Version = 2
	result := registry.Dispatch(context.Background(), wrongVersion)
	assertResultProblem(t, result, ResultRejected, ProblemUnsupportedVersion)

	invalidRequest := canonicalMatterCreateRequest()
	invalidRequest.Actor = ""
	result = registry.Dispatch(context.Background(), invalidRequest)
	assertResultProblem(t, result, ResultRejected, ProblemInvalidRequest)
	if called != 0 {
		t.Fatalf("handler called %d times for rejected requests, want 0", called)
	}
}

func TestDispatchRejectsInvalidHandlerResult(t *testing.T) {
	registry := NewRegistry()
	if err := registry.Register(MatterCreateV1, func(context.Context, Request) Result {
		return Result{Code: ResultSucceeded, Output: otherOutput{}}
	}); err != nil {
		t.Fatalf("Register() error = %v", err)
	}

	result := registry.Dispatch(context.Background(), canonicalMatterCreateRequest())
	assertResultProblem(t, result, ResultFailed, ProblemInvalidResult)
	if err := MatterCreateV1.ValidateResult(result); err != nil {
		t.Fatalf("dispatcher failure result is invalid: %v", err)
	}
}

func assertResolutionCode(t *testing.T, err error, want ProblemCode) {
	t.Helper()
	if err == nil {
		t.Fatalf("Resolve() error = nil, want %s", want)
	}
	var resolutionErr *ResolutionError
	if !errors.As(err, &resolutionErr) {
		t.Fatalf("Resolve() error = %T %v, want ResolutionError", err, err)
	}
	if resolutionErr.Code != want {
		t.Fatalf("resolution code = %s, want %s", resolutionErr.Code, want)
	}
}

func assertResultProblem(t *testing.T, result Result, wantResult ResultCode, wantProblem ProblemCode) {
	t.Helper()
	if result.Code != wantResult {
		t.Fatalf("result code = %s, want %s", result.Code, wantResult)
	}
	if result.Problem == nil {
		t.Fatalf("result problem = nil, want %s", wantProblem)
	}
	if result.Problem.Code != wantProblem {
		t.Fatalf("result problem code = %s, want %s", result.Problem.Code, wantProblem)
	}
}

func canonicalMatterCreateRequest() Request {
	return Request{
		Operation: MatterCreateV1.Metadata().Operation,
		Actor:     "role:builder",
		Context:   Context{Repo: "01REPO"},
		Input:     MatterCreateInput{Title: "Ship operation boundary", Locator: "operation-boundary"},
		Blobs:     []BlobInput{},
	}
}

type otherOutput struct{}

func (otherOutput) operationOutput() {}

type executionContextKey struct{}
