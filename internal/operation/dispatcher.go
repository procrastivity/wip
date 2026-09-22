package operation

import (
	"context"
	"errors"
	"fmt"
	"sync"
)

// Registration pairs one complete operation definition with the handler that
// implements it. The handler receives only a semantic Request; transport and
// storage adapters remain outside this package.
type Registration struct {
	Definition Definition
	Handler    Handler
}

// Registry resolves versioned operation identities to registered handlers.
// Definitions are registered before dispatch and may be resolved concurrently
// with other dispatches.
type Registry struct {
	mu            sync.RWMutex
	registrations map[ID]Registration
}

// NewRegistry returns an empty operation registry. Catalogue definitions are
// not implicitly registered because a definition has no handler or storage
// owner until an adopter supplies one.
func NewRegistry() *Registry {
	return &Registry{registrations: make(map[ID]Registration)}
}

// Register adds one operation handler. Invalid definitions and nil handlers
// are rejected before the registry changes; duplicate versioned identities
// are also rejected.
func (r *Registry) Register(definition Definition, handler Handler) error {
	if r == nil {
		return fmt.Errorf("operation: cannot register on a nil registry")
	}
	if err := validateDefinition(definition); err != nil {
		return fmt.Errorf("operation: invalid definition: %w", err)
	}
	if handler == nil {
		return fmt.Errorf("operation: cannot register %s with a nil handler", definition.metadata.Operation)
	}

	r.mu.Lock()
	defer r.mu.Unlock()
	if r.registrations == nil {
		r.registrations = make(map[ID]Registration)
	}
	if _, exists := r.registrations[definition.metadata.Operation]; exists {
		return fmt.Errorf("operation: duplicate registration for %s", definition.metadata.Operation)
	}
	r.registrations[definition.metadata.Operation] = Registration{
		Definition: definition,
		Handler:    handler,
	}
	return nil
}

// Resolve returns the definition and handler registered for id. A known name
// with a different version is reported separately from an unknown operation so
// callers can preserve the contract's stable problem codes.
func (r *Registry) Resolve(id ID) (Registration, error) {
	if err := id.Validate(); err != nil {
		return Registration{}, &ResolutionError{
			Operation: id,
			Code:      ProblemInvalidRequest,
			Message:   err.Error(),
		}
	}
	if r == nil {
		return Registration{}, unknownOperation(id)
	}

	r.mu.RLock()
	registration, ok := r.registrations[id]
	if ok {
		r.mu.RUnlock()
		return registration, nil
	}
	knownName := false
	for registeredID := range r.registrations {
		if registeredID.Name == id.Name {
			knownName = true
			break
		}
	}
	r.mu.RUnlock()

	if knownName {
		return Registration{}, &ResolutionError{
			Operation: id,
			Code:      ProblemUnsupportedVersion,
			Message:   fmt.Sprintf("operation %s is not registered", id),
		}
	}
	return Registration{}, unknownOperation(id)
}

// Dispatch resolves request.Operation, validates the semantic request, invokes
// its handler, and validates the returned semantic result. Boundary failures
// are represented as stable semantic results instead of leaking registry or
// validation errors into a transport adapter.
func (r *Registry) Dispatch(ctx context.Context, request Request) Result {
	registration, err := r.Resolve(request.Operation)
	if err != nil {
		var resolutionErr *ResolutionError
		if !errors.As(err, &resolutionErr) {
			return failedResult(ProblemInvalidRequest, err.Error())
		}
		return rejectedResult(resolutionErr.Code, resolutionErr.Error())
	}

	if err := registration.Definition.ValidateRequest(request); err != nil {
		return rejectedResult(ProblemInvalidRequest, err.Error())
	}

	result := registration.Handler(ctx, request)
	if err := registration.Definition.ValidateResult(result); err != nil {
		return failedResult(ProblemInvalidResult, err.Error())
	}
	return result
}

// ResolutionError reports why a requested operation identity could not be
// resolved. Code is one of the stable operation.* boundary problem codes.
type ResolutionError struct {
	Operation ID
	Code      ProblemCode
	Message   string
}

func (e *ResolutionError) Error() string {
	if e == nil {
		return "operation: unknown resolution error"
	}
	return e.Message
}

func unknownOperation(id ID) error {
	return &ResolutionError{
		Operation: id,
		Code:      ProblemUnknownOperation,
		Message:   fmt.Sprintf("operation %s is not registered", id),
	}
}

func validateDefinition(definition Definition) error {
	if err := ValidateMetadata(definition.metadata); err != nil {
		return err
	}
	if definition.inputType == nil || definition.outputType == nil {
		return fmt.Errorf("%s has no typed input or output", definition.metadata.Operation)
	}
	if err := validateTransportType(definition.inputType, "input"); err != nil {
		return err
	}
	if err := validateTransportType(definition.outputType, "output"); err != nil {
		return err
	}
	return nil
}

func rejectedResult(code ProblemCode, message string) Result {
	return Result{
		Code: ResultRejected,
		Problem: &Problem{
			Code:    code,
			Message: message,
		},
	}
}

func failedResult(code ProblemCode, message string) Result {
	return Result{
		Code: ResultFailed,
		Problem: &Problem{
			Code:    code,
			Message: message,
		},
	}
}
