package operation

import (
	"fmt"
	"reflect"
	"regexp"
	"strings"
)

var locatorPattern = regexp.MustCompile(`^[a-z0-9]+(?:-[a-z0-9]+)*$`)

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
	case MatterCreateInput:
		if d.metadata.Operation != MatterCreateV2.Metadata().Operation {
			break
		}
		if strings.TrimSpace(input.Title) == "" {
			return fmt.Errorf("matter title is empty")
		}
		if input.Locator != "" && !locatorPattern.MatchString(input.Locator) {
			return fmt.Errorf("requested Matter locator is not canonical")
		}
	case StageCreateInput:
		if err := validateULID("Matter ID", input.MatterID); err != nil {
			return err
		}
		if strings.TrimSpace(input.Title) == "" {
			return fmt.Errorf("stage title is empty")
		}
	case StepCreateInput:
		if d.metadata.Operation == StepCreateV1.Metadata().Operation {
			break
		}
		if err := validateULID("Step parent ID", input.ParentID); err != nil {
			return err
		}
		if strings.TrimSpace(input.Title) == "" {
			return fmt.Errorf("step title is empty")
		}
	case StepInsertInput:
		if err := validateULID("Step parent ID", input.ParentID); err != nil {
			return err
		}
		if strings.TrimSpace(input.Title) == "" || input.AfterID != "" && input.BeforeID != "" {
			return fmt.Errorf("step insertion requires a title and at most one anchor")
		}
		if input.AfterID != "" {
			if err := validateULID("after Step ID", input.AfterID); err != nil {
				return err
			}
		}
		if input.BeforeID != "" {
			if err := validateULID("before Step ID", input.BeforeID); err != nil {
				return err
			}
		}
	case StepReorderInput:
		if err := validateULID("Step parent ID", input.ParentID); err != nil {
			return err
		}
		if len(input.Order) == 0 {
			return fmt.Errorf("step order is empty")
		}
		for _, id := range input.Order {
			if err := validateULID("ordered Step ID", id); err != nil {
				return err
			}
		}
	case StepReplaceInput:
		if err := validateULID("Step ID", input.StepID); err != nil {
			return err
		}
		if strings.TrimSpace(input.Title) == "" {
			return fmt.Errorf("replacement Step title is empty")
		}
	case StepRemoveInput:
		if err := validateULID("Step ID", input.StepID); err != nil {
			return err
		}
		if strings.TrimSpace(input.Reason) == "" {
			return fmt.Errorf("step removal reason is empty")
		}
	case MatterLocatorRepairInput:
		if err := validateULID("Matter ID", input.MatterID); err != nil {
			return err
		}
		if !locatorPattern.MatchString(input.AssignedLocator) || (input.Action != "rename" && input.Action != "accept") {
			return fmt.Errorf("assigned Matter locator is not canonical")
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
		case MatterCreateV2Output:
			if validateULID("Matter output ID", output.ID) != nil || output.Title == "" ||
				!locatorPattern.MatchString(output.RequestedLocator) || !locatorPattern.MatchString(output.AssignedLocator) ||
				output.LocatorRepairRequired != (output.RequestedLocator != output.AssignedLocator) {
				return fmt.Errorf("matter v2 output has invalid identity or locator")
			}
		case StageCreateOutput:
			if validateULID("Stage output ID", output.ID) != nil || validateULID("Matter output ID", output.MatterID) != nil ||
				!locatorPattern.MatchString(output.Locator) || output.Title == "" || output.SortKey <= 0 || output.State != "planned" {
				return fmt.Errorf("stage output has invalid identity, locator, or order")
			}
		case StepCreateOutput:
			if d.metadata.Operation == StepCreateV1.Metadata().Operation {
				if output.ID == "" && output.MatterID == "" && output.Locator == "" && output.SortKey == 0 && output.State == "" {
					if validateULID("Step parent output ID", output.ParentID) != nil || strings.TrimSpace(output.Title) == "" {
						return fmt.Errorf("step.create@v1 provisional result has invalid parent or title")
					}
					break
				}
			}
			if validateULID("Step output ID", output.ID) != nil || validateULID("Step parent output ID", output.ParentID) != nil ||
				validateULID("Matter output ID", output.MatterID) != nil || !locatorPattern.MatchString(output.Locator) ||
				output.Title == "" || output.SortKey <= 0 || output.State != "planned" {
				return fmt.Errorf("step output has invalid identity, locator, or order")
			}
		case StepReorderOutput:
			if validateULID("Step parent output ID", output.ParentID) != nil || len(output.Order) == 0 {
				return fmt.Errorf("step reorder output has invalid parent or order")
			}
			for _, id := range output.Order {
				if validateULID("ordered Step output ID", id) != nil {
					return fmt.Errorf("step reorder output has invalid step identity")
				}
			}
		case StepReplaceOutput:
			replacement := output.Replacement
			if validateULID("removed Step output ID", output.RemovedStepID) != nil || validateULID("replacement Step output ID", replacement.ID) != nil ||
				replacement.ID == output.RemovedStepID || validateULID("replacement parent output ID", replacement.ParentID) != nil ||
				validateULID("replacement Matter output ID", replacement.MatterID) != nil || !locatorPattern.MatchString(replacement.Locator) ||
				replacement.Title == "" || replacement.SortKey <= 0 || replacement.State != "planned" {
				return fmt.Errorf("step replacement output has invalid identity")
			}
		case StepRemoveOutput:
			if validateULID("removed Step output ID", output.StepID) != nil {
				return fmt.Errorf("step removal output has invalid identity")
			}
		case MatterLocatorRepairOutput:
			if validateULID("Matter output ID", output.ID) != nil || (output.Action != "rename" && output.Action != "accept") ||
				!locatorPattern.MatchString(output.RequestedLocator) || !locatorPattern.MatchString(output.PreviousLocator) ||
				!locatorPattern.MatchString(output.AssignedLocator) {
				return fmt.Errorf("matter locator repair output is invalid")
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
