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

var catalogue = []Definition{MatterCreateV1}

// Catalogue returns the currently defined semantic operations. It is not the
// Cobra manifest: operations enter this list only as their semantic contracts
// are made concrete and migrated behind the in-process boundary.
func Catalogue() []Definition {
	return append([]Definition(nil), catalogue...)
}
