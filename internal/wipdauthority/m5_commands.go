package wipdauthority

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/tls"
	"errors"
	"io"
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

var (
	errLabControlCancel  = errors.New("wipdauthority: client canceled exchange")
	errLabControlInvalid = errors.New("wipdauthority: invalid post-submit control frame")
)

func (app *m5LabHandler) serveCommandSubmit(writer http.ResponseWriter, request *http.Request, frame wipdwire.Frame, environment authoritystore.EnvironmentCertificate) {
	var submit wipdwire.CommandSubmit
	if err := wipdwire.DecodeCanonical(frame.Payload, &submit,
		"schema", "canonical_command", "request_hash", "deadline"); err != nil || submit.Schema != "wipd.command-submit/1" || len(submit.CanonicalCommand) == 0 {
		writeLabProblem(writer, frame.RequestID, "protocol.malformed-message")
		return
	}
	var deadline time.Time
	if submit.Deadline != nil {
		parsed, err := time.Parse(time.RFC3339Nano, *submit.Deadline)
		if err != nil || parsed.UTC().Format(time.RFC3339Nano) != *submit.Deadline || !bytes.HasSuffix([]byte(*submit.Deadline), []byte("Z")) {
			writeLabProblem(writer, frame.RequestID, "protocol.malformed-message")
			return
		}
		deadline = parsed
		if !time.Now().Before(deadline) {
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

	admissionParent := request.Context()
	var cancelDeadline context.CancelFunc
	if !deadline.IsZero() {
		admissionParent, cancelDeadline = context.WithDeadline(admissionParent, deadline)
		defer cancelDeadline()
	}
	admissionCtx, cancelAdmission := context.WithCancelCause(admissionParent)
	defer cancelAdmission(context.Canceled)
	defer func() { _ = request.Body.Close() }()
	watchLabCommandControl(request.Context(), request.Body, frame.RequestID, cancelAdmission)

	status, err := app.store.SubmitCommandWithDeadline(admissionCtx, command, submit.RequestHash, *request.TLS, time.Now().UTC(), deadline)
	if err != nil {
		status, err = app.reconcileSubmissionError(admissionCtx, command, submit.RequestHash, *request.TLS, environment.EnvironmentID, deadline, err, frame.RequestID, writer)
		if err != nil {
			return
		}
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
	var completion *authoritystore.CommandCompletion
	if owner == nil {
		resumedOwner, resumed, claimed, claimErr := app.store.ClaimPendingCommandCompletion(command, submit.RequestHash)
		if claimErr != nil {
			writeLabProblem(writer, frame.RequestID, submissionProblem(claimErr))
			return
		}
		if claimed {
			owner = resumedOwner
			completion = &resumed
		} else {
			owner, err = app.store.RecoverCommand(context.Background(), command, submit.RequestHash)
			if err != nil {
				// An in-process owner may still be executing or completing. The
				// durable submission remains pending either way.
				owner = nil
			}
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
	_ = http.NewResponseController(writer).Flush()
	if owner == nil {
		return
	}
	executed := make(chan struct {
		terminal []byte
		err      error
	}, 1)
	go func() {
		var terminal []byte
		var executeErr error
		if completion != nil {
			terminal, executeErr = app.completeContinuation(owner, *completion)
		} else {
			terminal, executeErr = app.executeSubmitted(owner, command)
		}
		executed <- struct {
			terminal []byte
			err      error
		}{terminal: terminal, err: executeErr}
	}()
	select {
	case result := <-executed:
		if result.err == nil {
			writeLabFrameContinuation(writer, wipdwire.Frame{RequestID: frame.RequestID, Sequence: 1, Kind: "command.terminal", Payload: result.terminal})
		}
	case <-admissionCtx.Done():
		// Once SubmitCommand committed, cancellation ends only this wait. The
		// execution goroutine retains the owner and completes independently.
	}
}

func (app *m5LabHandler) reconcileSubmissionError(ctx context.Context, command operation.Command, hash string, peer tls.ConnectionState, environment string, deadline time.Time, submitErr error, requestID string, writer http.ResponseWriter) (authoritystore.CommandStatus, error) {
	if errors.Is(submitErr, authoritystore.ErrConflict) || errors.Is(submitErr, authoritystore.ErrFenced) ||
		errors.Is(submitErr, authoritystore.ErrInvalidProof) || errors.Is(submitErr, authoritystore.ErrPending) {
		writeLabProblem(writer, requestID, submissionProblem(submitErr))
		return authoritystore.CommandStatus{}, submitErr
	}
	status, err := app.store.QueryCommand(context.Background(), command.AuthorityDomainID, command.ID, hash,
		command.ExpectedAuthorityEpoch, peer, environment, time.Now().UTC())
	if err == nil {
		return status, nil
	}
	if !errors.Is(err, authoritystore.ErrNotFound) {
		// The caller cannot infer a no-effect outcome if reconciliation itself
		// fails after SubmitCommand returned an ambiguous error.
		return authoritystore.CommandStatus{}, err
	}
	code := submissionProblem(submitErr)
	if errors.Is(context.Cause(ctx), errLabControlCancel) || errors.Is(context.Cause(ctx), context.Canceled) {
		code = "transport.cancelled-before-submission"
	} else if errors.Is(context.Cause(ctx), errLabControlInvalid) {
		code = "protocol.out-of-order"
	} else if errors.Is(context.Cause(ctx), context.DeadlineExceeded) || errors.Is(submitErr, context.DeadlineExceeded) ||
		(!deadline.IsZero() && !time.Now().Before(deadline)) {
		code = "transport.deadline-before-submission"
	}
	writeLabProblem(writer, requestID, code)
	return authoritystore.CommandStatus{}, submitErr
}

func watchLabCommandControl(ctx context.Context, body io.Reader, requestID string, cancel context.CancelCauseFunc) {
	go func() {
		frame, err := wipdwire.ReadFrame(body)
		if errors.Is(err, io.EOF) {
			return
		}
		if err != nil {
			if cause := context.Cause(ctx); cause != nil {
				cancel(cause)
			} else {
				cancel(errLabControlInvalid)
			}
			return
		}
		if frame.RequestID != requestID || frame.Sequence != 1 || frame.Kind != "control.cancel" {
			cancel(errLabControlInvalid)
			return
		}
		if _, err = wipdwire.DecodeCanonicalMap(frame.Payload); err != nil {
			cancel(errLabControlInvalid)
			return
		}
		cancel(errLabControlCancel)
	}()
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
	completion := authoritystore.CommandCompletion{
		Result: result, MatterID: matterID, EventID: eventID, Occurred: time.Now().UTC(),
	}
	return app.completeContinuation(owner, completion)
}

func (app *m5LabHandler) completeContinuation(owner *authoritystore.Execution, completion authoritystore.CommandCompletion) ([]byte, error) {
	status, err := app.store.CompleteCommand(context.Background(), owner, completion.Result,
		completion.MatterID, completion.EventID, completion.Occurred, app.sign)
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
