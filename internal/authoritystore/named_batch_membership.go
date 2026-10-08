package authoritystore

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"reflect"
	"sort"
	"time"

	"github.com/procrastivity/wip/internal/operation"
)

type namedBatchMembership struct {
	domain, batch, matter, joined, left string
}

type namedBatchMembershipFold struct {
	result    operation.Result
	output    []byte
	position  uint64
	eventID   string
	eventKind string
	payload   map[string]any
}

func namedBatchMembershipDefinition(id operation.ID) (operation.Definition, bool) {
	switch id {
	case operation.BatchJoinV1.Metadata().Operation:
		return operation.BatchJoinV1, true
	case operation.BatchLeaveV1.Metadata().Operation:
		return operation.BatchLeaveV1, true
	case operation.BatchDismissV1.Metadata().Operation:
		return operation.BatchDismissV1, true
	default:
		return operation.Definition{}, false
	}
}

func isNamedBatchMembershipOperation(id operation.ID) bool {
	_, ok := namedBatchMembershipDefinition(id)
	return ok
}

func completeNamedBatchMembershipTx(ctx context.Context, tx *sql.Tx, command operation.Command, result operation.Result,
	subjectID, eventID string, occurred time.Time, identity eventIdentity,
) (namedBatchMembershipFold, error) {
	var fold namedBatchMembershipFold
	input, batchID, matterID, wantOutput := namedBatchCommandInput(command)
	if input == nil || subjectID != batchID {
		return fold, ErrInvalidProof
	}
	if result.Code != operation.ResultSucceeded || result.Problem != nil {
		return fold, ErrInvalidProof
	}
	switch command.Request.Operation {
	case operation.BatchJoinV1.Metadata().Operation, operation.BatchLeaveV1.Metadata().Operation:
		got, ok := result.Output.(operation.BatchMembershipOutput)
		if !ok || !reflect.DeepEqual(got, wantOutput.(operation.BatchMembershipOutput)) {
			return fold, ErrInvalidProof
		}
	case operation.BatchDismissV1.Metadata().Operation:
		got, ok := result.Output.(operation.BatchDismissOutput)
		if !ok || !reflect.DeepEqual(got, wantOutput.(operation.BatchDismissOutput)) {
			return fold, ErrInvalidProof
		}
	default:
		return fold, ErrInvalidProof
	}
	refuse := func(code operation.ProblemCode) namedBatchMembershipFold {
		return namedBatchMembershipFold{result: operation.Result{
			Code:    operation.ResultRefused,
			Problem: &operation.Problem{Code: code, Message: string(code)},
		}}
	}
	var dismissed sql.NullString
	err := tx.QueryRowContext(ctx, `SELECT dismissed_event_id FROM m6_named_batches WHERE domain_id=? AND batch_id=?`,
		command.AuthorityDomainID, batchID).Scan(&dismissed)
	if errors.Is(err, sql.ErrNoRows) {
		var crossDomain int
		if err = tx.QueryRowContext(ctx, `SELECT
			(SELECT count(*) FROM m6_named_batches WHERE batch_id=? AND domain_id!=?) +
			(SELECT count(*) FROM anonymous_batches WHERE batch_id=? AND domain_id!=?)`, batchID, command.AuthorityDomainID,
			batchID, command.AuthorityDomainID).Scan(&crossDomain); err != nil {
			return fold, err
		}
		if crossDomain != 0 {
			fold = refuse(operation.ProblemBatchCrossDomain)
		} else {
			fold = refuse(operation.ProblemBatchTargetMissing)
		}
		return fold, nil
	}
	if err != nil {
		return fold, err
	}
	if dismissed.Valid {
		return refuse(operation.ProblemBatchDismissed), nil
	}
	if matterID != "" {
		var exists int
		err = tx.QueryRowContext(ctx, `SELECT count(*) FROM matters WHERE domain_id=? AND matter_id=?`,
			command.AuthorityDomainID, matterID).Scan(&exists)
		if err != nil {
			return fold, err
		}
		if exists == 0 {
			var crossDomain int
			if err = tx.QueryRowContext(ctx, `SELECT count(*) FROM matters WHERE matter_id=? AND domain_id!=?`,
				matterID, command.AuthorityDomainID).Scan(&crossDomain); err != nil {
				return fold, err
			}
			if crossDomain != 0 {
				return refuse(operation.ProblemBatchCrossDomain), nil
			}
			return refuse(operation.ProblemBatchTargetMissing), nil
		}
	}
	fold.result = result
	fold.output, err = namedBatchReceiptOutput(wantOutput)
	if err != nil {
		return fold, err
	}
	fold.eventID = eventID
	fold.payload = map[string]any{}
	switch command.Request.Operation {
	case operation.BatchJoinV1.Metadata().Operation:
		var count int
		if err = tx.QueryRowContext(ctx, `SELECT count(*) FROM m6_named_batch_memberships
			WHERE domain_id=? AND batch_id=? AND matter_id=? AND left_event_id IS NULL`,
			command.AuthorityDomainID, batchID, matterID).Scan(&count); err != nil {
			return fold, err
		}
		if count != 0 {
			fold.eventID = ""
			return fold, nil
		}
		fold.eventKind = "batch.joined"
		fold.payload = map[string]any{"matter_id": matterID}
	case operation.BatchLeaveV1.Metadata().Operation:
		var count int
		if err = tx.QueryRowContext(ctx, `SELECT count(*) FROM m6_named_batch_memberships
			WHERE domain_id=? AND batch_id=? AND matter_id=? AND left_event_id IS NULL`,
			command.AuthorityDomainID, batchID, matterID).Scan(&count); err != nil {
			return fold, err
		}
		if count == 0 {
			return refuse(operation.ProblemBatchMembershipMissing), nil
		}
		fold.eventKind = "batch.left"
		fold.payload = map[string]any{"matter_id": matterID}
	case operation.BatchDismissV1.Metadata().Operation:
		fold.eventKind = "batch.dismissed"
	}
	fold.position, err = appendCommandEventWithRepo(ctx, tx, identity, nil, occurred, eventID, fold.eventKind, batchID, fold.payload)
	if err != nil {
		return fold, err
	}
	switch command.Request.Operation {
	case operation.BatchJoinV1.Metadata().Operation:
		_, err = tx.ExecContext(ctx, `INSERT INTO m6_named_batch_memberships(domain_id,batch_id,matter_id,joined_event_id)
			VALUES(?,?,?,?)`, command.AuthorityDomainID, batchID, matterID, eventID)
	case operation.BatchLeaveV1.Metadata().Operation:
		var updated sql.Result
		updated, err = tx.ExecContext(ctx, `UPDATE m6_named_batch_memberships SET left_event_id=?
			WHERE domain_id=? AND batch_id=? AND matter_id=? AND left_event_id IS NULL`,
			eventID, command.AuthorityDomainID, batchID, matterID)
		if err == nil {
			var rows int64
			rows, err = updated.RowsAffected()
			if err == nil && rows != 1 {
				err = ErrInvalidStore
			}
		}
	case operation.BatchDismissV1.Metadata().Operation:
		var updated sql.Result
		updated, err = tx.ExecContext(ctx, `UPDATE m6_named_batches SET dismissed_event_id=?
			WHERE domain_id=? AND batch_id=? AND dismissed_event_id IS NULL`, eventID, command.AuthorityDomainID, batchID)
		if err == nil {
			var rows int64
			rows, err = updated.RowsAffected()
			if err == nil && rows != 1 {
				err = ErrInvalidStore
			}
		}
	}
	if err != nil {
		return fold, writeError(err)
	}
	return fold, nil
}

func namedBatchCommandInput(command operation.Command) (any, string, string, any) {
	switch input := command.Request.Input.(type) {
	case operation.BatchMembershipInput:
		return input, input.BatchID, input.MatterID, operation.BatchMembershipOutput(input)
	case operation.BatchDismissInput:
		return input, input.BatchID, "", operation.BatchDismissOutput(input)
	default:
		return nil, "", "", nil
	}
}

func namedBatchReceiptOutput(output any) ([]byte, error) {
	var fields map[string]any
	switch output := output.(type) {
	case operation.BatchMembershipOutput:
		fields = map[string]any{"batch_id": output.BatchID, "matter_id": output.MatterID}
	case operation.BatchDismissOutput:
		fields = map[string]any{"batch_id": output.BatchID}
	default:
		return nil, ErrInvalidProof
	}
	return artifactEncoder.Marshal(fields)
}

type namedBatchHistoryEntry struct {
	domain, batch, matter, kind string
	epoch, generation, sequence uint64
	position                    uint64
	event                       bool
}

func checkNamedBatchMembershipState(db *sql.DB) error {
	ctx := context.Background()
	batches, err := deriveNamedBatches(ctx, db)
	if err != nil {
		return fmt.Errorf("%w: named Batch membership birth fold: %v", ErrInvalidStore, err)
	}
	known := make(map[string]bool, len(batches))
	for _, batch := range batches {
		known[ownerKey(batch.domain, batch.id)] = true
	}
	rows, err := db.Query(`SELECT domain_id,position,event_id,command_id,record FROM authority_events ORDER BY domain_id,position`)
	if err != nil {
		return err
	}
	var eventLog []step12Event
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
		eventLog = append(eventLog, event)
	}
	if err == nil {
		err = rows.Err()
	}
	_ = rows.Close()
	if err != nil {
		return fmt.Errorf("%w: named Batch membership event log: %v", ErrInvalidStore, err)
	}
	dismissed := make(map[string]string)
	born := make(map[string]bool)
	active := make(map[string]string)
	episodes := make(map[string]namedBatchMembership)
	commands := make(map[string]bool)
	for _, event := range eventLog {
		domain, eventID, commandID, position := event.domain, event.id, event.command, event.position
		if event.kind == "batch.created" {
			born[ownerKey(domain, event.subject)] = true
			continue
		}
		if event.kind != "batch.joined" && event.kind != "batch.left" && event.kind != "batch.dismissed" {
			continue
		}
		key := ownerKey(domain, commandID)
		batchKey := ownerKey(domain, event.subject)
		if commands[key] || event.repo != "" || !known[batchKey] || !born[batchKey] {
			err = ErrInvalidStore
			break
		}
		var submission storedSubmission
		if err = db.QueryRow(`SELECT domain_id,command_id,request_hash,command,epoch,environment_id,environment_sequence,operation_name,operation_version,state
			FROM submissions WHERE domain_id=? AND command_id=?`, domain, commandID).Scan(
			&submission.domain, &submission.id, &submission.hash, &submission.command, &submission.epoch,
			&submission.env, &submission.seq, &submission.operation, &submission.version, &submission.state); err != nil {
			break
		}
		command, decodeErr := operation.DecodeCanonicalCommand(submission.command)
		definition, defined := namedBatchMembershipDefinition(command.Request.Operation)
		if decodeErr != nil || !defined || definition.ValidateRequest(command.Request) != nil || submission.version != 1 ||
			command.ID != commandID || command.AuthorityDomainID != domain || command.EnvironmentID != submission.env ||
			command.EnvironmentSequence != submission.seq || command.ExpectedAuthorityEpoch != submission.epoch ||
			event.hash != submission.hash || event.environment != submission.env || event.sequence != submission.seq || event.acted != command.ActedAt {
			err = ErrInvalidStore
			break
		}
		input, batchID, matterID, wantOutput := namedBatchCommandInput(command)
		if input == nil || batchID != event.subject {
			err = ErrInvalidStore
			break
		}
		switch command.Request.Operation {
		case operation.BatchJoinV1.Metadata().Operation:
			if event.kind != "batch.joined" || !exactKeys(event.payload, "matter_id") || rawText(event.payload["matter_id"]) != matterID || dismissed[ownerKey(domain, batchID)] != "" {
				err = ErrInvalidStore
			}
		case operation.BatchLeaveV1.Metadata().Operation:
			if event.kind != "batch.left" || !exactKeys(event.payload, "matter_id") || rawText(event.payload["matter_id"]) != matterID || dismissed[ownerKey(domain, batchID)] != "" {
				err = ErrInvalidStore
			}
		case operation.BatchDismissV1.Metadata().Operation:
			if event.kind != "batch.dismissed" || !exactKeys(event.payload) || dismissed[ownerKey(domain, batchID)] != "" {
				err = ErrInvalidStore
			}
		}
		if err != nil {
			break
		}
		var receiptBytes []byte
		var first, last sql.NullInt64
		if err = db.QueryRow(`SELECT receipt,first_position,last_position FROM terminal_receipts WHERE domain_id=? AND command_id=?`, domain, commandID).Scan(&receiptBytes, &first, &last); err != nil {
			break
		}
		receipt, receiptErr := readReceipt(receiptBytes)
		wantOutputBytes, encodeErr := namedBatchReceiptOutput(wantOutput)
		if receiptErr != nil || encodeErr != nil || receipt.Result.Code != string(operation.ResultSucceeded) || receipt.Domain != domain ||
			receipt.ID != commandID || receipt.Hash != submission.hash || receipt.Operation.Name != command.Request.Operation.Name || receipt.Operation.Version != uint64(command.Request.Operation.Version) ||
			receipt.Environment.ID != submission.env || receipt.Environment.Sequence != submission.seq || receipt.Result.Problem != nil ||
			!reflect.DeepEqual(receipt.Result.Output, wantOutputBytes) || receipt.Range == nil || receipt.Range.Count != 1 ||
			receipt.Range.First != eventID || receipt.Range.Last != eventID || !first.Valid || !last.Valid ||
			first.Int64 != int64(position) || last.Int64 != int64(position) {
			err = ErrInvalidStore
			break
		}
		commands[key] = true
		pair := ownerKey(domain, batchID) + "/" + matterID
		switch event.kind {
		case "batch.joined":
			if active[pair] != "" {
				err = ErrInvalidStore
				break
			}
			var matterExists int
			if err = db.QueryRow(`SELECT count(*) FROM matters WHERE domain_id=? AND matter_id=?`, domain, matterID).Scan(&matterExists); err != nil || matterExists != 1 {
				err = ErrInvalidStore
				break
			}
			active[pair] = eventID
			episodes[ownerKey(domain, eventID)] = namedBatchMembership{domain: domain, batch: batchID, matter: matterID, joined: eventID}
		case "batch.left":
			joinedEventID := active[pair]
			member, exists := episodes[ownerKey(domain, joinedEventID)]
			if !exists || joinedEventID == "" {
				err = ErrInvalidStore
				break
			}
			member.left = eventID
			episodes[ownerKey(domain, joinedEventID)] = member
			delete(active, pair)
		case "batch.dismissed":
			dismissed[ownerKey(domain, batchID)] = eventID
		}
	}
	if err != nil {
		return fmt.Errorf("%w: named Batch membership event fold: %v", ErrInvalidStore, err)
	}
	gotDismissed := make([]struct{ domain, batch, event string }, 0)
	dismissRows, err := db.Query(`SELECT domain_id,batch_id,dismissed_event_id FROM m6_named_batches WHERE dismissed_event_id IS NOT NULL ORDER BY domain_id,batch_id`)
	if err != nil {
		return err
	}
	for dismissRows.Next() {
		var row struct{ domain, batch, event string }
		if err = dismissRows.Scan(&row.domain, &row.batch, &row.event); err != nil {
			break
		}
		gotDismissed = append(gotDismissed, row)
	}
	if err == nil {
		err = dismissRows.Err()
	}
	_ = dismissRows.Close()
	if err != nil {
		return err
	}
	wantDismissed := make([]struct{ domain, batch, event string }, 0, len(dismissed))
	for key, event := range dismissed {
		var domain, batch string
		for index := 0; index < len(key); index++ {
			if key[index] == '/' {
				domain, batch = key[:index], key[index+1:]
				break
			}
		}
		wantDismissed = append(wantDismissed, struct{ domain, batch, event string }{domain, batch, event})
	}
	sort.Slice(wantDismissed, func(i, j int) bool {
		if wantDismissed[i].domain != wantDismissed[j].domain {
			return wantDismissed[i].domain < wantDismissed[j].domain
		}
		return wantDismissed[i].batch < wantDismissed[j].batch
	})
	if !reflect.DeepEqual(gotDismissed, wantDismissed) {
		return fmt.Errorf("%w: named Batch dismissal projection differs from event history", ErrInvalidStore)
	}
	gotMembers := make([]namedBatchMembership, 0)
	memberRows, err := db.Query(`SELECT domain_id,batch_id,matter_id,joined_event_id,coalesce(left_event_id,'') FROM m6_named_batch_memberships
		ORDER BY domain_id,batch_id,matter_id,joined_event_id`)
	if err != nil {
		return err
	}
	for memberRows.Next() {
		var member namedBatchMembership
		if err = memberRows.Scan(&member.domain, &member.batch, &member.matter, &member.joined, &member.left); err != nil {
			break
		}
		gotMembers = append(gotMembers, member)
	}
	if err == nil {
		err = memberRows.Err()
	}
	_ = memberRows.Close()
	if err != nil {
		return err
	}
	wantMembers := make([]namedBatchMembership, 0, len(episodes))
	for _, member := range episodes {
		wantMembers = append(wantMembers, member)
	}
	sortNamedMemberships(gotMembers)
	sortNamedMemberships(wantMembers)
	if !reflect.DeepEqual(gotMembers, wantMembers) {
		return fmt.Errorf("%w: named Batch membership projection differs from event history", ErrInvalidStore)
	}
	return checkNamedBatchMembershipSubmissions(ctx, db, commands, known, eventLog)
}

func checkNamedBatchMembershipSubmissions(ctx context.Context, db namedBatchQueryer, eventCommands, known map[string]bool,
	eventLog []step12Event,
) error {
	history := make([]namedBatchHistoryEntry, 0, len(eventLog))
	for _, event := range eventLog {
		if event.kind != "batch.created" && event.kind != "batch.joined" && event.kind != "batch.left" && event.kind != "batch.dismissed" {
			continue
		}
		batchKey := ownerKey(event.domain, event.subject)
		if event.kind == "batch.created" && event.repo != "" {
			continue
		}
		if !known[batchKey] {
			return ErrInvalidStore
		}
		var epoch, generation, sequence uint64
		if err := db.QueryRowContext(ctx, `SELECT artifact_epoch,artifact_generation,artifact_sequence
			FROM terminal_receipts WHERE domain_id=? AND command_id=?`, event.domain, event.command).Scan(&epoch, &generation, &sequence); err != nil {
			return ErrInvalidStore
		}
		entry := namedBatchHistoryEntry{
			domain: event.domain, batch: event.subject, kind: event.kind,
			epoch: epoch, generation: generation, sequence: sequence, position: event.position, event: true,
		}
		if event.kind == "batch.joined" || event.kind == "batch.left" {
			entry.matter = rawText(event.payload["matter_id"])
		}
		history = append(history, entry)
	}
	rows, err := db.QueryContext(ctx, `SELECT domain_id,command_id,request_hash,command,epoch,environment_id,environment_sequence,operation_name,operation_version,state
		FROM submissions WHERE operation_name IN ('batch.join','batch.leave','batch.dismiss') ORDER BY domain_id,environment_id,environment_sequence`)
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
		definition, defined := namedBatchMembershipDefinition(command.Request.Operation)
		if decodeErr != nil || !defined || submission.version != 1 || definition.ValidateRequest(command.Request) != nil ||
			command.ID != submission.id || command.AuthorityDomainID != submission.domain || command.EnvironmentID != submission.env ||
			command.EnvironmentSequence != submission.seq || command.ExpectedAuthorityEpoch != submission.epoch {
			return ErrInvalidStore
		}
		var raw []byte
		var artifactEpoch, artifactGeneration, artifactSequence uint64
		err = db.QueryRowContext(ctx, `SELECT receipt,artifact_epoch,artifact_generation,artifact_sequence
			FROM terminal_receipts WHERE domain_id=? AND command_id=?`, submission.domain, submission.id).
			Scan(&raw, &artifactEpoch, &artifactGeneration, &artifactSequence)
		if errors.Is(err, sql.ErrNoRows) {
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
			receipt.Operation.Name != command.Request.Operation.Name || receipt.Operation.Version != uint64(command.Request.Operation.Version) || receipt.Environment.ID != submission.env || receipt.Environment.Sequence != submission.seq ||
			receipt.Result.Code == "" {
			return ErrInvalidStore
		}
		commandKey := ownerKey(submission.domain, submission.id)
		if receipt.Result.Code == string(operation.ResultSucceeded) {
			if submission.state != "terminal" {
				return ErrInvalidStore
			}
			if eventCommands[commandKey] {
				continue
			}
			if command.Request.Operation != operation.BatchJoinV1.Metadata().Operation || receipt.Range != nil || receipt.Result.Problem != nil {
				return ErrInvalidStore
			}
			_, batchID, matterID, wantOutput := namedBatchCommandInput(command)
			want, encodeErr := namedBatchReceiptOutput(wantOutput)
			if encodeErr != nil || !reflect.DeepEqual(receipt.Result.Output, want) || !known[ownerKey(submission.domain, batchID)] {
				return ErrInvalidStore
			}
			history = append(history, namedBatchHistoryEntry{
				domain: submission.domain, batch: batchID, matter: matterID, kind: "batch.join-noop",
				epoch: artifactEpoch, generation: artifactGeneration, sequence: artifactSequence,
			})
		} else if eventCommands[commandKey] || receipt.Result.Output != nil || receipt.Result.Problem == nil || receipt.Range != nil || submission.state != "terminal" {
			return ErrInvalidStore
		}
	}
	return checkNamedBatchMembershipReceiptHistory(history)
}

func checkNamedBatchMembershipReceiptHistory(history []namedBatchHistoryEntry) error {
	sort.Slice(history, func(i, j int) bool {
		left, right := history[i], history[j]
		if left.domain != right.domain {
			return left.domain < right.domain
		}
		if left.epoch != right.epoch {
			return left.epoch < right.epoch
		}
		if left.generation != right.generation {
			return left.generation < right.generation
		}
		return left.sequence < right.sequence
	})
	born := make(map[string]bool)
	dismissed := make(map[string]bool)
	active := make(map[string]bool)
	lastPosition := make(map[string]uint64)
	for _, entry := range history {
		batchKey := ownerKey(entry.domain, entry.batch)
		if entry.event {
			if entry.position <= lastPosition[entry.domain] {
				return ErrInvalidStore
			}
			lastPosition[entry.domain] = entry.position
		}
		switch entry.kind {
		case "batch.created":
			if !entry.event || born[batchKey] {
				return ErrInvalidStore
			}
			born[batchKey] = true
		case "batch.join-noop":
			pair := batchKey + "/" + entry.matter
			if entry.event || !born[batchKey] || dismissed[batchKey] || !active[pair] {
				return ErrInvalidStore
			}
		case "batch.joined":
			pair := batchKey + "/" + entry.matter
			if !entry.event || entry.matter == "" || !born[batchKey] || dismissed[batchKey] || active[pair] {
				return ErrInvalidStore
			}
			active[pair] = true
		case "batch.left":
			pair := batchKey + "/" + entry.matter
			if !entry.event || entry.matter == "" || !born[batchKey] || dismissed[batchKey] || !active[pair] {
				return ErrInvalidStore
			}
			delete(active, pair)
		case "batch.dismissed":
			if !entry.event || !born[batchKey] || dismissed[batchKey] {
				return ErrInvalidStore
			}
			dismissed[batchKey] = true
		default:
			return ErrInvalidStore
		}
	}
	return nil
}

func rawText(value []byte) string {
	var text string
	if artifactDecoder.Unmarshal(value, &text) != nil {
		return ""
	}
	return text
}

func sortNamedMemberships(members []namedBatchMembership) {
	sort.Slice(members, func(i, j int) bool {
		if members[i].domain != members[j].domain {
			return members[i].domain < members[j].domain
		}
		if members[i].batch != members[j].batch {
			return members[i].batch < members[j].batch
		}
		if members[i].matter != members[j].matter {
			return members[i].matter < members[j].matter
		}
		return members[i].joined < members[j].joined
	})
}
