package authoritystore

import (
	"bytes"
	"context"
	"crypto/tls"
	"database/sql"
	"errors"
	"fmt"
	"strconv"
	"time"

	"github.com/fxamacker/cbor/v2"
	"github.com/procrastivity/wip/internal/operation"
)

const gateExemptionRepairOperationName = "gate.exemption.repair"

// gateExemptionRepairCommand is the private canonical identity used by the
// admission seam before this operation is registered or transport-admissible.
// The detached proof is deliberately not a field here.
type gateExemptionRepairCommand struct {
	ID, DomainID, EnvironmentID                       string
	AuthorityEpoch, EnvironmentSequence               uint64
	ActedAt, Actor, CausationCommandID, CorrelationID string
	RepoID, CloneID, WorktreeID, ClaimID              string
	ClaimEpoch                                        uint64
	NodeID, Gate, IncidentRef, Reason                 string
	Boundary                                          gateExemptionRepairBoundary
	Evidence                                          []string
}

type gateExemptionRepairAdmission struct {
	DomainID, CommandID, RequestHash    string
	Command, Proof                      []byte
	AuthorityEpoch, EnvironmentSequence uint64
	EnvironmentID, RepoID, NodeID, Gate string
	Boundary                            gateExemptionRepairBoundary
	Nonce                               []byte
	VerifiedAt                          string
	Terminal                            *gateExemptionRepairTerminal
}

type gateRepairQueryer interface {
	QueryContext(context.Context, string, ...any) (*sql.Rows, error)
	QueryRowContext(context.Context, string, ...any) *sql.Row
}

func (command gateExemptionRepairCommand) canonicalBytes() ([]byte, string, error) {
	var empty []byte
	if !ulid.MatchString(command.ID) || !ulid.MatchString(command.DomainID) || command.AuthorityEpoch == 0 ||
		!ulid.MatchString(command.EnvironmentID) || command.EnvironmentSequence == 0 ||
		!ulid.MatchString(command.CorrelationID) || command.Actor != "human" ||
		(command.CausationCommandID != "" && (!ulid.MatchString(command.CausationCommandID) || command.CausationCommandID == command.ID)) ||
		(command.CausationCommandID == "" && command.CorrelationID != command.ID) ||
		!ulid.MatchString(command.RepoID) || !ulid.MatchString(command.CloneID) || !ulid.MatchString(command.WorktreeID) ||
		!ulid.MatchString(command.ClaimID) || command.ClaimEpoch == 0 || !validGateExemptionRepairGate(command.Gate) ||
		!ulid.MatchString(command.NodeID) || !validGateExemptionRepairIncidentRef(command.IncidentRef) ||
		!validGateExemptionRepairReason(command.Reason) || !validGateExemptionRepairEvidence(command.Evidence) ||
		command.Boundary.EventCount == 0 || command.Boundary.EventCount > 1<<63-1 ||
		!ulid.MatchString(command.Boundary.HighWaterEvent) || !validDigest(command.Boundary.PrefixDigest) {
		return empty, "", ErrInvalidProof
	}
	if _, err := utcTime(command.ActedAt); err != nil {
		return empty, "", ErrInvalidProof
	}
	var cause any
	if command.CausationCommandID != "" {
		cause = command.CausationCommandID
	}
	encoded, err := artifactEncoder.Marshal(map[string]any{
		"schema": "wipd.command/1", "command_id": command.ID,
		"authority":   map[string]any{"domain_id": command.DomainID, "expected_epoch": command.AuthorityEpoch},
		"environment": map[string]any{"id": command.EnvironmentID, "sequence": command.EnvironmentSequence},
		"acted_at":    command.ActedAt, "actor": command.Actor,
		"causation_command_id": cause, "correlation_command_id": command.CorrelationID,
		"operation": map[string]any{"name": gateExemptionRepairOperationName, "version": uint64(1)},
		"context":   map[string]any{"repo_id": command.RepoID, "clone_id": command.CloneID, "worktree_id": command.WorktreeID},
		"claim":     map[string]any{"id": command.ClaimID, "epoch": command.ClaimEpoch},
		"input": map[string]any{
			"node_id": command.NodeID, "gate": command.Gate,
			"event_count": command.Boundary.EventCount, "high_water_event_id": command.Boundary.HighWaterEvent,
			"prefix_digest": command.Boundary.PrefixDigest, "incident_ref": command.IncidentRef,
			"reason": command.Reason, "evidence_refs": command.Evidence,
		},
		"blobs": []any{},
	})
	if err != nil || len(encoded) == 0 || len(encoded) > 1<<20 {
		return empty, "", ErrInvalidProof
	}
	return encoded, gateRepairRequestHash(encoded), nil
}

func gateRepairRequestHash(command []byte) string {
	return digestBytes(append([]byte("wipd/request-hash/v1\x00"), command...))
}

func decodeGateExemptionRepairCommand(raw []byte, assertedHash string) (gateExemptionRepairCommand, error) {
	var command gateExemptionRepairCommand
	if len(raw) == 0 || len(raw) > 1<<20 || !canonicalArtifactCBOR(raw) || !validDigest(assertedHash) || gateRepairRequestHash(raw) != assertedHash {
		return command, ErrInvalidProof
	}
	top, err := gateRepairRawMap(raw, "schema", "command_id", "authority", "environment", "acted_at", "actor", "causation_command_id", "correlation_command_id", "operation", "context", "claim", "input", "blobs")
	if err != nil {
		return command, ErrInvalidProof
	}
	var schema string
	if readGateRepairString(top, "schema", &schema) != nil || schema != "wipd.command/1" ||
		readGateRepairString(top, "command_id", &command.ID) != nil ||
		readGateRepairString(top, "acted_at", &command.ActedAt) != nil ||
		readGateRepairString(top, "actor", &command.Actor) != nil ||
		readGateRepairString(top, "correlation_command_id", &command.CorrelationID) != nil {
		return gateExemptionRepairCommand{}, ErrInvalidProof
	}
	if !bytes.Equal(top["causation_command_id"], []byte{0xf6}) {
		if readGateRepairString(top, "causation_command_id", &command.CausationCommandID) != nil {
			return gateExemptionRepairCommand{}, ErrInvalidProof
		}
	}
	authority, err := gateRepairRawMap(top["authority"], "domain_id", "expected_epoch")
	if err != nil || readGateRepairString(authority, "domain_id", &command.DomainID) != nil ||
		readGateRepairUint(authority, "expected_epoch", &command.AuthorityEpoch) != nil {
		return gateExemptionRepairCommand{}, ErrInvalidProof
	}
	environment, err := gateRepairRawMap(top["environment"], "id", "sequence")
	if err != nil || readGateRepairString(environment, "id", &command.EnvironmentID) != nil ||
		readGateRepairUint(environment, "sequence", &command.EnvironmentSequence) != nil {
		return gateExemptionRepairCommand{}, ErrInvalidProof
	}
	operationFields, err := gateRepairRawMap(top["operation"], "name", "version")
	var operationName string
	var operationVersion uint64
	if err != nil || readGateRepairString(operationFields, "name", &operationName) != nil ||
		readGateRepairUint(operationFields, "version", &operationVersion) != nil ||
		operationName != gateExemptionRepairOperationName || operationVersion != 1 {
		return gateExemptionRepairCommand{}, ErrInvalidProof
	}
	requestContext, err := gateRepairRawMap(top["context"], "repo_id", "clone_id", "worktree_id")
	if err != nil || readGateRepairString(requestContext, "repo_id", &command.RepoID) != nil ||
		readGateRepairString(requestContext, "clone_id", &command.CloneID) != nil ||
		readGateRepairString(requestContext, "worktree_id", &command.WorktreeID) != nil {
		return gateExemptionRepairCommand{}, ErrInvalidProof
	}
	claim, err := gateRepairRawMap(top["claim"], "id", "epoch")
	if err != nil || readGateRepairString(claim, "id", &command.ClaimID) != nil ||
		readGateRepairUint(claim, "epoch", &command.ClaimEpoch) != nil {
		return gateExemptionRepairCommand{}, ErrInvalidProof
	}
	input, err := gateRepairRawMap(top["input"], "node_id", "gate", "event_count", "high_water_event_id", "prefix_digest", "incident_ref", "reason", "evidence_refs")
	if err != nil || readGateRepairString(input, "node_id", &command.NodeID) != nil ||
		readGateRepairString(input, "gate", &command.Gate) != nil ||
		readGateRepairUint(input, "event_count", &command.Boundary.EventCount) != nil ||
		readGateRepairString(input, "high_water_event_id", &command.Boundary.HighWaterEvent) != nil ||
		readGateRepairString(input, "prefix_digest", &command.Boundary.PrefixDigest) != nil ||
		readGateRepairString(input, "incident_ref", &command.IncidentRef) != nil ||
		readGateRepairString(input, "reason", &command.Reason) != nil ||
		readGateRepairStrings(input, "evidence_refs", &command.Evidence) != nil ||
		!bytes.Equal(top["blobs"], []byte{0x80}) {
		return gateExemptionRepairCommand{}, ErrInvalidProof
	}
	reencoded, _, err := command.canonicalBytes()
	if err != nil || !bytes.Equal(reencoded, raw) {
		return gateExemptionRepairCommand{}, ErrInvalidProof
	}
	return command, nil
}

func gateRepairRawMap(raw cbor.RawMessage, keys ...string) (map[string]cbor.RawMessage, error) {
	var fields map[string]cbor.RawMessage
	if len(raw) == 0 || raw[0]>>5 != 5 || canonicalDecode(raw, &fields) != nil || !exactKeys(fields, keys...) {
		return nil, ErrInvalidProof
	}
	return fields, nil
}

func readGateRepairString(fields map[string]cbor.RawMessage, key string, value *string) error {
	raw := fields[key]
	if len(raw) == 0 || raw[0]>>5 != 3 || artifactDecoder.Unmarshal(raw, value) != nil {
		return ErrInvalidProof
	}
	return nil
}

func readGateRepairUint(fields map[string]cbor.RawMessage, key string, value *uint64) error {
	raw := fields[key]
	if len(raw) == 0 || raw[0]>>5 != 0 || artifactDecoder.Unmarshal(raw, value) != nil {
		return ErrInvalidProof
	}
	return nil
}

func readGateRepairStrings(fields map[string]cbor.RawMessage, key string, value *[]string) error {
	raw := fields[key]
	if len(raw) == 0 || raw[0]>>5 != 4 || artifactDecoder.Unmarshal(raw, value) != nil {
		return ErrInvalidProof
	}
	return nil
}

func (command gateExemptionRepairCommand) binding(hash string) gateExemptionRepairBinding {
	return gateExemptionRepairBinding{
		CommandID: command.ID, RequestHash: hash, RepoID: command.RepoID, NodeID: command.NodeID,
		Gate: command.Gate, Boundary: command.Boundary, IncidentRef: command.IncidentRef,
		Reason: command.Reason, Evidence: append([]string(nil), command.Evidence...),
	}
}

func (command gateExemptionRepairCommand) claimCommand() operation.Command {
	return operation.Command{
		ID: command.ID, AuthorityDomainID: command.DomainID, ExpectedAuthorityEpoch: command.AuthorityEpoch,
		EnvironmentID: command.EnvironmentID, EnvironmentSequence: command.EnvironmentSequence,
		ActedAt: command.ActedAt, Request: operation.Request{
			Actor:   operation.Actor(command.Actor),
			Context: operation.Context{Repo: command.RepoID, Clone: command.CloneID, Worktree: command.WorktreeID},
			Claim:   &operation.ClaimContext{ID: command.ClaimID, Epoch: strconv.FormatUint(command.ClaimEpoch, 10)},
		},
	}
}

// admitGateExemptionRepair durably reserves the repair-only nonce and exact
// retry identity after authentication, claim fencing, historical-boundary
// verification, and owner-proof verification. It is not called by public
// command submission and creates no execution owner, event, or receipt.
func (s *Store) admitGateExemptionRepair(ctx context.Context, command gateExemptionRepairCommand, proof []byte, peer tls.ConnectionState, at time.Time) (gateExemptionRepairAdmission, error) {
	return s.admitGateRepair(ctx, command, proof, peer, at, time.Time{}, false)
}

func (s *Store) admitGateRepair(ctx context.Context, command gateExemptionRepairCommand, proof []byte, peer tls.ConnectionState, at, deadline time.Time, journal bool) (gateExemptionRepairAdmission, error) {
	var out gateExemptionRepairAdmission
	canonical, hash, err := command.canonicalBytes()
	if err != nil || len(proof) > 1<<20 || at.IsZero() {
		return out, ErrInvalidProof
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.db == nil {
		return out, errors.New("authoritystore: closed")
	}
	if err = checkStep4State(s.db); err != nil {
		return out, fmt.Errorf("%w: authority history invalid: %v", ErrInvalidStore, err)
	}
	if err = checkStep16State(s.db); err != nil {
		return out, fmt.Errorf("%w: claim journal boundary history invalid: %v", ErrInvalidStore, err)
	}
	if err = checkStep13State(s.db); err != nil {
		return out, fmt.Errorf("%w: signed gate history invalid: %v", ErrInvalidStore, err)
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return out, err
	}
	defer func() { _ = tx.Rollback() }()
	domain, err := domainOwner(ctx, tx, command.DomainID)
	if err != nil {
		return out, err
	}
	if domain.ActiveEpoch != command.AuthorityEpoch {
		return out, ErrFenced
	}
	if _, err = verifyPeer(ctx, tx, command.DomainID, command.EnvironmentID, command.AuthorityEpoch, peer, at); err != nil {
		return out, err
	}
	stored, err := readGateExemptionRepairAdmission(ctx, tx, command.DomainID, command.ID)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return out, err
	}
	if err == nil {
		if stored.RequestHash != hash || !bytes.Equal(stored.Command, canonical) || len(proof) != 0 && !bytes.Equal(stored.Proof, proof) {
			return out, ErrConflict
		}
		if journal {
			public, linkErr := gateRepairJournalLinked(ctx, tx, stored)
			if linkErr != nil || !public {
				return out, ErrConflict
			}
		}
		if terminal, terminalErr := readGateExemptionRepairTerminal(ctx, tx, command.DomainID, command.ID); terminalErr == nil {
			if err = validateStoredGateExemptionRepairAdmissionCore(ctx, tx, stored); err != nil {
				return out, err
			}
			if err = validateGateExemptionRepairTerminal(ctx, tx, stored, terminal); err != nil {
				return out, err
			}
			stored.Terminal = &terminal
		} else if !errors.Is(terminalErr, sql.ErrNoRows) {
			return out, terminalErr
		}
		return stored, nil
	}
	if len(proof) == 0 {
		return out, ErrInvalidProof
	}
	var member int
	if err = tx.QueryRowContext(ctx, `SELECT count(*) FROM repo_memberships WHERE domain_id=? AND repo_id=?`, command.DomainID, command.RepoID).Scan(&member); err != nil {
		return out, err
	}
	if member != 1 {
		return out, ErrFenced
	}
	if err = validateGateClaimTx(ctx, tx, command.claimCommand()); err != nil {
		return out, err
	}
	var head uint64
	if err = tx.QueryRowContext(ctx, `SELECT sequence_head FROM environments WHERE domain_id=? AND environment_id=?`, command.DomainID, command.EnvironmentID).Scan(&head); err != nil {
		return out, err
	}
	if command.EnvironmentSequence != head+1 {
		return out, ErrPending
	}
	var collision int
	if err = tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM submissions WHERE domain_id=? AND (command_id=? OR (environment_id=? AND environment_sequence=?)))`,
		command.DomainID, command.ID, command.EnvironmentID, command.EnvironmentSequence).Scan(&collision); err != nil {
		return out, err
	}
	if collision != 0 {
		return out, ErrConflict
	}
	if err = verifyGateExemptionRepairBoundary(ctx, tx, command, true); err != nil {
		return out, err
	}
	verified, err := parseGateExemptionRepairAuthorization(proof, domain, command.binding(hash), at)
	if err != nil {
		return out, err
	}
	verifiedAt := at.UTC().Format(time.RFC3339Nano)
	if _, err = tx.ExecContext(ctx, `INSERT INTO gate_exemption_repair_nonces(domain_id,nonce,command_id) VALUES(?,?,?)`,
		command.DomainID, verified.nonce, command.ID); err != nil {
		return out, writeError(err)
	}
	_, err = tx.ExecContext(ctx, `INSERT INTO gate_exemption_repair_admissions(
		domain_id,command_id,request_hash,command,authority_epoch,environment_id,environment_sequence,
		repo_id,node_id,gate,declaration_event_count,declaration_event_id,declaration_prefix_digest,nonce,proof,verified_at
	) VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`,
		command.DomainID, command.ID, hash, canonical, command.AuthorityEpoch, command.EnvironmentID, command.EnvironmentSequence,
		command.RepoID, command.NodeID, command.Gate, command.Boundary.EventCount, command.Boundary.HighWaterEvent,
		command.Boundary.PrefixDigest, verified.nonce, proof, verifiedAt)
	if err != nil {
		return out, writeError(err)
	}
	if journal {
		publicCommand, decodeErr := operation.DecodeCanonicalCommand(canonical)
		if decodeErr != nil {
			return out, ErrInvalidProof
		}
		if err = appendConnectedClaimJournalEntry(ctx, tx, publicCommand, canonical, hash); err != nil {
			return out, err
		}
		if err = ctx.Err(); err != nil {
			return out, err
		}
		if !deadline.IsZero() && !time.Now().Before(deadline) {
			return out, context.DeadlineExceeded
		}
	}
	if err = tx.Commit(); err != nil {
		return out, err
	}
	return gateExemptionRepairAdmission{
		DomainID: command.DomainID, CommandID: command.ID, RequestHash: hash,
		Command: append([]byte(nil), canonical...), Proof: append([]byte(nil), proof...),
		AuthorityEpoch: command.AuthorityEpoch, EnvironmentID: command.EnvironmentID,
		EnvironmentSequence: command.EnvironmentSequence, RepoID: command.RepoID, NodeID: command.NodeID,
		Gate: command.Gate, Boundary: command.Boundary, Nonce: append([]byte(nil), verified.nonce...), VerifiedAt: verifiedAt,
	}, nil
}

// recoverGateExemptionRepair validates and reclaims the same internal retry
// identity. Omitted proof is accepted only because the exact admitted bytes are
// loaded locally; a supplied proof must be byte-identical.
func (s *Store) recoverGateExemptionRepair(ctx context.Context, canonical []byte, hash string, suppliedProof []byte) (gateExemptionRepairAdmission, error) {
	var out gateExemptionRepairAdmission
	command, err := decodeGateExemptionRepairCommand(canonical, hash)
	if err != nil || len(suppliedProof) > 1<<20 {
		return out, ErrInvalidProof
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.db == nil {
		return out, errors.New("authoritystore: closed")
	}
	if err = checkStep4State(s.db); err != nil {
		return out, fmt.Errorf("%w: authority history invalid: %v", ErrInvalidStore, err)
	}
	if err = checkStep16State(s.db); err != nil {
		return out, fmt.Errorf("%w: claim journal boundary history invalid: %v", ErrInvalidStore, err)
	}
	if err = checkStep13State(s.db); err != nil {
		return out, fmt.Errorf("%w: signed gate history invalid: %v", ErrInvalidStore, err)
	}
	tx, err := s.db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return out, err
	}
	defer func() { _ = tx.Rollback() }()
	stored, err := readGateExemptionRepairAdmission(ctx, tx, command.DomainID, command.ID)
	if err != nil {
		return out, err
	}
	if stored.RequestHash != hash || !bytes.Equal(stored.Command, canonical) ||
		len(suppliedProof) != 0 && !bytes.Equal(stored.Proof, suppliedProof) {
		return out, ErrConflict
	}
	if err = validateStoredGateExemptionRepairAdmissionCore(ctx, tx, stored); err != nil {
		return out, err
	}
	if terminal, terminalErr := readGateExemptionRepairTerminal(ctx, tx, stored.DomainID, stored.CommandID); terminalErr == nil {
		if err = validateGateExemptionRepairTerminal(ctx, tx, stored, terminal); err != nil {
			return out, err
		}
		stored.Terminal = &terminal
	} else if errors.Is(terminalErr, sql.ErrNoRows) {
		if err = validatePendingGateExemptionRepairAdmission(ctx, tx, stored); err != nil {
			return out, err
		}
	} else {
		return out, terminalErr
	}
	return stored, nil
}

func readGateExemptionRepairAdmission(ctx context.Context, queryer gateRepairQueryer, domain, command string) (gateExemptionRepairAdmission, error) {
	var stored gateExemptionRepairAdmission
	var count int64
	err := queryer.QueryRowContext(ctx, `SELECT domain_id,command_id,request_hash,command,authority_epoch,environment_id,environment_sequence,
		repo_id,node_id,gate,declaration_event_count,declaration_event_id,declaration_prefix_digest,nonce,proof,verified_at
		FROM gate_exemption_repair_admissions WHERE domain_id=? AND command_id=?`, domain, command).Scan(
		&stored.DomainID, &stored.CommandID, &stored.RequestHash, &stored.Command, &stored.AuthorityEpoch,
		&stored.EnvironmentID, &stored.EnvironmentSequence, &stored.RepoID, &stored.NodeID, &stored.Gate,
		&count, &stored.Boundary.HighWaterEvent, &stored.Boundary.PrefixDigest, &stored.Nonce, &stored.Proof, &stored.VerifiedAt)
	if err != nil {
		return stored, err
	}
	if count <= 0 {
		return stored, ErrInvalidStore
	}
	stored.Boundary.EventCount = uint64(count)
	stored.Command = append([]byte(nil), stored.Command...)
	stored.Proof = append([]byte(nil), stored.Proof...)
	stored.Nonce = append([]byte(nil), stored.Nonce...)
	return stored, nil
}

func validateStoredGateExemptionRepairAdmission(ctx context.Context, queryer gateRepairQueryer, stored gateExemptionRepairAdmission) error {
	if err := validateStoredGateExemptionRepairAdmissionCore(ctx, queryer, stored); err != nil {
		return err
	}
	return validatePendingGateExemptionRepairAdmission(ctx, queryer, stored)
}

func validateStoredGateExemptionRepairAdmissionCore(ctx context.Context, queryer gateRepairQueryer, stored gateExemptionRepairAdmission) error {
	command, err := decodeGateExemptionRepairCommand(stored.Command, stored.RequestHash)
	if err != nil || command.ID != stored.CommandID || command.DomainID != stored.DomainID ||
		command.AuthorityEpoch != stored.AuthorityEpoch || command.EnvironmentID != stored.EnvironmentID ||
		command.EnvironmentSequence != stored.EnvironmentSequence || command.RepoID != stored.RepoID ||
		command.NodeID != stored.NodeID || command.Gate != stored.Gate || command.Boundary != stored.Boundary {
		return fmt.Errorf("%w: repair admission identity mismatch", ErrInvalidStore)
	}
	domain, err := gateRepairDomain(ctx, queryer, stored.DomainID)
	if err != nil || stored.AuthorityEpoch > domain.ActiveEpoch {
		return fmt.Errorf("%w: repair admission domain epoch mismatch", ErrInvalidStore)
	}
	domain.ActiveEpoch = stored.AuthorityEpoch
	verifiedAt, err := utcTime(stored.VerifiedAt)
	if err != nil {
		return fmt.Errorf("%w: repair admission verification time invalid", ErrInvalidStore)
	}
	verified, err := parseGateExemptionRepairAuthorization(stored.Proof, domain, command.binding(stored.RequestHash), verifiedAt)
	if err != nil || !bytes.Equal(verified.nonce, stored.Nonce) {
		return fmt.Errorf("%w: retained repair proof invalid", ErrInvalidStore)
	}
	if err = verifyGateExemptionRepairBoundary(ctx, queryer, command, false); err != nil {
		return fmt.Errorf("%w: retained repair declaration boundary invalid: %v", ErrInvalidStore, err)
	}
	return nil
}

func validatePendingGateExemptionRepairAdmission(ctx context.Context, queryer gateRepairQueryer, stored gateExemptionRepairAdmission) error {
	var head uint64
	if err := queryer.QueryRowContext(ctx, `SELECT sequence_head FROM environments WHERE domain_id=? AND environment_id=?`, stored.DomainID, stored.EnvironmentID).Scan(&head); err != nil || stored.EnvironmentSequence != head+1 {
		return fmt.Errorf("%w: repair admission sequence is not pending", ErrInvalidStore)
	}
	var collision int
	if err := queryer.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM submissions WHERE domain_id=? AND (command_id=? OR (environment_id=? AND environment_sequence=?)))`,
		stored.DomainID, stored.CommandID, stored.EnvironmentID, stored.EnvironmentSequence).Scan(&collision); err != nil || collision != 0 {
		return fmt.Errorf("%w: repair admission conflicts with submission", ErrInvalidStore)
	}
	return nil
}

func gateRepairDomain(ctx context.Context, queryer gateRepairQueryer, id string) (Domain, error) {
	var domain Domain
	var epoch int64
	err := queryer.QueryRowContext(ctx, `SELECT domain_id,owner_public_key,owner_key_id,active_epoch FROM domains WHERE domain_id=?`, id).Scan(
		&domain.ID, &domain.OwnerPublicKey, &domain.OwnerKeyID, &epoch)
	if err != nil {
		return domain, err
	}
	if epoch <= 0 {
		return domain, ErrInvalidStore
	}
	domain.ActiveEpoch = uint64(epoch)
	return domain, nil
}

func verifyGateExemptionRepairBoundary(ctx context.Context, queryer gateRepairQueryer, command gateExemptionRepairCommand, requireMissing bool) error {
	if requireMissing {
		if _, _, err := gateSubjectTx(ctx, queryer.(*sql.Tx), command.DomainID, command.RepoID, command.Gate, command.NodeID, command.ClaimID); err != nil {
			return ErrInvalidProof
		}
	}
	var projectedScale, projectedEvent string
	err := queryer.QueryRowContext(ctx, `SELECT scale,declaration_event_id FROM m6_gate_declarations WHERE domain_id=? AND repo_id=? AND gate=?`,
		command.DomainID, command.RepoID, command.Gate).Scan(&projectedScale, &projectedEvent)
	if err != nil || projectedEvent != command.Boundary.HighWaterEvent {
		return ErrInvalidProof
	}
	var anchor PrefixAnchor
	err = queryer.QueryRowContext(ctx, `SELECT position,event_id,prefix_digest FROM authority_events WHERE domain_id=? AND position=?`,
		command.DomainID, command.Boundary.EventCount).Scan(&anchor.EventCount, &anchor.EventID, &anchor.Digest)
	if err != nil || anchor.EventID != command.Boundary.HighWaterEvent || anchor.Digest != command.Boundary.PrefixDigest {
		return ErrInvalidProof
	}
	var position uint64
	var eventID, eventCommand string
	var record []byte
	err = queryer.QueryRowContext(ctx, `SELECT position,event_id,command_id,record FROM authority_events WHERE domain_id=? AND event_id=?`,
		command.DomainID, projectedEvent).Scan(&position, &eventID, &eventCommand, &record)
	if err != nil || position != command.Boundary.EventCount {
		return ErrInvalidProof
	}
	event, err := parseStep12Event(record, command.DomainID, position, eventID, eventCommand)
	if err != nil || event.kind != "gate.declared" || event.subject != command.RepoID || event.repo != command.RepoID {
		return ErrInvalidProof
	}
	var payload struct {
		Gate   string   `cbor:"gate"`
		Scale  string   `cbor:"scale"`
		Exempt []string `cbor:"exempt"`
	}
	if !step13ClosedPayload(event.payload, &payload, []string{"gate", "scale"}, []string{"exempt"}) ||
		payload.Gate != command.Gate || payload.Scale != projectedScale {
		return ErrInvalidProof
	}
	var kind, repo string
	var birthPosition uint64
	var tombstonePosition sql.NullInt64
	err = queryer.QueryRowContext(ctx, `SELECT n.kind,n.repo_id,b.position,t.position
		FROM m6_nodes n JOIN authority_events b ON b.domain_id=n.domain_id AND b.event_id=n.birth_event_id
		LEFT JOIN authority_events t ON t.domain_id=n.domain_id AND t.event_id=n.tombstone_event_id
		WHERE n.domain_id=? AND n.node_id=?`, command.DomainID, command.NodeID).Scan(&kind, &repo, &birthPosition, &tombstonePosition)
	if err != nil || kind != payload.Scale || repo != command.RepoID || birthPosition >= command.Boundary.EventCount ||
		tombstonePosition.Valid && uint64(tombstonePosition.Int64) <= command.Boundary.EventCount {
		return ErrInvalidProof
	}
	for _, exempt := range payload.Exempt {
		if exempt == command.NodeID {
			return ErrInvalidProof
		}
	}
	if requireMissing {
		var state sql.NullString
		err = queryer.QueryRowContext(ctx, `SELECT state FROM m6_gate_states WHERE domain_id=? AND node_id=? AND gate=?`,
			command.DomainID, command.NodeID, command.Gate).Scan(&state)
		if err != nil && !errors.Is(err, sql.ErrNoRows) {
			return err
		}
		if state.Valid && state.String != "exempt" {
			return ErrInvalidProof
		}
	}
	return nil
}

func checkStep14State(db *sql.DB) error {
	tx, err := db.BeginTx(context.Background(), &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	rows, err := tx.Query(`SELECT domain_id,command_id,request_hash,command,authority_epoch,environment_id,environment_sequence,
		repo_id,node_id,gate,declaration_event_count,declaration_event_id,declaration_prefix_digest,nonce,proof,verified_at
		FROM gate_exemption_repair_admissions ORDER BY domain_id,command_id`)
	if err != nil {
		return err
	}
	var admissions []gateExemptionRepairAdmission
	for rows.Next() {
		var stored gateExemptionRepairAdmission
		var count int64
		if err = rows.Scan(&stored.DomainID, &stored.CommandID, &stored.RequestHash, &stored.Command, &stored.AuthorityEpoch,
			&stored.EnvironmentID, &stored.EnvironmentSequence, &stored.RepoID, &stored.NodeID, &stored.Gate,
			&count, &stored.Boundary.HighWaterEvent, &stored.Boundary.PrefixDigest, &stored.Nonce, &stored.Proof, &stored.VerifiedAt); err != nil {
			_ = rows.Close()
			return err
		}
		if count <= 0 {
			_ = rows.Close()
			return ErrInvalidStore
		}
		stored.Boundary.EventCount = uint64(count)
		admissions = append(admissions, stored)
	}
	if err = rows.Err(); err != nil {
		_ = rows.Close()
		return err
	}
	if err = rows.Close(); err != nil {
		return err
	}
	var orphanedNonces int
	if err = tx.QueryRow(`SELECT count(*) FROM gate_exemption_repair_nonces n
		LEFT JOIN gate_exemption_repair_admissions a ON a.domain_id=n.domain_id AND a.command_id=n.command_id
		WHERE a.domain_id IS NULL OR a.nonce!=n.nonce`).Scan(&orphanedNonces); err != nil || orphanedNonces != 0 {
		return fmt.Errorf("%w: orphaned repair nonce reservation", ErrInvalidStore)
	}
	for _, stored := range admissions {
		terminal, terminalErr := readGateExemptionRepairTerminal(context.Background(), tx, stored.DomainID, stored.CommandID)
		if terminalErr == nil {
			if err = validateStoredGateExemptionRepairAdmissionCore(context.Background(), tx, stored); err != nil {
				return err
			}
			if err = validateGateExemptionRepairTerminal(context.Background(), tx, stored, terminal); err != nil {
				return err
			}
		} else if errors.Is(terminalErr, sql.ErrNoRows) {
			if err = validateStoredGateExemptionRepairAdmission(context.Background(), tx, stored); err != nil {
				return err
			}
		} else {
			return terminalErr
		}
	}
	return tx.Commit()
}
