package store

// This opt-in lab deliberately lives beside the real append/project functions.
// It is not a production synchronization API or an authorization boundary.

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"sort"
	"strings"
	"time"
)

// ExperimentAction keeps the original envelope separate from a local effect.
// Baseline is the preceding subject event, NOT the command's causation ID.
// Parent is a paired-fixture structural observation, not a path/Clone mapping.
type ExperimentAction struct {
	Event          Event
	Baseline       string
	NativeBaseline string `json:",omitempty"`
	Parent         string
	Ancestors      []ExperimentBaseline
}

// ExperimentBaseline names source-observed ancestor evidence, not a timestamp.
type ExperimentBaseline struct {
	Subject, Event string
	NativeEvent    string `json:",omitempty"`
}

// experimentSourceVersion is the portable-source schema this build writes and
// interprets. The absent field is the same version-1 shape every export
// predating the marker already used, so only a nonzero marker can be
// incompatible.
const experimentSourceVersion = 1

// experimentSourceCompatible reports whether this build can interpret the
// marked source schema. Absent (0) is the legacy version-1 shape; anything
// else is retained as an inspectable conflict, never applied.
func experimentSourceCompatible(version int) bool {
	return version == 0 || version == experimentSourceVersion
}

// ExperimentSource is one complete committed source command and its origin.
// Version is the portable-source schema marker: absent (0) is the legacy
// version-1 shape, so stored envelopes and captured retries stay readable;
// ExperimentExport stamps experimentSourceVersion explicitly.
type ExperimentSource struct {
	Version int `json:",omitempty"`
	Host    string
	Actions []ExperimentAction
	// Derivative is nil for a complete raw group. When set, this source is the
	// separately identified portable unit of the raw group Parent names, never
	// a truncation of it: Actions are exactly the portable members. Excluded
	// members remain in the sender's local event log, not the wire envelope.
	Derivative *ExperimentDerivative `json:",omitempty"`
}

// ExperimentDerivative marks an exported source as the portable unit of one
// raw command group. Parent is that group's identity — the shared origin
// event. The unit's atomicity covers Actions alone.
type ExperimentDerivative struct {
	Parent string
	// Omitted is read-only legacy evidence. Keep it decodable for retained
	// sources and rebuilds, but never include excluded raw actions in exports.
	Omitted []ExperimentAction `json:",omitempty"`
}

// ExperimentDecision links retained source activity to local representation.
type ExperimentDecision struct {
	Source      ExperimentSource
	Disposition string // integrated, conflict, or pending
	Reason      string
	Effects     []string // aligned with Actions; empty for unresolved groups
	key, target string   // local correspondence scope; never part of source JSON
}

// Only private correspondence keys are namespaced, never source/native IDs.
// Existing lab keys remain readable; a retained destination cannot be changed.
func experimentKey(repo, id string) string {
	if repo == "" {
		return id
	}
	return repo + ":" + id
}

func experimentOriginalID(key string) string {
	return key[strings.LastIndex(key, ":")+1:]
}

// experimentOriginVerified reports whether the claimed host is recorded export
// provenance for this native event. The first origin stays immutable;
// experiment_native_origin_hosts appends every installation that exported the
// same event, so a retired association's fresh installation still verifies.
// Stores predating that table fall back to the single first origin.
func experimentOriginVerified(ctx context.Context, q querier, id, host string, hostsTable bool) (bool, error) {
	var n int
	err := q.QueryRowContext(ctx, `SELECT COUNT(*) FROM experiment_native_origins WHERE id=? AND host=?`, id, host).Scan(&n)
	if err != nil || n != 0 || !hostsTable {
		return n != 0, err
	}
	err = q.QueryRowContext(ctx, `SELECT COUNT(*) FROM experiment_native_origin_hosts WHERE id=? AND host=?`, id, host).Scan(&n)
	return n != 0, err
}

// A native identity authenticates the whole action, not just its Event.
// Rechecks need the same immutable evidence proof as first imports.
func experimentNativeMatch(ctx context.Context, v View, a ExperimentAction, host string, hostsTable bool) (present, verified bool, err error) {
	e := a.Event
	native, err := v.eventList(ctx, `SELECT `+eventColumns+` FROM events WHERE id=?`, e.ID)
	if err != nil || len(native) == 0 {
		return false, false, err
	}
	origin, err := experimentOriginVerified(ctx, v.q, e.ID, host, hostsTable)
	if err != nil || !origin || !reflect.DeepEqual(native[0], e) {
		return true, false, err
	}
	// Exact capture equality proves the action. A mismatch may instead be an
	// earlier genuine export using another original alias of the same effect;
	// validate that historical correspondence below, without rewriting bytes.
	cached, err := exchangeTableExists(ctx, v.q, "exchange_exports")
	if err != nil {
		return true, false, err
	}
	if cached {
		var raw string
		err := v.q.QueryRowContext(ctx, `SELECT envelope FROM exchange_exports WHERE repo=? AND group_id=?`, e.Repo, e.Correlation).Scan(&raw)
		if err == nil {
			var change ExchangeChange
			var source ExperimentSource
			if err := json.Unmarshal([]byte(raw), &change); err != nil {
				return true, false, err
			}
			if err := json.Unmarshal([]byte(change.Source), &source); err != nil {
				return true, false, err
			}
			for _, original := range source.Actions {
				if original.Event.ID == e.ID && reflect.DeepEqual(original, a) {
					return true, true, nil
				}
			}
		}
		if err != nil && !errors.Is(err, sql.ErrNoRows) {
			return true, false, err
		}
	}
	// Direct lab exports need no exchange cache. Derive their observations
	// from immutable history at the action/group boundary, never live nodes.
	expected := ExperimentAction{Event: e}
	history, err := experimentSubjectHistory(ctx, v, e.Subject)
	if err != nil {
		return true, false, err
	}
	for _, prior := range history {
		if prior.ID < e.ID && experimentPortableBaseline(prior, e.Repo) {
			expected.Baseline = prior.ID
		}
	}
	parent, replacement, err := experimentHistoricalParent(ctx, v, e.Repo, e.Subject, e.ID)
	if err != nil {
		return true, false, err
	}
	expected.Parent = parent
	if expected.Baseline == "" {
		expected.Baseline = replacement
	}
	for parent != "" {
		b := ExperimentBaseline{Subject: parent}
		history, err := experimentSubjectHistory(ctx, v, parent)
		if err != nil {
			return true, false, err
		}
		for _, prior := range history {
			if prior.ID < e.Correlation && experimentPortableBaseline(prior, e.Repo) {
				b.Event = prior.ID
			}
		}
		expected.Ancestors = append(expected.Ancestors, b)
		parent, _, err = experimentHistoricalParent(ctx, v, e.Repo, parent, e.ID)
		if err != nil {
			return true, false, err
		}
	}
	// Export canonicalizes integration effects to retained original IDs.
	// Verify that correspondence, rather than choosing today's first alias:
	// another equivalent source may have acquired the same effect afterward.
	observation := func(original, native, observed string) (bool, error) {
		if original == observed {
			if native != "" {
				return false, nil
			}
			// Generated local effects must name their retained original, as
			// the exporter does; clearing Native* cannot turn one native.
			var effect bool
			err := v.q.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM experiment_effects WHERE id=?)`, observed).Scan(&effect)
			return !effect, err
		}
		if native != observed {
			return false, nil
		}
		rows, err := v.q.QueryContext(ctx, `SELECT a.id FROM experiment_actions a JOIN experiment_effects e ON a.effect=e.id WHERE a.effect=?`, observed)
		if err != nil {
			return false, err
		}
		defer func() { _ = rows.Close() }()
		for rows.Next() {
			var id string
			if err := rows.Scan(&id); err != nil {
				return false, err
			}
			if experimentOriginalID(id) == original {
				return true, nil
			}
		}
		return false, rows.Err()
	}
	ok, err := observation(a.Baseline, a.NativeBaseline, expected.Baseline)
	if err != nil || !ok {
		return true, false, err
	}
	expected.Baseline, expected.NativeBaseline = a.Baseline, a.NativeBaseline
	if len(a.Ancestors) != len(expected.Ancestors) {
		return true, false, nil
	}
	for i := range expected.Ancestors {
		b, claimed := &expected.Ancestors[i], a.Ancestors[i]
		ok, err := observation(claimed.Event, claimed.NativeEvent, b.Event)
		if err != nil || !ok {
			return true, false, err
		}
		b.Event, b.NativeEvent = claimed.Event, claimed.NativeEvent
	}
	return true, reflect.DeepEqual(expected, a), nil
}

// Parentage is immutable birth evidence, including replacement births filed
// under the replaced subject. Tombstones and later lifecycle are irrelevant.
func experimentHistoricalParent(ctx context.Context, v View, repo, subject, before string) (parent, replacement string, err error) {
	history, err := experimentSubjectHistory(ctx, v, subject)
	if err != nil {
		return "", "", err
	}
	for _, e := range history {
		if e.ID < before && e.Repo == repo && e.Type == TypeWorkSnapshot {
			p, err := decodeWorkSnapshot(e)
			if err != nil {
				return "", "", err
			}
			for _, n := range p.Snapshot.Nodes {
				if n.ID == subject && p.References[subject] == "" {
					return n.Parent, "", nil
				}
			}
		}
		if e.ID < before && (e.Type == TypeMatterCreated || e.Type == TypeStageCreated || e.Type == TypeStepCreated || e.Type == TypeStepInserted) {
			var birth NodeBirth
			if err := decode(e, &birth); err != nil {
				return "", "", err
			}
			return birth.Parent, "", nil
		}
	}
	// Snapshot births are filed under the import operation, not each node.
	// Read their immutable mapping rather than treating transfer origins as
	// authenticated sync aliases or consulting today's mutable node state.
	imports, err := v.eventList(ctx, `SELECT `+eventColumns+` FROM events WHERE repo=? AND type=? AND id<? AND EXISTS (SELECT 1 FROM json_each(events.payload,'$.plan.mapping') WHERE value=?) ORDER BY id`, repo, TypeWorkImported, before, subject)
	if err != nil {
		return "", "", err
	}
	for _, e := range imports {
		var p transferImported
		if err := decodeStrict(e, &p); err != nil {
			return "", "", err
		}
		for _, n := range p.Snapshot.Nodes {
			if p.Plan.Mapping[n.ID] == subject && !p.Plan.Reused[n.ID] {
				return p.Plan.Mapping[n.Parent], "", nil
			}
		}
	}
	births, err := v.eventList(ctx, `SELECT `+eventColumns+` FROM events WHERE repo=? AND type=? AND id<? AND json_extract(payload,'$.replacement')=? ORDER BY id`, repo, TypeStepReplaced, before, subject)
	if err != nil || len(births) == 0 {
		return "", "", err
	}
	parent, _, err = experimentHistoricalParent(ctx, v, repo, births[0].Subject, births[0].ID)
	return parent, births[0].ID, err
}

// Automatic recovery is limited to the old false-echo/ancestor admission
// outcomes. Other settled conflicts still require their existing explicit
// recheck; a later local edit must not silently approve a dependency cycle.
func (s *Store) experimentRecoverableConflict(ctx context.Context, d ExperimentDecision, target string) (bool, error) {
	if d.Reason == "incompatible ancestor lifecycle" || d.Reason == "ancestor history diverged from its start" {
		return true, nil
	}
	if !experimentSourceCompatible(d.Source.Version) {
		return false, nil
	}
	hosts, err := exchangeTableExists(ctx, s.db, "experiment_native_origin_hosts")
	if err != nil {
		return false, err
	}
	v := View{q: s.db, schemaVersion: s.version}
	for _, a := range d.Source.Actions {
		if a.Event.Repo != target || !experimentExchangePortable(a.Event, target) {
			return false, nil
		}
		_, verified, err := experimentNativeMatch(ctx, v, a, d.Source.Host, hosts)
		if err != nil || !verified {
			return false, err
		}
	}
	return len(d.Source.Actions) != 0, nil
}

func experimentSourceKey(ctx context.Context, q querier, repo, id string) (string, error) {
	scope, err := exchangeScope(ctx, q, repo)
	if err != nil {
		return "", err
	}
	if scope != repo {
		return experimentKey(scope, id), nil
	}
	var old string
	err = q.QueryRowContext(ctx, `SELECT id FROM experiment_sources WHERE id=? AND target_repo=?`, id, repo).Scan(&old)
	if err == nil {
		return old, nil
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return "", err
	}
	return experimentKey(repo, id), nil
}

// Legacy unscoped public helpers remain usable when identity is unambiguous.
// Bound callers always select the target explicitly rather than using this.
func experimentUniqueKey(ctx context.Context, q querier, id string) (string, error) {
	rows, err := q.QueryContext(ctx, `SELECT id FROM experiment_sources WHERE json_extract(source,'$.Actions[0].Event.ID')=?`, id)
	if err != nil {
		return "", err
	}
	defer func() { _ = rows.Close() }()
	key := ""
	for rows.Next() {
		if key != "" {
			return "", fmt.Errorf("experiment: ambiguous group; select its target Repo")
		}
		if err := rows.Scan(&key); err != nil {
			return "", err
		}
	}
	if err := rows.Err(); err != nil {
		return "", err
	}
	if key == "" {
		return "", sql.ErrNoRows
	}
	return key, nil
}

// ExperimentEvaluation is a changed decision or changed supporting evidence.
type ExperimentEvaluation struct {
	Kind                string // import or recheck; neither is work activity
	PreviousDisposition string
	Decision            ExperimentDecision
	Evidence            []string
	EvaluatedAt         time.Time
}

// ExperimentAcknowledgement is local handling, never integration or resolution.
type ExperimentAcknowledgement struct {
	Statement      string
	AcknowledgedAt time.Time
}

// EnableSharedStoreExperiment creates only lab tables, on explicit invocation.
// Normal Open and normal migrations do not create or consult these tables.
func (s *Store) EnableSharedStoreExperiment(ctx context.Context) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	_, err = tx.ExecContext(ctx, `CREATE TABLE IF NOT EXISTS experiment_sources (
	 id TEXT PRIMARY KEY, source TEXT NOT NULL, decision TEXT NOT NULL, target_repo TEXT NOT NULL DEFAULT '');
	 CREATE TABLE IF NOT EXISTS experiment_actions (
	 id TEXT PRIMARY KEY, source_group TEXT NOT NULL REFERENCES experiment_sources(id),
	 effect TEXT REFERENCES events(id), baseline TEXT NOT NULL);
	 CREATE TABLE IF NOT EXISTS experiment_effects (id TEXT PRIMARY KEY REFERENCES events(id));
	 CREATE TABLE IF NOT EXISTS experiment_evaluations (
	 source_group TEXT NOT NULL REFERENCES experiment_sources(id), ordinal INTEGER NOT NULL,
	 evaluation TEXT NOT NULL, PRIMARY KEY(source_group, ordinal));
	 CREATE TABLE IF NOT EXISTS experiment_acknowledgements (
	 source_group TEXT PRIMARY KEY REFERENCES experiment_sources(id), acknowledgement TEXT NOT NULL);
	 CREATE TABLE IF NOT EXISTS experiment_native_origins (id TEXT PRIMARY KEY REFERENCES events(id), host TEXT NOT NULL);
	 CREATE TABLE IF NOT EXISTS experiment_native_origin_hosts (
	 id TEXT NOT NULL REFERENCES events(id), host TEXT NOT NULL, PRIMARY KEY(id, host));
	 CREATE TABLE IF NOT EXISTS experiment_collisions (
	 id TEXT NOT NULL, source TEXT NOT NULL, target_repo TEXT NOT NULL, decision TEXT NOT NULL,
	 PRIMARY KEY(source, target_repo));
	 CREATE TRIGGER IF NOT EXISTS experiment_source_immutable BEFORE UPDATE OF id, source ON experiment_sources
	 BEGIN SELECT RAISE(ABORT, 'experiment source is immutable'); END;
	 CREATE TRIGGER IF NOT EXISTS experiment_source_no_delete BEFORE DELETE ON experiment_sources
	 BEGIN SELECT RAISE(ABORT, 'experiment source is immutable'); END;`)
	if err != nil {
		return err
	}
	return tx.Commit()
}

// ExperimentExport derives baseline evidence from the immutable local log.
// Export is explicit: the host label is fixture input, not authenticated identity.
// Integration effects cannot become new original user activities.
func (s *Store) ExperimentExport(ctx context.Context, host, correlation string) (ExperimentSource, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return ExperimentSource{}, err
	}
	defer func() { _ = tx.Rollback() }()
	v := View{q: tx, schemaVersion: s.version}
	group, err := v.EventsOfChain(ctx, correlation)
	if err != nil {
		return ExperimentSource{}, err
	}
	if host == "" || len(group) == 0 {
		return ExperimentSource{}, fmt.Errorf("experiment: host and committed group required")
	}
	out := ExperimentSource{Version: experimentSourceVersion, Host: host}
	for _, ev := range group {
		var count int
		if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM experiment_effects WHERE id=?`, ev.ID).Scan(&count); err != nil {
			return out, err
		}
		if count != 0 || ev.Actor == integrationEffectActor {
			return out, fmt.Errorf("experiment: integration effects are not source actions")
		}
		history, err := experimentSubjectHistory(ctx, v, ev.Subject)
		if err != nil {
			return out, err
		}
		a := ExperimentAction{Event: ev}
		for _, prior := range history {
			if prior.ID >= ev.ID {
				continue
			}
			if prior.Type == TypeMatterCreated || prior.Type == TypeStageCreated || prior.Type == TypeStepCreated || prior.Type == TypeStepInserted {
				var birth NodeBirth
				if err := decode(prior, &birth); err != nil {
					return out, err
				}
				a.Parent = birth.Parent
			}
			// Baseline names the last prior event a paired receiver can
			// actually hold — the portable-plane tail. Non-portable families
			// never leave this store, so naming one would make every later
			// portable event on the subject an unresolvable dependency on a
			// fresh peer: permanent pending, a silent stall.
			if experimentPortableBaseline(prior, ev.Repo) {
				a.Baseline = prior.ID
			}
		}
		if a.Parent == "" {
			// step.replaced files a replacement's birth under the replaced
			// node's subject, so this node's own history never shows one;
			// the projection's birth_event still names it. Recover the
			// structural evidence across that subject boundary — the
			// replaced node's parent is the one the replacement inherited —
			// and, for the first event on the node, anchor its baseline on
			// the birth itself.
			var birthID string
			err := tx.QueryRowContext(ctx, `SELECT birth_event FROM nodes WHERE id=?`, ev.Subject).Scan(&birthID)
			if err != nil && !errors.Is(err, sql.ErrNoRows) {
				return out, err
			}
			if err == nil {
				var replacedID, birthType string
				if err := tx.QueryRowContext(ctx, `SELECT subject, type FROM events WHERE id=?`, birthID).Scan(&replacedID, &birthType); err != nil {
					return out, err
				}
				if birthType == TypeWorkImported || birthType == TypeWorkSnapshot {
					a.Parent, _, err = experimentHistoricalParent(ctx, v, ev.Repo, ev.Subject, ev.ID)
					if err != nil {
						return out, err
					}
				}
				if birthType == TypeStepReplaced {
					var parent string
					if err := tx.QueryRowContext(ctx, `SELECT COALESCE(parent,'') FROM nodes WHERE id=?`, replacedID).Scan(&parent); err != nil {
						return out, err
					}
					a.Parent = parent
					if a.Baseline == "" {
						a.Baseline = birthID
					}
				}
			}
		}
		for parent := a.Parent; parent != ""; {
			history, err := experimentSubjectHistory(ctx, v, parent)
			if err != nil {
				return out, err
			}
			b := ExperimentBaseline{Subject: parent}
			for _, prior := range history {
				if prior.ID >= group[0].ID {
					break
				}
				// Same portable-plane rule as the action baseline: the
				// ancestor evidence a receiver compares against its own
				// portable tail must name an event sync can actually carry.
				if experimentPortableBaseline(prior, ev.Repo) {
					b.Event = prior.ID
				}
			}
			// The chain continues through the projected parent, never through
			// whichever event happens to be last: a started, finished or
			// otherwise-transitioned ancestor still has exactly one parent,
			// including one born inside this same group.
			var next string
			if err := tx.QueryRowContext(ctx, `SELECT COALESCE(parent,'') FROM nodes WHERE id=?`, parent).Scan(&next); err != nil {
				if !errors.Is(err, sql.ErrNoRows) {
					return out, err
				}
				next = ""
			}
			a.Ancestors = append(a.Ancestors, b)
			parent = next
		}
		// A generated local effect is not a portable baseline. Refer back to
		// its retained original so independently initialized stores can resolve it.
		canonical := func(id string) (string, error) {
			var original string
			err := tx.QueryRowContext(ctx, `SELECT a.id FROM experiment_actions a JOIN experiment_effects e ON a.effect=e.id WHERE a.effect=? ORDER BY a.id LIMIT 1`, id).Scan(&original)
			if errors.Is(err, sql.ErrNoRows) {
				return id, nil
			}
			return experimentOriginalID(original), err
		}
		observed := a.Baseline
		a.Baseline, err = canonical(a.Baseline)
		if err != nil {
			return out, err
		}
		if a.Baseline != observed {
			a.NativeBaseline = observed
		}
		for i := range a.Ancestors {
			observed := a.Ancestors[i].Event
			a.Ancestors[i].Event, err = canonical(a.Ancestors[i].Event)
			if err != nil {
				return out, err
			}
			if a.Ancestors[i].Event != observed {
				a.Ancestors[i].NativeEvent = observed
			}
		}
		out.Actions = append(out.Actions, a)
	}
	// Remember locally supplied provenance before any echo can claim it. The
	// first origin stays immutable; a retired association may re-export the
	// same native event under its fresh installation, recorded append-only.
	// This is fixture attribution, not authority conferred by an incoming envelope.
	for _, ev := range group {
		if _, err := tx.ExecContext(ctx, `INSERT OR IGNORE INTO experiment_native_origins VALUES (?,?)`, ev.ID, host); err != nil {
			return out, err
		}
		if _, err := tx.ExecContext(ctx, `INSERT OR IGNORE INTO experiment_native_origin_hosts VALUES (?,?)`, ev.ID, host); err != nil {
			return out, err
		}
	}
	return out, tx.Commit()
}

// experimentPortableUnit projects one complete exported group into the
// portable unit a paired exchange may carry. A fully portable group returns
// unchanged; a mixed group returns its separately identified derivative —
// portable members as Actions, the rest staying strictly local.
// ok=false means no portable unit exists and the source must not upload. The
// projection is deterministic, so exchange_exports retries emit identical
// bytes and receivers dedup on the same source identity.
func experimentPortableUnit(source ExperimentSource, repo string) (ExperimentSource, bool) {
	if len(source.Actions) == 0 {
		return source, false
	}
	events := make([]Event, len(source.Actions))
	for i, a := range source.Actions {
		events[i] = a.Event
	}
	mask := experimentUnitMask(events, repo)
	unit := 0
	for _, member := range mask {
		if member {
			unit++
		}
	}
	if unit == 0 {
		return source, false
	}
	if unit == len(mask) {
		return source, true
	}
	out := ExperimentSource{Version: source.Version, Host: source.Host, Derivative: &ExperimentDerivative{Parent: source.Actions[0].Event.Correlation}}
	for i, a := range source.Actions {
		if mask[i] {
			out.Actions = append(out.Actions, a)
		}
	}
	return out, true
}

// ExperimentImport retains even unsupported content and conflicted groups.
// Only starts on explicitly paired objects can have effects in this lab.
// Decisions and effects share one writer reservation and one commit.
func (s *Store) ExperimentImport(ctx context.Context, source ExperimentSource) (ExperimentDecision, error) {
	return s.experimentImport(ctx, source, "")
}

// ExperimentImportBound retains foreign Repo identity and projects only into an
// explicitly chosen native Repo. It also enables the bounded portable slice:
// node births, planned starts and the create-once content writes.
func (s *Store) ExperimentImportBound(ctx context.Context, source ExperimentSource, repo string) (ExperimentDecision, error) {
	if _, err := s.Repo(ctx, repo); err != nil {
		return ExperimentDecision{}, err
	}
	return s.experimentImport(ctx, source, repo)
}

func (s *Store) experimentImport(ctx context.Context, source ExperimentSource, repo string, binding ...ExchangeBinding) (ExperimentDecision, error) {
	if source.Host == "" || len(source.Actions) == 0 {
		return ExperimentDecision{}, fmt.Errorf("experiment: empty source")
	}
	group := source.Actions[0].Event.ID
	seen := map[string]bool{}
	for i, a := range source.Actions {
		e := a.Event
		if !IsIdentityShaped(e.ID) || seen[e.ID] || e.Correlation != group || (i == 0 && e.Causation != group) || (i > 0 && !seen[e.Causation]) {
			return ExperimentDecision{}, fmt.Errorf("experiment: incomplete or malformed source group")
		}
		seen[e.ID] = true
	}
	// A derivative is the sender's explicit statement that only portable
	// members carry syncable facts: its Actions stay a closed chain and every
	// omitted member is retained evidence whose links still name members of
	// the same raw group. Omitted members are never classified and never get
	// effects; the unit's atomicity covers Actions alone.
	if d := source.Derivative; d != nil {
		if d.Parent != group {
			return ExperimentDecision{}, fmt.Errorf("experiment: derivative does not name its source group")
		}
		claimed := make(map[string]bool, len(seen)+len(d.Omitted))
		for id := range seen {
			claimed[id] = true
		}
		for _, a := range d.Omitted {
			e := a.Event
			if !IsIdentityShaped(e.ID) || claimed[e.ID] || e.Correlation != group {
				return ExperimentDecision{}, fmt.Errorf("experiment: malformed derivative evidence")
			}
			claimed[e.ID] = true
		}
		for _, a := range d.Omitted {
			if e := a.Event; e.Causation == e.ID || !claimed[e.Causation] {
				return ExperimentDecision{}, fmt.Errorf("experiment: malformed derivative evidence")
			}
		}
	}
	raw, err := json.Marshal(source)
	if err != nil {
		return ExperimentDecision{}, err
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return ExperimentDecision{}, err
	}
	defer func() { _ = tx.Rollback() }()
	if len(binding) != 0 {
		if err := checkExchangeBinding(ctx, tx, binding[0]); err != nil {
			return ExperimentDecision{}, err
		}
	}
	scope, err := exchangeScope(ctx, tx, repo)
	if err != nil {
		return ExperimentDecision{}, err
	}
	// IDs are global, not scoped to a subject or an equivalent transition.
	// Compare untouched original evidence before Repo/baseline mapping and before
	// reserving source/action IDs that a later genuine self-echo must still use.
	v := View{q: tx, schemaVersion: s.version}
	originHosts, err := exchangeTableExists(ctx, tx, "experiment_native_origin_hosts")
	if err != nil {
		return ExperimentDecision{}, err
	}
	// An unreadable source schema is never matched against native evidence
	// under the wrong interpretation: its retained conflict names the
	// rejected version, not a coincidental identity comparison.
	if experimentSourceCompatible(source.Version) {
		claims := append([]ExperimentAction{}, source.Actions...)
		if source.Derivative != nil {
			claims = append(claims, source.Derivative.Omitted...)
		}
		for _, a := range claims {
			present, verified, err := experimentNativeMatch(ctx, v, a, source.Host, originHosts)
			if err != nil {
				return ExperimentDecision{}, err
			}
			if !present || verified {
				continue
			}
			result := ExperimentDecision{Source: source, Disposition: "conflict", Reason: "source identity differs from immutable native evidence or native origin is unverified"}
			encoded, err := json.Marshal(result)
			if err != nil {
				return ExperimentDecision{}, err
			}
			if _, err := tx.ExecContext(ctx, `INSERT OR IGNORE INTO experiment_collisions VALUES (?,?,?,?)`, group, string(raw), scope, string(encoded)); err != nil {
				return ExperimentDecision{}, err
			}
			return result, tx.Commit()
		}
	}
	key, err := experimentSourceKey(ctx, tx, repo, group)
	if err != nil {
		return ExperimentDecision{}, err
	}
	var old, decision, target string
	err = tx.QueryRowContext(ctx, `SELECT source, decision, target_repo FROM experiment_sources WHERE id=?`, key).Scan(&old, &decision, &target)
	if err == nil {
		if old != string(raw) || target != repo {
			return ExperimentDecision{}, fmt.Errorf("experiment: immutable source identity reused with different data")
		}
		var result ExperimentDecision
		err = json.Unmarshal([]byte(decision), &result)
		return result, err
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return ExperimentDecision{}, err
	}
	// Legacy raw action keys and scoped keys reserve the same immutable identity
	// within a target. Check every action before projecting any group effect.
	for _, a := range source.Actions {
		var reserved bool
		if err := tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM experiment_actions a JOIN experiment_sources s ON s.id=a.source_group WHERE s.target_repo=? AND (a.id=? OR (?=? AND a.id=?)))`, repo, experimentKey(scope, a.Event.ID), scope, repo, a.Event.ID).Scan(&reserved); err != nil {
			return ExperimentDecision{}, err
		}
		if reserved {
			return ExperimentDecision{}, fmt.Errorf("experiment: immutable action identity reused by another source group")
		}
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO experiment_sources VALUES (?, ?, '', ?)`, key, string(raw), repo); err != nil {
		return ExperimentDecision{}, err
	}
	result, err := s.experimentEvaluate(ctx, tx, key, source, nil)
	if err != nil {
		return ExperimentDecision{}, err
	}
	return result, tx.Commit()
}

// ExperimentRecheck explicitly reconsiders immutable retained input. Delivery
// retry never calls this. Integrated groups keep their original mappings.
func (s *Store) ExperimentRecheck(ctx context.Context, group string) (ExperimentDecision, error) {
	key, err := experimentUniqueKey(ctx, s.db, group)
	if err != nil {
		return ExperimentDecision{}, err
	}
	return s.experimentRecheck(ctx, key)
}

// ExperimentRecheckBound reconsiders only this target Repo's retained source.
func (s *Store) ExperimentRecheckBound(ctx context.Context, repo, group string) (ExperimentDecision, error) {
	key, err := experimentSourceKey(ctx, s.db, repo, group)
	if err != nil {
		return ExperimentDecision{}, err
	}
	return s.experimentRecheck(ctx, key)
}

func (s *Store) experimentRecheck(ctx context.Context, group string, binding ...ExchangeBinding) (ExperimentDecision, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return ExperimentDecision{}, err
	}
	defer func() { _ = tx.Rollback() }()
	if len(binding) != 0 {
		if err := checkExchangeBinding(ctx, tx, binding[0]); err != nil {
			return ExperimentDecision{}, err
		}
	}
	var raw, current string
	if err := tx.QueryRowContext(ctx, `SELECT source, decision FROM experiment_sources WHERE id=?`, group).Scan(&raw, &current); err != nil {
		return ExperimentDecision{}, err
	}
	var source ExperimentSource
	var previous ExperimentDecision
	if err := json.Unmarshal([]byte(raw), &source); err != nil {
		return previous, err
	}
	if err := json.Unmarshal([]byte(current), &previous); err != nil {
		return previous, err
	}
	var target string
	if err := tx.QueryRowContext(ctx, `SELECT target_repo FROM experiment_sources WHERE id=?`, group).Scan(&target); err != nil {
		return previous, err
	}
	expected, err := experimentSourceKey(ctx, tx, target, source.Actions[0].Event.ID)
	if err != nil {
		return previous, err
	}
	if expected != group {
		return previous, fmt.Errorf("experiment: source belongs to a retired association")
	}
	if previous.Disposition == "integrated" {
		return previous, nil
	}
	result, err := s.experimentEvaluate(ctx, tx, group, source, &previous)
	if err != nil {
		return ExperimentDecision{}, err
	}
	return result, tx.Commit()
}

func (s *Store) experimentEvaluate(ctx context.Context, tx *sql.Tx, group string, source ExperimentSource, previous *ExperimentDecision) (ExperimentDecision, error) {
	if err := s.loadFloorFrom(ctx, tx); err != nil {
		return ExperimentDecision{}, err
	}
	v := View{q: tx, schemaVersion: s.version}
	result := ExperimentDecision{Source: source, Disposition: "integrated"}
	var target string
	if err := tx.QueryRowContext(ctx, `SELECT target_repo FROM experiment_sources WHERE id=?`, group).Scan(&target); err != nil {
		return ExperimentDecision{}, err
	}
	scope, err := exchangeScope(ctx, tx, target)
	if err != nil {
		return ExperimentDecision{}, err
	}
	originHosts, err := exchangeTableExists(ctx, tx, "experiment_native_origin_hosts")
	if err != nil {
		return ExperimentDecision{}, err
	}
	evidence, err := experimentEvidence(ctx, v, source, target)
	if err != nil {
		return ExperimentDecision{}, err
	}
	effects := make([]string, len(source.Actions))
	subjects := map[string]bool{}
	for _, a := range source.Actions {
		subjects[a.Event.Subject] = true
	}
	// One pass over the group: member i is classified against candidate state —
	// committed rows plus the folded effects of members 0..i-1 — then its effect
	// is appended and its ledger row written before member i+1 is read, so a
	// same-group baseline or parent resolves through the same experiment_actions
	// JOIN prior groups use. The savepoint keeps the unit atomic: the first
	// non-integrated member rolls every effect and ledger write back, and the
	// ledger is rewritten exactly as two-phase classification left it — every
	// action retained, none with an effect.
	if _, err := tx.ExecContext(ctx, `SAVEPOINT experiment_candidate`); err != nil {
		return ExperimentDecision{}, err
	}
	var local []Event
	for i, a := range source.Actions {
		baseline, eventID := a.Baseline, a.Event.ID
		sourceRepo := a.Event.Repo
		var present, verified bool
		if experimentSourceCompatible(source.Version) {
			present, verified, err = experimentNativeMatch(ctx, v, a, source.Host, originHosts)
			if err != nil {
				return ExperimentDecision{}, err
			}
		}
		// Recognize the original before mapping a NULL predecessor or consulting
		// today's subject/ancestor state. Never borrow another target's native
		// event, an integration effect, or an excluded family's identity.
		nativeEcho := verified && (target == "" || sourceRepo == target) && experimentExchangePortable(a.Event, sourceRepo)
		if target != "" {
			a.Event.Repo = target
			// gate.declared keys at its Repo (D42, D54): the source subject
			// is the source Repo's own identity, so on a bound target it
			// maps to the paired Repo exactly as ev.Repo does. The fold
			// refuses any subject that is not the Repo, so the remap must
			// land before classification and stamping see one pair; a
			// subject naming anything else stays put and conflicts.
			if a.Event.Type == TypeGateDeclared && a.Event.Subject == sourceRepo {
				a.Event.Subject = target
			}
		}
		snapshotEvidence := true
		if !nativeEcho && experimentSourceCompatible(source.Version) {
			snapshotEvidence, err = experimentSnapshotObservations(ctx, v, a, target)
			if err != nil {
				return ExperimentDecision{}, err
			}
		}
		if !nativeEcho && a.Baseline != "" {
			a.Baseline, err = experimentMappedEvidence(ctx, v, a.Baseline, target)
			if err != nil {
				return ExperimentDecision{}, err
			}
		}
		var disposition, reason, effect string
		var need bool
		if !experimentSourceCompatible(source.Version) {
			// The schema marker is a group-level property: an unreadable
			// version takes the same atomic path as a first-member conflict —
			// every action retained as evidence, none effected — so receipt,
			// cursor and later feed records are unaffected.
			disposition, reason = "conflict", fmt.Sprintf("incompatible source version %d", source.Version)
		} else if present && !verified {
			disposition, reason = "conflict", "source identity differs from immutable native evidence or native origin is unverified"
		} else if nativeEcho {
			disposition, effect = "integrated", eventID
		} else if !snapshotEvidence {
			disposition, reason = "conflict", "snapshot anchor does not prove bound object membership"
		} else if target != "" && experimentPortableMember(a.Event.Type) {
			disposition, reason, effect, need, err = experimentClassifyPortable(ctx, v, a, s)
		} else {
			disposition, reason, effect, need, err = experimentClassify(ctx, v, a, subjects, target, s)
		}
		if err != nil {
			return ExperimentDecision{}, err
		}
		if disposition != "integrated" {
			result.Disposition, result.Reason = disposition, reason
			if _, err := tx.ExecContext(ctx, `ROLLBACK TO SAVEPOINT experiment_candidate`); err != nil {
				return ExperimentDecision{}, err
			}
			for _, member := range source.Actions {
				if err := experimentRecordAction(ctx, tx, group, scope, member.Event.ID, member.Baseline, "", previous == nil); err != nil {
					return ExperimentDecision{}, err
				}
			}
			break
		}
		if need {
			repo := a.Event.Repo
			if target != "" {
				repo = target
			}
			ev, err := s.stamp(Request{Actor: integrationEffectActor, Env: Env{Repo: repo}}, Draft{Type: a.Event.Type, Subject: a.Event.Subject, Payload: a.Event.Payload})
			if err != nil {
				return ExperimentDecision{}, err
			}
			ev.Causation, ev.Correlation = ev.ID, ev.ID
			if len(local) > 0 {
				ev.Causation, ev.Correlation = local[len(local)-1].ID, local[0].ID
			}
			if err := appendEvent(ctx, tx, ev); err != nil {
				return ExperimentDecision{}, err
			}
			if err := applyEventVersion(ctx, tx, ev, s.version); err != nil {
				return ExperimentDecision{}, err
			}
			if _, err := tx.ExecContext(ctx, `INSERT INTO experiment_effects VALUES (?)`, ev.ID); err != nil {
				return ExperimentDecision{}, err
			}
			local = append(local, ev)
			effects[i] = ev.ID
		} else {
			effects[i] = effect
		}
		if err := experimentRecordAction(ctx, tx, group, scope, eventID, baseline, effects[i], previous == nil); err != nil {
			return ExperimentDecision{}, err
		}
	}
	if _, err := tx.ExecContext(ctx, `RELEASE SAVEPOINT experiment_candidate`); err != nil {
		return ExperimentDecision{}, err
	}
	if result.Disposition == "integrated" {
		result.Effects = effects
	}
	encoded, err := json.Marshal(result)
	if err != nil {
		return ExperimentDecision{}, err
	}
	evaluation := ExperimentEvaluation{Kind: "import", Decision: result, Evidence: evidence, EvaluatedAt: s.ids.now().UTC()}
	if previous != nil {
		evaluation.Kind, evaluation.PreviousDisposition = "recheck", previous.Disposition
	}
	var last string
	err = tx.QueryRowContext(ctx, `SELECT evaluation FROM experiment_evaluations WHERE source_group=? ORDER BY ordinal DESC LIMIT 1`, group).Scan(&last)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return ExperimentDecision{}, err
	}
	var old ExperimentEvaluation
	if last != "" {
		if err := json.Unmarshal([]byte(last), &old); err != nil {
			return ExperimentDecision{}, err
		}
	}
	if last == "" || !reflect.DeepEqual(old.Decision, result) || !reflect.DeepEqual(old.Evidence, evidence) {
		raw, err := json.Marshal(evaluation)
		if err != nil {
			return ExperimentDecision{}, err
		}
		if _, err := tx.ExecContext(ctx, `INSERT INTO experiment_evaluations SELECT ?, COALESCE(MAX(ordinal),0)+1, ? FROM experiment_evaluations WHERE source_group=?`, group, string(raw), group); err != nil {
			return ExperimentDecision{}, err
		}
	}
	if _, err := tx.ExecContext(ctx, `UPDATE experiment_sources SET decision=? WHERE id=?`, string(encoded), group); err != nil {
		return ExperimentDecision{}, err
	}
	return result, nil
}

// experimentRecordAction writes one member's ledger row inside the group's
// single pass: first evaluation inserts, recheck updates the retained effect in
// place. The baseline column always keeps the untouched source-space value,
// never the locally mapped one, so later exports and rechecks read the same
// immutable correspondence.
func experimentRecordAction(ctx context.Context, tx *sql.Tx, group, scope, eventID, baseline, effect string, insert bool) error {
	key := experimentKey(scope, eventID)
	if insert {
		_, err := tx.ExecContext(ctx, `INSERT INTO experiment_actions VALUES (?, ?, ?, ?)`, key, group, nullable(effect), baseline)
		return err
	}
	_, err := tx.ExecContext(ctx, `UPDATE experiment_actions SET effect=? WHERE (id=? OR id=?) AND source_group=?`, nullable(effect), key, eventID, group)
	return err
}

func experimentClassify(ctx context.Context, v View, a ExperimentAction, subjects map[string]bool, target string, s *Store) (string, string, string, bool, error) {
	e := a.Event
	conflict := func(reason string) (string, string, string, bool, error) { return "conflict", reason, "", false, nil }
	pending := func(reason string) (string, string, string, bool, error) { return "pending", reason, "", false, nil }
	// The step amendment family mutates an already-paired object like the
	// lifecycle matrix does, so it shares this bound-and-unbound path; only
	// step.inserted joins the birth classifier, bound-only like *.created.
	// The backlog outcome family is Repo-durable paired state in the same
	// position — entered births the entry row, the three exits mutate it — so
	// it classifies here on both paths too. Findings appends mutate an
	// existing node or backlog entry the same way, so content.appended joins
	// them here while content.created stays bound-only with the births.
	if e.Type == TypeStepReordered || e.Type == TypeStepReplaced || e.Type == TypeStepRemoved {
		return experimentClassifyAmendment(ctx, v, a)
	}
	if e.Type == TypeBacklogEntered || e.Type == TypeBacklogPlanned ||
		e.Type == TypeBacklogDeclined || e.Type == TypeBacklogDelegated {
		return experimentClassifyBacklog(ctx, v, a)
	}
	if e.Type == TypeContentAppended {
		return experimentClassifyFinding(ctx, v, a, s)
	}
	if e.Type == TypeDependencyAdded || e.Type == TypeDependencyRemoved {
		return experimentClassifyDependency(ctx, v, a)
	}
	if e.Type == TypeGateDeclared || e.Type == TypeGateClosed ||
		e.Type == TypeGateDismissed || e.Type == TypeGateExemptionRepaired {
		return experimentClassifyGate(ctx, v, a)
	}
	if e.Type == TypeReferenceAdded || e.Type == TypeReferenceRemoved ||
		e.Type == TypeReferenceRebound {
		return experimentClassifyReference(ctx, v, a)
	}
	spec, admitted := experimentLifecycleEdges[e.Type]
	if !admitted {
		return conflict("unsupported family; original payload retained, no content overwritten")
	}
	var p Transition
	if err := decodeStrict(e, &p); err != nil {
		return conflict("invalid transition payload")
	}
	// The payload must be exactly what the verb emits: fixed From/To per edge,
	// Reason only where the edge records one (cancel), Cascade only where the
	// verb moves ancestors on its own initiative (start). Anything else is not
	// a native lifecycle move and stays retained evidence.
	if p.From != spec.from || p.To != spec.to || (!spec.reason && p.Reason != "") || (!spec.cascade && p.Cascade) {
		return conflict("not a native " + spec.verb)
	}
	n, err := v.Node(ctx, e.Subject)
	if err != nil {
		return pending("missing live paired object")
	}
	if n.Repo != e.Repo || n.Parent != a.Parent || n.Kind != spec.kind {
		return conflict("paired structure differs")
	}
	if a.Baseline == "" {
		return pending("missing baseline evidence")
	}
	history, err := experimentSubjectHistory(ctx, v, e.Subject)
	if err != nil {
		return "", "", "", false, err
	}
	baseline, anchored, err := experimentBaselineIndex(ctx, v, e.Subject, history, a.Baseline)
	if err != nil {
		return "", "", "", false, err
	}
	if baseline < 0 && !anchored {
		return pending("unknown baseline dependency")
	}
	// The subject's portable-plane tail: non-portable families legitimately
	// diverge between paired stores — they never sync — so "the last event"
	// every check below compares against the mapped baseline means the last
	// admitted portable-family event, integration effects and native
	// portable-type events alike.
	tail := experimentPortableTail(history)
	if spec.verb == "started" {
		for parent := n.Parent; parent != ""; {
			ancestor, err := v.Node(ctx, parent)
			if err != nil {
				return pending("missing ancestor")
			}
			if !subjects[parent] {
				if ancestor.Lifecycle == Planned {
					return pending("incomplete ancestor cascade")
				}
				history, err := experimentSubjectHistory(ctx, v, parent)
				if err != nil {
					return "", "", "", false, err
				}
				at := experimentPortableTail(history)
				if at < 0 {
					return conflict("ancestor history diverged from its start")
				}
				last := history[at]
				// Native Start skips every non-planned ancestor, including a
				// completed or paused one. Require the exact observed portable
				// history, not a particular lifecycle or tail event type.
				observed := ""
				for _, b := range a.Ancestors {
					if b.Subject == parent {
						observed = b.Event
					}
				}
				if observed == "" {
					return pending("missing ancestor baseline evidence")
				}
				mapped, err := experimentMappedEvidence(ctx, v, observed, target)
				if err != nil {
					return "", "", "", false, err
				}
				if mapped != last.ID {
					var count int
					if err := v.q.QueryRowContext(ctx, `SELECT COUNT(*) FROM events WHERE id=?`, mapped).Scan(&count); err != nil {
						return "", "", "", false, err
					}
					if count == 0 {
						// An unseen correspondence cannot justify moving past
						// independently advanced ancestor history. A known exact
						// observation of that history was accepted above instead.
						if last.Type != TypeMatterStarted && last.Type != TypeStageStarted {
							return conflict("ancestor history diverged from its start")
						}
						return pending("unknown ancestor correspondence")
					}
					return conflict("ancestor history differs from source observation")
				}
			}
			parent = ancestor.Parent
		}
	}
	if tail < 0 {
		// Only a step.replaced birth leaves a node with no portable entry in
		// its own subject history — the fold files that birth under the
		// replaced node's subject — and a history holding only non-portable
		// events is the same as-born state on the portable plane, so the
		// baseline resolved above is the birth anchor and nothing else. The
		// node is still as-born, so only a transition the fold can make from
		// Planned may apply.
		if n.Lifecycle != spec.from {
			return conflict("lifecycle or subject history diverged; never reopen or infer from current " + string(n.Lifecycle))
		}
		return "integrated", "", "", true, nil
	}
	last := history[tail]
	// The portable event before the newest is the baseline anchor: the
	// portable-plane predecessor ordinarily, the birth_event itself for a
	// step.replaced-born node's first event — the only case where the anchor
	// is not in portable history.
	before := experimentPortableBefore(history, tail)
	if before == "" && anchored {
		before = a.Baseline
	}
	if n.Lifecycle == spec.to && before == a.Baseline && last.Type == e.Type {
		var represented Transition
		if err := decodeStrict(last, &represented); err != nil {
			return "", "", "", false, err
		}
		// Cascade legitimately varies between a cascade member and a direct
		// start of the same node; every other payload field must be identical
		// for the existing effect to represent this source.
		if represented.From != p.From || represented.To != p.To || represented.Reason != p.Reason || represented.TrackerPushLevel != p.TrackerPushLevel {
			return conflict("transition snapshots differ")
		}
		return "integrated", "", last.ID, false, nil
	}
	if n.Lifecycle != spec.from || last.ID != a.Baseline {
		return conflict("lifecycle or subject history diverged; never reopen or infer from current " + string(n.Lifecycle))
	}
	return "integrated", "", "", true, nil
}

func experimentMappedEvidence(ctx context.Context, v View, id, target string) (string, error) {
	scope, err := exchangeScope(ctx, v.q, target)
	if err != nil {
		return "", err
	}
	var mapped sql.NullString
	err = v.q.QueryRowContext(ctx, `SELECT a.effect FROM experiment_actions a JOIN experiment_sources s ON s.id=a.source_group WHERE (a.id=? OR (?=? AND a.id=?)) AND s.target_repo=?`, experimentKey(scope, id), scope, target, id, target).Scan(&mapped)
	if errors.Is(err, sql.ErrNoRows) {
		return id, nil
	}
	return mapped.String, err
}

func experimentEvidence(ctx context.Context, v View, source ExperimentSource, target string) ([]string, error) {
	subjects := map[string]bool{}
	var evidence []string
	for _, a := range source.Actions {
		// gate.declared's subject is its Repo: on a bound target the effect
		// lands under the paired Repo, so evidence must watch that subject
		// rather than the source-space one.
		subject := a.Event.Subject
		if target != "" && a.Event.Type == TypeGateDeclared && subject == a.Event.Repo {
			subject = target
		}
		subjects[subject] = true
		if a.Event.Type == TypeStageCreated || a.Event.Type == TypeStepCreated || a.Event.Type == TypeStepInserted {
			var p NodeBirth
			if decodeStrict(a.Event, &p) == nil {
				subjects[p.Parent] = true
			}
		}
		if a.Event.Type == TypeWorkSnapshot {
			if p, err := decodeWorkSnapshot(a.Event); err == nil {
				for _, n := range p.Snapshot.Nodes {
					subjects[n.ID] = true
				}
				for _, e := range p.Snapshot.Backlog {
					subjects[e.ID] = true
				}
				for _, original := range p.References {
					mapped, err := experimentMappedEvidence(ctx, v, original, target)
					if err != nil {
						return nil, err
					}
					evidence = append(evidence, "mapping:"+original+":"+mapped)
				}
			}
		}
		if a.Parent != "" {
			subjects[a.Parent] = true
		}
		for _, b := range a.Ancestors {
			subjects[b.Subject] = true
			mapped, err := experimentMappedEvidence(ctx, v, b.Event, target)
			if err != nil {
				return nil, err
			}
			evidence = append(evidence, "mapping:"+b.Event+":"+mapped)
		}
	}
	for subject := range subjects {
		history, err := experimentSubjectHistory(ctx, v, subject)
		if err != nil {
			return nil, err
		}
		last := "missing"
		for _, event := range history {
			// Evidence tracks the portable plane the classifiers consult:
			// a local non-portable fact is not a changed decision input.
			if (target == "" || event.Repo == target) && experimentPortableType(event.Type) {
				last = event.ID
			}
		}
		evidence = append(evidence, "subject:"+subject+":"+last)
	}
	sort.Strings(evidence)
	return evidence, nil
}

// ExperimentDecisionHistory returns only meaningful evaluations, not retries.
func (s *Store) ExperimentDecisionHistory(ctx context.Context, group string) ([]ExperimentEvaluation, error) {
	key, err := experimentUniqueKey(ctx, s.db, group)
	if err != nil {
		return nil, err
	}
	return experimentDecisionHistory(ctx, s.db, key)
}

func experimentDecisionHistory(ctx context.Context, q querier, group string) ([]ExperimentEvaluation, error) {
	rows, err := q.QueryContext(ctx, `SELECT evaluation FROM experiment_evaluations WHERE source_group=? ORDER BY ordinal`, group)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	var out []ExperimentEvaluation
	for rows.Next() {
		var raw string
		var e ExperimentEvaluation
		if err := rows.Scan(&raw); err != nil {
			return nil, err
		}
		if err := json.Unmarshal([]byte(raw), &e); err != nil {
			return nil, err
		}
		out = append(out, e)
	}
	return out, rows.Err()
}

// ExperimentAcknowledge records local handling without changing the outcome,
// dependency mappings, native events, or retained source. Repeating it is inert.
func (s *Store) ExperimentAcknowledge(ctx context.Context, group string) (ExperimentAcknowledgement, error) {
	key, err := experimentUniqueKey(ctx, s.db, group)
	if err != nil {
		return ExperimentAcknowledgement{}, err
	}
	return s.experimentAcknowledge(ctx, key)
}

// ExperimentAcknowledgeBound records handling only for this target Repo.
func (s *Store) ExperimentAcknowledgeBound(ctx context.Context, repo, group string) (ExperimentAcknowledgement, error) {
	key, err := experimentSourceKey(ctx, s.db, repo, group)
	if err != nil {
		return ExperimentAcknowledgement{}, err
	}
	return s.experimentAcknowledge(ctx, key)
}

func (s *Store) experimentAcknowledge(ctx context.Context, group string) (ExperimentAcknowledgement, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return ExperimentAcknowledgement{}, err
	}
	defer func() { _ = tx.Rollback() }()
	var raw string
	if err := tx.QueryRowContext(ctx, `SELECT decision FROM experiment_sources WHERE id=?`, group).Scan(&raw); err != nil {
		return ExperimentAcknowledgement{}, err
	}
	var d ExperimentDecision
	if err := json.Unmarshal([]byte(raw), &d); err != nil {
		return ExperimentAcknowledgement{}, err
	}
	var target string
	if err := tx.QueryRowContext(ctx, `SELECT target_repo FROM experiment_sources WHERE id=?`, group).Scan(&target); err != nil {
		return ExperimentAcknowledgement{}, err
	}
	expected, err := experimentSourceKey(ctx, tx, target, d.Source.Actions[0].Event.ID)
	if err != nil {
		return ExperimentAcknowledgement{}, err
	}
	if expected != group {
		return ExperimentAcknowledgement{}, fmt.Errorf("experiment: source belongs to a retired association")
	}
	if d.Disposition != "conflict" {
		return ExperimentAcknowledgement{}, fmt.Errorf("experiment: acknowledgement requires a conflict; it cannot integrate it")
	}
	err = tx.QueryRowContext(ctx, `SELECT acknowledgement FROM experiment_acknowledgements WHERE source_group=?`, group).Scan(&raw)
	if err == nil {
		var ack ExperimentAcknowledgement
		err = json.Unmarshal([]byte(raw), &ack)
		return ack, err
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return ExperimentAcknowledgement{}, err
	}
	ack := ExperimentAcknowledgement{Statement: "retained; not applied here", AcknowledgedAt: s.ids.now().UTC()}
	encoded, err := json.Marshal(ack)
	if err != nil {
		return ack, err
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO experiment_acknowledgements VALUES (?,?)`, group, string(encoded)); err != nil {
		return ack, err
	}
	return ack, tx.Commit()
}

// ExperimentActivities reports source time/host, decisions and mapped local
// effects. Existing Session still narrates native integration time: unchanged.
func (s *Store) ExperimentActivities(ctx context.Context) ([]ExperimentDecision, error) {
	return experimentActivities(ctx, s.db, true, "")
}

func experimentActivities(ctx context.Context, q querier, includeCollisions bool, targetRepo string) ([]ExperimentDecision, error) {
	query := `SELECT id,decision,target_repo FROM experiment_sources`
	var args []any
	if targetRepo != "" {
		scope, err := exchangeScope(ctx, q, targetRepo)
		if err != nil {
			return nil, err
		}
		query += ` WHERE target_repo=? AND (?=? OR substr(id,1,length(?)+1)=?||':')`
		args = append(args, targetRepo, scope, targetRepo, scope, scope)
		if includeCollisions {
			query += ` UNION ALL SELECT id,decision,? AS target_repo FROM experiment_collisions WHERE target_repo=?`
			args = append(args, targetRepo, scope)
		}
	} else if includeCollisions {
		query += ` UNION ALL SELECT id,decision,target_repo FROM experiment_collisions`
	}
	query += ` ORDER BY id,decision`
	rows, err := q.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	var out []ExperimentDecision
	for rows.Next() {
		var raw string
		var d ExperimentDecision
		var key, target string
		if err := rows.Scan(&key, &raw, &target); err != nil {
			return nil, err
		}
		if err := json.Unmarshal([]byte(raw), &d); err != nil {
			return nil, err
		}
		d.key, d.target = key, target
		out = append(out, d)
	}
	return out, rows.Err()
}

// ExperimentEffect identifies native representation and its local log time.
// RecordedAt is not the source's work time (even for equivalent local effects).
type ExperimentEffect struct {
	ID         string
	RecordedAt time.Time
}

// ExperimentActivity is original work with synchronization diagnostics separate.
type ExperimentActivity struct {
	Event                                Event
	Origin                               string
	Disposition                          string
	Effects                              []ExperimentEffect
	ReceivedAt, LastRecordedEvaluationAt *time.Time
	Acknowledgement                      *ExperimentAcknowledgement
}

// ExperimentActivitySnapshot combines native originals and retained sources in
// one committed snapshot. localOrigin is fixture attribution; empty is unknown.
// Source identity deduplicates echoes, not equivalent independent activities.
func (s *Store) ExperimentActivitySnapshot(ctx context.Context, localOrigin string) ([]ExperimentActivity, error) {
	return s.experimentActivitySnapshot(ctx, localOrigin, "")
}

// ExchangeActivitySnapshot scopes by the retained target mapping, not by the
// foreign event's original Repo ID (which must remain immutable).
func (s *Store) ExchangeActivitySnapshot(ctx context.Context, localOrigin, repo string) ([]ExperimentActivity, error) {
	return s.experimentActivitySnapshot(ctx, localOrigin, repo)
}

func (s *Store) experimentActivitySnapshot(ctx context.Context, localOrigin, repo string) ([]ExperimentActivity, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer func() { _ = tx.Rollback() }()
	v := View{q: tx, schemaVersion: s.version}
	events, err := v.Events(ctx)
	if err != nil {
		return nil, err
	}
	native := map[string]Event{}
	activities := map[string]ExperimentActivity{}
	for _, e := range events {
		native[e.ID] = e
		if repo != "" && e.Repo != repo {
			continue
		}
		var effect int
		if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM experiment_effects WHERE id=?`, e.ID).Scan(&effect); err != nil {
			return nil, err
		}
		if effect != 0 || e.Actor == integrationEffectActor {
			continue
		}
		activities[e.ID] = ExperimentActivity{Event: e, Origin: localOrigin, Disposition: "local"}
	}
	// Collision claims are diagnostic evidence, never canonical work. Exclude
	// their whole groups, including actions whose IDs are absent from the native
	// log. Ordinary canonical conflicts remain part of this activity snapshot.
	decisions, err := experimentActivities(ctx, tx, false, repo)
	if err != nil {
		return nil, err
	}
	for _, d := range decisions {
		group := d.key
		if repo != "" {
			if d.target != repo && (d.target != "" || d.Source.Actions[0].Event.Repo != repo) {
				continue
			}
		}
		history, err := experimentDecisionHistory(ctx, tx, group)
		if err != nil {
			return nil, err
		}
		var received, evaluated *time.Time
		if len(history) > 0 {
			if history[0].Kind == "import" {
				received = &history[0].EvaluatedAt
			}
			evaluated = &history[len(history)-1].EvaluatedAt
		}
		var raw string
		var ack *ExperimentAcknowledgement
		err = tx.QueryRowContext(ctx, `SELECT acknowledgement FROM experiment_acknowledgements WHERE source_group=?`, group).Scan(&raw)
		if err == nil {
			if err := json.Unmarshal([]byte(raw), &ack); err != nil {
				return nil, err
			}
		} else if !errors.Is(err, sql.ErrNoRows) {
			return nil, err
		}
		for i, a := range d.Source.Actions {
			key := experimentKey(d.target, a.Event.ID)
			activity := ExperimentActivity{Event: a.Event, Origin: d.Source.Host, Disposition: d.Disposition, ReceivedAt: received, LastRecordedEvaluationAt: evaluated, Acknowledgement: ack}
			if e, ok := native[a.Event.ID]; ok && e.Actor != integrationEffectActor {
				// A conflicting claim is inspectable in ExperimentActivities, not a
				// replacement for authentic work. Even exact echoes cannot supply
				// native attribution that was unknown to this local reader.
				if d.Disposition != "integrated" || !reflect.DeepEqual(e, a.Event) {
					continue
				}
				activity.Event, activity.Origin = e, localOrigin
				key = e.ID
			}
			if len(d.Effects) > i && d.Effects[i] != "" {
				e := native[d.Effects[i]]
				activity.Effects = []ExperimentEffect{{ID: e.ID, RecordedAt: e.OccurredAt}}
			}
			activities[key] = activity
		}
	}
	out := make([]ExperimentActivity, 0, len(activities))
	for _, a := range activities {
		out = append(out, a)
	}
	sort.Slice(out, func(i, j int) bool {
		if !out[i].Event.OccurredAt.Equal(out[j].Event.OccurredAt) {
			return out[i].Event.OccurredAt.Before(out[j].Event.OccurredAt)
		}
		if out[i].Origin != out[j].Origin {
			return out[i].Origin < out[j].Origin
		}
		return out[i].Event.ID < out[j].Event.ID
	})
	return out, tx.Commit()
}
