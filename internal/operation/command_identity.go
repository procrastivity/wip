package operation

import (
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"fmt"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"
)

const (
	commandIdentitySchema = "wipd.command/1"
	requestHashDomain     = "wipd/request-hash/v1\x00"
	digestPrefix          = "sha256:"
)

var (
	ulidPattern   = regexp.MustCompile(`^[0-7][0-9A-HJKMNP-TV-Z]{25}$`)
	digestPattern = regexp.MustCompile(`^sha256:[0-9a-f]{64}$`)
)

// Command is the immutable authority intent around one M1 semantic Request.
// The CLI allocates ID before local IPC. An Environment supplies the domain,
// epoch, installation identity, durable sequence, and acted time before the
// command is journaled or submitted. Empty CausationCommandID is canonical
// null; an origin command names itself as CorrelationCommandID.
//
// Protocol versions, frame identifiers, endpoints, credentials, signatures,
// deadlines, asserted hashes, and static Definition metadata are deliberately
// absent. They cannot change this identity.
type Command struct {
	ID                     string
	AuthorityDomainID      string
	ExpectedAuthorityEpoch uint64
	EnvironmentID          string
	EnvironmentSequence    uint64
	ActedAt                string
	CausationCommandID     string
	CorrelationCommandID   string
	Request                Request
}

// CanonicalBytes returns the command's RFC 8949 deterministic CBOR identity
// encoding. It validates but never repairs semantic values: callers must build
// the immutable command with valid UTF-8, NFC text, canonical timestamps, and
// canonical identity spellings before journaling it.
func (command Command) CanonicalBytes() ([]byte, error) {
	value, err := command.canonicalValue()
	if err != nil {
		return nil, err
	}
	encoded, err := marshalCanonical(value)
	if err != nil {
		return nil, fmt.Errorf("operation: canonical command: %w", err)
	}
	return encoded, nil
}

// RequestHash recomputes the command identity digest. Domain separation is
// outside the canonical CBOR so those bytes remain independently inspectable
// and cannot be confused with future frame or event hashes.
func (command Command) RequestHash() (string, error) {
	encoded, err := command.CanonicalBytes()
	if err != nil {
		return "", err
	}
	hash := sha256.New()
	_, _ = hash.Write([]byte(requestHashDomain))
	_, _ = hash.Write(encoded)
	return digestPrefix + hex.EncodeToString(hash.Sum(nil)), nil
}

// VerifyRequestHash models the authority rule: recompute from the received
// semantic command and reject a missing, malformed, or mismatched assertion.
// Hash agreement authenticates nothing; the authority must separately bind
// the command's domain and Environment to its authenticated peer.
func VerifyRequestHash(command Command, asserted string) error {
	if !digestPattern.MatchString(asserted) {
		return fmt.Errorf("operation: asserted request_hash %q is not canonical sha256", asserted)
	}
	recomputed, err := command.RequestHash()
	if err != nil {
		return err
	}
	if subtle.ConstantTimeCompare([]byte(asserted), []byte(recomputed)) != 1 {
		return fmt.Errorf("operation: request_hash mismatch: asserted %s, recomputed %s", asserted, recomputed)
	}
	return nil
}

// BlobDigest returns the canonical digest of raw staged-blob bytes. Length is
// intentionally not part of this digest; every command reference carries and
// the authority independently verifies both digest and byte length.
func BlobDigest(content []byte) string {
	sum := sha256.Sum256(content)
	return digestPrefix + hex.EncodeToString(sum[:])
}

func (command Command) canonicalValue() (canonicalMap, error) {
	if err := validateULID("command ID", command.ID); err != nil {
		return nil, err
	}
	if err := validateULID("authority domain ID", command.AuthorityDomainID); err != nil {
		return nil, err
	}
	if command.ExpectedAuthorityEpoch == 0 {
		return nil, fmt.Errorf("operation: expected authority epoch must be positive")
	}
	if err := validateULID("Environment ID", command.EnvironmentID); err != nil {
		return nil, err
	}
	if command.EnvironmentSequence == 0 {
		return nil, fmt.Errorf("operation: Environment sequence must be positive")
	}
	if err := validateCanonicalTimestamp(command.ActedAt); err != nil {
		return nil, err
	}
	if command.CausationCommandID != "" {
		if err := validateULID("causation command ID", command.CausationCommandID); err != nil {
			return nil, err
		}
		if command.CausationCommandID == command.ID {
			return nil, fmt.Errorf("operation: command cannot cause itself")
		}
	}
	if err := validateULID("correlation command ID", command.CorrelationCommandID); err != nil {
		return nil, err
	}
	if command.CausationCommandID == "" && command.CorrelationCommandID != command.ID {
		return nil, fmt.Errorf("operation: an origin command must correlate to itself")
	}
	for name, identity := range map[string]string{
		"Repo ID":     command.Request.Context.Repo,
		"Clone ID":    command.Request.Context.Clone,
		"Worktree ID": command.Request.Context.Worktree,
	} {
		if identity != "" {
			if err := validateULID(name, identity); err != nil {
				return nil, err
			}
		}
	}

	definition, ok := catalogueDefinition(command.Request.Operation)
	if !ok {
		return nil, fmt.Errorf("operation: no canonical identity schema for %s", command.Request.Operation)
	}
	if err := definition.ValidateRequest(command.Request); err != nil {
		return nil, fmt.Errorf("operation: canonical command request: %w", err)
	}

	input, err := canonicalInput(command.Request.Input)
	if err != nil {
		return nil, err
	}
	blobs, err := canonicalBlobs(command.Request.Blobs)
	if err != nil {
		return nil, err
	}
	claim, err := canonicalClaim(command.Request.Claim)
	if err != nil {
		return nil, err
	}

	return canonicalMap{
		"schema":     commandIdentitySchema,
		"command_id": command.ID,
		"authority": canonicalMap{
			"domain_id":      command.AuthorityDomainID,
			"expected_epoch": command.ExpectedAuthorityEpoch,
		},
		"environment": canonicalMap{
			"id":       command.EnvironmentID,
			"sequence": command.EnvironmentSequence,
		},
		"acted_at":               command.ActedAt,
		"actor":                  string(command.Request.Actor),
		"causation_command_id":   nullableIdentity(command.CausationCommandID),
		"correlation_command_id": command.CorrelationCommandID,
		"operation": canonicalMap{
			"name":    command.Request.Operation.Name,
			"version": uint64(command.Request.Operation.Version),
		},
		"context": canonicalMap{
			"repo_id":     nullableIdentity(command.Request.Context.Repo),
			"clone_id":    nullableIdentity(command.Request.Context.Clone),
			"worktree_id": nullableIdentity(command.Request.Context.Worktree),
		},
		"claim": claim,
		"input": input,
		"blobs": blobs,
	}, nil
}

func catalogueDefinition(id ID) (Definition, bool) {
	for _, definition := range Catalogue() {
		if definition.metadata.Operation == id {
			return definition, true
		}
	}
	return Definition{}, false
}

func canonicalInput(input Input) (canonicalMap, error) {
	switch input := input.(type) {
	case MatterCreateInput:
		return canonicalMap{
			"title":             input.Title,
			"requested_locator": input.Locator,
		}, nil
	default:
		return nil, fmt.Errorf("operation: input type %T has no canonical identity schema", input)
	}
}

func canonicalBlobs(blobs []BlobInput) (canonicalArray, error) {
	ordered := append([]BlobInput(nil), blobs...)
	sort.Slice(ordered, func(i, j int) bool { return ordered[i].Name < ordered[j].Name })
	values := make(canonicalArray, 0, len(ordered))
	for i, blob := range ordered {
		if i > 0 && ordered[i-1].Name == blob.Name {
			return nil, fmt.Errorf("operation: blob input %q is duplicated", blob.Name)
		}
		if !digestPattern.MatchString(blob.Digest) {
			return nil, fmt.Errorf("operation: blob input %q digest %q is not canonical sha256", blob.Name, blob.Digest)
		}
		if blob.Size < 0 {
			return nil, fmt.Errorf("operation: blob input %q has negative size", blob.Name)
		}
		values = append(values, canonicalMap{
			"name":        blob.Name,
			"digest":      blob.Digest,
			"byte_length": uint64(blob.Size),
		})
	}
	return values, nil
}

func canonicalClaim(claim *ClaimContext) (any, error) {
	if claim == nil {
		return nil, nil
	}
	if err := validateULID("claim ID", claim.ID); err != nil {
		return nil, err
	}
	epoch, err := strconv.ParseUint(claim.Epoch, 10, 64)
	if err != nil || epoch == 0 || strconv.FormatUint(epoch, 10) != claim.Epoch {
		return nil, fmt.Errorf("operation: claim epoch %q must be a positive canonical uint64", claim.Epoch)
	}
	return canonicalMap{"id": claim.ID, "epoch": epoch}, nil
}

func nullableIdentity(identity string) any {
	if identity == "" {
		return nil
	}
	return identity
}

func validateULID(name, value string) error {
	if !ulidPattern.MatchString(value) {
		return fmt.Errorf("operation: %s %q is not a canonical ULID", name, value)
	}
	return nil
}

func validateCanonicalTimestamp(value string) error {
	parsed, err := time.Parse(time.RFC3339Nano, value)
	if err != nil {
		return fmt.Errorf("operation: acted_at %q is not RFC3339: %w", value, err)
	}
	canonical := parsed.UTC().Format(time.RFC3339Nano)
	if value != canonical || !strings.HasSuffix(value, "Z") {
		return fmt.Errorf("operation: acted_at %q is not canonical UTC RFC3339Nano; want %q", value, canonical)
	}
	return nil
}
