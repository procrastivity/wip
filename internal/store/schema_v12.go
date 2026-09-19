package store

// v12 widens content ownership beyond nodes: findings may attach to backlog
// entries, so triage evidence accumulates on the entry it is about while the
// entry sits in the funnel.
//
// The v1 `node REFERENCES nodes(id)` FK was the only rule narrowing content
// to plan nodes, and SQLite cannot declare "a node or a backlog entry", so
// the table is rebuilt with ULID-shape checks — the posture
// `outbox_entries.subject` took in v5 for the same reason. The existence
// proof the FK used to give moves into the projection (insertContent), which
// runs on the live path and on every rebuild alike: create-once kinds still
// demand a node; findings accept a node or a backlog entry.
func v12Statements() []string {
	return []string{
		// No taxonomy seed: no new event types. `content.appended` keeps
		// subject = the owning entity; the event envelope already admits any
		// ULID-shaped subject.
		`DROP INDEX content_node`,
		`DROP INDEX content_create_once`,
		`ALTER TABLE content RENAME TO content_v11`,
		`CREATE TABLE content (
			id              TEXT NOT NULL PRIMARY KEY,
			node            TEXT NOT NULL,
			kind            TEXT NOT NULL CHECK (kind IN ('brief','workplan','body','findings')),
			bytes           BLOB,
			blob_ref        TEXT,
			byte_len        INTEGER NOT NULL CHECK (byte_len >= 0),
			sha256          TEXT NOT NULL CHECK (length(sha256) = 64),
			birth_event     TEXT NOT NULL REFERENCES events(id),
			last_event      TEXT NOT NULL REFERENCES events(id),
			tombstone_event TEXT REFERENCES events(id),
			CHECK (length(id) = 26),
			CHECK (id NOT GLOB '*[^0123456789ABCDEFGHJKMNPQRSTVWXYZ]*'),
			CHECK (length(node) = 26),
			CHECK (node NOT GLOB '*[^0123456789ABCDEFGHJKMNPQRSTVWXYZ]*'),
			-- Exactly one of the two storage forms, never both and never
			-- neither: a row always knows where its bytes are.
			CHECK ((bytes IS NULL) <> (blob_ref IS NULL))
		) STRICT, WITHOUT ROWID`,
		// Copy before the guard triggers exist, as v5 did for backlog_entries:
		// a row whose last_event has advanced past its birth must not trip
		// content_born_by_event on the way across.
		`INSERT INTO content
			(id, node, kind, bytes, blob_ref, byte_len, sha256, birth_event, last_event, tombstone_event)
		 SELECT id, node, kind, bytes, blob_ref, byte_len, sha256, birth_event, last_event, tombstone_event
		 FROM content_v11`,
		`DROP TABLE content_v11`,
		`CREATE INDEX content_node ON content(node, kind, id) WHERE tombstone_event IS NULL`,
		`CREATE UNIQUE INDEX content_create_once ON content(node, kind)
		 WHERE kind IN ('brief','workplan','body') AND tombstone_event IS NULL`,
		// The four generated projection guards, recreated by hand because
		// projectionGuards also emits the nodes identity trigger and cannot be
		// reused for one table.
		`CREATE TRIGGER content_born_by_event BEFORE INSERT ON content
		 WHEN NEW.birth_event <> NEW.last_event
		 BEGIN SELECT RAISE(ABORT, 'content: a row is born by exactly one event'); END`,
		`CREATE TRIGGER content_immutable_birth BEFORE UPDATE ON content
		 WHEN NEW.birth_event <> OLD.birth_event
		 BEGIN SELECT RAISE(ABORT, 'content: a row''s birth event is immutable'); END`,
		`CREATE TRIGGER content_advance BEFORE UPDATE ON content
		 WHEN NEW.last_event <= OLD.last_event
		 BEGIN SELECT RAISE(ABORT, 'content: a row may only advance to a newer event'); END`,
		`CREATE TRIGGER content_no_delete BEFORE DELETE ON content
		 WHEN NOT EXISTS (SELECT 1 FROM store_meta WHERE key = 'rebuilding')
		 BEGIN SELECT RAISE(ABORT, 'content is a projection: rows are tombstoned, not deleted (D44); only a rebuild may clear it'); END`,
	}
}
