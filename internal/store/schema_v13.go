package store

// v13 closes the store's one documented persistence exception. Config, gate
// declarations and gate exemptions were mutable primary rows written outside
// the log, so Rebuild left them alone and D36's "the store is the source of
// truth" carried an asterisk. They fold from events from here on, which makes
// the log plus its referenced blobs the whole durable truth.
//
// Two things and no more: the three new event types, and the delete guard that
// makes each table a projection the substrate protects. The tables keep their
// v1/v8 shapes on purpose — no birth_event or last_event column is added.
// `run_matters` is the precedent: a projection keyed by a natural key, with no
// identity and no history of its own, carries the no-delete guard alone
// (projectionGuards' `eventLinked`/`born` split exists for exactly this). It is
// also the only honest option before the synthetic-history migration: a NOT NULL
// `REFERENCES events(id)` column has nothing to hold for the rows a store
// already carries.
func v13Statements() []string {
	return []string{
		seedTaxonomy(V13Taxonomy),

		`CREATE TRIGGER config_no_delete BEFORE DELETE ON config
		 WHEN NOT EXISTS (SELECT 1 FROM store_meta WHERE key = 'rebuilding')
		 BEGIN SELECT RAISE(ABORT, 'config is a projection: only a rebuild may clear it'); END`,
		`CREATE TRIGGER gate_declarations_no_delete BEFORE DELETE ON gate_declarations
		 WHEN NOT EXISTS (SELECT 1 FROM store_meta WHERE key = 'rebuilding')
		 BEGIN SELECT RAISE(ABORT, 'gate_declarations is a projection: only a rebuild may clear it'); END`,
		`CREATE TRIGGER gate_exemptions_no_delete BEFORE DELETE ON gate_exemptions
		 WHEN NOT EXISTS (SELECT 1 FROM store_meta WHERE key = 'rebuilding')
		 BEGIN SELECT RAISE(ABORT, 'gate_exemptions is a projection: only a rebuild may clear it'); END`,
	}
}
