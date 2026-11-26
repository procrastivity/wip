package store

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"

	"github.com/procrastivity/wip/internal/transfer"
)

func transferHash(v any) string {
	b, _ := json.Marshal(v)
	h := sha256.Sum256(b)
	return hex.EncodeToString(h[:])
}

func originKey(kind string, o transfer.Origin) string {
	b, _ := json.Marshal([]string{o.Lineage, o.Repo, kind, o.ID})
	return string(b)
}

func transferOrigin(ctx context.Context, q querier, repo, lineage, kind, id string) (transfer.Origin, error) {
	o := transfer.Origin{Lineage: lineage, Repo: repo, ID: id}
	var key string
	err := q.QueryRowContext(ctx, `SELECT origin FROM transfer_origins WHERE repo=? AND target=?`, repo, id).Scan(&key)
	if err == sql.ErrNoRows {
		return o, nil
	}
	if err != nil {
		return o, err
	}
	var parts []string
	if err := json.Unmarshal([]byte(key), &parts); err != nil {
		return o, err
	}
	if len(parts) != 4 || parts[2] != kind {
		return o, fmt.Errorf("store: corrupt transfer origin for %s", id)
	}
	return transfer.Origin{Lineage: parts[0], Repo: parts[1], ID: parts[3]}, nil
}

// transferRows reads all work, including terminal backlog and tombstones. The
// export closure below decides what may cross the snapshot-v1 boundary.
func transferRows(ctx context.Context, q querier, repo, lineage string) (*transfer.Snapshot, error) {
	var one int
	if err := q.QueryRowContext(ctx, `SELECT 1 FROM repos WHERE id=?`, repo).Scan(&one); err != nil {
		return nil, fmt.Errorf("store: source Repo %s does not exist: %w", repo, err)
	}
	x := &transfer.Snapshot{
		Version: transfer.Version, Profile: transfer.Profile, Repo: repo, Lineage: lineage,
		Exclusions: []string{"configuration", "filesystem paths and checkout bindings", "sync bindings and pairing", "credentials", "execution ownership (batches, runs, dispatches, roles)", "provider delivery state and queues", "historical native events"},
	}
	if err := q.QueryRowContext(ctx, `SELECT COALESCE(MAX(id),'') FROM events WHERE repo=?`, repo).Scan(&x.Revision); err != nil {
		return nil, err
	}
	rows, err := q.QueryContext(ctx, `SELECT id,kind,COALESCE(parent,''),matter,locator,title,lifecycle,sort_key FROM nodes WHERE repo=? ORDER BY matter,sort_key,birth_event`, repo)
	if err != nil {
		return nil, err
	}
	for rows.Next() {
		var n transfer.Node
		if err = rows.Scan(&n.ID, &n.Kind, &n.Parent, &n.Matter, &n.Locator, &n.Title, &n.Lifecycle, &n.SortKey); err != nil {
			break
		}
		x.Nodes = append(x.Nodes, n)
	}
	if err == nil {
		err = rows.Err()
	}
	_ = rows.Close()
	if err != nil {
		return nil, err
	}
	// Imports share one birth event, so tied sort keys cannot encode native
	// (sort_key, birth_event) order. Only Matters with ties need normalized
	// keys; rank the whole Matter so mixed parents and distinct keys retain
	// the same MatterNodes order as well as each parent's Children order.
	rankTiedTransferNodes(x.Nodes)
	for i := range x.Nodes {
		n := &x.Nodes[i]
		n.Origin, err = transferOrigin(ctx, q, repo, lineage, n.Kind, n.ID)
		if err != nil {
			return nil, err
		}
		if n.Lifecycle == "canceled" {
			var raw string
			err = q.QueryRowContext(ctx, `SELECT payload FROM events WHERE subject=? AND type IN ('matter.canceled','stage.canceled','step.canceled') ORDER BY id DESC LIMIT 1`, n.ID).Scan(&raw)
			switch err {
			case nil:
				var p Transition
				if err = json.Unmarshal([]byte(raw), &p); err != nil {
					return nil, err
				}
				n.Reason = p.Reason
			case sql.ErrNoRows:
				// An imported cancellation is a state fact, not a fabricated
				// historical transition. Its original metadata lives in the log.
				var payload string
				err = q.QueryRowContext(ctx, `SELECT e.payload FROM transfer_origins o JOIN events e ON e.id=o.birth_event WHERE o.repo=? AND o.target=?`, repo, n.ID).Scan(&payload)
				if err == nil {
					var p transferImported
					if err = json.Unmarshal([]byte(payload), &p); err != nil {
						return nil, err
					}
					for _, old := range p.Snapshot.Nodes {
						if p.Plan.Mapping[old.ID] == n.ID {
							n.Reason = old.Reason
						}
					}
				} else if err != sql.ErrNoRows {
					return nil, err
				}
			default:
				return nil, err
			}
		}
	}
	rows, err = q.QueryContext(ctx, `SELECT id,provenance,state,title,detail,COALESCE(origin_node,''),COALESCE(matter,''),decline_reason FROM backlog_entries WHERE repo=? ORDER BY id`, repo)
	if err != nil {
		return nil, err
	}
	for rows.Next() {
		var e transfer.Entry
		if err = rows.Scan(&e.ID, &e.Provenance, &e.State, &e.Title, &e.Detail, &e.OriginNode, &e.Matter, &e.Reason); err != nil {
			break
		}
		x.Backlog = append(x.Backlog, e)
	}
	if err == nil {
		err = rows.Err()
	}
	_ = rows.Close()
	if err != nil {
		return nil, err
	}
	for i := range x.Backlog {
		e := &x.Backlog[i]
		e.Origin, err = transferOrigin(ctx, q, repo, lineage, "backlog", e.ID)
		if err != nil {
			return nil, err
		}
	}
	return x, nil
}

// TransferInventory needs no checkout and deliberately includes declined and
// planned entries. Unsupported work remains visible with an actionable note.
func (s *Store) TransferInventory(ctx context.Context, repo string) (transfer.Inventory, error) {
	tx, err := s.db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return transfer.Inventory{}, err
	}
	defer func() { _ = tx.Rollback() }()
	x, err := transferRows(ctx, tx, repo, "")
	if err != nil {
		return transfer.Inventory{}, err
	}
	out := transfer.Inventory{Repo: repo, Backlog: x.Backlog}
	for _, n := range x.Nodes {
		if n.Kind == "matter" {
			out.Matters = append(out.Matters, n)
			if err := transferSupportedMatter(ctx, tx, repo, n.ID); err != nil {
				out.Notes = append(out.Notes, err.Error())
			}
		}
	}
	for _, e := range x.Backlog {
		if e.State == "delegated" {
			out.Notes = append(out.Notes, fmt.Sprintf("backlog %s: delegated backlog is unsupported in snapshot-v1", e.ID))
		}
	}
	return out, nil
}

func transferNoGates(ctx context.Context, q querier, repo string) error {
	var count int
	if err := q.QueryRowContext(ctx, `SELECT COUNT(*) FROM gate_declarations WHERE repo=?`, repo).Scan(&count); err != nil {
		return err
	}
	if count != 0 {
		return fmt.Errorf("store: Repo %s has gate declarations; snapshot-v1 cannot transfer gated work; use a Repo without gates", repo)
	}
	return nil
}

func transferSupportedMatter(ctx context.Context, q querier, repo, id string) error {
	if err := transferNoGates(ctx, q, repo); err != nil {
		return err
	}
	checks := []struct{ sql, why string }{
		{`SELECT COUNT(*) FROM nodes WHERE matter=? AND (tombstone_event IS NOT NULL OR external_ref IS NOT NULL)`, "tombstones/replacements/removals or external references"},
		{`SELECT COUNT(*) FROM tracker_references WHERE matter=?`, "tracker references (including removed references)"},
		{`SELECT COUNT(*) FROM batch_members WHERE matter=?`, "batch membership"},
		{`SELECT COUNT(*) FROM gate_state WHERE node IN (SELECT id FROM nodes WHERE matter=?)`, "gate state"},
		{`SELECT COUNT(*) FROM events WHERE subject IN (SELECT id FROM nodes WHERE matter=?) AND type IN ('step.replaced','step.removed')`, "replacement/removal history"},
	}
	for _, c := range checks {
		var count int
		if err := q.QueryRowContext(ctx, c.sql, id).Scan(&count); err != nil {
			return err
		}
		if count != 0 {
			return fmt.Errorf("store: Matter %s has %s; unsupported by snapshot-v1; exclude this Matter", id, c.why)
		}
	}
	return nil
}

// ExportTransfer closes selected roots over descendants, planned results and
// origin-node owners, but never silently includes a dependency's other endpoint.
func (s *Store) ExportTransfer(ctx context.Context, repo, lineage string, selection transfer.Selection) (*transfer.Snapshot, error) {
	tx, err := s.db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return nil, err
	}
	defer func() { _ = tx.Rollback() }()
	all, err := transferRows(ctx, tx, repo, lineage)
	if err != nil {
		return nil, err
	}
	if len(selection.Matters)+len(selection.Backlog) == 0 {
		return nil, fmt.Errorf("store: select at least one Matter or backlog entry")
	}
	if err = transferNoGates(ctx, tx, repo); err != nil {
		return nil, err
	}
	nodes := map[string]transfer.Node{}
	entries := map[string]transfer.Entry{}
	roots := map[string]bool{}
	chosen := map[string]bool{}
	for _, n := range all.Nodes {
		nodes[n.ID] = n
	}
	for _, e := range all.Backlog {
		entries[e.ID] = e
	}
	for _, id := range selection.Matters {
		n, ok := nodes[id]
		if !ok || n.Kind != "matter" {
			return nil, fmt.Errorf("store: selection %s is not a Matter root in Repo %s", id, repo)
		}
		roots[id] = true
	}
	for _, id := range selection.Backlog {
		e, ok := entries[id]
		if !ok {
			return nil, fmt.Errorf("store: no backlog %s in Repo %s", id, repo)
		}
		if e.State == "delegated" {
			return nil, fmt.Errorf("store: backlog %s is delegated; unsupported by snapshot-v1", id)
		}
		chosen[id] = true
		if e.Matter != "" {
			roots[e.Matter] = true
		}
		if e.OriginNode != "" {
			n, ok := nodes[e.OriginNode]
			if !ok {
				return nil, fmt.Errorf("store: backlog %s origin node is unavailable", id)
			}
			roots[n.Matter] = true
		}
	}
	x := *all
	x.Nodes = nil
	x.Backlog = nil
	for id := range roots {
		if err := transferSupportedMatter(ctx, tx, repo, id); err != nil {
			return nil, err
		}
		x.Selection.Matters = append(x.Selection.Matters, id)
	}
	for id := range chosen {
		x.Selection.Backlog = append(x.Selection.Backlog, id)
	}
	sort.Strings(x.Selection.Matters)
	sort.Strings(x.Selection.Backlog)
	owners := map[string]bool{}
	for _, n := range all.Nodes {
		if roots[n.Matter] {
			if n.Kind == "matter" {
				var payload string
				err = tx.QueryRowContext(ctx, `SELECT e.payload FROM transfer_origins o JOIN events e ON e.id=o.birth_event WHERE o.repo=? AND o.target=?`, repo, n.ID).Scan(&payload)
				if err == nil {
					var imported transferImported
					if err = json.Unmarshal([]byte(payload), &imported); err != nil {
						return nil, err
					}
					for _, old := range imported.Snapshot.Nodes {
						// An unchanged import-time alias is destination policy,
						// not a change to the upstream locator. Later native
						// renames differ from that alias and remain visible.
						if imported.Plan.Mapping[old.ID] == n.ID && imported.Plan.Locators[old.ID] == n.Locator {
							// A later descendant may now use the upstream name.
							// Keep the local alias for that changed closure.
							collision := false
							for _, current := range all.Nodes {
								if current.Matter == n.Matter && current.ID != n.ID && current.Locator == old.Locator {
									collision = true
									break
								}
							}
							if !collision {
								n.Locator = old.Locator
							}
							break
						}
					}
				} else if err != sql.ErrNoRows {
					return nil, err
				}
			}
			x.Nodes = append(x.Nodes, n)
			owners[n.ID] = true
		}
	}
	for _, e := range all.Backlog {
		if chosen[e.ID] {
			x.Backlog = append(x.Backlog, e)
			owners[e.ID] = true
		}
	}
	rows, err := tx.QueryContext(ctx, `SELECT e.id,e.blocked,e.blocker FROM edges e JOIN nodes n ON n.id=e.blocked WHERE n.repo=? AND e.tombstone_event IS NULL ORDER BY e.id`, repo)
	if err != nil {
		return nil, err
	}
	for rows.Next() {
		var e transfer.Edge
		if err = rows.Scan(&e.ID, &e.Blocked, &e.Blocker); err != nil {
			break
		}
		if owners[e.Blocked] || owners[e.Blocker] {
			if !owners[e.Blocked] || !owners[e.Blocker] {
				err = fmt.Errorf("store: dependency %s crosses the selection; select both endpoint Matters", e.ID)
				break
			}
			x.Edges = append(x.Edges, e)
		}
	}
	if err == nil {
		err = rows.Err()
	}
	_ = rows.Close()
	if err != nil {
		return nil, err
	}
	for i := range x.Edges {
		e := &x.Edges[i]
		e.Origin, err = transferOrigin(ctx, tx, repo, lineage, "edge", e.ID)
		if err != nil {
			return nil, err
		}
	}
	rows, err = tx.QueryContext(ctx, `SELECT id,node,kind,byte_len,sha256,bytes,COALESCE(blob_ref,'') FROM content WHERE tombstone_event IS NULL ORDER BY node,kind,id`)
	if err != nil {
		return nil, err
	}
	orders := map[string]int{}
	for rows.Next() {
		var c transfer.Content
		var ref string
		if err = rows.Scan(&c.ID, &c.Owner, &c.Kind, &c.Length, &c.SHA256, &c.Data, &ref); err != nil {
			break
		}
		if !owners[c.Owner] {
			continue
		}
		key := c.Owner + "/" + c.Kind
		c.Order = orders[key]
		orders[key]++
		if ref != "" {
			if filepath.Base(ref) != ref {
				err = fmt.Errorf("store: invalid blob reference")
				break
			}
			c.Data, err = os.ReadFile(filepath.Join(s.blobDir, ref))
			if err != nil {
				break
			}
		}
		x.Contents = append(x.Contents, c)
	}
	if err == nil {
		err = rows.Err()
	}
	_ = rows.Close()
	if err != nil {
		return nil, err
	}
	for i := range x.Contents {
		c := &x.Contents[i]
		c.Origin, err = transferOrigin(ctx, tx, repo, lineage, "content", c.ID)
		if err != nil {
			return nil, err
		}
	}
	if err := transfer.Validate(&x); err != nil {
		return nil, err
	}
	return &x, nil
}
