// Package operation defines wip's transport-neutral semantic operation
// boundary and its in-process dispatcher. It deliberately contains no
// transport, store handle, terminal stream, or process-global environment
// lookup.
package operation

import "context"

// ID identifies one version of a semantic operation. Name describes the
// behavior; Version changes only when that behavior's semantic contract is
// incompatible. How the pair is encoded on a wire belongs to the protocol.
type ID struct {
	Name    string
	Version uint16
}

// Actor is the authenticated semantic actor token. It preserves the current
// human, role:<name>, and system:<source> vocabulary without importing store.
type Actor string

// Context names the already-resolved tier identities against which an
// operation runs. Transport adapters resolve ambient checkout state before
// constructing a Request; handlers never call os.Getwd or read environment
// variables to discover these values.
type Context struct {
	Repo     string
	Clone    string
	Worktree string
}

// ClaimContext is the exact claim proof supplied to an operation whose static
// metadata requires one. Its eventual authenticated representation is a
// protocol concern, not part of this semantic contract.
type ClaimContext struct {
	ID    string
	Epoch string
}

// BlobInput names staged content by digest rather than by an arbitrary local
// path or open stream. M2 owns digest algorithms and transfer encoding.
type BlobInput struct {
	Name   string
	Digest string
	Size   int64
}

// Input is closed to this package so every operation input can be checked for
// transport coupling when its Definition is built.
type Input interface {
	operationInput()
}

// Output is the corresponding closed result payload set.
type Output interface {
	operationOutput()
}

// Request is the complete semantic input to a handler. context.Context stays
// outside this value: cancellation is an in-process execution concern, not a
// semantic argument or future protocol field.
type Request struct {
	Operation ID
	Actor     Actor
	Context   Context
	Claim     *ClaimContext
	Input     Input
	Blobs     []BlobInput
}

// Handler is the Step 3 adoption seam. Implementations receive only semantic
// data here; dispatcher composition may own execution dependencies without
// putting Cobra commands, streams, environment readers, or database handles
// into Request.
type Handler func(context.Context, Request) Result

// ResultCode is the stable semantic disposition of one handled request.
// Transport availability and outcome uncertainty are deliberately absent;
// M2 owns those protocol outcomes.
type ResultCode string

// ResultSucceeded and the other result constants are the closed semantic
// disposition vocabulary.
const (
	ResultSucceeded ResultCode = "result.succeeded"
	ResultRejected  ResultCode = "result.rejected"
	ResultRefused   ResultCode = "result.refused"
	ResultFailed    ResultCode = "result.failed"
)

// ProblemCode is a stable machine-readable reason. Message is presentation
// text and may improve without changing callers' branching behavior.
type ProblemCode string

// ProblemInvalidRequest and the other operation.* constants describe boundary
// failures before semantic execution.
const (
	ProblemInvalidRequest     ProblemCode = "operation.invalid-request"
	ProblemUnknownOperation   ProblemCode = "operation.unknown"
	ProblemUnsupportedVersion ProblemCode = "operation.unsupported-version"
	ProblemInvalidResult      ProblemCode = "internal.invalid-result"
	ProblemExecutionFailed    ProblemCode = "internal.execution-failed"
)

// ProblemInvalidTitle and the other matter.create constants retain the current
// CLI vocabulary so Step 3 can map without changing behavior.
const (
	ProblemInvalidTitle     ProblemCode = "validation.invalid-title"
	ProblemInvalidLocator   ProblemCode = "validation.invalid-locator"
	ProblemLocatorCollision ProblemCode = "validation.locator-collision"
	ProblemUnknownClone     ProblemCode = "refusal.unknown-clone"
)

// Problem carries one coded semantic failure.
type Problem struct {
	Code    ProblemCode
	Message string
}

// Result contains either one typed output or one coded problem. ValidateResult
// enforces the legal combinations for a particular Definition.
type Result struct {
	Code    ResultCode
	Output  Output
	Problem *Problem
}
