package authoritystore

import (
	"database/sql"

	"github.com/procrastivity/wip/internal/operation"
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
