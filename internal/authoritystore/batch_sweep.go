package authoritystore

import (
	"bytes"
	"context"
	"crypto/sha256"
	"crypto/tls"
	"database/sql"
	"encoding/binary"
	"errors"
	"strings"
	"time"

	"github.com/procrastivity/wip/internal/operation"
)

// sweepAnonymousBatch is an internal transaction seam, not command admission.
// claim_close is the authenticated closing Environment's installation assertion;
// only the Environment's ReleaseInstalledAnchor lookup can establish local
// installation. Here we revalidate its exact authoritative evidence and lineage.
// There is deliberately no registration, journal ACK, or new receipt schema.
func (s *Store) sweepAnonymousBatch(ctx context.Context, command operation.Command, hash string,
	peer tls.ConnectionState, occurred time.Time, eventID string, sign Signer,
) (CommandStatus, error) {
	return s.sweepAnonymousBatchWithDeadline(ctx, command, hash, peer, occurred, eventID, sign, time.Time{}, false)
}

// SweepAnonymousBatchWithDeadline is the explicit M6 authority admission seam
// for the candidate operation. It remains separate from generic SubmitCommand
// so M5/default admission cannot reach this transaction.
func (s *Store) SweepAnonymousBatchWithDeadline(ctx context.Context, command operation.Command, hash string,
	peer tls.ConnectionState, occurred time.Time, eventID string, sign Signer, deadline time.Time,
) (CommandStatus, error) {
	return s.sweepAnonymousBatchWithDeadline(ctx, command, hash, peer, occurred, eventID, sign, deadline, true)
}

func (s *Store) sweepAnonymousBatchWithDeadline(ctx context.Context, command operation.Command, hash string,
	peer tls.ConnectionState, occurred time.Time, eventID string, sign Signer, deadline time.Time, checkContext bool,
) (CommandStatus, error) {
	var empty CommandStatus
	if command.Request.Operation != operation.BatchSweepAnonymousV1.Metadata().Operation ||
		operation.BatchSweepAnonymousV1.ValidateRequest(command.Request) != nil ||
		operation.VerifyRequestHash(command, hash) != nil || occurred.IsZero() || sign == nil {
		return empty, ErrInvalidProof
	}
	if checkContext {
		if err := ctx.Err(); err != nil {
			return empty, err
		}
		if !deadline.IsZero() && !time.Now().Before(deadline) {
			return empty, context.DeadlineExceeded
		}
	}
	canonical, err := command.CanonicalBytes()
	if err != nil {
		return empty, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.db == nil {
		return empty, ErrInvalidStore
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return empty, err
	}
	defer func() { _ = tx.Rollback() }()
	d, err := domainOwner(ctx, tx, command.AuthorityDomainID)
	if err != nil {
		return empty, err
	}
	if d.ActiveEpoch != command.ExpectedAuthorityEpoch {
		return empty, ErrFenced
	}
	if _, err = verifyPeer(ctx, tx, d.ID, command.EnvironmentID, d.ActiveEpoch, peer, occurred); err != nil {
		return empty, err
	}
	var priorHash, state string
	var prior []byte
	err = tx.QueryRowContext(ctx, `SELECT request_hash,command,state FROM submissions WHERE domain_id=? AND command_id=?`, d.ID, command.ID).Scan(&priorHash, &prior, &state)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return empty, err
	}
	if err == nil {
		if priorHash != hash || !bytes.Equal(prior, canonical) {
			return empty, ErrConflict
		}
		// Never promote an incomplete/private historical row through replay.
		if state != "terminal" {
			return empty, ErrNotOwner
		}
		return s.status(ctx, tx, command)
	}
	var member int
	if err = tx.QueryRowContext(ctx, `SELECT count(*) FROM repo_memberships WHERE repo_id=? AND domain_id=?`, command.Request.Context.Repo, d.ID).Scan(&member); err != nil {
		return empty, err
	}
	if member != 1 {
		return empty, ErrFenced
	}
	var head uint64
	if err = tx.QueryRowContext(ctx, `SELECT sequence_head FROM environments WHERE domain_id=? AND environment_id=?`, d.ID, command.EnvironmentID).Scan(&head); err != nil {
		return empty, err
	}
	if command.EnvironmentSequence != head+1 {
		return empty, ErrPending
	}
	anchor, err := currentAnchor(ctx, tx, d.ID)
	if err != nil {
		return empty, err
	}
	result, err := batchSweepResultAt(ctx, tx, command, anchor.EventCount)
	if err != nil {
		return empty, err
	}
	if _, err = tx.ExecContext(ctx, `INSERT INTO submissions(domain_id,command_id,request_hash,command,epoch,environment_id,environment_sequence,operation_name,operation_version,state) VALUES(?,?,?,?,?,?,?,?,?,'submitted')`,
		d.ID, command.ID, hash, canonical, d.ActiveEpoch, command.EnvironmentID, command.EnvironmentSequence, command.Request.Operation.Name, command.Request.Operation.Version); err != nil {
		return empty, writeError(err)
	}
	c := commandIdentity{
		domain: d.ID, epoch: d.ActiveEpoch, environment: command.EnvironmentID,
		sequence: command.EnvironmentSequence, id: command.ID, name: command.Request.Operation.Name,
		version: uint64(command.Request.Operation.Version), repo: command.Request.Context.Repo, encoded: canonical, hash: hash,
	}
	boundary := func(receipt, _ []byte, _, _ uint64) error {
		if checkContext {
			if err := ctx.Err(); err != nil {
				return err
			}
			if !deadline.IsZero() && !time.Now().Before(deadline) {
				return context.DeadlineExceeded
			}
		}
		var outcome, problem any
		if result.Problem != nil {
			problem = string(result.Problem.Code)
		} else {
			outcome = string(result.Output.(operation.BatchSweepAnonymousOutput).Outcome)
		}
		var id any
		if anchor.EventCount > 0 {
			id = anchor.EventID
		}
		_, err := tx.ExecContext(ctx, `INSERT INTO batch_sweep_boundaries(domain_id,command_id,request_hash,terminal_receipt_digest,
			result_code,outcome,problem_code,event_count,event_id,prefix_digest) VALUES(?,?,?,?,?,?,?,?,?,?)`,
			c.domain, c.id, c.hash, digestBytes(receipt), string(result.Code), outcome, problem, anchor.EventCount, id, anchor.Digest)
		return err
	}
	if result.Problem != nil {
		return s.finishCommandTx(ctx, tx, c, head, string(result.Code), nil, string(result.Problem.Code), nil, nil, nil, occurred, sign, boundary)
	}
	outcome := result.Output.(operation.BatchSweepAnonymousOutput).Outcome
	output, err := artifactEncoder.Marshal(map[string]any{"outcome": string(outcome)})
	if err != nil {
		return empty, err
	}
	var first, last, accepted any
	if outcome == operation.BatchSweepAnonymousSwept {
		input := command.Request.Input.(operation.BatchSweepAnonymousInput)
		position, err := appendCommandEvent(ctx, tx, eventIdentity{
			d.ID, command.ID, hash, command.EnvironmentID,
			command.EnvironmentSequence, command.ActedAt, command.Request.Context.Repo,
		}, occurred, eventID, "batch.swept", input.BatchID, map[string]any{})
		if err != nil {
			return empty, err
		}
		first, last = position, position
		accepted = map[string]any{"first_event_id": eventID, "last_event_id": eventID, "event_count": uint64(1)}
	}
	return s.finishCommandTx(ctx, tx, c, head, string(result.Code), output, nil, accepted, first, last, occurred, sign, boundary)
}

func batchSweepResultAt(ctx context.Context, tx *sql.Tx, command operation.Command, position uint64) (operation.Result, error) {
	refuse := func(code operation.ProblemCode) (operation.Result, error) {
		return operation.Result{Code: operation.ResultRefused, Problem: &operation.Problem{Code: code, Message: string(code)}}, nil
	}
	input := command.Request.Input.(operation.BatchSweepAnonymousInput)
	nodes, err := step13NodesTx(ctx, tx)
	if err != nil {
		return operation.Result{}, err
	}
	node, found := nodes[ownerKey(command.AuthorityDomainID, input.MatterID)]
	if !found || node.node.kind != "matter" || node.birthPos > position || !step13NodeLiveAt(node, position) {
		return refuse(operation.ProblemBatchSweepTargetMissing)
	}
	events, err := batchSweepEventsAt(ctx, tx, command, position)
	if err != nil {
		if errors.Is(err, ErrInvalidProof) {
			return refuse(operation.ProblemBatchSweepClaimClose)
		}
		return operation.Result{}, err
	}
	batchMatter, batchExists, swept, dismissed := "", false, false, false
	lifecycle := "planned"
	for _, event := range events {
		if event.subject == input.BatchID {
			switch event.kind {
			case "batch.anonymous-created":
				batchExists = true
				if artifactDecoder.Unmarshal(event.payload["matter_id"], &batchMatter) != nil {
					return operation.Result{}, ErrInvalidStore
				}
			case "batch.created":
				batchExists = true // named/legacy batches are not this candidate
			case "batch.swept":
				swept = true // includes compatible legacy inline sweeps
			case "batch.dismissed":
				dismissed = true
			}
		}
		if event.subject == input.MatterID && lifecycleEventKind("matter", event.kind) {
			if artifactDecoder.Unmarshal(event.payload["to"], &lifecycle) != nil {
				return operation.Result{}, ErrInvalidStore
			}
		}
	}
	if !batchExists {
		return refuse(operation.ProblemBatchSweepTargetMissing)
	}
	if batchMatter != input.MatterID || node.node.repo != command.Request.Context.Repo || dismissed {
		return refuse(operation.ProblemBatchSweepNotEligible)
	}
	if err = batchSweepCloseEvidence(ctx, tx, command, position); err != nil {
		if errors.Is(err, ErrInvalidProof) || errors.Is(err, sql.ErrNoRows) || errors.Is(err, ErrPrefixMismatch) {
			return refuse(operation.ProblemBatchSweepClaimClose)
		}
		return operation.Result{}, err
	}
	if lifecycle != "done" {
		return refuse(operation.ProblemBatchSweepNotEligible)
	}
	var active int
	if err = tx.QueryRowContext(ctx, `SELECT
		(SELECT count(*) FROM claims c JOIN terminal_receipts a ON a.domain_id=c.domain_id AND a.command_id=c.acquire_command_id
		LEFT JOIN terminal_receipts r ON r.domain_id=c.domain_id AND r.command_id=c.close_command_id
		WHERE c.domain_id=? AND c.matter_id=? AND a.first_position<=? AND (r.last_position IS NULL OR r.last_position>?)) +
		(SELECT count(*) FROM birth_journals b JOIN terminal_receipts a ON a.domain_id=b.domain_id AND a.command_id=b.birth_command_id
		LEFT JOIN terminal_receipts r ON r.domain_id=b.domain_id AND r.command_id=b.release_command_id
		WHERE b.domain_id=? AND b.matter_id=? AND a.first_position<=? AND (r.last_position IS NULL OR r.last_position>?))`,
		command.AuthorityDomainID, input.MatterID, position, position, command.AuthorityDomainID, input.MatterID, position, position).Scan(&active); err != nil {
		return operation.Result{}, err
	}
	projection, err := deriveStep13Projection(nodes, events)
	if err != nil {
		return operation.Result{}, err
	}
	declarations := make(map[string]step13Declaration)
	states := make(map[string]step13GateState)
	for _, declaration := range projection.declarations {
		declarations[ownerKey(declaration.domain, declaration.repo)+"/"+declaration.gate] = declaration
	}
	for _, state := range projection.gateStates {
		states[ownerKey(state.domain, state.node)+"/"+state.gate] = state
	}
	if active != 0 || !step13Sealed(command.AuthorityDomainID, input.MatterID, nodes,
		map[string]string{ownerKey(command.AuthorityDomainID, input.MatterID): lifecycle}, declarations, states, position) {
		return refuse(operation.ProblemBatchSweepNotEligible)
	}
	if strings.HasPrefix(string(command.Request.Actor), "role:") || batchSweepOpenBrackets(events, input) {
		return refuse(operation.ProblemBatchSweepUnsupported)
	}
	outcome := operation.BatchSweepAnonymousSwept
	if swept {
		outcome = operation.BatchSweepAnonymousAlreadySwept
	}
	return operation.Result{Code: operation.ResultSucceeded, Output: operation.BatchSweepAnonymousOutput{Outcome: outcome}}, nil
}

func batchSweepEventsAt(ctx context.Context, tx *sql.Tx, command operation.Command, position uint64) ([]step13Event, error) {
	rows, err := tx.QueryContext(ctx, `SELECT position,event_id,command_id,record FROM authority_events WHERE domain_id=? AND position<=? ORDER BY position`, command.AuthorityDomainID, position)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	var events []step13Event
	for rows.Next() {
		var at uint64
		var id, owner string
		var raw []byte
		if err = rows.Scan(&at, &id, &owner, &raw); err != nil {
			return nil, err
		}
		event, err := parseStep12Event(raw, command.AuthorityDomainID, at, id, owner)
		if err != nil {
			if owner == command.Request.Input.(operation.BatchSweepAnonymousInput).ClaimClose.ReleaseCommandID {
				return nil, ErrInvalidProof
			}
			return nil, err
		}
		events = append(events, step13Event{step12Event: event})
	}
	return events, rows.Err()
}

// Fail closed on Step 10 brackets; no reap effects are implemented here.
func batchSweepOpenBrackets(events []step13Event, input operation.BatchSweepAnonymousInput) bool {
	runs, dispatches, roles := make(map[string]bool), make(map[string]bool), make(map[string]bool)
	text := func(event step13Event, key string) string {
		var value string
		_ = artifactDecoder.Unmarshal(event.payload[key], &value)
		return value
	}
	for _, event := range events {
		switch event.kind {
		case "run.started":
			if text(event, "batch_id") == input.BatchID || text(event, "batch") == input.BatchID {
				runs[event.subject] = true
			}
		case "run.finished", "run.stood-down":
			if _, related := runs[event.subject]; related {
				runs[event.subject] = false
			}
		case "dispatch.opened":
			_, relatedRun := runs[text(event, "run")]
			if text(event, "batch_id") == input.BatchID || text(event, "matter_id") == input.MatterID ||
				text(event, "matter") == input.MatterID || relatedRun {
				dispatches[event.subject] = true
			}
		case "dispatch.closed":
			if _, related := dispatches[event.subject]; related {
				dispatches[event.subject] = false
			}
		case "role.spawned":
			_, related := dispatches[text(event, "dispatch")]
			_, relatedID := dispatches[text(event, "dispatch_id")]
			if related || relatedID {
				roles[event.subject] = true
			}
		case "role.closed":
			delete(roles, event.subject)
		}
	}
	for _, bracket := range []map[string]bool{runs, dispatches, roles} {
		for _, open := range bracket {
			if open {
				return true
			}
		}
	}
	return false
}

func batchSweepCloseEvidence(ctx context.Context, tx *sql.Tx, command operation.Command, position uint64) error {
	input := command.Request.Input.(operation.BatchSweepAnonymousInput)
	ref := input.ClaimClose
	var canonical, raw, wrapper []byte
	var hash, environment, name, state, code string
	var epoch, sequence, version, artifactEpoch, generation, artifactSequence uint64
	var firstPosition, lastPosition sql.NullInt64
	if err := tx.QueryRowContext(ctx, `SELECT s.command,s.request_hash,s.environment_id,s.epoch,s.environment_sequence,s.operation_name,s.operation_version,s.state,
		r.receipt,r.wrapper,r.first_position,r.last_position,r.result_code,r.artifact_epoch,r.artifact_generation,r.artifact_sequence
		FROM submissions s JOIN terminal_receipts r USING(domain_id,command_id)
		WHERE s.domain_id=? AND s.command_id=?`, command.AuthorityDomainID, ref.ReleaseCommandID).
		Scan(&canonical, &hash, &environment, &epoch, &sequence, &name, &version, &state, &raw, &wrapper, &firstPosition, &lastPosition,
			&code, &artifactEpoch, &generation, &artifactSequence); err != nil {
		return err
	}
	if !firstPosition.Valid || !lastPosition.Valid || firstPosition.Int64 <= 0 || lastPosition.Int64 < firstPosition.Int64 {
		return ErrInvalidProof
	}
	first, last := uint64(firstPosition.Int64), uint64(lastPosition.Int64)
	c, err := parseLifecycle(canonical, hash)
	if err != nil || hash != ref.ReleaseRequestHash || digestBytes(raw) != ref.TerminalReceiptDigest ||
		name != "claim.release" || version != 1 || state != "terminal" || code != "result.succeeded" || artifactEpoch != epoch || epoch != command.ExpectedAuthorityEpoch ||
		environment != command.EnvironmentID || sequence >= command.EnvironmentSequence || c.domain != command.AuthorityDomainID ||
		c.id != ref.ReleaseCommandID || c.epoch != epoch || c.environment != environment || c.sequence != sequence ||
		c.repo != command.Request.Context.Repo || c.claimID != ref.ClaimID || c.claimEpoch != ref.ClaimEpoch ||
		c.barrier == nil || first == 0 || last < first || last > position {
		return ErrInvalidProof
	}
	r, err := readReceipt(raw)
	if err != nil || r.Domain != c.domain || r.Epoch != epoch || r.ID != c.id || r.Hash != hash ||
		r.Schema != "wipd.terminal-receipt/1" || r.Identity != "wipd.command/1" || r.Operation.Name != name || r.Operation.Version != 1 ||
		r.Environment.ID != environment || r.Environment.Sequence != sequence || r.Result.Code != "result.succeeded" ||
		r.Result.Problem != nil || r.Range == nil || r.Range.Count != last-first+1 {
		return ErrInvalidProof
	}
	var artifact signedArtifact
	if artifactDecoder.Unmarshal(wrapper, &artifact) != nil || artifact.Generation == nil || artifact.Sequence == nil ||
		*artifact.Generation != generation || *artifact.Sequence != artifactSequence || artifact.Epoch != artifactEpoch ||
		artifact.Kind != "portable-receipt" || artifact.PayloadSchema != "wipd.terminal-receipt/1" || !bytes.Equal(artifact.Payload, raw) {
		return ErrInvalidProof
	}
	var public, retained []byte
	var keyID, digest string
	var predecessor sql.NullString
	if err = tx.QueryRowContext(ctx, `SELECT k.public_key,k.key_id,a.wrapper,a.digest,a.predecessor
		FROM artifact_keys k JOIN authority_artifacts a ON a.domain_id=k.domain_id AND a.epoch=k.epoch AND a.generation=k.generation
		WHERE a.domain_id=? AND a.epoch=? AND a.generation=? AND a.sequence=?`, c.domain, epoch, *artifact.Generation, *artifact.Sequence).
		Scan(&public, &keyID, &retained, &digest, &predecessor); err != nil {
		return err
	}
	var previous *string
	if predecessor.Valid {
		previous = &predecessor.String
	}
	verified, err := verifyAuthorityArtifact(wrapper, public, c.domain, keyID, epoch, *artifact.Generation, *artifact.Sequence, previous)
	if err != nil || !bytes.Equal(wrapper, retained) || digest != verified {
		return ErrInvalidProof
	}
	var dispatch any
	if c.worktree == "" {
		var owner, repo, journalState string
		var release, barrier sql.NullString
		var claimEpoch uint64
		if err = tx.QueryRowContext(ctx, `SELECT owner_environment_id,repo_id,release_command_id,state,claim_epoch,barrier_digest FROM birth_journals
			WHERE domain_id=? AND matter_id=?`, c.domain, input.MatterID).Scan(&owner, &repo, &release, &journalState, &claimEpoch, &barrier); err != nil {
			return err
		}
		if ref.ClaimID != input.MatterID || ref.ClaimEpoch != 1 || claimEpoch != 1 || owner != environment || repo != c.repo ||
			!release.Valid || release.String != c.id || journalState != "released" || !barrier.Valid || barrier.String != c.barrier.Digest || first != last {
			return ErrInvalidProof
		}
	} else {
		var matter, batch, owner, worktree, kind, actor, dispatchID string
		var claimEpoch, authorityEpoch uint64
		var barrier []byte
		var release, reason, nonce sql.NullString
		if err = tx.QueryRowContext(ctx, `SELECT c.matter_id,c.batch_id,c.owner_environment_id,c.worktree_id,c.close_command_id,c.claim_epoch,c.authority_epoch,c.dispatch_id,
			x.kind,x.acting_environment_id,x.barrier,x.reason_digest,x.owner_nonce FROM claims c JOIN claim_closes x USING(domain_id,claim_id)
			WHERE c.domain_id=? AND c.claim_id=?`, c.domain, ref.ClaimID).
			Scan(&matter, &batch, &owner, &worktree, &release, &claimEpoch, &authorityEpoch, &dispatchID, &kind, &actor, &barrier, &reason, &nonce); err != nil {
			return err
		}
		if matter != input.MatterID || batch != input.BatchID || owner != environment || worktree != c.worktree ||
			!release.Valid || release.String != c.id || claimEpoch != ref.ClaimEpoch || authorityEpoch != epoch || kind != "release" || actor != environment ||
			reason.Valid || nonce.Valid || !bytes.Equal(barrier, mustRaw(mustRaw(canonical, "input"), "barrier")) || last != first+1 {
			return ErrInvalidProof
		}
		dispatch = dispatchID
	}
	output, err := artifactEncoder.Marshal(map[string]any{"claim_id": ref.ClaimID, "claim_epoch": ref.ClaimEpoch, "dispatch_id": dispatch, "barrier_digest": c.barrier.Digest})
	if err != nil || !bytes.Equal(output, r.Result.Output) {
		return ErrInvalidProof
	}
	for i := first; i <= last; i++ {
		var id string
		var record []byte
		if err = tx.QueryRowContext(ctx, `SELECT event_id,record FROM authority_events WHERE domain_id=? AND position=? AND command_id=?`, c.domain, i, c.id).Scan(&id, &record); err != nil {
			return err
		}
		event, err := parseStep12Event(record, c.domain, i, id, c.id)
		if err != nil || event.hash != hash || event.environment != environment || event.sequence != sequence || event.repo != c.repo || event.acted != c.actedAt ||
			i == first && id != r.Range.First || i == last && id != r.Range.Last {
			return ErrInvalidProof
		}
		kind, subject, payload := "claim.released", ref.ClaimID, output
		if i != last {
			kind, subject = "dispatch.closed", dispatch.(string)
			payload, _ = artifactEncoder.Marshal(map[string]any{"dispatch_id": dispatch, "claim_id": ref.ClaimID, "claim_epoch": ref.ClaimEpoch})
		}
		encoded, err := artifactEncoder.Marshal(event.payload)
		if err != nil || event.kind != kind || event.subject != subject || !bytes.Equal(encoded, payload) {
			return ErrInvalidProof
		}
	}
	var count uint64
	if err = tx.QueryRowContext(ctx, `SELECT count(*) FROM authority_events WHERE domain_id=? AND command_id=?`, c.domain, c.id).Scan(&count); err != nil || count != r.Range.Count {
		return ErrInvalidProof
	}
	anchor := PrefixAnchor{EventCount: ref.InstalledPrefixAnchor.EventCount, Digest: ref.InstalledPrefixAnchor.Digest}
	if ref.InstalledPrefixAnchor.EventID != nil {
		anchor.EventID = *ref.InstalledPrefixAnchor.EventID
	}
	if anchor.EventCount < last || anchor.EventCount > position {
		return ErrInvalidProof
	}
	return batchSweepVerifyPrefix(ctx, tx, c.domain, anchor)
}

func batchSweepVerifyPrefix(ctx context.Context, tx *sql.Tx, domain string, anchor PrefixAnchor) error {
	rows, err := tx.QueryContext(ctx, `SELECT position,event_id,record,prefix_digest FROM authority_events WHERE domain_id=? AND position<=? ORDER BY position`, domain, anchor.EventCount)
	if err != nil {
		return err
	}
	defer func() { _ = rows.Close() }()
	chain := sha256.Sum256([]byte("wipd/event-prefix/v1\x00"))
	var count uint64
	var previous string
	for rows.Next() {
		var position uint64
		var id, digest string
		var raw []byte
		if err = rows.Scan(&position, &id, &raw, &digest); err != nil {
			return err
		}
		if position != count+1 || !ulid.MatchString(id) || id <= previous {
			return ErrInvalidProof
		}
		var length [8]byte
		binary.BigEndian.PutUint64(length[:], uint64(len(raw)))
		h := sha256.New()
		_, _ = h.Write([]byte("wipd/event-prefix-step/v1\x00"))
		_, _ = h.Write(chain[:])
		_, _ = h.Write(length[:])
		_, _ = h.Write(raw)
		copy(chain[:], h.Sum(nil))
		if digest != digestRawBytes(chain[:]) {
			return ErrInvalidProof
		}
		count, previous = position, id
	}
	if err = rows.Err(); err != nil {
		return err
	}
	if count != anchor.EventCount || previous != anchor.EventID || digestRawBytes(chain[:]) != anchor.Digest {
		return ErrInvalidProof
	}
	return nil
}
