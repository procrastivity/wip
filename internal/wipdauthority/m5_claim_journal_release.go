package wipdauthority

import (
	"bufio"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"net/http"
	"os"
	"sort"
	"time"

	"github.com/procrastivity/wip/internal/authoritystore"
	"github.com/procrastivity/wip/internal/wipdwire"
)

func (app *m5LabHandler) serveAcquiredClaimRelease(writer http.ResponseWriter, request *http.Request, body *bufio.Reader,
	frame wipdwire.Frame, environment authoritystore.EnvironmentCertificate, release wipdwire.ClaimRelease,
) {
	commandID, environmentID, err := app.validateAcquiredClaimRelease(release)
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
		fmt.Fprintf(os.Stderr, "debug authority acquired-claim submit: %v\n", err)
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
		owner, err = app.store.RecoverClaimLifecycle(context.Background(), release.CanonicalCommand, release.RequestHash)
		if err != nil && !errors.Is(err, authoritystore.ErrNotOwner) {
			fmt.Fprintf(os.Stderr, "debug authority acquired-claim recover: %v\n", err)
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
	firstEvent, err := randomULID(time.Now().UTC())
	if err != nil {
		_ = app.store.AbandonClaimLifecycleExecution(owner)
		return
	}
	secondEvent, err := randomULID(time.Now().UTC())
	if err != nil {
		_ = app.store.AbandonClaimLifecycleExecution(owner)
		return
	}
	eventIDs := []string{firstEvent, secondEvent}
	sort.Strings(eventIDs)
	completed := make(chan struct {
		status authoritystore.CommandStatus
		err    error
	}, 1)
	go func() {
		status, completeErr := app.store.CompleteClaimLifecycle(context.Background(), owner, "", eventIDs, time.Now().UTC(), app.sign)
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
		if result.err != nil {
			fmt.Fprintf(os.Stderr, "debug authority acquired-claim complete: %v\n", result.err)
		}
		if result.err == nil && !result.status.Pending && len(result.status.Receipt) > 0 {
			writeLabFrameContinuation(writer, wipdwire.Frame{RequestID: frame.RequestID, Sequence: 1, Kind: "command.terminal", Payload: result.status.Receipt})
		}
	case <-admissionCtx.Done():
		// Once the exact claim.release identity is durable, cancellation ends
		// only this wait. Recovery retains the same command ID and request hash.
	}
}

func (app *m5LabHandler) validateAcquiredClaimRelease(release wipdwire.ClaimRelease) (string, string, error) {
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
	if !commandOK || !ulidPattern.MatchString(commandID) || !correlationOK || correlation != commandID {
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
		!isULID(contextFields["clone_id"]) || !isULID(contextFields["worktree_id"]) {
		return emptyID, emptyEnvironment, wipdwire.ErrInvalidRecord
	}
	claim, ok := fields["claim"].(map[string]any)
	if !ok || !wipdwire.ExactMapKeys(claim, "id", "epoch") || !isULID(claim["id"]) {
		return emptyID, emptyEnvironment, wipdwire.ErrInvalidRecord
	}
	claimEpoch, claimEpochOK := claim["epoch"].(uint64)
	if !claimEpochOK || claimEpoch == 0 || release.Barrier.Schema != "wipd.journal-barrier/1" ||
		release.Barrier.Claim.ID != claim["id"] || release.Barrier.Claim.Epoch != claimEpoch ||
		!ulidPattern.MatchString(release.Barrier.Journal) || !release.Barrier.Sealed || release.Barrier.Unresolved != 0 ||
		release.Barrier.Quarantined != 0 || release.Barrier.Count != release.Barrier.Last || release.Barrier.Count != release.Barrier.Receipts ||
		!canonicalSPKIDigestPattern.MatchString(release.Barrier.Digest) {
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

func isULID(value any) bool {
	text, ok := value.(string)
	return ok && ulidPattern.MatchString(text)
}
