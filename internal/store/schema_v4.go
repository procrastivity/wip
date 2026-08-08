package store

// v4 gives roles identity (MODEL §6, D14). A role instance is execution: it
// keys at Clone and binds to the Dispatch that spawned it (D59), so the table
// mirrors `runs` — a projection row born by one event, advanced by later ones,
// cleared only by a rebuild. v1 through v3 are shipped migrations and remain
// frozen.
func v4Statements() []string {
	return []string{
		seedTaxonomy(V4Taxonomy),

		`CREATE TABLE roles (
			id TEXT NOT NULL PRIMARY KEY,
			clone TEXT NOT NULL REFERENCES clones(id),
			dispatch TEXT NOT NULL REFERENCES dispatches(id),
			name TEXT NOT NULL CHECK (name IN ('orchestrator','coordinator','researcher','builder','verifier','warden')),
			state TEXT NOT NULL CHECK (state IN ('open','closed')),
			close_reason TEXT CHECK (close_reason IS NULL OR close_reason IN ('completed','reaped')),
			spawned_at TEXT NOT NULL,
			closed_at TEXT,
			birth_event TEXT NOT NULL REFERENCES events(id),
			last_event TEXT NOT NULL REFERENCES events(id),
			CHECK (length(id)=26),
			CHECK ((state='closed') = (close_reason IS NOT NULL)),
			CHECK ((state='closed') = (closed_at IS NOT NULL))
		) STRICT, WITHOUT ROWID`,

		// One open instance of a role per Dispatch: spawning the same role
		// twice into one bracket is a contention refusal, not a second row.
		`CREATE UNIQUE INDEX roles_open_dispatch_name ON roles(dispatch,name) WHERE state='open'`,

		`CREATE TRIGGER roles_born_by_event BEFORE INSERT ON roles WHEN NEW.birth_event <> NEW.last_event BEGIN SELECT RAISE(ABORT, 'roles: a row is born by exactly one event'); END`,
		`CREATE TRIGGER roles_immutable_birth BEFORE UPDATE ON roles WHEN NEW.birth_event <> OLD.birth_event BEGIN SELECT RAISE(ABORT, 'roles: a row birth is immutable'); END`,
		`CREATE TRIGGER roles_advance BEFORE UPDATE ON roles WHEN NEW.last_event <= OLD.last_event BEGIN SELECT RAISE(ABORT, 'roles: a row may only advance to a newer event'); END`,
		`CREATE TRIGGER roles_no_delete BEFORE DELETE ON roles WHEN NOT EXISTS (SELECT 1 FROM store_meta WHERE key = 'rebuilding') BEGIN SELECT RAISE(ABORT, 'roles is a projection: only a rebuild may clear it'); END`,
	}
}
