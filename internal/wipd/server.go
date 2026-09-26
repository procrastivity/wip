package wipd

import (
	"context"
	"errors"
	"net"
	"net/http"
	"regexp"
	"strings"
	"sync"
	"time"

	"github.com/procrastivity/wip/internal/operation"
	"golang.org/x/net/http2"
)

const (
	negotiatePath = "/wipd/v1/negotiate"
	exchangePath  = "/wipd/v1/exchange"
)

var requestHashPattern = regexp.MustCompile(`^sha256:[0-9a-f]{64}$`)

type Server struct {
	registry               *operation.Registry
	maxConcurrentExchanges uint64
	exchangeSlots          chan struct{}
}

type connectionSession struct {
	mu          sync.Mutex
	negotiating bool
	negotiated  bool
	failed      bool
	hello       serverHello
	parameters  sessionParameters
}

type sessionContextKey struct{}

// NewServer creates the production local frame server with no registered
// semantic operations. Step 6 supplies transport and typed routing only;
// durable submission and execution remain owned by a later execution layer.
func NewServer() *Server {
	return newServer(operation.NewRegistry(), defaultExchanges)
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

		state := &connectionSession{}
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
	selected, parameters, err := negotiateCapabilities(hello, s.registry)
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
	case s.exchangeSlots <- struct{}{}:
		defer func() { <-s.exchangeSlots }()
	default:
		writer.WriteHeader(http.StatusServiceUnavailable)
		return
	}

	frame, err := readOneFrame(request.Body, uint32(parameters.maxFrameBody))
	if err != nil {
		abortHTTP2Stream()
	}
	if frame.sequence != 0 {
		abortHTTP2Stream()
	}
	if frame.kind != "command.submit" {
		abortHTTP2Stream()
	}
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
	if _, err := s.registry.Resolve(command.Request.Operation); err != nil {
		problem := compatibilityProblem(command.Request.Operation, hello.operations)
		s.writeProblemPayload(writer, frame.requestID, 0, problem, uint32(parameters.maxFrameBody))
		return
	}
	// Step 5 requires a durable submission owner and authenticated domain,
	// epoch, and Environment binding before a semantic Handler can run. Step 6
	// deliberately owns neither, so this path proves no submission and stops.
	s.writeProblem(writer, frame.requestID, 0, "transport.unavailable", uint32(parameters.maxFrameBody))
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
	if id == operation.MatterCreateV1.Metadata().Operation {
		knownName = true
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
