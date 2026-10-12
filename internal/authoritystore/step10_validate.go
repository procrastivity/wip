package authoritystore

import (
	"database/sql"
	"fmt"
	"strings"

	"github.com/fxamacker/cbor/v2"
	"github.com/procrastivity/wip/internal/operation"
	"github.com/procrastivity/wip/internal/schedulerhook"
)

func checkStep10State(db *sql.DB, submissions []storedSubmission) error {
	contentCommands := make(map[string]storedSubmission)
	for _, submission := range submissions {
		if !contentOperation(operation.ID{Name: submission.operation, Version: uint16(submission.version)}) {
			continue
		}
		contentCommands[ownerKey(submission.domain, submission.id)] = submission
		var code string
		err := db.QueryRow(`SELECT result_code FROM terminal_receipts WHERE domain_id=? AND command_id=?`, submission.domain, submission.id).Scan(&code)
		if submission.state == "submitted" {
			if err != sql.ErrNoRows {
				return ErrInvalidStore
			}
			continue
		}
		if err != nil {
			return ErrInvalidStore
		}
		var events, segments int
		if err = db.QueryRow(`SELECT count(*) FROM authority_events WHERE domain_id=? AND command_id=?`, submission.domain, submission.id).Scan(&events); err != nil {
			return err
		}
		if err = db.QueryRow(`SELECT count(*) FROM content_segments WHERE domain_id=? AND command_id=?`, submission.domain, submission.id).Scan(&segments); err != nil {
			return err
		}
		if code == string(operation.ResultSucceeded) && (events != 1 || segments != 1) ||
			code != string(operation.ResultSucceeded) && (events != 0 || segments != 0) {
			return ErrInvalidStore
		}
	}
	rows, err := db.Query(`SELECT c.domain_id,c.content_id,c.subject_id,c.matter_id,c.repo_id,c.kind,c.blob_digest,c.byte_length,c.command_id,c.event_id,
		e.position,e.record,s.command,s.request_hash,s.environment_id,s.environment_sequence,s.operation_name,s.operation_version,
		p.verified,p.byte_length,r.first_position,coalesce(m.matter_id,st.matter_id),coalesce(m.repo_id,st.repo_id)
		FROM content_segments c
		JOIN authority_events e ON e.domain_id=c.domain_id AND e.event_id=c.event_id
		JOIN submissions s ON s.domain_id=c.domain_id AND s.command_id=c.command_id
		JOIN blob_products p ON p.domain_id=c.domain_id AND p.digest=c.blob_digest
		JOIN blob_references r ON r.domain_id=c.domain_id AND r.digest=c.blob_digest
		LEFT JOIN matters m ON m.domain_id=c.domain_id AND m.matter_id=c.subject_id
		LEFT JOIN steps st ON st.domain_id=c.domain_id AND st.step_id=c.subject_id
		ORDER BY c.domain_id,e.position`)
	if err != nil {
		return err
	}
	seen := make(map[string]bool, len(contentCommands))
	for rows.Next() {
		var domain, contentID, subject, matter, repo, kind, digest, commandID, eventID, hash, environment, operationName, subjectMatter, subjectRepo string
		var length, position, seq, version, productLength, refPosition uint64
		var record, command []byte
		var verified int
		if err = rows.Scan(&domain, &contentID, &subject, &matter, &repo, &kind, &digest, &length, &commandID, &eventID,
			&position, &record, &command, &hash, &environment, &seq, &operationName, &version, &verified, &productLength, &refPosition,
			&subjectMatter, &subjectRepo); err != nil {
			break
		}
		submission, ok := contentCommands[ownerKey(domain, commandID)]
		if !ok || seen[ownerKey(domain, commandID)] || submission.hash != hash || submission.env != environment || submission.seq != seq ||
			submission.operation != operationName || submission.version != version || submission.state != "terminal" || verified != 1 ||
			length != productLength || refPosition > position {
			err = ErrInvalidStore
			break
		}
		gotContentID, gotSubject, gotKind, gotDigest, gotLength, eventErr := validContentEventRecord(record, domain, eventID, submission)
		if eventErr != nil || gotContentID != contentID || gotSubject != subject || gotKind != kind || gotDigest != digest || gotLength != length {
			err = ErrInvalidStore
			break
		}
		if subjectMatter != matter || subjectRepo != repo {
			err = ErrInvalidStore
			break
		}
		seen[ownerKey(domain, commandID)] = true
	}
	if err == nil {
		err = rows.Err()
	}
	_ = rows.Close()
	if err != nil {
		return ErrInvalidStore
	}
	if len(seen) == 0 {
		for _, submission := range contentCommands {
			var code string
			if err = db.QueryRow(`SELECT result_code FROM terminal_receipts WHERE domain_id=? AND command_id=?`, submission.domain, submission.id).Scan(&code); err == nil && code == string(operation.ResultSucceeded) {
				return ErrInvalidStore
			}
		}
	}
	for key, submission := range contentCommands {
		var code string
		if err = db.QueryRow(`SELECT result_code FROM terminal_receipts WHERE domain_id=? AND command_id=?`, submission.domain, submission.id).Scan(&code); err == nil &&
			code == string(operation.ResultSucceeded) && !seen[key] {
			return ErrInvalidStore
		}
	}
	return nil
}

func validateSchedulerHookHistoryEvent(event step12Event, submission storedSubmission, command operation.Command) error {
	input, ok := command.Request.Input.(operation.SchedulerHookInput)
	if !ok || !operation.Step10Operation(command.Request.Operation) ||
		operation.SchedulerOrchestrateV1.ValidateRequest(command.Request) != nil ||
		event.kind != "scheduler.hook-completed" || event.subject != input.MatterID || event.repo != command.Request.Context.Repo {
		return ErrInvalidStore
	}
	selectedVerifier := input.Kind == "work" && input.Role == "verifier" &&
		input.Revision == operation.SchedulerStep10VerifierRevision
	if selectedVerifier {
		if !exactKeys(event.payload, "effect_key", "kind", "version", "input_digest", "typed_result", "result_digest", "source_revision") {
			return ErrInvalidStore
		}
	} else if !exactKeys(event.payload, "effect_key", "kind", "version", "input_digest", "typed_result", "result_digest") {
		return ErrInvalidStore
	}
	var effectKey map[string]cbor.RawMessage
	if canonicalDecode(event.payload["effect_key"], &effectKey) != nil || !exactKeys(effectKey, "domain_id", "command_id") {
		return ErrInvalidStore
	}
	var domain, commandID string
	if artifactDecoder.Unmarshal(effectKey["domain_id"], &domain) != nil || artifactDecoder.Unmarshal(effectKey["command_id"], &commandID) != nil ||
		domain != submission.domain || commandID != submission.id {
		return ErrInvalidStore
	}
	var kind string
	var version uint64
	var inputDigest, resultDigest string
	if artifactDecoder.Unmarshal(event.payload["kind"], &kind) != nil || artifactDecoder.Unmarshal(event.payload["version"], &version) != nil ||
		artifactDecoder.Unmarshal(event.payload["input_digest"], &inputDigest) != nil ||
		artifactDecoder.Unmarshal(event.payload["result_digest"], &resultDigest) != nil {
		return ErrInvalidStore
	}
	typedRaw := event.payload["typed_result"]
	var typedFields map[string]cbor.RawMessage
	var typed schedulerhook.TypedResult
	if canonicalDecode(typedRaw, &typedFields) != nil || !exactKeys(typedFields, "kind", "version", "work", "plan", "ask") ||
		artifactDecoder.Unmarshal(typedRaw, &typed) != nil {
		return ErrInvalidStore
	}
	switch typed.Kind {
	case schedulerhook.KindWork:
		var fields map[string]cbor.RawMessage
		if canonicalDecode(typedFields["work"], &fields) != nil || !exactKeys(fields, "outcome", "revision", "evidence_refs", "problem_code") || typed.Work == nil || typed.Plan != nil || typed.Ask != nil {
			return ErrInvalidStore
		}
	case schedulerhook.KindPlan:
		var fields map[string]cbor.RawMessage
		if canonicalDecode(typedFields["plan"], &fields) != nil || !exactKeys(fields, "outcome", "steps", "problem_code") || typed.Plan == nil || typed.Work != nil || typed.Ask != nil {
			return ErrInvalidStore
		}
	case schedulerhook.KindAsk:
		var fields map[string]cbor.RawMessage
		if canonicalDecode(typedFields["ask"], &fields) != nil || !exactKeys(fields, "handling") || typed.Ask == nil || typed.Work != nil || typed.Plan != nil {
			return ErrInvalidStore
		}
	default:
		return ErrInvalidStore
	}
	request, err := schedulerhook.RequestFromCommand(command)
	if err != nil || kind != string(typed.Kind) || version != uint64(typed.Version) ||
		kind != input.Kind || version != input.Version || typed.Version != uint16(input.Version) {
		return ErrInvalidStore
	}
	if selectedVerifier {
		var selected schedulerhook.SelectedSourceRevision
		if artifactDecoder.Unmarshal(event.payload["source_revision"], &selected) != nil ||
			selected.ProtocolRevision != input.Revision || selected.BaseRevision == "" ||
			!validDigest(selected.CandidateManifestSHA256) ||
			selected.CandidateRevision != "candidate-v1:"+strings.TrimPrefix(selected.CandidateManifestSHA256, "sha256:") ||
			!validDigest(selected.CompleteTreeSHA256) || !validDigest(selected.SnapshotSHA256) ||
			!schedulerHookBlobDigestMatches(command, "tree-manifest", selected.CompleteTreeSHA256) ||
			!schedulerHookBlobDigestMatches(command, "snapshot", selected.SnapshotSHA256) {
			return ErrInvalidStore
		}
	}
	computedInput, err := schedulerhook.InputDigest(request)
	if err != nil || computedInput != inputDigest {
		return ErrInvalidStore
	}
	key := schedulerhook.Key{DomainID: submission.domain, CommandID: submission.id}
	computedResult, err := schedulerhook.ResultDigest(key, inputDigest, typed)
	if err != nil || computedResult != resultDigest {
		return ErrInvalidStore
	}
	product := schedulerhook.Result{Key: key, InputDigest: inputDigest, Typed: typed, ResultDigest: resultDigest}
	output, err := schedulerhook.OperationOutput(product)
	if err != nil {
		return ErrInvalidStore
	}
	if _, err = validateSchedulerHookResult(command, operation.Result{Code: operation.ResultSucceeded, Output: output}); err != nil {
		return ErrInvalidStore
	}
	return nil
}

func schedulerHookBlobDigestMatches(command operation.Command, name, digest string) bool {
	matches := 0
	for _, blob := range command.Request.Blobs {
		if blob.Name == name {
			matches++
			if blob.Digest != digest {
				return false
			}
		}
	}
	return matches == 1
}

func schedulerHookProducedEvidenceAt(db *sql.DB, domain string, position uint64, digest string) error {
	var eventID, commandID, requestHash, environment, operationName, state string
	var sequence, version uint64
	var record, rawCommand []byte
	err := db.QueryRow(`SELECT e.event_id,e.command_id,e.record,s.request_hash,s.command,s.environment_id,
		s.environment_sequence,s.operation_name,s.operation_version,s.state
		FROM authority_events e
		JOIN submissions s ON s.domain_id=e.domain_id AND s.command_id=e.command_id
		JOIN terminal_receipts r ON r.domain_id=s.domain_id AND r.command_id=s.command_id
		WHERE e.domain_id=? AND e.position=?
		AND r.result_code='result.succeeded' AND r.first_position<=e.position AND r.last_position>=e.position`,
		domain, position).Scan(&eventID, &commandID, &record, &requestHash, &rawCommand, &environment,
		&sequence, &operationName, &version, &state)
	if err != nil {
		return fmt.Errorf("read completion event: %w", err)
	}
	if state != "terminal" {
		return fmt.Errorf("submission state is %q", state)
	}
	event, err := parseStep12Event(record, domain, position, eventID, commandID)
	command, commandErr := operation.DecodeCanonicalCommand(rawCommand)
	if err != nil || commandErr != nil || event.hash != requestHash || event.environment != environment ||
		event.sequence != sequence || event.acted != command.ActedAt ||
		operationName != command.Request.Operation.Name || version != uint64(command.Request.Operation.Version) ||
		command.ID != commandID || command.AuthorityDomainID != domain ||
		command.EnvironmentID != environment || command.EnvironmentSequence != sequence ||
		validateSchedulerHookHistoryEvent(event, storedSubmission{
			domain: domain, id: commandID, hash: requestHash, env: environment, operation: operationName,
			state: state, command: rawCommand, seq: sequence, version: version,
		}, command) != nil {
		return fmt.Errorf("event or command identity failed validation: event=%v command=%v", err, commandErr)
	}
	var typed schedulerhook.TypedResult
	if err = artifactDecoder.Unmarshal(event.payload["typed_result"], &typed); err != nil {
		return fmt.Errorf("decode typed result: %w", err)
	}
	if typed.Kind != schedulerhook.KindWork || typed.Work == nil {
		return fmt.Errorf("completion event has no Work result")
	}
	seen := make(map[string]bool, len(typed.Work.EvidenceRefs))
	for _, ref := range typed.Work.EvidenceRefs {
		if !validDigest(ref) || seen[ref] {
			return fmt.Errorf("work result has invalid or duplicate evidence reference")
		}
		seen[ref] = true
	}
	if !seen[digest] {
		return fmt.Errorf("work result does not name %s", digest)
	}
	return nil
}

func validateSchedulerHookEvidenceReferences(positions map[string]uint64, event step12Event, submission storedSubmission, position uint64) error {
	var typed schedulerhook.TypedResult
	if artifactDecoder.Unmarshal(event.payload["typed_result"], &typed) != nil {
		return ErrInvalidStore
	}
	if typed.Kind != schedulerhook.KindWork || typed.Work == nil {
		return nil
	}
	seen := make(map[string]bool, len(typed.Work.EvidenceRefs))
	for _, digest := range typed.Work.EvidenceRefs {
		if !validDigest(digest) || seen[digest] {
			return ErrInvalidStore
		}
		seen[digest] = true
		first, ok := positions[ownerKey(submission.domain, digest)]
		if !ok || first > position {
			return ErrInvalidStore
		}
	}
	return nil
}

func schedulerHookEvidencePositions(db *sql.DB) (map[string]uint64, error) {
	rows, err := db.Query(`SELECT domain_id,digest,first_position FROM blob_references`)
	if err != nil {
		return nil, err
	}
	positions := make(map[string]uint64)
	for rows.Next() {
		var domain, digest string
		var position uint64
		if err = rows.Scan(&domain, &digest, &position); err != nil {
			break
		}
		positions[ownerKey(domain, digest)] = position
	}
	if err == nil {
		err = rows.Err()
	}
	if closeErr := rows.Close(); err == nil {
		err = closeErr
	}
	if err != nil {
		return nil, err
	}
	return positions, nil
}
