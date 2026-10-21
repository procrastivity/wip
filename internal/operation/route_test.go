package operation

import (
	"reflect"
	"strings"
	"testing"
)

func TestStep11MutationInputsRoundTripCanonically(t *testing.T) {
	base := canonicalIdentityCommand()
	base.Request.Context = Context{Repo: "01J00000000000000000000001", Clone: "01J00000000000000000000002", Worktree: "01J00000000000000000000003"}
	proof, err := attachProofEncoder.Marshal(AttachCheckoutProof{
		Schema: "wipd.attach-checkout-proof/1", NormalizedRepoKey: "github.com/acme/widget",
		CommonDirDigest: "sha256:" + strings.Repeat("a", 64), GitDirDigest: "sha256:" + strings.Repeat("b", 64),
		PeerSnapshotDigest: "sha256:" + strings.Repeat("c", 64), RouteSnapshotDigest: "sha256:" + strings.Repeat("d", 64),
		EnvironmentID: "01J00000000000000000000004", EnvironmentSPKIDigest: "sha256:" + strings.Repeat("e", 64),
	})
	if err != nil {
		t.Fatal(err)
	}
	cases := []struct {
		name      string
		operation Definition
		input     Input
		repoOnly  bool
	}{
		{"create-local", RepoCreateLocalV1, RepoCreateLocalInput{GrantArtifact: "AQ"}, false},
		{"attach-once", EnvironmentAttachOnceV1, EnvironmentAttachOnceInput{GrantArtifact: "AQ", CheckoutProof: CanonicalBytes(proof)}, true},
		{"adopt-remote", RepoAdoptRemoteV1, RepoAdoptRemoteInput{NormalizedKey: "github.com/acme/widget"}, false},
		{"route-bind", RepoRouteBindV1, RepoRouteBindInput{NormalizedKey: "github.com/acme/widget"}, false},
	}
	for _, test := range cases {
		t.Run(test.name, func(t *testing.T) {
			command := base
			if test.repoOnly {
				command.Request.Context.Clone = ""
				command.Request.Context.Worktree = ""
			}
			command.Request.Operation = test.operation.Metadata().Operation
			command.Request.Input = test.input
			encoded, err := command.CanonicalBytes()
			if err != nil {
				t.Fatalf("CanonicalBytes() error = %v", err)
			}
			got, err := DecodeCanonicalCommand(encoded)
			if err != nil {
				t.Fatalf("DecodeCanonicalCommand() error = %v", err)
			}
			if got.Request.Operation != command.Request.Operation || !reflect.DeepEqual(got.Request.Input, test.input) {
				t.Fatalf("decoded operation/input = %s / %#v, want %s / %#v", got.Request.Operation, got.Request.Input, command.Request.Operation, test.input)
			}
		})
	}
}

func TestAttachOnceRequiresClosedProofAndRepoOnlyContext(t *testing.T) {
	command := canonicalIdentityCommand()
	command.Request.Operation = EnvironmentAttachOnceV1.Metadata().Operation
	command.Request.Context = Context{Repo: "01J00000000000000000000001"}
	proof, err := attachProofEncoder.Marshal(AttachCheckoutProof{
		Schema: "wipd.attach-checkout-proof/1", NormalizedRepoKey: "github.com/acme/widget",
		CommonDirDigest: "sha256:" + strings.Repeat("a", 64), GitDirDigest: "sha256:" + strings.Repeat("b", 64),
		PeerSnapshotDigest: "sha256:" + strings.Repeat("c", 64), RouteSnapshotDigest: "sha256:" + strings.Repeat("d", 64),
		EnvironmentID: "01J00000000000000000000004", EnvironmentSPKIDigest: "sha256:" + strings.Repeat("e", 64),
	})
	if err != nil {
		t.Fatal(err)
	}
	command.Request.Input = EnvironmentAttachOnceInput{GrantArtifact: "AQ", CheckoutProof: CanonicalBytes(proof)}
	if err = EnvironmentAttachOnceV1.ValidateRequest(command.Request); err != nil {
		t.Fatalf("valid Repo-only attach request rejected: %v", err)
	}
	command.Request.Context.Clone = "01J00000000000000000000002"
	if err = EnvironmentAttachOnceV1.ValidateRequest(command.Request); err == nil {
		t.Fatal("attach request accepted caller-supplied Clone context")
	}
	command.Request.Context.Clone = ""
	command.Request.Input = EnvironmentAttachOnceInput{GrantArtifact: "AQ==", CheckoutProof: CanonicalBytes(proof)}
	if err = EnvironmentAttachOnceV1.ValidateRequest(command.Request); err == nil {
		t.Fatal("attach request accepted padded grant encoding")
	}
	command.Request.Input = EnvironmentAttachOnceInput{GrantArtifact: "AQ", CheckoutProof: CanonicalBytes{0xa0}}
	if err = EnvironmentAttachOnceV1.ValidateRequest(command.Request); err == nil {
		t.Fatal("attach request accepted an open/incomplete proof")
	}
}

func TestRepoCreateLocalInputIsGrantOnlyAndRejectsLegacyTarget(t *testing.T) {
	command := canonicalIdentityCommand()
	command.Request.Operation = RepoCreateLocalV1.Metadata().Operation
	command.Request.Input = RepoCreateLocalInput{GrantArtifact: "AQ"}
	if err := RepoCreateLocalV1.ValidateRequest(command.Request); err != nil {
		t.Fatalf("valid create-local request rejected: %v", err)
	}
	value, err := command.canonicalValue()
	if err != nil {
		t.Fatal(err)
	}
	input := value["input"].(canonicalMap)
	input["target_domain_id"] = "01J00000000000000000000004"
	legacy, err := marshalCanonical(value)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = DecodeCanonicalCommand(legacy); err == nil {
		t.Fatal("create-local accepted the removed caller-selected target-domain field")
	}
}

func TestRepoCreateLocalGrantArtifactEncodingAndBounds(t *testing.T) {
	base := canonicalIdentityCommand().Request
	base.Operation = RepoCreateLocalV1.Metadata().Operation
	for _, test := range []struct {
		name  string
		grant string
		valid bool
	}{
		{name: "canonical unpadded base64url", grant: "AQ", valid: true},
		{name: "padding", grant: "AQ=="},
		{name: "standard alphabet", grant: "+/"},
		{name: "whitespace", grant: "AQ "},
		{name: "oversized decoded grant", grant: strings.Repeat("A", 5464)},
	} {
		t.Run(test.name, func(t *testing.T) {
			request := base
			request.Input = RepoCreateLocalInput{GrantArtifact: test.grant}
			err := RepoCreateLocalV1.ValidateRequest(request)
			if (err == nil) != test.valid {
				t.Fatalf("ValidateRequest() error = %v, want valid=%t", err, test.valid)
			}
		})
	}
}

func TestStep11ReadAndWriteMetadataKeepRouteResolveReadOnly(t *testing.T) {
	resolve := RouteResolveV1.Metadata()
	if resolve.Access != AccessRead || resolve.Delivery != DeliveryNone || len(resolve.Writes) != 0 {
		t.Fatalf("route.resolve metadata permits mutation: %+v", resolve)
	}
	for _, definition := range []Definition{RepoCreateLocalV1, EnvironmentAttachOnceV1, RepoAdoptRemoteV1, RepoRouteBindV1} {
		if definition.Metadata().Access != AccessMutation || definition.Metadata().Delivery != DeliveryAuthority {
			t.Errorf("%s metadata is not an authority mutation: %+v", definition.Metadata().Operation, definition.Metadata())
		}
	}
	valid := RouteResolveOutput{DomainID: "01ARZ3NDEKTSV4RRFFQ69G5FAV", Epoch: 1, ConnectionProfile: "primary"}
	if err := RouteResolveV1.ValidateResult(Result{Code: ResultSucceeded, Output: valid}); err != nil {
		t.Fatalf("valid fresh-miss output rejected: %v", err)
	}
	invalid := valid
	invalid.ConnectionProfile = " "
	if err := RouteResolveV1.ValidateResult(Result{Code: ResultSucceeded, Output: invalid}); err == nil {
		t.Fatal("route result with empty connection profile accepted")
	}
}

func TestStep11CatalogueIsExplicitAndIsolated(t *testing.T) {
	want := []Definition{RouteResolveV1, RepoCreateLocalV1, EnvironmentAttachOnceV1, RepoRouteBindV1, RepoAdoptRemoteV1}
	got := Step11Catalogue()
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("Step11Catalogue() = %+v, want %+v", got, want)
	}
	for _, definition := range got {
		id := definition.Metadata().Operation
		if !Step11Operation(id) {
			t.Errorf("Step11Operation(%s) = false", id)
		}
		for _, defaultDefinition := range Catalogue() {
			if defaultDefinition.Metadata().Operation == id && id != RepoRouteBindV1.Metadata().Operation {
				t.Errorf("Step 11 operation %s leaked into the default catalogue", id)
			}
		}
	}
	if Step11Operation(MatterCreateV1.Metadata().Operation) {
		t.Error("Step11Operation recognized a non-Step 11 operation")
	}
}
