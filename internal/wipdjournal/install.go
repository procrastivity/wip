package wipdjournal

import (
	"bytes"
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/binary"
	"encoding/hex"
	"fmt"
	"math"

	"github.com/procrastivity/wip/internal/operation"
	"github.com/procrastivity/wip/internal/wipdwire"
)

// InstallExpectation pins an atomic Environment update to one stable local
// base and overlay revision.
type InstallExpectation struct {
	Revision       uint64
	Anchor         wipdwire.PrefixAnchor
	ManifestDigest string
}

// InstalledReceipt is terminal Environment evidence bound to one immutable
// journal entry.
type InstalledReceipt struct {
	RequestHash      string
	EnvironmentSeq   uint64
	JournalPosition  uint64
	ResultCode       operation.ResultCode
	CanonicalReceipt []byte
}

// InstallSnapshot is a stable read of the installed authority prefix,
// complete manifest, terminal receipt index, and overlay revision.
type InstallSnapshot struct {
	Identity       Identity
	Revision       uint64
	Anchor         wipdwire.PrefixAnchor
	ManifestDigest string
	Receipts       map[string]InstalledReceipt
}

// Expectation returns the revision and installed prefix used to condition the
// next local transaction.
func (snapshot InstallSnapshot) Expectation() InstallExpectation {
	return InstallExpectation{Revision: snapshot.Revision, Anchor: cloneTransferAnchor(snapshot.Anchor), ManifestDigest: snapshot.ManifestDigest}
}

// OverlayItem is one durable overlay source. Folded entries contain exact
// authority event records; provisional entries contain exact canonical
// journal commands. The overlay is rebuilt from these immutable sources in
// the same transaction as every prefix, receipt, or admission change.
type OverlayItem struct {
	Kind           string
	ID             string
	EnvironmentSeq uint64
	SourceBytes    []byte
}

type sqlExecutor interface {
	Exec(string, ...any) (sql.Result, error)
}

func createEnvironmentInstallSchema(executor sqlExecutor) error {
	for _, statement := range []string{
		`CREATE TABLE environment_install(singleton INTEGER PRIMARY KEY CHECK(singleton=1),revision INTEGER NOT NULL CHECK(revision>0),event_count INTEGER NOT NULL CHECK(event_count>=0),high_water_event_id TEXT,prefix_digest TEXT NOT NULL CHECK(length(prefix_digest)=71),manifest_digest TEXT NOT NULL CHECK(length(manifest_digest)=71),manifest BLOB NOT NULL,CHECK((event_count=0 AND high_water_event_id IS NULL) OR (event_count>0 AND high_water_event_id IS NOT NULL))) STRICT`,
		`CREATE TRIGGER environment_install_revision BEFORE UPDATE ON environment_install WHEN NEW.singleton!=OLD.singleton OR NEW.revision!=OLD.revision+1 BEGIN SELECT RAISE(ABORT,'invalid installed Environment revision'); END`,
		`CREATE TABLE installed_events(position INTEGER PRIMARY KEY CHECK(position>0),event_id TEXT NOT NULL UNIQUE,record BLOB NOT NULL) STRICT`,
		`CREATE TRIGGER installed_event_no_update BEFORE UPDATE ON installed_events BEGIN SELECT RAISE(ABORT,'immutable installed event'); END`,
		`CREATE TRIGGER installed_event_no_delete BEFORE DELETE ON installed_events BEGIN SELECT RAISE(ABORT,'retained installed event'); END`,
		`CREATE TABLE installed_receipts(command_id TEXT PRIMARY KEY,request_hash TEXT NOT NULL CHECK(length(request_hash)=71),environment_sequence INTEGER NOT NULL CHECK(environment_sequence>0),journal_position INTEGER NOT NULL CHECK(journal_position>0),result_code TEXT NOT NULL CHECK(result_code IN ('result.succeeded','result.rejected','result.refused','result.failed')),canonical_receipt BLOB NOT NULL,first_event_id TEXT,last_event_id TEXT,event_count INTEGER NOT NULL CHECK(event_count>=0),first_position INTEGER,last_position INTEGER,CHECK((event_count=0 AND first_event_id IS NULL AND last_event_id IS NULL AND first_position IS NULL AND last_position IS NULL) OR (event_count>0 AND first_event_id IS NOT NULL AND last_event_id IS NOT NULL AND first_position IS NOT NULL AND last_position IS NOT NULL AND last_position-first_position+1=event_count))) STRICT, WITHOUT ROWID`,
		`CREATE TRIGGER installed_receipt_no_update BEFORE UPDATE ON installed_receipts BEGIN SELECT RAISE(ABORT,'immutable installed receipt'); END`,
		`CREATE TRIGGER installed_receipt_no_delete BEFORE DELETE ON installed_receipts BEGIN SELECT RAISE(ABORT,'retained installed receipt'); END`,
		`CREATE TABLE environment_overlay(item_kind TEXT NOT NULL CHECK(item_kind IN ('folded','provisional')),item_id TEXT NOT NULL,environment_sequence INTEGER,source_bytes BLOB NOT NULL,PRIMARY KEY(item_kind,item_id),CHECK((item_kind='folded' AND environment_sequence IS NULL) OR (item_kind='provisional' AND environment_sequence IS NOT NULL AND environment_sequence>0))) STRICT, WITHOUT ROWID`,
		`CREATE INDEX environment_overlay_order ON environment_overlay(item_kind,environment_sequence,item_id)`,
	} {
		if _, err := executor.Exec(statement); err != nil {
			return err
		}
	}
	return nil
}

// InstallSnapshot returns a stable Environment installation and receipt view.
func (j *Journal) InstallSnapshot(ctx context.Context) (InstallSnapshot, error) {
	if j == nil || ctx == nil {
		return InstallSnapshot{}, ErrClosed
	}
	j.mu.Lock()
	defer j.mu.Unlock()
	if j.db == nil {
		return InstallSnapshot{}, ErrClosed
	}
	tx, err := j.db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return InstallSnapshot{}, err
	}
	defer func() { _ = tx.Rollback() }()
	snapshot, err := loadInstallSnapshot(tx, j.identity)
	if err != nil {
		return InstallSnapshot{}, err
	}
	if err = tx.Commit(); err != nil {
		return InstallSnapshot{}, err
	}
	return snapshot, nil
}

// Overlay returns the currently committed folded/provisional source index.
func (j *Journal) Overlay(ctx context.Context) ([]OverlayItem, error) {
	if j == nil || ctx == nil {
		return nil, ErrClosed
	}
	j.mu.Lock()
	defer j.mu.Unlock()
	if j.db == nil {
		return nil, ErrClosed
	}
	rows, err := j.db.QueryContext(ctx, `SELECT item_kind,item_id,environment_sequence,source_bytes FROM environment_overlay ORDER BY item_kind,environment_sequence,item_id`)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	items := make([]OverlayItem, 0)
	for rows.Next() {
		var item OverlayItem
		var sequence sql.NullInt64
		if err = rows.Scan(&item.Kind, &item.ID, &sequence, &item.SourceBytes); err != nil {
			return nil, err
		}
		if sequence.Valid {
			item.EnvironmentSeq = uint64(sequence.Int64)
		}
		item.SourceBytes = bytes.Clone(item.SourceBytes)
		items = append(items, item)
	}
	return items, rows.Err()
}

// AdmitPending atomically materializes the provisional overlay and changes a
// pre-admission command to pending-return. A repeated call for an already
// admitted identity is read-only and never rebuilds or duplicates the row.
func (j *Journal) AdmitPending(ctx context.Context, expected InstallExpectation, commandID string) (InstallSnapshot, error) {
	return j.installTransaction(ctx, expected, func(tx *sql.Tx, state storedInstallState) error {
		entry, err := lookupTx(tx, commandID)
		if err != nil {
			return err
		}
		if entry.Delivery == operation.DeliveryAuthority || entry.JournalPosition == 0 {
			return ErrInvalidJournal
		}
		if entry.State == StatePendingReturn {
			return nil
		}
		if entry.State != StatePreAdmission {
			return ErrInvalidJournal
		}
		if _, err = tx.ExecContext(ctx, `UPDATE commands SET state='pending-return' WHERE command_id=? AND state='pre-admission'`, commandID); err != nil {
			return err
		}
		if err = rebuildOverlay(ctx, tx); err != nil {
			return err
		}
		return updateInstallState(ctx, tx, state, state.anchor, state.manifestDigest, state.manifest)
	})
}

// InstallPull atomically installs a complete verified tail, its complete
// manifest, and an overlay rebuilt from the resulting base and journal.
func (j *Journal) InstallPull(ctx context.Context, expected InstallExpectation, transfer VerifiedTransfer) (InstallSnapshot, error) {
	return j.installTransaction(ctx, expected, func(tx *sql.Tx, state storedInstallState) error {
		if !transfer.Valid() || transfer.domainID != j.identity.DomainID || transfer.epoch != j.identity.AuthorityEpoch ||
			!sameTransferAnchor(transfer.start, state.anchor) {
			return ErrInvalidTransfer
		}
		if err := appendVerifiedEvents(ctx, tx, state.anchor.EventCount, transfer.records); err != nil {
			return err
		}
		manifest, err := encodeManifest(transfer.manifest)
		if err != nil {
			return err
		}
		if err = rebuildOverlay(ctx, tx); err != nil {
			return err
		}
		return updateInstallState(ctx, tx, state, transfer.end, transfer.manifest.Digest, manifest)
	})
}

// InstallFold atomically installs the command's exact receipt and verified
// event delta, advances its journal disposition to returned, and rebuilds the
// folded/provisional overlay. Receipt ranges are matched against the verified
// delta or existing installed lineage, never caller-supplied event IDs.
func (j *Journal) InstallFold(ctx context.Context, expected InstallExpectation, entry Entry, result operation.ResultCode, receipt []byte, transfer VerifiedTransfer) (InstallSnapshot, error) {
	return j.installTransaction(ctx, expected, func(tx *sql.Tx, state storedInstallState) error {
		persisted, err := lookupTx(tx, entry.Command.ID)
		if err != nil || !sameInstalledCommand(persisted, entry) || persisted.State != StatePendingReturn ||
			!transfer.Valid() || transfer.domainID != j.identity.DomainID || transfer.epoch != j.identity.AuthorityEpoch ||
			!sameTransferAnchor(transfer.start, state.anchor) {
			return ErrInvalidTransfer
		}
		eventIDs, receiptFirstPosition, receiptLastPosition, err := receiptEventRange(ctx, tx, state.anchor, transfer, persisted, result, receipt)
		if err != nil || !validFoldEvents(transfer, persisted, eventIDs) {
			return ErrInvalidTransfer
		}
		if err = validateTerminalReceipt(persisted, result, receipt, eventIDs); err != nil {
			return err
		}
		if err = appendVerifiedEvents(ctx, tx, state.anchor.EventCount, transfer.records); err != nil {
			return err
		}
		manifest, err := encodeManifest(transfer.manifest)
		if err != nil {
			return err
		}
		var firstID, lastID, firstPosition, lastPosition any
		count := len(eventIDs)
		if count > 0 {
			firstID, lastID = eventIDs[0], eventIDs[count-1]
			firstPosition, lastPosition = receiptFirstPosition, receiptLastPosition
		}
		if _, err = tx.ExecContext(ctx, `INSERT INTO installed_receipts(command_id,request_hash,environment_sequence,journal_position,result_code,canonical_receipt,first_event_id,last_event_id,event_count,first_position,last_position)
			VALUES(?,?,?,?,?,?,?,?,?,?,?)`, persisted.Command.ID, persisted.RequestHash, persisted.EnvironmentSeq, persisted.JournalPosition,
			string(result), receipt, firstID, lastID, count, firstPosition, lastPosition); err != nil {
			return err
		}
		if _, err = tx.ExecContext(ctx, `UPDATE commands SET state='returned' WHERE command_id=? AND state='pending-return'`, persisted.Command.ID); err != nil {
			return err
		}
		if err = rebuildOverlay(ctx, tx); err != nil {
			return err
		}
		return updateInstallState(ctx, tx, state, transfer.end, transfer.manifest.Digest, manifest)
	})
}

func sameInstalledCommand(left, right Entry) bool {
	return left.Command.ID == right.Command.ID && left.RequestHash == right.RequestHash &&
		left.EnvironmentSeq == right.EnvironmentSeq && left.JournalPosition == right.JournalPosition &&
		left.Delivery == right.Delivery && bytes.Equal(left.CanonicalBytes, right.CanonicalBytes)
}

func (j *Journal) installTransaction(ctx context.Context, expected InstallExpectation, apply func(*sql.Tx, storedInstallState) error) (InstallSnapshot, error) {
	if j == nil || ctx == nil {
		return InstallSnapshot{}, ErrClosed
	}
	j.mu.Lock()
	defer j.mu.Unlock()
	if j.db == nil {
		return InstallSnapshot{}, ErrClosed
	}
	tx, err := j.db.BeginTx(ctx, nil)
	if err != nil {
		return InstallSnapshot{}, err
	}
	defer func() { _ = tx.Rollback() }()
	state, err := readInstallState(tx)
	if err != nil {
		return InstallSnapshot{}, err
	}
	if state.revision != expected.Revision || !sameTransferAnchor(state.anchor, expected.Anchor) || state.manifestDigest != expected.ManifestDigest {
		return InstallSnapshot{}, ErrInvalidJournal
	}
	if err = apply(tx, state); err != nil {
		return InstallSnapshot{}, err
	}
	if err = tx.Commit(); err != nil {
		return InstallSnapshot{}, err
	}
	return j.installSnapshotLocked(ctx)
}

func (j *Journal) installSnapshotLocked(ctx context.Context) (InstallSnapshot, error) {
	tx, err := j.db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return InstallSnapshot{}, err
	}
	defer func() { _ = tx.Rollback() }()
	snapshot, err := loadInstallSnapshot(tx, j.identity)
	if err != nil {
		return InstallSnapshot{}, err
	}
	if err = tx.Commit(); err != nil {
		return InstallSnapshot{}, err
	}
	return snapshot, nil
}

type storedInstallState struct {
	revision       uint64
	anchor         wipdwire.PrefixAnchor
	manifestDigest string
	manifest       []byte
}

func readInstallState(tx *sql.Tx) (storedInstallState, error) {
	var state storedInstallState
	var revision, eventCount int64
	var eventID sql.NullString
	if err := tx.QueryRow(`SELECT revision,event_count,high_water_event_id,prefix_digest,manifest_digest,manifest FROM environment_install WHERE singleton=1`).
		Scan(&revision, &eventCount, &eventID, &state.anchor.Digest, &state.manifestDigest, &state.manifest); err != nil {
		return state, err
	}
	if revision <= 0 || eventCount < 0 {
		return state, ErrInvalidJournal
	}
	state.revision = uint64(revision)
	state.anchor.EventCount = uint64(eventCount)
	if eventID.Valid {
		state.anchor.EventID = &eventID.String
	}
	return state, nil
}

func loadInstallSnapshot(tx *sql.Tx, identity Identity) (InstallSnapshot, error) {
	state, err := readInstallState(tx)
	if err != nil {
		return InstallSnapshot{}, err
	}
	snapshot := InstallSnapshot{
		Identity: identity, Revision: state.revision, Anchor: cloneTransferAnchor(state.anchor),
		ManifestDigest: state.manifestDigest, Receipts: make(map[string]InstalledReceipt),
	}
	rows, err := tx.Query(`SELECT command_id,request_hash,environment_sequence,journal_position,result_code,canonical_receipt FROM installed_receipts ORDER BY journal_position`)
	if err != nil {
		return InstallSnapshot{}, err
	}
	defer func() { _ = rows.Close() }()
	for rows.Next() {
		var id, hash, result string
		var sequence, position int64
		var receipt []byte
		if err = rows.Scan(&id, &hash, &sequence, &position, &result, &receipt); err != nil {
			return InstallSnapshot{}, err
		}
		if sequence <= 0 || position <= 0 {
			return InstallSnapshot{}, ErrInvalidJournal
		}
		snapshot.Receipts[id] = InstalledReceipt{
			RequestHash: hash, EnvironmentSeq: uint64(sequence), JournalPosition: uint64(position),
			ResultCode: operation.ResultCode(result), CanonicalReceipt: bytes.Clone(receipt),
		}
	}
	if err = rows.Err(); err != nil {
		return InstallSnapshot{}, err
	}
	return snapshot, nil
}

func updateInstallState(ctx context.Context, tx *sql.Tx, state storedInstallState, anchor wipdwire.PrefixAnchor, manifestDigest string, manifest []byte) error {
	if state.revision >= math.MaxInt64 || !validTransferAnchor(anchor) || !transferHash.MatchString(manifestDigest) {
		return ErrInvalidJournal
	}
	var eventID any
	if anchor.EventID != nil {
		eventID = *anchor.EventID
	}
	_, err := tx.ExecContext(ctx, `UPDATE environment_install SET revision=?,event_count=?,high_water_event_id=?,prefix_digest=?,manifest_digest=?,manifest=? WHERE singleton=1 AND revision=?`,
		state.revision+1, anchor.EventCount, eventID, anchor.Digest, manifestDigest, manifest, state.revision)
	return err
}

func appendVerifiedEvents(ctx context.Context, tx *sql.Tx, start uint64, records []wipdwire.EventRecord) error {
	for index, record := range records {
		position := start + uint64(index) + 1
		if _, err := tx.ExecContext(ctx, `INSERT INTO installed_events(position,event_id,record) VALUES(?,?,?)`, position, record.EventID, record.Record); err != nil {
			return err
		}
	}
	return nil
}

// receiptEventRange resolves a successful receipt's accepted range against the
// exact installed lineage plus this fold's verified delta. This permits an
// idempotent receipt replay whose events were already pulled, while rejecting
// caller-asserted or phantom IDs.
func receiptEventRange(ctx context.Context, tx *sql.Tx, installed wipdwire.PrefixAnchor, transfer VerifiedTransfer, entry Entry, result operation.ResultCode, raw []byte) ([]string, int64, int64, error) {
	if result != operation.ResultSucceeded {
		return nil, 0, 0, nil
	}
	fields, err := wipdwire.DecodeCanonicalMap(raw,
		"schema", "domain_id", "authority_epoch", "identity_schema", "command_id", "request_hash", "operation", "environment", "result", "accepted_events")
	if err != nil {
		return nil, 0, 0, ErrInvalidTransfer
	}
	rangeFields, ok := fields["accepted_events"].(map[string]any)
	if !ok || !wipdwire.ExactMapKeys(rangeFields, "first_event_id", "last_event_id", "event_count") {
		return nil, 0, 0, ErrInvalidTransfer
	}
	firstID, firstOK := rangeFields["first_event_id"].(string)
	lastID, lastOK := rangeFields["last_event_id"].(string)
	count, countOK := rangeFields["event_count"].(uint64)
	if !firstOK || !lastOK || !countOK || count == 0 || count > maxVerifiedEvents ||
		!transferULID.MatchString(firstID) || !transferULID.MatchString(lastID) || firstID > lastID || installed.EventCount > math.MaxInt64 {
		return nil, 0, 0, ErrInvalidTransfer
	}
	positionForID := func(eventID string) (int64, error) {
		for index, record := range transfer.records {
			if record.EventID == eventID {
				return int64(installed.EventCount) + int64(index) + 1, nil
			}
		}
		var position int64
		if err := tx.QueryRowContext(ctx, `SELECT position FROM installed_events WHERE event_id=?`, eventID).Scan(&position); err != nil || position <= 0 || uint64(position) > installed.EventCount {
			return 0, ErrInvalidTransfer
		}
		return position, nil
	}
	firstPosition, err := positionForID(firstID)
	if err != nil {
		return nil, 0, 0, err
	}
	lastPosition, err := positionForID(lastID)
	if err != nil || lastPosition < firstPosition || lastPosition-firstPosition+1 != int64(count) ||
		uint64(lastPosition) > transfer.end.EventCount {
		return nil, 0, 0, ErrInvalidTransfer
	}
	ids := make([]string, 0, int(count))
	for position := firstPosition; position <= lastPosition; position++ {
		var eventID string
		var record []byte
		if uint64(position) <= installed.EventCount {
			if err = tx.QueryRowContext(ctx, `SELECT event_id,record FROM installed_events WHERE position=?`, position).Scan(&eventID, &record); err != nil {
				return nil, 0, 0, ErrInvalidTransfer
			}
		} else {
			index := uint64(position) - installed.EventCount - 1
			if index >= uint64(len(transfer.records)) {
				return nil, 0, 0, ErrInvalidTransfer
			}
			verified := transfer.records[index]
			eventID, record = verified.EventID, verified.Record
		}
		if !transferULID.MatchString(eventID) || !eventMatchesCommand(record, entry) || len(ids) > 0 && ids[len(ids)-1] >= eventID {
			return nil, 0, 0, ErrInvalidTransfer
		}
		ids = append(ids, eventID)
	}
	if len(ids) != int(count) || ids[0] != firstID || ids[len(ids)-1] != lastID {
		return nil, 0, 0, ErrInvalidTransfer
	}
	return ids, firstPosition, lastPosition, nil
}

func validFoldEvents(transfer VerifiedTransfer, entry Entry, acceptedEventIDs []string) bool {
	if !transfer.Valid() {
		return false
	}
	accepted := make(map[string]struct{}, len(acceptedEventIDs))
	for _, eventID := range acceptedEventIDs {
		accepted[eventID] = struct{}{}
	}
	for _, record := range transfer.records {
		fields, err := wipdwire.DecodeCanonicalMap(record.Record,
			"schema", "event_id", "domain_id", "command_id", "request_hash", "environment", "acted_at", "occurred_at", "kind", "subject_id", "repo_id", "payload")
		if err != nil {
			return false
		}
		if fields["command_id"] == entry.Command.ID {
			if !eventMatchesCommand(record.Record, entry) {
				return false
			}
			if _, ok := accepted[record.EventID]; !ok {
				return false
			}
		}
		if !validMatterEvent(record.Record, transfer.domainID, record.EventID) {
			return false
		}
	}
	return true
}

func eventMatchesCommand(record []byte, entry Entry) bool {
	fields, err := wipdwire.DecodeCanonicalMap(record,
		"schema", "event_id", "domain_id", "command_id", "request_hash", "environment", "acted_at", "occurred_at", "kind", "subject_id", "repo_id", "payload")
	if err != nil || fields["command_id"] != entry.Command.ID || fields["request_hash"] != entry.RequestHash {
		return false
	}
	environment, ok := fields["environment"].(map[string]any)
	return ok && environment["id"] == entry.Command.EnvironmentID && environment["sequence"] == entry.EnvironmentSeq
}

func validateTerminalReceipt(entry Entry, result operation.ResultCode, raw []byte, eventIDs []string) error {
	fields, err := wipdwire.DecodeCanonicalMap(raw,
		"schema", "domain_id", "authority_epoch", "identity_schema", "command_id", "request_hash", "operation", "environment", "result", "accepted_events")
	if err != nil || fields["schema"] != "wipd.terminal-receipt/1" || fields["domain_id"] != entry.Command.AuthorityDomainID ||
		fields["authority_epoch"] != entry.Command.ExpectedAuthorityEpoch || fields["identity_schema"] != "wipd.command/1" ||
		fields["command_id"] != entry.Command.ID || fields["request_hash"] != entry.RequestHash {
		return ErrInvalidTransfer
	}
	operationFields, ok := fields["operation"].(map[string]any)
	if !ok || !wipdwire.ExactMapKeys(operationFields, "name", "version") || operationFields["name"] != entry.Command.Request.Operation.Name ||
		operationFields["version"] != uint64(entry.Command.Request.Operation.Version) {
		return ErrInvalidTransfer
	}
	environment, ok := fields["environment"].(map[string]any)
	if !ok || !wipdwire.ExactMapKeys(environment, "id", "sequence") || environment["id"] != entry.Command.EnvironmentID || environment["sequence"] != entry.EnvironmentSeq {
		return ErrInvalidTransfer
	}
	resultFields, ok := fields["result"].(map[string]any)
	if !ok || !wipdwire.ExactMapKeys(resultFields, "code", "output", "problem_code") || resultFields["code"] != string(result) ||
		(result != operation.ResultSucceeded && result != operation.ResultRejected && result != operation.ResultRefused && result != operation.ResultFailed) {
		return ErrInvalidTransfer
	}
	accepted := fields["accepted_events"]
	if result == operation.ResultSucceeded {
		if resultFields["problem_code"] != nil {
			return ErrInvalidTransfer
		}
		if _, ok = resultFields["output"].([]byte); !ok {
			return ErrInvalidTransfer
		}
		if len(eventIDs) == 0 {
			return fmt.Errorf("%w: successful effectful fold has no verified events", ErrInvalidTransfer)
		}
		rangeFields, ok := accepted.(map[string]any)
		if !ok || !wipdwire.ExactMapKeys(rangeFields, "first_event_id", "last_event_id", "event_count") ||
			rangeFields["first_event_id"] != eventIDs[0] || rangeFields["last_event_id"] != eventIDs[len(eventIDs)-1] ||
			rangeFields["event_count"] != uint64(len(eventIDs)) {
			return ErrInvalidTransfer
		}
	} else {
		problem, ok := resultFields["problem_code"].(string)
		if !ok || problem == "" || resultFields["output"] != nil || accepted != nil || len(eventIDs) != 0 {
			return ErrInvalidTransfer
		}
	}
	return nil
}

func encodeManifest(manifest wipdwire.BlobManifest) ([]byte, error) {
	return wipdwire.EncodeCanonical(map[string]any{
		"schema": manifest.Schema, "domain_id": manifest.DomainID, "authority_epoch": manifest.Epoch,
		"as_of": map[string]any{
			"event_count": manifest.AsOf.EventCount, "high_water_event_id": manifest.AsOf.EventID, "prefix_digest": manifest.AsOf.Digest,
		},
		"entries": manifest.Entries, "manifest_digest": manifest.Digest,
	})
}

func initializeEnvironmentInstall(tx *sql.Tx, identity Identity) error {
	anchor := emptyTransferAnchor()
	manifest := emptyTransferManifest(identity.DomainID, identity.AuthorityEpoch, anchor)
	encoded, err := encodeManifest(manifest)
	if err != nil {
		return err
	}
	_, err = tx.Exec(`INSERT INTO environment_install(singleton,revision,event_count,high_water_event_id,prefix_digest,manifest_digest,manifest)
		VALUES(1,1,0,NULL,?,?,?)`, anchor.Digest, manifest.Digest, encoded)
	return err
}

func emptyTransferAnchor() wipdwire.PrefixAnchor {
	empty := sha256.Sum256([]byte("wipd/event-prefix/v1\x00"))
	return wipdwire.PrefixAnchor{Digest: "sha256:" + hex.EncodeToString(empty[:])}
}

func emptyTransferManifest(domainID string, epoch uint64, anchor wipdwire.PrefixAnchor) wipdwire.BlobManifest {
	empty := sha256.Sum256([]byte("wipd/blob-manifest/v1\x00"))
	return wipdwire.BlobManifest{
		Schema: "wipd.blob-manifest/1", DomainID: domainID, Epoch: epoch, AsOf: cloneTransferAnchor(anchor),
		Entries: []wipdwire.BlobManifestEntry{}, Digest: "sha256:" + hex.EncodeToString(empty[:]),
	}
}

func rebuildOverlay(ctx context.Context, tx *sql.Tx) error {
	if _, err := tx.ExecContext(ctx, `DELETE FROM environment_overlay`); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO environment_overlay(item_kind,item_id,environment_sequence,source_bytes)
		SELECT 'folded',event_id,NULL,record FROM installed_events ORDER BY position`); err != nil {
		return err
	}
	_, err := tx.ExecContext(ctx, `INSERT INTO environment_overlay(item_kind,item_id,environment_sequence,source_bytes)
		SELECT 'provisional',command_id,environment_sequence,canonical_bytes FROM commands WHERE state='pending-return' ORDER BY journal_position`)
	return err
}

func lookupTx(tx *sql.Tx, commandID string) (Entry, error) {
	if !identityPattern.MatchString(commandID) {
		return Entry{}, ErrNotFound
	}
	return scanEntry(tx.QueryRow(`SELECT `+commandColumns+` FROM commands WHERE command_id=?`, commandID))
}

func checkInstallationDatabase(db *sql.DB, identity Identity) error {
	tx, err := db.BeginTx(context.Background(), &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	state, err := readInstallState(tx)
	if err != nil || !validTransferAnchor(state.anchor) || !transferHash.MatchString(state.manifestDigest) {
		return ErrInvalidJournal
	}
	var manifest wipdwire.BlobManifest
	if err = wipdwire.DecodeCanonical(state.manifest, &manifest, "schema", "domain_id", "authority_epoch", "as_of", "entries", "manifest_digest"); err != nil ||
		manifest.DomainID != identity.DomainID || manifest.Epoch != identity.AuthorityEpoch || manifest.Digest != state.manifestDigest ||
		!sameTransferAnchor(manifest.AsOf, state.anchor) || verifyManifest(manifest) != nil {
		return ErrInvalidJournal
	}
	rows, err := tx.Query(`SELECT position,event_id,record FROM installed_events ORDER BY position`)
	if err != nil {
		return err
	}
	chain := sha256.Sum256([]byte("wipd/event-prefix/v1\x00"))
	previous := ""
	var count uint64
	for rows.Next() {
		var position int64
		var eventID string
		var record []byte
		if err = rows.Scan(&position, &eventID, &record); err != nil {
			break
		}
		if position != int64(count+1) || !transferULID.MatchString(eventID) || previous != "" && eventID <= previous || !validMatterEvent(record, identity.DomainID, eventID) {
			err = ErrInvalidJournal
			break
		}
		var length [8]byte
		binary.BigEndian.PutUint64(length[:], uint64(len(record)))
		h := sha256.New()
		_, _ = h.Write([]byte("wipd/event-prefix-step/v1\x00"))
		_, _ = h.Write(chain[:])
		_, _ = h.Write(length[:])
		_, _ = h.Write(record)
		copy(chain[:], h.Sum(nil))
		count++
		previous = eventID
	}
	if err == nil {
		err = rows.Err()
	}
	_ = rows.Close()
	if err != nil || count != state.anchor.EventCount || "sha256:"+hex.EncodeToString(chain[:]) != state.anchor.Digest ||
		(count == 0 && state.anchor.EventID != nil) || (count > 0 && (state.anchor.EventID == nil || *state.anchor.EventID != previous)) {
		return ErrInvalidJournal
	}
	if err = checkInstalledReceipts(tx, identity); err != nil {
		return err
	}
	if err = checkOverlay(tx); err != nil {
		return err
	}
	return tx.Commit()
}

func checkInstalledReceipts(tx *sql.Tx, identity Identity) error {
	rows, err := tx.Query(`SELECT command_id,request_hash,environment_sequence,journal_position,result_code,canonical_receipt,event_count,first_position,last_position,first_event_id,last_event_id FROM installed_receipts ORDER BY journal_position`)
	if err != nil {
		return err
	}
	defer func() { _ = rows.Close() }()
	seen := make(map[string]struct{})
	for rows.Next() {
		var id, hash, result string
		var sequence, position, eventCount int64
		var receipt []byte
		var firstPosition, lastPosition sql.NullInt64
		var firstID, lastID sql.NullString
		if err = rows.Scan(&id, &hash, &sequence, &position, &result, &receipt, &eventCount, &firstPosition, &lastPosition, &firstID, &lastID); err != nil {
			return err
		}
		entry, lookupErr := lookupTx(tx, id)
		if lookupErr != nil || entry.State != StateReturned || entry.RequestHash != hash || int64(entry.EnvironmentSeq) != sequence ||
			int64(entry.JournalPosition) != position || entry.Command.AuthorityDomainID != identity.DomainID || entry.Command.ExpectedAuthorityEpoch != identity.AuthorityEpoch {
			return ErrInvalidJournal
		}
		if eventCount < 0 || eventCount > maxVerifiedEvents {
			return ErrInvalidJournal
		}
		ids := make([]string, 0, int(eventCount))
		if eventCount > 0 {
			if !firstPosition.Valid || !lastPosition.Valid || !firstID.Valid || !lastID.Valid || lastPosition.Int64-firstPosition.Int64+1 != eventCount {
				return ErrInvalidJournal
			}
			eventRows, queryErr := tx.Query(`SELECT event_id,record FROM installed_events WHERE position BETWEEN ? AND ? ORDER BY position`, firstPosition.Int64, lastPosition.Int64)
			if queryErr != nil {
				return queryErr
			}
			for eventRows.Next() {
				var eventID string
				var record []byte
				if queryErr = eventRows.Scan(&eventID, &record); queryErr != nil {
					break
				}
				if !eventMatchesCommand(record, entry) {
					queryErr = ErrInvalidJournal
					break
				}
				ids = append(ids, eventID)
			}
			if queryErr == nil {
				queryErr = eventRows.Err()
			}
			_ = eventRows.Close()
			if queryErr != nil || int64(len(ids)) != eventCount || ids[0] != firstID.String || ids[len(ids)-1] != lastID.String {
				return ErrInvalidJournal
			}
		}
		if err = validateTerminalReceipt(entry, operation.ResultCode(result), receipt, ids); err != nil {
			return ErrInvalidJournal
		}
		seen[id] = struct{}{}
	}
	if err = rows.Err(); err != nil {
		return err
	}
	entries, err := listEntries(tx, `SELECT `+commandColumns+` FROM commands WHERE state='returned'`)
	if err != nil {
		return err
	}
	for _, entry := range entries {
		if _, ok := seen[entry.Command.ID]; !ok {
			return ErrInvalidJournal
		}
	}
	return nil
}

func checkOverlay(tx *sql.Tx) error {
	var folded, expectedFolded int
	if err := tx.QueryRow(`SELECT count(*) FROM environment_overlay WHERE item_kind='folded'`).Scan(&folded); err != nil {
		return err
	}
	if err := tx.QueryRow(`SELECT count(*) FROM installed_events`).Scan(&expectedFolded); err != nil || folded != expectedFolded {
		return ErrInvalidJournal
	}
	var provisional, expectedProvisional int
	if err := tx.QueryRow(`SELECT count(*) FROM environment_overlay WHERE item_kind='provisional'`).Scan(&provisional); err != nil {
		return err
	}
	if err := tx.QueryRow(`SELECT count(*) FROM commands WHERE state='pending-return'`).Scan(&expectedProvisional); err != nil || provisional != expectedProvisional {
		return ErrInvalidJournal
	}
	var mismatch int
	if err := tx.QueryRow(`SELECT count(*) FROM installed_events e LEFT JOIN environment_overlay o ON o.item_kind='folded' AND o.item_id=e.event_id AND o.source_bytes=e.record WHERE o.item_id IS NULL`).Scan(&mismatch); err != nil || mismatch != 0 {
		return ErrInvalidJournal
	}
	if err := tx.QueryRow(`SELECT count(*) FROM commands c LEFT JOIN environment_overlay o ON o.item_kind='provisional' AND o.item_id=c.command_id AND o.environment_sequence=c.environment_sequence AND o.source_bytes=c.canonical_bytes WHERE c.state='pending-return' AND o.item_id IS NULL`).Scan(&mismatch); err != nil || mismatch != 0 {
		return ErrInvalidJournal
	}
	return nil
}

func upgradeSchemaV1(db *sql.DB, identity Identity) error {
	if err := checkV1Identity(db, identity); err != nil {
		return err
	}
	tx, err := db.BeginTx(context.Background(), nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	for _, statement := range []string{
		`DROP TRIGGER command_identity_immutable`, `DROP TRIGGER command_no_delete`, `DROP INDEX commands_pending_order`,
		`ALTER TABLE commands RENAME TO commands_v1`,
		`CREATE TABLE commands(
			command_id TEXT PRIMARY KEY, environment_sequence INTEGER NOT NULL UNIQUE CHECK(environment_sequence>0),
			journal_position INTEGER UNIQUE CHECK(journal_position IS NULL OR journal_position>0), request_hash TEXT NOT NULL CHECK(length(request_hash)=71),
			canonical_bytes BLOB NOT NULL, delivery TEXT NOT NULL CHECK(delivery IN ('authority','claim','provisional','capture','environment')),
			state TEXT NOT NULL CHECK(state IN ('attempt-prepared','pre-admission','pending-return','returned')),
			CHECK((delivery='authority' AND journal_position IS NULL AND state='attempt-prepared') OR
			(delivery!='authority' AND journal_position IS NOT NULL AND state IN ('pre-admission','pending-return','returned')))
		) STRICT, WITHOUT ROWID`,
		`INSERT INTO commands(command_id,environment_sequence,journal_position,request_hash,canonical_bytes,delivery,state)
			SELECT command_id,environment_sequence,journal_position,request_hash,canonical_bytes,delivery,
			CASE WHEN state='journaled' THEN 'pre-admission' ELSE state END FROM commands_v1`,
		`DROP TABLE commands_v1`,
		`CREATE INDEX commands_pending_order ON commands(state,journal_position)`,
		`CREATE TRIGGER command_identity_immutable BEFORE UPDATE OF command_id,environment_sequence,journal_position,request_hash,canonical_bytes,delivery ON commands
			BEGIN SELECT RAISE(ABORT,'immutable command identity'); END`,
		`CREATE TRIGGER command_no_delete BEFORE DELETE ON commands
			BEGIN SELECT RAISE(ABORT,'immutable command journal'); END`,
		`CREATE TRIGGER command_state_transition BEFORE UPDATE OF state ON commands
			WHEN NOT ((OLD.state='pre-admission' AND NEW.state='pending-return') OR (OLD.state='pending-return' AND NEW.state='returned'))
			BEGIN SELECT RAISE(ABORT,'invalid command disposition transition'); END`,
		`ALTER TABLE schema_migrations RENAME TO schema_migrations_v1`,
		`CREATE TABLE schema_migrations(version INTEGER PRIMARY KEY CHECK(version=2), name TEXT NOT NULL CHECK(name='environment-command-start-installation')) STRICT`,
		`INSERT INTO schema_migrations(version,name) VALUES(2,'environment-command-start-installation')`,
		`DROP TABLE schema_migrations_v1`,
		`PRAGMA user_version=2`,
	} {
		if _, err = tx.Exec(statement); err != nil {
			return err
		}
	}
	if err = createEnvironmentInstallSchema(tx); err != nil {
		return err
	}
	if err = initializeEnvironmentInstall(tx, identity); err != nil {
		return err
	}
	return tx.Commit()
}

func checkV1Identity(db *sql.DB, identity Identity) error {
	var repoID, domainID, environmentID, migration string
	var epoch int64
	if err := db.QueryRow(`SELECT repo_id,domain_id,authority_epoch,environment_id FROM environment_state WHERE singleton=1`).Scan(&repoID, &domainID, &epoch, &environmentID); err != nil {
		return err
	}
	if repoID != identity.RepoID || domainID != identity.DomainID || epoch != int64(identity.AuthorityEpoch) || environmentID != identity.EnvironmentID {
		return ErrInvalidIdentity
	}
	if err := db.QueryRow(`SELECT name FROM schema_migrations WHERE version=1`).Scan(&migration); err != nil || migration != "durable-client-command-journal" {
		return ErrInvalidJournal
	}
	return checkSchemaObjectsV1(db)
}

func checkSchemaObjectsV1(db *sql.DB) error {
	expected := map[string]string{
		"schema_migrations": "table", "environment_state": "table", "environment_identity_immutable": "trigger",
		"environment_counters_increment": "trigger", "commands": "table", "commands_pending_order": "index",
		"command_identity_immutable": "trigger", "command_no_delete": "trigger", "staged_blobs": "table",
		"staged_blob_no_update": "trigger", "staged_blob_no_delete": "trigger",
	}
	rows, err := db.Query(`SELECT type,name FROM sqlite_master WHERE name NOT LIKE 'sqlite_%'`)
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
			return ErrInvalidJournal
		}
		delete(expected, name)
	}
	if err = rows.Err(); err != nil {
		return err
	}
	if len(expected) != 0 {
		return ErrInvalidJournal
	}
	return nil
}
