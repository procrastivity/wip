package operation

// NodeLifecycleInput identifies one exact M6 node. Reason is only meaningful
// for cancellation and is optional.
type NodeLifecycleInput struct {
	NodeID string
	Reason string
}

func (NodeLifecycleInput) operationInput() {}

// NodeLifecycleOutput reports a Stage transition without implying Matter
// sealing.
type NodeLifecycleOutput struct {
	NodeID   string
	MatterID string
	State    string
}

func (NodeLifecycleOutput) operationOutput() {}

// MatterLifecycleOutput reports a Matter-local transition. Matter finish has
// its separate BecameSealed result.
type MatterLifecycleOutput struct {
	MatterID string
	State    string
}

func (MatterLifecycleOutput) operationOutput() {}

// StepCancelInput adds the optional cancellation reason to an exact Step ID.
type StepCancelInput struct {
	StepID string
	Reason string
}

func (StepCancelInput) operationInput() {}

func claimLifecycleMetadata(name string, guards, writes []Footprint) Metadata {
	return Metadata{
		Operation: ID{Name: name, Version: 1}, Access: AccessMutation, Delivery: DeliveryClaim,
		RequiredContext: []ContextDimension{ContextRepo, ContextClone, ContextWorktree},
		Guards:          guards, Writes: writes, BlobInputs: []BlobSpec{}, Claim: ClaimExact,
		ExternalEffects: []ExternalEffect{},
	}
}

func matterLifecycleDefinition(name string) Definition {
	return mustDefine[NodeLifecycleInput, MatterLifecycleOutput](claimLifecycleMetadata(name,
		[]Footprint{FootprintMatterActiveClaim, FootprintMatterLifecycle}, []Footprint{FootprintMatterLifecycle}))
}

func stageLifecycleDefinition(name string, start bool) Definition {
	guards := []Footprint{FootprintMatterActiveClaim, FootprintStageLifecycle}
	writes := []Footprint{FootprintStageLifecycle}
	if start {
		guards = append(guards, FootprintAncestorLifecycle)
		writes = []Footprint{FootprintMatterLifecycle, FootprintStageLifecycle}
	}
	return mustDefine[NodeLifecycleInput, NodeLifecycleOutput](claimLifecycleMetadata(name, guards, writes))
}

func stepLifecycleVariant(name string) Definition {
	return mustDefine[StepLifecycleInput, StepLifecycleOutput](claimLifecycleMetadata(name,
		[]Footprint{FootprintMatterActiveClaim, FootprintStepLifecycle}, []Footprint{FootprintStepLifecycle}))
}

// MatterStartV1 through StageFinishV1 define the M6 lifecycle operation variants.
var (
	MatterStartV1  = matterLifecycleDefinition("matter.start")
	StageStartV1   = stageLifecycleDefinition("stage.start", true)
	MatterPauseV1  = matterLifecycleDefinition("matter.pause")
	StagePauseV1   = stageLifecycleDefinition("stage.pause", false)
	StepPauseV1    = stepLifecycleVariant("step.pause")
	MatterResumeV1 = matterLifecycleDefinition("matter.resume")
	StageResumeV1  = stageLifecycleDefinition("stage.resume", false)
	StepResumeV1   = stepLifecycleVariant("step.resume")
	MatterCancelV1 = mustDefine[NodeLifecycleInput, MatterLifecycleOutput](claimLifecycleMetadata("matter.cancel",
		[]Footprint{FootprintMatterActiveClaim, FootprintMatterLifecycle, FootprintCancelReason}, []Footprint{FootprintMatterLifecycle}))
	StageCancelV1 = mustDefine[NodeLifecycleInput, NodeLifecycleOutput](claimLifecycleMetadata("stage.cancel",
		[]Footprint{FootprintMatterActiveClaim, FootprintStageLifecycle, FootprintCancelReason}, []Footprint{FootprintStageLifecycle}))
	StepCancelV1 = mustDefine[StepCancelInput, StepLifecycleOutput](claimLifecycleMetadata("step.cancel",
		[]Footprint{FootprintMatterActiveClaim, FootprintStepLifecycle, FootprintCancelReason}, []Footprint{FootprintStepLifecycle}))
	StageFinishV1 = stageLifecycleDefinition("stage.finish", false)
)

var lifecycleVariantCatalogue = []Definition{
	MatterStartV1, StageStartV1, MatterPauseV1, StagePauseV1, StepPauseV1,
	MatterResumeV1, StageResumeV1, StepResumeV1, MatterCancelV1, StageCancelV1,
	StepCancelV1, StageFinishV1,
}

// Step5Catalogue returns the M6 lifecycle variants added in Step 5.
func Step5Catalogue() []Definition { return append([]Definition(nil), lifecycleVariantCatalogue...) }

// Step5Operation reports whether id belongs to the M6 Step 5 lifecycle set.
func Step5Operation(id ID) bool {
	for _, definition := range lifecycleVariantCatalogue {
		if definition.Metadata().Operation == id {
			return true
		}
	}
	return false
}
