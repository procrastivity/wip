// Package wipdjournal owns the Environment-local durable command journal and
// content-addressed staged blobs. It stores no authority state or receipts.
package wipdjournal

import (
	"bytes"
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"time"

	"golang.org/x/sys/unix"
	// Register SQLite for the Environment-local durable journal.
	_ "modernc.org/sqlite"

	"github.com/procrastivity/wip/internal/operation"
	"github.com/procrastivity/wip/internal/wipdprofile"
)

const (
	databaseName   = "command-journal.sqlite"
	lockName       = "command-journal.lock"
	blobDirName    = "staged-blobs"
	schemaVersion  = 1
	maxBlobSize    = int64(1 << 40)
	digestPrefix   = "sha256:"
	commandColumns = `command_id, environment_sequence, journal_position, request_hash, canonical_bytes, delivery, state`
)

var (
	blobNamePattern = regexp.MustCompile(`^[0-9a-f]{64}$`)
	identityPattern = regexp.MustCompile(`^[0-7][0-9A-HJKMNP-TV-Z]{25}$`)

	// ErrClosed means the journal has been closed.
	ErrClosed = errors.New("wipdjournal: journal is closed")
	// ErrHeld means another process owns the journal writer lease.
	ErrHeld = errors.New("wipdjournal: another process owns the journal")
	// ErrInvalidJournal means persisted journal state is corrupt or incompatible.
	ErrInvalidJournal = errors.New("wipdjournal: invalid or incompatible journal")
	// ErrInvalidIdentity means the requested Environment binding is invalid or differs from disk.
	ErrInvalidIdentity = errors.New("wipdjournal: invalid or mismatched Environment identity")
	// ErrInvalidCommand means a command is not a supported, canonical mutation for this Environment.
	ErrInvalidCommand = errors.New("wipdjournal: invalid command")
	// ErrCommandIDConflict means an existing command ID was retried with different intent.
	ErrCommandIDConflict = errors.New("wipdjournal: command ID conflicts with persisted intent")
	// ErrNotFound means no command or staged blob exists for the requested identity.
	ErrNotFound = errors.New("wipdjournal: item not found")
	// ErrBlobLength means the staged reader did not contain exactly its declared length.
	ErrBlobLength = errors.New("wipdjournal: staged blob length mismatch")
)

// Identity binds the journal to one installed client Environment and Repo.
// The identity is immutable for the lifetime of this journal database.
type Identity struct {
	RepoID         string
	DomainID       string
	AuthorityEpoch uint64
	EnvironmentID  string
}

// CommandInput is the caller-allocated command identity and semantic intent.
// ActedAt, Environment sequence, and journal position are assigned once by
// PrepareCommand and are retained on an exact retry with the same ID.
type CommandInput struct {
	ID                   string
	CausationCommandID   string
	CorrelationCommandID string
	Request              operation.Request
}

// State is the durable local disposition, not an M1 result or authority receipt.
type State string

const (
	// StateJournaled means the immutable command is durable but has not yet
	// crossed the pending-return boundary with a recoverable local overlay.
	StateJournaled State = "journaled"
	// StateAttemptPrepared records identity evidence for an authority-class command.
	// It is not permission to queue or execute that command later.
	StateAttemptPrepared State = "attempt-prepared"
)

// Entry is the exact immutable command identity retained by the Environment.
// JournalPosition is zero for authority-class attempt evidence, which is not a
// deferred local command journal entry.
type Entry struct {
	Command         operation.Command
	CanonicalBytes  []byte
	RequestHash     string
	EnvironmentSeq  uint64
	JournalPosition uint64
	Delivery        operation.DeliveryClass
	State           State
}

// StagedBlob identifies durable content without exposing a local path.
type StagedBlob struct {
	Digest string
	Size   int64
}

// Journal holds one Environment's durable state and exclusive writer lease.
type Journal struct {
	mu                sync.Mutex
	db                *sql.DB
	lock              *os.File
	blobsDir          string
	identity          Identity
	syncBlobFile      func(string, string, int64) error
	syncBlobDirectory func(string) error
}

// Open creates a new journal only when its database path is absent. Existing
// databases are validated against the supplied Environment identity and every
// retained command/blob before they are returned to the caller.
func Open(root string, identity Identity) (*Journal, error) {
	if !validIdentity(identity) {
		return nil, ErrInvalidIdentity
	}
	profile, err := wipdprofile.Resolve(root)
	if err != nil {
		return nil, fmt.Errorf("wipdjournal: resolve private root: %w", err)
	}
	if err = makePrivatePath(profile.Root); err != nil {
		return nil, fmt.Errorf("wipdjournal: create private root: %w", err)
	}
	if err = verifyPrivateDirectory(profile.Root); err != nil {
		return nil, err
	}
	// Re-resolve after creation so no newly-created path component can silently
	// turn the supplied root into a symlink alias.
	profile, err = wipdprofile.Resolve(profile.Root)
	if err != nil {
		return nil, fmt.Errorf("wipdjournal: revalidate private root: %w", err)
	}
	lock, err := acquire(profile.Root)
	if err != nil {
		return nil, err
	}
	fail := func(err error) (*Journal, error) {
		_ = lock.Close()
		return nil, err
	}
	blobDir := filepath.Join(profile.Root, blobDirName)
	if err = makePrivateDirectory(blobDir); err != nil {
		return fail(fmt.Errorf("wipdjournal: prepare blob directory: %w", err))
	}
	databasePath := filepath.Join(profile.Root, databaseName)
	created, err := prepareDatabaseFile(databasePath)
	if err != nil {
		return fail(err)
	}
	db, err := connect(databasePath)
	if err != nil {
		return fail(fmt.Errorf("wipdjournal: open database: %w", err))
	}
	if created {
		err = installSchema(db, identity)
	} else {
		err = checkIdentity(db, identity)
	}
	if err == nil {
		err = checkDatabase(db, identity, blobDir)
	}
	if err != nil {
		_ = db.Close()
		return fail(fmt.Errorf("%w: %w", ErrInvalidJournal, err))
	}
	return &Journal{
		db: db, lock: lock, blobsDir: blobDir, identity: identity,
		syncBlobFile: syncVerifiedBlobFile, syncBlobDirectory: syncDirectory,
	}, nil
}

// Close releases the database and writer lease. It is idempotent.
func (j *Journal) Close() error {
	if j == nil {
		return nil
	}
	j.mu.Lock()
	defer j.mu.Unlock()
	if j.db == nil {
		return nil
	}
	dbErr := j.db.Close()
	j.db = nil
	lockErr := j.lock.Close()
	j.lock = nil
	return errors.Join(dbErr, lockErr)
}

// PrepareCommand validates and durably commits one immutable command before
// returning it. Exact retries by the caller-allocated ID return the original
// bytes, sequence, time, and order; different intent under that ID is refused.
func (j *Journal) PrepareCommand(input CommandInput) (Entry, error) {
	if j == nil {
		return Entry{}, ErrClosed
	}
	j.mu.Lock()
	defer j.mu.Unlock()
	if j.db == nil {
		return Entry{}, ErrClosed
	}
	delivery, correlation, err := validateInput(j.identity, input)
	if err != nil {
		return Entry{}, err
	}
	if entry, err := lookup(j.db, input.ID); err == nil {
		if err = j.verifyEntry(entry); err != nil {
			return Entry{}, err
		}
		if !sameIntent(entry, j.identity, input, correlation) {
			return Entry{}, ErrCommandIDConflict
		}
		return cloneEntry(entry), nil
	} else if !errors.Is(err, ErrNotFound) {
		return Entry{}, err
	}
	if err = j.verifyCommandBlobs(input.Request.Blobs); err != nil {
		return Entry{}, err
	}

	tx, err := j.db.BeginTx(context.Background(), nil)
	if err != nil {
		return Entry{}, err
	}
	defer func() { _ = tx.Rollback() }()
	var nextSequence, nextPosition int64
	if err = tx.QueryRow(`SELECT next_environment_sequence, next_journal_position FROM environment_state WHERE singleton=1`).Scan(&nextSequence, &nextPosition); err != nil {
		return Entry{}, err
	}
	if nextSequence < 1 || nextPosition < 1 || nextSequence == int64(^uint64(0)>>1) ||
		(delivery != operation.DeliveryAuthority && nextPosition == int64(^uint64(0)>>1)) {
		return Entry{}, ErrInvalidJournal
	}
	command := operation.Command{
		ID: input.ID, AuthorityDomainID: j.identity.DomainID, ExpectedAuthorityEpoch: j.identity.AuthorityEpoch,
		EnvironmentID: j.identity.EnvironmentID, EnvironmentSequence: uint64(nextSequence),
		ActedAt: time.Now().UTC().Format(time.RFC3339Nano), CausationCommandID: input.CausationCommandID,
		CorrelationCommandID: correlation, Request: input.Request,
	}
	canonical, err := command.CanonicalBytes()
	if err != nil {
		return Entry{}, fmt.Errorf("%w: canonical identity: %v", ErrInvalidCommand, err)
	}
	hash, err := command.RequestHash()
	if err != nil {
		return Entry{}, fmt.Errorf("%w: request hash: %v", ErrInvalidCommand, err)
	}
	state := StateJournaled
	var position any = nextPosition
	if delivery == operation.DeliveryAuthority {
		state = StateAttemptPrepared
		position = nil
	}
	if _, err = tx.Exec(`INSERT INTO commands(command_id, environment_sequence, journal_position, request_hash, canonical_bytes, delivery, state)
		VALUES(?, ?, ?, ?, ?, ?, ?)`, input.ID, nextSequence, position, hash, canonical, string(delivery), string(state)); err != nil {
		return Entry{}, err
	}
	if delivery == operation.DeliveryAuthority {
		_, err = tx.Exec(`UPDATE environment_state SET next_environment_sequence=? WHERE singleton=1 AND next_environment_sequence=?`, nextSequence+1, nextSequence)
	} else {
		_, err = tx.Exec(`UPDATE environment_state SET next_environment_sequence=?, next_journal_position=? WHERE singleton=1 AND next_environment_sequence=? AND next_journal_position=?`, nextSequence+1, nextPosition+1, nextSequence, nextPosition)
	}
	if err != nil {
		return Entry{}, err
	}
	if err = tx.Commit(); err != nil {
		return Entry{}, err
	}
	entry := Entry{
		Command: command, CanonicalBytes: bytes.Clone(canonical), RequestHash: hash,
		EnvironmentSeq: uint64(nextSequence), Delivery: delivery, State: state,
	}
	if delivery != operation.DeliveryAuthority {
		entry.JournalPosition = uint64(nextPosition)
	}
	return cloneEntry(entry), nil
}

// Get returns one exact retained command by its caller-allocated ID.
func (j *Journal) Get(commandID string) (Entry, error) {
	if j == nil {
		return Entry{}, ErrClosed
	}
	j.mu.Lock()
	defer j.mu.Unlock()
	if j.db == nil {
		return Entry{}, ErrClosed
	}
	entry, err := lookup(j.db, commandID)
	if err != nil {
		return Entry{}, err
	}
	if err = j.verifyEntry(entry); err != nil {
		return Entry{}, err
	}
	return cloneEntry(entry), nil
}

// Entries returns the immutable Environment command history in sequence order.
func (j *Journal) Entries() ([]Entry, error) {
	if j == nil {
		return nil, ErrClosed
	}
	j.mu.Lock()
	defer j.mu.Unlock()
	if j.db == nil {
		return nil, ErrClosed
	}
	entries, err := listEntries(j.db, `SELECT `+commandColumns+` FROM commands ORDER BY environment_sequence`)
	if err != nil {
		return nil, err
	}
	if err = j.verifyEntries(entries); err != nil {
		return nil, err
	}
	return entries, nil
}

// JournaledCommands returns deferable entries in contiguous journal order.
// The return coordinator must install/rebuild its overlay before exposing
// protocol-level pending-return acceptance. Authority attempts are never a queue.
func (j *Journal) JournaledCommands() ([]Entry, error) {
	if j == nil {
		return nil, ErrClosed
	}
	j.mu.Lock()
	defer j.mu.Unlock()
	if j.db == nil {
		return nil, ErrClosed
	}
	entries, err := listEntries(j.db, `SELECT `+commandColumns+` FROM commands WHERE state='journaled' ORDER BY journal_position`)
	if err != nil {
		return nil, err
	}
	if err = j.verifyEntries(entries); err != nil {
		return nil, err
	}
	return entries, nil
}

// StageBlob streams exactly declaredSize bytes into a private immutable
// content-addressed file, fsyncs it and its directory, then records the durable
// reference. A crash between the file link and DB commit leaves only a verified
// orphan that an exact retry can adopt; no command may reference it prematurely.
func (j *Journal) StageBlob(reader io.Reader, declaredSize int64) (StagedBlob, error) {
	if j == nil {
		return StagedBlob{}, ErrClosed
	}
	if reader == nil || declaredSize < 0 || declaredSize > maxBlobSize {
		return StagedBlob{}, ErrBlobLength
	}
	j.mu.Lock()
	defer j.mu.Unlock()
	if j.db == nil {
		return StagedBlob{}, ErrClosed
	}
	temporary, err := os.CreateTemp(j.blobsDir, ".tmp-")
	if err != nil {
		return StagedBlob{}, err
	}
	temporaryPath := temporary.Name()
	defer func() { _ = os.Remove(temporaryPath) }()
	digestHash := sha256.New()
	written, copyErr := io.Copy(io.MultiWriter(temporary, digestHash), io.LimitReader(reader, declaredSize+1))
	if copyErr == nil && written != declaredSize {
		copyErr = ErrBlobLength
	}
	if copyErr == nil {
		copyErr = temporary.Chmod(0o400)
	}
	if copyErr == nil {
		copyErr = temporary.Sync()
	}
	closeErr := temporary.Close()
	if copyErr != nil || closeErr != nil {
		return StagedBlob{}, errors.Join(copyErr, closeErr)
	}
	digest := digestPrefix + hex.EncodeToString(digestHash.Sum(nil))
	name := strings.TrimPrefix(digest, digestPrefix)
	path := filepath.Join(j.blobsDir, name)
	if err = os.Link(temporaryPath, path); err != nil && !errors.Is(err, os.ErrExist) {
		return StagedBlob{}, fmt.Errorf("wipdjournal: publish staged blob: %w", err)
	}
	// The temporary inode was synced before linking. Sync and re-verify the
	// published path on both new-link and orphan/retry paths, then always sync
	// the directory entry before a database transaction can reference it.
	if err = j.syncBlobFile(path, digest, declaredSize); err == nil {
		err = j.syncBlobDirectory(j.blobsDir)
	}
	if err != nil {
		return StagedBlob{}, fmt.Errorf("wipdjournal: publish staged blob: %w", err)
	}
	tx, err := j.db.BeginTx(context.Background(), nil)
	if err != nil {
		return StagedBlob{}, err
	}
	defer func() { _ = tx.Rollback() }()
	if _, err = tx.Exec(`INSERT INTO staged_blobs(digest, byte_length) VALUES(?, ?) ON CONFLICT(digest) DO NOTHING`, digest, declaredSize); err != nil {
		return StagedBlob{}, err
	}
	var storedSize int64
	if err = tx.QueryRow(`SELECT byte_length FROM staged_blobs WHERE digest=?`, digest).Scan(&storedSize); err != nil || storedSize != declaredSize {
		return StagedBlob{}, fmt.Errorf("%w: staged blob metadata conflict", ErrInvalidJournal)
	}
	if err = tx.Commit(); err != nil {
		return StagedBlob{}, err
	}
	return StagedBlob{Digest: digest, Size: declaredSize}, nil
}

// OpenBlob verifies the complete immutable file before returning a read handle.
func (j *Journal) OpenBlob(digest string) (io.ReadCloser, int64, error) {
	if j == nil {
		return nil, 0, ErrClosed
	}
	j.mu.Lock()
	defer j.mu.Unlock()
	if j.db == nil {
		return nil, 0, ErrClosed
	}
	if !validDigest(digest) {
		return nil, 0, ErrNotFound
	}
	var size int64
	if err := j.db.QueryRow(`SELECT byte_length FROM staged_blobs WHERE digest=?`, digest).Scan(&size); errors.Is(err, sql.ErrNoRows) {
		return nil, 0, ErrNotFound
	} else if err != nil {
		return nil, 0, err
	}
	path := filepath.Join(j.blobsDir, strings.TrimPrefix(digest, digestPrefix))
	if err := verifyBlobFile(path, digest, size); err != nil {
		return nil, 0, fmt.Errorf("%w: staged blob integrity check: %v", ErrInvalidJournal, err)
	}
	before, err := os.Lstat(path)
	if err != nil {
		return nil, 0, err
	}
	file, err := os.Open(path)
	if err != nil {
		return nil, 0, err
	}
	after, err := file.Stat()
	if err != nil || !os.SameFile(before, after) {
		_ = file.Close()
		return nil, 0, ErrInvalidJournal
	}
	return file, size, nil
}

func validateInput(identity Identity, input CommandInput) (operation.DeliveryClass, string, error) {
	if !identityPattern.MatchString(input.ID) {
		return "", "", ErrInvalidCommand
	}
	correlation := input.CorrelationCommandID
	if input.CausationCommandID == "" && correlation == "" {
		correlation = input.ID
	}
	if input.Request.Context.Repo != identity.RepoID {
		return "", "", ErrInvalidCommand
	}
	for _, definition := range operation.Catalogue() {
		metadata := definition.Metadata()
		if metadata.Operation != input.Request.Operation {
			continue
		}
		if metadata.Access != operation.AccessMutation || metadata.Delivery == operation.DeliveryNone || definition.ValidateRequest(input.Request) != nil {
			return "", "", ErrInvalidCommand
		}
		candidate := operation.Command{
			ID: input.ID, AuthorityDomainID: identity.DomainID,
			ExpectedAuthorityEpoch: identity.AuthorityEpoch, EnvironmentID: identity.EnvironmentID,
			EnvironmentSequence: 1, ActedAt: "2000-01-01T00:00:00Z", CausationCommandID: input.CausationCommandID,
			CorrelationCommandID: correlation, Request: input.Request,
		}
		if _, err := candidate.CanonicalBytes(); err != nil {
			return "", "", fmt.Errorf("%w: %v", ErrInvalidCommand, err)
		}
		return metadata.Delivery, correlation, nil
	}
	return "", "", ErrInvalidCommand
}

func sameIntent(entry Entry, identity Identity, input CommandInput, correlation string) bool {
	candidate := operation.Command{
		ID: input.ID, AuthorityDomainID: identity.DomainID,
		ExpectedAuthorityEpoch: identity.AuthorityEpoch, EnvironmentID: identity.EnvironmentID,
		EnvironmentSequence: entry.EnvironmentSeq, ActedAt: entry.Command.ActedAt,
		CausationCommandID: input.CausationCommandID, CorrelationCommandID: correlation,
		Request: input.Request,
	}
	encoded, err := candidate.CanonicalBytes()
	return err == nil && bytes.Equal(encoded, entry.CanonicalBytes)
}

func (j *Journal) verifyCommandBlobs(blobs []operation.BlobInput) error {
	for _, blob := range blobs {
		if !validDigest(blob.Digest) || blob.Size < 0 {
			return ErrInvalidCommand
		}
		var size int64
		err := j.db.QueryRow(`SELECT byte_length FROM staged_blobs WHERE digest=?`, blob.Digest).Scan(&size)
		if errors.Is(err, sql.ErrNoRows) || err == nil && size != blob.Size {
			return fmt.Errorf("%w: command references an unstaged blob", ErrInvalidCommand)
		}
		if err != nil {
			return err
		}
		if err := verifyBlobFile(filepath.Join(j.blobsDir, strings.TrimPrefix(blob.Digest, digestPrefix)), blob.Digest, size); err != nil {
			return fmt.Errorf("%w: staged command blob failed integrity check", ErrInvalidJournal)
		}
	}
	return nil
}

func (j *Journal) verifyEntries(entries []Entry) error {
	for _, entry := range entries {
		if err := j.verifyEntry(entry); err != nil {
			return err
		}
	}
	return nil
}

func (j *Journal) verifyEntry(entry Entry) error {
	if entry.Command.AuthorityDomainID != j.identity.DomainID || entry.Command.ExpectedAuthorityEpoch != j.identity.AuthorityEpoch ||
		entry.Command.EnvironmentID != j.identity.EnvironmentID || entry.Command.Request.Context.Repo != j.identity.RepoID {
		return ErrInvalidJournal
	}
	input := CommandInput{
		ID: entry.Command.ID, CausationCommandID: entry.Command.CausationCommandID,
		CorrelationCommandID: entry.Command.CorrelationCommandID, Request: entry.Command.Request,
	}
	delivery, _, err := validateInput(j.identity, input)
	if err != nil || delivery != entry.Delivery {
		return ErrInvalidJournal
	}
	if delivery == operation.DeliveryAuthority {
		if entry.JournalPosition != 0 || entry.State != StateAttemptPrepared {
			return ErrInvalidJournal
		}
	} else if entry.JournalPosition == 0 || entry.State != StateJournaled {
		return ErrInvalidJournal
	}
	return j.verifyCommandBlobs(entry.Command.Request.Blobs)
}

func lookup(db *sql.DB, commandID string) (Entry, error) {
	if !identityPattern.MatchString(commandID) {
		return Entry{}, ErrNotFound
	}
	return scanEntry(db.QueryRow(`SELECT `+commandColumns+` FROM commands WHERE command_id=?`, commandID))
}

func listEntries(db *sql.DB, query string) ([]Entry, error) {
	rows, err := db.Query(query)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	var entries []Entry
	for rows.Next() {
		entry, scanErr := scanEntry(rows)
		if scanErr != nil {
			return nil, scanErr
		}
		entries = append(entries, entry)
	}
	if err = rows.Err(); err != nil {
		return nil, err
	}
	if entries == nil {
		entries = []Entry{}
	}
	return entries, nil
}

type scanner interface {
	Scan(...any) error
}

func scanEntry(row scanner) (Entry, error) {
	var entry Entry
	var sequence int64
	var position sql.NullInt64
	var encoded []byte
	var delivery, state string
	if err := row.Scan(&entry.Command.ID, &sequence, &position, &entry.RequestHash, &encoded, &delivery, &state); errors.Is(err, sql.ErrNoRows) {
		return Entry{}, ErrNotFound
	} else if err != nil {
		return Entry{}, err
	}
	if sequence <= 0 || position.Valid && position.Int64 <= 0 {
		return Entry{}, ErrInvalidJournal
	}
	command, err := operation.DecodeCanonicalCommand(encoded)
	if err != nil {
		return Entry{}, fmt.Errorf("%w: decode command: %v", ErrInvalidJournal, err)
	}
	hash, err := command.RequestHash()
	if err != nil || hash != entry.RequestHash || command.ID != entry.Command.ID || command.EnvironmentSequence != uint64(sequence) {
		return Entry{}, ErrInvalidJournal
	}
	entry.Command = command
	entry.CanonicalBytes = bytes.Clone(encoded)
	entry.EnvironmentSeq = uint64(sequence)
	entry.Delivery = operation.DeliveryClass(delivery)
	entry.State = State(state)
	if position.Valid {
		entry.JournalPosition = uint64(position.Int64)
	}
	return entry, nil
}

func cloneEntry(entry Entry) Entry {
	entry.CanonicalBytes = bytes.Clone(entry.CanonicalBytes)
	entry.Command.Request = cloneRequest(entry.Command.Request)
	return entry
}

func cloneRequest(request operation.Request) operation.Request {
	request.Blobs = append([]operation.BlobInput(nil), request.Blobs...)
	if request.Claim != nil {
		claim := *request.Claim
		request.Claim = &claim
	}
	return request
}

func validIdentity(identity Identity) bool {
	return identityPattern.MatchString(identity.RepoID) && identityPattern.MatchString(identity.DomainID) &&
		identity.AuthorityEpoch > 0 && identity.AuthorityEpoch <= uint64(^uint64(0)>>1) && identityPattern.MatchString(identity.EnvironmentID)
}

func validDigest(digest string) bool {
	return strings.HasPrefix(digest, digestPrefix) && blobNamePattern.MatchString(strings.TrimPrefix(digest, digestPrefix))
}

func verifyBlobFile(path, digest string, expectedSize int64) error {
	file, err := openVerifiedBlob(path, digest, expectedSize)
	if err != nil {
		return err
	}
	return file.Close()
}

func syncVerifiedBlobFile(path, digest string, expectedSize int64) error {
	file, err := openVerifiedBlob(path, digest, expectedSize)
	if err != nil {
		return err
	}
	return errors.Join(file.Sync(), file.Close())
}

func openVerifiedBlob(path, digest string, expectedSize int64) (*os.File, error) {
	if expectedSize < 0 || expectedSize > maxBlobSize || !validDigest(digest) {
		return nil, ErrInvalidJournal
	}
	info, err := os.Lstat(path)
	if err != nil || !info.Mode().IsRegular() || info.Mode().Perm() != 0o400 || info.Size() != expectedSize {
		return nil, ErrInvalidJournal
	}
	file, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	opened, err := file.Stat()
	if err != nil || !os.SameFile(info, opened) {
		_ = file.Close()
		return nil, ErrInvalidJournal
	}
	h := sha256.New()
	length, err := io.Copy(h, io.LimitReader(file, expectedSize+1))
	if err != nil || length != expectedSize || digestPrefix+hex.EncodeToString(h.Sum(nil)) != digest {
		_ = file.Close()
		return nil, ErrInvalidJournal
	}
	return file, nil
}

func installSchema(db *sql.DB, identity Identity) error {
	tx, err := db.BeginTx(context.Background(), nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	for _, statement := range []string{
		`CREATE TABLE schema_migrations(version INTEGER PRIMARY KEY CHECK(version=1), name TEXT NOT NULL CHECK(name='durable-client-command-journal')) STRICT`,
		`CREATE TABLE environment_state(
			singleton INTEGER PRIMARY KEY CHECK(singleton=1),
			repo_id TEXT NOT NULL, domain_id TEXT NOT NULL,
			authority_epoch INTEGER NOT NULL CHECK(authority_epoch>0), environment_id TEXT NOT NULL,
			next_environment_sequence INTEGER NOT NULL CHECK(next_environment_sequence>0),
			next_journal_position INTEGER NOT NULL CHECK(next_journal_position>0)
		) STRICT`,
		`CREATE TRIGGER environment_identity_immutable BEFORE UPDATE OF repo_id, domain_id, authority_epoch, environment_id ON environment_state
		BEGIN SELECT RAISE(ABORT, 'immutable Environment identity'); END`,
		`CREATE TRIGGER environment_counters_increment BEFORE UPDATE OF next_environment_sequence, next_journal_position ON environment_state
		WHEN NEW.next_environment_sequence != OLD.next_environment_sequence+1 OR
			(NEW.next_journal_position != OLD.next_journal_position AND NEW.next_journal_position != OLD.next_journal_position+1)
		BEGIN SELECT RAISE(ABORT, 'invalid Environment journal counter'); END`,
		`CREATE TABLE commands(
			command_id TEXT PRIMARY KEY,
			environment_sequence INTEGER NOT NULL UNIQUE CHECK(environment_sequence>0),
			journal_position INTEGER UNIQUE CHECK(journal_position IS NULL OR journal_position>0),
			request_hash TEXT NOT NULL CHECK(length(request_hash)=71),
			canonical_bytes BLOB NOT NULL,
			delivery TEXT NOT NULL CHECK(delivery IN ('authority','claim','provisional','capture','environment')),
			state TEXT NOT NULL CHECK(state IN ('attempt-prepared','journaled')),
			CHECK((delivery='authority' AND journal_position IS NULL AND state='attempt-prepared') OR
				(delivery!='authority' AND journal_position IS NOT NULL AND state='journaled'))
		) STRICT, WITHOUT ROWID`,
		`CREATE INDEX commands_pending_order ON commands(state, journal_position)`,
		`CREATE TRIGGER command_identity_immutable BEFORE UPDATE OF command_id, environment_sequence, journal_position, request_hash, canonical_bytes, delivery ON commands
		BEGIN SELECT RAISE(ABORT, 'immutable command identity'); END`,
		`CREATE TRIGGER command_no_delete BEFORE DELETE ON commands
		BEGIN SELECT RAISE(ABORT, 'immutable command journal'); END`,
		`CREATE TABLE staged_blobs(
			digest TEXT PRIMARY KEY CHECK(length(digest)=71),
			byte_length INTEGER NOT NULL CHECK(byte_length>=0 AND byte_length<=1099511627776)
		) STRICT, WITHOUT ROWID`,
		`CREATE TRIGGER staged_blob_no_update BEFORE UPDATE ON staged_blobs
		BEGIN SELECT RAISE(ABORT, 'immutable staged blob'); END`,
		`CREATE TRIGGER staged_blob_no_delete BEFORE DELETE ON staged_blobs
		BEGIN SELECT RAISE(ABORT, 'retained staged blob'); END`,
		`INSERT INTO schema_migrations(version, name) VALUES(1, 'durable-client-command-journal')`,
		`PRAGMA user_version=1`,
	} {
		if _, err = tx.Exec(statement); err != nil {
			return err
		}
	}
	if _, err = tx.Exec(`INSERT INTO environment_state(singleton, repo_id, domain_id, authority_epoch, environment_id, next_environment_sequence, next_journal_position)
		VALUES(1, ?, ?, ?, ?, 1, 1)`, identity.RepoID, identity.DomainID, identity.AuthorityEpoch, identity.EnvironmentID); err != nil {
		return err
	}
	return tx.Commit()
}

func checkIdentity(db *sql.DB, identity Identity) error {
	var version int
	if err := db.QueryRow(`PRAGMA user_version`).Scan(&version); err != nil || version != schemaVersion {
		return fmt.Errorf("schema version %d: %v", version, err)
	}
	var repoID, domainID, environmentID string
	var epoch int64
	if err := db.QueryRow(`SELECT repo_id, domain_id, authority_epoch, environment_id FROM environment_state WHERE singleton=1`).Scan(&repoID, &domainID, &epoch, &environmentID); err != nil {
		return err
	}
	if repoID != identity.RepoID || domainID != identity.DomainID || epoch != int64(identity.AuthorityEpoch) || environmentID != identity.EnvironmentID {
		return ErrInvalidIdentity
	}
	var migration string
	if err := db.QueryRow(`SELECT name FROM schema_migrations WHERE version=1`).Scan(&migration); err != nil || migration != "durable-client-command-journal" {
		return fmt.Errorf("schema migration marker: %v", err)
	}
	return checkSchemaObjects(db)
}

func checkSchemaObjects(db *sql.DB) error {
	expected := map[string]string{
		"schema_migrations": "table", "environment_state": "table", "environment_identity_immutable": "trigger",
		"environment_counters_increment": "trigger", "commands": "table", "commands_pending_order": "index",
		"command_identity_immutable": "trigger", "command_no_delete": "trigger", "staged_blobs": "table",
		"staged_blob_no_update": "trigger", "staged_blob_no_delete": "trigger",
	}
	rows, err := db.Query(`SELECT type, name FROM sqlite_master WHERE name NOT LIKE 'sqlite_%'`)
	if err != nil {
		return err
	}
	defer func() { _ = rows.Close() }()
	for rows.Next() {
		var kind, name string
		if err = rows.Scan(&kind, &name); err != nil {
			return err
		}
		if expected[name] != kind {
			return fmt.Errorf("unexpected schema object %s", name)
		}
		delete(expected, name)
	}
	if err = rows.Err(); err != nil {
		return err
	}
	if len(expected) != 0 {
		return fmt.Errorf("missing schema objects: %v", expected)
	}
	return nil
}

func checkDatabase(db *sql.DB, identity Identity, blobDir string) error {
	if err := checkIdentity(db, identity); err != nil {
		return err
	}
	var integrity string
	if err := db.QueryRow(`PRAGMA quick_check`).Scan(&integrity); err != nil || integrity != "ok" {
		return fmt.Errorf("SQLite integrity check %q: %v", integrity, err)
	}
	entries, err := listEntries(db, `SELECT `+commandColumns+` FROM commands ORDER BY environment_sequence`)
	if err != nil {
		return err
	}
	nextPosition := uint64(1)
	for index, entry := range entries {
		if entry.EnvironmentSeq != uint64(index+1) || entry.Command.AuthorityDomainID != identity.DomainID ||
			entry.Command.ExpectedAuthorityEpoch != identity.AuthorityEpoch || entry.Command.EnvironmentID != identity.EnvironmentID {
			return ErrInvalidJournal
		}
		input := CommandInput{
			ID: entry.Command.ID, CausationCommandID: entry.Command.CausationCommandID,
			CorrelationCommandID: entry.Command.CorrelationCommandID, Request: entry.Command.Request,
		}
		delivery, _, inputErr := validateInput(identity, input)
		if inputErr != nil || delivery != entry.Delivery {
			return ErrInvalidJournal
		}
		if delivery == operation.DeliveryAuthority {
			if entry.JournalPosition != 0 || entry.State != StateAttemptPrepared {
				return ErrInvalidJournal
			}
		} else {
			if entry.JournalPosition != nextPosition || entry.State != StateJournaled {
				return ErrInvalidJournal
			}
			nextPosition++
		}
		if err = verifyBlobReferences(db, blobDir, entry.Command.Request.Blobs); err != nil {
			return err
		}
	}
	var nextSequence, storedPosition int64
	if err = db.QueryRow(`SELECT next_environment_sequence, next_journal_position FROM environment_state WHERE singleton=1`).Scan(&nextSequence, &storedPosition); err != nil {
		return err
	}
	if nextSequence != int64(len(entries))+1 || storedPosition != int64(nextPosition) {
		return ErrInvalidJournal
	}
	return checkBlobFiles(db, blobDir)
}

func verifyBlobReferences(db *sql.DB, blobDir string, blobs []operation.BlobInput) error {
	for _, blob := range blobs {
		var size int64
		if !validDigest(blob.Digest) || blob.Size < 0 || db.QueryRow(`SELECT byte_length FROM staged_blobs WHERE digest=?`, blob.Digest).Scan(&size) != nil || size != blob.Size {
			return ErrInvalidJournal
		}
		if err := verifyBlobFile(filepath.Join(blobDir, strings.TrimPrefix(blob.Digest, digestPrefix)), blob.Digest, size); err != nil {
			return err
		}
	}
	return nil
}

func checkBlobFiles(db *sql.DB, blobDir string) error {
	entries, err := os.ReadDir(blobDir)
	if err != nil {
		return err
	}
	removedTemporary := false
	for _, entry := range entries {
		path := filepath.Join(blobDir, entry.Name())
		info, statErr := os.Lstat(path)
		if statErr != nil {
			return statErr
		}
		if strings.HasPrefix(entry.Name(), ".tmp-") {
			if !info.Mode().IsRegular() || info.Mode().Perm()&0o077 != 0 {
				return ErrInvalidJournal
			}
			if err = os.Remove(path); err != nil {
				return err
			}
			removedTemporary = true
			continue
		}
		if !blobNamePattern.MatchString(entry.Name()) || !info.Mode().IsRegular() || info.Mode().Perm() != 0o400 {
			return ErrInvalidJournal
		}
		digest := digestPrefix + entry.Name()
		if err = verifyBlobFile(path, digest, info.Size()); err != nil {
			return err
		}
	}
	if removedTemporary {
		if err = syncDirectory(blobDir); err != nil {
			return err
		}
	}
	rows, err := db.Query(`SELECT digest, byte_length FROM staged_blobs ORDER BY digest`)
	if err != nil {
		return err
	}
	defer func() { _ = rows.Close() }()
	for rows.Next() {
		var digest string
		var size int64
		if err = rows.Scan(&digest, &size); err != nil {
			return err
		}
		if !validDigest(digest) || verifyBlobFile(filepath.Join(blobDir, strings.TrimPrefix(digest, digestPrefix)), digest, size) != nil {
			return ErrInvalidJournal
		}
	}
	return rows.Err()
}

func connect(path string) (*sql.DB, error) {
	location := url.URL{Scheme: "file", Path: path}
	query := location.Query()
	query.Set("mode", "rw")
	query.Add("_pragma", "foreign_keys(1)")
	query.Add("_pragma", "busy_timeout(5000)")
	query.Add("_pragma", "synchronous(FULL)")
	query.Add("_pragma", "journal_mode(DELETE)")
	location.RawQuery = query.Encode()
	db, err := sql.Open("sqlite", location.String())
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(1)
	if err = db.PingContext(context.Background()); err != nil {
		_ = db.Close()
		return nil, err
	}
	return db, nil
}

func prepareDatabaseFile(path string) (bool, error) {
	_, err := os.Lstat(path)
	if err == nil {
		if err = verifyPrivateRegular(path, 0o600); err != nil {
			return false, fmt.Errorf("wipdjournal: unsafe database file: %w", err)
		}
		return false, nil
	}
	if !errors.Is(err, os.ErrNotExist) {
		return false, err
	}
	file, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_RDWR, 0o600)
	if err != nil {
		return false, err
	}
	if err = file.Chmod(0o600); err == nil {
		err = file.Sync()
	}
	closeErr := file.Close()
	if err != nil || closeErr != nil {
		return false, errors.Join(err, closeErr)
	}
	if err = syncDirectory(filepath.Dir(path)); err != nil {
		return false, err
	}
	return true, nil
}

// makePrivatePath creates missing path components from the nearest existing
// ancestor down and syncs each parent after adding a directory entry. This
// makes a newly-created profile root durable before any journal commit can be
// exposed to its caller.
func makePrivatePath(path string) error {
	var missing []string
	for current := filepath.Clean(path); ; current = filepath.Dir(current) {
		info, err := os.Lstat(current)
		if err == nil {
			if info.Mode()&os.ModeSymlink != 0 || !info.IsDir() {
				return ErrInvalidIdentity
			}
			break
		}
		if !errors.Is(err, os.ErrNotExist) {
			return err
		}
		parent := filepath.Dir(current)
		if parent == current {
			return ErrInvalidIdentity
		}
		missing = append(missing, current)
	}
	for index := len(missing) - 1; index >= 0; index-- {
		if err := os.Mkdir(missing[index], 0o700); err != nil {
			return err
		}
		if err := syncDirectory(filepath.Dir(missing[index])); err != nil {
			return err
		}
	}
	return syncDirectory(filepath.Dir(path))
}

func verifyPrivateRegular(path string, mode os.FileMode) error {
	info, err := os.Lstat(path)
	if err != nil || !info.Mode().IsRegular() || info.Mode().Perm() != mode {
		return ErrInvalidJournal
	}
	var stat unix.Stat_t
	if err = unix.Lstat(path, &stat); err != nil || stat.Uid != uint32(os.Geteuid()) {
		return ErrInvalidJournal
	}
	return nil
}

func verifyPrivateDirectory(path string) error {
	info, err := os.Lstat(path)
	if err != nil || info.Mode()&os.ModeSymlink != 0 || !info.IsDir() || info.Mode().Perm() != 0o700 {
		return fmt.Errorf("%w: root is not an owner-only writable directory", ErrInvalidIdentity)
	}
	var stat unix.Stat_t
	if err = unix.Lstat(path, &stat); err != nil || stat.Uid != uint32(os.Geteuid()) {
		return fmt.Errorf("%w: root is not owned by the current user", ErrInvalidIdentity)
	}
	return nil
}

func makePrivateDirectory(path string) error {
	if err := os.Mkdir(path, 0o700); err != nil && !errors.Is(err, os.ErrExist) {
		return err
	}
	if err := verifyPrivateDirectory(path); err != nil {
		return err
	}
	return syncDirectory(filepath.Dir(path))
}

func acquire(root string) (*os.File, error) {
	path := filepath.Join(root, lockName)
	if info, err := os.Lstat(path); err == nil && !info.Mode().IsRegular() {
		return nil, ErrInvalidJournal
	} else if err != nil && !errors.Is(err, os.ErrNotExist) {
		return nil, err
	}
	fd, err := unix.Open(path, unix.O_CREAT|unix.O_RDWR|unix.O_CLOEXEC|unix.O_NOFOLLOW, 0o600)
	if err != nil {
		return nil, err
	}
	file := os.NewFile(uintptr(fd), path)
	if err = unix.Flock(int(file.Fd()), unix.LOCK_EX|unix.LOCK_NB); err != nil {
		_ = file.Close()
		if errors.Is(err, unix.EWOULDBLOCK) || errors.Is(err, unix.EAGAIN) {
			return nil, ErrHeld
		}
		return nil, err
	}
	if err = verifyPrivateRegular(path, 0o600); err != nil {
		_ = file.Close()
		return nil, err
	}
	opened, err := file.Stat()
	if err != nil {
		_ = file.Close()
		return nil, err
	}
	onPath, err := os.Lstat(path)
	if err != nil || !os.SameFile(opened, onPath) {
		_ = file.Close()
		return nil, ErrInvalidJournal
	}
	return file, nil
}

func syncDirectory(path string) error {
	directory, err := os.Open(path)
	if err != nil {
		return err
	}
	return errors.Join(directory.Sync(), directory.Close())
}
