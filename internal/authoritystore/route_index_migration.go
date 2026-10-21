package authoritystore

import (
	"database/sql"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

var routeIndexMigrationMarker = schemaObject{"schema_migrations", "table", strings.Replace(
	strings.Replace(namedBatchMembershipMigrationMarker.sql, "20)", "20, 21)", 1),
	"name = 'step-9b-named-batch-membership'))",
	"name = 'step-9b-named-batch-membership') OR (version = 21 AND name = 'm6-step-11-normalized-repo-route-index'))", 1)}

var routeIndexSchema = []schemaObject{
	{"repo_route_index", "table", `CREATE TABLE repo_route_index (
  normalized_key TEXT PRIMARY KEY CHECK(length(normalized_key)>0),
  repo_id TEXT NOT NULL UNIQUE REFERENCES repo_memberships(repo_id) ON DELETE RESTRICT
) STRICT, WITHOUT ROWID`},
	{"repo_route_index_identity_immutable", "trigger", `CREATE TRIGGER repo_route_index_identity_immutable BEFORE UPDATE ON repo_route_index
BEGIN SELECT RAISE(ABORT,'immutable normalized Repo route binding'); END`},
	{"repo_route_index_no_delete", "trigger", `CREATE TRIGGER repo_route_index_no_delete BEFORE DELETE ON repo_route_index
BEGIN SELECT RAISE(ABORT,'retained normalized Repo route binding'); END`},
}

func installRouteIndex(db *sql.DB) error {
	tx, err := db.Begin()
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	var version int
	if err = tx.QueryRow(`PRAGMA user_version`).Scan(&version); err != nil || version != 20 {
		return fmt.Errorf("%w: Step 11 route index requires schema 20", ErrInvalidStore)
	}
	for _, object := range routeIndexSchema {
		if _, err = tx.Exec(object.sql); err != nil {
			return fmt.Errorf("%s: %w", object.name, err)
		}
	}
	for _, statement := range []string{
		`ALTER TABLE schema_migrations RENAME TO old_schema_migrations`, routeIndexMigrationMarker.sql,
		`INSERT INTO schema_migrations SELECT * FROM old_schema_migrations`, `DROP TABLE old_schema_migrations`,
		`INSERT INTO schema_migrations VALUES(21,'m6-step-11-normalized-repo-route-index')`, `PRAGMA user_version=21`,
	} {
		if _, err = tx.Exec(statement); err != nil {
			return err
		}
	}
	return tx.Commit()
}

func checkRouteIndexState(db *sql.DB) error {
	rows, err := db.Query(`SELECT normalized_key,repo_id FROM repo_route_index ORDER BY normalized_key`)
	if err != nil {
		return err
	}
	var bindings []struct{ key, repo string }
	for rows.Next() {
		var binding struct{ key, repo string }
		if err = rows.Scan(&binding.key, &binding.repo); err != nil {
			_ = rows.Close()
			return err
		}
		if !validNormalizedRepoKey(binding.key) || !ulid.MatchString(binding.repo) {
			_ = rows.Close()
			return fmt.Errorf("%w: invalid normalized Repo route binding", ErrInvalidStore)
		}
		bindings = append(bindings, binding)
	}
	if err = rows.Err(); err != nil {
		_ = rows.Close()
		return err
	}
	if err = rows.Close(); err != nil {
		return err
	}
	for _, binding := range bindings {
		var member int
		if err = db.QueryRow(`SELECT count(*) FROM repo_memberships WHERE repo_id=?`, binding.repo).Scan(&member); err != nil || member != 1 {
			return fmt.Errorf("%w: route binding lacks unique Repo membership", ErrInvalidStore)
		}
	}
	return nil
}

// UpgradeV20 retains a validated exact-v20 backup before installing the
// authority-owned normalized Repo route index. Ordinary opens never migrate.
func UpgradeV20(root string) error {
	path, err := cleanRoot(root)
	if err != nil {
		return err
	}
	if err = checkRoot(path); err != nil {
		return err
	}
	lock, err := acquire(path)
	if err != nil {
		return err
	}
	defer func() { _ = lock.Close() }()
	db, err := connect(filepath.Join(path, "authority.db"), "rw", false)
	if err != nil {
		return err
	}
	defer func() { _ = db.Close() }()
	var version int
	if err = db.QueryRow(`PRAGMA user_version`).Scan(&version); err != nil || version < 20 || version > 23 {
		return fmt.Errorf("%w: Step 11 upgrade requires schema 20 through 23", ErrInvalidStore)
	}
	if version == 20 {
		if err = checkSchemaVersion(db, 20); err != nil {
			return fmt.Errorf("%w: v20 validation: %v", ErrInvalidStore, err)
		}
		if err = checkNamedBatchMembershipState(db); err != nil {
			return fmt.Errorf("%w: v20 named Batch state: %v", ErrInvalidStore, err)
		}
		if err = checkBlobFiles(db, filepath.Join(path, "blobs")); err != nil {
			return fmt.Errorf("%w: v20 blobs: %v", ErrInvalidStore, err)
		}
		backup := filepath.Join(path, "authority-v20.backup.db")
		if _, statErr := os.Lstat(backup); !errors.Is(statErr, os.ErrNotExist) {
			if statErr != nil {
				return statErr
			}
			return fmt.Errorf("%w: v20 backup already exists; inspect before retry", ErrInvalidStore)
		}
		file, createErr := os.OpenFile(backup, os.O_CREATE|os.O_EXCL|os.O_RDWR, 0o600)
		if createErr != nil {
			return createErr
		}
		if err = file.Close(); err != nil {
			return err
		}
		if _, err = db.Exec(`VACUUM INTO ?`, backup); err != nil {
			return err
		}
		if err = regularFile(backup); err != nil {
			return err
		}
		retained, openErr := connect(backup, "rw", true)
		if openErr != nil {
			return openErr
		}
		if err = errors.Join(checkSchemaVersion(retained, 20), retained.Close()); err != nil {
			return err
		}
		if err = sameStoreContents(db, backup); err != nil {
			return err
		}
		if err = syncFileAndDirectory(backup, path); err != nil {
			return err
		}
	} else if err = checkSchemaVersion(db, version); err != nil {
		return fmt.Errorf("%w: interrupted upgrade validation: %v", ErrInvalidStore, err)
	}
	if version == 20 {
		if err = installRouteIndex(db); err != nil {
			return err
		}
		version = 21
	}
	if version == 21 {
		if err = checkRouteIndexState(db); err != nil {
			return fmt.Errorf("%w: route-index state: %v", ErrInvalidStore, err)
		}
		if err = installD115(db); err != nil {
			return err
		}
		version = 22
	}
	if version == 22 {
		if err = checkD115State(db); err != nil {
			return fmt.Errorf("%w: D115 state: %v", ErrInvalidStore, err)
		}
		if err = installEnvironmentAttach(db); err != nil {
			return err
		}
	}
	if err = checkSchema(db); err != nil {
		return err
	}
	if err = checkRouteIndexState(db); err != nil {
		return err
	}
	return checkBlobFiles(db, filepath.Join(path, "blobs"))
}

func validNormalizedRepoKey(key string) bool {
	if key == "" || strings.TrimSpace(key) != key || strings.ContainsAny(key, "*?#") ||
		strings.Contains(key, "://") || strings.HasPrefix(key, "/") || strings.HasSuffix(key, "/") ||
		strings.HasSuffix(key, ".git") || !strings.Contains(key, "/") {
		return false
	}
	for _, segment := range strings.Split(key, "/") {
		if segment == "" || segment == "." || segment == ".." {
			return false
		}
	}
	return true
}
