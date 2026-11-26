package store

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"reflect"
	"sort"

	"github.com/procrastivity/wip/internal/transfer"
)

// All Snapshot object IDs and graph references are native IDs. Origin fields
// retain upstream provenance. References is a subset of those object IDs and
// names their established portable birth events; these members are NOT births
// of this root. Contents contains exactly one entry per Snapshot content ID,
// including reference members. Snapshot.Repo is the sender's destination Repo;
// projection always uses the receiver-mapped Event.Repo instead.
type workSnapshot struct {
	Version    int                       `json:"version"`
	Snapshot   *transfer.Snapshot        `json:"snapshot"`
	Contents   map[string]ContentWritten `json:"contents"`
	References map[string]string         `json:"references,omitempty"`
}

func transferObjectTable(kind string) string {
	switch kind {
	case "matter", "stage", "step":
		return "nodes"
	case "backlog":
		return "backlog_entries"
	case "edge":
		return "edges"
	default:
		return "content"
	}
}

func workSnapshotAnchor(ctx context.Context, q querier, kind, id string) (string, error) {
	var birth string
	if err := q.QueryRowContext(ctx, `SELECT birth_event FROM `+transferObjectTable(kind)+` WHERE id=?`, id).Scan(&birth); err != nil {
		return "", err
	}
	exists, err := exchangeTableExists(ctx, q, "experiment_actions")
	if err != nil || !exists {
		return birth, err
	}
	var original string
	err = q.QueryRowContext(ctx, `SELECT a.id FROM experiment_actions a JOIN experiment_effects e ON a.effect=e.id WHERE a.effect=? ORDER BY a.id LIMIT 1`, birth).Scan(&original)
	if err == sql.ErrNoRows {
		return birth, nil
	}
	if err != nil {
		return "", err
	}
	return experimentOriginalID(original), nil
}

// canonicalWorkSnapshot never queries mutable work metadata. The private
// reviewed snapshot and plan are the sole source of state; only established
// reference birth anchors and their immutable content representation are read.
func canonicalWorkSnapshot(ctx context.Context, q querier, p transferImported, recordedReferences ...map[string]string) (workSnapshot, error) {
	var out workSnapshot
	raw, err := json.Marshal(p.Snapshot)
	if err != nil {
		return out, err
	}
	var x transfer.Snapshot
	if err = json.Unmarshal(raw, &x); err != nil {
		return out, err
	}
	plan := p.Plan
	objects := transferObjects(p.Snapshot)
	if len(plan.Mapping) != len(objects) {
		return out, fmt.Errorf("store: snapshot mapping membership mismatch")
	}
	newContents, reused, matters := 0, 0, 0
	for _, o := range objects {
		if plan.Reused[o.id] {
			reused++
		} else if o.kind == "content" {
			newContents++
		}
		if o.kind == "matter" {
			matters++
		}
	}
	if len(plan.Reused) != reused || len(plan.Preconditions) != reused || len(p.Contents) != newContents || len(plan.Locators) != matters {
		return out, fmt.Errorf("store: snapshot plan/content membership mismatch")
	}
	out = workSnapshot{Version: 1, Snapshot: &x, Contents: map[string]ContentWritten{}, References: map[string]string{}}
	for _, o := range objects {
		id := plan.Mapping[o.id]
		if id == "" {
			return out, fmt.Errorf("store: incomplete snapshot mapping")
		}
		if plan.Reused[o.id] {
			var anchor string
			var e error
			if len(recordedReferences) != 0 {
				anchor = recordedReferences[0][id]
				e = validateWorkSnapshotAnchor(ctx, q, o.kind, id, anchor)
			} else {
				anchor, e = workSnapshotAnchor(ctx, q, o.kind, id)
			}
			if e != nil {
				return out, e
			}
			out.References[id] = anchor
		}
	}
	x.Repo = plan.Repo
	for i := range x.Selection.Matters {
		x.Selection.Matters[i] = plan.Mapping[x.Selection.Matters[i]]
	}
	for i := range x.Selection.Backlog {
		x.Selection.Backlog[i] = plan.Mapping[x.Selection.Backlog[i]]
	}
	x.Nodes = canonicalTransferNodes(x.Nodes)
	for i := range x.Nodes {
		n := &x.Nodes[i]
		if n.Kind == "matter" {
			n.Locator = plan.Locators[n.ID]
		}
		n.ID, n.Matter, n.Parent = plan.Mapping[n.ID], plan.Mapping[n.Matter], plan.Mapping[n.Parent]
	}
	for i := range x.Backlog {
		e := &x.Backlog[i]
		e.ID, e.Matter, e.OriginNode = plan.Mapping[e.ID], plan.Mapping[e.Matter], plan.Mapping[e.OriginNode]
	}
	for i := range x.Edges {
		e := &x.Edges[i]
		e.ID, e.Blocked, e.Blocker = plan.Mapping[e.ID], plan.Mapping[e.Blocked], plan.Mapping[e.Blocker]
	}
	x.Contents = transfer.CanonicalContents(x.Contents)
	for i := range x.Contents {
		c := &x.Contents[i]
		old := c.ID
		c.ID, c.Owner = plan.Mapping[c.ID], plan.Mapping[c.Owner]
		w, ok := p.Contents[old]
		if plan.Reused[old] {
			w = ContentWritten{Content: c.ID, Kind: ContentKind(c.Kind), ByteLen: c.Length, SHA256: c.SHA256}
			if err = q.QueryRowContext(ctx, `SELECT bytes,COALESCE(blob_ref,'') FROM content WHERE id=?`, c.ID).Scan(&w.Bytes, &w.BlobRef); err != nil {
				return out, err
			}
		} else if !ok {
			return out, fmt.Errorf("store: missing snapshot content %s", old)
		}
		out.Contents[c.ID] = w
	}
	x.Nodes = canonicalTransferNodes(x.Nodes)
	sort.Slice(x.Backlog, func(i, j int) bool { return x.Backlog[i].ID < x.Backlog[j].ID })
	sort.Slice(x.Edges, func(i, j int) bool { return x.Edges[i].ID < x.Edges[j].ID })
	x.Contents = transfer.CanonicalContents(x.Contents)
	sort.Strings(x.Selection.Matters)
	sort.Strings(x.Selection.Backlog)
	return out, nil
}

// decodeWorkSnapshot validates closed metadata and every content/reference
// shape without consulting local state or sidecar files. The receiver must
// authenticate reference correspondence and verify spilled bytes before fold.
func decodeWorkSnapshot(ev Event) (workSnapshot, error) {
	var p workSnapshot
	if err := transfer.DecodeJSON(ev.Payload, &p); err != nil {
		return p, err
	}
	if ev.Type != TypeWorkSnapshot || p.Version != 1 || p.Snapshot == nil {
		return p, fmt.Errorf("store: unsupported work snapshot")
	}
	if err := transfer.ValidateMetadata(p.Snapshot); err != nil {
		return p, err
	}
	x := p.Snapshot
	if !reflect.DeepEqual(x.Nodes, canonicalTransferNodes(x.Nodes)) || !reflect.DeepEqual(x.Contents, transfer.CanonicalContents(x.Contents)) || !sort.StringsAreSorted(x.Selection.Matters) || !sort.StringsAreSorted(x.Selection.Backlog) || !sort.SliceIsSorted(x.Backlog, func(i, j int) bool { return x.Backlog[i].ID < x.Backlog[j].ID }) || !sort.SliceIsSorted(x.Edges, func(i, j int) bool { return x.Edges[i].ID < x.Edges[j].ID }) {
		return p, fmt.Errorf("store: noncanonical snapshot order")
	}
	ids := map[string]bool{}
	for _, o := range transferObjects(p.Snapshot) {
		if o.id == ev.Subject {
			return p, fmt.Errorf("store: snapshot operation must not name a work member")
		}
		ids[o.id] = true
	}
	for id, anchor := range p.References {
		if !ids[id] {
			return p, fmt.Errorf("store: snapshot reference is not a member")
		}
		if _, err := timeOfID(anchor); err != nil {
			return p, fmt.Errorf("store: invalid snapshot reference anchor: %w", err)
		}
	}
	if p.Contents == nil || len(p.Contents) != len(p.Snapshot.Contents) {
		return p, fmt.Errorf("store: snapshot content membership mismatch")
	}
	for _, c := range p.Snapshot.Contents {
		w, ok := p.Contents[c.ID]
		if !ok || w.Content != c.ID || string(w.Kind) != c.Kind || w.ByteLen != c.Length || w.SHA256 != c.SHA256 || (w.Bytes == nil) == (w.BlobRef == "") || (w.BlobRef != "" && w.BlobRef != c.ID) {
			return p, fmt.Errorf("store: invalid snapshot content %s", c.ID)
		}
		if w.Bytes != nil {
			sum := sha256.Sum256(w.Bytes)
			if int64(len(w.Bytes)) != w.ByteLen || hex.EncodeToString(sum[:]) != w.SHA256 {
				return p, fmt.Errorf("store: snapshot content bytes mismatch")
			}
		}
	}
	mapping := map[string]string{}
	for id := range ids {
		mapping[id] = id
	}
	if err := transferContentOrder(p.Snapshot, mapping); err != nil {
		return p, err
	}
	return p, nil
}

// A companion validates the recorded alias, never today's choice among aliases.
func validateWorkSnapshotAnchor(ctx context.Context, q querier, kind, id, anchor string) error {
	var birth string
	if err := q.QueryRowContext(ctx, `SELECT birth_event FROM `+transferObjectTable(kind)+` WHERE id=?`, id).Scan(&birth); err != nil {
		return err
	}
	if birth == anchor {
		return nil
	}
	exists, err := exchangeTableExists(ctx, q, "experiment_actions")
	if err != nil {
		return err
	}
	if !exists {
		return fmt.Errorf("store: reference birth anchor mismatch")
	}
	rows, err := q.QueryContext(ctx, `SELECT a.id FROM experiment_actions a JOIN experiment_effects e ON a.effect=e.id WHERE a.effect=?`, birth)
	if err != nil {
		return err
	}
	defer func() { _ = rows.Close() }()
	for rows.Next() {
		var original string
		if err = rows.Scan(&original); err != nil {
			return err
		}
		if experimentOriginalID(original) == anchor {
			return nil
		}
	}
	if err = rows.Err(); err != nil {
		return err
	}
	return fmt.Errorf("store: reference birth anchor mismatch")
}

// validateWorkSnapshotReferences checks state equivalence, not portable birth
// authentication (the receiver owns that). It has no side effects; the public
// fold also invokes it, so savepoint admission and rebuild share the same guards.
func validateWorkSnapshotReferences(ctx context.Context, tx *sql.Tx, ev Event, p workSnapshot) error {
	check := func(id, query string, args ...any) error {
		var count int
		if err := tx.QueryRowContext(ctx, query, args...).Scan(&count); err != nil {
			return err
		}
		if count != 1 {
			return fmt.Errorf("store: snapshot reference %s has incompatible state", id)
		}
		return nil
	}
	var currentRows *transfer.Snapshot
	for _, n := range p.Snapshot.Nodes {
		if _, ok := p.References[n.ID]; !ok {
			continue
		}
		if err := check(n.ID, `SELECT COUNT(*) FROM nodes WHERE id=? AND repo=? AND kind=? AND COALESCE(parent,'')=? AND matter=? AND locator=? AND title=? AND lifecycle=? AND sort_key=? AND tombstone_event IS NULL`, n.ID, ev.Repo, n.Kind, n.Parent, n.Matter, n.Locator, n.Title, n.Lifecycle, n.SortKey); err != nil {
			return err
		}
		if n.Lifecycle == "canceled" {
			if currentRows == nil {
				var err error
				currentRows, err = transferRows(ctx, tx, ev.Repo, p.Snapshot.Lineage)
				if err != nil {
					return err
				}
			}
			for _, actual := range currentRows.Nodes {
				if actual.ID == n.ID && actual.Reason != n.Reason {
					return fmt.Errorf("store: snapshot reference cancellation reason mismatch")
				}
			}
		}
		if n.Kind == "matter" {
			if err := transferSupportedMatter(ctx, tx, ev.Repo, n.ID); err != nil {
				return err
			}
			var expected int
			for _, member := range p.Snapshot.Nodes {
				if member.Matter == n.ID {
					expected++
				}
			}
			if err := check(n.ID, `SELECT COUNT(*)=? FROM nodes WHERE matter=?`, expected, n.ID); err != nil {
				return err
			}
			expectedEdges := 0
			members := map[string]bool{}
			for _, member := range p.Snapshot.Nodes {
				if member.Matter == n.ID {
					members[member.ID] = true
				}
			}
			for _, edge := range p.Snapshot.Edges {
				if members[edge.Blocked] || members[edge.Blocker] {
					expectedEdges++
				}
			}
			if err := check(n.ID, `SELECT COUNT(*)=? FROM edges WHERE tombstone_event IS NULL AND (blocked IN (SELECT id FROM nodes WHERE matter=?) OR blocker IN (SELECT id FROM nodes WHERE matter=?))`, expectedEdges, n.ID, n.ID); err != nil {
				return err
			}
		}
	}
	for _, b := range p.Snapshot.Backlog {
		if _, ok := p.References[b.ID]; !ok {
			continue
		}
		if err := check(b.ID, `SELECT COUNT(*) FROM backlog_entries WHERE id=? AND repo=? AND provenance=? AND state=? AND title=? AND detail=? AND COALESCE(origin_node,'')=? AND COALESCE(matter,'')=? AND decline_reason=?`, b.ID, ev.Repo, b.Provenance, b.State, b.Title, b.Detail, b.OriginNode, b.Matter, b.Reason); err != nil {
			return err
		}
	}
	for _, e := range p.Snapshot.Edges {
		if _, ok := p.References[e.ID]; !ok {
			continue
		}
		if err := check(e.ID, `SELECT COUNT(*) FROM edges WHERE id=? AND blocked=? AND blocker=? AND tombstone_event IS NULL`, e.ID, e.Blocked, e.Blocker); err != nil {
			return err
		}
	}
	for _, c := range p.Snapshot.Contents {
		if _, ok := p.References[c.ID]; !ok {
			continue
		}
		if err := check(c.ID, `SELECT COUNT(*) FROM content WHERE id=? AND node=? AND kind=? AND byte_len=? AND sha256=? AND tombstone_event IS NULL`, c.ID, c.Owner, c.Kind, c.Length, c.SHA256); err != nil {
			return err
		}
	}
	for id := range p.References {
		for _, n := range p.Snapshot.Nodes {
			if n.ID != id {
				continue
			}
			expected := 0
			for _, c := range p.Snapshot.Contents {
				if c.Owner == id {
					expected++
				}
			}
			if err := check(id, `SELECT COUNT(*)=? FROM content WHERE node=? AND tombstone_event IS NULL`, expected, id); err != nil {
				return err
			}
		}
		for _, b := range p.Snapshot.Backlog {
			if b.ID != id {
				continue
			}
			expected := 0
			for _, c := range p.Snapshot.Contents {
				if c.Owner == id {
					expected++
				}
			}
			if err := check(id, `SELECT COUNT(*)=? FROM content WHERE node=? AND tombstone_event IS NULL`, expected, id); err != nil {
				return err
			}
		}
	}
	origins, err := transferOriginsSource(ctx, tx)
	if err != nil {
		return err
	}
	for _, o := range transferObjects(p.Snapshot) {
		if _, ok := p.References[o.id]; !ok {
			continue
		}
		var previous string
		err := tx.QueryRowContext(ctx, `SELECT state FROM `+origins+` WHERE repo=? AND target=?`, ev.Repo, o.id).Scan(&previous)
		if err == sql.ErrNoRows {
			continue
		}
		if err != nil {
			return err
		}
		current, err := transferState(ctx, tx, o.kind, o.id)
		if err != nil {
			return err
		}
		if previous != current {
			return fmt.Errorf("store: snapshot reference %s has independent edits", o.id)
		}
	}
	return nil
}

func projectWorkSnapshot(ctx context.Context, tx *sql.Tx, ev Event) error {
	p, err := decodeWorkSnapshot(ev)
	if err != nil {
		return err
	}
	if err = transferIDFree(ctx, tx, ev.Subject); err != nil {
		return fmt.Errorf("store: snapshot operation identity conflict: %w", err)
	}
	if err = transferNoGates(ctx, tx, ev.Repo); err != nil {
		return err
	}
	if err = validateWorkSnapshotReferences(ctx, tx, ev, p); err != nil {
		return err
	}
	plan := &TransferPlan{Mapping: map[string]string{}, Reused: map[string]bool{}, Locators: map[string]string{}}
	objects := transferObjects(p.Snapshot)
	for _, o := range objects {
		plan.Mapping[o.id] = o.id
		if _, ref := p.References[o.id]; ref {
			// Authentication of the portable anchor belongs to the receiver.
			// The fold still refuses absent/tombstoned/cross-Repo references.
			var repo string
			query := `SELECT repo FROM nodes WHERE id=? AND tombstone_event IS NULL`
			switch o.kind {
			case "backlog":
				query = `SELECT repo FROM backlog_entries WHERE id=?`
			case "content":
				query = `SELECT COALESCE(n.repo,b.repo) FROM content c LEFT JOIN nodes n ON n.id=c.node LEFT JOIN backlog_entries b ON b.id=c.node WHERE c.id=? AND c.tombstone_event IS NULL`
			case "edge":
				query = `SELECT n.repo FROM edges e JOIN nodes n ON n.id=e.blocked WHERE e.id=? AND e.tombstone_event IS NULL`
			}
			if err = tx.QueryRowContext(ctx, query, o.id).Scan(&repo); err != nil {
				return fmt.Errorf("store: snapshot reference %s unavailable in Repo: %w", o.id, err)
			}
			if repo != ev.Repo {
				return fmt.Errorf("store: snapshot reference %s belongs to a different Repo", o.id)
			}
			plan.Reused[o.id] = true
			continue
		}
		if err = transferIDFree(ctx, tx, o.id); err != nil {
			return err
		}
		if o.origin.ID != o.id {
			if err = transferIDFree(ctx, tx, o.origin.ID); err != nil {
				return fmt.Errorf("store: ambiguous snapshot original native identity: %w", err)
			}
		}
		origins, e := transferOriginsSource(ctx, tx)
		if e != nil {
			return e
		}
		var count int
		if err = tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM `+origins+` WHERE origin=? OR target=?`, o.key, o.id).Scan(&count); err != nil {
			return err
		}
		if count != 0 {
			return fmt.Errorf("store: snapshot origin/identity conflict for %s", o.id)
		}
	}
	for _, n := range p.Snapshot.Nodes {
		if n.Kind == "matter" {
			plan.Locators[n.ID] = n.Locator
			var count int
			if err = tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM nodes WHERE repo=? AND kind='matter' AND locator=? AND id<>? AND tombstone_event IS NULL`, ev.Repo, n.Locator, n.ID).Scan(&count); err != nil {
				return err
			}
			if count != 0 {
				return fmt.Errorf("store: snapshot Matter locator collision")
			}
		}
	}
	if err = projectTransferState(ctx, tx, ev, transferImported{Snapshot: p.Snapshot, Plan: plan, Contents: p.Contents}); err != nil {
		return err
	}
	for _, o := range objects {
		if plan.Reused[o.id] {
			continue
		}
		state, e := transferState(ctx, tx, o.kind, o.id)
		if e != nil {
			return e
		}
		if _, err = tx.ExecContext(ctx, `INSERT INTO transfer_origins(repo,origin,kind,target,fingerprint,state,birth_event) VALUES(?,?,?,?,?,?,?)`, ev.Repo, o.key, o.kind, o.id, o.fingerprint, state, ev.ID); err != nil {
			return err
		}
	}
	return nil
}

func projectTransferCompanion(ctx context.Context, tx *sql.Tx, ev Event, p transferImported) error {
	var root Event
	root.Type = TypeWorkSnapshot
	var raw []byte
	if ev.Causation == ev.ID || ev.Correlation != ev.Causation {
		return fmt.Errorf("store: import companion requires root causation/correlation")
	}
	if err := tx.QueryRowContext(ctx, `SELECT id,repo,subject,payload FROM events WHERE id=? AND type=?`, ev.Causation, TypeWorkSnapshot).Scan(&root.ID, &root.Repo, &root.Subject, &raw); err != nil {
		return err
	}
	root.Payload = raw
	if root.Repo != ev.Repo || root.Subject != ev.Subject {
		return fmt.Errorf("store: import companion root mismatch")
	}
	actual, err := decodeWorkSnapshot(root)
	if err != nil {
		return err
	}
	want, err := canonicalWorkSnapshot(ctx, tx, p, actual.References)
	if err != nil {
		return err
	}
	a, _ := json.Marshal(actual)
	b, _ := json.Marshal(want)
	if !reflect.DeepEqual(a, b) {
		return fmt.Errorf("store: import companion differs from canonical root")
	}
	// No second planTransfer against state already inserted by the root.
	return projectTransferReceipt(ctx, tx, ev, p)
}

// Private archive provenance takes precedence over a public root's effective
// alias fingerprint. The latter remains immutable and sufficient on receivers.
func transferOriginsSource(ctx context.Context, q querier) (string, error) {
	exists, err := exchangeTableExists(ctx, q, "transfer_snapshot_imports")
	if err != nil || !exists {
		return "transfer_origins", err
	}
	return `(SELECT o.repo,o.origin,o.kind,o.target,COALESCE(p.fingerprint,o.fingerprint) AS fingerprint,o.state,COALESCE(p.birth_event,o.birth_event) AS birth_event FROM transfer_origins o LEFT JOIN transfer_snapshot_imports p ON p.target=o.target)`, nil
}
