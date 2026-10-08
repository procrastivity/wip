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

// StepLifecycleInput identifies the exact Step changed by a claim-scoped
// lifecycle command. Its containing Matter is resolved from authority state.
type StepLifecycleInput struct {
	StepID string
}

func (StepLifecycleInput) operationInput() {}

// StepLifecycleOutput reports the authority's typed state for the exact Step.
type StepLifecycleOutput struct {
	StepID   string
	MatterID string
	State    string
}

func (StepLifecycleOutput) operationOutput() {}

// MatterFinishInput identifies the Matter completed under its active claim.
type MatterFinishInput struct {
	MatterID string
}

func (MatterFinishInput) operationInput() {}

// MatterFinishOutput reports whether completion crossed the seal boundary.
type MatterFinishOutput struct {
	MatterID     string
	State        string
	BecameSealed bool
}

func (MatterFinishOutput) operationOutput() {}

// ClaimClosePrefix is the exact Environment-installed authority prefix at the
// end of one successful claim.release receipt.
type ClaimClosePrefix struct {
	EventCount uint64
	EventID    *string
	Digest     string
}

// ClaimCloseReference binds a sweep request to the exact successful installed
// normal claim.release@v1 outcome that closed this claim.
type ClaimCloseReference struct {
	ClaimID               string
	ClaimEpoch            uint64
	ReleaseCommandID      string
	ReleaseRequestHash    string
	TerminalReceiptDigest string
	InstalledPrefixAnchor ClaimClosePrefix
}

// BatchSweepAnonymousInput targets one exact Matter and anonymous Batch and
// carries a reference to its installed normal claim-close result. Receipt and
// detached-proof bytes are never embedded in the command identity.
type BatchSweepAnonymousInput struct {
	MatterID   string
	BatchID    string
	ClaimClose ClaimCloseReference
}

func (BatchSweepAnonymousInput) operationInput() {}

// BatchCreateInput names one domain-scoped Batch. Repo context authenticates
// the initiating Environment but does not scope the Batch identity.
type BatchCreateInput struct {
	Name string
}

func (BatchCreateInput) operationInput() {}

// BatchCreateOutput is the authority-assigned identity of a named Batch.
type BatchCreateOutput struct {
	ID   string
	Name string
}

func (BatchCreateOutput) operationOutput() {}

// BatchSweepAnonymousOutcome is the closed success outcome vocabulary.
type BatchSweepAnonymousOutcome string

// BatchSweepAnonymousSwept and BatchSweepAnonymousAlreadySwept distinguish
// a newly emitted sweep from an already completed sweep.
const (
	BatchSweepAnonymousSwept        BatchSweepAnonymousOutcome = "swept"
	BatchSweepAnonymousAlreadySwept BatchSweepAnonymousOutcome = "already-swept"
)

// BatchSweepAnonymousOutput reports whether this command emitted the sweep.
// already-swept is a successful deterministic no-event outcome.
type BatchSweepAnonymousOutput struct {
	Outcome BatchSweepAnonymousOutcome
}

func (BatchSweepAnonymousOutput) operationOutput() {}

// ContentWriteInput writes one create-once prose kind to an existing Matter
// or Step. The bytes are supplied only through the declared staged blob.
type ContentWriteInput struct {
	SubjectID string
	Kind      string
}

func (ContentWriteInput) operationInput() {}

// FindingAppendInput appends one findings segment to an existing Matter or
// Step using the declared staged blob.
type FindingAppendInput struct {
	SubjectID string
}

func (FindingAppendInput) operationInput() {}

// ContentSegmentOutput identifies the exact authority-folded content segment.
type ContentSegmentOutput struct {
	ID         string
	SubjectID  string
	Kind       string
	BlobDigest string
	ByteLength int64
}

func (ContentSegmentOutput) operationOutput() {}

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

// StepStartV1 and StepFinishV1 are claim-delivered lifecycle operations. A
// Step start may atomically cascade the Matter ancestor from Planned to
// InProgress before starting the exact Step.
var StepStartV1 = mustDefine[StepLifecycleInput, StepLifecycleOutput](Metadata{
	Operation:       ID{Name: "step.start", Version: 1},
	Access:          AccessMutation,
	Delivery:        DeliveryClaim,
	RequiredContext: []ContextDimension{ContextRepo, ContextClone, ContextWorktree},
	Guards:          []Footprint{FootprintMatterActiveClaim, FootprintMatterLifecycle, FootprintStepLifecycle},
	Writes:          []Footprint{FootprintMatterLifecycle, FootprintStepLifecycle},
	BlobInputs:      []BlobSpec{},
	Claim:           ClaimExact,
	ExternalEffects: []ExternalEffect{},
})

// StepFinishV1 finishes one exact Step under its active Matter claim.
var StepFinishV1 = mustDefine[StepLifecycleInput, StepLifecycleOutput](Metadata{
	Operation:       ID{Name: "step.finish", Version: 1},
	Access:          AccessMutation,
	Delivery:        DeliveryClaim,
	RequiredContext: []ContextDimension{ContextRepo, ContextClone, ContextWorktree},
	Guards:          []Footprint{FootprintMatterActiveClaim, FootprintStepLifecycle},
	Writes:          []Footprint{FootprintStepLifecycle},
	BlobInputs:      []BlobSpec{},
	Claim:           ClaimExact,
	ExternalEffects: []ExternalEffect{},
})

// MatterFinishV1 is statically authority-delivered even though its exact
// active Matter claim is required as fencing and context proof. New finish
// executions do not sweep an anonymous Batch; compatible historical receipts
// may retain that legacy inline effect.
var MatterFinishV1 = mustDefine[MatterFinishInput, MatterFinishOutput](Metadata{
	Operation:       ID{Name: "matter.finish", Version: 1},
	Access:          AccessMutation,
	Delivery:        DeliveryAuthority,
	RequiredContext: []ContextDimension{ContextRepo, ContextClone, ContextWorktree},
	Guards:          []Footprint{FootprintMatterActiveClaim, FootprintMatterLifecycle},
	Writes:          []Footprint{FootprintMatterLifecycle},
	BlobInputs:      []BlobSpec{},
	Claim:           ClaimExact,
	ExternalEffects: []ExternalEffect{},
})

// BatchSweepAnonymousV1 is an authority-delivered operation available only
// through the explicit M6 command catalogue.
var BatchSweepAnonymousV1 = mustDefine[BatchSweepAnonymousInput, BatchSweepAnonymousOutput](Metadata{
	Operation:       ID{Name: "batch.sweep-anonymous", Version: 1},
	Access:          AccessMutation,
	Delivery:        DeliveryAuthority,
	RequiredContext: []ContextDimension{ContextRepo},
	Guards:          []Footprint{FootprintMatterLifecycle, FootprintAnonymousBatchLifecycle},
	Writes:          []Footprint{FootprintAnonymousBatchLifecycle},
	BlobInputs:      []BlobSpec{},
	Claim:           ClaimNone,
	ExternalEffects: []ExternalEffect{},
})

// BatchCreateV1 creates one named Batch in the authority domain. The explicit
// M6 Step 9A profile is the only connected capability that offers it.
var BatchCreateV1 = mustDefine[BatchCreateInput, BatchCreateOutput](Metadata{
	Operation:       ID{Name: "batch.create", Version: 1},
	Access:          AccessMutation,
	Delivery:        DeliveryAuthority,
	RequiredContext: []ContextDimension{ContextRepo},
	Guards:          []Footprint{FootprintNamedBatchLifecycle},
	Writes:          []Footprint{FootprintNamedBatchLifecycle},
	BlobInputs:      []BlobSpec{},
	Claim:           ClaimNone,
	ExternalEffects: []ExternalEffect{},
})

// ContentWriteOnceV1 and FindingAppendV1 are the claim-scoped content subset
// used by the online Matter workflow. Their bytes are always staged blobs.
var ContentWriteOnceV1 = mustDefine[ContentWriteInput, ContentSegmentOutput](Metadata{
	Operation:       ID{Name: "content.write-once", Version: 1},
	Access:          AccessMutation,
	Delivery:        DeliveryClaim,
	RequiredContext: []ContextDimension{ContextRepo, ContextClone, ContextWorktree},
	Guards:          []Footprint{FootprintMatterActiveClaim, FootprintNodeContent},
	Writes:          []Footprint{FootprintNodeContent},
	BlobInputs:      []BlobSpec{{Name: "content", Required: true}},
	Claim:           ClaimExact,
	ExternalEffects: []ExternalEffect{},
})

// FindingAppendV1 records one append-only findings segment under its exact
// active Matter claim.
var FindingAppendV1 = mustDefine[FindingAppendInput, ContentSegmentOutput](Metadata{
	Operation:       ID{Name: "finding.append", Version: 1},
	Access:          AccessMutation,
	Delivery:        DeliveryClaim,
	RequiredContext: []ContextDimension{ContextRepo, ContextClone, ContextWorktree},
	Guards:          []Footprint{FootprintMatterActiveClaim, FootprintNodeContent},
	Writes:          []Footprint{FootprintFindingSegments},
	BlobInputs:      []BlobSpec{{Name: "content", Required: true}},
	Claim:           ClaimExact,
	ExternalEffects: []ExternalEffect{},
})

var catalogue = []Definition{
	MatterCreateV1, StepCreateV1, StepStartV1, StepFinishV1, MatterFinishV1,
	ContentWriteOnceV1, FindingAppendV1,
	MatterCreateV2, StageCreateV1, StepCreateV2, StepInsertV1, StepReorderV1,
	StepReplaceV1, StepRemoveV1, MatterLocatorRepairV1,
	MatterStartV1, StageStartV1, MatterPauseV1, StagePauseV1, StepPauseV1,
	MatterResumeV1, StageResumeV1, StepResumeV1, MatterCancelV1, StageCancelV1,
	StepCancelV1, StageFinishV1, GateDeclareV1, GateCloseV1, GateDismissV1,
	GateExemptionRepairV1,
}

// Catalogue returns the currently defined semantic operations. It is not the
// Cobra manifest: operations enter this list only as their semantic contracts
// are made concrete and migrated behind the in-process boundary.
func Catalogue() []Definition {
	return append([]Definition(nil), catalogue...)
}
