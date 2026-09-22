package operation

import (
	"fmt"
	"reflect"
	"strings"
)

// ValidateRequest checks the envelope and typed payload against this
// Definition before a handler runs.
func (d Definition) ValidateRequest(request Request) error {
	if request.Operation != d.metadata.Operation {
		return fmt.Errorf("operation is %s, want %s", request.Operation, d.metadata.Operation)
	}
	if !validActor(request.Actor) {
		return fmt.Errorf("actor %q is not human, role:<name>, or system:<source>", request.Actor)
	}
	for _, dimension := range d.metadata.RequiredContext {
		var value string
		switch dimension {
		case ContextRepo:
			value = request.Context.Repo
		case ContextClone:
			value = request.Context.Clone
		case ContextWorktree:
			value = request.Context.Worktree
		}
		if value == "" {
			return fmt.Errorf("required %s context is empty", dimension)
		}
	}
	if d.metadata.Claim == ClaimExact {
		if request.Claim == nil || request.Claim.ID == "" || request.Claim.Epoch == "" {
			return fmt.Errorf("operation requires an exact claim ID and epoch")
		}
	} else if request.Claim != nil {
		return fmt.Errorf("operation does not accept claim context")
	}
	if request.Input == nil || reflect.TypeOf(request.Input) != d.inputType {
		return fmt.Errorf("input type is %T, want %s", request.Input, d.inputType)
	}
	return d.validateBlobs(request.Blobs)
}

func (d Definition) validateBlobs(blobs []BlobInput) error {
	specs := make(map[string]BlobSpec, len(d.metadata.BlobInputs))
	for _, spec := range d.metadata.BlobInputs {
		specs[spec.Name] = spec
	}
	seen := map[string]bool{}
	for _, blob := range blobs {
		if _, ok := specs[blob.Name]; !ok {
			return fmt.Errorf("blob input %q is not declared", blob.Name)
		}
		if seen[blob.Name] {
			return fmt.Errorf("blob input %q is duplicated", blob.Name)
		}
		if blob.Digest == "" {
			return fmt.Errorf("blob input %q has no digest", blob.Name)
		}
		if blob.Size < 0 {
			return fmt.Errorf("blob input %q has negative size", blob.Name)
		}
		seen[blob.Name] = true
	}
	for _, spec := range d.metadata.BlobInputs {
		if spec.Required && !seen[spec.Name] {
			return fmt.Errorf("required blob input %q is missing", spec.Name)
		}
	}
	return nil
}

// ValidateResult enforces the stable result/problem relationship and the
// output DTO belonging to this operation.
func (d Definition) ValidateResult(result Result) error {
	switch result.Code {
	case ResultSucceeded:
		if result.Problem != nil {
			return fmt.Errorf("a succeeded result cannot carry a problem")
		}
		if result.Output == nil || reflect.TypeOf(result.Output) != d.outputType {
			return fmt.Errorf("output type is %T, want %s", result.Output, d.outputType)
		}
		return nil
	case ResultRejected, ResultRefused, ResultFailed:
	default:
		return fmt.Errorf("result code %q is unknown", result.Code)
	}
	if result.Output != nil {
		return fmt.Errorf("a non-success result cannot carry output")
	}
	if result.Problem == nil {
		return fmt.Errorf("%s requires a problem", result.Code)
	}
	if result.Problem.Message == "" {
		return fmt.Errorf("problem %q has no message", result.Problem.Code)
	}
	prefix := problemPrefix(result.Problem.Code)
	switch result.Code {
	case ResultRejected:
		if prefix != "operation" && prefix != "validation" && prefix != "not-found" {
			return fmt.Errorf("rejected result cannot carry %q", result.Problem.Code)
		}
	case ResultRefused:
		if prefix != "refusal" {
			return fmt.Errorf("refused result requires a refusal.* problem, got %q", result.Problem.Code)
		}
	case ResultFailed:
		if prefix != "internal" {
			return fmt.Errorf("failed result requires an internal.* problem, got %q", result.Problem.Code)
		}
	}
	return nil
}

func problemPrefix(code ProblemCode) string {
	value := string(code)
	before, after, found := strings.Cut(value, ".")
	if !found || before == "" || after == "" || !tokenPattern.MatchString(value) {
		return ""
	}
	return before
}
