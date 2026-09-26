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
	response, err = postFrameRequest(client, exchangePath, commandFrame)
	if err != nil {
		t.Fatalf("POST command.submit: %v", err)
	}
	frames = readResponseFrames(t, response)
	if len(frames) != 1 || frames[0].requestID != requestID || frames[0].sequence != 0 || frames[0].kind != "problem" {
		t.Fatalf("command response records = %+v, want one correlated transport problem", frames)
	}
	problem, err := decodePayload(frames[0].payload)
	if err != nil {
		t.Fatalf("decode command response: %v", err)
	}
	if problem != "transport.unavailable" {
		t.Fatalf("command response = %#v, want transport.unavailable because durable Step 5 submission is absent", problem)
	}
	if got := handlerCalls.Load(); got != 0 {
		t.Fatalf("semantic Handler ran %d times without a durable submission owner, want zero", got)
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
			if got := handlerCalls.Load(); got != 0 {
				t.Fatalf("semantic Handler ran %d times after rejected %s frame, want zero", got, name)
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

	misorderedFrame, err := encodeFrame(frameRecord{requestID: requestID, sequence: 1, kind: "control.cancel", payload: []byte{0xa0}}, maxFrameBodyLimit)
	if err != nil {
		t.Fatal(err)
	}
	assertHTTP2StreamReset(t, client, append(firstIDFrame, misorderedFrame...))

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
	if len(frames) != 1 {
		t.Fatalf("post-reset probe response records = %+v, want transport.unavailable", frames)
	}
	problem, err = decodePayload(frames[0].payload)
	if err != nil {
		t.Fatal(err)
	}
	if problem != "transport.unavailable" {
		t.Fatalf("post-reset probe response = %#v, want same negotiated session to remain usable", problem)
	}
	if got := handlerCalls.Load(); got != 0 {
		t.Fatalf("semantic Handler ran %d times for malformed/correlated/order-rejected frames, want zero", got)
	}
}

func TestExchangeConcurrencyLimitAppliesBackpressureBeforeBodyParsing(t *testing.T) {
	server := newServer(operation.NewRegistry(), 1)
	client, _ := startHTTP2UnixServer(t, server)
	vector := loadStep4FrameVector(t)
	response, err := postFrameRequest(client, negotiatePath, mustHex(t, vector.WireHex))
	if err != nil {
		t.Fatalf("negotiate test session: %v", err)
	}
	_ = readResponseFrames(t, response)

	reader, writer := io.Pipe()
	request, err := http.NewRequest(http.MethodPost, "http://wipd"+exchangePath, reader)
	if err != nil {
		t.Fatal(err)
	}
	firstResponse := make(chan struct {
		response *http.Response
		err      error
	}, 1)
	go func() {
		response, err := client.Do(request)
		firstResponse <- struct {
			response *http.Response
			err      error
		}{response: response, err: err}
	}()

	deadline := time.Now().Add(5 * time.Second)
	for len(server.exchangeSlots) != 1 && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	if active := len(server.exchangeSlots); active != 1 {
		_ = writer.Close()
		t.Fatalf("active exchanges = %d, want first incomplete body to hold the sole slot", active)
	}

	secondFrame, err := encodeFrame(frameRecord{
		requestID: "01K6A000000000000000000003",
		sequence:  0,
		kind:      "server.hello",
		payload:   []byte{0xa0},
	}, maxFrameBodyLimit)
	if err != nil {
		t.Fatal(err)
	}
	secondResponse, err := postFrameRequest(client, exchangePath, secondFrame)
	if err != nil {
		t.Fatalf("second exchange request: %v", err)
	}
	if secondResponse.StatusCode != http.StatusServiceUnavailable || secondResponse.ProtoMajor != 2 {
		_ = secondResponse.Body.Close()
		t.Fatalf("overloaded response = HTTP/%d.%d %s, want HTTP/2 503", secondResponse.ProtoMajor, secondResponse.ProtoMinor, secondResponse.Status)
	}
	_ = secondResponse.Body.Close()

	_ = writer.Close()
	select {
	case result := <-firstResponse:
		if result.response != nil {
			body, bodyErr := io.ReadAll(result.response.Body)
			_ = result.response.Body.Close()
			if result.err == nil && bodyErr == nil {
				t.Fatalf("incomplete first exchange unexpectedly completed with body %x", body)
			}
		}
	case <-time.After(5 * time.Second):
		t.Fatal("first incomplete exchange did not stop after client closed its request body")
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
	t.Helper()
	root := testProfileRoot(t)
	daemon, err := Start(root)
	if err != nil {
		t.Fatalf("start isolated test daemon: %v", err)
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
