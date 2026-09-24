// Package authoritystore persists fresh authority domain identity, Repo
// membership, artifact keys, and Environment certificate registries. It does
// not open or import the legacy WIP store.
package authoritystore

import (
	"context"
	"crypto/ed25519"
	"crypto/sha256"
	"crypto/x509"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"sync"

	"golang.org/x/sys/unix"
	"modernc.org/sqlite"
)

const schemaVersion = 6

type schemaObject struct {
	name string
	kind string
	sql  string
}

var baselineSchema = []schemaObject{
	{name: "schema_migrations", kind: "table", sql: `CREATE TABLE schema_migrations (version INTEGER PRIMARY KEY CHECK (version = 1), name TEXT NOT NULL CHECK (name = 'baseline')) STRICT`},
	{name: "domains", kind: "table", sql: `CREATE TABLE domains (
  domain_id TEXT PRIMARY KEY,
  owner_public_key BLOB NOT NULL CHECK (length(owner_public_key) = 32),
  owner_key_id TEXT NOT NULL,
  initial_epoch INTEGER NOT NULL CHECK (initial_epoch > 0),
  active_epoch INTEGER NOT NULL CHECK (active_epoch > 0),
  CHECK (active_epoch >= initial_epoch)
) STRICT`},
	{name: "owner_root_immutable", kind: "trigger", sql: `CREATE TRIGGER owner_root_immutable BEFORE UPDATE OF domain_id, owner_public_key, owner_key_id, initial_epoch ON domains
BEGIN SELECT RAISE(ABORT, 'immutable owner root'); END`},
	{name: "domain_no_delete", kind: "trigger", sql: `CREATE TRIGGER domain_no_delete BEFORE DELETE ON domains
BEGIN SELECT RAISE(ABORT, 'immutable domain'); END`},
	{name: "epoch_promotions", kind: "table", sql: `CREATE TABLE epoch_promotions (
  domain_id TEXT NOT NULL REFERENCES domains(domain_id) ON DELETE RESTRICT,
  from_epoch INTEGER NOT NULL CHECK (from_epoch > 0),
  to_epoch INTEGER NOT NULL CHECK (to_epoch = from_epoch + 1),
  prior_fence_digest TEXT NOT NULL,
  promotion_proof_digest TEXT NOT NULL,
  PRIMARY KEY (domain_id, to_epoch),
  UNIQUE (domain_id, from_epoch)
) STRICT`},
	{name: "promotion_immutable", kind: "trigger", sql: `CREATE TRIGGER promotion_immutable BEFORE UPDATE ON epoch_promotions
BEGIN SELECT RAISE(ABORT, 'immutable promotion'); END`},
	{name: "promotion_no_delete", kind: "trigger", sql: `CREATE TRIGGER promotion_no_delete BEFORE DELETE ON epoch_promotions
BEGIN SELECT RAISE(ABORT, 'immutable promotion'); END`},
	{name: "repo_memberships", kind: "table", sql: `CREATE TABLE repo_memberships (
  repo_id TEXT PRIMARY KEY,
  domain_id TEXT NOT NULL REFERENCES domains(domain_id) ON DELETE RESTRICT
) STRICT`},
	{name: "membership_immutable", kind: "trigger", sql: `CREATE TRIGGER membership_immutable BEFORE UPDATE ON repo_memberships
BEGIN SELECT RAISE(ABORT, 'immutable membership'); END`},
	{name: "membership_no_delete", kind: "trigger", sql: `CREATE TRIGGER membership_no_delete BEFORE DELETE ON repo_memberships
BEGIN SELECT RAISE(ABORT, 'immutable membership'); END`},
}

var (
	// ErrHeld means another writable store handle owns this root's lease.
	ErrHeld = errors.New("authoritystore: writer lock held")
	// ErrExists means a domain or Repo identity is already registered.
	ErrExists = errors.New("authoritystore: identity or membership already exists")
	// ErrNotFound means the requested domain or Repo has no membership.
	ErrNotFound = errors.New("authoritystore: domain not found")
	// ErrInvalidStore means the root cannot be safely opened as this schema.
	ErrInvalidStore = errors.New("authoritystore: missing, incomplete, or unsupported store")
	ulid            = regexp.MustCompile(`^[0-7][0-9A-HJKMNP-TV-Z]{25}$`)
)

// Domain is the immutable trust root and current authority epoch. The public
// key is copied on both input and output; no private key is accepted or stored.
type Domain struct {
	ID             string
	OwnerPublicKey ed25519.PublicKey
	OwnerKeyID     string
	ActiveEpoch    uint64
}

// Store holds one exclusive host-local writer lease for the lifetime of its DB.
// Close releases the lease; a process exit releases it even without Close.
type Store struct {
	mu     sync.Mutex
	db     *sql.DB
	lock   *os.File
	blobs  string
	owners map[string]bool
}

// CreateEmpty creates a dedicated, absent root and installs each schema
// version before returning. Failed initialization leaves an incomplete root
// that must be inspected and explicitly removed, never completed by OpenExisting.
func CreateEmpty(root string) (*Store, error) {
	path, err := cleanRoot(root)
	if err != nil {
		return nil, err
	}
	if err := os.Mkdir(path, 0o700); err != nil {
		return nil, fmt.Errorf("authoritystore: create root: %w", err)
	}
	lock, err := acquire(path)
	if err != nil {
		return nil, err
	}
	file := filepath.Join(path, "authority.db")
	db, err := connect(file, "rwc", true)
	if err == nil {
		// SQLite uses the process umask for newly created files. Keep the DB
		// private even when the caller's umask is permissive.
		err = os.Chmod(file, 0o600)
		if err == nil {
			err = installBaseline(db)
			if err == nil {
				err = installStep3(db)
				if err == nil {
					err = installStep4(db)
					if err == nil {
						err = installStep6(db)
						if err == nil {
							err = installStep5(db)
							if err == nil {
								err = installStep7(db)
							}
						}
					}
				}
			}
		}
		if err == nil {
			err = initBlobDir(path)
			if err == nil {
				err = checkSchema(db)
			}
		}
	}
	if err != nil {
		if db != nil {
			_ = db.Close()
		}
		_ = lock.Close()
		return nil, fmt.Errorf("authoritystore: initialize: %w", err)
	}
	return &Store{db: db, lock: lock, blobs: filepath.Join(path, "blobs"), owners: make(map[string]bool)}, nil
}

// installBaseline commits schema objects and both version markers together.
func installBaseline(db *sql.DB) error {
	tx, err := db.Begin()
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	for _, object := range baselineSchema {
		if _, err := tx.Exec(object.sql); err != nil {
			return err
		}
	}
	for _, stmt := range []string{
		`INSERT INTO schema_migrations(version, name) VALUES (1, 'baseline')`,
		`PRAGMA user_version = 1`,
	} {
		if _, err := tx.Exec(stmt); err != nil {
			return err
		}
	}
	return tx.Commit()
}

// OpenExisting never creates, bootstraps, or migrates a store.
func OpenExisting(root string) (*Store, error) {
	path, err := cleanRoot(root)
	if err != nil {
		return nil, err
	}
	if err := checkRoot(path); err != nil {
		return nil, err
	}
	lock, err := acquire(path)
	if err != nil {
		return nil, err
	}
	defer func() {
		if lock != nil {
			_ = lock.Close()
		}
	}()
	file := filepath.Join(path, "authority.db")
	if err := regularFile(file); err != nil {
		return nil, fmt.Errorf("%w: database: %v", ErrInvalidStore, err)
	}
	db, err := connect(file, "rw", false)
	if err != nil {
		return nil, fmt.Errorf("%w: %v", ErrInvalidStore, err)
	}
	if err := checkSchema(db); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("%w: %v", ErrInvalidStore, err)
	}
	if err := checkBlobFiles(db, filepath.Join(path, "blobs")); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("%w: blobs: %v", ErrInvalidStore, err)
	}
	if err := checkStep7Closure(db, path); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("%w: continuity closure: %v", ErrInvalidStore, err)
	}
	store := &Store{db: db, lock: lock, blobs: filepath.Join(path, "blobs"), owners: make(map[string]bool)}
	lock = nil
	return store, nil
}

// Close closes the DB and releases the writer lease. It is idempotent.
func (s *Store) Close() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.db == nil {
		return nil
	}
	err := s.db.Close()
	s.db = nil
	return errors.Join(err, s.lock.Close())
}

// BootstrapDomain atomically creates the domain and its first Repo membership.
// Setup must separately certify an authority artifact key before command
// admission; neither this method nor ordinary open authorizes operations.
func (s *Store) BootstrapDomain(ctx context.Context, d Domain, repoID string) error {
	if err := validDomain(d); err != nil {
		return err
	}
	if !ulid.MatchString(repoID) {
		return errors.New("authoritystore: invalid Repo ID")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.db == nil {
		return errors.New("authoritystore: closed")
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	if _, err = tx.ExecContext(ctx, `INSERT INTO domains(domain_id, owner_public_key, owner_key_id, initial_epoch, active_epoch) VALUES (?, ?, ?, ?, ?)`, d.ID, []byte(d.OwnerPublicKey), d.OwnerKeyID, d.ActiveEpoch, d.ActiveEpoch); err != nil {
		return writeError(err)
	}
	if _, err = tx.ExecContext(ctx, `INSERT INTO repo_memberships(repo_id, domain_id) VALUES (?, ?)`, repoID, d.ID); err != nil {
		return writeError(err)
	}
	return tx.Commit()
}

// AttachRepo only attaches to a command/event history-empty domain. Step 4's
// submission table is the durable history boundary and blocks later changes.
func (s *Store) AttachRepo(ctx context.Context, domainID, repoID string) error {
	if !ulid.MatchString(domainID) || !ulid.MatchString(repoID) {
		return errors.New("authoritystore: invalid domain or Repo ID")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.db == nil {
		return errors.New("authoritystore: closed")
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	var count int
	if err := tx.QueryRowContext(ctx, `SELECT count(*) FROM domains WHERE domain_id = ?`, domainID).Scan(&count); err != nil {
		return err
	}
	if count == 0 {
		return ErrNotFound
	}
	d, err := domainOwner(ctx, tx, domainID)
	if err != nil {
		return err
	}
	if err = checkWriteAdmission(ctx, tx, domainID, d.ActiveEpoch); err != nil {
		return err
	}
	var history int
	if err := tx.QueryRowContext(ctx, `SELECT count(*) FROM submissions WHERE domain_id = ?`, domainID).Scan(&history); err != nil {
		return err
	}
	if history != 0 {
		return ErrFenced
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO repo_memberships(repo_id, domain_id) VALUES (?, ?)`, repoID, domainID); err != nil {
		return writeError(err)
	}
	return tx.Commit()
}

// LookupDomain returns the persisted identity, not a caller-supplied owner or epoch.
func (s *Store) LookupDomain(ctx context.Context, domainID string) (Domain, error) {
	if !ulid.MatchString(domainID) {
		return Domain{}, errors.New("authoritystore: invalid domain ID")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.db == nil {
		return Domain{}, errors.New("authoritystore: closed")
	}
	var d Domain
	var epoch int64
	err := s.db.QueryRowContext(ctx, `SELECT domain_id, owner_public_key, owner_key_id, active_epoch FROM domains WHERE domain_id = ?`, domainID).Scan(&d.ID, &d.OwnerPublicKey, &d.OwnerKeyID, &epoch)
	if errors.Is(err, sql.ErrNoRows) {
		return Domain{}, ErrNotFound
	}
	if err != nil {
		return Domain{}, err
	}
	d.ActiveEpoch = uint64(epoch)
	return d, nil
}

// RepoDomain resolves globally exclusive membership.
func (s *Store) RepoDomain(ctx context.Context, repoID string) (string, error) {
	if !ulid.MatchString(repoID) {
		return "", errors.New("authoritystore: invalid Repo ID")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.db == nil {
		return "", errors.New("authoritystore: closed")
	}
	var domain string
	err := s.db.QueryRowContext(ctx, `SELECT domain_id FROM repo_memberships WHERE repo_id = ?`, repoID).Scan(&domain)
	if errors.Is(err, sql.ErrNoRows) {
		return "", ErrNotFound
	}
	return domain, err
}

func validDomain(d Domain) error {
	if !ulid.MatchString(d.ID) || d.ActiveEpoch == 0 || d.ActiveEpoch > 1<<63-1 || len(d.OwnerPublicKey) != ed25519.PublicKeySize {
		return errors.New("authoritystore: invalid domain identity, epoch or owner public key")
	}
	der, err := x509.MarshalPKIXPublicKey(d.OwnerPublicKey)
	if err != nil {
		return err
	}
	h := sha256.Sum256(der)
	if d.OwnerKeyID != "sha256:"+hex.EncodeToString(h[:]) {
		return errors.New("authoritystore: owner key ID does not match public key")
	}
	return nil
}

func writeError(err error) error {
	var sqliteErr *sqlite.Error
	if errors.As(err, &sqliteErr) && strings.Contains(err.Error(), "authority admission closed") {
		return ErrFenced
	}
	// Only PRIMARYKEY (1555) and UNIQUE (2067) denote a duplicate identity.
	if errors.As(err, &sqliteErr) && (sqliteErr.Code() == 1555 || sqliteErr.Code() == 2067) {
		return fmt.Errorf("%w: %v", ErrExists, err)
	}
	return err
}

func cleanRoot(root string) (string, error) {
	if root == "" {
		return "", errors.New("authoritystore: empty root")
	}
	path, err := filepath.Abs(root)
	if err != nil {
		return "", err
	}
	// Reject symlinks in the supplied path, including an existing root.
	for part := path; part != filepath.Dir(part); part = filepath.Dir(part) {
		info, err := os.Lstat(part)
		if err == nil && info.Mode()&os.ModeSymlink != 0 {
			return "", fmt.Errorf("authoritystore: symlink in root path: %s", part)
		}
		if err != nil && !errors.Is(err, os.ErrNotExist) {
			return "", err
		}
	}
	return path, nil
}

func checkRoot(root string) error {
	info, err := os.Lstat(root)
	if err != nil || !info.IsDir() || info.Mode().Perm() != 0o700 {
		return fmt.Errorf("%w: root missing or not private", ErrInvalidStore)
	}
	return nil
}

func regularFile(path string) error {
	info, err := os.Lstat(path)
	if err != nil {
		return err
	}
	if !info.Mode().IsRegular() || info.Mode().Perm()&0o077 != 0 {
		return errors.New("not a private regular file")
	}
	return nil
}

func acquire(root string) (*os.File, error) {
	path := filepath.Join(root, "writer.lock")
	if info, err := os.Lstat(path); err == nil && !info.Mode().IsRegular() {
		return nil, fmt.Errorf("%w: invalid lock file", ErrInvalidStore)
	} else if err != nil && !errors.Is(err, os.ErrNotExist) {
		return nil, err
	}
	fd, err := unix.Open(path, unix.O_CREAT|unix.O_RDWR|unix.O_CLOEXEC|unix.O_NOFOLLOW, 0o600)
	if err != nil {
		return nil, err
	}
	f := os.NewFile(uintptr(fd), path)
	if err := unix.Flock(int(f.Fd()), unix.LOCK_EX|unix.LOCK_NB); err != nil {
		_ = f.Close()
		if errors.Is(err, unix.EWOULDBLOCK) || errors.Is(err, unix.EAGAIN) {
			return nil, ErrHeld
		}
		return nil, err
	}
	if err := regularFile(path); err != nil {
		_ = f.Close()
		return nil, fmt.Errorf("%w: lock: %v", ErrInvalidStore, err)
	}
	opened, err := f.Stat()
	if err != nil {
		_ = f.Close()
		return nil, err
	}
	onPath, err := os.Lstat(path)
	if err != nil || !os.SameFile(opened, onPath) {
		_ = f.Close()
		return nil, fmt.Errorf("%w: lock file replaced", ErrInvalidStore)
	}
	return f, nil
}

func connect(path, mode string, create bool) (*sql.DB, error) {
	u := url.URL{Scheme: "file", Path: path}
	q := u.Query()
	q.Set("mode", mode)
	q.Add("_pragma", "foreign_keys(1)")
	q.Add("_pragma", "busy_timeout(5000)")
	q.Add("_pragma", "synchronous(FULL)")
	if create {
		q.Add("_pragma", "journal_mode(WAL)")
	}
	u.RawQuery = q.Encode()
	db, err := sql.Open("sqlite", u.String())
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(1)
	if err := db.Ping(); err != nil {
		_ = db.Close()
		return nil, err
	}
	return db, nil
}

func checkSchema(db *sql.DB) error { return checkSchemaVersion(db, schemaVersion) }

func checkSchemaVersion(db *sql.DB, expectedVersion int) error {
	var version int
	if err := db.QueryRow(`PRAGMA user_version`).Scan(&version); err != nil || version != expectedVersion {
		return fmt.Errorf("schema version %d (expected %d): %v", version, expectedVersion, err)
	}
	var name string
	if err := db.QueryRow(`SELECT name FROM schema_migrations WHERE version = 1`).Scan(&name); err != nil || name != "baseline" {
		return fmt.Errorf("migration marker: %v", err)
	}
	var n int
	if err := db.QueryRow(`SELECT count(*) FROM schema_migrations`).Scan(&n); err != nil || n != expectedVersion {
		return fmt.Errorf("migration count: %v", err)
	}
	var journal string
	if err := db.QueryRow(`PRAGMA journal_mode`).Scan(&journal); err != nil || journal != "wal" {
		return fmt.Errorf("journal mode %s: %v", journal, err)
	}
	expected := make(map[string]schemaObject, len(baselineSchema))
	for _, object := range baselineSchema {
		expected[object.name] = object
	}
	if expectedVersion >= 2 {
		expected["schema_migrations"] = step3MigrationMarker
		for _, object := range step3Schema {
			expected[object.name] = object
		}
		if err := db.QueryRow(`SELECT name FROM schema_migrations WHERE version = 2`).Scan(&name); err != nil || name != "environment-and-artifacts" {
			return fmt.Errorf("step 3 migration marker: %v", err)
		}
	}
	if expectedVersion >= 3 {
		expected["schema_migrations"] = step4MigrationMarker
		delete(expected, "environment_sequence_no_advance")
		for _, object := range step4Schema {
			expected[object.name] = object
		}
		if err := db.QueryRow(`SELECT name FROM schema_migrations WHERE version = 3`).Scan(&name); err != nil || name != "submissions-and-receipts" {
			return fmt.Errorf("step 4 migration marker: %v", err)
		}
	}
	if expectedVersion >= 4 {
		expected["schema_migrations"] = step6MigrationMarker
		for _, object := range step6Schema {
			expected[object.name] = object
		}
		if err := db.QueryRow(`SELECT name FROM schema_migrations WHERE version = 4`).Scan(&name); err != nil || name != "prefix-snapshot-blob-transfer" {
			return fmt.Errorf("step 6 migration marker: %v", err)
		}
	}
	if expectedVersion >= 5 {
		expected["schema_migrations"] = step5MigrationMarker
		for _, object := range step5Schema {
			expected[object.name] = object
		}
		if err := db.QueryRow(`SELECT name FROM schema_migrations WHERE version = 5`).Scan(&name); err != nil || name != "claims-grants-journals-close" {
			return fmt.Errorf("step 5 migration marker: %v", err)
		}
	}
	if expectedVersion >= 6 {
		expected["schema_migrations"] = step7MigrationMarker
		for _, object := range step7Schema {
			expected[object.name] = object
		}
		if err := db.QueryRow(`SELECT name FROM schema_migrations WHERE version = 6`).Scan(&name); err != nil || name != "continuity-and-migration-proof" {
			return fmt.Errorf("step 7 migration marker: %v", err)
		}
	}
	objects, err := db.Query(`SELECT type, name, sql FROM sqlite_master WHERE name NOT LIKE 'sqlite_%'`)
	if err != nil {
		return err
	}
	for objects.Next() {
		var kind, name, definition string
		if err := objects.Scan(&kind, &name, &definition); err != nil {
			_ = objects.Close()
			return err
		}
		want, ok := expected[name]
		if !ok {
			_ = objects.Close()
			return fmt.Errorf("unexpected schema object %s", name)
		}
		if kind != want.kind || normalizeSchemaSQL(definition) != normalizeSchemaSQL(want.sql) {
			_ = objects.Close()
			return fmt.Errorf("altered schema object %s", name)
		}
		delete(expected, name)
	}
	err = objects.Err()
	_ = objects.Close()
	if err != nil {
		return err
	}
	if len(expected) != 0 {
		return fmt.Errorf("missing schema objects: %v", mapKeys(expected))
	}
	var result string
	if err := db.QueryRow(`PRAGMA integrity_check`).Scan(&result); err != nil || result != "ok" {
		return fmt.Errorf("integrity check: %s: %v", result, err)
	}
	if err := db.QueryRow(`PRAGMA foreign_key_check`).Scan(&result); err != sql.ErrNoRows {
		return fmt.Errorf("foreign key check: %s: %v", result, err)
	}
	rows, err := db.Query(`SELECT domain_id, owner_public_key, owner_key_id, initial_epoch, active_epoch FROM domains`)
	if err != nil {
		return err
	}
	type persistedDomain struct {
		id              string
		initial, active int64
	}
	var domains []persistedDomain
	for rows.Next() {
		var d Domain
		var initial, epoch int64
		if err := rows.Scan(&d.ID, &d.OwnerPublicKey, &d.OwnerKeyID, &initial, &epoch); err != nil {
			_ = rows.Close()
			return err
		}
		if epoch < 1 {
			_ = rows.Close()
			return errors.New("nonpositive epoch")
		}
		d.ActiveEpoch = uint64(epoch)
		if err := validDomain(d); err != nil {
			_ = rows.Close()
			return err
		}
		domains = append(domains, persistedDomain{d.ID, initial, epoch})
	}
	if err := rows.Err(); err != nil {
		_ = rows.Close()
		return err
	}
	if err := rows.Close(); err != nil {
		return err
	}
	for _, d := range domains {
		promotions, err := db.Query(`SELECT from_epoch, to_epoch, prior_fence_digest, promotion_proof_digest FROM epoch_promotions WHERE domain_id = ? ORDER BY from_epoch`, d.id)
		if err != nil {
			return err
		}
		current := d.initial
		for promotions.Next() {
			var from, to int64
			var fence, proof string
			if err := promotions.Scan(&from, &to, &fence, &proof); err != nil {
				_ = promotions.Close()
				return err
			}
			if from != current || to != from+1 || !validDigest(fence) || !validDigest(proof) {
				_ = promotions.Close()
				return errors.New("invalid promotion chain")
			}
			current = to
		}
		if err := promotions.Err(); err != nil {
			_ = promotions.Close()
			return err
		}
		if err := promotions.Close(); err != nil {
			return err
		}
		if current != d.active {
			return errors.New("active epoch lacks promotion proof")
		}
	}
	members, err := db.Query(`SELECT repo_id, domain_id FROM repo_memberships`)
	if err != nil {
		return err
	}
	defer func() { _ = members.Close() }()
	for members.Next() {
		var repo, domain string
		if err := members.Scan(&repo, &domain); err != nil {
			return err
		}
		if !ulid.MatchString(repo) || !ulid.MatchString(domain) {
			return errors.New("invalid Repo membership identity")
		}
	}
	if err := members.Err(); err != nil {
		return err
	}
	if err := members.Close(); err != nil {
		return err
	}
	if expectedVersion >= 2 {
		if err := checkStep3State(db); err != nil {
			return err
		}
		if expectedVersion == 2 {
			var advanced int
			if err := db.QueryRow(`SELECT count(*) FROM environments WHERE sequence_head != 0`).Scan(&advanced); err != nil || advanced != 0 {
				return ErrInvalidStore
			}
		}
		if expectedVersion >= 3 {
			if err := checkStep4State(db); err != nil {
				return err
			}
			if expectedVersion >= 4 {
				if err := checkStep6State(db); err != nil {
					return err
				}
				if expectedVersion >= 5 {
					if err := checkStep5State(db); err != nil {
						return err
					}
					if expectedVersion >= 6 {
						return checkStep7State(db)
					}
					return nil
				}
			}
		}
	}
	return nil
}

func validDigest(value string) bool {
	if !strings.HasPrefix(value, "sha256:") || len(value) != 71 {
		return false
	}
	_, err := hex.DecodeString(value[7:])
	return err == nil && strings.ToLower(value) == value
}

func normalizeSchemaSQL(value string) string {
	return strings.Join(strings.Fields(value), " ")
}

func mapKeys(objects map[string]schemaObject) []string {
	keys := make([]string, 0, len(objects))
	for key := range objects {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	return keys
}
