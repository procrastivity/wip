package operation

import (
	"bytes"
	"encoding/hex"
	"encoding/json"
	"strings"
	"testing"
)

func TestStep8DefinitionsAreAuthorityOnlyAndClaimFree(t *testing.T) {
	for _, definition := range step8ContractDefinitions {
		metadata := definition.Metadata()
		if metadata.Delivery != DeliveryAuthority || metadata.Claim != ClaimNone || len(metadata.RequiredContext) != 1 ||
			metadata.RequiredContext[0] != ContextRepo || len(metadata.BlobInputs) != 0 {
			t.Errorf("%s metadata = %+v, want Repo authority operation without claim/blob context", metadata.Operation, metadata)
		}
		request := Request{Operation: metadata.Operation, Actor: "human", Context: Context{Repo: "01ARZ3NDEKTSV4RRFFQ69G5FAV"}, Input: nil, Blobs: []BlobInput{}}
		switch metadata.Operation {
		case DependencyAddV1.Metadata().Operation:
			request.Input = DependencyAddInput{BlockedID: "01ARZ3NDEKTSV4RRFFQ69G5FAV", BlockerID: "01ARZ3NDEKTSV4RRFFQ69G5FAW"}
		case DependencyRemoveV1.Metadata().Operation:
			request.Input = DependencyRemoveInput{BlockedID: "01ARZ3NDEKTSV4RRFFQ69G5FAV", BlockerID: "01ARZ3NDEKTSV4RRFFQ69G5FAW"}
		case ReferenceBindV1.Metadata().Operation:
			request.Input = ReferenceBindInput{MatterID: "01ARZ3NDEKTSV4RRFFQ69G5FAV", Reference: "T-1"}
		case ReferenceUnbindV1.Metadata().Operation:
			request.Input = ReferenceUnbindInput{MatterID: "01ARZ3NDEKTSV4RRFFQ69G5FAV", Reference: "T-1"}
		case ReferenceRebindV1.Metadata().Operation:
			request.Input = ReferenceRebindInput{MatterID: "01ARZ3NDEKTSV4RRFFQ69G5FAV", From: "T-1", To: "T-2"}
		}
		if err := definition.ValidateRequest(request); err != nil {
			t.Errorf("%s valid request: %v", metadata.Operation, err)
		}
		if metadata.Operation == DependencyAddV1.Metadata().Operation {
			request.Input = DependencyAddInput{BlockedID: "01ARZ3NDEKTSV4RRFFQ69G5FAV", BlockerID: "01ARZ3NDEKTSV4RRFFQ69G5FAV"}
			if err := definition.ValidateRequest(request); err != nil {
				t.Errorf("dependency self-edge should reach authority cycle validation: %v", err)
			}
		}
		request.Claim = &ClaimContext{ID: "01ARZ3NDEKTSV4RRFFQ69G5FAY", Epoch: "1"}
		if err := definition.ValidateRequest(request); err == nil {
			t.Errorf("%s accepted one-Matter claim context", metadata.Operation)
		}
	}
	for _, definition := range Catalogue() {
		if _, exists := step8ContractDefinition(definition.Metadata().Operation); exists {
			t.Errorf("%s was advertised in the default runtime catalogue", definition.Metadata().Operation)
		}
	}
}

func TestStep8CanonicalCommandRoundTrip(t *testing.T) {
	inputs := []struct {
		definition Definition
		input      Input
	}{
		{DependencyAddV1, DependencyAddInput{BlockedID: "01ARZ3NDEKTSV4RRFFQ69G5FAV", BlockerID: "01ARZ3NDEKTSV4RRFFQ69G5FAW"}},
		{DependencyRemoveV1, DependencyRemoveInput{BlockedID: "01ARZ3NDEKTSV4RRFFQ69G5FAV", BlockerID: "01ARZ3NDEKTSV4RRFFQ69G5FAW"}},
		{ReferenceBindV1, ReferenceBindInput{MatterID: "01ARZ3NDEKTSV4RRFFQ69G5FAV", Reference: "T-1"}},
		{ReferenceUnbindV1, ReferenceUnbindInput{MatterID: "01ARZ3NDEKTSV4RRFFQ69G5FAV", Reference: "T-1"}},
		{ReferenceRebindV1, ReferenceRebindInput{MatterID: "01ARZ3NDEKTSV4RRFFQ69G5FAV", From: "T-1", To: "T-2"}},
	}
	for index, test := range inputs {
		commandID := "01ARZ3NDEKTSV4RRFFQ69G5FB" + string(rune('0'+index))
		command := Command{
			ID: commandID, AuthorityDomainID: "01ARZ3NDEKTSV4RRFFQ69G5FAV",
			ExpectedAuthorityEpoch: 1, EnvironmentID: "01ARZ3NDEKTSV4RRFFQ69G5FAW", EnvironmentSequence: uint64(index + 1),
			ActedAt: "2026-09-23T11:59:00Z", CorrelationCommandID: commandID,
			Request: Request{Operation: test.definition.Metadata().Operation, Actor: "human", Context: Context{Repo: "01ARZ3NDEKTSV4RRFFQ69G5FAX"}, Input: test.input, Blobs: []BlobInput{}},
		}
		encoded, err := command.CanonicalBytes()
		if err != nil {
			t.Fatalf("%s encode: %v", test.definition.Metadata().Operation, err)
		}
		decoded, err := DecodeCanonicalCommand(encoded)
		if err != nil {
			t.Fatalf("%s decode: %v", test.definition.Metadata().Operation, err)
		}
		if decoded.Request.Operation != command.Request.Operation || decoded.Request.Input != command.Request.Input || decoded.Request.Claim != nil {
			t.Fatalf("%s round trip = %+v, want input %+v without claim", test.definition.Metadata().Operation, decoded.Request, command.Request.Input)
		}
	}
	selfEdge := Command{
		ID: "01ARZ3NDEKTSV4RRFFQ69G5FB5", AuthorityDomainID: "01ARZ3NDEKTSV4RRFFQ69G5FAV",
		ExpectedAuthorityEpoch: 1, EnvironmentID: "01ARZ3NDEKTSV4RRFFQ69G5FAW", EnvironmentSequence: 6,
		ActedAt: "2026-09-23T11:59:00Z", CorrelationCommandID: "01ARZ3NDEKTSV4RRFFQ69G5FB5",
		Request: Request{
			Operation: DependencyAddV1.Metadata().Operation, Actor: "human",
			Context: Context{Repo: "01ARZ3NDEKTSV4RRFFQ69G5FAX"},
			Input:   DependencyAddInput{BlockedID: "01ARZ3NDEKTSV4RRFFQ69G5FAV", BlockerID: "01ARZ3NDEKTSV4RRFFQ69G5FAV"},
			Blobs:   []BlobInput{},
		},
	}
	encoded, err := selfEdge.CanonicalBytes()
	if err != nil {
		t.Fatalf("dependency self-edge canonical encode: %v", err)
	}
	decoded, err := DecodeCanonicalCommand(encoded)
	if err != nil || decoded.Request.Input != selfEdge.Request.Input {
		t.Fatalf("dependency self-edge canonical decode = %+v, %v", decoded.Request.Input, err)
	}
}

func TestStep8CanonicalInputVectors(t *testing.T) {
	const blockedID = "01ARZ3NDEKTSV4RRFFQ69G5FAV"
	const blockerID = "01ARZ3NDEKTSV4RRFFQ69G5FAW"
	const matterID = "01ARZ3NDEKTSV4RRFFQ69G5FAX"
	inputs := []struct {
		definition Definition
		input      Input
		want       string
	}{
		{DependencyAddV1, DependencyAddInput{BlockedID: blockedID, BlockerID: blockerID}, "a26a626c6f636b65645f6964781a303141525a334e44454b545356345252464651363947354641566a626c6f636b65725f6964781a303141525a334e44454b54535634525246465136394735464157"},
		{DependencyRemoveV1, DependencyRemoveInput{BlockedID: blockedID, BlockerID: blockerID}, "a26a626c6f636b65645f6964781a303141525a334e44454b545356345252464651363947354641566a626c6f636b65725f6964781a303141525a334e44454b54535634525246465136394735464157"},
		{ReferenceBindV1, ReferenceBindInput{MatterID: matterID, Reference: "T-1"}, "a2696d61747465725f6964781a303141525a334e44454b54535634525246465136394735464158697265666572656e636563542d31"},
		{ReferenceUnbindV1, ReferenceUnbindInput{MatterID: matterID, Reference: "T-1"}, "a2696d61747465725f6964781a303141525a334e44454b54535634525246465136394735464158697265666572656e636563542d31"},
		{ReferenceRebindV1, ReferenceRebindInput{MatterID: matterID, From: "T-1", To: "T-2"}, "a362746f63542d326466726f6d63542d31696d61747465725f6964781a303141525a334e44454b54535634525246465136394735464158"},
	}
	for _, test := range inputs {
		value, err := canonicalInput(test.input)
		if err != nil {
			t.Fatalf("%s canonical input: %v", test.definition.Metadata().Operation, err)
		}
		got, err := marshalCanonical(value)
		if err != nil {
			t.Fatalf("%s marshal input: %v", test.definition.Metadata().Operation, err)
		}
		want, err := hex.DecodeString(strings.ReplaceAll(test.want, " ", ""))
		if err != nil {
			t.Fatalf("%s expected vector: %v", test.definition.Metadata().Operation, err)
		}
		if !bytes.Equal(got, want) {
			t.Errorf("%s canonical input = %x, want %x", test.definition.Metadata().Operation, got, want)
		}
	}
}

func TestStep8SuccessOutputJSONShapes(t *testing.T) {
	tests := []struct {
		definition Definition
		output     Output
		want       string
	}{
		{DependencyAddV1, DependencyOutput{EdgeID: "01ARZ3NDEKTSV4RRFFQ69G5FAZ", BlockedID: "01ARZ3NDEKTSV4RRFFQ69G5FAV", BlockerID: "01ARZ3NDEKTSV4RRFFQ69G5FAW"}, `{"edge":"01ARZ3NDEKTSV4RRFFQ69G5FAZ","blocked_id":"01ARZ3NDEKTSV4RRFFQ69G5FAV","blocker_id":"01ARZ3NDEKTSV4RRFFQ69G5FAW"}`},
		{DependencyRemoveV1, DependencyOutput{BlockedID: "01ARZ3NDEKTSV4RRFFQ69G5FAV", BlockerID: "01ARZ3NDEKTSV4RRFFQ69G5FAW"}, `{"blocked_id":"01ARZ3NDEKTSV4RRFFQ69G5FAV","blocker_id":"01ARZ3NDEKTSV4RRFFQ69G5FAW"}`},
		{ReferenceBindV1, ReferenceOutput{MatterID: "01ARZ3NDEKTSV4RRFFQ69G5FAX", Reference: "T-1"}, `{"matter_id":"01ARZ3NDEKTSV4RRFFQ69G5FAX","reference":"T-1"}`},
		{ReferenceUnbindV1, ReferenceOutput{MatterID: "01ARZ3NDEKTSV4RRFFQ69G5FAX", Reference: "T-1"}, `{"matter_id":"01ARZ3NDEKTSV4RRFFQ69G5FAX","reference":"T-1"}`},
		{ReferenceRebindV1, ReferenceOutput{MatterID: "01ARZ3NDEKTSV4RRFFQ69G5FAX", Reference: "T-2", PreviousReference: "T-1"}, `{"matter_id":"01ARZ3NDEKTSV4RRFFQ69G5FAX","reference":"T-2","previous_reference":"T-1"}`},
	}
	if err := DependencyAddV1.ValidateResult(Result{Code: ResultSucceeded, Output: DependencyOutput{
		BlockedID: "01ARZ3NDEKTSV4RRFFQ69G5FAV", BlockerID: "01ARZ3NDEKTSV4RRFFQ69G5FAW",
	}}); err == nil {
		t.Fatal("dependency.add accepted success output without authority-assigned edge ID")
	}
	if err := DependencyRemoveV1.ValidateResult(Result{Code: ResultSucceeded, Output: DependencyOutput{
		EdgeID: "01ARZ3NDEKTSV4RRFFQ69G5FAZ", BlockedID: "01ARZ3NDEKTSV4RRFFQ69G5FAV", BlockerID: "01ARZ3NDEKTSV4RRFFQ69G5FAW",
	}}); err == nil {
		t.Fatal("dependency.remove accepted add-only edge ID")
	}
	for _, test := range tests {
		if err := test.definition.ValidateResult(Result{Code: ResultSucceeded, Output: test.output}); err != nil {
			t.Errorf("%s success result rejected: %v", test.definition.Metadata().Operation, err)
		}
		got, err := json.Marshal(test.output)
		if err != nil || string(got) != test.want {
			t.Errorf("%s output JSON = %s, %v; want %s", test.definition.Metadata().Operation, got, err, test.want)
		}
	}
}
