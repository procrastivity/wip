package wipd

import (
	"context"
	"errors"
	"io"
	"net/http"
	"testing"

	"github.com/procrastivity/wip/internal/operation"
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
