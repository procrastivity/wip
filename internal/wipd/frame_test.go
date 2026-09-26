package wipd

import (
	"bytes"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"os"
	"testing"

	"github.com/fxamacker/cbor/v2"
)

const step4FrameVectorPath = "../../docs/wipd/frame-security-execution-vectors.json"

type step4FrameVector struct {
	Name         string `json:"name"`
	RequestID    string `json:"request_id"`
	Kind         string `json:"kind"`
	Sequence     uint64 `json:"sequence"`
	PayloadHex   string `json:"payload_hex"`
	FrameBodyHex string `json:"frame_body_hex"`
	WireHex      string `json:"wire_hex"`
}

type step4VectorFile struct {
	FrameVectors []step4FrameVector `json:"frame_vectors"`
}

func TestFrameMatchesExactM2GoldenWireRecord(t *testing.T) {
	vector := loadStep4FrameVector(t)
	wire, err := hex.DecodeString(vector.WireHex)
	if err != nil {
		t.Fatalf("decode Step 4 wire vector: %v", err)
	}
	frame, err := readOneFrame(bytes.NewReader(wire), bootstrapFrameBodyLimit)
	if err != nil {
		t.Fatalf("read exact Step 4 frame: %v", err)
	}
	if frame.requestID != vector.RequestID || frame.sequence != vector.Sequence || frame.kind != vector.Kind {
		t.Fatalf("decoded frame = %+v, want vector request=%s sequence=%d kind=%s", frame, vector.RequestID, vector.Sequence, vector.Kind)
	}
	payload, err := hex.DecodeString(vector.PayloadHex)
	if err != nil {
		t.Fatalf("decode Step 4 payload vector: %v", err)
	}
	if !bytes.Equal(frame.payload, payload) {
		t.Fatal("decoded golden frame payload differs from the published M2 vector")
	}
	encoded, err := encodeFrame(frame, bootstrapFrameBodyLimit)
	if err != nil {
		t.Fatalf("re-encode golden frame: %v", err)
	}
	if !bytes.Equal(encoded, wire) {
		t.Fatalf("encoded frame = %x, want exact M2 golden bytes %x", encoded, wire)
	}
}

func TestFrameParserRejectsMalformedAndNoncanonicalCBOR(t *testing.T) {
	vector := loadStep4FrameVector(t)
	golden, err := hex.DecodeString(vector.WireHex)
	if err != nil {
		t.Fatal(err)
	}
	body := append([]byte(nil), golden[4:]...)
	fields := map[string]any{
		"schema":     frameSchema,
		"request_id": vector.RequestID,
		"sequence":   vector.Sequence,
		"kind":       vector.Kind,
		"payload":    mustHex(t, vector.PayloadHex),
	}

	unknownFields := make(map[string]any, len(fields)+1)
	for key, value := range fields {
		unknownFields[key] = value
	}
	unknownFields["extra"] = true
	unknownBody := mustMarshal(t, unknownFields)
	unknownFrame := framedBody(unknownBody)

	emptyPayloadFields := make(map[string]any, len(fields))
	for key, value := range fields {
		emptyPayloadFields[key] = value
	}
	emptyPayloadFields["payload"] = []byte{}
	emptyPayloadFrame := framedBody(mustMarshal(t, emptyPayloadFields))

	duplicateBody := append([]byte(nil), body...)
	if duplicateBody[0] != 0xa5 {
		t.Fatalf("golden frame map header = 0x%x, want a5", duplicateBody[0])
	}
	duplicateBody[0] = 0xa6
	duplicateBody = append(duplicateBody, 0x64, 'k', 'i', 'n', 'd', 0x6c)
	duplicateBody = append(duplicateBody, []byte("client.hello")...)
	duplicateFrame := framedBody(duplicateBody)

	indefiniteBody := append([]byte(nil), body...)
	indefiniteBody[0] = 0xbf
	indefiniteBody = append(indefiniteBody, 0xff)
	indefiniteFrame := framedBody(indefiniteBody)

	taggedBody := append([]byte{0xc0}, body...)
	taggedFrame := framedBody(taggedBody)

	nonShortestBody := bytes.Replace(body,
		[]byte{0x68, 's', 'e', 'q', 'u', 'e', 'n', 'c', 'e', 0x00},
		[]byte{0x68, 's', 'e', 'q', 'u', 'e', 'n', 'c', 'e', 0x18, 0x00}, 1)
	if bytes.Equal(nonShortestBody, body) {
		t.Fatal("non-shortest test mutation did not find sequence=0")
	}
	nonShortestFrame := framedBody(nonShortestBody)

	// Reordering the deterministic map's encoded key/value pairs yields a
	// structurally valid frame that the canonical round-trip check must reject.
	unsortedBody := reorderFrameMapPairs(t, body)
	unsortedFrame := framedBody(unsortedBody)

	for name, wire := range map[string][]byte{
		"unknown frame field":    unknownFrame,
		"empty payload":          emptyPayloadFrame,
		"duplicate map key":      duplicateFrame,
		"indefinite map":         indefiniteFrame,
		"tagged map":             taggedFrame,
		"non-shortest integer":   nonShortestFrame,
		"noncanonical key order": unsortedFrame,
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := readOneFrame(bytes.NewReader(wire), bootstrapFrameBodyLimit); !errors.Is(err, errInvalidFrame) {
				t.Fatalf("readOneFrame() error = %v, want protocol.invalid-frame", err)
			}
		})
	}
}

func TestFramePayloadClosedSchemaAndCanonicalEncoding(t *testing.T) {
	vector := loadStep4FrameVector(t)
	payload := mustHex(t, vector.PayloadHex)
	if _, err := decodeClientHello(payload); err != nil {
		t.Fatalf("decode published ClientHello: %v", err)
	}
	value, err := decodePayload(payload)
	if err != nil {
		t.Fatal(err)
	}
	fields := value.(map[string]any)
	fields["future_field"] = "not in wipd.protocol/1"
	unknownPayload, err := encodePayload(fields)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := decodeClientHello(unknownPayload); !errors.Is(err, errMalformedMessage) {
		t.Fatalf("decodeClientHello(unknown field) error = %v, want malformed-message", err)
	}

	duplicatePayload := append([]byte(nil), payload...)
	if duplicatePayload[0] != 0xa6 {
		t.Fatalf("golden ClientHello map header = 0x%x, want a6", duplicatePayload[0])
	}
	duplicatePayload[0] = 0xa7
	duplicatePayload = append(duplicatePayload, 0x68)
	duplicatePayload = append(duplicatePayload, []byte("features")...)
	duplicatePayload = append(duplicatePayload, 0x81, 0x6c)
	duplicatePayload = append(duplicatePayload, []byte(frameSchema)...)
	if _, err := decodeClientHello(duplicatePayload); !errors.Is(err, errMalformedMessage) {
		t.Fatalf("decodeClientHello(duplicate key) error = %v, want malformed-message", err)
	}

	decomposedText, err := frameEncoder.Marshal("Cafe\u0301")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := decodePayload(decomposedText); !errors.Is(err, errMalformedMessage) {
		t.Fatalf("decodePayload(non-NFC text) error = %v, want malformed-message", err)
	}

	nonShortest := bytes.Replace(payload,
		[]byte{0x6c, 'p', 'r', 'o', 't', 'o', 'c', 'o', 'l', '_', 'm', 'i', 'n', 0x82, 0x01, 0x00},
		[]byte{0x6c, 'p', 'r', 'o', 't', 'o', 'c', 'o', 'l', '_', 'm', 'i', 'n', 0x82, 0x18, 0x01, 0x00}, 1)
	if bytes.Equal(nonShortest, payload) {
		t.Fatal("payload non-shortest mutation did not find protocol_min")
	}
	if _, err := decodeClientHello(nonShortest); !errors.Is(err, errMalformedMessage) {
		t.Fatalf("decodeClientHello(noncanonical payload) error = %v, want malformed-message", err)
	}

	negativeInteger, err := frameEncoder.Marshal(int64(-25))
	if err != nil {
		t.Fatal(err)
	}
	decodedNegative, err := decodePayload(negativeInteger)
	if err != nil || decodedNegative != int64(-25) {
		t.Fatalf("decodePayload(canonical negative integer) = %#v, %v; want -25", decodedNegative, err)
	}
	floatValue, err := frameEncoder.Marshal(float64(1.5))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := decodePayload(floatValue); !errors.Is(err, errMalformedMessage) {
		t.Fatalf("decodePayload(float) error = %v, want malformed-message", err)
	}
}

func TestFrameLengthPrefixAndBodyTruncation(t *testing.T) {
	tests := []struct {
		name string
		wire []byte
		want error
	}{
		{name: "truncated prefix", wire: []byte{0, 0, 1}, want: errTruncated},
		{name: "truncated body", wire: []byte{0, 0, 0, 3, 0xa0}, want: errTruncated},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if _, err := readFrame(bytes.NewReader(test.wire), bootstrapFrameBodyLimit); !errors.Is(err, test.want) {
				t.Fatalf("readFrame() error = %v, want %v", err, test.want)
			}
		})
	}
}

func TestZeroAndOversizedLengthsAllocateNothingAndReadNoBody(t *testing.T) {
	for name, length := range map[string]uint32{"zero": 0, "oversized": ^uint32(0)} {
		t.Run(name, func(t *testing.T) {
			prefix := make([]byte, 4)
			binary.BigEndian.PutUint32(prefix, length)
			reader := &prefixOnlyReader{prefix: prefix}
			allocations := 0
			_, err := readFrameUsing(reader, maxFrameBodyLimit, func(uint32) []byte {
				allocations++
				return nil
			})
			want := errInvalidFrame
			if length != 0 {
				want = errFrameTooLarge
			}
			if !errors.Is(err, want) {
				t.Fatalf("readFrameUsing() error = %v, want %v", err, want)
			}
			if allocations != 0 || reader.read != len(prefix) {
				t.Fatalf("body allocations/reads = %d/%d, want 0/%d", allocations, reader.read, len(prefix))
			}
		})
	}
}

func TestOneFrameChecksCorrelationAndOrderingOfTrailingRecords(t *testing.T) {
	first := frameRecord{requestID: "01K5V8JQFM6Q3Q0XZ6F1Z7A2BC", sequence: 0, kind: "client.hello", payload: []byte{0xa0}}
	second := first
	second.requestID = "01K6A000000000000000000001"
	otherID, err := encodeFrame(second, bootstrapFrameBodyLimit)
	if err != nil {
		t.Fatal(err)
	}
	firstWire, err := encodeFrame(first, bootstrapFrameBodyLimit)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := readOneFrame(bytes.NewReader(append(firstWire, otherID...)), bootstrapFrameBodyLimit); !errors.Is(err, errWrongCorrelation) {
		t.Fatalf("readOneFrame(mismatched second request ID) error = %v, want wrong-correlation", err)
	}

	second = first
	second.sequence = 1
	secondWire, err := encodeFrame(second, bootstrapFrameBodyLimit)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := readOneFrame(bytes.NewReader(append(firstWire, secondWire...)), bootstrapFrameBodyLimit); !errors.Is(err, errOutOfOrder) {
		t.Fatalf("readOneFrame(extra ordered frame after final) error = %v, want out-of-order", err)
	}
}

type prefixOnlyReader struct {
	prefix []byte
	read   int
}

func (reader *prefixOnlyReader) Read(destination []byte) (int, error) {
	if reader.read == len(reader.prefix) {
		return 0, io.EOF
	}
	n := copy(destination, reader.prefix[reader.read:])
	reader.read += n
	return n, nil
}

func loadStep4FrameVector(t *testing.T) step4FrameVector {
	t.Helper()
	data, err := os.ReadFile(step4FrameVectorPath)
	if err != nil {
		t.Fatal(err)
	}
	var vectors step4VectorFile
	if err := json.Unmarshal(data, &vectors); err != nil {
		t.Fatalf("decode Step 4 vectors: %v", err)
	}
	if len(vectors.FrameVectors) != 1 || vectors.FrameVectors[0].Name != "client-hello-frame" {
		t.Fatalf("Step 4 frame vectors = %#v, want the client-hello golden", vectors.FrameVectors)
	}
	return vectors.FrameVectors[0]
}

func mustHex(t *testing.T, value string) []byte {
	t.Helper()
	decoded, err := hex.DecodeString(value)
	if err != nil {
		t.Fatal(err)
	}
	return decoded
}

func mustMarshal(t *testing.T, value any) []byte {
	t.Helper()
	encoded, err := frameEncoder.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	return encoded
}

func framedBody(body []byte) []byte {
	wire := make([]byte, 4+len(body))
	binary.BigEndian.PutUint32(wire, uint32(len(body)))
	copy(wire[4:], body)
	return wire
}

func reorderFrameMapPairs(t *testing.T, body []byte) []byte {
	t.Helper()
	// Encode the same closed frame map in deliberately reverse field order.
	// Struct order is preserved by SortNone, unlike deterministic map order.
	type unsortedFrame struct {
		RequestID string `cbor:"request_id"`
		Payload   []byte `cbor:"payload"`
		Kind      string `cbor:"kind"`
		Sequence  uint64 `cbor:"sequence"`
		Schema    string `cbor:"schema"`
	}
	mode, err := cborEncUnsorted()
	if err != nil {
		t.Fatal(err)
	}
	var expected frameRecord
	decoded, err := decodeFrameBody(body)
	if err == nil {
		expected = decoded
	} else {
		t.Fatal(err)
	}
	unsorted, err := mode.Marshal(unsortedFrame{
		RequestID: expected.requestID,
		Payload:   expected.payload,
		Kind:      expected.kind,
		Sequence:  expected.sequence,
		Schema:    frameSchema,
	})
	if err != nil {
		t.Fatal(err)
	}
	return unsorted
}

func cborEncUnsorted() (cbor.EncMode, error) {
	return cbor.EncOptions{Sort: cbor.SortNone}.EncMode()
}
