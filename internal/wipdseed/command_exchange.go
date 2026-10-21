package wipdseed

import (
	"bytes"
	"context"
	"crypto/x509"
	"encoding/json"
	"errors"
	"fmt"
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

// SupportsNamedBatchRead reports the authority's explicit read-feature selection.
func (client *CommandExchangeClient) SupportsNamedBatchRead() bool {
	return client != nil && client.client != nil && client.limits.namedBatchRead
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
	return openCommandExchangeClient(ctx, profile, roots, directory, directory, "", operations, false, false)
}

// OpenCommandExchangeClientWithNamedBatchRead additionally requires the
// authority's explicit batch.read@v1 capability during negotiation.
func OpenCommandExchangeClientWithNamedBatchRead(ctx context.Context, profile wipdauthority.Profile, roots *x509.CertPool, directory string, operations []operation.ID) (*CommandExchangeClient, error) {
	return openCommandExchangeClient(ctx, profile, roots, directory, directory, "", operations, true, false)
}

// OpenStep11CommandExchangeClient requires the canonical profile-local
// Environment credential in addition to validating the Repo-bound state. The
// Step 11 runtime uses it before opening the Environment journal.
func OpenStep11CommandExchangeClient(ctx context.Context, profile wipdauthority.Profile, roots *x509.CertPool, directory string, operations []operation.ID) (*CommandExchangeClient, error) {
	return OpenStep11CommandExchangeClientWithCredential(ctx, profile, roots, directory, directory, operations)
}

// OpenStep11CommandExchangeClientWithCredential validates the Repo-bound
// command state in directory against the canonical profile-local Environment
// credential in credentialDirectory without copying private credential data.
func OpenStep11CommandExchangeClientWithCredential(ctx context.Context, profile wipdauthority.Profile, roots *x509.CertPool,
	directory, credentialDirectory string, operations []operation.ID,
) (*CommandExchangeClient, error) {
	return openCommandExchangeClient(ctx, profile, roots, directory, credentialDirectory, "", operations, true, true)
}

// OpenStep11CommandExchangeClientForRepoWithCredential opens authority M2
// state for one receipt-authorized Repo while validating the Environment
// credential from its separate canonical profile directory. This supports
// D54-selected Repo identities without pinning them into the route profile.
func OpenStep11CommandExchangeClientForRepoWithCredential(ctx context.Context, profile wipdauthority.Profile, roots *x509.CertPool,
	directory, credentialDirectory, repoID string, operations []operation.ID,
) (*CommandExchangeClient, error) {
	if !clientULIDPattern.MatchString(repoID) {
		return nil, ErrInvalidClientState
	}
	return openCommandExchangeClient(ctx, profile, roots, directory, credentialDirectory, repoID, operations, true, true)
}

func openCommandExchangeClient(ctx context.Context, profile wipdauthority.Profile, roots *x509.CertPool, directory, credentialDirectory, expectedRepoID string,
	operations []operation.ID, namedBatchRead, requireCanonicalCredential bool,
) (*CommandExchangeClient, error) {
	if ctx == nil || roots == nil || directory == "" || len(operations) == 0 {
		return nil, ErrInvalidClientState
	}
	state, err := loadInstalledClientStateModeWithCredential(directory, profile, requireCanonicalCredential, credentialDirectory, expectedRepoID)
	if err != nil {
		return nil, fmt.Errorf("validate installed M2 state: %w", err)
	}
	if requireCanonicalCredential {
		credential, credentialErr := loadEnvironmentCredential(credentialDirectory)
		if credentialErr != nil || credential.DomainID != state.DomainID || credential.Epoch != state.Epoch ||
			credential.EnvironmentID != state.EnvironmentID || credential.OwnerKeyID != state.OwnerKeyID || len(credential.CertificateDER) != 2 {
			clear(credential.PrivateKeyPKCS8)
			clear(state.PrivateKeyPKCS8)
			return nil, fmt.Errorf("validate canonical Environment credential: %w", ErrInvalidClientState)
		}
		clear(state.PrivateKeyPKCS8)
		state.PrivateKeyPKCS8 = bytes.Clone(credential.PrivateKeyPKCS8)
		state.CertificateDER = clone2D(credential.CertificateDER)
		state.SPKIDigest = credential.SPKIDigest
		clear(credential.PrivateKeyPKCS8)
	}
	privateKey, err := x509.ParsePKCS8PrivateKey(state.PrivateKeyPKCS8)
	if err != nil {
		clear(state.PrivateKeyPKCS8)
		return nil, ErrInvalidClientState
	}
	certificate, err := validateStoredCertificate(state, privateKey, time.Now())
	if err != nil {
		clear(state.PrivateKeyPKCS8)
		return nil, fmt.Errorf("validate stored Environment certificate: %w", err)
	}
	client, err := profile.HTTPClientWithCertificate(roots, &certificate)
	if err != nil {
		clear(state.PrivateKeyPKCS8)
		return nil, err
	}
	limits, err := negotiateRemoteCapabilities(ctx, client, profile.Origin(), operations, namedBatchRead)
	if err != nil {
		client.CloseIdleConnections()
		clear(state.PrivateKeyPKCS8)
		return nil, fmt.Errorf("negotiate M2 command capabilities: %w", err)
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
	case "scheduler.hook-result":
		maxFrames = 2
	case "batch.read":
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
	if kind == "batch.read" && !client.SupportsNamedBatchRead() {
		return nil, errors.New("protocol.unsupported-extension")
	}
	if kind == "scheduler.hook-result" && !client.SupportsOperation(operation.SchedulerOrchestrateV1.Metadata().Operation) {
		return nil, errors.New("protocol.unsupported-extension")
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
	return loadInstalledClientStateMode(directory, profile, false)
}

func loadInstalledClientStateMode(directory string, profile wipdauthority.Profile, requireCanonicalCredential bool) (ClientState, error) {
	return loadInstalledClientStateModeWithCredential(directory, profile, requireCanonicalCredential, directory, "")
}

func loadInstalledClientStateModeWithCredential(directory string, profile wipdauthority.Profile, requireCanonicalCredential bool,
	credentialDirectory, expectedRepoID string,
) (ClientState, error) {
	var empty ClientState
	if requireCanonicalCredential && credentialDirectory == "" {
		return empty, ErrInvalidClientState
	}
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
	if err = decoder.Decode(&state); err == nil {
		if trailingErr := decoder.Decode(new(any)); trailingErr != io.EOF {
			err = ErrInvalidClientState
		}
	}
	var validationErr error
	if err == nil {
		if expectedRepoID == "" {
			validationErr = validateInstalledState(state, profile)
		} else {
			validationErr = validateInstalledStateForRepo(state, profile, expectedRepoID)
		}
	}
	if err != nil || validationErr != nil {
		clear(state.PrivateKeyPKCS8)
		return empty, ErrInvalidClientState
	}
	if requireCanonicalCredential {
		err = validateStateCredentialRequired(credentialDirectory, state)
	} else {
		err = validateStateCredential(directory, state)
	}
	if err != nil {
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
