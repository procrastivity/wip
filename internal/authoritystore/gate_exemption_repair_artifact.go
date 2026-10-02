package authoritystore

import (
	"bytes"
	"net/url"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/fxamacker/cbor/v2"
	"golang.org/x/text/unicode/norm"
)

const (
	gateExemptionRepairMaxLifetime = 10 * time.Minute
)

type gateExemptionRepairBoundary struct {
	EventCount     uint64
	HighWaterEvent string
	PrefixDigest   string
}

type gateExemptionRepairBinding struct {
	CommandID   string
	RequestHash string
	RepoID      string
	NodeID      string
	Gate        string
	Boundary    gateExemptionRepairBoundary
	IncidentRef string
	Reason      string
	Evidence    []string
}

type gateExemptionRepairSubject struct {
	Schema         string   `cbor:"schema"`
	CommandID      string   `cbor:"command_id"`
	RequestHash    string   `cbor:"request_hash"`
	RepoID         string   `cbor:"repo_id"`
	NodeID         string   `cbor:"node_id"`
	Gate           string   `cbor:"gate"`
	EventCount     uint64   `cbor:"event_count"`
	HighWaterEvent string   `cbor:"high_water_event_id"`
	PrefixDigest   string   `cbor:"prefix_digest"`
	IncidentRef    string   `cbor:"incident_ref"`
	ReasonDigest   string   `cbor:"reason_digest"`
	Evidence       []string `cbor:"evidence_refs"`
}

type gateExemptionRepairAttestation struct {
	Schema        string  `cbor:"schema"`
	Action        string  `cbor:"action"`
	DomainID      string  `cbor:"domain_id"`
	CurrentEpoch  uint64  `cbor:"current_epoch"`
	NextEpoch     *uint64 `cbor:"next_epoch"`
	SubjectSchema string  `cbor:"subject_schema"`
	SubjectDigest string  `cbor:"subject_digest"`
	Subject       []byte  `cbor:"subject"`
	Nonce         []byte  `cbor:"nonce"`
	IssuedAt      string  `cbor:"issued_at"`
	ExpiresAt     string  `cbor:"expires_at"`
	LossAccepted  bool    `cbor:"loss_accepted"`
}

type verifiedGateExemptionRepairAuthorization struct {
	nonce             []byte
	issuedAt, expires time.Time
}

// parseGateExemptionRepairAuthorization verifies only the detached owner
// artifact and its binding to caller-supplied canonical command/history data.
// It deliberately performs no authority-state reads or writes and reserves no
// nonce; admission, historical boundary proof, and recovery are separate work.
func parseGateExemptionRepairAuthorization(raw []byte, domain Domain, binding gateExemptionRepairBinding, verifiedAt time.Time) (verifiedGateExemptionRepairAuthorization, error) {
	var verified verifiedGateExemptionRepairAuthorization
	if len(raw) == 0 || len(raw) > 1<<20 || validDomain(domain) != nil || verifiedAt.IsZero() || !validGateExemptionRepairArtifactEncoding(raw) {
		return verified, ErrInvalidProof
	}
	expectedSubject, err := gateExemptionRepairSubjectBytes(binding)
	if err != nil {
		return verified, ErrInvalidProof
	}
	payload, err := ownerArtifact(raw, domain.OwnerPublicKey, domain.ID, domain.OwnerKeyID,
		"owner-attestation", "wipd.owner-attestation/1", domain.ActiveEpoch)
	if err != nil {
		return verified, ErrInvalidProof
	}
	var wrapper signedArtifact
	if err = artifactDecoder.Unmarshal(raw, &wrapper); err != nil {
		return verified, ErrInvalidProof
	}
	var attestation gateExemptionRepairAttestation
	if closedPayload(payload, &attestation,
		"schema", "action", "domain_id", "current_epoch", "next_epoch", "subject_schema", "subject_digest", "subject", "nonce", "issued_at", "expires_at", "loss_accepted") != nil ||
		attestation.Schema != "wipd.owner-attestation/1" || attestation.Action != "gate-exemption-repair" ||
		attestation.DomainID != domain.ID || attestation.CurrentEpoch != domain.ActiveEpoch || attestation.NextEpoch != nil ||
		attestation.SubjectSchema != "wipd.gate-exemption-repair-subject/1" ||
		attestation.SubjectDigest != digestBytes(attestation.Subject) || len(attestation.Nonce) != 16 || attestation.LossAccepted ||
		wrapper.IssuedAt != attestation.IssuedAt {
		return verified, ErrInvalidProof
	}
	if closedPayload(attestation.Subject, new(gateExemptionRepairSubject),
		"schema", "command_id", "request_hash", "repo_id", "node_id", "gate", "event_count", "high_water_event_id", "prefix_digest", "incident_ref", "reason_digest", "evidence_refs") != nil ||
		!bytes.Equal(attestation.Subject, expectedSubject) {
		return verified, ErrInvalidProof
	}
	issued, err := utcTime(attestation.IssuedAt)
	if err != nil {
		return verified, ErrInvalidProof
	}
	expires, err := utcTime(attestation.ExpiresAt)
	if err != nil || !issued.Before(expires) || expires.Sub(issued) > gateExemptionRepairMaxLifetime {
		return verified, ErrInvalidProof
	}
	verifiedAt = verifiedAt.UTC()
	if issued.After(verifiedAt) || !verifiedAt.Before(expires) {
		return verified, ErrInvalidProof
	}
	return verifiedGateExemptionRepairAuthorization{
		nonce: append([]byte(nil), attestation.Nonce...), issuedAt: issued, expires: expires,
	}, nil
}

func validGateExemptionRepairArtifactEncoding(raw []byte) bool {
	if !canonicalArtifactCBOR(raw) {
		return false
	}
	var wrapperFields map[string]cbor.RawMessage
	var wrapper signedArtifact
	if artifactDecoder.Unmarshal(raw, &wrapperFields) != nil ||
		!rawCBORFieldEquals(wrapperFields, "key_generation", 0xf6) ||
		!rawCBORFieldEquals(wrapperFields, "artifact_sequence", 0xf6) ||
		!rawCBORFieldEquals(wrapperFields, "previous_artifact_digest", 0xf6) ||
		artifactDecoder.Unmarshal(raw, &wrapper) != nil || !canonicalArtifactCBOR(wrapper.Payload) {
		return false
	}
	var payloadFields map[string]cbor.RawMessage
	var attestation gateExemptionRepairAttestation
	if artifactDecoder.Unmarshal(wrapper.Payload, &payloadFields) != nil ||
		!rawCBORFieldEquals(payloadFields, "next_epoch", 0xf6) ||
		!rawCBORFieldEquals(payloadFields, "loss_accepted", 0xf4) ||
		artifactDecoder.Unmarshal(wrapper.Payload, &attestation) != nil {
		return false
	}
	return canonicalArtifactCBOR(attestation.Subject)
}

func canonicalArtifactCBOR(raw []byte) bool {
	if len(raw) == 0 {
		return false
	}
	var value any
	if artifactDecoder.Unmarshal(raw, &value) != nil {
		return false
	}
	encoded, err := artifactEncoder.Marshal(value)
	return err == nil && bytes.Equal(encoded, raw)
}

func rawCBORFieldEquals(fields map[string]cbor.RawMessage, key string, expected byte) bool {
	value, ok := fields[key]
	return ok && bytes.Equal(value, []byte{expected})
}

func gateExemptionRepairSubjectBytes(binding gateExemptionRepairBinding) ([]byte, error) {
	if !ulid.MatchString(binding.CommandID) || !validDigest(binding.RequestHash) ||
		!ulid.MatchString(binding.RepoID) || !ulid.MatchString(binding.NodeID) ||
		!validGateExemptionRepairGate(binding.Gate) || binding.Boundary.EventCount == 0 || binding.Boundary.EventCount > 1<<63-1 ||
		!ulid.MatchString(binding.Boundary.HighWaterEvent) || !validDigest(binding.Boundary.PrefixDigest) ||
		!validGateExemptionRepairIncidentRef(binding.IncidentRef) || !validGateExemptionRepairReason(binding.Reason) ||
		!validGateExemptionRepairEvidence(binding.Evidence) {
		return nil, ErrInvalidProof
	}
	return artifactEncoder.Marshal(gateExemptionRepairSubject{
		Schema: "wipd.gate-exemption-repair-subject/1", CommandID: binding.CommandID,
		RequestHash: binding.RequestHash, RepoID: binding.RepoID, NodeID: binding.NodeID,
		Gate: binding.Gate, EventCount: binding.Boundary.EventCount, HighWaterEvent: binding.Boundary.HighWaterEvent,
		PrefixDigest: binding.Boundary.PrefixDigest, IncidentRef: binding.IncidentRef,
		ReasonDigest: digestBytes([]byte(binding.Reason)), Evidence: binding.Evidence,
	})
}

func validGateExemptionRepairGate(value string) bool {
	return validNFCText(value, 1, 256, false)
}

func validGateExemptionRepairReason(value string) bool {
	return validNFCText(value, 1, 4096, true)
}

func validGateExemptionRepairIncidentRef(value string) bool {
	if !validNFCText(value, 1, 2048, false) || containsSpaceOrControl(value) {
		return false
	}
	parsed, err := url.Parse(value)
	if err != nil || !parsed.IsAbs() {
		return false
	}
	switch {
	case strings.EqualFold(parsed.Scheme, "https"):
		return parsed.Opaque == "" && parsed.Host != "" && parsed.Hostname() != "" && parsed.User == nil
	case strings.EqualFold(parsed.Scheme, "urn"):
		return validGateExemptionRepairURN(value)
	default:
		return false
	}
}

func validGateExemptionRepairURN(value string) bool {
	if len(value) < len("urn:") || !strings.EqualFold(value[:len("urn:")], "urn:") {
		return false
	}
	nid, assignedName, found := strings.Cut(value[len("urn:"):], ":")
	if !found || len(nid) < 2 || len(nid) > 32 || assignedName == "" {
		return false
	}
	for index := 0; index < len(nid); index++ {
		char := nid[index]
		if !urnAlphaNumeric(char) && (char != '-' || index == 0 || index == len(nid)-1) {
			return false
		}
	}

	assignedEnd := strings.IndexAny(assignedName, "?#")
	nss, optional := assignedName, ""
	if assignedEnd >= 0 {
		nss, optional = assignedName[:assignedEnd], assignedName[assignedEnd:]
	}
	if !validURNComponentWithPCharPrefix(nss, true, false) {
		return false
	}
	return validURNOptionalComponents(optional)
}

func validURNOptionalComponents(optional string) bool {
	if optional == "" {
		return true
	}
	if strings.HasPrefix(optional, "#") {
		return validURNComponent(optional[1:], true, true)
	}
	if strings.HasPrefix(optional, "?=") {
		return validURNQComponent(optional[2:])
	}
	if !strings.HasPrefix(optional, "?+") {
		return false
	}
	remainder := optional[2:]
	end := len(remainder)
	if index := strings.Index(remainder, "?="); index >= 0 && index < end {
		end = index
	}
	if index := strings.IndexByte(remainder, '#'); index >= 0 && index < end {
		end = index
	}
	if !validURNComponentWithPCharPrefix(remainder[:end], true, true) {
		return false
	}
	trailing := remainder[end:]
	if trailing == "" {
		return true
	}
	if strings.HasPrefix(trailing, "?=") {
		return validURNQComponent(trailing[2:])
	}
	if strings.HasPrefix(trailing, "#") {
		return validURNComponent(trailing[1:], true, true)
	}
	return false
}

func validURNQComponent(value string) bool {
	query, fragment, hasFragment := strings.Cut(value, "#")
	if !validURNComponentWithPCharPrefix(query, true, true) {
		return false
	}
	return !hasFragment || validURNComponent(fragment, true, true)
}

func validURNComponentWithPCharPrefix(value string, allowSlash, allowQuestion bool) bool {
	prefixLength, ok := urnPCharPrefix(value)
	return ok && validURNComponent(value[prefixLength:], allowSlash, allowQuestion)
}

func urnPCharPrefix(value string) (int, bool) {
	if len(value) == 0 {
		return 0, false
	}
	if value[0] == '%' {
		if len(value) < 3 || !urnHex(value[1]) || !urnHex(value[2]) {
			return 0, false
		}
		return 3, true
	}
	return 1, urnPChar(value[0])
}

func validURNComponent(value string, allowSlash, allowQuestion bool) bool {
	for index := 0; index < len(value); {
		char := value[index]
		if char == '%' {
			if index+2 >= len(value) || !urnHex(value[index+1]) || !urnHex(value[index+2]) {
				return false
			}
			index += 3
			continue
		}
		if urnPChar(char) || (allowSlash && char == '/') || (allowQuestion && char == '?') {
			index++
			continue
		}
		return false
	}
	return true
}

func urnPChar(char byte) bool {
	return urnAlphaNumeric(char) || strings.ContainsRune("-._~!$&'()*+,;=:@", rune(char))
}

func urnAlphaNumeric(char byte) bool {
	return char >= 'a' && char <= 'z' || char >= 'A' && char <= 'Z' || char >= '0' && char <= '9'
}

func urnHex(char byte) bool {
	return char >= '0' && char <= '9' || char >= 'a' && char <= 'f' || char >= 'A' && char <= 'F'
}

func validGateExemptionRepairEvidence(values []string) bool {
	if len(values) < 1 || len(values) > 32 {
		return false
	}
	for index, value := range values {
		if !validDigest(value) || (index > 0 && values[index-1] >= value) {
			return false
		}
	}
	return true
}

func validNFCText(value string, minBytes, maxBytes int, allowControls bool) bool {
	if !utf8.ValidString(value) || len(value) < minBytes || len(value) > maxBytes || !norm.NFC.IsNormalString(value) {
		return false
	}
	if !allowControls {
		for _, char := range value {
			if unicode.IsControl(char) {
				return false
			}
		}
	}
	return true
}

func containsSpaceOrControl(value string) bool {
	for _, char := range value {
		if unicode.IsSpace(char) || unicode.IsControl(char) {
			return true
		}
	}
	return false
}
