package wipdremote

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"errors"
	"fmt"
	"time"

	"github.com/procrastivity/wip/internal/operation"
	"github.com/procrastivity/wip/internal/wipd"
	"github.com/procrastivity/wip/internal/wipdjournal"
	"github.com/procrastivity/wip/internal/wipdseed"
	"github.com/procrastivity/wip/internal/wipdwire"
)

// AcquireClaim submits the exact durable acquisition identity and returns only
// a validated terminal receipt and, on success, its verified signed grant.
func (runtime *Runtime) AcquireClaim(ctx context.Context, attempt wipdjournal.ClaimAcquireAttempt, installed wipdwire.PrefixAnchor) (wipd.ClaimAcquireAuthorityResult, error) {
	var empty wipd.ClaimAcquireAuthorityResult
	if runtime == nil || runtime.client == nil || ctx == nil || attempt.ID == "" || attempt.RequestHash == "" ||
		!sameAnchor(attempt.Installed, installed) || len(attempt.CanonicalBytes) == 0 {
		return empty, wipdseed.ErrInvalidClientState
	}
	payload := wipdwire.ClaimAcquire{
		Schema: "wipd.claim-acquire/1", CanonicalCommand: bytes.Clone(attempt.CanonicalBytes),
		RequestHash: attempt.RequestHash, Installed: installed,
	}
	frames, err := runtime.client.Exchange(ctx, "claim.acquire", payload)
	if err != nil {
		return empty, fmt.Errorf("claim.acquire exchange: %w", err)
	}
	if len(frames) == 1 && frames[0].Kind == "command.terminal" {
		return decodeAcquireRefusal(runtime, attempt, frames[0].Payload)
	}
	if len(frames) == 0 || frames[0].Kind != "submission.accepted" || !validAcquireAccepted(frames[0].Payload, runtime.state.DomainID, runtime.state.Epoch, attempt) {
		return empty, errors.New("wipdremote: authority did not acknowledge the exact acquisition")
	}
	if len(frames) == 1 {
		return runtime.resolvePendingAcquire(ctx, attempt, payload)
	}
	if len(frames) < 2 || frames[1].Kind != "command.terminal" {
		return empty, errors.New("wipdremote: acquisition response omitted its terminal receipt")
	}
	code, err := terminalResultCode(frames[1].Payload)
	if err != nil || !validAcquireTerminal(frames[1].Payload, runtime.state.DomainID, runtime.state.Epoch, runtime.state.EnvironmentID, attempt, code) {
		return empty, errors.New("wipdremote: authority returned an invalid acquisition receipt")
	}
	if code != operation.ResultSucceeded {
		if len(frames) != 2 {
			return empty, errors.New("wipdremote: refused acquisition included a grant")
		}
		return wipd.ClaimAcquireAuthorityResult{Code: code, Receipt: bytes.Clone(frames[1].Payload)}, nil
	}
	grant, err := runtime.verifyAcquireGrant(ctx, attempt, installed, frames[1].Payload, frames[2:])
	if err != nil {
		return empty, err
	}
	return wipd.ClaimAcquireAuthorityResult{Code: code, Receipt: grant.TerminalReceipt(), Grant: &grant}, nil
}

func (runtime *Runtime) resolvePendingAcquire(ctx context.Context, attempt wipdjournal.ClaimAcquireAttempt, payload wipdwire.ClaimAcquire) (wipd.ClaimAcquireAuthorityResult, error) {
	var empty wipd.ClaimAcquireAuthorityResult
	deadline := time.NewTimer(30 * time.Second)
	defer deadline.Stop()
	query := wipdwire.ReceiptQuery{Schema: "wipd.receipt-query/1", DomainID: runtime.state.DomainID, CommandID: attempt.ID, RequestHash: attempt.RequestHash}
	for {
		frames, err := runtime.client.Exchange(ctx, "receipt.query", query)
		if err != nil {
			return empty, err
		}
		if len(frames) != 1 {
			return empty, errors.New("wipdremote: acquisition receipt query returned an invalid frame count")
		}
		switch frames[0].Kind {
		case "command.terminal":
			code, codeErr := terminalResultCode(frames[0].Payload)
			if codeErr != nil || !validAcquireTerminal(frames[0].Payload, runtime.state.DomainID, runtime.state.Epoch, runtime.state.EnvironmentID, attempt, code) {
				return empty, errors.New("wipdremote: acquisition receipt query returned another command")
			}
			if code != operation.ResultSucceeded {
				return wipd.ClaimAcquireAuthorityResult{Code: code, Receipt: bytes.Clone(frames[0].Payload)}, nil
			}
			frames, err = runtime.client.Exchange(ctx, "claim.acquire", payload)
			if err != nil {
				return empty, err
			}
			if len(frames) < 3 || frames[0].Kind != "submission.accepted" ||
				!validAcquireAccepted(frames[0].Payload, runtime.state.DomainID, runtime.state.Epoch, attempt) || frames[1].Kind != "command.terminal" {
				return empty, errors.New("wipdremote: completed acquisition replay omitted its retained grant")
			}
			code, err = terminalResultCode(frames[1].Payload)
			if err != nil || code != operation.ResultSucceeded || !validAcquireTerminal(frames[1].Payload,
				runtime.state.DomainID, runtime.state.Epoch, runtime.state.EnvironmentID, attempt, code) {
				return empty, errors.New("wipdremote: completed acquisition replay changed its receipt")
			}
			grant, verifyErr := runtime.verifyAcquireGrant(ctx, attempt, attempt.Installed, frames[1].Payload, frames[2:])
			if verifyErr != nil {
				return empty, verifyErr
			}
			return wipd.ClaimAcquireAuthorityResult{Code: code, Receipt: grant.TerminalReceipt(), Grant: &grant}, nil
		case "receipt.pending":
			if !validAcquireAccepted(frames[0].Payload, runtime.state.DomainID, runtime.state.Epoch, attempt) {
				return empty, errors.New("wipdremote: pending acquisition receipt changed identity")
			}
		case "receipt.not-found":
			missing, decodeErr := wipdwire.DecodeCanonicalMap(frames[0].Payload, "schema", "domain_id", "command_id", "request_hash")
			if decodeErr != nil || missing["schema"] != "wipd.receipt-not-found/1" || missing["domain_id"] != runtime.state.DomainID ||
				missing["command_id"] != attempt.ID || missing["request_hash"] != attempt.RequestHash {
				return empty, errors.New("wipdremote: acquisition absence is not bound to its durable identity")
			}
			frames, err = runtime.client.Exchange(ctx, "claim.acquire", payload)
			if err != nil {
				return empty, err
			}
			if len(frames) != 1 || frames[0].Kind != "submission.accepted" ||
				!validAcquireAccepted(frames[0].Payload, runtime.state.DomainID, runtime.state.Epoch, attempt) {
				return empty, errors.New("wipdremote: exact acquisition retry was not accepted")
			}
		default:
			return empty, errors.New("wipdremote: authority returned an unexpected acquisition state")
		}
		select {
		case <-ctx.Done():
			return empty, ctx.Err()
		case <-deadline.C:
			return empty, errors.New("wipdremote: acquisition remains pending; exact retry is safe")
		case <-time.After(25 * time.Millisecond):
		}
	}
}

func (runtime *Runtime) verifyAcquireGrant(ctx context.Context, attempt wipdjournal.ClaimAcquireAttempt, installed wipdwire.PrefixAnchor,
	receipt []byte, frames []wipdwire.Frame,
) (wipdjournal.VerifiedClaimGrant, error) {
	var empty wipdjournal.VerifiedClaimGrant
	if len(frames) < 4 || frames[0].Kind != "claim.grant.start" || frames[len(frames)-1].Kind != "claim.grant.end" || frames[len(frames)-2].Kind != "blob.manifest" {
		return empty, errors.New("wipdremote: incomplete acquisition grant transfer")
	}
	wrapper := bytes.Clone(frames[0].Payload)
	wrapperFields, err := wipdwire.DecodeCanonicalMap(wrapper,
		"schema", "kind", "domain_id", "authority_epoch", "signer_role", "signer_key_id", "key_generation", "artifact_sequence",
		"previous_artifact_digest", "issued_at", "payload_schema", "payload_digest", "payload", "signature")
	if err != nil {
		return empty, err
	}
	start, ok := wrapperFields["payload"].([]byte)
	if !ok {
		return empty, fmt.Errorf("claim grant wrapper payload: %w", wipdseed.ErrInvalidClientState)
	}
	var startRecord struct {
		GrantID     string `cbor:"grant_id"`
		AcquireID   string `cbor:"acquire_command_id"`
		RequestHash string `cbor:"acquire_request_hash"`
		Prefix      struct {
			Start wipdwire.PrefixAnchor `cbor:"start"`
		} `cbor:"prefix"`
	}
	if err = wipdwire.DecodeCanonical(start, &startRecord,
		"schema", "grant_id", "acquire_command_id", "acquire_request_hash", "domain_id", "authority_epoch", "owner_environment_id",
		"claim", "matter_id", "batch_id", "dispatch_id", "receipt", "prefix", "blob_manifest_digest"); err != nil ||
		startRecord.AcquireID != attempt.ID || startRecord.RequestHash != attempt.RequestHash || !sameAnchor(startRecord.Prefix.Start, installed) {
		return empty, fmt.Errorf("claim grant start does not bind the durable attempt: %w", wipdseed.ErrInvalidClientState)
	}
	var records []wipdwire.EventRecord
	for _, frame := range frames[1 : len(frames)-2] {
		if frame.Kind != "event.record" {
			return empty, errors.New("wipdremote: unexpected acquisition transfer frame")
		}
		var record wipdwire.EventRecord
		if err = wipdwire.DecodeCanonical(frame.Payload, &record, "event_id", "record"); err != nil {
			return empty, err
		}
		records = append(records, record)
	}
	var manifest wipdwire.BlobManifest
	if err = wipdwire.DecodeCanonical(frames[len(frames)-2].Payload, &manifest,
		"schema", "domain_id", "authority_epoch", "as_of", "entries", "manifest_digest"); err != nil {
		return empty, err
	}
	var end struct {
		Schema         string                `cbor:"schema"`
		GrantID        string                `cbor:"grant_id"`
		VerifiedPrefix wipdwire.PrefixAnchor `cbor:"verified_prefix"`
		ManifestDigest string                `cbor:"verified_blob_manifest_digest"`
		Complete       bool                  `cbor:"complete"`
	}
	if err = wipdwire.DecodeCanonical(frames[len(frames)-1].Payload, &end,
		"schema", "grant_id", "verified_prefix", "verified_blob_manifest_digest", "complete"); err != nil ||
		end.Schema != "wipd.claim-grant-end/1" || end.GrantID != startRecord.GrantID || !end.Complete {
		return empty, fmt.Errorf("claim grant end frame: %w", wipdseed.ErrInvalidClientState)
	}
	previous := runtime.state
	previous.Prefix = installed
	previous.EventRecords, err = runtime.journal.EventRecords(ctx)
	if err != nil || len(previous.EventRecords) != int(installed.EventCount) {
		return empty, fmt.Errorf("claim grant does not extend the installed journal prefix: %w", wipdseed.ErrInvalidClientState)
	}
	transfer, err := wipdseed.VerifyClaimGrantTransfer(runtime.profile, previous, installed, end.VerifiedPrefix, records, manifest)
	if err != nil {
		return empty, fmt.Errorf("claim grant transfer verification: %w", err)
	}
	identity := wipdjournal.Identity{
		RepoID: runtime.state.RepoID, DomainID: runtime.state.DomainID, AuthorityEpoch: runtime.state.Epoch,
		EnvironmentID: runtime.state.EnvironmentID, OwnerRootSPKI: runtime.state.OwnerKeyID,
	}
	grant, err := wipdjournal.VerifyClaimGrant(identity, wipdjournal.ClaimGrantTrust{
		OwnerRootPublicKey: ed25519.PublicKey(runtime.config.OwnerRootPublicKey),
		OwnerRootSPKI:      runtime.config.OwnerRootSPKI, VerifiedAt: time.Now().UTC(),
	}, wipdjournal.ClaimGrantEvidence{
		ArtifactKeyCertificate: runtime.config.ArtifactKeyCertificate, Wrapper: wrapper, Start: start,
		End: bytes.Clone(frames[len(frames)-1].Payload), Transfer: transfer,
	})
	if err != nil || !bytes.Equal(grant.TerminalReceipt(), receipt) {
		return empty, fmt.Errorf("claim grant signature or terminal receipt verification: %w", wipdseed.ErrInvalidClientState)
	}
	return grant, nil
}

func decodeAcquireRefusal(runtime *Runtime, attempt wipdjournal.ClaimAcquireAttempt, receipt []byte) (wipd.ClaimAcquireAuthorityResult, error) {
	code, err := terminalResultCode(receipt)
	if err != nil || code == operation.ResultSucceeded || !validAcquireTerminal(receipt, runtime.state.DomainID, runtime.state.Epoch,
		runtime.state.EnvironmentID, attempt, code) {
		return wipd.ClaimAcquireAuthorityResult{}, wipdseed.ErrInvalidClientState
	}
	return wipd.ClaimAcquireAuthorityResult{Code: code, Receipt: bytes.Clone(receipt)}, nil
}

func validAcquireAccepted(raw []byte, domain string, epoch uint64, attempt wipdjournal.ClaimAcquireAttempt) bool {
	fields, err := wipdwire.DecodeCanonicalMap(raw, "schema", "domain_id", "authority_epoch", "command_id", "request_hash")
	return err == nil && fields["schema"] == "wipd.submission-accepted/1" && fields["domain_id"] == domain &&
		fields["authority_epoch"] == epoch && fields["command_id"] == attempt.ID && fields["request_hash"] == attempt.RequestHash
}

func validAcquireTerminal(raw []byte, domain string, epoch uint64, environment string, attempt wipdjournal.ClaimAcquireAttempt, code operation.ResultCode) bool {
	fields, err := wipdwire.DecodeCanonicalMap(raw,
		"schema", "domain_id", "authority_epoch", "identity_schema", "command_id", "request_hash", "operation", "environment", "result", "accepted_events")
	if err != nil || fields["schema"] != "wipd.terminal-receipt/1" || fields["domain_id"] != domain || fields["authority_epoch"] != epoch ||
		fields["identity_schema"] != "wipd.command/1" || fields["command_id"] != attempt.ID || fields["request_hash"] != attempt.RequestHash {
		return false
	}
	operationValue, ok := fields["operation"].(map[string]any)
	result, resultOK := fields["result"].(map[string]any)
	environmentValue, environmentOK := fields["environment"].(map[string]any)
	return ok && wipdwire.ExactMapKeys(operationValue, "name", "version") && operationValue["name"] == "claim.acquire" && operationValue["version"] == uint64(1) &&
		resultOK && wipdwire.ExactMapKeys(result, "code", "output", "problem_code") && result["code"] == string(code) &&
		environmentOK && wipdwire.ExactMapKeys(environmentValue, "id", "sequence") && environmentValue["id"] == environment && environmentValue["sequence"] == attempt.EnvironmentSeq
}

func sameAnchor(left, right wipdwire.PrefixAnchor) bool {
	return left.EventCount == right.EventCount && left.Digest == right.Digest &&
		(left.EventID == nil) == (right.EventID == nil) && (left.EventID == nil || *left.EventID == *right.EventID)
}
