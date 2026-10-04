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

var step17MigrationMarker = schemaObject{"schema_migrations", "table", `CREATE TABLE schema_migrations (version INTEGER PRIMARY KEY CHECK (version IN (1, 2, 3, 4, 5, 6, 7, 8, 9, 10, 11, 12, 13, 14, 15, 16)), name TEXT NOT NULL CHECK ((version = 1 AND name = 'baseline') OR (version = 2 AND name = 'environment-and-artifacts') OR (version = 3 AND name = 'submissions-and-receipts') OR (version = 4 AND name = 'prefix-snapshot-blob-transfer') OR (version = 5 AND name = 'claims-grants-journals-close') OR (version = 6 AND name = 'm5-lab-genesis-grant-consumption') OR (version = 7 AND name = 'step-8-provisional-birth-projection') OR (version = 8 AND name = 'step-9-birth-journal-receipt-barrier') OR (version = 9 AND name = 'step-10-content-and-findings') OR (version = 10 AND name = 'step-16-claim-journal-sequence-order') OR (version = 11 AND name = 'step-4-stage-step-operations') OR (version = 12 AND name = 'step-7-authority-gate-config-projections') OR (version = 13 AND name = 'step-7-detached-gate-repair-admissions') OR (version = 14 AND name = 'step-7-private-gate-repair-terminals') OR (version = 15 AND name = 'step-7-terminal-boundary-witnesses') OR (version = 16 AND name = 'step-7-terminal-and-journal-state-boundaries'))) STRICT`}

var step17Schema = []schemaObject{
	{"gate_exemption_repair_terminals", "table", strings.Replace(step16Schema[0].sql,
		"  boundary_witness BLOB,", "  boundary_witness BLOB,\n  journal_state_sequence INTEGER NOT NULL DEFAULT 0 CHECK(journal_state_sequence>=0),", 1)},
	{"claim_journal_state_boundaries", "table", `CREATE TABLE claim_journal_state_boundaries (
  transition_id INTEGER PRIMARY KEY AUTOINCREMENT,
  domain_id TEXT NOT NULL,
  claim_id TEXT NOT NULL,
  journal_id TEXT NOT NULL,
  generation INTEGER NOT NULL CHECK(generation > 0),
  state TEXT NOT NULL CHECK(state IN ('sealed','quarantined')),
  event_count INTEGER NOT NULL CHECK(event_count > 0),
  event_id TEXT NOT NULL,
  prefix_digest TEXT NOT NULL,
  FOREIGN KEY(domain_id,journal_id) REFERENCES claim_journals(domain_id,journal_id),
  UNIQUE(domain_id,journal_id,state)
) STRICT`},
	{"claim_journal_state_boundaries_insert", "trigger", `CREATE TRIGGER claim_journal_state_boundaries_insert BEFORE INSERT ON claim_journal_state_boundaries
WHEN NEW.transition_id != COALESCE((SELECT max(transition_id)+1 FROM claim_journal_state_boundaries),1)
  OR NOT EXISTS(
    SELECT 1 FROM claim_journals j JOIN authority_events e ON e.domain_id=NEW.domain_id AND e.position=NEW.event_count
    WHERE j.domain_id=NEW.domain_id AND j.claim_id=NEW.claim_id AND j.journal_id=NEW.journal_id
      AND j.generation=NEW.generation AND j.state=NEW.state AND e.event_id=NEW.event_id AND e.prefix_digest=NEW.prefix_digest
  )
  OR NEW.event_count < COALESCE((SELECT max(event_count) FROM claim_journal_state_boundaries
    WHERE domain_id=NEW.domain_id AND journal_id=NEW.journal_id),0)
  OR (NEW.state='sealed' AND EXISTS(SELECT 1 FROM claim_journal_state_boundaries WHERE domain_id=NEW.domain_id AND journal_id=NEW.journal_id))
  OR (NEW.state='quarantined' AND EXISTS(SELECT 1 FROM claim_journal_state_boundaries WHERE domain_id=NEW.domain_id AND journal_id=NEW.journal_id AND state='quarantined'))
BEGIN SELECT RAISE(ABORT,'invalid claim journal state boundary'); END`},
	{"claim_journal_state_boundaries_immutable", "trigger", `CREATE TRIGGER claim_journal_state_boundaries_immutable BEFORE UPDATE ON claim_journal_state_boundaries BEGIN SELECT RAISE(ABORT,'immutable claim journal state boundary'); END`},
	{"claim_journal_state_boundaries_no_delete", "trigger", `CREATE TRIGGER claim_journal_state_boundaries_no_delete BEFORE DELETE ON claim_journal_state_boundaries BEGIN SELECT RAISE(ABORT,'immutable claim journal state boundary'); END`},
	{"gate_exemption_repair_terminal_boundaries", "table", `CREATE TABLE gate_exemption_repair_terminal_boundaries (
  domain_id TEXT NOT NULL,
  command_id TEXT NOT NULL,
  event_count INTEGER NOT NULL CHECK(event_count > 0),
  high_water_event_id TEXT NOT NULL,
  prefix_digest TEXT NOT NULL,
  journal_state_sequence INTEGER NOT NULL CHECK(journal_state_sequence >= 0),
  boundary_witness BLOB NOT NULL CHECK(length(boundary_witness) BETWEEN 1 AND 4096),
  PRIMARY KEY(domain_id,command_id),
  FOREIGN KEY(domain_id,command_id) REFERENCES gate_exemption_repair_terminals(domain_id,command_id)
) STRICT`},
	{"gate_exemption_repair_terminal_boundaries_insert", "trigger", `CREATE TRIGGER gate_exemption_repair_terminal_boundaries_insert BEFORE INSERT ON gate_exemption_repair_terminal_boundaries
WHEN NOT EXISTS(
  SELECT 1 FROM gate_exemption_repair_terminals t JOIN authority_events e
    ON e.domain_id=NEW.domain_id AND e.position=NEW.event_count
  WHERE t.domain_id=NEW.domain_id AND t.command_id=NEW.command_id
    AND t.observed_position=NEW.event_count AND t.observed_event_id=NEW.high_water_event_id
    AND t.observed_prefix_digest=NEW.prefix_digest AND t.journal_state_sequence=NEW.journal_state_sequence
    AND t.boundary_witness=NEW.boundary_witness
    AND e.event_id=NEW.high_water_event_id AND e.prefix_digest=NEW.prefix_digest
)
BEGIN SELECT RAISE(ABORT,'repair terminal boundary is not bound to its persisted prefix'); END`},
	{"gate_exemption_repair_terminal_boundaries_immutable", "trigger", `CREATE TRIGGER gate_exemption_repair_terminal_boundaries_immutable BEFORE UPDATE ON gate_exemption_repair_terminal_boundaries BEGIN SELECT RAISE(ABORT,'immutable repair terminal boundary'); END`},
	{"gate_exemption_repair_terminal_boundaries_no_delete", "trigger", `CREATE TRIGGER gate_exemption_repair_terminal_boundaries_no_delete BEFORE DELETE ON gate_exemption_repair_terminal_boundaries BEGIN SELECT RAISE(ABORT,'immutable repair terminal boundary'); END`},
}

func installStep17(db *sql.DB) error {
	tx, err := db.Begin()
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	rows, err := tx.Query(`SELECT boundary_witness FROM gate_exemption_repair_terminals ORDER BY domain_id,command_id`)
	if err != nil {
		return err
	}
	for rows.Next() {
		var raw []byte
		if err = rows.Scan(&raw); err != nil {
			_ = rows.Close()
			return err
		}
		witness, decodeErr := decodeGateRepairBoundaryWitness(raw)
		if decodeErr != nil || witness.Fence == "journal-state" {
			_ = rows.Close()
			return fmt.Errorf("%w: v15 journal-fenced repair terminal lacks an independently retained state boundary", ErrInvalidStore)
		}
	}
	if err = rows.Err(); err != nil {
		_ = rows.Close()
		return err
	}
	if err = rows.Close(); err != nil {
		return err
	}
	if _, err = tx.Exec(`ALTER TABLE gate_exemption_repair_terminals ADD COLUMN journal_state_sequence INTEGER NOT NULL DEFAULT 0 CHECK(journal_state_sequence>=0)`); err != nil {
		return fmt.Errorf("add terminal journal-state watermark: %w", err)
	}
	for _, object := range step17Schema[1:] {
		if _, err = tx.Exec(object.sql); err != nil {
			return fmt.Errorf("%s: %w", object.name, err)
		}
	}
	rows, err = tx.Query(`SELECT domain_id,claim_id,journal_id,generation,state FROM claim_journals
		WHERE state IN ('sealed','quarantined') ORDER BY domain_id,journal_id`)
	if err != nil {
		return err
	}
	type journal struct {
		domain, claim, id, state string
		generation               uint64
	}
	var legacyJournals []journal
	for rows.Next() {
		var item journal
		if err = rows.Scan(&item.domain, &item.claim, &item.id, &item.generation, &item.state); err != nil {
			_ = rows.Close()
			return err
		}
		legacyJournals = append(legacyJournals, item)
	}
	if err = rows.Err(); err != nil {
		_ = rows.Close()
		return err
	}
	if err = rows.Close(); err != nil {
		return err
	}
	for _, item := range legacyJournals {
		anchor, anchorErr := currentAnchor(context.Background(), tx, item.domain)
		if anchorErr != nil || anchor.EventCount == 0 {
			return fmt.Errorf("%w: cannot backfill journal state boundary", ErrInvalidStore)
		}
		if err = insertClaimJournalStateBoundary(context.Background(), tx, item.domain, item.claim, item.id,
			item.generation, item.state, anchor); err != nil {
			return err
		}
	}
	if _, err = tx.Exec(`DROP TABLE schema_migrations`); err != nil {
		return err
	}
	if _, err = tx.Exec(step17MigrationMarker.sql); err != nil {
		return err
	}
	rows, err = tx.Query(`SELECT domain_id,command_id,observed_position,observed_event_id,observed_prefix_digest,
		journal_state_sequence,boundary_witness FROM gate_exemption_repair_terminals ORDER BY domain_id,command_id`)
	if err != nil {
		return err
	}
	for rows.Next() {
		var terminal gateExemptionRepairTerminal
		if err = rows.Scan(&terminal.DomainID, &terminal.CommandID, &terminal.ObservedPosition, &terminal.ObservedEventID,
			&terminal.ObservedPrefixDigest, &terminal.JournalStateSequence, &terminal.BoundaryWitness); err != nil {
			_ = rows.Close()
			return err
		}
		if err = insertGateRepairTerminalBoundary(context.Background(), tx, terminal); err != nil {
			_ = rows.Close()
			return err
		}
	}
	if err = rows.Err(); err != nil {
		_ = rows.Close()
		return err
	}
	if err = rows.Close(); err != nil {
		return err
	}
	if _, err = tx.Exec(`INSERT INTO schema_migrations VALUES
		(1,'baseline'),(2,'environment-and-artifacts'),(3,'submissions-and-receipts'),(4,'prefix-snapshot-blob-transfer'),
		(5,'claims-grants-journals-close'),(6,'m5-lab-genesis-grant-consumption'),(7,'step-8-provisional-birth-projection'),
		(8,'step-9-birth-journal-receipt-barrier'),(9,'step-10-content-and-findings'),(10,'step-16-claim-journal-sequence-order'),
		(11,'step-4-stage-step-operations'),(12,'step-7-authority-gate-config-projections'),(13,'step-7-detached-gate-repair-admissions'),
		(14,'step-7-private-gate-repair-terminals'),(15,'step-7-terminal-boundary-witnesses'),(16,'step-7-terminal-and-journal-state-boundaries')`); err != nil {
		return err
	}
	if _, err = tx.Exec(`PRAGMA user_version=16`); err != nil {
		return err
	}
	return tx.Commit()
}

func insertGateRepairTerminalBoundary(ctx context.Context, tx *sql.Tx, terminal gateExemptionRepairTerminal) error {
	_, err := tx.ExecContext(ctx, `INSERT INTO gate_exemption_repair_terminal_boundaries(
		domain_id,command_id,event_count,high_water_event_id,prefix_digest,journal_state_sequence,boundary_witness
	) VALUES(?,?,?,?,?,?,?)`, terminal.DomainID, terminal.CommandID, terminal.ObservedPosition, terminal.ObservedEventID,
		terminal.ObservedPrefixDigest, terminal.JournalStateSequence, terminal.BoundaryWitness)
	return err
}

func insertClaimJournalStateBoundary(ctx context.Context, tx *sql.Tx, domain, claim, journal string, generation uint64,
	state string, anchor PrefixAnchor,
) error {
	if anchor.EventCount == 0 || (state != "sealed" && state != "quarantined") {
		return ErrInvalidStore
	}
	var transition uint64
	if err := tx.QueryRowContext(ctx, `SELECT COALESCE(max(transition_id),0)+1 FROM claim_journal_state_boundaries`).Scan(&transition); err != nil {
		return err
	}
	_, err := tx.ExecContext(ctx, `INSERT INTO claim_journal_state_boundaries(
		transition_id,domain_id,claim_id,journal_id,generation,state,event_count,event_id,prefix_digest) VALUES(?,?,?,?,?,?,?,?,?)`,
		transition, domain, claim, journal, generation, state, anchor.EventCount, anchor.EventID, anchor.Digest)
	return err
}

func recordClaimJournalStateBoundary(ctx context.Context, tx *sql.Tx, domain, claim, journal string, generation uint64, state string) error {
	anchor, err := currentAnchor(ctx, tx, domain)
	if err != nil {
		return err
	}
	return insertClaimJournalStateBoundary(ctx, tx, domain, claim, journal, generation, state, anchor)
}

func claimJournalStateWatermark(ctx context.Context, queryer gateRepairQueryer) (uint64, error) {
	var sequence uint64
	err := queryer.QueryRowContext(ctx, `SELECT COALESCE(max(transition_id),0) FROM claim_journal_state_boundaries`).Scan(&sequence)
	return sequence, err
}

func claimJournalStateAtBoundary(ctx context.Context, queryer gateRepairQueryer, domain, claim, journal string,
	generation, eventCount, sequence uint64,
) (string, error) {
	var maximum uint64
	if err := queryer.QueryRowContext(ctx, `SELECT COALESCE(max(transition_id),0) FROM claim_journal_state_boundaries`).Scan(&maximum); err != nil || sequence > maximum {
		return "", fmt.Errorf("%w: terminal journal-state watermark is not retained", ErrInvalidStore)
	}
	rows, err := queryer.QueryContext(ctx, `SELECT transition_id,state,event_count,event_id,prefix_digest FROM claim_journal_state_boundaries
		WHERE domain_id=? AND claim_id=? AND journal_id=? AND generation=? ORDER BY transition_id`, domain, claim, journal, generation)
	if err != nil {
		return "", err
	}
	defer func() { _ = rows.Close() }()
	state := "open"
	var priorPosition uint64
	for rows.Next() {
		var id, position uint64
		var transitionState, eventID, digest string
		if err = rows.Scan(&id, &transitionState, &position, &eventID, &digest); err != nil {
			return "", err
		}
		var storedID, storedDigest string
		if err = queryer.QueryRowContext(ctx, `SELECT event_id,prefix_digest FROM authority_events WHERE domain_id=? AND position=?`, domain, position).Scan(&storedID, &storedDigest); err != nil ||
			storedID != eventID || storedDigest != digest || position < priorPosition {
			return "", fmt.Errorf("%w: journal-state transition is not bound to authority history", ErrInvalidStore)
		}
		priorPosition = position
		if id > sequence {
			continue
		}
		if position > eventCount || (transitionState != "sealed" && transitionState != "quarantined") ||
			transitionState == "sealed" && state != "open" || transitionState == "quarantined" && state == "quarantined" {
			return "", fmt.Errorf("%w: impossible journal state at terminal boundary", ErrInvalidStore)
		}
		state = transitionState
	}
	if err = rows.Err(); err != nil {
		return "", err
	}
	return state, nil
}

func checkStep16State(db *sql.DB) error {
	tx, err := db.BeginTx(context.Background(), &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	rows, err := tx.Query(`SELECT transition_id,domain_id,claim_id,journal_id,generation,state,event_count,event_id,prefix_digest
		FROM claim_journal_state_boundaries ORDER BY transition_id`)
	if err != nil {
		return err
	}
	type boundary struct {
		id, generation, position                       uint64
		domain, claim, journal, state, eventID, digest string
	}
	var boundaries []boundary
	for rows.Next() {
		var item boundary
		if err = rows.Scan(&item.id, &item.domain, &item.claim, &item.journal, &item.generation, &item.state, &item.position, &item.eventID, &item.digest); err != nil {
			_ = rows.Close()
			return err
		}
		boundaries = append(boundaries, item)
	}
	if err = rows.Err(); err != nil {
		_ = rows.Close()
		return err
	}
	if err = rows.Close(); err != nil {
		return err
	}
	if len(boundaries) != 0 && boundaries[len(boundaries)-1].id != uint64(len(boundaries)) {
		return fmt.Errorf("%w: claim journal state boundary sequence has gaps", ErrInvalidStore)
	}
	type journalKey struct {
		domain, claim, journal string
		generation             uint64
	}
	states := make(map[journalKey]string)
	positions := make(map[journalKey]uint64)
	for _, item := range boundaries {
		key := journalKey{item.domain, item.claim, item.journal, item.generation}
		if item.state != "sealed" && item.state != "quarantined" || item.position < positions[key] {
			return fmt.Errorf("%w: invalid claim journal state transition", ErrInvalidStore)
		}
		var eventID, digest string
		if err = tx.QueryRow(`SELECT event_id,prefix_digest FROM authority_events WHERE domain_id=? AND position=?`, item.domain, item.position).Scan(&eventID, &digest); err != nil || eventID != item.eventID || digest != item.digest {
			return fmt.Errorf("%w: claim journal state boundary is not in authority history", ErrInvalidStore)
		}
		prior := states[key]
		if item.state == "sealed" && prior != "" || item.state == "quarantined" && prior == "quarantined" {
			return fmt.Errorf("%w: invalid claim journal state transition order", ErrInvalidStore)
		}
		states[key], positions[key] = item.state, item.position
	}
	for key, state := range states {
		var current string
		var generation uint64
		if err = tx.QueryRow(`SELECT state,generation FROM claim_journals WHERE domain_id=? AND claim_id=? AND journal_id=?`,
			key.domain, key.claim, key.journal).Scan(&current, &generation); err != nil || current != state || generation != key.generation {
			return fmt.Errorf("%w: claim journal state projection disagrees with immutable boundary history", ErrInvalidStore)
		}
	}
	var missing int
	if err = tx.QueryRow(`SELECT count(*) FROM claim_journals j WHERE j.state!='open' AND NOT EXISTS(
		SELECT 1 FROM claim_journal_state_boundaries b WHERE b.domain_id=j.domain_id AND b.claim_id=j.claim_id
		AND b.journal_id=j.journal_id AND b.generation=j.generation AND b.state=j.state)`).Scan(&missing); err != nil || missing != 0 {
		return fmt.Errorf("%w: closed claim journal lacks its immutable state boundary", ErrInvalidStore)
	}
	var terminalCount, boundaryCount int
	if err = tx.QueryRow(`SELECT count(*) FROM gate_exemption_repair_terminals`).Scan(&terminalCount); err != nil {
		return err
	}
	if err = tx.QueryRow(`SELECT count(*) FROM gate_exemption_repair_terminal_boundaries`).Scan(&boundaryCount); err != nil || terminalCount != boundaryCount {
		return fmt.Errorf("%w: private repair terminal boundary history is incomplete", ErrInvalidStore)
	}
	rows, err = tx.Query(`SELECT domain_id,command_id,request_hash,result_code,refusal_code,refusal_message,
		observed_position,observed_event_id,observed_prefix_digest,event_id,occurred_at,boundary_witness,journal_state_sequence
		FROM gate_exemption_repair_terminals ORDER BY domain_id,command_id`)
	if err != nil {
		return err
	}
	var terminals []gateExemptionRepairTerminal
	for rows.Next() {
		var terminal gateExemptionRepairTerminal
		var refusalCode, refusalMessage, eventID sql.NullString
		if err = rows.Scan(&terminal.DomainID, &terminal.CommandID, &terminal.RequestHash, &terminal.ResultCode,
			&refusalCode, &refusalMessage, &terminal.ObservedPosition, &terminal.ObservedEventID,
			&terminal.ObservedPrefixDigest, &eventID, &terminal.OccurredAt, &terminal.BoundaryWitness,
			&terminal.JournalStateSequence); err != nil {
			_ = rows.Close()
			return err
		}
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
		if err = validateGateRepairTerminalBoundaryRecord(context.Background(), tx, terminal, terminal.BoundaryWitness); err != nil {
			return err
		}
	}
	return tx.Commit()
}

// UpgradeV15 validates and backs up a v15 store before installing
// independently retained repair-terminal and journal-state boundaries.
func UpgradeV15(root string) error {
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
	if err = checkSchemaVersion(db, 15); err != nil {
		return fmt.Errorf("%w: v15 validation: %v", ErrInvalidStore, err)
	}
	if err = checkBlobFiles(db, filepath.Join(path, "blobs")); err != nil {
		return fmt.Errorf("%w: v15 blobs: %v", ErrInvalidStore, err)
	}
	backup := filepath.Join(path, "authority-v15.backup.db")
	if _, statErr := os.Lstat(backup); !errors.Is(statErr, os.ErrNotExist) {
		if statErr != nil {
			return statErr
		}
		return fmt.Errorf("%w: v15 backup already exists; inspect before retry", ErrInvalidStore)
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
	if err = errors.Join(checkSchemaVersion(retained, 15), retained.Close()); err != nil {
		return err
	}
	if err = sameStoreContents(db, backup); err != nil {
		return err
	}
	if err = syncFileAndDirectory(backup, path); err != nil {
		return err
	}
	if err = installStep17(db); err != nil {
		return err
	}
	if err = installBatchSweep(db); err != nil {
		return err
	}
	if err = installDependencies(db); err != nil {
		return err
	}
	return checkSchema(db)
}
