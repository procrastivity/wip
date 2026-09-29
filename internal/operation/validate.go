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
	if d.metadata.Claim == ClaimExact || d.metadata.Claim == ClaimImplicitBirth {
		if request.Claim == nil || request.Claim.ID == "" || request.Claim.Epoch == "" {
			return fmt.Errorf("operation requires an exact claim ID and epoch")
		}
	} else if request.Claim != nil {
		return fmt.Errorf("operation does not accept claim context")
	}
	if request.Input == nil || reflect.TypeOf(request.Input) != d.inputType {
		return fmt.Errorf("input type is %T, want %s", request.Input, d.inputType)
	}
	if d.metadata.Claim == ClaimImplicitBirth {
		input := request.Input.(StepCreateInput)
		if !ulidPattern.MatchString(input.ParentID) || request.Claim.ID != input.ParentID || request.Claim.Epoch != "1" {
			return fmt.Errorf("step.create@v1 requires the parent Matter's implicit birth claim at epoch 1")
		}
	}
	switch input := request.Input.(type) {
	case StepLifecycleInput:
		if err := validateULID("Step ID", input.StepID); err != nil {
			return err
		}
	case MatterFinishInput:
		if err := validateULID("Matter ID", input.MatterID); err != nil {
			return err
		}
	case ContentWriteInput:
		if err := validateULID("content subject ID", input.SubjectID); err != nil {
			return err
		}
		if input.Kind != "brief" && input.Kind != "workplan" && input.Kind != "body" {
			return fmt.Errorf("content.write-once@v1 requires brief, workplan, or body kind")
		}
	case FindingAppendInput:
		if err := validateULID("finding subject ID", input.SubjectID); err != nil {
			return err
		}
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
		switch output := result.Output.(type) {
		case StepLifecycleOutput:
			if validateULID("Step output ID", output.StepID) != nil || validateULID("Matter output ID", output.MatterID) != nil ||
				(output.State != "in-progress" && output.State != "done") {
				return fmt.Errorf("step lifecycle output has invalid identity or state")
			}
		case MatterFinishOutput:
			if validateULID("Matter output ID", output.MatterID) != nil || output.State != "done" {
				return fmt.Errorf("matter finish output has invalid identity or state")
			}
		case ContentSegmentOutput:
			if validateULID("content output ID", output.ID) != nil || validateULID("content subject ID", output.SubjectID) != nil ||
				(output.Kind != "brief" && output.Kind != "workplan" && output.Kind != "body" && output.Kind != "findings") ||
				!digestPattern.MatchString(output.BlobDigest) || output.ByteLength < 0 ||
				d.metadata.Operation == ContentWriteOnceV1.Metadata().Operation && output.Kind == "findings" ||
				d.metadata.Operation == FindingAppendV1.Metadata().Operation && output.Kind != "findings" {
				return fmt.Errorf("content output has invalid identity, kind, or staged-blob metadata")
			}
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
