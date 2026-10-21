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
	"sort"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/procrastivity/wip/internal/d115"
	"github.com/procrastivity/wip/internal/operation"
	"github.com/procrastivity/wip/internal/wipdprofile"
	"github.com/procrastivity/wip/internal/wipdwire"
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
	// ErrD115DefinitelyUnsent reports an explicit local proof that no D115
	// request byte could have reached the authority for this exchange.
	ErrD115DefinitelyUnsent = errors.New("create-local.definitely-unsent")
	// ErrD115ReceiptDurable reports that success is durable locally but final
	// receipt installation still needs recovery.
	ErrD115ReceiptDurable = errors.New("create-local.receipt-durable")
)

// Client is an authenticated local IPC session. It owns no database handle
// and never resolves a legacy WIP store.
type Client struct {
	transport  *http2.Transport
	httpClient *http.Client
	hello      serverHello
	parameters sessionParameters
	m6         bool
	step8      bool
	step9a     bool
	step9b     bool
	step9c     bool
	step10     bool
	step11     bool
	routeOnly  bool
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

// ConnectM6 is reserved for isolated M6 acceptance. Ordinary CLI activation
// and Connect retain the sealed M5 capability request unchanged.
func ConnectM6(ctx context.Context, explicitProfileRoot string) (*Client, error) {
	profile, err := wipdprofile.Resolve(explicitProfileRoot)
	if err != nil {
		return nil, fmt.Errorf("%w: profile verification: %v", ErrUnavailable, err)
	}
	return connectProfileCapabilities(ctx, profile, true, false, false, false, false, false, false)
}

// ConnectM6Step8 explicitly offers the dependency/reference acceptance set.
// Connect and ConnectM6 retain their M5 and Step 7 capability offers.
func ConnectM6Step8(ctx context.Context, explicitProfileRoot string) (*Client, error) {
	profile, err := wipdprofile.Resolve(explicitProfileRoot)
	if err != nil {
		return nil, fmt.Errorf("%w: profile verification: %v", ErrUnavailable, err)
	}
	return connectProfileCapabilities(ctx, profile, true, true, false, false, false, false, false)
}

// ConnectM6Step9A explicitly offers named Batch birth. It refuses to connect
// unless the authority negotiates that exact operation; no older profile is
// retried or used as a fallback.
func ConnectM6Step9A(ctx context.Context, explicitProfileRoot string) (*Client, error) {
	profile, err := wipdprofile.Resolve(explicitProfileRoot)
	if err != nil {
		return nil, fmt.Errorf("%w: profile verification: %v", ErrUnavailable, err)
	}
	return connectProfileCapabilities(ctx, profile, true, true, true, false, false, false, false)
}

// ConnectM6Step9B explicitly offers named Batch membership and dismissal. It
// requires the full S9-B capability set and never falls back to an older one.
func ConnectM6Step9B(ctx context.Context, explicitProfileRoot string) (*Client, error) {
	profile, err := wipdprofile.Resolve(explicitProfileRoot)
	if err != nil {
		return nil, fmt.Errorf("%w: profile verification: %v", ErrUnavailable, err)
	}
	return connectProfileCapabilities(ctx, profile, true, true, true, true, false, false, false)
}

// ConnectM6Step9C requires the explicit named-Batch read feature in addition
// to the complete S9-A/S9-B operation set; it never falls back.
func ConnectM6Step9C(ctx context.Context, explicitProfileRoot string) (*Client, error) {
	profile, err := wipdprofile.Resolve(explicitProfileRoot)
	if err != nil {
		return nil, fmt.Errorf("%w: profile verification: %v", ErrUnavailable, err)
	}
	return connectProfileCapabilities(ctx, profile, true, true, true, true, true, false, false)
}

// ConnectM6Step10 explicitly offers the isolated engine-only hook operation
// on top of the complete Step 9C profile; ordinary Connect paths never offer it.
func ConnectM6Step10(ctx context.Context, explicitProfileRoot string) (*Client, error) {
	profile, err := wipdprofile.Resolve(explicitProfileRoot)
	if err != nil {
		return nil, fmt.Errorf("%w: profile verification: %v", ErrUnavailable, err)
	}
	return connectProfileCapabilities(ctx, profile, true, true, true, true, true, true, false)
}

// ConnectM6Step11 offers the complete Step 10 profile plus the explicit
// authority-owned Repo route-binding operation; older profiles never inherit it.
func ConnectM6Step11(ctx context.Context, explicitProfileRoot string) (*Client, error) {
	profile, err := wipdprofile.Resolve(explicitProfileRoot)
	if err != nil {
		return nil, fmt.Errorf("%w: profile verification: %v", ErrUnavailable, err)
	}
	return connectProfileCapabilities(ctx, profile, true, true, true, true, true, true, true)
}

// ConnectRouteResolve opens the standalone prebinding profile's read-only
// route capability without selecting Repo-bound M6 commands.
func ConnectRouteResolve(ctx context.Context, explicitProfileRoot string) (*Client, error) {
	profile, err := wipdprofile.Resolve(explicitProfileRoot)
	if err != nil {
		return nil, err
	}
	return connectProfileCapabilitiesWithRouteResolve(ctx, profile, false, false, false, false, false, false, false, true)
}

// ConnectAttachLifecycle explicitly negotiates Step 11's attach lifecycle and
// read-only route discovery without offering Repo-bound command execution.
func ConnectAttachLifecycle(ctx context.Context, explicitProfileRoot string) (*Client, error) {
	profile, err := wipdprofile.Resolve(explicitProfileRoot)
	if err != nil {
		return nil, err
	}
	return connectProfileCapabilitiesWithRouteResolve(ctx, profile, false, false, false, false, false, false, true, true)
}

// ConnectStep11Routes opens the prebinding daemon's authenticated route
// mutation and route-resolution surface without requiring unrelated Step 9/10
// capabilities that this profile does not advertise.
func ConnectStep11Routes(ctx context.Context, explicitProfileRoot string) (*Client, error) {
	profile, err := wipdprofile.Resolve(explicitProfileRoot)
	if err != nil {
		return nil, err
	}
	return connectProfileCapabilitiesWithRouteResolve(ctx, profile, true, false, false, false, false, false, true, false)
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
	return c.executeCommand(ctx, command, false, nil)
}

// ExecuteCommandV2 requires the selected v2 local capability even when proof
// is omitted on replay. It never retries through ExecuteCommand or falls back
// to v1 after any negotiation, protocol, or post-send transport error.
func (c *Client) ExecuteCommandV2(ctx context.Context, command operation.Command, proof []byte) (operation.Result, error) {
	return c.executeCommand(ctx, command, true, proof)
}

// ResolveRoute asks the daemon to discover the caller's current checkout and
// resolve it through the configured read-only Step 11 route path. No path,
// remote URL, or identity is accepted from the caller.
func (c *Client) ResolveRoute(ctx context.Context) (operation.RouteResolveOutput, error) {
	var empty operation.RouteResolveOutput
	if c == nil || c.httpClient == nil {
		return empty, &ExchangeError{Code: "transport.unavailable", Err: ErrUnavailable}
	}
	id := operation.RouteResolveV1.Metadata().Operation
	if !c.step11 && !c.routeOnly || !operationCapabilityContains(c.hello.operations, id, identitySchemaV1) {
		return empty, &ExchangeError{Code: string(operation.ProblemUnsupportedVersion)}
	}
	requestID, err := newFrameRequestID()
	if err != nil {
		return empty, &ExchangeError{Code: "transport.unavailable", Err: err}
	}
	payload, err := wipdwire.EncodeCanonical(wipdwire.QueryRequest{
		Schema: "wipd.query-request/1", Query: wipdwire.QueryOperation{Name: id.Name, Version: id.Version},
		QueryPayload: []byte{},
	})
	if err != nil {
		return empty, &ExchangeError{Code: "protocol.malformed-message", Err: err}
	}
	wire, err := encodeFrame(frameRecord{requestID: requestID, sequence: 0, kind: "query.request", payload: payload}, uint32(c.parameters.maxFrameBody))
	if err != nil {
		return empty, &ExchangeError{Code: "protocol.frame-too-large", Err: err}
	}
	response, err := c.post(ctx, exchangePath, wire)
	if err != nil {
		return empty, uncertainExchange(err)
	}
	defer func() { _ = response.Body.Close() }()
	if response.ProtoMajor != 2 || response.StatusCode != http.StatusOK {
		return empty, uncertainExchange(fmt.Errorf("unexpected HTTP/%d.%d status %s", response.ProtoMajor, response.ProtoMinor, response.Status))
	}
	frame, err := readFrame(response.Body, uint32(c.parameters.maxFrameBody))
	if err != nil || frame.requestID != requestID || frame.sequence != 0 {
		return empty, uncertainExchange(errors.Join(err, errWrongCorrelation))
	}
	if frame.kind == "problem" {
		code, decodeErr := problemCodeFromPayload(frame.payload)
		if decodeErr != nil {
			return empty, uncertainExchange(decodeErr)
		}
		if _, readErr := readFrame(response.Body, uint32(c.parameters.maxFrameBody)); !errors.Is(readErr, io.EOF) {
			return empty, uncertainExchange(errOutOfOrder)
		}
		return empty, &ExchangeError{Code: code}
	}
	if frame.kind != "response.end" {
		return empty, uncertainExchange(errUnsupportedKind)
	}
	var output operation.RouteResolveOutput
	if err = wipdwire.DecodeCanonical(frame.payload, &output, "repo_id", "domain_id", "authority_epoch", "connection_profile"); err != nil {
		return empty, uncertainExchange(err)
	}
	if err = operation.RouteResolveV1.ValidateResult(operation.Result{Code: operation.ResultSucceeded, Output: output}); err != nil {
		return empty, uncertainExchange(err)
	}
	if _, err = readFrame(response.Body, uint32(c.parameters.maxFrameBody)); !errors.Is(err, io.EOF) {
		return empty, uncertainExchange(errOutOfOrder)
	}
	return output, nil
}

func (c *Client) executeCommand(ctx context.Context, command operation.Command, version2 bool, proof []byte) (operation.Result, error) {
	if c == nil || c.httpClient == nil {
		return operation.Result{}, &ExchangeError{Code: "transport.unavailable", Err: ErrUnavailable}
	}
	if command.Request.Operation == operation.BatchSweepAnonymousV1.Metadata().Operation {
		return operation.Result{}, &ExchangeError{Code: "protocol.unsupported-extension"}
	}
	if version2 && !containsString(c.hello.features, wipdwire.CommandSubmitV2Feature) ||
		!version2 && command.Request.Operation.Name == "gate.exemption.repair" {
		return operation.Result{}, &ExchangeError{Code: "protocol.unsupported-extension"}
	}
	if proof != nil && (len(proof) == 0 || len(proof) > 1<<20) {
		return operation.Result{}, &ExchangeError{Code: "protocol.malformed-message"}
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
	fields := map[string]any{
		"schema":            "wipd.command-submit/1",
		"canonical_command": canonical,
		"request_hash":      hash,
		"deadline":          nil,
	}
	if version2 {
		fields["schema"] = wipdwire.CommandSubmitV2Feature
		fields["detached_proof"] = proof
	}
	payload, err := encodePayload(fields)
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
		if code == "transport.outcome-unknown" {
			return operation.Result{}, uncertainExchange(ErrOutcomeUnknown)
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
	return connectProfileCapabilities(ctx, profile, false, false, false, false, false, false, false)
}

func connectProfileCapabilities(ctx context.Context, profile wipdprofile.Profile, m6, step8, namedBatch, namedBatchMembership, namedBatchRead, selectStep10, selectStep11 bool) (*Client, error) {
	return connectProfileCapabilitiesWithRouteResolve(ctx, profile, m6, step8, namedBatch, namedBatchMembership, namedBatchRead, selectStep10, selectStep11, false)
}

func connectProfileCapabilitiesWithRouteResolve(ctx context.Context, profile wipdprofile.Profile, m6, step8, namedBatch, namedBatchMembership, namedBatchRead, selectStep10, selectStep11, selectRouteResolve bool) (*Client, error) {
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
		m6:        m6,
		step8:     step8,
		step9a:    namedBatch,
		step9b:    namedBatchMembership,
		step9c:    namedBatchRead,
		step10:    selectStep10,
		step11:    selectStep11,
		routeOnly: selectRouteResolve,
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
	if namedBatch && !operationCapabilityContains(client.hello.operations, operation.BatchCreateV1.Metadata().Operation, identitySchemaV1) {
		transport.CloseIdleConnections()
		return nil, fmt.Errorf("%w: named Batch birth is not supported by this profile", ErrUnavailable)
	}
	if namedBatchMembership {
		for _, definition := range operation.NamedBatchMembershipCatalogue() {
			if !operationCapabilityContains(client.hello.operations, definition.Metadata().Operation, identitySchemaV1) {
				transport.CloseIdleConnections()
				return nil, fmt.Errorf("%w: named Batch membership is not supported by this profile", ErrUnavailable)
			}
		}
	}
	if namedBatchRead && !containsString(client.hello.features, wipdwire.NamedBatchReadFeature) {
		transport.CloseIdleConnections()
		return nil, fmt.Errorf("%w: named Batch read is not supported by this profile", ErrUnavailable)
	}
	if selectStep10 && !operationCapabilityContains(client.hello.operations, operation.SchedulerOrchestrateV1.Metadata().Operation, identitySchemaV1) {
		transport.CloseIdleConnections()
		return nil, fmt.Errorf("%w: scheduler hooks are not supported by this profile", ErrUnavailable)
	}
	if selectStep11 {
		for _, definition := range operation.Step11Catalogue() {
			if !operationCapabilityContains(client.hello.operations, definition.Metadata().Operation, identitySchemaV1) {
				transport.CloseIdleConnections()
				return nil, fmt.Errorf("%w: Step 11 operation %s is not supported by this profile", ErrUnavailable, definition.Metadata().Operation)
			}
		}
	}
	if selectRouteResolve && !operationCapabilityContains(client.hello.operations, operation.RouteResolveV1.Metadata().Operation, identitySchemaV1) {
		transport.CloseIdleConnections()
		return nil, fmt.Errorf("%w: route.resolve is not supported by this profile", ErrUnavailable)
	}
	return client, nil
}

func (c *Client) negotiate(ctx context.Context) error {
	requestID, err := newFrameRequestID()
	if err != nil {
		return err
	}
	matterVersions := []any{uint64(1)}
	stepVersions := []any{uint64(1)}
	if c.m6 {
		matterVersions = append(matterVersions, uint64(2))
		stepVersions = append(stepVersions, uint64(2))
	}
	var operations []any
	if c.routeOnly {
		operations = []any{}
	} else {
		operations = []any{map[string]any{
			"name": "content.write-once", "versions": []any{uint64(1)}, "identity_schemas": []any{identitySchemaV1},
		}, map[string]any{
			"name": "finding.append", "versions": []any{uint64(1)}, "identity_schemas": []any{identitySchemaV1},
		}, map[string]any{
			"name": "matter.create", "versions": matterVersions, "identity_schemas": []any{identitySchemaV1},
		}, map[string]any{
			"name": "matter.finish", "versions": []any{uint64(1)}, "identity_schemas": []any{identitySchemaV1},
		}, map[string]any{
			"name": "step.create", "versions": stepVersions, "identity_schemas": []any{identitySchemaV1},
		}, map[string]any{
			"name": "step.finish", "versions": []any{uint64(1)}, "identity_schemas": []any{identitySchemaV1},
		}, map[string]any{
			"name": "step.start", "versions": []any{uint64(1)}, "identity_schemas": []any{identitySchemaV1},
		}}
	}
	if c.step9a {
		operations = append(operations, map[string]any{
			"name": operation.BatchCreateV1.Metadata().Operation.Name, "versions": []any{uint64(operation.BatchCreateV1.Metadata().Operation.Version)}, "identity_schemas": []any{identitySchemaV1},
		})
	}
	if c.step9b {
		for _, definition := range operation.NamedBatchMembershipCatalogue() {
			id := definition.Metadata().Operation
			operations = append(operations, map[string]any{
				"name": id.Name, "versions": []any{uint64(id.Version)}, "identity_schemas": []any{identitySchemaV1},
			})
		}
	}
	if c.step10 {
		for _, definition := range operation.Step10Catalogue() {
			id := definition.Metadata().Operation
			operations = append(operations, map[string]any{
				"name": id.Name, "versions": []any{uint64(id.Version)}, "identity_schemas": []any{identitySchemaV1},
			})
		}
	}
	if c.step11 {
		for _, definition := range operation.Step11Catalogue() {
			id := definition.Metadata().Operation
			operations = append(operations, map[string]any{
				"name": id.Name, "versions": []any{uint64(id.Version)}, "identity_schemas": []any{identitySchemaV1},
			})
		}
	}
	if c.routeOnly && !c.step11 {
		id := operation.RouteResolveV1.Metadata().Operation
		operations = append(operations, map[string]any{
			"name": id.Name, "versions": []any{uint64(id.Version)}, "identity_schemas": []any{identitySchemaV1},
		})
	}
	if c.m6 {
		operations = append(operations, map[string]any{
			"name": "gate.exemption.repair", "versions": []any{uint64(1)}, "identity_schemas": []any{identitySchemaV1},
		})
		batchSweep := operation.BatchSweepAnonymousV1.Metadata().Operation
		operations = append(operations, map[string]any{
			"name": batchSweep.Name, "versions": []any{uint64(batchSweep.Version)},
			"identity_schemas": []any{identitySchemaV1},
		})
		operations = append(operations, map[string]any{
			"name": "matter.locator-repair", "versions": []any{uint64(1)}, "identity_schemas": []any{identitySchemaV1},
		}, map[string]any{
			"name": "stage.create", "versions": []any{uint64(1)}, "identity_schemas": []any{identitySchemaV1},
		}, map[string]any{
			"name": "step.insert", "versions": []any{uint64(1)}, "identity_schemas": []any{identitySchemaV1},
		}, map[string]any{
			"name": "step.reorder", "versions": []any{uint64(1)}, "identity_schemas": []any{identitySchemaV1},
		}, map[string]any{
			"name": "step.replace", "versions": []any{uint64(1)}, "identity_schemas": []any{identitySchemaV1},
		}, map[string]any{
			"name": "step.remove", "versions": []any{uint64(1)}, "identity_schemas": []any{identitySchemaV1},
		})
		for _, definition := range operation.Step5Catalogue() {
			id := definition.Metadata().Operation
			operations = append(operations, map[string]any{
				"name": id.Name, "versions": []any{uint64(id.Version)}, "identity_schemas": []any{identitySchemaV1},
			})
		}
		for _, definition := range operation.GateCatalogue() {
			id := definition.Metadata().Operation
			operations = append(operations, map[string]any{
				"name": id.Name, "versions": []any{uint64(id.Version)}, "identity_schemas": []any{identitySchemaV1},
			})
		}
		if c.step8 {
			for _, definition := range operation.Step8Catalogue() {
				id := definition.Metadata().Operation
				operations = append(operations, map[string]any{
					"name": id.Name, "versions": []any{uint64(id.Version)}, "identity_schemas": []any{identitySchemaV1},
				})
			}
		}
		sort.Slice(operations, func(i, j int) bool {
			return operations[i].(map[string]any)["name"].(string) < operations[j].(map[string]any)["name"].(string)
		})
	}
	if c.routeOnly {
		sort.Slice(operations, func(i, j int) bool {
			return operations[i].(map[string]any)["name"].(string) < operations[j].(map[string]any)["name"].(string)
		})
	}
	features := []any{birthReleaseFeature, claimAcquireFeature, claimJournalCloseFeature, frameSchema}
	if c.m6 {
		features = []any{birthReleaseFeature, claimAcquireFeature, claimJournalCloseFeature, wipdwire.CommandSubmitV2Feature, frameSchema}
	}
	if c.step9c {
		features = append(features, wipdwire.NamedBatchReadFeature)
		sort.Slice(features, func(i, j int) bool { return features[i].(string) < features[j].(string) })
	}
	payload, err := encodePayload(map[string]any{
		"protocol_min":     []any{uint64(1), uint64(0)},
		"protocol_max":     []any{uint64(1), uint64(0)},
		"identity_schemas": []any{identitySchemaV1},
		"operations":       operations,
		"store_schemas":    []any{storeSchemaV1},
		"features":         features,
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
	if !c.m6 && containsString(hello.features, wipdwire.CommandSubmitV2Feature) {
		return errUnsupportedExtension
	}
	if !c.step8 {
		for _, capability := range hello.operations {
			for _, version := range capability.versions {
				if operation.Step8Operation(operation.ID{Name: capability.name, Version: version}) {
					return errInvalidCapabilities
				}
			}
		}
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
	if err != nil || !containsString(features, frameSchema) || len(features) > 6 ||
		!onlyKnownFeatures(features) {
		return serverHello{}, errUnsupportedExtension
	}
	operations, err := parseOperationCapabilities(fields["operations"])
	if err != nil {
		return serverHello{}, errInvalidCapabilities
	}
	for _, capability := range operations {
		if capability.name == "gate.exemption.repair" && !containsString(features, wipdwire.CommandSubmitV2Feature) {
			return serverHello{}, errInvalidCapabilities
		}
		if !equalStrings(capability.identitySchemas, []string{identitySchemaV1}) {
			return serverHello{}, errInvalidCapabilities
		}
		for _, version := range capability.versions {
			if !knownOperationVersion(capability.name, version) {
				return serverHello{}, errInvalidCapabilities
			}
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

func knownOperationVersion(name string, version uint16) bool {
	id := operation.ID{Name: name, Version: version}
	switch id {
	case operation.MatterCreateV1.Metadata().Operation, operation.MatterCreateV2.Metadata().Operation,
		operation.StepCreateV1.Metadata().Operation, operation.StepCreateV2.Metadata().Operation,
		operation.StepStartV1.Metadata().Operation, operation.StepFinishV1.Metadata().Operation,
		operation.MatterFinishV1.Metadata().Operation, operation.ContentWriteOnceV1.Metadata().Operation,
		operation.FindingAppendV1.Metadata().Operation, operation.GateExemptionRepairV1.Metadata().Operation,
		operation.GateDeclareV1.Metadata().Operation, operation.GateCloseV1.Metadata().Operation, operation.GateDismissV1.Metadata().Operation,
		operation.BatchSweepAnonymousV1.Metadata().Operation, operation.BatchCreateV1.Metadata().Operation:
		return true
	default:
		return operation.Step4Operation(id) || operation.Step5Operation(id) || operation.Step8Operation(id) || operation.Step10CapabilityOperation(id) || operation.Step11Operation(id) || operation.NamedBatchMembershipOperation(id)
	}
}

func onlyKnownFeatures(features []string) bool {
	for _, feature := range features {
		if feature != frameSchema && feature != birthReleaseFeature && feature != claimAcquireFeature && feature != claimJournalCloseFeature && feature != wipdwire.CommandSubmitV2Feature && feature != wipdwire.NamedBatchReadFeature {
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

// D115Attempt is the safe local IPC projection of a pre-domain attempt.
type D115Attempt struct {
	AttemptID      string
	State          string
	SigningRequest []byte
	Receipt        []byte
}

// D115Prepare durably prepares an attempt and returns its pinned signing request.
func (c *Client) D115Prepare(ctx context.Context, grantArtifact string) (D115Attempt, error) {
	if grantArtifact == "" || len(grantArtifact) > 5462 {
		return D115Attempt{}, errors.New("wipd: invalid D115 grant artifact")
	}
	return c.d115AttemptCall(ctx, "/wipd/v1/create-local/prepare", map[string]any{
		"schema": "wipd.create-local-prepare/1", "operation": "repo.create-local@v1", "grant_artifact": grantArtifact,
	}, "prepared")
}

// AttachOnce submits or resumes the exact local intent for a closed owner
// grant. The authenticated daemon derives all checkout and Environment fields.
func (c *Client) AttachOnce(ctx context.Context, grantArtifact string) (AttachLocalAttempt, error) {
	if c == nil || c.httpClient == nil || ctx == nil || !c.step11 || grantArtifact == "" || len(grantArtifact) > 1<<20 {
		return AttachLocalAttempt{}, errors.New("wipd: invalid attach request")
	}
	return c.attachAttemptCall(ctx, "/wipd/v1/environment/attach-once", map[string]any{
		"schema": "wipd.environment-attach-once/1", "grant_artifact": grantArtifact,
	}, true)
}

// AttachStatus retrieves only the safe disposition and exact retained receipt
// for an existing local attach intent; it never submits or recovers it.
func (c *Client) AttachStatus(ctx context.Context, attemptID string) (AttachLocalAttempt, error) {
	if c == nil || c.httpClient == nil || ctx == nil || !c.step11 || !d115.ValidULID(attemptID) {
		return AttachLocalAttempt{}, errors.New("wipd: invalid attach status request")
	}
	return c.attachAttemptCall(ctx, "/wipd/v1/environment/attach-status", map[string]any{
		"schema": "wipd.environment-attach-status/1", "attempt_id": attemptID,
	}, false)
}

func (c *Client) attachAttemptCall(ctx context.Context, path string, request map[string]any, mayHaveSubmitted bool) (AttachLocalAttempt, error) {
	var empty AttachLocalAttempt
	wire, err := wipdwire.EncodeCanonical(request)
	if err != nil {
		return empty, err
	}
	response, err := c.d115Post(ctx, path, wire)
	if err != nil {
		if mayHaveSubmitted {
			return empty, errors.Join(ErrOutcomeUnknown, err)
		}
		return empty, err
	}
	body, readErr := io.ReadAll(io.LimitReader(response.Body, (1<<20)+1))
	closeErr := response.Body.Close()
	if readErr != nil || closeErr != nil || len(body) > 1<<20 {
		clear(body)
		if mayHaveSubmitted {
			return empty, errors.Join(ErrOutcomeUnknown, readErr, closeErr)
		}
		return empty, errors.Join(readErr, closeErr)
	}
	if response.StatusCode != http.StatusOK || response.Header.Get("Content-Type") != "application/cbor" {
		diagnostic := strings.TrimSpace(string(body))
		clear(body)
		if mayHaveSubmitted && response.StatusCode >= http.StatusInternalServerError {
			return empty, errors.Join(ErrOutcomeUnknown, errors.New("wipd: attach response unavailable"))
		}
		return empty, errors.New("wipd: attach request refused: " + diagnostic)
	}
	fields, err := wipdwire.DecodeCanonicalMap(body, "schema", "attempt_id", "state", "receipt", "signed_receipt")
	clear(body)
	if err != nil || fields["schema"] != "wipd.environment-attach-state/1" {
		return empty, errors.Join(ErrOutcomeUnknown, err)
	}
	id, idOK := fields["attempt_id"].(string)
	state, stateOK := fields["state"].(string)
	if !idOK || !d115.ValidULID(id) || !stateOK || state == "" {
		return empty, errors.New("wipd: malformed attach state")
	}
	attempt := AttachLocalAttempt{AttemptID: id, State: state}
	if value := fields["receipt"]; value != nil {
		attempt.Receipt, idOK = value.([]byte)
		if !idOK {
			return empty, errors.Join(ErrOutcomeUnknown, errors.New("wipd: malformed attach receipt"))
		}
		attempt.Receipt = bytes.Clone(attempt.Receipt)
	}
	if value := fields["signed_receipt"]; value != nil {
		attempt.SignedReceipt, idOK = value.([]byte)
		if !idOK {
			return empty, errors.Join(ErrOutcomeUnknown, errors.New("wipd: malformed signed attach receipt"))
		}
		attempt.SignedReceipt = bytes.Clone(attempt.SignedReceipt)
	}
	if (len(attempt.Receipt) == 0) != (len(attempt.SignedReceipt) == 0) {
		return empty, errors.Join(ErrOutcomeUnknown, errors.New("wipd: incomplete attach receipt pair"))
	}
	return attempt, nil
}

// D115ImportProof imports a detached owner proof without submitting it.
func (c *Client) D115ImportProof(ctx context.Context, commandID string, proof []byte) (D115Attempt, error) {
	if !d115.ValidULID(commandID) {
		return D115Attempt{}, errors.New("wipd: invalid D115 attempt ID")
	}
	return c.d115AttemptCall(ctx, "/wipd/v1/create-local/import-proof", map[string]any{
		"schema": "wipd.create-local-import-proof/1", "attempt_id": commandID, "proof_artifact": proof,
	}, "state")
}

// D115Status returns durable attempt status without triggering recovery.
func (c *Client) D115Status(ctx context.Context, commandID string) (D115Attempt, error) {
	if !d115.ValidULID(commandID) {
		return D115Attempt{}, errors.New("wipd: invalid D115 attempt ID")
	}
	return c.d115AttemptCall(ctx, "/wipd/v1/create-local/status", map[string]any{
		"schema": "wipd.create-local-status/1", "attempt_id": commandID,
	}, "state")
}

// D115Cancel explicitly cancels an attempt that has never possibly been sent.
func (c *Client) D115Cancel(ctx context.Context, commandID string) (D115Attempt, error) {
	if !d115.ValidULID(commandID) {
		return D115Attempt{}, errors.New("wipd: invalid D115 attempt ID")
	}
	return c.d115AttemptCall(ctx, "/wipd/v1/create-local/cancel", map[string]any{
		"schema": "wipd.create-local-cancel/1", "attempt_id": commandID,
	}, "state")
}

// D115Submit explicitly submits imported exact bytes and returns the terminal receipt.
func (c *Client) D115Submit(ctx context.Context, commandID string) (d115.Receipt, error) {
	if c == nil || c.httpClient == nil || ctx == nil || !d115.ValidULID(commandID) {
		return d115.Receipt{}, errors.New("wipd: invalid D115 client")
	}
	wire, err := wipdwire.EncodeCanonical(map[string]any{"schema": "wipd.create-local-submit/1", "attempt_id": commandID})
	if err != nil {
		return d115.Receipt{}, err
	}
	response, err := c.d115Post(ctx, "/wipd/v1/create-local/submit", wire)
	if err != nil {
		return d115.Receipt{}, errors.Join(ErrOutcomeUnknown, err)
	}
	body, readErr := io.ReadAll(io.LimitReader(response.Body, 8193))
	closeErr := response.Body.Close()
	if readErr != nil || closeErr != nil || len(body) > 8192 {
		clear(body)
		return d115.Receipt{}, errors.Join(ErrOutcomeUnknown, readErr, closeErr)
	}
	if response.StatusCode != http.StatusOK || response.Header.Get("Content-Type") != "application/cbor" {
		clear(body)
		if response.StatusCode >= http.StatusInternalServerError {
			return d115.Receipt{}, errors.Join(ErrOutcomeUnknown, errors.New("wipd: D115 submit response unavailable"))
		}
		return d115.Receipt{}, errors.New("wipd: D115 submit refused")
	}
	receipt, err := d115.DecodeReceipt(body)
	if err == nil {
		clear(body)
		if receipt.CommandID != commandID {
			return d115.Receipt{}, errors.Join(ErrOutcomeUnknown, errors.New("wipd: D115 receipt command ID mismatch"))
		}
		return receipt, nil
	}
	fields, dispositionErr := wipdwire.DecodeCanonicalMap(body, "schema", "attempt_id", "disposition")
	clear(body)
	if dispositionErr != nil || fields["schema"] != "wipd.create-local-disposition/1" || fields["attempt_id"] != commandID {
		return d115.Receipt{}, errors.Join(ErrOutcomeUnknown, err, dispositionErr)
	}
	disposition, ok := fields["disposition"].(string)
	if !ok {
		return d115.Receipt{}, errors.Join(ErrOutcomeUnknown, errors.New("wipd: invalid D115 submit disposition"))
	}
	switch disposition {
	case "definitely-unsent":
		return d115.Receipt{}, ErrD115DefinitelyUnsent
	case "outcome-unknown":
		return d115.Receipt{}, errors.Join(ErrOutcomeUnknown, errors.New("wipd: D115 command outcome unknown"))
	case "receipt-durable":
		return d115.Receipt{}, ErrD115ReceiptDurable
	case "refused":
		return d115.Receipt{}, errors.New("wipd: D115 submit refused")
	default:
		return d115.Receipt{}, errors.Join(ErrOutcomeUnknown, errors.New("wipd: unknown D115 submit disposition"))
	}
}

func (c *Client) d115AttemptCall(ctx context.Context, path string, request map[string]any, responseKind string) (D115Attempt, error) {
	if c == nil || c.httpClient == nil || ctx == nil {
		return D115Attempt{}, errors.New("wipd: invalid D115 client")
	}
	wire, err := wipdwire.EncodeCanonical(request)
	if err != nil {
		return D115Attempt{}, err
	}
	response, err := c.d115Post(ctx, path, wire)
	if err != nil {
		return D115Attempt{}, err
	}
	body, readErr := io.ReadAll(io.LimitReader(response.Body, 8193))
	closeErr := response.Body.Close()
	if readErr != nil || closeErr != nil || len(body) > 8192 {
		clear(body)
		return D115Attempt{}, errors.Join(ErrOutcomeUnknown, readErr, closeErr)
	}
	if response.StatusCode != http.StatusOK || response.Header.Get("Content-Type") != "application/cbor" {
		clear(body)
		return D115Attempt{}, errors.New("wipd: D115 local operation refused")
	}
	var fields map[string]any
	var decodeErr error
	if responseKind == "prepared" {
		fields, decodeErr = wipdwire.DecodeCanonicalMap(body, "schema", "attempt_id", "state", "signing_request")
	} else {
		fields, decodeErr = wipdwire.DecodeCanonicalMap(body, "schema", "attempt_id", "state", "receipt")
	}
	clear(body)
	if decodeErr != nil {
		return D115Attempt{}, decodeErr
	}
	id, idOK := fields["attempt_id"].(string)
	state, stateOK := fields["state"].(string)
	schema, schemaOK := fields["schema"].(string)
	if !idOK || id == "" || !stateOK || state == "" || !schemaOK {
		return D115Attempt{}, errors.New("wipd: invalid D115 local response")
	}
	result := D115Attempt{AttemptID: id, State: state}
	if responseKind == "prepared" {
		if schema != "wipd.create-local-prepared/1" {
			return D115Attempt{}, errors.New("wipd: invalid D115 prepare response")
		}
		if value := fields["signing_request"]; value != nil {
			data, ok := value.([]byte)
			if !ok {
				return D115Attempt{}, errors.New("wipd: invalid signing request response")
			}
			request, requestErr := d115.DecodeOwnerSigningRequest(data)
			if requestErr != nil || request.CommandID != id {
				return D115Attempt{}, errors.Join(ErrOutcomeUnknown, requestErr, errors.New("wipd: signing request command ID mismatch"))
			}
			result.SigningRequest = data
		}
	} else {
		if schema != "wipd.create-local-state/1" {
			return D115Attempt{}, errors.New("wipd: invalid D115 state response")
		}
		if value := fields["receipt"]; value != nil {
			data, ok := value.([]byte)
			if !ok {
				return D115Attempt{}, errors.New("wipd: invalid receipt response")
			}
			receipt, receiptErr := d115.DecodeReceipt(data)
			if receiptErr != nil || receipt.CommandID != id {
				return D115Attempt{}, errors.Join(ErrOutcomeUnknown, receiptErr, errors.New("wipd: receipt command ID mismatch"))
			}
			result.Receipt = data
		}
	}
	return result, nil
}

func (c *Client) d115Post(ctx context.Context, path string, wire []byte) (*http.Response, error) {
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, "http://wipd"+path, bytes.NewReader(wire))
	if err != nil {
		return nil, err
	}
	request.Header.Set("Content-Type", "application/cbor")
	request.Header.Set("Accept", "application/cbor")
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
