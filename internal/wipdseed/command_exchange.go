package wipdseed

import (
	"bytes"
	"context"
	"crypto/x509"
	"encoding/json"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"time"

	"github.com/procrastivity/wip/internal/operation"
	"github.com/procrastivity/wip/internal/wipdauthority"
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
	return state
}

func cloneRawMessages(values []json.RawMessage) []json.RawMessage {
	result := make([]json.RawMessage, len(values))
	for index := range values {
		result[index] = bytes.Clone(values[index])
	}
	return result
}

// Exchange sends one negotiated command, lifecycle, receipt, birth-journal
// control, or pull frame over the authenticated HTTP/2 session.
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
	case "claim.release":
		maxFrames = 2
	default:
		return nil, ErrInvalidClientState
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
	return state, nil
}
