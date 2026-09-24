package authoritystore

import (
	"bytes"
	"context"
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
	store     *Store
	command   operation.Command
	lifecycle *lifecycleCommand
	hash      string
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
	return s.submitIdentity(ctx, commandIdentity{command.AuthorityDomainID, command.ExpectedAuthorityEpoch, command.EnvironmentID, command.EnvironmentSequence, command.ID, command.Request.Operation.Name, uint64(command.Request.Operation.Version), command.Request.Context.Repo, encoded, asserted, &command, nil}, peer, at, nil)
}

type commandIdentity struct {
	domain      string
	epoch       uint64
	environment string
	sequence    uint64
	id, name    string
	version     uint64
	repo        string
	encoded     []byte
	hash        string
	m1          *operation.Command
	lifecycle   *lifecycleCommand
}

// before runs after authentication and replay detection but before the durable
// submission point. It may only read the caller-owned transaction.
func (s *Store) submitIdentity(ctx context.Context, c commandIdentity, peer tls.ConnectionState, at time.Time, before func(*sql.Tx) error) (CommandStatus, error) {
	var out CommandStatus
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
	d, err := domainOwner(ctx, tx, c.domain)
	if err != nil {
		return out, err
	}
	if d.ActiveEpoch != c.epoch {
		return out, ErrFenced
	}
	if _, err = verifyPeer(ctx, tx, d.ID, c.environment, d.ActiveEpoch, peer, at); err != nil {
		return out, err
	}
	// Authenticate the active Environment before revealing whether a command
	// ID exists or conflicts. A conflict discloses neither stored hash nor
	// payload, and is reserved for an admitted exchange.
	var priorHash string
	var priorBytes []byte
	err = tx.QueryRowContext(ctx, `SELECT request_hash,command FROM submissions WHERE domain_id=? AND command_id=?`, c.domain, c.id).Scan(&priorHash, &priorBytes)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return out, err
	}
	if err == nil && (priorHash != c.hash || !bytes.Equal(priorBytes, c.encoded)) {
		return out, ErrConflict
	}
	if priorHash != "" { // replay never re-enters semantic guards
		return s.status(ctx, tx, operation.Command{ID: c.id, AuthorityDomainID: c.domain})
	}
	var admissionClosed int
	var closedEpoch sql.NullInt64
	if err = tx.QueryRowContext(ctx, `SELECT admission_closed,closed_epoch FROM authority_continuity WHERE domain_id=?`, d.ID).Scan(&admissionClosed, &closedEpoch); err != nil {
		return out, err
	}
	if admissionClosed != 0 && closedEpoch.Valid && uint64(closedEpoch.Int64) == d.ActiveEpoch {
		return out, ErrFenced
	}
	var migrationAuthorizationPending int
	if err = tx.QueryRowContext(ctx, `SELECT count(*) FROM continuity_products a WHERE a.domain_id=? AND a.authority_epoch=? AND a.kind='migration-authorization' AND NOT EXISTS(SELECT 1 FROM continuity_products z WHERE z.domain_id=a.domain_id AND z.authority_epoch=a.authority_epoch AND z.kind='migration-seal')`, d.ID, d.ActiveEpoch).Scan(&migrationAuthorizationPending); err != nil {
		return out, err
	}
	if migrationAuthorizationPending != 0 {
		return out, ErrFenced
	}
	if before != nil {
		if err = before(tx); err != nil {
			return out, err
		}
	}
	var member int
	if err = tx.QueryRowContext(ctx, `SELECT count(*) FROM repo_memberships WHERE repo_id=? AND domain_id=?`, c.repo, d.ID).Scan(&member); err != nil {
		return out, err
	}
	if member != 1 {
		return out, ErrFenced
	}
	var head uint64
	if err = tx.QueryRowContext(ctx, `SELECT sequence_head FROM environments WHERE domain_id=? AND environment_id=?`, d.ID, c.environment).Scan(&head); err != nil {
		return out, err
	}
	if c.sequence != head+1 {
		return out, ErrPending
	}
	if _, err = tx.ExecContext(ctx, `INSERT INTO submissions(domain_id,command_id,request_hash,command,epoch,environment_id,environment_sequence,operation_name,operation_version,state) VALUES(?,?,?,?,?,?,?,?,?,'submitted')`, d.ID, c.id, c.hash, c.encoded, d.ActiveEpoch, c.environment, c.sequence, c.name, c.version); err != nil {
		return out, writeError(err)
	}
	if err = tx.Commit(); err != nil {
		return out, err
	}
	s.owners[ownerKey(d.ID, c.id)] = true
	out.Pending = true
	out.Owner = &Execution{store: s, hash: c.hash, lifecycle: c.lifecycle}
	if c.m1 != nil {
		out.Owner.command = *c.m1
	}
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
	var active uint64
	if err = s.db.QueryRowContext(ctx, `SELECT active_epoch FROM domains WHERE domain_id=?`, command.AuthorityDomainID).Scan(&active); err != nil {
		return nil, err
	}
	if active != command.ExpectedAuthorityEpoch {
		return nil, ErrFenced
	}
	s.owners[key] = true
	return &Execution{store: s, command: command, hash: hash}, nil
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
		position, e := appendCommandEvent(ctx, tx, eventIdentity{d.ID, cmd.ID, owner.hash, cmd.EnvironmentID, cmd.EnvironmentSequence, cmd.ActedAt, cmd.Request.Context.Repo}, occurred, eventID, "matter.created", matterID, map[string]any{"id": matterID, "locator": got.Locator, "title": got.Title})
		if e != nil {
			return out, e
		}
		if _, err = tx.ExecContext(ctx, `INSERT INTO matters VALUES(?,?,?,?,?,?)`, d.ID, matterID, cmd.Request.Context.Repo, got.Locator, got.Title, eventID); err != nil {
			return out, writeError(err)
		}
		first, last = position, position
		rangeValue = map[string]any{"first_event_id": eventID, "last_event_id": eventID, "event_count": uint64(1)}
	} else {
		problem = string(result.Problem.Code)
	}
	return s.finishCommandTx(ctx, tx, commandIdentity{domain: d.ID, epoch: d.ActiveEpoch, environment: cmd.EnvironmentID, sequence: cmd.EnvironmentSequence, id: cmd.ID, name: cmd.Request.Operation.Name, version: uint64(cmd.Request.Operation.Version), hash: owner.hash}, head, string(result.Code), output, problem, rangeValue, first, last, occurred, sign, nil)
}

type eventIdentity struct {
	domain, id, hash, environment string
	sequence                      uint64
	actedAt, repo                 string
}

func appendCommandEvent(ctx context.Context, tx *sql.Tx, c eventIdentity, occurred time.Time, id, kind, subject string, payload map[string]any) (uint64, error) {
	var position uint64
	var previousID, previousDigest string
	err := tx.QueryRowContext(ctx, `SELECT position,event_id,prefix_digest FROM authority_events WHERE domain_id=? ORDER BY position DESC LIMIT 1`, c.domain).Scan(&position, &previousID, &previousDigest)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return 0, err
	}
	if !ulid.MatchString(id) || (previousID != "" && id <= previousID) {
		return 0, ErrInvalidProof
	}
	position++
	record, err := artifactEncoder.Marshal(map[string]any{"schema": "wipd.event/1", "event_id": id, "domain_id": c.domain, "command_id": c.id, "request_hash": c.hash, "environment": map[string]any{"id": c.environment, "sequence": c.sequence}, "acted_at": c.actedAt, "occurred_at": occurred.UTC().Format(time.RFC3339Nano), "kind": kind, "subject_id": subject, "repo_id": c.repo, "payload": payload})
	if err != nil {
		return 0, err
	}
	prefix := sha256.Sum256([]byte("wipd/event-prefix/v1\x00"))
	if previousDigest != "" {
		decoded, e := digestRaw(previousDigest)
		if e != nil {
			return 0, e
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
	_, err = tx.ExecContext(ctx, `INSERT INTO authority_events VALUES(?,?,?,?,?,?)`, c.domain, position, id, c.id, record, digestRawBytes(h.Sum(nil)))
	return position, err
}

func (s *Store) finishCommandTx(ctx context.Context, tx *sql.Tx, c commandIdentity, head uint64, code string, output, problem, rangeValue, first, last any, occurred time.Time, sign Signer, beforeCommit func([]byte, []byte, uint64, uint64) error) (CommandStatus, error) {
	var out CommandStatus
	receipt, err := artifactEncoder.Marshal(map[string]any{"schema": "wipd.terminal-receipt/1", "domain_id": c.domain, "authority_epoch": c.epoch, "identity_schema": "wipd.command/1", "command_id": c.id, "request_hash": c.hash, "operation": map[string]any{"name": c.name, "version": c.version}, "environment": map[string]any{"id": c.environment, "sequence": c.sequence}, "result": map[string]any{"code": code, "output": output, "problem_code": problem}, "accepted_events": rangeValue})
	if err != nil {
		return out, err
	}
	appended, err := appendSignedArtifactTx(ctx, tx, c.domain, c.epoch, "portable-receipt", "wipd.terminal-receipt/1", receipt, occurred, sign)
	if err != nil {
		return out, err
	}
	if _, err = tx.ExecContext(ctx, `INSERT INTO terminal_receipts VALUES(?,?,?,?,?,?,?,?,?,?)`, c.domain, c.id, receipt, appended.Wrapper, c.epoch, appended.Generation, appended.Sequence, first, last, code); err != nil {
		return out, err
	}
	if _, err = tx.ExecContext(ctx, `UPDATE submissions SET state='terminal' WHERE domain_id=? AND command_id=?`, c.domain, c.id); err != nil {
		return out, err
	}
	if _, err = tx.ExecContext(ctx, `UPDATE environments SET sequence_head=? WHERE domain_id=? AND environment_id=? AND sequence_head=?`, c.sequence, c.domain, c.environment, head); err != nil {
		return out, err
	}
	if beforeCommit != nil {
		if err = beforeCommit(receipt, appended.Wrapper, appended.Generation, appended.Sequence); err != nil {
			return out, err
		}
	}
	if err = tx.Commit(); err != nil {
		return out, err
	}
	delete(s.owners, ownerKey(c.domain, c.id))
	out.Receipt, out.SignedReceipt = receipt, appended.Wrapper
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
