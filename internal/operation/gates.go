package operation

// GateDeclareInput declares one prospective gate boundary for a Repo.
type GateDeclareInput struct {
	Gate  string
	Scale string
}

func (GateDeclareInput) operationInput() {}

// GateDeclareOutput identifies the fixed Repo gate binding.
type GateDeclareOutput struct {
	Gate  string
	Scale string
}

func (GateDeclareOutput) operationOutput() {}

// GateCloseInput closes one declared gate on an exact node.
type GateCloseInput struct {
	Gate   string
	NodeID string
}

func (GateCloseInput) operationInput() {}

// GateCloseOutput identifies the recorded gate closure.
type GateCloseOutput struct {
	Gate   string
	NodeID string
	Scale  string
}

func (GateCloseOutput) operationOutput() {}

// GateDismissInput dismisses one declared gate on an exact Done node.
type GateDismissInput struct {
	Gate   string
	NodeID string
	Reason string
}

func (GateDismissInput) operationInput() {}

// GateDismissOutput identifies the recorded gate dismissal.
type GateDismissOutput struct {
	Gate   string
	NodeID string
	Scale  string
}

func (GateDismissOutput) operationOutput() {}

// GateDeclareV1 declares a prospective Repo gate boundary.
var GateDeclareV1 = mustDefine[GateDeclareInput, GateDeclareOutput](Metadata{
	Operation: ID{Name: "gate.declare", Version: 1}, Access: AccessMutation,
	Delivery: DeliveryClaim, RequiredContext: []ContextDimension{ContextRepo, ContextClone, ContextWorktree},
	Guards: []Footprint{FootprintMatterActiveClaim, FootprintRepoGateDeclarations, FootprintGateOrder, FootprintGateExemptionSnapshot},
	Writes: []Footprint{FootprintRepoGateDeclarations, FootprintGateExemptionSnapshot}, BlobInputs: []BlobSpec{},
	Claim: ClaimExact, ExternalEffects: []ExternalEffect{},
})

// GateCloseV1 closes a declared gate on an exact node.
var GateCloseV1 = mustDefine[GateCloseInput, GateCloseOutput](Metadata{
	Operation: ID{Name: "gate.close", Version: 1}, Access: AccessMutation,
	Delivery: DeliveryClaim, RequiredContext: []ContextDimension{ContextRepo, ContextClone, ContextWorktree},
	Guards: []Footprint{
		FootprintMatterActiveClaim, FootprintGateDeclaration, FootprintGateOwner, FootprintGateState, FootprintGateSubject,
		FootprintRepoTrackerPushConfig, FootprintTrackerReferences, FootprintTrackerSharedAggregates,
	},
	Writes: []Footprint{FootprintGateState, FootprintTrackerSharedAggregates, FootprintTrackerCandidates}, BlobInputs: []BlobSpec{},
	Claim: ClaimExact, ExternalEffects: []ExternalEffect{},
})

// GateDismissV1 dismisses a declared gate on an exact Done node.
var GateDismissV1 = mustDefine[GateDismissInput, GateDismissOutput](Metadata{
	Operation: ID{Name: "gate.dismiss", Version: 1}, Access: AccessMutation,
	Delivery: DeliveryClaim, RequiredContext: []ContextDimension{ContextRepo, ContextClone, ContextWorktree},
	Guards: []Footprint{
		FootprintMatterActiveClaim, FootprintGateDeclaration, FootprintGateOwner, FootprintGateState, FootprintGateSubject,
		FootprintGateDismissalReason, FootprintRepoTrackerPushConfig, FootprintTrackerReferences, FootprintTrackerSharedAggregates,
	},
	Writes: []Footprint{FootprintGateState, FootprintTrackerSharedAggregates, FootprintTrackerCandidates}, BlobInputs: []BlobSpec{},
	Claim: ClaimExact, ExternalEffects: []ExternalEffect{},
})

var gateCatalogue = []Definition{GateDeclareV1, GateCloseV1, GateDismissV1}

// GateCatalogue returns the ordinary M6 authority gate operations.
func GateCatalogue() []Definition { return append([]Definition(nil), gateCatalogue...) }
