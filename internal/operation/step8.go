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

// ReferenceUnbindInput removes one tracker reference from a Matter's active set.
type ReferenceUnbindInput struct {
	MatterID  string
	Reference string
}

func (ReferenceUnbindInput) operationInput() {}

// ReferenceRebindInput atomically replaces one reference with another.
type ReferenceRebindInput struct {
	MatterID string
	From     string
	To       string
}

func (ReferenceRebindInput) operationInput() {}

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
)

var step8ContractDefinitions = []Definition{DependencyAddV1, DependencyRemoveV1, ReferenceBindV1, ReferenceUnbindV1, ReferenceRebindV1}

// Step8Catalogue returns the closed dependency/reference acceptance set. These
// definitions remain outside the default runtime catalogue and CLI boundary.
func Step8Catalogue() []Definition {
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
