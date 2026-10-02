package authoritystore

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

var step16MigrationMarker = schemaObject{"schema_migrations", "table", `CREATE TABLE schema_migrations (version INTEGER PRIMARY KEY CHECK (version IN (1, 2, 3, 4, 5, 6, 7, 8, 9, 10, 11, 12, 13, 14, 15)), name TEXT NOT NULL CHECK ((version = 1 AND name = 'baseline') OR (version = 2 AND name = 'environment-and-artifacts') OR (version = 3 AND name = 'submissions-and-receipts') OR (version = 4 AND name = 'prefix-snapshot-blob-transfer') OR (version = 5 AND name = 'claims-grants-journals-close') OR (version = 6 AND name = 'm5-lab-genesis-grant-consumption') OR (version = 7 AND name = 'step-8-provisional-birth-projection') OR (version = 8 AND name = 'step-9-birth-journal-receipt-barrier') OR (version = 9 AND name = 'step-10-content-and-findings') OR (version = 10 AND name = 'step-16-claim-journal-sequence-order') OR (version = 11 AND name = 'step-4-stage-step-operations') OR (version = 12 AND name = 'step-7-authority-gate-config-projections') OR (version = 13 AND name = 'step-7-detached-gate-repair-admissions') OR (version = 14 AND name = 'step-7-private-gate-repair-terminals') OR (version = 15 AND name = 'step-7-terminal-boundary-witnesses'))) STRICT`}

var step16Schema = []schemaObject{{
	"gate_exemption_repair_terminals", "table",
	strings.Replace(step15Schema[0].sql, "  occurred_at TEXT NOT NULL,", "  occurred_at TEXT NOT NULL,\n  boundary_witness BLOB,", 1),
}}

func installStep16(db *sql.DB) error {
	tx, err := db.Begin()
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	for _, statement := range []string{
		`DROP TRIGGER gate_exemption_repair_terminals_immutable`,
		`ALTER TABLE gate_exemption_repair_terminals ADD COLUMN boundary_witness BLOB`,
	} {
		if _, err = tx.Exec(statement); err != nil {
			return fmt.Errorf("install terminal boundary witness: %w", err)
		}
	}
	rows, err := tx.Query(`SELECT domain_id,command_id,request_hash,result_code,refusal_code,refusal_message,
		observed_position,observed_event_id,observed_prefix_digest,event_id,occurred_at
		FROM gate_exemption_repair_terminals ORDER BY domain_id,command_id`)
	if err != nil {
		return err
	}
	var terminals []gateExemptionRepairTerminal
	for rows.Next() {
		var terminal gateExemptionRepairTerminal
		var position int64
		var refusalCode, refusalMessage, eventID sql.NullString
		if err = rows.Scan(&terminal.DomainID, &terminal.CommandID, &terminal.RequestHash, &terminal.ResultCode,
			&refusalCode, &refusalMessage, &position, &terminal.ObservedEventID, &terminal.ObservedPrefixDigest,
			&eventID, &terminal.OccurredAt); err != nil {
			_ = rows.Close()
			return err
		}
		if position <= 0 {
			_ = rows.Close()
			return ErrInvalidStore
		}
		terminal.ObservedPosition = uint64(position)
		terminal.RefusalCode, terminal.RefusalMessage, terminal.EventID = refusalCode.String, refusalMessage.String, eventID.String
		terminals = append(terminals, terminal)
	}
	if err = rows.Err(); err != nil {
		_ = rows.Close()
		return err
	}
	if err = rows.Close(); err != nil {
		return err
	}
	for _, terminal := range terminals {
		admission, admissionErr := readGateExemptionRepairAdmission(context.Background(), tx, terminal.DomainID, terminal.CommandID)
		command, commandErr := decodeGateExemptionRepairCommand(admission.Command, admission.RequestHash)
		if admissionErr != nil || commandErr != nil {
			return fmt.Errorf("%w: cannot bind v14 terminal to its admitted command", ErrInvalidStore)
		}
		witness, witnessErr := migratedGateRepairBoundaryWitness(context.Background(), tx, command, terminal)
		if witnessErr != nil {
			return witnessErr
		}
		encoded, encodeErr := encodeGateRepairBoundaryWitness(witness)
		if encodeErr != nil {
			return encodeErr
		}
		if _, err = tx.Exec(`UPDATE gate_exemption_repair_terminals SET boundary_witness=? WHERE domain_id=? AND command_id=?`,
			encoded, terminal.DomainID, terminal.CommandID); err != nil {
			return err
		}
	}
	for _, object := range step15Schema {
		if object.name == "gate_exemption_repair_terminals_immutable" {
			if _, err = tx.Exec(object.sql); err != nil {
				return err
			}
			break
		}
	}
	for _, statement := range []string{
		`DROP TABLE schema_migrations`, step16MigrationMarker.sql,
		`INSERT INTO schema_migrations VALUES (1,'baseline'),(2,'environment-and-artifacts'),(3,'submissions-and-receipts'),(4,'prefix-snapshot-blob-transfer'),(5,'claims-grants-journals-close'),(6,'m5-lab-genesis-grant-consumption'),(7,'step-8-provisional-birth-projection'),(8,'step-9-birth-journal-receipt-barrier'),(9,'step-10-content-and-findings'),(10,'step-16-claim-journal-sequence-order'),(11,'step-4-stage-step-operations'),(12,'step-7-authority-gate-config-projections'),(13,'step-7-detached-gate-repair-admissions'),(14,'step-7-private-gate-repair-terminals'),(15,'step-7-terminal-boundary-witnesses')`,
	} {
		if _, err = tx.Exec(statement); err != nil {
			return err
		}
	}
	if _, err = tx.Exec(`PRAGMA user_version=15`); err != nil {
		return err
	}
	return tx.Commit()
}

// UpgradeV14 validates and backs up a terminal-outcome store before adding
// immutable claim-fence witnesses to its private repair terminals.
func UpgradeV14(root string) error {
	path, err := cleanRoot(root)
	if err != nil {
		return err
	}
	if err = checkRoot(path); err != nil {
		return err
	}
	lock, err := acquire(path)
	if err != nil {
		return err
	}
	defer func() { _ = lock.Close() }()
	db, err := connect(filepath.Join(path, "authority.db"), "rw", false)
	if err != nil {
		return err
	}
	defer func() { _ = db.Close() }()
	if err = checkSchemaVersion(db, 14); err != nil {
		return fmt.Errorf("%w: v14 validation: %v", ErrInvalidStore, err)
	}
	if err = checkBlobFiles(db, filepath.Join(path, "blobs")); err != nil {
		return fmt.Errorf("%w: v14 blobs: %v", ErrInvalidStore, err)
	}
	backup := filepath.Join(path, "authority-v14.backup.db")
	if _, statErr := os.Lstat(backup); !errors.Is(statErr, os.ErrNotExist) {
		if statErr != nil {
			return statErr
		}
		return fmt.Errorf("%w: v14 backup already exists; inspect before retry", ErrInvalidStore)
	}
	file, err := os.OpenFile(backup, os.O_CREATE|os.O_EXCL|os.O_RDWR, 0o600)
	if err != nil {
		return err
	}
	if err = file.Close(); err != nil {
		return err
	}
	if _, err = db.Exec(`VACUUM INTO ?`, backup); err != nil {
		return err
	}
	if err = regularFile(backup); err != nil {
		return err
	}
	retained, err := connect(backup, "rw", true)
	if err != nil {
		return err
	}
	if err = errors.Join(checkSchemaVersion(retained, 14), retained.Close()); err != nil {
		return err
	}
	if err = sameStoreContents(db, backup); err != nil {
		return err
	}
	if err = syncFileAndDirectory(backup, path); err != nil {
		return err
	}
	if err = installStep16(db); err != nil {
		return err
	}
	if err = installStep17(db); err != nil {
		return err
	}
	return checkSchema(db)
}
