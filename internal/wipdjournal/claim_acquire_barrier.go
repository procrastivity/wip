package wipdjournal

import (
	"context"
	"database/sql"
)

func upgradeSchemaV8(db *sql.DB, identity Identity) error {
	var version int
	var repoID, domainID, environmentID, marker string
	var epoch int64
	if err := db.QueryRow(`PRAGMA user_version`).Scan(&version); err != nil || version != 8 {
		return ErrInvalidJournal
	}
	if err := db.QueryRow(`SELECT repo_id,domain_id,authority_epoch,environment_id FROM environment_state WHERE singleton=1`).
		Scan(&repoID, &domainID, &epoch, &environmentID); err != nil ||
		repoID != identity.RepoID || domainID != identity.DomainID || epoch != int64(identity.AuthorityEpoch) || environmentID != identity.EnvironmentID {
		return ErrInvalidIdentity
	}
	if err := db.QueryRow(`SELECT name FROM schema_migrations WHERE version=8`).Scan(&marker); err != nil || marker != "environment-claim-journal-close" {
		return ErrInvalidJournal
	}
	if err := checkSchemaObjects(db); err != nil {
		return ErrInvalidJournal
	}
	tx, err := db.BeginTx(context.Background(), nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	for _, statement := range []string{
		`DROP TRIGGER claim_acquire_before_insert`,
		`CREATE TRIGGER claim_acquire_before_insert BEFORE INSERT ON claim_acquire_attempts
			WHEN EXISTS(SELECT 1 FROM commands c LEFT JOIN authority_command_outcomes o USING(command_id)
				WHERE c.state!='returned' AND (c.delivery!='authority' OR o.command_id IS NULL)) OR
				EXISTS(SELECT 1 FROM birth_release_attempts WHERE state='attempt-prepared') OR
				EXISTS(SELECT 1 FROM claim_acquire_attempts WHERE state='attempt-prepared')
			BEGIN SELECT RAISE(ABORT,'Environment work is unresolved'); END`,
		`DROP TABLE schema_migrations`,
		`CREATE TABLE schema_migrations(version INTEGER PRIMARY KEY CHECK(version=9),name TEXT NOT NULL CHECK(name='environment-resolved-claim-acquire-barrier')) STRICT`,
		`INSERT INTO schema_migrations(version,name) VALUES(9,'environment-resolved-claim-acquire-barrier')`,
		`PRAGMA user_version=9`,
	} {
		if _, err = tx.Exec(statement); err != nil {
			return err
		}
	}
	return tx.Commit()
}
