package wipdjournal

import (
	"bufio"
	"bytes"
	"context"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"

	"github.com/procrastivity/wip/internal/operation"
)

const (
	testRepoID        = "01KZ7XHAQT1S46NYPN1PW1DX3C"
	testDomainID      = "01KZ7XHAQT1S46NYPN1PW1DX3B"
	testEnvironmentID = "01KZ7XHAQT1S46NYPN1PW1DX3D"
	testCommandPrefix = "01KZ7XHAQT1S46NYPN1PW1DX"
)

var testIdentity = Identity{
	RepoID: testRepoID, DomainID: testDomainID, AuthorityEpoch: 7, EnvironmentID: testEnvironmentID,
}

func TestPrepareCommandSurvivesLostResponseAndRestart(t *testing.T) {
	root := filepath.Join(t.TempDir(), "client-profile")
	journal, err := Open(root, testIdentity)
	if err != nil {
		t.Fatal(err)
	}
	firstInput := commandInput(testCommandPrefix+"40", "first-locator", "Café protocol continuity")
	// Deliberately discard the successful return to model a local response lost
	// after SQLite committed. The caller retries with the same preallocated ID.
	if _, err = journal.PrepareCommand(firstInput); err != nil {
		t.Fatal(err)
	}
	if err = journal.Close(); err != nil {
		t.Fatal(err)
	}

	journal, err = Open(root, testIdentity)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = journal.Close() })
	retried, err := journal.PrepareCommand(firstInput)
	if err != nil {
		t.Fatal(err)
	}
	if retried.Command.ID != firstInput.ID || retried.EnvironmentSeq != 1 || retried.JournalPosition != 1 ||
		retried.State != StateJournaled || retried.Delivery != operation.DeliveryProvisional {
		t.Fatalf("recovered entry = %+v, want first journaled entry at sequence/position 1", retried)
	}
	if retried.Command.ActedAt == "" || retried.RequestHash == "" || len(retried.CanonicalBytes) == 0 {
		t.Fatalf("recovered immutable identity is incomplete: %+v", retried)
	}
	if got, err := journal.Get(firstInput.ID); err != nil || !sameEntryIdentity(got, retried) {
		t.Fatalf("Get() after lost response = %+v, %v; want exact retry identity", got, err)
	}

	conflict := commandInput(firstInput.ID, "different-locator", "different title")
	if _, err = journal.PrepareCommand(conflict); !errors.Is(err, ErrCommandIDConflict) {
		t.Fatalf("same ID with different intent = %v, want ErrCommandIDConflict", err)
	}
	secondInput := commandInput(testCommandPrefix+"41", "second-locator", "Separate second command")
	second, err := journal.PrepareCommand(secondInput)
	if err != nil {
		t.Fatal(err)
	}
	if second.EnvironmentSeq != 2 || second.JournalPosition != 2 || second.Command.ID != secondInput.ID {
		t.Fatalf("second command = %+v, want next sequence and journal position 2", second)
	}

	entries, err := journal.Entries()
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 2 || entries[0].Command.ID != firstInput.ID || entries[1].Command.ID != secondInput.ID ||
		!sameEntryIdentity(entries[0], retried) || !sameEntryIdentity(entries[1], second) {
		t.Fatalf("ordered journal entries = %+v; want the two exact committed commands", entries)
	}
	journaled, err := journal.JournaledCommands()
	if err != nil || len(journaled) != 0 {
		t.Fatalf("JournaledCommands() before overlay admission = %+v, %v; want no eligible return heads", journaled, err)
	}
	installed, err := journal.InstallSnapshot(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	installed, err = journal.AdmitPending(context.Background(), installed.Expectation(), firstInput.ID)
	if err != nil {
		t.Fatal(err)
	}
	_, err = journal.AdmitPending(context.Background(), installed.Expectation(), secondInput.ID)
	if err != nil {
		t.Fatal(err)
	}
	journaled, err = journal.JournaledCommands()
	if err != nil || len(journaled) != 2 || journaled[0].JournalPosition != 1 || journaled[1].JournalPosition != 2 {
		t.Fatalf("JournaledCommands() after atomic overlay admission = %+v, %v; want both eligible entries in order", journaled, err)
	}

	if err = journal.Close(); err != nil {
		t.Fatal(err)
	}
	restarted, err := Open(root, testIdentity)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = restarted.Close() })
	gotEntries, err := restarted.Entries()
	if err != nil || len(gotEntries) != 2 || !sameEntryBytes(gotEntries[0], retried) || !sameEntryBytes(gotEntries[1], second) ||
		gotEntries[0].State != StatePendingReturn || gotEntries[1].State != StatePendingReturn {
		t.Fatalf("entries after second restart = %+v, %v; want exact bytes/IDs/order and admitted pending state", gotEntries, err)
	}
}

func TestPreparedCommandSurvivesWriterProcessCrashBeforeResponse(t *testing.T) {
	root := filepath.Join(t.TempDir(), "client-profile")
	command := exec.Command(os.Args[0], "-test.run=^TestPrepareCommandCrashHelper$")
	command.Env = append(os.Environ(), "WIPDJOURNAL_CRASH_HELPER=1", "WIPDJOURNAL_CRASH_ROOT="+root)
	stdout, err := command.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	command.Stderr = io.Discard
	if err = command.Start(); err != nil {
		t.Fatal(err)
	}
	finished := make(chan struct{})
	t.Cleanup(func() {
		select {
		case <-finished:
		default:
			_ = command.Process.Kill()
			_ = command.Wait()
		}
	})
	line, err := bufio.NewReader(stdout).ReadString('\n')
	fields := strings.Fields(line)
	if err != nil || len(fields) != 8 || fields[0] != "commit-durable" {
		t.Fatalf("writer helper signal = %q, %v; want committed command and blob evidence", line, err)
	}
	sequence, sequenceErr := strconv.ParseUint(fields[2], 10, 64)
	position, positionErr := strconv.ParseUint(fields[3], 10, 64)
	blobSize, blobSizeErr := strconv.ParseInt(fields[7], 10, 64)
	originalBytes, bytesErr := hex.DecodeString(fields[5])
	if sequenceErr != nil || positionErr != nil || blobSizeErr != nil || bytesErr != nil {
		t.Fatalf("writer helper evidence is malformed: %q", line)
	}
	if err = command.Process.Kill(); err != nil {
		t.Fatal(err)
	}
	if err = command.Wait(); err == nil {
		t.Fatal("writer helper exited cleanly; expected abrupt process termination")
	}
	close(finished)

	journal, err := Open(root, testIdentity)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = journal.Close() })
	input := commandInput(fields[1], "crash-survivor", "Committed before lost response")
	entry, err := journal.PrepareCommand(input)
	if err != nil {
		t.Fatal(err)
	}
	if entry.Command.ID != fields[1] || entry.EnvironmentSeq != sequence || entry.JournalPosition != position ||
		entry.RequestHash != fields[4] || !bytes.Equal(entry.CanonicalBytes, originalBytes) || entry.State != StateJournaled {
		t.Fatalf("recovered command after writer crash = %+v; want exact pre-crash ID/bytes/hash/order/state", entry)
	}
	entries, err := journal.Entries()
	if err != nil || len(entries) != 1 || !sameEntryIdentity(entries[0], entry) {
		t.Fatalf("journal after lost response and process crash = %+v, %v; want one exact retained command", entries, err)
	}
	blob := StagedBlob{Digest: fields[6], Size: blobSize}
	readAndCompareBlob(t, journal, blob, []byte("crash-surviving staged bytes\x00\xff"))
}

func TestPrepareCommandCrashHelper(t *testing.T) {
	if os.Getenv("WIPDJOURNAL_CRASH_HELPER") != "1" {
		return
	}
	journal, err := Open(os.Getenv("WIPDJOURNAL_CRASH_ROOT"), testIdentity)
	if err != nil {
		t.Fatal(err)
	}
	entry, err := journal.PrepareCommand(commandInput(testCommandPrefix+"42", "crash-survivor", "Committed before lost response"))
	if err != nil {
		t.Fatal(err)
	}
	content := []byte("crash-surviving staged bytes\x00\xff")
	blob, err := journal.StageBlob(bytes.NewReader(content), int64(len(content)))
	if err != nil {
		t.Fatal(err)
	}
	if _, err = fmt.Fprintf(os.Stdout, "commit-durable %s %d %d %s %x %s %d\n",
		entry.Command.ID, entry.EnvironmentSeq, entry.JournalPosition, entry.RequestHash,
		entry.CanonicalBytes, blob.Digest, blob.Size); err != nil {
		t.Fatal(err)
	}
	select {}
}

func TestJournalUsesFullSynchronousDurability(t *testing.T) {
	journal, err := Open(filepath.Join(t.TempDir(), "new", "nested", "client-profile"), testIdentity)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = journal.Close() })
	var mode string
	var synchronous int
	if err = journal.db.QueryRow(`PRAGMA journal_mode`).Scan(&mode); err != nil {
		t.Fatal(err)
	}
	if err = journal.db.QueryRow(`PRAGMA synchronous`).Scan(&synchronous); err != nil {
		t.Fatal(err)
	}
	if !strings.EqualFold(mode, "delete") || synchronous != 2 {
		t.Fatalf("SQLite durability = journal_mode %q, synchronous %d; want DELETE and FULL (2)", mode, synchronous)
	}
}

func TestDurableBlobOrphanAfterWriterCrashIsInvisibleAndAdoptable(t *testing.T) {
	root := filepath.Join(t.TempDir(), "client-profile")
	command := exec.Command(os.Args[0], "-test.run=^TestStageBlobOrphanCrashHelper$")
	command.Env = append(os.Environ(), "WIPDJOURNAL_ORPHAN_HELPER=1", "WIPDJOURNAL_ORPHAN_ROOT="+root)
	stdout, err := command.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	command.Stderr = io.Discard
	if err = command.Start(); err != nil {
		t.Fatal(err)
	}
	finished := make(chan struct{})
	t.Cleanup(func() {
		select {
		case <-finished:
		default:
			_ = command.Process.Kill()
			_ = command.Wait()
		}
	})
	line, err := bufio.NewReader(stdout).ReadString('\n')
	if err != nil || line != "orphan-durable\n" {
		t.Fatalf("writer helper signal = %q, %v; want synced orphan blob", line, err)
	}
	if err = command.Process.Kill(); err != nil {
		t.Fatal(err)
	}
	if err = command.Wait(); err == nil {
		t.Fatal("writer helper exited cleanly; expected abrupt process termination")
	}
	close(finished)

	journal, err := Open(root, testIdentity)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = journal.Close() })
	content := []byte("durable orphan bytes\x00\xff")
	digest := operation.BlobDigest(content)
	if reader, _, openErr := journal.OpenBlob(digest); !errors.Is(openErr, ErrNotFound) {
		if reader != nil {
			_ = reader.Close()
		}
		t.Fatalf("unreferenced durable blob OpenBlob() = %v; want it invisible as ErrNotFound", openErr)
	}
	blob, err := journal.StageBlob(bytes.NewReader(content), int64(len(content)))
	if err != nil || blob.Digest != digest || blob.Size != int64(len(content)) {
		t.Fatalf("retry adopts verified orphan = %+v, %v; want %s/%d", blob, err, digest, len(content))
	}
	readAndCompareBlob(t, journal, blob, content)
}

func TestStageBlobRetryResyncsOrphanBeforeMetadataCommit(t *testing.T) {
	journal, err := Open(filepath.Join(t.TempDir(), "client-profile"), testIdentity)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = journal.Close() })
	if _, err = journal.db.Exec(`
		CREATE TABLE journal_test_blob_sync (file_synced INTEGER NOT NULL, directory_synced INTEGER NOT NULL);
		INSERT INTO journal_test_blob_sync(file_synced, directory_synced) VALUES(0, 0);
		CREATE TRIGGER journal_test_require_blob_sync BEFORE INSERT ON staged_blobs
		WHEN NOT EXISTS (SELECT 1 FROM journal_test_blob_sync WHERE file_synced=1 AND directory_synced=1)
		BEGIN SELECT RAISE(ABORT, 'blob durability barrier was not crossed'); END;
	`); err != nil {
		t.Fatal(err)
	}

	content := []byte("sync barrier retry\x00\xff")
	digest := operation.BlobDigest(content)
	var events []string
	actualFileSync := journal.syncBlobFile
	journal.syncBlobFile = func(path, digest string, size int64) error {
		if err := actualFileSync(path, digest, size); err != nil {
			return err
		}
		if _, err := journal.db.Exec(`UPDATE journal_test_blob_sync SET file_synced=1`); err != nil {
			return err
		}
		events = append(events, "file-synced")
		return nil
	}
	directorySyncFailed := errors.New("injected first blob directory sync failure")
	directorySyncCalls := 0
	journal.syncBlobDirectory = func(path string) error {
		directorySyncCalls++
		if directorySyncCalls == 1 {
			events = append(events, "directory-sync-failed")
			return directorySyncFailed
		}
		if err := syncDirectory(path); err != nil {
			return err
		}
		if _, err := journal.db.Exec(`UPDATE journal_test_blob_sync SET directory_synced=1`); err != nil {
			return err
		}
		events = append(events, "directory-synced")
		return nil
	}

	if _, err = journal.StageBlob(bytes.NewReader(content), int64(len(content))); !errors.Is(err, directorySyncFailed) {
		t.Fatalf("first StageBlob() error = %v; want injected directory-sync failure", err)
	}
	if len(events) != 2 || events[0] != "file-synced" || events[1] != "directory-sync-failed" {
		t.Fatalf("new-link sync order before injected failure = %v; want file sync then directory failure", events)
	}
	var rows int
	if err = journal.db.QueryRow(`SELECT count(*) FROM staged_blobs WHERE digest=?`, digest).Scan(&rows); err != nil || rows != 0 {
		t.Fatalf("metadata rows after failed directory sync = %d, %v; want none", rows, err)
	}
	path := filepath.Join(journal.blobsDir, strings.TrimPrefix(digest, digestPrefix))
	if err = verifyBlobFile(path, digest, int64(len(content))); err != nil {
		t.Fatalf("linked orphan after failed directory sync is not intact: %v", err)
	}

	// Make the retry prove both barriers again rather than relying on any
	// successful file sync from the failed attempt.
	if _, err = journal.db.Exec(`UPDATE journal_test_blob_sync SET file_synced=0, directory_synced=0`); err != nil {
		t.Fatal(err)
	}
	events = nil
	blob, err := journal.StageBlob(bytes.NewReader(content), int64(len(content)))
	if err != nil || blob.Digest != digest || blob.Size != int64(len(content)) {
		t.Fatalf("EEXIST StageBlob() retry = %+v, %v; want same verified blob", blob, err)
	}
	if len(events) != 2 || events[0] != "file-synced" || events[1] != "directory-synced" {
		t.Fatalf("successful retry sync order = %v; want file then directory before metadata commit", events)
	}
	if err = journal.db.QueryRow(`SELECT count(*) FROM staged_blobs WHERE digest=?`, digest).Scan(&rows); err != nil || rows != 1 {
		t.Fatalf("metadata rows after synced retry = %d, %v; want one", rows, err)
	}
	readAndCompareBlob(t, journal, blob, content)
}

func TestStageBlobOrphanCrashHelper(t *testing.T) {
	if os.Getenv("WIPDJOURNAL_ORPHAN_HELPER") != "1" {
		return
	}
	journal, err := Open(os.Getenv("WIPDJOURNAL_ORPHAN_ROOT"), testIdentity)
	if err != nil {
		t.Fatal(err)
	}
	content := []byte("durable orphan bytes\x00\xff")
	digest := operation.BlobDigest(content)
	path := filepath.Join(journal.blobsDir, strings.TrimPrefix(digest, digestPrefix))
	file, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = file.Write(content); err == nil {
		err = file.Chmod(0o400)
	}
	if err == nil {
		err = file.Sync()
	}
	closeErr := file.Close()
	if err != nil || closeErr != nil {
		t.Fatal(errors.Join(err, closeErr))
	}
	if err = syncDirectory(journal.blobsDir); err != nil {
		t.Fatal(err)
	}
	if _, err = fmt.Fprintln(os.Stdout, "orphan-durable"); err != nil {
		t.Fatal(err)
	}
	select {}
}

func TestConcurrentPreparationAllocatesContiguousSequenceAndJournalOrder(t *testing.T) {
	journal, err := Open(filepath.Join(t.TempDir(), "client-profile"), testIdentity)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = journal.Close() })

	const count = 12
	errs := make(chan error, count)
	var wait sync.WaitGroup
	for index := 0; index < count; index++ {
		index := index
		wait.Add(1)
		go func() {
			defer wait.Done()
			id := fmt.Sprintf("%s%02X", testCommandPrefix, 0x50+index)
			input := commandInput(id, fmt.Sprintf("parallel-%02d", index), fmt.Sprintf("Command %02d", index))
			entry, prepareErr := journal.PrepareCommand(input)
			if prepareErr == nil && (entry.EnvironmentSeq == 0 || entry.JournalPosition == 0 || entry.State != StateJournaled) {
				prepareErr = fmt.Errorf("invalid concurrently allocated entry: %+v", entry)
			}
			errs <- prepareErr
		}()
	}
	wait.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatal(err)
		}
	}

	entries, err := journal.Entries()
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != count {
		t.Fatalf("entry count = %d, want %d", len(entries), count)
	}
	seen := make(map[string]bool, count)
	for index, entry := range entries {
		wantPosition := uint64(index + 1)
		if entry.EnvironmentSeq != wantPosition || entry.JournalPosition != wantPosition || entry.State != StateJournaled {
			t.Fatalf("entry %d = %+v; want contiguous sequence and journal position %d", index, entry, wantPosition)
		}
		if seen[entry.Command.ID] {
			t.Fatalf("duplicate command ID in durable order: %s", entry.Command.ID)
		}
		seen[entry.Command.ID] = true
	}
}

func TestConcurrentSameIDRetryReturnsSingleCommittedEntry(t *testing.T) {
	journal, err := Open(filepath.Join(t.TempDir(), "client-profile"), testIdentity)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = journal.Close() })
	input := commandInput(testCommandPrefix+"70", "same-id", "Concurrent retries preserve one identity")
	start := make(chan struct{})
	entries := make([]Entry, 2)
	errs := make([]error, 2)
	var wait sync.WaitGroup
	for index := range entries {
		wait.Add(1)
		go func(index int) {
			defer wait.Done()
			<-start
			entries[index], errs[index] = journal.PrepareCommand(input)
		}(index)
	}
	close(start)
	wait.Wait()
	for index, err := range errs {
		if err != nil {
			t.Fatalf("same-ID preparation %d: %v", index, err)
		}
	}
	if !sameEntryIdentity(entries[0], entries[1]) || entries[0].EnvironmentSeq != 1 ||
		entries[0].JournalPosition != 1 || entries[0].State != StateJournaled {
		t.Fatalf("concurrent same-ID entries = %+v / %+v; want one exact sequence-1 journal entry", entries[0], entries[1])
	}
	retained, err := journal.Entries()
	if err != nil || len(retained) != 1 || !sameEntryIdentity(retained[0], entries[0]) {
		t.Fatalf("retained entries = %+v, %v; want exactly the shared identity", retained, err)
	}
}

func TestStagedBlobIntegrityAndLengthSurviveRestart(t *testing.T) {
	root := filepath.Join(t.TempDir(), "client-profile")
	journal, err := Open(root, testIdentity)
	if err != nil {
		t.Fatal(err)
	}
	content := make([]byte, 16*1024+1)
	for index := range content {
		content[index] = byte((index*73 + 19) % 256)
	}
	blob, err := journal.StageBlob(bytes.NewReader(content), int64(len(content)))
	if err != nil {
		t.Fatal(err)
	}
	const wantDigest = "sha256:09963d84d6dbc392b840affa03ae606f19da801c50475e6be48e596ff14b5f2c"
	if blob.Digest != wantDigest || blob.Size != int64(len(content)) {
		t.Fatalf("staged blob = %+v; want independently computed digest %s and length %d", blob, wantDigest, len(content))
	}
	for name, input := range map[string]struct {
		reader io.Reader
		size   int64
	}{
		"short input":    {reader: strings.NewReader("short"), size: 6},
		"trailing input": {reader: strings.NewReader("longer"), size: 3},
	} {
		t.Run(name, func(t *testing.T) {
			if _, stageErr := journal.StageBlob(input.reader, input.size); !errors.Is(stageErr, ErrBlobLength) {
				t.Fatalf("StageBlob() error = %v, want ErrBlobLength", stageErr)
			}
		})
	}
	readAndCompareBlob(t, journal, blob, content)
	if err = journal.Close(); err != nil {
		t.Fatal(err)
	}

	restarted, err := Open(root, testIdentity)
	if err != nil {
		t.Fatal(err)
	}
	readAndCompareBlob(t, restarted, blob, content)
	if again, stageErr := restarted.StageBlob(bytes.NewReader(content), int64(len(content))); stageErr != nil || again != blob {
		t.Fatalf("idempotent StageBlob() = %+v, %v; want %+v", again, stageErr, blob)
	}
	if err = restarted.Close(); err != nil {
		t.Fatal(err)
	}

	path := filepath.Join(root, blobDirName, strings.TrimPrefix(blob.Digest, digestPrefix))
	if err = os.Chmod(path, 0o600); err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(path, []byte("tampered staged bytes"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err = Open(root, testIdentity); !errors.Is(err, ErrInvalidJournal) {
		t.Fatalf("Open() after blob tampering = %v, want ErrInvalidJournal", err)
	}
}

func TestOpenBindsIdentityAndOwnsSingleWriterLease(t *testing.T) {
	root := filepath.Join(t.TempDir(), "client-profile")
	journal, err := Open(root, testIdentity)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = Open(root, testIdentity); !errors.Is(err, ErrHeld) {
		t.Fatalf("second writer Open() = %v, want ErrHeld", err)
	}
	if err = journal.Close(); err != nil {
		t.Fatal(err)
	}

	wrongIdentity := testIdentity
	wrongIdentity.AuthorityEpoch++
	if _, err = Open(root, wrongIdentity); !errors.Is(err, ErrInvalidIdentity) {
		t.Fatalf("Open() under a different installed epoch = %v, want ErrInvalidIdentity", err)
	}
	correct, err := Open(root, testIdentity)
	if err != nil {
		t.Fatalf("reopen after releasing the lease: %v", err)
	}
	if err = correct.Close(); err != nil {
		t.Fatal(err)
	}
}

func TestRejectedCommandDoesNotConsumeIDSequenceOrJournalPosition(t *testing.T) {
	journal, err := Open(filepath.Join(t.TempDir(), "client-profile"), testIdentity)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = journal.Close() })

	wrongRepo := commandInput(testCommandPrefix+"60", "wrong-repo", "Wrong Repo must not enter journal")
	wrongRepo.Request.Context.Repo = "01KZ7XHAQT1S46NYPN1PW1DX3E"
	if _, err = journal.PrepareCommand(wrongRepo); !errors.Is(err, ErrInvalidCommand) {
		t.Fatalf("wrong-Repo command = %v, want ErrInvalidCommand", err)
	}
	unsupportedVersion := commandInput(testCommandPrefix+"60", "unsupported-version", "Unknown operation version")
	unsupportedVersion.Request.Operation.Version = 99
	if _, err = journal.PrepareCommand(unsupportedVersion); !errors.Is(err, ErrInvalidCommand) {
		t.Fatalf("unsupported operation version = %v, want ErrInvalidCommand", err)
	}
	undeclaredBlob := commandInput(testCommandPrefix+"60", "undeclared-blob", "Operation does not accept blobs")
	undeclaredBlob.Request.Blobs = []operation.BlobInput{{
		Name: "source", Digest: "sha256:ba7816bf8f01cfea414140de5dae2223b00361a396177a9cb410ff61f20015ad", Size: 3,
	}}
	if _, err = journal.PrepareCommand(undeclaredBlob); !errors.Is(err, ErrInvalidCommand) {
		t.Fatalf("undeclared blob reference = %v, want ErrInvalidCommand", err)
	}

	valid := commandInput(testCommandPrefix+"60", "accepted-after-refusal", "Rejected attempts allocate nothing")
	entry, err := journal.PrepareCommand(valid)
	if err != nil {
		t.Fatal(err)
	}
	if entry.EnvironmentSeq != 1 || entry.JournalPosition != 1 || entry.State != StateJournaled {
		t.Fatalf("first durable command = %+v, want sequence and position 1", entry)
	}
	entries, err := journal.Entries()
	if err != nil || len(entries) != 1 || entries[0].Command.ID != valid.ID {
		t.Fatalf("journal after rejected inputs = %+v, %v; want only the durable command", entries, err)
	}
}

func commandInput(id, locator, title string) CommandInput {
	return CommandInput{
		ID: id,
		Request: operation.Request{
			Operation: operation.MatterCreateV1.Metadata().Operation,
			Actor:     operation.Actor("human"),
			Context:   operation.Context{Repo: testRepoID},
			Input:     operation.MatterCreateInput{Title: title, Locator: locator},
		},
	}
}

func sameEntryIdentity(left, right Entry) bool {
	return left.Command.ID == right.Command.ID && left.EnvironmentSeq == right.EnvironmentSeq &&
		left.JournalPosition == right.JournalPosition && left.RequestHash == right.RequestHash &&
		left.Delivery == right.Delivery && left.State == right.State &&
		bytes.Equal(left.CanonicalBytes, right.CanonicalBytes)
}

func sameEntryBytes(left, right Entry) bool {
	return left.Command.ID == right.Command.ID && left.EnvironmentSeq == right.EnvironmentSeq &&
		left.JournalPosition == right.JournalPosition && left.RequestHash == right.RequestHash &&
		left.Delivery == right.Delivery && bytes.Equal(left.CanonicalBytes, right.CanonicalBytes)
}

func readAndCompareBlob(t *testing.T, journal *Journal, expected StagedBlob, want []byte) {
	t.Helper()
	reader, size, err := journal.OpenBlob(expected.Digest)
	if err != nil {
		t.Fatal(err)
	}
	got, readErr := io.ReadAll(reader)
	closeErr := reader.Close()
	if readErr != nil || closeErr != nil || size != expected.Size || !bytes.Equal(got, want) {
		t.Fatalf("staged bytes = %q (%d bytes), read/close = %v/%v; want %q (%d bytes)", got, size, readErr, closeErr, want, expected.Size)
	}
}
