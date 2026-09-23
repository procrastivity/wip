package authoritystore

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/sha256"
	"crypto/tls"
	"database/sql"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/procrastivity/wip/internal/operation"
)

var (
	// ErrConflict refuses a reused domain command ID with different identity.
	ErrConflict = errors.New("authoritystore: command ID conflict")
	// ErrPending means the Environment sequence cannot advance past its head.
	ErrPending = errors.New("authoritystore: command pending or sequence blocked")
	// ErrNotOwner means this handle does not own the durable execution.
	ErrNotOwner = errors.New("authoritystore: execution owner unavailable")
)

// Execution is an unforgeable in-process owner for one durable submission. A
// recovered owner requires the exact retained command and a new writer lease.
type Execution struct {
	store   *Store
	command operation.Command
	hash    string
}

// CommandStatus carries either a pending acknowledgment and optional new owner,
// or the exact retained terminal receipt and signed wrapper.
type CommandStatus struct {
	Pending                bool
	Receipt, SignedReceipt []byte
	Owner                  *Execution
}

func ownerKey(domain, id string) string { return domain + "/" + id }

// SubmitCommand recomputes identity before any lookup. peer must be the actual
// completed TLS state supplied by the eventual authenticated adapter; neither
// actor text nor a forwarded certificate is proof of Environment identity.
func (s *Store) SubmitCommand(ctx context.Context, command operation.Command, asserted string, peer tls.ConnectionState, at time.Time) (CommandStatus, error) {
	var out CommandStatus
	if err := operation.VerifyRequestHash(command, asserted); err != nil {
		return out, err
	}
	encoded, err := command.CanonicalBytes()
	if err != nil {
		return out, err
	}
	if command.Request.Operation != operation.MatterCreateV1.Metadata().Operation || command.Request.Context.Repo == "" || command.Request.Context.Clone != "" || command.Request.Context.Worktree != "" {
		return out, ErrInvalidProof
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.db == nil {
		return out, errors.New("authoritystore: closed")
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return out, err
	}
	defer func() { _ = tx.Rollback() }()
	d, err := domainOwner(ctx, tx, command.AuthorityDomainID)
	if err != nil {
		return out, err
	}
	if d.ActiveEpoch != command.ExpectedAuthorityEpoch {
		return out, ErrFenced
	}
	if _, err = verifyPeer(ctx, tx, d.ID, command.EnvironmentID, d.ActiveEpoch, peer, at); err != nil {
		return out, err
	}
	// Authenticate the active Environment before revealing whether a command
	// ID exists or conflicts. A conflict discloses neither stored hash nor
	// payload, and is reserved for an admitted exchange.
	var priorHash string
	var priorBytes []byte
	err = tx.QueryRowContext(ctx, `SELECT request_hash,command FROM submissions WHERE domain_id=? AND command_id=?`, command.AuthorityDomainID, command.ID).Scan(&priorHash, &priorBytes)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return out, err
	}
	if err == nil && (priorHash != asserted || !bytes.Equal(priorBytes, encoded)) {
		return out, ErrConflict
	}
	if priorHash != "" { // replay never re-enters semantic guards
		return s.status(ctx, tx, command)
	}
	var member int
	if err = tx.QueryRowContext(ctx, `SELECT count(*) FROM repo_memberships WHERE repo_id=? AND domain_id=?`, command.Request.Context.Repo, d.ID).Scan(&member); err != nil {
		return out, err
	}
	if member != 1 {
		return out, ErrFenced
	}
	var head uint64
	if err = tx.QueryRowContext(ctx, `SELECT sequence_head FROM environments WHERE domain_id=? AND environment_id=?`, d.ID, command.EnvironmentID).Scan(&head); err != nil {
		return out, err
	}
	if command.EnvironmentSequence != head+1 {
		return out, ErrPending
	}
	if _, err = tx.ExecContext(ctx, `INSERT INTO submissions(domain_id,command_id,request_hash,command,epoch,environment_id,environment_sequence,operation_name,operation_version,state) VALUES(?,?,?,?,?,?,?,?,?,'submitted')`, d.ID, command.ID, asserted, encoded, d.ActiveEpoch, command.EnvironmentID, command.EnvironmentSequence, command.Request.Operation.Name, command.Request.Operation.Version); err != nil {
		return out, writeError(err)
	}
	if err = tx.Commit(); err != nil {
		return out, err
	}
	s.owners[ownerKey(d.ID, command.ID)] = true
	out.Pending = true
	out.Owner = &Execution{s, command, asserted}
	return out, nil
}

func (s *Store) status(ctx context.Context, tx *sql.Tx, command operation.Command) (CommandStatus, error) {
	var out CommandStatus
	err := tx.QueryRowContext(ctx, `SELECT receipt,wrapper FROM terminal_receipts WHERE domain_id=? AND command_id=?`, command.AuthorityDomainID, command.ID).Scan(&out.Receipt, &out.SignedReceipt)
	if errors.Is(err, sql.ErrNoRows) {
		out.Pending = true
		return out, nil
	}
	return out, err
}

// QueryCommand is a same-scope receipt query: absence means not found under
// this active epoch, not an assertion about a regressed predecessor history.
func (s *Store) QueryCommand(ctx context.Context, domain, id, hash string, epoch uint64, peer tls.ConnectionState, environment string, at time.Time) (CommandStatus, error) {
	var out CommandStatus
	if !ulid.MatchString(domain) || !ulid.MatchString(id) || !validDigest(hash) {
		return out, ErrInvalidProof
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.db == nil {
		return out, errors.New("authoritystore: closed")
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return out, err
	}
	defer func() { _ = tx.Rollback() }()
	d, err := domainOwner(ctx, tx, domain)
	if err != nil {
		return out, err
	}
	if d.ActiveEpoch != epoch {
		return out, ErrFenced
	}
	if _, err = verifyPeer(ctx, tx, domain, environment, epoch, peer, at); err != nil {
		return out, err
	}
	var stored, env string
	err = tx.QueryRowContext(ctx, `SELECT request_hash,environment_id FROM submissions WHERE domain_id=? AND command_id=?`, domain, id).Scan(&stored, &env)
	if errors.Is(err, sql.ErrNoRows) {
		return out, ErrNotFound
	}
	if err != nil {
		return out, err
	}
	if stored != hash {
		return out, ErrConflict
	}
	if env != environment {
		return out, ErrFenced
	}
	return s.status(ctx, tx, operation.Command{ID: id, AuthorityDomainID: domain})
}

// RecoverCommand may claim a pending submission only with exact original bytes
// under a new writer handle. A retry on the live handle never claims it.
func (s *Store) RecoverCommand(ctx context.Context, command operation.Command, hash string) (*Execution, error) {
	if err := operation.VerifyRequestHash(command, hash); err != nil {
		return nil, err
	}
	b, err := command.CanonicalBytes()
	if err != nil {
		return nil, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.db == nil {
		return nil, errors.New("authoritystore: closed")
	}
	key := ownerKey(command.AuthorityDomainID, command.ID)
	if s.owners[key] {
		return nil, ErrNotOwner
	}
	var stored []byte
	var storedHash, state string
	err = s.db.QueryRowContext(ctx, `SELECT command,request_hash,state FROM submissions WHERE domain_id=? AND command_id=?`, command.AuthorityDomainID, command.ID).Scan(&stored, &storedHash, &state)
	if err != nil {
		return nil, err
	}
	if hash != storedHash || !bytes.Equal(b, stored) {
		return nil, ErrConflict
	}
	if state != "submitted" {
		return nil, ErrNotOwner
	}
	s.owners[key] = true
	return &Execution{s, command, hash}, nil
}

// Signer holds the restricted authority private key outside SQLite. It signs
// exactly the artifact preimage; the store checks the resulting signature.
type Signer func(context.Context, []byte) ([]byte, error)

// CompleteCommand commits the sole currently supported typed fold. A semantic
// rejection/refusal/failure is no-effect; a successful matter.create@v1 must
// create one event and matching projection. No current version declares no-op.
func (s *Store) CompleteCommand(ctx context.Context, owner *Execution, result operation.Result, matterID, eventID string, occurred time.Time, sign Signer) (CommandStatus, error) {
	var out CommandStatus
	if owner == nil || owner.store != s || sign == nil {
		return out, ErrNotOwner
	}
	cmd := owner.command
	if err := operation.MatterCreateV1.ValidateResult(result); err != nil {
		return out, err
	}
	if occurred.IsZero() || (result.Code == operation.ResultSucceeded && (!ulid.MatchString(matterID) || !ulid.MatchString(eventID))) {
		return out, ErrInvalidProof
	}
	if result.Code != operation.ResultSucceeded && (matterID != "" || eventID != "") {
		return out, ErrInvalidProof
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.db == nil || !s.owners[ownerKey(cmd.AuthorityDomainID, cmd.ID)] {
		return out, ErrNotOwner
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return out, err
	}
	defer func() { _ = tx.Rollback() }()
	var state, storedHash string
	var stored []byte
	var head uint64
	err = tx.QueryRowContext(ctx, `SELECT s.state,s.request_hash,s.command,e.sequence_head FROM submissions s JOIN environments e USING(domain_id,environment_id) WHERE s.domain_id=? AND s.command_id=?`, cmd.AuthorityDomainID, cmd.ID).Scan(&state, &storedHash, &stored, &head)
	if err != nil {
		return out, err
	}
	canonical, err := cmd.CanonicalBytes()
	if err != nil {
		return out, err
	}
	if state != "submitted" || storedHash != owner.hash || !bytes.Equal(stored, canonical) {
		return out, ErrNotOwner
	}
	if cmd.EnvironmentSequence != head+1 {
		return out, ErrPending
	}
	d, err := domainOwner(ctx, tx, cmd.AuthorityDomainID)
	if err != nil {
		return out, err
	}
	if d.ActiveEpoch != cmd.ExpectedAuthorityEpoch {
		return out, ErrFenced
	}
	var first, last any
	var rangeValue any
	var output any
	var problem any
	if result.Code == operation.ResultSucceeded {
		input := cmd.Request.Input.(operation.MatterCreateInput)
		got := result.Output.(operation.MatterCreateOutput)
		locator := input.Locator
		if locator == "" {
			locator = matterLocator(input.Title)
		}
		if locator == "" || matterLocator(locator) != locator || got.ID != matterID || got.Locator != locator || got.Title != input.Title {
			return out, ErrInvalidProof
		}
		var n int
		if err = tx.QueryRowContext(ctx, `SELECT count(*) FROM matters WHERE domain_id=? AND repo_id=? AND locator=?`, d.ID, cmd.Request.Context.Repo, locator).Scan(&n); err != nil {
			return out, err
		}
		if n != 0 {
			return out, ErrFenced
		}
		output, err = artifactEncoder.Marshal(map[string]any{"id": got.ID, "locator": got.Locator, "title": got.Title})
		if err != nil {
			return out, err
		}
		var position uint64
		var previousID, previousDigest string
		err = tx.QueryRowContext(ctx, `SELECT position,event_id,prefix_digest FROM authority_events WHERE domain_id=? ORDER BY position DESC LIMIT 1`, d.ID).Scan(&position, &previousID, &previousDigest)
		if err != nil && !errors.Is(err, sql.ErrNoRows) {
			return out, err
		}
		if previousID != "" && eventID <= previousID {
			return out, ErrInvalidProof
		}
		position++
		record, e := artifactEncoder.Marshal(map[string]any{"schema": "wipd.event/1", "event_id": eventID, "domain_id": d.ID, "command_id": cmd.ID, "request_hash": owner.hash, "environment": map[string]any{"id": cmd.EnvironmentID, "sequence": cmd.EnvironmentSequence}, "acted_at": cmd.ActedAt, "occurred_at": occurred.UTC().Format(time.RFC3339Nano), "kind": "matter.created", "subject_id": matterID, "repo_id": cmd.Request.Context.Repo, "payload": map[string]any{"id": matterID, "locator": got.Locator, "title": got.Title}})
		if e != nil {
			return out, e
		}
		prefix := sha256.Sum256([]byte("wipd/event-prefix/v1\x00"))
		if previousDigest != "" {
			decoded, e := digestRaw(previousDigest)
			if e != nil {
				return out, e
			}
			copy(prefix[:], decoded)
		}
		var length [8]byte
		binary.BigEndian.PutUint64(length[:], uint64(len(record)))
		h := sha256.New()
		_, _ = h.Write([]byte("wipd/event-prefix-step/v1\x00"))
		_, _ = h.Write(prefix[:])
		_, _ = h.Write(length[:])
		_, _ = h.Write(record)
		if _, err = tx.ExecContext(ctx, `INSERT INTO authority_events VALUES(?,?,?,?,?,?)`, d.ID, position, eventID, cmd.ID, record, digestRawBytes(h.Sum(nil))); err != nil {
			return out, err
		}
		if _, err = tx.ExecContext(ctx, `INSERT INTO matters VALUES(?,?,?,?,?,?)`, d.ID, matterID, cmd.Request.Context.Repo, got.Locator, got.Title, eventID); err != nil {
			return out, writeError(err)
		}
		first, last = position, position
		rangeValue = map[string]any{"first_event_id": eventID, "last_event_id": eventID, "event_count": uint64(1)}
	} else {
		problem = string(result.Problem.Code)
	}
	receipt, err := artifactEncoder.Marshal(map[string]any{"schema": "wipd.terminal-receipt/1", "domain_id": d.ID, "authority_epoch": d.ActiveEpoch, "identity_schema": "wipd.command/1", "command_id": cmd.ID, "request_hash": owner.hash, "operation": map[string]any{"name": cmd.Request.Operation.Name, "version": uint64(cmd.Request.Operation.Version)}, "environment": map[string]any{"id": cmd.EnvironmentID, "sequence": cmd.EnvironmentSequence}, "result": map[string]any{"code": string(result.Code), "output": output, "problem_code": problem}, "accepted_events": rangeValue})
	if err != nil {
		return out, err
	}
	var generation, sequence uint64
	var keyID, before, after string
	var public, fence []byte
	err = tx.QueryRowContext(ctx, `SELECT generation,key_id,public_key,not_before,not_after,fence FROM artifact_keys WHERE domain_id=? AND epoch=? ORDER BY generation DESC LIMIT 1`, d.ID, d.ActiveEpoch).Scan(&generation, &keyID, &public, &before, &after, &fence)
	if err != nil {
		return out, err
	}
	if fence != nil || interval(before, after, occurred.UTC()) != nil {
		return out, ErrFenced
	}
	var predecessor sql.NullString
	err = tx.QueryRowContext(ctx, `SELECT sequence,digest FROM authority_artifacts WHERE domain_id=? AND epoch=? AND generation=? ORDER BY sequence DESC LIMIT 1`, d.ID, d.ActiveEpoch, generation).Scan(&sequence, &predecessor)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return out, err
	}
	sequence++
	var prev any
	var ptr *string
	if predecessor.Valid {
		prev = predecessor.String
		ptr = &predecessor.String
	}
	fields := map[string]any{"schema": "wipd.signed-artifact/1", "kind": "portable-receipt", "domain_id": d.ID, "authority_epoch": d.ActiveEpoch, "signer_role": "authority", "signer_key_id": keyID, "key_generation": generation, "artifact_sequence": sequence, "previous_artifact_digest": prev, "issued_at": occurred.UTC().Format(time.RFC3339Nano), "payload_schema": "wipd.terminal-receipt/1", "payload_digest": digestBytes(receipt), "payload": receipt}
	unsigned, err := artifactEncoder.Marshal(fields)
	if err != nil {
		return out, err
	}
	sig, err := sign(ctx, append([]byte("wipd/signed-artifact/v1\x00"), unsigned...))
	if err != nil {
		return out, err
	}
	if !ed25519.Verify(public, append([]byte("wipd/signed-artifact/v1\x00"), unsigned...), sig) {
		return out, ErrInvalidProof
	}
	fields["signature"] = sig
	wrapper, err := artifactEncoder.Marshal(fields)
	if err != nil {
		return out, err
	}
	digest, err := verifyAuthorityArtifact(wrapper, public, d.ID, keyID, d.ActiveEpoch, generation, sequence, ptr)
	if err != nil {
		return out, err
	}
	if _, err = tx.ExecContext(ctx, `INSERT INTO authority_artifacts VALUES(?,?,?,?,?,?,?)`, d.ID, d.ActiveEpoch, generation, sequence, digest, prev, wrapper); err != nil {
		return out, err
	}
	if _, err = tx.ExecContext(ctx, `INSERT INTO terminal_receipts VALUES(?,?,?,?,?,?,?,?,?,?)`, d.ID, cmd.ID, receipt, wrapper, d.ActiveEpoch, generation, sequence, first, last, string(result.Code)); err != nil {
		return out, err
	}
	if _, err = tx.ExecContext(ctx, `UPDATE submissions SET state='terminal' WHERE domain_id=? AND command_id=?`, d.ID, cmd.ID); err != nil {
		return out, err
	}
	if _, err = tx.ExecContext(ctx, `UPDATE environments SET sequence_head=? WHERE domain_id=? AND environment_id=? AND sequence_head=?`, cmd.EnvironmentSequence, d.ID, cmd.EnvironmentID, head); err != nil {
		return out, err
	}
	if err = tx.Commit(); err != nil {
		return out, err
	}
	delete(s.owners, ownerKey(d.ID, cmd.ID))
	out.Receipt, out.SignedReceipt = receipt, wrapper
	return out, nil
}

func digestRaw(value string) ([]byte, error) {
	if !validDigest(value) {
		return nil, ErrInvalidStore
	}
	return hex.DecodeString(value[7:])
}
func digestRawBytes(value []byte) string { return fmt.Sprintf("sha256:%x", value) }

// matterLocator preserves matter.create@v1's title-derived locator semantics
// without importing the M1 write surface or changing its CLI ownership.
func matterLocator(value string) string {
	var b strings.Builder
	prevDash := true
	for _, r := range strings.ToLower(value) {
		if r >= 'a' && r <= 'z' || r >= '0' && r <= '9' {
			b.WriteRune(r)
			prevDash = false
		} else if !prevDash {
			b.WriteByte('-')
			prevDash = true
		}
	}
	return strings.TrimSuffix(b.String(), "-")
}
