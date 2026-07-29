package tables

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"io"
	"os"
	"time"
)

// migrate brings db up to (at most) target, applying each pending migration in
// its own transaction and recording it in schema_migrations.
//
// PLAN 1.2's posture is versioned migrations *plus* backup-before-migrate. The
// backup here is a plain file copy taken before the first pending migration
// runs, which is honest for a spike (single process, single connection, no
// concurrent writer) and not what production should do — see report.md.
func migrate(ctx context.Context, db *sql.DB, path string, target int) (int, error) {
	if _, err := db.ExecContext(ctx, `CREATE TABLE IF NOT EXISTS schema_migrations (
		version    INTEGER PRIMARY KEY,
		name       TEXT NOT NULL,
		applied_at TEXT NOT NULL
	) STRICT`); err != nil {
		return 0, fmt.Errorf("create schema_migrations: %w", err)
	}

	current, err := currentVersion(ctx, db)
	if err != nil {
		return 0, err
	}
	if current > target {
		return current, fmt.Errorf("database is at v%d, newer than the requested v%d", current, target)
	}

	pending := make([]migration, 0, len(migrations))
	for _, m := range migrations {
		if m.version > current && m.version <= target {
			pending = append(pending, m)
		}
	}
	if len(pending) == 0 {
		return current, nil
	}
	if current > 0 {
		if err := backup(path, current); err != nil {
			return current, fmt.Errorf("backup before migrate: %w", err)
		}
	}

	for _, m := range pending {
		if err := applyMigration(ctx, db, m); err != nil {
			return current, fmt.Errorf("migration v%d (%s): %w", m.version, m.name, err)
		}
		current = m.version
	}
	return current, nil
}

func currentVersion(ctx context.Context, db *sql.DB) (int, error) {
	var v sql.NullInt64
	if err := db.QueryRowContext(ctx, `SELECT MAX(version) FROM schema_migrations`).Scan(&v); err != nil {
		return 0, fmt.Errorf("read schema version: %w", err)
	}
	return int(v.Int64), nil
}

func applyMigration(ctx context.Context, db *sql.DB, m migration) error {
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()

	for i, stmt := range m.stmts {
		if _, err := tx.ExecContext(ctx, stmt); err != nil {
			return fmt.Errorf("statement %d: %w", i+1, err)
		}
	}
	if _, err := tx.ExecContext(ctx,
		`INSERT INTO schema_migrations (version, name, applied_at) VALUES (?, ?, ?)`,
		m.version, m.name, time.Now().UTC().Format(time.RFC3339Nano),
	); err != nil {
		return err
	}
	return tx.Commit()
}

// backup copies the database file to <path>.bak-v<version> before any schema
// change touches it.
func backup(path string, version int) error {
	src, err := os.Open(path) //nolint:gosec // spike; path is the caller's own store
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil
		}
		return err
	}
	defer func() { _ = src.Close() }()

	dst, err := os.Create(fmt.Sprintf("%s.bak-v%d", path, version)) //nolint:gosec // spike
	if err != nil {
		return err
	}
	if _, err := io.Copy(dst, src); err != nil {
		_ = dst.Close()
		return err
	}
	return dst.Close()
}
