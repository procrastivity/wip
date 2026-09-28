//go:build linux

package wipd

import (
	"bytes"
	"context"
	"crypto/tls"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"path/filepath"
	"reflect"
	"strconv"
	"sync/atomic"
	"testing"
	"time"

	"github.com/procrastivity/wip/internal/operation"
	"github.com/procrastivity/wip/internal/wipdfixture"
	"github.com/procrastivity/wip/internal/wipdjournal"
	"golang.org/x/net/http2"
)

func TestAuthenticatedHTTP2UnixNegotiationAndCommandBoundary(t *testing.T) {
	registry := operation.NewRegistry()
	var handlerCalls atomic.Int32
	handlerResults := make(chan operation.Result, 8)
	handlerHolds := make(chan chan struct{}, 2)
	handlerHeld := make(chan struct{}, 2)
	if err := registry.Register(operation.MatterCreateV1, func(context.Context, operation.Request) operation.Result {
		handlerCalls.Add(1)
		select {
		case release := <-handlerHolds:
			handlerHeld <- struct{}{}
			<-release
		default:
		}
		result := operation.Result{Code: operation.ResultSucceeded, Output: operation.MatterCreateOutput{ID: "01M4F1XT4R3E00000000000001", Locator: "fixture-17", Title: "M4 Fixture"}}
		handlerResults <- result
		return result
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
	defer func() { _ = requestWriter.Close() }()
	var callResult httpCallResult
	select {
	case callResult = <-call:
	case <-time.After(5 * time.Second):
		t.Fatal("open-body exchange did not return before the request writer was closed")
	}
	assertCallM1Response(t, callResult, requestID, operation.Result{
		Code:   operation.ResultSucceeded,
		Output: operation.MatterCreateOutput{ID: "01M4F1XT4R3E00000000000001", Locator: "fixture-17", Title: "M4 Fixture"},
	})
	assertTypedHandlerResult(t, <-handlerResults, operation.ResultSucceeded, operation.MatterCreateOutput{
		ID: "01M4F1XT4R3E00000000000001", Locator: "fixture-17", Title: "M4 Fixture",
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
	if got := handlerCalls.Load(); got != 1 {
		t.Fatalf("semantic Handler ran %d times after rejected first records, want only the accepted request", got)
	}

	firstIDFrame, err := encodeFrame(frameRecord{requestID: requestID, sequence: 0, kind: "command.submit", payload: commandPayload}, maxFrameBodyLimit)
	if err != nil {
		t.Fatal(err)
	}
	otherIDFrame, err := encodeFrame(frameRecord{requestID: "01K6A000000000000000000002", sequence: 1, kind: "control.cancel", payload: []byte{0xa0}}, maxFrameBodyLimit)
	if err != nil {
		t.Fatal(err)
	}

	misorderedFrame, err := encodeFrame(frameRecord{requestID: requestID, sequence: 2, kind: "control.cancel", payload: []byte{0xa0}}, maxFrameBodyLimit)
	if err != nil {
		t.Fatal(err)
	}
	assertTrailingRecordsReset := func(t *testing.T, wire []byte) {
		t.Helper()
		firstFrameLength := int(binary.BigEndian.Uint32(wire[:4])) + 4
		release := make(chan struct{})
		handlerHolds <- release
		call, writer, writeDone, err := postOpenFrameRequest(client, exchangePath, wire[:firstFrameLength])
		if err != nil {
			t.Fatalf("start exchange with invalid trailing record: %v", err)
		}
		defer func() { _ = writer.Close() }()
		released := false
		defer func() {
			if !released {
				close(release)
			}
		}()
		select {
		case <-handlerHeld:
		case <-time.After(5 * time.Second):
			t.Fatal("first valid command did not reach its held Handler")
		}
		if err := <-writeDone; err != nil {
			t.Fatalf("write first command record: %v", err)
		}
		if _, err := writer.Write(wire[firstFrameLength:]); err != nil {
			t.Fatalf("write invalid trailing record after dispatch: %v", err)
		}
		select {
		case result := <-call:
			assertStreamReset(t, result)
		case <-time.After(5 * time.Second):
			t.Fatal("server did not reset the invalid trailing-record stream")
		}
		close(release)
		released = true
		assertTypedHandlerResult(t, <-handlerResults, operation.ResultSucceeded, operation.MatterCreateOutput{
			ID: "01M4F1XT4R3E00000000000001", Locator: "fixture-17", Title: "M4 Fixture",
		})
	}
	assertTrailingRecordsReset(t, append(firstIDFrame, otherIDFrame...))
	assertTrailingRecordsReset(t, append(firstIDFrame, misorderedFrame...))

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
	assertHTTPM1Response(t, response, err, "01K6A000000000000000000004", operation.Result{
		Code:   operation.ResultSucceeded,
		Output: operation.MatterCreateOutput{ID: "01M4F1XT4R3E00000000000001", Locator: "fixture-17", Title: "M4 Fixture"},
	})
	assertTypedHandlerResult(t, <-handlerResults, operation.ResultSucceeded, operation.MatterCreateOutput{
		ID: "01M4F1XT4R3E00000000000001", Locator: "fixture-17", Title: "M4 Fixture",
	})
	if got := handlerCalls.Load(); got != 4 {
		t.Fatalf("semantic Handler ran %d times, want accepted first records to dispatch and invalid first records to be rejected", got)
	}
}

func TestConfiguredDaemonRoutesMatterAndStepBirthThroughCommandStart(t *testing.T) {
	_, journal, environment, authority, _, server := newCommandStartFixture(t)
	var localDispatch atomic.Int32
	registry := server.registry
	if err := registry.Register(operation.MatterCreateV1, func(context.Context, operation.Request) operation.Result {
		localDispatch.Add(1)
		return operation.Result{Code: operation.ResultSucceeded, Output: operation.MatterCreateOutput{ID: commandStartCommandPrefix + "80", Locator: "local", Title: "local"}}
	}); err != nil {
		t.Fatal(err)
	}
	if err := registry.Register(operation.StepCreateV1, func(_ context.Context, request operation.Request) operation.Result {
		localDispatch.Add(1)
		input := request.Input.(operation.StepCreateInput)
		return operation.Result{Code: operation.ResultSucceeded, Output: operation.StepCreateOutput{ParentID: input.ParentID, Title: input.Title}}
	}); err != nil {
		t.Fatal(err)
	}
	authority.returnResults = []operation.ResultCode{operation.ResultSucceeded, operation.ResultSucceeded}
	authority.returnContinue = []bool{false, false}
	if err := server.ConfigureConnectedCommands(commandStartDomainID, journal, authority, environment); err != nil {
		t.Fatal(err)
	}
	client, _ := startHTTP2UnixServer(t, server)

	operations := []any{
		map[string]any{"name": "matter.create", "versions": []any{uint64(1)}, "identity_schemas": []any{identitySchemaV1}},
		map[string]any{"name": "step.create", "versions": []any{uint64(1)}, "identity_schemas": []any{identitySchemaV1}},
	}
	helloPayload, err := encodePayload(map[string]any{
		"protocol_min": []any{uint64(1), uint64(0)}, "protocol_max": []any{uint64(1), uint64(0)},
		"identity_schemas": []any{identitySchemaV1}, "operations": operations,
		"store_schemas": []any{storeSchemaV1}, "features": []any{frameSchema},
	})
	if err != nil {
		t.Fatal(err)
	}
	helloFrame, err := encodeFrame(frameRecord{requestID: commandStartCommandPrefix + "81", kind: "client.hello", payload: helloPayload}, bootstrapFrameBodyLimit)
	if err != nil {
		t.Fatal(err)
	}
	response, err := postFrameRequest(client, negotiatePath, helloFrame)
	if err != nil {
		t.Fatalf("negotiate birth command session: %v", err)
	}
	if frames := readResponseFrames(t, response); len(frames) != 2 || frames[0].kind != "server.hello" || frames[1].kind != "session.parameters" {
		t.Fatalf("birth session negotiation frames=%+v", frames)
	}

	matter := commandStartCanonicalCommand(commandStartCommandPrefix+"82", 1, "", commandStartCommandPrefix+"82",
		operation.MatterCreateV1.Metadata().Operation, operation.MatterCreateInput{Title: "Authority matter", Locator: "authority-matter"}, nil)
	environment.setCurrentID(matter.ID)
	matterResponse := submitConnectedCommand(t, client, matter, commandStartCommandPrefix+"83")
	matterResult, err := decodeOperationResultPayload(matter.Request.Operation, matterResponse.payload)
	if err != nil {
		t.Fatal(err)
	}
	matterOutput, ok := matterResult.Output.(operation.MatterCreateOutput)
	if matterResponse.kind != "response.end" || !ok || matterOutput.ID != commandStartCommandPrefix+"44" ||
		matterOutput.Locator != "authority-matter" || matterOutput.Title != "Authority matter" {
		t.Fatalf("daemon Matter response=%+v typed=%#v", matterResponse, matterResult)
	}

	step := commandStartCanonicalCommand(commandStartCommandPrefix+"84", 2, matter.ID, matter.ID,
		operation.StepCreateV1.Metadata().Operation, operation.StepCreateInput{ParentID: matterOutput.ID, Title: "Authority Step"},
		&operation.ClaimContext{ID: matterOutput.ID, Epoch: "1"})
	environment.setCurrentID(step.ID)
	stepResponse := submitConnectedCommand(t, client, step, commandStartCommandPrefix+"85")
	if stepResponse.kind != "response.end" {
		t.Fatalf("Step command returned protocol problem %q: %s", m2ProblemCode(t, stepResponse.payload), stepResponse.kind)
	}
	stepResult, err := decodeOperationResultPayload(step.Request.Operation, stepResponse.payload)
	if err != nil {
		t.Fatal(err)
	}
	stepOutput, ok := stepResult.Output.(operation.StepCreateOutput)
	if stepResponse.kind != "response.end" || !ok || stepOutput.ID != commandStartCommandPrefix+"46" || stepOutput.ParentID != matterOutput.ID ||
		stepOutput.MatterID != matterOutput.ID || stepOutput.Locator != "step-01" || stepOutput.Title != "Authority Step" ||
		stepOutput.SortKey != 1000 || stepOutput.State != "planned" {
		t.Fatalf("daemon Step response=%+v typed=%#v", stepResponse, stepResult)
	}
	if localDispatch.Load() != 0 {
		t.Fatalf("connected daemon dispatched locally %d times; expected authority-only execution", localDispatch.Load())
	}
	stepReceipt := append([]byte(nil), journalReceipt(t, journal, step.ID)...)

	replay := submitConnectedCommand(t, client, step, commandStartCommandPrefix+"86")
	replayedResult, err := decodeOperationResultPayload(step.Request.Operation, replay.payload)
	if err != nil || replay.kind != "response.end" || !reflect.DeepEqual(replayedResult, stepResult) || authority.returnIndex != 2 {
		t.Fatalf("same-ID/hash daemon replay=%+v result=%+v returnIndex=%d err=%v", replay, replayedResult, authority.returnIndex, err)
	}
	if !bytes.Equal(stepReceipt, journalReceipt(t, journal, step.ID)) || localDispatch.Load() != 0 {
		t.Fatal("exact daemon replay changed receipt or invoked local semantic dispatch")
	}
	conflict := step
	conflict.Request.Input = operation.StepCreateInput{ParentID: matterOutput.ID, Title: "Conflicting title"}
	conflictResponse := submitConnectedCommand(t, client, conflict, commandStartCommandPrefix+"87")
	if conflictResponse.kind != "problem" || m2ProblemCode(t, conflictResponse.payload) != "command.id-conflict" || authority.returnIndex != 2 {
		t.Fatalf("conflicting daemon retry=%+v authority returns=%d", conflictResponse, authority.returnIndex)
	}
	installed, err := journal.InstallSnapshot(context.Background())
	if err != nil || len(installed.Receipts) != 2 || installed.Anchor.EventCount != 5 {
		t.Fatalf("daemon birth replay duplicated terminal effect: snapshot=%+v err=%v", installed, err)
	}
}

func TestRegistryDispatchPersistsOnlyLocalFixtureAndReturnsTypedOutput(t *testing.T) {
	registry := operation.NewRegistry()
	var daemon *Daemon
	handlerResults := make(chan operation.Result, 1)
	wantRecord := wipdfixture.Record{ID: "m4-fixture-handler-record", Key: "fixture-17", Value: "M4 Fixture"}
	wantOutput := operation.MatterCreateOutput{ID: "01M4F1XT4R3E00000000000001", Locator: "fixture-17", Title: "M4 Fixture"}
	if err := registry.Register(operation.MatterCreateV1, func(ctx context.Context, request operation.Request) operation.Result {
		input, ok := request.Input.(operation.MatterCreateInput)
		if !ok {
			return operation.Result{Code: operation.ResultFailed, Problem: &operation.Problem{Code: operation.ProblemExecutionFailed, Message: "unexpected fixture input type"}}
		}
		record := wipdfixture.Record{ID: wantRecord.ID, Key: input.Locator, Value: input.Title}
		if err := daemon.fixture.Put(ctx, record); err != nil {
			return operation.Result{Code: operation.ResultFailed, Problem: &operation.Problem{Code: operation.ProblemExecutionFailed, Message: err.Error()}}
		}
		result := operation.Result{Code: operation.ResultSucceeded, Output: wantOutput}
		handlerResults <- result
		return result
	}); err != nil {
		t.Fatal(err)
	}
	client, _ := startHTTP2UnixServerWithSetup(t, newServer(registry, 1), func(started *Daemon) { daemon = started })
	negotiateHTTP2TestSession(t, client)
	command, hash := canonicalFixtureCommand(t)
	commandFrame := mustCommandFrame(t, "01K6A000000000000000000010", command, hash)
	response, err := postFrameRequest(client, exchangePath, commandFrame)
	assertHTTPM1Response(t, response, err, "01K6A000000000000000000010", operation.Result{
		Code:   operation.ResultSucceeded,
		Output: wantOutput,
	})
	assertTypedHandlerResult(t, <-handlerResults, operation.ResultSucceeded, wantOutput)
	gotRecord, err := daemon.fixture.Get(context.Background(), wantRecord.Key)
	if err != nil {
		t.Fatalf("read test-only persisted fixture record: %v", err)
	}
	if gotRecord != wantRecord {
		t.Fatalf("persisted fixture record = %+v, want asymmetric fixture value %+v", gotRecord, wantRecord)
	}
}

func TestDeclaredLongerOpenBodyDispatchesWithoutWaitingForEOF(t *testing.T) {
	registry := operation.NewRegistry()
	handlerResults := make(chan operation.Result, 1)
	wantOutput := operation.MatterCreateOutput{ID: "01M4F1XT4R3E00000000000001", Locator: "fixture-17", Title: "M4 Fixture"}
	if err := registry.Register(operation.MatterCreateV1, func(context.Context, operation.Request) operation.Result {
		result := operation.Result{Code: operation.ResultSucceeded, Output: wantOutput}
		handlerResults <- result
		return result
	}); err != nil {
		t.Fatal(err)
	}
	client, _ := startHTTP2UnixServer(t, newServer(registry, 1))
	negotiateHTTP2TestSession(t, client)
	command, hash := canonicalFixtureCommand(t)
	requestID := "01K6A000000000000000000014"
	commandFrame := mustCommandFrame(t, requestID, command, hash)
	cancelFrame, err := encodeFrame(frameRecord{requestID: requestID, sequence: 1, kind: "control.cancel", payload: []byte{0xa0}}, maxFrameBodyLimit)
	if err != nil {
		t.Fatal(err)
	}
	call, requestWriter, writeDone, err := postOpenFrameRequestWithLength(client, exchangePath, commandFrame, int64(len(commandFrame)+len(cancelFrame)))
	if err != nil {
		t.Fatalf("start declared-longer open-body request: %v", err)
	}
	defer func() { _ = requestWriter.Close() }()
	if err := <-writeDone; err != nil {
		t.Fatalf("write command frame while request remains open: %v", err)
	}
	select {
	case result := <-call:
		assertCallM1Response(t, result, requestID, operation.Result{
			Code:   operation.ResultSucceeded,
			Output: wantOutput,
		})
	case <-time.After(5 * time.Second):
		t.Fatal("server waited for the declared request body remainder instead of dispatching the first frame")
	}
	assertTypedHandlerResult(t, <-handlerResults, operation.ResultSucceeded, wantOutput)
	if err := requestWriter.Close(); err != nil {
		t.Fatalf("close request body after exchange returned: %v", err)
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
		return operation.Result{Code: operation.ResultSucceeded, Output: operation.MatterCreateOutput{ID: "01M4F1XT4R3E00000000000001", Locator: "fixture-17", Title: "M4 Fixture"}}
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
	defer func() { _ = firstWriter.Close() }()
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
		assertCallM1Response(t, result, firstRequestID, operation.Result{
			Code:   operation.ResultSucceeded,
			Output: operation.MatterCreateOutput{ID: "01M4F1XT4R3E00000000000001", Locator: "fixture-17", Title: "M4 Fixture"},
		})
	case <-time.After(5 * time.Second):
		t.Fatal("first exchange did not complete after releasing its Handler")
	}
	if err := <-firstWriteDone; err != nil {
		t.Fatalf("write first exchange frame: %v", err)
	}
}

func TestDomainExecutionLaneSerializesMutationsAndIsolatesOtherDomains(t *testing.T) {
	registry := operation.NewRegistry()
	var daemon *Daemon
	var callsA, callsB atomic.Int32
	firstAStarted := make(chan struct{}, 1)
	releaseFirstA := make(chan struct{})
	if err := registry.Register(operation.MatterCreateV1, func(ctx context.Context, request operation.Request) operation.Result {
		input := request.Input.(operation.MatterCreateInput)
		if input.Locator == "counter-a" {
			if callsA.Add(1) == 1 {
				firstAStarted <- struct{}{}
				select {
				case <-releaseFirstA:
				case <-ctx.Done():
					return executionFailed(ctx.Err())
				}
			}
		} else {
			callsB.Add(1)
		}
		return incrementFixtureCounter(ctx, daemon.fixture, request)
	}); err != nil {
		t.Fatal(err)
	}
	server := newServer(registry, 3)
	root, _ := startLocalIPCServerWithSetup(t, server, func(started *Daemon) { daemon = started })
	seedFixtureCounter(t, daemon.fixture, "counter-a")
	seedFixtureCounter(t, daemon.fixture, "counter-b")
	client, err := Connect(context.Background(), root)
	if err != nil {
		t.Fatalf("connect local test client: %v", err)
	}
	t.Cleanup(func() { _ = client.Close() })

	domainA := "01K5V8K1Q5VX6Y0J8C9W3M4N5P"
	domainB := "01K6B000000000000000000001"
	commandA1 := fixtureCommand(t, domainA, "01K6A000000000000000000031", 43, "increment-a-1", "counter-a")
	commandA2 := fixtureCommand(t, domainA, "01K6A000000000000000000032", 44, "increment-a-2", "counter-a")
	commandB := fixtureCommand(t, domainB, "01K6A000000000000000000033", 45, "increment-b", "counter-b")
	type callResult struct {
		result operation.Result
		err    error
	}
	run := func(command operation.Command) <-chan callResult {
		completed := make(chan callResult, 1)
		go func() {
			result, err := client.ExecuteCommand(context.Background(), command)
			completed <- callResult{result: result, err: err}
		}()
		return completed
	}
	waitResult := func(name string, completed <-chan callResult, want operation.Result) {
		t.Helper()
		select {
		case got := <-completed:
			if got.err != nil {
				t.Fatalf("%s execution error: %v", name, got.err)
			}
			assertSemanticResultsEqual(t, got.result, want)
		case <-time.After(5 * time.Second):
			t.Fatalf("%s did not complete", name)
		}
	}

	callA1 := run(commandA1)
	select {
	case <-firstAStarted:
	case <-time.After(5 * time.Second):
		t.Fatal("first domain-A mutation did not enter its Handler")
	}
	callA2 := run(commandA2)
	waitForLaneOccupancy(t, server.executionLanes, domainA, 1, 1)
	if got := callsA.Load(); got != 1 {
		t.Fatalf("same-domain Handler calls while first is held = %d, want only the first dispatch", got)
	}

	callB := run(commandB)
	waitResult("independent domain-B mutation", callB, operation.Result{
		Code: operation.ResultSucceeded,
		Output: operation.MatterCreateOutput{
			ID: "01M4F1XT4R3E00000000000001", Locator: "counter-b", Title: "1",
		},
	})
	if got := callsB.Load(); got != 1 {
		t.Fatalf("domain-B Handler calls = %d, want one independent dispatch while domain A is blocked", got)
	}

	close(releaseFirstA)
	waitResult("first domain-A mutation", callA1, operation.Result{
		Code: operation.ResultSucceeded,
		Output: operation.MatterCreateOutput{
			ID: "01M4F1XT4R3E00000000000001", Locator: "counter-a", Title: "1",
		},
	})
	waitResult("queued domain-A mutation", callA2, operation.Result{
		Code: operation.ResultSucceeded,
		Output: operation.MatterCreateOutput{
			ID: "01M4F1XT4R3E00000000000001", Locator: "counter-a", Title: "2",
		},
	})
	assertFixtureCounter(t, daemon.fixture, "counter-a", "2")
	assertFixtureCounter(t, daemon.fixture, "counter-b", "1")
	waitForExecutionLanesEmpty(t, server.executionLanes)
}

func TestControlCancelBeforeLaneDispatchReturnsNoEffect(t *testing.T) {
	registry := operation.NewRegistry()
	var daemon *Daemon
	var handlerCalls atomic.Int32
	firstStarted := make(chan struct{}, 1)
	releaseFirst := make(chan struct{})
	if err := registry.Register(operation.MatterCreateV1, func(ctx context.Context, request operation.Request) operation.Result {
		handlerCalls.Add(1)
		input := request.Input.(operation.MatterCreateInput)
		if input.Title == "hold-first" {
			firstStarted <- struct{}{}
			select {
			case <-releaseFirst:
			case <-ctx.Done():
				return executionFailed(ctx.Err())
			}
		}
		return incrementFixtureCounter(ctx, daemon.fixture, request)
	}); err != nil {
		t.Fatal(err)
	}
	server := newServer(registry, 2)
	var client *http.Client
	client, _ = startHTTP2UnixServerWithSetup(t, server, func(started *Daemon) { daemon = started })
	negotiateHTTP2TestSession(t, client)
	seedFixtureCounter(t, daemon.fixture, "held-counter")
	seedFixtureCounter(t, daemon.fixture, "cancelled-counter")

	domain := "01K5V8K1Q5VX6Y0J8C9W3M4N5P"
	first := fixtureCommand(t, domain, "01K6A000000000000000000041", 51, "hold-first", "held-counter")
	cancelled := fixtureCommand(t, domain, "01K6A000000000000000000042", 52, "must-not-run", "cancelled-counter")
	firstRequestID := "01K6A000000000000000000051"
	firstFrame := mustCommandFrameFor(t, firstRequestID, first)
	firstCall := make(chan httpCallResult, 1)
	go func() {
		response, err := postFrameRequest(client, exchangePath, firstFrame)
		firstCall <- httpCallResult{response: response, err: err}
	}()
	select {
	case <-firstStarted:
	case <-time.After(5 * time.Second):
		t.Fatal("first same-domain mutation did not enter its Handler")
	}

	cancelRequestID := "01K6A000000000000000000052"
	cancelFrame := mustCommandFrameFor(t, cancelRequestID, cancelled)
	call, requestWriter, writeDone, err := postOpenFrameRequest(client, exchangePath, cancelFrame)
	if err != nil {
		t.Fatalf("start queued cancellable exchange: %v", err)
	}
	defer func() { _ = requestWriter.Close() }()
	if err := <-writeDone; err != nil {
		t.Fatalf("write queued command: %v", err)
	}
	waitForLaneOccupancy(t, server.executionLanes, domain, 1, 1)
	cancelRecord, err := encodeFrame(frameRecord{
		requestID: cancelRequestID,
		sequence:  1,
		kind:      "control.cancel",
		payload:   []byte{0xa0},
	}, maxFrameBodyLimit)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := requestWriter.Write(cancelRecord); err != nil {
		t.Fatalf("send control.cancel before lane dispatch: %v", err)
	}
	select {
	case response := <-call:
		if response.err != nil || response.response == nil {
			t.Fatalf("pre-dispatch cancellation response = %v, err %v", response.response, response.err)
		}
		frames := readResponseFrames(t, response.response)
		if len(frames) != 1 || frames[0].requestID != cancelRequestID || frames[0].sequence != 0 || frames[0].kind != "problem" {
			t.Fatalf("pre-dispatch cancel frames = %+v, want one correlated problem", frames)
		}
		code, err := problemCodeFromPayload(frames[0].payload)
		if err != nil || code != "transport.cancelled-before-submission" {
			t.Fatalf("pre-dispatch cancel code = %q, err %v; want transport.cancelled-before-submission", code, err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("queued control.cancel did not receive a no-effect response")
	}
	if got := handlerCalls.Load(); got != 1 {
		t.Fatalf("Handlers called after queued cancellation = %d, want only the active mutation", got)
	}
	assertFixtureCounter(t, daemon.fixture, "cancelled-counter", "0")

	close(releaseFirst)
	select {
	case response := <-firstCall:
		assertHTTPM1Response(t, response.response, response.err, firstRequestID, operation.Result{
			Code: operation.ResultSucceeded,
			Output: operation.MatterCreateOutput{
				ID: "01M4F1XT4R3E00000000000001", Locator: "held-counter", Title: "1",
			},
		})
	case <-time.After(5 * time.Second):
		t.Fatal("active mutation did not complete after release")
	}
	if got := handlerCalls.Load(); got != 1 {
		t.Fatalf("Handlers called after active mutation completed = %d, want cancelled request never dispatched", got)
	}
	assertFixtureCounter(t, daemon.fixture, "held-counter", "1")
	waitForExecutionLanesEmpty(t, server.executionLanes)
}

func TestDisconnectWhileQueuedCancelsBeforeLaneRelease(t *testing.T) {
	registry := operation.NewRegistry()
	var daemon *Daemon
	var handlerCalls atomic.Int32
	firstStarted := make(chan struct{}, 1)
	releaseFirst := make(chan struct{})
	if err := registry.Register(operation.MatterCreateV1, func(ctx context.Context, request operation.Request) operation.Result {
		handlerCalls.Add(1)
		input := request.Input.(operation.MatterCreateInput)
		if input.Locator == "held-counter" {
			firstStarted <- struct{}{}
			select {
			case <-releaseFirst:
			case <-ctx.Done():
				return executionFailed(ctx.Err())
			}
		}
		return incrementFixtureCounter(ctx, daemon.fixture, request)
	}); err != nil {
		t.Fatal(err)
	}
	server := newServer(registry, 2)
	client, _ := startHTTP2UnixServerWithSetup(t, server, func(started *Daemon) { daemon = started })
	negotiateHTTP2TestSession(t, client)
	seedFixtureCounter(t, daemon.fixture, "held-counter")
	seedFixtureCounter(t, daemon.fixture, "cancelled-counter")

	domain := "01K5V8K1Q5VX6Y0J8C9W3M4N5P"
	first := fixtureCommand(t, domain, "01K6A000000000000000000071", 71, "hold-first", "held-counter")
	queued := fixtureCommand(t, domain, "01K6A000000000000000000072", 72, "must-not-run", "cancelled-counter")
	firstRequestID := "01K6A000000000000000000073"
	firstFrame := mustCommandFrameFor(t, firstRequestID, first)
	firstCall := make(chan httpCallResult, 1)
	go func() {
		response, err := postFrameRequest(client, exchangePath, firstFrame)
		firstCall <- httpCallResult{response: response, err: err}
	}()
	select {
	case <-firstStarted:
	case <-time.After(5 * time.Second):
		t.Fatal("first mutation did not acquire its domain lane")
	}

	queuedRequestID := "01K6A000000000000000000074"
	queuedFrame := mustCommandFrameFor(t, queuedRequestID, queued)
	requestContext, cancelRequest := context.WithCancel(context.Background())
	defer cancelRequest()
	reader, writer := io.Pipe()
	defer func() { _ = writer.Close() }()
	request, err := http.NewRequestWithContext(requestContext, http.MethodPost, "http://wipd"+exchangePath, reader)
	if err != nil {
		t.Fatalf("create cancellable queued request: %v", err)
	}
	request.Header.Set("Content-Type", "application/octet-stream")
	queuedCall := make(chan httpCallResult, 1)
	go func() {
		response, err := client.Do(request)
		queuedCall <- httpCallResult{response: response, err: err}
	}()
	writeDone := make(chan error, 1)
	go func() {
		_, err := writer.Write(queuedFrame)
		writeDone <- err
	}()
	if err := <-writeDone; err != nil {
		t.Fatalf("write queued command frame: %v", err)
	}
	waitForLaneOccupancy(t, server.executionLanes, domain, 1, 1)

	// Cancel B while A still holds the lane. The queued acquisition must leave
	// immediately from request-context cancellation, before A is released.
	cancelRequest()
	_ = writer.Close()
	select {
	case result := <-queuedCall:
		if result.response != nil {
			_ = result.response.Body.Close()
		}
		if result.err == nil {
			t.Fatal("canceled queued exchange unexpectedly received a response")
		}
	case <-time.After(5 * time.Second):
		t.Fatal("canceled queued exchange did not reset")
	}
	waitForLaneOccupancy(t, server.executionLanes, domain, 1, 0)
	if got := handlerCalls.Load(); got != 1 {
		t.Fatalf("Handler calls before releasing domain lane = %d, want only active A", got)
	}

	close(releaseFirst)
	select {
	case result := <-firstCall:
		assertHTTPM1Response(t, result.response, result.err, firstRequestID, operation.Result{
			Code: operation.ResultSucceeded,
			Output: operation.MatterCreateOutput{
				ID: "01M4F1XT4R3E00000000000001", Locator: "held-counter", Title: "1",
			},
		})
	case <-time.After(5 * time.Second):
		t.Fatal("active mutation did not complete after lane release")
	}
	if got := handlerCalls.Load(); got != 1 {
		t.Fatalf("queued Handler ran after its request was reset: total calls %d, want 1", got)
	}
	assertFixtureCounter(t, daemon.fixture, "held-counter", "1")
	assertFixtureCounter(t, daemon.fixture, "cancelled-counter", "0")
	waitForExecutionLanesEmpty(t, server.executionLanes)
	waitForExchangeSlots(t, server, 0)
}

func TestDispatchGateRejectsCanceledRequestAtBegin(t *testing.T) {
	requestContext, cancelRequest := context.WithCancel(context.Background())
	cancelRequest()
	gate := &dispatchGate{cancel: func() {}}
	if gate.begin(requestContext) {
		t.Fatal("dispatch gate began after request cancellation")
	}
	if gate.dispatched {
		t.Fatal("dispatch gate marked canceled request as dispatched")
	}
}

func TestDisconnectAfterFixtureCommitReturnsOutcomeUnknownAndKeepsValue(t *testing.T) {
	registry := operation.NewRegistry()
	var daemon *Daemon
	committed := make(chan struct{}, 1)
	releaseHandler := make(chan struct{})
	handlerDone := make(chan error, 1)
	wantRecord := wipdfixture.Record{ID: "fixture-disconnect-record", Key: "disconnect-key", Value: "committed-before-disconnect"}
	if err := registry.Register(operation.MatterCreateV1, func(ctx context.Context, _ operation.Request) operation.Result {
		if err := daemon.fixture.Put(ctx, wantRecord); err != nil {
			handlerDone <- err
			return executionFailed(err)
		}
		committed <- struct{}{}
		select {
		case <-releaseHandler:
		case <-ctx.Done():
			handlerDone <- ctx.Err()
			return executionFailed(ctx.Err())
		}
		handlerDone <- nil
		return operation.Result{Code: operation.ResultSucceeded, Output: operation.MatterCreateOutput{
			ID: "01M4F1XT4R3E00000000000001", Locator: "disconnect-key", Title: "Committed",
		}}
	}); err != nil {
		t.Fatal(err)
	}
	server := newServer(registry, 1)
	root, _ := startLocalIPCServerWithSetup(t, server, func(started *Daemon) { daemon = started })
	client, err := Connect(context.Background(), root)
	if err != nil {
		t.Fatalf("connect local test client: %v", err)
	}
	defer func() { _ = client.Close() }()
	command := fixtureCommand(t, "01K5V8K1Q5VX6Y0J8C9W3M4N5P", "01K6A000000000000000000061", 61, "disconnect", "disconnect-key")
	requestContext, cancel := context.WithCancel(context.Background())
	completed := make(chan struct {
		result operation.Result
		err    error
	}, 1)
	go func() {
		result, err := client.ExecuteCommand(requestContext, command)
		completed <- struct {
			result operation.Result
			err    error
		}{result: result, err: err}
	}()
	select {
	case <-committed:
	case <-time.After(5 * time.Second):
		cancel()
		t.Fatal("fixture Handler did not durably commit before disconnect")
	}
	cancel()
	select {
	case outcome := <-completed:
		var exchangeError *ExchangeError
		if !errors.As(outcome.err, &exchangeError) || exchangeError.Code != "transport.outcome-unknown" ||
			!exchangeError.Uncertain || !errors.Is(outcome.err, ErrOutcomeUnknown) {
			t.Fatalf("post-commit disconnect result=%+v error=%v; want uncertain transport.outcome-unknown", outcome.result, outcome.err)
		}
		if outcome.result.Code != "" {
			t.Fatalf("post-commit disconnect invented semantic result %q; want transport uncertainty only", outcome.result.Code)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("client did not report outcome-unknown after disconnect")
	}
	got, err := daemon.fixture.Get(context.Background(), wantRecord.Key)
	if err != nil || got != wantRecord {
		t.Fatalf("fixture value after response loss = %+v, err %v; want durable committed value %+v", got, err, wantRecord)
	}
	waitForLaneOccupancy(t, server.executionLanes, command.AuthorityDomainID, 1, 0)
	waitForExchangeSlots(t, server, 1)

	close(releaseHandler)
	select {
	case err := <-handlerDone:
		if err != nil {
			t.Fatalf("Handler completion after client disconnect: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("Handler did not complete after response loss")
	}
	waitForExchangeSlots(t, server, 0)
}

func TestStalledPartialFrameDoesNotStarveValidExchange(t *testing.T) {
	registry := operation.NewRegistry()
	var handlerCalls atomic.Int32
	handlerResults := make(chan operation.Result, 1)
	if err := registry.Register(operation.MatterCreateV1, func(context.Context, operation.Request) operation.Result {
		handlerCalls.Add(1)
		result := operation.Result{Code: operation.ResultSucceeded, Output: operation.MatterCreateOutput{ID: "01M4F1XT4R3E00000000000001", Locator: "fixture-17", Title: "M4 Fixture"}}
		handlerResults <- result
		return result
	}); err != nil {
		t.Fatal(err)
	}
	server := newServer(registry, 1)
	client, _ := startHTTP2UnixServer(t, server)
	negotiateHTTP2TestSession(t, client)

	stalledCall, stalledWriter, stalledWriteDone, err := postOpenFrameRequest(client, exchangePath, stalledFramePrefix(64))
	if err != nil {
		t.Fatalf("start stalled partial frame: %v", err)
	}
	defer func() { _ = stalledWriter.Close() }()
	if err := <-stalledWriteDone; err != nil {
		t.Fatalf("write stalled frame prefix: %v", err)
	}
	waitForChannelLength(t, server.preflightSlots, 1)

	command, hash := canonicalFixtureCommand(t)
	requestID := "01K6A000000000000000000020"
	frame := mustCommandFrameWithDeadline(t, requestID, command, hash, "2020-01-01T00:00:00Z")
	response, err := postFrameRequest(client, exchangePath, frame)
	if err != nil {
		t.Fatalf("valid request alongside stalled partial frame: %v", err)
	}
	frames := readResponseFrames(t, response)
	if len(frames) != 1 || frames[0].requestID != requestID || frames[0].sequence != 0 || frames[0].kind != "problem" {
		t.Fatalf("deadline response beside stalled frame = %+v, want correlated problem", frames)
	}
	problem, err := decodePayload(frames[0].payload)
	if err != nil || problem != "transport.deadline-before-submission" {
		t.Fatalf("valid request response = %#v, err %v; want transport.deadline-before-submission", problem, err)
	}
	if active := len(server.preflightSlots); active != 1 {
		t.Fatalf("preflight slots after unrelated response = %d, want stalled parser to retain only its own slot", active)
	}
	if got := handlerCalls.Load(); got != 0 {
		t.Fatalf("Handler ran %d times for the expired request, want no dispatch", got)
	}

	successID := "01K6A000000000000000000022"
	response, err = postFrameRequest(client, exchangePath, mustCommandFrame(t, successID, command, hash))
	assertHTTPM1Response(t, response, err, successID, operation.Result{
		Code:   operation.ResultSucceeded,
		Output: operation.MatterCreateOutput{ID: "01M4F1XT4R3E00000000000001", Locator: "fixture-17", Title: "M4 Fixture"},
	})
	assertTypedHandlerResult(t, <-handlerResults, operation.ResultSucceeded, operation.MatterCreateOutput{
		ID: "01M4F1XT4R3E00000000000001", Locator: "fixture-17", Title: "M4 Fixture",
	})
	if active := len(server.preflightSlots); active != 1 {
		t.Fatalf("preflight slots after successful dispatch = %d, want stalled parser to retain only its own slot", active)
	}

	if err := stalledWriter.Close(); err != nil {
		t.Fatalf("close stalled partial request: %v", err)
	}
	select {
	case result := <-stalledCall:
		assertStreamReset(t, result)
	case <-time.After(5 * time.Second):
		t.Fatal("stalled partial exchange did not release after its body closed")
	}
	waitForChannelLength(t, server.preflightSlots, 0)
}

func TestPreflightAdmissionIsBoundedAndFailsFast(t *testing.T) {
	server := newServer(operation.NewRegistry(), 1)
	client, _ := startHTTP2UnixServer(t, server)
	negotiateHTTP2TestSession(t, client)

	var stalledWriters []*io.PipeWriter
	var stalledCalls []<-chan httpCallResult
	t.Cleanup(func() {
		for _, writer := range stalledWriters {
			_ = writer.Close()
		}
	})
	for range 2 {
		call, writer, writeDone, err := postOpenFrameRequest(client, exchangePath, stalledFramePrefix(64))
		if err != nil {
			t.Fatalf("start stalled preflight request: %v", err)
		}
		stalledWriters = append(stalledWriters, writer)
		stalledCalls = append(stalledCalls, call)
		if err := <-writeDone; err != nil {
			t.Fatalf("write stalled request prefix: %v", err)
		}
	}
	waitForChannelLength(t, server.preflightSlots, cap(server.preflightSlots))

	command, hash := canonicalFixtureCommand(t)
	thirdFrame := mustCommandFrame(t, "01K6A000000000000000000021", command, hash)
	started := time.Now()
	thirdCall := make(chan httpCallResult, 1)
	go func() {
		response, err := postFrameRequest(client, exchangePath, thirdFrame)
		thirdCall <- httpCallResult{response: response, err: err}
	}()
	select {
	case result := <-thirdCall:
		if result.response != nil {
			_ = result.response.Body.Close()
		}
		if result.err == nil {
			t.Fatal("request beyond bounded preflight capacity was not rejected")
		}
		if elapsed := time.Since(started); elapsed >= time.Second {
			t.Fatalf("request beyond bounded preflight capacity took %s to reject, want fail-fast", elapsed)
		}
	case <-time.After(time.Second):
		t.Fatal("request beyond preflight capacity waited instead of failing fast")
	}
	if got, limit := len(server.preflightSlots), cap(server.preflightSlots); got != limit {
		t.Fatalf("preflight occupancy = %d, capacity = %d after overflow request; waiting requests must not accumulate", got, limit)
	}

	for _, writer := range stalledWriters {
		_ = writer.Close()
	}
	for _, call := range stalledCalls {
		select {
		case result := <-call:
			assertStreamReset(t, result)
		case <-time.After(5 * time.Second):
			t.Fatal("stalled parser did not release after its body closed")
		}
	}
	waitForChannelLength(t, server.preflightSlots, 0)
}

func TestControlCancelStopsWaitingWithoutCancellingOrRollingBackHandler(t *testing.T) {
	registry := operation.NewRegistry()
	var daemon *Daemon
	handlerStarted := make(chan struct{}, 1)
	subsequentAdmission := make(chan struct{}, 1)
	var handlerCalls atomic.Int32
	releaseHandler := make(chan struct{})
	handlerFinished := make(chan error, 1)
	wantRecord := wipdfixture.Record{ID: "cancelled-wait-handler-record", Key: "cancel-wait-key", Value: "persisted-after-cancel"}
	if err := registry.Register(operation.MatterCreateV1, func(ctx context.Context, _ operation.Request) operation.Result {
		if handlerCalls.Add(1) > 1 {
			subsequentAdmission <- struct{}{}
			return operation.Result{Code: operation.ResultSucceeded, Output: operation.MatterCreateOutput{ID: "01M4F1XT4R3E00000000000001", Locator: "fixture-17", Title: "M4 Fixture"}}
		}
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
		return operation.Result{Code: operation.ResultSucceeded, Output: operation.MatterCreateOutput{ID: "01M4F1XT4R3E00000000000001", Locator: "fixture-17", Title: "M4 Fixture"}}
	}); err != nil {
		t.Fatal(err)
	}
	server := newServer(registry, 1)
	client, _ := startHTTP2UnixServerWithSetup(t, server, func(started *Daemon) { daemon = started })
	negotiateHTTP2TestSession(t, client)
	command, hash := canonicalFixtureCommand(t)
	requestID := "01K6A000000000000000000013"
	commandFrame := mustCommandFrame(t, requestID, command, hash)
	cancelFrame, err := encodeFrame(frameRecord{requestID: requestID, sequence: 1, kind: "control.cancel", payload: []byte{0xa0}}, maxFrameBodyLimit)
	if err != nil {
		t.Fatal(err)
	}
	call, requestWriter, writeDone, err := postOpenFrameRequest(client, exchangePath, commandFrame)
	if err != nil {
		t.Fatalf("start cancellable wait: %v", err)
	}
	defer func() { _ = requestWriter.Close() }()
	select {
	case <-handlerStarted:
	case <-time.After(5 * time.Second):
		t.Fatal("valid request did not enter the pending Handler")
	}
	if err := <-writeDone; err != nil {
		t.Fatalf("write command frame: %v", err)
	}

	time.Sleep(50 * time.Millisecond)
	if _, err := requestWriter.Write(cancelFrame); err != nil {
		t.Fatalf("write control.cancel while Handler is pending: %v", err)
	}
	select {
	case result := <-call:
		assertStreamReset(t, result)
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

	const retryInterval = time.Millisecond
	releaseBy := time.Now().Add(5 * time.Second)
	releaseDeadline := time.NewTimer(time.Until(releaseBy))
	defer releaseDeadline.Stop()
	retryDelay := time.NewTimer(0)
	defer retryDelay.Stop()
	admitted := false
	for attempt := 0; attempt < 77; attempt++ {
		select {
		case <-releaseDeadline.C:
			t.Fatal("exchange slot was not released in time for a subsequent request")
		case <-retryDelay.C:
		}

		nextRequestID := fmt.Sprintf("%s%02d", requestID[:len(requestID)-2], 23+attempt)
		attemptAdmitted := false
		func() {
			requestContext, cancel := context.WithDeadline(context.Background(), releaseBy)
			defer cancel()
			request, err := http.NewRequestWithContext(requestContext, http.MethodPost, "http://wipd"+exchangePath,
				bytes.NewReader(mustCommandFrame(t, nextRequestID, command, hash)))
			if err != nil {
				t.Fatalf("create subsequent exchange request: %v", err)
			}
			request.Header.Set("Content-Type", "application/octet-stream")
			response, err := client.Do(request)
			select {
			case <-subsequentAdmission:
				assertHTTPM1Response(t, response, err, nextRequestID, operation.Result{
					Code:   operation.ResultSucceeded,
					Output: operation.MatterCreateOutput{ID: "01M4F1XT4R3E00000000000001", Locator: "fixture-17", Title: "M4 Fixture"},
				})
				attemptAdmitted = true
				return
			default:
			}
			if err != nil || response == nil {
				t.Fatalf("subsequent request failed before admission: response=%v err=%v", response, err)
			}
			frames := readResponseFrames(t, response)
			if len(frames) != 1 || frames[0].requestID != nextRequestID || frames[0].sequence != 0 || frames[0].kind != "problem" {
				t.Fatalf("response while waiting for slot release = %+v, want correlated overload problem", frames)
			}
			problem, err := decodePayload(frames[0].payload)
			if err != nil || problem != "transport.overloaded" {
				t.Fatalf("response while waiting for slot release = %#v, err %v; want transport.overloaded", problem, err)
			}
		}()
		if attemptAdmitted {
			admitted = true
			break
		}
		retryDelay.Reset(retryInterval)
	}
	if !admitted {
		t.Fatal("subsequent request was not admitted after the Handler released its slot")
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
	return postOpenFrameRequestWithLength(client, path, body, -1)
}

func postOpenFrameRequestWithLength(client *http.Client, path string, body []byte, contentLength int64) (<-chan httpCallResult, *io.PipeWriter, <-chan error, error) {
	reader, writer := io.Pipe()
	request, err := http.NewRequest(http.MethodPost, "http://wipd"+path, reader)
	if err != nil {
		_ = reader.Close()
		_ = writer.Close()
		return nil, nil, nil, err
	}
	request.ContentLength = contentLength
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
	return mustCommandFrameWithDeadline(t, requestID, canonicalCommand, hash, nil)
}

func mustCommandFrameWithDeadline(t *testing.T, requestID string, canonicalCommand []byte, hash string, deadline any) []byte {
	t.Helper()
	payload, err := encodePayload(map[string]any{
		"schema":            "wipd.command-submit/1",
		"canonical_command": canonicalCommand,
		"request_hash":      hash,
		"deadline":          deadline,
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

func fixtureCommand(t *testing.T, domainID, commandID string, sequence uint64, title, locator string) operation.Command {
	t.Helper()
	canonical, _ := canonicalGoldenCommand(t)
	command, err := operation.DecodeCanonicalCommand(canonical)
	if err != nil {
		t.Fatalf("decode canonical command for synthetic fixture key: %v", err)
	}
	command.AuthorityDomainID = domainID
	command.ID = commandID
	command.CorrelationCommandID = commandID
	command.EnvironmentSequence = sequence
	command.Request.Input = operation.MatterCreateInput{Title: title, Locator: locator}
	if _, err := command.CanonicalBytes(); err != nil {
		t.Fatalf("validate synthetic fixture command: %v", err)
	}
	return command
}

func mustCommandFrameFor(t *testing.T, requestID string, command operation.Command) []byte {
	t.Helper()
	canonical, err := command.CanonicalBytes()
	if err != nil {
		t.Fatalf("encode command frame identity: %v", err)
	}
	hash, err := command.RequestHash()
	if err != nil {
		t.Fatalf("hash command frame identity: %v", err)
	}
	return mustCommandFrame(t, requestID, canonical, hash)
}

func seedFixtureCounter(t *testing.T, fixture *wipdfixture.Store, key string) {
	t.Helper()
	if err := fixture.Put(context.Background(), wipdfixture.Record{ID: "fixture-counter-" + key, Key: key, Value: "0"}); err != nil {
		t.Fatalf("seed synthetic fixture counter %q: %v", key, err)
	}
}

func incrementFixtureCounter(ctx context.Context, fixture *wipdfixture.Store, request operation.Request) operation.Result {
	input := request.Input.(operation.MatterCreateInput)
	record, err := fixture.Get(ctx, input.Locator)
	if err != nil {
		return executionFailed(err)
	}
	value, err := strconv.Atoi(record.Value)
	if err != nil {
		return executionFailed(err)
	}
	record.Value = strconv.Itoa(value + 1)
	if err := fixture.Put(ctx, record); err != nil {
		return executionFailed(err)
	}
	return operation.Result{Code: operation.ResultSucceeded, Output: operation.MatterCreateOutput{
		ID: "01M4F1XT4R3E00000000000001", Locator: input.Locator, Title: record.Value,
	}}
}

func executionFailed(err error) operation.Result {
	return operation.Result{Code: operation.ResultFailed, Problem: &operation.Problem{
		Code: operation.ProblemExecutionFailed, Message: err.Error(),
	}}
}

func assertFixtureCounter(t *testing.T, fixture *wipdfixture.Store, key, want string) {
	t.Helper()
	record, err := fixture.Get(context.Background(), key)
	if err != nil || record.Value != want {
		t.Fatalf("synthetic fixture counter %q = %+v, err %v; want value %q", key, record, err, want)
	}
}

func waitForLaneOccupancy(t *testing.T, lanes *executionLanes, key string, wantActive, wantWaiting int) {
	t.Helper()
	ticker := time.NewTicker(time.Millisecond)
	defer ticker.Stop()
	timer := time.NewTimer(5 * time.Second)
	defer timer.Stop()
	for {
		active, waiting := lanes.occupancy(key)
		if active == wantActive && waiting == wantWaiting {
			return
		}
		select {
		case <-ticker.C:
		case <-timer.C:
			t.Fatalf("execution lane %q occupancy = active %d, waiting %d; want active %d, waiting %d", key, active, waiting, wantActive, wantWaiting)
		}
	}
}

func waitForExecutionLanesEmpty(t *testing.T, lanes *executionLanes) {
	t.Helper()
	ticker := time.NewTicker(time.Millisecond)
	defer ticker.Stop()
	timer := time.NewTimer(5 * time.Second)
	defer timer.Stop()
	for {
		lanes.mu.Lock()
		count := len(lanes.byKey)
		lanes.mu.Unlock()
		if count == 0 {
			return
		}
		select {
		case <-ticker.C:
		case <-timer.C:
			t.Fatalf("execution lane table retained %d entries after all exchanges completed", count)
		}
	}
}

func waitForExchangeSlots(t *testing.T, server *Server, want int) {
	t.Helper()
	ticker := time.NewTicker(time.Millisecond)
	defer ticker.Stop()
	timer := time.NewTimer(5 * time.Second)
	defer timer.Stop()
	for {
		if active := len(server.exchangeSlots); active == want {
			return
		}
		select {
		case <-ticker.C:
		case <-timer.C:
			t.Fatalf("active exchange slots = %d, want %d", len(server.exchangeSlots), want)
		}
	}
}

func assertStreamReset(t *testing.T, result httpCallResult) {
	t.Helper()
	if result.response != nil {
		body, err := io.ReadAll(result.response.Body)
		_ = result.response.Body.Close()
		if err == nil {
			t.Fatalf("exchange returned a complete HTTP response instead of resetting: HTTP/%d.%d %s body=%x", result.response.ProtoMajor, result.response.ProtoMinor, result.response.Status, body)
		}
		return
	}
	if result.err == nil {
		t.Fatal("exchange returned neither an HTTP/2 stream reset nor an error")
	}
}

func assertCallM1Response(t *testing.T, call httpCallResult, requestID string, want operation.Result) {
	t.Helper()
	if call.err != nil || call.response == nil {
		t.Fatalf("exchange response = %v, err %v; want a framed M1 response", call.response, call.err)
	}
	assertHTTPM1Response(t, call.response, nil, requestID, want)
}

func assertHTTPM1Response(t *testing.T, response *http.Response, responseErr error, requestID string, want operation.Result) {
	t.Helper()
	if responseErr != nil || response == nil {
		t.Fatalf("exchange response = %v, err %v; want a framed M1 response", response, responseErr)
	}
	frames := readResponseFrames(t, response)
	if len(frames) != 1 || frames[0].requestID != requestID || frames[0].sequence != 0 || frames[0].kind != "response.end" {
		t.Fatalf("M1 response frames = %+v, want one correlated response.end", frames)
	}
	got, err := decodeM1ResultPayload(frames[0].payload)
	if err != nil {
		t.Fatalf("decode bare M1 response result: %v", err)
	}
	assertSemanticResultsEqual(t, got, want)
}

func assertSemanticResultsEqual(t *testing.T, got, want operation.Result) {
	t.Helper()
	if got.Code != want.Code || !reflect.DeepEqual(got.Output, want.Output) {
		t.Fatalf("M1 result = %+v, want disposition/output %+v", got, want)
	}
	if (got.Problem == nil) != (want.Problem == nil) {
		t.Fatalf("M1 result problem presence = %+v, want %+v", got.Problem, want.Problem)
	}
	if got.Problem != nil && got.Problem.Code != want.Problem.Code {
		t.Fatalf("M1 result problem code = %q, want %q", got.Problem.Code, want.Problem.Code)
	}
}

func assertTypedHandlerResult(t *testing.T, result operation.Result, code operation.ResultCode, wantOutput operation.Output) {
	t.Helper()
	if result.Code != code || result.Problem != nil {
		t.Fatalf("test-only Handler result = %+v, want %s without problem", result, code)
	}
	if !reflect.DeepEqual(result.Output, wantOutput) {
		t.Fatalf("test-only typed Handler output = %#v, want asymmetric value %#v", result.Output, wantOutput)
	}
}

func stalledFramePrefix(bodyLength uint32) []byte {
	prefix := make([]byte, 4)
	binary.BigEndian.PutUint32(prefix, bodyLength)
	return prefix
}

func waitForChannelLength(t *testing.T, channel chan struct{}, want int) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for len(channel) != want && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	if got := len(channel); got != want {
		t.Fatalf("channel occupancy = %d, want %d", got, want)
	}
}

func readResponseFrames(t *testing.T, response *http.Response) []frameRecord {
	t.Helper()
	defer func() { _ = response.Body.Close() }()
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

func submitConnectedCommand(t *testing.T, client *http.Client, command operation.Command, requestID string) frameRecord {
	t.Helper()
	canonical, err := command.CanonicalBytes()
	if err != nil {
		t.Fatal(err)
	}
	hash, err := command.RequestHash()
	if err != nil {
		t.Fatal(err)
	}
	wire := mustCommandFrame(t, requestID, canonical, hash)
	response, err := postFrameRequest(client, exchangePath, wire)
	if err != nil {
		t.Fatalf("submit connected %s command: %v", command.Request.Operation, err)
	}
	frames := readResponseFrames(t, response)
	if len(frames) != 1 || frames[0].requestID != requestID || frames[0].sequence != 0 {
		t.Fatalf("connected command response frames=%+v", frames)
	}
	return frames[0]
}

func journalReceipt(t *testing.T, journal *wipdjournal.Journal, commandID string) []byte {
	t.Helper()
	installed, err := journal.InstallSnapshot(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	receipt, ok := installed.Receipts[commandID]
	if !ok {
		t.Fatalf("journal has no terminal receipt for %s", commandID)
	}
	return append([]byte(nil), receipt.CanonicalReceipt...)
}

func m2ProblemCode(t *testing.T, payload []byte) string {
	t.Helper()
	code, err := problemCodeFromPayload(payload)
	if err != nil {
		t.Fatal(err)
	}
	return code
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
