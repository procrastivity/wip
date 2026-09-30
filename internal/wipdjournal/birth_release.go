package wipdjournal

import (
	"bytes"
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"time"

	"github.com/procrastivity/wip/internal/operation"
	"github.com/procrastivity/wip/internal/wipdwire"
)

// PrepareBirthRelease durably records one exact claim.release@v1 command
// before authority submission. It consumes the next Environment sequence but
// does not create an M1 journal position. Exact retries recover the original
// canonical bytes; another release cannot pass an unresolved attempt.
func (j *Journal) PrepareBirthRelease(commandID string, barrier wipdwire.JournalBarrier, actor string) (BirthReleaseCommand, error) {
	if j == nil || !identityPattern.MatchString(commandID) || actor == "" || !validBirthReleaseBarrier(barrier) {
		return BirthReleaseCommand{}, ErrInvalidCommand
	}
	j.mu.Lock()
	defer j.mu.Unlock()
	if j.db == nil {
		return BirthReleaseCommand{}, ErrClosed
	}
	if attempt, err := readBirthReleaseAttempt(j.db.QueryRow(`SELECT command_id,environment_sequence,request_hash,canonical_bytes,state,canonical_receipt,result_code FROM birth_release_attempts WHERE command_id=?`, commandID)); err == nil {
		if !bytes.Equal(encodeBirthBarrier(attempt.Barrier), encodeBirthBarrier(barrier)) || birthReleaseActor(attempt.CanonicalBytes) != actor {
			return BirthReleaseCommand{}, ErrCommandIDConflict
		}
		return attempt, nil
	} else if !errors.Is(err, sql.ErrNoRows) {
		return BirthReleaseCommand{}, err
	}
	var conflictingCommand int
	if err := j.db.QueryRow(`SELECT count(*) FROM commands WHERE command_id=?`, commandID).Scan(&conflictingCommand); err != nil {
		return BirthReleaseCommand{}, err
	}
	if conflictingCommand != 0 {
		return BirthReleaseCommand{}, ErrCommandIDConflict
	}
	var pending int
	if err := j.db.QueryRow(`SELECT count(*) FROM birth_release_attempts WHERE state='attempt-prepared'`).Scan(&pending); err != nil {
		return BirthReleaseCommand{}, err
	}
	if pending != 0 {
		return BirthReleaseCommand{}, ErrPendingBirthRelease
	}
	if hasInstalledBirthRelease(j.db, barrier.Journal) {
		return BirthReleaseCommand{}, ErrInvalidCommand
	}
	tx, err := j.db.BeginTx(context.Background(), nil)
	if err != nil {
		return BirthReleaseCommand{}, err
	}
	defer func() { _ = tx.Rollback() }()
	var nextSequence int64
	if err = tx.QueryRow(`SELECT next_environment_sequence FROM environment_state WHERE singleton=1`).Scan(&nextSequence); err != nil || nextSequence <= 0 {
		return BirthReleaseCommand{}, ErrInvalidJournal
	}
	identity := j.identity
	canonical, err := wipdwire.EncodeCanonical(map[string]any{
		"schema": "wipd.command/1", "command_id": commandID,
		"authority":   map[string]any{"domain_id": identity.DomainID, "expected_epoch": identity.AuthorityEpoch},
		"environment": map[string]any{"id": identity.EnvironmentID, "sequence": uint64(nextSequence)},
		"acted_at":    time.Now().UTC().Format(time.RFC3339Nano), "actor": actor,
		"causation_command_id": nil, "correlation_command_id": commandID,
		"operation": map[string]any{"name": "claim.release", "version": uint64(1)},
		"context":   map[string]any{"repo_id": identity.RepoID, "clone_id": nil, "worktree_id": nil},
		"claim":     map[string]any{"id": barrier.Claim.ID, "epoch": barrier.Claim.Epoch},
		"input":     map[string]any{"barrier": barrier}, "blobs": []any{},
	})
	if err != nil {
		return BirthReleaseCommand{}, ErrInvalidCommand
	}
	hashBytes := sha256.Sum256(append([]byte("wipd/request-hash/v1\x00"), canonical...))
	hash := "sha256:" + hex.EncodeToString(hashBytes[:])
	if _, err = tx.Exec(`INSERT INTO birth_release_attempts(command_id,environment_sequence,request_hash,canonical_bytes,state)
		VALUES(?,?,?,?,'attempt-prepared')`, commandID, nextSequence, hash, canonical); err != nil {
		return BirthReleaseCommand{}, err
	}
	if _, err = tx.Exec(`UPDATE environment_state SET next_environment_sequence=? WHERE singleton=1 AND next_environment_sequence=?`, nextSequence+1, nextSequence); err != nil {
		return BirthReleaseCommand{}, err
	}
	if err = tx.Commit(); err != nil {
		return BirthReleaseCommand{}, err
	}
	return BirthReleaseCommand{
		ID: commandID, RequestHash: hash, EnvironmentSeq: uint64(nextSequence),
		CanonicalBytes: bytes.Clone(canonical), Barrier: barrier,
	}, nil
}

// HasPendingBirthRelease reports whether a lifecycle submission may have
// crossed the authority boundary without its exact terminal receipt installed.
func (j *Journal) HasPendingBirthRelease() (bool, error) {
	if j == nil {
		return false, ErrClosed
	}
	j.mu.Lock()
	defer j.mu.Unlock()
	if j.db == nil {
		return false, ErrClosed
	}
	var count int
	err := j.db.QueryRow(`SELECT count(*) FROM birth_release_attempts WHERE state='attempt-prepared'`).Scan(&count)
	return count != 0, err
}

// BirthReleaseAttempt returns the exact persisted release identity, including
// an outcome-unknown attempt that must be retried without changing its barrier.
func (j *Journal) BirthReleaseAttempt(commandID string) (BirthReleaseCommand, error) {
	if j == nil || !identityPattern.MatchString(commandID) {
		return BirthReleaseCommand{}, ErrNotFound
	}
	j.mu.Lock()
	defer j.mu.Unlock()
	if j.db == nil {
		return BirthReleaseCommand{}, ErrClosed
	}
	attempt, err := readBirthReleaseAttempt(j.db.QueryRow(`SELECT command_id,environment_sequence,request_hash,canonical_bytes,state,canonical_receipt,result_code FROM birth_release_attempts WHERE command_id=?`, commandID))
	if errors.Is(err, sql.ErrNoRows) {
		return BirthReleaseCommand{}, ErrNotFound
	}
	return attempt, err
}

// ErrPendingBirthRelease blocks a new connected command while a prior release
// attempt remains outcome-unknown.
var ErrPendingBirthRelease = errors.New("wipdjournal: a birth-claim release outcome is unresolved")

// ErrBirthBarrierIncomplete means the complete authority-defined birth journal
// is not yet represented by successful terminal receipts installed locally.
var ErrBirthBarrierIncomplete = errors.New("wipdjournal: birth journal has unresolved or quarantined entries")

// BirthJournalReceipt binds one authority-ordered birth-journal position to
// the Environment's exact returned command and installed terminal receipt.
type BirthJournalReceipt struct {
	Position uint64
	Entry    Entry
	Receipt  InstalledReceipt
}

// BirthJournal constructs the candidate release barrier from one stable local
// transaction. Only the Matter birth and its causally bound Step commands are
// members; every member must already have a successful installed receipt whose
// accepted range exists in the installed event lineage.
func (j *Journal) BirthJournal(matterID string) (wipdwire.JournalBarrier, []BirthJournalReceipt, error) {
	var empty wipdwire.JournalBarrier
	if j == nil || !identityPattern.MatchString(matterID) {
		return empty, nil, ErrInvalidCommand
	}
	j.mu.Lock()
	defer j.mu.Unlock()
	if j.db == nil {
		return empty, nil, ErrClosed
	}
	tx, err := j.db.BeginTx(context.Background(), &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return empty, nil, err
	}
	defer func() { _ = tx.Rollback() }()
	entries, err := listEntries(tx, `SELECT `+commandColumns+` FROM commands ORDER BY environment_sequence`)
	if err != nil {
		return empty, nil, err
	}
	installed, err := loadInstallSnapshot(tx, j.identity)
	if err != nil {
		return empty, nil, err
	}
	var birth *Entry
	var members []Entry
	for index := range entries {
		entry := entries[index]
		birthOperation := entry.Command.Request.Operation == operation.MatterCreateV1.Metadata().Operation ||
			entry.Command.Request.Operation == operation.MatterCreateV2.Metadata().Operation
		if birthOperation {
			if receipt, ok := installed.Receipts[entry.Command.ID]; ok && receipt.ResultCode == operation.ResultSucceeded {
				fields, decodeErr := wipdwire.DecodeCanonicalMap(receipt.CanonicalReceipt,
					"schema", "domain_id", "authority_epoch", "identity_schema", "command_id", "request_hash", "operation", "environment", "result", "accepted_events")
				result, resultOK := fields["result"].(map[string]any)
				output, outputOK := result["output"].([]byte)
				if decodeErr != nil || !resultOK || !outputOK {
					return empty, nil, ErrInvalidJournal
				}
				var createdID string
				if entry.Command.Request.Operation == operation.MatterCreateV1.Metadata().Operation {
					created, outputErr := wipdwire.DecodeCanonicalMap(output, "id", "locator", "title")
					if outputErr != nil {
						return empty, nil, ErrInvalidJournal
					}
					createdID = asString(created["id"])
				} else {
					created, outputErr := wipdwire.DecodeCanonicalMap(output,
						"id", "title", "requested_locator", "assigned_locator", "locator_repair_required")
					if outputErr != nil {
						return empty, nil, ErrInvalidJournal
					}
					createdID = asString(created["id"])
				}
				if createdID == matterID {
					if birth != nil {
						return empty, nil, ErrInvalidJournal
					}
					birthEntry := entry
					birth = &birthEntry
					members = append(members, entry)
				}
			}
			continue
		}
		if entry.Command.Request.Operation != operation.StepCreateV1.Metadata().Operation {
			continue
		}
		input, ok := entry.Command.Request.Input.(operation.StepCreateInput)
		if !ok {
			return empty, nil, ErrInvalidJournal
		}
		claim := entry.Command.Request.Claim
		if input.ParentID == matterID || claim != nil && claim.ID == matterID {
			members = append(members, entry)
		}
	}
	if birth == nil || len(members) == 0 {
		return empty, nil, fmt.Errorf("%w: local successful Matter birth receipt was not found", ErrBirthBarrierIncomplete)
	}
	receipts := make([]BirthJournalReceipt, 0, len(members))
	barrierEntries := make([]wipdwire.JournalBarrierEntry, 0, len(members))
	for index, entry := range members {
		if entry.State != StateReturned || entry.Command.EnvironmentID != j.identity.EnvironmentID ||
			entry.Command.Request.Context.Repo != j.identity.RepoID {
			return empty, nil, fmt.Errorf("%w: a birth journal command is not returned in this Repo and Environment", ErrBirthBarrierIncomplete)
		}
		if index == 0 {
			if (entry.Command.Request.Operation != operation.MatterCreateV1.Metadata().Operation &&
				entry.Command.Request.Operation != operation.MatterCreateV2.Metadata().Operation) ||
				entry.Command.CausationCommandID != "" || entry.Command.CorrelationCommandID != birth.Command.ID {
				return empty, nil, fmt.Errorf("%w: Matter birth identity is inconsistent", ErrBirthBarrierIncomplete)
			}
		} else {
			input := entry.Command.Request.Input.(operation.StepCreateInput)
			claim := entry.Command.Request.Claim
			if input.ParentID != matterID || claim == nil || claim.ID != matterID || claim.Epoch != "1" ||
				entry.Command.CausationCommandID != birth.Command.ID || entry.Command.CorrelationCommandID != birth.Command.ID ||
				entry.EnvironmentSeq <= members[index-1].EnvironmentSeq {
				return empty, nil, fmt.Errorf("%w: Step does not bind to the earlier Matter birth", ErrBirthBarrierIncomplete)
			}
		}
		receipt, ok := installed.Receipts[entry.Command.ID]
		if !ok || receipt.RequestHash != entry.RequestHash || receipt.EnvironmentSeq != entry.EnvironmentSeq ||
			receipt.JournalPosition != entry.JournalPosition || receipt.ResultCode != operation.ResultSucceeded {
			return empty, nil, fmt.Errorf("%w: birth journal command lacks an installed successful receipt", ErrBirthBarrierIncomplete)
		}
		rangeValue, err := installedBirthReceiptRange(tx, installed.Anchor, entry, receipt)
		if err != nil || rangeValue == nil {
			return empty, nil, fmt.Errorf("%w: receipt accepted range is not installed command lineage", ErrBirthBarrierIncomplete)
		}
		position := uint64(index + 1)
		receipts = append(receipts, BirthJournalReceipt{Position: position, Entry: entry, Receipt: receipt})
		barrierEntries = append(barrierEntries, wipdwire.JournalBarrierEntry{
			Position: position, CommandID: entry.Command.ID, RequestHash: entry.RequestHash,
			ResultCode: string(receipt.ResultCode), Range: rangeValue,
		})
	}
	digest, err := wipdwire.JournalBarrierDigest(barrierEntries)
	if err != nil {
		return empty, nil, ErrInvalidJournal
	}
	barrier := wipdwire.JournalBarrier{
		Schema: "wipd.journal-barrier/1", Journal: matterID,
		Claim: wipdwire.ClaimRef{ID: matterID, Epoch: 1},
		Count: uint64(len(receipts)), Last: uint64(len(receipts)), Receipts: uint64(len(receipts)),
		Digest: digest, Sealed: true,
	}
	if err = tx.Commit(); err != nil {
		return empty, nil, err
	}
	return barrier, receipts, nil
}

func installedBirthReceiptRange(tx *sql.Tx, installed wipdwire.PrefixAnchor, entry Entry, receipt InstalledReceipt) (*wipdwire.JournalBarrierRange, error) {
	fields, err := wipdwire.DecodeCanonicalMap(receipt.CanonicalReceipt,
		"schema", "domain_id", "authority_epoch", "identity_schema", "command_id", "request_hash", "operation", "environment", "result", "accepted_events")
	if err != nil {
		return nil, ErrInvalidJournal
	}
	rangeFields, ok := fields["accepted_events"].(map[string]any)
	if !ok || !wipdwire.ExactMapKeys(rangeFields, "first_event_id", "last_event_id", "event_count") {
		return nil, ErrInvalidJournal
	}
	first, firstOK := rangeFields["first_event_id"].(string)
	last, lastOK := rangeFields["last_event_id"].(string)
	count, countOK := rangeFields["event_count"].(uint64)
	if !firstOK || !lastOK || !countOK || count == 0 || count > maxVerifiedEvents || first > last {
		return nil, ErrInvalidJournal
	}
	var firstPosition, lastPosition int64
	if err = tx.QueryRow(`SELECT first_position,last_position FROM installed_receipts WHERE command_id=? AND request_hash=?`, entry.Command.ID, entry.RequestHash).
		Scan(&firstPosition, &lastPosition); err != nil || firstPosition <= 0 || lastPosition < firstPosition ||
		lastPosition-firstPosition+1 != int64(count) || uint64(lastPosition) > installed.EventCount {
		return nil, ErrInvalidJournal
	}
	rows, err := tx.Query(`SELECT event_id,record FROM installed_events WHERE position BETWEEN ? AND ? ORDER BY position`, firstPosition, lastPosition)
	if err != nil {
		return nil, err
	}
	seen := uint64(0)
	for rows.Next() {
		var eventID string
		var record []byte
		if err = rows.Scan(&eventID, &record); err != nil {
			break
		}
		if !eventMatchesCommand(record, entry) || seen == 0 && eventID != first || seen+1 == count && eventID != last {
			err = ErrInvalidJournal
			break
		}
		seen++
	}
	if err == nil {
		err = rows.Err()
	}
	_ = rows.Close()
	if err != nil || seen != count {
		return nil, ErrInvalidJournal
	}
	return &wipdwire.JournalBarrierRange{First: first, Last: last, Count: count}, nil
}

func validBirthReleaseBarrier(barrier wipdwire.JournalBarrier) bool {
	return barrier.Schema == "wipd.journal-barrier/1" && identityPattern.MatchString(barrier.Journal) &&
		barrier.Claim.ID == barrier.Journal && barrier.Claim.Epoch == 1 && barrier.Count > 0 &&
		barrier.Count == barrier.Last && barrier.Count == barrier.Receipts && barrier.Sealed &&
		barrier.Unresolved == 0 && barrier.Quarantined == 0 && validDigest(barrier.Digest)
}

func encodeBirthBarrier(barrier wipdwire.JournalBarrier) []byte {
	encoded, _ := wipdwire.EncodeCanonical(barrier)
	return encoded
}

func birthReleaseActor(canonical []byte) string {
	fields, err := wipdwire.DecodeCanonicalMap(canonical,
		"schema", "command_id", "authority", "environment", "acted_at", "actor", "causation_command_id", "correlation_command_id", "operation", "context", "claim", "input", "blobs")
	if err != nil {
		return ""
	}
	actor, _ := fields["actor"].(string)
	return actor
}

func hasInstalledBirthRelease(db *sql.DB, matterID string) bool {
	rows, err := db.Query(`SELECT record FROM installed_events`)
	if err != nil {
		return true
	}
	defer func() { _ = rows.Close() }()
	for rows.Next() {
		var raw []byte
		if rows.Scan(&raw) != nil {
			return true
		}
		fields, decodeErr := wipdwire.DecodeCanonicalMap(raw,
			"schema", "event_id", "domain_id", "command_id", "request_hash", "environment", "acted_at", "occurred_at", "kind", "subject_id", "repo_id", "payload")
		if decodeErr != nil {
			return true
		}
		if fields["kind"] == "claim.released" && fields["subject_id"] == matterID {
			return true
		}
	}
	return rows.Err() != nil
}

func readBirthReleaseAttempt(row scanner) (BirthReleaseCommand, error) {
	var attempt BirthReleaseCommand
	var sequence int64
	var state string
	var code sql.NullString
	var receipt []byte
	if err := row.Scan(&attempt.ID, &sequence, &attempt.RequestHash, &attempt.CanonicalBytes, &state, &receipt, &code); err != nil {
		return attempt, err
	}
	if sequence <= 0 || !identityPattern.MatchString(attempt.ID) || !validDigest(attempt.RequestHash) {
		return BirthReleaseCommand{}, ErrInvalidJournal
	}
	fields, err := wipdwire.DecodeCanonicalMap(attempt.CanonicalBytes,
		"schema", "command_id", "authority", "environment", "acted_at", "actor", "causation_command_id", "correlation_command_id", "operation", "context", "claim", "input", "blobs")
	if err != nil {
		return BirthReleaseCommand{}, ErrInvalidJournal
	}
	input, ok := fields["input"].(map[string]any)
	barrierValue, barrierOK := input["barrier"]
	barrierBytes, encodeErr := wipdwire.EncodeCanonical(barrierValue)
	if !ok || !barrierOK || encodeErr != nil || wipdwire.DecodeCanonical(barrierBytes, &attempt.Barrier,
		"schema", "journal_id", "claim", "entry_count", "last_position", "terminal_receipt_count", "entries_digest", "sealed", "unresolved_count", "quarantined_count") != nil {
		return BirthReleaseCommand{}, ErrInvalidJournal
	}
	hashBytes := sha256.Sum256(append([]byte("wipd/request-hash/v1\x00"), attempt.CanonicalBytes...))
	if !bytes.Equal([]byte(attempt.RequestHash), []byte("sha256:"+hex.EncodeToString(hashBytes[:]))) || fields["command_id"] != attempt.ID {
		return BirthReleaseCommand{}, ErrInvalidJournal
	}
	attempt.EnvironmentSeq = uint64(sequence)
	attempt.Returned = state == "returned"
	attempt.Receipt = bytes.Clone(receipt)
	if code.Valid {
		attempt.ResultCode = operation.ResultCode(code.String)
	}
	if state != "attempt-prepared" && state != "returned" || !validBirthReleaseBarrier(attempt.Barrier) ||
		(attempt.Returned && (len(attempt.Receipt) == 0 || attempt.ResultCode == "")) || (!attempt.Returned && (len(attempt.Receipt) != 0 || code.Valid)) {
		return BirthReleaseCommand{}, ErrInvalidJournal
	}
	return attempt, nil
}

func checkBirthReleaseAttempts(db *sql.DB, identity Identity) ([]uint64, []string, error) {
	tx, err := db.BeginTx(context.Background(), &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return nil, nil, err
	}
	defer func() { _ = tx.Rollback() }()
	state, err := readInstallState(tx)
	if err != nil {
		return nil, nil, err
	}
	rows, err := tx.Query(`SELECT command_id,environment_sequence,request_hash,canonical_bytes,state,canonical_receipt,result_code FROM birth_release_attempts ORDER BY environment_sequence`)
	if err != nil {
		return nil, nil, err
	}
	var sequences []uint64
	var ids []string
	for rows.Next() {
		attempt, readErr := readBirthReleaseAttempt(rows)
		if readErr != nil || validateBirthReleaseCommand(identity, attempt) != nil {
			err = ErrInvalidJournal
			break
		}
		if attempt.Returned {
			result, eventID, output, receiptErr := validateBirthReleaseReceipt(attempt, identity, attempt.Receipt)
			if receiptErr != nil || validateInstalledBirthRelease(tx, state.anchor, identity, attempt, eventID, output, result) != nil {
				err = ErrInvalidJournal
				break
			}
		}
		sequences = append(sequences, attempt.EnvironmentSeq)
		ids = append(ids, attempt.ID)
	}
	if err == nil {
		err = rows.Err()
	}
	_ = rows.Close()
	if err != nil {
		return nil, nil, err
	}
	if err = tx.Commit(); err != nil {
		return nil, nil, err
	}
	return sequences, ids, nil
}

func validateBirthReleaseCommand(identity Identity, attempt BirthReleaseCommand) error {
	fields, err := wipdwire.DecodeCanonicalMap(attempt.CanonicalBytes,
		"schema", "command_id", "authority", "environment", "acted_at", "actor", "causation_command_id", "correlation_command_id", "operation", "context", "claim", "input", "blobs")
	if err != nil || fields["schema"] != "wipd.command/1" || fields["command_id"] != attempt.ID || fields["causation_command_id"] != nil ||
		fields["correlation_command_id"] != attempt.ID {
		return ErrInvalidJournal
	}
	actor, ok := fields["actor"].(string)
	if !ok || actor == "" {
		return ErrInvalidJournal
	}
	authority, ok := fields["authority"].(map[string]any)
	if !ok || !wipdwire.ExactMapKeys(authority, "domain_id", "expected_epoch") || authority["domain_id"] != identity.DomainID ||
		authority["expected_epoch"] != identity.AuthorityEpoch {
		return ErrInvalidJournal
	}
	environment, ok := fields["environment"].(map[string]any)
	if !ok || !wipdwire.ExactMapKeys(environment, "id", "sequence") || environment["id"] != identity.EnvironmentID ||
		environment["sequence"] != attempt.EnvironmentSeq {
		return ErrInvalidJournal
	}
	operationFields, ok := fields["operation"].(map[string]any)
	if !ok || !wipdwire.ExactMapKeys(operationFields, "name", "version") || operationFields["name"] != "claim.release" || operationFields["version"] != uint64(1) {
		return ErrInvalidJournal
	}
	contextFields, ok := fields["context"].(map[string]any)
	if !ok || !wipdwire.ExactMapKeys(contextFields, "repo_id", "clone_id", "worktree_id") || contextFields["repo_id"] != identity.RepoID ||
		contextFields["clone_id"] != nil || contextFields["worktree_id"] != nil {
		return ErrInvalidJournal
	}
	claim, ok := fields["claim"].(map[string]any)
	if !ok || !wipdwire.ExactMapKeys(claim, "id", "epoch") || claim["id"] != attempt.Barrier.Claim.ID || claim["epoch"] != attempt.Barrier.Claim.Epoch {
		return ErrInvalidJournal
	}
	input, ok := fields["input"].(map[string]any)
	if !ok || !wipdwire.ExactMapKeys(input, "barrier") {
		return ErrInvalidJournal
	}
	barrier, err := wipdwire.EncodeCanonical(input["barrier"])
	if err != nil || !bytes.Equal(barrier, encodeBirthBarrier(attempt.Barrier)) {
		return ErrInvalidJournal
	}
	blobs, ok := fields["blobs"].([]any)
	if !ok || len(blobs) != 0 {
		return ErrInvalidJournal
	}
	actedAt, ok := fields["acted_at"].(string)
	parsed, timeErr := time.Parse(time.RFC3339Nano, actedAt)
	if !ok || timeErr != nil || parsed.UTC().Format(time.RFC3339Nano) != actedAt || !bytes.HasSuffix([]byte(actedAt), []byte("Z")) {
		return ErrInvalidJournal
	}
	return nil
}

func validateInstalledBirthRelease(tx *sql.Tx, anchor wipdwire.PrefixAnchor, identity Identity, attempt BirthReleaseCommand, eventID string, output []byte, result operation.ResultCode) error {
	count, err := installedEventCountForCommand(tx, attempt.ID)
	if err != nil {
		return err
	}
	if result != operation.ResultSucceeded {
		if count != 0 {
			return ErrInvalidJournal
		}
		return nil
	}
	var position int64
	var raw []byte
	if err = tx.QueryRow(`SELECT position,record FROM installed_events WHERE event_id=?`, eventID).Scan(&position, &raw); err != nil ||
		position <= 0 || uint64(position) > anchor.EventCount || !validBirthReleaseEventRecord(raw, identity, attempt, eventID, output) {
		return fmt.Errorf("%w: installed birth-release event does not match its receipt", ErrInvalidJournal)
	}
	if count != 1 {
		return ErrInvalidJournal
	}
	return nil
}

func installedEventCountForCommand(tx interface {
	Query(string, ...any) (*sql.Rows, error)
}, commandID string,
) (int, error) {
	rows, err := tx.Query(`SELECT record FROM installed_events ORDER BY position`)
	if err != nil {
		return 0, err
	}
	defer func() { _ = rows.Close() }()
	count := 0
	for rows.Next() {
		var raw []byte
		if err = rows.Scan(&raw); err != nil {
			return 0, err
		}
		fields, decodeErr := wipdwire.DecodeCanonicalMap(raw,
			"schema", "event_id", "domain_id", "command_id", "request_hash", "environment", "acted_at", "occurred_at", "kind", "subject_id", "repo_id", "payload")
		if decodeErr != nil {
			return 0, ErrInvalidJournal
		}
		if fields["command_id"] == commandID {
			count++
		}
	}
	if err = rows.Err(); err != nil {
		return 0, err
	}
	return count, nil
}
