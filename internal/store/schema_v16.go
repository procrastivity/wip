package store

// Public roots retain their own immutable provenance. A local private companion
// additionally retains the private archive fingerprint and companion pointer,
// without inserting a second origin identity or modifying the root provenance.
// Remote roots need no private companion to survive rebuild or re-export.
func v16Statements() []string {
	return []string{
		seedTaxonomy(V16Taxonomy),
		`CREATE TABLE transfer_snapshot_imports (
		 target TEXT PRIMARY KEY REFERENCES transfer_origins(target),
		 fingerprint TEXT NOT NULL, birth_event TEXT NOT NULL REFERENCES events(id)
		) STRICT, WITHOUT ROWID`,
		`CREATE TRIGGER transfer_snapshot_imports_no_delete BEFORE DELETE ON transfer_snapshot_imports
		 WHEN NOT EXISTS (SELECT 1 FROM store_meta WHERE key='rebuilding')
		 BEGIN SELECT RAISE(ABORT, 'transfer projection: only a rebuild may clear it'); END`,
		`CREATE TRIGGER transfer_snapshot_imports_no_update BEFORE UPDATE ON transfer_snapshot_imports
		 BEGIN SELECT RAISE(ABORT, 'transfer identity is immutable'); END`,
	}
}
