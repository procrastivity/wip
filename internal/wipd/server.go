package wipd

import (
	"context"
	"errors"
	"io"
	"net"
	"net/http"
	"regexp"
	"strings"
	"sync"
	"time"

	"github.com/procrastivity/wip/internal/operation"
	"github.com/procrastivity/wip/internal/wipdjournal"
	"golang.org/x/net/http2"
)

const (
	negotiatePath = "/wipd/v1/negotiate"
	exchangePath  = "/wipd/v1/exchange"
)

var requestHashPattern = regexp.MustCompile(`^sha256:[0-9a-f]{64}$`)

// Server handles authenticated local frame sessions through its registered operations.
type Server struct {
	registry               *operation.Registry
	maxConcurrentExchanges uint64
	exchangeSlots          chan struct{}
	preflightSlots         chan struct{}
	executionLanes         *executionLanes
	commandStartMu         sync.RWMutex
	commandStart           *CommandStartCoordinator
}

// ConfigureConnectedCommands enables the durable connected birth, command-
// start, and Step 9 birth-release paths for this daemon. Configure it before
// Serve; local fixture servers that do not call this method retain their
// existing in-process dispatch behavior.
func (s *Server) ConfigureConnectedCommands(domainID string, journal *wipdjournal.Journal, authority CommandStartAuthority, environment CommandStartEnvironment) error {
	if s == nil {
		return errors.New("wipd: server is required")
	}
	coordinator, err := s.NewCommandStartCoordinator(domainID, journal, authority, environment)
	if err != nil {
		return err
	}
	s.commandStartMu.Lock()
	defer s.commandStartMu.Unlock()
	if s.commandStart != nil {
		return errors.New("wipd: connected command path is already configured")
	}
	s.commandStart = coordinator
	return nil
}

func (s *Server) connectedCommandStart() *CommandStartCoordinator {
	if s == nil {
		return nil
	}
	s.commandStartMu.RLock()
	defer s.commandStartMu.RUnlock()
	return s.commandStart
}

type connectionSession struct {
	mu          sync.Mutex
	ctx         context.Context
	negotiating bool
	negotiated  bool
	failed      bool
	hello       serverHello
	parameters  sessionParameters
}

type sessionContextKey struct{}

// NewServer creates the production local frame server with no registered
// semantic operations. The Handler dispatch path is exercised only by an
// explicitly composed local test fixture; durable submission and authority
// execution remain owned by a later execution layer.
func NewServer() *Server {
	return newServer(operation.NewRegistry(), defaultExchanges)
}

// NewServerWithRegistry creates a local frame server that advertises and can
// dispatch the supplied operation definitions. Connected deployments must
// configure their durable command-start coordinator before Serve.
func NewServerWithRegistry(registry *operation.Registry) *Server {
	return newServer(registry, defaultExchanges)
}

func newServer(registry *operation.Registry, maxExchanges uint64) *Server {
	if registry == nil {
		registry = operation.NewRegistry()
	}
	if maxExchanges == 0 || maxExchanges > maxExchangesAbsolute {
		panic("wipd: invalid concurrent exchange limit")
	}
	return &Server{
		registry:               registry,
		maxConcurrentExchanges: maxExchanges,
		exchangeSlots:          make(chan struct{}, int(maxExchanges)),
		preflightSlots:         make(chan struct{}, int(maxExchanges)+1), // bounded parser slots plus one overload frame
		executionLanes:         &executionLanes{},
	}
}

// Serve accepts only kernel-authenticated peers. Each accepted connection is
// handed directly to the HTTP/2 prior-knowledge server after Daemon.Accept has
// verified SO_PEERCRED, so no HTTP parser sees an unauthenticated stream.
func (s *Server) Serve(ctx context.Context, daemon *Daemon) error {
	if s == nil || daemon == nil {
		return errors.New("wipd: server and daemon are required")
	}
	serveCtx, cancel := context.WithCancel(ctx)
	defer cancel()

	var connectionsMu sync.Mutex
	connections := make(map[net.Conn]struct{})
	var connectionsWG sync.WaitGroup
	shutdown := make(chan struct{})
	go func() {
		select {
		case <-serveCtx.Done():
			_ = daemon.Close()
			connectionsMu.Lock()
			for connection := range connections {
				_ = connection.Close()
			}
			connectionsMu.Unlock()
		case <-shutdown:
		}
	}()
	defer close(shutdown)

	h2Server := &http2.Server{
		MaxConcurrentStreams:         uint32(maxExchangesAbsolute),
		MaxUploadBufferPerConnection: int32(defaultReceiveWindow),
		MaxUploadBufferPerStream:     int32(maxFrameBodyLimit),
		IdleTimeout:                  90 * time.Second,
	}

	var serveErr error
	for {
		connection, err := daemon.Accept()
		if err != nil {
			if errors.Is(err, ErrPeerUIDMismatch) {
				continue
			}
			if serveCtx.Err() != nil || errors.Is(err, net.ErrClosed) {
				break
			}
			serveErr = err
			cancel()
			break
		}

		state := &connectionSession{ctx: serveCtx}
		connectionCtx := context.WithValue(serveCtx, sessionContextKey{}, state)
		connectionsMu.Lock()
		if serveCtx.Err() != nil {
			connectionsMu.Unlock()
			_ = connection.Close()
			break
		}
		connections[connection] = struct{}{}
		connectionsWG.Add(1)
		connectionsMu.Unlock()

		go func() {
			defer connectionsWG.Done()
			defer func() {
				_ = connection.Close()
				connectionsMu.Lock()
				delete(connections, connection)
				connectionsMu.Unlock()
			}()
			h2Server.ServeConn(connection, &http2.ServeConnOpts{
				Context: connectionCtx,
				Handler: s,
			})
		}()
	}

	cancel()
	closeErr := daemon.Close()
	connectionsMu.Lock()
	for connection := range connections {
		_ = connection.Close()
	}
	connectionsMu.Unlock()
	connectionsWG.Wait()
	return errors.Join(serveErr, closeErr)
}

func (s *Server) ServeHTTP(writer http.ResponseWriter, request *http.Request) {
	if request.ProtoMajor != 2 || request.TLS != nil {
		writer.WriteHeader(http.StatusHTTPVersionNotSupported)
		return
	}
	if request.Method != http.MethodPost || request.URL.RawQuery != "" || request.URL.ForceQuery ||
		hasHeader(request.Header, "Authorization") || hasHeader(request.Header, "Cookie") ||
		hasHeader(request.Header, "Content-Encoding") {
		writer.WriteHeader(http.StatusBadRequest)
		return
	}

	switch request.URL.Path {
	case negotiatePath:
		s.serveNegotiate(writer, request)
	case exchangePath:
		s.serveExchange(writer, request)
	default:
		writer.WriteHeader(http.StatusNotFound)
	}
}

func (s *Server) serveNegotiate(writer http.ResponseWriter, request *http.Request) {
	state := requestSession(request)
	if state == nil {
		abortHTTP2Stream()
	}
	state.mu.Lock()
	if state.negotiating || state.negotiated || state.failed {
		state.mu.Unlock()
		abortHTTP2Stream()
	}
	state.negotiating = true
	state.mu.Unlock()
	negotiationSucceeded := false
	defer func() {
		state.mu.Lock()
		state.negotiating = false
		if !negotiationSucceeded {
			state.failed = true
		}
		state.mu.Unlock()
	}()

	frame, err := readOneFrame(request.Body, bootstrapFrameBodyLimit)
	if err != nil {
		abortHTTP2Stream()
	}
	if frame.sequence != 0 {
		abortHTTP2Stream()
	}
	if frame.kind != "client.hello" {
		abortHTTP2Stream()
	}
	hello, err := decodeClientHello(frame.payload)
	if err != nil {
		if errors.Is(err, errMalformedMessage) || errors.Is(err, errInvalidCapabilities) ||
			errors.Is(err, errIncompatibleVersion) || errors.Is(err, errUnsupportedExtension) {
			s.writeProblem(writer, frame.requestID, 0, err.Error(), bootstrapFrameBodyLimit)
			return
		}
		abortHTTP2Stream()
	}
	selected, parameters, err := negotiateCapabilities(hello, s.registry, s.supportsBirthClaimRelease(), s.supportsClaimAcquire())
	if err != nil {
		if errors.Is(err, errInvalidCapabilities) || errors.Is(err, errIncompatibleVersion) || errors.Is(err, errUnsupportedExtension) {
			s.writeProblem(writer, frame.requestID, 0, err.Error(), bootstrapFrameBodyLimit)
			return
		}
		abortHTTP2Stream()
	}
	parameters.maxConcurrentExchanges = s.maxConcurrentExchanges

	serverPayload, err := encodeServerHello(selected)
	if err != nil {
		abortHTTP2Stream()
	}
	parametersPayload, err := encodePayload(parameters.value())
	if err != nil {
		abortHTTP2Stream()
	}
	first, err := encodeFrame(frameRecord{requestID: frame.requestID, sequence: 0, kind: "server.hello", payload: serverPayload}, bootstrapFrameBodyLimit)
	if err != nil {
		abortHTTP2Stream()
	}
	second, err := encodeFrame(frameRecord{requestID: frame.requestID, sequence: 1, kind: "session.parameters", payload: parametersPayload}, bootstrapFrameBodyLimit)
	if err != nil {
		abortHTTP2Stream()
	}
	if _, err := writer.Write(first); err != nil {
		return
	}
	if _, err := writer.Write(second); err != nil {
		return
	}
	state.mu.Lock()
	state.hello = selected
	state.parameters = parameters
	state.negotiated = true
	negotiationSucceeded = true
	state.mu.Unlock()
}

func (s *Server) serveExchange(writer http.ResponseWriter, request *http.Request) {
	state := requestSession(request)
	if state == nil {
		abortHTTP2Stream()
	}
	state.mu.Lock()
	negotiated := state.negotiated && !state.failed
	parameters := state.parameters
	hello := state.hello
	state.mu.Unlock()
	if !negotiated {
		abortHTTP2Stream()
	}

	select {
	case s.preflightSlots <- struct{}{}:
	default:
		// No request ID is available until one complete frame has been
		// parsed, so fail this bounded admission attempt with a stream reset
		// rather than queueing an unbounded number of waiting handlers.
		abortHTTP2Stream()
	}
	preflightOwned := true
	defer func() {
		if preflightOwned {
			<-s.preflightSlots
		}
	}()

	frame, err := readFrame(request.Body, uint32(parameters.maxFrameBody))
	if err != nil {
		abortHTTP2Stream()
	}
	if frame.sequence != 0 {
		abortHTTP2Stream()
	}
	if frame.kind == "claim.release" {
		<-s.preflightSlots
		preflightOwned = false
		s.serveBirthClaimRelease(writer, request, state, hello, parameters, frame)
		return
	}
	if frame.kind == claimAcquireFrameKind {
		<-s.preflightSlots
		preflightOwned = false
		s.serveClaimAcquire(writer, request, hello, parameters, frame)
		return
	}
	if frame.kind != "command.submit" {
		abortHTTP2Stream()
	}

	select {
	case s.exchangeSlots <- struct{}{}:
	default:
		s.writeProblem(writer, frame.requestID, 0, "transport.overloaded", uint32(parameters.maxFrameBody))
		return
	}
	slotOwnedByDispatch := false
	defer func() {
		if !slotOwnedByDispatch {
			<-s.exchangeSlots
		}
	}()

	command, deadline, err := decodeCommandSubmit(frame.payload)
	if err != nil {
		s.writeProblem(writer, frame.requestID, 0, errMalformedMessage.Error(), uint32(parameters.maxFrameBody))
		return
	}
	if deadline != nil && !deadline.After(time.Now()) {
		s.writeProblem(writer, frame.requestID, 0, "transport.deadline-before-submission", uint32(parameters.maxFrameBody))
		return
	}
	if !operationCapabilityContains(hello.operations, command.Request.Operation, identitySchemaV1) {
		problem := compatibilityProblem(command.Request.Operation, hello.operations)
		s.writeProblemPayload(writer, frame.requestID, 0, problem, uint32(parameters.maxFrameBody))
		return
	}

	<-s.preflightSlots
	preflightOwned = false

	// Queued work follows the exchange lifetime. Registry.Dispatch still gets
	// the server context after the dispatch gate opens, so a later disconnect
	// cannot cancel a Handler that may already have durable effects.
	dispatchContext, cancelDispatch := context.WithCancel(request.Context())
	defer cancelDispatch()
	gate := &dispatchGate{cancel: cancelDispatch}
	dispatchDone := make(chan struct{})
	dispatchOutcomes := make(chan dispatchOutcome, 1)
	go func() {
		defer close(dispatchDone)
		outcome := func() dispatchOutcome {
			if coordinator := s.connectedCommandStart(); coordinator != nil {
				if !gate.begin(request.Context()) {
					return dispatchOutcome{problemCode: "transport.cancelled-before-submission"}
				}
				connected, err := coordinator.RunConnectedCanonicalTerminal(state.ctx, command,
					func(context.Context, CommandStartSnapshot, operation.Command) error { return nil })
				if err != nil {
					if errors.Is(err, wipdjournal.ErrCommandIDConflict) {
						return dispatchOutcome{problemCode: "command.id-conflict"}
					}
					if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
						return dispatchOutcome{problemCode: "transport.outcome-unknown"}
					}
					return dispatchOutcome{problemCode: "authority.unavailable"}
				}
				if !connected.Returned {
					return dispatchOutcome{problemCode: "authority.unavailable"}
				}
				return dispatchOutcome{result: connected.SemanticResult}
			}
			// M2 D128's domain lane encloses semantic dispatch only. Later
			// return/pull barriers belong immediately before this point; frame
			// reads, cancellation waits, and response writes remain outside it.
			releaseLane, acquired := s.executionLanes.acquire(dispatchContext, command.AuthorityDomainID)
			if !acquired {
				return dispatchOutcome{problemCode: "transport.cancelled-before-submission"}
			}
			defer releaseLane()
			if deadline != nil && !deadline.After(time.Now()) {
				return dispatchOutcome{problemCode: "transport.deadline-before-submission"}
			}
			if !gate.begin(request.Context()) {
				return dispatchOutcome{problemCode: "transport.cancelled-before-submission"}
			}
			return dispatchOutcome{result: s.registry.Dispatch(state.ctx, command.Request)}
		}()
		dispatchOutcomes <- outcome
	}()
	transferSlotToDispatch := func() {
		slotOwnedByDispatch = true
		go func() {
			<-dispatchDone
			<-s.exchangeSlots
		}()
	}

	nextFrame := make(chan frameReadResult, 1)
	go func() {
		next, err := readFrame(request.Body, uint32(parameters.maxFrameBody))
		nextFrame <- frameReadResult{frame: next, err: err}
	}()

	for {
		select {
		case incoming := <-nextFrame:
			if errors.Is(incoming.err, io.EOF) {
				nextFrame = nil
				continue
			}
			if incoming.err != nil {
				_ = gate.cancelBeforeDispatch()
				transferSlotToDispatch()
				abortHTTP2Stream()
			}
			if err := validateCancelFrame(frame, incoming.frame); err != nil {
				_ = gate.cancelBeforeDispatch()
				transferSlotToDispatch()
				abortHTTP2Stream()
			}
			if gate.cancelBeforeDispatch() {
				transferSlotToDispatch()
				s.writeProblem(writer, frame.requestID, 0, "transport.cancelled-before-submission", uint32(parameters.maxFrameBody))
				return
			}
			transferSlotToDispatch()
			// Once a Handler may have effects, CANCEL stops this exchange's
			// wait only. Resetting this HTTP/2 stream makes no rollback claim;
			// dispatch retains its bounded exchange slot until it completes.
			abortHTTP2Stream()
		case <-request.Context().Done():
			// A disconnected stream cancels queued work before dispatch. After
			// the dispatch boundary it only ends this wait; the Handler uses the
			// server context and is allowed to finish its durable fixture effect.
			_ = gate.cancelBeforeDispatch()
			transferSlotToDispatch()
			return
		case outcome := <-dispatchOutcomes:
			if outcome.problemCode != "" {
				s.writeProblem(writer, frame.requestID, 0, outcome.problemCode, uint32(parameters.maxFrameBody))
				return
			}
			// A completed Handler is the only source of the existing bare M1
			// result payload on response.end. A transport reset before the
			// client receives it remains uncertain; it is never rewritten as a
			// semantic failure or refusal.
			select {
			case incoming := <-nextFrame:
				if !errors.Is(incoming.err, io.EOF) {
					transferSlotToDispatch()
					abortHTTP2Stream()
				}
				nextFrame = nil
			default:
			}
			payload, err := encodeOperationResultPayload(command.Request.Operation, outcome.result)
			if err != nil {
				abortHTTP2Stream()
			}
			wire, err := encodeFrame(frameRecord{requestID: frame.requestID, sequence: 0, kind: "response.end", payload: payload}, uint32(parameters.maxFrameBody))
			if err != nil {
				abortHTTP2Stream()
			}
			_, _ = writer.Write(wire)
			return
		}
	}
}

type frameReadResult struct {
	frame frameRecord
	err   error
}

type dispatchOutcome struct {
	result      operation.Result
	problemCode string
}

func validateCancelFrame(submit, cancel frameRecord) error {
	if cancel.requestID != submit.requestID {
		return errWrongCorrelation
	}
	if cancel.sequence != 1 {
		return errOutOfOrder
	}
	if cancel.kind != "control.cancel" {
		return errUnsupportedKind
	}
	payload, err := decodePayload(cancel.payload)
	if err != nil {
		return err
	}
	fields, ok := payload.(map[string]any)
	if !ok || len(fields) != 0 {
		return errMalformedMessage
	}
	return nil
}

func (s *Server) writeProblem(writer http.ResponseWriter, requestID string, sequence uint64, code string, limit uint32) {
	s.writeProblemPayload(writer, requestID, sequence, code, limit)
}

func (s *Server) writeProblemPayload(writer http.ResponseWriter, requestID string, sequence uint64, payload any, limit uint32) {
	encodedPayload, err := encodePayload(payload)
	if err != nil {
		abortHTTP2Stream()
	}
	wire, err := encodeFrame(frameRecord{requestID: requestID, sequence: sequence, kind: "problem", payload: encodedPayload}, limit)
	if err != nil {
		abortHTTP2Stream()
	}
	if _, err := writer.Write(wire); err != nil {
		return
	}
}

func requestSession(request *http.Request) *connectionSession {
	state, _ := request.Context().Value(sessionContextKey{}).(*connectionSession)
	return state
}

func abortHTTP2Stream() {
	panic(http.ErrAbortHandler)
}

func operationCapabilityContains(capabilities []operationCapability, id operation.ID, schema string) bool {
	for _, capability := range capabilities {
		if capability.name == id.Name && containsVersion(capability.versions, id.Version) &&
			containsString(capability.identitySchemas, schema) {
			return true
		}
	}
	return false
}

func compatibilityProblem(id operation.ID, capabilities []operationCapability) map[string]any {
	knownName := false
	supportedVersions := make([]any, 0)
	for _, capability := range capabilities {
		if capability.name != id.Name {
			continue
		}
		knownName = true
		for _, version := range capability.versions {
			supportedVersions = append(supportedVersions, uint64(version))
		}
	}
	for _, definition := range operation.Catalogue() {
		if definition.Metadata().Operation.Name == id.Name {
			knownName = true
			break
		}
	}
	code := string(operation.ProblemUnknownOperation)
	if knownName {
		code = string(operation.ProblemUnsupportedVersion)
	}
	return map[string]any{
		"code":               code,
		"operation_name":     id.Name,
		"requested_version":  uint64(id.Version),
		"supported_versions": supportedVersions,
		"identity_schema":    nil,
	}
}

func decodeCommandSubmit(payload []byte) (operation.Command, *time.Time, error) {
	value, err := decodePayload(payload)
	if err != nil {
		return operation.Command{}, nil, err
	}
	fields, ok := value.(map[string]any)
	if !ok || !exactFields(fields, "schema", "canonical_command", "request_hash", "deadline") {
		return operation.Command{}, nil, errMalformedMessage
	}
	schema, ok := fields["schema"].(string)
	if !ok || schema != "wipd.command-submit/1" {
		return operation.Command{}, nil, errMalformedMessage
	}
	canonicalCommand, ok := fields["canonical_command"].([]byte)
	if !ok || len(canonicalCommand) == 0 {
		return operation.Command{}, nil, errMalformedMessage
	}
	assertedHash, ok := fields["request_hash"].(string)
	if !ok || !requestHashPattern.MatchString(assertedHash) {
		return operation.Command{}, nil, errMalformedMessage
	}
	var deadline *time.Time
	if rawDeadline := fields["deadline"]; rawDeadline != nil {
		text, ok := rawDeadline.(string)
		if !ok {
			return operation.Command{}, nil, errMalformedMessage
		}
		parsed, err := time.Parse(time.RFC3339Nano, text)
		if err != nil || parsed.UTC().Format(time.RFC3339Nano) != text || text[len(text)-1] != 'Z' {
			return operation.Command{}, nil, errMalformedMessage
		}
		deadline = &parsed
	}
	command, err := operation.DecodeCanonicalCommand(canonicalCommand)
	if err != nil || operation.VerifyRequestHash(command, assertedHash) != nil {
		return operation.Command{}, nil, errMalformedMessage
	}
	return command, deadline, nil
}

func hasHeader(headers http.Header, name string) bool {
	for key := range headers {
		if strings.EqualFold(key, name) {
			return true
		}
	}
	return false
}
