package authoritystore

import (
	"database/sql"
	"fmt"

	"github.com/fxamacker/cbor/v2"
)

func checkStep8State(db *sql.DB, submissions []storedSubmission) error {
	byID := make(map[string]storedSubmission, len(submissions))
	for _, submission := range submissions {
		byID[ownerKey(submission.domain, submission.id)] = submission
	}

	var matters, claims int
	if err := db.QueryRow(`SELECT count(*) FROM matters`).Scan(&matters); err != nil {
		return err
	}
	if err := db.QueryRow(`SELECT count(*) FROM implicit_birth_claims`).Scan(&claims); err != nil || claims != matters {
		return ErrInvalidStore
	}
	claimRows, err := db.Query(`SELECT c.domain_id,c.matter_id,c.claim_epoch,c.owner_environment_id,c.repo_id,c.birth_command_id,
		m.repo_id,m.birth_event_id,e.command_id
		FROM implicit_birth_claims c
		JOIN matters m ON m.domain_id=c.domain_id AND m.matter_id=c.matter_id
		JOIN authority_events e ON e.domain_id=m.domain_id AND e.event_id=m.birth_event_id`)
	if err != nil {
		return err
	}
	type birthClaim struct {
		domain, matter, environment, repo, command, event string
		sequence                                          uint64
	}
	claimsByMatter := make(map[string]birthClaim, claims)
	for claimRows.Next() {
		var claim birthClaim
		var epoch uint64
		var matterRepo, eventCommand string
		if err = claimRows.Scan(&claim.domain, &claim.matter, &epoch, &claim.environment, &claim.repo, &claim.command, &matterRepo, &claim.event, &eventCommand); err != nil {
			break
		}
		birth, ok := byID[ownerKey(claim.domain, claim.command)]
		if epoch != 1 || claim.repo != matterRepo || claim.command != eventCommand || !ok || birth.operation != "matter.create" || birth.state != "terminal" || birth.env != claim.environment || birth.seq == 0 {
			err = ErrInvalidStore
			break
		}
		claim.sequence = birth.seq
		claimsByMatter[ownerKey(claim.domain, claim.matter)] = claim
	}
	if err == nil {
		err = claimRows.Err()
	}
	_ = claimRows.Close()
	if err != nil || len(claimsByMatter) != claims {
		return ErrInvalidStore
	}

	eventRows, err := db.Query(`SELECT domain_id,record FROM authority_events`)
	if err != nil {
		return err
	}
	stepEvents := 0
	for eventRows.Next() {
		var domain string
		var raw []byte
		var fields map[string]cbor.RawMessage
		if err = eventRows.Scan(&domain, &raw); err != nil {
			break
		}
		if canonicalDecode(raw, &fields) != nil {
			err = ErrInvalidStore
			break
		}
		var kind string
		if artifactDecoder.Unmarshal(fields["kind"], &kind) != nil {
			err = ErrInvalidStore
			break
		}
		if kind == "step.created" {
			var commandID string
			if artifactDecoder.Unmarshal(fields["command_id"], &commandID) != nil {
				err = ErrInvalidStore
				break
			}
			submission, ok := byID[ownerKey(domain, commandID)]
			if !ok {
				err = ErrInvalidStore
				break
			}
			if submission.version != 1 {
				continue
			}
			stepEvents++
		}
	}
	if err == nil {
		err = eventRows.Err()
	}
	_ = eventRows.Close()
	if err != nil {
		return ErrInvalidStore
	}
	var stepCount int
	if err = db.QueryRow(`SELECT count(*) FROM steps`).Scan(&stepCount); err != nil || stepCount != stepEvents {
		return ErrInvalidStore
	}
	type terminalReceipt struct {
		payload     []byte
		first, last sql.NullInt64
	}
	receipts := make(map[string]terminalReceipt)
	receiptRows, err := db.Query(`SELECT domain_id,command_id,receipt,first_position,last_position FROM terminal_receipts`)
	if err != nil {
		return err
	}
	for receiptRows.Next() {
		var domain, commandID string
		var receipt terminalReceipt
		if err = receiptRows.Scan(&domain, &commandID, &receipt.payload, &receipt.first, &receipt.last); err != nil {
			break
		}
		receipts[ownerKey(domain, commandID)] = receipt
	}
	if err == nil {
		err = receiptRows.Err()
	}
	_ = receiptRows.Close()
	if err != nil {
		return ErrInvalidStore
	}
	rows, err := db.Query(`SELECT s.domain_id,s.step_id,s.repo_id,s.matter_id,s.parent_id,s.locator,s.title,s.sort_key,s.state,s.birth_event_id,
		e.command_id,e.position,e.record
		FROM steps s JOIN authority_events e ON e.domain_id=s.domain_id AND e.event_id=s.birth_event_id
		ORDER BY s.domain_id,s.matter_id,e.position`)
	if err != nil {
		return err
	}
	counts := make(map[string]int)
	for rows.Next() {
		var domain, id, repo, matter, parent, locator, title, state, eventID, commandID string
		var sortKey int64
		var position uint64
		var rawEvent []byte
		if err = rows.Scan(&domain, &id, &repo, &matter, &parent, &locator, &title, &sortKey, &state, &eventID, &commandID, &position, &rawEvent); err != nil {
			break
		}
		key := ownerKey(domain, matter)
		claim, ok := claimsByMatter[key]
		submission, submitted := byID[ownerKey(domain, commandID)]
		if !ok || !submitted || submission.operation != "step.create" || submission.state != "terminal" || submission.env != claim.environment || submission.seq <= claim.sequence ||
			repo != claim.repo || parent != matter || state != "planned" || !ulid.MatchString(id) || !ulid.MatchString(eventID) || position == 0 || sortKey <= 0 {
			err = ErrInvalidStore
			break
		}
		count := counts[key] + 1
		counts[key] = count
		if locator != fmt.Sprintf("step-%02d", count) || sortKey != int64(count)*1000 {
			err = ErrInvalidStore
			break
		}

		var event struct {
			Schema      string `cbor:"schema"`
			ID          string `cbor:"event_id"`
			Domain      string `cbor:"domain_id"`
			Command     string `cbor:"command_id"`
			Hash        string `cbor:"request_hash"`
			Kind        string `cbor:"kind"`
			Subject     string `cbor:"subject_id"`
			Repo        string `cbor:"repo_id"`
			Environment struct {
				ID       string `cbor:"id"`
				Sequence uint64 `cbor:"sequence"`
			} `cbor:"environment"`
			Payload struct {
				Title   string `cbor:"title"`
				Locator string `cbor:"locator"`
				Parent  string `cbor:"parent"`
				SortKey int64  `cbor:"sort_key"`
			} `cbor:"payload"`
		}
		var eventFields map[string]cbor.RawMessage
		if closedPayload(rawEvent, &event, "schema", "event_id", "domain_id", "command_id", "request_hash", "kind", "subject_id", "repo_id", "acted_at", "occurred_at", "environment", "payload") != nil || canonicalDecode(rawEvent, &eventFields) != nil {
			err = ErrInvalidStore
			break
		}
		var nested map[string]cbor.RawMessage
		if canonicalDecode(eventFields["environment"], &nested) != nil || !exactKeys(nested, "id", "sequence") {
			err = ErrInvalidStore
			break
		}
		nested = nil
		if canonicalDecode(eventFields["payload"], &nested) != nil || !exactKeys(nested, "title", "locator", "parent", "sort_key") ||
			event.Schema != "wipd.event/1" || event.ID != eventID || event.Domain != domain || event.Command != commandID || event.Hash != submission.hash ||
			event.Kind != "step.created" || event.Subject != id || event.Repo != repo || event.Environment.ID != submission.env || event.Environment.Sequence != submission.seq ||
			event.Payload.Title != title || event.Payload.Locator != locator || event.Payload.Parent != matter || event.Payload.SortKey != sortKey {
			err = ErrInvalidStore
			break
		}

		var commandFields map[string]cbor.RawMessage
		if canonicalDecode(submission.command, &commandFields) != nil {
			err = ErrInvalidStore
			break
		}
		var commandIdentity struct {
			Causation   *string `cbor:"causation_command_id"`
			Correlation string  `cbor:"correlation_command_id"`
		}
		var input struct {
			Parent string `cbor:"parent_id"`
			Title  string `cbor:"title"`
		}
		var claimContext struct {
			ID    string `cbor:"id"`
			Epoch uint64 `cbor:"epoch"`
		}
		var context struct {
			Repo string `cbor:"repo_id"`
		}
		if artifactDecoder.Unmarshal(submission.command, &commandIdentity) != nil || artifactDecoder.Unmarshal(commandFields["input"], &input) != nil ||
			artifactDecoder.Unmarshal(commandFields["claim"], &claimContext) != nil || artifactDecoder.Unmarshal(commandFields["context"], &context) != nil ||
			commandIdentity.Causation == nil || *commandIdentity.Causation != claim.command || commandIdentity.Correlation != claim.command ||
			input.Parent != matter || input.Title != title || claimContext.ID != matter || claimContext.Epoch != 1 || context.Repo != repo {
			err = ErrInvalidStore
			break
		}

		terminal, ok := receipts[ownerKey(domain, commandID)]
		if !ok {
			err = ErrInvalidStore
			break
		}
		r, receiptErr := readReceipt(terminal.payload)
		if receiptErr != nil || !terminal.first.Valid || !terminal.last.Valid || terminal.first.Int64 != int64(position) || terminal.last.Int64 != int64(position) || r.Result.Code != "result.succeeded" || r.Range == nil ||
			r.Range.Count != 1 || r.Range.First != eventID || r.Range.Last != eventID {
			err = ErrInvalidStore
			break
		}
		var output struct {
			ID       string `cbor:"id"`
			ParentID string `cbor:"parent_id"`
			MatterID string `cbor:"matter_id"`
			Locator  string `cbor:"locator"`
			Title    string `cbor:"title"`
			SortKey  int64  `cbor:"sort_key"`
			State    string `cbor:"state"`
		}
		if closedPayload(r.Result.Output, &output, "id", "parent_id", "matter_id", "locator", "title", "sort_key", "state") != nil ||
			output.ID != id || output.ParentID != matter || output.MatterID != matter || output.Locator != locator || output.Title != title || output.SortKey != sortKey || output.State != state {
			err = ErrInvalidStore
			break
		}
	}
	if err == nil {
		err = rows.Err()
	}
	_ = rows.Close()
	if err != nil {
		return ErrInvalidStore
	}
	return nil
}
