package authoritystore

import (
	"bytes"
	"crypto/ed25519"
	"errors"
	"fmt"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/fxamacker/cbor/v2"
	"github.com/procrastivity/wip/internal/operation"
)

type gateRepairArtifactFixture struct {
	domain     Domain
	owner      ed25519.PrivateKey
	binding    gateExemptionRepairBinding
	verifiedAt time.Time
}

func gateRepairFixture(t *testing.T) gateRepairArtifactFixture {
	t.Helper()
	domain, owner := identity(domainA, 7)
	return gateRepairArtifactFixture{
		domain: domain, owner: owner,
		binding: gateExemptionRepairBinding{
			CommandID: claimTestID(801), RequestHash: digestBytes([]byte("canonical command")),
			RepoID: repoA, NodeID: claimTestID(802), Gate: "verified",
			Boundary: gateExemptionRepairBoundary{
				EventCount: 42, HighWaterEvent: claimTestID(803), PrefixDigest: digestBytes([]byte("inclusive declaration prefix")),
			},
			IncidentRef: "https://incident.invalid/ticket/123", Reason: "Owner-approved prospective exemption",
			Evidence: []string{"sha256:" + strings.Repeat("0", 64), "sha256:" + strings.Repeat("f", 64)},
		},
		verifiedAt: time.Date(2026, 9, 23, 12, 0, 0, 0, time.UTC),
	}
}

func gateRepairSubjectFields(binding gateExemptionRepairBinding) map[string]any {
	return map[string]any{
		"schema": "wipd.gate-exemption-repair-subject/1", "command_id": binding.CommandID,
		"request_hash": binding.RequestHash, "repo_id": binding.RepoID, "node_id": binding.NodeID,
		"gate": binding.Gate, "event_count": binding.Boundary.EventCount,
		"high_water_event_id": binding.Boundary.HighWaterEvent, "prefix_digest": binding.Boundary.PrefixDigest,
		"incident_ref": binding.IncidentRef, "reason_digest": digestBytes([]byte(binding.Reason)),
		"evidence_refs": append([]string(nil), binding.Evidence...),
	}
}

func signGateRepairArtifact(t *testing.T, fixture gateRepairArtifactFixture, subject []byte,
	issuedAt, expiresAt time.Time, mutatePayload, mutateWrapper func(map[string]any),
) []byte {
	t.Helper()
	issued := issuedAt.UTC().Format(time.RFC3339Nano)
	payloadFields := map[string]any{
		"schema": "wipd.owner-attestation/1", "action": "gate-exemption-repair",
		"domain_id": fixture.domain.ID, "current_epoch": fixture.domain.ActiveEpoch, "next_epoch": nil,
		"subject_schema": "wipd.gate-exemption-repair-subject/1", "subject_digest": digestBytes(subject),
		"subject": subject, "nonce": bytes.Repeat([]byte{0x37}, 16),
		"issued_at": issued, "expires_at": expiresAt.UTC().Format(time.RFC3339Nano), "loss_accepted": false,
	}
	if mutatePayload != nil {
		mutatePayload(payloadFields)
	}
	payload := encodeTest(t, payloadFields)
	wrapperFields := map[string]any{
		"schema": "wipd.signed-artifact/1", "kind": "owner-attestation", "domain_id": fixture.domain.ID,
		"authority_epoch": fixture.domain.ActiveEpoch, "signer_role": "owner", "signer_key_id": fixture.domain.OwnerKeyID,
		"key_generation": nil, "artifact_sequence": nil, "previous_artifact_digest": nil,
		"issued_at": issued, "payload_schema": "wipd.owner-attestation/1",
		"payload_digest": digestBytes(payload), "payload": payload,
	}
	if mutateWrapper != nil {
		mutateWrapper(wrapperFields)
	}
	preimage := append([]byte("wipd/signed-artifact/v1\x00"), encodeTest(t, wrapperFields)...)
	wrapperFields["signature"] = ed25519.Sign(fixture.owner, preimage)
	return encodeTest(t, wrapperFields)
}

func signGateRepairBinding(t *testing.T, fixture gateRepairArtifactFixture, binding gateExemptionRepairBinding,
	issuedAt, expiresAt time.Time, mutateSubject, mutatePayload, mutateWrapper func(map[string]any),
) []byte {
	t.Helper()
	subjectFields := gateRepairSubjectFields(binding)
	if mutateSubject != nil {
		mutateSubject(subjectFields)
	}
	return signGateRepairArtifact(t, fixture, encodeTest(t, subjectFields), issuedAt, expiresAt, mutatePayload, mutateWrapper)
}

func signDefaultGateRepairBinding(t *testing.T, fixture gateRepairArtifactFixture,
	mutateSubject, mutatePayload, mutateWrapper func(map[string]any),
) []byte {
	t.Helper()
	return signGateRepairBinding(t, fixture, fixture.binding, fixture.verifiedAt.Add(-time.Minute), fixture.verifiedAt.Add(time.Minute), mutateSubject, mutatePayload, mutateWrapper)
}

func TestGateExemptionRepairArtifactAcceptsValidDetachedOwnerAttestation(t *testing.T) {
	fixture := gateRepairFixture(t)
	raw := signDefaultGateRepairBinding(t, fixture, nil, nil, nil)
	verified, err := parseGateExemptionRepairAuthorization(raw, fixture.domain, fixture.binding, fixture.verifiedAt)
	if err != nil {
		t.Fatalf("verify valid owner-root exemption repair: %v", err)
	}
	if !bytes.Equal(verified.nonce, bytes.Repeat([]byte{0x37}, 16)) ||
		!verified.issuedAt.Equal(fixture.verifiedAt.Add(-time.Minute)) ||
		!verified.expires.Equal(fixture.verifiedAt.Add(time.Minute)) {
		t.Fatalf("verified artifact fields: nonce=%x issued=%s expires=%s", verified.nonce, verified.issuedAt, verified.expires)
	}
	// Evidence references are opaque hashes. No evidence bytes or provider
	// lookup are required for this pure authorization check.
	if len(fixture.binding.Evidence) != 2 || fixture.binding.Evidence[0] != "sha256:"+strings.Repeat("0", 64) {
		t.Fatalf("fixture no longer demonstrates reference-only evidence: %v", fixture.binding.Evidence)
	}
}

func TestGateExemptionRepairArtifactRejectsWrongVersionsActionsAndUnknownFields(t *testing.T) {
	fixture := gateRepairFixture(t)
	tests := []struct {
		name          string
		mutateSubject func(map[string]any)
		mutatePayload func(map[string]any)
		mutateWrapper func(map[string]any)
	}{
		{name: "wrapper version", mutateWrapper: func(fields map[string]any) { fields["schema"] = "wipd.signed-artifact/2" }},
		{name: "unknown wrapper field", mutateWrapper: func(fields map[string]any) { fields["unknown"] = "closed" }},
		{name: "wrapper kind", mutateWrapper: func(fields map[string]any) { fields["kind"] = "different" }},
		{name: "wrapper signer role", mutateWrapper: func(fields map[string]any) { fields["signer_role"] = "authority" }},
		{name: "wrapper signer key", mutateWrapper: func(fields map[string]any) { fields["signer_key_id"] = digestBytes([]byte("different owner root")) }},
		{name: "non-null key generation", mutateWrapper: func(fields map[string]any) { fields["key_generation"] = uint64(1) }},
		{name: "non-null artifact sequence", mutateWrapper: func(fields map[string]any) { fields["artifact_sequence"] = uint64(1) }},
		{name: "non-null predecessor", mutateWrapper: func(fields map[string]any) { fields["previous_artifact_digest"] = digestBytes([]byte("predecessor")) }},
		{name: "wrapper payload version", mutateWrapper: func(fields map[string]any) { fields["payload_schema"] = "wipd.owner-attestation/2" }},
		{name: "attestation version", mutatePayload: func(fields map[string]any) { fields["schema"] = "wipd.owner-attestation/2" }},
		{name: "unknown attestation field", mutatePayload: func(fields map[string]any) { fields["unknown"] = true }},
		{name: "wrong action", mutatePayload: func(fields map[string]any) { fields["action"] = "claim-stand-down" }},
		{name: "wrong domain", mutatePayload: func(fields map[string]any) { fields["domain_id"] = domainB }},
		{name: "wrong epoch", mutatePayload: func(fields map[string]any) { fields["current_epoch"] = uint64(8) }},
		{name: "non-null next epoch", mutatePayload: func(fields map[string]any) { fields["next_epoch"] = uint64(8) }},
		{name: "wrong subject version", mutatePayload: func(fields map[string]any) { fields["subject_schema"] = "wipd.gate-exemption-repair-subject/2" }},
		{name: "loss flag", mutatePayload: func(fields map[string]any) { fields["loss_accepted"] = true }},
		{name: "wrong subject digest", mutatePayload: func(fields map[string]any) { fields["subject_digest"] = digestBytes([]byte("not this subject")) }},
		{name: "wrong nonce length", mutatePayload: func(fields map[string]any) { fields["nonce"] = []byte{1, 2, 3} }},
		{name: "unknown subject field", mutateSubject: func(fields map[string]any) { fields["unknown"] = "closed" }},
		{name: "subject version", mutateSubject: func(fields map[string]any) { fields["schema"] = "wipd.gate-exemption-repair-subject/2" }},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			raw := signDefaultGateRepairBinding(t, fixture, test.mutateSubject, test.mutatePayload, test.mutateWrapper)
			if _, err := parseGateExemptionRepairAuthorization(raw, fixture.domain, fixture.binding, fixture.verifiedAt); !errors.Is(err, ErrInvalidProof) {
				t.Fatalf("invalid signed artifact accepted: %v", err)
			}
		})
	}
}

func TestGateExemptionRepairArtifactRejectsCanonicalSubjectMismatches(t *testing.T) {
	fixture := gateRepairFixture(t)
	tests := []struct {
		name   string
		mutate func(map[string]any)
	}{
		{name: "command ID", mutate: func(fields map[string]any) { fields["command_id"] = claimTestID(804) }},
		{name: "request hash", mutate: func(fields map[string]any) { fields["request_hash"] = digestBytes([]byte("different command")) }},
		{name: "Repo", mutate: func(fields map[string]any) { fields["repo_id"] = repoB }},
		{name: "node", mutate: func(fields map[string]any) { fields["node_id"] = claimTestID(805) }},
		{name: "gate", mutate: func(fields map[string]any) { fields["gate"] = "unregistered-gate" }},
		{name: "declaration event count", mutate: func(fields map[string]any) { fields["event_count"] = uint64(43) }},
		{name: "declaration high water event", mutate: func(fields map[string]any) { fields["high_water_event_id"] = claimTestID(806) }},
		{name: "declaration prefix", mutate: func(fields map[string]any) { fields["prefix_digest"] = digestBytes([]byte("different prefix")) }},
		{name: "incident reference", mutate: func(fields map[string]any) { fields["incident_ref"] = "https://incident.invalid/other" }},
		{name: "reason digest", mutate: func(fields map[string]any) { fields["reason_digest"] = digestBytes([]byte("different reason")) }},
		{name: "evidence references", mutate: func(fields map[string]any) { fields["evidence_refs"] = []string{"sha256:" + strings.Repeat("1", 64)} }},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			raw := signDefaultGateRepairBinding(t, fixture, test.mutate, nil, nil)
			if _, err := parseGateExemptionRepairAuthorization(raw, fixture.domain, fixture.binding, fixture.verifiedAt); !errors.Is(err, ErrInvalidProof) {
				t.Fatalf("proof bound to different %s accepted: %v", test.name, err)
			}
		})
	}
}

func TestGateExemptionRepairArtifactRejectsBadSignatureAndMalformedCBOR(t *testing.T) {
	fixture := gateRepairFixture(t)
	valid := signDefaultGateRepairBinding(t, fixture, nil, nil, nil)
	tests := []struct {
		name string
		raw  []byte
	}{
		{name: "bad owner-root signature", raw: mutateGateRepairSignature(t, valid)},
		{name: "noncanonical wrapper map", raw: reverseCanonicalStringMap(t, valid)},
		{name: "duplicate wrapper key", raw: duplicateStringMapKey(t, valid, "kind")},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if _, err := parseGateExemptionRepairAuthorization(test.raw, fixture.domain, fixture.binding, fixture.verifiedAt); !errors.Is(err, ErrInvalidProof) {
				t.Fatalf("malformed artifact accepted: %v", err)
			}
		})
	}

	baseSubject := encodeTest(t, gateRepairSubjectFields(fixture.binding))
	for _, test := range []struct {
		name string
		make func() []byte
	}{
		{name: "noncanonical subject map", make: func() []byte { return reverseCanonicalStringMap(t, baseSubject) }},
		{name: "duplicate subject field", make: func() []byte { return duplicateStringMapKey(t, baseSubject, "gate") }},
	} {
		t.Run(test.name, func(t *testing.T) {
			raw := signGateRepairArtifact(t, fixture, test.make(), fixture.verifiedAt.Add(-time.Minute), fixture.verifiedAt.Add(time.Minute), nil, nil)
			if _, err := parseGateExemptionRepairAuthorization(raw, fixture.domain, fixture.binding, fixture.verifiedAt); !errors.Is(err, ErrInvalidProof) {
				t.Fatalf("malformed subject CBOR accepted: %v", err)
			}
		})
	}

	oversized := append(append([]byte(nil), valid...), bytes.Repeat([]byte{0}, (1<<20)+1)...)
	if _, err := parseGateExemptionRepairAuthorization(oversized, fixture.domain, fixture.binding, fixture.verifiedAt); !errors.Is(err, ErrInvalidProof) {
		t.Fatalf("oversized artifact accepted: %v", err)
	}
}

func mutateGateRepairSignature(t *testing.T, raw []byte) []byte {
	t.Helper()
	var fields map[string]any
	if err := artifactDecoder.Unmarshal(raw, &fields); err != nil {
		t.Fatal(err)
	}
	signature, ok := fields["signature"].([]byte)
	if !ok || len(signature) == 0 {
		t.Fatalf("artifact signature missing: %T", fields["signature"])
	}
	signature[0] ^= 0x80
	fields["signature"] = signature
	return encodeTest(t, fields)
}

func reverseCanonicalStringMap(t *testing.T, raw []byte) []byte {
	t.Helper()
	var fields map[string]cbor.RawMessage
	if err := artifactDecoder.Unmarshal(raw, &fields); err != nil {
		t.Fatal(err)
	}
	keys := make([]string, 0, len(fields))
	for key := range fields {
		keys = append(keys, key)
	}
	sort.Slice(keys, func(i, j int) bool {
		left, right := encodeTest(t, keys[i]), encodeTest(t, keys[j])
		if len(left) != len(right) {
			return len(left) < len(right)
		}
		return bytes.Compare(left, right) < 0
	})
	for left, right := 0, len(keys)-1; left < right; left, right = left+1, right-1 {
		keys[left], keys[right] = keys[right], keys[left]
	}
	return encodeRawStringMap(t, fields, keys)
}

func duplicateStringMapKey(t *testing.T, raw []byte, duplicate string) []byte {
	t.Helper()
	var fields map[string]cbor.RawMessage
	if err := artifactDecoder.Unmarshal(raw, &fields); err != nil {
		t.Fatal(err)
	}
	value, ok := fields[duplicate]
	if !ok || len(raw) == 0 || raw[0]>>5 != 5 || raw[0]&0x1f >= 23 {
		t.Fatalf("cannot duplicate map key %q in fixture", duplicate)
	}
	keyBytes := encodeTest(t, duplicate)
	result := []byte{raw[0] + 1}
	result = append(result, keyBytes...)
	result = append(result, value...)
	return append(result, raw[1:]...)
}

func encodeRawStringMap(t *testing.T, fields map[string]cbor.RawMessage, order []string) []byte {
	t.Helper()
	if len(order) > 23 {
		t.Fatalf("test raw map helper supports at most 23 fields, got %d", len(order))
	}
	result := []byte{0xa0 | byte(len(order))}
	for _, key := range order {
		value, ok := fields[key]
		if !ok {
			t.Fatalf("raw map field %q missing", key)
		}
		result = append(result, encodeTest(t, key)...)
		result = append(result, value...)
	}
	return result
}

func TestGateExemptionRepairArtifactEnforcesFreshnessWithoutClockSkew(t *testing.T) {
	fixture := gateRepairFixture(t)
	for _, test := range []struct {
		name       string
		issued     time.Time
		expires    time.Time
		mutateWrap func(map[string]any)
		wantValid  bool
	}{
		{name: "issue equals verification and maximum lifetime", issued: fixture.verifiedAt, expires: fixture.verifiedAt.Add(10 * time.Minute), wantValid: true},
		{name: "expiry strictly after verification", issued: fixture.verifiedAt.Add(-time.Minute), expires: fixture.verifiedAt.Add(time.Nanosecond), wantValid: true},
		{name: "issue one nanosecond in future", issued: fixture.verifiedAt.Add(time.Nanosecond), expires: fixture.verifiedAt.Add(time.Minute)},
		{name: "expiry equals verification", issued: fixture.verifiedAt.Add(-time.Minute), expires: fixture.verifiedAt},
		{name: "lifetime one nanosecond over ten minutes", issued: fixture.verifiedAt.Add(-10 * time.Minute), expires: fixture.verifiedAt.Add(time.Nanosecond)},
		{name: "zero lifetime", issued: fixture.verifiedAt, expires: fixture.verifiedAt},
		{name: "wrapper and payload issue times differ", issued: fixture.verifiedAt.Add(-time.Minute), expires: fixture.verifiedAt.Add(time.Minute), mutateWrap: func(fields map[string]any) {
			fields["issued_at"] = fixture.verifiedAt.Add(-59 * time.Second).Format(time.RFC3339Nano)
		}},
	} {
		t.Run(test.name, func(t *testing.T) {
			raw := signGateRepairBinding(t, fixture, fixture.binding, test.issued, test.expires, nil, nil, test.mutateWrap)
			_, err := parseGateExemptionRepairAuthorization(raw, fixture.domain, fixture.binding, fixture.verifiedAt)
			if test.wantValid && err != nil {
				t.Fatalf("valid freshness boundary rejected: %v", err)
			}
			if !test.wantValid && !errors.Is(err, ErrInvalidProof) {
				t.Fatalf("freshness violation accepted: %v", err)
			}
		})
	}
}

func TestGateExemptionRepairArtifactEnforcesTextReferenceAndBoundaryBounds(t *testing.T) {
	fixture := gateRepairFixture(t)
	baseProof := signDefaultGateRepairBinding(t, fixture, nil, nil, nil)
	invalid := []struct {
		name   string
		mutate func(*gateExemptionRepairBinding)
	}{
		{name: "gate too long", mutate: func(binding *gateExemptionRepairBinding) { binding.Gate = strings.Repeat("g", 257) }},
		{name: "gate control", mutate: func(binding *gateExemptionRepairBinding) { binding.Gate = "verified\n" }},
		{name: "gate not NFC", mutate: func(binding *gateExemptionRepairBinding) { binding.Gate = "e\u0301" }},
		{name: "reason empty", mutate: func(binding *gateExemptionRepairBinding) { binding.Reason = "" }},
		{name: "reason too long", mutate: func(binding *gateExemptionRepairBinding) { binding.Reason = strings.Repeat("r", 4097) }},
		{name: "reason not NFC", mutate: func(binding *gateExemptionRepairBinding) { binding.Reason = "e\u0301" }},
		{name: "incident too long", mutate: func(binding *gateExemptionRepairBinding) {
			binding.IncidentRef = "https://x/" + strings.Repeat("p", 2040)
		}},
		{name: "incident non-NFC", mutate: func(binding *gateExemptionRepairBinding) { binding.IncidentRef = "https://incident.invalid/cafe\u0301" }},
		{name: "incident whitespace", mutate: func(binding *gateExemptionRepairBinding) { binding.IncidentRef = "https://incident.invalid/a b" }},
		{name: "incident control", mutate: func(binding *gateExemptionRepairBinding) { binding.IncidentRef = "https://incident.invalid/a\n" }},
		{name: "incident userinfo", mutate: func(binding *gateExemptionRepairBinding) { binding.IncidentRef = "https://user@incident.invalid/a" }},
		{name: "incident empty HTTPS host", mutate: func(binding *gateExemptionRepairBinding) { binding.IncidentRef = "https:///path" }},
		{name: "incident non-HTTPS URI", mutate: func(binding *gateExemptionRepairBinding) { binding.IncidentRef = "http://incident.invalid/a" }},
		{name: "invalid URN", mutate: func(binding *gateExemptionRepairBinding) { binding.IncidentRef = "urn:x:" }},
		{name: "boundary count zero", mutate: func(binding *gateExemptionRepairBinding) { binding.Boundary.EventCount = 0 }},
		{name: "boundary count beyond SQLite range", mutate: func(binding *gateExemptionRepairBinding) { binding.Boundary.EventCount = uint64(1) << 63 }},
		{name: "boundary event ID", mutate: func(binding *gateExemptionRepairBinding) { binding.Boundary.HighWaterEvent = "not-an-event" }},
		{name: "boundary prefix digest", mutate: func(binding *gateExemptionRepairBinding) { binding.Boundary.PrefixDigest = "sha256:bad" }},
		{name: "evidence absent", mutate: func(binding *gateExemptionRepairBinding) { binding.Evidence = nil }},
		{name: "evidence over limit", mutate: func(binding *gateExemptionRepairBinding) {
			binding.Evidence = make([]string, 33)
			for i := range binding.Evidence {
				binding.Evidence[i] = fmt.Sprintf("sha256:%064x", i)
			}
		}},
		{name: "evidence duplicate", mutate: func(binding *gateExemptionRepairBinding) {
			binding.Evidence = []string{fixture.binding.Evidence[0], fixture.binding.Evidence[0]}
		}},
		{name: "evidence unsorted", mutate: func(binding *gateExemptionRepairBinding) {
			binding.Evidence = []string{fixture.binding.Evidence[1], fixture.binding.Evidence[0]}
		}},
		{name: "evidence uppercase digest", mutate: func(binding *gateExemptionRepairBinding) {
			binding.Evidence = []string{"sha256:" + strings.Repeat("A", 64)}
		}},
		{name: "command ID", mutate: func(binding *gateExemptionRepairBinding) { binding.CommandID = "bad" }},
		{name: "request hash", mutate: func(binding *gateExemptionRepairBinding) { binding.RequestHash = "sha256:bad" }},
		{name: "Repo ID", mutate: func(binding *gateExemptionRepairBinding) { binding.RepoID = "bad" }},
		{name: "node ID", mutate: func(binding *gateExemptionRepairBinding) { binding.NodeID = "bad" }},
	}
	for _, test := range invalid {
		t.Run(test.name, func(t *testing.T) {
			binding := fixture.binding
			binding.Evidence = append([]string(nil), fixture.binding.Evidence...)
			test.mutate(&binding)
			if _, err := parseGateExemptionRepairAuthorization(baseProof, fixture.domain, binding, fixture.verifiedAt); !errors.Is(err, ErrInvalidProof) {
				t.Fatalf("out-of-contract binding accepted: %v", err)
			}
		})
	}

	maxBinding := fixture.binding
	maxBinding.Gate = strings.Repeat("g", 256)
	maxBinding.Reason = strings.Repeat("r", 4096)
	maxBinding.IncidentRef = "https://x/" + strings.Repeat("p", 2038)
	maxBinding.Evidence = make([]string, 32)
	for i := range maxBinding.Evidence {
		maxBinding.Evidence[i] = fmt.Sprintf("sha256:%064x", i)
	}
	maxBinding.Boundary.EventCount = uint64(1<<63 - 1)
	maxProof := signGateRepairBinding(t, fixture, maxBinding, fixture.verifiedAt, fixture.verifiedAt.Add(10*time.Minute), nil, nil, nil)
	if _, err := parseGateExemptionRepairAuthorization(maxProof, fixture.domain, maxBinding, fixture.verifiedAt); err != nil {
		t.Fatalf("valid inclusive field and reference bounds rejected: %v", err)
	}
}

func TestGateExemptionRepairArtifactAcceptsHTTPSAndURNReferences(t *testing.T) {
	fixture := gateRepairFixture(t)
	for _, reference := range []string{"https://incident.invalid/root/issue-42", "urn:example:incident:issue-42"} {
		t.Run(reference, func(t *testing.T) {
			binding := fixture.binding
			binding.IncidentRef = reference
			raw := signGateRepairBinding(t, fixture, binding, fixture.verifiedAt, fixture.verifiedAt.Add(10*time.Minute), nil, nil, nil)
			if _, err := parseGateExemptionRepairAuthorization(raw, fixture.domain, binding, fixture.verifiedAt); err != nil {
				t.Fatalf("valid incident reference rejected: %v", err)
			}
		})
	}
	t.Run("NFC reason bytes", func(t *testing.T) {
		binding := fixture.binding
		binding.Reason = "Owner approved café exemption"
		raw := signGateRepairBinding(t, fixture, binding, fixture.verifiedAt, fixture.verifiedAt.Add(10*time.Minute), nil, nil, nil)
		if _, err := parseGateExemptionRepairAuthorization(raw, fixture.domain, binding, fixture.verifiedAt); err != nil {
			t.Fatalf("NFC reason with exact-byte digest rejected: %v", err)
		}
	})
}

func TestGateExemptionRepairProofRemainsDetachedFromCommandHash(t *testing.T) {
	fixture := gateRepairFixture(t)
	commandID := claimTestID(811)
	command := operation.Command{
		ID: commandID, AuthorityDomainID: fixture.domain.ID, ExpectedAuthorityEpoch: fixture.domain.ActiveEpoch,
		EnvironmentID: envA, EnvironmentSequence: 7, ActedAt: fixture.verifiedAt.Format(time.RFC3339Nano), CorrelationCommandID: commandID,
		Request: operation.Request{
			Operation: operation.GateCloseV1.Metadata().Operation, Actor: "human",
			Context: operation.Context{Repo: repoA, Clone: claimTestID(812), Worktree: claimTestID(813)},
			Claim:   &operation.ClaimContext{ID: claimTestID(814), Epoch: "1"},
			Input:   operation.GateCloseInput{Gate: fixture.binding.Gate, NodeID: fixture.binding.NodeID},
		},
	}
	canonicalBefore, err := command.CanonicalBytes()
	if err != nil {
		t.Fatal(err)
	}
	hashBefore, err := command.RequestHash()
	if err != nil {
		t.Fatal(err)
	}
	binding := fixture.binding
	binding.CommandID, binding.RequestHash = command.ID, hashBefore
	proof := signGateRepairBinding(t, fixture, binding, fixture.verifiedAt, fixture.verifiedAt.Add(10*time.Minute), nil, nil, nil)
	// No repair operation or submit-v2 transport exists yet. Validate the proof
	// separately against the final command identity without extending its bytes.
	if _, err = parseGateExemptionRepairAuthorization(proof, fixture.domain, binding, fixture.verifiedAt); err != nil {
		t.Fatalf("verify detached proof bound to canonical request hash: %v", err)
	}
	canonicalAfter, err := command.CanonicalBytes()
	if err != nil {
		t.Fatal(err)
	}
	hashAfter, err := command.RequestHash()
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(canonicalBefore, canonicalAfter) || hashBefore != hashAfter || bytes.Contains(canonicalAfter, proof) {
		t.Fatal("detached authorization bytes changed or entered canonical command identity")
	}
}
