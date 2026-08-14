package store

// v8 makes gate declarations prospective. A declaration snapshots nodes at
// its scale that were already sealed under the previous declaration set. The
// snapshot is configuration, like the declaration itself, and survives a
// rebuild without inventing gate.closed events.
func v8Statements() []string {
	return []string{
		`CREATE TABLE gate_exemptions (
			repo TEXT NOT NULL REFERENCES repos(id),
			gate TEXT NOT NULL,
			node TEXT NOT NULL REFERENCES nodes(id),
			PRIMARY KEY (repo,gate,node),
			FOREIGN KEY (repo,gate) REFERENCES gate_declarations(repo,gate)
		) STRICT, WITHOUT ROWID`,

		`DROP VIEW archived_matters`,
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
			      AND  NOT EXISTS (
				       SELECT 1 FROM gate_exemptions x
				       WHERE x.repo = n.repo AND x.node = n.id AND x.gate = g.gate)
		   )`,
	}
}
