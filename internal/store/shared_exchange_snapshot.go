package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"slices"
	"sort"

	"modernc.org/sqlite"
)

// experimentSubjectHistory observes the real snapshot root as a member's
// initial state anchor. It does not create per-object events or rewrite the
// root's subject. The projection's birth and immutable payload must agree;
// merely receiving a root (or sharing an archive origin) proves no membership.
func experimentSubjectHistory(ctx context.Context, v View, subject string) ([]Event, error) {
	history, err := v.EventsOfSubject(ctx, subject)
	if err != nil {
		return nil, err
	}
	// Root subjects name import operations, not member history. Only the
	// verified projection birth below can add a root as this object's anchor.
	history = slices.DeleteFunc(history, func(ev Event) bool { return ev.Type == TypeWorkSnapshot })
	if v.schemaVersion < 16 {
		return history, nil
	}
	var birth, repo, kind, parent string
	err = v.q.QueryRowContext(ctx, `SELECT birth_event,repo,kind,COALESCE(parent,'') FROM nodes WHERE id=?
	 UNION ALL SELECT birth_event,repo,'backlog','' FROM backlog_entries WHERE id=?`, subject, subject).Scan(&birth, &repo, &kind, &parent)
	if errors.Is(err, sql.ErrNoRows) {
		return history, nil
	}
	if err != nil {
		return nil, err
	}
	roots, err := v.eventList(ctx, `SELECT `+eventColumns+` FROM events WHERE id=? AND repo=? AND type=?`, birth, repo, TypeWorkSnapshot)
	if err != nil || len(roots) == 0 {
		return history, err
	}
	p, err := decodeWorkSnapshot(roots[0])
	if err != nil {
		return nil, err
	}
	member := false
	if p.References[subject] == "" {
		for _, n := range p.Snapshot.Nodes {
			if n.ID == subject && n.Kind == kind && n.Parent == parent {
				member = true
			}
		}
		for _, e := range p.Snapshot.Backlog {
			if e.ID == subject && kind == "backlog" {
				member = true
			}
		}
	}
	if member {
		history = append(history, roots[0])
		sort.Slice(history, func(i, j int) bool { return history[i].ID < history[j].ID })
	}
	return history, nil
}

// An unmapped original may name a native birth, never a receiver-private
// generated effect. Genuine Repo-bound original aliases still resolve normally.
func experimentSnapshotEvidence(ctx context.Context, v View, original, target string) (string, bool, error) {
	mapped, err := experimentMappedEvidence(ctx, v, original, target)
	if err != nil || mapped != original {
		return mapped, err == nil, err
	}
	var effect bool
	err = v.q.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM experiment_effects WHERE id=?)`, original).Scan(&effect)
	return mapped, !effect, err
}

// Root correspondence authenticates only the root. Every object observation
// must additionally belong to that root in this bound Repo's immutable state.
func experimentSnapshotObservations(ctx context.Context, v View, a ExperimentAction, target string) (bool, error) {
	observations := append([]ExperimentBaseline{{Subject: a.Event.Subject, Event: a.Baseline}}, a.Ancestors...)
	for _, b := range observations {
		mapped, portable, err := experimentSnapshotEvidence(ctx, v, b.Event, target)
		if err != nil || !portable {
			return false, err
		}
		roots, err := v.eventList(ctx, `SELECT `+eventColumns+` FROM events WHERE id=? AND type=?`, mapped, TypeWorkSnapshot)
		if err != nil {
			return false, err
		}
		if len(roots) == 0 {
			continue // Ordinary classifiers handle dependencies not yet received.
		}
		if roots[0].Repo != a.Event.Repo {
			return false, nil
		}
		history, err := experimentSubjectHistory(ctx, v, b.Subject)
		if err != nil {
			return false, err
		}
		member := false
		for _, event := range history {
			if event.ID == mapped && event.Type == TypeWorkSnapshot {
				member = true
			}
		}
		if !member {
			return false, nil
		}
	}
	return true, nil
}

// Development stores may contain the old private-only import. Preserve its
// identity and history, but diagnose missing portable prerequisites before HTTP
// rather than rewriting an import or sending a malformed descendant group.
func experimentLegacyImportDependency(ctx context.Context, v View, a ExperimentAction) error {
	ids := []string{a.Event.Subject, a.Parent}
	for _, b := range a.Ancestors {
		ids = append(ids, b.Subject)
	}
	switch a.Event.Type {
	case TypeStageCreated, TypeStepCreated, TypeStepInserted:
		var p NodeBirth
		if decode(a.Event, &p) == nil {
			ids = append(ids, p.Parent)
		}
	case TypeDependencyAdded, TypeDependencyRemoved:
		var p DependencyChange
		if decode(a.Event, &p) == nil {
			ids = append(ids, p.Blocker)
		}
	case TypeBacklogPlanned:
		var p BacklogPlanned
		if decode(a.Event, &p) == nil {
			ids = append(ids, p.Matter)
		}
	case TypeBacklogEntered:
		var p BacklogEntered
		if decode(a.Event, &p) == nil {
			ids = append(ids, p.OriginNode)
		}
	case TypeWorkSnapshot:
		p, err := decodeWorkSnapshot(a.Event)
		if err != nil {
			return err
		}
		for id := range p.References {
			ids = append(ids, id)
		}
	}
	seen := map[string]bool{}
	for _, id := range ids {
		for id != "" && !seen[id] {
			seen[id] = true
			var parent, typ, operation string
			err := v.q.QueryRowContext(ctx, `SELECT COALESCE(n.parent,''),e.type,e.subject FROM nodes n JOIN events e ON e.id=n.birth_event WHERE n.id=? AND n.repo=?
			 UNION ALL SELECT '',e.type,e.subject FROM backlog_entries b JOIN events e ON e.id=b.birth_event WHERE b.id=? AND b.repo=?`, id, a.Event.Repo, id, a.Event.Repo).Scan(&parent, &typ, &operation)
			if errors.Is(err, sql.ErrNoRows) {
				break
			}
			if err != nil {
				return err
			}
			if typ == TypeWorkImported {
				return fmt.Errorf("exchange: legacy import %s has no portable snapshot anchor for %s; work and cached source identities retained, automatic legacy conversion is not supported", operation, id)
			}
			id = parent
		}
	}
	return nil
}

// A snapshot is one atomic state fact. The receiver's fold checks structural
// collisions; this admission also verifies all bytes and genuine references
// before appending any effect. Missing content never leaves partial work.
func experimentClassifySnapshot(ctx context.Context, v View, a ExperimentAction, s *Store) (string, string, string, bool, error) {
	refuse := func(reason string) (string, string, string, bool, error) { return "conflict", reason, "", false, nil }
	wait := func(reason string) (string, string, string, bool, error) { return "pending", reason, "", false, nil }
	p, err := decodeWorkSnapshot(a.Event)
	if err != nil || !IsIdentityShaped(a.Event.Subject) || a.Baseline != "" || a.NativeBaseline != "" || a.Parent != "" || len(a.Ancestors) != 0 {
		return refuse("invalid imported snapshot profile")
	}
	for _, w := range p.Contents {
		if w.ByteLen > exchangeBlobMaxBytes && w.BlobRef != "" {
			return refuse("snapshot content exceeds transport blob limit")
		}
		ready, err := experimentContentReady(s, w)
		if err != nil {
			return "", "", "", false, err
		}
		if !ready.integrity {
			return refuse("snapshot content integrity mismatch")
		}
		if !ready.bytes {
			return wait(ready.reason)
		}
	}
	for id, original := range p.References {
		mapped, portable, err := experimentSnapshotEvidence(ctx, v, original, a.Event.Repo)
		if err != nil {
			return "", "", "", false, err
		}
		if !portable {
			return refuse("snapshot reference names a private generated effect, not a portable original")
		}
		var birth string
		err = v.q.QueryRowContext(ctx, `SELECT birth_event FROM nodes WHERE id=? AND repo=?
		 UNION ALL SELECT birth_event FROM backlog_entries WHERE id=? AND repo=?
		 UNION ALL SELECT c.birth_event FROM content c JOIN nodes n ON n.id=c.node WHERE c.id=? AND n.repo=?
		 UNION ALL SELECT c.birth_event FROM content c JOIN backlog_entries b ON b.id=c.node WHERE c.id=? AND b.repo=?
		 UNION ALL SELECT e.birth_event FROM edges e JOIN nodes n ON n.id=e.blocked WHERE e.id=? AND n.repo=?`, id, a.Event.Repo, id, a.Event.Repo, id, a.Event.Repo, id, a.Event.Repo, id, a.Event.Repo).Scan(&birth)
		if errors.Is(err, sql.ErrNoRows) {
			return wait("snapshot awaiting established member")
		}
		if err != nil {
			return "", "", "", false, err
		}
		if birth != mapped {
			return refuse("snapshot reference differs from established birth")
		}
	}
	// Exercise the exact fold under a savepoint so user conflicts are retained
	// as decisions, not SQL failures that wedge the inbox. No event or effect
	// survives this check; the ordinary evaluator commits the real fold.
	tx, ok := v.q.(*sql.Tx)
	if !ok {
		return refuse("snapshot requires transactional classification")
	}
	if _, err := tx.ExecContext(ctx, `SAVEPOINT snapshot_admission`); err != nil {
		return "", "", "", false, err
	}
	// Foreign event IDs are not in the local log. Use a real inert temporary
	// root for FK validation, then roll back everything unconditionally.
	ev, err := s.stamp(Request{Actor: integrationEffectActor, Env: Env{Repo: a.Event.Repo}}, Draft{Type: TypeWorkSnapshot, Subject: a.Event.Subject, Payload: a.Event.Payload})
	if err == nil {
		ev.Causation, ev.Correlation = ev.ID, ev.ID
		err = appendEvent(ctx, tx, ev)
	}
	var foldErr error
	if err == nil {
		foldErr = projectWorkSnapshot(ctx, tx, ev)
	}
	_, rollback := tx.ExecContext(ctx, `ROLLBACK TO SAVEPOINT snapshot_admission`)
	if rollback != nil {
		return "", "", "", false, rollback
	}
	if _, release := tx.ExecContext(ctx, `RELEASE SAVEPOINT snapshot_admission`); release != nil {
		return "", "", "", false, release
	}
	if err != nil {
		return "", "", "", false, err
	}
	if foldErr != nil {
		// Storage failures are not durable user conflicts. Abort the exchange
		// so a repair/retry can evaluate the same immutable source again.
		var databaseError *sqlite.Error
		if errors.As(foldErr, &databaseError) || errors.Is(foldErr, context.Canceled) || errors.Is(foldErr, context.DeadlineExceeded) {
			return "", "", "", false, foldErr
		}
		return refuse("snapshot identity, address, origin or state conflicts: " + foldErr.Error())
	}
	return "integrated", "", "", true, nil
}
