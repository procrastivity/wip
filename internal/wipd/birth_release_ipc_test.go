package wipd

import (
	"context"
	"errors"
	"io"
	"net/http"
	"sync"
	"testing"
	"time"

	"github.com/procrastivity/wip/internal/operation"
	"github.com/procrastivity/wip/internal/wipdjournal"
)

func TestDefaultDaemonDoesNotAdvertiseOrExecuteBirthClaimRelease(t *testing.T) {
	root, _ := startLocalIPCServer(t, NewServer())
	client, err := Connect(context.Background(), root)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = client.Close() }()
	if !equalStrings(client.hello.features, []string{frameSchema}) {
		t.Fatalf("default daemon features = %v, want only %s", client.hello.features, frameSchema)
	}
	_, err = client.ReleaseBirthClaim(context.Background(), "01KZ7XHAQT1S46NYPN1PW1DX3E",
		"01KZ7XHAQT1S46NYPN1PW1DX4C", operation.Actor("human"))
	var exchangeErr *ExchangeError
	if err == nil || !errors.As(err, &exchangeErr) || exchangeErr.Code != "protocol.unsupported-extension" {
		t.Fatalf("default daemon release attempt error = %v, want unsupported-extension", err)
	}

	requestID, err := newFrameRequestID()
	if err != nil {
		t.Fatal(err)
	}
	payload, err := encodePayload(map[string]any{
		"schema": birthReleaseRequestSchema, "matter_id": "01KZ7XHAQT1S46NYPN1PW1DX3E",
		"command_id": "01KZ7XHAQT1S46NYPN1PW1DX4C", "actor": "human",
	})
	if err != nil {
		t.Fatal(err)
	}
	wire, err := encodeFrame(frameRecord{requestID: requestID, kind: claimReleaseFrameKind, payload: payload}, uint32(client.parameters.maxFrameBody))
	if err != nil {
		t.Fatal(err)
	}
	response, err := client.post(context.Background(), exchangePath, wire)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = response.Body.Close() }()
	if response.ProtoMajor != 2 || response.StatusCode != http.StatusOK {
		t.Fatalf("direct unnegotiated release response = HTTP/%d %d", response.ProtoMajor, response.StatusCode)
	}
	frame, err := readFrame(response.Body, uint32(client.parameters.maxFrameBody))
	if err != nil || frame.requestID != requestID || frame.sequence != 0 || frame.kind != "problem" {
		t.Fatalf("direct unnegotiated release frame = %+v, %v", frame, err)
	}
	code, err := problemCodeFromPayload(frame.payload)
	if err != nil || code != "protocol.unsupported-extension" {
		t.Fatalf("direct unnegotiated release problem = %q, %v", code, err)
	}
	if _, err = readFrame(response.Body, uint32(client.parameters.maxFrameBody)); !errors.Is(err, io.EOF) {
		t.Fatalf("direct unnegotiated release trailing response = %v, want EOF", err)
	}
}

func TestBirthClaimReleaseCancellationBeforeDurableAttempt(t *testing.T) {
	for _, mode := range []string{"control-cancel", "stream-reset"} {
		t.Run(mode, func(t *testing.T) {
			client, server, journal, authority, matterID := configuredBirthReleaseClient(t)
			releaseLane, acquired := server.executionLanes.acquire(context.Background(), commandStartDomainID)
			if !acquired {
				t.Fatal("test did not acquire release-blocking domain lane")
			}
			defer releaseLane()

			releaseID := commandStartCommandPrefix + "98"
			requestID := commandStartCommandPrefix + "99"
			requestFrame := birthReleaseRequestFrame(t, requestID, matterID, releaseID)
			var call <-chan httpCallResult
			var requestWriter *io.PipeWriter
			var writeDone <-chan error
			var cancelRequest context.CancelFunc
			if mode == "stream-reset" {
				requestContext, cancel := context.WithCancel(context.Background())
				cancelRequest = cancel
				call, requestWriter, writeDone = postResettableBirthReleaseRequest(t, client, requestContext, requestFrame)
				defer cancelRequest()
			} else {
				var err error
				call, requestWriter, writeDone, err = postOpenFrameRequest(client.httpClient, exchangePath, requestFrame)
				if err != nil {
					t.Fatal(err)
				}
			}
			defer func() { _ = requestWriter.Close() }()
			if err := <-writeDone; err != nil {
				t.Fatalf("write birth-release request: %v", err)
			}
			waitForLaneOccupancy(t, server.executionLanes, commandStartDomainID, 1, 1)

			if mode == "stream-reset" {
				cancelRequest()
				_ = requestWriter.Close()
				select {
				case result := <-call:
					assertStreamReset(t, result)
				case <-time.After(5 * time.Second):
					t.Fatal("stream reset did not stop pre-submission release wait")
				}
			} else {
				cancelFrame, err := encodeFrame(frameRecord{
					requestID: requestID, sequence: 1, kind: "control.cancel", payload: []byte{0xa0},
				}, maxFrameBodyLimit)
				if err != nil {
					t.Fatal(err)
				}
				if _, err = requestWriter.Write(cancelFrame); err != nil {
					t.Fatalf("send pre-submission control.cancel: %v", err)
				}
				select {
				case result := <-call:
					if result.err != nil || result.response == nil {
						t.Fatalf("pre-submission cancellation response = %v, %v", result.response, result.err)
					}
					frames := readResponseFrames(t, result.response)
					if len(frames) != 1 || frames[0].kind != "problem" || frames[0].requestID != requestID {
						t.Fatalf("pre-submission cancel response frames = %+v", frames)
					}
					code, decodeErr := problemCodeFromPayload(frames[0].payload)
					if decodeErr != nil || code != "transport.cancelled-before-submission" {
						t.Fatalf("pre-submission cancel problem = %q, %v", code, decodeErr)
					}
				case <-time.After(5 * time.Second):
					t.Fatal("control.cancel did not stop pre-submission release wait")
				}
			}

			waitForLaneOccupancy(t, server.executionLanes, commandStartDomainID, 1, 0)
			if _, err := journal.BirthReleaseAttempt(releaseID); !errors.Is(err, wipdjournal.ErrNotFound) {
				t.Fatalf("pre-submission cancellation prepared a release before lane release: %v", err)
			}
			if authority.releaseCalls != 0 || len(authority.acks) != 0 {
				t.Fatalf("pre-submission cancellation performed release work: submits=%d acks=%d", authority.releaseCalls, len(authority.acks))
			}
			releaseLane()
			waitForExchangeSlots(t, server, 0)
			if _, err := journal.BirthReleaseAttempt(releaseID); !errors.Is(err, wipdjournal.ErrNotFound) {
				t.Fatalf("canceled release prepared after lane became available: %v", err)
			}
			if authority.releaseCalls != 0 || len(authority.acks) != 0 {
				t.Fatalf("canceled release continued after lane release: submits=%d acks=%d", authority.releaseCalls, len(authority.acks))
			}
		})
	}
}

func TestBirthClaimReleaseCancellationAfterDurableAttemptContinuesResolution(t *testing.T) {
	client, server, journal, authority, matterID := configuredBirthReleaseClient(t)
	before, err := journal.InstallSnapshot(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	authority.releaseStarted = make(chan struct{}, 1)
	releaseContinue := make(chan struct{})
	authority.releaseContinue = releaseContinue
	var continueOnce sync.Once
	continueRelease := func() { continueOnce.Do(func() { close(releaseContinue) }) }
	defer continueRelease()

	releaseID := commandStartCommandPrefix + "98"
	requestID := commandStartCommandPrefix + "99"
	requestFrame := birthReleaseRequestFrame(t, requestID, matterID, releaseID)
	call, requestWriter, writeDone, err := postOpenFrameRequest(client.httpClient, exchangePath, requestFrame)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = requestWriter.Close() }()
	if err = <-writeDone; err != nil {
		t.Fatalf("write birth-release request: %v", err)
	}
	select {
	case <-authority.releaseStarted:
	case <-time.After(5 * time.Second):
		t.Fatal("release did not reach authority after durable preparation")
	}
	prepared, err := journal.BirthReleaseAttempt(releaseID)
	if err != nil || prepared.Returned || authority.releaseCalls != 1 {
		t.Fatalf("attempt at authority submission = %+v, submits=%d err=%v", prepared, authority.releaseCalls, err)
	}
	cancelFrame, err := encodeFrame(frameRecord{
		requestID: requestID, sequence: 1, kind: "control.cancel", payload: []byte{0xa0},
	}, maxFrameBodyLimit)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = requestWriter.Write(cancelFrame); err != nil {
		t.Fatalf("send post-boundary control.cancel: %v", err)
	}
	select {
	case result := <-call:
		assertStreamReset(t, result)
	case <-time.After(5 * time.Second):
		t.Fatal("post-boundary cancellation did not stop only the response wait")
	}
	stillPrepared, err := journal.BirthReleaseAttempt(releaseID)
	if err != nil || stillPrepared.Returned || authority.releaseCalls != 1 {
		t.Fatalf("post-boundary cancellation changed durable attempt: %+v submits=%d err=%v", stillPrepared, authority.releaseCalls, err)
	}

	continueRelease()
	waitForExchangeSlots(t, server, 0)
	completed, err := journal.BirthReleaseAttempt(releaseID)
	if err != nil || !completed.Returned || completed.ResultCode != operation.ResultSucceeded || len(completed.Receipt) == 0 || authority.releaseCalls != 1 {
		t.Fatalf("post-boundary release recovery = %+v submits=%d err=%v", completed, authority.releaseCalls, err)
	}
	snapshot, err := journal.InstallSnapshot(context.Background())
	if err != nil || snapshot.Anchor.EventCount != before.Anchor.EventCount+2 {
		t.Fatalf("post-boundary release installed tail = %+v, before=%+v, err=%v", snapshot.Anchor, before.Anchor, err)
	}
}

func configuredBirthReleaseClient(t *testing.T) (*Client, *Server, *wipdjournal.Journal, *commandStartFakeAuthority, string) {
	t.Helper()
	coordinator, journal, environment, authority, _, server := newCommandStartFixture(t)
	matter := commandStartCanonicalCommand(commandStartCommandPrefix+"97", 1, "", commandStartCommandPrefix+"97",
		operation.MatterCreateV1.Metadata().Operation, operation.MatterCreateInput{Title: "Birth", Locator: "cancel-boundary"}, nil)
	environment.setCurrentID(matter.ID)
	authority.returnResults = []operation.ResultCode{operation.ResultSucceeded}
	authority.returnContinue = []bool{false}
	result, err := coordinator.RunConnectedCanonicalTerminal(context.Background(), matter,
		func(context.Context, CommandStartSnapshot, operation.Command) error { return nil })
	if err != nil || !result.Returned || result.SemanticResult.Code != operation.ResultSucceeded {
		t.Fatalf("prepare birth for cancellation test: result=%+v err=%v", result, err)
	}
	matterID, ok := result.SemanticResult.Output.(operation.MatterCreateOutput)
	if !ok || matterID.ID == "" {
		t.Fatalf("birth result output = %#v", result.SemanticResult.Output)
	}
	if err = server.ConfigureConnectedCommands(commandStartDomainID, journal, authority, environment); err != nil {
		t.Fatal(err)
	}
	root, _ := startLocalIPCServer(t, server)
	client, err := Connect(context.Background(), root)
	if err != nil {
		t.Fatalf("connect to configured release daemon: %v", err)
	}
	t.Cleanup(func() { _ = client.Close() })
	if !containsString(client.hello.features, birthReleaseFeature) {
		t.Fatal("configured release daemon did not negotiate birth release")
	}
	return client, server, journal, authority, matterID.ID
}

func birthReleaseRequestFrame(t *testing.T, requestID, matterID, commandID string) []byte {
	t.Helper()
	payload, err := encodePayload(map[string]any{
		"schema": birthReleaseRequestSchema, "matter_id": matterID, "command_id": commandID, "actor": "human",
	})
	if err != nil {
		t.Fatal(err)
	}
	frame, err := encodeFrame(frameRecord{requestID: requestID, kind: claimReleaseFrameKind, payload: payload}, maxFrameBodyLimit)
	if err != nil {
		t.Fatal(err)
	}
	return frame
}

func postResettableBirthReleaseRequest(t *testing.T, client *Client, ctx context.Context, frame []byte) (<-chan httpCallResult, *io.PipeWriter, <-chan error) {
	t.Helper()
	reader, writer := io.Pipe()
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, "http://wipd"+exchangePath, reader)
	if err != nil {
		t.Fatal(err)
	}
	request.Header.Set("Content-Type", "application/octet-stream")
	call := make(chan httpCallResult, 1)
	go func() {
		response, err := client.httpClient.Do(request)
		call <- httpCallResult{response: response, err: err}
	}()
	writeDone := make(chan error, 1)
	go func() {
		_, err := writer.Write(frame)
		writeDone <- err
	}()
	return call, writer, writeDone
}
