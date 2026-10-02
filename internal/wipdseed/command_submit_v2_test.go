package wipdseed

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/procrastivity/wip/internal/operation"
	"github.com/procrastivity/wip/internal/wipdauthority"
	"github.com/procrastivity/wip/internal/wipdwire"
)

type submitV2RoundTrip func(*http.Request) (*http.Response, error)

func (roundTrip submitV2RoundTrip) RoundTrip(request *http.Request) (*http.Response, error) {
	return roundTrip(request)
}

func TestCommandSubmitV2RemoteSelectionIsIndependentAndClosed(t *testing.T) {
	for _, test := range []struct {
		name      string
		features  []any
		valid, v2 bool
	}{
		{"v1-peer", []any{"wipd.frame/1"}, true, false},
		{"v2-peer", []any{wipdwire.CommandSubmitV2Feature, "wipd.frame/1"}, true, true},
		{"v2-without-frame", []any{wipdwire.CommandSubmitV2Feature}, false, false},
		{"unsorted", []any{"wipd.frame/1", wipdwire.CommandSubmitV2Feature}, false, false},
		{"duplicate", []any{wipdwire.CommandSubmitV2Feature, wipdwire.CommandSubmitV2Feature, "wipd.frame/1"}, false, false},
		{"unknown", []any{"wipd.command-submit/3", "wipd.frame/1"}, false, false},
	} {
		t.Run(test.name, func(t *testing.T) {
			calls := 0
			client := &http.Client{Transport: submitV2RoundTrip(func(request *http.Request) (*http.Response, error) {
				calls++
				frame, err := wipdwire.ReadFrame(request.Body)
				if err != nil || frame.Kind != "client.hello" {
					t.Fatalf("negotiation request: %+v %v", frame, err)
				}
				offer, err := wipdwire.DecodeCanonicalMap(frame.Payload,
					"protocol_min", "protocol_max", "identity_schemas", "operations", "store_schemas", "features")
				if err != nil || !equalStringsValue(offer["features"], wipdwire.CommandSubmitV2Feature, "wipd.frame/1") {
					t.Fatalf("authority hop not offered v2 independently: %+v %v", offer, err)
				}
				hello, _ := wipdwire.EncodeCanonical(map[string]any{
					"selected_protocol": []any{uint64(1), uint64(0)}, "identity_schemas": []any{"wipd.command/1"},
					"operations": offer["operations"], "store_schemas": []any{"wipd.store/1"}, "features": test.features,
				})
				parameters, _ := wipdwire.EncodeCanonical(map[string]any{
					"frame_schema": "wipd.frame/1", "max_frame_body": uint64(wipdwire.FrameLimit), "max_chunk_data": uint64(65_536),
					"max_stream_bytes": uint64(maxClientTransferBytes), "max_concurrent_exchanges": uint64(32), "receive_window_bytes": uint64(1_048_576),
				})
				return submitV2Response(t, []wipdwire.Frame{
					{RequestID: frame.RequestID, Kind: "server.hello", Payload: hello},
					{RequestID: frame.RequestID, Sequence: 1, Kind: "session.parameters", Payload: parameters},
				}), nil
			})}
			limits, err := negotiateRemoteOperations(context.Background(), client, "https://authority.example:8443",
				[]operation.ID{operation.MatterCreateV1.Metadata().Operation})
			if (err == nil) != test.valid || limits.commandSubmitV2 != test.v2 || calls != 1 {
				t.Fatalf("selection=%+v err=%v calls=%d", limits, err, calls)
			}
		})
	}
}

func TestRepairRemoteOfferIsOptionalOnlyWithoutSelectedV2(t *testing.T) {
	operations := []operation.ID{operation.GateExemptionRepairV1.Metadata().Operation, operation.MatterCreateV1.Metadata().Operation}
	for _, tc := range []struct {
		name, features string
		includeRepair  bool
		valid          bool
	}{
		{"v1-omits-repair", "v1", false, true},
		{"v1-includes-repair", "v1", true, false},
		{"v2-includes-repair", "v2", true, true},
		{"v2-omits-repair", "v2", false, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			client := &http.Client{Transport: submitV2RoundTrip(func(request *http.Request) (*http.Response, error) {
				frame, err := wipdwire.ReadFrame(request.Body)
				if err != nil {
					t.Fatal(err)
				}
				offer, err := wipdwire.DecodeCanonicalMap(frame.Payload,
					"protocol_min", "protocol_max", "identity_schemas", "operations", "store_schemas", "features")
				if err != nil {
					t.Fatal(err)
				}
				selected := offer["operations"].([]any)
				if !tc.includeRepair {
					selected = selected[1:]
				}
				features := []any{"wipd.frame/1"}
				if tc.features == "v2" {
					features = []any{wipdwire.CommandSubmitV2Feature, "wipd.frame/1"}
				}
				hello, _ := wipdwire.EncodeCanonical(map[string]any{
					"selected_protocol": []any{uint64(1), uint64(0)}, "identity_schemas": []any{"wipd.command/1"},
					"operations": selected, "store_schemas": []any{"wipd.store/1"}, "features": features,
				})
				parameters, _ := wipdwire.EncodeCanonical(map[string]any{
					"frame_schema": "wipd.frame/1", "max_frame_body": uint64(wipdwire.FrameLimit), "max_chunk_data": uint64(65_536),
					"max_stream_bytes": uint64(maxClientTransferBytes), "max_concurrent_exchanges": uint64(32), "receive_window_bytes": uint64(1_048_576),
				})
				return submitV2Response(t, []wipdwire.Frame{
					{RequestID: frame.RequestID, Kind: "server.hello", Payload: hello},
					{RequestID: frame.RequestID, Sequence: 1, Kind: "session.parameters", Payload: parameters},
				}), nil
			})}
			_, err := negotiateRemoteOperations(context.Background(), client, "https://authority.example:8443", operations)
			if (err == nil) != tc.valid {
				t.Fatalf("repair selection error=%v; valid=%t", err, tc.valid)
			}
		})
	}
}

func TestCommandSubmitV2RemoteProofForwardingAndErrorsNeverDowngrade(t *testing.T) {
	pin := "sha256:" + strings.Repeat("0", 64)
	profile, err := wipdauthority.NewProfile("https://authority.example:8443", testDomainID, 1, pin, pin)
	if err != nil {
		t.Fatal(err)
	}
	proof := []byte{0x00, 0xff, 0x81, 0xc0}
	submit := wipdwire.CommandSubmitV2{
		Schema:           wipdwire.CommandSubmitV2Feature,
		CanonicalCommand: []byte{0xa0}, RequestHash: pin, DetachedProof: proof,
	}
	for _, fault := range []string{"lost-response", "problem", "malformed-response"} {
		t.Run(fault, func(t *testing.T) {
			calls := 0
			client := &CommandExchangeClient{
				profile: profile,
				limits:  sessionLimits{frameBody: wipdwire.FrameLimit, streamBytes: maxClientTransferBytes, commandSubmitV2: true},
			}
			client.client = &http.Client{Transport: submitV2RoundTrip(func(request *http.Request) (*http.Response, error) {
				calls++
				frame, err := wipdwire.ReadFrame(request.Body)
				if err != nil || frame.Kind != "command.submit" {
					t.Fatalf("authority send: %+v %v", frame, err)
				}
				decoded, v2, err := wipdwire.DecodeCommandSubmit(frame.Payload)
				if err != nil || !v2 || !bytes.Equal(decoded.DetachedProof, proof) || !bytes.Equal(decoded.CanonicalCommand, submit.CanonicalCommand) || decoded.RequestHash != pin {
					t.Fatalf("authority proof forwarding: %+v %t %v", decoded, v2, err)
				}
				switch fault {
				case "lost-response":
					return nil, errors.New("lost after authority send")
				case "problem":
					problem, _ := wipdwire.EncodeCanonical(map[string]any{"code": "protocol.unsupported-extension"})
					return submitV2Response(t, []wipdwire.Frame{{RequestID: frame.RequestID, Kind: "problem", Payload: problem}}), nil
				default:
					return &http.Response{StatusCode: http.StatusOK, Header: http.Header{"Content-Type": []string{"application/cbor"}}, Body: io.NopCloser(bytes.NewReader([]byte{0xff}))}, nil
				}
			})}
			if _, err = client.Exchange(context.Background(), "command.submit", submit); err == nil || calls != 1 {
				t.Fatalf("%s caused fallback or success: %v calls=%d", fault, err, calls)
			}
			client.limits.commandSubmitV2 = false
			submit.DetachedProof = nil
			if _, err = client.Exchange(context.Background(), "command.submit", submit); err == nil || calls != 1 {
				t.Fatalf("omitted proof permitted v1-peer retry: %v calls=%d", err, calls)
			}
			submit.DetachedProof = proof
		})
	}
}

func submitV2Response(t *testing.T, frames []wipdwire.Frame) *http.Response {
	t.Helper()
	var body []byte
	for _, frame := range frames {
		encoded, err := wipdwire.EncodeFrame(frame)
		if err != nil {
			t.Fatal(fmt.Errorf("test response frame: %w", err))
		}
		body = append(body, encoded...)
	}
	return &http.Response{
		StatusCode: http.StatusOK, ProtoMajor: 2,
		Header: http.Header{"Content-Type": []string{"application/cbor"}}, Body: io.NopCloser(bytes.NewReader(body)),
	}
}
