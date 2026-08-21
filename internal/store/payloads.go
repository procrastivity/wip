package store

// The payloads. One struct per event type or per family of types with the same
// type-specific fields, so a caller never hand-rolls JSON and the projection
// never guesses at a shape.
//
// A locator may appear in a payload — it is a fact about the write, and MODEL
// §10 permits that explicitly. What a payload may never be is what an event
// *references*: that is `subject`, and it is always an identity.

// NodeBirth is the payload of matter.created / stage.created / step.created and
// of step.inserted. The node's kind comes from the event type and its repo from
// the envelope, so neither is repeated here; its Matter is derived from Parent
// by the projection, so a payload cannot disagree with the tree.
type NodeBirth struct {
	Title   string `json:"title"`
	Locator string `json:"locator"`
	// Parent is empty for a Matter and required for anything else.
	Parent string `json:"parent,omitempty"`
	// SortKey is presentation-only (D51): sibling position, carrying no
	// execution-ordering meaning at all.
	SortKey int64 `json:"sort_key"`
}

// Transition is the payload of every lifecycle event. From is not decoration:
// the projection updates the row only if it is still in that state, so a
// transition that did not happen becomes a write-time error rather than an
// event describing a move the store never made.
type Transition struct {
	From Lifecycle `json:"from"`
	To   Lifecycle `json:"to"`
	// Cascade marks a node wip started on its own initiative because a
	// descendant was started (D57). The actor is still the caller — the command
	// is theirs and so is its whole chain — and this is where the fact that they
	// did not name this node is recorded. See docs/schema/decisions.md.
	Cascade bool `json:"cascade,omitempty"`
	// TrackerPushLevel snapshots candidate policy at the causal boundary so a
	// rebuild never consults mutable current configuration.
	TrackerPushLevel TrackerPushLevel `json:"tracker_push_level,omitempty"`
	// Reason is set only by cancel, recording why the work ended without
	// sealing. `omitempty` keeps every reasonless transition payload
	// byte-identical to the old shape.
	Reason string `json:"reason,omitempty"`
}

// ContentWritten is the payload of content.created and content.appended. The
// bytes travel in the event, because the log is the source of truth for all
// content including prose (D36, D61) — except when they spilled, in which case
// the reference travels and the bytes live in a sidecar file the store owns.
type ContentWritten struct {
	Kind ContentKind `json:"kind"`
	// Content is the content row's own identity. Append kinds accumulate as
	// segments, one per event, ordered by this identity.
	Content string `json:"content"`
	// Bytes carries the content itself, and is explicitly null rather than
	// omitted when the bytes spilled. `omitempty` would be wrong here in a way
	// that matters: an empty byte slice and an absent one encode identically
	// under it, so a zero-length content object would come back off the wire
	// looking like a spilled one with no reference — which the projection then
	// refuses with the wrong reason. The schema permits zero length
	// (`byte_len >= 0`), so the payload has to be able to say it.
	Bytes []byte `json:"bytes"`
	// BlobRef names the sidecar file under the store's blobs directory, set
	// exactly when Bytes is absent.
	BlobRef string `json:"blob_ref,omitempty"`
	ByteLen int64  `json:"byte_len"`
	SHA256  string `json:"sha256"`
}

// Reordered is the payload of step.reordered. subject is the parent whose
// children moved; Order is every live child, in the order they should present.
// Reordering is inherently about a set, and carrying the whole set is what makes
// the log narrate the result rather than a delta.
type Reordered struct {
	Order []string `json:"order"`
}

// Replaced is the payload of step.replaced. subject is the Step being replaced:
// it is tombstoned and the replacement is born in its sibling position, which is
// the amendment D44's identity rule made expressible.
type Replaced struct {
	Replacement string `json:"replacement"`
	Title       string `json:"title"`
	Locator     string `json:"locator"`
}

// Removed is the payload of step.removed. The row is tombstoned, never deleted:
// its identity is never reissued and every prior event referencing it stays a
// valid reference.
type Removed struct {
	Reason string `json:"reason,omitempty"`
}

// DependencyChange is the payload of dependency.added and dependency.removed.
// subject is the *blocked* node — the node the verb was invoked on — and the
// edge carries its own identity.
type DependencyChange struct {
	Edge    string `json:"edge"`
	Blocker string `json:"blocker"`
}

// GateClosed is the payload of gate.closed: gate name + scale + subject, where
// the subject is the envelope's.
type GateClosed struct {
	Gate             string           `json:"gate"`
	Scale            Scale            `json:"scale"`
	TrackerPushLevel TrackerPushLevel `json:"tracker_push_level,omitempty"`
}

// BacklogEntered is the payload of backlog.entered. subject is the entry.
type BacklogEntered struct {
	Provenance Provenance `json:"provenance"`
	Title      string     `json:"title"`
	// Detail carries why, which `deferred` provenance needs and the others may
	// leave empty.
	Detail string `json:"detail,omitempty"`
	// OriginNode is the node a mid-flight discovery was found in.
	OriginNode string `json:"origin_node,omitempty"`
}

// BacklogPlanned is the payload of backlog.planned: the Matter the entry became.
type BacklogPlanned struct {
	Matter string `json:"matter"`
}

// BacklogDeclined is the payload of backlog.declined. Declined must be
// distinguishable from not-yet-acted-upon (MODEL §4), and a reason is what makes
// it so at the point of the decision rather than in a retro.
type BacklogDeclined struct {
	Reason string `json:"reason"`
}

// BacklogDelegated is the third backlog exit. Outbox is the durable creation
// entry that owns every retry, and IdempotencyKey is the provider-neutral key
// that prevents a retry from creating a second external item.
type BacklogDelegated struct {
	Outbox         string `json:"outbox"`
	IdempotencyKey string `json:"idempotency_key"`
}

// ReferenceBound is the payload of reference.bound. References are acquired, not
// assigned (MODEL §5): a late, mutable update to a nullable column keyed on
// identity. Inert until P3.
type ReferenceBound struct {
	Ref string `json:"ref"`
}

// ReferenceAdded and ReferenceRemoved change one member of a Matter's active
// tracker-reference set. The subject is always the Matter.
type ReferenceAdded struct {
	Ref              string           `json:"ref"`
	TrackerPushLevel TrackerPushLevel `json:"tracker_push_level,omitempty"`
}

// ReferenceRemoved identifies the membership that leaves the active set.
type ReferenceRemoved struct {
	Ref              string           `json:"ref"`
	TrackerPushLevel TrackerPushLevel `json:"tracker_push_level,omitempty"`
}

// ReferenceRebound atomically replaces one member of a Matter's reference set.
type ReferenceRebound struct {
	From             string           `json:"from"`
	To               string           `json:"to"`
	TrackerPushLevel TrackerPushLevel `json:"tracker_push_level,omitempty"`
}

// TrackerDisposition is the monotonic provider-neutral state recorded at the
// seam. A provider adapter maps these values to its own state vocabulary.
type TrackerDisposition string

const (
	// TrackerActive means at least one bound Matter is in progress.
	TrackerActive TrackerDisposition = "active"
	// TrackerCompleted means all bound Matters are terminal and at least one sealed.
	TrackerCompleted TrackerDisposition = "completed"
	// TrackerCanceled means all bound Matters were canceled.
	TrackerCanceled TrackerDisposition = "canceled"
)

// TrackerStatePushed records one successful, lease-guarded state delivery.
// The event subject is the outbox entry. Ref identifies the shared external
// item; Lease is the opaque provider token established by the successful
// conditional write.
type TrackerStatePushed struct {
	Ref         string             `json:"ref"`
	Disposition TrackerDisposition `json:"disposition"`
	Lease       string             `json:"lease"`
}

// TrackerStateObserved records that a provider was already at the requested
// disposition. The event subject is the outbox entry. Ref identifies the
// external item, and Lease is the opaque token read with the observed state.
type TrackerStateObserved struct {
	Ref         string             `json:"ref"`
	Disposition TrackerDisposition `json:"disposition"`
	Lease       string             `json:"lease"`
}

// TrackerItemCreated records an unambiguous successful response for one
// delegated creation entry. Ref remains in the event log after retirement.
type TrackerItemCreated struct {
	Ref string `json:"ref"`
}

// OutboxApproved records the human boundary before a provider seam call.
type OutboxApproved struct{}

// OutboxDeclined records a terminal human disposition.
type OutboxDeclined struct {
	Reason string `json:"reason"`
}

// WithholdCause is the machine-readable reason that an outbox entry cannot be
// sent. Reason remains free prose for an operator.
type WithholdCause string

// The complete set of withholding causes.
const (
	CauseMalformedCandidate       WithholdCause = "malformed-candidate"
	CauseLocalRegression          WithholdCause = "local-regression"
	CauseSuperseded               WithholdCause = "superseded"
	CauseLeaseMismatch            WithholdCause = "lease-mismatch"
	CausePermanentRefusal         WithholdCause = "permanent-refusal"
	CauseMalformedProviderSuccess WithholdCause = "malformed-provider-success"
	CauseUnknownOutcome           WithholdCause = "unknown-outcome"
)

// OutboxWithheld records work that must remain visible and must not be sent.
// Attempted distinguishes a local composition/monotonicity refusal from a
// provider response; only a provider call increments Attempts. Cause is
// optional because events written before the discriminator do not carry it.
type OutboxWithheld struct {
	Reason    string        `json:"reason"`
	Attempted bool          `json:"attempted"`
	Cause     WithholdCause `json:"cause,omitempty"`
}

// OutboxDeliveryFailed records a retryable provider response.
type OutboxDeliveryFailed struct {
	Reason string `json:"reason"`
}

// OutboxRetried records an explicit decision to send failed or withheld work
// again. It preserves the entry identity, payload, and idempotency key.
type OutboxRetried struct{}

// OutboxFlushed records successful delivery for an entry whose tracker fact
// needs no richer event. State and create success retain their existing events.
type OutboxFlushed struct{}

// RepoAttached is the payload of repo.attached. RemoteURL is the normalised
// remote in its normal form, and is empty for a local-only repo — the natural
// key is nullable (D37, D39).
type RepoAttached struct {
	RemoteURL string `json:"remote_url,omitempty"`
	// IdentityRemote is which remote name the URL came from (`origin` by
	// convention), so origin-is-my-fork has an explicit answer rather than a
	// default.
	IdentityRemote string `json:"identity_remote,omitempty"`
	Label          string `json:"label,omitempty"`
}

// CloneAttached is the payload of clone.attached. The git-common-dir is the
// Clone's natural key: it resolves identically from a subdirectory and from a
// linked worktree.
type CloneAttached struct {
	Repo         string `json:"repo"`
	GitCommonDir string `json:"git_common_dir"`
	Label        string `json:"label"`
}

// WorktreeAttached is the payload of worktree.attached. Name is empty for the
// main worktree — D37's null case.
type WorktreeAttached struct {
	Clone string `json:"clone"`
	Name  string `json:"name,omitempty"`
}

// RepoKeyAdopted is the payload of repo.key-adopted: a local-only Repo gaining a
// remote. It keeps its ULID and its whole history, because nothing keys on the
// natural key (D37).
type RepoKeyAdopted struct {
	RemoteURL      string `json:"remote_url"`
	IdentityRemote string `json:"identity_remote,omitempty"`
}

// CloneRelinked is the payload of clone.relinked: the same clone at a new
// git-common-dir.
type CloneRelinked struct {
	GitCommonDir string `json:"git_common_dir"`
}

// CloneLabeled is the payload of clone.labeled.
type CloneLabeled struct {
	Label string `json:"label"`
}

// CursorMoved is the payload of cursor.moved. subject is the Worktree the cursor
// belongs to; the Clone is the event's own dimension, which is what makes the
// cursor keyed at Clone + Worktree (D38). Node is empty when the cursor is
// cleared. The cursor is attention, never a fact about the work.
type CursorMoved struct {
	Node     string `json:"node,omitempty"`
	Previous string `json:"previous,omitempty"`
}

// BatchCreated is the payload of batch.created. A named Batch carries Name;
// an anonymous Batch carries its associated Matter identity.
type BatchCreated struct {
	Name   string `json:"name,omitempty"`
	Matter string `json:"matter,omitempty"`
}

// BatchMembership is the payload of batch.joined and batch.left. subject is the
// Batch; membership implies nothing and is idempotent per (batch, matter) (D58).
type BatchMembership struct {
	Matter string `json:"matter"`
}

// BatchDismissedPayload is the empty payload of batch.dismissed.
type BatchDismissedPayload struct{}

// BatchSweptPayload is the empty payload of batch.swept.
type BatchSweptPayload struct{}

// DispatchOpened is the payload of dispatch.opened. Empty Run and Matter keep
// the P1 shape; both fields are required for a P2 Run dispatch.
type DispatchOpened struct {
	Run    string `json:"run,omitempty"`
	Matter string `json:"matter,omitempty"`
}

// DispatchClosed is the payload of dispatch.closed. Session discounts every
// reason but `completed`, whose occurred_at is the only one that is evidence of
// work rather than administration (D59).
type DispatchClosed struct {
	Reason CloseReason `json:"reason"`
}

// RunStarted is the payload of run.started: the Batch this pass covers and the
// frozen Matter set at start.
type RunStarted struct {
	Batch   string   `json:"batch"`
	Locator string   `json:"locator"`
	Matters []string `json:"matters"`
}

// RunSkipped is the payload of run.skipped.
type RunSkipped struct {
	Run    string        `json:"run"`
	Reason RunSkipReason `json:"reason"`
}

// RunResumed is the empty payload of run.resumed.
type RunResumed struct{}

// RunFinished is the empty payload of run.finished.
type RunFinished struct{}

// RunStoodDown is the payload of run.stood-down. The event Clone is the acting
// Clone; OwningClone is the Run owner — F9 allows them to differ.
type RunStoodDown struct {
	ActingClone string `json:"acting_clone"`
	OwningClone string `json:"owning_clone"`
}

// RoleSpawned is the payload of role.spawned. subject is the role instance;
// Dispatch is the bracket it is bound to (D59) and Name is which of the six
// roles it is (MODEL §6).
type RoleSpawned struct {
	Dispatch string   `json:"dispatch"`
	Name     RoleName `json:"name"`
}

// RoleClosed is the payload of role.closed.
type RoleClosed struct {
	Reason RoleCloseReason `json:"reason"`
}

// RenderPerformed is the payload of render.performed. Nothing is projected from
// it: the render target is a projection of the store and never a source (D36,
// D40), so the event exists to be read as history, not folded into state.
type RenderPerformed struct {
	Target string `json:"target,omitempty"`
}
