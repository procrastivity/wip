package authoritystore

import (
	"bytes"
	"context"
	"crypto/tls"
	"database/sql"
	"errors"
	"time"

	"github.com/procrastivity/wip/internal/operation"
)

// SubmitGateExemptionRepairV2 is the detached admission seam for an adapter
// that has already selected command-submit/2. Generic SubmitCommand never
// accepts repair. Nonce/proof/time and the claim-journal entry commit together.
func (s *Store) SubmitGateExemptionRepairV2(ctx context.Context, command operation.Command, hash string, proof []byte, peer tls.ConnectionState, at, deadline time.Time) (CommandStatus, error) {
	if command.Request.Operation != operation.GateExemptionRepairV1.Metadata().Operation {
		return CommandStatus{}, ErrInvalidProof
	}
	canonical, err := command.CanonicalBytes()
	if err != nil {
		return CommandStatus{}, ErrInvalidProof
	}
	private, err := decodeGateExemptionRepairCommand(canonical, hash)
	if err != nil {
		return CommandStatus{}, err
	}
	if _, err = s.admitGateRepair(ctx, private, proof, peer, at, deadline, true); err != nil {
		return CommandStatus{}, err
	}
	return s.QueryCommand(ctx, private.DomainID, private.ID, hash, private.AuthorityEpoch, peer, private.EnvironmentID, at)
}

// CompleteGateExemptionRepairV2 folds the accepted private terminal and public
// receipt in one transaction. A lost response or signing failure cannot replace
// the admitted authorization or leave a terminal without its exact receipt.
func (s *Store) CompleteGateExemptionRepairV2(ctx context.Context, command operation.Command, hash string, proof []byte, eventID string, occurred time.Time, sign Signer) (CommandStatus, error) {
	canonical, err := command.CanonicalBytes()
	if err != nil || sign == nil || command.Request.Operation != operation.GateExemptionRepairV1.Metadata().Operation {
		return CommandStatus{}, ErrInvalidProof
	}
	if _, err = s.completeGateRepair(ctx, canonical, hash, proof, []byte(eventID), occurred, sign); err != nil {
		return CommandStatus{}, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.db == nil {
		return CommandStatus{}, ErrInvalidStore
	}
	tx, err := s.db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return CommandStatus{}, err
	}
	defer func() { _ = tx.Rollback() }()
	return s.status(ctx, tx, command)
}

// The exact journal entry distinguishes public v2 admission from historical
// private-only work. Historical terminals are never silently re-admitted or
// assigned a new journal position or signed result.
func gateRepairJournalLinked(ctx context.Context, queryer gateRepairQueryer, admission gateExemptionRepairAdmission) (bool, error) {
	var command []byte
	var hash string
	var sequence uint64
	err := queryer.QueryRowContext(ctx, `SELECT command,request_hash,environment_sequence FROM claim_journal_entries WHERE domain_id=? AND command_id=?`,
		admission.DomainID, admission.CommandID).Scan(&command, &hash, &sequence)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	if !bytes.Equal(command, admission.Command) || hash != admission.RequestHash || sequence != admission.EnvironmentSequence {
		return false, ErrInvalidStore
	}
	return true, nil
}

func (s *Store) gateRepairStatusTx(ctx context.Context, tx *sql.Tx, domain, id, hash, environment string) (CommandStatus, error) {
	admission, err := readGateExemptionRepairAdmission(ctx, tx, domain, id)
	if errors.Is(err, sql.ErrNoRows) {
		return CommandStatus{}, ErrNotFound
	}
	if err != nil {
		return CommandStatus{}, err
	}
	public, err := gateRepairJournalLinked(ctx, tx, admission)
	if err != nil {
		return CommandStatus{}, err
	}
	if !public {
		return CommandStatus{}, ErrNotFound
	}
	if admission.RequestHash != hash {
		return CommandStatus{}, ErrConflict
	}
	if admission.EnvironmentID != environment {
		return CommandStatus{}, ErrFenced
	}
	if err = validateStoredGateExemptionRepairAdmissionCore(ctx, tx, admission); err != nil {
		return CommandStatus{}, err
	}
	terminal, err := readGateExemptionRepairTerminal(ctx, tx, domain, id)
	if errors.Is(err, sql.ErrNoRows) {
		if err = validatePendingGateExemptionRepairAdmission(ctx, tx, admission); err != nil {
			return CommandStatus{}, err
		}
		return CommandStatus{Pending: true}, nil
	}
	if err != nil {
		return CommandStatus{}, err
	}
	if err = validateGateExemptionRepairTerminal(ctx, tx, admission, terminal); err != nil {
		return CommandStatus{}, err
	}
	return s.status(ctx, tx, operation.Command{ID: id, AuthorityDomainID: domain})
}

func gateRepairResult(command gateExemptionRepairCommand, terminal gateExemptionRepairTerminal) (output, problem, accepted, first, last any, err error) {
	if terminal.ResultCode == "result.refused" {
		return nil, terminal.RefusalCode, nil, nil, nil, nil
	}
	output, err = artifactEncoder.Marshal(operation.GateExemptionRepairOutput{
		Gate: command.Gate, NodeID: command.NodeID, AlreadyExempt: terminal.EventID == "",
	})
	if terminal.EventID != "" {
		accepted = map[string]any{"first_event_id": terminal.EventID, "last_event_id": terminal.EventID, "event_count": uint64(1)}
		first, last = terminal.ObservedPosition+1, terminal.ObservedPosition+1
	}
	return
}

func validateGateRepairReceipt(ctx context.Context, tx *sql.Tx, admission gateExemptionRepairAdmission, terminal gateExemptionRepairTerminal, command gateExemptionRepairCommand) error {
	public, err := gateRepairJournalLinked(ctx, tx, admission)
	if err != nil {
		return err
	}
	var receipt, wrapper []byte
	var epoch, generation, sequence uint64
	var first, last sql.NullInt64
	var code string
	err = tx.QueryRowContext(ctx, `SELECT receipt,wrapper,artifact_epoch,artifact_generation,artifact_sequence,first_position,last_position,result_code FROM terminal_receipts WHERE domain_id=? AND command_id=?`,
		admission.DomainID, admission.CommandID).Scan(&receipt, &wrapper, &epoch, &generation, &sequence, &first, &last, &code)
	if !public && errors.Is(err, sql.ErrNoRows) {
		return nil
	}
	if !public || err != nil || epoch != admission.AuthorityEpoch || code != terminal.ResultCode {
		return ErrInvalidStore
	}
	output, problem, accepted, _, _, err := gateRepairResult(command, terminal)
	if err != nil {
		return err
	}
	expected, err := artifactEncoder.Marshal(map[string]any{
		"schema": "wipd.terminal-receipt/1", "domain_id": admission.DomainID, "authority_epoch": admission.AuthorityEpoch,
		"identity_schema": "wipd.command/1", "command_id": admission.CommandID, "request_hash": admission.RequestHash,
		"operation":   map[string]any{"name": gateExemptionRepairOperationName, "version": uint64(1)},
		"environment": map[string]any{"id": admission.EnvironmentID, "sequence": admission.EnvironmentSequence},
		"result":      map[string]any{"code": terminal.ResultCode, "output": output, "problem_code": problem}, "accepted_events": accepted,
	})
	if err != nil || !bytes.Equal(receipt, expected) || first.Valid != (terminal.EventID != "") || last.Valid != first.Valid ||
		first.Valid && (uint64(first.Int64) != terminal.ObservedPosition+1 || first.Int64 != last.Int64) {
		return ErrInvalidStore
	}
	var artifact signedArtifact
	if artifactDecoder.Unmarshal(wrapper, &artifact) != nil || artifact.Kind != "portable-receipt" || artifact.PayloadSchema != "wipd.terminal-receipt/1" ||
		artifact.Epoch != epoch || artifact.Generation == nil || *artifact.Generation != generation || artifact.Sequence == nil || *artifact.Sequence != sequence || !bytes.Equal(artifact.Payload, receipt) {
		return ErrInvalidStore
	}
	var retained []byte
	if err = tx.QueryRowContext(ctx, `SELECT wrapper FROM authority_artifacts WHERE domain_id=? AND epoch=? AND generation=? AND sequence=?`,
		admission.DomainID, epoch, generation, sequence).Scan(&retained); err != nil || !bytes.Equal(wrapper, retained) {
		return ErrInvalidStore
	}
	return nil
}
