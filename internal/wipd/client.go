package wipd

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/tls"
	"errors"
	"fmt"
	"io"
	"math/big"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/procrastivity/wip/internal/operation"
	"github.com/procrastivity/wip/internal/wipdprofile"
	"golang.org/x/net/http2"
)

const (
	connectTimeout    = 750 * time.Millisecond
	activationTimeout = 5 * time.Second
	activationPoll    = 20 * time.Millisecond
	frameIDAlphabet   = "0123456789ABCDEFGHJKMNPQRSTVWXYZ"
)

var (
	// ErrUnavailable reports a local transport that cannot be reached.
	ErrUnavailable = errors.New("transport.unavailable")
	// ErrOutcomeUnknown reports a lost response after possible dispatch.
	ErrOutcomeUnknown = errors.New("transport.outcome-unknown")
)

// Client is an authenticated local IPC session. It owns no database handle
// and never resolves a legacy WIP store.
type Client struct {
	transport  *http2.Transport
	httpClient *http.Client
	hello      serverHello
	parameters sessionParameters
	candidate  *daemonCandidate
	started    bool
}

type daemonCandidate struct {
	command *exec.Cmd
	done    chan struct{}
	waitErr error
}

// LocalStatus is the Step 1 readiness floor. These fields describe only local
// profile/process/fixture readiness; they make no authority or command claim.
type LocalStatus struct {
	ProcessReady    bool   `json:"process_ready"`
	ProfileVerified bool   `json:"profile_verified"`
	FixtureReady    bool   `json:"fixture_ready"`
	StatusCode      string `json:"status_code"`
}

// Connect performs bounded authenticated AF_UNIX IPC and M2 capability
// negotiation without starting a daemon.
func Connect(ctx context.Context, explicitProfileRoot string) (*Client, error) {
	profile, err := wipdprofile.Resolve(explicitProfileRoot)
	if err != nil {
		return nil, fmt.Errorf("%w: profile verification: %v", ErrUnavailable, err)
	}
	return connectProfile(ctx, profile)
}

// Activate first connects to a ready daemon. Only when the profile socket is
// proven absent or stale does it spawn one foreground wipd candidate; racing
// candidates converge through wipd's kernel singleton lock. An empty daemon
// executable name resolves only the explicit `wipd` program on PATH.
func Activate(ctx context.Context, explicitProfileRoot, daemonExecutable string) (*Client, error) {
	activationCtx, cancel := context.WithTimeout(ctx, activationTimeout)
	defer cancel()

	profile, err := wipdprofile.Resolve(explicitProfileRoot)
	if err != nil {
		return nil, fmt.Errorf("%w: profile verification: %v", ErrUnavailable, err)
	}
	if client, err := connectProfile(activationCtx, profile); err == nil {
		return client, nil
	}
	if err := activationCtx.Err(); err != nil {
		return nil, fmt.Errorf("%w: activation was cancelled before daemon start", ErrUnavailable)
	}

	canStart, err := socketHasNoListener(profile)
	if err != nil {
		return nil, fmt.Errorf("%w: cannot prove daemon absence: %v", ErrUnavailable, err)
	}
	if !canStart {
		return waitForProfile(activationCtx, profile, nil)
	}
	if daemonExecutable == "" {
		daemonExecutable, err = exec.LookPath("wipd")
		if err != nil {
			return nil, fmt.Errorf("%w: foreground wipd executable is unavailable", ErrUnavailable)
		}
	}

	command := exec.Command(daemonExecutable, "--profile-root", profile.Root)
	command.Stdout = io.Discard
	command.Stderr = io.Discard
	var candidate *daemonCandidate
	if err := command.Start(); err != nil {
		return nil, fmt.Errorf("%w: start foreground daemon: %v", ErrUnavailable, err)
	}
	candidate = &daemonCandidate{command: command, done: make(chan struct{})}
	go func() {
		candidate.waitErr = command.Wait()
		close(candidate.done)
	}()
	return waitForProfile(activationCtx, profile, candidate)
}

func waitForProfile(ctx context.Context, profile wipdprofile.Profile, candidate *daemonCandidate) (*Client, error) {
	ticker := time.NewTicker(activationPoll)
	defer ticker.Stop()
	for {
		if client, err := connectProfile(ctx, profile); err == nil {
			client.candidate = candidate
			if candidate == nil || candidateOwnsProfileLock(profile.Root, candidate) {
				client.started = candidate != nil
				return client, nil
			}
			// A competing Activate call may have reached the live daemon while
			// its own foreground candidate has not attempted the singleton lock
			// yet. Do not report convergence until that candidate has either
			// lost the lock or become the ready owner itself.
			select {
			case <-candidate.done:
				if profileLockPID(profile.Root) == candidate.command.Process.Pid {
					_ = client.Close()
					return nil, fmt.Errorf("%w: foreground wipd exited after taking the profile lock: %v", ErrUnavailable, candidate.waitErr)
				}
				// The candidate has now definitively lost the singleton lock,
				// so discard this possibly-stale readiness probe and recheck the
				// winner. The candidate will not be started a second time.
				_ = client.Close()
				candidate = nil
				continue
			case <-ctx.Done():
				_ = client.Close()
				return nil, fmt.Errorf("%w: readiness deadline expired", ErrUnavailable)
			case <-ticker.C:
				_ = client.Close()
				continue
			}
		}
		if candidate != nil {
			select {
			case <-candidate.done:
				if profileLockPID(profile.Root) == candidate.command.Process.Pid {
					return nil, fmt.Errorf("%w: foreground wipd exited before readiness: %v", ErrUnavailable, candidate.waitErr)
				}
			default:
			}
		}
		select {
		case <-ctx.Done():
			return nil, fmt.Errorf("%w: readiness deadline expired", ErrUnavailable)
		case <-ticker.C:
		}
	}
}

func candidateOwnsProfileLock(profileRoot string, candidate *daemonCandidate) bool {
	if candidate == nil || candidate.command == nil || candidate.command.Process == nil {
		return false
	}
	select {
	case <-candidate.done:
		return false
	default:
	}
	return profileLockPID(profileRoot) == candidate.command.Process.Pid
}

func profileLockPID(profileRoot string) int {
	// This private-file diagnostic only associates an activation child with its
	// attempted startup; the daemon's kernel flock remains the lock authority.
	contents, err := os.ReadFile(filepath.Join(profileRoot, lockFileName))
	if err != nil {
		return 0
	}
	line := strings.TrimSpace(string(contents))
	pidText, ok := strings.CutPrefix(line, "pid=")
	if !ok {
		return 0
	}
	pid, err := strconv.Atoi(pidText)
	if err != nil {
		return 0
	}
	return pid
}

// ProbeStatus checks readiness without activating or mutating a profile.
func ProbeStatus(ctx context.Context, explicitProfileRoot string) LocalStatus {
	profile, err := wipdprofile.Resolve(explicitProfileRoot)
	if err != nil {
		return LocalStatus{StatusCode: "profile.unavailable"}
	}
	status := LocalStatus{ProfileVerified: true, StatusCode: "transport.unavailable"}
	client, err := connectProfile(ctx, profile)
	if err != nil {
		return status
	}
	_ = client.Close()
	return readyStatus()
}

// Status returns the readiness floor established by this negotiated session.
func (c *Client) Status() LocalStatus {
	if c == nil || c.httpClient == nil {
		return LocalStatus{StatusCode: "transport.unavailable"}
	}
	return readyStatus()
}

func readyStatus() LocalStatus {
	return LocalStatus{
		ProcessReady:    true,
		ProfileVerified: true,
		FixtureReady:    true,
		StatusCode:      "wipd.ready",
	}
}

// StartedDaemon reports whether this Activate call's foreground candidate
// remained alive after readiness. It is local diagnostic state, not a wire
// field or a status-floor field.
func (c *Client) StartedDaemon() bool {
	return c != nil && c.started
}

// ExecuteCommand sends one immutable M1 command over this negotiated M2
// session. A response.end payload is decoded only as the bare M1 semantic
// Result mapping documented for the opt-in local fixture Handler. A transport
// loss after submission is reported as uncertain, never as refusal or failure.
func (c *Client) ExecuteCommand(ctx context.Context, command operation.Command) (operation.Result, error) {
	if c == nil || c.httpClient == nil {
		return operation.Result{}, &ExchangeError{Code: "transport.unavailable", Err: ErrUnavailable}
	}
	if !operationCapabilityContains(c.hello.operations, command.Request.Operation, identitySchemaV1) {
		return operation.Result{}, &ExchangeError{Code: string(operation.ProblemUnsupportedVersion)}
	}
	canonical, err := command.CanonicalBytes()
	if err != nil {
		return operation.Result{}, &ExchangeError{Code: "protocol.malformed-message", Err: err}
	}
	hash, err := command.RequestHash()
	if err != nil {
		return operation.Result{}, &ExchangeError{Code: "protocol.malformed-message", Err: err}
	}
	payload, err := encodePayload(map[string]any{
		"schema":            "wipd.command-submit/1",
		"canonical_command": canonical,
		"request_hash":      hash,
		"deadline":          nil,
	})
	if err != nil {
		return operation.Result{}, &ExchangeError{Code: "protocol.malformed-message", Err: err}
	}
	requestID, err := newFrameRequestID()
	if err != nil {
		return operation.Result{}, &ExchangeError{Code: "transport.unavailable", Err: err}
	}
	wire, err := encodeFrame(frameRecord{requestID: requestID, sequence: 0, kind: "command.submit", payload: payload}, uint32(c.parameters.maxFrameBody))
	if err != nil {
		return operation.Result{}, &ExchangeError{Code: "protocol.frame-too-large", Err: err}
	}
	response, err := c.post(ctx, exchangePath, wire)
	if err != nil {
		return operation.Result{}, uncertainExchange(err)
	}
	// Closing a read-only response cannot change the command's outcome.
	defer func() { _ = response.Body.Close() }()
	if response.ProtoMajor != 2 || response.StatusCode != http.StatusOK {
		return operation.Result{}, uncertainExchange(fmt.Errorf("unexpected HTTP/%d.%d status %s", response.ProtoMajor, response.ProtoMinor, response.Status))
	}
	frame, err := readFrame(response.Body, uint32(c.parameters.maxFrameBody))
	if err != nil {
		return operation.Result{}, uncertainExchange(err)
	}
	if frame.requestID != requestID || frame.sequence != 0 {
		return operation.Result{}, uncertainExchange(errWrongCorrelation)
	}
	if frame.kind == "problem" {
		code, err := problemCodeFromPayload(frame.payload)
		if err != nil {
			return operation.Result{}, uncertainExchange(err)
		}
		if _, err := readFrame(response.Body, uint32(c.parameters.maxFrameBody)); !errors.Is(err, io.EOF) {
			return operation.Result{}, &ExchangeError{Code: code, Err: err}
		}
		return operation.Result{}, &ExchangeError{Code: code}
	}
	if frame.kind != "response.end" {
		return operation.Result{}, uncertainExchange(errUnsupportedKind)
	}
	result, err := decodeOperationResultPayload(command.Request.Operation, frame.payload)
	if err != nil {
		return operation.Result{}, uncertainExchange(err)
	}
	if _, err := readFrame(response.Body, uint32(c.parameters.maxFrameBody)); !errors.Is(err, io.EOF) {
		return operation.Result{}, uncertainExchange(errOutOfOrder)
	}
	return result, nil
}

// ExchangeError keeps protocol no-effect problems distinct from semantic M1
// dispositions and from post-submission uncertainty.
type ExchangeError struct {
	Code      string
	Uncertain bool
	Err       error
}

func (e *ExchangeError) Error() string {
	if e == nil {
		return "wipd exchange error"
	}
	if e.Err != nil {
		return e.Code + ": " + e.Err.Error()
	}
	return e.Code
}

func (e *ExchangeError) Unwrap() error {
	if e == nil {
		return nil
	}
	return e.Err
}

func uncertainExchange(err error) *ExchangeError {
	return &ExchangeError{Code: "transport.outcome-unknown", Uncertain: true, Err: errors.Join(ErrOutcomeUnknown, err)}
}

// Close releases only this client's HTTP/2 resources. It does not stop a
// foreground daemon that may be serving other clients.
func (c *Client) Close() error {
	if c == nil || c.transport == nil {
		return nil
	}
	c.transport.CloseIdleConnections()
	c.httpClient = nil
	return nil
}

func connectProfile(ctx context.Context, profile wipdprofile.Profile) (*Client, error) {
	connectCtx, cancel := context.WithTimeout(ctx, connectTimeout)
	defer cancel()
	transport := &http2.Transport{
		AllowHTTP: true,
		DialTLSContext: func(ctx context.Context, _, _ string, _ *tls.Config) (net.Conn, error) {
			return (&net.Dialer{}).DialContext(ctx, "unix", filepath.Join(profile.Root, socketFileName))
		},
	}
	client := &Client{
		transport: transport,
		httpClient: &http.Client{
			Transport: transport,
			CheckRedirect: func(*http.Request, []*http.Request) error {
				return http.ErrUseLastResponse
			},
		},
	}
	if err := client.negotiate(connectCtx); err != nil {
		transport.CloseIdleConnections()
		return nil, fmt.Errorf("%w: local IPC negotiation: %v", ErrUnavailable, err)
	}
	return client, nil
}

func (c *Client) negotiate(ctx context.Context) error {
	requestID, err := newFrameRequestID()
	if err != nil {
		return err
	}
	payload, err := encodePayload(map[string]any{
		"protocol_min":     []any{uint64(1), uint64(0)},
		"protocol_max":     []any{uint64(1), uint64(0)},
		"identity_schemas": []any{identitySchemaV1},
		"operations": []any{map[string]any{
			"name":             "content.write-once",
			"versions":         []any{uint64(1)},
			"identity_schemas": []any{identitySchemaV1},
		}, map[string]any{
			"name":             "finding.append",
			"versions":         []any{uint64(1)},
			"identity_schemas": []any{identitySchemaV1},
		}, map[string]any{
			"name":             "matter.create",
			"versions":         []any{uint64(1)},
			"identity_schemas": []any{identitySchemaV1},
		}, map[string]any{
			"name":             "matter.finish",
			"versions":         []any{uint64(1)},
			"identity_schemas": []any{identitySchemaV1},
		}, map[string]any{
			"name":             "step.create",
			"versions":         []any{uint64(1)},
			"identity_schemas": []any{identitySchemaV1},
		}, map[string]any{
			"name":             "step.finish",
			"versions":         []any{uint64(1)},
			"identity_schemas": []any{identitySchemaV1},
		}, map[string]any{
			"name":             "step.start",
			"versions":         []any{uint64(1)},
			"identity_schemas": []any{identitySchemaV1},
		}},
		"store_schemas": []any{storeSchemaV1},
		"features":      []any{birthReleaseFeature, claimAcquireFeature, claimJournalCloseFeature, frameSchema},
	})
	if err != nil {
		return err
	}
	wire, err := encodeFrame(frameRecord{requestID: requestID, sequence: 0, kind: "client.hello", payload: payload}, bootstrapFrameBodyLimit)
	if err != nil {
		return err
	}
	response, err := c.post(ctx, negotiatePath, wire)
	if err != nil {
		return err
	}
	defer func() { _ = response.Body.Close() }()
	if response.ProtoMajor != 2 || response.StatusCode != http.StatusOK {
		return fmt.Errorf("unexpected HTTP/%d.%d status %s", response.ProtoMajor, response.ProtoMinor, response.Status)
	}
	first, err := readFrame(response.Body, bootstrapFrameBodyLimit)
	if err != nil {
		return err
	}
	if first.requestID != requestID || first.sequence != 0 {
		return errWrongCorrelation
	}
	if first.kind == "problem" {
		code, err := problemCodeFromPayload(first.payload)
		if err != nil {
			return err
		}
		return errors.New(code)
	}
	if first.kind != "server.hello" {
		return errUnsupportedKind
	}
	second, err := readFrame(response.Body, bootstrapFrameBodyLimit)
	if err != nil {
		return err
	}
	if second.requestID != requestID || second.sequence != 1 || second.kind != "session.parameters" {
		return errWrongCorrelation
	}
	if _, err := readFrame(response.Body, bootstrapFrameBodyLimit); !errors.Is(err, io.EOF) {
		return errOutOfOrder
	}
	hello, err := decodeServerHello(first.payload)
	if err != nil {
		return err
	}
	parameters, err := decodeSessionParameters(second.payload)
	if err != nil {
		return err
	}
	c.hello = hello
	c.parameters = parameters
	return nil
}

func decodeServerHello(payload []byte) (serverHello, error) {
	value, err := decodePayload(payload)
	if err != nil {
		return serverHello{}, err
	}
	fields, ok := value.(map[string]any)
	if !ok || !exactFields(fields, "selected_protocol", "identity_schemas", "operations", "store_schemas", "features") {
		return serverHello{}, errMalformedMessage
	}
	version, err := parseVersion(fields["selected_protocol"])
	if err != nil || version != (protocolVersion{major: 1, minor: 0}) {
		return serverHello{}, errIncompatibleVersion
	}
	identitySchemas, err := parseSortedIDs(fields["identity_schemas"])
	if err != nil || !equalStrings(identitySchemas, []string{identitySchemaV1}) {
		return serverHello{}, errUnsupportedExtension
	}
	storeSchemas, err := parseSortedIDs(fields["store_schemas"])
	if err != nil || !equalStrings(storeSchemas, []string{storeSchemaV1}) {
		return serverHello{}, errUnsupportedExtension
	}
	features, err := parseSortedIDs(fields["features"])
	if err != nil || !containsString(features, frameSchema) || len(features) > 4 ||
		!onlyKnownFeatures(features) {
		return serverHello{}, errUnsupportedExtension
	}
	operations, err := parseOperationCapabilities(fields["operations"])
	if err != nil {
		return serverHello{}, errInvalidCapabilities
	}
	for _, capability := range operations {
		if capability.name != "content.write-once" && capability.name != "finding.append" && capability.name != "matter.create" &&
			capability.name != "matter.finish" && capability.name != "step.create" &&
			capability.name != "step.finish" && capability.name != "step.start" || len(capability.versions) != 1 || capability.versions[0] != 1 ||
			!equalStrings(capability.identitySchemas, []string{identitySchemaV1}) {
			return serverHello{}, errInvalidCapabilities
		}
	}
	return serverHello{
		selectedProtocol: version,
		identitySchemas:  identitySchemas,
		operations:       operations,
		storeSchemas:     storeSchemas,
		features:         features,
	}, nil
}

func onlyKnownFeatures(features []string) bool {
	for _, feature := range features {
		if feature != frameSchema && feature != birthReleaseFeature && feature != claimAcquireFeature && feature != claimJournalCloseFeature {
			return false
		}
	}
	return true
}

func decodeSessionParameters(payload []byte) (sessionParameters, error) {
	value, err := decodePayload(payload)
	if err != nil {
		return sessionParameters{}, err
	}
	fields, ok := value.(map[string]any)
	if !ok || !exactFields(fields, "frame_schema", "max_frame_body", "max_chunk_data", "max_stream_bytes", "max_concurrent_exchanges", "receive_window_bytes") {
		return sessionParameters{}, errMalformedMessage
	}
	schema, ok := fields["frame_schema"].(string)
	if !ok || schema != frameSchema {
		return sessionParameters{}, errUnsupportedExtension
	}
	frameBody, frameOK := fields["max_frame_body"].(uint64)
	chunkData, chunkOK := fields["max_chunk_data"].(uint64)
	streamBytes, streamOK := fields["max_stream_bytes"].(uint64)
	exchanges, exchangesOK := fields["max_concurrent_exchanges"].(uint64)
	receiveWindow, receiveOK := fields["receive_window_bytes"].(uint64)
	if !frameOK || !chunkOK || !streamOK || !exchangesOK || !receiveOK ||
		frameBody == 0 || frameBody > uint64(maxFrameBodyLimit) ||
		chunkData == 0 || chunkData > defaultChunkSize ||
		streamBytes == 0 || streamBytes > absoluteStreamMax ||
		exchanges == 0 || exchanges > maxExchangesAbsolute ||
		receiveWindow == 0 || receiveWindow > absoluteReceiveWindow {
		return sessionParameters{}, errInvalidCapabilities
	}
	return sessionParameters{
		frameSchema:            schema,
		maxFrameBody:           frameBody,
		maxChunkData:           chunkData,
		maxStreamBytes:         streamBytes,
		maxConcurrentExchanges: exchanges,
		receiveWindowBytes:     receiveWindow,
	}, nil
}

func (c *Client) post(ctx context.Context, path string, wire []byte) (*http.Response, error) {
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, "http://wipd"+path, bytes.NewReader(wire))
	if err != nil {
		return nil, err
	}
	request.Header.Set("Content-Type", "application/octet-stream")
	return c.httpClient.Do(request)
}

func problemCodeFromPayload(payload []byte) (string, error) {
	value, err := decodePayload(payload)
	if err != nil {
		return "", err
	}
	if code, ok := value.(string); ok && code != "" {
		return code, nil
	}
	fields, ok := value.(map[string]any)
	if !ok || !exactFields(fields, "code", "operation_name", "requested_version", "supported_versions", "identity_schema") {
		return "", errMalformedMessage
	}
	code, ok := fields["code"].(string)
	name, nameOK := fields["operation_name"].(string)
	version, versionOK := fields["requested_version"].(uint64)
	versions, versionsOK := fields["supported_versions"].([]any)
	identitySchema := fields["identity_schema"]
	if !ok || !capabilityIDPattern.MatchString(code) || !nameOK || name == "" || !versionOK || version == 0 || !versionsOK {
		return "", errMalformedMessage
	}
	if identitySchema != nil {
		if schema, ok := identitySchema.(string); !ok || !capabilityIDPattern.MatchString(schema) {
			return "", errMalformedMessage
		}
	}
	var previous uint64
	for _, item := range versions {
		version, ok := item.(uint64)
		if !ok || version == 0 || version <= previous {
			return "", errMalformedMessage
		}
		previous = version
	}
	return code, nil
}

func socketHasNoListener(profile wipdprofile.Profile) (bool, error) {
	path := filepath.Join(profile.Root, socketFileName)
	info, err := os.Lstat(path)
	if errors.Is(err, os.ErrNotExist) {
		return true, nil
	}
	if err != nil {
		return false, err
	}
	if info.Mode()&os.ModeSymlink != 0 || info.Mode()&os.ModeSocket == 0 {
		return false, errors.New("profile socket path is not a socket")
	}
	connection, err := net.DialTimeout("unix", path, 100*time.Millisecond)
	if err == nil {
		_ = connection.Close()
		return false, nil
	}
	if errors.Is(err, syscall.ECONNREFUSED) {
		return true, nil
	}
	return false, err
}

func newFrameRequestID() (string, error) {
	var entropy [16]byte
	if _, err := rand.Read(entropy[:]); err != nil {
		return "", err
	}
	number := new(big.Int).SetBytes(entropy[:])
	base := big.NewInt(32)
	encoded := make([]byte, 26)
	for index := len(encoded) - 1; index >= 0; index-- {
		remainder := new(big.Int)
		number.DivMod(number, base, remainder)
		encoded[index] = frameIDAlphabet[remainder.Int64()]
	}
	value := string(encoded)
	if !validRequestID(value) {
		return "", fmt.Errorf("generated non-canonical request ID")
	}
	return value, nil
}

func equalStrings(left, right []string) bool {
	if len(left) != len(right) {
		return false
	}
	for index := range left {
		if left[index] != right[index] {
			return false
		}
	}
	return true
}
