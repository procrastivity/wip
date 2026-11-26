package store

import "fmt"

// Transfer identity and receipts are projections, not a second source of truth.
// Their only writer is work.imported; rebuilding clears and refolds both.
func v15Statements() []string {
	out := []string{
		seedTaxonomy(V15Taxonomy),
		`CREATE TABLE transfer_origins (
		 repo TEXT NOT NULL REFERENCES repos(id), origin TEXT NOT NULL,
		 kind TEXT NOT NULL, target TEXT NOT NULL, fingerprint TEXT NOT NULL,
		 state TEXT NOT NULL, birth_event TEXT NOT NULL REFERENCES events(id),
		 PRIMARY KEY(repo,origin), UNIQUE(origin), UNIQUE(target)
		) STRICT, WITHOUT ROWID`,
		`CREATE TABLE transfer_receipts (
		 operation TEXT PRIMARY KEY, repo TEXT NOT NULL REFERENCES repos(id),
		 digest TEXT NOT NULL, receipt TEXT NOT NULL,
		 birth_event TEXT NOT NULL REFERENCES events(id), UNIQUE(repo,digest)
		) STRICT, WITHOUT ROWID`,
	}
	for _, table := range []string{"transfer_origins", "transfer_receipts"} {
		out = append(out, fmt.Sprintf(`CREATE TRIGGER %s_no_delete BEFORE DELETE ON %s
		 WHEN NOT EXISTS (SELECT 1 FROM store_meta WHERE key='rebuilding')
		 BEGIN SELECT RAISE(ABORT, 'transfer projection: only a rebuild may clear it'); END`, table, table))
		out = append(out, fmt.Sprintf(`CREATE TRIGGER %s_no_update BEFORE UPDATE ON %s
		 BEGIN SELECT RAISE(ABORT, 'transfer identity is immutable'); END`, table, table))
	}
	return out
}
