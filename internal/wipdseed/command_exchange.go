package wipdseed

import (
	"bytes"
	"context"
	"crypto/x509"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"time"

	"github.com/procrastivity/wip/internal/operation"
	"github.com/procrastivity/wip/internal/wipdauthority"
	"github.com/procrastivity/wip/internal/wipdjournal"
	"github.com/procrastivity/wip/internal/wipdwire"
)

// CommandExchangeClient is the authenticated negotiated M2 command/receipt/
// pull adapter for one installed Environment identity.
type CommandExchangeClient struct {
	profile wipdauthority.Profile
	client  *http.Client
	limits  sessionLimits
	state   ClientState
}

// SupportsCommandSubmitV2 reports the feature selected by this authority
// session. Local support alone never authorizes sending a v2 envelope.
func (client *CommandExchangeClient) SupportsCommandSubmitV2() bool {
	return client != nil && client.client != nil && client.limits.commandSubmitV2
}

// SupportsOperation reports an exact authority operation selected during
// this authenticated session's capability negotiation.
func (client *CommandExchangeClient) SupportsOperation(id operation.ID) bool {
	if client == nil || client.client == nil {
		return false
	}
	for _, selected := range client.limits.operations {
		if selected == id {
			return true
		}
	}
	return false
}

// OpenCommandExchangeClient validates the installed Environment identity,
// proves its private-key possession with mTLS, and negotiates the exact birth
// operation set required by the caller.
func OpenCommandExchangeClient(ctx context.Context, profile wipdauthority.Profile, roots *x509.CertPool, directory string, operations []operation.ID) (*CommandExchangeClient, error) {
	if ctx == nil || roots == nil || directory == "" || len(operations) == 0 {
		return nil, ErrInvalidClientState
	}
	state, err := loadInstalledClientState(directory, profile)
	if err != nil {
		return nil, err
	}
	privateKey, err := x509.ParsePKCS8PrivateKey(state.PrivateKeyPKCS8)
	if err != nil {
		clear(state.PrivateKeyPKCS8)
		return nil, ErrInvalidClientState
	}
	certificate, err := validateStoredCertificate(state, privateKey, time.Now())
	if err != nil {
		clear(state.PrivateKeyPKCS8)
		return nil, err
	}
	client, err := profile.HTTPClientWithCertificate(roots, &certificate)
	if err != nil {
		clear(state.PrivateKeyPKCS8)
		return nil, err
	}
	limits, err := negotiateRemoteOperations(ctx, client, profile.Origin(), operations)
	if err != nil {
		client.CloseIdleConnections()
		clear(state.PrivateKeyPKCS8)
		return nil, err
	}
	return &CommandExchangeClient{profile: profile, client: client, limits: limits, state: state}, nil
}

// State returns a deep copy of the validated public identity and enrollment
// prefix. Private key and certificate bytes are intentionally not returned.
func (client *CommandExchangeClient) State() ClientState {
	if client == nil {
		return ClientState{}
	}
	state := client.state
	state.PrivateKeyPKCS8 = nil
	state.CertificateDER = nil
	if state.Prefix.EventID != nil {
		eventID := *state.Prefix.EventID
		state.Prefix.EventID = &eventID
	}
	state.EventRecords = cloneEventRecords(state.EventRecords)
	state.ManifestEntries = append([]wipdwire.BlobManifestEntry(nil), state.ManifestEntries...)
	state.Projections = cloneRawMessages(state.Projections)
	state.StepProjections = cloneRawMessages(state.StepProjections)
	state.ContentProjections = cloneRawMessages(state.ContentProjections)
	if state.Step8Projection != nil {
		projection := *state.Step8Projection
		projection.Dependencies = append(projection.Dependencies[:0:0], projection.Dependencies...)
		projection.References = append(projection.References[:0:0], projection.References...)
		projection.Aggregates = append(projection.Aggregates[:0:0], projection.Aggregates...)
		projection.Candidates = append(projection.Candidates[:0:0], projection.Candidates...)
		projection.ConfigHistory = append(projection.ConfigHistory[:0:0], projection.ConfigHistory...)
		state.Step8Projection = &projection
	}
	return state
}

func cloneRawMessages(values []json.RawMessage) []json.RawMessage {
	result := make([]json.RawMessage, len(values))
	for index := range values {
		result[index] = bytes.Clone(values[index])
	}
	return result
}

// Exchange sends one negotiated command, lifecycle, receipt, journal control,
// or pull frame over the authenticated HTTP/2 session.
func (client *CommandExchangeClient) Exchange(ctx context.Context, kind string, payload any) ([]wipdwire.Frame, error) {
	if client == nil || client.client == nil || ctx == nil {
		return nil, ErrInvalidClientState
	}
	maxFrames := 1
	switch kind {
	case "command.submit":
		maxFrames = 2
	case "receipt.query":
		maxFrames = 1
	case "pull.request":
		maxFrames = maxClientTransferEvents + 3
	case "birth-journal.ack":
		maxFrames = 1
	case "claim-journal.query", "claim-journal.ack", "claim-journal.seal":
		maxFrames = 1
	case "claim.release":
		maxFrames = 2
	case "claim.acquire":
		maxFrames = maxClientTransferEvents + 5
	case "blob.upload-start", "blob.upload-chunk", "blob.upload-finish":
		maxFrames = 1
	default:
		return nil, ErrInvalidClientState
	}
	if kind == "command.submit" {
		encoded, err := wipdwire.EncodeCanonical(payload)
		if err != nil {
			return nil, ErrInvalidClientState
		}
		_, version2, err := wipdwire.DecodeCommandSubmit(encoded)
		if err != nil {
			return nil, ErrInvalidClientState
		}
		if version2 && !client.SupportsCommandSubmitV2() {
			return nil, errors.New("protocol.unsupported-extension")
		}
	}
	requestFrame, requestID, err := encodeRequestFrame(kind, payload)
	if err != nil || !withinSessionFrame(requestFrame, client.limits) {
		return nil, ErrInvalidClientState
	}
	frames, err := postFramesWithinSession(ctx, client.client, client.profile.Origin()+"/wipd/v1/exchange",
		requestFrame, requestID, maxFrames, client.limits)
	if err != nil {
		return nil, err
	}
	for index, frame := range frames {
		if frame.Sequence != uint64(index) {
			return nil, ErrInvalidClientState
		}
		if frame.Kind == "problem" {
			return nil, decodeProblem(frame.Payload)
		}
	}
	return frames, nil
}

// UploadBlob resumes one locally verified staged blob at the authority's
// durable contiguous offset. It only creates a verified temporary product;
// terminal command completion owns promotion into referenced domain state.
func (client *CommandExchangeClient) UploadBlob(ctx context.Context, digest string, size uint64, reader io.Reader) error {
	if client == nil || client.client == nil || ctx == nil || reader == nil || !validDigest(digest) ||
		size > uint64(client.limits.streamBytes) || client.state.DomainID == "" || client.state.Epoch == 0 {
		return ErrInvalidClientState
	}
	startFrames, err := client.Exchange(ctx, "blob.upload-start", wipdwire.BlobUploadStart{
		Schema: "wipd.blob-upload/1", DomainID: client.state.DomainID,
		Digest: digest, ByteLength: size, ResumeOffset: 0,
	})
	if err != nil {
		return err
	}
	if len(startFrames) != 1 {
		return ErrInvalidClientState
	}
	var offset uint64
	switch startFrames[0].Kind {
	case "blob.available":
		var available wipdwire.BlobAvailable
		if wipdwire.DecodeCanonical(startFrames[0].Payload, &available, "schema", "digest", "byte_length") != nil ||
			available.Schema != "wipd.blob-available/1" || available.Digest != digest || available.ByteLength != size {
			return ErrInvalidClientState
		}
		return nil
	case "blob.upload-ready":
		var ready wipdwire.BlobUploadReady
		if wipdwire.DecodeCanonical(startFrames[0].Payload, &ready, "schema", "digest", "byte_length", "offset") != nil ||
			ready.Schema != "wipd.blob-upload-ready/1" || ready.Digest != digest || ready.ByteLength != size || ready.Offset > size {
			return ErrInvalidClientState
		}
		offset = ready.Offset
	default:
		return ErrInvalidClientState
	}
	if offset != 0 {
		if _, err = io.CopyN(io.Discard, reader, int64(offset)); err != nil {
			return ErrInvalidClientState
		}
	}
	chunkLimit := client.limits.chunkData
	if frameLimit := client.limits.frameBody - 512; chunkLimit > frameLimit {
		chunkLimit = frameLimit
	}
	if chunkLimit <= 0 {
		return ErrInvalidClientState
	}
	for offset < size {
		length := uint64(chunkLimit)
		if remaining := size - offset; length > remaining {
			length = remaining
		}
		chunk := make([]byte, int(length))
		if _, err = io.ReadFull(reader, chunk); err != nil {
			clear(chunk)
			return ErrInvalidClientState
		}
		frames, exchangeErr := client.Exchange(ctx, "blob.upload-chunk", wipdwire.BlobUploadChunk{
			Schema: "wipd.blob-upload-chunk/1", DomainID: client.state.DomainID,
			Digest: digest, Offset: offset, Data: chunk,
		})
		clear(chunk)
		if exchangeErr != nil {
			return exchangeErr
		}
		if len(frames) != 1 || frames[0].Kind != "blob.upload-offset" {
			return ErrInvalidClientState
		}
		var acknowledged wipdwire.BlobUploadOffset
		if wipdwire.DecodeCanonical(frames[0].Payload, &acknowledged, "schema", "digest", "offset") != nil ||
			acknowledged.Schema != "wipd.blob-upload-offset/1" || acknowledged.Digest != digest || acknowledged.Offset != offset+length {
			return ErrInvalidClientState
		}
		offset = acknowledged.Offset
	}
	frames, err := client.Exchange(ctx, "blob.upload-finish", wipdwire.BlobUploadFinish{
		Schema: "wipd.blob-upload-finish/1", DomainID: client.state.DomainID,
		Digest: digest, ByteLength: size,
	})
	if err != nil {
		return err
	}
	if len(frames) != 1 || frames[0].Kind != "blob.staged" {
		return ErrInvalidClientState
	}
	var staged wipdwire.BlobStaged
	if wipdwire.DecodeCanonical(frames[0].Payload, &staged, "schema", "digest", "byte_length") != nil ||
		staged.Schema != "wipd.blob-staged/1" || staged.Digest != digest || staged.ByteLength != size {
		return ErrInvalidClientState
	}
	return nil
}

// Close clears the in-memory Environment key and releases the HTTP/2 client.
func (client *CommandExchangeClient) Close() error {
	if client == nil {
		return nil
	}
	if client.client != nil {
		client.client.CloseIdleConnections()
		client.client = nil
	}
	clear(client.state.PrivateKeyPKCS8)
	client.state = ClientState{}
	return nil
}

func loadInstalledClientState(directory string, profile wipdauthority.Profile) (ClientState, error) {
	var empty ClientState
	lock, err := lockInstalledState(directory)
	if err != nil {
		return empty, err
	}
	defer func() { _ = lock.Close() }()
	path := filepath.Join(directory, stateName)
	info, err := os.Lstat(path)
	if err != nil {
		return empty, err
	}
	if !info.Mode().IsRegular() || info.Mode().Perm()&0o077 != 0 || info.Size() > maxClientStateBytes {
		return empty, ErrInvalidClientState
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return empty, err
	}
	defer clear(data)
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	var state ClientState
	if err = decoder.Decode(&state); err != nil || decoder.Decode(new(any)) != io.EOF || validateInstalledState(state, profile) != nil {
		clear(state.PrivateKeyPKCS8)
		return empty, ErrInvalidClientState
	}
	if state.Step8Projection == nil {
		state.Step8Projection, err = wipdjournal.FoldStep8Projection(state.EventRecords, state.DomainID)
		if err != nil {
			clear(state.PrivateKeyPKCS8)
			return empty, ErrInvalidClientState
		}
	}
	return state, nil
}
