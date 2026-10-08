package authoritystore

import (
	"context"
	"database/sql"
	"fmt"
	"reflect"
	"sort"

	"github.com/procrastivity/wip/internal/operation"
)

type namedBatch struct {
	domain, id, name, birth string
}

type namedBatchQueryer interface {
	QueryContext(context.Context, string, ...any) (*sql.Rows, error)
	QueryRowContext(context.Context, string, ...any) *sql.Row
}

func deriveNamedBatches(ctx context.Context, queryer namedBatchQueryer) ([]namedBatch, error) {
	rows, err := queryer.QueryContext(ctx, `SELECT domain_id,position,event_id,command_id,record FROM authority_events ORDER BY domain_id,position`)
	if err != nil {
		return nil, err
	}
	type batchEvent struct {
		domain, eventID, commandID string
		position                   uint64
		event                      step12Event
	}
	var batchEvents []batchEvent
	for rows.Next() {
		var domain, eventID, commandID string
		var position uint64
		var raw []byte
		if err = rows.Scan(&domain, &position, &eventID, &commandID, &raw); err != nil {
			break
		}
		event, parseErr := parseStep12Event(raw, domain, position, eventID, commandID)
		if parseErr != nil {
			err = parseErr
			break
		}
		if event.kind != "batch.created" {
			continue
		}
		batchEvents = append(batchEvents, batchEvent{
			domain: domain, eventID: eventID, commandID: commandID, position: position, event: event,
		})
	}
	if err == nil {
		err = rows.Err()
	}
	_ = rows.Close()
	if err != nil {
		return nil, ErrInvalidStore
	}
	var batches []namedBatch
	byID, byName, eventCommands := make(map[string]bool), make(map[string]bool), make(map[string]bool)
	for _, batchEvent := range batchEvents {
		domain, eventID, commandID, position, event := batchEvent.domain, batchEvent.eventID, batchEvent.commandID, batchEvent.position, batchEvent.event
		var submission storedSubmission
		err = queryer.QueryRowContext(ctx, `SELECT domain_id,command_id,request_hash,command,epoch,environment_id,environment_sequence,operation_name,operation_version,state
			FROM submissions WHERE domain_id=? AND command_id=?`, domain, commandID).Scan(
			&submission.domain, &submission.id, &submission.hash, &submission.command, &submission.epoch,
			&submission.env, &submission.seq, &submission.operation, &submission.version, &submission.state)
		if err != nil {
			if err == sql.ErrNoRows && event.repo != "" {
				err = nil // retain legacy Repo-scoped batch.created history outside named-Batch projection
				continue
			}
			break
		}
		if submission.operation != operation.BatchCreateV1.Metadata().Operation.Name {
			if event.repo != "" {
				continue // legacy/other Batch history is not a named-Batch birth
			}
			err = ErrInvalidStore
			break
		}
		command, decodeErr := operation.DecodeCanonicalCommand(submission.command)
		if decodeErr != nil || submission.operation != operation.BatchCreateV1.Metadata().Operation.Name || submission.version != 1 ||
			command.Request.Operation != operation.BatchCreateV1.Metadata().Operation || operation.BatchCreateV1.ValidateRequest(command.Request) != nil ||
			command.ID != commandID || command.AuthorityDomainID != domain || command.EnvironmentID != submission.env ||
			command.EnvironmentSequence != submission.seq || command.ExpectedAuthorityEpoch != submission.epoch ||
			event.hash != submission.hash || event.repo != "" || event.environment != submission.env ||
			event.sequence != submission.seq || event.acted != command.ActedAt || !ulid.MatchString(event.subject) ||
			!exactKeys(event.payload, "name") {
			err = ErrInvalidStore
			break
		}
		input := command.Request.Input.(operation.BatchCreateInput)
		var name string
		if artifactDecoder.Unmarshal(event.payload["name"], &name) != nil || name != input.Name {
			err = ErrInvalidStore
			break
		}
		var member int
		if err = queryer.QueryRowContext(ctx, `SELECT count(*) FROM repo_memberships WHERE domain_id=? AND repo_id=?`, domain, command.Request.Context.Repo).Scan(&member); err != nil || member != 1 {
			err = ErrInvalidStore
			break
		}
		var receiptBytes []byte
		var first, last sql.NullInt64
		if err = queryer.QueryRowContext(ctx, `SELECT receipt,first_position,last_position FROM terminal_receipts WHERE domain_id=? AND command_id=?`, domain, commandID).Scan(&receiptBytes, &first, &last); err != nil {
			break
		}
		receipt, receiptErr := readReceipt(receiptBytes)
		wantOutput, encodeErr := artifactEncoder.Marshal(map[string]any{"id": event.subject, "name": name})
		if receiptErr != nil || encodeErr != nil || receipt.Result.Code != string(operation.ResultSucceeded) ||
			receipt.Domain != domain || receipt.ID != commandID || receipt.Hash != submission.hash ||
			receipt.Operation.Name != operation.BatchCreateV1.Metadata().Operation.Name || receipt.Operation.Version != 1 ||
			receipt.Environment.ID != submission.env || receipt.Environment.Sequence != submission.seq ||
			receipt.Result.Problem != nil || !reflect.DeepEqual(receipt.Result.Output, wantOutput) || receipt.Range == nil ||
			receipt.Range.Count != 1 || receipt.Range.First != eventID || receipt.Range.Last != eventID ||
			!first.Valid || !last.Valid || first.Int64 != int64(position) || last.Int64 != int64(position) {
			err = ErrInvalidStore
			break
		}
		idKey, nameKey := ownerKey(domain, event.subject), ownerKey(domain, name)
		if byID[idKey] || byName[nameKey] {
			err = ErrInvalidStore
			break
		}
		commandKey := ownerKey(domain, commandID)
		if eventCommands[commandKey] {
			err = ErrInvalidStore
			break
		}
		byID[idKey], byName[nameKey] = true, true
		eventCommands[commandKey] = true
		batches = append(batches, namedBatch{domain: domain, id: event.subject, name: name, birth: eventID})
	}
	if err != nil {
		return nil, ErrInvalidStore
	}
	if err = checkNamedBatchSubmissions(ctx, queryer, eventCommands); err != nil {
		return nil, err
	}
	sort.Slice(batches, func(i, j int) bool {
		return ownerKey(batches[i].domain, batches[i].id) < ownerKey(batches[j].domain, batches[j].id)
	})
	return batches, nil
}

func checkNamedBatchSubmissions(ctx context.Context, queryer namedBatchQueryer, eventCommands map[string]bool) error {
	rows, err := queryer.QueryContext(ctx, `SELECT domain_id,command_id,request_hash,command,epoch,environment_id,environment_sequence,operation_name,operation_version,state
		FROM submissions WHERE operation_name='batch.create' ORDER BY domain_id,environment_id,environment_sequence`)
	if err != nil {
		return err
	}
	var submissions []storedSubmission
	for rows.Next() {
		var submission storedSubmission
		if err = rows.Scan(&submission.domain, &submission.id, &submission.hash, &submission.command, &submission.epoch,
			&submission.env, &submission.seq, &submission.operation, &submission.version, &submission.state); err != nil {
			break
		}
		submissions = append(submissions, submission)
	}
	if err == nil {
		err = rows.Err()
	}
	if closeErr := rows.Close(); err == nil {
		err = closeErr
	}
	if err != nil {
		return err
	}
	for _, submission := range submissions {
		command, decodeErr := operation.DecodeCanonicalCommand(submission.command)
		if decodeErr != nil || submission.version != 1 || command.Request.Operation != operation.BatchCreateV1.Metadata().Operation ||
			operation.BatchCreateV1.ValidateRequest(command.Request) != nil || command.ID != submission.id ||
			command.AuthorityDomainID != submission.domain || command.EnvironmentID != submission.env ||
			command.EnvironmentSequence != submission.seq || command.ExpectedAuthorityEpoch != submission.epoch {
			return ErrInvalidStore
		}
		var raw []byte
		err = queryer.QueryRowContext(ctx, `SELECT receipt FROM terminal_receipts WHERE domain_id=? AND command_id=?`, submission.domain, submission.id).Scan(&raw)
		if err == sql.ErrNoRows {
			if submission.state != "submitted" || eventCommands[ownerKey(submission.domain, submission.id)] {
				return ErrInvalidStore
			}
			continue
		}
		if err != nil {
			return ErrInvalidStore
		}
		receipt, receiptErr := readReceipt(raw)
		if receiptErr != nil || receipt.Domain != submission.domain || receipt.ID != submission.id || receipt.Hash != submission.hash ||
			receipt.Operation.Name != "batch.create" || receipt.Operation.Version != 1 || receipt.Result.Code == "" {
			return ErrInvalidStore
		}
		if receipt.Result.Code == string(operation.ResultSucceeded) {
			if !eventCommands[ownerKey(submission.domain, submission.id)] || submission.state != "terminal" {
				return ErrInvalidStore
			}
		} else if eventCommands[ownerKey(submission.domain, submission.id)] || receipt.Result.Output != nil || receipt.Result.Problem == nil || receipt.Range != nil {
			return ErrInvalidStore
		}
	}
	return nil
}

func checkNamedBatchState(db *sql.DB) error {
	want, err := deriveNamedBatches(context.Background(), db)
	if err != nil {
		return fmt.Errorf("%w: named Batch event fold: %v", ErrInvalidStore, err)
	}
	rows, err := db.Query(`SELECT domain_id,batch_id,name,birth_event_id FROM m6_named_batches ORDER BY domain_id,batch_id`)
	if err != nil {
		return err
	}
	defer func() { _ = rows.Close() }()
	var got []namedBatch
	for rows.Next() {
		var batch namedBatch
		if err = rows.Scan(&batch.domain, &batch.id, &batch.name, &batch.birth); err != nil {
			return err
		}
		got = append(got, batch)
	}
	if err = rows.Err(); err != nil {
		return err
	}
	if !reflect.DeepEqual(got, want) {
		return fmt.Errorf("%w: named Batch projection differs from event history", ErrInvalidStore)
	}
	return nil
}

func rebuildNamedBatchesTx(ctx context.Context, tx *sql.Tx) error {
	batches, err := deriveNamedBatches(ctx, tx)
	if err != nil {
		return err
	}
	for _, batch := range batches {
		if _, err = tx.ExecContext(ctx, `INSERT INTO m6_named_batches(domain_id,batch_id,name,birth_event_id) VALUES(?,?,?,?)`,
			batch.domain, batch.id, batch.name, batch.birth); err != nil {
			return writeError(err)
		}
	}
	return nil
}
