package wipdjournal

import (
	"bytes"
	"context"
	"database/sql"

	"github.com/procrastivity/wip/internal/wipdwire"
)

// SubmissionPayload reconstructs only the retained transport version. An
// ambiguous error or a lost peer capability never permits a v2-to-v1 retry.
func (entry Entry) SubmissionPayload() (any, error) {
	switch entry.SubmissionSchema {
	case "wipd.command-submit/1":
		if entry.DetachedProof != nil || entry.Command.Request.Operation.Name == "gate.exemption.repair" {
			return nil, ErrInvalidCommand
		}
		return wipdwire.CommandSubmit{
			Schema:           entry.SubmissionSchema,
			CanonicalCommand: bytes.Clone(entry.CanonicalBytes), RequestHash: entry.RequestHash,
		}, nil
	case wipdwire.CommandSubmitV2Feature:
		if entry.DetachedProof != nil && (len(entry.DetachedProof) == 0 || len(entry.DetachedProof) > 1<<20) {
			return nil, ErrInvalidCommand
		}
		return wipdwire.CommandSubmitV2{
			Schema:           entry.SubmissionSchema,
			CanonicalCommand: bytes.Clone(entry.CanonicalBytes), RequestHash: entry.RequestHash,
			DetachedProof: bytes.Clone(entry.DetachedProof),
		}, nil
	default:
		return nil, ErrInvalidCommand
	}
}

func upgradeSchemaV9(db *sql.DB, identity Identity) error {
	var version int
	var repoID, domainID, environmentID, marker string
	var epoch uint64
	if err := db.QueryRow(`PRAGMA user_version`).Scan(&version); err != nil || version != 9 {
		return ErrInvalidJournal
	}
	if err := db.QueryRow(`SELECT repo_id,domain_id,authority_epoch,environment_id FROM environment_state WHERE singleton=1`).
		Scan(&repoID, &domainID, &epoch, &environmentID); err != nil ||
		repoID != identity.RepoID || domainID != identity.DomainID || epoch != identity.AuthorityEpoch || environmentID != identity.EnvironmentID {
		return ErrInvalidIdentity
	}
	if err := db.QueryRow(`SELECT name FROM schema_migrations WHERE version=9`).Scan(&marker); err != nil || marker != "environment-resolved-claim-acquire-barrier" {
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
		`ALTER TABLE commands ADD COLUMN submission_schema TEXT NOT NULL DEFAULT 'wipd.command-submit/1' CHECK(submission_schema IN ('wipd.command-submit/1','wipd.command-submit/2'))`,
		`ALTER TABLE commands ADD COLUMN detached_proof BLOB CHECK(detached_proof IS NULL OR (submission_schema='wipd.command-submit/2' AND length(detached_proof) BETWEEN 1 AND 1048576))`,
		`DROP TRIGGER command_identity_immutable`,
		`CREATE TRIGGER command_identity_immutable BEFORE UPDATE OF command_id,environment_sequence,journal_position,request_hash,canonical_bytes,delivery,submission_schema,detached_proof ON commands
		BEGIN SELECT RAISE(ABORT,'immutable command identity and detached proof'); END`,
		`DROP TABLE schema_migrations`,
		`CREATE TABLE schema_migrations(version INTEGER PRIMARY KEY CHECK(version=10),name TEXT NOT NULL CHECK(name='environment-detached-command-proof')) STRICT`,
		`INSERT INTO schema_migrations VALUES(10,'environment-detached-command-proof')`,
		`PRAGMA user_version=10`,
	} {
		if _, err = tx.Exec(statement); err != nil {
			return err
		}
	}
	return tx.Commit()
}
