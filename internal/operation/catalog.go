package operation

// MatterCreateInput is the semantic input to matter.create. Checkout
// discovery, flag parsing, and title/locator rendering remain outside it.
type MatterCreateInput struct {
	Title   string
	Locator string
}

func (MatterCreateInput) operationInput() {}

// MatterCreateOutput is the semantic result consumed by both human and JSON
// renderers; neither renderer belongs in the handler.
type MatterCreateOutput struct {
	ID      string
	Locator string
	Title   string
}

func (MatterCreateOutput) operationOutput() {}

// StepCreateInput names a Matter by stable identity. Locators are presentation
// addresses and are never accepted as authority command targets.
type StepCreateInput struct {
	ParentID string
	Title    string
}

func (StepCreateInput) operationInput() {}

// StepCreateOutput is the authority-assigned Planned Step projection.
type StepCreateOutput struct {
	ID       string
	ParentID string
	MatterID string
	Locator  string
	Title    string
	SortKey  int64
	State    string
}

func (StepCreateOutput) operationOutput() {}

// MatterCreateV1 is the canonical M1 definition and the first Step 3 adoption
// candidate. Its future delivery class is provisional per D120/D127, while its
// current implementation and storage ownership remain unchanged.
var MatterCreateV1 = mustDefine[MatterCreateInput, MatterCreateOutput](Metadata{
	Operation:       ID{Name: "matter.create", Version: 1},
	Access:          AccessMutation,
	Delivery:        DeliveryProvisional,
	RequiredContext: []ContextDimension{ContextRepo},
	Guards:          []Footprint{FootprintRepoMatterLocators},
	Writes:          []Footprint{FootprintNewbornMatter},
	BlobInputs:      []BlobSpec{},
	Claim:           ClaimNone,
	ExternalEffects: []ExternalEffect{},
})

// StepCreateV1 is scoped to a direct Step birth beneath a Matter born by the
// same provisional Environment. Its exact implicit-birth claim is not the M3
// claim.acquire lifecycle and cannot authorize any other footprint.
var StepCreateV1 = mustDefine[StepCreateInput, StepCreateOutput](Metadata{
	Operation:       ID{Name: "step.create", Version: 1},
	Access:          AccessMutation,
	Delivery:        DeliveryProvisional,
	RequiredContext: []ContextDimension{ContextRepo},
	Guards:          []Footprint{FootprintRepoMatterLocators, FootprintImplicitBirthClaim, FootprintStepParent, FootprintStepLocator, FootprintStepSortKey},
	Writes:          []Footprint{FootprintNewbornStep},
	BlobInputs:      []BlobSpec{},
	Claim:           ClaimImplicitBirth,
	ExternalEffects: []ExternalEffect{},
})

var catalogue = []Definition{MatterCreateV1, StepCreateV1}

// Catalogue returns the currently defined semantic operations. It is not the
// Cobra manifest: operations enter this list only as their semantic contracts
// are made concrete and migrated behind the in-process boundary.
func Catalogue() []Definition {
	return append([]Definition(nil), catalogue...)
}
