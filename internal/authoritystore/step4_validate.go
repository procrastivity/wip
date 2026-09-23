package authoritystore

import (
	"bytes"
	"crypto/sha256"
	"database/sql"
	"encoding/binary"
	"errors"

	"github.com/fxamacker/cbor/v2"
)

type receiptRecord struct {
	Schema    string `cbor:"schema"`
	Domain    string `cbor:"domain_id"`
	Epoch     uint64 `cbor:"authority_epoch"`
	Identity  string `cbor:"identity_schema"`
	ID        string `cbor:"command_id"`
	Hash      string `cbor:"request_hash"`
	Operation struct {
		Name    string `cbor:"name"`
		Version uint64 `cbor:"version"`
	} `cbor:"operation"`
	Environment struct {
		ID       string `cbor:"id"`
		Sequence uint64 `cbor:"sequence"`
	} `cbor:"environment"`
	Result struct {
		Code    string  `cbor:"code"`
		Output  []byte  `cbor:"output"`
		Problem *string `cbor:"problem_code"`
	} `cbor:"result"`
	Range *struct {
		First string `cbor:"first_event_id"`
		Last  string `cbor:"last_event_id"`
		Count uint64 `cbor:"event_count"`
	} `cbor:"accepted_events"`
}

type storedSubmission struct {
	domain, id, hash, env, operation, state string
	command                                 []byte
	epoch, seq, version                     uint64
}

func readReceipt(raw []byte) (receiptRecord, error) {
	var r receiptRecord
	if err := closedPayload(raw, &r, "schema", "domain_id", "authority_epoch", "identity_schema", "command_id", "request_hash", "operation", "environment", "result", "accepted_events"); err != nil {
		return r, err
	}
	var fields map[string]cbor.RawMessage
	if err := canonicalDecode(raw, &fields); err != nil {
		return r, err
	}
	var nested map[string]cbor.RawMessage
	for _, part := range []struct {
		key   string
		names []string
	}{
		{"operation", []string{"name", "version"}}, {"environment", []string{"id", "sequence"}}, {"result", []string{"code", "output", "problem_code"}},
	} {
		nested = nil
		if err := canonicalDecode(fields[part.key], &nested); err != nil || !exactKeys(nested, part.names...) {
			return r, ErrInvalidStore
		}
	}
	if r.Range != nil {
		nested = nil
		if err := canonicalDecode(fields["accepted_events"], &nested); err != nil || !exactKeys(nested, "first_event_id", "last_event_id", "event_count") {
			return r, ErrInvalidStore
		}
	}
	return r, nil
}

// checkStep4State rederives range, prefix and materialized projection and
// links every artifact row to one exact signed terminal payload on reopen.
func checkStep4State(db *sql.DB) error {
	rows, err := db.Query(`SELECT domain_id,command_id,request_hash,command,epoch,environment_id,environment_sequence,operation_name,operation_version,state FROM submissions ORDER BY domain_id,environment_id,environment_sequence`)
	if err != nil {
		return err
	}
	var all []storedSubmission
	for rows.Next() {
		var s storedSubmission
		if err = rows.Scan(&s.domain, &s.id, &s.hash, &s.command, &s.epoch, &s.env, &s.seq, &s.operation, &s.version, &s.state); err != nil {
			break
		}
		all = append(all, s)
	}
	if err == nil {
		err = rows.Err()
	}
	_ = rows.Close()
	if err != nil {
		return err
	}
	var count int
	if err = db.QueryRow(`SELECT count(*) FROM authority_artifacts`).Scan(&count); err != nil {
		return err
	}
	var terminals int
	for _, s := range all {
		if !ulid.MatchString(s.domain) || !ulid.MatchString(s.id) || !ulid.MatchString(s.env) || !validDigest(s.hash) || s.epoch == 0 || s.seq == 0 || s.operation != "matter.create" || s.version != 1 {
			return ErrInvalidStore
		}
		var commandFields map[string]cbor.RawMessage
		if err = canonicalDecode(s.command, &commandFields); err != nil || !exactKeys(commandFields, "schema", "command_id", "authority", "environment", "acted_at", "actor", "causation_command_id", "correlation_command_id", "operation", "context", "claim", "input", "blobs") || digestBytes(append([]byte("wipd/request-hash/v1\x00"), s.command...)) != s.hash {
			return ErrInvalidStore
		}
		var identity struct {
			Schema    string `cbor:"schema"`
			ID        string `cbor:"command_id"`
			Authority struct {
				Domain string `cbor:"domain_id"`
				Epoch  uint64 `cbor:"expected_epoch"`
			} `cbor:"authority"`
			Environment struct {
				ID       string `cbor:"id"`
				Sequence uint64 `cbor:"sequence"`
			} `cbor:"environment"`
			Operation struct {
				Name    string `cbor:"name"`
				Version uint64 `cbor:"version"`
			} `cbor:"operation"`
		}
		if err = artifactDecoder.Unmarshal(s.command, &identity); err != nil || identity.Schema != "wipd.command/1" || identity.ID != s.id || identity.Authority.Domain != s.domain || identity.Authority.Epoch != s.epoch || identity.Environment.ID != s.env || identity.Environment.Sequence != s.seq || identity.Operation.Name != s.operation || identity.Operation.Version != s.version {
			return ErrInvalidStore
		}
		var receipt, wrapper []byte
		var epoch, gen, seq uint64
		var first, last sql.NullInt64
		var code string
		err = db.QueryRow(`SELECT receipt,wrapper,artifact_epoch,artifact_generation,artifact_sequence,first_position,last_position,result_code FROM terminal_receipts WHERE domain_id=? AND command_id=?`, s.domain, s.id).Scan(&receipt, &wrapper, &epoch, &gen, &seq, &first, &last, &code)
		if s.state == "submitted" {
			if !errors.Is(err, sql.ErrNoRows) {
				return ErrInvalidStore
			}
			continue
		}
		if s.state != "terminal" || err != nil || epoch != s.epoch {
			return ErrInvalidStore
		}
		terminals++
		r, e := readReceipt(receipt)
		if e != nil || r.Schema != "wipd.terminal-receipt/1" || r.Identity != "wipd.command/1" || r.Domain != s.domain || r.Epoch != s.epoch || r.ID != s.id || r.Hash != s.hash || r.Operation.Name != s.operation || r.Operation.Version != s.version || r.Environment.ID != s.env || r.Environment.Sequence != s.seq || r.Result.Code != code {
			return ErrInvalidStore
		}
		var artifact signedArtifact
		if err = artifactDecoder.Unmarshal(wrapper, &artifact); err != nil || artifact.Kind != "portable-receipt" || artifact.PayloadSchema != "wipd.terminal-receipt/1" || !bytes.Equal(artifact.Payload, receipt) || artifact.Epoch != epoch || artifact.Generation == nil || *artifact.Generation != gen || artifact.Sequence == nil || *artifact.Sequence != seq {
			return ErrInvalidStore
		}
		var storedWrapper []byte
		if err = db.QueryRow(`SELECT wrapper FROM authority_artifacts WHERE domain_id=? AND epoch=? AND generation=? AND sequence=?`, s.domain, epoch, gen, seq).Scan(&storedWrapper); err != nil || !bytes.Equal(wrapper, storedWrapper) {
			return ErrInvalidStore
		}
		if code == "result.succeeded" {
			if !first.Valid || !last.Valid || first.Int64 != last.Int64 || r.Range == nil || r.Range.Count != 1 || r.Result.Problem != nil || len(r.Result.Output) == 0 {
				return ErrInvalidStore
			}
			var eventID string
			if err = db.QueryRow(`SELECT event_id FROM authority_events WHERE domain_id=? AND position=? AND command_id=?`, s.domain, first.Int64, s.id).Scan(&eventID); err != nil || eventID != r.Range.First || eventID != r.Range.Last {
				return ErrInvalidStore
			}
		} else if (code == "result.rejected" || code == "result.refused" || code == "result.failed") && !first.Valid && !last.Valid && r.Range == nil && r.Result.Output == nil && r.Result.Problem != nil {
			prefix := string(*r.Result.Problem)
			if !validResultProblem(code, prefix) {
				return ErrInvalidStore
			}
		} else {
			return ErrInvalidStore
		}
		var eventCount int
		if err = db.QueryRow(`SELECT count(*) FROM authority_events WHERE domain_id=? AND command_id=?`, s.domain, s.id).Scan(&eventCount); err != nil || (code == "result.succeeded" && eventCount != 1) || (code != "result.succeeded" && eventCount != 0) {
			return ErrInvalidStore
		}
	}
	if terminals != count {
		return ErrInvalidStore
	}
	return checkStep4Events(db, all)
}

func validResultProblem(code, problem string) bool {
	switch code {
	case "result.rejected":
		return bytes.HasPrefix([]byte(problem), []byte("operation.")) || bytes.HasPrefix([]byte(problem), []byte("validation.")) || bytes.HasPrefix([]byte(problem), []byte("not-found."))
	case "result.refused":
		return bytes.HasPrefix([]byte(problem), []byte("refusal."))
	case "result.failed":
		return bytes.HasPrefix([]byte(problem), []byte("internal."))
	}
	return false
}

func checkStep4Events(db *sql.DB, submissions []storedSubmission) error {
	byID := make(map[string]storedSubmission, len(submissions))
	for _, s := range submissions {
		byID[ownerKey(s.domain, s.id)] = s
	}
	rows, err := db.Query(`SELECT domain_id,position,event_id,command_id,record,prefix_digest FROM authority_events ORDER BY domain_id,position`)
	if err != nil {
		return err
	}
	type projected struct{ domain, id, repo, locator, title, birth, command string }
	var expected []projected
	var domain, previousID string
	var position uint64
	prefix := sha256.Sum256([]byte("wipd/event-prefix/v1\x00"))
	for rows.Next() {
		var d, id, cmd, digest string
		var pos uint64
		var raw []byte
		if err = rows.Scan(&d, &pos, &id, &cmd, &raw, &digest); err != nil {
			break
		}
		if d != domain {
			domain = d
			previousID = ""
			position = 0
			prefix = sha256.Sum256([]byte("wipd/event-prefix/v1\x00"))
		}
		position++
		s, ok := byID[ownerKey(d, cmd)]
		if !ok || s.state != "terminal" || pos != position || !ulid.MatchString(id) || (previousID != "" && id <= previousID) {
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
			Acted       string `cbor:"acted_at"`
			Occurred    string `cbor:"occurred_at"`
			Environment struct {
				ID       string `cbor:"id"`
				Sequence uint64 `cbor:"sequence"`
			} `cbor:"environment"`
			Payload struct {
				ID      string `cbor:"id"`
				Locator string `cbor:"locator"`
				Title   string `cbor:"title"`
			} `cbor:"payload"`
		}
		if err = closedPayload(raw, &event, "schema", "event_id", "domain_id", "command_id", "request_hash", "kind", "subject_id", "repo_id", "acted_at", "occurred_at", "environment", "payload"); err != nil {
			break
		}
		var fields map[string]cbor.RawMessage
		_ = canonicalDecode(raw, &fields)
		var nested map[string]cbor.RawMessage
		if canonicalDecode(fields["environment"], &nested) != nil || !exactKeys(nested, "id", "sequence") {
			err = ErrInvalidStore
			break
		}
		nested = nil
		if canonicalDecode(fields["payload"], &nested) != nil || !exactKeys(nested, "id", "locator", "title") {
			err = ErrInvalidStore
			break
		}
		if event.Schema != "wipd.event/1" || event.ID != id || event.Domain != d || event.Command != cmd || event.Hash != s.hash || event.Kind != "matter.created" || event.Subject != event.Payload.ID || event.Environment.ID != s.env || event.Environment.Sequence != s.seq || event.Repo == "" || event.Acted == "" {
			err = ErrInvalidStore
			break
		}
		if _, e := utcTime(event.Occurred); e != nil {
			err = e
			break
		}
		var length [8]byte
		binary.BigEndian.PutUint64(length[:], uint64(len(raw)))
		h := sha256.New()
		_, _ = h.Write([]byte("wipd/event-prefix-step/v1\x00"))
		_, _ = h.Write(prefix[:])
		_, _ = h.Write(length[:])
		_, _ = h.Write(raw)
		copy(prefix[:], h.Sum(nil))
		if digest != digestRawBytes(prefix[:]) {
			err = ErrInvalidStore
			break
		}
		previousID = id
		var cmdFields map[string]cbor.RawMessage
		if err = canonicalDecode(s.command, &cmdFields); err != nil {
			break
		}
		var contextFields, inputFields map[string]cbor.RawMessage
		if err = canonicalDecode(cmdFields["context"], &contextFields); err != nil || !exactKeys(contextFields, "repo_id", "clone_id", "worktree_id") {
			err = ErrInvalidStore
			break
		}
		if err = canonicalDecode(cmdFields["input"], &inputFields); err != nil || !exactKeys(inputFields, "title", "requested_locator") {
			err = ErrInvalidStore
			break
		}
		var repo, title, locator, acted string
		if artifactDecoder.Unmarshal(contextFields["repo_id"], &repo) != nil || artifactDecoder.Unmarshal(inputFields["title"], &title) != nil || artifactDecoder.Unmarshal(inputFields["requested_locator"], &locator) != nil || artifactDecoder.Unmarshal(cmdFields["acted_at"], &acted) != nil {
			err = ErrInvalidStore
			break
		}
		if locator == "" {
			locator = matterLocator(title)
		}
		if repo != event.Repo || title != event.Payload.Title || locator != event.Payload.Locator || acted != event.Acted {
			err = ErrInvalidStore
			break
		}
		expected = append(expected, projected{d, event.Payload.ID, event.Repo, event.Payload.Locator, event.Payload.Title, id, cmd})
	}
	if err == nil {
		err = rows.Err()
	}
	_ = rows.Close()
	if err != nil {
		return err
	}
	var n int
	if err = db.QueryRow(`SELECT count(*) FROM matters`).Scan(&n); err != nil || n != len(expected) {
		return ErrInvalidStore
	}
	for _, p := range expected {
		var repo, locator, title, birth string
		if err = db.QueryRow(`SELECT repo_id,locator,title,birth_event_id FROM matters WHERE domain_id=? AND matter_id=?`, p.domain, p.id).Scan(&repo, &locator, &title, &birth); err != nil || repo != p.repo || locator != p.locator || title != p.title || birth != p.birth {
			return ErrInvalidStore
		}
		var receipt []byte
		if err = db.QueryRow(`SELECT receipt FROM terminal_receipts WHERE domain_id=? AND command_id=?`, p.domain, p.command).Scan(&receipt); err != nil {
			return ErrInvalidStore
		}
		r, e := readReceipt(receipt)
		if e != nil {
			return ErrInvalidStore
		}
		var output struct {
			ID      string `cbor:"id"`
			Locator string `cbor:"locator"`
			Title   string `cbor:"title"`
		}
		if e = closedPayload(r.Result.Output, &output, "id", "locator", "title"); e != nil || output.ID != p.id || output.Locator != p.locator || output.Title != p.title {
			return ErrInvalidStore
		}
	}
	rows, err = db.Query(`SELECT domain_id,environment_id,sequence_head FROM environments`)
	if err != nil {
		return err
	}
	type head struct {
		domain, env string
		seq         uint64
	}
	var heads []head
	for rows.Next() {
		var h head
		if err = rows.Scan(&h.domain, &h.env, &h.seq); err != nil {
			break
		}
		heads = append(heads, h)
	}
	if err == nil {
		err = rows.Err()
	}
	_ = rows.Close()
	if err != nil {
		return err
	}
	for _, h := range heads {
		var n, maximum uint64
		if err = db.QueryRow(`SELECT count(*),coalesce(max(environment_sequence),0) FROM submissions WHERE domain_id=? AND environment_id=? AND state='terminal'`, h.domain, h.env).Scan(&n, &maximum); err != nil || n != h.seq || maximum != h.seq {
			return ErrInvalidStore
		}
	}
	var invalidPending int
	if err = db.QueryRow(`SELECT count(*) FROM submissions s JOIN environments e USING(domain_id,environment_id) WHERE s.state='submitted' AND s.environment_sequence - 1 != e.sequence_head`).Scan(&invalidPending); err != nil || invalidPending != 0 {
		return ErrInvalidStore
	}
	return nil
}
