package wipdauthority

import (
	"bytes"
	"crypto/ed25519"
	"crypto/sha256"
	"crypto/x509"
	"encoding/hex"
	"errors"
	"fmt"
	"time"

	"github.com/fxamacker/cbor/v2"
)

var (
	caDelegationEncoder, _ = cbor.CoreDetEncOptions().EncMode()
	caDelegationDecoder, _ = (cbor.DecOptions{DupMapKey: cbor.DupMapKeyEnforcedAPF, IndefLength: cbor.IndefLengthForbidden, TagsMd: cbor.TagsForbidden, MaxMapPairs: 64, MaxArrayElements: 64}).DecMode()
)

// ErrInvalidEnvironmentCADelegation means the owner-signed CA delegation is
// malformed, untrusted, outside its validity interval, or bound to another
// authority identity.
var ErrInvalidEnvironmentCADelegation = errors.New("wipdauthority: invalid Environment CA delegation")

type caDelegationArtifact struct {
	Schema        string  `cbor:"schema"`
	Kind          string  `cbor:"kind"`
	DomainID      string  `cbor:"domain_id"`
	Epoch         uint64  `cbor:"authority_epoch"`
	SignerRole    string  `cbor:"signer_role"`
	SignerKeyID   string  `cbor:"signer_key_id"`
	Generation    *uint64 `cbor:"key_generation"`
	Sequence      *uint64 `cbor:"artifact_sequence"`
	Predecessor   *string `cbor:"previous_artifact_digest"`
	IssuedAt      string  `cbor:"issued_at"`
	PayloadSchema string  `cbor:"payload_schema"`
	PayloadDigest string  `cbor:"payload_digest"`
	Payload       []byte  `cbor:"payload"`
	Signature     []byte  `cbor:"signature"`
}

type caDelegationRecord struct {
	Schema      string `cbor:"schema"`
	DomainID    string `cbor:"domain_id"`
	Epoch       uint64 `cbor:"authority_epoch"`
	OwnerKeyID  string `cbor:"owner_key_id"`
	Generation  uint64 `cbor:"ca_generation"`
	KeyID       string `cbor:"ca_key_id"`
	Certificate []byte `cbor:"ca_certificate_der"`
	NotBefore   string `cbor:"not_before"`
	NotAfter    string `cbor:"not_after"`
}

// VerifyEnvironmentCADelegation verifies the retained owner's exact signed
// delegation and returns its delegated CA certificate for client-side leaf
// validation. It performs no store access or mutation.
func VerifyEnvironmentCADelegation(profile Profile, ownerRoot ed25519.PublicKey, wrapper []byte, now time.Time) ([]byte, error) {
	if profile.validate() != nil || len(ownerRoot) != ed25519.PublicKeySize || len(wrapper) == 0 || len(wrapper) > 1<<20 || now.IsZero() {
		return nil, ErrInvalidEnvironmentCADelegation
	}
	ownerSPKI, err := x509.MarshalPKIXPublicKey(ownerRoot)
	if err != nil || digestSPKI(ownerSPKI) != profile.OwnerRootSPKI() {
		return nil, ErrInvalidEnvironmentCADelegation
	}
	var fields map[string]any
	if err = caDelegationDecoder.Unmarshal(wrapper, &fields); err != nil || !exactCBORKeys(fields,
		"schema", "kind", "domain_id", "authority_epoch", "signer_role", "signer_key_id", "key_generation", "artifact_sequence",
		"previous_artifact_digest", "issued_at", "payload_schema", "payload_digest", "payload", "signature") || !canonicalCBOR(wrapper, fields) {
		return nil, ErrInvalidEnvironmentCADelegation
	}
	var artifact caDelegationArtifact
	if err = caDelegationDecoder.Unmarshal(wrapper, &artifact); err != nil || artifact.Schema != "wipd.signed-artifact/1" ||
		artifact.Kind != "environment-ca-delegation" || artifact.DomainID != profile.domainID || artifact.Epoch != profile.epoch ||
		artifact.SignerRole != "owner" || artifact.SignerKeyID != profile.OwnerRootSPKI() || artifact.Generation != nil || artifact.Sequence != nil ||
		artifact.Predecessor != nil || artifact.PayloadSchema != "wipd.environment-ca-delegation/1" ||
		artifact.PayloadDigest != digestBytes(artifact.Payload) || len(artifact.Signature) != ed25519.SignatureSize || !validCanonicalUTC(artifact.IssuedAt) {
		return nil, ErrInvalidEnvironmentCADelegation
	}
	delete(fields, "signature")
	unsigned, err := caDelegationEncoder.Marshal(fields)
	if err != nil || !ed25519.Verify(ownerRoot, append([]byte("wipd/signed-artifact/v1\x00"), unsigned...), artifact.Signature) {
		return nil, ErrInvalidEnvironmentCADelegation
	}
	var payloadFields map[string]any
	if err = caDelegationDecoder.Unmarshal(artifact.Payload, &payloadFields); err != nil || !exactCBORKeys(payloadFields,
		"schema", "domain_id", "authority_epoch", "owner_key_id", "ca_generation", "ca_key_id", "ca_certificate_der", "not_before", "not_after") || !canonicalCBOR(artifact.Payload, payloadFields) {
		return nil, ErrInvalidEnvironmentCADelegation
	}
	var delegation caDelegationRecord
	if err = caDelegationDecoder.Unmarshal(artifact.Payload, &delegation); err != nil || delegation.Schema != "wipd.environment-ca-delegation/1" ||
		delegation.DomainID != profile.domainID || delegation.Epoch != profile.epoch || delegation.OwnerKeyID != profile.OwnerRootSPKI() || delegation.Generation != 1 ||
		!validCanonicalUTC(delegation.NotBefore) || !validCanonicalUTC(delegation.NotAfter) {
		return nil, ErrInvalidEnvironmentCADelegation
	}
	ca, err := x509.ParseCertificate(delegation.Certificate)
	if err != nil || !ca.IsCA || !ca.BasicConstraintsValid || !ca.MaxPathLenZero || ca.MaxPathLen != 0 || ca.KeyUsage != x509.KeyUsageCertSign ||
		!hasCriticalExtension(ca, []int{2, 5, 29, 19}) || !hasCriticalExtension(ca, []int{2, 5, 29, 15}) ||
		len(ca.ExtKeyUsage) != 0 || len(ca.UnknownExtKeyUsage) != 0 || len(ca.UnhandledCriticalExtensions) != 0 || hasSANExtension(ca) ||
		!bytes.Equal(ca.RawSubject, ca.RawIssuer) || ca.CheckSignatureFrom(ca) != nil || !validCanonicalUTC(delegation.NotBefore) || !validCanonicalUTC(delegation.NotAfter) ||
		!ca.NotBefore.Before(ca.NotAfter) {
		return nil, ErrInvalidEnvironmentCADelegation
	}
	if _, ok := ca.PublicKey.(ed25519.PublicKey); !ok {
		return nil, ErrInvalidEnvironmentCADelegation
	}
	caSPKI, err := x509.MarshalPKIXPublicKey(ca.PublicKey)
	if err != nil || digestSPKI(caSPKI) != delegation.KeyID || digestSPKI(caSPKI) == profile.OwnerRootSPKI() ||
		!ca.NotBefore.Equal(mustParseUTC(delegation.NotBefore)) || !ca.NotAfter.Equal(mustParseUTC(delegation.NotAfter)) {
		return nil, ErrInvalidEnvironmentCADelegation
	}
	issued, err := parseCanonicalUTC(artifact.IssuedAt)
	if err != nil || now.Before(issued) || now.Before(ca.NotBefore) || !now.Before(ca.NotAfter) {
		return nil, ErrInvalidEnvironmentCADelegation
	}
	return bytes.Clone(delegation.Certificate), nil
}

func digestSPKI(der []byte) string {
	sum := sha256.Sum256(der)
	return "sha256:" + hex.EncodeToString(sum[:])
}

func digestBytes(value []byte) string {
	sum := sha256.Sum256(value)
	return "sha256:" + hex.EncodeToString(sum[:])
}

func exactCBORKeys(fields map[string]any, keys ...string) bool {
	if len(fields) != len(keys) {
		return false
	}
	for _, key := range keys {
		if _, ok := fields[key]; !ok {
			return false
		}
	}
	return true
}

func canonicalCBOR(data []byte, fields map[string]any) bool {
	encoded, err := caDelegationEncoder.Marshal(fields)
	return err == nil && bytes.Equal(encoded, data)
}

func validCanonicalUTC(value string) bool {
	_, err := parseCanonicalUTC(value)
	return err == nil
}

func parseCanonicalUTC(value string) (time.Time, error) {
	parsed, err := time.Parse(time.RFC3339Nano, value)
	if err != nil || parsed.Location() != time.UTC || parsed.Format(time.RFC3339Nano) != value {
		return time.Time{}, fmt.Errorf("%w: non-canonical UTC time", ErrInvalidEnvironmentCADelegation)
	}
	return parsed, nil
}

func mustParseUTC(value string) time.Time {
	parsed, _ := parseCanonicalUTC(value)
	return parsed
}

func hasSANExtension(certificate *x509.Certificate) bool {
	for _, extension := range certificate.Extensions {
		if extension.Id.Equal(subjectAlternativeNameOID) {
			return true
		}
	}
	return false
}

func hasCriticalExtension(certificate *x509.Certificate, oid []int) bool {
	for _, extension := range certificate.Extensions {
		if extension.Id.Equal(oid) {
			return extension.Critical
		}
	}
	return false
}
