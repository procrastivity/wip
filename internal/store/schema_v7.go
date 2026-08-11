package store

// v7 activates the complete provider-neutral outbox lifecycle. The table is
// rebuilt so a queued retryable failure can retain its diagnostic reason.
func v7Statements() []string {
	return []string{
		seedTaxonomy(V7Taxonomy),
		`DROP INDEX outbox_idempotency`,
		`DROP INDEX outbox_queue`,
		`ALTER TABLE outbox_entries RENAME TO outbox_entries_v6`,
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
			CHECK (state NOT IN ('declined','withheld') OR length(reason) > 0)
		) STRICT, WITHOUT ROWID`,
		`INSERT INTO outbox_entries
			(id,repo,kind,state,subject,ref,idempotency_key,payload,reason,attempts,birth_event,last_event)
		 SELECT id,repo,kind,state,subject,ref,idempotency_key,payload,reason,attempts,birth_event,last_event
		 FROM outbox_entries_v6`,
		`DROP TABLE outbox_entries_v6`,
		`CREATE UNIQUE INDEX outbox_idempotency ON outbox_entries(idempotency_key)`,
		`CREATE INDEX outbox_queue ON outbox_entries(repo,state,birth_event)`,
		`CREATE TRIGGER outbox_entries_born_by_event BEFORE INSERT ON outbox_entries WHEN NEW.birth_event <> NEW.last_event BEGIN SELECT RAISE(ABORT, 'outbox_entries: a row is born by exactly one event'); END`,
		`CREATE TRIGGER outbox_entries_advance BEFORE UPDATE ON outbox_entries WHEN NEW.last_event <= OLD.last_event BEGIN SELECT RAISE(ABORT, 'outbox_entries: a row may only advance to a newer event'); END`,
		`CREATE TRIGGER outbox_entries_immutable_birth BEFORE UPDATE ON outbox_entries WHEN NEW.birth_event <> OLD.birth_event BEGIN SELECT RAISE(ABORT, 'outbox_entries: a row birth is immutable'); END`,
		`CREATE TRIGGER outbox_entries_no_delete BEFORE DELETE ON outbox_entries WHEN NOT EXISTS (SELECT 1 FROM store_meta WHERE key = 'rebuilding') BEGIN SELECT RAISE(ABORT, 'outbox_entries is a projection: rows are tombstoned, not deleted (D44); only a rebuild may clear it'); END`,
	}
}
