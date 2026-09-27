package wipd

import (
	"bytes"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"math"
	"reflect"
	"regexp"

	"github.com/fxamacker/cbor/v2"
	"golang.org/x/text/unicode/norm"
)

const (
	frameSchema             = "wipd.frame/1"
	bootstrapFrameBodyLimit = uint32(65_536)
	maxFrameBodyLimit       = uint32(1_048_576)
)

var (
	frameRequestIDPattern = regexp.MustCompile(`^[0-7][0-9A-HJKMNP-TV-Z]{25}$`)
	frameKindPattern      = regexp.MustCompile(`^[a-z][a-z0-9]*(?:\.[a-z][a-z0-9]*)*$`)
	frameEncoder          = mustFrameEncoder()
	frameDecoder          = mustFrameDecoder()

	errInvalidFrame         = errors.New("protocol.invalid-frame")
	errFrameTooLarge        = errors.New("protocol.frame-too-large")
	errTruncated            = errors.New("protocol.truncated-frame")
	errOutOfOrder           = errors.New("protocol.out-of-order")
	errWrongCorrelation     = errors.New("protocol.wrong-correlation")
	errUnsupportedKind      = errors.New("protocol.unsupported-kind")
	errUnsupportedExtension = errors.New("protocol.unsupported-extension")
	errInvalidCapabilities  = errors.New("protocol.invalid-capabilities")
	errIncompatibleVersion  = errors.New("protocol.incompatible-version")
	errMalformedMessage     = errors.New("protocol.malformed-message")
)

type frameRecord struct {
	requestID string
	sequence  uint64
	kind      string
	payload   []byte
}

func mustFrameEncoder() cbor.EncMode {
	mode, err := cbor.CoreDetEncOptions().EncMode()
	if err != nil {
		panic(err)
	}
	return mode
}

func mustFrameDecoder() cbor.DecMode {
	mode, err := (cbor.DecOptions{
		DupMapKey:        cbor.DupMapKeyEnforcedAPF,
		IndefLength:      cbor.IndefLengthForbidden,
		TagsMd:           cbor.TagsForbidden,
		MaxNestedLevels:  16,
		MaxArrayElements: 4096,
		MaxMapPairs:      1024,
		DefaultMapType:   reflect.TypeOf(map[string]any{}),
	}).DecMode()
	if err != nil {
		panic(err)
	}
	return mode
}

func encodeFrame(frame frameRecord, limit uint32) ([]byte, error) {
	if !validRequestID(frame.requestID) || !frameKindPattern.MatchString(frame.kind) || len(frame.payload) == 0 {
		return nil, errInvalidFrame
	}
	body, err := frameEncoder.Marshal(map[string]any{
		"schema":     frameSchema,
		"request_id": frame.requestID,
		"sequence":   frame.sequence,
		"kind":       frame.kind,
		"payload":    frame.payload,
	})
	if err != nil {
		return nil, fmt.Errorf("%w: encode CBOR frame: %v", errInvalidFrame, err)
	}
	if len(body) == 0 || uint64(len(body)) > uint64(limit) || uint64(len(body)) > math.MaxUint32 {
		return nil, errFrameTooLarge
	}
	wire := make([]byte, 4+len(body))
	binary.BigEndian.PutUint32(wire[:4], uint32(len(body)))
	copy(wire[4:], body)
	return wire, nil
}

// readFrame checks the fixed-size prefix against the caller's limit before it
// allocates storage for the declared body.
func readFrame(reader io.Reader, limit uint32) (frameRecord, error) {
	return readFrameUsing(reader, limit, func(length uint32) []byte { return make([]byte, length) })
}

type countingReader struct {
	reader    io.Reader
	bytesRead int64
}

func (reader *countingReader) Read(data []byte) (int, error) {
	n, err := reader.reader.Read(data)
	reader.bytesRead += int64(n)
	return n, err
}

func readFrameUsing(reader io.Reader, limit uint32, allocate func(uint32) []byte) (frameRecord, error) {
	var prefix [4]byte
	n, err := io.ReadFull(reader, prefix[:])
	if err != nil {
		if n == 0 && errors.Is(err, io.EOF) {
			return frameRecord{}, io.EOF
		}
		return frameRecord{}, errTruncated
	}
	bodyLength := binary.BigEndian.Uint32(prefix[:])
	if bodyLength == 0 {
		return frameRecord{}, errInvalidFrame
	}
	if bodyLength > limit {
		return frameRecord{}, errFrameTooLarge
	}
	body := allocate(bodyLength)
	if _, err := io.ReadFull(reader, body); err != nil {
		return frameRecord{}, errTruncated
	}
	return decodeFrameBody(body)
}

func readOneFrame(reader io.Reader, limit uint32) (frameRecord, error) {
	frame, err := readFrame(reader, limit)
	if err != nil {
		return frameRecord{}, err
	}
	next, err := readFrame(reader, limit)
	if err == nil {
		if next.requestID != frame.requestID {
			return frameRecord{}, errWrongCorrelation
		}
		return frameRecord{}, errOutOfOrder
	} else if !errors.Is(err, io.EOF) {
		return frameRecord{}, err
	}
	return frame, nil
}

func decodeFrameBody(body []byte) (frameRecord, error) {
	var value any
	if err := frameDecoder.Unmarshal(body, &value); err != nil {
		return frameRecord{}, fmt.Errorf("%w: decode CBOR frame: %v", errInvalidFrame, err)
	}
	if err := validateFrameValue(value); err != nil {
		return frameRecord{}, fmt.Errorf("%w: %v", errInvalidFrame, err)
	}
	canonical, err := frameEncoder.Marshal(value)
	if err != nil || !bytes.Equal(canonical, body) {
		return frameRecord{}, errInvalidFrame
	}
	fields, ok := value.(map[string]any)
	if !ok || len(fields) != 5 {
		return frameRecord{}, errInvalidFrame
	}
	for _, key := range []string{"schema", "request_id", "sequence", "kind", "payload"} {
		if _, ok := fields[key]; !ok {
			return frameRecord{}, errInvalidFrame
		}
	}
	schema, schemaOK := fields["schema"].(string)
	requestID, requestIDOK := fields["request_id"].(string)
	sequence, sequenceOK := fields["sequence"].(uint64)
	kind, kindOK := fields["kind"].(string)
	payload, payloadOK := fields["payload"].([]byte)
	if !schemaOK || schema != frameSchema || !requestIDOK || !validRequestID(requestID) ||
		!sequenceOK || !kindOK || !frameKindPattern.MatchString(kind) || !payloadOK || len(payload) == 0 {
		return frameRecord{}, errInvalidFrame
	}
	return frameRecord{requestID: requestID, sequence: sequence, kind: kind, payload: payload}, nil
}

func decodePayload(data []byte) (any, error) {
	var value any
	if err := frameDecoder.Unmarshal(data, &value); err != nil {
		return nil, fmt.Errorf("%w: decode CBOR payload: %v", errMalformedMessage, err)
	}
	if err := validateFrameValue(value); err != nil {
		return nil, fmt.Errorf("%w: %v", errMalformedMessage, err)
	}
	canonical, err := frameEncoder.Marshal(value)
	if err != nil || !bytes.Equal(canonical, data) {
		return nil, errMalformedMessage
	}
	return value, nil
}

func validateFrameValue(value any) error {
	switch value := value.(type) {
	case nil, bool, uint64, int64, []byte:
		return nil
	case string:
		if !norm.NFC.IsNormalString(value) {
			return fmt.Errorf("CBOR text is not NFC")
		}
		return nil
	case []any:
		for _, element := range value {
			if err := validateFrameValue(element); err != nil {
				return err
			}
		}
		return nil
	case map[string]any:
		for key, element := range value {
			if !norm.NFC.IsNormalString(key) {
				return fmt.Errorf("CBOR map key is not NFC")
			}
			if err := validateFrameValue(element); err != nil {
				return err
			}
		}
		return nil
	default:
		return fmt.Errorf("unsupported deterministic CBOR value %T", value)
	}
}

func validRequestID(value string) bool {
	return frameRequestIDPattern.MatchString(value)
}

func encodePayload(value any) ([]byte, error) {
	if err := validateFrameValue(value); err != nil {
		return nil, fmt.Errorf("%w: %v", errMalformedMessage, err)
	}
	encoded, err := frameEncoder.Marshal(value)
	if err != nil {
		return nil, fmt.Errorf("%w: encode CBOR payload: %v", errMalformedMessage, err)
	}
	return encoded, nil
}
