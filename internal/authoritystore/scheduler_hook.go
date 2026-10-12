package authoritystore

import (
	"context"
	"crypto/tls"
	"database/sql"
	"time"

	"github.com/procrastivity/wip/internal/operation"
	"github.com/procrastivity/wip/internal/schedulerhook"
)

func (s *Store) submitSchedulerHook(ctx context.Context, command operation.Command, canonical []byte, hash string,
	peer tls.ConnectionState, at, deadline time.Time, checkContext bool,
) (CommandStatus, error) {
	if operation.SchedulerOrchestrateV1.ValidateRequest(command.Request) != nil || command.Request.Context.Repo == "" ||
		command.Request.Context.Clone == "" || command.Request.Context.Worktree == "" {
		return CommandStatus{}, ErrInvalidProof
	}
	input := command.Request.Input.(operation.SchedulerHookInput)
	if input.Kind != "ask" && command.Request.Claim == nil || input.Kind == "ask" && command.Request.Claim != nil {
		return CommandStatus{}, ErrInvalidProof
	}
	before := func(tx *sql.Tx) error {
		if err := validateSchedulerPrefixTx(ctx, tx, command, input); err != nil {
			return err
		}
		if err := validateSchedulerHookContextTx(ctx, tx, command, input); err != nil {
			return err
		}
		if err := validateSchedulerHookTargetTx(ctx, tx, command, input); err != nil {
			return err
		}
		if err := validateSchedulerHookUnitTx(ctx, tx, command, input); err != nil {
			return err
		}
		if input.Kind == "ask" {
			return nil
		}
		if err := validateStep4ClaimTx(ctx, tx, command); err != nil {
			return err
		}
		valid, err := validateTargetClaimsTx(ctx, tx, command, []string{input.MatterID})
		if err != nil {
			return err
		}
		if !valid {
			return ErrFenced
		}
		var dispatch string
		if err = tx.QueryRowContext(ctx, `SELECT dispatch_id FROM claims WHERE domain_id=? AND claim_id=?`,
			command.AuthorityDomainID, command.Request.Claim.ID).Scan(&dispatch); err != nil || dispatch != input.DispatchID {
			return ErrFenced
		}
		for _, blob := range command.Request.Blobs {
			var size uint64
			var verified int
			if err = tx.QueryRowContext(ctx, `SELECT byte_length,verified FROM blob_products WHERE domain_id=? AND digest=?`,
				command.AuthorityDomainID, blob.Digest).Scan(&size, &verified); err != nil || verified != 1 || size != uint64(blob.Size) {
				return ErrBlobAbsent
			}
		}
		if input.Kind == "work" && input.Role == "verifier" && input.Revision == operation.SchedulerStep10VerifierRevision {
			if _, err := selectStep10SourceRevisionTx(ctx, tx, s.blobs, command.AuthorityDomainID, command); err != nil {
				return ErrInvalidProof
			}
		}
		return nil
	}
	var beforeCommit func() error
	if checkContext {
		beforeCommit = func() error {
			if err := ctx.Err(); err != nil {
				return err
			}
			if !deadline.IsZero() && !time.Now().Before(deadline) {
				return context.DeadlineExceeded
			}
			return nil
		}
	}
	return s.submitIdentity(ctx, commandIdentity{
		domain: command.AuthorityDomainID, epoch: command.ExpectedAuthorityEpoch,
		environment: command.EnvironmentID, sequence: command.EnvironmentSequence,
		id: command.ID, name: command.Request.Operation.Name, version: uint64(command.Request.Operation.Version),
		repo: command.Request.Context.Repo, encoded: canonical, hash: hash, m1: &command,
	}, peer, at, before, beforeCommit, nil)
}

func validateSchedulerHookTargetTx(ctx context.Context, tx *sql.Tx, command operation.Command, input operation.SchedulerHookInput) error {
	if input.Kind != "work" {
		return nil
	}
	var kind, matter string
	var tombstone sql.NullString
	err := tx.QueryRowContext(ctx, `SELECT kind,matter_id,tombstone_event_id FROM m6_nodes WHERE domain_id=? AND node_id=?`,
		command.AuthorityDomainID, input.TargetID).Scan(&kind, &matter, &tombstone)
	if err != nil || kind != input.TargetKind || matter != input.MatterID || tombstone.Valid {
		return ErrFenced
	}
	return nil
}

// validateSchedulerHookUnitTx binds a scheduler work unit to its first
// authority command before the Environment can launch the adapter. The
// immutable submission is the durable binding; a new command ID cannot make a
// completed or uncertain unit look unstarted after restart.
func validateSchedulerHookUnitTx(ctx context.Context, tx *sql.Tx, command operation.Command, input operation.SchedulerHookInput) error {
	rows, err := tx.QueryContext(ctx, `SELECT command_id,command FROM submissions
		WHERE domain_id=? AND operation_name='scheduler.orchestrate' ORDER BY environment_sequence`, command.AuthorityDomainID)
	if err != nil {
		return err
	}
	defer func() { _ = rows.Close() }()
	for rows.Next() {
		var id string
		var raw []byte
		if err = rows.Scan(&id, &raw); err != nil {
			return err
		}
		if id == command.ID {
			continue
		}
		prior, decodeErr := operation.DecodeCanonicalCommand(raw)
		if decodeErr != nil {
			return ErrInvalidStore
		}
		priorInput, ok := prior.Request.Input.(operation.SchedulerHookInput)
		if !ok || !sameSchedulerWorkUnit(input, priorInput) {
			continue
		}
		return ErrConflict
	}
	return rows.Err()
}

func sameSchedulerWorkUnit(left, right operation.SchedulerHookInput) bool {
	if left.Kind != right.Kind || left.RunID != right.RunID ||
		left.MatterID != right.MatterID || left.TargetID != right.TargetID {
		return false
	}
	switch left.Kind {
	case "plan":
		// A Matter has one Plan unit in a Run. Role activation, claim, prefix,
		// and command IDs are attempt context, not permission to plan it twice.
		return true
	case "work":
		// A newly activated Builder/Verifier is a distinct work pass. Retrying
		// the same pass under the same Role must retain its original command.
		return left.Role == right.Role && left.RoleID == right.RoleID && left.Revision == right.Revision
	case "ask":
		return left.Reason == right.Reason
	default:
		return false
	}
}

func retainSchedulerHookEvidenceTx(ctx context.Context, tx *sql.Tx, blobDir, domain string, position uint64, refs []string) error {
	if len(refs) > 128 {
		return ErrResourceLimit
	}
	seen := make(map[string]bool, len(refs))
	for _, digest := range refs {
		if !validDigest(digest) || seen[digest] {
			return ErrInvalidProof
		}
		seen[digest] = true
		var length uint64
		var verified int
		if err := tx.QueryRowContext(ctx, `SELECT byte_length,verified FROM blob_products WHERE domain_id=? AND digest=?`,
			domain, digest).Scan(&length, &verified); err != nil || verified != 1 {
			return ErrBlobAbsent
		}
		if err := verifyProduct(blobDir, digest, length); err != nil {
			return ErrInvalidStore
		}
		if _, err := tx.ExecContext(ctx, `INSERT INTO blob_references(domain_id,digest,first_position)
			VALUES(?,?,?) ON CONFLICT(domain_id,digest) DO NOTHING`, domain, digest, position); err != nil {
			return err
		}
		var first uint64
		if err := tx.QueryRowContext(ctx, `SELECT first_position FROM blob_references WHERE domain_id=? AND digest=?`,
			domain, digest).Scan(&first); err != nil || first > position {
			return ErrInvalidStore
		}
	}
	return nil
}

func validateSchedulerPrefixTx(ctx context.Context, tx *sql.Tx, command operation.Command, input operation.SchedulerHookInput) error {
	anchor, err := anchorAt(ctx, tx, command.AuthorityDomainID, input.InstalledPrefix.EventCount)
	if err != nil || anchor.Digest != input.InstalledPrefix.Digest ||
		(anchor.EventID == "") != (input.InstalledPrefix.EventID == nil) ||
		anchor.EventID != "" && anchor.EventID != *input.InstalledPrefix.EventID {
		return ErrFenced
	}
	return nil
}

func validateSchedulerHookResult(command operation.Command, result operation.Result) (schedulerhook.Result, error) {
	output, ok := result.Output.(operation.SchedulerHookOutput)
	if !ok || result.Code != operation.ResultSucceeded || result.Problem != nil ||
		operation.SchedulerOrchestrateV1.ValidateResult(result) != nil {
		return schedulerhook.Result{}, ErrInvalidProof
	}
	input, ok := command.Request.Input.(operation.SchedulerHookInput)
	if !ok || output.Kind != input.Kind || output.Version != input.Version {
		return schedulerhook.Result{}, ErrInvalidProof
	}
	request, err := schedulerhook.RequestFromCommand(command)
	if err != nil {
		return schedulerhook.Result{}, ErrInvalidProof
	}
	inputDigest, err := schedulerhook.InputDigest(request)
	if err != nil || inputDigest != output.InputDigest {
		return schedulerhook.Result{}, ErrInvalidProof
	}
	typed, err := schedulerhook.TypedFromOperationOutput(output)
	if err != nil {
		return schedulerhook.Result{}, ErrInvalidProof
	}
	if typed.Kind != schedulerhook.Kind(input.Kind) || typed.Version != uint16(input.Version) {
		return schedulerhook.Result{}, ErrInvalidProof
	}
	if typed.Work != nil && typed.Work.Revision != input.Revision {
		return schedulerhook.Result{}, ErrInvalidProof
	}
	key := schedulerhook.Key{DomainID: command.AuthorityDomainID, CommandID: command.ID}
	resultDigest, err := schedulerhook.ResultDigest(key, inputDigest, typed)
	if err != nil || resultDigest != output.ResultDigest {
		return schedulerhook.Result{}, ErrInvalidProof
	}
	return schedulerhook.Result{Key: key, InputDigest: inputDigest, Typed: typed, ResultDigest: resultDigest}, nil
}

func selectStep10SourceRevisionTx(ctx context.Context, tx *sql.Tx, blobDir, domain string,
	command operation.Command,
) (schedulerhook.SelectedSourceRevision, error) {
	request, err := schedulerhook.RequestFromCommand(command)
	if err != nil || request.Kind != schedulerhook.KindWork || request.Role != "verifier" ||
		request.Revision != operation.SchedulerStep10VerifierRevision {
		return schedulerhook.SelectedSourceRevision{}, ErrInvalidProof
	}
	for index := range request.Inputs {
		input := &request.Inputs[index]
		var size uint64
		var verified int
		if err = tx.QueryRowContext(ctx, `SELECT byte_length,verified FROM blob_products WHERE domain_id=? AND digest=?`,
			domain, input.Digest).Scan(&size, &verified); err != nil || verified != 1 || size != uint64(input.Size) || size > 4<<20 {
			return schedulerhook.SelectedSourceRevision{}, ErrInvalidProof
		}
		input.Bytes, err = readBoundedVerifiedProduct(blobDir, input.Digest, size)
		if err != nil {
			return schedulerhook.SelectedSourceRevision{}, err
		}
	}
	revision, err := schedulerhook.SelectStep10SourceRevision(request, domain)
	if err != nil || revision.ProtocolRevision != request.Revision || revision.CandidateRevision == "" ||
		revision.CandidateManifestSHA256 == "" || revision.CompleteTreeSHA256 == "" || revision.SnapshotSHA256 == "" {
		return schedulerhook.SelectedSourceRevision{}, ErrInvalidProof
	}
	return revision, nil
}
