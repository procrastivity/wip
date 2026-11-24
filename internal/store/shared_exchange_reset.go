package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/google/uuid"
)

func exchangeTableExists(ctx context.Context, q querier, table string) (bool, error) {
	var n int
	err := q.QueryRowContext(ctx, `SELECT COUNT(*) FROM sqlite_master WHERE type='table' AND name=?`, table).Scan(&n)
	return n != 0, err
}

// Explicit sync mutations install this additive metadata, never ordinary Open.
func ensureExchangeReset(ctx context.Context, tx *sql.Tx) error {
	_, err := tx.ExecContext(ctx, `CREATE TABLE IF NOT EXISTS exchange_associations (
	 repo TEXT PRIMARY KEY REFERENCES repos(id), id TEXT UNIQUE NOT NULL, installation TEXT NOT NULL);
	 CREATE TABLE IF NOT EXISTS exchange_reset_holds (
	 repo TEXT PRIMARY KEY REFERENCES repos(id), reset_id TEXT NOT NULL, held INTEGER NOT NULL);
	 CREATE TABLE IF NOT EXISTS exchange_resets (
	 reset_id TEXT PRIMARY KEY, connection TEXT NOT NULL, statuses TEXT NOT NULL, complete INTEGER NOT NULL DEFAULT 0);
	 CREATE TABLE IF NOT EXISTS exchange_retired (
	 id TEXT PRIMARY KEY, repo TEXT NOT NULL REFERENCES repos(id), connection TEXT NOT NULL,
	 reset_id TEXT NOT NULL, status TEXT NOT NULL, transport TEXT NOT NULL);`)
	return err
}

func exchangeGeneration(ctx context.Context, q querier, repo string) (string, error) {
	if exists, err := exchangeTableExists(ctx, q, "exchange_associations"); err != nil {
		return "", err
	} else if exists {
		var id string
		err := q.QueryRowContext(ctx, `SELECT id FROM exchange_associations WHERE repo=?`, repo).Scan(&id)
		if err == nil || !errors.Is(err, sql.ErrNoRows) {
			return id, err
		}
	}
	if exists, err := exchangeTableExists(ctx, q, "exchange_reset_holds"); err != nil {
		return "", err
	} else if exists {
		var id string
		err := q.QueryRowContext(ctx, `SELECT reset_id FROM exchange_reset_holds WHERE repo=?`, repo).Scan(&id)
		if err == nil {
			return "reset:" + id, nil
		}
		if !errors.Is(err, sql.ErrNoRows) {
			return "", err
		}
	}
	if exists, err := exchangeTableExists(ctx, q, "exchange_bindings"); err != nil {
		return "", err
	} else if exists {
		var n int
		if err := q.QueryRowContext(ctx, `SELECT COUNT(*) FROM exchange_bindings WHERE repo=?`, repo).Scan(&n); err != nil {
			return "", err
		} else if n != 0 {
			return repo, nil
		}
	}
	return "", nil
}

func exchangeScope(ctx context.Context, q querier, repo string) (string, error) {
	if repo == "" {
		return "", nil
	}
	id, err := exchangeGeneration(ctx, q, repo)
	if id == "" {
		id = repo
	}
	return id, err
}

func checkExchangeBinding(ctx context.Context, q querier, b ExchangeBinding) error {
	generation, err := exchangeGeneration(ctx, q, b.LocalRepo)
	if err != nil {
		return err
	}
	expected := b.AssociationID
	if expected == "" { // Legacy callers retain their native-Repo scope.
		expected = b.LocalRepo
	}
	if generation != expected {
		return fmt.Errorf("exchange: association retired or changed; late result not applied")
	}
	var endpoint string
	if err := q.QueryRowContext(ctx, `SELECT endpoint FROM exchange_bindings WHERE repo=?`, b.LocalRepo).Scan(&endpoint); err != nil {
		return err
	}
	if endpoint != b.Endpoint {
		return fmt.Errorf("exchange: destination changed")
	}
	var installation string
	if err := q.QueryRowContext(ctx, `SELECT id FROM exchange_installation`).Scan(&installation); err != nil {
		return err
	}
	if exists, err := exchangeTableExists(ctx, q, "exchange_associations"); err != nil {
		return err
	} else if exists {
		err := q.QueryRowContext(ctx, `SELECT installation FROM exchange_associations WHERE repo=?`, b.LocalRepo).Scan(&installation)
		if err != nil && !errors.Is(err, sql.ErrNoRows) {
			return err
		}
	}
	if installation != b.InstallationID {
		return fmt.Errorf("exchange: installation changed")
	}
	return nil
}

func exchangeConnectionGeneration(ctx context.Context, q querier, connection string) (string, error) {
	exists, err := exchangeTableExists(ctx, q, "exchange_resets")
	if err != nil || !exists {
		return "", err
	}
	var id string
	err = q.QueryRowContext(ctx, `SELECT reset_id FROM exchange_resets WHERE connection=? ORDER BY rowid DESC LIMIT 1`, connection).Scan(&id)
	if errors.Is(err, sql.ErrNoRows) {
		return "", nil
	}
	return id, err
}

// ExchangeConnectionGeneration fences initial pairing even when forgetting had
// no existing bindings to retire. Reading it never installs exchange tables.
func (s *Store) ExchangeConnectionGeneration(ctx context.Context, connection string) (string, error) {
	return exchangeConnectionGeneration(ctx, s.db, connection)
}

func exchangeConnectionResetting(ctx context.Context, q querier, connection string) error {
	if exists, err := exchangeTableExists(ctx, q, "exchange_resets"); err != nil {
		return err
	} else if !exists {
		return nil
	}
	var n int
	if err := q.QueryRowContext(ctx, `SELECT COUNT(*) FROM exchange_resets WHERE connection=? AND complete=0`, connection).Scan(&n); err != nil {
		return err
	}
	if n != 0 {
		return fmt.Errorf("exchange: connection reset unfinished; resume connection forget")
	}
	return nil
}

func installExchangeAssociation(ctx context.Context, tx *sql.Tx, repo, legacyInstallation string) (string, string, error) {
	var id, installation string
	err := tx.QueryRowContext(ctx, `SELECT id,installation FROM exchange_associations WHERE repo=?`, repo).Scan(&id, &installation)
	if err == nil || !errors.Is(err, sql.ErrNoRows) {
		return id, installation, err
	}
	id, installation = repo, legacyInstallation
	var retired int
	if err := tx.QueryRowContext(ctx, `SELECT (SELECT COUNT(*) FROM exchange_retired WHERE repo=?) + (SELECT COUNT(*) FROM exchange_reset_holds WHERE repo=?)`, repo, repo).Scan(&retired); err != nil {
		return "", "", err
	}
	if retired != 0 {
		id = uuid.NewString()
		installation = id
	}
	_, err = tx.ExecContext(ctx, `INSERT INTO exchange_associations VALUES(?,?,?)`, repo, id, installation)
	return id, installation, err
}

// ExchangeOnboardingHeld is offline and does not initialize sync metadata.
func (s *Store) ExchangeOnboardingHeld(ctx context.Context, repo string) (bool, error) {
	exists, err := exchangeTableExists(ctx, s.db, "exchange_reset_holds")
	if err != nil || !exists {
		return false, err
	}
	var held bool
	err = s.db.QueryRowContext(ctx, `SELECT held FROM exchange_reset_holds WHERE repo=?`, repo).Scan(&held)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	return held, err
}

// ReleaseExchangeHold permits explicit reconnection only for the reviewed state.
func (s *Store) ReleaseExchangeHold(ctx context.Context, repo, expectedGeneration string) error {
	exists, err := exchangeTableExists(ctx, s.db, "exchange_reset_holds")
	if err != nil || !exists {
		return err
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	generation, err := exchangeGeneration(ctx, tx, repo)
	if err != nil {
		return err
	}
	if generation != expectedGeneration {
		return fmt.Errorf("exchange: project changed during reconnection; hold retained")
	}
	if _, err := tx.ExecContext(ctx, `UPDATE exchange_reset_holds SET held=0 WHERE repo=?`, repo); err != nil {
		return err
	}
	return tx.Commit()
}

// HoldExchangeOnboarding also protects never-paired projects with an abandoned
// creation request. It preserves their native identity and local history.
func (s *Store) HoldExchangeOnboarding(ctx context.Context, repo, resetID string) error {
	if resetID == "" {
		return fmt.Errorf("exchange: reset identity required")
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	if err := ensureExchangeReset(ctx, tx); err != nil {
		return err
	}
	var previous string
	err = tx.QueryRowContext(ctx, `SELECT reset_id FROM exchange_reset_holds WHERE repo=?`, repo).Scan(&previous)
	if err == nil {
		if previous == resetID {
			return nil // A completed reconnect may already have released this hold.
		}
		var newer bool
		if err := tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM exchange_resets n JOIN exchange_resets p ON n.rowid>p.rowid WHERE n.reset_id=? AND p.reset_id=?)`, resetID, previous).Scan(&newer); err != nil {
			return err
		}
		if !newer {
			return fmt.Errorf("exchange: newer or unrecognized reset hold retained")
		}
	} else if !errors.Is(err, sql.ErrNoRows) {
		return err
	}
	if exists, err := exchangeTableExists(ctx, tx, "exchange_bindings"); err != nil {
		return err
	} else if exists {
		var paired bool
		if err := tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM exchange_bindings WHERE repo=?)`, repo).Scan(&paired); err != nil {
			return err
		}
		if paired {
			return fmt.Errorf("exchange: project paired during pending creation cleanup; new binding retained")
		}
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO exchange_reset_holds VALUES(?,?,1) ON CONFLICT(repo) DO UPDATE SET reset_id=excluded.reset_id,held=1`, repo, resetID); err != nil {
		return err
	}
	return tx.Commit()
}

// FinishExchangeConnectionReset releases only this operation's local barrier.
func (s *Store) FinishExchangeConnectionReset(ctx context.Context, connection, resetID string) error {
	_, err := s.db.ExecContext(ctx, `UPDATE exchange_resets SET complete=1 WHERE reset_id=? AND connection=?`, resetID, connection)
	return err
}

// ForgetExchangeConnection preserves native data and immutable provenance. A
// repeat operation returns its original snapshot, never retires a new pairing.
func (s *Store) ForgetExchangeConnection(ctx context.Context, connection, resetID string, apply bool) ([]ExchangeStatus, error) {
	if connection == "" || (apply && resetID == "") {
		return nil, fmt.Errorf("exchange: connection and reset identity required")
	}
	if exists, err := exchangeTableExists(ctx, s.db, "exchange_resets"); err != nil {
		return nil, err
	} else if exists && resetID != "" {
		var raw string
		err := s.db.QueryRowContext(ctx, `SELECT statuses FROM exchange_resets WHERE reset_id=? AND connection=?`, resetID, connection).Scan(&raw)
		if err == nil {
			var result []ExchangeStatus
			err = json.Unmarshal([]byte(raw), &result)
			return result, err
		}
		if !errors.Is(err, sql.ErrNoRows) {
			return nil, err
		}
	}
	var result []ExchangeStatus
	if exists, err := exchangeTableExists(ctx, s.db, "exchange_bindings"); err != nil {
		return nil, err
	} else if exists {
		accounts, err := exchangeTableExists(ctx, s.db, "exchange_accounts")
		if err != nil {
			return nil, err
		}
		query := `SELECT repo FROM exchange_bindings WHERE ?='default' ORDER BY repo`
		if accounts {
			query = `SELECT b.repo FROM exchange_bindings b LEFT JOIN exchange_accounts a ON a.repo=b.repo WHERE COALESCE(a.connection,'default')=? ORDER BY b.repo`
		}
		rows, err := s.db.QueryContext(ctx, query, connection)
		if err != nil {
			return nil, err
		}
		var repos []string
		for rows.Next() {
			var repo string
			if err := rows.Scan(&repo); err != nil {
				_ = rows.Close()
				return nil, err
			}
			repos = append(repos, repo)
		}
		err = rows.Err()
		_ = rows.Close()
		if err != nil {
			return nil, err
		}
		for _, repo := range repos {
			status, err := s.ExchangeStatus(ctx, repo)
			if err != nil {
				return nil, err
			}
			// Low-level exchange bindings predate the CLI boundary metadata.
			// They still need retirement, without inventing a CLI pairing.
			if !status.Paired {
				if err := s.db.QueryRowContext(ctx, `SELECT b.endpoint,i.id FROM exchange_bindings b CROSS JOIN exchange_installation i WHERE b.repo=?`, repo).Scan(&status.Binding.Endpoint, &status.Binding.InstallationID); err != nil {
					return nil, err
				}
				status.Binding.AssociationID = status.Generation
				if exists, err := exchangeTableExists(ctx, s.db, "exchange_associations"); err != nil {
					return nil, err
				} else if exists {
					err := s.db.QueryRowContext(ctx, `SELECT installation FROM exchange_associations WHERE repo=?`, repo).Scan(&status.Binding.InstallationID)
					if err != nil && !errors.Is(err, sql.ErrNoRows) {
						return nil, err
					}
				}
			}
			result = append(result, status)
		}
	}
	if !apply {
		return result, nil
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer func() { _ = tx.Rollback() }()
	if err := ensureExchangeReset(ctx, tx); err != nil {
		return nil, err
	}
	// Another invocation may have finished while this one inventoried the store.
	var saved string
	err = tx.QueryRowContext(ctx, `SELECT statuses FROM exchange_resets WHERE reset_id=? AND connection=?`, resetID, connection).Scan(&saved)
	if err == nil {
		var original []ExchangeStatus
		err := json.Unmarshal([]byte(saved), &original)
		return original, err
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return nil, err
	}
	if err := exchangeConnectionResetting(ctx, tx, connection); err != nil {
		return nil, err
	}
	if exists, err := exchangeTableExists(ctx, tx, "exchange_bindings"); err != nil {
		return nil, err
	} else if exists {
		query := `SELECT COUNT(*) FROM exchange_bindings WHERE ?='default'`
		if accounts, err := exchangeTableExists(ctx, tx, "exchange_accounts"); err != nil {
			return nil, err
		} else if accounts {
			query = `SELECT COUNT(*) FROM exchange_bindings b LEFT JOIN exchange_accounts a ON a.repo=b.repo WHERE COALESCE(a.connection,'default')=?`
		}
		var n int
		if err := tx.QueryRowContext(ctx, query, connection).Scan(&n); err != nil {
			return nil, err
		}
		if n != len(result) {
			return nil, fmt.Errorf("exchange: connection bindings changed during reset; resume connection forget")
		}
	}
	raw, err := json.Marshal(result)
	if err != nil {
		return nil, err
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO exchange_resets(reset_id,connection,statuses) VALUES(?,?,?)`, resetID, connection, string(raw)); err != nil {
		return nil, err
	}
	for _, status := range result {
		b := status.Binding
		if err := checkExchangeBinding(ctx, tx, b); err != nil {
			return nil, err
		}
		// Archive full transport rows before removing the active indexes. Immutable
		// source/effect evidence remains in its original tables, not in this blob.
		transport := map[string]json.RawMessage{}
		for _, table := range []string{"exchange_bindings", "exchange_accounts", "exchange_cli", "exchange_upload_ack", "exchange_exports", "exchange_inbox"} {
			exists, err := exchangeTableExists(ctx, tx, table)
			if err != nil {
				return nil, err
			}
			if !exists {
				continue
			}
			rows, err := tx.QueryContext(ctx, `SELECT * FROM `+table+` WHERE repo=?`, b.LocalRepo)
			if err != nil {
				return nil, err
			}
			columns, err := rows.Columns()
			if err != nil {
				_ = rows.Close()
				return nil, err
			}
			var archived []map[string]any
			for rows.Next() {
				values := make([]any, len(columns))
				pointers := make([]any, len(columns))
				for i := range values {
					pointers[i] = &values[i]
				}
				if err := rows.Scan(pointers...); err != nil {
					_ = rows.Close()
					return nil, err
				}
				row := map[string]any{}
				for i, column := range columns {
					row[column] = values[i]
				}
				archived = append(archived, row)
			}
			err = rows.Err()
			_ = rows.Close()
			if err != nil {
				return nil, err
			}
			transport[table], err = json.Marshal(archived)
			if err != nil {
				return nil, err
			}
		}
		encoded, err := json.Marshal(transport)
		if err != nil {
			return nil, err
		}
		state, err := json.Marshal(status)
		if err != nil {
			return nil, err
		}
		if _, err := tx.ExecContext(ctx, `INSERT INTO exchange_retired VALUES(?,?,?,?,?,?)`, status.Generation, b.LocalRepo, connection, resetID, string(state), string(encoded)); err != nil {
			return nil, err
		}
		for _, table := range []string{"exchange_upload_ack", "exchange_exports", "exchange_inbox", "exchange_cli", "exchange_accounts", "exchange_bindings", "exchange_associations"} {
			if exists, err := exchangeTableExists(ctx, tx, table); err != nil {
				return nil, err
			} else if exists {
				if _, err := tx.ExecContext(ctx, `DELETE FROM `+table+` WHERE repo=?`, b.LocalRepo); err != nil {
					return nil, err
				}
			}
		}
		if _, err := tx.ExecContext(ctx, `INSERT INTO exchange_reset_holds VALUES(?,?,1) ON CONFLICT(repo) DO UPDATE SET reset_id=excluded.reset_id,held=1`, b.LocalRepo, resetID); err != nil {
			return nil, err
		}
	}
	return result, tx.Commit()
}
