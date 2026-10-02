package operation

// GateExemptionRepairInput is the exact historical declaration boundary and
// incident identity attested by the owner. The detached authorization proof
// is never part of this command or its blob inputs.
type GateExemptionRepairInput struct {
	NodeID           string
	Gate             string
	EventCount       uint64
	HighWaterEventID string
	PrefixDigest     string
	IncidentRef      string
	Reason           string
	EvidenceRefs     []string
}

func (GateExemptionRepairInput) operationInput() {}

// GateExemptionRepairOutput reports the exact exemption projection, including
// the idempotent no-event outcome when it was already exempt.
type GateExemptionRepairOutput struct {
	Gate          string `json:"gate" cbor:"gate"`
	NodeID        string `json:"node_id" cbor:"node_id"`
	AlreadyExempt bool   `json:"already_exempt" cbor:"already_exempt"`
}

func (GateExemptionRepairOutput) operationOutput() {}

// GateExemptionRepairV1 is authority-delivered only after detached owner
// admission through command-submit/2 on both hops, never generic gate submit.
var GateExemptionRepairV1 = mustDefine[GateExemptionRepairInput, GateExemptionRepairOutput](Metadata{
	Operation: ID{Name: "gate.exemption.repair", Version: 1}, Access: AccessMutation,
	Delivery: DeliveryAuthority, RequiredContext: []ContextDimension{ContextRepo, ContextClone, ContextWorktree},
	Guards: []Footprint{
		FootprintMatterActiveClaim, FootprintGateSubject, FootprintAncestorLifecycle,
		FootprintGateDeclaration, FootprintGateState, FootprintGateExemptionSnapshot,
		FootprintGateRepairBoundary, FootprintGateRepairOwnerAuthorization,
	},
	Writes: []Footprint{FootprintGateState, FootprintGateExemptionSnapshot}, BlobInputs: []BlobSpec{},
	Claim: ClaimExact, ExternalEffects: []ExternalEffect{},
})
