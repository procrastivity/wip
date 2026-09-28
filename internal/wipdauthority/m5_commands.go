package wipdauthority

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"errors"
	"net/http"
	"time"

	"github.com/procrastivity/wip/internal/authoritystore"
	"github.com/procrastivity/wip/internal/operation"
	"github.com/procrastivity/wip/internal/wipdwire"
)

func artifactCertificatePublicKey(wrapper []byte) (ed25519.PublicKey, error) {
	fields, err := wipdwire.DecodeCanonicalMap(wrapper,
		"schema", "kind", "domain_id", "authority_epoch", "signer_role", "signer_key_id", "key_generation",
		"artifact_sequence", "previous_artifact_digest", "issued_at", "payload_schema", "payload_digest", "payload", "signature")
	if err != nil {
		return nil, err
	}
	payload, ok := fields["payload"].([]byte)
	if !ok {
		return nil, wipdwire.ErrInvalidRecord
	}
	payloadFields, err := wipdwire.DecodeCanonicalMap(payload,
		"schema", "domain_id", "authority_epoch", "key_generation", "key_id", "ed25519_public_key", "not_before", "not_after")
	if err != nil {
		return nil, err
	}
	public, ok := payloadFields["ed25519_public_key"].([]byte)
	if !ok || len(public) != ed25519.PublicKeySize {
		return nil, wipdwire.ErrInvalidRecord
	}
	return ed25519.PublicKey(bytes.Clone(public)), nil
}

func (app *m5LabHandler) serveCommandSubmit(writer http.ResponseWriter, request *http.Request, frame wipdwire.Frame) {
	var submit wipdwire.CommandSubmit
	if err := wipdwire.DecodeCanonical(frame.Payload, &submit,
		"schema", "canonical_command", "request_hash", "deadline"); err != nil || submit.Schema != "wipd.command-submit/1" || len(submit.CanonicalCommand) == 0 {
		writeLabProblem(writer, frame.RequestID, "protocol.malformed-message")
		return
	}
	now := time.Now().UTC()
	if submit.Deadline != nil {
		deadline, err := time.Parse(time.RFC3339Nano, *submit.Deadline)
		if err != nil || deadline.UTC().Format(time.RFC3339Nano) != *submit.Deadline || !bytes.HasSuffix([]byte(*submit.Deadline), []byte("Z")) {
			writeLabProblem(writer, frame.RequestID, "protocol.malformed-message")
			return
		}
		if !now.Before(deadline) {
			writeLabProblem(writer, frame.RequestID, "transport.deadline-before-submission")
			return
		}
	}
	command, err := operation.DecodeCanonicalCommand(submit.CanonicalCommand)
	if err != nil {
		writeLabProblem(writer, frame.RequestID, "protocol.malformed-message")
		return
	}
	computedHash, err := command.RequestHash()
	if err != nil || computedHash != submit.RequestHash {
		writeLabProblem(writer, frame.RequestID, "protocol.malformed-message")
		return
	}
	if command.AuthorityDomainID != app.profile.domainID || command.ExpectedAuthorityEpoch != app.profile.epoch {
		writeLabProblem(writer, frame.RequestID, "auth.authority-binding-mismatch")
		return
	}
	if command.Request.Operation != operation.MatterCreateV1.Metadata().Operation {
		writeLabProblem(writer, frame.RequestID, "operation.unknown")
		return
	}
	status, err := app.store.SubmitCommand(request.Context(), command, submit.RequestHash, *request.TLS, now)
	if err != nil {
		writeLabProblem(writer, frame.RequestID, submissionProblem(err))
		return
	}
	if !status.Pending {
		if len(status.Receipt) == 0 {
			writeLabProblem(writer, frame.RequestID, "authority.unavailable")
			return
		}
		writeLabFrame(writer, wipdwire.Frame{RequestID: frame.RequestID, Kind: "command.terminal", Payload: status.Receipt})
		return
	}
	owner := status.Owner
	if owner == nil {
		owner, err = app.store.RecoverCommand(context.Background(), command, submit.RequestHash)
		if err != nil {
			// SubmitCommand already proved that these exact bytes and hash are
			// durably retained. Recovery failure cannot turn that into a refusal.
			owner = nil
		}
	}
	accepted, err := wipdwire.EncodeCanonical(wipdwire.SubmissionAccepted{
		Schema: "wipd.submission-accepted/1", DomainID: command.AuthorityDomainID,
		Epoch: command.ExpectedAuthorityEpoch, CommandID: command.ID, RequestHash: submit.RequestHash,
	})
	if err != nil {
		writeLabProblem(writer, frame.RequestID, "authority.unavailable")
		return
	}
	writeLabFrame(writer, wipdwire.Frame{RequestID: frame.RequestID, Sequence: 0, Kind: "submission.accepted", Payload: accepted})
	if owner == nil {
		return
	}
	terminal, err := app.executeSubmitted(owner, command)
	if err != nil {
		// The durable submission remains pending. The client can reconnect and
		// query or retry its exact identity; execution errors are not M1 results.
		return
	}
	writeLabFrameContinuation(writer, wipdwire.Frame{RequestID: frame.RequestID, Sequence: 1, Kind: "command.terminal", Payload: terminal})
}

func (app *m5LabHandler) executeSubmitted(owner *authoritystore.Execution, command operation.Command) ([]byte, error) {
	result := app.registry.Dispatch(context.Background(), command.Request)
	var matterID, eventID string
	if result.Code == operation.ResultSucceeded {
		output, ok := result.Output.(operation.MatterCreateOutput)
		if !ok {
			return nil, authoritystore.ErrInvalidProof
		}
		matterID = output.ID
		var err error
		eventID, err = randomULID(time.Now().UTC().Add(time.Millisecond))
		if err != nil {
			return nil, err
		}
	}
	status, err := app.store.CompleteCommand(context.Background(), owner, result, matterID, eventID, time.Now().UTC(), app.sign)
	if err != nil {
		return nil, err
	}
	if len(status.Receipt) == 0 {
		return nil, errors.New("authoritystore: terminal completion returned no receipt")
	}
	return status.Receipt, nil
}

func (app *m5LabHandler) serveReceiptQuery(writer http.ResponseWriter, request *http.Request, frame wipdwire.Frame, environment authoritystore.EnvironmentCertificate) {
	var query wipdwire.ReceiptQuery
	if err := wipdwire.DecodeCanonical(frame.Payload, &query,
		"schema", "domain_id", "command_id", "request_hash"); err != nil || query.Schema != "wipd.receipt-query/1" ||
		query.DomainID != app.profile.domainID || !ulidPattern.MatchString(query.CommandID) || !canonicalSPKIDigestPattern.MatchString(query.RequestHash) {
		writeLabProblem(writer, frame.RequestID, "protocol.malformed-message")
		return
	}
	status, err := app.store.QueryCommand(request.Context(), query.DomainID, query.CommandID, query.RequestHash,
		app.profile.epoch, *request.TLS, environment.EnvironmentID, time.Now().UTC())
	if errors.Is(err, authoritystore.ErrNotFound) {
		payload, encodeErr := wipdwire.EncodeCanonical(wipdwire.ReceiptNotFound{
			Schema: "wipd.receipt-not-found/1", DomainID: query.DomainID, CommandID: query.CommandID, RequestHash: query.RequestHash,
		})
		if encodeErr != nil {
			writeLabProblem(writer, frame.RequestID, "authority.unavailable")
			return
		}
		writeLabFrame(writer, wipdwire.Frame{RequestID: frame.RequestID, Kind: "receipt.not-found", Payload: payload})
		return
	}
	if err != nil {
		writeLabProblem(writer, frame.RequestID, receiptQueryProblem(err))
		return
	}
	if status.Pending {
		payload, encodeErr := wipdwire.EncodeCanonical(wipdwire.SubmissionAccepted{
			Schema: "wipd.submission-accepted/1", DomainID: query.DomainID, Epoch: app.profile.epoch,
			CommandID: query.CommandID, RequestHash: query.RequestHash,
		})
		if encodeErr != nil {
			writeLabProblem(writer, frame.RequestID, "authority.unavailable")
			return
		}
		writeLabFrame(writer, wipdwire.Frame{RequestID: frame.RequestID, Kind: "receipt.pending", Payload: payload})
		return
	}
	writeLabFrame(writer, wipdwire.Frame{RequestID: frame.RequestID, Kind: "command.terminal", Payload: status.Receipt})
}

func submissionProblem(err error) string {
	switch {
	case errors.Is(err, authoritystore.ErrConflict):
		return "command.id-conflict"
	case errors.Is(err, authoritystore.ErrPending):
		return "command.sequence-blocked"
	case errors.Is(err, authoritystore.ErrFenced), errors.Is(err, authoritystore.ErrInvalidProof):
		return "auth.environment-domain-mismatch"
	default:
		return "authority.unavailable"
	}
}

func receiptQueryProblem(err error) string {
	if errors.Is(err, authoritystore.ErrConflict) {
		return "command.id-conflict"
	}
	if errors.Is(err, authoritystore.ErrFenced) || errors.Is(err, authoritystore.ErrInvalidProof) {
		return "auth.environment-domain-mismatch"
	}
	return "authority.unavailable"
}

func writeLabFrameContinuation(writer http.ResponseWriter, frame wipdwire.Frame) {
	wire, err := wipdwire.EncodeFrame(frame)
	if err == nil {
		_, _ = writer.Write(wire)
	}
}
