package operation

import (
	"reflect"
	"testing"
)

func schedulerHookCommand() Command {
	command := canonicalIdentityCommand()
	command.Request.Operation = SchedulerOrchestrateV1.Metadata().Operation
	command.Request.Actor = "role:builder"
	command.Request.Context.Clone = "01K5V8K8A4J2N7R9T0V3X6Y8ZC"
	command.Request.Context.Worktree = "01K5V8K8A4J2N7R9T0V3X6Y8ZD"
	command.Request.Claim = &ClaimContext{ID: testDomainID, Epoch: "7"}
	command.Request.Input = SchedulerHookInput{
		Kind: "work", Version: 1, AdapterID: "builtin.pinned-markdown", AdapterVersion: 1,
		RunID: testCommandID, MatterID: testCommandID, TargetID: testCommandID, TargetKind: "matter",
		Role: "builder", RoleID: testEnvironmentID, DispatchID: testDomainID,
		Revision: "builtin.markdown-outline@v1", TargetClaims: []TargetClaim{{
			MatterID: testCommandID, ClaimID: testDomainID, ClaimEpoch: 7,
		}}, InstalledPrefix: SchedulerPrefixAnchor{
			EventCount: 5, EventID: stringPointer(testEnvironmentID), Digest: BlobDigest([]byte("prefix-five")),
		},
	}
	command.Request.Blobs = []BlobInput{{Name: "source", Digest: BlobDigest([]byte("# Asymmetric\n")), Size: 14}}
	return command
}

func stringPointer(value string) *string { return &value }

func TestStep10HookCommandCanonicalRoundTripAndConflictIdentity(t *testing.T) {
	if Step10Operation(SchedulerOrchestrateV1.Metadata().Operation) != true {
		t.Fatal("Step10Operation() does not recognize scheduler.orchestrate@v1")
	}
	if !Step10CapabilityOperation(RoleSpawnV1.Metadata().Operation) || !Step10CapabilityOperation(RoleCloseV1.Metadata().Operation) {
		t.Fatal("isolated Step 10 catalogue omits an authority-owned Role transition")
	}
	for _, definition := range Catalogue() {
		if Step10CapabilityOperation(definition.Metadata().Operation) {
			t.Fatalf("%s leaked into the default operation catalogue", definition.Metadata().Operation)
		}
	}

	original := schedulerHookCommand()
	encoded, err := original.CanonicalBytes()
	if err != nil {
		t.Fatalf("CanonicalBytes() error = %v", err)
	}
	decoded, err := DecodeCanonicalCommand(encoded)
	if err != nil {
		t.Fatalf("DecodeCanonicalCommand() error = %v", err)
	}
	got, ok := decoded.Request.Input.(SchedulerHookInput)
	if !ok || !reflect.DeepEqual(got, original.Request.Input) || !reflect.DeepEqual(decoded.Request.Blobs, original.Request.Blobs) {
		t.Fatalf("decoded scheduler hook intent = %#v, want %#v", decoded.Request.Input, original.Request.Input)
	}

	originalHash, err := original.RequestHash()
	if err != nil {
		t.Fatalf("RequestHash() error = %v", err)
	}
	changed := original
	changedInput := changed.Request.Input.(SchedulerHookInput)
	changedInput.TargetClaims = []TargetClaim{{MatterID: testCommandID, ClaimID: testDomainID, ClaimEpoch: 8}}
	changed.Request.Input = changedInput
	changed.Request.Claim = &ClaimContext{ID: testDomainID, Epoch: "8"}
	changedHash, err := changed.RequestHash()
	if err != nil {
		t.Fatalf("changed RequestHash() error = %v", err)
	}
	if changedHash == originalHash {
		t.Fatal("changing the claim epoch did not change canonical command identity")
	}

	unsupported := original
	unsupportedInput := unsupported.Request.Input.(SchedulerHookInput)
	unsupportedInput.AdapterID = "caller-supplied-executable"
	unsupported.Request.Input = unsupportedInput
	if _, err = unsupported.CanonicalBytes(); err == nil {
		t.Fatal("unsupported adapter identity was admitted by the operation contract")
	}
}

func TestStep10RoleTransitionsHaveExactTypedIdentity(t *testing.T) {
	base := canonicalIdentityCommand()
	base.Request.Context.Clone = "01K5V8K8A4J2N7R9T0V3X6Y8ZC"
	base.Request.Context.Worktree = "01K5V8K8A4J2N7R9T0V3X6Y8ZD"
	base.Request.Claim = &ClaimContext{ID: testDomainID, Epoch: "7"}
	input := RoleLifecycleInput{
		RunID: testCommandID, RunLocator: "run-asymmetric", MatterID: testCommandID, DispatchID: testDomainID,
		RoleID: testEnvironmentID, Role: "verifier",
	}
	base.Request.Operation = RoleSpawnV1.Metadata().Operation
	base.Request.Actor = "role:orchestrator"
	base.Request.Input = input
	if err := RoleSpawnV1.ValidateRequest(base.Request); err != nil {
		t.Fatalf("valid Role spawn rejected: %v", err)
	}
	encoded, err := base.CanonicalBytes()
	if err != nil {
		t.Fatal(err)
	}
	decoded, err := DecodeCanonicalCommand(encoded)
	got, ok := decoded.Request.Input.(RoleLifecycleInput)
	if err != nil || !ok || !reflect.DeepEqual(got, input) {
		t.Fatalf("role command roundtrip = %#v, %v", decoded.Request.Input, err)
	}
	if err = RoleSpawnV1.ValidateResult(Result{Code: ResultSucceeded, Output: RoleLifecycleOutput{
		RunID: input.RunID, MatterID: input.MatterID, DispatchID: input.DispatchID,
		RoleID: input.RoleID, Role: input.Role, State: "open",
	}}); err != nil {
		t.Fatalf("valid typed spawn result rejected: %v", err)
	}
	closeRequest := base.Request
	closeRequest.Operation = RoleCloseV1.Metadata().Operation
	closeRequest.Actor = "role:verifier"
	input.Reason = "completed"
	closeRequest.Input = input
	if err = RoleCloseV1.ValidateRequest(closeRequest); err != nil {
		t.Fatalf("exact role-owner close rejected: %v", err)
	}
	closeRequest.Actor = "role:builder"
	if err = RoleCloseV1.ValidateRequest(closeRequest); err == nil {
		t.Fatal("a different role actor closed the Verifier")
	}
}

func TestStep10HookResultsRequireKindSpecificTypedShape(t *testing.T) {
	digest := BlobDigest([]byte("result"))
	cases := []struct {
		name   string
		output SchedulerHookOutput
		valid  bool
	}{
		{
			name: "builder result",
			output: SchedulerHookOutput{
				Kind: "work", Version: 1, InputDigest: digest, ResultDigest: digest,
				Outcome: "completed", Revision: "builtin.markdown-outline@v1", EvidenceRefs: []string{digest}, Steps: nil,
			},
			valid: true,
		},
		{
			name: "ordered plan",
			output: SchedulerHookOutput{
				Kind: "plan", Version: 1, InputDigest: digest, ResultDigest: digest,
				Outcome: "planned", Steps: []SchedulerHookStep{{Title: "First asymmetric"}, {Title: "Second"}},
			},
			valid: true,
		},
		{
			name:   "ask answer",
			output: SchedulerHookOutput{Kind: "ask", Version: 1, InputDigest: digest, ResultDigest: digest, Handling: "halt"},
			valid:  true,
		},
		{
			name: "false Plan success with failure code",
			output: SchedulerHookOutput{
				Kind: "plan", Version: 1, InputDigest: digest, ResultDigest: digest,
				Outcome: "planned", ProblemCode: "adapter.failed", Steps: []SchedulerHookStep{},
			},
		},
		{
			name: "Ask with hidden evidence",
			output: SchedulerHookOutput{
				Kind: "ask", Version: 1, InputDigest: digest, ResultDigest: digest,
				Handling: "skip", EvidenceRefs: []string{digest},
			},
		},
	}
	for _, test := range cases {
		t.Run(test.name, func(t *testing.T) {
			err := SchedulerOrchestrateV1.ValidateResult(Result{Code: ResultSucceeded, Output: test.output})
			if (err == nil) != test.valid {
				t.Fatalf("ValidateResult() error = %v, want valid=%v", err, test.valid)
			}
		})
	}
}

func TestStep10AskIsRunLevelAndClaimless(t *testing.T) {
	command := schedulerHookCommand()
	input := command.Request.Input.(SchedulerHookInput)
	input.Kind = "ask"
	input.Role = ""
	input.RoleID = ""
	input.DispatchID = ""
	input.Revision = "builtin.run-policy@v1"
	input.Reason = "blocked"
	input.Contention = "skip"
	input.Blocked = "halt"
	input.Failed = "skip"
	input.TargetClaims = []TargetClaim{}
	command.Request.Actor = "system:scheduler"
	command.Request.Claim = nil
	command.Request.Blobs = nil
	command.Request.Input = input

	if err := SchedulerOrchestrateV1.ValidateRequest(command.Request); err != nil {
		t.Fatalf("claimless Run-level Ask request rejected: %v", err)
	}

	work := schedulerHookCommand()
	work.Request.Claim = nil
	if err := SchedulerOrchestrateV1.ValidateRequest(work.Request); err == nil {
		t.Fatal("claimless Work request was admitted")
	}
}

func TestStep10VerifierRequiresTheClosedRevisionBoundInputSet(t *testing.T) {
	command := schedulerHookCommand()
	input := command.Request.Input.(SchedulerHookInput)
	input.Role = "verifier"
	input.Revision = SchedulerStep10VerifierRevision
	command.Request.Actor = "role:verifier"
	command.Request.Input = input
	command.Request.Blobs = []BlobInput{
		{Name: "model", Digest: BlobDigest([]byte("model")), Size: 5},
		{Name: "review", Digest: BlobDigest([]byte("review")), Size: 6},
		{Name: "snapshot", Digest: BlobDigest([]byte("snapshot")), Size: 8},
		{Name: "tree-manifest", Digest: BlobDigest([]byte("manifest")), Size: 8},
		{Name: "workplan", Digest: BlobDigest([]byte("workplan")), Size: 8},
	}
	if err := SchedulerOrchestrateV1.ValidateRequest(command.Request); err != nil {
		t.Fatalf("complete pinned Verifier request rejected: %v", err)
	}

	for _, mutate := range []struct {
		name string
		edit func(*Command)
	}{
		{name: "missing review", edit: func(value *Command) {
			value.Request.Blobs = append(value.Request.Blobs[:1], value.Request.Blobs[2:]...)
		}},
		{name: "substituted blob order", edit: func(value *Command) {
			value.Request.Blobs[1], value.Request.Blobs[2] = value.Request.Blobs[2], value.Request.Blobs[1]
		}},
		{name: "caller-selected revision", edit: func(value *Command) {
			request := value.Request.Input.(SchedulerHookInput)
			request.Revision = "caller.shell@v1"
			value.Request.Input = request
		}},
	} {
		t.Run(mutate.name, func(t *testing.T) {
			changed := command
			changed.Request.Blobs = append([]BlobInput(nil), command.Request.Blobs...)
			mutate.edit(&changed)
			if _, err := changed.CanonicalBytes(); err == nil {
				t.Fatal("malformed or caller-selected Verifier intent was admitted")
			}
		})
	}
}
