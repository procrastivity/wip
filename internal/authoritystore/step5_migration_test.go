package authoritystore

import (
	"database/sql"
	"errors"
	"os"
	"path/filepath"
	"testing"
)

func TestStep5FreshAndExplicitV4Upgrade(t *testing.T) {
	s, root := fresh(t)
	var version int
	if err := s.db.QueryRow(`PRAGMA user_version`).Scan(&version); err != nil || version != 5 {
		t.Fatalf("fresh schema version %d: %v", version, err)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	reopened, err := OpenExisting(root)
	if err != nil {
		t.Fatal(err)
	}
	if err := reopened.Close(); err != nil {
		t.Fatal(err)
	}
	// The test-created v4 store is not silently upgraded by ordinary open.
	legacy := filepath.Join(t.TempDir(), "v4")
	if err := os.Mkdir(legacy, 0o700); err != nil {
		t.Fatal(err)
	}
	file := filepath.Join(legacy, "authority.db")
	db, err := connect(file, "rwc", true)
	if err != nil {
		t.Fatal(err)
	}
	if err = os.Chmod(file, 0o600); err != nil {
		t.Fatal(err)
	}
	for _, install := range []func(*sql.DB) error{installBaseline, installStep3, installStep4, installStep6} {
		if err = install(db); err != nil {
			t.Fatal(err)
		}
	}
	if err = initBlobDir(legacy); err != nil {
		t.Fatal(err)
	}
	if err = db.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err = OpenExisting(legacy); !errors.Is(err, ErrInvalidStore) {
		t.Fatalf("ordinary open of v4: %v", err)
	}
	if err = UpgradeV4(legacy); err != nil {
		t.Fatal(err)
	}
	upgraded, err := OpenExisting(legacy)
	if err != nil {
		t.Fatal(err)
	}
	if err = upgraded.Close(); err != nil {
		t.Fatal(err)
	}
	if err = UpgradeV4(legacy); !errors.Is(err, ErrInvalidStore) {
		t.Fatalf("repeated upgrade: %v", err)
	}
	backup, err := connect(filepath.Join(legacy, "authority-v4.backup.db"), "ro", false)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = backup.Close() }()
	if err = checkSchemaVersion(backup, 4); err != nil {
		t.Fatalf("retained v4 backup: %v", err)
	}
}

func TestStep5JournalAcceptsBeforeSubmissionAndRejectsGaps(t *testing.T) {
	s, _ := fresh(t)
	// Isolate the journal constraints from the older projection setup. The
	// entry must not require a Step 4 submission to exist at local acceptance.
	if _, err := s.db.Exec(`PRAGMA foreign_keys=OFF`); err != nil {
		t.Fatal(err)
	}
	const claim = "01KZ7XHAQT1S46NYPN1PW1DX3B"
	const journal = "01KZ7XHAQT1S46NYPN1PW1DX3C"
	const command = "01KZ7XHAQT1S46NYPN1PW1DX3D"
	if _, err := s.db.Exec(`INSERT INTO claims(domain_id,claim_id,matter_id,claim_epoch,authority_epoch,owner_environment_id,worktree_id,batch_id,dispatch_id,acquire_command_id) VALUES(?,?,?,1,1,?,?,?,?,?)`, domainA, claim, repoA, repoB, repoC, "01KZ7XHAQT1S46NYPN1PW1DX3E", "01KZ7XHAQT1S46NYPN1PW1DX3F", "01KZ7XHAQT1S46NYPN1PW1DX3G"); err != nil {
		t.Fatal(err)
	}
	if _, err := s.db.Exec(`INSERT INTO claim_journals(domain_id,journal_id,claim_id,generation,state) VALUES(?,?,?,1,'open')`, domainA, journal, claim); err != nil {
		t.Fatal(err)
	}
	insert := `INSERT INTO claim_journal_entries(domain_id,journal_id,position,command_id,request_hash,command,environment_sequence,state) VALUES(?,?,?,?,?,?,?,'pending-return')`
	if _, err := s.db.Exec(insert, domainA, journal, 1, command, "hash", []byte{0xa0}, 17); err != nil {
		t.Fatalf("local acceptance required authority submission: %v", err)
	}
	if _, err := s.db.Exec(insert, domainA, journal, 3, repoA, "hash", []byte{0xa0}, 19); err == nil {
		t.Fatal("accepted skipped journal position")
	}
	if _, err := s.db.Exec(insert, domainA, journal, 2, repoA, "hash", []byte{0xa0}, 19); err == nil {
		t.Fatal("accepted skipped environment sequence")
	}
	if _, err := s.db.Exec(`DELETE FROM claim_journal_entries WHERE domain_id=? AND journal_id=?`, domainA, journal); err == nil {
		t.Fatal("deleted immutable journal evidence")
	}
}
