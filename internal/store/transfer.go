package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strings"

	"github.com/procrastivity/wip/internal/transfer"
)

// TransferPlan is a reviewable, read-only decision. IDs minted during preview
// are reserved only by the eventual transaction; apply checks every one again.
type TransferPlan struct {
	Repo            string            `json:"repo"`
	Digest          string            `json:"digest"`
	Operation       string            `json:"operation"`
	Mapping         map[string]string `json:"mapping"`
	Renames         map[string]string `json:"renames"`
	Locators        map[string]string `json:"locators"`
	Preconditions   map[string]string `json:"preconditions"`
	Reused          map[string]bool   `json:"reused"`
	Pairing         TransferPairing   `json:"pairing"`
	AlreadyImported bool              `json:"already_imported"`
}

// TransferPairing is retained metadata only: importing does not pair, resume or retarget sync.
type TransferPairing struct {
	Generation           string `json:"generation"`
	Endpoint             string `json:"endpoint,omitempty"`
	Connection           string `json:"connection,omitempty"`
	Account              string `json:"account,omitempty"`
	Active               bool   `json:"active"`
	Boundary             string `json:"boundary,omitempty"`
	ConnectionGeneration string `json:"connection_generation,omitempty"`
}

func transferPairing(ctx context.Context, q querier, repo string) (TransferPairing, error) {
	var p TransferPairing
	var err error
	p.Generation, err = exchangeGeneration(ctx, q, repo)
	if err != nil {
		return p, err
	}
	for _, item := range []struct {
		table, query string
		dest         []any
	}{
		{"exchange_bindings", `SELECT endpoint FROM exchange_bindings WHERE repo=?`, []any{&p.Endpoint}},
		{"exchange_cli", `SELECT active,boundary FROM exchange_cli WHERE repo=?`, []any{&p.Active, &p.Boundary}},
		{"exchange_accounts", `SELECT connection,account FROM exchange_accounts WHERE repo=?`, []any{&p.Connection, &p.Account}},
	} {
		exists, e := exchangeTableExists(ctx, q, item.table)
		if e != nil {
			return p, e
		}
		if exists {
			if e = q.QueryRowContext(ctx, item.query, repo).Scan(item.dest...); e != nil && e != sql.ErrNoRows {
				return p, e
			}
		}
	}
	if p.Connection != "" {
		p.ConnectionGeneration, err = exchangeConnectionGeneration(ctx, q, p.Connection)
		if err != nil {
			return p, err
		}
	}
	return p, nil
}

// TransferReceipt identifies one atomic import and its durable native aliases.
type TransferReceipt struct {
	Operation string            `json:"operation"`
	Digest    string            `json:"digest"`
	Repo      string            `json:"repo"`
	Mapping   map[string]string `json:"mapping"`
}

type transferObject struct {
	id, kind, key, fingerprint string
	origin                     transfer.Origin
}

// rankTiedTransferNodes preserves the supplied effective order. Nodes must be
// grouped by Matter and sorted by key; only Matters with ties are re-ranked.
func rankTiedTransferNodes(nodes []transfer.Node) {
	ties := map[string]bool{}
	for i := 1; i < len(nodes); i++ {
		a, b := nodes[i-1], nodes[i]
		if a.Matter == b.Matter && a.SortKey == b.SortKey {
			ties[b.Matter] = true
		}
	}
	ranks := map[string]int64{}
	for i := range nodes {
		n := &nodes[i]
		if ties[n.Matter] {
			n.SortKey = ranks[n.Matter]
			ranks[n.Matter]++
		}
	}
}

// Incoming ties carry no native birth order. Resolve them by durable origin,
// not slice order or archive aliases, identically for fingerprints and storage.
func canonicalTransferNodes(nodes []transfer.Node) []transfer.Node {
	nodes = append([]transfer.Node(nil), nodes...)
	sort.Slice(nodes, func(i, j int) bool {
		a, b := nodes[i], nodes[j]
		if a.Matter != b.Matter {
			return a.Matter < b.Matter
		}
		if a.SortKey != b.SortKey {
			return a.SortKey < b.SortKey
		}
		return originKey(a.Kind, a.Origin) < originKey(b.Kind, b.Origin)
	})
	rankTiedTransferNodes(nodes)
	return nodes
}

// Fingerprints use durable origin references, not archive aliases. A Matter's
// fingerprint includes its complete closure: additions cannot masquerade as an
// unchanged root during a later overlapping import.
func transferObjects(x *transfer.Snapshot) []transferObject {
	keys := map[string]string{}
	origins := map[string]transfer.Origin{}
	for _, n := range x.Nodes {
		keys[n.ID] = originKey(n.Kind, n.Origin)
		origins[n.ID] = n.Origin
	}
	for _, e := range x.Backlog {
		keys[e.ID] = originKey("backlog", e.Origin)
		origins[e.ID] = e.Origin
	}
	for _, e := range x.Edges {
		keys[e.ID] = originKey("edge", e.Origin)
		origins[e.ID] = e.Origin
	}
	for _, c := range x.Contents {
		keys[c.ID] = originKey("content", c.Origin)
		origins[c.ID] = c.Origin
	}
	meta := map[string]any{}
	for _, n := range canonicalTransferNodes(x.Nodes) {
		id := n.ID
		n.ID = keys[id]
		n.Parent = keys[n.Parent]
		n.Matter = keys[n.Matter]
		meta[id] = n
	}
	for _, e := range x.Backlog {
		id := e.ID
		e.ID = keys[id]
		e.OriginNode = keys[e.OriginNode]
		e.Matter = keys[e.Matter]
		meta[id] = e
	}
	for _, e := range x.Edges {
		id := e.ID
		e.ID = keys[id]
		e.Blocked = keys[e.Blocked]
		e.Blocker = keys[e.Blocker]
		meta[id] = e
	}
	for _, c := range transfer.CanonicalContents(x.Contents) {
		id := c.ID
		c.ID = keys[id]
		c.Owner = keys[c.Owner]
		c.Data = nil
		meta[id] = c
	}
	var out []transferObject
	add := func(id, kind string, parts []string) {
		sort.Strings(parts)
		out = append(out, transferObject{id, kind, keys[id], transferHash(parts), origins[id]})
	}
	encoded := func(id string) string { b, _ := json.Marshal(meta[id]); return string(b) }
	for _, n := range x.Nodes {
		parts := []string{encoded(n.ID)}
		if n.Kind == "matter" {
			for _, v := range x.Nodes {
				if v.Matter == n.ID && v.ID != n.ID {
					parts = append(parts, encoded(v.ID))
				}
			}
			for _, c := range x.Contents {
				for _, v := range x.Nodes {
					if v.ID == c.Owner && v.Matter == n.ID {
						parts = append(parts, encoded(c.ID))
					}
				}
			}
			for _, e := range x.Edges {
				for _, v := range x.Nodes {
					if (v.ID == e.Blocked || v.ID == e.Blocker) && v.Matter == n.ID {
						parts = append(parts, encoded(e.ID))
						break
					}
				}
			}
		}
		add(n.ID, n.Kind, parts)
	}
	for _, e := range x.Backlog {
		parts := []string{encoded(e.ID)}
		for _, c := range x.Contents {
			if c.Owner == e.ID {
				parts = append(parts, encoded(c.ID))
			}
		}
		add(e.ID, "backlog", parts)
	}
	for _, e := range x.Edges {
		add(e.ID, "edge", []string{encoded(e.ID)})
	}
	contents := append([]transfer.Content(nil), x.Contents...)
	sort.Slice(contents, func(i, j int) bool {
		a, b := contents[i], contents[j]
		if a.Owner != b.Owner {
			return a.Owner < b.Owner
		}
		if a.Kind != b.Kind {
			return a.Kind < b.Kind
		}
		return a.Order < b.Order
	})
	for _, c := range contents {
		add(c.ID, "content", []string{encoded(c.ID)})
	}
	return out
}

func transferReceipt(ctx context.Context, q querier, repo, digest string) (*TransferReceipt, error) {
	var raw string
	err := q.QueryRowContext(ctx, `SELECT receipt FROM transfer_receipts WHERE repo=? AND digest=?`, repo, digest).Scan(&raw)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var r TransferReceipt
	if err = json.Unmarshal([]byte(raw), &r); err != nil {
		return nil, err
	}
	return &r, nil
}

// transferState detects local changes without interpreting them as permission
// to overwrite. Matter state includes new descendants, content, dependencies,
// references, gates and batch membership, including tombstones.
func transferState(ctx context.Context, q querier, kind, id string) (string, error) {
	var queries []string
	switch kind {
	case "matter", "stage", "step":
		queries = []string{`SELECT id,last_event,COALESCE(tombstone_event,'') FROM nodes WHERE matter=(SELECT matter FROM nodes WHERE id=?) ORDER BY id`, `SELECT id,last_event,COALESCE(tombstone_event,'') FROM content WHERE node IN (SELECT id FROM nodes WHERE matter=(SELECT matter FROM nodes WHERE id=?)) ORDER BY id`, `SELECT id,last_event,COALESCE(tombstone_event,'') FROM edges WHERE blocked IN (SELECT id FROM nodes WHERE matter=(SELECT matter FROM nodes WHERE id=?)) OR blocker IN (SELECT id FROM nodes WHERE matter=(SELECT matter FROM nodes WHERE id=?)) ORDER BY id`}
		queries = append(queries,
			`SELECT matter,ref,last_event FROM tracker_references WHERE matter=(SELECT matter FROM nodes WHERE id=?) ORDER BY ref`,
			`SELECT batch,matter,last_event FROM batch_members WHERE matter=(SELECT matter FROM nodes WHERE id=?) ORDER BY batch`,
			`SELECT node,gate,last_event FROM gate_state WHERE node IN (SELECT id FROM nodes WHERE matter=(SELECT matter FROM nodes WHERE id=?)) ORDER BY node,gate`)
	case "backlog":
		queries = []string{`SELECT id,last_event FROM backlog_entries WHERE id=?`, `SELECT id,last_event,COALESCE(tombstone_event,'') FROM content WHERE node=? ORDER BY id`}
	case "content":
		queries = []string{`SELECT id,last_event,COALESCE(tombstone_event,'') FROM content WHERE id=?`}
	case "edge":
		queries = []string{`SELECT id,last_event,COALESCE(tombstone_event,'') FROM edges WHERE id=?`}
	default:
		return "", fmt.Errorf("store: unknown transfer kind %q", kind)
	}
	var state []any
	for _, query := range queries {
		args := []any{id}
		if kind == "matter" || kind == "stage" || kind == "step" {
			if query == queries[2] {
				args = append(args, id)
			}
		}
		rows, err := q.QueryContext(ctx, query, args...)
		if err != nil {
			return "", err
		}
		cols, err := rows.Columns()
		if err != nil {
			_ = rows.Close()
			return "", err
		}
		var group [][]string
		for rows.Next() {
			values := make([]string, len(cols))
			ptrs := make([]any, len(cols))
			for i := range values {
				ptrs[i] = &values[i]
			}
			if err = rows.Scan(ptrs...); err != nil {
				break
			}
			group = append(group, values)
		}
		if err == nil {
			err = rows.Err()
		}
		_ = rows.Close()
		if err != nil {
			return "", err
		}
		if len(state) == 0 && len(group) == 0 {
			return "", fmt.Errorf("store: mapped %s %s no longer exists", kind, id)
		}
		state = append(state, group)
	}
	return transferHash(state), nil
}

func transferIDFree(ctx context.Context, q querier, id string) error {
	if _, err := timeOfID(id); err != nil {
		return err
	}
	for _, table := range []string{"events", "repos", "clones", "worktrees", "nodes", "content", "edges", "backlog_entries", "outbox_entries", "batches", "runs", "dispatches", "roles"} {
		var count int
		if err := q.QueryRowContext(ctx, `SELECT COUNT(*) FROM `+table+` WHERE id=?`, id).Scan(&count); err != nil {
			return err
		}
		if count != 0 {
			return fmt.Errorf("store: transfer ID %s already exists in %s (including tombstones); preview again", id, table)
		}
	}
	var count int
	if err := q.QueryRowContext(ctx, `SELECT COUNT(*) FROM transfer_receipts WHERE operation=?`, id).Scan(&count); err != nil {
		return err
	}
	if count != 0 {
		return fmt.Errorf("store: transfer operation ID already exists")
	}
	return nil
}

// PlanTransfer previews an import without writing any database or blob state.
func (s *Store) PlanTransfer(ctx context.Context, x *transfer.Snapshot, repo string, renames map[string]string) (*TransferPlan, error) {
	if err := transfer.Validate(x); err != nil {
		return nil, err
	}
	tx, err := s.db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return nil, err
	}
	defer func() { _ = tx.Rollback() }()
	return s.planTransfer(ctx, tx, x, repo, renames, nil, false)
}

func (s *Store) planTransfer(ctx context.Context, q querier, x *transfer.Snapshot, repo string, renames map[string]string, reviewed *TransferPlan, replay bool) (*TransferPlan, error) {
	digest, err := transfer.Digest(x)
	if err != nil {
		return nil, err
	}
	r, err := transferReceipt(ctx, q, repo, digest)
	if err != nil {
		return nil, err
	}
	if r != nil {
		return &TransferPlan{Repo: repo, Digest: digest, Operation: r.Operation, Mapping: r.Mapping, AlreadyImported: true}, nil
	}
	var one int
	if err = q.QueryRowContext(ctx, `SELECT 1 FROM repos WHERE id=?`, repo).Scan(&one); err != nil {
		return nil, fmt.Errorf("store: target Repo %s: %w", repo, err)
	}
	if err = transferNoGates(ctx, q, repo); err != nil {
		return nil, err
	}
	p := &TransferPlan{Repo: repo, Digest: digest, Mapping: map[string]string{}, Renames: map[string]string{}, Locators: map[string]string{}, Preconditions: map[string]string{}, Reused: map[string]bool{}}
	if replay {
		// Pairing is retained opt-in metadata, not a native projection. It was
		// checked under the writer lock before the event was appended. Replay
		// must not consult the user's *current* sync destination or grant one.
		p.Pairing = reviewed.Pairing
	} else {
		p.Pairing, err = transferPairing(ctx, q, repo)
		if err != nil {
			return nil, err
		}
	}
	if reviewed != nil {
		p.Operation = reviewed.Operation
	} else {
		p.Operation = s.NewID()
	}
	if err = transferIDFree(ctx, q, p.Operation); err != nil {
		return nil, err
	}
	for id, name := range renames {
		found := false
		for _, n := range x.Nodes {
			if n.ID == id && n.Kind == "matter" {
				found = true
			}
		}
		if !found || strings.TrimSpace(name) == "" {
			return nil, fmt.Errorf("store: rename %s must name a selected Matter and a nonempty locator", id)
		}
		p.Renames[id] = name
	}
	used := map[string]bool{p.Operation: true}
	objects := transferObjects(x)
	origins, err := transferOriginsSource(ctx, q)
	if err != nil {
		return nil, err
	}
	archiveIDs := map[string]bool{x.Repo: true}
	for _, o := range objects {
		archiveIDs[o.id], archiveIDs[o.origin.ID] = true, true
	}
	for _, o := range objects {
		var existingRepo string
		err = q.QueryRowContext(ctx, `SELECT repo FROM `+origins+` WHERE origin=?`, o.key).Scan(&existingRepo)
		if err == nil && existingRepo != repo {
			return nil, fmt.Errorf("store: source origin %s already maps to Repo %s; cannot remap it to Repo %s", o.key, existingRepo, repo)
		}
		if err != nil && err != sql.ErrNoRows {
			return nil, err
		}
		var target, fp, state string
		err = q.QueryRowContext(ctx, `SELECT target,fingerprint,state FROM `+origins+` WHERE repo=? AND origin=?`, repo, o.key).Scan(&target, &fp, &state)
		switch err {
		case nil:
			if fp != o.fingerprint {
				return nil, fmt.Errorf("store: changed source origin %s; snapshot-v1 does not overwrite prior imports", o.key)
			}
			now, e := transferState(ctx, q, o.kind, target)
			if e != nil {
				return nil, e
			}
			if now != state {
				return nil, fmt.Errorf("store: imported %s %s has local edits/removal; refusing overwrite", o.kind, target)
			}
			p.Reused[o.id] = true
			p.Preconditions[o.id] = state
		case sql.ErrNoRows:
			// A native alias already here is evidence of a copied/self store,
			// not permission to duplicate it under a fresh identity.
			if err = transferIDFree(ctx, q, o.id); err != nil {
				return nil, fmt.Errorf("store: likely self-import / ambiguous native identity %s: %w", o.id, err)
			}
			if err = transferIDFree(ctx, q, o.origin.ID); err != nil {
				return nil, fmt.Errorf("store: likely self-import / ambiguous original native identity %s: %w", o.origin.ID, err)
			}
			var nativeRepo int
			if err = q.QueryRowContext(ctx, `SELECT COUNT(*) FROM repos WHERE id IN (?,?)`, x.Repo, o.origin.Repo).Scan(&nativeRepo); err != nil {
				return nil, err
			}
			if nativeRepo != 0 {
				return nil, fmt.Errorf("store: likely self-import: source Repo identity already belongs to this store without a transfer mapping for %s", o.id)
			}
			if reviewed != nil {
				target = reviewed.Mapping[o.id]
			} else {
				target = s.NewID()
				for archiveIDs[target] {
					target = s.NewID()
				}
			}
			if archiveIDs[target] {
				return nil, fmt.Errorf("store: fresh target ID %s must not reuse an archive/native source identity", target)
			}
			if err = transferIDFree(ctx, q, target); err != nil {
				return nil, err
			}
		default:
			return nil, err
		}
		if used[target] {
			return nil, fmt.Errorf("store: transfer mapping has duplicate target %s", target)
		}
		used[target] = true
		p.Mapping[o.id] = target
	}
	if err = transferContentOrder(x, p.Mapping); err != nil {
		return nil, err
	}
	locators := map[string]bool{}
	for _, n := range x.Nodes {
		if n.Kind != "matter" {
			continue
		}
		name := n.Locator
		if rename, ok := p.Renames[n.ID]; ok {
			name = rename
		}
		for _, child := range x.Nodes {
			if child.Matter == n.ID && child.ID != n.ID && child.Locator == name {
				return nil, fmt.Errorf("store: Matter locator %q conflicts with descendant %s", name, child.ID)
			}
		}
		if p.Reused[n.ID] {
			var actual string
			if err = q.QueryRowContext(ctx, `SELECT locator FROM nodes WHERE id=? AND tombstone_event IS NULL`, p.Mapping[n.ID]).Scan(&actual); err != nil {
				return nil, err
			}
			if _, ok := p.Renames[n.ID]; ok && name != actual {
				return nil, fmt.Errorf("store: cannot rename reused Matter %s", n.ID)
			}
			name = actual
		}
		if locators[name] {
			return nil, fmt.Errorf("store: duplicate target Matter locator %q", name)
		}
		locators[name] = true
		p.Locators[n.ID] = name
		var count int
		if err = q.QueryRowContext(ctx, `SELECT COUNT(*) FROM nodes WHERE repo=? AND kind='matter' AND locator=? AND tombstone_event IS NULL AND id<>?`, repo, name, p.Mapping[n.ID]).Scan(&count); err != nil {
			return nil, err
		}
		if count != 0 {
			return nil, fmt.Errorf("store: Matter locator %q exists; provide a rename keyed by source Matter %s", name, n.ID)
		}
	}
	if reviewed != nil && !reflect.DeepEqual(p, reviewed) {
		return nil, fmt.Errorf("store: reviewed transfer plan is stale or altered; preview again")
	}
	return p, nil
}

func transferContentOrder(x *transfer.Snapshot, mapping map[string]string) error {
	groups := map[string][]transfer.Content{}
	for _, c := range x.Contents {
		key := c.Owner + "/" + c.Kind
		groups[key] = append(groups[key], c)
	}
	for _, group := range groups {
		sort.Slice(group, func(i, j int) bool { return group[i].Order < group[j].Order })
		for i := 1; i < len(group); i++ {
			if mapping[group[i-1].ID] >= mapping[group[i].ID] {
				return fmt.Errorf("store: transfer content IDs would change segment order")
			}
		}
	}
	return nil
}

type transferImported struct {
	Root     bool                      `json:"root,omitempty"`
	Snapshot *transfer.Snapshot        `json:"snapshot"`
	Plan     *TransferPlan             `json:"plan"`
	Contents map[string]ContentWritten `json:"contents"`
}

var errTransferReceipt = errors.New("transfer receipt already committed")

// ApplyTransfer revalidates a reviewed decision under the writer lock and
// atomically imports work, origin mappings and its receipt. Exact retries return
// the original receipt before consulting later target edits or policy changes.
func (s *Store) ApplyTransfer(ctx context.Context, actor Actor, x *transfer.Snapshot, p *TransferPlan) (*TransferReceipt, error) {
	if p == nil {
		return nil, fmt.Errorf("store: transfer requires a reviewed plan")
	}
	if err := transfer.Validate(x); err != nil {
		return nil, err
	}
	digest, err := transfer.Digest(x)
	if err != nil {
		return nil, err
	}
	if digest != p.Digest {
		return nil, fmt.Errorf("store: archive differs from reviewed digest")
	}
	// Recovery is a receipt-only read, not another role-attributed write. The
	// importing role may have closed since the operation committed.
	receipt, err := transferReceipt(ctx, s.db, p.Repo, digest)
	if err != nil || receipt != nil {
		return receipt, err
	}
	_, err = s.Commit(ctx, Request{Actor: actor, Env: Env{Repo: p.Repo}}, func(ctx context.Context, t *Tx) ([]Draft, error) {
		var err error
		receipt, err = transferReceipt(ctx, t.q, p.Repo, digest)
		if err != nil {
			return nil, err
		}
		if receipt != nil {
			return nil, errTransferReceipt
		}
		if _, err = s.planTransfer(ctx, t.q, x, p.Repo, p.Renames, p, false); err != nil {
			return nil, err
		}
		// Preview IDs may come from another process with a future clock.
		// Adopt them only after revalidation under the writer reservation so
		// both this event and later native appends sort after imported IDs.
		if p.Operation > s.floor {
			s.floor = p.Operation
		}
		for _, id := range p.Mapping {
			if id > s.floor {
				s.floor = id
			}
		}
		payload := transferImported{Snapshot: x, Plan: p, Contents: map[string]ContentWritten{}}
		for _, c := range x.Contents {
			if p.Reused[c.ID] {
				continue
			}
			w := ContentWritten{Content: p.Mapping[c.ID], Kind: ContentKind(c.Kind), ByteLen: c.Length, SHA256: c.SHA256}
			if len(c.Data) > SpillThreshold {
				if err = s.installTransferBlob(w.Content, c.Data); err != nil {
					return nil, err
				}
				w.BlobRef = w.Content
			} else {
				w.Bytes = append([]byte{}, c.Data...)
			}
			payload.Contents[c.ID] = w
		}
		root, err := canonicalWorkSnapshot(ctx, t.q, payload)
		if err != nil {
			return nil, err
		}
		payload.Root = true
		receipt = &TransferReceipt{Operation: p.Operation, Digest: p.Digest, Repo: p.Repo, Mapping: p.Mapping}
		return []Draft{{Type: TypeWorkSnapshot, Subject: p.Operation, Payload: root}, {Type: TypeWorkImported, Subject: p.Operation, Payload: payload, Cause: 0}}, nil
	})
	if errors.Is(err, errTransferReceipt) {
		return receipt, nil
	}
	if err != nil {
		return nil, err
	}
	return receipt, nil
}

// Install privately, then link atomically without replacing any existing file.
// Called under the DB writer reservation, also used by orphan cleanup.
func (s *Store) installTransferBlob(ref string, data []byte) error {
	if err := os.MkdirAll(s.blobDir, 0o700); err != nil {
		return err
	}
	f, err := os.CreateTemp(s.blobDir, ".transfer-")
	if err != nil {
		return err
	}
	name := f.Name()
	defer func() { _ = os.Remove(name) }()
	if _, err = f.Write(data); err == nil {
		err = f.Sync()
	}
	closeErr := f.Close()
	if err != nil {
		return err
	}
	if closeErr != nil {
		return closeErr
	}
	if err = os.Link(name, filepath.Join(s.blobDir, ref)); err != nil {
		return fmt.Errorf("store: installing transfer blob without overwrite: %w", err)
	}
	return nil
}

// projectTransfer inserts state facts directly through their dedicated fold.
// It never invokes native start/finish/delegation projectors, so no execution
// ownership, tracker candidates, outbox entries or jobs are reconstructed.
func projectTransfer(ctx context.Context, tx *sql.Tx, ev Event) error {
	var p transferImported
	if err := decodeStrict(ev, &p); err != nil {
		return err
	}
	if p.Snapshot == nil || p.Plan == nil {
		return fmt.Errorf("store: incomplete import event")
	}
	x, plan := p.Snapshot, p.Plan
	if err := transfer.ValidateMetadata(x); err != nil {
		return err
	}
	digest, err := transfer.Digest(x)
	if err != nil {
		return err
	}
	if digest != plan.Digest || plan.Operation != ev.Subject || plan.Repo != ev.Repo {
		return fmt.Errorf("store: import event metadata mismatch")
	}
	if p.Root {
		return projectTransferCompanion(ctx, tx, ev, p)
	}
	// The dedicated fold enforces exactly the same mapping/identity/state
	// guards as apply. It intentionally excludes only non-evented pairing.
	if checked, e := (&Store{}).planTransfer(ctx, tx, x, plan.Repo, plan.Renames, plan, true); e != nil {
		return e
	} else if checked.AlreadyImported {
		return fmt.Errorf("store: duplicate import event")
	}
	if err = transferContentOrder(x, plan.Mapping); err != nil {
		return err
	}
	if err = projectTransferState(ctx, tx, ev, p); err != nil {
		return err
	}
	return projectTransferReceipt(ctx, tx, ev, p)
}

func projectTransferState(ctx context.Context, tx *sql.Tx, ev Event, p transferImported) error {
	x, plan := p.Snapshot, p.Plan
	var err error
	nodes := canonicalTransferNodes(x.Nodes)
	sort.SliceStable(nodes, func(i, j int) bool {
		rank := func(k string) int {
			switch k {
			case "matter":
				return 0
			case "stage":
				return 1
			default:
				return 2
			}
		}
		return rank(nodes[i].Kind) < rank(nodes[j].Kind)
	})
	for _, n := range nodes {
		if plan.Reused[n.ID] {
			continue
		}
		locator := n.Locator
		if n.Kind == "matter" {
			locator = plan.Locators[n.ID]
		}
		_, err = tx.ExecContext(ctx, `INSERT INTO nodes(id,kind,repo,matter,parent,locator,title,lifecycle,sort_key,birth_event,last_event) VALUES(?,?,?,?,?,?,?,?,?,?,?)`, plan.Mapping[n.ID], n.Kind, ev.Repo, plan.Mapping[n.Matter], nullable(plan.Mapping[n.Parent]), locator, n.Title, n.Lifecycle, n.SortKey, ev.ID, ev.ID)
		if err != nil {
			return err
		}
	}
	for _, e := range x.Backlog {
		if plan.Reused[e.ID] {
			continue
		}
		_, err = tx.ExecContext(ctx, `INSERT INTO backlog_entries(id,repo,provenance,state,title,detail,origin_node,matter,decline_reason,birth_event,last_event) VALUES(?,?,?,?,?,?,?,?,?,?,?)`, plan.Mapping[e.ID], ev.Repo, e.Provenance, e.State, e.Title, e.Detail, nullable(plan.Mapping[e.OriginNode]), nullable(plan.Mapping[e.Matter]), e.Reason, ev.ID, ev.ID)
		if err != nil {
			return err
		}
	}
	for _, e := range x.Edges {
		if plan.Reused[e.ID] {
			continue
		}
		_, err = tx.ExecContext(ctx, `INSERT INTO edges(id,blocked,blocker,birth_event,last_event) VALUES(?,?,?,?,?)`, plan.Mapping[e.ID], plan.Mapping[e.Blocked], plan.Mapping[e.Blocker], ev.ID, ev.ID)
		if err != nil {
			return err
		}
	}
	for _, c := range x.Contents {
		if plan.Reused[c.ID] {
			continue
		}
		w, ok := p.Contents[c.ID]
		if !ok || w.Content != plan.Mapping[c.ID] || string(w.Kind) != c.Kind || w.ByteLen != c.Length || w.SHA256 != c.SHA256 {
			return fmt.Errorf("store: import content metadata mismatch for %s", c.ID)
		}
		if w.BlobRef != "" && w.BlobRef != w.Content {
			return fmt.Errorf("store: unsafe import blob reference")
		}
		raw, err := json.Marshal(w)
		if err != nil {
			return err
		}
		contentEvent := ev
		contentEvent.Subject = plan.Mapping[c.Owner]
		contentEvent.Type = TypeContentCreated
		if w.Kind.AppendOnlyKind() {
			contentEvent.Type = TypeContentAppended
		}
		contentEvent.Payload = raw
		if err = insertContent(ctx, tx, contentEvent); err != nil {
			return err
		}
	}
	return nil
}

func projectTransferReceipt(ctx context.Context, tx *sql.Tx, ev Event, p transferImported) error {
	x, plan := p.Snapshot, p.Plan
	for _, o := range transferObjects(x) {
		if plan.Reused[o.id] {
			continue
		}
		if p.Root {
			if _, err := tx.ExecContext(ctx, `INSERT INTO transfer_snapshot_imports(target,fingerprint,birth_event) VALUES(?,?,?)`, plan.Mapping[o.id], o.fingerprint, ev.ID); err != nil {
				return err
			}
			continue
		}
		state, err := transferState(ctx, tx, o.kind, plan.Mapping[o.id])
		if err != nil {
			return err
		}
		_, err = tx.ExecContext(ctx, `INSERT INTO transfer_origins(repo,origin,kind,target,fingerprint,state,birth_event) VALUES(?,?,?,?,?,?,?)`, ev.Repo, o.key, o.kind, plan.Mapping[o.id], o.fingerprint, state, ev.ID)
		if err != nil {
			return err
		}
	}
	r := TransferReceipt{Operation: plan.Operation, Digest: plan.Digest, Repo: plan.Repo, Mapping: plan.Mapping}
	raw, err := json.Marshal(r)
	if err != nil {
		return err
	}
	_, err = tx.ExecContext(ctx, `INSERT INTO transfer_receipts(operation,repo,digest,receipt,birth_event) VALUES(?,?,?,?,?)`, r.Operation, r.Repo, r.Digest, string(raw), ev.ID)
	return err
}
