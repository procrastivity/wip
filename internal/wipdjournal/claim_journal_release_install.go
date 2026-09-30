package wipdjournal

import (
	"bytes"
	"context"
	"database/sql"
	"fmt"

	"github.com/procrastivity/wip/internal/operation"
	"github.com/procrastivity/wip/internal/wipdwire"
)

// InstallClaimJournalRelease atomically retains the terminal release receipt,
// verified authority tail, rebuilt overlay, and released local claim pin.
func (j *Journal) InstallClaimJournalRelease(ctx context.Context, expected InstallExpectation, attempt ClaimJournalReleaseCommand,
	receipt []byte, transfer VerifiedTransfer,
) (InstallSnapshot, error) {
	return j.installTransaction(ctx, expected, func(tx *sql.Tx, state storedInstallState) error {
		persisted, err := readClaimJournalReleaseAttempt(tx.QueryRow(`SELECT command_id,environment_sequence,request_hash,canonical_bytes,claim_id,claim_epoch,matter_id,dispatch_id,journal_id,generation,barrier,state,canonical_receipt,result_code
			FROM claim_journal_release_attempts WHERE command_id=?`, attempt.ID))
		if err != nil || !sameClaimJournalReleaseAttempt(persisted, attempt) || !transfer.Valid() ||
			transfer.domainID != j.identity.DomainID || transfer.epoch != j.identity.AuthorityEpoch ||
			!sameTransferAnchor(transfer.start, state.anchor) {
			return fmt.Errorf("%w: claim journal release identity or transfer anchor mismatch", ErrInvalidTransfer)
		}
		if persisted.Returned {
			if !bytes.Equal(persisted.Receipt, receipt) {
				return ErrInvalidTransfer
			}
			return nil
		}
		result, eventIDs, output, err := validateClaimJournalReleaseReceipt(persisted, j.identity, receipt)
		if err != nil {
			return fmt.Errorf("validate acquired claim release receipt: %w", err)
		}
		if err = validateClaimJournalReleaseEvents(tx, transfer, j.identity, persisted, eventIDs, output, result); err != nil {
			return fmt.Errorf("validate acquired claim release events: %w", err)
		}
		if err = appendVerifiedEvents(ctx, tx, state.anchor.EventCount, transfer.records); err != nil {
			return err
		}
		manifest, err := encodeManifest(transfer.manifest)
		if err != nil {
			return err
		}
		if _, err = tx.ExecContext(ctx, `UPDATE claim_journal_release_attempts SET state='returned',canonical_receipt=?,result_code=? WHERE command_id=? AND state='attempt-prepared'`,
			receipt, string(result), attempt.ID); err != nil {
			return err
		}
		if result == operation.ResultSucceeded {
			if _, err = tx.ExecContext(ctx, `UPDATE installed_claim_journals SET state='released' WHERE claim_id=? AND journal_id=? AND generation=? AND state='sealed'`,
				persisted.Binding.ClaimID, persisted.Binding.JournalID, persisted.Binding.Generation); err != nil {
				return err
			}
		}
		if err = rebuildOverlay(ctx, tx); err != nil {
			return err
		}
		return updateInstallState(ctx, tx, state, transfer.end, transfer.manifest.Digest, manifest)
	})
}

func sameClaimJournalReleaseAttempt(left, right ClaimJournalReleaseCommand) bool {
	return left.ID == right.ID && left.RequestHash == right.RequestHash && left.EnvironmentSeq == right.EnvironmentSeq &&
		bytes.Equal(left.CanonicalBytes, right.CanonicalBytes) && left.Binding.ClaimID == right.Binding.ClaimID &&
		left.Binding.ClaimEpoch == right.Binding.ClaimEpoch && left.Binding.MatterID == right.Binding.MatterID &&
		left.Binding.DispatchID == right.Binding.DispatchID && left.Binding.JournalID == right.Binding.JournalID &&
		left.Binding.Generation == right.Binding.Generation && bytes.Equal(encodeClaimJournalBarrier(left.Barrier), encodeClaimJournalBarrier(right.Barrier))
}

func validateClaimJournalReleaseReceipt(attempt ClaimJournalReleaseCommand, identity Identity, raw []byte) (operation.ResultCode, []string, []byte, error) {
	var empty operation.ResultCode
	fields, err := wipdwire.DecodeCanonicalMap(raw,
		"schema", "domain_id", "authority_epoch", "identity_schema", "command_id", "request_hash", "operation", "environment", "result", "accepted_events")
	if err != nil || fields["schema"] != "wipd.terminal-receipt/1" || fields["domain_id"] != identity.DomainID ||
		fields["authority_epoch"] != identity.AuthorityEpoch || fields["identity_schema"] != "wipd.command/1" ||
		fields["command_id"] != attempt.ID || fields["request_hash"] != attempt.RequestHash {
		return empty, nil, nil, ErrInvalidTransfer
	}
	operationFields, ok := fields["operation"].(map[string]any)
	if !ok || !wipdwire.ExactMapKeys(operationFields, "name", "version") || operationFields["name"] != "claim.release" || operationFields["version"] != uint64(1) {
		return empty, nil, nil, ErrInvalidTransfer
	}
	environment, ok := fields["environment"].(map[string]any)
	if !ok || !wipdwire.ExactMapKeys(environment, "id", "sequence") || environment["id"] != identity.EnvironmentID || environment["sequence"] != attempt.EnvironmentSeq {
		return empty, nil, nil, ErrInvalidTransfer
	}
	resultFields, ok := fields["result"].(map[string]any)
	if !ok || !wipdwire.ExactMapKeys(resultFields, "code", "output", "problem_code") {
		return empty, nil, nil, ErrInvalidTransfer
	}
	codeText, ok := resultFields["code"].(string)
	code := operation.ResultCode(codeText)
	if !ok || code != operation.ResultSucceeded && code != operation.ResultRejected && code != operation.ResultRefused && code != operation.ResultFailed {
		return empty, nil, nil, ErrInvalidTransfer
	}
	if code != operation.ResultSucceeded {
		problem, problemOK := resultFields["problem_code"].(string)
		if fields["accepted_events"] != nil || resultFields["output"] != nil || !problemOK || problem == "" {
			return empty, nil, nil, ErrInvalidTransfer
		}
		return code, nil, nil, nil
	}
	if resultFields["problem_code"] != nil {
		return empty, nil, nil, ErrInvalidTransfer
	}
	output, ok := resultFields["output"].([]byte)
	if !ok {
		return empty, nil, nil, ErrInvalidTransfer
	}
	wantOutput, err := wipdwire.EncodeCanonical(map[string]any{
		"claim_id": attempt.Binding.ClaimID, "claim_epoch": attempt.Binding.ClaimEpoch,
		"dispatch_id": attempt.Binding.DispatchID, "barrier_digest": attempt.Barrier.Digest,
	})
	if err != nil || !bytes.Equal(output, wantOutput) {
		return empty, nil, nil, ErrInvalidTransfer
	}
	rangeFields, ok := fields["accepted_events"].(map[string]any)
	if !ok || !wipdwire.ExactMapKeys(rangeFields, "first_event_id", "last_event_id", "event_count") || rangeFields["event_count"] != uint64(2) {
		return empty, nil, nil, ErrInvalidTransfer
	}
	first, firstOK := rangeFields["first_event_id"].(string)
	last, lastOK := rangeFields["last_event_id"].(string)
	if !firstOK || !lastOK || !identityPattern.MatchString(first) || !identityPattern.MatchString(last) || first >= last {
		return empty, nil, nil, ErrInvalidTransfer
	}
	return code, []string{first, last}, bytes.Clone(output), nil
}

func validateClaimJournalReleaseEvents(tx *sql.Tx, transfer VerifiedTransfer, identity Identity,
	attempt ClaimJournalReleaseCommand, eventIDs []string, output []byte, result operation.ResultCode,
) error {
	var matching []wipdwire.EventRecord
	var matchingPositions []int
	for index, record := range transfer.records {
		fields, err := wipdwire.DecodeCanonicalMap(record.Record,
			"schema", "event_id", "domain_id", "command_id", "request_hash", "environment", "acted_at", "occurred_at", "kind", "subject_id", "repo_id", "payload")
		if err != nil {
			return ErrInvalidTransfer
		}
		if fields["command_id"] == attempt.ID {
			matching = append(matching, record)
			matchingPositions = append(matchingPositions, index)
		}
	}
	installedCount, err := installedEventCountForCommand(tx, attempt.ID)
	if err != nil {
		return err
	}
	if result != operation.ResultSucceeded {
		if len(eventIDs) != 0 || len(matching) != 0 || installedCount != 0 {
			return ErrInvalidTransfer
		}
		return nil
	}
	if len(eventIDs) != 2 || len(matching) != 2 || installedCount != 0 ||
		matching[0].EventID != eventIDs[0] || matching[1].EventID != eventIDs[1] || matchingPositions[1] != matchingPositions[0]+1 {
		return ErrInvalidTransfer
	}
	closePayload, err := wipdwire.EncodeCanonical(map[string]any{
		"dispatch_id": attempt.Binding.DispatchID, "claim_id": attempt.Binding.ClaimID, "claim_epoch": attempt.Binding.ClaimEpoch,
	})
	if err != nil || !validClaimJournalReleaseEventRecord(matching[0].Record, identity, attempt, eventIDs[0],
		"dispatch.closed", attempt.Binding.DispatchID, closePayload) ||
		!validClaimJournalReleaseEventRecord(matching[1].Record, identity, attempt, eventIDs[1],
			"claim.released", attempt.Binding.ClaimID, output) {
		return ErrInvalidTransfer
	}
	return nil
}

func validClaimJournalReleaseEventRecord(raw []byte, identity Identity, attempt ClaimJournalReleaseCommand,
	eventID, kind, subject string, payload []byte,
) bool {
	fields, err := wipdwire.DecodeCanonicalMap(raw,
		"schema", "event_id", "domain_id", "command_id", "request_hash", "environment", "acted_at", "occurred_at", "kind", "subject_id", "repo_id", "payload")
	if err != nil || fields["schema"] != "wipd.event/1" || fields["event_id"] != eventID || fields["domain_id"] != identity.DomainID ||
		fields["command_id"] != attempt.ID || fields["request_hash"] != attempt.RequestHash || fields["kind"] != kind ||
		fields["subject_id"] != subject || fields["repo_id"] != identity.RepoID {
		return false
	}
	environment, ok := fields["environment"].(map[string]any)
	actualPayload, payloadErr := wipdwire.EncodeCanonical(fields["payload"])
	return ok && environment["id"] == identity.EnvironmentID && environment["sequence"] == attempt.EnvironmentSeq &&
		payloadErr == nil && bytes.Equal(actualPayload, payload)
}

func validateInstalledClaimJournalRelease(db *sql.DB, identity Identity, attempt ClaimJournalReleaseCommand,
	eventIDs []string, output []byte, result operation.ResultCode,
) error {
	count, err := installedEventCountForCommand(db, attempt.ID)
	if err != nil {
		return err
	}
	if result != operation.ResultSucceeded {
		if count != 0 {
			return ErrInvalidJournal
		}
		return nil
	}
	if count != 2 || len(eventIDs) != 2 {
		return ErrInvalidJournal
	}
	positions := make([]int64, 2)
	closePayload, err := wipdwire.EncodeCanonical(map[string]any{
		"dispatch_id": attempt.Binding.DispatchID, "claim_id": attempt.Binding.ClaimID, "claim_epoch": attempt.Binding.ClaimEpoch,
	})
	if err != nil {
		return ErrInvalidJournal
	}
	wantPayloads := [][]byte{closePayload, output}
	wantKinds := []string{"dispatch.closed", "claim.released"}
	wantSubjects := []string{attempt.Binding.DispatchID, attempt.Binding.ClaimID}
	for index, eventID := range eventIDs {
		var raw []byte
		if err = db.QueryRow(`SELECT position,record FROM installed_events WHERE event_id=?`, eventID).Scan(&positions[index], &raw); err != nil ||
			positions[index] <= 0 || !validClaimJournalReleaseEventRecord(raw, identity, attempt, eventID,
			wantKinds[index], wantSubjects[index], wantPayloads[index]) {
			return ErrInvalidJournal
		}
	}
	var anchor wipdwire.PrefixAnchor
	var highWater sql.NullString
	if err = db.QueryRow(`SELECT event_count,high_water_event_id,prefix_digest FROM environment_install WHERE singleton=1`).Scan(&anchor.EventCount, &highWater, &anchor.Digest); err != nil ||
		positions[0] >= positions[1] || positions[1]-positions[0] != 1 || uint64(positions[1]) > anchor.EventCount {
		return ErrInvalidJournal
	}
	if highWater.Valid {
		anchor.EventID = &highWater.String
	}
	return nil
}
