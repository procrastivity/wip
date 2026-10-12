package wipd

import (
	"context"
	"net/http/httptest"
	"testing"

	"github.com/procrastivity/wip/internal/wipdwire"
)

type countingNamedBatchReader struct{ calls int }

func (reader *countingNamedBatchReader) ReadNamedBatch(context.Context, wipdwire.LocalNamedBatchReadRequest) (wipdwire.NamedBatchReadResponse, error) {
	reader.calls++
	return wipdwire.NamedBatchReadResponse{}, nil
}

func TestNamedBatchReceiverRefusesUnsupportedFeatureBeforeReading(t *testing.T) {
	reader := &countingNamedBatchReader{}
	server := NewServer()
	if err := server.ConfigureNamedBatchRead(reader); err != nil {
		t.Fatal(err)
	}
	payload, err := wipdwire.EncodeCanonical(wipdwire.LocalNamedBatchReadRequest{
		Schema: namedBatchReadRequestSchema,
		Query: wipdwire.NamedBatchReadQuery{
			Name: "batch.read", Version: 1,
			Filter: wipdwire.NamedBatchReadFilter{BatchID: "01M4F1XT4R3E00000000000001"},
		},
		PageSize: 1,
	})
	if err != nil {
		t.Fatal(err)
	}
	request := httptest.NewRequest("POST", exchangePath, nil)
	writer := httptest.NewRecorder()
	server.serveNamedBatchRead(writer, request,
		serverHello{features: []string{frameSchema}}, defaultSessionParameters(defaultExchanges),
		frameRecord{requestID: "01M4F1XT4R3E00000000000002", sequence: 0, kind: namedBatchReadFrameKind, payload: payload})
	response, err := readFrame(writer.Body, bootstrapFrameBodyLimit)
	if err != nil {
		t.Fatal(err)
	}
	code, err := problemCodeFromPayload(response.payload)
	if err != nil || response.kind != "problem" || code != "protocol.unsupported-extension" {
		t.Fatalf("unsupported receiver response = kind %q, code %q, error %v", response.kind, code, err)
	}
	if reader.calls != 0 || writer.Code != 200 {
		t.Fatalf("unsupported profile reached the read handler or mutated HTTP state: calls=%d status=%d", reader.calls, writer.Code)
	}
}
