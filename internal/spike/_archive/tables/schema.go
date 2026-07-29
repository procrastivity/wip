package tables

// This file is the whole schema, as a list of numbered migrations. The two
// versions are kept as separate units so the diff between them is legible on
// its own — v1 is the pinned scenario, v2 is the migration exercise from
// docs/store-fork/scenario.md ("nodes gain an optional label").
//
// The teeth of MODEL §10 invariant 1 live here, not in Go. See coupling.go for
// the Go-side containment and report.md for what each trigger buys.

// migration is one numbered, all-or-nothing schema unit. PLAN 1.2's posture is
// versioned migrations plus backup-before-migrate; `migrate` does both.
type migration struct {
	version int
	name    string
	stmts   []string
}

// migrations is the ordered, gapless list. `latestVersion` is its last entry.
var migrations = []migration{
	{version: 1, name: "core", stmts: v1Statements},
	{version: 2, name: "labels", stmts: v2Statements},
}

func latestVersion() int { return migrations[len(migrations)-1].version }

// ---------------------------------------------------------------------------
// v1 — the pinned scenario: nodes as the source of truth, events as the log.
// ---------------------------------------------------------------------------

var v1Statements = []string{
	// The taxonomy is data, not a Go constant, because D56 says required tier
	// dimensions are a *static function of event type* and are "checked in
	// schema". Making the function a table means (a) an unknown event type is
	// rejected by a foreign key rather than silently logged, and (b) a new
	// event type in a later migration is one INSERT.
	`CREATE TABLE event_types (
		type              TEXT    PRIMARY KEY,
		requires_repo     INTEGER NOT NULL CHECK (requires_repo     IN (0,1)),
		requires_clone    INTEGER NOT NULL CHECK (requires_clone    IN (0,1)),
		requires_worktree INTEGER NOT NULL CHECK (requires_worktree IN (0,1))
	) STRICT`,

	`INSERT INTO event_types (type, requires_repo, requires_clone, requires_worktree) VALUES
		('matter.created',  1, 0, 0),
		('step.created',    1, 0, 0),
		('matter.started',  1, 0, 0),
		('step.started',    1, 0, 0),
		('matter.finished', 1, 0, 0),
		('step.finished',   1, 0, 0)`,

	// The audit log. MODEL §10's envelope, one column per field. `id` is the
	// event's own ULID and doubles as the total-order key — there is no
	// sequence column (D44, D51).
	`CREATE TABLE events (
		id          TEXT PRIMARY KEY,
		type        TEXT NOT NULL REFERENCES event_types(type),
		occurred_at TEXT NOT NULL,
		repo        TEXT,
		clone       TEXT,
		worktree    TEXT,
		subject     TEXT NOT NULL,
		payload     TEXT NOT NULL,
		CHECK (length(id) = 26),
		CHECK (length(subject) = 26),
		CHECK (json_valid(payload) AND json_type(payload) = 'object')
	) STRICT`,

	// Append-only is a property of the table, not of the code that writes it.
	`CREATE TRIGGER events_no_update BEFORE UPDATE ON events
	 BEGIN SELECT RAISE(ABORT, 'events is append-only: no UPDATE'); END`,

	`CREATE TRIGGER events_no_delete BEFORE DELETE ON events
	 BEGIN SELECT RAISE(ABORT, 'events is append-only: no DELETE'); END`,

	// Append-only *at the end*: an event may only be added after every event
	// already there. This is what lets the ULID double as the order key.
	`CREATE TRIGGER events_monotonic BEFORE INSERT ON events
	 WHEN NEW.id <= (SELECT COALESCE(MAX(id), '') FROM events)
	 BEGIN SELECT RAISE(ABORT, 'event ids must ascend strictly'); END`,

	// D56, checked in schema: the dimension set is a function of the type.
	`CREATE TRIGGER events_dimensions BEFORE INSERT ON events
	 WHEN NOT (
		 (SELECT requires_repo     FROM event_types WHERE type = NEW.type) = (NEW.repo     IS NOT NULL)
	 AND (SELECT requires_clone    FROM event_types WHERE type = NEW.type) = (NEW.clone    IS NOT NULL)
	 AND (SELECT requires_worktree FROM event_types WHERE type = NEW.type) = (NEW.worktree IS NOT NULL)
	 )
	 BEGIN SELECT RAISE(ABORT, 'tier dimensions must match the static rule for this event type (D56)'); END`,

	// The source of truth. `born_event_id` is the matter.created / step.created
	// event: it is also the creation-order sort key, for free, since event ULIDs
	// ascend. `last_event_id` is the coupling handle — see the two triggers
	// below.
	`CREATE TABLE nodes (
		id            TEXT PRIMARY KEY,
		kind          TEXT NOT NULL CHECK (kind IN ('matter','step')),
		parent        TEXT REFERENCES nodes(id),
		locator       TEXT NOT NULL,
		title         TEXT NOT NULL,
		lifecycle     TEXT NOT NULL CHECK (lifecycle IN ('planned','in-progress','done')),
		born_event_id TEXT NOT NULL REFERENCES events(id),
		last_event_id TEXT NOT NULL REFERENCES events(id),
		CHECK (length(id) = 26),
		CHECK ((kind = 'matter') = (parent IS NULL))
	) STRICT`,

	// Locators are unique within a parent and mutable; nothing references them.
	// (A Matter's locator is the constant "matter"; only siblings compete.)
	`CREATE UNIQUE INDEX nodes_locator ON nodes (parent, locator) WHERE parent IS NOT NULL`,

	`CREATE INDEX nodes_lifecycle ON nodes (lifecycle, born_event_id)`,

	// ---- MODEL §10 invariant 1, enforced by the database -------------------
	//
	// A nodes row may only appear if an event about *that node* (subject =
	// the node's id) was written in the same transaction and is named by
	// last_event_id.
	`CREATE TRIGGER nodes_insert_requires_event AFTER INSERT ON nodes
	 WHEN NOT EXISTS (
		 SELECT 1 FROM events WHERE id = NEW.last_event_id AND subject = NEW.id
	 )
	 BEGIN SELECT RAISE(ABORT, 'nodes INSERT must carry an event about that node (MODEL §10 invariant 1)'); END`,

	// A nodes row may only change if it advances to a *strictly newer* event
	// about that same node. `>` (not `<>`) is deliberate: event ids ascend
	// globally, so only an event minted after the row's current one can
	// satisfy it — a stale or replayed event id cannot.
	`CREATE TRIGGER nodes_update_requires_event AFTER UPDATE ON nodes
	 WHEN NEW.last_event_id <= OLD.last_event_id
	   OR NOT EXISTS (
		 SELECT 1 FROM events WHERE id = NEW.last_event_id AND subject = NEW.id
	 )
	 BEGIN SELECT RAISE(ABORT, 'nodes UPDATE must advance to a newer event about that node (MODEL §10 invariant 1)'); END`,

	// The birth event may never be rewritten, and neither may identity or
	// parentage: a node's history has a fixed start.
	`CREATE TRIGGER nodes_immutable_birth BEFORE UPDATE ON nodes
	 WHEN NEW.id <> OLD.id OR NEW.born_event_id <> OLD.born_event_id
	   OR NEW.kind <> OLD.kind OR COALESCE(NEW.parent,'') <> COALESCE(OLD.parent,'')
	 BEGIN SELECT RAISE(ABORT, 'node identity, kind, parent and birth event are immutable'); END`,

	// Out of scope here (MODEL wants tombstones, D44), but the spike should not
	// leave a silent hole where a row can vanish without a trace in the log.
	`CREATE TRIGGER nodes_no_delete BEFORE DELETE ON nodes
	 BEGIN SELECT RAISE(ABORT, 'nodes are never deleted; removal is a tombstone verb (D44), out of scope'); END`,
}

// ---------------------------------------------------------------------------
// v2 — the migration exercise. Everything below is the entire cost of "nodes
// gain an optional label, set by a new verb, emitting a new event type, and
// surfaced in the in-progress answer".
// ---------------------------------------------------------------------------

var v2Statements = []string{
	// Nullable, so every row that already exists is already valid: no backfill,
	// no rewrite, no default. This is the whole "what had to be true about
	// existing data" answer.
	`ALTER TABLE nodes ADD COLUMN label TEXT`,

	// Two new event types, with their D56 dimension rule, as data.
	`INSERT INTO event_types (type, requires_repo, requires_clone, requires_worktree) VALUES
		('matter.labeled', 1, 0, 0),
		('step.labeled',   1, 0, 0)`,
}
