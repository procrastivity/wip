//go:build linux

package wipd

import (
	"bytes"
	"context"
	"crypto/tls"
	"errors"
	"io"
	"net"
	"net/http"
	"path/filepath"
	"reflect"
	"sync/atomic"
	"testing"
	"time"

	"github.com/procrastivity/wip/internal/operation"
	"github.com/procrastivity/wip/internal/wipdfixture"
	"golang.org/x/net/http2"
)

func TestAuthenticatedHTTP2UnixNegotiationAndCommandBoundary(t *testing.T) {
	registry := operation.NewRegistry()
	var handlerCalls atomic.Int32
	if err := registry.Register(operation.MatterCreateV1, func(context.Context, operation.Request) operation.Result {
		handlerCalls.Add(1)
		return operation.Result{Code: operation.ResultSucceeded, Output: operation.MatterCreateOutput{ID: "01M4FIXTURE0000000000000001", Locator: "fixture-17", Title: "M4 Fixture"}}
	}); err != nil {
		t.Fatal(err)
	}
	server := newServer(registry, 2)
	client, _ := startHTTP2UnixServer(t, server)

	vector := loadStep4FrameVector(t)
	negotiationWire := mustHex(t, vector.WireHex)
	response, err := postFrameRequest(client, negotiatePath, negotiationWire)
	if err != nil {
		t.Fatalf("HTTP/2 prior-knowledge negotiation over AF_UNIX: %v", err)
	}
	frames := readResponseFrames(t, response)
	if len(frames) != 2 {
		t.Fatalf("negotiation response records = %d, want ServerHello and SessionParameters", len(frames))
	}
	if frames[0].requestID != vector.RequestID || frames[0].sequence != 0 || frames[0].kind != "server.hello" {
		t.Fatalf("first negotiation response = %+v, want correlated ServerHello sequence 0", frames[0])
	}
	if frames[1].requestID != vector.RequestID || frames[1].sequence != 1 || frames[1].kind != "session.parameters" {
		t.Fatalf("second negotiation response = %+v, want correlated SessionParameters sequence 1", frames[1])
	}
	serverHello, err := decodePayload(frames[0].payload)
	if err != nil {
		t.Fatalf("decode ServerHello: %v", err)
	}
	wantHello := map[string]any{
		"selected_protocol": []any{uint64(1), uint64(0)},
		"identity_schemas":  []any{identitySchemaV1},
		"operations": []any{map[string]any{
			"name":             "matter.create",
			"versions":         []any{uint64(1)},
			"identity_schemas": []any{identitySchemaV1},
		}},
		"store_schemas": []any{storeSchemaV1},
		"features":      []any{frameSchema},
	}
	if !reflect.DeepEqual(serverHello, wantHello) {
		t.Fatalf("ServerHello = %#v, want exact M2 capability selection %#v", serverHello, wantHello)
	}
	parameters, err := decodePayload(frames[1].payload)
	if err != nil {
		t.Fatalf("decode SessionParameters: %v", err)
	}
	wantParameters := map[string]any{
		"frame_schema":             frameSchema,
		"max_frame_body":           uint64(1_048_576),
		"max_chunk_data":           uint64(65_536),
		"max_stream_bytes":         uint64(8_589_934_592),
		"max_concurrent_exchanges": uint64(2),
		"receive_window_bytes":     uint64(1_048_576),
	}
	if !reflect.DeepEqual(parameters, wantParameters) {
		t.Fatalf("SessionParameters = %#v, want protocol-bounded values %#v", parameters, wantParameters)
	}

	command, hash := canonicalGoldenCommand(t)
	commandPayload, err := encodePayload(map[string]any{
		"schema":            "wipd.command-submit/1",
		"canonical_command": command,
		"request_hash":      hash,
		"deadline":          nil,
	})
	if err != nil {
		t.Fatal(err)
	}
	requestID := "01K6A000000000000000000001"
	commandFrame, err := encodeFrame(frameRecord{requestID: requestID, sequence: 0, kind: "command.submit", payload: commandPayload}, maxFrameBodyLimit)
	if err != nil {
		t.Fatal(err)
	}
	call, requestWriter, writeDone, err := postOpenFrameRequest(client, exchangePath, commandFrame)
	if err != nil {
		t.Fatalf("start open-body command.submit: %v", err)
	}
	defer requestWriter.Close()
	var callResult httpCallResult
	select {
	case callResult = <-call:
	case <-time.After(5 * time.Second):
		t.Fatal("open-body exchange did not respond before the request writer was closed")
	}
	if callResult.err != nil {
		t.Fatalf("POST command.submit with open request body: %v", callResult.err)
	}
	frames = readResponseFrames(t, callResult.response)
	assertFixtureHandlerResult(t, frames, requestID, operation.ResultSucceeded, map[string]any{
		"id": "01M4FIXTURE0000000000000001", "locator": "fixture-17", "title": "M4 Fixture",
	})
	if err := <-writeDone; err != nil {
		t.Fatalf("write open-body command frame: %v", err)
	}
	if err := requestWriter.Close(); err != nil {
		t.Fatalf("close request writer after response: %v", err)
	}
	if got := handlerCalls.Load(); got != 1 {
		t.Fatalf("semantic Handler ran %d times for one accepted request, want 1", got)
	}

	for name, frame := range map[string]frameRecord{
		"wrong direction": {requestID: requestID, sequence: 0, kind: "server.hello", payload: []byte{0xa0}},
		"wrong sequence":  {requestID: requestID, sequence: 2, kind: "command.submit", payload: commandPayload},
	} {
		t.Run(name, func(t *testing.T) {
			wire, err := encodeFrame(frame, maxFrameBodyLimit)
			if err != nil {
				t.Fatal(err)
			}
			assertHTTP2StreamReset(t, client, wire)
			if got := handlerCalls.Load(); got != 1 {
				t.Fatalf("semantic Handler ran %d times after rejected %s frame, want only the prior accepted request", got, name)
			}
		})
	}

	badCBOR := framedBody([]byte{0xff})
	assertHTTP2StreamReset(t, client, badCBOR)

	firstIDFrame, err := encodeFrame(frameRecord{requestID: requestID, sequence: 0, kind: "command.submit", payload: commandPayload}, maxFrameBodyLimit)
	if err != nil {
		t.Fatal(err)
	}
	otherIDFrame, err := encodeFrame(frameRecord{requestID: "01K6A000000000000000000002", sequence: 1, kind: "control.cancel", payload: []byte{0xa0}}, maxFrameBodyLimit)
	if err != nil {
		t.Fatal(err)
	}
	assertHTTP2StreamReset(t, client, append(firstIDFrame, otherIDFrame...))

	misorderedFrame, err := encodeFrame(frameRecord{requestID: requestID, sequence: 2, kind: "control.cancel", payload: []byte{0xa0}}, maxFrameBodyLimit)
	if err != nil {
		t.Fatal(err)
	}

	assertHTTP2StreamReset(t, client, append(firstIDFrame, misorderedFrame...))

	validCancel, err := encodeFrame(frameRecord{requestID: requestID, sequence: 1, kind: "control.cancel", payload: []byte{0xa0}}, maxFrameBodyLimit)
	if err != nil {
		t.Fatal(err)
	}
	response, err = postFrameRequest(client, exchangePath, append(firstIDFrame, validCancel...))
	if err != nil {
		t.Fatalf("pre-dispatch control.cancel: %v", err)
	}
	frames = readResponseFrames(t, response)
	if len(frames) != 1 || frames[0].requestID != requestID || frames[0].kind != "problem" {
		t.Fatalf("pre-dispatch cancellation response = %+v, want correlated no-submission problem", frames)
	}
	problem, err := decodePayload(frames[0].payload)
	if err != nil || problem != "transport.cancelled-before-submission" {
		t.Fatalf("pre-dispatch cancellation problem = %#v, err %v; want transport.cancelled-before-submission", problem, err)
	}
	if got := handlerCalls.Load(); got != 1 {
		t.Fatalf("semantic Handler ran %d times after pre-dispatch cancellation, want only the prior accepted request", got)
	}

	probeFrame, err := encodeFrame(frameRecord{
		requestID: "01K6A000000000000000000004",
		sequence:  0,
		kind:      "command.submit",
		payload:   commandPayload,
	}, maxFrameBodyLimit)
	if err != nil {
		t.Fatal(err)
	}
	response, err = postFrameRequest(client, exchangePath, probeFrame)
	if err != nil {
		t.Fatalf("valid exchange after stream-local errors: %v", err)
	}
	frames = readResponseFrames(t, response)
	assertFixtureHandlerResult(t, frames, "01K6A000000000000000000004", operation.ResultSucceeded, map[string]any{
		"id": "01M4FIXTURE0000000000000001", "locator": "fixture-17", "title": "M4 Fixture",
	})
	if got := handlerCalls.Load(); got != 2 {
		t.Fatalf("semantic Handler ran %d times after malformed/rejected exchanges, want exactly the two accepted requests", got)
	}
}

func TestRegistryDispatchPersistsOnlyLocalFixtureAndReturnsTypedOutput(t *testing.T) {
	registry := operation.NewRegistry()
	var daemon *Daemon
	wantRecord := wipdfixture.Record{ID: "m4-fixture-handler-record", Key: "fixture-17", Value: "M4 Fixture"}
	wantOutput := operation.MatterCreateOutput{ID: "01M4FIXTURE0000000000000001", Locator: "fixture-17", Title: "M4 Fixture"}
	if err := registry.Register(operation.MatterCreateV1, func(ctx context.Context, request operation.Request) operation.Result {
		input, ok := request.Input.(operation.MatterCreateInput)
		if !ok {
			return operation.Result{Code: operation.ResultFailed, Problem: &operation.Problem{Code: operation.ProblemExecutionFailed, Message: "unexpected fixture input type"}}
		}
		record := wipdfixture.Record{ID: wantRecord.ID, Key: input.Locator, Value: input.Title}
		if err := daemon.fixture.Put(ctx, record); err != nil {
			return operation.Result{Code: operation.ResultFailed, Problem: &operation.Problem{Code: operation.ProblemExecutionFailed, Message: err.Error()}}
		}
		return operation.Result{Code: operation.ResultSucceeded, Output: wantOutput}
	}); err != nil {
		t.Fatal(err)
	}
	client, _ := startHTTP2UnixServerWithSetup(t, newServer(registry, 1), func(started *Daemon) { daemon = started })
	negotiateHTTP2TestSession(t, client)
	command, hash := canonicalFixtureCommand(t)
	commandFrame := mustCommandFrame(t, "01K6A000000000000000000010", command, hash)
	response, err := postFrameRequest(client, exchangePath, commandFrame)
	if err != nil {
		t.Fatalf("POST fixture-backed command: %v", err)
	}
	frames := readResponseFrames(t, response)
	assertFixtureHandlerResult(t, frames, "01K6A000000000000000000010", operation.ResultSucceeded, map[string]any{
		"id": wantOutput.ID, "locator": wantOutput.Locator, "title": wantOutput.Title,
	})
	gotRecord, err := daemon.fixture.Get(context.Background(), wantRecord.Key)
	if err != nil {
		t.Fatalf("read test-only persisted fixture record: %v", err)
	}
	if gotRecord != wantRecord {
		t.Fatalf("persisted fixture record = %+v, want asymmetric fixture value %+v", gotRecord, wantRecord)
	}
}

func TestExchangeConcurrencyLimitReturnsCorrelatedOverloadBeforeDispatch(t *testing.T) {
	registry := operation.NewRegistry()
	var handlerCalls atomic.Int32
	handlerStarted := make(chan struct{}, 1)
	releaseHandler := make(chan struct{})
	if err := registry.Register(operation.MatterCreateV1, func(context.Context, operation.Request) operation.Result {
		handlerCalls.Add(1)
		handlerStarted <- struct{}{}
		<-releaseHandler
		return operation.Result{Code: operation.ResultSucceeded, Output: operation.MatterCreateOutput{ID: "01M4FIXTURE0000000000000001", Locator: "fixture-17", Title: "M4 Fixture"}}
	}); err != nil {
		t.Fatal(err)
	}
	server := newServer(registry, 1)
	client, _ := startHTTP2UnixServer(t, server)
	vector := loadStep4FrameVector(t)
	response, err := postFrameRequest(client, negotiatePath, mustHex(t, vector.WireHex))
	if err != nil {
		t.Fatalf("negotiate test session: %v", err)
	}
	_ = readResponseFrames(t, response)

	command, hash := canonicalFixtureCommand(t)
	firstRequestID := "01K6A000000000000000000011"
	firstCall, firstWriter, firstWriteDone, err := postOpenFrameRequest(client, exchangePath, mustCommandFrame(t, firstRequestID, command, hash))
	if err != nil {
		t.Fatalf("start first active exchange: %v", err)
	}
	defer firstWriter.Close()
	select {
	case <-handlerStarted:
	case <-time.After(5 * time.Second):
		t.Fatal("first valid exchange did not reach the registered Handler")
	}
	if active := len(server.exchangeSlots); active != 1 {
		t.Fatalf("active exchanges = %d, want first pending Handler to hold the sole slot", active)
	}

	secondRequestID := "01K6A000000000000000000012"
	secondFrame := mustCommandFrame(t, secondRequestID, command, hash)
	secondResponse, err := postFrameRequest(client, exchangePath, secondFrame)
	if err != nil {
		t.Fatalf("second exchange request: %v", err)
	}
	frames := readResponseFrames(t, secondResponse)
	if len(frames) != 1 || frames[0].requestID != secondRequestID || frames[0].sequence != 0 || frames[0].kind != "problem" {
		t.Fatalf("overloaded response frames = %+v, want one correlated problem", frames)
	}
	problem, err := decodePayload(frames[0].payload)
	if err != nil || problem != "transport.overloaded" {
		t.Fatalf("overloaded response payload = %#v, err %v; want transport.overloaded", problem, err)
	}
	if got := handlerCalls.Load(); got != 1 {
		t.Fatalf("Handlers called = %d after overload, want only the admitted request", got)
	}

	close(releaseHandler)
	select {
	case result := <-firstCall:
		if result.err != nil {
			t.Fatalf("first exchange after Handler completion: %v", result.err)
		}
		frames := readResponseFrames(t, result.response)
		assertFixtureHandlerResult(t, frames, firstRequestID, operation.ResultSucceeded, map[string]any{
			"id": "01M4FIXTURE0000000000000001", "locator": "fixture-17", "title": "M4 Fixture",
		})
	case <-time.After(5 * time.Second):
		t.Fatal("first exchange did not complete after releasing its Handler")
	}
	if err := <-firstWriteDone; err != nil {
		t.Fatalf("write first exchange frame: %v", err)
	}
}

func TestControlCancelStopsWaitingWithoutCancellingOrRollingBackHandler(t *testing.T) {
	registry := operation.NewRegistry()
	var daemon *Daemon
	handlerStarted := make(chan struct{}, 1)
	releaseHandler := make(chan struct{})
	handlerFinished := make(chan error, 1)
	wantRecord := wipdfixture.Record{ID: "cancelled-wait-handler-record", Key: "cancel-wait-key", Value: "persisted-after-cancel"}
	if err := registry.Register(operation.MatterCreateV1, func(ctx context.Context, request operation.Request) operation.Result {
		handlerStarted <- struct{}{}
		select {
		case <-releaseHandler:
		case <-ctx.Done():
			handlerFinished <- ctx.Err()
			return operation.Result{Code: operation.ResultFailed, Problem: &operation.Problem{Code: operation.ProblemExecutionFailed, Message: ctx.Err().Error()}}
		}
		err := daemon.fixture.Put(ctx, wantRecord)
		handlerFinished <- err
		if err != nil {
			return operation.Result{Code: operation.ResultFailed, Problem: &operation.Problem{Code: operation.ProblemExecutionFailed, Message: err.Error()}}
		}
		return operation.Result{Code: operation.ResultSucceeded, Output: operation.MatterCreateOutput{ID: "01M4FIXTURE0000000000000001", Locator: "fixture-17", Title: "M4 Fixture"}}
	}); err != nil {
		t.Fatal(err)
	}
	server := newServer(registry, 1)
	client, _ := startHTTP2UnixServerWithSetup(t, server, func(started *Daemon) { daemon = started })
	negotiateHTTP2TestSession(t, client)
	command, hash := canonicalFixtureCommand(t)
	requestID := "01K6A000000000000000000013"
	call, requestWriter, writeDone, err := postOpenFrameRequest(client, exchangePath, mustCommandFrame(t, requestID, command, hash))
	if err != nil {
		t.Fatalf("start cancellable wait: %v", err)
	}
	defer requestWriter.Close()
	select {
	case <-handlerStarted:
	case <-time.After(5 * time.Second):
		t.Fatal("valid request did not enter the pending Handler")
	}
	if err := <-writeDone; err != nil {
		t.Fatalf("write command frame: %v", err)
	}

	cancelFrame, err := encodeFrame(frameRecord{requestID: requestID, sequence: 1, kind: "control.cancel", payload: []byte{0xa0}}, maxFrameBodyLimit)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := requestWriter.Write(cancelFrame); err != nil {
		t.Fatalf("write control.cancel while Handler is pending: %v", err)
	}
	select {
	case result := <-call:
		if result.response != nil {
			_ = result.response.Body.Close()
		}
		if result.err == nil {
			t.Fatal("cancelled exchange returned a semantic response; want only a stopped HTTP/2 wait stream")
		}
	case <-time.After(5 * time.Second):
		t.Fatal("valid control.cancel did not stop the waiting HTTP/2 stream")
	}
	if active := len(server.exchangeSlots); active != 1 {
		t.Fatalf("active Handler slots after cancel = %d, want 1 until Handler completion", active)
	}
	select {
	case err := <-handlerFinished:
		t.Fatalf("Handler completed before explicit release after wait cancellation: %v", err)
	default:
	}

	close(releaseHandler)
	select {
	case err := <-handlerFinished:
		if err != nil {
			t.Fatalf("complete test-only Handler after wait cancellation: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("Handler did not finish after explicit release")
	}
	got, err := daemon.fixture.Get(context.Background(), wantRecord.Key)
	if err != nil {
		t.Fatalf("read fixture after cancelled wait: %v", err)
	}
	if got != wantRecord {
		t.Fatalf("fixture after cancelled wait = %+v, want completed Handler value %+v; cancellation must not imply rollback", got, wantRecord)
	}
	if active := len(server.exchangeSlots); active != 0 {
		t.Fatalf("active Handler slots after completion = %d, want 0", active)
	}
}

func TestCapabilityRejectionIsReportedOverAuthenticatedHTTP2(t *testing.T) {
	server := NewServer()
	client, _ := startHTTP2UnixServer(t, server)
	vector := loadStep4FrameVector(t)
	payload := mustHex(t, vector.PayloadHex)
	value, err := decodePayload(payload)
	if err != nil {
		t.Fatal(err)
	}
	hello := value.(map[string]any)
	hello["protocol_min"] = []any{uint64(2), uint64(0)}
	hello["protocol_max"] = []any{uint64(2), uint64(1)}
	encodedHello, err := encodePayload(hello)
	if err != nil {
		t.Fatal(err)
	}
	wire, err := encodeFrame(frameRecord{requestID: vector.RequestID, sequence: 0, kind: "client.hello", payload: encodedHello}, bootstrapFrameBodyLimit)
	if err != nil {
		t.Fatal(err)
	}
	response, err := postFrameRequest(client, negotiatePath, wire)
	if err != nil {
		t.Fatalf("incompatible capability request: %v", err)
	}
	frames := readResponseFrames(t, response)
	if len(frames) != 1 || frames[0].kind != "problem" || frames[0].sequence != 0 {
		t.Fatalf("incompatible negotiation response = %+v, want one correlated problem", frames)
	}
	code, err := decodePayload(frames[0].payload)
	if err != nil {
		t.Fatal(err)
	}
	if code != errIncompatibleVersion.Error() {
		t.Fatalf("incompatible negotiation code = %#v, want %q", code, errIncompatibleVersion)
	}
	assertHTTP2StreamReset(t, client, mustHex(t, vector.WireHex))

	malformedHello := make(map[string]any, len(hello)+1)
	for key, value := range hello {
		malformedHello[key] = value
	}
	malformedHello["unknown"] = "not part of ClientHello"
	malformedPayload, err := encodePayload(malformedHello)
	if err != nil {
		t.Fatal(err)
	}
	malformedWire, err := encodeFrame(frameRecord{
		requestID: "01K6A000000000000000000005",
		sequence:  0,
		kind:      "client.hello",
		payload:   malformedPayload,
	}, bootstrapFrameBodyLimit)
	if err != nil {
		t.Fatal(err)
	}
	client, _ = startHTTP2UnixServer(t, NewServer())
	response, err = postFrameRequest(client, negotiatePath, malformedWire)
	if err != nil {
		t.Fatalf("unknown ClientHello field: %v", err)
	}
	frames = readResponseFrames(t, response)
	if len(frames) != 1 || frames[0].requestID != "01K6A000000000000000000005" || frames[0].kind != "problem" {
		t.Fatalf("malformed negotiation response = %+v, want correlated protocol problem", frames)
	}
	code, err = decodePayload(frames[0].payload)
	if err != nil {
		t.Fatal(err)
	}
	if code != errMalformedMessage.Error() {
		t.Fatalf("malformed negotiation code = %#v, want %q", code, errMalformedMessage)
	}
}

func startHTTP2UnixServer(t *testing.T, server *Server) (*http.Client, *http2.Transport) {
	return startHTTP2UnixServerWithSetup(t, server, nil)
}

func startHTTP2UnixServerWithSetup(t *testing.T, server *Server, setup func(*Daemon)) (*http.Client, *http2.Transport) {
	t.Helper()
	root := testProfileRoot(t)
	daemon, err := Start(root)
	if err != nil {
		t.Fatalf("start isolated test daemon: %v", err)
	}
	if setup != nil {
		setup(daemon)
	}
	ctx, cancel := context.WithCancel(context.Background())
	serveDone := make(chan error, 1)
	go func() { serveDone <- server.Serve(ctx, daemon) }()
	transport := &http2.Transport{
		AllowHTTP: true,
		DialTLSContext: func(ctx context.Context, _, _ string, _ *tls.Config) (net.Conn, error) {
			return (&net.Dialer{}).DialContext(ctx, "unix", filepath.Join(root, socketFileName))
		},
	}
	t.Cleanup(func() {
		transport.CloseIdleConnections()
		cancel()
		select {
		case err := <-serveDone:
			if err != nil {
				t.Errorf("authenticated HTTP/2 server shutdown: %v", err)
			}
		case <-time.After(5 * time.Second):
			t.Error("HTTP/2 server did not stop after context cancellation")
		}
	})
	return &http.Client{Transport: transport, Timeout: 10 * time.Second}, transport
}

func postFrameRequest(client *http.Client, path string, body []byte) (*http.Response, error) {
	request, err := http.NewRequest(http.MethodPost, "http://wipd"+path, bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	request.Header.Set("Content-Type", "application/octet-stream")
	return client.Do(request)
}

type httpCallResult struct {
	response *http.Response
	err      error
}

func postOpenFrameRequest(client *http.Client, path string, body []byte) (<-chan httpCallResult, *io.PipeWriter, <-chan error, error) {
	reader, writer := io.Pipe()
	request, err := http.NewRequest(http.MethodPost, "http://wipd"+path, reader)
	if err != nil {
		_ = reader.Close()
		_ = writer.Close()
		return nil, nil, nil, err
	}
	request.Header.Set("Content-Type", "application/octet-stream")
	call := make(chan httpCallResult, 1)
	go func() {
		response, err := client.Do(request)
		call <- httpCallResult{response: response, err: err}
	}()
	writeDone := make(chan error, 1)
	go func() {
		_, err := writer.Write(body)
		writeDone <- err
	}()
	return call, writer, writeDone, nil
}

func negotiateHTTP2TestSession(t *testing.T, client *http.Client) {
	t.Helper()
	vector := loadStep4FrameVector(t)
	response, err := postFrameRequest(client, negotiatePath, mustHex(t, vector.WireHex))
	if err != nil {
		t.Fatalf("negotiate test session: %v", err)
	}
	frames := readResponseFrames(t, response)
	if len(frames) != 2 || frames[0].kind != "server.hello" || frames[1].kind != "session.parameters" {
		t.Fatalf("test session negotiation records = %+v, want ServerHello and SessionParameters", frames)
	}
}

func mustCommandFrame(t *testing.T, requestID string, canonicalCommand []byte, hash string) []byte {
	t.Helper()
	payload, err := encodePayload(map[string]any{
		"schema":            "wipd.command-submit/1",
		"canonical_command": canonicalCommand,
		"request_hash":      hash,
		"deadline":          nil,
	})
	if err != nil {
		t.Fatal(err)
	}
	wire, err := encodeFrame(frameRecord{requestID: requestID, sequence: 0, kind: "command.submit", payload: payload}, maxFrameBodyLimit)
	if err != nil {
		t.Fatal(err)
	}
	return wire
}

func canonicalFixtureCommand(t *testing.T) ([]byte, string) {
	t.Helper()
	canonical, _ := canonicalGoldenCommand(t)
	command, err := operation.DecodeCanonicalCommand(canonical)
	if err != nil {
		t.Fatalf("decode M2 golden command for fixture input: %v", err)
	}
	command.Request.Input = operation.MatterCreateInput{Title: "M4 Fixture", Locator: "fixture-17"}
	canonical, err = command.CanonicalBytes()
	if err != nil {
		t.Fatalf("encode asymmetric fixture command: %v", err)
	}
	hash, err := command.RequestHash()
	if err != nil {
		t.Fatalf("hash asymmetric fixture command: %v", err)
	}
	return canonical, hash
}

func assertFixtureHandlerResult(t *testing.T, frames []frameRecord, requestID string, code operation.ResultCode, wantOutput map[string]any) {
	t.Helper()
	if len(frames) != 1 || frames[0].requestID != requestID || frames[0].sequence != 0 || frames[0].kind != "response.end" {
		t.Fatalf("fixture Handler response frames = %+v, want one correlated response.end", frames)
	}
	value, err := decodePayload(frames[0].payload)
	if err != nil {
		t.Fatalf("decode fixture Handler result: %v", err)
	}
	fields, ok := value.(map[string]any)
	if !ok || len(fields) != 5 || fields["schema"] != fixtureHandlerResultSchema || fields["result_code"] != string(code) || fields["problem_code"] != nil {
		t.Fatalf("fixture Handler result envelope = %#v, want closed local fixture schema and %s", value, code)
	}
	operationID, ok := fields["operation"].(map[string]any)
	if !ok || !reflect.DeepEqual(operationID, map[string]any{"name": "matter.create", "version": uint64(1)}) {
		t.Fatalf("fixture Handler operation identity = %#v, want matter.create@v1", fields["operation"])
	}
	outputBytes, ok := fields["output"].([]byte)
	if !ok {
		t.Fatalf("fixture Handler output = %#v, want canonical typed-output CBOR bytes", fields["output"])
	}
	output, err := decodePayload(outputBytes)
	if err != nil {
		t.Fatalf("decode canonical typed Handler output: %v", err)
	}
	if !reflect.DeepEqual(output, wantOutput) {
		t.Fatalf("typed Handler output = %#v, want asymmetric output %#v", output, wantOutput)
	}
}

func readResponseFrames(t *testing.T, response *http.Response) []frameRecord {
	t.Helper()
	defer response.Body.Close()
	if response.ProtoMajor != 2 || response.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(response.Body)
		t.Fatalf("protocol response = HTTP/%d.%d %s body=%x, want HTTP/2 200", response.ProtoMajor, response.ProtoMinor, response.Status, body)
	}
	body, err := io.ReadAll(response.Body)
	if err != nil {
		t.Fatalf("read HTTP/2 response body: %v", err)
	}
	reader := bytes.NewReader(body)
	var frames []frameRecord
	for {
		frame, err := readFrame(reader, maxFrameBodyLimit)
		if errors.Is(err, io.EOF) {
			return frames
		}
		if err != nil {
			t.Fatalf("decode HTTP/2 response frame: %v", err)
		}
		frames = append(frames, frame)
	}
}

func assertHTTP2StreamReset(t *testing.T, client *http.Client, wire []byte) {
	t.Helper()
	response, err := postFrameRequest(client, exchangePath, wire)
	if err != nil {
		return
	}
	body, bodyErr := io.ReadAll(response.Body)
	_ = response.Body.Close()
	if bodyErr == nil {
		t.Fatalf("malformed exchange returned HTTP/%d.%d %s with complete body %x instead of resetting the stream", response.ProtoMajor, response.ProtoMinor, response.Status, body)
	}
}

func canonicalGoldenCommand(t *testing.T) ([]byte, string) {
	t.Helper()
	requestID := "01K5V8JQFM6Q3Q0XZ6F1Z7A2BC"
	command := operation.Command{
		ID:                     requestID,
		AuthorityDomainID:      "01K5V8K1Q5VX6Y0J8C9W3M4N5P",
		ExpectedAuthorityEpoch: 7,
		EnvironmentID:          "01K5V8K8A4J2N7R9T0V3X6Y8ZB",
		EnvironmentSequence:    42,
		ActedAt:                "2026-09-22T17:31:42.123456789Z",
		CorrelationCommandID:   requestID,
		Request: operation.Request{
			Operation: operation.MatterCreateV1.Metadata().Operation,
			Actor:     "role:builder",
			Context:   operation.Context{Repo: "01K5V8KGD3F6H9J2M4N7Q0R5TW"},
			Input:     operation.MatterCreateInput{Title: "Café protocol identity", Locator: "protocol-identity"},
			Blobs:     []operation.BlobInput{},
		},
	}
	canonical, err := command.CanonicalBytes()
	if err != nil {
		t.Fatalf("encode canonical M2 command: %v", err)
	}
	hash, err := command.RequestHash()
	if err != nil {
		t.Fatalf("hash canonical M2 command: %v", err)
	}
	const wantHash = "sha256:ad15faaa76992d045529ab28b7bd9ddd63799fa851641c131b881e141e6a9503"
	if hash != wantHash {
		t.Fatalf("canonical command hash = %s, want M2 golden %s", hash, wantHash)
	}
	return canonical, hash
}
