package store

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"reflect"
	"slices"
)

// experimentPortableMember reports whether the type belongs to the bounded
// portable slice routed through experimentClassifyPortable on a bound target:
// the four node births — step.inserted is the positioned-birth verb's event,
// the same NodeBirth fold as step.created — and the inline create-once Brief.
// The step amendment family (reorder/replace/remove) is not here: like the
// lifecycle matrix it mutates an already-paired object, so experimentClassify
// admits it on both the bound and the unbound path.
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

// experimentExchangePortable is the single per-member predicate behind
// ExchangeStatus eligibility and portable derivatives: the event must be a
// repo-scoped original of an admitted family — the node births including the
// positioned step.inserted, the step amendment family, the inline
// create-once Brief and the lifecycle matrix above, exactly what
// experimentClassifyPortable and experimentClassify can project on a bound
// target. The predicate is per event, not per subject: reordered names the
// parent, replaced the old step and removed the step, and each classifier
// resolves its own subject shape. Batch/execution-scoped events carry no
// Repo, generated integration effects are never originals, and every
// unadmitted family stays local. The whitelist lives only here and in the
// import classifiers, never in a second list that could drift.
func experimentExchangePortable(ev Event, repo string) bool {
	if ev.Repo != repo || ev.Actor == integrationEffectActor {
		return false
	}
	if _, ok := experimentLifecycleEdges[ev.Type]; ok {
		return true
	}
	switch ev.Type {
	case TypeMatterCreated, TypeStageCreated, TypeStepCreated, TypeStepInserted,
		TypeStepReordered, TypeStepReplaced, TypeStepRemoved:
		return true
	case TypeContentCreated:
		var p ContentWritten
		return json.Unmarshal(ev.Payload, &p) == nil && p.Kind == KindBrief && p.Bytes != nil && p.BlobRef == ""
	}
	return false
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
func experimentClassifyPortable(ctx context.Context, v View, a ExperimentAction) (string, string, string, bool, error) {
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
		return experimentClassifyBrief(ctx, v, a, history)
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

// experimentClassifyBrief is the create-once inline Brief rule, unchanged from
// the standalone-group era: integrity-verified bytes, a globally unused content
// identity, a live Matter target and an untouched subject history tail.
func experimentClassifyBrief(ctx context.Context, v View, a ExperimentAction, history []Event) (string, string, string, bool, error) {
	refuse := func(reason string) (string, string, string, bool, error) { return "conflict", reason, "", false, nil }
	wait := func(reason string) (string, string, string, bool, error) { return "pending", reason, "", false, nil }
	e := a.Event
	var p ContentWritten
	if decodeStrict(e, &p) != nil || p.Kind != KindBrief || p.Bytes == nil || p.BlobRef != "" || !IsIdentityShaped(p.Content) {
		return refuse("only inline Brief supported; bytes unavailable or unsupported kind")
	}
	sum := sha256.Sum256(p.Bytes)
	if p.ByteLen != int64(len(p.Bytes)) || hex.EncodeToString(sum[:]) != p.SHA256 {
		return refuse("Brief integrity mismatch")
	}
	// Content identity is global, including tombstoned rows. A different
	// original cannot reuse it on another Matter (or revive its old slot).
	var count int
	if err := v.q.QueryRowContext(ctx, `SELECT COUNT(*) FROM content WHERE id=?`, p.Content).Scan(&count); err != nil {
		return "", "", "", false, err
	}
	if count != 0 {
		return refuse("content identity already exists; accepted bytes unchanged")
	}
	if len(history) == 0 {
		return wait("Brief awaiting Matter")
	}
	n, err := v.Node(ctx, e.Subject)
	if err != nil {
		return "", "", "", false, err
	}
	if n.Repo != e.Repo || n.Kind != ScaleMatter {
		return refuse("Brief target differs")
	}
	segments, err := v.ContentSegments(ctx, e.Subject, KindBrief)
	if err != nil {
		return "", "", "", false, err
	}
	if len(segments) > 0 {
		return refuse("competing Brief retained; accepted bytes unchanged")
	}
	if a.Baseline == "" {
		return wait("missing Brief baseline")
	}
	if history[len(history)-1].ID != a.Baseline {
		return refuse("Brief subject history diverged")
	}
	return "integrated", "", "", true, nil
}

// experimentBaselineIndex locates the mapped baseline inside the subject's
// local history. The ordinary answer is a history index; a node born by
// step.replaced is the exception — that birth is filed under the replaced
// node's subject, so the first event after it has no entry of its own here
// and anchors on nodes.birth_event instead, which the mapped baseline names
// directly. index is -1 when the baseline is not a subject-history event;
// anchored then says whether it is the birth anchor (true) or resolves to
// nothing local (false).
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
	// newest subject event is the baseline — history[-2] ordinarily, the
	// birth anchor for a step.replaced-born node's first event.
	if len(history) > 0 {
		last := history[len(history)-1]
		before := ""
		if len(history) >= 2 {
			before = history[len(history)-2].ID
		} else if anchored {
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
