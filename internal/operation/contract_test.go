package operation

import (
	"context"
	"database/sql"
	"io"
	"strings"
	"testing"

	"github.com/spf13/cobra"

	"github.com/procrastivity/wip/internal/iostreams"
)

func TestMatterCreateDefinitionIsComplete(t *testing.T) {
	metadata := MatterCreateV1.Metadata()
	if got := metadata.Operation.String(); got != "matter.create@v1" {
		t.Fatalf("operation = %q, want matter.create@v1", got)
	}
	if metadata.Access != AccessMutation || metadata.Delivery != DeliveryProvisional {
		t.Fatalf("classification = %s/%s, want mutation/provisional", metadata.Access, metadata.Delivery)
	}
	if len(metadata.RequiredContext) != 1 || metadata.RequiredContext[0] != ContextRepo {
		t.Fatalf("required context = %v, want repo", metadata.RequiredContext)
	}
	if len(metadata.Guards) != 1 || metadata.Guards[0] != FootprintRepoMatterLocators {
		t.Fatalf("guards = %v, want Repo Matter locator index", metadata.Guards)
	}
	if len(metadata.Writes) != 1 || metadata.Writes[0] != FootprintNewbornMatter {
		t.Fatalf("writes = %v, want newborn Matter subtree", metadata.Writes)
	}
	if metadata.BlobInputs == nil || len(metadata.BlobInputs) != 0 {
		t.Fatalf("blob inputs = %#v, want explicit empty set", metadata.BlobInputs)
	}
	if metadata.Claim != ClaimNone {
		t.Fatalf("claim = %q, want none", metadata.Claim)
	}
	if metadata.ExternalEffects == nil || len(metadata.ExternalEffects) != 0 {
		t.Fatalf("external effects = %#v, want explicit empty set", metadata.ExternalEffects)
	}
	if len(Catalogue()) != 1 {
		t.Fatalf("catalogue has %d definitions, want the one Step 3 seed", len(Catalogue()))
	}
}

func TestMetadataValidationExposesEveryOmittedDimension(t *testing.T) {
	valid := MatterCreateV1.Metadata()
	tests := []struct {
		name string
		edit func(*Metadata)
		want string
	}{
		{"operation version", func(m *Metadata) { m.Operation.Version = 0 }, "no version"},
		{"access", func(m *Metadata) { m.Access = "" }, "access"},
		{"delivery", func(m *Metadata) { m.Delivery = "" }, "delivery class"},
		{"context", func(m *Metadata) { m.RequiredContext = nil }, "context metadata is omitted"},
		{"guards", func(m *Metadata) { m.Guards = nil }, "guard footprint metadata is omitted"},
		{"writes", func(m *Metadata) { m.Writes = nil }, "write footprint metadata is omitted"},
		{"blobs", func(m *Metadata) { m.BlobInputs = nil }, "blob input metadata is omitted"},
		{"claim", func(m *Metadata) { m.Claim = "" }, "claim requirement"},
		{"external effects", func(m *Metadata) { m.ExternalEffects = nil }, "side-effect metadata is omitted"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			metadata := cloneMetadata(valid)
			test.edit(&metadata)
			err := ValidateMetadata(metadata)
			if err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("ValidateMetadata() = %v, want an error containing %q", err, test.want)
			}
		})
	}
}

func TestMetadataValidationRejectsContradictoryClassifications(t *testing.T) {
	tests := []struct {
		name string
		edit func(*Metadata)
		want string
	}{
		{"read with writes", func(m *Metadata) { m.Access = AccessRead; m.Delivery = DeliveryNone }, "read cannot declare a write"},
		{"mutation without delivery", func(m *Metadata) { m.Delivery = DeliveryNone }, "mutation must declare a delivery"},
		{"mutation without writes", func(m *Metadata) { m.Writes = []Footprint{} }, "at least one write"},
		{"claim class without claim", func(m *Metadata) { m.Delivery = DeliveryClaim }, "requires an exact claim"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			metadata := MatterCreateV1.Metadata()
			test.edit(&metadata)
			err := ValidateMetadata(metadata)
			if err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("ValidateMetadata() = %v, want an error containing %q", err, test.want)
			}
		})
	}
}

func TestCanonicalMatterCreateRequestAndResults(t *testing.T) {
	request := Request{
		Operation: ID{Name: "matter.create", Version: 1},
		Actor:     "role:builder",
		Context:   Context{Repo: "01REPO"},
		Input:     MatterCreateInput{Title: "Ship operation boundary", Locator: "operation-boundary"},
		Blobs:     []BlobInput{},
	}
	if err := MatterCreateV1.ValidateRequest(request); err != nil {
		t.Fatalf("canonical request: %v", err)
	}

	success := Result{
		Code: ResultSucceeded,
		Output: MatterCreateOutput{
			ID: "01MATTER", Locator: "operation-boundary", Title: "Ship operation boundary",
		},
	}
	if err := MatterCreateV1.ValidateResult(success); err != nil {
		t.Fatalf("canonical success: %v", err)
	}
	refusal := Result{
		Code: ResultRefused,
		Problem: &Problem{
			Code: ProblemUnknownClone, Message: "this clone is unknown",
		},
	}
	if err := MatterCreateV1.ValidateResult(refusal); err != nil {
		t.Fatalf("canonical refusal: %v", err)
	}
}

func TestRequestValidationRejectsAmbientOrMismatchedInputs(t *testing.T) {
	valid := Request{
		Operation: MatterCreateV1.Metadata().Operation,
		Actor:     "human",
		Context:   Context{Repo: "01REPO"},
		Input:     MatterCreateInput{Title: "Title"},
		Blobs:     []BlobInput{},
	}
	tests := []struct {
		name string
		edit func(*Request)
		want string
	}{
		{"wrong version", func(r *Request) { r.Operation.Version = 2 }, "want matter.create@v1"},
		{"missing actor", func(r *Request) { r.Actor = "" }, "actor"},
		{"missing resolved Repo", func(r *Request) { r.Context.Repo = "" }, "repo context"},
		{"unexpected claim", func(r *Request) { r.Claim = &ClaimContext{ID: "claim", Epoch: "1"} }, "does not accept claim"},
		{"wrong payload", func(r *Request) { r.Input = otherInput{} }, "want operation.MatterCreateInput"},
		{"undeclared blob", func(r *Request) { r.Blobs = []BlobInput{{Name: "body", Digest: "sha256:abc", Size: 3}} }, "not declared"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			request := valid
			test.edit(&request)
			err := MatterCreateV1.ValidateRequest(request)
			if err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("ValidateRequest() = %v, want an error containing %q", err, test.want)
			}
		})
	}
}

func TestResultValidationKeepsStableCodeNamespacesDistinct(t *testing.T) {
	tests := []struct {
		name   string
		result Result
		want   string
	}{
		{
			"refusal carrying validation code",
			Result{Code: ResultRefused, Problem: &Problem{Code: ProblemInvalidTitle, Message: "bad title"}},
			"requires a refusal.* problem",
		},
		{
			"rejection carrying refusal code",
			Result{Code: ResultRejected, Problem: &Problem{Code: ProblemUnknownClone, Message: "unknown clone"}},
			"rejected result cannot carry",
		},
		{
			"failure carrying validation code",
			Result{Code: ResultFailed, Problem: &Problem{Code: ProblemInvalidLocator, Message: "bad locator"}},
			"requires an internal.* problem",
		},
		{
			"success carrying problem",
			Result{Code: ResultSucceeded, Output: MatterCreateOutput{}, Problem: &Problem{Code: ProblemInvalidTitle, Message: "bad title"}},
			"cannot carry a problem",
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			err := MatterCreateV1.ValidateResult(test.result)
			if err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("ValidateResult() = %v, want an error containing %q", err, test.want)
			}
		})
	}
}

type otherInput struct{}

func (otherInput) operationInput() {}

type cobraInput struct{ Command *cobra.Command }

func (cobraInput) operationInput() {}

type streamInput struct{ Streams *iostreams.Streams }

func (streamInput) operationInput() {}

type databaseInput struct{ DB *sql.DB }

func (databaseInput) operationInput() {}

type terminalInput struct{ Out io.Writer }

func (terminalInput) operationInput() {}

type contextInput struct{ Context context.Context }

func (contextInput) operationInput() {}

type environmentInput struct {
	LookupEnv func(string) (string, bool)
}

func (environmentInput) operationInput() {}

type bytesInput struct{ Body []byte }

func (bytesInput) operationInput() {}

type byteArrayInput struct{ Body [32]byte }

func (byteArrayInput) operationInput() {}

func TestDefinitionRejectsTransportAndRuntimeCoupling(t *testing.T) {
	metadata := MatterCreateV1.Metadata()
	tests := []struct {
		name string
		call func() error
		want string
	}{
		{"Cobra command", func() error { _, err := define[cobraInput, MatterCreateOutput](metadata); return err }, "github.com/spf13/cobra"},
		{"terminal streams", func() error { _, err := define[streamInput, MatterCreateOutput](metadata); return err }, "internal/iostreams"},
		{"database handle", func() error { _, err := define[databaseInput, MatterCreateOutput](metadata); return err }, "database/sql"},
		{"terminal writer", func() error { _, err := define[terminalInput, MatterCreateOutput](metadata); return err }, "io.Writer from io"},
		{"execution context", func() error { _, err := define[contextInput, MatterCreateOutput](metadata); return err }, "context.Context from context"},
		{"environment lookup", func() error { _, err := define[environmentInput, MatterCreateOutput](metadata); return err }, "non-representable func"},
		{"raw input bytes", func() error { _, err := define[bytesInput, MatterCreateOutput](metadata); return err }, "staged BlobInput"},
		{"raw input byte array", func() error { _, err := define[byteArrayInput, MatterCreateOutput](metadata); return err }, "staged BlobInput"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			err := test.call()
			if err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("define() = %v, want an error containing %q", err, test.want)
			}
		})
	}
}
