package wipd

import (
	"bytes"
	"context"
	"errors"
	"io"
	"net"
	"net/http"
	"regexp"
	"strings"
	"sync"
	"time"

	"github.com/procrastivity/wip/internal/d115"
	"github.com/procrastivity/wip/internal/operation"
	"github.com/procrastivity/wip/internal/wipdjournal"
	"github.com/procrastivity/wip/internal/wipdwire"
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
	namedBatchReadMu       sync.RWMutex
	namedBatchReader       NamedBatchReader
	routeResolveMu         sync.RWMutex
	routeResolver          RouteResolver
	routeMutationMu        sync.RWMutex
	routeMutations         RouteMutationHandler
	d115CreateMu           sync.RWMutex
	d115Lifecycle          D115LifecycleHandler
	attachMu               sync.RWMutex
	attachLifecycle        AttachLifecycleHandler
}

// RouteResolver runs the local, read-only route.resolve request using the
// kernel-authenticated process attached to the IPC connection.
type RouteResolver interface {
	ResolveRoute(context.Context, *PeerProcess) (operation.RouteResolveOutput, error)
}

// RouteMutationHandler forwards the authenticated peer's registered-checkout
// route mutation through the configured authority command exchange.
type RouteMutationHandler interface {
	ExecuteRouteCommand(context.Context, *PeerProcess, operation.Command) (operation.Result, error)
}

// ConfigureRouteMutations installs the authenticated route-bind/adoption path
// before Serve. The handler must derive live checkout evidence from peer.
func (s *Server) ConfigureRouteMutations(handler RouteMutationHandler) error {
	if s == nil || handler == nil {
		return errors.New("wipd: route mutation handler is required")
	}
	s.routeMutationMu.Lock()
	defer s.routeMutationMu.Unlock()
	if s.routeMutations != nil {
		return errors.New("wipd: route mutation handler is already configured")
	}
	s.routeMutations = handler
	return nil
}

func (s *Server) routeMutationHandler() RouteMutationHandler {
	if s == nil {
		return nil
	}
	s.routeMutationMu.RLock()
	defer s.routeMutationMu.RUnlock()
	return s.routeMutations
}

// D115LifecycleHandler implements the prepare/import/explicit-submit local
// create-domain lifecycle. The authenticated peer is the only source of local
// checkout correlation.
type D115LifecycleHandler interface {
	Prepare(context.Context, *PeerProcess, string) (D115LocalAttempt, error)
	ImportProof(context.Context, string, []byte) (D115LocalAttempt, error)
	Submit(context.Context, *PeerProcess, string) ([]byte, error)
	Status(context.Context, string) (D115LocalAttempt, error)
	Cancel(context.Context, string) (D115LocalAttempt, error)
}

// D115LocalAttempt is the restricted local-IPC attempt projection.
type D115LocalAttempt struct {
	AttemptID      string
	State          string
	SigningRequest []byte
	Receipt        []byte
}

// AttachLocalAttempt is the deliberately narrow local view of one durable
// Environment attach intent. It never returns grant or checkout path bytes.
type AttachLocalAttempt struct {
	AttemptID     string
	State         string
	Receipt       []byte
	SignedReceipt []byte
}

// AttachLifecycleHandler owns durable same-intent create/resume and receipt
// installation for an authenticated local checkout peer.
type AttachLifecycleHandler interface {
	AttachOnce(context.Context, *PeerProcess, string) (AttachLocalAttempt, error)
	AttachStatus(context.Context, *PeerProcess, string) (AttachLocalAttempt, error)
}

// ConfigureAttachLifecycle installs the Step 11 attach owner before Serve.
func (s *Server) ConfigureAttachLifecycle(handler AttachLifecycleHandler) error {
	if s == nil || handler == nil {
		return errors.New("wipd: attach lifecycle handler is required")
	}
	s.attachMu.Lock()
	defer s.attachMu.Unlock()
	if s.attachLifecycle != nil {
		return errors.New("wipd: attach lifecycle is already configured")
	}
	s.attachLifecycle = handler
	return nil
}

func (s *Server) attachLifecycleHandler() AttachLifecycleHandler {
	if s == nil {
		return nil
	}
	s.attachMu.RLock()
	defer s.attachMu.RUnlock()
	return s.attachLifecycle
}

// D115SubmitDispositionError carries the durable command disposition for a
// submit that did not return a receipt.
type D115SubmitDispositionError struct{ Disposition string }

func (err *D115SubmitDispositionError) Error() string {
	if err == nil {
		return "wipd: D115 submit disposition unavailable"
	}
	return "wipd: D115 submit disposition: " + err.Disposition
}

// ConfigureD115Lifecycle installs the owner-mediated D115 lifecycle before Serve.
func (s *Server) ConfigureD115Lifecycle(handler D115LifecycleHandler) error {
	if s == nil || handler == nil {
		return errors.New("wipd: D115 lifecycle handler is required")
	}
	s.d115CreateMu.Lock()
	defer s.d115CreateMu.Unlock()
	if s.d115Lifecycle != nil {
		return errors.New("wipd: D115 path is already configured")
	}
	s.d115Lifecycle = handler
	return nil
}

// RouteResolverFunc adapts a function to RouteResolver.
type RouteResolverFunc func(context.Context, *PeerProcess) (operation.RouteResolveOutput, error)

// ResolveRoute implements RouteResolver by invoking f with the authenticated peer.
func (f RouteResolverFunc) ResolveRoute(ctx context.Context, peer *PeerProcess) (operation.RouteResolveOutput, error) {
	return f(ctx, peer)
}

// ConfigureRouteResolve installs the trusted local route resolver before Serve.
// It is independent of Repo-bound command runtime and journal configuration.
func (s *Server) ConfigureRouteResolve(resolver RouteResolver) error {
	if s == nil || resolver == nil {
		return errors.New("wipd: route resolver is required")
	}
	s.routeResolveMu.Lock()
	defer s.routeResolveMu.Unlock()
	if s.routeResolver != nil {
		return errors.New("wipd: route resolver is already configured")
	}
	s.routeResolver = resolver
	return nil
}

func (s *Server) routeResolvePath() RouteResolver {
	if s == nil {
		return nil
	}
	s.routeResolveMu.RLock()
	defer s.routeResolveMu.RUnlock()
	return s.routeResolver
}

// NamedBatchReader executes only the authenticated batch.read@v1 authority
// query. Implementations must not mutate Environment or command state.
type NamedBatchReader interface {
	ReadNamedBatch(context.Context, wipdwire.LocalNamedBatchReadRequest) (wipdwire.NamedBatchReadResponse, error)
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

// ConnectedCommandsConfigured reports whether the daemon has the durable
// connected-command journal and return path configured.
func (s *Server) ConnectedCommandsConfigured() bool {
	return s.connectedCommandStart() != nil
}

// RunConnectedCanonicalTerminal executes one exact authority command through
// the daemon's shared journal receipt barrier.
func (s *Server) RunConnectedCanonicalTerminal(ctx context.Context, command operation.Command,
	guardAndWrite func(context.Context, CommandStartSnapshot, operation.Command) error,
) (CommandStartResult, error) {
	coordinator := s.connectedCommandStart()
	if coordinator == nil {
		return CommandStartResult{}, ErrCommandStartBlocked
	}
	return coordinator.RunConnectedCanonicalTerminal(ctx, command, guardAndWrite)
}

// ConfigureNamedBatchRead enables the explicit Step 09 read capability. It
// must be configured before Serve and is independent of command submission.
func (s *Server) ConfigureNamedBatchRead(reader NamedBatchReader) error {
	if s == nil || reader == nil {
		return errors.New("wipd: named-Batch reader is required")
	}
	s.namedBatchReadMu.Lock()
	defer s.namedBatchReadMu.Unlock()
	if s.namedBatchReader != nil {
		return errors.New("wipd: named-Batch read path is already configured")
	}
	s.namedBatchReader = reader
	return nil
}

func (s *Server) namedBatchReadPath() NamedBatchReader {
	if s == nil {
		return nil
	}
	s.namedBatchReadMu.RLock()
	defer s.namedBatchReadMu.RUnlock()
	return s.namedBatchReader
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

		connectionCtx := context.WithValue(serveCtx, peerProcessContextKey{}, peerProcessForConnection(connection))
		state := &connectionSession{ctx: connectionCtx}
		connectionCtx = context.WithValue(connectionCtx, sessionContextKey{}, state)
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
	case "/wipd/v1/create-local/prepare", "/wipd/v1/create-local/import-proof", "/wipd/v1/create-local/submit", "/wipd/v1/create-local/status", "/wipd/v1/create-local/cancel":
		s.serveD115Lifecycle(writer, request)
	case "/wipd/v1/environment/attach-once", "/wipd/v1/environment/attach-status":
		s.serveAttachLifecycle(writer, request)
	default:
		writer.WriteHeader(http.StatusNotFound)
	}
}

func (s *Server) serveAttachLifecycle(writer http.ResponseWriter, request *http.Request) {
	const maxAttachIPCBytes = 1 << 20
	if request.ContentLength < 1 || request.ContentLength > maxAttachIPCBytes || len(request.TransferEncoding) != 0 ||
		request.Header.Get("Content-Type") != "application/cbor" || request.Header.Get("Accept") != "application/cbor" {
		http.Error(writer, "protocol.malformed-message", http.StatusBadRequest)
		return
	}
	body, err := io.ReadAll(http.MaxBytesReader(writer, request.Body, maxAttachIPCBytes))
	if err != nil || int64(len(body)) != request.ContentLength {
		http.Error(writer, "protocol.malformed-message", http.StatusBadRequest)
		return
	}
	defer clear(body)
	peer := PeerProcessFromContext(request.Context())
	if peer == nil {
		http.Error(writer, "route.discovery-unavailable", http.StatusForbidden)
		return
	}
	handler := s.attachLifecycleHandler()
	if handler == nil {
		http.Error(writer, "protocol.unsupported-extension", http.StatusNotImplemented)
		return
	}
	var attempt AttachLocalAttempt
	if request.URL.Path == "/wipd/v1/environment/attach-once" {
		fields, decodeErr := wipdwire.DecodeCanonicalMap(body, "schema", "grant_artifact")
		grant, ok := fields["grant_artifact"].(string)
		if decodeErr != nil || fields["schema"] != "wipd.environment-attach-once/1" || !ok || grant == "" || len(grant) > 1<<20 {
			http.Error(writer, "protocol.malformed-message", http.StatusBadRequest)
			return
		}
		attempt, err = handler.AttachOnce(request.Context(), peer, grant)
	} else {
		fields, decodeErr := wipdwire.DecodeCanonicalMap(body, "schema", "attempt_id")
		attemptID, ok := fields["attempt_id"].(string)
		if decodeErr != nil || fields["schema"] != "wipd.environment-attach-status/1" || !ok || !d115.ValidULID(attemptID) {
			http.Error(writer, "protocol.malformed-message", http.StatusBadRequest)
			return
		}
		attempt, err = handler.AttachStatus(request.Context(), peer, attemptID)
	}
	if err != nil {
		http.Error(writer, "environment.attach-refused", http.StatusConflict)
		return
	}
	if !d115.ValidULID(attempt.AttemptID) || attempt.State == "" || len(attempt.Receipt) > maxAttachIPCBytes || len(attempt.SignedReceipt) > maxAttachIPCBytes {
		http.Error(writer, "authority.invalid-response", http.StatusServiceUnavailable)
		return
	}
	var receipt any
	if len(attempt.Receipt) != 0 {
		receipt = bytes.Clone(attempt.Receipt)
	}
	var signedReceipt any
	if len(attempt.SignedReceipt) != 0 {
		signedReceipt = bytes.Clone(attempt.SignedReceipt)
	}
	response, err := wipdwire.EncodeCanonical(map[string]any{
		"schema": "wipd.environment-attach-state/1", "attempt_id": attempt.AttemptID, "state": attempt.State, "receipt": receipt, "signed_receipt": signedReceipt,
	})
	if err != nil || len(response) > maxAttachIPCBytes {
		http.Error(writer, "authority.invalid-response", http.StatusServiceUnavailable)
		return
	}
	writer.Header().Set("Content-Type", "application/cbor")
	writer.Header().Set("Cache-Control", "no-store")
	writer.WriteHeader(http.StatusOK)
	_, _ = writer.Write(response)
}

func (s *Server) serveD115Lifecycle(writer http.ResponseWriter, request *http.Request) {
	if request.ContentLength < 1 || request.ContentLength > 8192 || len(request.TransferEncoding) != 0 ||
		request.Header.Get("Content-Type") != "application/cbor" || request.Header.Get("Accept") != "application/cbor" {
		http.Error(writer, "protocol.malformed-message", http.StatusBadRequest)
		return
	}
	body, err := io.ReadAll(http.MaxBytesReader(writer, request.Body, 8192))
	if err != nil || int64(len(body)) != request.ContentLength {
		http.Error(writer, "protocol.malformed-message", http.StatusBadRequest)
		return
	}
	defer clear(body)
	peer := PeerProcessFromContext(request.Context())
	if peer == nil {
		http.Error(writer, "route.discovery-unavailable", http.StatusForbidden)
		return
	}
	handler := s.d115LifecyclePathHandler()
	if handler == nil {
		http.Error(writer, "protocol.unsupported-extension", http.StatusNotImplemented)
		return
	}
	var response []byte
	var attempt D115LocalAttempt
	var attemptID string
	switch request.URL.Path {
	case "/wipd/v1/create-local/prepare":
		fields, decodeErr := wipdwire.DecodeCanonicalMap(body, "schema", "operation", "grant_artifact")
		grant, ok := fields["grant_artifact"].(string)
		if decodeErr != nil || fields["schema"] != "wipd.create-local-prepare/1" || fields["operation"] != "repo.create-local@v1" || !ok || grant == "" || len(grant) > 5462 {
			http.Error(writer, "protocol.malformed-message", http.StatusBadRequest)
			return
		}
		attempt, err = handler.Prepare(request.Context(), peer, grant)
	case "/wipd/v1/create-local/import-proof":
		fields, decodeErr := wipdwire.DecodeCanonicalMap(body, "schema", "attempt_id", "proof_artifact")
		id, idOK := fields["attempt_id"].(string)
		proof, proofOK := fields["proof_artifact"].([]byte)
		if decodeErr != nil || fields["schema"] != "wipd.create-local-import-proof/1" || !idOK || !d115.ValidULID(id) || !proofOK || len(proof) > 2048 {
			http.Error(writer, "protocol.malformed-message", http.StatusBadRequest)
			return
		}
		attempt, err = handler.ImportProof(request.Context(), id, proof)
	case "/wipd/v1/create-local/submit":
		fields, decodeErr := wipdwire.DecodeCanonicalMap(body, "schema", "attempt_id")
		id, idOK := fields["attempt_id"].(string)
		if decodeErr != nil || fields["schema"] != "wipd.create-local-submit/1" || !idOK || !d115.ValidULID(id) {
			http.Error(writer, "protocol.malformed-message", http.StatusBadRequest)
			return
		}
		attemptID = id
		response, err = handler.Submit(request.Context(), peer, id)
	case "/wipd/v1/create-local/status", "/wipd/v1/create-local/cancel":
		fields, decodeErr := wipdwire.DecodeCanonicalMap(body, "schema", "attempt_id")
		id, idOK := fields["attempt_id"].(string)
		expected := "wipd.create-local-status/1"
		if strings.HasSuffix(request.URL.Path, "/cancel") {
			expected = "wipd.create-local-cancel/1"
		}
		if decodeErr != nil || fields["schema"] != expected || !idOK || !d115.ValidULID(id) {
			http.Error(writer, "protocol.malformed-message", http.StatusBadRequest)
			return
		}
		if expected == "wipd.create-local-cancel/1" {
			attempt, err = handler.Cancel(request.Context(), id)
		} else {
			attempt, err = handler.Status(request.Context(), id)
		}
	}
	if err != nil {
		var disposition *D115SubmitDispositionError
		if request.URL.Path == "/wipd/v1/create-local/submit" && errors.As(err, &disposition) && validD115SubmitDisposition(disposition.Disposition) {
			response, err = wipdwire.EncodeCanonical(map[string]any{
				"schema": "wipd.create-local-disposition/1", "attempt_id": attemptID, "disposition": disposition.Disposition,
			})
			if err == nil {
				writer.Header().Set("Content-Type", "application/cbor")
				writer.Header().Set("Cache-Control", "no-store")
				writer.WriteHeader(http.StatusOK)
				_, _ = writer.Write(response)
				return
			}
		}
		http.Error(writer, "create-local.refused", http.StatusConflict)
		return
	}
	if response == nil {
		if request.URL.Path == "/wipd/v1/create-local/submit" {
			http.Error(writer, "authority.invalid-response", http.StatusServiceUnavailable)
			return
		}
		schema := "wipd.create-local-state/1"
		if request.URL.Path == "/wipd/v1/create-local/prepare" {
			schema = "wipd.create-local-prepared/1"
		}
		fields := map[string]any{"schema": schema, "attempt_id": attempt.AttemptID, "state": attempt.State}
		if schema == "wipd.create-local-prepared/1" {
			if len(attempt.SigningRequest) > 0 {
				fields["signing_request"] = attempt.SigningRequest
			} else {
				fields["signing_request"] = nil
			}
		}
		if schema == "wipd.create-local-state/1" {
			if len(attempt.Receipt) > 0 {
				fields["receipt"] = attempt.Receipt
			} else {
				fields["receipt"] = nil
			}
		}
		response, err = wipdwire.EncodeCanonical(fields)
		if err != nil {
			http.Error(writer, "authority.invalid-response", http.StatusServiceUnavailable)
			return
		}
	}
	if len(response) == 0 || len(response) > 8192 {
		http.Error(writer, "authority.invalid-response", http.StatusServiceUnavailable)
		return
	}
	writer.Header().Set("Content-Type", "application/cbor")
	writer.Header().Set("Cache-Control", "no-store")
	writer.WriteHeader(http.StatusOK)
	_, _ = writer.Write(response)
}

func validD115SubmitDisposition(disposition string) bool {
	switch disposition {
	case "definitely-unsent", "outcome-unknown", "receipt-durable", "refused":
		return true
	default:
		return false
	}
}

func (s *Server) d115LifecyclePathHandler() D115LifecycleHandler {
	s.d115CreateMu.RLock()
	defer s.d115CreateMu.RUnlock()
	return s.d115Lifecycle
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
	selected, parameters, err := negotiateCapabilities(hello, s.registry, s.supportsBirthClaimRelease(), s.supportsClaimAcquire(), s.supportsClaimJournalClose(), s.supportsCommandSubmitV2(), s.namedBatchReadPath() != nil)
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
	if frame.kind == claimJournalCloseFrameKind {
		<-s.preflightSlots
		preflightOwned = false
		s.serveClaimJournalClose(writer, request, hello, parameters, frame)
		return
	}
	if frame.kind == claimAcquireFrameKind {
		<-s.preflightSlots
		preflightOwned = false
		s.serveClaimAcquire(writer, request, hello, parameters, frame)
		return
	}
	if frame.kind == schedulerResumeFrameKind {
		<-s.preflightSlots
		preflightOwned = false
		s.serveSchedulerResume(writer, request, hello, parameters, frame)
		return
	}
	if frame.kind == batchSweepFrameKind {
		<-s.preflightSlots
		preflightOwned = false
		s.serveBatchSweepAnonymous(writer, request, state, hello, parameters, frame)
		return
	}
	if frame.kind == namedBatchReadFrameKind {
		<-s.preflightSlots
		preflightOwned = false
		s.serveNamedBatchRead(writer, request, hello, parameters, frame)
		return
	}
	if frame.kind == "query.request" {
		<-s.preflightSlots
		preflightOwned = false
		s.serveRouteResolve(writer, request, hello, parameters, frame)
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

	command, deadline, submit, err := decodeNegotiatedCommandSubmit(frame.payload,
		containsString(hello.features, wipdwire.CommandSubmitV2Feature), s.supportsCommandSubmitV2())
	if err != nil {
		code := errMalformedMessage.Error()
		if errors.Is(err, errUnsupportedExtension) {
			code = errUnsupportedExtension.Error()
		}
		s.writeProblem(writer, frame.requestID, 0, code, uint32(parameters.maxFrameBody))
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
	if command.Request.Operation == operation.BatchSweepAnonymousV1.Metadata().Operation {
		s.writeProblem(writer, frame.requestID, 0, errUnsupportedExtension.Error(), uint32(parameters.maxFrameBody))
		return
	}
	if command.Request.Operation == operation.GateExemptionRepairV1.Metadata().Operation && s.connectedCommandStart() == nil {
		s.writeProblem(writer, frame.requestID, 0, errUnsupportedExtension.Error(), uint32(parameters.maxFrameBody))
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
	authenticatedPeer := PeerProcessFromContext(request.Context())
	go func() {
		defer close(dispatchDone)
		outcome := func() dispatchOutcome {
			if isStep11RouteMutation(command.Request.Operation) {
				handler := s.routeMutationHandler()
				if handler == nil || authenticatedPeer == nil {
					return dispatchOutcome{problemCode: "auth.environment-domain-mismatch"}
				}
				if !gate.begin(request.Context()) {
					return dispatchOutcome{problemCode: "transport.cancelled-before-submission"}
				}
				result, err := handler.ExecuteRouteCommand(state.ctx, authenticatedPeer, command)
				if err != nil {
					if errors.Is(err, ErrOutcomeUnknown) {
						return dispatchOutcome{problemCode: "transport.outcome-unknown"}
					}
					return dispatchOutcome{problemCode: "authority.unavailable"}
				}
				return dispatchOutcome{result: result}
			}
			if coordinator := s.connectedCommandStart(); coordinator != nil {
				if !gate.begin(request.Context()) {
					return dispatchOutcome{problemCode: "transport.cancelled-before-submission"}
				}
				connected, err := coordinator.RunConnectedCanonicalSubmission(state.ctx, command, submit,
					func(context.Context, CommandStartSnapshot, operation.Command) error { return nil })
				if err != nil {
					if errors.Is(err, wipdjournal.ErrCommandIDConflict) {
						return dispatchOutcome{problemCode: "command.id-conflict"}
					}
					if errors.Is(err, errUnsupportedExtension) {
						return dispatchOutcome{problemCode: errUnsupportedExtension.Error()}
					}
					if errors.Is(err, ErrOutcomeUnknown) {
						return dispatchOutcome{problemCode: "transport.outcome-unknown"}
					}
					if errors.Is(err, ErrSchedulerRunHeld) {
						return dispatchOutcome{problemCode: "run.lock-held"}
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

func isStep11RouteMutation(id operation.ID) bool {
	return id == operation.RepoRouteBindV1.Metadata().Operation || id == operation.RepoAdoptRemoteV1.Metadata().Operation
}

func (s *Server) serveRouteResolve(writer http.ResponseWriter, request *http.Request, hello serverHello, parameters sessionParameters, frame frameRecord) {
	id := operation.RouteResolveV1.Metadata().Operation
	if !operationCapabilityContains(hello.operations, id, identitySchemaV1) {
		s.writeProblem(writer, frame.requestID, 0, string(operation.ProblemUnsupportedVersion), uint32(parameters.maxFrameBody))
		return
	}
	var envelope wipdwire.QueryRequest
	if wipdwire.DecodeCanonical(frame.payload, &envelope, "schema", "query", "query_payload", "deadline") != nil ||
		envelope.Schema != "wipd.query-request/1" || envelope.Query.Name != id.Name || envelope.Query.Version != id.Version ||
		len(envelope.QueryPayload) != 0 || envelope.Deadline != nil {
		s.writeProblem(writer, frame.requestID, 0, "protocol.malformed-message", uint32(parameters.maxFrameBody))
		return
	}
	peer := PeerProcessFromContext(request.Context())
	if peer == nil {
		s.writeProblem(writer, frame.requestID, 0, "route.discovery-unavailable", uint32(parameters.maxFrameBody))
		return
	}
	resolver := s.routeResolvePath()
	if resolver == nil {
		s.writeProblem(writer, frame.requestID, 0, "protocol.unsupported-extension", uint32(parameters.maxFrameBody))
		return
	}
	output, err := resolver.ResolveRoute(request.Context(), peer)
	if err != nil {
		code := "query.route-unresolved"
		if errors.Is(err, ErrUnsupportedCheckoutContext) {
			code = "route.discovery-unavailable"
		}
		s.writeProblem(writer, frame.requestID, 0, code, uint32(parameters.maxFrameBody))
		return
	}
	result := operation.Result{Code: operation.ResultSucceeded, Output: output}
	if err = operation.RouteResolveV1.ValidateResult(result); err != nil {
		s.writeProblem(writer, frame.requestID, 0, "authority.invalid-response", uint32(parameters.maxFrameBody))
		return
	}
	payload, err := wipdwire.EncodeCanonical(map[string]any{
		"repo_id": output.RepoID, "domain_id": output.DomainID, "authority_epoch": output.Epoch,
		"connection_profile": output.ConnectionProfile,
	})
	if err != nil {
		s.writeProblem(writer, frame.requestID, 0, "authority.invalid-response", uint32(parameters.maxFrameBody))
		return
	}
	wire, err := encodeFrame(frameRecord{requestID: frame.requestID, sequence: 0, kind: "response.end", payload: payload}, uint32(parameters.maxFrameBody))
	if err != nil {
		abortHTTP2Stream()
	}
	_, _ = writer.Write(wire)
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
