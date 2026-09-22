package protocol

import (
	"bytes"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"reflect"
	"regexp"
	"sort"
	"strings"
	"testing"
	"unicode/utf8"
)

const vectorsPath = "../../docs/wipd/frame-security-execution-vectors.json"

type fixture struct {
	Notation                    string                       `json:"notation"`
	Limits                      limits                       `json:"limits"`
	FrameVectors                []frameVector                `json:"frame_vectors"`
	IdentityIndependenceVectors []identityIndependenceVector `json:"identity_independence_vectors"`
	SecurityVectors             []securityVector             `json:"security_vectors"`
	LimitVectors                []limitVector                `json:"limit_vectors"`
	ExecutionVectors            []executionVector            `json:"execution_vectors"`
	RedactionVectors            []redactionVector            `json:"redaction_vectors"`
}

type limits struct {
	BootstrapFrameBody         uint64 `json:"bootstrap_frame_body"`
	MaxFrameBody               uint64 `json:"max_frame_body"`
	MaxChunkData               uint64 `json:"max_chunk_data"`
	DefaultStreamBytes         uint64 `json:"default_stream_bytes"`
	AbsoluteStreamBytes        uint64 `json:"absolute_stream_bytes"`
	MaxConcurrentExchanges     uint64 `json:"max_concurrent_exchanges"`
	DefaultConcurrentExchanges uint64 `json:"default_concurrent_exchanges"`
	DefaultReceiveWindowBytes  uint64 `json:"default_receive_window_bytes"`
	AbsoluteReceiveWindowBytes uint64 `json:"absolute_receive_window_bytes"`
}

type frameVector struct {
	Name         string `json:"name"`
	Kind         string `json:"kind"`
	RequestID    string `json:"request_id"`
	Sequence     uint64 `json:"sequence"`
	PayloadHex   string `json:"payload_hex"`
	FrameBodyHex string `json:"frame_body_hex"`
	BodyLength   uint32 `json:"body_length"`
	WireHex      string `json:"wire_hex"`
	BodySHA256   string `json:"body_sha256"`
	Expected     string `json:"expected"`
}

type identityIndependenceVector struct {
	Name                  string             `json:"name"`
	CanonicalVector       string             `json:"canonical_vector"`
	CommandID             string             `json:"command_id"`
	RequestHash           string             `json:"request_hash"`
	Attempts              []transportAttempt `json:"attempts"`
	ExpectedRequestHashes []string           `json:"expected_request_hashes"`
	Expected              string             `json:"expected"`
}

type transportAttempt struct {
	RequestID       string  `json:"request_id"`
	Deadline        *string `json:"deadline"`
	HTTP2DataSplits []int   `json:"http2_data_splits"`
}

type securityVector struct {
	Name         string   `json:"name"`
	Boundary     string   `json:"boundary"`
	Facts        []string `json:"facts"`
	ExpectedCode string   `json:"expected_code"`
	Submitted    bool     `json:"submitted"`
	Effects      string   `json:"effects"`
}

type limitVector struct {
	Name            string   `json:"name"`
	Facts           []string `json:"facts"`
	ExpectedCode    string   `json:"expected_code"`
	AllocationBytes uint64   `json:"allocation_bytes"`
	Submitted       bool     `json:"submitted"`
}

type executionVector struct {
	Name          string   `json:"name"`
	Events        []string `json:"events"`
	ExpectedCode  string   `json:"expected_code"`
	ClientOutcome string   `json:"client_outcome"`
	Submitted     *bool    `json:"submitted"`
	Effects       string   `json:"effects"`
}

type redactionVector struct {
	Name                   string         `json:"name"`
	Input                  map[string]any `json:"input"`
	ExpectedRoutine        map[string]any `json:"expected_routine"`
	ForbiddenRoutineFields []string       `json:"forbidden_routine_fields"`
}

func loadFixture(t *testing.T) fixture {
	t.Helper()

	f, err := os.Open(vectorsPath)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = f.Close() }()

	var got fixture
	decoder := json.NewDecoder(f)
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&got); err != nil {
		t.Fatalf("decode vectors: %v", err)
	}
	if err := decoder.Decode(&struct{}{}); err != io.EOF {
		t.Fatalf("unexpected trailing JSON: %v", err)
	}
	return got
}

func TestStep4FrameGoldenVector(t *testing.T) {
	vectors := loadFixture(t)
	if vectors.Notation != "wipd.frame-security-vector/1" {
		t.Fatalf("notation = %q", vectors.Notation)
	}
	if len(vectors.FrameVectors) != 1 {
		t.Fatalf("frame vector count = %d", len(vectors.FrameVectors))
	}

	v := vectors.FrameVectors[0]
	body := mustDecodeHex(t, v.FrameBodyHex)
	wire := mustDecodeHex(t, v.WireHex)
	payload := mustDecodeHex(t, v.PayloadHex)
	if len(body) != int(v.BodyLength) {
		t.Fatalf("body length = %d, vector = %d", len(body), v.BodyLength)
	}
	if len(wire) != len(body)+4 {
		t.Fatalf("wire length = %d, want %d", len(wire), len(body)+4)
	}
	if got := binary.BigEndian.Uint32(wire[:4]); got != v.BodyLength {
		t.Fatalf("wire prefix = %d, want %d", got, v.BodyLength)
	}
	if !bytes.Equal(wire[4:], body) {
		t.Fatal("wire body does not equal frame body")
	}
	digest := sha256.Sum256(body)
	if got := "sha256:" + hex.EncodeToString(digest[:]); got != v.BodySHA256 {
		t.Fatalf("body digest = %s, want %s", got, v.BodySHA256)
	}

	decoded := decodeDeterministicCBOR(t, body)
	frame, ok := decoded.(map[string]any)
	if !ok {
		t.Fatalf("frame type = %T", decoded)
	}
	wantFrame := map[string]any{
		"schema":     "wipd.frame/1",
		"request_id": v.RequestID,
		"sequence":   v.Sequence,
		"kind":       v.Kind,
		"payload":    payload,
	}
	if !reflect.DeepEqual(frame, wantFrame) {
		t.Fatalf("decoded frame mismatch\n got: %#v\nwant: %#v", frame, wantFrame)
	}

	hello, ok := decodeDeterministicCBOR(t, payload).(map[string]any)
	if !ok {
		t.Fatal("ClientHello payload is not a map")
	}
	assertArray(t, hello["protocol_min"], uint64(1), uint64(0))
	assertArray(t, hello["protocol_max"], uint64(1), uint64(0))
	assertArray(t, hello["features"], "wipd.frame/1")
	assertArray(t, hello["identity_schemas"], "wipd.command/1")
	assertArray(t, hello["store_schemas"], "wipd.store/1")
	operations, ok := hello["operations"].([]any)
	if !ok || len(operations) != 1 {
		t.Fatalf("operations = %#v", hello["operations"])
	}
	operation, ok := operations[0].(map[string]any)
	if !ok || operation["name"] != "matter.create" {
		t.Fatalf("operation = %#v", operations[0])
	}
	assertArray(t, operation["versions"], uint64(1))
	assertArray(t, operation["identity_schemas"], "wipd.command/1")
}

func TestStep4CanonicalIdentityIsTransportIndependent(t *testing.T) {
	vectors := loadFixture(t)
	if len(vectors.IdentityIndependenceVectors) != 1 {
		t.Fatalf("identity vector count = %d", len(vectors.IdentityIndependenceVectors))
	}
	v := vectors.IdentityIndependenceVectors[0]
	if len(v.Attempts) != 2 || len(v.ExpectedRequestHashes) != len(v.Attempts) {
		t.Fatalf("attempt/hash counts = %d/%d", len(v.Attempts), len(v.ExpectedRequestHashes))
	}
	if v.Attempts[0].RequestID == v.Attempts[1].RequestID {
		t.Fatal("transport retries reused request_id")
	}
	if reflect.DeepEqual(v.Attempts[0].HTTP2DataSplits, v.Attempts[1].HTTP2DataSplits) {
		t.Fatal("identity vector does not vary HTTP/2 splitting")
	}
	for i, got := range v.ExpectedRequestHashes {
		if got != v.RequestHash {
			t.Fatalf("attempt %d hash = %s, want %s", i, got, v.RequestHash)
		}
	}

	identityContract, err := os.ReadFile("../../docs/wipd/canonical-semantic-identity.md")
	if err != nil {
		t.Fatal(err)
	}
	for _, pinned := range []string{v.CanonicalVector, v.CommandID, v.RequestHash} {
		if !bytes.Contains(identityContract, []byte(pinned)) {
			t.Fatalf("canonical identity contract does not contain %q", pinned)
		}
	}
}

func TestStep4SecurityLimitAndExecutionBoundaries(t *testing.T) {
	vectors := loadFixture(t)
	wantLimits := limits{
		BootstrapFrameBody:         65536,
		MaxFrameBody:               1048576,
		MaxChunkData:               65536,
		DefaultStreamBytes:         8589934592,
		AbsoluteStreamBytes:        1099511627776,
		MaxConcurrentExchanges:     64,
		DefaultConcurrentExchanges: 32,
		DefaultReceiveWindowBytes:  1048576,
		AbsoluteReceiveWindowBytes: 16777216,
	}
	if vectors.Limits != wantLimits {
		t.Fatalf("limits = %#v, want %#v", vectors.Limits, wantLimits)
	}

	security := indexSecurityVectors(t, vectors.SecurityVectors)
	assertSecurity(t, security, "local-peer-owner-mismatch", "auth.local-peer-mismatch")
	assertSecurity(t, security, "remote-authority-pin-mismatch", "auth.authority-pin-mismatch")
	assertSecurity(t, security, "remote-environment-domain-mismatch", "auth.environment-domain-mismatch")
	assertSecurity(t, security, "remote-environment-revoked-on-retained-connection", "auth.environment-revoked")
	assertSecurity(t, security, "enrollment-grant-reuse-with-different-key", "auth.grant-consumed")
	assertSecurity(t, security, "application-signature-cannot-replace-mtls", "auth.environment-certificate-required")

	limitsByName := indexLimitVectors(t, vectors.LimitVectors)
	for name, code := range map[string]string{
		"oversized-frame-before-allocation": "protocol.frame-too-large",
		"oversized-chunk":                   "protocol.chunk-too-large",
		"noncontiguous-stream-offset":       "protocol.stream-offset",
		"declared-stream-over-limit":        "protocol.stream-too-large",
		"concurrency-overload":              "transport.overloaded",
	} {
		got, ok := limitsByName[name]
		if !ok {
			t.Fatalf("missing limit vector %q", name)
		}
		if got.ExpectedCode != code || got.Submitted || got.AllocationBytes != 0 {
			t.Fatalf("limit %s = %#v", name, got)
		}
	}

	execution := indexExecutionVectors(t, vectors.ExecutionVectors)
	assertExecution(t, execution, "cancel-wins-before-submission", "transport.cancelled-before-submission", "unavailable", false)
	assertExecution(t, execution, "deadline-wins-before-submission", "transport.deadline-before-submission", "unavailable", false)
	assertExecution(t, execution, "cancel-after-submission", "transport.wait-cancelled", "outcome-unknown", true)
	unknown := execution["disconnect-after-bytes-before-ack"]
	if unknown.Submitted != nil || unknown.ExpectedCode != "outcome-unknown" {
		t.Fatalf("ambiguous disconnect = %#v", unknown)
	}
	if !strings.Contains(execution["cancel-after-submission"].Effects, "no-rollback") {
		t.Fatal("post-submission cancellation vector does not pin no-rollback")
	}
}

func TestStep4RoutineLogRedaction(t *testing.T) {
	vectors := loadFixture(t)
	if len(vectors.RedactionVectors) != 1 {
		t.Fatalf("redaction vector count = %d", len(vectors.RedactionVectors))
	}
	v := vectors.RedactionVectors[0]
	allowed := map[string]struct{}{
		"event": {}, "stable_code": {}, "transport": {}, "selected_protocol": {},
		"request_id": {}, "domain_id": {}, "authority_epoch": {},
		"environment_id": {}, "command_id": {}, "operation": {},
	}
	got := make(map[string]any)
	for key, value := range v.Input {
		if _, ok := allowed[key]; ok {
			got[key] = value
		}
	}
	if !reflect.DeepEqual(got, v.ExpectedRoutine) {
		t.Fatalf("routine projection mismatch\n got: %#v\nwant: %#v", got, v.ExpectedRoutine)
	}
	encoded, err := json.Marshal(got)
	if err != nil {
		t.Fatal(err)
	}
	for _, key := range v.ForbiddenRoutineFields {
		if _, ok := got[key]; ok {
			t.Fatalf("forbidden field %q survived routine projection", key)
		}
		if value, ok := v.Input[key]; ok && bytes.Contains(encoded, []byte(fmt.Sprint(value))) {
			t.Fatalf("forbidden value from %q survived routine projection", key)
		}
	}
}

func TestStep4ContractMapsRequiredDecisionsAndQuestions(t *testing.T) {
	contract, err := os.ReadFile("../../docs/wipd/frame-security-execution-contract.md")
	if err != nil {
		t.Fatal(err)
	}
	for _, token := range []string{
		"D115", "D117", "D120", "D122", "D123", "D124", "D128", "D131",
		"Q04", "Q05", "Q18", "Q19", "Q20", "Q21", "Q23", "Q24",
		"canonical-semantic-identity.md", "protocol.incompatible-version",
	} {
		if !bytes.Contains(contract, []byte(token)) {
			t.Errorf("contract does not map %s", token)
		}
	}
}

func mustDecodeHex(t *testing.T, value string) []byte {
	t.Helper()
	decoded, err := hex.DecodeString(value)
	if err != nil {
		t.Fatalf("decode hex: %v", err)
	}
	return decoded
}

func assertArray(t *testing.T, got any, want ...any) {
	t.Helper()
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("array = %#v, want %#v", got, want)
	}
}

func indexSecurityVectors(t *testing.T, vectors []securityVector) map[string]securityVector {
	t.Helper()
	got := make(map[string]securityVector, len(vectors))
	for _, vector := range vectors {
		if _, exists := got[vector.Name]; exists {
			t.Fatalf("duplicate security vector %q", vector.Name)
		}
		got[vector.Name] = vector
	}
	return got
}

func assertSecurity(t *testing.T, vectors map[string]securityVector, name, code string) {
	t.Helper()
	got, ok := vectors[name]
	if !ok {
		t.Fatalf("missing security vector %q", name)
	}
	if got.ExpectedCode != code || got.Submitted {
		t.Fatalf("security vector %s = %#v", name, got)
	}
}

func indexLimitVectors(t *testing.T, vectors []limitVector) map[string]limitVector {
	t.Helper()
	got := make(map[string]limitVector, len(vectors))
	for _, vector := range vectors {
		if _, exists := got[vector.Name]; exists {
			t.Fatalf("duplicate limit vector %q", vector.Name)
		}
		got[vector.Name] = vector
	}
	return got
}

func indexExecutionVectors(t *testing.T, vectors []executionVector) map[string]executionVector {
	t.Helper()
	got := make(map[string]executionVector, len(vectors))
	for _, vector := range vectors {
		if _, exists := got[vector.Name]; exists {
			t.Fatalf("duplicate execution vector %q", vector.Name)
		}
		got[vector.Name] = vector
	}
	return got
}

func assertExecution(t *testing.T, vectors map[string]executionVector, name, code, outcome string, submitted bool) {
	t.Helper()
	got, ok := vectors[name]
	if !ok {
		t.Fatalf("missing execution vector %q", name)
	}
	if got.ExpectedCode != code || got.ClientOutcome != outcome || got.Submitted == nil || *got.Submitted != submitted {
		t.Fatalf("execution vector %s = %#v", name, got)
	}
}

type cborDecoder struct {
	data []byte
	off  int
}

func decodeDeterministicCBOR(t *testing.T, data []byte) any {
	t.Helper()
	decoder := cborDecoder{data: data}
	value, err := decoder.value()
	if err != nil {
		t.Fatalf("decode deterministic CBOR: %v", err)
	}
	if decoder.off != len(data) {
		t.Fatalf("CBOR has %d trailing bytes", len(data)-decoder.off)
	}
	return value
}

func (d *cborDecoder) value() (any, error) {
	if d.off >= len(d.data) {
		return nil, fmt.Errorf("unexpected end")
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
	case 2:
		value, err := d.take(argument)
		if err != nil {
			return nil, err
		}
		return append([]byte(nil), value...), nil
	case 3:
		value, err := d.take(argument)
		if err != nil {
			return nil, err
		}
		if !utf8.Valid(value) {
			return nil, fmt.Errorf("invalid UTF-8")
		}
		return string(value), nil
	case 4:
		items := make([]any, argument)
		for i := range items {
			items[i], err = d.value()
			if err != nil {
				return nil, err
			}
		}
		return items, nil
	case 5:
		result := make(map[string]any, argument)
		var previousKey []byte
		for range argument {
			start := d.off
			decodedKey, err := d.value()
			if err != nil {
				return nil, err
			}
			keyBytes := d.data[start:d.off]
			key, ok := decodedKey.(string)
			if !ok {
				return nil, fmt.Errorf("map key is %T, not text", decodedKey)
			}
			if previousKey != nil && compareCBORKeys(previousKey, keyBytes) >= 0 {
				return nil, fmt.Errorf("map keys not in deterministic order at %q", key)
			}
			if _, duplicate := result[key]; duplicate {
				return nil, fmt.Errorf("duplicate map key %q", key)
			}
			previousKey = append(previousKey[:0], keyBytes...)
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
			return nil, fmt.Errorf("unsupported simple value %d", additional)
		}
	default:
		return nil, fmt.Errorf("unsupported major type %d", major)
	}
}

func (d *cborDecoder) argument(additional byte) (uint64, error) {
	switch {
	case additional < 24:
		return uint64(additional), nil
	case additional == 24:
		value, err := d.takeUint(1)
		if err == nil && value < 24 {
			return 0, fmt.Errorf("non-shortest integer %d", value)
		}
		return value, err
	case additional == 25:
		value, err := d.takeUint(2)
		if err == nil && value <= 0xff {
			return 0, fmt.Errorf("non-shortest integer %d", value)
		}
		return value, err
	case additional == 26:
		value, err := d.takeUint(4)
		if err == nil && value <= 0xffff {
			return 0, fmt.Errorf("non-shortest integer %d", value)
		}
		return value, err
	case additional == 27:
		value, err := d.takeUint(8)
		if err == nil && value <= 0xffffffff {
			return 0, fmt.Errorf("non-shortest integer %d", value)
		}
		return value, err
	default:
		return 0, fmt.Errorf("indefinite or reserved additional value %d", additional)
	}
}

func (d *cborDecoder) take(length uint64) ([]byte, error) {
	if length > uint64(len(d.data)-d.off) {
		return nil, fmt.Errorf("declared length %d exceeds remaining bytes", length)
	}
	start := d.off
	d.off += int(length)
	return d.data[start:d.off], nil
}

func (d *cborDecoder) takeUint(length uint64) (uint64, error) {
	data, err := d.take(length)
	if err != nil {
		return 0, err
	}
	var padded [8]byte
	copy(padded[8-len(data):], data)
	return binary.BigEndian.Uint64(padded[:]), nil
}

func compareCBORKeys(left, right []byte) int {
	if len(left) != len(right) {
		if len(left) < len(right) {
			return -1
		}
		return 1
	}
	return bytes.Compare(left, right)
}

func TestVectorRequestIDsAreCanonicalULIDs(t *testing.T) {
	vectors := loadFixture(t)
	pattern := regexp.MustCompile(`^[0-7][0-9A-HJKMNP-TV-Z]{25}$`)
	var ids []string
	for _, vector := range vectors.FrameVectors {
		ids = append(ids, vector.RequestID)
	}
	for _, vector := range vectors.IdentityIndependenceVectors {
		ids = append(ids, vector.CommandID)
		for _, attempt := range vector.Attempts {
			ids = append(ids, attempt.RequestID)
		}
	}
	sort.Strings(ids)
	for _, id := range ids {
		if !pattern.MatchString(id) {
			t.Errorf("noncanonical ULID %q", id)
		}
	}
}
