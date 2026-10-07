package wipdjournal

import (
	"bytes"
	"context"
	"database/sql"
	"errors"
	"fmt"
	"path/filepath"
	"testing"

	"github.com/procrastivity/wip/internal/operation"
	"github.com/procrastivity/wip/internal/wipdwire"
)

func TestReleaseInstalledAnchorRetainsExactInstallEnd(t *testing.T) {
	for _, acquired := range []bool{false, true} {
		t.Run(fmt.Sprint("acquired=", acquired), func(t *testing.T) {
			journal, id, hash, install := prepareReleaseAnchorFixture(t, acquired)
			root, identity := filepath.Dir(journal.lock.Name()), journal.Identity()
			defer func() { _ = journal.Close() }()
			if _, err := journal.ReleaseInstalledAnchor(context.Background(), id, hash); !errors.Is(err, ErrNotFound) {
				t.Fatalf("unreturned attempt supplied eligible evidence: %v", err)
			}
			before := mustInstallSnapshot(t, journal)
			if _, err := journal.db.Exec(`CREATE TRIGGER fail_release_install BEFORE UPDATE ON environment_install BEGIN SELECT RAISE(ABORT,'injected install failure'); END`); err != nil {
				t.Fatal(err)
			}
			if _, err := install(); err == nil {
				t.Fatal("injected transaction failure did not fail")
			}
			var state string
			var anchor sql.NullInt64
			table := releaseAnchorTestTable(acquired)
			if err := journal.db.QueryRow(`SELECT state,installed_event_count FROM `+table+` WHERE command_id=?`, id).Scan(&state, &anchor); err != nil || state != "attempt-prepared" || anchor.Valid {
				t.Fatalf("failed transaction retained outcome/anchor: %s %+v %v", state, anchor, err)
			}
			if after := mustInstallSnapshot(t, journal); after.Revision != before.Revision || !sameTransferAnchor(before.Anchor, after.Anchor) {
				t.Fatal("failed transaction advanced installed state")
			}
			if _, err := journal.db.Exec(`DROP TRIGGER fail_release_install`); err != nil {
				t.Fatal(err)
			}
			installed, err := install()
			if err != nil {
				t.Fatal(err)
			}
			want := installed.Anchor
			if want.EventID == nil || *want.EventID != testCommandPrefix+"92" {
				t.Fatalf("fixture must include an event after the accepted release: %+v", want)
			}
			for _, statement := range []string{
				`UPDATE ` + table + ` SET installed_prefix_digest=installed_prefix_digest`,
				`UPDATE ` + table + ` SET canonical_receipt=canonical_receipt`,
			} {
				if _, err = journal.db.Exec(statement); err == nil {
					t.Fatalf("returned evidence is mutable: %s", statement)
				}
			}
			for phase := 0; phase < 3; phase++ {
				got, lookupErr := journal.ReleaseInstalledAnchor(context.Background(), id, hash)
				if lookupErr != nil || !sameTransferAnchor(got, want) {
					t.Fatalf("phase %d retained anchor=%+v want=%+v err=%v", phase, got, want, lookupErr)
				}
				if _, err = journal.ReleaseInstalledAnchor(context.Background(), testCommandPrefix+"99", hash); !errors.Is(err, ErrNotFound) {
					t.Fatalf("wrong command ID: %v", err)
				}
				if _, err = journal.ReleaseInstalledAnchor(context.Background(), id, emptyTransferAnchor().Digest); !errors.Is(err, ErrCommandIDConflict) {
					t.Fatalf("wrong request hash: %v", err)
				}
				switch phase {
				case 0:
					advanceReleaseAnchorFixture(t, journal)
				case 1:
					if err = journal.Close(); err != nil {
						t.Fatal(err)
					}
					journal, err = Open(root, identity)
					if err != nil {
						t.Fatal(err)
					}
				}
			}
		})
	}
}

func TestReleaseInstalledAnchorRejectsTamperedEvidence(t *testing.T) {
	for _, acquired := range []bool{false, true} {
		for _, mutation := range []string{"digest", "event-id", "count", "earlier-prefix", "event", "release-event", "receipt", "attempt"} {
			t.Run(fmt.Sprintf("acquired=%v/%s", acquired, mutation), func(t *testing.T) {
				journal, id, hash, install := prepareReleaseAnchorFixture(t, acquired)
				defer func() { _ = journal.Close() }()
				before := mustInstallSnapshot(t, journal).Anchor
				if _, err := install(); err != nil {
					t.Fatal(err)
				}
				table := releaseAnchorTestTable(acquired)
				// Bypass immutability to simulate on-disk corruption. Lookup must
				// validate independently, even without reopening the database.
				for _, trigger := range []string{stringsForReleaseTrigger(acquired), "installed_event_no_update"} {
					if _, err := journal.db.Exec(`DROP TRIGGER ` + trigger); err != nil {
						t.Fatal(err)
					}
				}
				var err error
				switch mutation {
				case "digest":
					_, err = journal.db.Exec(`UPDATE `+table+` SET installed_prefix_digest=?`, emptyTransferAnchor().Digest)
				case "event-id":
					_, err = journal.db.Exec(`UPDATE `+table+` SET installed_event_id=?`, testCommandPrefix+"90")
				case "count":
					_, err = journal.db.Exec(`UPDATE ` + table + ` SET installed_event_count=installed_event_count+1`)
				case "earlier-prefix":
					_, err = journal.db.Exec(`UPDATE `+table+` SET installed_event_count=?,installed_event_id=?,installed_prefix_digest=?`, before.EventCount, before.EventID, before.Digest)
				case "event":
					var raw []byte
					err = journal.db.QueryRow(`SELECT record FROM installed_events WHERE event_id=?`, testCommandPrefix+"92").Scan(&raw)
					if err == nil {
						raw = bytes.Replace(raw, []byte("Installed matter"), []byte("Corrupted matter"), 1)
						_, err = journal.db.Exec(`UPDATE installed_events SET record=? WHERE event_id=?`, raw, testCommandPrefix+"92")
					}
				case "release-event":
					// Recompute a valid chain after substituting the accepted event's
					// request hash. Prefix validity alone must not establish identity.
					records, readErr := journal.EventRecords(context.Background())
					if readErr != nil {
						t.Fatal(readErr)
					}
					for index := range records {
						if records[index].EventID == testCommandPrefix+"91" {
							records[index].Record = bytes.Replace(records[index].Record, []byte(hash), []byte(emptyTransferAnchor().Digest), 1)
							_, err = journal.db.Exec(`UPDATE installed_events SET record=? WHERE event_id=?`, records[index].Record, records[index].EventID)
						}
					}
					if err == nil {
						transfer := verifiedInstallTestTransfer(t, emptyTransferAnchor(), records)
						_, err = journal.db.Exec(`UPDATE `+table+` SET installed_prefix_digest=?`, transfer.End().Digest)
					}
				case "receipt":
					if acquired {
						_, err = journal.db.Exec(`DROP TRIGGER claim_journal_release_outcome_immutable`)
					}
					if err == nil {
						_, err = journal.db.Exec(`UPDATE ` + table + ` SET canonical_receipt=x'a0'`)
					}
				case "attempt":
					prefix := "birth_release"
					if acquired {
						prefix = "claim_journal_release"
					}
					_, err = journal.db.Exec(`DROP TRIGGER ` + prefix + `_identity_immutable`)
					if err == nil {
						_, err = journal.db.Exec(`UPDATE ` + table + ` SET canonical_bytes=x'a0'`)
					}
				}
				if err != nil {
					t.Fatal(err)
				}
				if _, err = journal.ReleaseInstalledAnchor(context.Background(), id, hash); !errors.Is(err, ErrInvalidJournal) {
					t.Fatalf("tampered %s supplied eligible evidence: %v", mutation, err)
				}
			})
		}
	}
}

func TestReleaseAnchorMigrationLeavesLegacyReturnedEvidenceMissing(t *testing.T) {
	for _, acquired := range []bool{false, true} {
		t.Run(fmt.Sprint("acquired=", acquired), func(t *testing.T) {
			journal, id, hash, install := prepareReleaseAnchorFixture(t, acquired)
			root, identity := filepath.Dir(journal.lock.Name()), journal.Identity()
			if _, err := install(); err != nil {
				t.Fatal(err)
			}
			advanceReleaseAnchorFixture(t, journal)
			var receipt []byte
			if err := journal.db.QueryRow(`SELECT canonical_receipt FROM `+releaseAnchorTestTable(acquired)+` WHERE command_id=?`, id).Scan(&receipt); err != nil {
				t.Fatal(err)
			}
			if err := downgradeReleaseAnchorTestDBToV10(journal.db); err != nil {
				t.Fatal(err)
			}
			_ = journal.Close()
			journal, err := Open(root, identity)
			if err != nil {
				t.Fatalf("legacy returned row must remain readable: %v", err)
			}
			defer func() { _ = journal.Close() }()
			var got []byte
			if acquired {
				attempt, readErr := journal.ClaimJournalReleaseAttempt(id)
				got, err = attempt.Receipt, readErr
			} else {
				attempt, readErr := journal.BirthReleaseAttempt(id)
				got, err = attempt.Receipt, readErr
			}
			if err != nil || !bytes.Equal(got, receipt) {
				t.Fatalf("migration changed legacy receipt: %v", err)
			}
			if _, err = journal.ReleaseInstalledAnchor(context.Background(), id, hash); !errors.Is(err, ErrNotFound) {
				t.Fatalf("legacy row acquired invented anchor evidence: %v", err)
			}
		})
	}
}

func releaseAnchorTestTable(acquired bool) string {
	if acquired {
		return "claim_journal_release_attempts"
	}
	return "birth_release_attempts"
}

func stringsForReleaseTrigger(acquired bool) string {
	if acquired {
		return "claim_journal_release_state_transition"
	}
	return "birth_release_state_transition"
}

func prepareReleaseAnchorFixture(t *testing.T, acquired bool) (*Journal, string, string, func() (InstallSnapshot, error)) {
	t.Helper()
	const id = testCommandPrefix + "88"
	var journal *Journal
	var hash, claim, digest string
	var sequence uint64
	var dispatch any
	var install func([]byte, VerifiedTransfer) (InstallSnapshot, error)
	if acquired {
		j, binding, acquisition := openClaimJournalCloseTestFixture(t, 30)
		journal = j
		binding.State = "sealed"
		if _, err := journal.PinClaimJournalIdentity(context.Background(), binding); err != nil {
			t.Fatal(err)
		}
		barrier, _, err := journal.ClaimJournal(binding)
		if err != nil {
			t.Fatal(err)
		}
		attempt, err := journal.PrepareClaimJournalRelease(id, binding, barrier, acquisition.CloneID, acquisition.WorktreeID, "human")
		if err != nil {
			t.Fatal(err)
		}
		hash, sequence, claim, digest, dispatch = attempt.RequestHash, attempt.EnvironmentSeq, binding.ClaimID, barrier.Digest, binding.DispatchID
		install = func(receipt []byte, transfer VerifiedTransfer) (InstallSnapshot, error) {
			return journal.InstallClaimJournalRelease(context.Background(), mustInstallSnapshot(t, journal).Expectation(), attempt, receipt, transfer)
		}
	} else {
		journal = openInstallTestJournal(t, filepath.Join(privateTempDir(t), "journal"))
		claim = testCommandPrefix + "70"
		barrier := wipdwire.JournalBarrier{
			Schema: "wipd.journal-barrier/1", Journal: claim,
			Claim: wipdwire.ClaimRef{ID: claim, Epoch: 1}, Count: 1, Last: 1, Receipts: 1, Sealed: true, Digest: emptyTransferAnchor().Digest,
		}
		attempt, err := journal.PrepareBirthRelease(id, barrier, "human")
		if err != nil {
			t.Fatal(err)
		}
		hash, sequence, digest = attempt.RequestHash, attempt.EnvironmentSeq, barrier.Digest
		install = func(receipt []byte, transfer VerifiedTransfer) (InstallSnapshot, error) {
			return journal.InstallBirthRelease(context.Background(), mustInstallSnapshot(t, journal).Expectation(), attempt, receipt, transfer)
		}
	}
	encode := func(value any) []byte {
		raw, err := wipdwire.EncodeCanonical(value)
		if err != nil {
			t.Fatal(err)
		}
		return raw
	}
	output := map[string]any{"claim_id": claim, "claim_epoch": uint64(1), "dispatch_id": dispatch, "barrier_digest": digest}
	record := func(eventID, kind, subject string, payload any) wipdwire.EventRecord {
		return wipdwire.EventRecord{EventID: eventID, Record: encode(map[string]any{
			"schema": "wipd.event/1", "event_id": eventID, "domain_id": testDomainID,
			"command_id": id, "request_hash": hash, "environment": map[string]any{"id": testEnvironmentID, "sequence": sequence},
			"acted_at": "2026-09-28T00:00:00Z", "occurred_at": "2026-09-28T00:00:01Z",
			"kind": kind, "subject_id": subject, "repo_id": testRepoID, "payload": payload,
		})}
	}
	records := []wipdwire.EventRecord{}
	if acquired {
		records = append(records, record(testCommandPrefix+"90", "dispatch.closed", dispatch.(string), map[string]any{"dispatch_id": dispatch, "claim_id": claim, "claim_epoch": uint64(1)}))
	}
	records = append(records, record(testCommandPrefix+"91", "claim.released", claim, output))
	first := records[0].EventID
	count := uint64(len(records))
	records = append(records, wipdwire.EventRecord{EventID: testCommandPrefix + "92", Record: installTestEventRecord(t, testCommandPrefix+"92", testCommandPrefix+"87", emptyTransferAnchor().Digest, 9)})
	transfer := verifiedInstallTestTransfer(t, mustInstallSnapshot(t, journal).Anchor, records)
	receipt := encode(map[string]any{
		"schema": "wipd.terminal-receipt/1", "domain_id": testDomainID, "authority_epoch": testIdentity.AuthorityEpoch,
		"identity_schema": "wipd.command/1", "command_id": id, "request_hash": hash,
		"operation":       map[string]any{"name": "claim.release", "version": uint64(1)},
		"environment":     map[string]any{"id": testEnvironmentID, "sequence": sequence},
		"result":          map[string]any{"code": string(operation.ResultSucceeded), "output": encode(output), "problem_code": nil},
		"accepted_events": map[string]any{"first_event_id": first, "last_event_id": testCommandPrefix + "91", "event_count": count},
	})
	return journal, id, hash, func() (InstallSnapshot, error) { return install(receipt, transfer) }
}

func advanceReleaseAnchorFixture(t *testing.T, journal *Journal) {
	t.Helper()
	snapshot := mustInstallSnapshot(t, journal)
	record := wipdwire.EventRecord{EventID: testCommandPrefix + "93", Record: installTestEventRecord(t, testCommandPrefix+"93", testCommandPrefix+"86", emptyTransferAnchor().Digest, 10)}
	if _, err := journal.InstallPull(context.Background(), snapshot.Expectation(), verifiedInstallTestTransfer(t, snapshot.Anchor, []wipdwire.EventRecord{record})); err != nil {
		t.Fatal(err)
	}
}

func downgradeReleaseAnchorTestDBToV10(db *sql.DB) error {
	for _, prefix := range []string{"birth_release", "claim_journal_release"} {
		for _, statement := range []string{
			`DROP TRIGGER ` + prefix + `_state_transition`,
			`ALTER TABLE ` + prefix + `_attempts DROP COLUMN installed_prefix_digest`,
			`ALTER TABLE ` + prefix + `_attempts DROP COLUMN installed_event_id`,
			`ALTER TABLE ` + prefix + `_attempts DROP COLUMN installed_event_count`,
			`CREATE TRIGGER ` + prefix + `_state_transition BEFORE UPDATE OF state ON ` + prefix + `_attempts WHEN NOT (OLD.state='attempt-prepared' AND NEW.state='returned') BEGIN SELECT RAISE(ABORT,'invalid release transition'); END`,
		} {
			if _, err := db.Exec(statement); err != nil {
				return err
			}
		}
	}
	for _, statement := range []string{
		`DROP TABLE schema_migrations`,
		`CREATE TABLE schema_migrations(version INTEGER PRIMARY KEY CHECK(version=10),name TEXT NOT NULL CHECK(name='environment-detached-command-proof')) STRICT`,
		`INSERT INTO schema_migrations VALUES(10,'environment-detached-command-proof')`,
		`PRAGMA user_version=10`,
	} {
		if _, err := db.Exec(statement); err != nil {
			return err
		}
	}
	return nil
}
