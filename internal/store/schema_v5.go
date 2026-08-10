package store

// v5 introduces the provider-neutral tracker substrate. References are a
// Matter-owned many-to-many relation; provider adapters consume this relation
// but never write provider names or provider states into it.
func v5Statements() []string {
	return []string{
		seedTaxonomy(V5Taxonomy),

		// v1 provisioned these two nouns before they had events. v5 is their
		// first shipped behavior, so replace the provisional shapes before any
		// production event can depend on them.
		`DROP INDEX backlog_state`,
		`ALTER TABLE backlog_entries RENAME TO backlog_entries_v1`,
		`CREATE TABLE backlog_entries (
			id          TEXT NOT NULL PRIMARY KEY,
			repo        TEXT NOT NULL REFERENCES repos(id),
			provenance  TEXT NOT NULL CHECK (provenance IN ('intake','found','deferred')),
			state       TEXT NOT NULL CHECK (state IN ('entered','planned','declined','delegated')),
			title       TEXT NOT NULL,
			detail      TEXT NOT NULL DEFAULT '',
			origin_node TEXT REFERENCES nodes(id),
			matter      TEXT REFERENCES nodes(id),
			outbox      TEXT,
			birth_event TEXT NOT NULL REFERENCES events(id),
			last_event  TEXT NOT NULL REFERENCES events(id),
			CHECK (length(id) = 26),
			CHECK ((state = 'planned') = (matter IS NOT NULL)),
			CHECK ((state = 'delegated') = (outbox IS NOT NULL)),
			CHECK (outbox IS NULL OR (length(outbox) = 26 AND outbox NOT GLOB '*[^0123456789ABCDEFGHJKMNPQRSTVWXYZ]*'))
		) STRICT, WITHOUT ROWID`,
		`INSERT INTO backlog_entries
			(id,repo,provenance,state,title,detail,origin_node,matter,outbox,birth_event,last_event)
		 SELECT id,repo,provenance,state,title,detail,origin_node,matter,NULL,birth_event,last_event
		 FROM backlog_entries_v1`,
		`DROP TABLE backlog_entries_v1`,
		`CREATE INDEX backlog_state ON backlog_entries(repo,state,id)`,
		`CREATE TRIGGER backlog_entries_born_by_event BEFORE INSERT ON backlog_entries WHEN NEW.birth_event <> NEW.last_event BEGIN SELECT RAISE(ABORT, 'backlog_entries: a row is born by exactly one event'); END`,
		`CREATE TRIGGER backlog_entries_advance BEFORE UPDATE ON backlog_entries WHEN NEW.last_event <= OLD.last_event BEGIN SELECT RAISE(ABORT, 'backlog_entries: a row may only advance to a newer event'); END`,
		`CREATE TRIGGER backlog_entries_immutable_birth BEFORE UPDATE ON backlog_entries WHEN NEW.birth_event <> OLD.birth_event BEGIN SELECT RAISE(ABORT, 'backlog_entries: a row birth is immutable'); END`,
		`CREATE TRIGGER backlog_entries_no_delete BEFORE DELETE ON backlog_entries WHEN NOT EXISTS (SELECT 1 FROM store_meta WHERE key = 'rebuilding') BEGIN SELECT RAISE(ABORT, 'backlog_entries is a projection: rows are tombstoned, not deleted (D44); only a rebuild may clear it'); END`,

		`DROP INDEX outbox_idempotency`,
		`ALTER TABLE outbox_entries RENAME TO outbox_entries_v1`,
		`CREATE TABLE outbox_entries (
			id              TEXT NOT NULL PRIMARY KEY,
			repo            TEXT NOT NULL REFERENCES repos(id),
			kind            TEXT NOT NULL CHECK (kind IN ('create','state','comment')),
			state           TEXT NOT NULL CHECK (state IN ('queued','approved','declined','withheld','flushed')),
			subject         TEXT NOT NULL,
			ref             TEXT,
			idempotency_key TEXT NOT NULL,
			payload         TEXT NOT NULL,
			reason          TEXT NOT NULL DEFAULT '',
			attempts        INTEGER NOT NULL DEFAULT 0 CHECK (attempts >= 0),
			birth_event     TEXT NOT NULL REFERENCES events(id),
			last_event      TEXT NOT NULL REFERENCES events(id),
			CHECK (length(id) = 26),
			CHECK (length(subject) = 26),
			CHECK (id NOT GLOB '*[^0123456789ABCDEFGHJKMNPQRSTVWXYZ]*'),
			CHECK (subject NOT GLOB '*[^0123456789ABCDEFGHJKMNPQRSTVWXYZ]*'),
			CHECK (json_valid(payload) AND json_type(payload) = 'object'),
			CHECK ((kind = 'create') = (ref IS NULL)),
			CHECK ((state IN ('declined','withheld')) = (length(reason) > 0))
		) STRICT, WITHOUT ROWID`,
		// No event could populate the provisional v1 table. Refuse an
		// impossible row instead of inventing kind, Repo, or subject facts.
		`CREATE TRIGGER outbox_v1_must_be_empty BEFORE DELETE ON outbox_entries_v1 WHEN EXISTS (SELECT 1 FROM outbox_entries_v1) BEGIN SELECT RAISE(ABORT, 'v5 cannot migrate provisional outbox rows: no event can reconstruct them'); END`,
		`DELETE FROM outbox_entries_v1`,
		`DROP TABLE outbox_entries_v1`,
		`CREATE UNIQUE INDEX outbox_idempotency ON outbox_entries(idempotency_key)`,
		`CREATE INDEX outbox_queue ON outbox_entries(repo,state,birth_event)`,
		`CREATE TRIGGER outbox_entries_born_by_event BEFORE INSERT ON outbox_entries WHEN NEW.birth_event <> NEW.last_event BEGIN SELECT RAISE(ABORT, 'outbox_entries: a row is born by exactly one event'); END`,
		`CREATE TRIGGER outbox_entries_advance BEFORE UPDATE ON outbox_entries WHEN NEW.last_event <= OLD.last_event BEGIN SELECT RAISE(ABORT, 'outbox_entries: a row may only advance to a newer event'); END`,
		`CREATE TRIGGER outbox_entries_immutable_birth BEFORE UPDATE ON outbox_entries WHEN NEW.birth_event <> OLD.birth_event BEGIN SELECT RAISE(ABORT, 'outbox_entries: a row birth is immutable'); END`,
		`CREATE TRIGGER outbox_entries_no_delete BEFORE DELETE ON outbox_entries WHEN NOT EXISTS (SELECT 1 FROM store_meta WHERE key = 'rebuilding') BEGIN SELECT RAISE(ABORT, 'outbox_entries is a projection: rows are tombstoned, not deleted (D44); only a rebuild may clear it'); END`,

		`CREATE TABLE tracker_references (
			matter TEXT NOT NULL REFERENCES nodes(id),
			ref TEXT NOT NULL,
			removed_event TEXT REFERENCES events(id),
			birth_event TEXT NOT NULL REFERENCES events(id),
			last_event TEXT NOT NULL REFERENCES events(id),
			PRIMARY KEY (matter,ref),
			CHECK (length(ref) > 0)
		) STRICT, WITHOUT ROWID`,
		`CREATE INDEX tracker_references_ref ON tracker_references(ref,matter) WHERE removed_event IS NULL`,
		`CREATE TRIGGER tracker_references_advance BEFORE UPDATE ON tracker_references WHEN NEW.last_event <= OLD.last_event BEGIN SELECT RAISE(ABORT, 'tracker_references: a row may only advance to a newer event'); END`,
		`CREATE TRIGGER tracker_references_immutable_birth BEFORE UPDATE ON tracker_references WHEN NEW.birth_event <> OLD.birth_event BEGIN SELECT RAISE(ABORT, 'tracker_references: a row birth is immutable'); END`,
		`CREATE TRIGGER tracker_references_no_delete BEFORE DELETE ON tracker_references WHEN NOT EXISTS (SELECT 1 FROM store_meta WHERE key = 'rebuilding') BEGIN SELECT RAISE(ABORT, 'tracker_references is a projection: rows are tombstoned, not deleted (D44); only a rebuild may clear it'); END`,

		`CREATE TABLE tracker_push_records (
			ref             TEXT NOT NULL PRIMARY KEY,
			disposition     TEXT NOT NULL CHECK (disposition IN ('active','completed','canceled')),
			lease           TEXT NOT NULL,
			outbox          TEXT NOT NULL,
			birth_event     TEXT NOT NULL REFERENCES events(id),
			last_event      TEXT NOT NULL REFERENCES events(id),
			CHECK (length(ref) > 0),
			CHECK (length(lease) > 0),
			CHECK (length(outbox) = 26),
			CHECK (outbox NOT GLOB '*[^0123456789ABCDEFGHJKMNPQRSTVWXYZ]*')
		) STRICT, WITHOUT ROWID`,
		`CREATE TRIGGER tracker_push_records_advance BEFORE UPDATE ON tracker_push_records WHEN NEW.last_event <= OLD.last_event BEGIN SELECT RAISE(ABORT, 'tracker_push_records: a row may only advance to a newer event'); END`,
		`CREATE TRIGGER tracker_push_records_immutable_birth BEFORE UPDATE ON tracker_push_records WHEN NEW.birth_event <> OLD.birth_event BEGIN SELECT RAISE(ABORT, 'tracker_push_records: a row birth is immutable'); END`,
		`CREATE TRIGGER tracker_push_records_no_delete BEFORE DELETE ON tracker_push_records WHEN NOT EXISTS (SELECT 1 FROM store_meta WHERE key = 'rebuilding') BEGIN SELECT RAISE(ABORT, 'tracker_push_records is a projection: rows are tombstoned, not deleted (D44); only a rebuild may clear it'); END`,
	}
}
