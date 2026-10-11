package operation

import (
	"fmt"
	"strings"
)

// SchedulerStep10VerifierRevision names the one process-backed Verifier
// behavior admitted by the isolated M6 Step 10 profile.
const SchedulerStep10VerifierRevision = "builtin.step10-verifier@v1"

// SchedulerPrefixAnchor identifies the exact installed authority prefix used
// for one scheduler hook invocation.
type SchedulerPrefixAnchor struct {
	EventCount uint64  `cbor:"event_count"`
	EventID    *string `cbor:"event_id"`
	Digest     string  `cbor:"digest"`
}

// SchedulerHookInput binds one bounded adapter invocation to its immutable
// scheduler, role, claim, and installed-prefix context. Pinned bytes are named
// separately by Request.Blobs.
type SchedulerHookInput struct {
	Kind            string                `cbor:"kind"`
	Version         uint64                `cbor:"version"`
	AdapterID       string                `cbor:"adapter_id"`
	AdapterVersion  uint64                `cbor:"adapter_version"`
	RunID           string                `cbor:"run_id"`
	MatterID        string                `cbor:"matter_id"`
	TargetID        string                `cbor:"target_id"`
	TargetKind      string                `cbor:"target_kind"`
	Role            string                `cbor:"role"`
	RoleID          string                `cbor:"role_id"`
	DispatchID      string                `cbor:"dispatch_id"`
	Revision        string                `cbor:"revision"`
	Reason          string                `cbor:"reason"`
	Contention      string                `cbor:"contention"`
	Blocked         string                `cbor:"blocked"`
	Failed          string                `cbor:"failed"`
	TargetClaims    []TargetClaim         `cbor:"target_claims"`
	InstalledPrefix SchedulerPrefixAnchor `cbor:"installed_prefix_anchor"`
}

func (SchedulerHookInput) operationInput() {}

// SchedulerHookStep is one authored Plan item, in authored order.
type SchedulerHookStep struct {
	Title string `cbor:"title" json:"title"`
}

// SchedulerHookOutput is a closed typed result from the named compiled
// adapter. Exactly one kind-specific field family may be populated.
type SchedulerHookOutput struct {
	Kind         string              `cbor:"kind" json:"kind"`
	Version      uint64              `cbor:"version" json:"version"`
	InputDigest  string              `cbor:"input_digest" json:"input_digest"`
	ResultDigest string              `cbor:"result_digest" json:"result_digest"`
	Outcome      string              `cbor:"outcome" json:"outcome"`
	Revision     string              `cbor:"revision" json:"revision"`
	EvidenceRefs []string            `cbor:"evidence_refs" json:"evidence_refs"`
	ProblemCode  string              `cbor:"problem_code" json:"problem_code"`
	Steps        []SchedulerHookStep `cbor:"steps" json:"steps"`
	Handling     string              `cbor:"handling" json:"handling"`
}

func (SchedulerHookOutput) operationOutput() {}

// RoleLifecycleInput binds one engine-owned Role transition to the Run,
// Dispatch, and exact claim that owns it. The scheduler, not the hook adapter,
// decides when an activated Role is spawned or closed.
type RoleLifecycleInput struct {
	RunID      string `cbor:"run_id"`
	RunLocator string `cbor:"run_locator"`
	MatterID   string `cbor:"matter_id"`
	DispatchID string `cbor:"dispatch_id"`
	RoleID     string `cbor:"role_id"`
	Role       string `cbor:"role"`
	Reason     string `cbor:"reason"`
}

func (RoleLifecycleInput) operationInput() {}

// RoleLifecycleOutput confirms the exact transitioned Role identity.
type RoleLifecycleOutput struct {
	RunID      string `cbor:"run_id"`
	MatterID   string `cbor:"matter_id"`
	DispatchID string `cbor:"dispatch_id"`
	RoleID     string `cbor:"role_id"`
	Role       string `cbor:"role"`
	State      string `cbor:"state"`
}

func (RoleLifecycleOutput) operationOutput() {}

// RunFinishInput settles one completed Run after its claim-owned work and
// brackets have been closed by the owning Environment.
type RunFinishInput struct {
	RunID string `cbor:"run_id"`
}

func (RunFinishInput) operationInput() {}

// RunFinishOutput confirms the exact Run settlement.
type RunFinishOutput struct {
	RunID string `cbor:"run_id"`
	State string `cbor:"state"`
}

func (RunFinishOutput) operationOutput() {}

// RunResumeInput records an explicit owner-Environment recovery after old
// claim journals and Dispatches have been closed and before fresh work starts.
type RunResumeInput struct {
	RunID      string `cbor:"run_id"`
	RunLocator string `cbor:"run_locator"`
}

func (RunResumeInput) operationInput() {}

// RunResumeOutput confirms the exact resumed Run identity.
type RunResumeOutput struct {
	RunID string `cbor:"run_id"`
	State string `cbor:"state"`
}

func (RunResumeOutput) operationOutput() {}

// SchedulerOrchestrateV1 is the isolated engine-only hook command. It is
// deliberately absent from the default operation catalogue and is selected
// only by the explicit Step 10 profile.
var SchedulerOrchestrateV1 = mustDefine[SchedulerHookInput, SchedulerHookOutput](Metadata{
	Operation: ID{Name: "scheduler.orchestrate", Version: 1},
	Access:    AccessMutation, Delivery: DeliveryAuthority,
	RequiredContext: []ContextDimension{ContextRepo, ContextClone, ContextWorktree},
	Guards:          []Footprint{"run.owner", "run.frontier", "dispatch.owner", "role.owner", "claim.active", "scheduler.hook-intent"},
	Writes:          []Footprint{"scheduler.hook-engagement", "scheduler.hook-result"},
	BlobInputs: []BlobSpec{
		{Name: "model"},
		{Name: "outline"},
		{Name: "review"},
		{Name: "snapshot"},
		{Name: "source"},
		{Name: "tree-manifest"},
		{Name: "workplan"},
	},
	Claim: ClaimNone, ExternalEffects: []ExternalEffect{EffectAgentHook},
})

// RoleSpawnV1 activates one supported non-inert Role in an exact open
// Dispatch. Role events are authority facts used by hook and gate guards.
var RoleSpawnV1 = mustDefine[RoleLifecycleInput, RoleLifecycleOutput](Metadata{
	Operation: ID{Name: "role.spawn", Version: 1},
	Access:    AccessMutation, Delivery: DeliveryClaim,
	RequiredContext: []ContextDimension{ContextRepo, ContextClone, ContextWorktree},
	Guards:          []Footprint{"run.owner", "claim.active", "dispatch.active", "role.activation", "role.unique"},
	Writes:          []Footprint{"role.spawned"}, BlobInputs: []BlobSpec{},
	Claim: ClaimExact, ExternalEffects: []ExternalEffect{},
})

// RoleCloseV1 closes one exact open Role before its Dispatch or claim closes.
var RoleCloseV1 = mustDefine[RoleLifecycleInput, RoleLifecycleOutput](Metadata{
	Operation: ID{Name: "role.close", Version: 1},
	Access:    AccessMutation, Delivery: DeliveryClaim,
	RequiredContext: []ContextDimension{ContextRepo, ContextClone, ContextWorktree},
	Guards:          []Footprint{"run.owner", "claim.active", "dispatch.active", "role.owner", "role.open"},
	Writes:          []Footprint{"role.closed"}, BlobInputs: []BlobSpec{},
	Claim: ClaimExact, ExternalEffects: []ExternalEffect{},
})

// RunFinishV1 settles a Run only after its Matters and claim-owned brackets
// are complete. It is engine-only and never enters the ordinary CLI catalogue.
var RunFinishV1 = mustDefine[RunFinishInput, RunFinishOutput](Metadata{
	Operation: ID{Name: "run.finish", Version: 1}, Access: AccessMutation, Delivery: DeliveryAuthority,
	RequiredContext: []ContextDimension{ContextRepo, ContextClone, ContextWorktree},
	Guards:          []Footprint{"run.owner", "run.active", "run.matters-done", "run.roles-closed", "run.claims-closed"},
	Writes:          []Footprint{"run.finished"}, BlobInputs: []BlobSpec{},
	Claim: ClaimNone, ExternalEffects: []ExternalEffect{},
})

// SchedulerResumeV1 records owner-authorized recovery only after the old
// claim/Role brackets are resolved; the fresh claim remains a separate step.
var SchedulerResumeV1 = mustDefine[RunResumeInput, RunResumeOutput](Metadata{
	Operation: ID{Name: "scheduler.resume", Version: 1}, Access: AccessMutation, Delivery: DeliveryAuthority,
	RequiredContext: []ContextDimension{ContextRepo, ContextClone, ContextWorktree},
	Guards:          []Footprint{"run.owner", "run.active", "run.claims-closed", "run.roles-closed", "dispatch.old-closed"},
	Writes:          []Footprint{"run.resumed"}, BlobInputs: []BlobSpec{},
	Claim: ClaimNone, ExternalEffects: []ExternalEffect{},
})

// Step10Catalogue is the closed Step 10-only capability set. It is not part
// of Catalogue(), so older connected profiles remain unchanged. Post-seal is
// intentionally absent: its settled Step 10 behavior is no-action.
func Step10Catalogue() []Definition {
	return []Definition{SchedulerOrchestrateV1, RoleSpawnV1, RoleCloseV1, SchedulerResumeV1, RunFinishV1}
}

// Step10Operation reports membership in the isolated Step 10 operation set.
func Step10Operation(id ID) bool {
	return id == SchedulerOrchestrateV1.Metadata().Operation
}

// Step10CapabilityOperation reports membership in the complete isolated
// profile, including scheduler hooks and their authority-owned Role brackets.
func Step10CapabilityOperation(id ID) bool {
	for _, definition := range Step10Catalogue() {
		if definition.Metadata().Operation == id {
			return true
		}
	}
	return false
}

// Step10RoleOperation distinguishes authority-owned Role transitions from
// split-phase scheduler hook engagements.
func Step10RoleOperation(id ID) bool {
	return id == RoleSpawnV1.Metadata().Operation || id == RoleCloseV1.Metadata().Operation
}

// Step10RunOperation identifies the isolated Run lifecycle operations.
func Step10RunOperation(id ID) bool {
	return Step10RunResumeOperation(id) || Step10RunFinishOperation(id)
}

// Step10RunResumeOperation identifies the explicit Run recovery operation.
func Step10RunResumeOperation(id ID) bool { return id == SchedulerResumeV1.Metadata().Operation }

// Step10RunFinishOperation identifies the isolated Run settlement operation.
func Step10RunFinishOperation(id ID) bool { return id == RunFinishV1.Metadata().Operation }

func validateRoleLifecycleRequest(id ID, input RoleLifecycleInput, request Request) error {
	for name, value := range map[string]string{
		"Run ID": input.RunID, "Matter ID": input.MatterID,
		"Dispatch ID": input.DispatchID, "Role ID": input.RoleID,
	} {
		if err := validateULID(name, value); err != nil {
			return err
		}
	}
	if !locatorPattern.MatchString(input.RunLocator) {
		return fmt.Errorf("%s requires a canonical Run locator", id)
	}
	switch input.Role {
	case "orchestrator", "coordinator", "researcher", "builder", "verifier":
	default:
		return fmt.Errorf("%s uses an unsupported or inert Role", id)
	}
	switch id {
	case RoleSpawnV1.Metadata().Operation:
		if input.Reason != "" || request.Actor != "system:scheduler" && request.Actor != "role:orchestrator" {
			return fmt.Errorf("role.spawn@v1 requires the scheduler or Orchestrator actor and no close reason")
		}
	case RoleCloseV1.Metadata().Operation:
		if input.Reason != "completed" && input.Reason != "reaped" {
			return fmt.Errorf("role.close@v1 requires completed or reaped reason")
		}
		if input.Reason == "completed" && request.Actor != Actor("role:"+input.Role) ||
			input.Reason == "reaped" && request.Actor != "system:scheduler" {
			return fmt.Errorf("role.close@v1 actor does not own the requested closure")
		}
	}
	return nil
}

func validateRunFinishRequest(input RunFinishInput, request Request) error {
	if err := validateULID("Run ID", input.RunID); err != nil {
		return err
	}
	if request.Actor != "system:scheduler" {
		return fmt.Errorf("run.finish@v1 requires the scheduler actor")
	}
	return nil
}

func validateRunResumeRequest(input RunResumeInput, request Request) error {
	if err := validateULID("Run ID", input.RunID); err != nil {
		return err
	}
	if !locatorPattern.MatchString(input.RunLocator) {
		return fmt.Errorf("scheduler.resume@v1 requires a canonical Run locator")
	}
	if request.Actor != "system:scheduler" {
		return fmt.Errorf("scheduler.resume@v1 requires the scheduler actor")
	}
	return nil
}

func validateSchedulerHookInput(input SchedulerHookInput, request Request) error {
	if input.Version != 1 || input.AdapterID != "builtin.pinned-markdown" || input.AdapterVersion != 1 {
		return fmt.Errorf("scheduler.orchestrate@v1 names an unsupported hook or adapter version")
	}
	for name, value := range map[string]string{"Run ID": input.RunID, "Matter ID": input.MatterID, "target ID": input.TargetID} {
		if err := validateULID(name, value); err != nil {
			return err
		}
	}
	if !digestPattern.MatchString(input.InstalledPrefix.Digest) ||
		input.InstalledPrefix.EventCount == 0 && input.InstalledPrefix.EventID != nil ||
		input.InstalledPrefix.EventCount > 0 && (input.InstalledPrefix.EventID == nil || validateULID("installed prefix event ID", *input.InstalledPrefix.EventID) != nil) {
		return fmt.Errorf("scheduler.orchestrate@v1 has an invalid installed prefix anchor")
	}
	if input.TargetClaims == nil {
		return fmt.Errorf("scheduler.orchestrate@v1 target-claim set must be explicit")
	}
	if err := validateTargetClaims(input.TargetClaims); err != nil {
		return err
	}
	if input.Kind == "work" || input.Kind == "plan" {
		if request.Claim == nil || len(input.TargetClaims) != 1 || request.Claim.ID != input.TargetClaims[0].ClaimID ||
			request.Claim.Epoch != fmt.Sprint(input.TargetClaims[0].ClaimEpoch) {
			return fmt.Errorf("scheduler.orchestrate@v1 requires the exact active claim for its target Matter")
		}
	} else if request.Claim != nil {
		return fmt.Errorf("scheduler.orchestrate@v1 Ask hook cannot carry a Matter claim")
	}
	if request.Actor != Actor("role:"+input.Role) && input.Kind != "ask" {
		return fmt.Errorf("scheduler.orchestrate@v1 actor does not match the activated role")
	}
	for index, blob := range request.Blobs {
		if index > 0 && request.Blobs[index-1].Name >= blob.Name {
			return fmt.Errorf("scheduler.orchestrate@v1 pinned blobs must be uniquely ordered by name")
		}
	}
	switch input.Kind {
	case "work":
		if !validSchedulerTargetKind(input.TargetKind) || input.TargetKind == "matter" && input.TargetID != input.MatterID ||
			input.TargetKind != "matter" && input.TargetID == input.MatterID ||
			len(input.TargetClaims) != 1 || input.TargetClaims[0].MatterID != input.MatterID ||
			validateULID("role ID", input.RoleID) != nil || validateULID("Dispatch ID", input.DispatchID) != nil ||
			input.Reason != "" || input.Contention != "" || input.Blocked != "" || input.Failed != "" {
			return fmt.Errorf("scheduler.orchestrate@v1 work hook has invalid claim, role, or policy context")
		}
		switch input.Role {
		case "builder":
			if input.Revision != "builtin.markdown-outline@v1" || len(request.Blobs) != 1 || request.Blobs[0].Name != "source" {
				return fmt.Errorf("scheduler.orchestrate@v1 Builder hook requires pinned source and the supported outline revision")
			}
		case "verifier":
			switch input.Revision {
			case "builtin.markdown-outline-verifier@v1":
				if len(request.Blobs) != 2 || request.Blobs[0].Name != "outline" || request.Blobs[1].Name != "source" {
					return fmt.Errorf("scheduler.orchestrate@v1 legacy negative Verifier requires pinned source and outline")
				}
			case SchedulerStep10VerifierRevision:
				if len(request.Blobs) != 5 || request.Blobs[0].Name != "model" || request.Blobs[1].Name != "review" ||
					request.Blobs[2].Name != "snapshot" || request.Blobs[3].Name != "tree-manifest" || request.Blobs[4].Name != "workplan" {
					return fmt.Errorf("scheduler.orchestrate@v1 complete Verifier requires pinned contract, review, source snapshot, tree manifest, and Workplan")
				}
			default:
				return fmt.Errorf("scheduler.orchestrate@v1 Verifier revision is unsupported")
			}
		default:
			return fmt.Errorf("scheduler.orchestrate@v1 work hook role is unsupported")
		}
	case "plan":
		if input.TargetID != input.MatterID || input.TargetKind != "matter" || input.Role != "researcher" || validateULID("role ID", input.RoleID) != nil ||
			validateULID("Dispatch ID", input.DispatchID) != nil || input.Revision != "builtin.markdown-checklist-plan@v1" ||
			len(input.TargetClaims) != 1 || input.TargetClaims[0].MatterID != input.MatterID ||
			len(request.Blobs) != 1 || request.Blobs[0].Name != "workplan" ||
			input.Reason != "" || input.Contention != "" || input.Blocked != "" || input.Failed != "" {
			return fmt.Errorf("scheduler.orchestrate@v1 Plan hook has unsupported role, revision, claims, or inputs")
		}
	case "ask":
		if input.TargetID != input.MatterID || input.TargetKind != "matter" || input.Role != "" || input.RoleID != "" || input.DispatchID != "" || len(input.TargetClaims) != 0 ||
			input.Revision != "builtin.run-policy@v1" || len(request.Blobs) != 0 ||
			(input.Reason != "contended" && input.Reason != "blocked" && input.Reason != "failed") ||
			!validSchedulerHandling(input.Contention) || !validSchedulerHandling(input.Blocked) || !validSchedulerHandling(input.Failed) ||
			request.Actor != "system:scheduler" {
			return fmt.Errorf("scheduler.orchestrate@v1 Ask hook has invalid Run policy context")
		}
	default:
		return fmt.Errorf("scheduler.orchestrate@v1 hook kind is unsupported")
	}
	return nil
}

func validSchedulerTargetKind(kind string) bool {
	return kind == "matter" || kind == "stage" || kind == "step"
}

func validSchedulerHandling(value string) bool { return value == "skip" || value == "halt" }

func validateSchedulerHookOutput(output SchedulerHookOutput) error {
	if !digestPattern.MatchString(output.InputDigest) || !digestPattern.MatchString(output.ResultDigest) {
		return fmt.Errorf("scheduler hook output digests are invalid")
	}
	for _, ref := range output.EvidenceRefs {
		if !digestPattern.MatchString(ref) {
			return fmt.Errorf("scheduler hook evidence reference is invalid")
		}
	}
	switch output.Kind {
	case "work":
		if output.Version != 1 || (output.Outcome != "completed" && output.Outcome != "failed" && output.Outcome != "verified" && output.Outcome != "not-verified") ||
			output.Revision == "" || output.Steps != nil || output.Handling != "" ||
			(output.Outcome == "completed" || output.Outcome == "verified") && output.ProblemCode != "" ||
			(output.Outcome == "failed" || output.Outcome == "not-verified") && output.ProblemCode == "" {
			return fmt.Errorf("scheduler hook Work result is invalid")
		}
	case "plan":
		if output.Version != 1 || (output.Outcome != "planned" && output.Outcome != "failed") || output.Revision != "" ||
			output.EvidenceRefs != nil || output.Handling != "" ||
			(output.Outcome == "planned" && output.ProblemCode != "") || (output.Outcome == "failed" && (output.ProblemCode == "" || len(output.Steps) != 0)) ||
			len(output.Steps) > 128 {
			return fmt.Errorf("scheduler hook Plan result is invalid")
		}
		for _, step := range output.Steps {
			if strings.TrimSpace(step.Title) == "" || len(step.Title) > 240 {
				return fmt.Errorf("scheduler hook Plan result contains an invalid Step title")
			}
		}
	case "ask":
		if output.Version != 1 || (output.Handling != "skip" && output.Handling != "halt") || output.Outcome != "" ||
			output.Revision != "" || output.EvidenceRefs != nil || output.ProblemCode != "" || output.Steps != nil {
			return fmt.Errorf("scheduler hook Ask result is invalid")
		}
	default:
		return fmt.Errorf("scheduler hook result kind is unsupported")
	}
	return nil
}
