package wipdauthority

import (
	"bufio"
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/sha256"
	"crypto/tls"
	"encoding/hex"
	"errors"
	"io"
	"net/http"
	"sync"
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

// ArtifactKeyPublicKey extracts the owner-certified authority-artifact key
// identity so an external restricted signer can be matched without exposing
// the owner's private key to the online process.
func ArtifactKeyPublicKey(wrapper []byte) (ed25519.PublicKey, error) {
	return artifactCertificatePublicKey(wrapper)
}

var (
	errLabControlCancel       = errors.New("wipdauthority: client canceled exchange")
	errLabControlInvalid      = errors.New("wipdauthority: invalid post-first-frame control frame")
	errLabFinalAlreadyStarted = errors.New("wipdauthority: final response already started")
)

type labExchangeArbiter struct {
	mu           sync.Mutex
	cause        error
	finalStarted bool
}

func (arbiter *labExchangeArbiter) publish(cause error) bool {
	arbiter.mu.Lock()
	defer arbiter.mu.Unlock()
	if arbiter.finalStarted {
		return false
	}
	if arbiter.cause == nil {
		arbiter.cause = cause
	}
	return true
}

func (arbiter *labExchangeArbiter) causeLocked(ctx context.Context) error {
	if arbiter.cause == nil {
		arbiter.cause = context.Cause(ctx)
	}
	return arbiter.cause
}

func (arbiter *labExchangeArbiter) causeFor(ctx context.Context) error {
	arbiter.mu.Lock()
	defer arbiter.mu.Unlock()
	return arbiter.causeLocked(ctx)
}

func (arbiter *labExchangeArbiter) beginResponse(ctx context.Context, final bool) error {
	arbiter.mu.Lock()
	defer arbiter.mu.Unlock()
	if arbiter.finalStarted {
		return errLabFinalAlreadyStarted
	}
	if cause := arbiter.causeLocked(ctx); cause != nil {
		return cause
	}
	if final {
		arbiter.finalStarted = true
	}
	return nil
}

func (arbiter *labExchangeArbiter) writeFinal(ctx context.Context, allowInvalid bool, write func()) (bool, error) {
	arbiter.mu.Lock()
	if arbiter.finalStarted {
		arbiter.mu.Unlock()
		return false, errLabFinalAlreadyStarted
	}
	cause := arbiter.causeLocked(ctx)
	if cause != nil && (!allowInvalid || !errors.Is(cause, errLabControlInvalid)) {
		arbiter.mu.Unlock()
		return false, cause
	}
	arbiter.finalStarted = true
	arbiter.mu.Unlock()
	write()
	return true, cause
}

func (app *m5LabHandler) serveCommandSubmit(writer http.ResponseWriter, request *http.Request, body io.Reader, frame wipdwire.Frame, environment authoritystore.EnvironmentCertificate) {
	submit, version2, err := wipdwire.DecodeCommandSubmit(frame.Payload)
	if err != nil {
		writeLabProblem(writer, frame.RequestID, "protocol.malformed-message")
		return
	}
	session := connectionSession(request)
	if session == nil {
		writeLabProblem(writer, frame.RequestID, "protocol.out-of-order")
		return
	}
	session.mu.Lock()
	negotiated := session.negotiated && !session.failed && !session.negotiating
	supportsV2 := session.commandSubmitV2
	session.mu.Unlock()
	if !negotiated {
		writeLabProblem(writer, frame.RequestID, "protocol.out-of-order")
		return
	}
	if version2 && !supportsV2 {
		writeLabProblem(writer, frame.RequestID, "protocol.unsupported-extension")
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
	repair := command.Request.Operation == operation.GateExemptionRepairV1.Metadata().Operation
	if repair && (!version2 || !app.m6) || !repair && submit.DetachedProof != nil {
		writeLabProblem(writer, frame.RequestID, "protocol.unsupported-extension")
		return
	}
	session.mu.Lock()
	_, supportsOperation := session.operations[command.Request.Operation]
	session.mu.Unlock()
	if !supportsOperation {
		writeLabProblem(writer, frame.RequestID, "operation.unknown")
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
	admissionParent := request.Context()
	var cancelDeadline context.CancelFunc
	if !deadline.IsZero() {
		admissionParent, cancelDeadline = context.WithDeadline(admissionParent, deadline)
		defer cancelDeadline()
	}
	admissionCtx, cancelAdmission := context.WithCancelCause(admissionParent)
	defer cancelAdmission(context.Canceled)
	watchLabCommandControl(request.Context(), body, frame.RequestID, cancelAdmission)

	var status authoritystore.CommandStatus
	if repair {
		status, err = app.store.SubmitGateExemptionRepairV2(admissionCtx, command, submit.RequestHash, submit.DetachedProof, *request.TLS, time.Now().UTC(), deadline)
	} else {
		status, err = app.store.SubmitCommandWithDeadline(admissionCtx, command, submit.RequestHash, *request.TLS, time.Now().UTC(), deadline)
	}
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
	if owner == nil && !repair {
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
	if owner == nil && !repair {
		return
	}
	executed := make(chan struct {
		terminal []byte
		err      error
	}, 1)
	go func() {
		var terminal []byte
		var executeErr error
		if repair {
			occurred := time.Now().UTC()
			eventID, idErr := randomULID(occurred)
			if idErr != nil {
				executeErr = idErr
			} else {
				completed, completeErr := app.store.CompleteGateExemptionRepairV2(context.Background(), command, submit.RequestHash, nil, eventID, occurred, app.sign)
				terminal, executeErr = completed.Receipt, completeErr
			}
		} else if completion != nil {
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
		outcome := readLabControlFrame(body, requestID)
		if outcome != io.EOF {
			if cause := context.Cause(ctx); cause != nil {
				outcome = cause
			}
			cancel(outcome)
		}
	}()
}

func watchLabControl(ctx context.Context, body io.Reader, requestID string, cancel context.CancelCauseFunc, arbiter *labExchangeArbiter) {
	go func() {
		outcome := readLabControlFrame(body, requestID)
		if outcome != io.EOF {
			if cause := context.Cause(ctx); cause != nil {
				outcome = cause
			}
			if arbiter.publish(outcome) {
				cancel(outcome)
			}
		}
	}()
}

func readLabControlFrame(body io.Reader, requestID string) error {
	frame, err := wipdwire.ReadFrame(body)
	if errors.Is(err, io.EOF) {
		return io.EOF
	}
	if err != nil || frame.RequestID != requestID || frame.Sequence != 1 || frame.Kind != "control.cancel" {
		return errLabControlInvalid
	}
	fields, err := wipdwire.DecodeCanonicalMap(frame.Payload)
	if err != nil || len(fields) != 0 {
		return errLabControlInvalid
	}
	return errLabControlCancel
}

func (app *m5LabHandler) beginReadOnlyExchange(writer http.ResponseWriter, request *http.Request, body *bufio.Reader, requestID string) (context.Context, context.CancelCauseFunc, *labExchangeArbiter, bool) {
	arbiter := &labExchangeArbiter{}
	if body.Buffered() > 0 {
		outcome := readLabControlFrame(body, requestID)
		if errors.Is(outcome, errLabControlCancel) {
			return nil, nil, nil, false
		}
		if request.Context().Err() != nil {
			return nil, nil, nil, false
		}
		arbiter.publish(errLabControlInvalid)
		writeReadOnlyFinal(request.Context(), arbiter, writer, requestID, wipdwire.Frame{}, "protocol.out-of-order")
		return nil, nil, nil, false
	}
	ctx, cancel := context.WithCancelCause(request.Context())
	watchLabControl(ctx, body, requestID, cancel, arbiter)
	return ctx, cancel, arbiter, true
}

func writeReadOnlyFinal(ctx context.Context, arbiter *labExchangeArbiter, writer http.ResponseWriter, requestID string, frame wipdwire.Frame, problemCode string) bool {
	write := func() {
		if problemCode != "" {
			writeLabProblem(writer, requestID, problemCode)
			return
		}
		writeLabFrame(writer, frame)
	}
	started, cause := arbiter.writeFinal(ctx, false, write)
	if started {
		return true
	}
	if errors.Is(cause, errLabControlInvalid) {
		started, _ = arbiter.writeFinal(ctx, true, func() {
			writeLabProblem(writer, requestID, "protocol.out-of-order")
		})
	}
	return started
}

func (app *m5LabHandler) executeSubmitted(owner *authoritystore.Execution, command operation.Command) ([]byte, error) {
	if app.m6 && operation.Step4Operation(command.Request.Operation) {
		completion, err := app.step4Completion(command)
		if err != nil {
			return nil, err
		}
		return app.completeContinuation(owner, completion)
	}
	switch command.Request.Operation {
	case operation.StepStartV1.Metadata().Operation, operation.StepFinishV1.Metadata().Operation,
		operation.MatterFinishV1.Metadata().Operation,
		operation.MatterStartV1.Metadata().Operation, operation.StageStartV1.Metadata().Operation,
		operation.MatterPauseV1.Metadata().Operation, operation.StagePauseV1.Metadata().Operation,
		operation.StepPauseV1.Metadata().Operation, operation.MatterResumeV1.Metadata().Operation,
		operation.StageResumeV1.Metadata().Operation, operation.StepResumeV1.Metadata().Operation,
		operation.MatterCancelV1.Metadata().Operation, operation.StageCancelV1.Metadata().Operation,
		operation.StepCancelV1.Metadata().Operation, operation.StageFinishV1.Metadata().Operation:
		now := time.Now().UTC()
		first, err := randomULID(now)
		if err != nil {
			return nil, err
		}
		second, err := randomULID(now.Add(time.Millisecond))
		if err != nil {
			return nil, err
		}
		third, err := randomULID(now.Add(2 * time.Millisecond))
		if err != nil {
			return nil, err
		}
		status, err := app.store.CompleteConnectedLifecycle(context.Background(), owner, []string{first, second, third}, now, app.sign)
		if err != nil {
			return nil, err
		}
		if len(status.Receipt) == 0 {
			return nil, errors.New("authoritystore: terminal lifecycle completion returned no receipt")
		}
		return status.Receipt, nil
	}
	result := app.registry.Dispatch(context.Background(), command.Request)
	var subjectID, eventID string
	if result.Code == operation.ResultSucceeded {
		switch command.Request.Operation {
		case operation.MatterCreateV1.Metadata().Operation:
			output, ok := result.Output.(operation.MatterCreateOutput)
			if !ok {
				return nil, authoritystore.ErrInvalidProof
			}
			subjectID = output.ID
		case operation.StepCreateV1.Metadata().Operation:
			_, ok := result.Output.(operation.StepCreateOutput)
			if !ok {
				return nil, authoritystore.ErrInvalidProof
			}
			var err error
			subjectID, err = randomULID(time.Now().UTC())
			if err != nil {
				return nil, err
			}
		case operation.ContentWriteOnceV1.Metadata().Operation, operation.FindingAppendV1.Metadata().Operation:
			output, ok := result.Output.(operation.ContentSegmentOutput)
			if !ok {
				return nil, authoritystore.ErrInvalidProof
			}
			subjectID = output.ID
		default:
			return nil, authoritystore.ErrInvalidProof
		}
		var err error
		eventID, err = randomULID(time.Now().UTC().Add(time.Millisecond))
		if err != nil {
			return nil, err
		}
	}
	completion := authoritystore.CommandCompletion{
		Result: result, SubjectID: subjectID, EventID: eventID, Occurred: time.Now().UTC(),
	}
	return app.completeContinuation(owner, completion)
}

func (app *m5LabHandler) completeContinuation(owner *authoritystore.Execution, completion authoritystore.CommandCompletion) ([]byte, error) {
	status, err := app.store.CompleteCommand(context.Background(), owner, completion.Result,
		completion.SubjectID, completion.EventID, completion.Occurred, app.sign, completion.AdditionalEventIDs...)
	if err != nil {
		return nil, err
	}
	if len(status.Receipt) == 0 {
		return nil, errors.New("authoritystore: terminal completion returned no receipt")
	}
	return status.Receipt, nil
}

func (app *m5LabHandler) step4Completion(command operation.Command) (authoritystore.CommandCompletion, error) {
	var completion authoritystore.CommandCompletion
	var subject string
	var result operation.Result
	switch input := command.Request.Input.(type) {
	case operation.MatterCreateInput:
		if command.Request.Operation != operation.MatterCreateV2.Metadata().Operation {
			return completion, authoritystore.ErrInvalidProof
		}
		id, err := randomULID(time.Now().UTC())
		if err != nil {
			return completion, err
		}
		requested := input.Locator
		if requested == "" {
			requested = authoritystore.MatterLocator(input.Title)
		}
		subject = id
		result = operation.Result{Code: operation.ResultSucceeded, Output: operation.MatterCreateV2Output{
			ID: id, Title: input.Title, RequestedLocator: requested, AssignedLocator: requested,
		}}
	case operation.StageCreateInput:
		id, err := randomULID(time.Now().UTC())
		if err != nil {
			return completion, err
		}
		locator := authoritystore.MatterLocator(input.Title)
		subject = id
		result = operation.Result{Code: operation.ResultSucceeded, Output: operation.StageCreateOutput{
			ID: id, MatterID: input.MatterID, Locator: locator, Title: input.Title, SortKey: 1, State: "planned",
		}}
	case operation.StepCreateInput:
		if command.Request.Operation != operation.StepCreateV2.Metadata().Operation {
			return completion, authoritystore.ErrInvalidProof
		}
		id, err := randomULID(time.Now().UTC())
		if err != nil {
			return completion, err
		}
		subject = id
		result = operation.Result{Code: operation.ResultSucceeded, Output: operation.StepCreateOutput{
			ID: id, ParentID: input.ParentID, MatterID: input.ParentID, Locator: "step-01", Title: input.Title, SortKey: 1, State: "planned",
		}}
	case operation.StepInsertInput:
		id, err := randomULID(time.Now().UTC())
		if err != nil {
			return completion, err
		}
		subject = id
		result = operation.Result{Code: operation.ResultSucceeded, Output: operation.StepCreateOutput{
			ID: id, ParentID: input.ParentID, MatterID: input.ParentID, Locator: "step-01", Title: input.Title, SortKey: 1, State: "planned",
		}}
	case operation.StepReorderInput:
		subject = input.ParentID
		result = operation.Result{Code: operation.ResultSucceeded, Output: operation.StepReorderOutput{ParentID: input.ParentID, Order: append([]string(nil), input.Order...)}}
	case operation.StepReplaceInput:
		id, err := randomULID(time.Now().UTC())
		if err != nil {
			return completion, err
		}
		subject = id
		result = operation.Result{Code: operation.ResultSucceeded, Output: operation.StepReplaceOutput{
			RemovedStepID: input.StepID, Replacement: operation.StepCreateOutput{
				ID: id, ParentID: input.StepID, MatterID: input.StepID, Locator: "step-01", Title: input.Title, SortKey: 1, State: "planned",
			},
		}}
	case operation.StepRemoveInput:
		subject = input.StepID
		result = operation.Result{Code: operation.ResultSucceeded, Output: operation.StepRemoveOutput{StepID: input.StepID}}
	case operation.MatterLocatorRepairInput:
		subject = input.MatterID
		result = operation.Result{Code: operation.ResultSucceeded, Output: operation.MatterLocatorRepairOutput{
			ID: input.MatterID, Action: input.Action, RequestedLocator: input.AssignedLocator,
			PreviousLocator: input.AssignedLocator, AssignedLocator: input.AssignedLocator,
		}}
	default:
		return completion, authoritystore.ErrInvalidProof
	}
	base := time.Now().UTC()
	eventID, err := randomULID(base.Add(2 * time.Millisecond))
	if err != nil {
		return completion, err
	}
	completion = authoritystore.CommandCompletion{Result: result, SubjectID: subject, EventID: eventID, Occurred: base}
	if command.Request.Operation == operation.MatterCreateV2.Metadata().Operation || command.Request.Operation == operation.StepInsertV1.Metadata().Operation {
		additional, allocationErr := randomULID(base.Add(3 * time.Millisecond))
		if allocationErr != nil {
			return authoritystore.CommandCompletion{}, allocationErr
		}
		completion.AdditionalEventIDs = []string{additional}
	}
	return completion, nil
}

func (app *m5LabHandler) serveReceiptQuery(writer http.ResponseWriter, request *http.Request, body *bufio.Reader, frame wipdwire.Frame, environment authoritystore.EnvironmentCertificate) {
	var query wipdwire.ReceiptQuery
	if err := wipdwire.DecodeCanonical(frame.Payload, &query,
		"schema", "domain_id", "command_id", "request_hash"); err != nil || query.Schema != "wipd.receipt-query/1" ||
		query.DomainID != app.profile.domainID || !ulidPattern.MatchString(query.CommandID) || !canonicalSPKIDigestPattern.MatchString(query.RequestHash) {
		writeLabProblem(writer, frame.RequestID, "protocol.malformed-message")
		return
	}
	ctx, cancel, arbiter, ok := app.beginReadOnlyExchange(writer, request, body, frame.RequestID)
	if !ok {
		return
	}
	defer cancel(context.Canceled)
	status, err := app.store.QueryCommand(ctx, query.DomainID, query.CommandID, query.RequestHash,
		app.profile.epoch, *request.TLS, environment.EnvironmentID, time.Now().UTC())
	if errors.Is(err, authoritystore.ErrNotFound) {
		payload, encodeErr := wipdwire.EncodeCanonical(wipdwire.ReceiptNotFound{
			Schema: "wipd.receipt-not-found/1", DomainID: query.DomainID, CommandID: query.CommandID, RequestHash: query.RequestHash,
		})
		if encodeErr != nil {
			writeReadOnlyFinal(ctx, arbiter, writer, frame.RequestID, wipdwire.Frame{}, "authority.unavailable")
			return
		}
		writeReadOnlyFinal(ctx, arbiter, writer, frame.RequestID,
			wipdwire.Frame{RequestID: frame.RequestID, Kind: "receipt.not-found", Payload: payload}, "")
		return
	}
	if err != nil {
		writeReadOnlyFinal(ctx, arbiter, writer, frame.RequestID, wipdwire.Frame{}, receiptQueryProblem(err))
		return
	}
	if status.Pending {
		payload, encodeErr := wipdwire.EncodeCanonical(wipdwire.SubmissionAccepted{
			Schema: "wipd.submission-accepted/1", DomainID: query.DomainID, Epoch: app.profile.epoch,
			CommandID: query.CommandID, RequestHash: query.RequestHash,
		})
		if encodeErr != nil {
			writeReadOnlyFinal(ctx, arbiter, writer, frame.RequestID, wipdwire.Frame{}, "authority.unavailable")
			return
		}
		writeReadOnlyFinal(ctx, arbiter, writer, frame.RequestID,
			wipdwire.Frame{RequestID: frame.RequestID, Kind: "receipt.pending", Payload: payload}, "")
		return
	}
	writeReadOnlyFinal(ctx, arbiter, writer, frame.RequestID,
		wipdwire.Frame{RequestID: frame.RequestID, Kind: "command.terminal", Payload: status.Receipt}, "")
}

func (app *m5LabHandler) serveBirthJournalAck(writer http.ResponseWriter, request *http.Request, body *bufio.Reader, frame wipdwire.Frame, environment authoritystore.EnvironmentCertificate) {
	var ack wipdwire.BirthJournalAck
	if err := wipdwire.DecodeCanonical(frame.Payload, &ack,
		"schema", "domain_id", "authority_epoch", "matter_id", "command_id", "request_hash", "terminal_receipt", "installed_prefix"); err != nil ||
		ack.Schema != "wipd.birth-journal-ack/1" {
		writeLabProblem(writer, frame.RequestID, "protocol.malformed-message")
		return
	}
	ctx, cancel, arbiter, ok := app.beginReadOnlyExchange(writer, request, body, frame.RequestID)
	if !ok {
		return
	}
	defer cancel(context.Canceled)
	err := app.store.AcknowledgeBirthJournalEntry(ctx, ack, *request.TLS, environment.EnvironmentID, time.Now().UTC())
	if err != nil {
		code := "authority.unavailable"
		if errors.Is(err, authoritystore.ErrFenced) || errors.Is(err, authoritystore.ErrInvalidProof) {
			code = "auth.environment-domain-mismatch"
		} else if errors.Is(err, authoritystore.ErrPending) || errors.Is(err, authoritystore.ErrPrefixMismatch) {
			code = "command.sequence-blocked"
		}
		writeReadOnlyFinal(ctx, arbiter, writer, frame.RequestID, wipdwire.Frame{}, code)
		return
	}
	payload, err := wipdwire.EncodeCanonical(wipdwire.BirthJournalAcked{
		Schema: "wipd.birth-journal-acked/1", DomainID: ack.DomainID,
		MatterID: ack.MatterID, CommandID: ack.CommandID, RequestHash: ack.RequestHash,
	})
	if err != nil {
		writeReadOnlyFinal(ctx, arbiter, writer, frame.RequestID, wipdwire.Frame{}, "authority.unavailable")
		return
	}
	writeReadOnlyFinal(ctx, arbiter, writer, frame.RequestID,
		wipdwire.Frame{RequestID: frame.RequestID, Kind: "birth-journal.acknowledged", Payload: payload}, "")
}

func (app *m5LabHandler) serveBirthClaimRelease(writer http.ResponseWriter, request *http.Request, body *bufio.Reader, frame wipdwire.Frame, environment authoritystore.EnvironmentCertificate) {
	var release wipdwire.ClaimRelease
	if err := wipdwire.DecodeCanonical(frame.Payload, &release,
		"schema", "canonical_command", "request_hash", "barrier", "deadline"); err != nil ||
		release.Schema != "wipd.claim-release/1" || len(release.CanonicalCommand) == 0 {
		writeLabProblem(writer, frame.RequestID, "protocol.malformed-message")
		return
	}
	fields, decodeErr := wipdwire.DecodeCanonicalMap(release.CanonicalCommand,
		"schema", "command_id", "authority", "environment", "acted_at", "actor", "causation_command_id", "correlation_command_id", "operation", "context", "claim", "input", "blobs")
	if decodeErr != nil {
		writeLabProblem(writer, frame.RequestID, "protocol.malformed-message")
		return
	}
	contextFields, _ := fields["context"].(map[string]any)
	if contextFields["clone_id"] != nil || contextFields["worktree_id"] != nil {
		app.serveAcquiredClaimRelease(writer, request, body, frame, environment, release)
		return
	}
	commandID, environmentID, err := app.validateBirthReleaseCommand(release)
	if err != nil || environmentID != environment.EnvironmentID {
		writeLabProblem(writer, frame.RequestID, "protocol.malformed-message")
		return
	}
	var deadline time.Time
	if release.Deadline != nil {
		parsed, parseErr := time.Parse(time.RFC3339Nano, *release.Deadline)
		if parseErr != nil || parsed.UTC().Format(time.RFC3339Nano) != *release.Deadline || !bytes.HasSuffix([]byte(*release.Deadline), []byte("Z")) {
			writeLabProblem(writer, frame.RequestID, "protocol.malformed-message")
			return
		}
		deadline = parsed
		if !deadline.After(time.Now()) {
			writeLabProblem(writer, frame.RequestID, "transport.deadline-before-submission")
			return
		}
	}
	admissionParent := request.Context()
	var cancelDeadline context.CancelFunc
	if !deadline.IsZero() {
		admissionParent, cancelDeadline = context.WithDeadline(admissionParent, deadline)
		defer cancelDeadline()
	}
	admissionCtx, cancelAdmission := context.WithCancelCause(admissionParent)
	defer cancelAdmission(context.Canceled)
	watchLabCommandControl(request.Context(), body, frame.RequestID, cancelAdmission)
	status, err := app.store.SubmitClaimLifecycle(admissionCtx, release.CanonicalCommand, release.RequestHash, *request.TLS, time.Now().UTC(), nil)
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
		var recoverErr error
		owner, recoverErr = app.store.RecoverClaimLifecycle(context.Background(), release.CanonicalCommand, release.RequestHash)
		if recoverErr != nil && !errors.Is(recoverErr, authoritystore.ErrNotOwner) {
			writeLabProblem(writer, frame.RequestID, "authority.unavailable")
			return
		}
	}
	accepted, err := wipdwire.EncodeCanonical(wipdwire.SubmissionAccepted{
		Schema: "wipd.submission-accepted/1", DomainID: app.profile.domainID, Epoch: app.profile.epoch,
		CommandID: commandID, RequestHash: release.RequestHash,
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
	eventID, err := randomULID(time.Now().UTC())
	if err != nil {
		_ = app.store.AbandonClaimLifecycleExecution(owner)
		return
	}
	completed := make(chan struct {
		status authoritystore.CommandStatus
		err    error
	}, 1)
	go func() {
		status, completeErr := app.store.CompleteClaimLifecycle(context.Background(), owner, "", []string{eventID}, time.Now().UTC(), app.sign)
		if completeErr != nil {
			_ = app.store.AbandonClaimLifecycleExecution(owner)
		}
		completed <- struct {
			status authoritystore.CommandStatus
			err    error
		}{status: status, err: completeErr}
	}()
	select {
	case result := <-completed:
		if result.err == nil && !result.status.Pending && len(result.status.Receipt) > 0 {
			writeLabFrameContinuation(writer, wipdwire.Frame{RequestID: frame.RequestID, Sequence: 1, Kind: "command.terminal", Payload: result.status.Receipt})
		}
	case <-admissionCtx.Done():
		// Submission is durable; cancellation ends this wait, not lifecycle completion.
	}
}

func (app *m5LabHandler) validateBirthReleaseCommand(release wipdwire.ClaimRelease) (string, string, error) {
	var emptyID, emptyEnvironment string
	fields, err := wipdwire.DecodeCanonicalMap(release.CanonicalCommand,
		"schema", "command_id", "authority", "environment", "acted_at", "actor", "causation_command_id", "correlation_command_id", "operation", "context", "claim", "input", "blobs")
	if err != nil || fields["schema"] != "wipd.command/1" || fields["causation_command_id"] != nil {
		return emptyID, emptyEnvironment, wipdwire.ErrInvalidRecord
	}
	hash := sha256.Sum256(append([]byte("wipd/request-hash/v1\x00"), release.CanonicalCommand...))
	if release.RequestHash != "sha256:"+hex.EncodeToString(hash[:]) {
		return emptyID, emptyEnvironment, wipdwire.ErrInvalidRecord
	}
	commandID, commandOK := fields["command_id"].(string)
	correlation, correlationOK := fields["correlation_command_id"].(string)
	if !commandOK || commandID == "" || !correlationOK || correlation != commandID {
		return emptyID, emptyEnvironment, wipdwire.ErrInvalidRecord
	}
	authority, ok := fields["authority"].(map[string]any)
	if !ok || !wipdwire.ExactMapKeys(authority, "domain_id", "expected_epoch") || authority["domain_id"] != app.profile.domainID || authority["expected_epoch"] != app.profile.epoch {
		return emptyID, emptyEnvironment, wipdwire.ErrInvalidRecord
	}
	environment, ok := fields["environment"].(map[string]any)
	environmentID, environmentOK := environment["id"].(string)
	sequence, sequenceOK := environment["sequence"].(uint64)
	if !ok || !wipdwire.ExactMapKeys(environment, "id", "sequence") || !environmentOK || !ulidPattern.MatchString(environmentID) || !sequenceOK || sequence == 0 {
		return emptyID, emptyEnvironment, wipdwire.ErrInvalidRecord
	}
	operationFields, ok := fields["operation"].(map[string]any)
	if !ok || !wipdwire.ExactMapKeys(operationFields, "name", "version") || operationFields["name"] != "claim.release" || operationFields["version"] != uint64(1) {
		return emptyID, emptyEnvironment, wipdwire.ErrInvalidRecord
	}
	contextFields, ok := fields["context"].(map[string]any)
	if !ok || !wipdwire.ExactMapKeys(contextFields, "repo_id", "clone_id", "worktree_id") || contextFields["repo_id"] != app.repoID ||
		contextFields["clone_id"] != nil || contextFields["worktree_id"] != nil {
		return emptyID, emptyEnvironment, wipdwire.ErrInvalidRecord
	}
	claim, ok := fields["claim"].(map[string]any)
	if !ok || !wipdwire.ExactMapKeys(claim, "id", "epoch") || claim["id"] != release.Barrier.Claim.ID || claim["epoch"] != release.Barrier.Claim.Epoch {
		return emptyID, emptyEnvironment, wipdwire.ErrInvalidRecord
	}
	input, ok := fields["input"].(map[string]any)
	if !ok || !wipdwire.ExactMapKeys(input, "barrier") {
		return emptyID, emptyEnvironment, wipdwire.ErrInvalidRecord
	}
	commandBarrier, err := wipdwire.EncodeCanonical(input["barrier"])
	requestBarrier, requestErr := wipdwire.EncodeCanonical(release.Barrier)
	if err != nil || requestErr != nil || !bytes.Equal(commandBarrier, requestBarrier) {
		return emptyID, emptyEnvironment, wipdwire.ErrInvalidRecord
	}
	blobs, ok := fields["blobs"].([]any)
	if !ok || len(blobs) != 0 {
		return emptyID, emptyEnvironment, wipdwire.ErrInvalidRecord
	}
	actor, actorOK := fields["actor"].(string)
	actedAt, timeOK := fields["acted_at"].(string)
	parsed, timeErr := time.Parse(time.RFC3339Nano, actedAt)
	if !actorOK || actor == "" || !timeOK || timeErr != nil || parsed.UTC().Format(time.RFC3339Nano) != actedAt || !bytes.HasSuffix([]byte(actedAt), []byte("Z")) {
		return emptyID, emptyEnvironment, wipdwire.ErrInvalidRecord
	}
	return commandID, environmentID, nil
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
