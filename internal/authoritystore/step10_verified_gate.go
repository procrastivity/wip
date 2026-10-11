package authoritystore

import (
	"context"
	"database/sql"
	"errors"
	"io"
	"os"
	"path/filepath"

	"github.com/procrastivity/wip/internal/operation"
	"github.com/procrastivity/wip/internal/schedulerhook"
)

func validateStep10VerifiedGateTx(ctx context.Context, tx *sql.Tx, blobDir string, gateCommand operation.Command, matterID string) error {
	commandID := gateCommand.CausationCommandID
	if commandID == "" || gateCommand.Request.Actor != "role:verifier" || gateCommand.Request.Claim == nil {
		return ErrInvalidProof
	}
	var canonical, rawReceipt []byte
	var state, environment string
	err := tx.QueryRowContext(ctx, `SELECT s.command,s.state,s.environment_id,r.receipt
		FROM submissions s JOIN terminal_receipts r USING(domain_id,command_id)
		WHERE s.domain_id=? AND s.command_id=? AND s.operation_name='scheduler.orchestrate' AND s.operation_version=1`,
		gateCommand.AuthorityDomainID, commandID).Scan(&canonical, &state, &environment, &rawReceipt)
	if err != nil {
		return ErrInvalidProof
	}
	verifierCommand, err := operation.DecodeCanonicalCommand(canonical)
	if err != nil || verifierCommand.ID != commandID || verifierCommand.AuthorityDomainID != gateCommand.AuthorityDomainID ||
		verifierCommand.ExpectedAuthorityEpoch != gateCommand.ExpectedAuthorityEpoch || verifierCommand.EnvironmentID != environment ||
		verifierCommand.EnvironmentID != gateCommand.EnvironmentID || verifierCommand.Request.Context.Repo != gateCommand.Request.Context.Repo ||
		verifierCommand.Request.Claim == nil || verifierCommand.Request.Claim.ID != gateCommand.Request.Claim.ID ||
		verifierCommand.Request.Claim.Epoch != gateCommand.Request.Claim.Epoch {
		return ErrInvalidProof
	}
	input, ok := verifierCommand.Request.Input.(operation.SchedulerHookInput)
	if !ok || input.Kind != "work" || input.Role != "verifier" || input.Revision != operation.SchedulerStep10VerifierRevision ||
		input.TargetKind != "matter" || input.TargetID != matterID || input.MatterID != matterID {
		return ErrInvalidProof
	}
	active, err := step10RolesAtTx(ctx, tx, gateCommand.AuthorityDomainID, input.RunID)
	if err != nil {
		return err
	}
	role, found := active[input.RoleID]
	if !found || role.name != "verifier" || role.runID != input.RunID || role.matter != matterID ||
		role.dispatch != input.DispatchID || role.claim != gateCommand.Request.Claim.ID ||
		role.claimEpoch != gateCommand.Request.Claim.Epoch {
		return ErrInvalidProof
	}
	if state != "terminal" {
		return ErrInvalidProof
	}
	receipt, err := readReceipt(rawReceipt)
	if err != nil || receipt.Domain != gateCommand.AuthorityDomainID || receipt.ID != commandID ||
		receipt.Environment.ID != verifierCommand.EnvironmentID || receipt.Operation.Name != operation.SchedulerOrchestrateV1.Metadata().Operation.Name ||
		receipt.Operation.Version != uint64(operation.SchedulerOrchestrateV1.Metadata().Operation.Version) ||
		receipt.Result.Code != string(operation.ResultSucceeded) || receipt.Result.Problem != nil {
		return ErrInvalidProof
	}
	var output operation.SchedulerHookOutput
	if artifactDecoder.Unmarshal(receipt.Result.Output, &output) != nil || output.Kind != "work" || output.Outcome != "verified" ||
		output.Revision != input.Revision || len(output.EvidenceRefs) != 1 {
		return ErrInvalidProof
	}
	if _, err = validateSchedulerHookResult(verifierCommand, operation.Result{Code: operation.ResultSucceeded, Output: output}); err != nil {
		return ErrInvalidProof
	}
	request, err := schedulerhook.RequestFromCommand(verifierCommand)
	if err != nil || len(request.Inputs) != 5 {
		return ErrInvalidProof
	}
	blobs := make(map[string]operation.BlobInput, len(verifierCommand.Request.Blobs))
	for _, blob := range verifierCommand.Request.Blobs {
		if _, exists := blobs[blob.Name]; exists {
			return ErrInvalidProof
		}
		blobs[blob.Name] = blob
	}
	for index, name := range []string{"model", "review", "snapshot", "tree-manifest", "workplan"} {
		blob, exists := blobs[name]
		if !exists || request.Inputs[index].Name != name || !validDigest(blob.Digest) || blob.Size < 0 || blob.Size > 4<<20 {
			return ErrInvalidProof
		}
		var size uint64
		var verified int
		if err = tx.QueryRowContext(ctx, `SELECT byte_length,verified FROM blob_products WHERE domain_id=? AND digest=?`,
			gateCommand.AuthorityDomainID, blob.Digest).Scan(&size, &verified); err != nil || verified != 1 || size != uint64(blob.Size) {
			return ErrInvalidProof
		}
		content, readErr := readBoundedVerifiedProduct(blobDir, blob.Digest, size)
		if readErr != nil {
			return readErr
		}
		request.Inputs[index].Bytes = content
	}
	rows, err := tx.QueryContext(ctx, `SELECT position,event_id,record FROM authority_events
		WHERE domain_id=? AND command_id=? ORDER BY position`, gateCommand.AuthorityDomainID, commandID)
	if err != nil {
		return ErrInvalidProof
	}
	var sourceEvent step12Event
	var sourceEventFound bool
	for rows.Next() {
		var position uint64
		var eventID string
		var record []byte
		if err = rows.Scan(&position, &eventID, &record); err != nil {
			break
		}
		event, parseErr := parseStep12Event(record, gateCommand.AuthorityDomainID, position, eventID, commandID)
		if parseErr != nil {
			err = parseErr
			break
		}
		if event.kind != "scheduler.hook-completed" {
			continue
		}
		if sourceEventFound || event.subject != matterID {
			err = ErrInvalidProof
			break
		}
		sourceEvent = event
		sourceEventFound = true
	}
	err = errors.Join(err, rows.Err(), rows.Close())
	if err != nil || !sourceEventFound {
		return ErrInvalidProof
	}
	selected, selectErr := schedulerhook.SelectStep10SourceRevision(request, gateCommand.AuthorityDomainID)
	if selectErr != nil {
		return ErrInvalidProof
	}
	var retained schedulerhook.SelectedSourceRevision
	if artifactDecoder.Unmarshal(sourceEvent.payload["source_revision"], &retained) != nil {
		return ErrInvalidProof
	}
	if retained != selected {
		return ErrInvalidProof
	}
	var evidenceSize uint64
	var evidenceVerified int
	if err = tx.QueryRowContext(ctx, `SELECT byte_length,verified FROM blob_products WHERE domain_id=? AND digest=?`,
		gateCommand.AuthorityDomainID, output.EvidenceRefs[0]).Scan(&evidenceSize, &evidenceVerified); err != nil ||
		evidenceVerified != 1 || evidenceSize > 4<<20 {
		return ErrInvalidProof
	}
	evidence, err := readBoundedVerifiedProduct(blobDir, output.EvidenceRefs[0], evidenceSize)
	if err != nil {
		return err
	}
	if int64(len(evidence)) > 4<<20 || schedulerhook.ValidateStep10VerificationEvidence(evidence, request, gateCommand.AuthorityDomainID) != nil {
		return ErrInvalidProof
	}
	return nil
}

func readBoundedVerifiedProduct(dir, digest string, limit uint64) (content []byte, resultErr error) {
	if dir == "" || !validDigest(digest) || limit > 4<<20 {
		return nil, ErrInvalidProof
	}
	path := filepath.Join(dir, productName(digest))
	before, err := os.Lstat(path)
	if err != nil || !before.Mode().IsRegular() || before.Size() < 0 || uint64(before.Size()) > limit || before.Mode().Perm() != 0o400 {
		return nil, ErrInvalidStore
	}
	file, err := os.Open(path)
	if err != nil {
		return nil, ErrInvalidStore
	}
	defer func() {
		resultErr = errors.Join(resultErr, file.Close())
	}()
	after, err := file.Stat()
	if err != nil || !os.SameFile(before, after) || after.Size() != before.Size() {
		return nil, ErrInvalidStore
	}
	content, err = io.ReadAll(io.LimitReader(file, int64(limit)+1))
	if err != nil || uint64(len(content)) != uint64(after.Size()) || digestRawBytes(sha256Bytes(content)) != digest {
		return nil, ErrInvalidStore
	}
	return content, nil
}
