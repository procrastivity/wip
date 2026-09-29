package wipdauthority

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"net/http"
	"sort"
	"time"

	"github.com/procrastivity/wip/internal/authoritystore"
	"github.com/procrastivity/wip/internal/wipdwire"
)

func (app *m5LabHandler) serveClaimAcquire(writer http.ResponseWriter, request *http.Request, body *bufio.Reader, frame wipdwire.Frame, environment authoritystore.EnvironmentCertificate) {
	var acquire wipdwire.ClaimAcquire
	if err := wipdwire.DecodeCanonical(frame.Payload, &acquire,
		"schema", "canonical_command", "request_hash", "installed", "deadline"); err != nil ||
		acquire.Schema != "wipd.claim-acquire/1" || len(acquire.CanonicalCommand) == 0 || acquire.Deadline != nil {
		writeLabProblem(writer, frame.RequestID, "protocol.malformed-message")
		return
	}
	command, err := wipdwire.DecodeCanonicalMap(acquire.CanonicalCommand,
		"schema", "command_id", "authority", "environment", "acted_at", "actor", "causation_command_id", "correlation_command_id", "operation", "context", "claim", "input", "blobs")
	if err != nil || command["schema"] != "wipd.command/1" {
		writeLabProblem(writer, frame.RequestID, "protocol.malformed-message")
		return
	}
	commandID, ok := command["command_id"].(string)
	if !ok || !ulidPattern.MatchString(commandID) {
		writeLabProblem(writer, frame.RequestID, "protocol.malformed-message")
		return
	}
	admissionCtx, cancel := context.WithCancelCause(request.Context())
	defer cancel(context.Canceled)
	watchLabCommandControl(request.Context(), body, frame.RequestID, cancel)
	status, err := app.store.SubmitClaimAcquire(admissionCtx, acquire.CanonicalCommand, acquire.RequestHash,
		authorityAnchor(acquire.Installed), *request.TLS, time.Now().UTC())
	if err != nil {
		writeLabProblem(writer, frame.RequestID, submissionProblem(err))
		return
	}
	var grant authoritystore.ClaimGrant
	if !status.Pending {
		status, grant, err = app.store.QueryClaimGrant(context.Background(), app.profile.domainID, commandID, acquire.RequestHash,
			app.profile.epoch, *request.TLS, environment.EnvironmentID, time.Now().UTC())
		if err != nil {
			writeLabProblem(writer, frame.RequestID, receiptQueryProblem(err))
			return
		}
	} else {
		owner := status.Owner
		if owner == nil {
			owner, err = app.store.RecoverClaimAcquire(context.Background(), acquire.CanonicalCommand, acquire.RequestHash)
			if errors.Is(err, authoritystore.ErrNotOwner) {
				app.writeAcquireAccepted(writer, frame.RequestID, commandID, acquire.RequestHash)
				return
			}
			if err != nil {
				writeLabProblem(writer, frame.RequestID, "authority.unavailable")
				return
			}
		}
		allocation, allocationErr := newAcquireAllocation(time.Now().UTC(), acquire.Installed)
		if allocationErr != nil {
			_ = app.store.AbandonClaimLifecycleExecution(owner)
			writeLabProblem(writer, frame.RequestID, "authority.unavailable")
			return
		}
		status, grant, err = app.store.CompleteClaimAcquire(context.Background(), owner, allocation, time.Now().UTC(), app.sign)
		if err != nil {
			_ = app.store.AbandonClaimLifecycleExecution(owner)
			writeLabProblem(writer, frame.RequestID, "authority.unavailable")
			return
		}
	}
	if status.Pending || len(status.Receipt) == 0 {
		app.writeAcquireAccepted(writer, frame.RequestID, commandID, acquire.RequestHash)
		return
	}
	if grant.ID == "" {
		accepted, encodeErr := wipdwire.EncodeCanonical(wipdwire.SubmissionAccepted{
			Schema: "wipd.submission-accepted/1", DomainID: app.profile.domainID, Epoch: app.profile.epoch,
			CommandID: commandID, RequestHash: acquire.RequestHash,
		})
		if encodeErr != nil {
			writeLabProblem(writer, frame.RequestID, "authority.unavailable")
			return
		}
		if err = writeLabFrames(writer, frame.RequestID, []labFrameRecord{
			{kind: "submission.accepted", payload: accepted}, {kind: "command.terminal", payload: status.Receipt},
		}, nil); err != nil {
			return
		}
		return
	}
	product, err := acquireGrantFrames(app.profile.domainID, app.profile.epoch, commandID, acquire.RequestHash, status.Receipt, grant)
	if err != nil {
		writeLabProblem(writer, frame.RequestID, "authority.unavailable")
		return
	}
	if err = writeLabFrames(writer, frame.RequestID, product, nil); err != nil {
		writeLabProblem(writer, frame.RequestID, "authority.unavailable")
	}
}

func (app *m5LabHandler) writeAcquireAccepted(writer http.ResponseWriter, requestID, commandID, requestHash string) {
	payload, err := wipdwire.EncodeCanonical(wipdwire.SubmissionAccepted{
		Schema: "wipd.submission-accepted/1", DomainID: app.profile.domainID, Epoch: app.profile.epoch,
		CommandID: commandID, RequestHash: requestHash,
	})
	if err != nil {
		writeLabProblem(writer, requestID, "authority.unavailable")
		return
	}
	writeLabFrame(writer, wipdwire.Frame{RequestID: requestID, Kind: "submission.accepted", Payload: payload})
}

func acquireGrantFrames(domain string, epoch uint64, commandID, requestHash string, receipt []byte, grant authoritystore.ClaimGrant) ([]labFrameRecord, error) {
	if len(grant.Wrapper) == 0 || len(grant.Delta) == 0 || len(grant.Manifest) == 0 || len(grant.End) == 0 ||
		len(receipt) == 0 || len(grant.Delta) > labMaxTransferBytes || len(grant.Manifest) > labMaxTransferBytes {
		return nil, authoritystore.ErrInvalidStore
	}
	var delta struct {
		Start  wipdwire.PrefixAnchor  `cbor:"start"`
		End    wipdwire.PrefixAnchor  `cbor:"end"`
		Events []wipdwire.EventRecord `cbor:"events"`
	}
	if err := wipdwire.DecodeCanonical(grant.Delta, &delta, "start", "end", "events"); err != nil ||
		len(delta.Events) > labMaxTransferEvents {
		return nil, authoritystore.ErrInvalidStore
	}
	var manifest wipdwire.BlobManifest
	if err := wipdwire.DecodeCanonical(grant.Manifest, &manifest,
		"schema", "domain_id", "authority_epoch", "as_of", "entries", "manifest_digest"); err != nil ||
		manifest.Schema != "wipd.blob-manifest/1" || manifest.DomainID != domain || manifest.Epoch != epoch ||
		!sameAuthorityAnchor(manifest.AsOf, delta.End) || len(manifest.Entries) > labMaxManifestEntries {
		return nil, authoritystore.ErrInvalidStore
	}
	if len(delta.Events) == 0 || delta.End.EventCount < delta.Start.EventCount ||
		delta.End.EventCount-delta.Start.EventCount != uint64(len(delta.Events)) {
		return nil, authoritystore.ErrInvalidStore
	}
	product := []labFrameRecord{}
	accepted, err := wipdwire.EncodeCanonical(wipdwire.SubmissionAccepted{
		Schema: "wipd.submission-accepted/1", DomainID: domain, Epoch: epoch, CommandID: commandID, RequestHash: requestHash,
	})
	if err != nil {
		return nil, err
	}
	product = append(product, labFrameRecord{kind: "submission.accepted", payload: accepted})
	product = append(product, labFrameRecord{kind: "command.terminal", payload: bytes.Clone(receipt)})
	product = append(product, labFrameRecord{kind: "claim.grant.start", payload: bytes.Clone(grant.Wrapper)})
	var eventBytes int
	for _, event := range delta.Events {
		if len(event.Record) == 0 || eventBytes > labMaxTransferBytes-len(event.Record) {
			return nil, authoritystore.ErrResourceLimit
		}
		eventBytes += len(event.Record)
		payload, encodeErr := wipdwire.EncodeCanonical(event)
		if encodeErr != nil {
			return nil, encodeErr
		}
		product = append(product, labFrameRecord{kind: "event.record", payload: payload})
	}
	if err = wipdwire.DecodeCanonical(grant.End, new(map[string]any),
		"schema", "grant_id", "verified_prefix", "verified_blob_manifest_digest", "complete"); err != nil {
		return nil, authoritystore.ErrInvalidStore
	}
	product = append(product, labFrameRecord{kind: "blob.manifest", payload: bytes.Clone(grant.Manifest)})
	product = append(product, labFrameRecord{kind: "claim.grant.end", payload: bytes.Clone(grant.End)})
	return product, nil
}

func newAcquireAllocation(now time.Time, installed wipdwire.PrefixAnchor) (authoritystore.AcquireAllocation, error) {
	ids := make([]string, 8)
	seen := make(map[string]struct{}, len(ids))
	for index := range ids {
		for {
			id, err := randomULID(now)
			if err != nil {
				return authoritystore.AcquireAllocation{}, err
			}
			if _, exists := seen[id]; !exists {
				ids[index] = id
				seen[id] = struct{}{}
				break
			}
		}
	}
	events := append([]string(nil), ids[5:]...)
	sort.Strings(events)
	return authoritystore.AcquireAllocation{
		ClaimID: ids[0], BatchID: ids[1], GrantID: ids[2], SnapshotID: ids[3], JournalID: ids[4],
		EventIDs: events, Installed: authorityAnchor(installed),
	}, nil
}

func sameAuthorityAnchor(left, right wipdwire.PrefixAnchor) bool {
	return left.EventCount == right.EventCount && left.Digest == right.Digest &&
		(left.EventID == nil) == (right.EventID == nil) && (left.EventID == nil || *left.EventID == *right.EventID)
}
