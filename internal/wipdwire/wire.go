// Package wipdwire contains the closed M2 enrollment and M2/M6 seed records
// used by the bounded online-authority lab. It does not define new protocol
// schemas; all encoded maps correspond to docs/wipd/conformance-schemas.cddl.
package wipdwire

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
)

const (
	// FrameLimit is the bootstrap maximum body size in bytes.
	FrameLimit  = 65_536
	frameSchema = "wipd.frame/1"
)

var (
	// ErrInvalidRecord reports malformed or non-conformant wire records.
	ErrInvalidRecord = errors.New("wipdwire: invalid record")
	requestIDPattern = regexp.MustCompile(`^[0-7][0-9A-HJKMNP-TV-Z]{25}$`)
	kindPattern      = regexp.MustCompile(`^[a-z][a-z0-9]*(?:\.[a-z][a-z0-9]*)*$`)
	encoder          = mustEncoder()
	decoder          = mustDecoder()
)

func mustEncoder() cbor.EncMode {
	mode, err := cbor.CoreDetEncOptions().EncMode()
	if err != nil {
		panic(err)
	}
	return mode
}

func mustDecoder() cbor.DecMode {
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

// Frame is the M2 length-delimited deterministic-CBOR record.
type Frame struct {
	RequestID string `cbor:"request_id"`
	Sequence  uint64 `cbor:"sequence"`
	Kind      string `cbor:"kind"`
	Payload   []byte `cbor:"payload"`
}

// EncodeFrame encodes one length-prefixed deterministic-CBOR frame.
func EncodeFrame(frame Frame) ([]byte, error) {
	if !requestIDPattern.MatchString(frame.RequestID) || !kindPattern.MatchString(frame.Kind) || len(frame.Payload) == 0 {
		return nil, ErrInvalidRecord
	}
	body, err := encoder.Marshal(map[string]any{
		"schema": frameSchema, "request_id": frame.RequestID,
		"sequence": frame.Sequence, "kind": frame.Kind, "payload": frame.Payload,
	})
	if err != nil || len(body) == 0 || len(body) > FrameLimit || uint64(len(body)) > math.MaxUint32 {
		return nil, ErrInvalidRecord
	}
	wire := make([]byte, 4+len(body))
	binary.BigEndian.PutUint32(wire[:4], uint32(len(body)))
	copy(wire[4:], body)
	return wire, nil
}

// ReadFrame reads one bounded length-prefixed deterministic-CBOR frame.
func ReadFrame(reader io.Reader) (Frame, error) {
	var prefix [4]byte
	if _, err := io.ReadFull(reader, prefix[:]); err != nil {
		return Frame{}, err
	}
	length := binary.BigEndian.Uint32(prefix[:])
	if length == 0 || length > FrameLimit {
		return Frame{}, ErrInvalidRecord
	}
	body := make([]byte, length)
	if _, err := io.ReadFull(reader, body); err != nil {
		return Frame{}, ErrInvalidRecord
	}
	var fields map[string]any
	if err := DecodeCanonical(body, &fields, "schema", "request_id", "sequence", "kind", "payload"); err != nil {
		return Frame{}, err
	}
	var envelope struct {
		Schema string `cbor:"schema"`
		Frame
	}
	if err := decoder.Unmarshal(body, &envelope); err != nil || envelope.Schema != frameSchema ||
		!requestIDPattern.MatchString(envelope.RequestID) || !kindPattern.MatchString(envelope.Kind) || len(envelope.Payload) == 0 {
		return Frame{}, ErrInvalidRecord
	}
	return envelope.Frame, nil
}

// ReadFrames parses at most maxFrames complete frames from data.
func ReadFrames(data []byte, maxFrames int) ([]Frame, error) {
	if maxFrames <= 0 || len(data) > maxFrames*(FrameLimit+4) {
		return nil, ErrInvalidRecord
	}
	reader := bytes.NewReader(data)
	frames := make([]Frame, 0, 4)
	for reader.Len() > 0 {
		if len(frames) == maxFrames {
			return nil, ErrInvalidRecord
		}
		frame, err := ReadFrame(reader)
		if err != nil {
			return nil, err
		}
		frames = append(frames, frame)
	}
	return frames, nil
}

// EncodeCanonical encodes a value using the protocol's deterministic CBOR mode.
func EncodeCanonical(value any) ([]byte, error) {
	return encoder.Marshal(value)
}

// DecodeCanonical enforces deterministic encoding and an exact closed map
// before decoding into dst.
func DecodeCanonical(data []byte, dst any, keys ...string) error {
	if _, err := DecodeCanonicalMap(data, keys...); err != nil {
		return fmt.Errorf("%w: CBOR map", ErrInvalidRecord)
	}
	if err := decoder.Unmarshal(data, dst); err != nil {
		return fmt.Errorf("%w: typed CBOR map", ErrInvalidRecord)
	}
	return nil
}

// DecodeCanonicalMap returns a deterministic CBOR map with exactly the
// requested keys. Nested maps remain available for schema-specific closure
// checks before a typed decode.
func DecodeCanonicalMap(data []byte, keys ...string) (map[string]any, error) {
	var fields map[string]any
	if err := decoder.Unmarshal(data, &fields); err != nil || fields == nil || !ExactMapKeys(fields, keys...) {
		return nil, fmt.Errorf("%w: CBOR map", ErrInvalidRecord)
	}
	canonical, err := encoder.Marshal(fields)
	if err != nil || !bytes.Equal(canonical, data) {
		return nil, fmt.Errorf("%w: non-canonical CBOR", ErrInvalidRecord)
	}
	return fields, nil
}

// ExactMapKeys reports whether fields is a string-keyed CBOR map with exactly
// the expected keys.
func ExactMapKeys(fields map[string]any, keys ...string) bool {
	if fields == nil || len(fields) != len(keys) {
		return false
	}
	for _, key := range keys {
		if _, ok := fields[key]; !ok {
			return false
		}
	}
	return true
}

// EnrollmentRequest is the closed M2 request for enrollment before mTLS.
type EnrollmentRequest struct {
	Schema     string  `cbor:"schema"`
	Credential []byte  `cbor:"credential"`
	CSRDER     []byte  `cbor:"csr_der"`
	PriorID    *string `cbor:"prior_environment_id"`
}

// EnrollmentIssued is the authority's grant-bound Environment certificate result.
type EnrollmentIssued struct {
	Schema           string   `cbor:"schema"`
	DomainID         string   `cbor:"domain_id"`
	Epoch            uint64   `cbor:"authority_epoch"`
	EnvironmentID    string   `cbor:"environment_id"`
	OwnerKeyID       string   `cbor:"owner_key_id"`
	SPKIDigest       string   `cbor:"spki_digest"`
	CertificateChain [][]byte `cbor:"certificate_chain_der"`
}

// PrefixAnchor identifies one exact authority event-prefix boundary.
type PrefixAnchor struct {
	EventCount uint64  `cbor:"event_count"`
	EventID    *string `cbor:"high_water_event_id"`
	Digest     string  `cbor:"prefix_digest"`
}

// SeedRequest asks for the initial authority seed for a bound domain epoch.
type SeedRequest struct {
	Schema      string  `cbor:"schema"`
	DomainID    string  `cbor:"domain_id"`
	Epoch       uint64  `cbor:"expected_epoch"`
	StoreSchema string  `cbor:"store_schema"`
	ResumeToken *string `cbor:"resume_token"`
}

// PullRequest asks for the complete authority delta after an exact installed
// prefix. The current M5 lab slice requires a nil ResumeToken.
type PullRequest struct {
	Schema      string       `cbor:"schema"`
	DomainID    string       `cbor:"domain_id"`
	Epoch       uint64       `cbor:"expected_epoch"`
	Installed   PrefixAnchor `cbor:"installed"`
	ResumeToken *string      `cbor:"resume_token"`
}

// SeedStart describes the stable prefix and snapshot beginning a seed transfer.
type SeedStart struct {
	Schema      string `cbor:"schema"`
	TransferID  string `cbor:"transfer_id"`
	DomainID    string `cbor:"domain_id"`
	Epoch       uint64 `cbor:"authority_epoch"`
	StoreSchema string `cbor:"store_schema"`
	SnapshotID  string `cbor:"snapshot_id"`
	Prefix      struct {
		Start PrefixAnchor `cbor:"start"`
		End   PrefixAnchor `cbor:"end"`
	} `cbor:"prefix"`
	EventCount      uint64 `cbor:"event_count"`
	EventByteLength uint64 `cbor:"event_byte_length"`
	ManifestDigest  string `cbor:"blob_manifest_digest"`
}

// PullStart describes a bounded pull from the client's installed anchor to a
// pinned current authority prefix, using the existing M2 logical message.
type PullStart struct {
	Schema     string `cbor:"schema"`
	TransferID string `cbor:"transfer_id"`
	DomainID   string `cbor:"domain_id"`
	Epoch      uint64 `cbor:"authority_epoch"`
	Prefix     struct {
		Start PrefixAnchor `cbor:"start"`
		End   PrefixAnchor `cbor:"end"`
	} `cbor:"prefix"`
	EventCount      uint64 `cbor:"event_count"`
	EventByteLength uint64 `cbor:"event_byte_length"`
	ManifestDigest  string `cbor:"blob_manifest_digest"`
}

// EventRecord carries the exact immutable event bytes and their authority ID.
type EventRecord struct {
	EventID string `cbor:"event_id"`
	Record  []byte `cbor:"record"`
}

// BlobManifestEntry describes one blob in a seed's complete blob manifest.
type BlobManifestEntry struct {
	Digest      string `cbor:"digest"`
	ByteLength  uint64 `cbor:"byte_length"`
	Requirement string `cbor:"requirement"`
}

// BlobManifest is the complete blob set anchored to one authority prefix.
type BlobManifest struct {
	Schema   string              `cbor:"schema"`
	DomainID string              `cbor:"domain_id"`
	Epoch    uint64              `cbor:"authority_epoch"`
	AsOf     PrefixAnchor        `cbor:"as_of"`
	Entries  []BlobManifestEntry `cbor:"entries"`
	Digest   string              `cbor:"manifest_digest"`
}

// SeedEnd closes a seed transfer with the verified prefix and manifest digest.
type SeedEnd struct {
	Schema         string       `cbor:"schema"`
	TransferID     string       `cbor:"transfer_id"`
	VerifiedPrefix PrefixAnchor `cbor:"verified_prefix"`
	ManifestDigest string       `cbor:"verified_blob_manifest_digest"`
	Complete       bool         `cbor:"complete"`
}

// PullEnd closes an M2 pull with its verified prefix and manifest digest.
type PullEnd struct {
	TransferID     string       `cbor:"transfer_id"`
	VerifiedPrefix PrefixAnchor `cbor:"verified_prefix"`
	ManifestDigest string       `cbor:"verified_blob_manifest_digest"`
	Complete       bool         `cbor:"complete"`
}
