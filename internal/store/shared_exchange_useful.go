package store

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"reflect"
)

// Only independent Matter births and inline create-once Briefs extend the lab.
// Colliding identities/addresses/content remain retained conflicts, not merges.
func experimentClassifyUseful(ctx context.Context, v View, a ExperimentAction) (string, string, string, bool, error) {
	refuse := func(reason string) (string, string, string, bool, error) { return "conflict", reason, "", false, nil }
	wait := func(reason string) (string, string, string, bool, error) { return "pending", reason, "", false, nil }
	e := a.Event
	if e.ID != e.Correlation {
		return refuse("birth and Brief groups must be standalone")
	}
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
	if e.Type == TypeMatterCreated {
		var p NodeBirth
		if decodeStrict(e, &p) != nil || p.Parent != "" || p.Title == "" || p.Locator == "" || a.Baseline != "" {
			return refuse("invalid Matter birth")
		}
		if len(history) != 0 {
			return refuse("object identity already exists")
		}
		var count int
		if err := v.q.QueryRowContext(ctx, `SELECT COUNT(*) FROM nodes WHERE repo=? AND kind='matter' AND locator=? AND tombstone_event IS NULL`, e.Repo, p.Locator).Scan(&count); err != nil {
			return "", "", "", false, err
		}
		if count != 0 {
			return refuse("Matter address already occupied; identities not merged")
		}
		return "integrated", "", "", true, nil
	}
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
	if len(history) == 0 || a.Baseline == "" {
		return wait("missing Brief baseline")
	}
	if history[len(history)-1].ID != a.Baseline {
		return refuse("Brief subject history diverged")
	}
	return "integrated", "", "", true, nil
}
