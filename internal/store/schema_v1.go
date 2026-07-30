package store

import (
	"fmt"
	"strings"
)

// This file is the v1 baseline: the whole store, as one numbered migration.
//
// v1 exists even though there is nothing to migrate *from*, so v2 is an
// increment and never a retrofit (the `schema` Brief's migrations posture, and
// the same "exists from P1 so it never needs a retrofit" logic MODEL §10
// applies to the Run and dispatch rows). Nothing in here may be edited once
// shipped; a change is a new numbered migration.
//
// Everything except `event_types`, `config` and `gate_declarations` is a
// projection of the log (D61). See project.go.

// v1ProjectionTables is the frozen v1 list of tables that are projections of
// the event log — every table Rebuild is allowed to clear and re-fold, and
// every table whose rows may only appear or move because of an event.
//
// It is frozen because the guard triggers generated from it are shipped SQL.
// projectionTables (project.go) is the *current* list Rebuild walks; a later
// migration adds its table to both.
var v1ProjectionTables = []string{
	"repos", "clones", "worktrees",
	"nodes", "content", "edges", "gate_state",
	"backlog_entries", "outbox_entries",
	"batches", "batch_members", "runs", "dispatches", "cursors",
}

// v1EventLinkedTables is the frozen v1 list of projection tables carrying a
// `last_event` column — the ones whose rows may only ever advance to a newer
// event. Every projection table has one today; the split exists so a future
// pure-lookup projection is not forced to invent one.
var v1EventLinkedTables = v1ProjectionTables

// v1BornTables is the frozen v1 list of projection tables carrying a
// `birth_event` — rows with their own identity and a fixed start.
var v1BornTables = []string{
	"repos", "clones", "worktrees",
	"nodes", "content", "edges",
	"backlog_entries", "outbox_entries",
	"batches", "runs", "dispatches",
}

// rebuildSentinel is the store_meta key Rebuild sets for the duration of its
// transaction. The delete guards below stand down while it is present.
//
// It is a guard against accident, not against malice: anything that can write
// the database can write this key. What it buys is that no ordinary code path
// can clear a projection by mistake — a deletion has to say, in the same
// transaction, that it is a rebuild. Under this shape that is enough, because a
// cleared projection is recoverable from the log and a lost *event* is not.
const rebuildSentinel = "rebuilding"

func v1Statements() []string {
	stmts := []string{
		// -------------------------------------------------------------------
		// Store metadata. Not a projection: it records facts about the store
		// itself, including the projection version (distinct from the schema
		// version, so the store knows when a rebuild is required).
		// -------------------------------------------------------------------
		`CREATE TABLE store_meta (
			key   TEXT PRIMARY KEY,
			value TEXT NOT NULL
		) STRICT, WITHOUT ROWID`,

		// -------------------------------------------------------------------
		// The taxonomy, as data (D56, "checked in schema"). events.type is a
		// foreign key into it, so an unknown type is refused rather than
		// logged, and each type's required tier dimensions are a row rather
		// than a branch in Go.
		// -------------------------------------------------------------------
		`CREATE TABLE event_types (
			type              TEXT    PRIMARY KEY,
			family            TEXT    NOT NULL,
			requires_repo     INTEGER NOT NULL CHECK (requires_repo     IN (0,1)),
			requires_clone    INTEGER NOT NULL CHECK (requires_clone    IN (0,1)),
			requires_worktree INTEGER NOT NULL CHECK (requires_worktree IN (0,1))
		) STRICT, WITHOUT ROWID`,

		seedTaxonomy(P1Taxonomy),

		// -------------------------------------------------------------------
		// The envelope — eleven columns, exactly as the `schema` Brief §A
		// fixes them. `id` is the event's own ULID and doubles as the
		// total-order sort key: there is no sequence column (D44, D51).
		//
		// actor, causation and correlation are in the v1 baseline rather than
		// in a later migration on purpose. MODEL §10 says later phases add
		// event *types* but never change these columns, so an envelope field
		// arriving as an increment would be exactly the retrofit the contract
		// exists to prevent.
		//
		// causation and correlation are NOT NULL and self-referential on an
		// origin event (the a:a:a form) rather than nullable, so "began its own
		// chain" and "nobody filled this in" are never the same value. Both are
		// foreign keys into events(id), so a chain pointing nowhere is refused
		// by the store rather than by the caller's discipline.
		// -------------------------------------------------------------------
		`CREATE TABLE events (
			id          TEXT NOT NULL PRIMARY KEY,
			type        TEXT NOT NULL REFERENCES event_types(type),
			occurred_at TEXT NOT NULL,
			actor       TEXT NOT NULL,
			causation   TEXT NOT NULL REFERENCES events(id),
			correlation TEXT NOT NULL REFERENCES events(id),
			repo        TEXT,
			clone       TEXT,
			worktree    TEXT,
			subject     TEXT NOT NULL,
			payload     TEXT NOT NULL,

			CHECK (length(id) = 26),
			CHECK (length(subject) = 26),
			CHECK (length(causation) = 26 AND length(correlation) = 26),
			CHECK (repo     IS NULL OR length(repo)     = 26),
			CHECK (clone    IS NULL OR length(clone)    = 26),
			CHECK (worktree IS NULL OR length(worktree) = 26),
			-- Length alone is not identity-hood. occurred_at is *derived* from the
			-- id (timeOfID) rather than read from a second clock, which is how the
			-- store answers store-fork's "two clocks" finding without a twelfth
			-- column — so a 26-character id outside Crockford's alphabet would be an
			-- event no reader could date. The alphabet is therefore checked, not
			-- assumed. causation and correlation inherit it through their foreign
			-- keys into events(id).
			CHECK (id       NOT GLOB '*[^0123456789ABCDEFGHJKMNPQRSTVWXYZ]*'),
			CHECK (subject  NOT GLOB '*[^0123456789ABCDEFGHJKMNPQRSTVWXYZ]*'),
			CHECK (repo     IS NULL OR repo     NOT GLOB '*[^0123456789ABCDEFGHJKMNPQRSTVWXYZ]*'),
			CHECK (clone    IS NULL OR clone    NOT GLOB '*[^0123456789ABCDEFGHJKMNPQRSTVWXYZ]*'),
			CHECK (worktree IS NULL OR worktree NOT GLOB '*[^0123456789ABCDEFGHJKMNPQRSTVWXYZ]*'),
			-- occurred_at is RFC3339 UTC to the millisecond, fixed width, so
			-- lexical order is chronological order.
			CHECK (occurred_at GLOB '[0-9][0-9][0-9][0-9]-[0-9][0-9]-[0-9][0-9]T[0-9][0-9]:[0-9][0-9]:[0-9][0-9].[0-9][0-9][0-9]Z'),
			-- actor is never empty and is always a prefixed token: human,
			-- role:<name>, system:<source>. New roles arrive in P2 with no
			-- migration precisely because this is one open-ended column.
			CHECK (actor = 'human' OR actor GLOB 'role:?*' OR actor GLOB 'system:?*'),
			CHECK (json_valid(payload) AND json_type(payload) = 'object')
		) STRICT, WITHOUT ROWID`,

		`CREATE INDEX events_type ON events(type)`,
		`CREATE INDEX events_subject ON events(subject, id)`,
		// Chains are walked in both directions: "what did this event cause" and
		// "everything in this command".
		`CREATE INDEX events_causation ON events(causation)`,
		`CREATE INDEX events_correlation ON events(correlation)`,
		// Session is a query over a contiguous working period (D17).
		`CREATE INDEX events_occurred_at ON events(occurred_at)`,

		// Append-only, enforced by the substrate rather than by Go discipline:
		// no code path in or out of this package can rewrite or drop an event.
		`CREATE TRIGGER events_no_update BEFORE UPDATE ON events
		 BEGIN SELECT RAISE(ABORT, 'events are append-only: no UPDATE'); END`,
		`CREATE TRIGGER events_no_delete BEFORE DELETE ON events
		 BEGIN SELECT RAISE(ABORT, 'events are append-only: no DELETE'); END`,
		// Append-only *at the end*: an event may only be added after every
		// event already in the log. This is what lets the ULID double as the
		// order key, and it is the database-level half of the high-water mark
		// Store.loadFloor establishes at open.
		`CREATE TRIGGER events_monotonic BEFORE INSERT ON events
		 WHEN NEW.id <= (SELECT COALESCE(MAX(id), '') FROM events)
		 BEGIN SELECT RAISE(ABORT, 'event ids must ascend strictly: the id is the total order'); END`,
		// A cause never points forward: both pointers always name an event
		// already in the log, or this event itself (the origin form).
		`CREATE TRIGGER events_cause_never_forward BEFORE INSERT ON events
		 WHEN NEW.causation > NEW.id OR NEW.correlation > NEW.id
		 BEGIN SELECT RAISE(ABORT, 'causation and correlation may never name a later event'); END`,
		// D56, checked in schema: the dimension set is a total function of the
		// type. `batch.*` carrying a null repo is a row in event_types, not a
		// special case in code.
		`CREATE TRIGGER events_dimensions BEFORE INSERT ON events
		 WHEN NOT (
			 (SELECT requires_repo     FROM event_types WHERE type = NEW.type) = (NEW.repo     IS NOT NULL)
		 AND (SELECT requires_clone    FROM event_types WHERE type = NEW.type) = (NEW.clone    IS NOT NULL)
		 AND (SELECT requires_worktree FROM event_types WHERE type = NEW.type) = (NEW.worktree IS NOT NULL)
		 )
		 BEGIN SELECT RAISE(ABORT, 'tier dimensions must match the static rule for this event type (D56)'); END`,

		// -------------------------------------------------------------------
		// The three tiers (MODEL §7, D37): each an entity with a ULID identity
		// and one natural key. Paths and labels are mutable attributes, never
		// keys — remote adoption and relinking keep the ULID and the history.
		// -------------------------------------------------------------------
		`CREATE TABLE repos (
			id              TEXT NOT NULL PRIMARY KEY,
			remote_url      TEXT,
			identity_remote TEXT,
			label           TEXT,
			birth_event     TEXT NOT NULL REFERENCES events(id),
			last_event      TEXT NOT NULL REFERENCES events(id),
			CHECK (length(id) = 26)
		) STRICT, WITHOUT ROWID`,
		// The natural key is nullable (a Repo needs no remote, D39) and unique
		// where present: two local-only repos are distinguished by identity
		// alone, and a repo adopting a remote another already claims is refused
		// here rather than discovered later.
		`CREATE UNIQUE INDEX repos_remote_url ON repos(remote_url) WHERE remote_url IS NOT NULL`,

		`CREATE TABLE clones (
			id             TEXT NOT NULL PRIMARY KEY,
			repo           TEXT NOT NULL REFERENCES repos(id),
			git_common_dir TEXT NOT NULL,
			label          TEXT NOT NULL,
			birth_event    TEXT NOT NULL REFERENCES events(id),
			last_event     TEXT NOT NULL REFERENCES events(id),
			CHECK (length(id) = 26)
		) STRICT, WITHOUT ROWID`,
		`CREATE UNIQUE INDEX clones_git_common_dir ON clones(git_common_dir)`,
		// Labels are per-repo unique, defaulted and mutable (PLAN 1.1).
		`CREATE UNIQUE INDEX clones_label ON clones(repo, label)`,

		`CREATE TABLE worktrees (
			id          TEXT NOT NULL PRIMARY KEY,
			clone       TEXT NOT NULL REFERENCES clones(id),
			name        TEXT,
			birth_event TEXT NOT NULL REFERENCES events(id),
			last_event  TEXT NOT NULL REFERENCES events(id),
			CHECK (length(id) = 26)
		) STRICT, WITHOUT ROWID`,
		// name is NULL for the main worktree (D37's null case). COALESCE makes
		// the uniqueness hold for it too — SQLite treats NULLs as distinct, so
		// a bare unique index would let one clone have two main worktrees.
		`CREATE UNIQUE INDEX worktrees_name ON worktrees(clone, COALESCE(name, ''))`,

		// -------------------------------------------------------------------
		// Nodes: Matter, Stage and Step in one table.
		//
		// One table rather than three, because the model is uniform at every
		// scale (MODEL §2.2) and because `blocked-by` puts any node on either
		// end of an edge (MODEL §9) — that needs one identity space. Stage's
		// entity-hood is not weakened by sharing the table: it is a row exactly
		// like Step, with its own ULID, sort key, gate state and edges.
		//
		// sort_key is presentation-only (D51). Nothing infers execution order
		// from it or from row order; ordering that means something comes only
		// from `blocked-by` edges.
		// -------------------------------------------------------------------
		`CREATE TABLE nodes (
			id              TEXT NOT NULL PRIMARY KEY,
			kind            TEXT NOT NULL CHECK (kind IN ('matter','stage','step')),
			repo            TEXT NOT NULL REFERENCES repos(id),
			matter          TEXT NOT NULL REFERENCES nodes(id),
			parent          TEXT REFERENCES nodes(id),
			locator         TEXT NOT NULL,
			title           TEXT NOT NULL,
			lifecycle       TEXT NOT NULL CHECK (lifecycle IN ('planned','in-progress','done','canceled','paused')),
			sort_key        INTEGER NOT NULL,
			external_ref    TEXT,
			birth_event     TEXT NOT NULL REFERENCES events(id),
			last_event      TEXT NOT NULL REFERENCES events(id),
			tombstone_event TEXT REFERENCES events(id),
			CHECK (length(id) = 26),
			-- No Matter nesting; a Matter is the addressable root (D19, D20).
			CHECK ((kind = 'matter') = (parent IS NULL)),
			-- A Matter is its own Matter, which is what makes this column usable
			-- as the addressing scope for every node including the root.
			CHECK ((kind = 'matter') = (matter = id))
		) STRICT, WITHOUT ROWID`,
		// Invariant 2's index. birth_event is the ULID of the *.created event
		// that produced the row, so creation order is a column and needs no
		// sort key of its own. Partial, because a tombstoned row is not part of
		// any answer.
		`CREATE INDEX nodes_lifecycle ON nodes(lifecycle, birth_event) WHERE tombstone_event IS NULL`,
		`CREATE INDEX nodes_parent ON nodes(parent, sort_key) WHERE tombstone_event IS NULL`,
		`CREATE INDEX nodes_repo ON nodes(repo, kind) WHERE tombstone_event IS NULL`,
		`CREATE INDEX nodes_matter ON nodes(matter, sort_key) WHERE tombstone_event IS NULL`,
		// Locators are mutable and nothing references them (MODEL §10), but they
		// must still address exactly one live node within a Matter: `step-NN` is
		// globally sequential within a Matter, and a grouping is not a namespace
		// (D16), so the scope is the Matter and not the parent. A Stage grouping
		// its Steps therefore neither renames nor renumbers them.
		`CREATE UNIQUE INDEX nodes_locator ON nodes(matter, locator) WHERE tombstone_event IS NULL`,

		// Max depth Matter -> Stage -> Step (D20), checked in schema rather
		// than trusted to the verbs.
		`CREATE TRIGGER nodes_depth BEFORE INSERT ON nodes
		 WHEN (NEW.kind = 'stage' AND (SELECT kind FROM nodes WHERE id = NEW.parent) <> 'matter')
		   OR (NEW.kind = 'step'  AND (SELECT kind FROM nodes WHERE id = NEW.parent) NOT IN ('matter','stage'))
		 BEGIN SELECT RAISE(ABORT, 'max depth is Matter -> Stage -> Step (D20)'); END`,

		// -------------------------------------------------------------------
		// Prose and content. One table; each content object is a row carrying
		// its owning node, its kind, and its bytes (D36: the store is the
		// source of truth for all content including prose).
		//
		// Append kinds accumulate as segments, ordered by identity; create-once
		// kinds are constrained to exactly one live segment by the partial
		// unique index below. Oversized ingested blobs spill to a sidecar file
		// and the row keeps the reference — see content.go.
		// -------------------------------------------------------------------
		`CREATE TABLE content (
			id              TEXT NOT NULL PRIMARY KEY,
			node            TEXT NOT NULL REFERENCES nodes(id),
			kind            TEXT NOT NULL CHECK (kind IN ('brief','workplan','body','findings')),
			bytes           BLOB,
			blob_ref        TEXT,
			byte_len        INTEGER NOT NULL CHECK (byte_len >= 0),
			sha256          TEXT NOT NULL CHECK (length(sha256) = 64),
			birth_event     TEXT NOT NULL REFERENCES events(id),
			last_event      TEXT NOT NULL REFERENCES events(id),
			tombstone_event TEXT REFERENCES events(id),
			CHECK (length(id) = 26),
			-- Exactly one of the two storage forms, never both and never
			-- neither: a row always knows where its bytes are.
			CHECK ((bytes IS NULL) <> (blob_ref IS NULL))
		) STRICT, WITHOUT ROWID`,
		`CREATE INDEX content_node ON content(node, kind, id) WHERE tombstone_event IS NULL`,
		`CREATE UNIQUE INDEX content_create_once ON content(node, kind)
		 WHERE kind IN ('brief','workplan','body') AND tombstone_event IS NULL`,

		// -------------------------------------------------------------------
		// `blocked-by` edges: one row per directed edge, each with its own
		// identity, tombstoned on removal and never hard-deleted (D44). Any
		// node may block any node, within or across Matters (D28, D29).
		// -------------------------------------------------------------------
		`CREATE TABLE edges (
			id              TEXT NOT NULL PRIMARY KEY,
			blocked         TEXT NOT NULL REFERENCES nodes(id),
			blocker         TEXT NOT NULL REFERENCES nodes(id),
			birth_event     TEXT NOT NULL REFERENCES events(id),
			last_event      TEXT NOT NULL REFERENCES events(id),
			tombstone_event TEXT REFERENCES events(id),
			CHECK (length(id) = 26),
			-- A self-edge is the degenerate cycle; the static check refuses it
			-- too, and this is the floor under that check.
			CHECK (blocked <> blocker)
		) STRICT, WITHOUT ROWID`,
		`CREATE UNIQUE INDEX edges_live ON edges(blocked, blocker) WHERE tombstone_event IS NULL`,
		`CREATE INDEX edges_blocker ON edges(blocker) WHERE tombstone_event IS NULL`,

		// An edge is in force only while both its ends are, and this view is the
		// single definition of that — every read of the dependency graph goes
		// through it, so "what blocks this" and "what loops does this store have"
		// cannot answer the question differently.
		//
		// Why both ends and not just the row: an edge is a relation between two
		// nodes, and a relation to a node that has been structurally removed is not
		// a relation. Removing the blocker is how D44's amendment clears an
		// obstruction; if the edge outlived it, `wip status` would report a blocker
		// that can never complete and `doctor` would report loops running through
		// removed nodes — loops no repair can break, because there is nothing left
		// to unblock. The edge row itself is untouched and stays tombstone-free: it
		// is out of force, not removed, and its own `dependency.removed` would still
		// be a legitimate event.
		`CREATE VIEW edges_in_force AS
		 SELECT e.id, e.blocked, e.blocker
		 FROM   edges e
		 JOIN   nodes blocked ON blocked.id = e.blocked AND blocked.tombstone_event IS NULL
		 JOIN   nodes blocker ON blocker.id = e.blocker AND blocker.tombstone_event IS NULL
		 WHERE  e.tombstone_event IS NULL`,

		// -------------------------------------------------------------------
		// Gate state, storable at any scale (MODEL §9). This is a storage
		// capability, not a declaration: the dogfood declares exactly one gate,
		// `reviewed-local: matter` (HANDOFF §1.2), and declaring others would
		// make Matters structurally unable to seal (MODEL §2.3).
		// -------------------------------------------------------------------
		`CREATE TABLE gate_state (
			node       TEXT NOT NULL REFERENCES nodes(id),
			gate       TEXT NOT NULL,
			scale      TEXT NOT NULL CHECK (scale IN ('matter','stage','step')),
			closed_at  TEXT NOT NULL,
			last_event TEXT NOT NULL REFERENCES events(id),
			PRIMARY KEY (node, gate)
		) STRICT, WITHOUT ROWID`,

		// -------------------------------------------------------------------
		// Project config at the Repo tier (D42, D54) — and gate *declarations*,
		// which are config rather than events (D4: declared up front, statically
		// knowable). These two tables are the documented exception to "every
		// durable table is a projection": there is no event to fold, so Rebuild
		// leaves them untouched.
		// -------------------------------------------------------------------
		`CREATE TABLE config (
			repo  TEXT NOT NULL REFERENCES repos(id),
			key   TEXT NOT NULL,
			value TEXT NOT NULL,
			PRIMARY KEY (repo, key)
		) STRICT, WITHOUT ROWID`,

		`CREATE TABLE gate_declarations (
			repo  TEXT NOT NULL REFERENCES repos(id),
			gate  TEXT NOT NULL,
			scale TEXT NOT NULL CHECK (scale IN ('matter','stage','step')),
			PRIMARY KEY (repo, gate)
		) STRICT, WITHOUT ROWID`,

		// -------------------------------------------------------------------
		// Backlog (MODEL §4): one list, one noun, one exit set. Deferred is a
		// provenance, not a second list (D9).
		// -------------------------------------------------------------------
		`CREATE TABLE backlog_entries (
			id          TEXT NOT NULL PRIMARY KEY,
			repo        TEXT NOT NULL REFERENCES repos(id),
			provenance  TEXT NOT NULL CHECK (provenance IN ('intake','found','deferred')),
			state       TEXT NOT NULL CHECK (state IN ('entered','planned','declined')),
			title       TEXT NOT NULL,
			detail      TEXT NOT NULL DEFAULT '',
			origin_node TEXT REFERENCES nodes(id),
			matter      TEXT REFERENCES nodes(id),
			birth_event TEXT NOT NULL REFERENCES events(id),
			last_event  TEXT NOT NULL REFERENCES events(id),
			CHECK (length(id) = 26),
			-- Declined must be distinguishable from not-yet-acted-upon
			-- (MODEL §4), and planned must name what it became.
			CHECK ((state = 'planned') = (matter IS NOT NULL))
		) STRICT, WITHOUT ROWID`,
		`CREATE INDEX backlog_state ON backlog_entries(repo, state, id)`,

		// -------------------------------------------------------------------
		// The outbox (D15, D50). Provisioned here and consumed in a later
		// phase: with no backend it accumulates and never flushes, which is a
		// legitimate steady state.
		// -------------------------------------------------------------------
		`CREATE TABLE outbox_entries (
			id              TEXT NOT NULL PRIMARY KEY,
			state           TEXT NOT NULL CHECK (state IN ('pending','accepted','declined')),
			subject         TEXT REFERENCES nodes(id),
			idempotency_key TEXT NOT NULL,
			payload         TEXT NOT NULL,
			birth_event     TEXT NOT NULL REFERENCES events(id),
			last_event      TEXT NOT NULL REFERENCES events(id),
			CHECK (length(id) = 26),
			CHECK (json_valid(payload) AND json_type(payload) = 'object')
		) STRICT, WITHOUT ROWID`,
		`CREATE UNIQUE INDEX outbox_idempotency ON outbox_entries(idempotency_key)`,

		// -------------------------------------------------------------------
		// Batch (MODEL §6): keys at no tier, so cross-repo Batches are legal by
		// construction rather than by exception (D39). Membership is evented
		// and idempotent per (batch, matter); an empty Batch is legal (D58).
		// -------------------------------------------------------------------
		`CREATE TABLE batches (
			id          TEXT NOT NULL PRIMARY KEY,
			name        TEXT,
			birth_event TEXT NOT NULL REFERENCES events(id),
			last_event  TEXT NOT NULL REFERENCES events(id),
			CHECK (length(id) = 26)
		) STRICT, WITHOUT ROWID`,
		// name is NULL for the anonymous batch a bare "work this Matter" wraps
		// in, so there is exactly one dispatch path (MODEL §6, D23).
		`CREATE UNIQUE INDEX batches_name ON batches(name) WHERE name IS NOT NULL`,

		`CREATE TABLE batch_members (
			batch      TEXT NOT NULL REFERENCES batches(id),
			matter     TEXT NOT NULL REFERENCES nodes(id),
			left_event TEXT REFERENCES events(id),
			last_event TEXT NOT NULL REFERENCES events(id),
			PRIMARY KEY (batch, matter)
		) STRICT, WITHOUT ROWID`,

		// -------------------------------------------------------------------
		// Run (D48, provisional noun) and Dispatch (D59). The Run row exists
		// from P1 even though P2 drives it, so the single dispatch path never
		// needs a retrofit (MODEL §10).
		// -------------------------------------------------------------------
		`CREATE TABLE runs (
			id          TEXT NOT NULL PRIMARY KEY,
			clone       TEXT NOT NULL REFERENCES clones(id),
			batch       TEXT NOT NULL REFERENCES batches(id),
			state       TEXT NOT NULL,
			birth_event TEXT NOT NULL REFERENCES events(id),
			last_event  TEXT NOT NULL REFERENCES events(id),
			CHECK (length(id) = 26)
		) STRICT, WITHOUT ROWID`,

		`CREATE TABLE dispatches (
			id           TEXT NOT NULL PRIMARY KEY,
			clone        TEXT NOT NULL REFERENCES clones(id),
			worktree     TEXT NOT NULL REFERENCES worktrees(id),
			state        TEXT NOT NULL CHECK (state IN ('open','closed')),
			close_reason TEXT CHECK (close_reason IS NULL OR close_reason IN ('completed','superseded','reaped')),
			opened_at    TEXT NOT NULL,
			closed_at    TEXT,
			birth_event  TEXT NOT NULL REFERENCES events(id),
			last_event   TEXT NOT NULL REFERENCES events(id),
			CHECK (length(id) = 26),
			-- A close always carries its reason (D59); Session discounts every
			-- reason but 'completed', so the reason may not be absent.
			CHECK ((state = 'closed') = (close_reason IS NOT NULL)),
			CHECK ((state = 'closed') = (closed_at IS NOT NULL))
		) STRICT, WITHOUT ROWID`,
		`CREATE INDEX dispatches_open ON dispatches(worktree, state)`,

		// -------------------------------------------------------------------
		// The cursor: attention, never a fact about the work (D38). A state row
		// keyed at Clone + Worktree whose moves are logged like any write
		// (PLAN 1.7), so reading it is one keyed lookup.
		// -------------------------------------------------------------------
		`CREATE TABLE cursors (
			clone      TEXT NOT NULL REFERENCES clones(id),
			worktree   TEXT NOT NULL REFERENCES worktrees(id),
			node       TEXT REFERENCES nodes(id),
			last_event TEXT NOT NULL REFERENCES events(id),
			PRIMARY KEY (clone, worktree)
		) STRICT, WITHOUT ROWID`,

		// -------------------------------------------------------------------
		// Archive (MODEL §9): sealed Matters, keyed at Repo. It is a view and
		// not a table, because D55 makes *sealed* a predicate over lifecycle
		// and gates rather than a frame or a state, and there is no `*.sealed`
		// event to project. At Matter scale there are no enclosing scales, so
		// the predicate is: Done, with every matter-scale gate declared for its
		// Repo closed against it.
		// -------------------------------------------------------------------
		`CREATE VIEW archived_matters AS
		 SELECT n.id, n.repo, n.locator, n.title, n.birth_event, n.last_event
		 FROM   nodes n
		 WHERE  n.kind = 'matter'
		   AND  n.lifecycle = 'done'
		   AND  n.tombstone_event IS NULL
		   AND  NOT EXISTS (
			    SELECT 1 FROM gate_declarations g
			    WHERE  g.repo = n.repo AND g.scale = 'matter'
			      AND  NOT EXISTS (
				       SELECT 1 FROM gate_state s
				       WHERE  s.node = n.id AND s.gate = g.gate)
		   )`,
	}

	return append(stmts, projectionGuards(v1ProjectionTables, v1EventLinkedTables, v1BornTables)...)
}

// projectionGuards generates the triggers that make "a projection row may only
// appear or move because of an event" a property of the database rather than of
// this package's discipline.
//
// This is the hole the winning spike's own report named as its largest: the log
// had triggers and the projection could not, because Rebuild has to be able to
// clear it. Three guards close most of it, and the part they do not close is
// stated rather than implied:
//
//   - a row is born naming the event that bore it (birth_event = last_event on
//     insert), and its identity, kind and parentage never change afterwards;
//   - a row may only ever advance to a strictly newer event — which holds on
//     the live path (event ids ascend) and on the rebuild path (the fold is in
//     ascending order), so a stale or replayed event cannot move a row;
//   - a row may only be deleted inside a rebuild.
//
// What remains unguarded: nothing checks that the event a row names is an event
// *about* that row. It cannot, because amendment is legitimately not
// one-event-one-row — `step.reordered` names the parent and moves its children.
// The loser spike's per-node coupling bought that check by making a shape where
// amendment did not exist yet.
func projectionGuards(projection, eventLinked, born []string) []string {
	var stmts []string

	for _, table := range born {
		stmts = append(stmts, fmt.Sprintf(
			`CREATE TRIGGER %[1]s_born_by_event BEFORE INSERT ON %[1]s
			 WHEN NEW.birth_event <> NEW.last_event
			 BEGIN SELECT RAISE(ABORT, '%[1]s: a row is born by exactly one event'); END`, table))

		stmts = append(stmts, fmt.Sprintf(
			`CREATE TRIGGER %[1]s_immutable_birth BEFORE UPDATE ON %[1]s
			 WHEN NEW.birth_event <> OLD.birth_event
			 BEGIN SELECT RAISE(ABORT, '%[1]s: a row''s birth event is immutable'); END`, table))
	}

	// Identity, kind and parentage are the facts every prior event referencing
	// this row relied on (MODEL §10's identity-not-locator rule), so they are
	// immutable even though the locator hanging off them is not.
	stmts = append(stmts, `CREATE TRIGGER nodes_immutable_identity BEFORE UPDATE ON nodes
		 WHEN NEW.kind <> OLD.kind
		   OR NEW.repo <> OLD.repo
		   OR NEW.matter <> OLD.matter
		   OR COALESCE(NEW.parent, '') <> COALESCE(OLD.parent, '')
		 BEGIN SELECT RAISE(ABORT, 'a node''s kind, repo, matter and parent are immutable; the locator is not'); END`)

	for _, table := range eventLinked {
		stmts = append(stmts, fmt.Sprintf(
			`CREATE TRIGGER %[1]s_advance BEFORE UPDATE ON %[1]s
			 WHEN NEW.last_event <= OLD.last_event
			 BEGIN SELECT RAISE(ABORT, '%[1]s: a row may only advance to a newer event'); END`, table))
	}

	for _, table := range projection {
		stmts = append(stmts, fmt.Sprintf(
			`CREATE TRIGGER %[1]s_no_delete BEFORE DELETE ON %[1]s
			 WHEN NOT EXISTS (SELECT 1 FROM store_meta WHERE key = '%[2]s')
			 BEGIN SELECT RAISE(ABORT, '%[1]s is a projection: rows are tombstoned, not deleted (D44); only a rebuild may clear it'); END`,
			table, rebuildSentinel))
	}

	return stmts
}

// seedTaxonomy renders one taxonomy set as the INSERT its migration ships.
func seedTaxonomy(types []EventType) string {
	rows := make([]string, 0, len(types))
	for _, t := range types {
		rows = append(rows, fmt.Sprintf("('%s', '%s', %d, %d, %d)",
			t.Type, t.Family, boolToInt(t.Repo), boolToInt(t.Clone), boolToInt(t.Worktree)))
	}
	return "INSERT INTO event_types (type, family, requires_repo, requires_clone, requires_worktree) VALUES\n\t" +
		strings.Join(rows, ",\n\t")
}

func boolToInt(b bool) int {
	if b {
		return 1
	}
	return 0
}
