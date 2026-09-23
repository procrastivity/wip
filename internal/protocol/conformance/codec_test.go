package conformance_test

import (
	"bytes"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"regexp"
	"sort"
	"testing"
	"unicode/utf8"

	"golang.org/x/text/unicode/norm"
)

const defaultFrameLimit = uint32(1_048_576)

var conformanceULID = regexp.MustCompile(`^[0-7][0-9A-HJKMNP-TV-Z]{25}$`)

type frameValue struct {
	Kind      string
	Schema    string
	Payload   []byte
	Sequence  uint64
	RequestID string
}

func clientEncodeFrame(frame frameValue) ([]byte, error) {
	body, err := clientCanonical(map[string]any{
		"schema": frame.Schema, "request_id": frame.RequestID, "sequence": frame.Sequence,
		"kind": frame.Kind, "payload": frame.Payload,
	})
	if err != nil {
		return nil, err
	}
	wire := make([]byte, 4, len(body)+4)
	binary.BigEndian.PutUint32(wire, uint32(len(body)))
	return append(wire, body...), nil
}

func serverEncodeFrame(frame frameValue) ([]byte, error) {
	if frame.Schema != "wipd.frame/1" || !conformanceULID.MatchString(frame.RequestID) {
		return nil, errors.New("invalid frame identity")
	}
	body := []byte{0xa5}
	body = appendTextPair(body, "kind", frame.Kind)
	body = appendTextPair(body, "schema", frame.Schema)
	body = appendServerText(body, "payload")
	body = appendServerBytes(body, frame.Payload)
	body = appendServerText(body, "sequence")
	body = appendServerHead(body, 0, frame.Sequence)
	body = appendTextPair(body, "request_id", frame.RequestID)
	wire := make([]byte, 4, len(body)+4)
	binary.BigEndian.PutUint32(wire, uint32(len(body)))
	return append(wire, body...), nil
}

func appendTextPair(dst []byte, key, value string) []byte {
	dst = appendServerText(dst, key)
	return appendServerText(dst, value)
}

func appendServerText(dst []byte, value string) []byte {
	dst = appendServerHead(dst, 3, uint64(len(value)))
	return append(dst, value...)
}

func appendServerBytes(dst, value []byte) []byte {
	dst = appendServerHead(dst, 2, uint64(len(value)))
	return append(dst, value...)
}

type protocolError string

func (e protocolError) Error() string { return string(e) }

const (
	errInvalidFrame  = protocolError("protocol.invalid-frame")
	errFrameTooLarge = protocolError("protocol.frame-too-large")
	errTruncated     = protocolError("protocol.truncated-frame")
)

type clientParser struct {
	limit    uint32
	prefix   [4]byte
	prefixN  int
	declared uint32
	body     []byte
	bodyN    int
	done     bool
	err      error
}

func newClientParser(limit uint32) *clientParser {
	return &clientParser{limit: limit}
}

func (p *clientParser) Feed(data []byte) {
	if p.err != nil {
		return
	}
	for len(data) > 0 {
		if p.done {
			p.err = errInvalidFrame
			return
		}
		if p.prefixN < len(p.prefix) {
			count := copy(p.prefix[p.prefixN:], data)
			p.prefixN += count
			data = data[count:]
			if p.prefixN < len(p.prefix) {
				continue
			}
			p.declared = binary.BigEndian.Uint32(p.prefix[:])
			switch {
			case p.declared == 0:
				p.err = errInvalidFrame
				return
			case p.declared > p.limit:
				p.err = errFrameTooLarge
				return
			default:
				p.body = make([]byte, p.declared)
			}
		}
		count := copy(p.body[p.bodyN:], data)
		p.bodyN += count
		data = data[count:]
		if p.bodyN == len(p.body) {
			if _, err := clientDecodeFrame(p.body); err != nil {
				p.err = errInvalidFrame
				return
			}
			p.done = true
		}
	}
}

func (p *clientParser) End() error {
	if p.err != nil {
		return p.err
	}
	if !p.done {
		return errTruncated
	}
	return nil
}

func parseServerWire(wire []byte, limit uint32) error {
	if len(wire) < 4 {
		return errTruncated
	}
	declared := binary.BigEndian.Uint32(wire[:4])
	if declared == 0 {
		return errInvalidFrame
	}
	if declared > limit {
		return errFrameTooLarge
	}
	if uint64(declared)+4 > uint64(len(wire)) {
		return errTruncated
	}
	if uint64(declared)+4 != uint64(len(wire)) {
		return errInvalidFrame
	}
	decoded, err := serverDecodeCanonical(wire[4:])
	if err != nil {
		return errInvalidFrame
	}
	fields, ok := decoded.(map[string]any)
	if !ok || len(fields) != 5 {
		return errInvalidFrame
	}
	kind, kindOK := fields["kind"].(string)
	schema, schemaOK := fields["schema"].(string)
	_, payloadOK := fields["payload"].([]byte)
	_, sequenceOK := fields["sequence"].(uint64)
	requestID, requestIDOK := fields["request_id"].(string)
	if !kindOK || kind == "" || !schemaOK || schema != "wipd.frame/1" || !payloadOK ||
		!sequenceOK || !requestIDOK || !conformanceULID.MatchString(requestID) {
		return errInvalidFrame
	}
	return nil
}

type explicitReader struct {
	data []byte
	off  int
}

func clientDecodeFrame(body []byte) (frameValue, error) {
	r := explicitReader{data: body}
	if !r.takeByte(0xa5) || !r.takeText("kind") {
		return frameValue{}, errInvalidFrame
	}
	kind, ok := r.text()
	if !ok || kind == "" || !r.takeText("schema") {
		return frameValue{}, errInvalidFrame
	}
	schema, ok := r.text()
	if !ok || schema != "wipd.frame/1" || !r.takeText("payload") {
		return frameValue{}, errInvalidFrame
	}
	payload, ok := r.bytes()
	if !ok || !r.takeText("sequence") {
		return frameValue{}, errInvalidFrame
	}
	sequence, ok := r.unsigned()
	if !ok || !r.takeText("request_id") {
		return frameValue{}, errInvalidFrame
	}
	requestID, ok := r.text()
	if !ok || !conformanceULID.MatchString(requestID) || r.off != len(r.data) {
		return frameValue{}, errInvalidFrame
	}
	return frameValue{Kind: kind, Schema: schema, Payload: payload, Sequence: sequence, RequestID: requestID}, nil
}

func (r *explicitReader) takeByte(want byte) bool {
	if r.off >= len(r.data) || r.data[r.off] != want {
		return false
	}
	r.off++
	return true
}

func (r *explicitReader) takeText(want string) bool {
	got, ok := r.text()
	return ok && got == want
}

func (r *explicitReader) text() (string, bool) {
	value, ok := r.raw(3)
	if !ok || !utf8.Valid(value) || !norm.NFC.IsNormal(value) {
		return "", false
	}
	return string(value), true
}

func (r *explicitReader) bytes() ([]byte, bool) {
	value, ok := r.raw(2)
	if !ok {
		return nil, false
	}
	return append([]byte(nil), value...), true
}

func (r *explicitReader) raw(major byte) ([]byte, bool) {
	length, ok := r.head(major)
	if !ok || length > uint64(len(r.data)-r.off) {
		return nil, false
	}
	start := r.off
	r.off += int(length)
	return r.data[start:r.off], true
}

func (r *explicitReader) unsigned() (uint64, bool) { return r.head(0) }

func (r *explicitReader) head(wantMajor byte) (uint64, bool) {
	if r.off >= len(r.data) {
		return 0, false
	}
	initial := r.data[r.off]
	r.off++
	if initial>>5 != wantMajor {
		return 0, false
	}
	additional := initial & 0x1f
	if additional < 24 {
		return uint64(additional), true
	}
	var width int
	switch additional {
	case 24:
		width = 1
	case 25:
		width = 2
	case 26:
		width = 4
	case 27:
		width = 8
	default:
		return 0, false
	}
	if width > len(r.data)-r.off {
		return 0, false
	}
	var padded [8]byte
	copy(padded[8-width:], r.data[r.off:r.off+width])
	r.off += width
	value := binary.BigEndian.Uint64(padded[:])
	minimum := map[int]uint64{1: 24, 2: 0x100, 4: 0x10000, 8: 0x100000000}[width]
	if value < minimum {
		return 0, false
	}
	return value, true
}

type genericDecoder struct {
	data []byte
	off  int
}

func serverDecodeCanonical(data []byte) (any, error) {
	d := genericDecoder{data: data}
	value, err := d.value()
	if err != nil {
		return nil, err
	}
	if d.off != len(data) {
		return nil, errors.New("trailing CBOR")
	}
	return value, nil
}

func (d *genericDecoder) value() (any, error) {
	if d.off >= len(d.data) {
		return nil, errUnexpectedEnd
	}
	initial := d.data[d.off]
	d.off++
	major, additional := initial>>5, initial&0x1f
	argument, err := d.argument(additional)
	if err != nil {
		return nil, err
	}
	switch major {
	case 0:
		return argument, nil
	case 2, 3:
		value, err := d.take(argument)
		if err != nil {
			return nil, err
		}
		if major == 2 {
			return append([]byte(nil), value...), nil
		}
		if !utf8.Valid(value) || !norm.NFC.IsNormal(value) {
			return nil, errors.New("invalid text")
		}
		return string(value), nil
	case 4:
		if argument > uint64(len(d.data)-d.off) {
			return nil, errors.New("impossible array length")
		}
		items := make([]any, 0, int(argument))
		for range argument {
			item, err := d.value()
			if err != nil {
				return nil, err
			}
			items = append(items, item)
		}
		return items, nil
	case 5:
		if argument > uint64(len(d.data)-d.off)/2 {
			return nil, errors.New("impossible map length")
		}
		result := make(map[string]any)
		var previous []byte
		for range argument {
			start := d.off
			keyValue, err := d.value()
			if err != nil {
				return nil, err
			}
			keyBytes := d.data[start:d.off]
			key, ok := keyValue.(string)
			if !ok || (previous != nil && compareEncodedKeys(previous, keyBytes) >= 0) {
				return nil, errors.New("invalid map key order")
			}
			if _, duplicate := result[key]; duplicate {
				return nil, errors.New("duplicate map key")
			}
			previous = append(previous[:0], keyBytes...)
			result[key], err = d.value()
			if err != nil {
				return nil, err
			}
		}
		return result, nil
	case 7:
		switch additional {
		case 20:
			return false, nil
		case 21:
			return true, nil
		case 22:
			return nil, nil
		default:
			return nil, errors.New("unsupported simple value")
		}
	default:
		return nil, errors.New("unsupported CBOR major type")
	}
}

var errUnexpectedEnd = errors.New("unexpected end")

func (d *genericDecoder) argument(additional byte) (uint64, error) {
	if additional < 24 {
		return uint64(additional), nil
	}
	width := 0
	switch additional {
	case 24:
		width = 1
	case 25:
		width = 2
	case 26:
		width = 4
	case 27:
		width = 8
	default:
		return 0, errors.New("indefinite or reserved value")
	}
	data, err := d.take(uint64(width))
	if err != nil {
		return 0, err
	}
	var padded [8]byte
	copy(padded[8-width:], data)
	value := binary.BigEndian.Uint64(padded[:])
	minimum := map[int]uint64{1: 24, 2: 0x100, 4: 0x10000, 8: 0x100000000}[width]
	if value < minimum {
		return 0, errors.New("non-shortest value")
	}
	return value, nil
}

func (d *genericDecoder) take(length uint64) ([]byte, error) {
	if length > uint64(len(d.data)-d.off) {
		return nil, errUnexpectedEnd
	}
	start := d.off
	d.off += int(length)
	return d.data[start:d.off], nil
}

func compareEncodedKeys(left, right []byte) int {
	if len(left) < len(right) {
		return -1
	}
	if len(left) > len(right) {
		return 1
	}
	return bytes.Compare(left, right)
}

func clientCanonical(value any) ([]byte, error) {
	var result []byte
	err := appendClientCanonical(&result, value)
	return result, err
}

func appendClientCanonical(dst *[]byte, value any) error {
	switch value := value.(type) {
	case nil:
		*dst = append(*dst, 0xf6)
	case bool:
		if value {
			*dst = append(*dst, 0xf5)
		} else {
			*dst = append(*dst, 0xf4)
		}
	case string:
		if !utf8.ValidString(value) || !norm.NFC.IsNormalString(value) {
			return errors.New("noncanonical text")
		}
		*dst = appendServerHead(*dst, 3, uint64(len(value)))
		*dst = append(*dst, value...)
	case []byte:
		*dst = appendServerHead(*dst, 2, uint64(len(value)))
		*dst = append(*dst, value...)
	case uint64:
		*dst = appendServerHead(*dst, 0, value)
	case []any:
		*dst = appendServerHead(*dst, 4, uint64(len(value)))
		for _, item := range value {
			if err := appendClientCanonical(dst, item); err != nil {
				return err
			}
		}
	case map[string]any:
		type encodedEntry struct {
			key   string
			bytes []byte
		}
		entries := make([]encodedEntry, 0, len(value))
		for key := range value {
			encoded, err := clientCanonical(key)
			if err != nil {
				return err
			}
			entries = append(entries, encodedEntry{key: key, bytes: encoded})
		}
		sort.Slice(entries, func(i, j int) bool { return compareEncodedKeys(entries[i].bytes, entries[j].bytes) < 0 })
		*dst = appendServerHead(*dst, 5, uint64(len(entries)))
		for _, entry := range entries {
			*dst = append(*dst, entry.bytes...)
			if err := appendClientCanonical(dst, value[entry.key]); err != nil {
				return err
			}
		}
	default:
		return fmt.Errorf("unsupported canonical value %T", value)
	}
	return nil
}

func serverCanonical(value any) ([]byte, error) {
	var result []byte
	err := appendServerCanonical(&result, value)
	return result, err
}

func appendServerCanonical(dst *[]byte, value any) error {
	switch value := value.(type) {
	case nil:
		*dst = append(*dst, 0xf6)
	case bool:
		if value {
			*dst = append(*dst, 0xf5)
		} else {
			*dst = append(*dst, 0xf4)
		}
	case string:
		if !utf8.ValidString(value) || !norm.NFC.IsNormalString(value) {
			return errors.New("noncanonical text")
		}
		*dst = appendServerText(*dst, value)
	case []byte:
		*dst = appendServerBytes(*dst, value)
	case uint64:
		*dst = appendServerHead(*dst, 0, value)
	case []any:
		*dst = appendServerHead(*dst, 4, uint64(len(value)))
		for _, item := range value {
			if err := appendServerCanonical(dst, item); err != nil {
				return err
			}
		}
	case map[string]any:
		keys := make([]string, 0, len(value))
		for key := range value {
			keys = append(keys, key)
		}
		sort.Slice(keys, func(i, j int) bool {
			if len(keys[i]) != len(keys[j]) {
				return len(keys[i]) < len(keys[j])
			}
			return keys[i] < keys[j]
		})
		*dst = appendServerHead(*dst, 5, uint64(len(keys)))
		for _, key := range keys {
			*dst = appendServerText(*dst, key)
			if err := appendServerCanonical(dst, value[key]); err != nil {
				return err
			}
		}
	default:
		return fmt.Errorf("unsupported canonical value %T", value)
	}
	return nil
}

func appendServerHead(dst []byte, major byte, value uint64) []byte {
	prefix := major << 5
	switch {
	case value < 24:
		return append(dst, prefix|byte(value))
	case value <= 0xff:
		return append(dst, prefix|24, byte(value))
	case value <= 0xffff:
		dst = append(dst, prefix|25, 0, 0)
		binary.BigEndian.PutUint16(dst[len(dst)-2:], uint16(value))
	case value <= 0xffffffff:
		dst = append(dst, prefix|26, 0, 0, 0, 0)
		binary.BigEndian.PutUint32(dst[len(dst)-4:], uint32(value))
	default:
		dst = append(dst, prefix|27, 0, 0, 0, 0, 0, 0, 0, 0)
		binary.BigEndian.PutUint64(dst[len(dst)-8:], value)
	}
	return dst
}

func TestIndependentFrameCodecsMatchPublishedGolden(t *testing.T) {
	data, err := os.ReadFile("../../../docs/wipd/frame-security-execution-vectors.json")
	if err != nil {
		t.Fatal(err)
	}
	var source struct {
		Frames []struct {
			Kind       string `json:"kind"`
			RequestID  string `json:"request_id"`
			Sequence   uint64 `json:"sequence"`
			PayloadHex string `json:"payload_hex"`
			WireHex    string `json:"wire_hex"`
		} `json:"frame_vectors"`
	}
	if err := json.Unmarshal(data, &source); err != nil || len(source.Frames) != 1 {
		t.Fatalf("decode frame vector: %v / %d frames", err, len(source.Frames))
	}
	payload, err := hex.DecodeString(source.Frames[0].PayloadHex)
	if err != nil {
		t.Fatal(err)
	}
	want, err := hex.DecodeString(source.Frames[0].WireHex)
	if err != nil {
		t.Fatal(err)
	}
	frame := frameValue{
		Kind: source.Frames[0].Kind, Schema: "wipd.frame/1", Payload: payload,
		Sequence: source.Frames[0].Sequence, RequestID: source.Frames[0].RequestID,
	}
	client, err := clientEncodeFrame(frame)
	if err != nil {
		t.Fatal(err)
	}
	server, err := serverEncodeFrame(frame)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(client, server) || !bytes.Equal(client, want) {
		t.Fatalf("independent frame bytes disagree\nclient: %x\nserver: %x\nvector: %x", client, server, want)
	}
	parser := newClientParser(defaultFrameLimit)
	for _, split := range []int{1, 1, 2, 17, len(client) - 21} {
		parser.Feed(client[:split])
		client = client[split:]
	}
	if err := parser.End(); err != nil {
		t.Fatalf("fragmented client parse: %v", err)
	}
	if err := parseServerWire(want, defaultFrameLimit); err != nil {
		t.Fatalf("server parse: %v", err)
	}
}

func TestFrameParserNegativeCorpusAndPreallocationLimits(t *testing.T) {
	valid, err := clientEncodeFrame(frameValue{
		Kind: "query.request", Schema: "wipd.frame/1", Payload: []byte{0xa0},
		RequestID: "01K6A000000000000000000001",
	})
	if err != nil {
		t.Fatal(err)
	}
	oversized := []byte{0, 0, 4, 1}
	unknownFieldBody, err := clientCanonical(map[string]any{
		"x": uint64(0), "kind": "query.request", "schema": "wipd.frame/1", "payload": []byte{0xa0},
		"sequence": uint64(0), "request_id": "01K6A000000000000000000001",
	})
	if err != nil {
		t.Fatal(err)
	}
	body := valid[4:]
	nonShortestSequence := bytes.Replace(body, []byte("\x68sequence\x00\x6arequest_id"), []byte("\x68sequence\x18\x00\x6arequest_id"), 1)
	if bytes.Equal(body, nonShortestSequence) {
		t.Fatal("failed to construct non-shortest sequence corpus entry")
	}
	duplicateKey := append([]byte(nil), body...)
	duplicateKey[0] = 0xa6
	duplicateKey = appendTextPair(duplicateKey, "request_id", "01K6A000000000000000000001")
	tests := []struct {
		name string
		wire []byte
		want error
	}{
		{"partial-prefix", []byte{0, 0, 1}, errTruncated},
		{"zero-length", []byte{0, 0, 0, 0}, errInvalidFrame},
		{"oversized-before-allocation", oversized, errFrameTooLarge},
		{"partial-body", valid[:len(valid)-1], errTruncated},
		{"trailing-record-byte", append(append([]byte(nil), valid...), 0), errInvalidFrame},
		{"indefinite-map", record([]byte{0xbf, 0xff}), errInvalidFrame},
		{"tagged-map", record([]byte{0xc0, 0xa0}), errInvalidFrame},
		{"non-shortest-map-length", record([]byte{0xb8, 0x00}), errInvalidFrame},
		{"non-shortest-sequence", record(nonShortestSequence), errInvalidFrame},
		{"duplicate-map-key", record(duplicateKey), errInvalidFrame},
		{"unknown-closed-map-field", record(unknownFieldBody), errInvalidFrame},
		{"wrong-map-size", record([]byte{0xa0}), errInvalidFrame},
		{"invalid-utf8", record([]byte{0xa1, 0x61, 0xff, 0x00}), errInvalidFrame},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			client := newClientParser(1024)
			for _, b := range test.wire {
				client.Feed([]byte{b})
			}
			clientErr := client.End()
			serverErr := parseServerWire(test.wire, 1024)
			if !errors.Is(clientErr, test.want) || !errors.Is(serverErr, test.want) {
				t.Fatalf("errors client/server = %v / %v, want %v", clientErr, serverErr, test.want)
			}
			if test.name == "oversized-before-allocation" && client.body != nil {
				t.Fatalf("oversized frame allocated %d bytes", len(client.body))
			}
		})
	}
}

func record(body []byte) []byte {
	wire := make([]byte, 4, len(body)+4)
	binary.BigEndian.PutUint32(wire, uint32(len(body)))
	return append(wire, body...)
}

func originCommand(title string, epoch uint64) map[string]any {
	return map[string]any{
		"schema":     "wipd.command/1",
		"command_id": "01K5V8JQFM6Q3Q0XZ6F1Z7A2BC",
		"authority": map[string]any{
			"domain_id": "01K5V8K1Q5VX6Y0J8C9W3M4N5P", "expected_epoch": epoch,
		},
		"environment": map[string]any{
			"id": "01K5V8K8A4J2N7R9T0V3X6Y8ZB", "sequence": uint64(42),
		},
		"acted_at":               "2026-09-22T17:31:42.123456789Z",
		"actor":                  "role:builder",
		"causation_command_id":   nil,
		"correlation_command_id": "01K5V8JQFM6Q3Q0XZ6F1Z7A2BC",
		"operation":              map[string]any{"name": "matter.create", "version": uint64(1)},
		"context": map[string]any{
			"repo_id": "01K5V8KGD3F6H9J2M4N7Q0R5TW", "clone_id": nil, "worktree_id": nil,
		},
		"claim": nil,
		"input": map[string]any{"title": title, "requested_locator": "protocol-identity"},
		"blobs": []any{},
	}
}

func canonicalHash(data []byte) string {
	digest := sha256.Sum256(append([]byte("wipd/request-hash/v1\x00"), data...))
	return "sha256:" + hex.EncodeToString(digest[:])
}

func TestIndependentCanonicalCommandAgreement(t *testing.T) {
	command := originCommand("Café protocol identity", 7)
	client, err := clientCanonical(command)
	if err != nil {
		t.Fatal(err)
	}
	server, err := serverCanonical(command)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(client, server) {
		t.Fatalf("canonical encoders disagree\nclient: %x\nserver: %x", client, server)
	}
	fixture := loadConformanceFixture(t)
	var want string
	for _, vector := range fixture.CanonicalVectors {
		if vector.Name == "matter-create-v1-origin" {
			want = vector.RequestHash
		}
	}
	if got := canonicalHash(client); got != want {
		t.Fatalf("request hash = %s, want %s", got, want)
	}
}

func FuzzFrameParsersAgree(f *testing.F) {
	valid, err := clientEncodeFrame(frameValue{
		Kind: "query.request", Schema: "wipd.frame/1", Payload: []byte{0xa0},
		RequestID: "01K6A000000000000000000001",
	})
	if err != nil {
		f.Fatal(err)
	}
	for _, seed := range [][]byte{
		valid, {}, {0}, {0, 0, 0, 0}, {0, 0, 4, 1}, record([]byte{0xbf, 0xff}),
	} {
		f.Add(seed)
	}
	f.Fuzz(func(t *testing.T, data []byte) {
		if len(data) > 2_048 {
			t.Skip()
		}
		client := newClientParser(1024)
		for offset := 0; offset < len(data); {
			width := 1 + int(data[offset])%17
			end := min(offset+width, len(data))
			client.Feed(data[offset:end])
			offset = end
		}
		clientErr := client.End()
		serverErr := parseServerWire(data, 1024)
		if errorCode(clientErr) != errorCode(serverErr) {
			t.Fatalf("parser disagreement for %x: client=%v server=%v", data, clientErr, serverErr)
		}
	})
}

func errorCode(err error) string {
	if err == nil {
		return "ok"
	}
	return err.Error()
}
