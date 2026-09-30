package operation

// MatterCreateV2Output keeps collision repair explicit without changing the
// established matter.create@v1 result contract.
type MatterCreateV2Output struct {
	ID                    string
	Title                 string
	RequestedLocator      string
	AssignedLocator       string
	LocatorRepairRequired bool
}

func (MatterCreateV2Output) operationOutput() {}

// StageCreateInput identifies the Matter and title for a new Stage.
type StageCreateInput struct {
	MatterID string
	Title    string
}

func (StageCreateInput) operationInput() {}

// StageCreateOutput describes a created Stage and its initial order/state.
type StageCreateOutput struct {
	ID       string
	MatterID string
	Locator  string
	Title    string
	SortKey  int64
	State    string
}

func (StageCreateOutput) operationOutput() {}

// StepInsertInput identifies a parent and optional sibling anchor for insertion.
type StepInsertInput struct {
	ParentID string
	Title    string
	AfterID  string
	BeforeID string
}

func (StepInsertInput) operationInput() {}

// StepReorderInput supplies the exact permutation of a parent's live Steps.
type StepReorderInput struct {
	ParentID string
	Order    []string
}

func (StepReorderInput) operationInput() {}

// StepReorderOutput reports the accepted sibling order.
type StepReorderOutput struct {
	ParentID string
	Order    []string
}

func (StepReorderOutput) operationOutput() {}

// StepReplaceInput identifies the Step to replace and the replacement title.
type StepReplaceInput struct {
	StepID string
	Title  string
}

func (StepReplaceInput) operationInput() {}

// StepReplaceOutput reports the removed Step and its replacement.
type StepReplaceOutput struct {
	RemovedStepID string
	Replacement   StepCreateOutput
}

func (StepReplaceOutput) operationOutput() {}

// StepRemoveInput identifies a Step and the required removal reason.
type StepRemoveInput struct {
	StepID string
	Reason string
}

func (StepRemoveInput) operationInput() {}

// StepRemoveOutput identifies the removed Step.
type StepRemoveOutput struct {
	StepID string
}

func (StepRemoveOutput) operationOutput() {}

// MatterLocatorRepairInput accepts a provisional locator or renames it.
type MatterLocatorRepairInput struct {
	MatterID        string
	AssignedLocator string
	Action          string
}

func (MatterLocatorRepairInput) operationInput() {}

// MatterLocatorRepairOutput records the requested and resulting locators.
type MatterLocatorRepairOutput struct {
	ID               string
	Action           string
	RequestedLocator string
	PreviousLocator  string
	AssignedLocator  string
}

func (MatterLocatorRepairOutput) operationOutput() {}

// MatterCreateV2 creates a Matter while making collision repair explicit.
var MatterCreateV2 = mustDefine[MatterCreateInput, MatterCreateV2Output](Metadata{
	Operation:       ID{Name: "matter.create", Version: 2},
	Access:          AccessMutation,
	Delivery:        DeliveryProvisional,
	RequiredContext: []ContextDimension{ContextRepo},
	Guards:          []Footprint{FootprintRepoMatterLocators},
	Writes:          []Footprint{FootprintNewbornMatter},
	BlobInputs:      []BlobSpec{},
	Claim:           ClaimNone,
	ExternalEffects: []ExternalEffect{},
})

// StageCreateV1 creates a Stage under an actively claimed Matter.
var StageCreateV1 = mustDefine[StageCreateInput, StageCreateOutput](Metadata{
	Operation:       ID{Name: "stage.create", Version: 1},
	Access:          AccessMutation,
	Delivery:        DeliveryClaim,
	RequiredContext: []ContextDimension{ContextRepo, ContextClone, ContextWorktree},
	Guards:          []Footprint{FootprintMatterActiveClaim, FootprintMatterLifecycle, FootprintStageParent, FootprintMatterStageLocators},
	Writes:          []Footprint{FootprintNewStageSubtree},
	BlobInputs:      []BlobSpec{},
	Claim:           ClaimExact,
	ExternalEffects: []ExternalEffect{},
})

// StepCreateV2 creates a Step under an actively claimed parent.
var StepCreateV2 = mustDefine[StepCreateInput, StepCreateOutput](Metadata{
	Operation:       ID{Name: "step.create", Version: 2},
	Access:          AccessMutation,
	Delivery:        DeliveryClaim,
	RequiredContext: []ContextDimension{ContextRepo, ContextClone, ContextWorktree},
	Guards:          []Footprint{FootprintMatterActiveClaim, FootprintMatterLifecycle, FootprintStepParent, FootprintStepLocator, FootprintStepSortKey},
	Writes:          []Footprint{FootprintNewbornStep},
	BlobInputs:      []BlobSpec{},
	Claim:           ClaimExact,
	ExternalEffects: []ExternalEffect{},
})

// StepInsertV1 inserts a new Step at an exact sibling boundary.
var StepInsertV1 = mustDefine[StepInsertInput, StepCreateOutput](Metadata{
	Operation:       ID{Name: "step.insert", Version: 1},
	Access:          AccessMutation,
	Delivery:        DeliveryClaim,
	RequiredContext: []ContextDimension{ContextRepo, ContextClone, ContextWorktree},
	Guards:          []Footprint{FootprintMatterActiveClaim, FootprintStepParent, FootprintStepSiblingSet, FootprintStepAnchor, FootprintStepSortKey},
	Writes:          []Footprint{FootprintNewbornStep, FootprintStepSiblingSet},
	BlobInputs:      []BlobSpec{},
	Claim:           ClaimExact,
	ExternalEffects: []ExternalEffect{},
})

// StepReorderV1 reorders all live sibling Steps as an exact permutation.
var StepReorderV1 = mustDefine[StepReorderInput, StepReorderOutput](Metadata{
	Operation:       ID{Name: "step.reorder", Version: 1},
	Access:          AccessMutation,
	Delivery:        DeliveryClaim,
	RequiredContext: []ContextDimension{ContextRepo, ContextClone, ContextWorktree},
	Guards:          []Footprint{FootprintMatterActiveClaim, FootprintStepParent, FootprintStepSiblingSet, FootprintStepOrderPermutation},
	Writes:          []Footprint{FootprintStepSiblingSet},
	BlobInputs:      []BlobSpec{},
	Claim:           ClaimExact,
	ExternalEffects: []ExternalEffect{},
})

// StepReplaceV1 removes one live Step and creates its replacement atomically.
var StepReplaceV1 = mustDefine[StepReplaceInput, StepReplaceOutput](Metadata{
	Operation:       ID{Name: "step.replace", Version: 1},
	Access:          AccessMutation,
	Delivery:        DeliveryClaim,
	RequiredContext: []ContextDimension{ContextRepo, ContextClone, ContextWorktree},
	Guards:          []Footprint{FootprintMatterActiveClaim, FootprintStepLive, FootprintStepNextLocator},
	Writes:          []Footprint{FootprintStepTombstone, FootprintReplacementStep},
	BlobInputs:      []BlobSpec{},
	Claim:           ClaimExact,
	ExternalEffects: []ExternalEffect{},
})

// StepRemoveV1 removes a live Step with an explicit reason.
var StepRemoveV1 = mustDefine[StepRemoveInput, StepRemoveOutput](Metadata{
	Operation:       ID{Name: "step.remove", Version: 1},
	Access:          AccessMutation,
	Delivery:        DeliveryClaim,
	RequiredContext: []ContextDimension{ContextRepo, ContextClone, ContextWorktree},
	Guards:          []Footprint{FootprintMatterActiveClaim, FootprintStepLive, FootprintStepRemovalReason},
	Writes:          []Footprint{FootprintStepTombstone},
	BlobInputs:      []BlobSpec{},
	Claim:           ClaimExact,
	ExternalEffects: []ExternalEffect{},
})

// MatterLocatorRepairV1 explicitly accepts or renames a provisional locator.
var MatterLocatorRepairV1 = mustDefine[MatterLocatorRepairInput, MatterLocatorRepairOutput](Metadata{
	Operation:       ID{Name: "matter.locator-repair", Version: 1},
	Access:          AccessMutation,
	Delivery:        DeliveryAuthority,
	RequiredContext: []ContextDimension{ContextRepo, ContextClone, ContextWorktree},
	Guards:          []Footprint{FootprintMatterActiveClaim, FootprintMatterLocatorRepair, FootprintRepoMatterLocators},
	Writes:          []Footprint{FootprintMatterLocatorRepair, FootprintRepoMatterLocators},
	BlobInputs:      []BlobSpec{},
	Claim:           ClaimExact,
	ExternalEffects: []ExternalEffect{},
})

var step4Catalogue = []Definition{
	MatterCreateV2, StageCreateV1, StepCreateV2, StepInsertV1, StepReorderV1,
	StepReplaceV1, StepRemoveV1, MatterLocatorRepairV1,
}

// Step4Catalogue returns the versioned M6 Step 4 semantic definitions.
func Step4Catalogue() []Definition { return append([]Definition(nil), step4Catalogue...) }

// Step4Operation reports whether id belongs to the Step 4 M6 operation set.
func Step4Operation(id ID) bool {
	for _, definition := range step4Catalogue {
		if definition.Metadata().Operation == id {
			return true
		}
	}
	return false
}
