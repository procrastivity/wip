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
	TypeBacklogEntered   = "backlog.entered"
	TypeBacklogPlanned   = "backlog.planned"
	TypeBacklogDeclined  = "backlog.declined"
	TypeBacklogDelegated = "backlog.delegated"

	// Reference — ships in P1, inert until P3.
	TypeReferenceBound   = "reference.bound"
	TypeReferenceAdded   = "reference.added"
	TypeReferenceRemoved = "reference.removed"
	TypeReferenceRebound = "reference.rebound"

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
	TypeBatchDismissed = "batch.dismissed"
	TypeBatchSwept     = "batch.swept"
	TypeDispatchOpened = "dispatch.opened"
	TypeDispatchClosed = "dispatch.closed"

	TypeRunStarted   = "run.started"
	TypeRunSkipped   = "run.skipped"
	TypeRunResumed   = "run.resumed"
	TypeRunFinished  = "run.finished"
	TypeRunStoodDown = "run.stood-down"

	// Role — execution events (MODEL §6, §7). subject = the role instance's
	// own ULID; the instance keys at Clone and binds to the Dispatch that
	// spawned it (D59).
	TypeRoleSpawned = "role.spawned"
	TypeRoleClosed  = "role.closed"

	// Tracker — provider-neutral delivery facts. Provider-specific names and
	// states never enter this family.
	TypeTrackerItemCreated = "tracker.item-created"
	TypeTrackerStatePushed = "tracker.state-pushed"
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
	FamilyRun        Family = "run"
	FamilyRole       Family = "role"
	FamilyTracker    Family = "tracker"
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

// V2Taxonomy is the run.* event family seeded by the v2 migration. Like
// P1Taxonomy, it is frozen once shipped.
var V2Taxonomy = []EventType{
	execution(TypeRunStarted, FamilyRun),
	execution(TypeRunSkipped, FamilyRun),
	execution(TypeRunResumed, FamilyRun),
	execution(TypeRunFinished, FamilyRun),
	execution(TypeRunStoodDown, FamilyRun),
}

// V3Taxonomy is the Batch lifecycle-close pair seeded by the v3 migration.
// Like P1Taxonomy, it is frozen once shipped.
var V3Taxonomy = []EventType{
	batchScoped(TypeBatchDismissed),
	batchScoped(TypeBatchSwept),
}

// V4Taxonomy is the role pair seeded by the v4 migration. Like P1Taxonomy, it
// is frozen once shipped.
var V4Taxonomy = []EventType{
	execution(TypeRoleSpawned, FamilyRole),
	execution(TypeRoleClosed, FamilyRole),
}

// V5Taxonomy is the provider-neutral tracker substrate. Like every earlier
// taxonomy slice, it is frozen when its migration ships.
var V5Taxonomy = []EventType{
	durable(TypeReferenceAdded, FamilyReference),
	durable(TypeReferenceRemoved, FamilyReference),
	durable(TypeReferenceRebound, FamilyReference),
	durable(TypeBacklogDelegated, FamilyBacklog),
	durable(TypeTrackerStatePushed, FamilyTracker),
}

// V6Taxonomy records successful creation at the provider seam. It is separate
// from v5 because numbered taxonomy migrations are immutable once committed.
var V6Taxonomy = []EventType{
	durable(TypeTrackerItemCreated, FamilyTracker),
}

// taxonomySets is one slice per numbered migration that seeds event types.
// P1Taxonomy is v1's frozen content and is never edited: a later phase adds its
// types as a *new* slice, seeded by its own migration and appended here. That is
// what keeps "never edit a shipped migration; append" structural rather than
// remembered — the migration's INSERT is generated from the same slice that
// stamp-time validation reads, so the two cannot drift.
var taxonomySets = [][]EventType{P1Taxonomy, V2Taxonomy, V3Taxonomy, V4Taxonomy, V5Taxonomy, V6Taxonomy}

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

// RunSkipReason is why a Run was skipped rather than started.
type RunSkipReason string

// The three skip reasons.
const (
	RunSkipContention RunSkipReason = "contention"
	RunSkipBlocked    RunSkipReason = "blocked"
	RunSkipFailed     RunSkipReason = "failed"
)

// The three close reasons.
const (
	CloseCompleted  CloseReason = "completed"
	CloseSuperseded CloseReason = "superseded"
	CloseReaped     CloseReason = "reaped"
)

// BatchCloseReason is why a Batch left the live state (run-model F5/F7).
type BatchCloseReason string

// The two Batch close reasons.
const (
	BatchDismissed BatchCloseReason = "dismissed"
	BatchSwept     BatchCloseReason = "swept"
)

// RoleName is one of the six roles (MODEL §6). The `actor` column stays an
// open-ended token — new roles arrive without a migration — but a role *row*
// is one of these: the taxonomy is a fact of the model, and a row the model
// cannot name is a typo, not a new role.
type RoleName string

// The six roles. Inner loop — spawned to do a bounded thing, then close.
// Outer loop — watchers attending gates that answer asynchronously (D14).
const (
	RoleOrchestrator RoleName = "orchestrator"
	RoleCoordinator  RoleName = "coordinator"
	RoleResearcher   RoleName = "researcher"
	RoleBuilder      RoleName = "builder"
	RoleVerifier     RoleName = "verifier"
	RoleWarden       RoleName = "warden"
)

// KnownRole reports whether name is one of the six roles.
func KnownRole(name RoleName) bool {
	switch name {
	case RoleOrchestrator, RoleCoordinator, RoleResearcher, RoleBuilder,
		RoleVerifier, RoleWarden:
		return true
	default:
		return false
	}
}

// OuterLoop reports whether a role is a gate-owning watcher. Everything else
// known is inner loop.
func (n RoleName) OuterLoop() bool { return n == RoleVerifier || n == RoleWarden }

// Actor is the role's own actor token.
func (n RoleName) Actor() Actor { return RoleActor(string(n)) }

// GateOwner reports the outer-loop role that owns a gate (D14: the gate list
// and the outer-loop role list are one list from two sides). A gate no role
// owns — `reviewed-local`, and any name this table does not know — is
// human-owned, and false says so.
func GateOwner(gate string) (RoleName, bool) {
	switch gate {
	case "verified":
		return RoleVerifier, true
	case "reviewed", "ci-green":
		return RoleWarden, true
	default:
		return "", false
	}
}

// OwnedGates is GateOwner from the role's side: the gates whose declaration
// activates this role. Empty for every inner-loop role.
func (n RoleName) OwnedGates() []string {
	switch n {
	case RoleVerifier:
		return []string{"verified"}
	case RoleWarden:
		return []string{"reviewed", "ci-green"}
	default:
		return nil
	}
}

// RoleCloseReason is why a role instance closed: `completed` when its bounded
// thing is done or its watch ended, `reaped` when the Dispatch it was bound to
// closed under it.
type RoleCloseReason string

// The two role close reasons.
const (
	RoleCloseCompleted RoleCloseReason = "completed"
	RoleCloseReaped    RoleCloseReason = "reaped"
)
