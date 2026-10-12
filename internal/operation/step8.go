package operation

// DependencyAddInput adds a blocked-by edge between live Matter/Step nodes in
// one authority domain, including across Repos. Context.Repo is the command's
// domain-member guard, not endpoint scope; uniqueness and cycles are domain-wide.
type DependencyAddInput struct {
	BlockedID string
	BlockerID string
}

func (DependencyAddInput) operationInput() {}

// DependencyRemoveInput removes the exact live endpoint pair in one authority
// domain, including across Repos. Its domain-valid Context.Repo need not match
// either endpoint or the add command's Context.Repo.
type DependencyRemoveInput struct {
	BlockedID string
	BlockerID string
}

func (DependencyRemoveInput) operationInput() {}

// TargetClaim is an exact proof for an authority-derived affected Matter.
type TargetClaim struct {
	MatterID   string
	ClaimID    string
	ClaimEpoch uint64
}

// DependencyAddV2Input adds a dependency with exact claims for its affected Matters.
type DependencyAddV2Input struct {
	BlockedID    string
	BlockerID    string
	TargetClaims []TargetClaim
}

func (DependencyAddV2Input) operationInput() {}

// DependencyRemoveV2Input removes a dependency with exact claims for its affected Matters.
type DependencyRemoveV2Input struct {
	BlockedID    string
	BlockerID    string
	TargetClaims []TargetClaim
}

func (DependencyRemoveV2Input) operationInput() {}

// DependencyOutput identifies the affected edge endpoints.
type DependencyOutput struct {
	EdgeID    string `json:"edge,omitempty"`
	BlockedID string `json:"blocked_id"`
	BlockerID string `json:"blocker_id"`
}

func (DependencyOutput) operationOutput() {}

// ReferenceBindInput adds one tracker reference to a Matter's active set.
type ReferenceBindInput struct {
	MatterID  string
	Reference string
}

func (ReferenceBindInput) operationInput() {}

// ReferenceBindV2Input adds a tracker reference with an exact claim for its Matter.
type ReferenceBindV2Input struct {
	MatterID     string
	Reference    string
	TargetClaims []TargetClaim
}

func (ReferenceBindV2Input) operationInput() {}

// ReferenceUnbindInput removes one tracker reference from a Matter's active set.
type ReferenceUnbindInput struct {
	MatterID  string
	Reference string
}

func (ReferenceUnbindInput) operationInput() {}

// ReferenceUnbindV2Input removes a tracker reference with an exact claim for its Matter.
type ReferenceUnbindV2Input struct {
	MatterID     string
	Reference    string
	TargetClaims []TargetClaim
}

func (ReferenceUnbindV2Input) operationInput() {}

// ReferenceRebindInput atomically replaces one reference with another.
type ReferenceRebindInput struct {
	MatterID string
	From     string
	To       string
}

func (ReferenceRebindInput) operationInput() {}

// ReferenceRebindV2Input replaces a tracker reference with an exact claim for its Matter.
type ReferenceRebindV2Input struct {
	MatterID     string
	From         string
	To           string
	TargetClaims []TargetClaim
}

func (ReferenceRebindV2Input) operationInput() {}

// ReferenceOutput describes one committed reference membership change.
type ReferenceOutput struct {
	MatterID          string `json:"matter_id"`
	Reference         string `json:"reference"`
	PreviousReference string `json:"previous_reference,omitempty"`
}

func (ReferenceOutput) operationOutput() {}

var (
	// DependencyAddV1 adds a domain-wide dependency edge.
	DependencyAddV1 = mustDefine[DependencyAddInput, DependencyOutput](Metadata{
		Operation: ID{Name: "dependency.add", Version: 1}, Access: AccessMutation,
		Delivery: DeliveryAuthority, RequiredContext: []ContextDimension{ContextRepo},
		Guards: []Footprint{FootprintDependencyEndpoints, FootprintDependencyGraph},
		Writes: []Footprint{FootprintDependencyEdge, FootprintDependencyGraph}, BlobInputs: []BlobSpec{},
		Claim: ClaimNone, ExternalEffects: []ExternalEffect{},
	})
	// DependencyRemoveV1 removes a domain-wide dependency edge.
	DependencyRemoveV1 = mustDefine[DependencyRemoveInput, DependencyOutput](Metadata{
		Operation: ID{Name: "dependency.remove", Version: 1}, Access: AccessMutation,
		Delivery: DeliveryAuthority, RequiredContext: []ContextDimension{ContextRepo},
		Guards: []Footprint{FootprintDependencyEndpoints, FootprintDependencyEdge},
		Writes: []Footprint{FootprintDependencyEdge, FootprintDependencyGraph}, BlobInputs: []BlobSpec{},
		Claim: ClaimNone, ExternalEffects: []ExternalEffect{},
	})
	// ReferenceBindV1 binds a tracker reference to a Matter.
	ReferenceBindV1 = mustDefine[ReferenceBindInput, ReferenceOutput](Metadata{
		Operation: ID{Name: "reference.bind", Version: 1}, Access: AccessMutation,
		Delivery: DeliveryAuthority, RequiredContext: []ContextDimension{ContextRepo},
		Guards: []Footprint{FootprintRepoTrackerPushConfig, FootprintMatterTrackerReferences, FootprintTrackerSharedAggregates},
		Writes: []Footprint{FootprintMatterTrackerReferences, FootprintTrackerSharedAggregates, FootprintTrackerCandidates}, BlobInputs: []BlobSpec{},
		Claim: ClaimNone, ExternalEffects: []ExternalEffect{},
	})
	// ReferenceUnbindV1 removes a tracker reference from a Matter.
	ReferenceUnbindV1 = mustDefine[ReferenceUnbindInput, ReferenceOutput](Metadata{
		Operation: ID{Name: "reference.unbind", Version: 1}, Access: AccessMutation,
		Delivery: DeliveryAuthority, RequiredContext: []ContextDimension{ContextRepo},
		Guards: []Footprint{FootprintRepoTrackerPushConfig, FootprintMatterTrackerReferences, FootprintTrackerSharedAggregates},
		Writes: []Footprint{FootprintMatterTrackerReferences, FootprintTrackerSharedAggregates, FootprintTrackerCandidates}, BlobInputs: []BlobSpec{},
		Claim: ClaimNone, ExternalEffects: []ExternalEffect{},
	})
	// ReferenceRebindV1 atomically replaces a Matter's tracker reference.
	ReferenceRebindV1 = mustDefine[ReferenceRebindInput, ReferenceOutput](Metadata{
		Operation: ID{Name: "reference.rebind", Version: 1}, Access: AccessMutation,
		Delivery: DeliveryAuthority, RequiredContext: []ContextDimension{ContextRepo},
		Guards: []Footprint{FootprintRepoTrackerPushConfig, FootprintMatterTrackerReferences, FootprintTrackerSharedAggregates},
		Writes: []Footprint{FootprintMatterTrackerReferences, FootprintTrackerSharedAggregates, FootprintTrackerCandidates}, BlobInputs: []BlobSpec{},
		Claim: ClaimNone, ExternalEffects: []ExternalEffect{},
	})
	// DependencyAddV2 adds a dependency only with exact claims for all affected Matters.
	DependencyAddV2 = mustDefine[DependencyAddV2Input, DependencyOutput](Metadata{
		Operation: ID{Name: "dependency.add", Version: 2}, Access: AccessMutation,
		Delivery: DeliveryAuthority, RequiredContext: []ContextDimension{ContextRepo},
		Guards: []Footprint{FootprintDependencyEndpoints, FootprintDependencyGraph},
		Writes: []Footprint{FootprintDependencyEdge, FootprintDependencyGraph}, BlobInputs: []BlobSpec{},
		Claim: ClaimTargetSet, ExternalEffects: []ExternalEffect{},
	})
	// DependencyRemoveV2 removes a dependency only with exact claims for all affected Matters.
	DependencyRemoveV2 = mustDefine[DependencyRemoveV2Input, DependencyOutput](Metadata{
		Operation: ID{Name: "dependency.remove", Version: 2}, Access: AccessMutation,
		Delivery: DeliveryAuthority, RequiredContext: []ContextDimension{ContextRepo},
		Guards: []Footprint{FootprintDependencyEndpoints, FootprintDependencyEdge},
		Writes: []Footprint{FootprintDependencyEdge, FootprintDependencyGraph}, BlobInputs: []BlobSpec{},
		Claim: ClaimTargetSet, ExternalEffects: []ExternalEffect{},
	})
	// ReferenceBindV2 adds a reference only with the target Matter's exact claim.
	ReferenceBindV2 = mustDefine[ReferenceBindV2Input, ReferenceOutput](Metadata{
		Operation: ID{Name: "reference.bind", Version: 2}, Access: AccessMutation,
		Delivery: DeliveryAuthority, RequiredContext: []ContextDimension{ContextRepo},
		Guards: []Footprint{FootprintRepoTrackerPushConfig, FootprintMatterTrackerReferences, FootprintTrackerSharedAggregates},
		Writes: []Footprint{FootprintMatterTrackerReferences, FootprintTrackerSharedAggregates, FootprintTrackerCandidates}, BlobInputs: []BlobSpec{},
		Claim: ClaimTargetSet, ExternalEffects: []ExternalEffect{},
	})
	// ReferenceUnbindV2 removes a reference only with the target Matter's exact claim.
	ReferenceUnbindV2 = mustDefine[ReferenceUnbindV2Input, ReferenceOutput](Metadata{
		Operation: ID{Name: "reference.unbind", Version: 2}, Access: AccessMutation,
		Delivery: DeliveryAuthority, RequiredContext: []ContextDimension{ContextRepo},
		Guards: []Footprint{FootprintRepoTrackerPushConfig, FootprintMatterTrackerReferences, FootprintTrackerSharedAggregates},
		Writes: []Footprint{FootprintMatterTrackerReferences, FootprintTrackerSharedAggregates, FootprintTrackerCandidates}, BlobInputs: []BlobSpec{},
		Claim: ClaimTargetSet, ExternalEffects: []ExternalEffect{},
	})
	// ReferenceRebindV2 replaces a reference only with the target Matter's exact claim.
	ReferenceRebindV2 = mustDefine[ReferenceRebindV2Input, ReferenceOutput](Metadata{
		Operation: ID{Name: "reference.rebind", Version: 2}, Access: AccessMutation,
		Delivery: DeliveryAuthority, RequiredContext: []ContextDimension{ContextRepo},
		Guards: []Footprint{FootprintRepoTrackerPushConfig, FootprintMatterTrackerReferences, FootprintTrackerSharedAggregates},
		Writes: []Footprint{FootprintMatterTrackerReferences, FootprintTrackerSharedAggregates, FootprintTrackerCandidates}, BlobInputs: []BlobSpec{},
		Claim: ClaimTargetSet, ExternalEffects: []ExternalEffect{},
	})
)

var step8ContractDefinitions = []Definition{
	DependencyAddV1, DependencyRemoveV1, ReferenceBindV1, ReferenceUnbindV1, ReferenceRebindV1,
	DependencyAddV2, DependencyRemoveV2, ReferenceBindV2, ReferenceUnbindV2, ReferenceRebindV2,
}

var step8StrictDefinitions = []Definition{DependencyAddV2, DependencyRemoveV2, ReferenceBindV2, ReferenceUnbindV2, ReferenceRebindV2}

// Step8Catalogue returns the closed dependency/reference acceptance set. These
// definitions remain outside the default runtime catalogue and CLI boundary.
func Step8Catalogue() []Definition {
	return append([]Definition(nil), step8StrictDefinitions...)
}

// Step8HistoryCatalogue includes strict command definitions and historical v1 schemas.
func Step8HistoryCatalogue() []Definition {
	return append([]Definition(nil), step8ContractDefinitions...)
}

// Step8Operation reports an exact dependency/reference operation version.
func Step8Operation(id ID) bool {
	_, found := step8ContractDefinition(id)
	return found
}

func step8ContractDefinition(id ID) (Definition, bool) {
	for _, definition := range step8ContractDefinitions {
		if definition.Metadata().Operation == id {
			return definition, true
		}
	}
	return Definition{}, false
}
