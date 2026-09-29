package wipdauthority

import (
	"bufio"
	"context"
	"errors"
	"net/http"
	"time"

	"github.com/procrastivity/wip/internal/authoritystore"
	"github.com/procrastivity/wip/internal/operation"
	"github.com/procrastivity/wip/internal/wipdwire"
)

const maxLabUploadChunk = 65_536

func contentUploadNegotiated(request *http.Request) bool {
	session := connectionSession(request)
	if session == nil {
		return false
	}
	session.mu.Lock()
	defer session.mu.Unlock()
	if !session.negotiated || session.failed || session.negotiating {
		return false
	}
	_, writes := session.operations[operation.ContentWriteOnceV1.Metadata().Operation]
	_, findings := session.operations[operation.FindingAppendV1.Metadata().Operation]
	return writes || findings
}

func (app *m5LabHandler) serveBlobUpload(writer http.ResponseWriter, request *http.Request, body *bufio.Reader, frame wipdwire.Frame) {
	var (
		response wipdwire.Frame
		code     string
	)
	switch frame.Kind {
	case "blob.upload-start":
		var start wipdwire.BlobUploadStart
		if wipdwire.DecodeCanonical(frame.Payload, &start, "schema", "domain_id", "digest", "byte_length", "resume_offset") != nil ||
			start.Schema != "wipd.blob-upload/1" || start.DomainID != app.profile.domainID ||
			!canonicalSPKIDigestPattern.MatchString(start.Digest) || start.ByteLength > labMaxTransferBytes || start.ResumeOffset > start.ByteLength {
			writeLabProblem(writer, frame.RequestID, "protocol.malformed-message")
			return
		}
		ctx, cancel, arbiter, ok := app.beginReadOnlyExchange(writer, request, body, frame.RequestID)
		if !ok {
			return
		}
		defer cancel(context.Canceled)
		upload, err := app.store.StartBlob(ctx, start.DomainID, app.profile.epoch, start.Digest, start.ByteLength, time.Now().UTC())
		if err != nil {
			code = blobUploadProblem(err)
		} else if upload.Available {
			response = wipdwire.Frame{RequestID: frame.RequestID, Kind: "blob.available", Payload: mustBlobPayload(wipdwire.BlobAvailable{
				Schema: "wipd.blob-available/1", Digest: start.Digest, ByteLength: start.ByteLength,
			})}
		} else {
			response = wipdwire.Frame{RequestID: frame.RequestID, Kind: "blob.upload-ready", Payload: mustBlobPayload(wipdwire.BlobUploadReady{
				Schema: "wipd.blob-upload-ready/1", Digest: start.Digest, ByteLength: start.ByteLength, Offset: upload.Offset,
			})}
		}
		writeReadOnlyFinal(ctx, arbiter, writer, frame.RequestID, response, code)
	case "blob.upload-chunk":
		var chunk wipdwire.BlobUploadChunk
		if wipdwire.DecodeCanonical(frame.Payload, &chunk, "schema", "domain_id", "digest", "offset", "data") != nil ||
			chunk.Schema != "wipd.blob-upload-chunk/1" || chunk.DomainID != app.profile.domainID ||
			!canonicalSPKIDigestPattern.MatchString(chunk.Digest) || len(chunk.Data) == 0 || len(chunk.Data) > maxLabUploadChunk {
			writeLabProblem(writer, frame.RequestID, "protocol.malformed-message")
			return
		}
		ctx, cancel, arbiter, ok := app.beginReadOnlyExchange(writer, request, body, frame.RequestID)
		if !ok {
			return
		}
		defer cancel(context.Canceled)
		offset, err := app.store.StageBlobChunk(ctx, chunk.DomainID, app.profile.epoch, chunk.Digest, chunk.Offset, chunk.Data)
		if err != nil {
			code = blobUploadProblem(err)
		} else {
			response = wipdwire.Frame{RequestID: frame.RequestID, Kind: "blob.upload-offset", Payload: mustBlobPayload(wipdwire.BlobUploadOffset{
				Schema: "wipd.blob-upload-offset/1", Digest: chunk.Digest, Offset: offset,
			})}
		}
		writeReadOnlyFinal(ctx, arbiter, writer, frame.RequestID, response, code)
	case "blob.upload-finish":
		var finish wipdwire.BlobUploadFinish
		if wipdwire.DecodeCanonical(frame.Payload, &finish, "schema", "domain_id", "digest", "byte_length") != nil ||
			finish.Schema != "wipd.blob-upload-finish/1" || finish.DomainID != app.profile.domainID ||
			!canonicalSPKIDigestPattern.MatchString(finish.Digest) || finish.ByteLength > labMaxTransferBytes {
			writeLabProblem(writer, frame.RequestID, "protocol.malformed-message")
			return
		}
		ctx, cancel, arbiter, ok := app.beginReadOnlyExchange(writer, request, body, frame.RequestID)
		if !ok {
			return
		}
		defer cancel(context.Canceled)
		upload, err := app.store.StartBlob(ctx, finish.DomainID, app.profile.epoch, finish.Digest, finish.ByteLength, time.Now().UTC())
		if err == nil && !upload.Available && upload.Offset != finish.ByteLength {
			err = errors.New("transfer.incomplete")
		}
		if err == nil {
			err = app.store.FinishBlob(ctx, finish.DomainID, app.profile.epoch, finish.Digest)
		}
		if err != nil {
			code = blobUploadProblem(err)
		} else {
			response = wipdwire.Frame{RequestID: frame.RequestID, Kind: "blob.staged", Payload: mustBlobPayload(wipdwire.BlobStaged{
				Schema: "wipd.blob-staged/1", Digest: finish.Digest, ByteLength: finish.ByteLength,
			})}
		}
		writeReadOnlyFinal(ctx, arbiter, writer, frame.RequestID, response, code)
	default:
		writeLabProblem(writer, frame.RequestID, "protocol.unsupported-kind")
	}
}

func mustBlobPayload(value any) []byte {
	payload, err := wipdwire.EncodeCanonical(value)
	if err != nil {
		return nil
	}
	return payload
}

func blobUploadProblem(err error) string {
	switch {
	case errors.Is(err, authoritystore.ErrBlobLength):
		return "blob.length-mismatch"
	case errors.Is(err, authoritystore.ErrBlobDigest):
		return "blob.digest-mismatch"
	case errors.Is(err, authoritystore.ErrBlobOffset):
		return "transfer.resume-invalid"
	case errors.Is(err, authoritystore.ErrBlobRange):
		return "blob.range-invalid"
	case errors.Is(err, authoritystore.ErrBlobAbsent):
		return "blob.not-found"
	case errors.Is(err, authoritystore.ErrResourceLimit):
		return "resource.limit"
	case errors.Is(err, authoritystore.ErrFenced), errors.Is(err, authoritystore.ErrInvalidProof):
		return "auth.authority-binding-mismatch"
	default:
		if err.Error() == "transfer.incomplete" {
			return "transfer.incomplete"
		}
		return "authority.unavailable"
	}
}
