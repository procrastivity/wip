package wipdjournal

import (
	"bytes"
	"database/sql"
	"errors"
	"path/filepath"
	"testing"

	"github.com/procrastivity/wip/internal/operation"
	"github.com/procrastivity/wip/internal/wipdwire"
)

func TestDetachedProofRetrySurvivesRestartWithoutChangingIdentity(t *testing.T) {
	root := filepath.Join(t.TempDir(), "journal")
	journal, err := Open(root, testIdentity)
	if err != nil {
		t.Fatal(err)
	}
	input := commandInput(testCommandPrefix+"46", "detached", "Detached retry")
	command := operation.Command{
		ID: input.ID, AuthorityDomainID: testIdentity.DomainID, ExpectedAuthorityEpoch: testIdentity.AuthorityEpoch,
		EnvironmentID: testIdentity.EnvironmentID, EnvironmentSequence: 1, ActedAt: "2026-10-02T00:00:00Z",
		CorrelationCommandID: input.ID, Request: input.Request,
	}
	canonical, _ := command.CanonicalBytes()
	hash, _ := command.RequestHash()
	proof := []byte{0xff, 0x00, 0x80, 0xc0, 0x41, 0x73}
	wantProof := bytes.Clone(proof)
	entry, err := journal.PrepareCanonicalSubmission(command, wipdwire.CommandSubmitV2Feature, proof)
	if err != nil || !entry.Created || !bytes.Equal(entry.DetachedProof, proof) ||
		!bytes.Equal(entry.CanonicalBytes, canonical) || entry.RequestHash != hash {
		t.Fatalf("detached preparation: %+v %v", entry, err)
	}
	proof[0] = 0
	entry.DetachedProof[1] = 0xff
	if err = journal.Close(); err != nil {
		t.Fatal(err)
	}
	journal, err = Open(root, testIdentity)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = journal.Close() })
	for _, supplied := range [][]byte{nil, wantProof} {
		retry, err := journal.PrepareCanonicalSubmission(command, wipdwire.CommandSubmitV2Feature, supplied)
		if err != nil || retry.Created || retry.Command.ID != command.ID || retry.EnvironmentSeq != 1 ||
			retry.JournalPosition != 1 || retry.State != StatePreAdmission || retry.RequestHash != hash ||
			!bytes.Equal(retry.CanonicalBytes, canonical) || !bytes.Equal(retry.DetachedProof, wantProof) {
			t.Fatalf("omitted/equal proof retry: %+v %v", retry, err)
		}
		payload, err := retry.SubmissionPayload()
		if err != nil {
			t.Fatal(err)
		}
		encoded, _ := wipdwire.EncodeCanonical(payload)
		decoded, v2, err := wipdwire.DecodeCommandSubmit(encoded)
		if err != nil || !v2 || !bytes.Equal(decoded.DetachedProof, wantProof) ||
			!bytes.Equal(decoded.CanonicalCommand, canonical) || decoded.RequestHash != hash {
			t.Fatalf("reconstructed transport: %+v %t %v", decoded, v2, err)
		}
	}
	if _, err = journal.PrepareCanonicalSubmission(command, wipdwire.CommandSubmitV2Feature, proof); !errors.Is(err, ErrCommandIDConflict) {
		t.Fatalf("substituted proof = %v", err)
	}
	if _, err = journal.PrepareCanonicalCommand(command); !errors.Is(err, ErrCommandIDConflict) {
		t.Fatalf("v2-to-v1 retry = %v", err)
	}
	for _, statement := range []string{
		`UPDATE commands SET detached_proof=x'01'`,
		`UPDATE commands SET submission_schema='wipd.command-submit/1'`,
	} {
		if _, err = journal.db.Exec(statement); err == nil {
			t.Fatalf("mutable retained transport: %s", statement)
		}
	}
	var blobs, sequence int
	if err = journal.db.QueryRow(`SELECT count(*) FROM staged_blobs`).Scan(&blobs); err != nil || blobs != 0 {
		t.Fatalf("detached proof entered blobs: %d %v", blobs, err)
	}
	if err = journal.db.QueryRow(`SELECT next_environment_sequence FROM environment_state`).Scan(&sequence); err != nil || sequence != 2 {
		t.Fatalf("retry consumed sequence: %d %v", sequence, err)
	}
	other, err := Open(filepath.Join(t.TempDir(), "other"), testIdentity)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = other.Close() })
	different, err := other.PrepareCanonicalSubmission(command, wipdwire.CommandSubmitV2Feature, []byte{0x01, 0x23})
	if err != nil || !bytes.Equal(different.CanonicalBytes, canonical) || different.RequestHash != hash ||
		different.Command.ID != command.ID || len(different.Command.Request.Blobs) != 0 || bytes.Contains(canonical, wantProof) {
		t.Fatalf("proof altered identity/hash/blobs: %+v %v", different, err)
	}
}

func TestDetachedProofMigrationPreservesV1Entries(t *testing.T) {
	root := filepath.Join(t.TempDir(), "journal")
	journal, err := Open(root, testIdentity)
	if err != nil {
		t.Fatal(err)
	}
	before, err := journal.PrepareCommand(commandInput(testCommandPrefix+"47", "legacy", "Legacy v1"))
	if err != nil {
		t.Fatal(err)
	}
	if err = downgradeDetachedProofTestDBToV9(journal.db); err != nil {
		t.Fatal(err)
	}
	_ = journal.Close()
	journal, err = Open(root, testIdentity)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = journal.Close() })
	after, err := journal.Get(before.Command.ID)
	if err != nil || !sameEntryIdentity(before, after) || after.SubmissionSchema != "wipd.command-submit/1" || after.DetachedProof != nil {
		t.Fatalf("v9 migration changed v1 identity: %+v %v", after, err)
	}
	payload, err := after.SubmissionPayload()
	if err != nil {
		t.Fatal(err)
	}
	encoded, _ := wipdwire.EncodeCanonical(payload)
	if _, err = wipdwire.DecodeCanonicalMap(encoded, "schema", "canonical_command", "request_hash", "deadline"); err != nil {
		t.Fatalf("migrated v1 gained fields: %v", err)
	}
}

func downgradeDetachedProofTestDBToV9(db *sql.DB) error {
	for _, statement := range []string{
		`DROP TRIGGER command_identity_immutable`,
		`ALTER TABLE commands DROP COLUMN detached_proof`,
		`ALTER TABLE commands DROP COLUMN submission_schema`,
		`CREATE TRIGGER command_identity_immutable BEFORE UPDATE OF command_id,environment_sequence,journal_position,request_hash,canonical_bytes,delivery ON commands BEGIN SELECT RAISE(ABORT,'immutable command identity'); END`,
		`DROP TABLE schema_migrations`,
		`CREATE TABLE schema_migrations(version INTEGER PRIMARY KEY CHECK(version=9),name TEXT NOT NULL CHECK(name='environment-resolved-claim-acquire-barrier')) STRICT`,
		`INSERT INTO schema_migrations VALUES(9,'environment-resolved-claim-acquire-barrier')`,
		`PRAGMA user_version=9`,
	} {
		if _, err := db.Exec(statement); err != nil {
			return err
		}
	}
	return nil
}
