package store

import "fmt"

// The P1 event taxonomy (MODEL §10, the `schema` Brief §B). Later phases add
// types; the envelope never changes.
//
// Every type below is a row in the `event_types` table, and `events.type` is a
// foreign key into it — so an unknown type is refused by the database rather
// than logged, and D56's "required tier dimensions are a static function of
// event type, checked in schema" is satisfied literally rather than by
// convention. Adding a type in a later phase is one INSERT inside a numbered
// migration; the table below is the single source both the seed data and the
// stamp-time dimension rule are derived from.
const (
	// Lifecycle — uniform at every scale (MODEL §2.2). subject = node ULID.
	TypeMatterCreated  = "matter.created"
	TypeStageCreated   = "stage.created"
	TypeStepCreated    = "step.created"
	TypeMatterStarted  = "matter.started"
	TypeStageStarted   = "stage.started"
	TypeStepStarted    = "step.started"
	TypeMatterFinished = "matter.finished"
	TypeStageFinished  = "stage.finished"
	TypeStepFinished   = "step.finished"
	TypeMatterCanceled = "matter.canceled"
	TypeStageCanceled  = "stage.canceled"
	TypeStepCanceled   = "step.canceled"
	TypeMatterPaused   = "matter.paused"
	TypeStagePaused    = "stage.paused"
	TypeStepPaused     = "step.paused"
	TypeMatterResumed  = "matter.resumed"
	TypeStageResumed   = "stage.resumed"
	TypeStepResumed    = "step.resumed"

	// Content — subject = the owning node; payload.kind discriminates.
	TypeContentCreated  = "content.created"
	TypeContentAppended = "content.appended"

	// Amendment — plan structure. Removal leaves a tombstone (D44).
	TypeStepInserted  = "step.inserted"
	TypeStepReordered = "step.reordered"
	TypeStepReplaced  = "step.replaced"
	TypeStepRemoved   = "step.removed"

	// Dependency — `blocked-by` edges. Cycles are refused statically, never
	// recorded as events.
	TypeDependencyAdded   = "dependency.added"
	TypeDependencyRemoved = "dependency.removed"

	// Gate — the only gate event in P1; declaring a gate is config, not an
	// event (D4, D54).
	TypeGateClosed = "gate.closed"

	// Intake/backlog.
	TypeBacklogEntered  = "backlog.entered"
	TypeBacklogPlanned  = "backlog.planned"
	TypeBacklogDeclined = "backlog.declined"

	// Reference — ships in P1, inert until P3.
	TypeReferenceBound = "reference.bound"

	// Tier — emitted by `tiers`.
	TypeRepoAttached     = "repo.attached"
	TypeCloneAttached    = "clone.attached"
	TypeWorktreeAttached = "worktree.attached"
	TypeRepoKeyAdopted   = "repo.key-adopted"
	TypeCloneRelinked    = "clone.relinked"
	TypeCloneLabeled     = "clone.labeled"

	// Render — emitted by `render-scratch`.
	TypeRenderPerformed = "render.performed"

	// Cursor — emitted by `read-surface`. subject = the Worktree the cursor
	// belongs to; the cursor itself is keyed at Clone + Worktree (D38).
	TypeCursorMoved = "cursor.moved"

	// Batch/dispatch — execution events. `batch.*` carries a null repo (D56).
	TypeBatchCreated   = "batch.created"
	TypeBatchJoined    = "batch.joined"
	TypeBatchLeft      = "batch.left"
	TypeDispatchOpened = "dispatch.opened"
	TypeDispatchClosed = "dispatch.closed"
)

// Family groups event types for auditing (MODEL §10's family list). It is
// recorded as data next to each type so `events-traces`' fidelity audit can
// ask the store which families it has actually produced.
type Family string

// The P1 families.
const (
	FamilyLifecycle  Family = "lifecycle"
	FamilyContent    Family = "content"
	FamilyAmendment  Family = "amendment"
	FamilyDependency Family = "dependency"
	FamilyGate       Family = "gate"
	FamilyBacklog    Family = "backlog"
	FamilyReference  Family = "reference"
	FamilyTier       Family = "tier"
	FamilyRender     Family = "render"
	FamilyCursor     Family = "cursor"
	FamilyBatch      Family = "batch"
	FamilyDispatch   Family = "dispatch"
)

// EventType is one row of the taxonomy: a type token, its family, and the tier
// dimensions it requires. The three booleans are D56's total function.
type EventType struct {
	Type   string
	Family Family

	// Repo, Clone and Worktree report which tier dimensions this type must
	// carry. Durable-object events carry repo only; execution events carry all
	// three; `batch.*` alone carries clone and worktree with a null repo,
	// because a Batch keys at no tier (D39, D56).
	Repo, Clone, Worktree bool
}

// durable, execution and batchScoped spell the three dimension rules once, so
// a new type joins a rule rather than restating one.
func durable(t string, f Family) EventType {
	return EventType{Type: t, Family: f, Repo: true}
}

func execution(t string, f Family) EventType {
	return EventType{Type: t, Family: f, Repo: true, Clone: true, Worktree: true}
}

func batchScoped(t string) EventType {
	return EventType{Type: t, Family: FamilyBatch, Clone: true, Worktree: true}
}

// P1Taxonomy is the whole P1 list. It is both the seed for the `event_types`
// table (schema_v1.go) and the source of the stamp-time dimension rule
// (requiredDimensions) — one list, two uses, so the Go rule and the database
// check can never drift apart.
var P1Taxonomy = []EventType{
	durable(TypeMatterCreated, FamilyLifecycle),
	durable(TypeStageCreated, FamilyLifecycle),
	durable(TypeStepCreated, FamilyLifecycle),
	durable(TypeMatterStarted, FamilyLifecycle),
	durable(TypeStageStarted, FamilyLifecycle),
	durable(TypeStepStarted, FamilyLifecycle),
	durable(TypeMatterFinished, FamilyLifecycle),
	durable(TypeStageFinished, FamilyLifecycle),
	durable(TypeStepFinished, FamilyLifecycle),
	durable(TypeMatterCanceled, FamilyLifecycle),
	durable(TypeStageCanceled, FamilyLifecycle),
	durable(TypeStepCanceled, FamilyLifecycle),
	durable(TypeMatterPaused, FamilyLifecycle),
	durable(TypeStagePaused, FamilyLifecycle),
	durable(TypeStepPaused, FamilyLifecycle),
	durable(TypeMatterResumed, FamilyLifecycle),
	durable(TypeStageResumed, FamilyLifecycle),
	durable(TypeStepResumed, FamilyLifecycle),

	durable(TypeContentCreated, FamilyContent),
	durable(TypeContentAppended, FamilyContent),

	durable(TypeStepInserted, FamilyAmendment),
	durable(TypeStepReordered, FamilyAmendment),
	durable(TypeStepReplaced, FamilyAmendment),
	durable(TypeStepRemoved, FamilyAmendment),

	durable(TypeDependencyAdded, FamilyDependency),
	durable(TypeDependencyRemoved, FamilyDependency),

	durable(TypeGateClosed, FamilyGate),

	durable(TypeBacklogEntered, FamilyBacklog),
	durable(TypeBacklogPlanned, FamilyBacklog),
	durable(TypeBacklogDeclined, FamilyBacklog),

	durable(TypeReferenceBound, FamilyReference),

	durable(TypeRepoAttached, FamilyTier),
	durable(TypeCloneAttached, FamilyTier),
	durable(TypeWorktreeAttached, FamilyTier),
	durable(TypeRepoKeyAdopted, FamilyTier),
	durable(TypeCloneRelinked, FamilyTier),
	durable(TypeCloneLabeled, FamilyTier),

	execution(TypeRenderPerformed, FamilyRender),
	execution(TypeCursorMoved, FamilyCursor),
	execution(TypeDispatchOpened, FamilyDispatch),
	execution(TypeDispatchClosed, FamilyDispatch),

	batchScoped(TypeBatchCreated),
	batchScoped(TypeBatchJoined),
	batchScoped(TypeBatchLeft),
}

// taxonomySets is one slice per numbered migration that seeds event types.
// P1Taxonomy is v1's frozen content and is never edited: a later phase adds its
// types as a *new* slice, seeded by its own migration and appended here. That is
// what keeps "never edit a shipped migration; append" structural rather than
// remembered — the migration's INSERT is generated from the same slice that
// stamp-time validation reads, so the two cannot drift.
var taxonomySets = [][]EventType{P1Taxonomy}

// registeredTypes is every event type this binary knows about, across every
// taxonomy set.
func registeredTypes() []EventType {
	var all []EventType
	for _, set := range taxonomySets {
		all = append(all, set...)
	}
	return all
}

// taxonomyIndex is registeredTypes keyed by type, built once.
var taxonomyIndex = func() map[string]EventType {
	all := registeredTypes()
	index := make(map[string]EventType, len(all))
	for _, t := range all {
		if _, clash := index[t.Type]; clash {
			panic(fmt.Sprintf("store: %q appears twice in the taxonomy", t.Type))
		}
		index[t.Type] = t
	}
	return index
}()

// requiredDimensions reports which tier dimensions an event type must carry
// (D56). It is asked once, in Store.stamp, and never by a verb — a verb that
// could name a tier dimension is a verb that could get one wrong.
func requiredDimensions(eventType string) (EventType, error) {
	t, ok := taxonomyIndex[eventType]
	if !ok {
		return EventType{}, fmt.Errorf("store: %q is not a registered event type (the taxonomy is data: add it in a numbered migration)", eventType)
	}
	return t, nil
}

// Lifecycle is MODEL §2.2's lifecycle. Nothing else is a state: gates are not
// states, and a tombstone is not a state either (see tombstones in edges.go).
type Lifecycle string

// The five lifecycle states, uniform at every scale.
const (
	Planned    Lifecycle = "planned"
	InProgress Lifecycle = "in-progress"
	Done       Lifecycle = "done"
	Canceled   Lifecycle = "canceled"
	Paused     Lifecycle = "paused"
)

// Scale is a node's scale, and the scale a gate binds to (D12).
type Scale string

// The three scales. A Matter may be its own smallest node (D2), so every scale
// is optional except Matter.
const (
	ScaleMatter Scale = "matter"
	ScaleStage  Scale = "stage"
	ScaleStep   Scale = "step"
)

// ContentKind is the prose kind a content row carries (`payload.kind`).
type ContentKind string

// The four content kinds. Brief, Workplan and body are create-once; findings
// accumulate by append.
const (
	KindBrief    ContentKind = "brief"
	KindWorkplan ContentKind = "workplan"
	KindBody     ContentKind = "body"
	KindFindings ContentKind = "findings"
)

// AppendOnlyKind reports whether a content kind accumulates across
// content.appended rather than being written exactly once.
func (k ContentKind) AppendOnlyKind() bool { return k == KindFindings }

// Provenance is how a BacklogEntry arrived (D9).
type Provenance string

// The three provenances.
const (
	ProvenanceIntake   Provenance = "intake"
	ProvenanceFound    Provenance = "found"
	ProvenanceDeferred Provenance = "deferred"
)

// CloseReason is why a Dispatch closed (D59). Session discounts every reason
// but `completed`.
type CloseReason string

// The three close reasons.
const (
	CloseCompleted  CloseReason = "completed"
	CloseSuperseded CloseReason = "superseded"
	CloseReaped     CloseReason = "reaped"
)
