package operation

import (
	"bytes"
	"fmt"

	"github.com/fxamacker/cbor/v2"
)

var attachProofDecoder, _ = (cbor.DecOptions{
	DupMapKey: cbor.DupMapKeyEnforcedAPF, IndefLength: cbor.IndefLengthForbidden,
	TagsMd: cbor.TagsForbidden, MaxMapPairs: 16,
}).DecMode()

var attachProofEncoder, _ = cbor.CoreDetEncOptions().EncMode()

// CanonicalBytes is reserved for the contract-defined embedded canonical
// proof byte string. Ordinary operation data must use staged BlobInput.
type CanonicalBytes []byte

// AttachCheckoutProof is the closed daemon-derived checkout identity included
// in the M2 command hash. It contains no raw paths, remote URLs, or peer IDs.
type AttachCheckoutProof struct {
	Schema                string `cbor:"schema"`
	NormalizedRepoKey     string `cbor:"normalized_repo_key"`
	CommonDirDigest       string `cbor:"common_dir_digest"`
	GitDirDigest          string `cbor:"git_dir_digest"`
	WorktreeName          string `cbor:"worktree_name"`
	PeerSnapshotDigest    string `cbor:"peer_snapshot_digest"`
	RouteSnapshotDigest   string `cbor:"route_snapshot_digest"`
	EnvironmentID         string `cbor:"environment_id"`
	EnvironmentSPKIDigest string `cbor:"environment_spki_digest"`
}

// DecodeAttachCheckoutProof validates exact deterministic CBOR and the closed
// proof field set before returning the semantic proof.
func DecodeAttachCheckoutProof(raw []byte) (AttachCheckoutProof, error) {
	var proof AttachCheckoutProof
	var fields map[string]cbor.RawMessage
	if len(raw) == 0 || len(raw) > 4096 || attachProofDecoder.Unmarshal(raw, &fields) != nil ||
		len(fields) != 9 {
		return proof, fmt.Errorf("operation: invalid attach checkout proof")
	}
	for _, key := range []string{"schema", "normalized_repo_key", "common_dir_digest", "git_dir_digest", "worktree_name", "peer_snapshot_digest", "route_snapshot_digest", "environment_id", "environment_spki_digest"} {
		if _, ok := fields[key]; !ok {
			return proof, fmt.Errorf("operation: invalid attach checkout proof")
		}
	}
	if attachProofDecoder.Unmarshal(raw, &proof) != nil {
		return AttachCheckoutProof{}, fmt.Errorf("operation: invalid attach checkout proof")
	}
	canonical, err := attachProofEncoder.Marshal(proof)
	if err != nil || !bytes.Equal(canonical, raw) || proof.Schema != "wipd.attach-checkout-proof/1" ||
		!validNormalizedRouteKey(proof.NormalizedRepoKey) || !validDigest(proof.CommonDirDigest) ||
		!validDigest(proof.GitDirDigest) || !validDigest(proof.PeerSnapshotDigest) ||
		!validDigest(proof.RouteSnapshotDigest) || !validDigest(proof.EnvironmentSPKIDigest) ||
		validateULID("Environment ID", proof.EnvironmentID) != nil || proof.WorktreeName != "" && !validWorktreeName(proof.WorktreeName) {
		return AttachCheckoutProof{}, fmt.Errorf("operation: invalid attach checkout proof")
	}
	return proof, nil
}

func validDigest(value string) bool { return digestPattern.MatchString(value) }

func validWorktreeName(name string) bool {
	return name != "." && name != ".." && name != "" && !bytes.ContainsAny([]byte(name), "/\\\x00\r\n")
}

// RouteResolveInput is populated only by daemon-derived Git discovery. It is
// a read request: executing it must not create identity, consume a grant, or
// initialize an Environment journal.
type RouteResolveInput struct {
	NormalizedKey string
}

func (RouteResolveInput) operationInput() {}

// RouteResolveOutput contains either the authority's persisted binding or the
// caller's already selected D54 target. A nil RepoID denotes an unbound key.
type RouteResolveOutput struct {
	RepoID            *string `cbor:"repo_id"`
	DomainID          string  `cbor:"domain_id"`
	Epoch             uint64  `cbor:"authority_epoch"`
	ConnectionProfile string  `cbor:"connection_profile"`
}

func (RouteResolveOutput) operationOutput() {}

// RouteResolveV1 is the read-only authority route-index lookup operation.
var RouteResolveV1 = mustDefine[RouteResolveInput, RouteResolveOutput](Metadata{
	Operation:       ID{Name: "route.resolve", Version: 1},
	Access:          AccessRead,
	Delivery:        DeliveryNone,
	RequiredContext: []ContextDimension{},
	Guards:          []Footprint{FootprintRepoRouteIndex, FootprintRepoMembership},
	Writes:          []Footprint{},
	BlobInputs:      []BlobSpec{},
	Claim:           ClaimNone,
	ExternalEffects: []ExternalEffect{},
})

// RepoCreateLocalInput carries only the exact operator-issued D115 grant.
// The authority creates a new default domain and mints both identities.
type RepoCreateLocalInput struct {
	GrantArtifact string
}

func (RepoCreateLocalInput) operationInput() {}

// RepoCreateLocalOutput reports authority-created identity for an unattached local Repo.
type RepoCreateLocalOutput struct {
	RepoID   string
	DomainID string
	Epoch    uint64
}

func (RepoCreateLocalOutput) operationOutput() {}

// RepoCreateLocalV1 creates a remote-less Repo under an explicit D115 grant.
var RepoCreateLocalV1 = mustDefine[RepoCreateLocalInput, RepoCreateLocalOutput](Metadata{
	Operation:       ID{Name: "repo.create-local", Version: 1},
	Access:          AccessMutation,
	Delivery:        DeliveryAuthority,
	RequiredContext: []ContextDimension{},
	Guards:          []Footprint{FootprintRepoMembership, FootprintRepoRouteIndex},
	Writes:          []Footprint{FootprintRepoMembership},
	BlobInputs:      []BlobSpec{},
	Claim:           ClaimNone,
	ExternalEffects: []ExternalEffect{},
})

// EnvironmentAttachOnceInput carries the exact same-owner one-use grant and
// the daemon-generated, closed checkout proof. The local IPC request never
// accepts checkout proof bytes; only the authenticated daemon constructs this
// semantic input.
type EnvironmentAttachOnceInput struct {
	GrantArtifact string
	CheckoutProof CanonicalBytes
}

func (EnvironmentAttachOnceInput) operationInput() {}

// EnvironmentAttachOnceOutput is the exact authority binding accepted for an Environment.
type EnvironmentAttachOnceOutput struct {
	RepoID        string
	DomainID      string
	Epoch         uint64
	EnvironmentID string
	CloneID       string
	WorktreeID    string
}

func (EnvironmentAttachOnceOutput) operationOutput() {}

// EnvironmentAttachOnceV1 consumes one scoped grant to attach an Environment once.
var EnvironmentAttachOnceV1 = mustDefine[EnvironmentAttachOnceInput, EnvironmentAttachOnceOutput](Metadata{
	Operation:       ID{Name: "environment.attach-once", Version: 1},
	Access:          AccessMutation,
	Delivery:        DeliveryAuthority,
	RequiredContext: []ContextDimension{ContextRepo},
	Guards:          []Footprint{FootprintRepoMembership, FootprintEnvironmentIdentity},
	Writes:          []Footprint{FootprintEnvironmentIdentity},
	BlobInputs:      []BlobSpec{},
	Claim:           ClaimNone,
	ExternalEffects: []ExternalEffect{},
})

// RepoAdoptRemoteInput supplies the daemon-derived normalized key to adopt.
type RepoAdoptRemoteInput struct {
	NormalizedKey string
}

func (RepoAdoptRemoteInput) operationInput() {}

// RepoAdoptRemoteOutput reports the preserved Repo membership after adoption.
type RepoAdoptRemoteOutput struct {
	RepoID   string
	DomainID string
	Epoch    uint64
}

func (RepoAdoptRemoteOutput) operationOutput() {}

// RepoAdoptRemoteV1 explicitly assigns a key only to an unkeyed Repo.
var RepoAdoptRemoteV1 = mustDefine[RepoAdoptRemoteInput, RepoAdoptRemoteOutput](Metadata{
	Operation:       ID{Name: "repo.adopt-remote", Version: 1},
	Access:          AccessMutation,
	Delivery:        DeliveryAuthority,
	RequiredContext: []ContextDimension{ContextRepo, ContextClone, ContextWorktree},
	Guards:          []Footprint{FootprintRepoMembership, FootprintRepoRouteIndex, FootprintEnvironmentIdentity},
	Writes:          []Footprint{FootprintRepoRouteIndex},
	BlobInputs:      []BlobSpec{},
	Claim:           ClaimNone,
	ExternalEffects: []ExternalEffect{},
})

// RepoRouteBindInput binds an authenticated Environment's checked-out Repo to
// its normalized remote key. Bootstrap grants are handled by the dedicated
// provisioning boundary and are never represented as a client-selected RepoID.
type RepoRouteBindInput struct {
	NormalizedKey string
}

func (RepoRouteBindInput) operationInput() {}

// RepoRouteBindOutput reports the authority-derived membership and epoch for
// an accepted or exact-retry route binding.
type RepoRouteBindOutput struct {
	RepoID   string
	DomainID string
	Epoch    uint64
}

func (RepoRouteBindOutput) operationOutput() {}

// RepoRouteBindV1 is the explicit authority mutation for an already-existing
// Repo. The authority derives domain and active epoch from membership; the
// authenticated command context supplies Repo identity.
var RepoRouteBindV1 = mustDefine[RepoRouteBindInput, RepoRouteBindOutput](Metadata{
	Operation:       ID{Name: "repo.route-bind", Version: 1},
	Access:          AccessMutation,
	Delivery:        DeliveryAuthority,
	RequiredContext: []ContextDimension{ContextRepo, ContextClone, ContextWorktree},
	Guards:          []Footprint{FootprintRepoMembership, FootprintRepoRouteIndex, FootprintEnvironmentIdentity},
	Writes:          []Footprint{FootprintRepoRouteIndex},
	BlobInputs:      []BlobSpec{},
	Claim:           ClaimNone,
	ExternalEffects: []ExternalEffect{},
})

// Step11Catalogue is the isolated routing/adoption capability set. It is
// excluded from the default catalogue and is offered only by explicit Step 11
// profile negotiation.
func Step11Catalogue() []Definition {
	return []Definition{RouteResolveV1, RepoCreateLocalV1, EnvironmentAttachOnceV1, RepoRouteBindV1, RepoAdoptRemoteV1}
}

// Step11Operation reports membership in the closed Step 11 capability set.
func Step11Operation(id ID) bool {
	for _, definition := range Step11Catalogue() {
		if definition.Metadata().Operation == id {
			return true
		}
	}
	return false
}
