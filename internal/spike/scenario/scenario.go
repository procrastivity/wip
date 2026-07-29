// Package scenario is the shared spike harness for the `store-fork` Matter
// (MODEL D46). It pins the one scope both spikes implement — create a Matter,
// add a Step under it, query "what is in progress" (MODEL §1) — so the two
// shapes are compared against identical work, not against each other's
// interpretation of the work.
//
// Nothing here is production code. The harness is deliberately shape-neutral:
// it names no table, no event stream, no projection. It says what a caller can
// ask a store to do and what the store must be able to say afterwards; how the
// bytes are arranged is exactly the question the fork is open on.
package scenario

import (
	"context"
	"encoding/json"
	"time"
)

// Lifecycle is MODEL §2.2's lifecycle, reduced to the three states the pinned
// scope reaches. Canceled and Paused exist in the model and are deliberately
// out of scope here.
type Lifecycle string

// The three lifecycle states in scope.
const (
	Planned    Lifecycle = "planned"
	InProgress Lifecycle = "in-progress"
	Done       Lifecycle = "done"
)

// Kind distinguishes the two node scales in scope. Stage is out of scope.
type Kind string

// The two node kinds in scope.
const (
	KindMatter Kind = "matter"
	KindStep   Kind = "step"
)

// Node is what a store reports about a node. Identity is the ULID; Locator is
// the human-facing, mutable name (`step-01`) that MODEL §10 forbids events
// from referencing.
type Node struct {
	ID        string
	Kind      Kind
	Parent    string // empty for a Matter
	Locator   string // "matter" or "step-NN" — a locator, never an identity
	Title     string
	Lifecycle Lifecycle
}

// Actor is who or what performed a verb. MODEL §10 requires system-driven
// events to be first-class — "a Builder closing and CI going red are events,
// or outer-loop roles are invisible to Session" — which is not expressible
// unless the envelope can say who acted.
//
// One column, a prefixed token: bare `human`, `role:<name>` for an inner- or
// outer-loop role, `system:<source>` for anything wip or a foreign system did
// on its own initiative. New roles arrive in Phase 2 without a migration.
type Actor string

// The actor vocabulary in scope. Roles beyond these follow the same prefixes.
const (
	ActorHuman Actor = "human"
	// ActorSystemWip marks an event wip emitted on its own initiative rather
	// than because it was asked to — in this scope, exactly D57's auto-start
	// of a Planned ancestor.
	ActorSystemWip   Actor = "system:wip"
	ActorRoleBuilder Actor = "role:builder"
)

// Event is MODEL §10's envelope. Both spikes write this identically — that it
// does not branch on the fork is the point of the contract being
// shape-neutral. `payload` carries type-specific fields.
//
// Actor, Causation and Correlation are a post-decision amendment: both spikes
// independently found the envelope unable to say *who* acted or *why*, and the
// three fields were ratified after the fork closed. See
// docs/store-fork/scenario.md §"Amendment".
type Event struct {
	ID          string          // the event's own ULID, monotonic — doubles as total order
	Type        string          // a token from the taxonomy below
	OccurredAt  time.Time       // UTC
	Actor       Actor           // who performed the verb — never empty
	Causation   string          // ULID of the event that entailed this one; own ID if this is an origin
	Correlation string          // ULID of the origin event of this chain; own ID if this is an origin
	Repo        string          // Repo ULID — always present for every type in scope
	Clone       string          // Clone ULID — execution events only; empty for every type in scope
	Worktree    string          // Worktree ULID — execution events only; empty for every type in scope
	Subject     string          // ULID of the entity the event is about — identity, never a locator
	Payload     json.RawMessage // type-specific fields
}

// IsOrigin reports whether an event began its own chain — the a:a:a form, in
// which all three identity fields are the event's own ULID.
func (e Event) IsOrigin() bool {
	return e.ID == e.Causation && e.ID == e.Correlation
}

// The event taxonomy in scope. Six types, one per verb-and-scale pair. Later
// phases add types; the envelope never changes (MODEL §10).
const (
	TypeMatterCreated  = "matter.created"
	TypeStepCreated    = "step.created"
	TypeMatterStarted  = "matter.started"
	TypeStepStarted    = "step.started"
	TypeMatterFinished = "matter.finished"
	TypeStepFinished   = "step.finished"
)

// Env is the tier context a store is opened against. The pinned scope excludes
// tier *resolution* (no remote-URL normal form, no git-common-dir probing —
// that is `tiers`' work); the dimensions themselves are not excluded, because
// MODEL §10 lists them as fidelity that binds from event one and cannot be
// retrofitted. So a store is handed three fixed ULIDs at open time and is
// expected to stamp them on events per the static per-type rule below.
type Env struct {
	Repo     string
	Clone    string
	Worktree string
}

// Store is the shared scenario contract. Every method is a verb; every verb
// emits exactly one event per node it mutates (MODEL §10 invariant 1).
//
// Start is in scope even though the workplan's scope line names only three
// operations: MODEL §2.2 is explicit that a merely-planned Matter never
// reports In Progress, so without a start verb "query in-progress" has no
// discriminating answer. Finish is in scope for the same reason from the other
// side — the query must exclude Done, not merely include everything born.
// Gates, Session, tiers resolution, `blocked-by`, Stage, Cancel and Pause are
// all out of scope, per the workplan.
//
// Every verb takes the actor performing it. Events wip emits on its own
// initiative within a command — D57's ancestor auto-start — carry
// ActorSystemWip instead, decided by the store, never by the caller.
type Store interface {
	// CreateMatter births a Matter in Planned and returns its ULID.
	// Emits exactly one matter.created.
	CreateMatter(ctx context.Context, actor Actor, title string) (string, error)

	// AddStep births a Step under a Matter in Planned and returns its ULID.
	// The locator (`step-NN`) is assigned by the store, sequential within the
	// Matter. Emits exactly one step.created.
	AddStep(ctx context.Context, actor Actor, matterID, title string) (string, error)

	// Start moves a node to In Progress, auto-starting Planned ancestors
	// ancestor-first (D57). One command, several verbs, each emitting exactly
	// one event: starting a Step under a Planned Matter emits matter.started
	// then step.started, in that order. Starting an already-started node is an
	// error, not a silent no-op — it would emit a second event for one
	// transition.
	//
	// The first event of the command is the chain origin (a:a:a); the events
	// that follow carry the id of the event that entailed them. In this scope
	// that means the ancestor's start entails the Step's start.
	Start(ctx context.Context, actor Actor, nodeID string) error

	// Finish moves a node to Done. Ancestors are not auto-finished (MODEL has
	// no such rule; only start cascades). Emits exactly one *.finished.
	Finish(ctx context.Context, actor Actor, nodeID string) error

	// InProgress answers the founding question (MODEL §1) — every node whose
	// lifecycle is In Progress, in a stable order (creation order). This is
	// MODEL §10 invariant 2's subject: it must be a SELECT or a maintained
	// projection, never ad-hoc replay.
	InProgress(ctx context.Context) ([]Node, error)

	// Events returns the full event log in total order. It exists so the
	// harness can hold invariant 1 to its word; no production read path is
	// implied.
	Events(ctx context.Context) ([]Event, error)

	// Close releases the store.
	Close() error
}

// RequiredDimensions reports which tier dimensions an event type must carry.
// D56: required dimensions are a static function of event type, never a
// runtime condition. Every type in the pinned scope is a durable-write event —
// repo always, clone and worktree never. (Execution events — render, cursor,
// dispatch — are out of scope; they are where clone and worktree become
// required, which is why the rule is a function and not a constant.)
func RequiredDimensions(eventType string) (repo, clone, worktree bool) {
	switch eventType {
	case TypeMatterCreated, TypeStepCreated,
		TypeMatterStarted, TypeStepStarted,
		TypeMatterFinished, TypeStepFinished:
		return true, false, false
	default:
		return true, false, false
	}
}
