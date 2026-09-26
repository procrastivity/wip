// Package wipdfixture owns a disposable local test database beneath a
// validated wipdprofile root. Its data is never authority state or evidence.
package wipdfixture

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"net/url"
	"os"
	"path/filepath"

	_ "modernc.org/sqlite"

	"github.com/procrastivity/wip/internal/wipdprofile"
)

const databaseName = "m4-test-fixture.sqlite"

// Record is asymmetric fixture data, unrelated to the production Matter
// projection or any authority-owned model.
type Record struct {
	ID    string
	Key   string
	Value string
}

// Store owns the SQLite handle; callers interact only through fixture methods.
type Store struct {
	db *sql.DB
}

// Open creates or opens the disposable fixture database below profile.Root.
// The profile is revalidated before any directory or file is created/opened.
func Open(profile wipdprofile.Profile) (*Store, error) {
	verified, err := wipdprofile.Resolve(profile.Root)
	if err != nil {
		return nil, err
	}
	if err := os.MkdirAll(verified.Root, 0o700); err != nil {
		return nil, fmt.Errorf("wipd fixture: create private root: %w", err)
	}
	rootInfo, err := os.Lstat(verified.Root)
	if err != nil {
		return nil, fmt.Errorf("wipd fixture: inspect private root: %w", err)
	}
	if rootInfo.Mode()&os.ModeSymlink != 0 || !rootInfo.IsDir() || rootInfo.Mode().Perm()&0o077 != 0 || rootInfo.Mode().Perm()&0o700 != 0o700 {
		return nil, errors.New("wipd fixture: root is not a private writable directory")
	}

	databasePath := filepath.Join(verified.Root, databaseName)
	if err := prepareDatabaseFile(databasePath); err != nil {
		return nil, err
	}
	dsn := (&url.URL{Scheme: "file", Path: databasePath}).String() + "?_pragma=journal_mode(WAL)&_pragma=busy_timeout(5000)"
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, fmt.Errorf("wipd fixture: open disposable database: %w", err)
	}
	db.SetMaxOpenConns(1)
	if err := db.PingContext(context.Background()); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("wipd fixture: connect disposable database: %w", err)
	}
	if _, err := db.ExecContext(context.Background(), `CREATE TABLE IF NOT EXISTS test_fixture_records (
		record_id TEXT NOT NULL PRIMARY KEY,
		fixture_key TEXT NOT NULL UNIQUE,
		fixture_value TEXT NOT NULL
	) STRICT, WITHOUT ROWID`); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("wipd fixture: initialize test-only schema: %w", err)
	}
	return &Store{db: db}, nil
}

func prepareDatabaseFile(path string) error {
	info, err := os.Lstat(path)
	if err == nil {
		if info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() || info.Mode().Perm() != 0o600 {
			return errors.New("wipd fixture: existing database file is not private and regular")
		}
		return nil
	}
	if !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("wipd fixture: inspect database path: %w", err)
	}
	file, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_RDWR, 0o600)
	if err != nil {
		return fmt.Errorf("wipd fixture: create private database file: %w", err)
	}
	if err := file.Chmod(0o600); err != nil {
		_ = file.Close()
		return fmt.Errorf("wipd fixture: secure new database file: %w", err)
	}
	if err := file.Close(); err != nil {
		return fmt.Errorf("wipd fixture: close initial database file: %w", err)
	}
	return nil
}

// Put persists one fixture record. It creates no production operation effects.
func (s *Store) Put(ctx context.Context, record Record) error {
	if s == nil || s.db == nil {
		return errors.New("wipd fixture: store is closed")
	}
	if record.ID == "" || record.Key == "" || record.Value == "" {
		return errors.New("wipd fixture: record fields must be non-empty")
	}
	_, err := s.db.ExecContext(ctx, `INSERT INTO test_fixture_records(record_id, fixture_key, fixture_value)
		VALUES (?, ?, ?)
		ON CONFLICT(fixture_key) DO UPDATE SET record_id = excluded.record_id, fixture_value = excluded.fixture_value`, record.ID, record.Key, record.Value)
	if err != nil {
		return fmt.Errorf("wipd fixture: persist record: %w", err)
	}
	return nil
}

// Get retrieves fixture data by its fixture-only key.
func (s *Store) Get(ctx context.Context, key string) (Record, error) {
	if s == nil || s.db == nil {
		return Record{}, errors.New("wipd fixture: store is closed")
	}
	var record Record
	err := s.db.QueryRowContext(ctx, `SELECT record_id, fixture_key, fixture_value
		FROM test_fixture_records WHERE fixture_key = ?`, key).Scan(&record.ID, &record.Key, &record.Value)
	if err != nil {
		return Record{}, fmt.Errorf("wipd fixture: read record: %w", err)
	}
	return record, nil
}

// Close releases the fixture database handle.
func (s *Store) Close() error {
	if s == nil || s.db == nil {
		return nil
	}
	err := s.db.Close()
	s.db = nil
	if err != nil {
		return fmt.Errorf("wipd fixture: close database: %w", err)
	}
	return nil
}
