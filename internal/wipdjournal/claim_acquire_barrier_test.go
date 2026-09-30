package wipdjournal

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/procrastivity/wip/internal/operation"
)

func TestOpenMigratesV8ResolvedClaimAcquireBarrier(t *testing.T) {
	root := filepath.Join(t.TempDir(), "journal")
	journal := openInstallTestJournal(t, root)
	identity := journal.Identity()
	if err := journal.Close(); err != nil {
		t.Fatal(err)
	}
	db, err := connect(filepath.Join(root, databaseName))
	if err != nil {
		t.Fatal(err)
	}
	for _, statement := range []string{
		`DROP TRIGGER claim_acquire_before_insert`,
		`CREATE TRIGGER claim_acquire_before_insert BEFORE INSERT ON claim_acquire_attempts
			WHEN EXISTS(SELECT 1 FROM commands WHERE state!='returned') OR
				EXISTS(SELECT 1 FROM birth_release_attempts WHERE state='attempt-prepared') OR
				EXISTS(SELECT 1 FROM claim_acquire_attempts WHERE state='attempt-prepared')
			BEGIN SELECT RAISE(ABORT,'Environment work is unresolved'); END`,
		`DROP TABLE schema_migrations`,
		`CREATE TABLE schema_migrations(version INTEGER PRIMARY KEY CHECK(version=8),name TEXT NOT NULL CHECK(name='environment-claim-journal-close')) STRICT`,
		`INSERT INTO schema_migrations(version,name) VALUES(8,'environment-claim-journal-close')`,
		`PRAGMA user_version=8`,
	} {
		if _, err = db.Exec(statement); err != nil {
			_ = db.Close()
			t.Fatalf("prepare v8 journal: %v", err)
		}
	}
	if err = db.Close(); err != nil {
		t.Fatal(err)
	}

	journal, err = Open(root, identity)
	if err != nil {
		t.Fatalf("open and upgrade v8 journal: %v", err)
	}
	t.Cleanup(func() { _ = journal.Close() })
	var version int
	var marker string
	if err = journal.db.QueryRow(`PRAGMA user_version`).Scan(&version); err != nil || version != 9 {
		t.Fatalf("upgraded journal version=%d err=%v; want 9", version, err)
	}
	if err = journal.db.QueryRow(`SELECT name FROM schema_migrations WHERE version=9`).Scan(&marker); err != nil ||
		marker != "environment-resolved-claim-acquire-barrier" {
		t.Fatalf("upgraded journal migration marker=%q err=%v", marker, err)
	}
	installed, err := journal.InstallSnapshot(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if _, err = journal.PrepareClaimAcquire(testCommandPrefix+"91", testCommandPrefix+"92", testCommandPrefix+"93",
		testCommandPrefix+"94", testCommandPrefix+"95", "human", installed.Anchor); err != nil {
		t.Fatalf("claim-acquire barrier after migration: %v", err)
	}
}

func TestClaimAcquireBarrierStillRejectsUnresolvedClientCommand(t *testing.T) {
	journal := openInstallTestJournal(t, filepath.Join(t.TempDir(), "journal"))
	t.Cleanup(func() { _ = journal.Close() })
	entry, err := journal.PrepareCommand(CommandInput{
		ID: testCommandPrefix + "96",
		Request: operation.Request{
			Operation: operation.MatterCreateV1.Metadata().Operation,
			Actor:     "human",
			Context:   operation.Context{Repo: testRepoID},
			Input:     operation.MatterCreateInput{Title: "Pending", Locator: "pending"},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if entry.State == StateReturned {
		t.Fatal("prepared client command is unexpectedly terminal")
	}
	installed, err := journal.InstallSnapshot(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if _, err = journal.PrepareClaimAcquire(testCommandPrefix+"97", testCommandPrefix+"98", testCommandPrefix+"99",
		testCommandPrefix+"9A", testCommandPrefix+"9B", "human", installed.Anchor); err == nil {
		t.Fatal("claim acquisition crossed an unresolved client command")
	}
}
