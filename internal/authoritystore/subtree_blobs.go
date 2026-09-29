package authoritystore

import (
	"context"
	"database/sql"
	"sort"
	"time"

	"github.com/fxamacker/cbor/v2"
)

// subtreeRequiredBlobClosureTx derives claim-required blobs from the exact
// accepted birth events and immutable submitted commands in the Matter's
// direct subtree. A caller-provided digest list is never the source of truth.
func subtreeRequiredBlobClosureTx(ctx context.Context, tx *sql.Tx, domain, matter string, asOf uint64) ([]string, error) {
	var exists int
	if err := tx.QueryRowContext(ctx, `SELECT count(*) FROM matters m JOIN authority_events e ON e.domain_id=m.domain_id AND e.event_id=m.birth_event_id WHERE m.domain_id=? AND m.matter_id=? AND e.position<=?`, domain, matter, asOf).Scan(&exists); err != nil {
		return nil, err
	}
	if exists != 1 {
		return nil, ErrNotFound
	}
	rows, err := tx.QueryContext(ctx, `SELECT event_id,record,command_id FROM authority_events WHERE domain_id=? AND position<=? ORDER BY position`, domain, asOf)
	if err != nil {
		return nil, err
	}
	commands := make([]string, 0)
	for rows.Next() {
		var eventID string
		var record []byte
		var commandID string
		if err = rows.Scan(&eventID, &record, &commandID); err != nil {
			break
		}
		member, belongs, eventErr := subtreeEventMember(record, domain, eventID, commandID, matter)
		if eventErr != nil {
			err = eventErr
			break
		}
		if belongs {
			if member != commandID {
				err = ErrInvalidStore
				break
			}
			commands = append(commands, commandID)
		}
	}
	if err == nil {
		err = rows.Err()
	}
	_ = rows.Close()
	if err != nil {
		return nil, err
	}
	if len(commands) == 0 {
		return nil, ErrInvalidStore
	}
	commandClosures := make([]map[string]uint64, 0, len(commands))
	for _, commandID := range commands {
		var command []byte
		if err = tx.QueryRowContext(ctx, `SELECT command FROM submissions WHERE domain_id=? AND command_id=? AND state='terminal'`, domain, commandID).Scan(&command); err != nil {
			return nil, ErrInvalidStore
		}
		blobs, e := commandBlobLengths(command)
		if e != nil {
			return nil, e
		}
		commandClosures = append(commandClosures, blobs)
	}
	closure, err := mergeBlobClosures(commandClosures)
	if err != nil {
		return nil, err
	}
	result := make([]string, 0, len(closure))
	for digest, length := range closure {
		var referenced int
		if err = tx.QueryRowContext(ctx, `SELECT count(*) FROM blob_references r JOIN blob_products p USING(domain_id,digest) WHERE r.domain_id=? AND r.digest=? AND r.first_position<=? AND p.verified=1 AND p.byte_length=?`, domain, digest, asOf, length).Scan(&referenced); err != nil {
			return nil, err
		}
		if referenced != 1 {
			return nil, ErrManifestMismatch
		}
		result = append(result, digest)
	}
	sort.Strings(result)
	return result, nil
}

func subtreeEventMember(record []byte, domain, eventID, commandID, matter string) (string, bool, error) {
	var event map[string]cbor.RawMessage
	if err := canonicalDecode(record, &event); err != nil || !exactKeys(event, "schema", "event_id", "domain_id", "command_id", "request_hash", "environment", "acted_at", "occurred_at", "kind", "subject_id", "repo_id", "payload") {
		return "", false, ErrInvalidStore
	}
	var envelope struct {
		Schema      string `cbor:"schema"`
		EventID     string `cbor:"event_id"`
		DomainID    string `cbor:"domain_id"`
		CommandID   string `cbor:"command_id"`
		RequestHash string `cbor:"request_hash"`
		ActedAt     string `cbor:"acted_at"`
		OccurredAt  string `cbor:"occurred_at"`
		Kind        string `cbor:"kind"`
		SubjectID   string `cbor:"subject_id"`
		RepoID      string `cbor:"repo_id"`
		Environment struct {
			ID       string `cbor:"id"`
			Sequence uint64 `cbor:"sequence"`
		} `cbor:"environment"`
		Payload cbor.RawMessage `cbor:"payload"`
	}
	if artifactDecoder.Unmarshal(record, &envelope) != nil || envelope.Schema != "wipd.event/1" ||
		envelope.EventID != eventID || !ulid.MatchString(eventID) || envelope.DomainID != domain || !ulid.MatchString(domain) ||
		envelope.CommandID != commandID || !ulid.MatchString(commandID) || !validDigest(envelope.RequestHash) ||
		!ulid.MatchString(envelope.SubjectID) || !ulid.MatchString(envelope.RepoID) || !ulid.MatchString(envelope.Environment.ID) || envelope.Environment.Sequence == 0 {
		return "", false, ErrInvalidStore
	}
	for _, timestamp := range []string{envelope.ActedAt, envelope.OccurredAt} {
		parsed, err := utcTime(timestamp)
		if err != nil || parsed.Format(time.RFC3339Nano) != timestamp {
			return "", false, ErrInvalidStore
		}
	}
	var environmentFields map[string]cbor.RawMessage
	var payload map[string]cbor.RawMessage
	if canonicalDecode(event["environment"], &environmentFields) != nil || !exactKeys(environmentFields, "id", "sequence") ||
		canonicalDecode(event["payload"], &payload) != nil {
		return "", false, ErrInvalidStore
	}
	if envelope.Kind != "matter.created" && envelope.Kind != "step.created" {
		return "", false, nil
	}
	if envelope.Kind == "matter.created" {
		var matterPayload struct {
			ID      string `cbor:"id"`
			Locator string `cbor:"locator"`
			Title   string `cbor:"title"`
		}
		if !exactKeys(payload, "id", "locator", "title") || artifactDecoder.Unmarshal(event["payload"], &matterPayload) != nil ||
			matterPayload.ID != envelope.SubjectID || matterPayload.Locator == "" || matterPayload.Title == "" {
			return "", false, ErrInvalidStore
		}
		return commandID, envelope.SubjectID == matter, nil
	}
	var stepPayload struct {
		Title   string `cbor:"title"`
		Locator string `cbor:"locator"`
		Parent  string `cbor:"parent"`
		SortKey int64  `cbor:"sort_key"`
	}
	if !exactKeys(payload, "title", "locator", "parent", "sort_key") || artifactDecoder.Unmarshal(event["payload"], &stepPayload) != nil ||
		!ulid.MatchString(stepPayload.Parent) || stepPayload.Title == "" || stepPayload.Locator == "" || stepPayload.SortKey <= 0 {
		return "", false, ErrInvalidStore
	}
	return commandID, stepPayload.Parent == matter, nil
}

func mergeBlobClosures(commands []map[string]uint64) (map[string]uint64, error) {
	closure := make(map[string]uint64)
	for _, blobs := range commands {
		for digest, length := range blobs {
			if prior, ok := closure[digest]; ok && prior != length {
				return nil, ErrManifestMismatch
			}
			closure[digest] = length
		}
	}
	return closure, nil
}

func sameDigestSet(asserted, derived []string) bool {
	if len(asserted) != len(derived) {
		return false
	}
	seen := make(map[string]struct{}, len(asserted))
	for _, digest := range asserted {
		if _, duplicate := seen[digest]; duplicate {
			return false
		}
		seen[digest] = struct{}{}
	}
	for _, digest := range derived {
		if _, ok := seen[digest]; !ok {
			return false
		}
	}
	return true
}
