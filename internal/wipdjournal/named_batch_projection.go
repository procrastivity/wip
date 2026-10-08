package wipdjournal

import (
	"database/sql"
	"sort"
	"strings"

	"github.com/procrastivity/wip/internal/operation"
	"github.com/procrastivity/wip/internal/wipdwire"
)

// NamedBatchProjection is the read-only local fold of installed named-Batch
// birth events. Authority events remain the durable source of truth.
type NamedBatchProjection struct {
	DomainID     string `json:"domain_id"`
	BatchID      string `json:"batch_id"`
	Name         string `json:"name"`
	BirthEventID string `json:"birth_event_id"`
}

func installedNamedBatchProjection(tx *sql.Tx, domain string) ([]NamedBatchProjection, error) {
	rows, err := tx.Query(`SELECT event_id,record FROM installed_events ORDER BY position`)
	if err != nil {
		return nil, err
	}
	projection := make([]NamedBatchProjection, 0)
	ids, names := make(map[string]bool), make(map[string]bool)
	for rows.Next() {
		var eventID string
		var record []byte
		if err = rows.Scan(&eventID, &record); err != nil {
			break
		}
		fields, decodeErr := wipdwire.DecodeCanonicalMap(record,
			"schema", "event_id", "domain_id", "command_id", "request_hash", "environment", "acted_at", "occurred_at", "kind", "subject_id", "repo_id", "payload")
		if decodeErr != nil {
			err = ErrInvalidJournal
			break
		}
		if fields["kind"] != "batch.created" {
			continue
		}
		if !validAuthorityEvent(record, domain, eventID) || fields["repo_id"] != nil {
			err = ErrInvalidJournal
			break
		}
		payload, ok := fields["payload"].(map[string]any)
		batchID, idOK := fields["subject_id"].(string)
		name, nameOK := payload["name"].(string)
		if !ok || !idOK || !nameOK || name == "" || name != strings.TrimSpace(name) {
			err = ErrInvalidJournal
			break
		}
		if ids[batchID] || names[name] {
			err = ErrInvalidJournal
			break
		}
		ids[batchID], names[name] = true, true
		var requestHash string
		var sequence, position, count int64
		var code string
		var receipt []byte
		var firstID, lastID sql.NullString
		eventPosition, ok := fields["event_id"].(string)
		if !ok {
			err = ErrInvalidJournal
			break
		}
		queryErr := tx.QueryRow(`SELECT request_hash,environment_sequence,journal_position,result_code,canonical_receipt,first_event_id,last_event_id,event_count
			FROM installed_receipts WHERE command_id=?`, asString(fields["command_id"])).Scan(
			&requestHash, &sequence, &position, &code, &receipt, &firstID, &lastID, &count)
		if queryErr == nil {
			if fields["request_hash"] != requestHash || sequence <= 0 || position <= 0 || code != string(operation.ResultSucceeded) ||
				count != 1 || !firstID.Valid || !lastID.Valid || firstID.String != eventID || lastID.String != eventID ||
				!namedBatchReceiptMatches(receipt, domain, asString(fields["command_id"]), requestHash, sequence, name, batchID, eventPosition) {
				err = ErrInvalidJournal
				break
			}
		} else if queryErr != sql.ErrNoRows {
			err = queryErr
			break
		}
		projection = append(projection, NamedBatchProjection{DomainID: domain, BatchID: batchID, Name: name, BirthEventID: eventID})
	}
	if err == nil {
		err = rows.Err()
	}
	_ = rows.Close()
	if err != nil {
		return nil, err
	}
	sort.Slice(projection, func(i, j int) bool { return projection[i].BatchID < projection[j].BatchID })
	return projection, nil
}

func namedBatchReceiptMatches(raw []byte, domain, commandID, hash string, sequence int64, name, batchID, eventID string) bool {
	fields, err := wipdwire.DecodeCanonicalMap(raw,
		"schema", "domain_id", "authority_epoch", "identity_schema", "command_id", "request_hash", "operation", "environment", "result", "accepted_events")
	if err != nil || fields["schema"] != "wipd.terminal-receipt/1" || fields["domain_id"] != domain || fields["identity_schema"] != "wipd.command/1" ||
		fields["command_id"] != commandID || fields["request_hash"] != hash {
		return false
	}
	operationFields, ok := fields["operation"].(map[string]any)
	if !ok || !wipdwire.ExactMapKeys(operationFields, "name", "version") || operationFields["name"] != "batch.create" || operationFields["version"] != uint64(1) {
		return false
	}
	environment, ok := fields["environment"].(map[string]any)
	if !ok || !wipdwire.ExactMapKeys(environment, "id", "sequence") || environment["sequence"] != uint64(sequence) {
		return false
	}
	result, ok := fields["result"].(map[string]any)
	if !ok || !wipdwire.ExactMapKeys(result, "code", "output", "problem_code") || result["code"] != string(operation.ResultSucceeded) || result["problem_code"] != nil {
		return false
	}
	output, ok := result["output"].([]byte)
	if !ok {
		return false
	}
	outputFields, err := wipdwire.DecodeCanonicalMap(output, "id", "name")
	if err != nil || outputFields["id"] != batchID || outputFields["name"] != name {
		return false
	}
	accepted, ok := fields["accepted_events"].(map[string]any)
	return ok && wipdwire.ExactMapKeys(accepted, "first_event_id", "last_event_id", "event_count") &&
		accepted["first_event_id"] == eventID && accepted["last_event_id"] == eventID && accepted["event_count"] == uint64(1)
}
