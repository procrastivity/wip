package store

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
)

// experimentPortableMember reports whether the type belongs to the bounded
// portable slice routed through experimentClassifyPortable on a bound target:
// the four node births — step.inserted is the positioned-birth verb's event,
// the same NodeBirth fold as step.created — and the create-once content
// writes. The step amendment family (reorder/replace/remove), the lifecycle
// matrix, the backlog outcomes and content.appended are not here: like the
// lifecycle matrix they mutate an already-paired object, so experimentClassify
// admits them on both the bound and the unbound path.
func experimentPortableMember(typ string) bool {
	switch typ {
	case TypeMatterCreated, TypeStageCreated, TypeStepCreated, TypeStepInserted, TypeContentCreated:
		return true
	}
	return false
}

// experimentLifecycleEdge is the admitted shape of one lifecycle event type —
// exactly what writesurface's verb emits (MODEL §2.2): the payload's From/To
// are fixed per edge, Reason is set only where the verb records one (cancel
// alone says why work ended without sealing) and Cascade is set only where the
// verb moves ancestors on its own initiative (start alone). The terminal To
// values — done and canceled — are never reopened: the fold moves a node only
// while its live lifecycle equals From, so no admitted edge leaves them.
type experimentLifecycleEdge struct {
	verb     string
	from, to Lifecycle
	reason   bool
	cascade  bool
	kind     Scale
}

// experimentLifecycleEdges is the whole admitted lifecycle matrix, keyed by
// event type so the export whitelist and the import classifier share one
// source of truth and cannot drift apart.
var experimentLifecycleEdges = map[string]experimentLifecycleEdge{
	TypeMatterStarted:  {verb: "started", from: Planned, to: InProgress, cascade: true, kind: ScaleMatter},
	TypeStageStarted:   {verb: "started", from: Planned, to: InProgress, cascade: true, kind: ScaleStage},
	TypeStepStarted:    {verb: "started", from: Planned, to: InProgress, cascade: true, kind: ScaleStep},
	TypeMatterFinished: {verb: "finished", from: InProgress, to: Done, kind: ScaleMatter},
	TypeStageFinished:  {verb: "finished", from: InProgress, to: Done, kind: ScaleStage},
	TypeStepFinished:   {verb: "finished", from: InProgress, to: Done, kind: ScaleStep},
	TypeMatterCanceled: {verb: "canceled", from: InProgress, to: Canceled, reason: true, kind: ScaleMatter},
	TypeStageCanceled:  {verb: "canceled", from: InProgress, to: Canceled, reason: true, kind: ScaleStage},
	TypeStepCanceled:   {verb: "canceled", from: InProgress, to: Canceled, reason: true, kind: ScaleStep},
	TypeMatterPaused:   {verb: "paused", from: InProgress, to: Paused, kind: ScaleMatter},
	TypeStagePaused:    {verb: "paused", from: InProgress, to: Paused, kind: ScaleStage},
	TypeStepPaused:     {verb: "paused", from: InProgress, to: Paused, kind: ScaleStep},
	TypeMatterResumed:  {verb: "resumed", from: Paused, to: InProgress, kind: ScaleMatter},
	TypeStageResumed:   {verb: "resumed", from: Paused, to: InProgress, kind: ScaleStage},
	TypeStepResumed:    {verb: "resumed", from: Paused, to: InProgress, kind: ScaleStep},
}

// experimentPortableTypeNames is the non-lifecycle half of the admitted
// portable family, in declaration order. experimentLifecycleEdges supplies
// the lifecycle half; together they are the single list experimentPortableType,
// the export whitelist and the exchange status dependency scan all derive
// from, so the three can never drift apart.
var experimentPortableTypeNames = []string{
	TypeMatterCreated, TypeStageCreated, TypeStepCreated, TypeStepInserted,
	TypeStepReordered, TypeStepReplaced, TypeStepRemoved,
	TypeBacklogEntered, TypeBacklogPlanned, TypeBacklogDeclined, TypeBacklogDelegated,
	TypeContentCreated, TypeContentAppended,
}

// experimentPortableTypes is every admitted portable event type — the
// lifecycle matrix plus experimentPortableTypeNames — materialized once,
// sorted, for both the Go predicate and the status scan's SQL IN clause.
var experimentPortableTypes = func() []string {
	types := make([]string, 0, len(experimentLifecycleEdges)+len(experimentPortableTypeNames))
	for typ := range experimentLifecycleEdges {
		types = append(types, typ)
	}
	types = append(types, experimentPortableTypeNames...)
	slices.Sort(types)
	return types
}()

// experimentPortableTypeSQL is experimentPortableTypes as a quoted,
// comma-separated SQL literal list for IN clauses.
var experimentPortableTypeSQL = func() string {
	quoted := make([]string, len(experimentPortableTypes))
	for i, typ := range experimentPortableTypes {
		quoted[i] = "'" + typ + "'"
	}
	return strings.Join(quoted, ",")
}()

// experimentPortableType is the admitted portable family at type level —
// every event type experimentExchangePortable can carry to a paired
// receiver, without the export-side Repo, actor and payload-shape checks.
// Receiver-side tail checks share it so "portable history" means one thing
// everywhere: an event already in local history is an observed fact on the
// portable plane whatever its bytes, and a divergent one must conflict
// rather than be ignored, so payload shape is deliberately not consulted
// here.
func experimentPortableType(typ string) bool {
	return slices.Contains(experimentPortableTypes, typ)
}

// experimentPortableEvent is portable admission minus the original-work
// exclusions — an admitted family type carrying a well-formed payload where
// the family has one. experimentExchangePortable layers Repo and the
// integration-effect actor check on top for export eligibility;
// experimentPortableBaseline uses it without the actor check because a
// generated effect is never an exportable original yet IS portable-family
// history a baseline may name — canonical() rewrites it to the retained
// source identity.
func experimentPortableEvent(ev Event) bool {
	if !experimentPortableType(ev.Type) {
		return false
	}
	switch ev.Type {
	case TypeContentCreated:
		var p ContentWritten
		return json.Unmarshal(ev.Payload, &p) == nil && experimentPortableCreated(p)
	case TypeContentAppended:
		var p ContentWritten
		return json.Unmarshal(ev.Payload, &p) == nil && experimentPortableFinding(p)
	}
	return true
}

// experimentPortableBaseline reports whether a subject-history predecessor
// can appear in a paired receiver's portable history — the export half of
// baseline selection. It is experimentPortableEvent scoped to the exchanged
// Repo: integration effects count because canonical() maps them back to
// their retained source identity, and any actor is fine because the
// receiver's tail is a type-level plane. A predecessor outside the Repo —
// of an unadmitted family, or a content write whose payload cannot sync —
// can never arrive, so it must never be named as the baseline a receiver
// has to resolve.
func experimentPortableBaseline(ev Event, repo string) bool {
	return ev.Repo == repo && experimentPortableEvent(ev)
}

// experimentExchangePortable is the single per-member predicate behind
// ExchangeStatus eligibility and portable derivatives: the event must be a
// repo-scoped original of an admitted family — the node births including the
// positioned step.inserted, the step amendment family, the lifecycle matrix
// above, the backlog outcome family and the content family (the create-once
// writes and findings appends), exactly what experimentClassifyPortable and
// experimentClassify can project on a bound target. The predicate is per
// event, not per subject: reordered names the parent, replaced the old step
// and removed the step, and each classifier resolves its own subject shape.
// Batch/execution-scoped events carry no Repo, generated integration effects
// are never originals, and every unadmitted family stays local. The
// whitelist lives only here and in the import classifiers, never in a
// second list that could drift.
func experimentExchangePortable(ev Event, repo string) bool {
	if ev.Repo != repo || ev.Actor == integrationEffectActor {
		return false
	}
	return experimentPortableEvent(ev)
}

// experimentPortableContentShape is the storage half of the content family's
// whitelist, shared by both content classifiers: ContentWritten carries
// either inline bytes or a blob reference, never both and never neither —
// the same XOR insertContent enforces. A spilled payload's BlobRef is the
// content identity itself, the only form ContentDraft ever writes and the
// only key sync-content can install verified bytes against. ByteLen is
// bounded by the blob route's whole-object cap: larger content can never be
// installed on a peer, so it stays local evidence instead of wedging the
// sender's upload queue on a permanent rejection.
func experimentPortableContentShape(p ContentWritten) bool {
	if p.ByteLen > exchangeBlobMaxBytes {
		return false
	}
	if p.Bytes != nil && p.BlobRef == "" {
		return true
	}
	return p.Bytes == nil && p.BlobRef != "" && p.BlobRef == p.Content
}

// experimentPortableCreated reports whether a content.created payload is in
// the admitted create-once family — Brief, Workplan or body — with a
// well-formed content identity. Findings are append-only and never belong
// here; the fold refuses the pairing.
func experimentPortableCreated(p ContentWritten) bool {
	return (p.Kind == KindBrief || p.Kind == KindWorkplan || p.Kind == KindBody) &&
		IsIdentityShaped(p.Content) && experimentPortableContentShape(p)
}

// experimentPortableFinding reports whether a content.appended payload is a
// well-formed findings segment: append-only kind, identity-shaped content id,
// inline-or-blob storage.
func experimentPortableFinding(p ContentWritten) bool {
	return p.Kind == KindFindings && IsIdentityShaped(p.Content) && experimentPortableContentShape(p)
}

// experimentUnitMask reports which members of one committed chain form its
// portable unit. A member qualifies only when the member itself is portable
// AND every in-group causal ancestor qualifies: the version-1 envelope's
// causation chain must stay inside the unit, so portable facts are never a
// truncation that leaves a dangling cause. The origin member must qualify for
// any unit to exist, which also means a non-portable origin condemns the
// whole group to local-only inventory.
func experimentUnitMask(events []Event, repo string) []bool {
	mask := make([]bool, len(events))
	index := make(map[string]int, len(events))
	for i, ev := range events {
		index[ev.ID] = i
	}
	for i, ev := range events {
		if !experimentExchangePortable(ev, repo) {
			continue
		}
		if i > 0 {
			cause, ok := index[ev.Causation]
			if !ok || cause >= i || !mask[cause] {
				continue
			}
		}
		mask[i] = true
	}
	return mask
}

// Portable members validate against candidate state: experimentEvaluate folds
// each member's effect before classifying the next, so a birth or Brief may be
// a non-origin member of one group and still see the parents/subjects earlier
// members just created. Whole-group atomicity is the caller's savepoint, not
// this classifier's. Colliding identities/addresses/content remain retained
// conflicts, never merges.
func experimentClassifyPortable(ctx context.Context, v View, a ExperimentAction, s *Store) (string, string, string, bool, error) {
	refuse := func(reason string) (string, string, string, bool, error) { return "conflict", reason, "", false, nil }
	e := a.Event
	history, err := v.EventsOfSubject(ctx, e.Subject)
	if err != nil {
		return "", "", "", false, err
	}
	for _, prior := range history {
		if prior.ID == e.ID {
			// Repo is intentionally mapped, all other original evidence must match.
			if reflect.DeepEqual(prior, e) {
				return "integrated", "", prior.ID, false, nil
			}
			return refuse("source identity differs from native history")
		}
	}
	switch e.Type {
	case TypeMatterCreated, TypeStageCreated, TypeStepCreated, TypeStepInserted:
		return experimentClassifyBirth(ctx, v, a, history)
	case TypeContentCreated:
		return experimentClassifyContent(ctx, v, a, history, s)
	}
	return refuse("unsupported family; original payload retained, no content overwritten")
}

// experimentClassifyBirth admits one Matter/Stage/Step birth. Identity is the
// source subject — shared business identity means the local node keeps it — so
// an existing history is a collision, never a merge target. A Stage or Step
// names its parent by that same identity; the parent may be a live node or a
// same-group birth already folded into candidate state. Missing parents wait;
// wrong kinds, tombstones, foreign Repos and occupied addresses conflict.
func experimentClassifyBirth(ctx context.Context, v View, a ExperimentAction, history []Event) (string, string, string, bool, error) {
	refuse := func(reason string) (string, string, string, bool, error) { return "conflict", reason, "", false, nil }
	wait := func(reason string) (string, string, string, bool, error) { return "pending", reason, "", false, nil }
	e := a.Event
	kind := map[string]Scale{
		TypeMatterCreated: ScaleMatter, TypeStageCreated: ScaleStage,
		TypeStepCreated: ScaleStep, TypeStepInserted: ScaleStep,
	}[e.Type]
	var p NodeBirth
	if !IsIdentityShaped(e.Subject) || decodeStrict(e, &p) != nil || p.Title == "" || p.Locator == "" || a.Baseline != "" ||
		(kind == ScaleMatter) != (p.Parent == "") || (p.Parent != "" && !IsIdentityShaped(p.Parent)) {
		return refuse("invalid " + string(kind) + " birth")
	}
	if len(history) != 0 {
		return refuse("object identity already exists")
	}
	// Subject history alone cannot see every existing node: a step.replaced
	// birth is filed under the replaced node's subject, so the replacement
	// has an empty history of its own while its row already exists. The
	// projection row is the floor under that gap — without it the fold's
	// primary-key insert would roll the whole unit back.
	var rows int
	if err := v.q.QueryRowContext(ctx, `SELECT COUNT(*) FROM nodes WHERE id=?`, e.Subject).Scan(&rows); err != nil {
		return "", "", "", false, err
	}
	if rows != 0 {
		return refuse("object identity already exists")
	}
	matter := ""
	if kind != ScaleMatter {
		parent, err := v.Node(ctx, p.Parent)
		if err != nil {
			// v.Node sees only live rows; distinguish a removed parent (a
			// permanent incompatibility) from one that has not arrived.
			var rows int
			if qerr := v.q.QueryRowContext(ctx, `SELECT COUNT(*) FROM nodes WHERE id=?`, p.Parent).Scan(&rows); qerr != nil {
				return "", "", "", false, qerr
			}
			if rows != 0 {
				return refuse(string(kind) + " parent was removed; a tombstoned node is not a place to put a " + string(kind))
			}
			return wait(string(kind) + " awaiting parent")
		}
		if parent.Repo != e.Repo {
			return refuse(string(kind) + " parent belongs to another Repo")
		}
		validParent := parent.Kind == ScaleMatter || (kind == ScaleStep && parent.Kind == ScaleStage)
		if !validParent {
			return refuse(string(kind) + " parent is a " + string(parent.Kind) + "; max depth is Matter -> Stage -> Step")
		}
		matter = parent.Matter
	}
	var count int
	var err error
	if kind == ScaleMatter {
		// A Matter is its own Matter, so the (matter, locator) index cannot
		// pre-check its slug; repo+kind is the addressing scope instead.
		err = v.q.QueryRowContext(ctx, `SELECT COUNT(*) FROM nodes WHERE repo=? AND kind='matter' AND locator=? AND tombstone_event IS NULL`, e.Repo, p.Locator).Scan(&count)
	} else {
		err = v.q.QueryRowContext(ctx, `SELECT COUNT(*) FROM nodes WHERE matter=? AND locator=? AND tombstone_event IS NULL`, matter, p.Locator).Scan(&count)
	}
	if err != nil {
		return "", "", "", false, err
	}
	if count != 0 {
		return refuse(string(kind) + " address already occupied; identities not merged")
	}
	return "integrated", "", "", true, nil
}

// experimentClassifyContent admits one create-once content write — Brief,
// Workplan or body — the writesurface WriteOnce verbs' events. It stays
// bound-only like the births it may share a group with: the payload mints a
// new content identity, so it needs the same target-Repo mapping a birth
// does. Every precondition insertContent enforces is pre-validated here —
// a fold failure would roll the whole unit back — with the fold's own
// subject rule: any node row carries create-once content (contentSubjectExists
// counts tombstoned rows too, and the native WriteOnce resolves any live
// node kind), while a backlog entry can never take one. Ordering is strict:
// the mapped baseline must be the subject-history tail, because a
// create-once write asserts "the subject as last observed", never a fact
// concurrent with later writes.
func experimentClassifyContent(ctx context.Context, v View, a ExperimentAction, history []Event, s *Store) (string, string, string, bool, error) {
	refuse := func(reason string) (string, string, string, bool, error) { return "conflict", reason, "", false, nil }
	wait := func(reason string) (string, string, string, bool, error) { return "pending", reason, "", false, nil }
	e := a.Event
	var p ContentWritten
	if decodeStrict(e, &p) != nil || !experimentPortableCreated(p) {
		return refuse("unsupported or invalid create-once content")
	}
	ready, err := experimentContentReady(s, p)
	if err != nil {
		return "", "", "", false, err
	}
	if !ready.integrity {
		return refuse("content integrity mismatch")
	}
	if !ready.bytes {
		return wait(ready.reason)
	}
	// Content identity is global, including tombstoned rows. A different
	// original cannot reuse it on another subject (or revive its old slot).
	var count int
	if err := v.q.QueryRowContext(ctx, `SELECT COUNT(*) FROM content WHERE id=?`, p.Content).Scan(&count); err != nil {
		return "", "", "", false, err
	}
	if count != 0 {
		return refuse("content identity already exists; accepted bytes unchanged")
	}
	var subjectRepo string
	err = v.q.QueryRowContext(ctx, `SELECT repo FROM nodes WHERE id=?`, e.Subject).Scan(&subjectRepo)
	if err != nil {
		if !errors.Is(err, sql.ErrNoRows) {
			return "", "", "", false, err
		}
		// The subject may be a backlog entry — create-once kinds can never
		// land there, so that is a permanent incompatibility, not a wait.
		if qerr := v.q.QueryRowContext(ctx, `SELECT COUNT(*) FROM backlog_entries WHERE id=?`, e.Subject).Scan(&count); qerr != nil {
			return "", "", "", false, qerr
		}
		if count != 0 {
			return refuse("create-once content is written on nodes; backlog entries carry findings only")
		}
		return wait("content awaiting subject")
	}
	if subjectRepo != e.Repo {
		return refuse("content target belongs to another Repo")
	}
	segments, err := v.ContentSegments(ctx, e.Subject, p.Kind)
	if err != nil {
		return "", "", "", false, err
	}
	if len(segments) > 0 {
		return refuse("competing " + string(p.Kind) + " retained; accepted bytes unchanged")
	}
	if a.Baseline == "" {
		return wait("missing content baseline")
	}
	index, anchored, err := experimentBaselineIndex(ctx, v, e.Subject, history, a.Baseline)
	if err != nil {
		return "", "", "", false, err
	}
	if index < 0 && !anchored {
		return wait("unknown baseline dependency")
	}
	// The baseline must be the tail: the last portable-plane event, or the
	// replaced-birth anchor when the subject has no portable events of its
	// own yet. Non-portable families never sync, so a local reference, gate
	// or render fact after the baseline is not divergence.
	tail := experimentPortableTail(history)
	last := ""
	if tail >= 0 {
		last = history[tail].ID
	} else if anchored {
		last = a.Baseline
	}
	if last != a.Baseline {
		return refuse("content subject history diverged; create-once content never reorders observed facts")
	}
	return "integrated", "", "", true, nil
}

// experimentClassifyFinding admits one findings append — the only
// append-only content kind, written on nodes and backlog entries alike. Like
// the amendment family it mutates an already-paired object, so
// experimentClassify routes it on both the bound and the unbound path. The
// subject rule is contentSubjectExists's own: a node row in any lifecycle
// state (the fold counts tombstoned rows) or a backlog entry, in the mapped
// Repo. Ordering is causal, not positional: the mapped baseline must resolve
// inside the subject's local history — or the replaced-birth anchor — but
// need not be the tail, because two sources appending findings against the
// same observed tail are independent facts that both survive; the content
// rows order themselves by segment identity. A baseline that resolves
// nowhere waits, and a source event already in subject history is the
// verified self-echo.
func experimentClassifyFinding(ctx context.Context, v View, a ExperimentAction, s *Store) (string, string, string, bool, error) {
	refuse := func(reason string) (string, string, string, bool, error) { return "conflict", reason, "", false, nil }
	wait := func(reason string) (string, string, string, bool, error) { return "pending", reason, "", false, nil }
	e := a.Event
	history, err := v.EventsOfSubject(ctx, e.Subject)
	if err != nil {
		return "", "", "", false, err
	}
	for _, prior := range history {
		if prior.ID == e.ID {
			// Repo is intentionally mapped, all other original evidence must match.
			if reflect.DeepEqual(prior, e) {
				return "integrated", "", prior.ID, false, nil
			}
			return refuse("source identity differs from native history")
		}
	}
	var p ContentWritten
	if decodeStrict(e, &p) != nil || !experimentPortableFinding(p) {
		return refuse("unsupported or invalid findings append")
	}
	ready, err := experimentContentReady(s, p)
	if err != nil {
		return "", "", "", false, err
	}
	if !ready.integrity {
		return refuse("findings integrity mismatch")
	}
	if !ready.bytes {
		return wait(ready.reason)
	}
	// Content identity is global, including tombstoned rows — same floor as
	// the create-once classifier.
	var count int
	if err := v.q.QueryRowContext(ctx, `SELECT COUNT(*) FROM content WHERE id=?`, p.Content).Scan(&count); err != nil {
		return "", "", "", false, err
	}
	if count != 0 {
		return refuse("content identity already exists; accepted bytes unchanged")
	}
	var subjectRepo string
	err = v.q.QueryRowContext(ctx, `SELECT repo FROM nodes WHERE id=?`, e.Subject).Scan(&subjectRepo)
	if err != nil {
		if !errors.Is(err, sql.ErrNoRows) {
			return "", "", "", false, err
		}
		err = v.q.QueryRowContext(ctx, `SELECT repo FROM backlog_entries WHERE id=?`, e.Subject).Scan(&subjectRepo)
		if err != nil {
			if errors.Is(err, sql.ErrNoRows) {
				return wait("finding awaiting subject")
			}
			return "", "", "", false, err
		}
	}
	if subjectRepo != e.Repo {
		return refuse("finding target belongs to another Repo")
	}
	if a.Baseline == "" {
		return wait("missing finding baseline")
	}
	index, anchored, err := experimentBaselineIndex(ctx, v, e.Subject, history, a.Baseline)
	if err != nil {
		return "", "", "", false, err
	}
	if index < 0 && !anchored {
		return wait("unknown baseline dependency")
	}
	return "integrated", "", "", true, nil
}

// experimentContentReadiness is the classifier's view of one ContentWritten
// payload's bytes: integrity reports whether inline bytes verify against the
// recorded length and digest (always true for spilled payloads — the blob
// check below covers them), and bytes reports whether the payload's bytes
// are servable at all, with reason naming why not when they are not.
type experimentContentReadiness struct {
	integrity bool
	bytes     bool
	reason    string
}

// experimentContentReady verifies a content payload's storage half. Inline
// bytes must match byte_len and sha256 exactly as insertContent enforces; a
// spilled payload — Bytes absent, BlobRef naming the content identity — must
// already be installed where this store can read it, because a projected
// content row whose bytes cannot be served is exactly what portable sync
// must never create.
func experimentContentReady(s *Store, p ContentWritten) (experimentContentReadiness, error) {
	if p.Bytes != nil {
		sum := sha256.Sum256(p.Bytes)
		return experimentContentReadiness{
			integrity: p.ByteLen == int64(len(p.Bytes)) && hex.EncodeToString(sum[:]) == p.SHA256,
			bytes:     true,
		}, nil
	}
	state, err := s.spilledContentState(p.BlobRef, p.ByteLen, p.SHA256)
	if err != nil {
		return experimentContentReadiness{}, err
	}
	switch state {
	case "ready":
		return experimentContentReadiness{integrity: true, bytes: true}, nil
	case "missing":
		return experimentContentReadiness{integrity: true, bytes: false, reason: "content bytes unavailable"}, nil
	default:
		return experimentContentReadiness{integrity: true, bytes: false, reason: "content bytes failed verification"}, nil
	}
}

// spilledContentState is the content-readiness seam the sync-content work
// plugs into. It reports what this store can actually serve for a spilled
// payload: the sidecar file blobDir/<ref> — safe because the classifiers
// only admit BlobRef == the identity-shaped content id — checked against the
// payload's own byte_len and sha256 exactly as SegmentBytes will check it on
// every later read. "missing" and "invalid" both pend the importing group —
// sync-content's verified, atomic install is what flips either to "ready"
// on the next recheck — and neither ever becomes an empty or invented row.
func (s *Store) spilledContentState(ref string, byteLen int64, digest string) (string, error) {
	data, err := os.ReadFile(filepath.Join(s.blobDir, ref))
	if err != nil {
		if os.IsNotExist(err) {
			return "missing", nil
		}
		return "", fmt.Errorf("store: read spilled content %s: %w", ref, err)
	}
	if int64(len(data)) != byteLen {
		return "invalid", nil
	}
	sum := sha256.Sum256(data)
	if hex.EncodeToString(sum[:]) != digest {
		return "invalid", nil
	}
	return "ready", nil
}

// experimentPortableTail returns the index of the last admitted portable-
// family event in subject history, or -1 when the portable plane is empty.
// Non-portable families — references, gates, config, tracker, outbox, tier,
// execution — legitimately diverge between paired stores because they never
// sync, so every "subject tail" the classifiers compare against a mapped
// baseline is defined over this plane only. Integration effects and native
// portable-type events both count: either way they are the receiver's
// portable history.
func experimentPortableTail(history []Event) int {
	for i := len(history) - 1; i >= 0; i-- {
		if experimentPortableType(history[i].Type) {
			return i
		}
	}
	return -1
}

// experimentPortableBefore returns the ID of the portable-family event
// immediately preceding history[tail] — the portable-plane analogue of
// history[-2] — or "" when tail is the subject's first portable event.
func experimentPortableBefore(history []Event, tail int) string {
	for i := tail - 1; i >= 0; i-- {
		if experimentPortableType(history[i].Type) {
			return history[i].ID
		}
	}
	return ""
}

// experimentBaselineIndex locates the mapped baseline inside the subject's
// local history. The ordinary answer is a history index; a node born by
// step.replaced is the exception — that birth is filed under the replaced
// node's subject, so the first event after it has no entry of its own here
// and anchors on nodes.birth_event instead, which the mapped baseline names
// directly. index is -1 when the baseline is not a subject-history event;
// anchored then says whether it is the birth anchor (true) or resolves to
// nothing local (false). Callers use the result as a resolution check only —
// positional comparison against the tail is experimentPortableTail's job.
func experimentBaselineIndex(ctx context.Context, v View, subject string, history []Event, baseline string) (index int, anchored bool, err error) {
	for i, prior := range history {
		if prior.ID == baseline {
			return i, false, nil
		}
	}
	var birth string
	qerr := v.q.QueryRowContext(ctx, `SELECT birth_event FROM nodes WHERE id=?`, subject).Scan(&birth)
	if qerr != nil {
		if errors.Is(qerr, sql.ErrNoRows) {
			return -1, false, nil
		}
		return -1, false, qerr
	}
	return -1, birth == baseline, nil
}

// experimentClassifyAmendment admits the step amendment family —
// step.reordered, step.replaced, step.removed — the structural mutations
// writesurface's amendment verbs emit. Like the lifecycle matrix they are
// paired-object facts classified on both the bound and the unbound path:
// the subject must already exist locally, and every fold precondition is
// pre-validated here because a fold failure would roll the whole unit back
// and wedge the inbox. Echoes return the existing effect; divergent subject
// history conflicts rather than reordering observed facts.
func experimentClassifyAmendment(ctx context.Context, v View, a ExperimentAction) (string, string, string, bool, error) {
	refuse := func(reason string) (string, string, string, bool, error) { return "conflict", reason, "", false, nil }
	wait := func(reason string) (string, string, string, bool, error) { return "pending", reason, "", false, nil }
	e := a.Event

	// Decode and shape-check the payload first: a malformed amendment is
	// retained evidence whatever the local state happens to be. same reports
	// whether a prior subject event of this type carries an equal payload —
	// the echo test below.
	var (
		order   []string
		replace Replaced
		same    func(Event) bool
	)
	switch e.Type {
	case TypeStepReordered:
		var p Reordered
		if decodeStrict(e, &p) != nil || len(p.Order) == 0 {
			return refuse("invalid step reorder")
		}
		for _, id := range p.Order {
			if !IsIdentityShaped(id) {
				return refuse("invalid step reorder")
			}
		}
		order = p.Order
		same = func(prior Event) bool {
			var q Reordered
			return decode(prior, &q) == nil && slices.Equal(order, q.Order)
		}
	case TypeStepReplaced:
		if decodeStrict(e, &replace) != nil || !IsIdentityShaped(replace.Replacement) ||
			replace.Title == "" || replace.Locator == "" {
			return refuse("invalid step replacement")
		}
		same = func(prior Event) bool {
			var q Replaced
			return decode(prior, &q) == nil && q.Replacement == replace.Replacement &&
				q.Title == replace.Title && q.Locator == replace.Locator
		}
	case TypeStepRemoved:
		var p Removed
		if decodeStrict(e, &p) != nil {
			return refuse("invalid step removal")
		}
		same = func(prior Event) bool {
			var q Removed
			return decode(prior, &q) == nil && q.Reason == p.Reason
		}
	default:
		return refuse("unsupported family; original payload retained, no content overwritten")
	}
	// Every amendment subject is a node identity; anything else can never
	// resolve, so it is retained evidence rather than a pending wait.
	if !IsIdentityShaped(e.Subject) {
		return refuse("invalid amendment subject")
	}

	if a.Baseline == "" {
		return wait("missing baseline evidence")
	}
	history, err := v.EventsOfSubject(ctx, e.Subject)
	if err != nil {
		return "", "", "", false, err
	}
	baseline, anchored, err := experimentBaselineIndex(ctx, v, e.Subject, history, a.Baseline)
	if err != nil {
		return "", "", "", false, err
	}
	if baseline < 0 && !anchored {
		return wait("unknown baseline dependency")
	}
	// Echo first: a successful replace or remove leaves its subject
	// tombstoned, so liveness cannot be asked before the history tail has
	// been recognized as this source's own effect. The predecessor of the
	// newest portable event is the baseline — the portable-plane analogue of
	// history[-2], with the birth anchor for a step.replaced-born node's
	// first event. Non-portable families legitimately diverge between
	// paired stores, so they never enter this comparison.
	if tail := experimentPortableTail(history); tail >= 0 {
		last := history[tail]
		before := experimentPortableBefore(history, tail)
		if before == "" && anchored {
			before = a.Baseline
		}
		if before == a.Baseline && last.Type == e.Type && same(last) {
			return "integrated", "", last.ID, false, nil
		}
		if last.ID != a.Baseline {
			return refuse("subject history diverged; amendments never reorder observed facts")
		}
	}

	n, err := v.Node(ctx, e.Subject)
	if err != nil {
		// v.Node sees only live rows; distinguish a removed subject (a
		// permanent incompatibility) from one that has not arrived.
		var rows int
		if qerr := v.q.QueryRowContext(ctx, `SELECT COUNT(*) FROM nodes WHERE id=?`, e.Subject).Scan(&rows); qerr != nil {
			return "", "", "", false, qerr
		}
		if rows != 0 {
			return refuse("amendment subject was removed; a tombstoned node is not amendable")
		}
		return wait("missing live paired object")
	}
	if n.Repo != e.Repo || n.Parent != a.Parent {
		return refuse("paired structure differs")
	}

	switch e.Type {
	case TypeStepReordered:
		// subject is the parent whose Step children moved: a Matter or a
		// Stage, never a Step — a Step has no Step children to order.
		if n.Kind != ScaleMatter && n.Kind != ScaleStage {
			return refuse("step.reordered subject is a " + string(n.Kind) + "; only a matter or stage orders steps")
		}
		// The payload contract is the whole sibling set in its new order —
		// ReorderStep refuses a partial order natively — so Order must be a
		// permutation of the live Step children, not a subset that would
		// leave stale keys or a superset the fold cannot match.
		children, err := v.Children(ctx, n.ID)
		if err != nil {
			return "", "", "", false, err
		}
		live := map[string]bool{}
		for _, c := range children {
			if c.Kind == ScaleStep {
				live[c.ID] = true
			}
		}
		named := map[string]bool{}
		for _, id := range order {
			if named[id] {
				return refuse("step.reordered names a step twice")
			}
			named[id] = true
			if live[id] {
				continue
			}
			// Only a never-existent identity may still arrive; anything else
			// — tombstoned, wrong kind, another parent's child — is a
			// permanent incompatibility the fold's guarded update cannot
			// absorb.
			var rows int
			if err := v.q.QueryRowContext(ctx, `SELECT COUNT(*) FROM nodes WHERE id=?`, id).Scan(&rows); err != nil {
				return "", "", "", false, err
			}
			if rows == 0 {
				return wait("step.reordered awaits a step")
			}
			return refuse("step.reordered names an identity that is not a live step of the subject")
		}
		for id := range live {
			if !named[id] {
				return refuse("step.reordered is not the complete live sibling set")
			}
		}
	case TypeStepReplaced, TypeStepRemoved:
		// subject is the Step being amended; the fold's own kind check makes
		// replacement step-scoped and removal kind-agnostic, so the
		// classifier is where "only a step" is enforced for both.
		if n.Kind != ScaleStep {
			return refuse(e.Type + " subject is a " + string(n.Kind) + "; only a step may be amended")
		}
		if e.Type == TypeStepReplaced {
			// The replacement's identity is spent the moment the fold runs:
			// nodes.id is a primary key over live and tombstoned rows alike,
			// so any existing row — including a step.replaced-born node with
			// no subject history of its own — is a collision, never a merge.
			var rows int
			if err := v.q.QueryRowContext(ctx, `SELECT COUNT(*) FROM nodes WHERE id=?`, replace.Replacement).Scan(&rows); err != nil {
				return "", "", "", false, err
			}
			if rows != 0 {
				return refuse("replacement identity already exists; identities not merged")
			}
			// nodes_locator is unique over live rows within the Matter the
			// replacement inherits — the same scope births check.
			if err := v.q.QueryRowContext(ctx,
				`SELECT COUNT(*) FROM nodes WHERE matter=? AND locator=? AND tombstone_event IS NULL`,
				n.Matter, replace.Locator).Scan(&rows); err != nil {
				return "", "", "", false, err
			}
			if rows != 0 {
				return refuse("replacement address already occupied; identities not merged")
			}
		}
	}
	return "integrated", "", "", true, nil
}

// experimentClassifyBacklog admits the backlog outcome family —
// backlog.entered plus the three terminal exits planned, declined and
// delegated — the intake facts writesurface's backlog verbs emit. Entries are
// Repo-durable paired objects like nodes, so the family classifies on both the
// bound and the unbound path: the subject is the entry's own identity and
// every fold precondition is pre-validated here because a fold failure would
// roll the whole unit back and wedge the inbox. Delegated is admitted but
// inert: the fold already records the fact without queueing an outbox row for
// integration effects (delegateBacklogEntry's integrationEffectActor guard),
// so the source's outbox identity is kept as audit evidence and never becomes
// delivery authority here. All four states are terminal exits from entered:
// an equal-payload tail of the same type is the echo, and any other observed
// successor conflicts rather than reordering facts.
func experimentClassifyBacklog(ctx context.Context, v View, a ExperimentAction) (string, string, string, bool, error) {
	refuse := func(reason string) (string, string, string, bool, error) { return "conflict", reason, "", false, nil }
	wait := func(reason string) (string, string, string, bool, error) { return "pending", reason, "", false, nil }
	e := a.Event

	// Decode and shape-check first: a malformed outcome is retained evidence
	// whatever the local state happens to be. The fold's own decodeStrict on
	// delegated sets the bar; the other folds decode leniently, but admission
	// stays strict — extra fields are not something the verbs emit.
	var (
		entered   BacklogEntered
		planned   BacklogPlanned
		declined  BacklogDeclined
		delegated BacklogDelegated
		same      func(Event) bool
	)
	switch e.Type {
	case TypeBacklogEntered:
		if decodeStrict(e, &entered) != nil ||
			(entered.Provenance != ProvenanceIntake && entered.Provenance != ProvenanceFound && entered.Provenance != ProvenanceDeferred) ||
			entered.Title == "" || (entered.OriginNode != "" && !IsIdentityShaped(entered.OriginNode)) {
			return refuse("invalid backlog entry")
		}
	case TypeBacklogPlanned:
		if decodeStrict(e, &planned) != nil || !IsIdentityShaped(planned.Matter) {
			return refuse("invalid backlog plan")
		}
		same = func(prior Event) bool {
			var q BacklogPlanned
			return decode(prior, &q) == nil && q.Matter == planned.Matter
		}
	case TypeBacklogDeclined:
		// The fold refuses an empty reason — declined must be distinguishable
		// from not-yet-acted-upon — so admission refuses it first.
		if decodeStrict(e, &declined) != nil || declined.Reason == "" {
			return refuse("invalid backlog decline")
		}
		same = func(prior Event) bool {
			var q BacklogDeclined
			return decode(prior, &q) == nil && q.Reason == declined.Reason
		}
	case TypeBacklogDelegated:
		// Same bar the fold enforces: an outbox identity plus the key that
		// keeps a retry from minting a second external item.
		if decodeStrict(e, &delegated) != nil || !IsIdentityShaped(delegated.Outbox) || delegated.IdempotencyKey == "" {
			return refuse("invalid backlog delegation")
		}
		same = func(prior Event) bool {
			var q BacklogDelegated
			return decode(prior, &q) == nil && q.Outbox == delegated.Outbox &&
				q.IdempotencyKey == delegated.IdempotencyKey
		}
	default:
		return refuse("unsupported family; original payload retained, no content overwritten")
	}
	// The subject is the entry's own ULID everywhere in the family.
	if !IsIdentityShaped(e.Subject) {
		return refuse("invalid backlog subject")
	}

	// One backlog_entries read serves both branches: entered needs the row's
	// absence (a plain PK insert) or its birth event (the echo test); the
	// exits need repo, state and the entered precondition the fold's guarded
	// UPDATE enforces with exactlyRows.
	var entryRepo, entryState, entryBirth string
	entryErr := v.q.QueryRowContext(ctx,
		`SELECT repo,state,birth_event FROM backlog_entries WHERE id=?`, e.Subject).
		Scan(&entryRepo, &entryState, &entryBirth)

	if e.Type == TypeBacklogEntered {
		// entered is the subject's first and only birth: a claimed baseline
		// names evidence that can never exist for a fresh entry identity.
		if a.Baseline != "" {
			return refuse("invalid backlog entry")
		}
		// The fold's INSERT carries repo as a foreign key; a Repo not yet
		// represented locally waits rather than wedging on the constraint.
		var repos int
		if err := v.q.QueryRowContext(ctx, `SELECT COUNT(*) FROM repos WHERE id=?`, e.Repo).Scan(&repos); err != nil {
			return "", "", "", false, err
		}
		if repos == 0 {
			return wait("missing paired repo")
		}
		// origin_node's fold check is existence-only; admission wants the live
		// node the verb resolved, in this Repo — a tombstone or a foreign node
		// is a permanent incompatibility, a missing one may still arrive.
		if entered.OriginNode != "" {
			n, err := v.Node(ctx, entered.OriginNode)
			if err != nil {
				var rows int
				if qerr := v.q.QueryRowContext(ctx, `SELECT COUNT(*) FROM nodes WHERE id=?`, entered.OriginNode).Scan(&rows); qerr != nil {
					return "", "", "", false, qerr
				}
				if rows != 0 {
					return refuse("backlog origin node was removed; a tombstoned node is not a discovery origin")
				}
				return wait("backlog origin awaiting node")
			}
			if n.Repo != e.Repo {
				return refuse("backlog origin belongs to another Repo")
			}
		}
		if entryErr == nil {
			// The only legitimate existing row is this event's own earlier
			// fold — the verified self-echo, whose local identity IS the
			// source identity. Any other birth is a divergent claim on a
			// spent entry identity, never a merge.
			if entryBirth == e.ID {
				return "integrated", "", e.ID, false, nil
			}
			return refuse("backlog entry identity already exists")
		}
		if !errors.Is(entryErr, sql.ErrNoRows) {
			return "", "", "", false, entryErr
		}
		return "integrated", "", "", true, nil
	}

	// The three exits share entered -> terminal: the mapped baseline must be
	// the portable-plane tail the source observed, an equal-payload tail of
	// this same type is the echo, and anything else is divergence the fold's
	// state='entered' guard could never absorb. Non-portable families
	// legitimately diverge between paired stores, so only admitted portable
	// events count as observed successors here.
	if a.Baseline == "" {
		return wait("missing baseline evidence")
	}
	history, err := v.EventsOfSubject(ctx, e.Subject)
	if err != nil {
		return "", "", "", false, err
	}
	baseline, _, err := experimentBaselineIndex(ctx, v, e.Subject, history, a.Baseline)
	if err != nil {
		return "", "", "", false, err
	}
	if baseline < 0 {
		return wait("unknown baseline dependency")
	}
	if tail := experimentPortableTail(history); tail >= 0 {
		last := history[tail]
		before := experimentPortableBefore(history, tail)
		if before == a.Baseline && last.Type == e.Type && same(last) {
			return "integrated", "", last.ID, false, nil
		}
		if last.ID != a.Baseline {
			return refuse("backlog subject history diverged; outcomes never reorder observed facts")
		}
	}
	if errors.Is(entryErr, sql.ErrNoRows) {
		return wait("missing backlog entry")
	}
	if entryErr != nil {
		return "", "", "", false, entryErr
	}
	if entryRepo != e.Repo {
		return refuse("backlog entry belongs to another Repo")
	}
	if entryState != "entered" {
		return refuse("backlog entry already exited; planned, declined and delegated are terminal")
	}
	if e.Type == TypeBacklogPlanned {
		// matter's fold check is existence-only; admission wants the live
		// Matter the verb resolved, in this Repo — the fold cannot tell a
		// Stage or a tombstone from the Matter the entry became.
		n, err := v.Node(ctx, planned.Matter)
		if err != nil {
			var rows int
			if qerr := v.q.QueryRowContext(ctx, `SELECT COUNT(*) FROM nodes WHERE id=?`, planned.Matter).Scan(&rows); qerr != nil {
				return "", "", "", false, qerr
			}
			if rows != 0 {
				return refuse("planned matter was removed; a tombstoned node is not a plan target")
			}
			return wait("backlog plan awaiting matter")
		}
		if n.Kind != ScaleMatter || n.Repo != e.Repo {
			return refuse("backlog plan target is not a live matter of this Repo")
		}
	}
	return "integrated", "", "", true, nil
}
