package store

// v5 introduces the provider-neutral tracker substrate. References are a
// Matter-owned many-to-many relation; provider adapters consume this relation
// but never write provider names or provider states into it.
func v5Statements() []string {
	return []string{
		seedTaxonomy(V5Taxonomy),

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
	}
}
