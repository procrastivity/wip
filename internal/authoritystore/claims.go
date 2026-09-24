package authoritystore

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/sha256"
	"crypto/tls"
	"database/sql"
	"encoding/binary"
	"errors"
	"fmt"
	"time"

	"github.com/fxamacker/cbor/v2"
	"golang.org/x/text/unicode/norm"
)

// lifecycleCommand is the protocol-1 command identity, independent of the M1
// operation catalogue. The exact received bytes are retained; no field is
// normalized or reconstructed before hashing or submission.
type lifecycleCommand struct {
	commandIdentity
	actedAt, worktree, clone, matter, dispatch, mode string
	claimID                                          string
	claimEpoch                                       uint64
	repair                                           *repairInput
	barrier                                          *journalBarrier
	stand                                            *standDownInput
	ownerNonce                                       string
}

type claimRef struct {
	ID    string `cbor:"id"`
	Epoch uint64 `cbor:"epoch"`
}
type repairProof struct {
	Receipt      cbor.RawMessage `cbor:"terminal_receipt"`
	NotSubmitted *string         `cbor:"not_submitted_proof"`
}
type repairAction struct {
	Kind        string      `cbor:"kind"`
	Proof       repairProof `cbor:"proof"`
	Replacement []byte      `cbor:"replacement_canonical_command"`
	Hash        string      `cbor:"replacement_request_hash"`
}
type repairInput struct {
	Journal  string       `cbor:"journal_id"`
	Position uint64       `cbor:"head_position"`
	HeadID   string       `cbor:"head_command_id"`
	HeadHash string       `cbor:"head_request_hash"`
	Action   repairAction `cbor:"action"`
}
type journalBarrier struct {
	Schema      string   `cbor:"schema"`
	Journal     string   `cbor:"journal_id"`
	Claim       claimRef `cbor:"claim"`
	Count       uint64   `cbor:"entry_count"`
	Last        uint64   `cbor:"last_position"`
	Receipts    uint64   `cbor:"terminal_receipt_count"`
	Digest      string   `cbor:"entries_digest"`
	Sealed      bool     `cbor:"sealed"`
	Unresolved  uint64   `cbor:"unresolved_count"`
	Quarantined uint64   `cbor:"quarantined_count"`
}
type standDownInput struct {
	Target struct {
		ClaimID string `cbor:"claim_id"`
		Epoch   uint64 `cbor:"claim_epoch"`
		Owner   string `cbor:"owner_environment_id"`
	} `cbor:"target"`
	Reason string `cbor:"reason"`
	Loss   bool   `cbor:"acknowledge_unreturned_work_loss"`
}

func closedMap(raw []byte, keys ...string) (map[string]cbor.RawMessage, error) {
	var fields map[string]cbor.RawMessage
	if canonicalDecode(raw, &fields) != nil || !exactKeys(fields, keys...) {
		return nil, ErrInvalidProof
	}
	return fields, nil
}

func parseLifecycle(raw []byte, hash string) (*lifecycleCommand, error) {
	if len(raw) == 0 || len(raw) > 1<<20 || !validDigest(hash) || digestBytes(append([]byte("wipd/request-hash/v1\x00"), raw...)) != hash {
		return nil, ErrInvalidProof
	}
	fields, err := closedMap(raw, "schema", "command_id", "authority", "environment", "acted_at", "actor", "causation_command_id", "correlation_command_id", "operation", "context", "claim", "input", "blobs")
	if err != nil {
		return nil, err
	}
	for _, part := range []struct {
		key   string
		names []string
	}{
		{"authority", []string{"domain_id", "expected_epoch"}},
		{"environment", []string{"id", "sequence"}},
		{"operation", []string{"name", "version"}},
		{"context", []string{"repo_id", "clone_id", "worktree_id"}},
	} {
		if _, err = closedMap(fields[part.key], part.names...); err != nil {
			return nil, err
		}
	}
	// Decode into explicit wire tags; Go field names do not imply snake_case.
	var w struct {
		Schema      string  `cbor:"schema"`
		ID          string  `cbor:"command_id"`
		ActedAt     string  `cbor:"acted_at"`
		Actor       string  `cbor:"actor"`
		Causation   *string `cbor:"causation_command_id"`
		Correlation string  `cbor:"correlation_command_id"`
		Authority   struct {
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
		Context struct {
			Repo     *string `cbor:"repo_id"`
			Clone    *string `cbor:"clone_id"`
			Worktree *string `cbor:"worktree_id"`
		} `cbor:"context"`
		Claim *struct {
			ID    string `cbor:"id"`
			Epoch uint64 `cbor:"epoch"`
		} `cbor:"claim"`
		Blobs []cbor.RawMessage `cbor:"blobs"`
	}
	if err = artifactDecoder.Unmarshal(raw, &w); err != nil {
		return nil, ErrInvalidProof
	}
	if w.Schema != "wipd.command/1" || !ulid.MatchString(w.ID) || !ulid.MatchString(w.Authority.Domain) || w.Authority.Epoch == 0 || !ulid.MatchString(w.Environment.ID) || w.Environment.Sequence == 0 || !ulid.MatchString(w.Correlation) || (w.Causation == nil && w.Correlation != w.ID) || (w.Causation != nil && (!ulid.MatchString(*w.Causation) || *w.Causation == w.ID)) || w.Actor == "" || !norm.NFC.IsNormalString(w.Actor) || w.Operation.Version != 1 || !bytes.Equal(fields["blobs"], []byte{0x80}) {
		return nil, ErrInvalidProof
	}
	if t, e := utcTime(w.ActedAt); e != nil || t.Format(time.RFC3339Nano) != w.ActedAt {
		return nil, ErrInvalidProof
	}
	if w.Context.Repo == nil || !ulid.MatchString(*w.Context.Repo) {
		return nil, ErrInvalidProof
	}
	c := &lifecycleCommand{commandIdentity: commandIdentity{domain: w.Authority.Domain, epoch: w.Authority.Epoch, environment: w.Environment.ID, sequence: w.Environment.Sequence, id: w.ID, name: w.Operation.Name, version: 1, repo: *w.Context.Repo, encoded: append([]byte(nil), raw...), hash: hash}, actedAt: w.ActedAt}
	if w.Context.Clone != nil {
		c.clone = *w.Context.Clone
		if !ulid.MatchString(c.clone) {
			return nil, ErrInvalidProof
		}
	}
	if w.Context.Worktree != nil {
		c.worktree = *w.Context.Worktree
		if !ulid.MatchString(c.worktree) {
			return nil, ErrInvalidProof
		}
	}
	if w.Claim != nil {
		c.claimID, c.claimEpoch = w.Claim.ID, w.Claim.Epoch
		if !ulid.MatchString(c.claimID) || c.claimEpoch == 0 {
			return nil, ErrInvalidProof
		}
		if _, err = closedMap(fields["claim"], "id", "epoch"); err != nil {
			return nil, err
		}
	}
	switch c.name {
	case "claim.acquire":
		if w.Claim != nil || c.worktree == "" || c.clone == "" {
			return nil, ErrInvalidProof
		}
		if _, err = closedMap(fields["input"], "matter_id", "worktree_id", "dispatch_mode", "requested_dispatch_id"); err != nil {
			return nil, err
		}
		var in struct {
			Matter   string `cbor:"matter_id"`
			Worktree string `cbor:"worktree_id"`
			Mode     string `cbor:"dispatch_mode"`
			Dispatch string `cbor:"requested_dispatch_id"`
		}
		if artifactDecoder.Unmarshal(fields["input"], &in) != nil || !ulid.MatchString(in.Matter) || !ulid.MatchString(in.Dispatch) || in.Mode != "anonymous-matter" || in.Worktree != c.worktree {
			return nil, ErrInvalidProof
		}
		c.matter, c.dispatch, c.mode = in.Matter, in.Dispatch, in.Mode
	case "claim.journal-repair", "claim.release":
		if w.Claim == nil || c.worktree == "" || c.clone == "" {
			return nil, ErrInvalidProof
		}
		if c.name == "claim.release" {
			f, e := closedMap(fields["input"], "barrier")
			if e != nil {
				return nil, e
			}
			if _, e = closedMap(f["barrier"], "schema", "journal_id", "claim", "entry_count", "last_position", "terminal_receipt_count", "entries_digest", "sealed", "unresolved_count", "quarantined_count"); e != nil {
				return nil, e
			}
			var b journalBarrier
			if artifactDecoder.Unmarshal(f["barrier"], &b) != nil || b.Schema != "wipd.journal-barrier/1" || !ulid.MatchString(b.Journal) || b.Claim != (claimRef{c.claimID, c.claimEpoch}) || !validDigest(b.Digest) {
				return nil, ErrInvalidProof
			}
			if _, e = closedMap(mustRaw(f["barrier"], "claim"), "id", "epoch"); e != nil {
				return nil, e
			}
			c.barrier = &b
		} else {
			f, e := closedMap(fields["input"], "journal_id", "head_position", "head_command_id", "head_request_hash", "action")
			if e != nil {
				return nil, e
			}
			a, e := closedMap(f["action"], "kind", "proof")
			if e != nil {
				a, e = closedMap(f["action"], "kind", "proof", "replacement_canonical_command", "replacement_request_hash")
				if e != nil {
					return nil, e
				}
			}
			if _, e = closedMap(a["proof"], "terminal_receipt", "not_submitted_proof"); e != nil {
				return nil, e
			}
			var r repairInput
			if artifactDecoder.Unmarshal(fields["input"], &r) != nil || !ulid.MatchString(r.Journal) || r.Position == 0 || !ulid.MatchString(r.HeadID) || !validDigest(r.HeadHash) || (r.Action.Kind != "abandon" && r.Action.Kind != "replace") || (r.Action.Kind == "abandon" && len(a) != 2) || (r.Action.Kind == "replace" && (len(a) != 4 || !validDigest(r.Action.Hash))) {
				return nil, ErrInvalidProof
			}
			c.repair = &r
		}
	case "claim.stand-down":
		if w.Claim != nil || c.clone != "" || c.worktree != "" {
			return nil, ErrInvalidProof
		}
		f, e := closedMap(fields["input"], "target", "reason", "acknowledge_unreturned_work_loss")
		if e != nil {
			return nil, e
		}
		if _, e = closedMap(f["target"], "claim_id", "claim_epoch", "owner_environment_id"); e != nil {
			return nil, e
		}
		var in standDownInput
		if artifactDecoder.Unmarshal(fields["input"], &in) != nil || !ulid.MatchString(in.Target.ClaimID) || in.Target.Epoch == 0 || !ulid.MatchString(in.Target.Owner) || !norm.NFC.IsNormalString(in.Reason) {
			return nil, ErrInvalidProof
		}
		c.stand = &in
	default:
		return nil, ErrInvalidProof
	}
	c.lifecycle = c
	return c, nil
}

func mustRaw(raw []byte, key string) []byte {
	var m map[string]cbor.RawMessage
	_ = artifactDecoder.Unmarshal(raw, &m)
	return m[key]
}

// SubmitClaimLifecycle admits repair, release, or stand-down. Stand-down's
// signed owner authorization is an exchange proof, not part of the command.
func (s *Store) SubmitClaimLifecycle(ctx context.Context, canonical []byte, hash string, peer tls.ConnectionState, at time.Time, ownerAuthorization []byte) (CommandStatus, error) {
	c, err := parseLifecycle(canonical, hash)
	if err != nil || c.name == "claim.acquire" {
		return CommandStatus{}, ErrInvalidProof
	}
	if c.name != "claim.stand-down" {
		if len(ownerAuthorization) != 0 {
			return CommandStatus{}, ErrInvalidProof
		}
		return s.submitIdentity(ctx, c.commandIdentity, peer, at, func(tx *sql.Tx) error {
			x, err := loadClaim(ctx, tx, c, c.claimID)
			if errors.Is(err, sql.ErrNoRows) {
				return ErrFenced
			}
			if err != nil {
				return err
			}
			if x.closed.Valid || x.authority != c.epoch || x.epoch != c.claimEpoch || x.owner != c.environment || x.repo != c.repo || x.worktree != c.worktree {
				return ErrFenced
			}
			return nil
		})
	}
	if len(ownerAuthorization) == 0 {
		return CommandStatus{}, ErrInvalidProof
	}
	return s.submitIdentity(ctx, c.commandIdentity, peer, at, func(tx *sql.Tx) error {
		if err := verifyStandDownAuthorization(ctx, tx, c, ownerAuthorization, at); err != nil {
			return err
		}
		_, err := tx.ExecContext(ctx, `INSERT INTO claim_stand_down_proofs VALUES(?,?,?,?,?)`, c.domain, c.id, c.ownerNonce, ownerAuthorization, at.UTC().Format(time.RFC3339Nano))
		return writeError(err)
	})
}

func verifyStandDownAuthorization(ctx context.Context, tx *sql.Tx, c *lifecycleCommand, ownerAuthorization []byte, at time.Time) error {
	d, e := domainOwner(ctx, tx, c.domain)
	if e != nil {
		return e
	}
	payload, e := ownerArtifact(ownerAuthorization, d.OwnerPublicKey, c.domain, d.OwnerKeyID, "owner-attestation", "wipd.owner-attestation/1", c.epoch)
	if e != nil {
		return ErrInvalidProof
	}
	// Explicit wire tags are required for the attestation's snake_case keys.
	var a struct {
		Schema        string  `cbor:"schema"`
		Action        string  `cbor:"action"`
		Domain        string  `cbor:"domain_id"`
		Current       uint64  `cbor:"current_epoch"`
		Next          *uint64 `cbor:"next_epoch"`
		SubjectSchema string  `cbor:"subject_schema"`
		SubjectDigest string  `cbor:"subject_digest"`
		Subject       []byte  `cbor:"subject"`
		Nonce         []byte  `cbor:"nonce"`
		Issued        string  `cbor:"issued_at"`
		Expires       string  `cbor:"expires_at"`
		Loss          bool    `cbor:"loss_accepted"`
	}
	if closedPayload(payload, &a, "schema", "action", "domain_id", "current_epoch", "next_epoch", "subject_schema", "subject_digest", "subject", "nonce", "issued_at", "expires_at", "loss_accepted") != nil || a.Schema != "wipd.owner-attestation/1" || a.Action != "claim-stand-down" || a.Domain != c.domain || a.Current != c.epoch || a.Next != nil || a.SubjectSchema != "wipd.claim-stand-down-subject/1" || a.SubjectDigest != digestBytes(a.Subject) || len(a.Nonce) != 16 || !a.Loss || interval(a.Issued, a.Expires, at.UTC()) != nil {
		return ErrInvalidProof
	}
	issued, _ := utcTime(a.Issued)
	expires, _ := utcTime(a.Expires)
	if expires.Sub(issued) > 10*time.Minute {
		return ErrInvalidProof
	}
	var sub struct {
		Schema  string `cbor:"schema"`
		Command string `cbor:"command_id"`
		Hash    string `cbor:"request_hash"`
		Claim   string `cbor:"claim_id"`
		Epoch   uint64 `cbor:"claim_epoch"`
		Owner   string `cbor:"owner_environment_id"`
		Acting  string `cbor:"acting_environment_id"`
		Reason  string `cbor:"reason_digest"`
	}
	if closedPayload(a.Subject, &sub, "schema", "command_id", "request_hash", "claim_id", "claim_epoch", "owner_environment_id", "acting_environment_id", "reason_digest") != nil || sub.Schema != "wipd.claim-stand-down-subject/1" || sub.Command != c.id || sub.Hash != c.hash || sub.Claim != c.stand.Target.ClaimID || sub.Epoch != c.stand.Target.Epoch || sub.Owner != c.stand.Target.Owner || sub.Acting != c.environment || sub.Reason != digestBytes([]byte(c.stand.Reason)) {
		return ErrInvalidProof
	}
	c.ownerNonce = fmt.Sprintf("%x", a.Nonce)
	var used int
	if e = tx.QueryRowContext(ctx, `SELECT count(*) FROM claim_stand_down_proofs WHERE owner_nonce=? AND (domain_id!=? OR command_id!=?)`, c.ownerNonce, c.domain, c.id).Scan(&used); e != nil {
		return e
	}
	if used != 0 {
		return ErrFenced
	}
	var priorAction, priorDigest string
	e = tx.QueryRowContext(ctx, `SELECT action,artifact_digest FROM owner_nonce_uses WHERE domain_id=? AND nonce=?`, c.domain, c.ownerNonce).Scan(&priorAction, &priorDigest)
	if e == nil {
		if priorAction != "claim-stand-down" || priorDigest != digestBytes(ownerAuthorization) {
			return ErrFenced
		}
	} else if !errors.Is(e, sql.ErrNoRows) {
		return e
	}
	return nil
}

// RecoverClaimLifecycle uses the exact proof admitted at submission and its
// original verification time; later expiration cannot revoke the submission.
func (s *Store) RecoverClaimLifecycle(ctx context.Context, raw []byte, hash string, ownerAuthorization ...[]byte) (*Execution, error) {
	c, err := parseLifecycle(raw, hash)
	if err != nil || c.name == "claim.acquire" {
		return nil, ErrInvalidProof
	}
	if c.stand == nil {
		if len(ownerAuthorization) != 0 {
			return nil, ErrInvalidProof
		}
		return s.recoverLifecycle(ctx, c, raw, hash)
	}
	if len(ownerAuthorization) > 1 {
		return nil, ErrInvalidProof
	}
	var proof []byte
	if len(ownerAuthorization) == 1 {
		proof = ownerAuthorization[0]
	}
	return s.recoverLifecycleWithProof(ctx, c, raw, hash, proof)
}

// SubmitClaimAcquire validates the exact installed anchor before crossing the
// shared authenticated submission point. A replay bypasses the prefix guard.
func (s *Store) SubmitClaimAcquire(ctx context.Context, canonical []byte, hash string, installed PrefixAnchor, peer tls.ConnectionState, at time.Time) (CommandStatus, error) {
	c, err := parseLifecycle(canonical, hash)
	if err != nil || c.name != "claim.acquire" {
		return CommandStatus{}, ErrInvalidProof
	}
	return s.submitIdentity(ctx, c.commandIdentity, peer, at, func(tx *sql.Tx) error {
		a, e := anchorAt(ctx, tx, c.domain, installed.EventCount)
		if e != nil || !equalAnchor(a, installed) {
			return ErrPrefixMismatch
		}
		var eventID any
		if installed.EventID != "" {
			eventID = installed.EventID
		}
		_, e = tx.ExecContext(ctx, `INSERT INTO claim_acquire_intents VALUES(?,?,?,?,?)`, c.domain, c.id, installed.EventCount, eventID, installed.Digest)
		return e
	})
}

// RecoverClaimAcquire claims an abandoned pending execution without altering
// the immutable command or its domain-lifetime identity.
func (s *Store) RecoverClaimAcquire(ctx context.Context, raw []byte, hash string) (*Execution, error) {
	c, err := parseLifecycle(raw, hash)
	if err != nil || c.name != "claim.acquire" {
		return nil, ErrInvalidProof
	}
	return s.recoverLifecycle(ctx, c, raw, hash)
}

func (s *Store) recoverLifecycle(ctx context.Context, c *lifecycleCommand, raw []byte, hash string) (*Execution, error) {
	return s.recoverLifecycleWithProof(ctx, c, raw, hash, nil)
}

func (s *Store) recoverLifecycleWithProof(ctx context.Context, c *lifecycleCommand, raw []byte, hash string, proof []byte) (*Execution, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.db == nil || s.owners[ownerKey(c.domain, c.id)] {
		return nil, ErrNotOwner
	}
	var stored []byte
	var storedHash, state string
	err := s.db.QueryRowContext(ctx, `SELECT command,request_hash,state FROM submissions WHERE domain_id=? AND command_id=?`, c.domain, c.id).Scan(&stored, &storedHash, &state)
	if err != nil {
		return nil, err
	}
	if storedHash != hash || !bytes.Equal(stored, raw) {
		return nil, ErrConflict
	}
	if state != "submitted" {
		return nil, ErrNotOwner
	}
	var activeEpoch uint64
	if err = s.db.QueryRowContext(ctx, `SELECT active_epoch FROM domains WHERE domain_id=?`, c.domain).Scan(&activeEpoch); err != nil {
		return nil, err
	}
	if activeEpoch != c.epoch {
		return nil, ErrFenced
	}
	if c.stand != nil {
		tx, e := s.db.BeginTx(ctx, nil)
		if e != nil {
			return nil, e
		}
		defer func() { _ = tx.Rollback() }()
		var admitted []byte
		var verified string
		if e = tx.QueryRowContext(ctx, `SELECT authorization,verified_at FROM claim_stand_down_proofs WHERE domain_id=? AND command_id=?`, c.domain, c.id).Scan(&admitted, &verified); e != nil {
			return nil, e
		}
		if proof != nil && !bytes.Equal(proof, admitted) {
			return nil, ErrInvalidProof
		}
		at, e := utcTime(verified)
		if e != nil {
			return nil, ErrInvalidProof
		}
		if e = verifyStandDownAuthorization(ctx, tx, c, admitted, at); e != nil {
			return nil, e
		}
	}
	s.owners[ownerKey(c.domain, c.id)] = true
	return &Execution{store: s, lifecycle: c, hash: hash}, nil
}

type journalCommand struct {
	Domain, ID, Hash, Environment, Repo, Worktree, Claim string
	Epoch, ClaimEpoch, Sequence                          uint64
}

func parseJournalCommand(raw []byte, hash string) (journalCommand, error) {
	var c journalCommand
	if len(raw) == 0 || len(raw) > 1<<20 || !validDigest(hash) || digestBytes(append([]byte("wipd/request-hash/v1\x00"), raw...)) != hash {
		return c, ErrInvalidProof
	}
	f, err := closedMap(raw, "schema", "command_id", "authority", "environment", "acted_at", "actor", "causation_command_id", "correlation_command_id", "operation", "context", "claim", "input", "blobs")
	if err != nil {
		return c, err
	}
	for _, part := range []struct {
		key  string
		keys []string
	}{{"authority", []string{"domain_id", "expected_epoch"}}, {"environment", []string{"id", "sequence"}}, {"operation", []string{"name", "version"}}, {"context", []string{"repo_id", "clone_id", "worktree_id"}}, {"claim", []string{"id", "epoch"}}} {
		if _, err = closedMap(f[part.key], part.keys...); err != nil {
			return c, err
		}
	}
	var w struct {
		Schema      string            `cbor:"schema"`
		ID          string            `cbor:"command_id"`
		ActedAt     string            `cbor:"acted_at"`
		Actor       string            `cbor:"actor"`
		Correlation string            `cbor:"correlation_command_id"`
		Causation   *string           `cbor:"causation_command_id"`
		Blobs       []cbor.RawMessage `cbor:"blobs"`
		Authority   struct {
			Domain string `cbor:"domain_id"`
			Epoch  uint64 `cbor:"expected_epoch"`
		} `cbor:"authority"`
		Environment struct {
			ID       string `cbor:"id"`
			Sequence uint64 `cbor:"sequence"`
		} `cbor:"environment"`
		Claim     claimRef `cbor:"claim"`
		Operation struct {
			Name    string `cbor:"name"`
			Version uint64 `cbor:"version"`
		} `cbor:"operation"`
	}
	// Context requires explicit tags rather than Go's camel-case field names.
	var contextFields struct {
		Repo     string `cbor:"repo_id"`
		Clone    string `cbor:"clone_id"`
		Worktree string `cbor:"worktree_id"`
	}
	if artifactDecoder.Unmarshal(raw, &w) != nil || artifactDecoder.Unmarshal(f["context"], &contextFields) != nil || w.Schema != "wipd.command/1" || !ulid.MatchString(w.ID) || !ulid.MatchString(w.Authority.Domain) || w.Authority.Epoch == 0 || !ulid.MatchString(w.Environment.ID) || w.Environment.Sequence == 0 || !ulid.MatchString(contextFields.Repo) || !ulid.MatchString(contextFields.Clone) || !ulid.MatchString(contextFields.Worktree) || !ulid.MatchString(w.Claim.ID) || w.Claim.Epoch == 0 || w.Operation.Name == "" || w.Operation.Version == 0 || w.Actor == "" || !norm.NFC.IsNormalString(w.Actor) || !ulid.MatchString(w.Correlation) || (w.Causation == nil && w.Correlation != w.ID) || (w.Causation != nil && (!ulid.MatchString(*w.Causation) || *w.Causation == w.ID)) {
		return c, ErrInvalidProof
	}
	if w.Operation.Name == "claim.acquire" || w.Operation.Name == "claim.release" || w.Operation.Name == "claim.journal-repair" || w.Operation.Name == "claim.stand-down" {
		return c, ErrInvalidProof
	}
	if t, e := utcTime(w.ActedAt); e != nil || t.Format(time.RFC3339Nano) != w.ActedAt {
		return c, ErrInvalidProof
	}
	return journalCommand{w.Authority.Domain, w.ID, hash, w.Environment.ID, contextFields.Repo, contextFields.Worktree, w.Claim.ID, w.Authority.Epoch, w.Claim.Epoch, w.Environment.Sequence}, nil
}

// AppendClaimJournalEntry persists one exact command at the next position.
// This is an internal journal primitive, not claim-delivery admission: the
// trusted caller must check operation delivery, negotiation, footprint, and
// pins before calling it. No claim-delivery operation is registered yet.
func (s *Store) AppendClaimJournalEntry(ctx context.Context, journal string, position uint64, raw []byte, hash string) error {
	c, err := parseJournalCommand(raw, hash)
	if err != nil || !ulid.MatchString(journal) || position == 0 {
		return ErrInvalidProof
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.db == nil {
		return ErrInvalidStore
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	var owner, worktree, repo, state string
	var claimEpoch, authorityEpoch uint64
	var closed sql.NullString
	err = tx.QueryRowContext(ctx, `SELECT c.owner_environment_id,c.worktree_id,m.repo_id,j.state,c.claim_epoch,c.authority_epoch,c.close_command_id FROM claim_journals j JOIN claims c USING(domain_id,claim_id) JOIN matters m ON m.domain_id=c.domain_id AND m.matter_id=c.matter_id JOIN domains d ON d.domain_id=c.domain_id WHERE j.domain_id=? AND j.journal_id=? AND j.claim_id=? AND c.authority_epoch=d.active_epoch`, c.Domain, journal, c.Claim).Scan(&owner, &worktree, &repo, &state, &claimEpoch, &authorityEpoch, &closed)
	if err != nil {
		return err
	}
	if state != "open" || closed.Valid || c.Environment != owner || c.Repo != repo || c.Worktree != worktree || c.Epoch != authorityEpoch || c.ClaimEpoch != claimEpoch {
		return ErrFenced
	}
	var count, pending int
	var lastSeq uint64
	if err = tx.QueryRowContext(ctx, `SELECT count(*),COALESCE(max(environment_sequence),0),count(*) FILTER (WHERE state!='terminal' OR receipt IS NULL) FROM claim_journal_entries WHERE domain_id=? AND journal_id=?`, c.Domain, journal).Scan(&count, &lastSeq, &pending); err != nil {
		return err
	}
	if position != uint64(count)+1 || pending != 0 {
		return ErrPending
	}
	if count > 0 && c.Sequence != lastSeq+1 {
		return ErrPending
	}
	if count == 0 {
		var head uint64
		if err = tx.QueryRowContext(ctx, `SELECT sequence_head FROM environments WHERE domain_id=? AND environment_id=?`, c.Domain, c.Environment).Scan(&head); err != nil {
			return err
		}
		if c.Sequence != head+1 {
			return ErrPending
		}
	}
	var existing int
	if err = tx.QueryRowContext(ctx, `SELECT count(*) FROM claim_journal_entries WHERE domain_id=? AND command_id=?`, c.Domain, c.ID).Scan(&existing); err != nil {
		return err
	}
	if existing != 0 {
		return ErrConflict
	}
	if _, err = tx.ExecContext(ctx, `INSERT INTO claim_journal_entries(domain_id,journal_id,position,command_id,request_hash,command,environment_sequence,state) VALUES(?,?,?,?,?,?,?,'pending-return')`, c.Domain, journal, position, c.ID, hash, raw, c.Sequence); err != nil {
		return writeError(err)
	}
	return tx.Commit()
}

// AcknowledgeClaimJournalEntry requires the exact retained terminal receipt
// and complete installed prefix before allowing the next return.
func (s *Store) AcknowledgeClaimJournalEntry(ctx context.Context, domain, journal string, position uint64, receipt []byte, end PrefixAnchor) error {
	r, err := readReceipt(receipt)
	if err != nil {
		return ErrInvalidProof
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.db == nil {
		return ErrInvalidStore
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	var id, hash, state string
	var seq uint64
	var retained, command []byte
	var receiptEpoch, lastPosition uint64
	err = tx.QueryRowContext(ctx, `SELECT e.command_id,e.request_hash,e.state,e.environment_sequence,e.command,t.receipt,t.epoch,COALESCE(t.last_position,0) FROM claim_journal_entries e JOIN claim_journals j USING(domain_id,journal_id) JOIN terminal_receipts t ON t.domain_id=e.domain_id AND t.command_id=e.command_id WHERE e.domain_id=? AND e.journal_id=? AND e.position=? AND j.state='open'`, domain, journal, position).Scan(&id, &hash, &state, &seq, &command, &retained, &receiptEpoch, &lastPosition)
	if err != nil {
		return err
	}
	if state != "pending-return" && state != "unknown" {
		return ErrPending
	}
	c, parseErr := parseJournalCommand(command, hash)
	if parseErr != nil || !bytes.Equal(retained, receipt) || r.Domain != domain || r.ID != id || r.Hash != hash || r.Environment.ID != c.Environment || r.Environment.Sequence != seq || r.Epoch != receiptEpoch || c.Epoch != receiptEpoch || c.ID != id || c.Sequence != seq || (r.Result.Code != "result.succeeded" && r.Result.Code != "result.rejected" && r.Result.Code != "result.refused" && r.Result.Code != "result.failed") {
		return ErrInvalidProof
	}
	if position > 1 {
		var preceding string
		if err = tx.QueryRowContext(ctx, `SELECT state FROM claim_journal_entries WHERE domain_id=? AND journal_id=? AND position=?`, domain, journal, position-1).Scan(&preceding); err != nil {
			return err
		}
		if preceding != "terminal" {
			return ErrPending
		}
	}
	anchor, err := anchorAt(ctx, tx, domain, end.EventCount)
	if err != nil || !equalAnchor(anchor, end) || end.EventCount < lastPosition {
		return ErrPrefixMismatch
	}
	entryState := "terminal"
	if r.Result.Code != "result.succeeded" {
		entryState = "quarantined"
	}
	if _, err = tx.ExecContext(ctx, `UPDATE claim_journal_entries SET state=?,receipt=?,installed_end_count=?,installed_end_digest=? WHERE domain_id=? AND journal_id=? AND position=?`, entryState, receipt, end.EventCount, end.Digest, domain, journal, position); err != nil {
		return err
	}
	return tx.Commit()
}

// SealClaimJournal freezes the current generation only after every entry has
// an exact successful terminal acknowledgment. Repair archives failed heads.
func (s *Store) SealClaimJournal(ctx context.Context, domain, journal string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.db == nil {
		return ErrInvalidStore
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	var state string
	var closed sql.NullString
	err = tx.QueryRowContext(ctx, `SELECT j.state,c.close_command_id FROM claim_journals j JOIN claims c USING(domain_id,claim_id) JOIN domains d ON d.domain_id=c.domain_id WHERE j.domain_id=? AND j.journal_id=? AND c.authority_epoch=d.active_epoch`, domain, journal).Scan(&state, &closed)
	if err != nil {
		return err
	}
	if state != "open" || closed.Valid {
		return ErrFenced
	}
	if _, _, err = barrierDigest(ctx, tx, domain, journal); err != nil {
		return err
	}
	if _, err = tx.ExecContext(ctx, `UPDATE claim_journals SET state='sealed' WHERE domain_id=? AND journal_id=?`, domain, journal); err != nil {
		return err
	}
	return tx.Commit()
}

// CompleteClaimNoEffect commits only a terminal receipt. It cannot mutate a
// claim or append events, even if a caller supplies purported allocation IDs.
func (s *Store) CompleteClaimNoEffect(ctx context.Context, owner *Execution, code, problem string, occurred time.Time, sign Signer) (CommandStatus, error) {
	if owner == nil || owner.store != s || owner.lifecycle == nil || sign == nil || occurred.IsZero() || !validResultProblem(code, problem) {
		return CommandStatus{}, ErrInvalidProof
	}
	c := owner.lifecycle
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.db == nil || !s.owners[ownerKey(c.domain, c.id)] {
		return CommandStatus{}, ErrNotOwner
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return CommandStatus{}, err
	}
	defer func() { _ = tx.Rollback() }()
	head, err := checkLifecycleOwner(ctx, tx, c, owner.hash)
	if err != nil {
		return CommandStatus{}, err
	}
	return s.finishCommandTx(ctx, tx, c.commandIdentity, head, code, nil, problem, nil, nil, nil, occurred, sign, nil)
}

func checkLifecycleOwner(ctx context.Context, tx *sql.Tx, c *lifecycleCommand, hash string) (uint64, error) {
	var state, storedHash string
	var stored []byte
	var head uint64
	err := tx.QueryRowContext(ctx, `SELECT s.state,s.request_hash,s.command,e.sequence_head FROM submissions s JOIN environments e USING(domain_id,environment_id) WHERE s.domain_id=? AND s.command_id=?`, c.domain, c.id).Scan(&state, &storedHash, &stored, &head)
	if err != nil {
		return 0, err
	}
	if state != "submitted" || storedHash != hash || !bytes.Equal(stored, c.encoded) {
		return 0, ErrNotOwner
	}
	if c.sequence != head+1 {
		return 0, ErrPending
	}
	d, err := domainOwner(ctx, tx, c.domain)
	if err != nil {
		return 0, err
	}
	if d.ActiveEpoch != c.epoch {
		return 0, ErrFenced
	}
	return head, nil
}

// AcquireAllocation supplies fresh authority IDs in event order. The Batch ID
// is used only if the Matter does not already have an anonymous Batch. The
// trusted authority caller supplies the required blob closure; the current
// operation catalogue has no claim-delivery operation or blob inputs.
type AcquireAllocation struct {
	ClaimID, BatchID, GrantID, SnapshotID, JournalID string
	EventIDs                                         []string     // batch-created (if needed), acquired, dispatch-opened
	Installed                                        PrefixAnchor // exact pre-submission anchor, also required on recovery
	RequiredDigests                                  []string
}

// ClaimGrant holds the retained acquisition receipt's pinned transfer product.
type ClaimGrant struct {
	ID                  string
	Snapshot            PinnedSnapshot
	Start, End, Wrapper []byte
	Delta, Manifest     []byte
}

// CompleteClaimAcquire folds the acquisition, terminal receipt, complete pin,
// retained signed grant and initial journal in the same transaction.
func (s *Store) CompleteClaimAcquire(ctx context.Context, owner *Execution, a AcquireAllocation, occurred time.Time, sign Signer) (CommandStatus, ClaimGrant, error) {
	var grant ClaimGrant
	if owner == nil || owner.store != s || owner.lifecycle == nil || owner.lifecycle.name != "claim.acquire" || sign == nil || occurred.IsZero() {
		return CommandStatus{}, grant, ErrInvalidProof
	}
	for _, id := range []string{a.ClaimID, a.GrantID, a.SnapshotID, a.JournalID} {
		if !ulid.MatchString(id) {
			return CommandStatus{}, grant, ErrInvalidProof
		}
	}
	c := owner.lifecycle
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.db == nil || !s.owners[ownerKey(c.domain, c.id)] {
		return CommandStatus{}, grant, ErrNotOwner
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return CommandStatus{}, grant, err
	}
	defer func() { _ = tx.Rollback() }()
	head, err := checkLifecycleOwner(ctx, tx, c, owner.hash)
	if err != nil {
		return CommandStatus{}, grant, err
	}
	var startCount uint64
	var startID sql.NullString
	var startDigest string
	if err = tx.QueryRowContext(ctx, `SELECT start_count,start_event_id,start_digest FROM claim_acquire_intents WHERE domain_id=? AND command_id=?`, c.domain, c.id).Scan(&startCount, &startID, &startDigest); err != nil {
		return CommandStatus{}, grant, err
	}
	if startCount != a.Installed.EventCount || startDigest != a.Installed.Digest || startID.Valid != (a.Installed.EventID != "") || (startID.Valid && startID.String != a.Installed.EventID) {
		return CommandStatus{}, grant, ErrPrefixMismatch
	}
	retained, err := anchorAt(ctx, tx, c.domain, a.Installed.EventCount)
	if err != nil || !equalAnchor(retained, a.Installed) {
		return CommandStatus{}, grant, ErrPrefixMismatch
	}
	var repo string
	if err = tx.QueryRowContext(ctx, `SELECT repo_id FROM matters WHERE domain_id=? AND matter_id=?`, c.domain, c.matter).Scan(&repo); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			status, e := s.finishCommandTx(ctx, tx, c.commandIdentity, head, "result.rejected", nil, "validation.matter-not-found", nil, nil, nil, occurred, sign, nil)
			return status, grant, e
		}
		return CommandStatus{}, grant, err
	}
	if repo != c.repo {
		status, e := s.finishCommandTx(ctx, tx, c.commandIdentity, head, "result.refused", nil, "refusal.claim-worktree-mismatch", nil, nil, nil, occurred, sign, nil)
		return status, grant, e
	}
	var active int
	if err = tx.QueryRowContext(ctx, `SELECT count(*) FROM claims WHERE domain_id=? AND authority_epoch=? AND ((matter_id=? AND close_command_id IS NULL) OR dispatch_id=?)`, c.domain, c.epoch, c.matter, c.dispatch).Scan(&active); err != nil {
		return CommandStatus{}, grant, err
	}
	if active != 0 {
		status, e := s.finishCommandTx(ctx, tx, c.commandIdentity, head, "result.refused", nil, "refusal.claim-contended", nil, nil, nil, occurred, sign, nil)
		return status, grant, e
	}
	var epoch uint64
	if err = tx.QueryRowContext(ctx, `SELECT COALESCE(max(claim_epoch),0)+1 FROM claims WHERE domain_id=? AND matter_id=?`, c.domain, c.matter).Scan(&epoch); err != nil {
		return CommandStatus{}, grant, err
	}
	var batch string
	err = tx.QueryRowContext(ctx, `SELECT batch_id FROM anonymous_batches WHERE domain_id=? AND matter_id=?`, c.domain, c.matter).Scan(&batch)
	created := false
	if errors.Is(err, sql.ErrNoRows) {
		if !ulid.MatchString(a.BatchID) {
			return CommandStatus{}, grant, ErrInvalidProof
		}
		batch = a.BatchID
		created = true
	} else if err != nil {
		return CommandStatus{}, grant, err
	}
	want := 2
	if created {
		want++
	}
	if len(a.EventIDs) != want {
		return CommandStatus{}, grant, ErrInvalidProof
	}
	if created {
		if _, err = tx.ExecContext(ctx, `INSERT INTO anonymous_batches VALUES(?,?,?)`, c.domain, c.matter, batch); err != nil {
			return CommandStatus{}, grant, writeError(err)
		}
	}
	if _, err = tx.ExecContext(ctx, `INSERT INTO claims VALUES(?,?,?,?,?,?,?,?,?, ?,NULL)`, c.domain, a.ClaimID, c.matter, epoch, c.epoch, c.environment, c.worktree, batch, c.dispatch, c.id); err != nil {
		return CommandStatus{}, grant, writeError(err)
	}
	if _, err = tx.ExecContext(ctx, `INSERT INTO claim_journals VALUES(?,?,?,1,'open',NULL)`, c.domain, a.JournalID, a.ClaimID); err != nil {
		return CommandStatus{}, grant, writeError(err)
	}
	identity := eventIdentity{c.domain, c.id, c.hash, c.environment, c.sequence, c.actedAt, repo}
	idx := 0
	var first, last uint64
	add := func(kind, subject string, payload map[string]any) error {
		p, e := appendCommandEvent(ctx, tx, identity, occurred, a.EventIDs[idx], kind, subject, payload)
		if e != nil {
			return e
		}
		if idx == 0 {
			first = p
		}
		last = p
		idx++
		return nil
	}
	if created {
		if err = add("batch.anonymous-created", batch, map[string]any{"batch_id": batch, "matter_id": c.matter}); err != nil {
			return CommandStatus{}, grant, err
		}
	}
	if err = add("claim.acquired", a.ClaimID, map[string]any{"claim_id": a.ClaimID, "claim_epoch": epoch, "matter_id": c.matter, "batch_id": batch, "dispatch_id": c.dispatch, "owner_environment_id": c.environment, "worktree_id": c.worktree}); err != nil {
		return CommandStatus{}, grant, err
	}
	if err = add("dispatch.opened", c.dispatch, map[string]any{"dispatch_id": c.dispatch, "matter_id": c.matter, "batch_id": batch, "claim_id": a.ClaimID, "worktree_id": c.worktree}); err != nil {
		return CommandStatus{}, grant, err
	}
	output, err := artifactEncoder.Marshal(map[string]any{"claim": map[string]any{"id": a.ClaimID, "epoch": epoch}, "matter_id": c.matter, "batch_id": batch, "dispatch_id": c.dispatch})
	if err != nil {
		return CommandStatus{}, grant, err
	}
	rangeValue := map[string]any{"first_event_id": a.EventIDs[0], "last_event_id": a.EventIDs[len(a.EventIDs)-1], "event_count": uint64(len(a.EventIDs))}
	status, err := s.finishCommandTx(ctx, tx, c.commandIdentity, head, "result.succeeded", output, nil, rangeValue, first, last, occurred, sign, func(receipt, _ []byte, generation, sequence uint64) error {
		snapshot, e := pinSnapshotTx(ctx, tx, c.domain, c.epoch, a.Installed, a.SnapshotID, occurred, maxSnapshotLife, a.RequiredDigests)
		if e != nil {
			return e
		}
		grant.Snapshot = snapshot
		grant.ID = a.GrantID
		anchor := func(p PrefixAnchor) map[string]any {
			var id any
			if p.EventID != "" {
				id = p.EventID
			}
			return map[string]any{"event_count": p.EventCount, "high_water_event_id": id, "prefix_digest": p.Digest}
		}
		var receiptValue map[string]any
		if e = artifactDecoder.Unmarshal(receipt, &receiptValue); e != nil {
			return e
		}
		start := map[string]any{"schema": "wipd.claim-grant-start/1", "grant_id": a.GrantID, "acquire_command_id": c.id, "acquire_request_hash": c.hash, "domain_id": c.domain, "authority_epoch": c.epoch, "owner_environment_id": c.environment, "claim": map[string]any{"id": a.ClaimID, "epoch": epoch}, "matter_id": c.matter, "batch_id": batch, "dispatch_id": c.dispatch, "receipt": receiptValue, "prefix": map[string]any{"start": anchor(a.Installed), "end": anchor(snapshot.Delta.End)}, "blob_manifest_digest": snapshot.Manifest.Digest}
		grant.Start, e = artifactEncoder.Marshal(start)
		if e != nil {
			return e
		}
		grant.End, e = artifactEncoder.Marshal(map[string]any{"schema": "wipd.claim-grant-end/1", "grant_id": a.GrantID, "verified_prefix": anchor(snapshot.Delta.End), "verified_blob_manifest_digest": snapshot.Manifest.Digest, "complete": true})
		if e != nil {
			return e
		}
		events := make([]map[string]any, 0, len(snapshot.Delta.Events))
		for _, item := range snapshot.Delta.Events {
			events = append(events, map[string]any{"event_id": item.EventID, "record": item.Record})
		}
		delta, e := artifactEncoder.Marshal(map[string]any{"start": anchor(a.Installed), "end": anchor(snapshot.Delta.End), "events": events})
		if e != nil {
			return e
		}
		entries := make([]map[string]any, 0, len(snapshot.Manifest.Entries))
		for _, item := range snapshot.Manifest.Entries {
			entries = append(entries, map[string]any{"digest": item.Digest, "byte_length": item.ByteLength, "requirement": item.Requirement})
		}
		manifest, e := artifactEncoder.Marshal(map[string]any{"schema": "wipd.blob-manifest/1", "domain_id": c.domain, "authority_epoch": c.epoch, "as_of": anchor(snapshot.Delta.End), "entries": entries, "manifest_digest": snapshot.Manifest.Digest})
		if e != nil {
			return e
		}
		grant.Delta, grant.Manifest = delta, manifest
		var keyID, priorDigest string
		var public []byte
		if e = tx.QueryRowContext(ctx, `SELECT key_id,public_key FROM artifact_keys WHERE domain_id=? AND epoch=? AND generation=?`, c.domain, c.epoch, generation).Scan(&keyID, &public); e != nil {
			return e
		}
		if e = tx.QueryRowContext(ctx, `SELECT digest FROM authority_artifacts WHERE domain_id=? AND epoch=? AND generation=? AND sequence=?`, c.domain, c.epoch, generation, sequence).Scan(&priorDigest); e != nil {
			return e
		}
		fields := map[string]any{"schema": "wipd.signed-artifact/1", "kind": "claim-grant", "domain_id": c.domain, "authority_epoch": c.epoch, "signer_role": "authority", "signer_key_id": keyID, "key_generation": generation, "artifact_sequence": sequence + 1, "previous_artifact_digest": priorDigest, "issued_at": occurred.UTC().Format(time.RFC3339Nano), "payload_schema": "wipd.claim-grant-start/1", "payload_digest": digestBytes(grant.Start), "payload": grant.Start}
		unsigned, e := artifactEncoder.Marshal(fields)
		if e != nil {
			return e
		}
		preimage := append([]byte("wipd/signed-artifact/v1\x00"), unsigned...)
		signature, e := sign(ctx, preimage)
		if e != nil {
			return e
		}
		if !ed25519.Verify(public, preimage, signature) {
			return ErrInvalidProof
		}
		fields["signature"] = signature
		grantWrapper, e := artifactEncoder.Marshal(fields)
		if e != nil {
			return e
		}
		grant.Wrapper = grantWrapper
		artifactDigest, e := verifyAuthorityArtifact(grantWrapper, public, c.domain, keyID, c.epoch, generation, sequence+1, &priorDigest)
		if e != nil {
			return e
		}
		if _, e = tx.ExecContext(ctx, `INSERT INTO authority_artifacts VALUES(?,?,?,?,?,?,?)`, c.domain, c.epoch, generation, sequence+1, artifactDigest, priorDigest, grantWrapper); e != nil {
			return e
		}
		var startID, endID any
		if a.Installed.EventID != "" {
			startID = a.Installed.EventID
		}
		if snapshot.Delta.End.EventID != "" {
			endID = snapshot.Delta.End.EventID
		}
		_, e = tx.ExecContext(ctx, `INSERT INTO claim_grants VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`, c.domain, c.id, a.GrantID, a.ClaimID, a.Installed.EventCount, startID, a.Installed.Digest, snapshot.Delta.End.EventCount, endID, snapshot.Delta.End.Digest, snapshot.Manifest.Digest, receipt, grantWrapper, c.epoch, generation, sequence+1, delta, manifest, a.SnapshotID)
		return e
	})
	if err != nil {
		return CommandStatus{}, ClaimGrant{}, fmt.Errorf("claim acquire: %w", err)
	}
	return status, grant, nil
}

// QueryClaimGrant authenticates the original acting Environment through the
// Step 5 receipt query and returns only a matching successful pinned product.
func (s *Store) QueryClaimGrant(ctx context.Context, domain, commandID, hash string, epoch uint64, peer tls.ConnectionState, environment string, at time.Time) (CommandStatus, ClaimGrant, error) {
	status, err := s.QueryCommand(ctx, domain, commandID, hash, epoch, peer, environment, at)
	if err != nil || status.Pending {
		return status, ClaimGrant{}, err
	}
	receipt, err := readReceipt(status.Receipt)
	if err != nil || receipt.Operation.Name != "claim.acquire" || receipt.Operation.Version != 1 || receipt.Environment.ID != environment {
		return status, ClaimGrant{}, ErrInvalidProof
	}
	if receipt.Result.Code != "result.succeeded" {
		return status, ClaimGrant{}, nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.db == nil {
		return status, ClaimGrant{}, ErrInvalidStore
	}
	var grant ClaimGrant
	var startCount, endCount uint64
	var startID, endID sql.NullString
	var startDigest, endDigest, manifestDigest, claimID string
	var storedReceipt []byte
	err = s.db.QueryRowContext(ctx, `SELECT grant_id,claim_id,start_count,start_event_id,start_digest,end_count,end_event_id,end_digest,manifest_digest,receipt,grant_wrapper,delta,manifest FROM claim_grants WHERE domain_id=? AND acquire_command_id=?`, domain, commandID).Scan(&grant.ID, &claimID, &startCount, &startID, &startDigest, &endCount, &endID, &endDigest, &manifestDigest, &storedReceipt, &grant.Wrapper, &grant.Delta, &grant.Manifest)
	if err != nil {
		return status, ClaimGrant{}, err
	}
	if !bytes.Equal(storedReceipt, status.Receipt) {
		return status, ClaimGrant{}, ErrInvalidStore
	}
	var wrapper signedArtifact
	if artifactDecoder.Unmarshal(grant.Wrapper, &wrapper) != nil || wrapper.Kind != "claim-grant" || wrapper.PayloadSchema != "wipd.claim-grant-start/1" {
		return status, ClaimGrant{}, ErrInvalidStore
	}
	grant.Start = wrapper.Payload
	var start map[string]cbor.RawMessage
	if canonicalDecode(grant.Start, &start) != nil || !exactKeys(start, "schema", "grant_id", "acquire_command_id", "acquire_request_hash", "domain_id", "authority_epoch", "owner_environment_id", "claim", "matter_id", "batch_id", "dispatch_id", "receipt", "prefix", "blob_manifest_digest") {
		return status, ClaimGrant{}, ErrInvalidStore
	}
	var binding struct {
		ID      string `cbor:"grant_id"`
		Command string `cbor:"acquire_command_id"`
		Hash    string `cbor:"acquire_request_hash"`
		Claim   struct {
			ID string `cbor:"id"`
		} `cbor:"claim"`
		Prefix struct {
			Start struct {
				Count  uint64  `cbor:"event_count"`
				ID     *string `cbor:"high_water_event_id"`
				Digest string  `cbor:"prefix_digest"`
			} `cbor:"start"`
			End struct {
				Count  uint64  `cbor:"event_count"`
				ID     *string `cbor:"high_water_event_id"`
				Digest string  `cbor:"prefix_digest"`
			} `cbor:"end"`
		} `cbor:"prefix"`
		Manifest string `cbor:"blob_manifest_digest"`
	}
	if artifactDecoder.Unmarshal(grant.Start, &binding) != nil || binding.ID != grant.ID || binding.Command != commandID || binding.Hash != hash || binding.Claim.ID != claimID || binding.Prefix.Start.Count != startCount || binding.Prefix.Start.Digest != startDigest || binding.Prefix.End.Count != endCount || binding.Prefix.End.Digest != endDigest || binding.Manifest != manifestDigest || (binding.Prefix.Start.ID == nil) != (!startID.Valid) || (startID.Valid && *binding.Prefix.Start.ID != startID.String) || (binding.Prefix.End.ID == nil) != (!endID.Valid) || (endID.Valid && *binding.Prefix.End.ID != endID.String) {
		return status, ClaimGrant{}, ErrInvalidStore
	}
	anchor := func(count uint64, id sql.NullString, digest string) map[string]any {
		var value any
		if id.Valid {
			value = id.String
		}
		return map[string]any{"event_count": count, "high_water_event_id": value, "prefix_digest": digest}
	}
	grant.End, err = artifactEncoder.Marshal(map[string]any{"schema": "wipd.claim-grant-end/1", "grant_id": grant.ID, "verified_prefix": anchor(endCount, endID, endDigest), "verified_blob_manifest_digest": manifestDigest, "complete": true})
	return status, grant, err
}

type claimState struct {
	matter, repo, owner, worktree, dispatch, journal, state string
	epoch, authority, generation                            uint64
	closed                                                  sql.NullString
}

func loadClaim(ctx context.Context, tx *sql.Tx, c *lifecycleCommand, id string) (claimState, error) {
	var x claimState
	err := tx.QueryRowContext(ctx, `SELECT c.matter_id,m.repo_id,c.owner_environment_id,c.worktree_id,c.dispatch_id,c.claim_epoch,c.authority_epoch,c.close_command_id,j.journal_id,j.state,j.generation FROM claims c JOIN matters m ON m.domain_id=c.domain_id AND m.matter_id=c.matter_id JOIN claim_journals j USING(domain_id,claim_id) WHERE c.domain_id=? AND c.claim_id=? AND j.state IN ('open','sealed')`, c.domain, id).Scan(&x.matter, &x.repo, &x.owner, &x.worktree, &x.dispatch, &x.epoch, &x.authority, &x.closed, &x.journal, &x.state, &x.generation)
	return x, err
}

func barrierDigest(ctx context.Context, tx *sql.Tx, domain, journal string) (string, uint64, error) {
	root := sha256.Sum256([]byte("wipd/journal-barrier/v1\x00"))
	count := uint64(0)
	rows, err := tx.QueryContext(ctx, `SELECT position,command_id,request_hash,receipt,state FROM claim_journal_entries WHERE domain_id=? AND journal_id=? ORDER BY position`, domain, journal)
	if err != nil {
		return "", 0, err
	}
	defer func() { _ = rows.Close() }()
	for rows.Next() {
		var pos uint64
		var id, hash, state string
		var receipt []byte
		if err = rows.Scan(&pos, &id, &hash, &receipt, &state); err != nil {
			return "", 0, err
		}
		count++
		if pos != count || state != "terminal" || len(receipt) == 0 {
			return "", 0, ErrPending
		}
		r, e := readReceipt(receipt)
		if e != nil || r.ID != id || r.Hash != hash || r.Domain != domain {
			return "", 0, ErrInvalidProof
		}
		b, e := digestRaw(hash)
		if e != nil {
			return "", 0, e
		}
		component := make([]byte, 8, 8+26+32+1+1+26+26+8)
		binary.BigEndian.PutUint64(component, pos)
		component = append(component, id...)
		component = append(component, b...)
		switch r.Result.Code {
		case "result.succeeded":
			component = append(component, 0)
		case "result.rejected":
			component = append(component, 1)
		case "result.refused":
			component = append(component, 2)
		case "result.failed":
			component = append(component, 3)
		default:
			return "", 0, ErrInvalidProof
		}
		if r.Range == nil {
			component = append(component, 0)
		} else {
			if !ulid.MatchString(r.Range.First) || !ulid.MatchString(r.Range.Last) || r.Range.Count == 0 {
				return "", 0, ErrInvalidProof
			}
			component = append(component, 1)
			component = append(component, r.Range.First...)
			component = append(component, r.Range.Last...)
			var n [8]byte
			binary.BigEndian.PutUint64(n[:], r.Range.Count)
			component = append(component, n[:]...)
		}
		h := sha256.New()
		_, _ = h.Write([]byte("wipd/journal-barrier-step/v1\x00"))
		_, _ = h.Write(root[:])
		_, _ = h.Write(component)
		copy(root[:], h.Sum(nil))
	}
	if err = rows.Err(); err != nil {
		return "", 0, err
	}
	return digestRawBytes(root[:]), count, nil
}

// CompleteClaimLifecycle folds one successful repair or close, or issues an
// effect-free terminal refusal for a semantic mismatch.
func (s *Store) CompleteClaimLifecycle(ctx context.Context, owner *Execution, newJournal string, eventIDs []string, occurred time.Time, sign Signer) (CommandStatus, error) {
	if owner == nil || owner.store != s || owner.lifecycle == nil || owner.lifecycle.name == "claim.acquire" || sign == nil || occurred.IsZero() {
		return CommandStatus{}, ErrInvalidProof
	}
	c := owner.lifecycle
	if (c.name == "claim.journal-repair" && (len(eventIDs) != 1 || !ulid.MatchString(newJournal))) || (c.name != "claim.journal-repair" && (len(eventIDs) != 2 || newJournal != "")) {
		return CommandStatus{}, ErrInvalidProof
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.db == nil || !s.owners[ownerKey(c.domain, c.id)] {
		return CommandStatus{}, ErrNotOwner
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return CommandStatus{}, err
	}
	defer func() { _ = tx.Rollback() }()
	head, err := checkLifecycleOwner(ctx, tx, c, owner.hash)
	if err != nil {
		return CommandStatus{}, err
	}
	id := c.claimID
	if c.stand != nil {
		id = c.stand.Target.ClaimID
	}
	x, e := loadClaim(ctx, tx, c, id)
	problem := ""
	if e != nil && !errors.Is(e, sql.ErrNoRows) {
		return CommandStatus{}, e
	}
	if e != nil || x.closed.Valid || x.authority != c.epoch || x.repo != c.repo || (c.stand != nil && (x.epoch != c.stand.Target.Epoch || x.owner != c.stand.Target.Owner || x.owner == c.environment)) || (c.stand == nil && (x.epoch != c.claimEpoch || x.owner != c.environment || x.worktree != c.worktree)) {
		problem = "refusal.claim-stand-down-fenced"
		if c.stand == nil {
			problem = "refusal.claim-release-barrier"
			if c.repair != nil {
				problem = "refusal.journal-repair-conflict"
			}
		}
	}
	var barrier string
	if problem == "" {
		switch c.name {
		case "claim.stand-down":
			if c.stand.Reason == "" {
				problem = "validation.stand-down-reason"
			} else if !c.stand.Loss {
				problem = "refusal.claim-loss-not-acknowledged"
			} else if c.ownerNonce == "" {
				return CommandStatus{}, ErrInvalidProof
			} else {
				var used int
				if e = tx.QueryRowContext(ctx, `SELECT count(*) FROM claim_closes WHERE owner_nonce=?`, c.ownerNonce).Scan(&used); e != nil {
					return CommandStatus{}, e
				}
				if used != 0 {
					problem = "refusal.claim-stand-down-fenced"
				}
			}
		case "claim.release":
			b := c.barrier
			if x.journal != b.Journal || x.state != "sealed" || !b.Sealed || b.Unresolved != 0 || b.Quarantined != 0 || b.Count != b.Last || b.Count != b.Receipts {
				problem = "refusal.claim-release-barrier"
			} else {
				var n uint64
				var ex error
				barrier, n, ex = barrierDigest(ctx, tx, c.domain, x.journal)
				if ex != nil || n != b.Count || barrier != b.Digest {
					problem = "refusal.claim-release-barrier"
				}
			}
		case "claim.journal-repair":
			r := c.repair
			if x.journal != r.Journal || x.state != "open" || newJournal == x.journal {
				problem = "refusal.journal-repair-conflict"
			} else {
				var used int
				if e = tx.QueryRowContext(ctx, `SELECT count(*) FROM claim_journals WHERE domain_id=? AND journal_id=?`, c.domain, newJournal).Scan(&used); e != nil {
					return CommandStatus{}, e
				}
				if used != 0 {
					problem = "refusal.journal-repair-conflict"
				}
				if r.Action.Kind == "replace" {
					replacement, pe := parseJournalCommand(r.Action.Replacement, r.Action.Hash)
					if pe != nil || replacement.ID == r.HeadID || replacement.Claim != id || replacement.ClaimEpoch != x.epoch || replacement.Domain != c.domain || replacement.Epoch != c.epoch || replacement.Environment != x.owner || replacement.Repo != x.repo || replacement.Worktree != x.worktree || replacement.Sequence != c.sequence+1 {
						problem = "refusal.journal-repair-conflict"
					} else if e = tx.QueryRowContext(ctx, `SELECT (SELECT count(*) FROM submissions WHERE domain_id=? AND command_id=?)+(SELECT count(*) FROM claim_journal_entries WHERE domain_id=? AND command_id=?)`, c.domain, replacement.ID, c.domain, replacement.ID).Scan(&used); e != nil {
						return CommandStatus{}, e
					} else if used != 0 {
						problem = "refusal.journal-repair-conflict"
					}
				}
				if problem != "" {
					break
				}
				var headID, headHash string
				var position uint64
				e = tx.QueryRowContext(ctx, `SELECT position,command_id,request_hash FROM claim_journal_entries WHERE domain_id=? AND journal_id=? AND state!='terminal' ORDER BY position LIMIT 1`, c.domain, x.journal).Scan(&position, &headID, &headHash)
				if e != nil || position != r.Position || headID != r.HeadID || headHash != r.HeadHash {
					problem = "refusal.journal-repair-conflict"
				} else {
					proof := r.Action.Proof
					var retained []byte
					e = tx.QueryRowContext(ctx, `SELECT receipt FROM terminal_receipts WHERE domain_id=? AND command_id=?`, c.domain, headID).Scan(&retained)
					if !bytes.Equal(proof.Receipt, []byte{0xf6}) {
						rec, pe := readReceipt(proof.Receipt)
						if e != nil || pe != nil || !bytes.Equal(retained, proof.Receipt) || rec.Result.Code == "result.succeeded" || rec.ID != headID || rec.Hash != headHash || proof.NotSubmitted != nil {
							problem = "refusal.journal-repair-proof"
						}
					} else if proof.NotSubmitted == nil || (*proof.NotSubmitted != "definitely-unsent" && *proof.NotSubmitted != "same-epoch-receipt-not-found") || !errors.Is(e, sql.ErrNoRows) {
						problem = "refusal.journal-repair-proof"
					} else {
						var submitted int
						e = tx.QueryRowContext(ctx, `SELECT count(*) FROM submissions WHERE domain_id=? AND command_id=?`, c.domain, headID).Scan(&submitted)
						if e != nil {
							return CommandStatus{}, e
						}
						if submitted != 0 {
							problem = "refusal.journal-repair-proof"
						}
					}
				}
			}
		}
	}
	if problem != "" {
		code := "result.refused"
		if problem == "validation.stand-down-reason" {
			code = "result.rejected"
		}
		return s.finishCommandTx(ctx, tx, c.commandIdentity, head, code, nil, problem, nil, nil, nil, occurred, sign, nil)
	}
	var output map[string]any
	var events []struct {
		kind, subject string
		payload       map[string]any
	}
	if c.repair != nil {
		output = map[string]any{"claim_id": id, "archived_journal_id": x.journal, "new_journal_id": newJournal, "action": c.repair.Action.Kind}
		events = append(events, struct {
			kind, subject string
			payload       map[string]any
		}{"claim.journal-repaired", id, output})
	}
	if c.barrier != nil {
		output = map[string]any{"claim_id": id, "claim_epoch": x.epoch, "dispatch_id": x.dispatch, "barrier_digest": barrier}
		events = append(events, struct {
			kind, subject string
			payload       map[string]any
		}{"dispatch.closed", x.dispatch, map[string]any{"dispatch_id": x.dispatch, "claim_id": id, "claim_epoch": x.epoch}}, struct {
			kind, subject string
			payload       map[string]any
		}{"claim.released", id, output})
	}
	if c.stand != nil {
		reason := digestBytes([]byte(c.stand.Reason))
		output = map[string]any{"claim_id": id, "claim_epoch": x.epoch, "dispatch_id": x.dispatch, "reason_digest": reason}
		events = append(events, struct {
			kind, subject string
			payload       map[string]any
		}{"dispatch.closed", x.dispatch, map[string]any{"dispatch_id": x.dispatch, "claim_id": id, "claim_epoch": x.epoch}}, struct {
			kind, subject string
			payload       map[string]any
		}{"claim.stood-down", id, map[string]any{"claim_id": id, "claim_epoch": x.epoch, "dispatch_id": x.dispatch, "owner_environment_id": x.owner, "acting_environment_id": c.environment, "reason_digest": reason, "loss_accepted": true}})
	}
	var first, last uint64
	for i, event := range events {
		p, ex := appendCommandEvent(ctx, tx, eventIdentity{c.domain, c.id, c.hash, c.environment, c.sequence, c.actedAt, c.repo}, occurred, eventIDs[i], event.kind, event.subject, event.payload)
		if ex != nil {
			return CommandStatus{}, ex
		}
		if i == 0 {
			first = p
		}
		last = p
	}
	encoded, err := artifactEncoder.Marshal(output)
	if err != nil {
		return CommandStatus{}, err
	}
	rangeValue := map[string]any{"first_event_id": eventIDs[0], "last_event_id": eventIDs[len(eventIDs)-1], "event_count": uint64(len(eventIDs))}
	return s.finishCommandTx(ctx, tx, c.commandIdentity, head, "result.succeeded", encoded, nil, rangeValue, first, last, occurred, sign, func(_ []byte, _ []byte, _, _ uint64) error {
		if c.repair != nil {
			if _, e = tx.ExecContext(ctx, `UPDATE claim_journals SET state='quarantined',repair_command_id=? WHERE domain_id=? AND journal_id=?`, c.id, c.domain, x.journal); e != nil {
				return e
			}
			if _, e = tx.ExecContext(ctx, `INSERT INTO claim_journals VALUES(?,?,?,?,'open',NULL)`, c.domain, newJournal, id, x.generation+1); e != nil {
				return e
			}
			if c.repair.Action.Kind == "replace" {
				r := c.repair.Action
				replacement, ex := parseJournalCommand(r.Replacement, r.Hash)
				if ex != nil || replacement.ID == c.repair.HeadID || replacement.Claim != id || replacement.ClaimEpoch != x.epoch || replacement.Domain != c.domain || replacement.Epoch != c.epoch || replacement.Environment != x.owner || replacement.Repo != x.repo || replacement.Worktree != x.worktree || replacement.Sequence != c.sequence+1 {
					return ErrInvalidProof
				}
				_, e = tx.ExecContext(ctx, `INSERT INTO claim_journal_entries(domain_id,journal_id,position,command_id,request_hash,command,environment_sequence,state) VALUES(?,?,1,?,?,?,?,'pending-return')`, c.domain, newJournal, replacement.ID, r.Hash, r.Replacement, replacement.Sequence)
				return e
			}
			return nil
		}
		var reason, nonce any
		var kind string
		var storedBarrier any
		if c.barrier != nil {
			kind = "release"
			storedBarrier = mustRaw(mustRaw(c.encoded, "input"), "barrier")
		} else {
			kind = "stand-down"
			reason = digestBytes([]byte(c.stand.Reason))
			nonce = c.ownerNonce
		}
		if _, e = tx.ExecContext(ctx, `INSERT INTO claim_closes VALUES(?,?,?,?,?,?,?,?)`, c.domain, id, c.id, kind, storedBarrier, c.environment, reason, nonce); e != nil {
			return e
		}
		if kind == "stand-down" {
			var authorization []byte
			var verifiedAt string
			if e = tx.QueryRowContext(ctx, `SELECT authorization,verified_at FROM claim_stand_down_proofs WHERE domain_id=? AND command_id=?`, c.domain, c.id).Scan(&authorization, &verifiedAt); e != nil {
				return e
			}
			if _, e = tx.ExecContext(ctx, `INSERT INTO owner_nonce_uses(domain_id,nonce,action,artifact_digest,consumed_at) VALUES(?,?,'claim-stand-down',?,?)`, c.domain, c.ownerNonce, digestBytes(authorization), verifiedAt); e != nil {
				return ErrFenced
			}
		}
		_, e = tx.ExecContext(ctx, `UPDATE claims SET close_command_id=? WHERE domain_id=? AND claim_id=? AND close_command_id IS NULL`, c.id, c.domain, id)
		return e
	})
}
