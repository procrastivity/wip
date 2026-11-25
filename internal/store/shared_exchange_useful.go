package store

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"reflect"
)

// experimentPortableMember reports whether the type belongs to the bounded
// portable slice: the three node births and the inline create-once Brief.
// Planned starts and everything else keep their own classifiers.
func experimentPortableMember(typ string) bool {
	switch typ {
	case TypeMatterCreated, TypeStageCreated, TypeStepCreated, TypeContentCreated:
		return true
	}
	return false
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
	case TypeMatterCreated, TypeStageCreated, TypeStepCreated:
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
	kind := map[string]Scale{TypeMatterCreated: ScaleMatter, TypeStageCreated: ScaleStage, TypeStepCreated: ScaleStep}[e.Type]
	var p NodeBirth
	if !IsIdentityShaped(e.Subject) || decodeStrict(e, &p) != nil || p.Title == "" || p.Locator == "" || a.Baseline != "" ||
		(kind == ScaleMatter) != (p.Parent == "") || (p.Parent != "" && !IsIdentityShaped(p.Parent)) {
		return refuse("invalid " + string(kind) + " birth")
	}
	if len(history) != 0 {
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
