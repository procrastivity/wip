// Package d115 implements the closed, pre-domain create-domain protocol.
package d115

import (
	"bytes"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"math/big"
	"regexp"
	"time"

	"github.com/fxamacker/cbor/v2"
	"golang.org/x/text/unicode/norm"
)

// The exported schema constants pin the closed D115 canonical-CBOR message set.
const (
	GrantSchema          = "wipd.create-domain-grant/1"
	ClaimsSchema         = "wipd.create-domain-grant-claims/1"
	SubmitSchema         = "wipd.create-domain-submit/1"
	ReceiptSchema        = "wipd.create-domain-receipt/1"
	SigningRequestSchema = "wipd.create-domain-signing-request/1"
	OwnerProofSchema     = "wipd.create-domain-owner-proof-artifact/1"
	TrustBundleSchema    = "wipd.d115-operator-trust-bundle/1"
	CreateDomainScope    = "repo.create-local@v1:new-default-domain+initial-repo-membership"
)

var (
	ulidPattern   = regexp.MustCompile(`^[0-7][0-9A-HJKMNP-TV-Z]{25}$`)
	digestPattern = regexp.MustCompile(`^sha256:[0-9a-f]{64}$`)
	encoder, _    = cbor.CanonicalEncOptions().EncMode()
	decoder, _    = (cbor.DecOptions{
		DupMapKey:         cbor.DupMapKeyEnforcedAPF,
		IndefLength:       cbor.IndefLengthForbidden,
		TagsMd:            cbor.TagsForbidden,
		ExtraReturnErrors: cbor.ExtraDecErrorUnknownField,
	}).DecMode()
)

// Claims bind one operator authorization to an audience, scope, and owner root.
type Claims struct {
	Schema         string `cbor:"schema"`
	GrantID        string `cbor:"grant_id"`
	Audience       string `cbor:"audience"`
	Scope          string `cbor:"scope"`
	OwnerPublicKey []byte `cbor:"owner_root_public_key"`
	OwnerKeyID     string `cbor:"owner_root_key_id"`
	IssuedAt       string `cbor:"issued_at"`
	NotBefore      string `cbor:"not_before"`
	ExpiresAt      string `cbor:"expires_at"`
}

// Grant is the operator-signed canonical claims envelope.
type Grant struct {
	Schema      string `cbor:"schema"`
	Claims      []byte `cbor:"claims"`
	SignerKeyID string `cbor:"signer_key_id"`
	Signature   []byte `cbor:"signature"`
}

// Submit carries a grant and the owner proof for one exact command identity.
type Submit struct {
	Schema      string `cbor:"schema"`
	RealmID     string `cbor:"provisioning_realm_id"`
	CommandID   string `cbor:"command_id"`
	Grant       []byte `cbor:"grant_artifact"`
	RequestHash string `cbor:"request_hash"`
	OwnerProof  []byte `cbor:"owner_proof"`
}

// OwnerSigningRequest is the exact durable request exported for the separate
// owner-invoked offline signing tool.
type OwnerSigningRequest struct {
	Schema        string `cbor:"schema"`
	RealmID       string `cbor:"provisioning_realm_id"`
	CommandID     string `cbor:"command_id"`
	RequestHash   string `cbor:"request_hash"`
	GrantDigest   string `cbor:"grant_digest"`
	GrantArtifact []byte `cbor:"grant_artifact"`
	OwnerKeyID    string `cbor:"owner_root_key_id"`
	Preimage      []byte `cbor:"owner_proof_preimage"`
}

// OwnerProofArtifact is a detached proof bound to one pinned signing request.
type OwnerProofArtifact struct {
	Schema      string `cbor:"schema"`
	RealmID     string `cbor:"provisioning_realm_id"`
	CommandID   string `cbor:"command_id"`
	RequestHash string `cbor:"request_hash"`
	GrantDigest string `cbor:"grant_digest"`
	OwnerKeyID  string `cbor:"owner_root_key_id"`
	OwnerProof  []byte `cbor:"owner_proof"`
}

// OperatorTrustBundle is the owner-transferred public snapshot used only by
// the offline signer; the AuthorityStore remains the admission authority.
type OperatorTrustBundle struct {
	Schema     string             `cbor:"schema"`
	RealmID    string             `cbor:"provisioning_realm_id"`
	Generation uint64             `cbor:"generation"`
	Enabled    bool               `cbor:"enabled"`
	Keys       []OperatorTrustKey `cbor:"keys"`
}

// OperatorTrustKey is one closed public operator-key record.
type OperatorTrustKey struct {
	KeyID     string `cbor:"key_id"`
	PublicKey []byte `cbor:"public_key"`
	Scope     string `cbor:"scope"`
	Audience  string `cbor:"audience"`
	NotBefore string `cbor:"not_before"`
	NotAfter  string `cbor:"not_after"`
	State     string `cbor:"state"`
}

// EncodeOperatorTrustBundle validates and encodes a canonical bounded bundle.
func EncodeOperatorTrustBundle(bundle OperatorTrustBundle) ([]byte, error) {
	if err := ValidateOperatorTrustBundle(bundle); err != nil {
		return nil, err
	}
	return encodeBounded(bundle, 1<<20)
}

// DecodeOperatorTrustBundle validates the canonical closed snapshot.
func DecodeOperatorTrustBundle(data []byte) (OperatorTrustBundle, error) {
	var bundle OperatorTrustBundle
	if len(data) == 0 || len(data) > 1<<20 || decodeCanonical(data, &bundle) != nil || ValidateOperatorTrustBundle(bundle) != nil {
		return OperatorTrustBundle{}, errors.New("d115: invalid operator trust bundle")
	}
	return bundle, nil
}

// ValidateOperatorTrustBundle enforces identity, key, state, and ordering invariants.
func ValidateOperatorTrustBundle(bundle OperatorTrustBundle) error {
	if bundle.Schema != TrustBundleSchema || !ulidPattern.MatchString(bundle.RealmID) || bundle.Generation == 0 {
		return errors.New("d115: invalid trust-bundle header")
	}
	active := 0
	previous := ""
	for _, key := range bundle.Keys {
		keyID, err := KeyID(ed25519.PublicKey(key.PublicKey))
		if err != nil || key.KeyID != keyID || key.Scope != CreateDomainScope || key.Audience != bundle.RealmID ||
			(key.State != "active" && key.State != "revoked") || !validTime(key.NotBefore) || !validTime(key.NotAfter) || !previousLess(previous, key.KeyID) {
			return errors.New("d115: invalid trust-bundle key")
		}
		start, _ := time.Parse(time.RFC3339Nano, key.NotBefore)
		end, _ := time.Parse(time.RFC3339Nano, key.NotAfter)
		if !start.Before(end) {
			return errors.New("d115: invalid trust-bundle interval")
		}
		if key.State == "active" {
			active++
		}
		previous = key.KeyID
	}
	if active > 1 || bundle.Enabled && active != 1 {
		return errors.New("d115: invalid trust-bundle active-key set")
	}
	return nil
}

func previousLess(previous, current string) bool { return previous == "" || previous < current }

// EncodeOwnerSigningRequest returns a bounded deterministic-CBOR request.
func EncodeOwnerSigningRequest(request OwnerSigningRequest) ([]byte, error) {
	if request.Schema != SigningRequestSchema || !ulidPattern.MatchString(request.RealmID) || !ulidPattern.MatchString(request.CommandID) ||
		!digestPattern.MatchString(request.RequestHash) || !digestPattern.MatchString(request.GrantDigest) || len(request.GrantArtifact) == 0 ||
		len(request.GrantArtifact) > 4096 || !digestPattern.MatchString(request.OwnerKeyID) || len(request.Preimage) == 0 || len(request.Preimage) > 1024 {
		return nil, errors.New("d115: invalid owner signing request")
	}
	return encodeBounded(request, 8192)
}

// DecodeOwnerSigningRequest accepts only the canonical closed request schema.
func DecodeOwnerSigningRequest(data []byte) (OwnerSigningRequest, error) {
	var request OwnerSigningRequest
	if len(data) == 0 || len(data) > 8192 || decodeCanonical(data, &request) != nil || request.Schema != SigningRequestSchema ||
		!ulidPattern.MatchString(request.RealmID) || !ulidPattern.MatchString(request.CommandID) || !digestPattern.MatchString(request.RequestHash) ||
		!digestPattern.MatchString(request.GrantDigest) || len(request.GrantArtifact) == 0 || len(request.GrantArtifact) > 4096 ||
		!digestPattern.MatchString(request.OwnerKeyID) || len(request.Preimage) == 0 || len(request.Preimage) > 1024 {
		return OwnerSigningRequest{}, errors.New("d115: invalid owner signing request")
	}
	return request, nil
}

// EncodeOwnerProofArtifact returns a bounded deterministic-CBOR detached proof.
func EncodeOwnerProofArtifact(proof OwnerProofArtifact) ([]byte, error) {
	if proof.Schema != OwnerProofSchema || !ulidPattern.MatchString(proof.RealmID) || !ulidPattern.MatchString(proof.CommandID) ||
		!digestPattern.MatchString(proof.RequestHash) || !digestPattern.MatchString(proof.GrantDigest) ||
		!digestPattern.MatchString(proof.OwnerKeyID) || len(proof.OwnerProof) != ed25519.SignatureSize {
		return nil, errors.New("d115: invalid owner proof artifact")
	}
	return encodeBounded(proof, 2048)
}

// DecodeOwnerProofArtifact accepts only the canonical closed proof schema.
func DecodeOwnerProofArtifact(data []byte) (OwnerProofArtifact, error) {
	var proof OwnerProofArtifact
	if len(data) == 0 || len(data) > 2048 || decodeCanonical(data, &proof) != nil || proof.Schema != OwnerProofSchema ||
		!ulidPattern.MatchString(proof.RealmID) || !ulidPattern.MatchString(proof.CommandID) || !digestPattern.MatchString(proof.RequestHash) ||
		!digestPattern.MatchString(proof.GrantDigest) || !digestPattern.MatchString(proof.OwnerKeyID) || len(proof.OwnerProof) != ed25519.SignatureSize {
		return OwnerProofArtifact{}, errors.New("d115: invalid owner proof artifact")
	}
	return proof, nil
}

// Receipt is the authority-minted, replayable result of create-domain.
type Receipt struct {
	Schema       string    `cbor:"schema"`
	RealmID      string    `cbor:"provisioning_realm_id"`
	CommandID    string    `cbor:"command_id"`
	RequestHash  string    `cbor:"request_hash"`
	Operation    Operation `cbor:"operation"`
	ResultCode   string    `cbor:"result_code"`
	DomainID     string    `cbor:"domain_id"`
	RepoID       string    `cbor:"repo_id"`
	InitialEpoch uint64    `cbor:"initial_authority_epoch"`
	OwnerKeyID   string    `cbor:"owner_root_key_id"`
	GrantID      string    `cbor:"grant_id"`
	CommittedAt  string    `cbor:"committed_at"`
}

// Operation is the closed name/version pair recorded in a D115 receipt.
type Operation struct {
	Name    string `cbor:"name"`
	Version uint16 `cbor:"version"`
}

// DecodeGrant validates a canonical signed grant envelope and its claims.
func DecodeGrant(data []byte) (Grant, Claims, error) {
	var grant Grant
	var claims Claims
	if len(data) == 0 || len(data) > 4096 {
		return grant, claims, errors.New("d115: grant size is outside bounds")
	}
	if err := decodeCanonical(data, &grant); err != nil || grant.Schema != GrantSchema || len(grant.Signature) != ed25519.SignatureSize || !digestPattern.MatchString(grant.SignerKeyID) {
		return Grant{}, Claims{}, errors.New("d115: invalid grant envelope")
	}
	if err := decodeCanonical(grant.Claims, &claims); err != nil || !validClaims(claims) {
		return Grant{}, Claims{}, errors.New("d115: invalid grant claims")
	}
	return grant, claims, nil
}

// DecodeGrantArtifact decodes the canonical unpadded base64url public input.
func DecodeGrantArtifact(artifact string) ([]byte, Grant, Claims, error) {
	if artifact == "" || len(artifact) > 5462 {
		return nil, Grant{}, Claims{}, errors.New("d115: grant artifact is outside bounds")
	}
	data, err := base64.RawURLEncoding.DecodeString(artifact)
	if err != nil || len(data) > 4096 || base64.RawURLEncoding.EncodeToString(data) != artifact {
		return nil, Grant{}, Claims{}, errors.New("d115: grant artifact is not canonical unpadded base64url")
	}
	grant, claims, err := DecodeGrant(data)
	if err != nil {
		return nil, Grant{}, Claims{}, err
	}
	return data, grant, claims, nil
}

// NewULID generates a canonical ULID using the supplied UTC timestamp.
func NewULID(now time.Time) (string, error) {
	const alphabet = "0123456789ABCDEFGHJKMNPQRSTVWXYZ"
	var data [16]byte
	millis := uint64(now.UnixMilli())
	for index := 5; index >= 0; index-- {
		data[index] = byte(millis)
		millis >>= 8
	}
	if _, err := rand.Read(data[6:]); err != nil {
		return "", err
	}
	value, base := new(big.Int).SetBytes(data[:]), big.NewInt(32)
	var encoded [26]byte
	for index := len(encoded) - 1; index >= 0; index-- {
		quotient, remainder := new(big.Int), new(big.Int)
		quotient.QuoRem(value, base, remainder)
		encoded[index] = alphabet[remainder.Int64()]
		value = quotient
	}
	return string(encoded[:]), nil
}

// EncodeGrant encodes a valid grant using deterministic CBOR.
func EncodeGrant(grant Grant) ([]byte, error) {
	if grant.Schema != GrantSchema || len(grant.Signature) != ed25519.SignatureSize || !digestPattern.MatchString(grant.SignerKeyID) {
		return nil, errors.New("d115: invalid grant envelope")
	}
	var claims Claims
	if err := decodeCanonical(grant.Claims, &claims); err != nil || !validClaims(claims) {
		return nil, errors.New("d115: invalid grant claims")
	}
	return encodeBounded(grant, 4096)
}

// EncodeClaims encodes valid claims using deterministic CBOR.
func EncodeClaims(claims Claims) ([]byte, error) {
	if !validClaims(claims) {
		return nil, errors.New("d115: invalid grant claims")
	}
	return encodeBounded(claims, 4096)
}

// GrantSigningPreimage returns the domain-separated operator signature input.
func GrantSigningPreimage(grant Grant) ([]byte, error) {
	if grant.Schema != GrantSchema || !digestPattern.MatchString(grant.SignerKeyID) || len(grant.Signature) != ed25519.SignatureSize {
		return nil, errors.New("d115: invalid grant envelope")
	}
	unsigned := struct {
		Schema      string `cbor:"schema"`
		Claims      []byte `cbor:"claims"`
		SignerKeyID string `cbor:"signer_key_id"`
	}{grant.Schema, grant.Claims, grant.SignerKeyID}
	encoded, err := encoder.Marshal(unsigned)
	if err != nil {
		return nil, err
	}
	return append([]byte("wipd/create-domain-grant/v1\x00"), encoded...), nil
}

// GrantDigest returns the domain-separated digest of exact canonical grant bytes.
func GrantDigest(data []byte) string {
	h := sha256.New()
	_, _ = h.Write([]byte("wipd/create-domain-grant-digest/v1\x00"))
	_, _ = h.Write(data)
	return "sha256:" + hex.EncodeToString(h.Sum(nil))
}

// RequestHash derives the command identity from its realm, ID, operation, and grant.
func RequestHash(realmID, commandID, grantDigest string) (string, error) {
	if !ulidPattern.MatchString(realmID) || !ulidPattern.MatchString(commandID) || !digestPattern.MatchString(grantDigest) {
		return "", errors.New("d115: invalid command identity")
	}
	identity := struct {
		Schema      string    `cbor:"schema"`
		Realm       string    `cbor:"provisioning_realm_id"`
		ID          string    `cbor:"command_id"`
		Op          Operation `cbor:"operation"`
		GrantDigest string    `cbor:"grant_digest"`
	}{"wipd.create-domain-command/1", realmID, commandID, Operation{"repo.create-local", 1}, grantDigest}
	encoded, err := encoder.Marshal(identity)
	if err != nil {
		return "", err
	}
	h := sha256.Sum256(append([]byte("wipd/create-domain-request-hash/v1\x00"), encoded...))
	return "sha256:" + hex.EncodeToString(h[:]), nil
}

// OwnerProofPreimage returns the domain-separated exact-command owner proof input.
func OwnerProofPreimage(realmID, commandID, requestHash, ownerKeyID string) ([]byte, error) {
	if !ulidPattern.MatchString(realmID) || !ulidPattern.MatchString(commandID) ||
		!digestPattern.MatchString(requestHash) || !digestPattern.MatchString(ownerKeyID) {
		return nil, errors.New("d115: invalid owner-proof identity")
	}
	proof := struct {
		Schema     string `cbor:"schema"`
		Realm      string `cbor:"provisioning_realm_id"`
		Command    string `cbor:"command_id"`
		Hash       string `cbor:"request_hash"`
		OwnerKeyID string `cbor:"owner_root_key_id"`
	}{"wipd.create-domain-owner-proof/1", realmID, commandID, requestHash, ownerKeyID}
	encoded, err := encoder.Marshal(proof)
	if err != nil {
		return nil, err
	}
	return append([]byte("wipd/create-domain-owner-proof/v1\x00"), encoded...), nil
}

// EncodeSubmit encodes a valid closed create-domain request.
func EncodeSubmit(submit Submit) ([]byte, error) {
	if submit.Schema != SubmitSchema || !ulidPattern.MatchString(submit.RealmID) || !ulidPattern.MatchString(submit.CommandID) ||
		!digestPattern.MatchString(submit.RequestHash) || len(submit.OwnerProof) != ed25519.SignatureSize || len(submit.Grant) == 0 || len(submit.Grant) > 4096 {
		return nil, errors.New("d115: invalid submit envelope")
	}
	if len(submit.Grant) > 4096 {
		return nil, errors.New("d115: grant too large")
	}
	return encodeBounded(submit, 8192)
}

// DecodeSubmit validates and decodes a canonical create-domain request.
func DecodeSubmit(data []byte) (Submit, error) {
	var submit Submit
	if len(data) == 0 || len(data) > 8192 || decodeCanonical(data, &submit) != nil || submit.Schema != SubmitSchema ||
		!ulidPattern.MatchString(submit.RealmID) || !ulidPattern.MatchString(submit.CommandID) || !digestPattern.MatchString(submit.RequestHash) ||
		len(submit.OwnerProof) != ed25519.SignatureSize || len(submit.Grant) == 0 || len(submit.Grant) > 4096 {
		return Submit{}, errors.New("d115: invalid submit envelope")
	}
	return submit, nil
}

// EncodeReceipt encodes a validated terminal create-domain receipt.
func EncodeReceipt(receipt Receipt) ([]byte, error) {
	if err := ValidateReceipt(receipt); err != nil {
		return nil, err
	}
	return encodeBounded(receipt, 8192)
}

// DecodeReceipt validates and decodes a canonical terminal receipt.
func DecodeReceipt(data []byte) (Receipt, error) {
	var receipt Receipt
	if len(data) == 0 || len(data) > 8192 || decodeCanonical(data, &receipt) != nil || ValidateReceipt(receipt) != nil {
		return Receipt{}, errors.New("d115: invalid receipt")
	}
	return receipt, nil
}

// ValidateReceipt checks the fixed operation and identity fields in a receipt.
func ValidateReceipt(receipt Receipt) error {
	if receipt.Schema != ReceiptSchema || !ulidPattern.MatchString(receipt.RealmID) || !ulidPattern.MatchString(receipt.CommandID) ||
		!digestPattern.MatchString(receipt.RequestHash) || receipt.Operation != (Operation{"repo.create-local", 1}) ||
		receipt.ResultCode != "result.succeeded" || !ulidPattern.MatchString(receipt.DomainID) || !ulidPattern.MatchString(receipt.RepoID) ||
		receipt.InitialEpoch != 1 || !digestPattern.MatchString(receipt.OwnerKeyID) || !ulidPattern.MatchString(receipt.GrantID) || !validTime(receipt.CommittedAt) {
		return errors.New("d115: invalid receipt fields")
	}
	return nil
}

// VerifyGrantSignature verifies the operator signature over a grant.
func VerifyGrantSignature(grant Grant, publicKey ed25519.PublicKey) bool {
	if len(publicKey) != ed25519.PublicKeySize {
		return false
	}
	preimage, err := GrantSigningPreimage(grant)
	return err == nil && ed25519.Verify(publicKey, preimage, grant.Signature)
}

// KeyID derives the canonical SPKI digest identifier for an Ed25519 public key.
func KeyID(publicKey ed25519.PublicKey) (string, error) {
	der, err := x509.MarshalPKIXPublicKey(publicKey)
	if err != nil || len(publicKey) != ed25519.PublicKeySize {
		return "", errors.New("d115: invalid Ed25519 public key")
	}
	digest := sha256.Sum256(der)
	return "sha256:" + hex.EncodeToString(digest[:]), nil
}

// ValidULID reports whether value is a canonical uppercase Crockford ULID.
func ValidULID(value string) bool { return ulidPattern.MatchString(value) }

func validClaims(claims Claims) bool {
	ownerID, err := KeyID(ed25519.PublicKey(claims.OwnerPublicKey))
	return claims.Schema == ClaimsSchema && ulidPattern.MatchString(claims.GrantID) && ulidPattern.MatchString(claims.Audience) &&
		claims.Scope == CreateDomainScope && len(claims.OwnerPublicKey) == ed25519.PublicKeySize && err == nil && ownerID == claims.OwnerKeyID &&
		digestPattern.MatchString(claims.OwnerKeyID) && validTime(claims.IssuedAt) && validTime(claims.NotBefore) && validTime(claims.ExpiresAt)
}

func validTime(value string) bool {
	parsed, err := time.Parse(time.RFC3339Nano, value)
	return err == nil && parsed.UTC().Format(time.RFC3339Nano) == value && len(value) > 0 && value[len(value)-1] == 'Z' && norm.NFC.IsNormalString(value)
}

func encodeBounded(value any, limit int) ([]byte, error) {
	encoded, err := encoder.Marshal(value)
	if err != nil || len(encoded) > limit {
		return nil, errors.New("d115: canonical message exceeds limit")
	}
	return encoded, nil
}

func decodeCanonical(data []byte, target any) error {
	if len(data) == 0 {
		return errors.New("d115: empty encoding")
	}
	if err := decoder.Unmarshal(data, target); err != nil {
		return fmt.Errorf("d115: decode canonical message: %w", err)
	}
	encoded, err := encoder.Marshal(target)
	if err != nil || !bytes.Equal(encoded, data) {
		return errors.New("d115: message is not deterministic CBOR")
	}
	return nil
}
