package authoritystore

import (
	"bytes"
	"context"
	"crypto/tls"
	"database/sql"
	"errors"
	"strconv"
	"time"

	"github.com/fxamacker/cbor/v2"
	"github.com/procrastivity/wip/internal/operation"
)

func contentOperation(id operation.ID) bool {
	return id == operation.ContentWriteOnceV1.Metadata().Operation || id == operation.FindingAppendV1.Metadata().Operation
}

func contentDefinition(id operation.ID) (operation.Definition, bool) {
	switch id {
	case operation.ContentWriteOnceV1.Metadata().Operation:
		return operation.ContentWriteOnceV1, true
	case operation.FindingAppendV1.Metadata().Operation:
		return operation.FindingAppendV1, true
	default:
		return operation.Definition{}, false
	}
}

func (s *Store) submitConnectedContent(ctx context.Context, command operation.Command, canonical []byte, hash string,
	peer tls.ConnectionState, at, deadline time.Time, checkContext bool,
) (CommandStatus, error) {
	var empty CommandStatus
	definition, ok := contentDefinition(command.Request.Operation)
	metadata := definition.Metadata()
	if !ok || definition.ValidateRequest(command.Request) != nil || metadata.Delivery != operation.DeliveryClaim ||
		command.Request.Context.Repo == "" || command.Request.Context.Clone == "" || command.Request.Context.Worktree == "" {
		return empty, ErrInvalidProof
	}
	before := func(tx *sql.Tx) error {
		kind, subject := contentInput(command.Request)
		_, err := validateContentClaimTx(ctx, tx, command, kind, subject)
		return err
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
		id: command.ID, name: metadata.Operation.Name, version: uint64(metadata.Operation.Version),
		repo: command.Request.Context.Repo, encoded: canonical, hash: hash, m1: &command,
	}, peer, at, before, beforeCommit, nil)
}

func contentInput(request operation.Request) (string, string) {
	switch input := request.Input.(type) {
	case operation.ContentWriteInput:
		return input.Kind, input.SubjectID
	case operation.FindingAppendInput:
		return "findings", input.SubjectID
	default:
		return "", ""
	}
}

func validateContentClaimTx(ctx context.Context, tx *sql.Tx, command operation.Command, kind, subject string) (string, error) {
	if command.Request.Claim == nil || !ulid.MatchString(command.Request.Claim.ID) || kind == "" || !ulid.MatchString(subject) {
		return "", ErrFenced
	}
	claimEpoch, err := strconv.ParseUint(command.Request.Claim.Epoch, 10, 64)
	if err != nil || claimEpoch == 0 || strconv.FormatUint(claimEpoch, 10) != command.Request.Claim.Epoch {
		return "", ErrFenced
	}
	var matter, owner, worktree, journalState string
	var storedEpoch, authorityEpoch uint64
	var closed sql.NullString
	err = tx.QueryRowContext(ctx, `SELECT c.matter_id,c.owner_environment_id,c.worktree_id,c.claim_epoch,c.authority_epoch,c.close_command_id,j.state
		FROM claims c JOIN claim_journals j USING(domain_id,claim_id)
		WHERE c.domain_id=? AND c.claim_id=? AND j.state IN ('open','sealed')`, command.AuthorityDomainID, command.Request.Claim.ID).
		Scan(&matter, &owner, &worktree, &storedEpoch, &authorityEpoch, &closed, &journalState)
	if errors.Is(err, sql.ErrNoRows) {
		return "", ErrFenced
	}
	if err != nil {
		return "", err
	}
	if closed.Valid || journalState == "quarantined" || storedEpoch != claimEpoch || authorityEpoch != command.ExpectedAuthorityEpoch ||
		owner != command.EnvironmentID || worktree != command.Request.Context.Worktree {
		return "", ErrFenced
	}
	var subjectMatter, repo string
	row := tx.QueryRowContext(ctx, `SELECT matter_id,repo_id FROM matters WHERE domain_id=? AND matter_id=?`, command.AuthorityDomainID, subject)
	err = row.Scan(&subjectMatter, &repo)
	if errors.Is(err, sql.ErrNoRows) {
		stepRow := tx.QueryRowContext(ctx, `SELECT matter_id,repo_id FROM steps WHERE domain_id=? AND step_id=?`, command.AuthorityDomainID, subject)
		err = stepRow.Scan(&subjectMatter, &repo)
		if errors.Is(err, sql.ErrNoRows) {
			return "", ErrFenced
		}
		if err != nil {
			return "", err
		}
	} else if err != nil {
		return "", err
	}
	if subjectMatter != matter || repo != command.Request.Context.Repo {
		return "", ErrFenced
	}
	if _, err = lifecycleStateTx(ctx, tx, command.AuthorityDomainID, matter, "matter"); err != nil {
		return "", err
	}
	return matter, nil
}

func validContentEventRecord(raw []byte, domain, eventID string, submission storedSubmission) (string, string, string, string, uint64, error) {
	var event struct {
		Schema      string `cbor:"schema"`
		EventID     string `cbor:"event_id"`
		DomainID    string `cbor:"domain_id"`
		CommandID   string `cbor:"command_id"`
		RequestHash string `cbor:"request_hash"`
		Environment struct {
			ID       string `cbor:"id"`
			Sequence uint64 `cbor:"sequence"`
		} `cbor:"environment"`
		ActedAt    string `cbor:"acted_at"`
		OccurredAt string `cbor:"occurred_at"`
		Kind       string `cbor:"kind"`
		SubjectID  string `cbor:"subject_id"`
		RepoID     string `cbor:"repo_id"`
		Payload    struct {
			Kind    string          `cbor:"kind"`
			Content string          `cbor:"content"`
			Bytes   cbor.RawMessage `cbor:"bytes"`
			BlobRef string          `cbor:"blob_ref"`
			ByteLen uint64          `cbor:"byte_len"`
			SHA256  string          `cbor:"sha256"`
		} `cbor:"payload"`
	}
	var fields map[string]cbor.RawMessage
	if closedPayload(raw, &event, "schema", "event_id", "domain_id", "command_id", "request_hash", "environment", "acted_at", "occurred_at", "kind", "subject_id", "repo_id", "payload") != nil || canonicalDecode(raw, &fields) != nil {
		return "", "", "", "", 0, ErrInvalidStore
	}
	var environment, payload map[string]cbor.RawMessage
	if canonicalDecode(fields["environment"], &environment) != nil || !exactKeys(environment, "id", "sequence") ||
		canonicalDecode(fields["payload"], &payload) != nil || !exactKeys(payload, "kind", "content", "bytes", "blob_ref", "byte_len", "sha256") ||
		!bytes.Equal(event.Payload.Bytes, []byte{0xf6}) {
		return "", "", "", "", 0, ErrInvalidStore
	}
	if event.Schema != "wipd.event/1" || event.EventID != eventID || event.DomainID != domain || event.CommandID != submission.id ||
		event.RequestHash != submission.hash || event.Environment.ID != submission.env || event.Environment.Sequence != submission.seq ||
		event.RepoID == "" || event.ActedAt == "" || event.SubjectID == "" || !ulid.MatchString(event.Payload.Content) ||
		!validDigest(event.Payload.BlobRef) || !validDigest("sha256:"+event.Payload.SHA256) ||
		event.Payload.BlobRef != "sha256:"+event.Payload.SHA256 || event.Payload.ByteLen > maxBlobLength {
		return "", "", "", "", 0, ErrInvalidStore
	}
	wantEvent := "content.created"
	if submission.operation == "finding.append" {
		wantEvent = "content.appended"
	}
	if event.Kind != wantEvent || event.Payload.Kind == "" || event.SubjectID == "" || event.CommandID == "" {
		return "", "", "", "", 0, ErrInvalidStore
	}
	command, err := operation.DecodeCanonicalCommand(submission.command)
	if err != nil || event.ActedAt != command.ActedAt {
		return "", "", "", "", 0, ErrInvalidStore
	}
	wantKind, wantSubject := contentInput(command.Request)
	if wantKind != event.Payload.Kind || wantSubject != event.SubjectID || command.Request.Context.Repo != event.RepoID || len(command.Request.Blobs) != 1 ||
		command.Request.Blobs[0].Digest != event.Payload.BlobRef || uint64(command.Request.Blobs[0].Size) != event.Payload.ByteLen {
		return "", "", "", "", 0, ErrInvalidStore
	}
	if _, err := utcTime(event.OccurredAt); err != nil {
		return "", "", "", "", 0, ErrInvalidStore
	}
	return event.Payload.Content, event.SubjectID, event.Payload.Kind, event.Payload.BlobRef, event.Payload.ByteLen, nil
}
