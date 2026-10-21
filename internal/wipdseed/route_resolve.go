package wipdseed

import (
	"context"
	"crypto/ed25519"
	"crypto/sha256"
	"crypto/x509"
	"encoding/hex"
	"errors"
	"fmt"
	"net/http"
	"time"

	"github.com/procrastivity/wip/internal/operation"
	"github.com/procrastivity/wip/internal/reporoute"
	"github.com/procrastivity/wip/internal/wipd"
	"github.com/procrastivity/wip/internal/wipdauthority"
	"github.com/procrastivity/wip/internal/wipdwire"
)

// PrebindingConnection contains trusted local pins for one D54 connection
// profile. It is not caller command input and carries no Repo identity.
type PrebindingConnection struct {
	AuthorityProfile       wipdauthority.Profile
	AuthorityRoots         *x509.CertPool
	OwnerRoot              ed25519.PublicKey
	CADElegation           []byte
	ProfileRoot            string
	ArtifactKeyCertificate []byte
}

// LocalRouteResolver is the trusted daemon configuration behind the public
// local route.resolve request. Its configuration is supplied by provisioning,
// never by the IPC caller.
type LocalRouteResolver struct {
	SelectedRemote  string
	Entries         []reporoute.Entry
	Connections     map[string]PrebindingConnection
	RememberedRoute func(string) (*reporoute.Binding, error)
}

var _ wipd.RouteResolver = (*LocalRouteResolver)(nil)

// ResolveRoute implements wipd.RouteResolver using only the authenticated
// peer process and the resolver's trusted D54/profile configuration.
func (resolver *LocalRouteResolver) ResolveRoute(ctx context.Context, peer *wipd.PeerProcess) (operation.RouteResolveOutput, error) {
	if resolver == nil {
		return operation.RouteResolveOutput{}, ErrInvalidClientState
	}
	remote := resolver.SelectedRemote
	if remote == "" {
		remote = "origin"
	}
	return resolveRouteFromPeer(ctx, peer, remote, resolver.Entries, resolver.Connections, resolver.RememberedRoute)
}

// ResolveRouteFromPeer derives the normalized remote from the authenticated
// peer's live Linux checkout, selects a trusted D54 profile, and performs one
// capability-negotiated, read-only M2 route.resolve query. No caller-provided
// path, remote URL, or identity enters the query.
func ResolveRouteFromPeer(ctx context.Context, peer *wipd.PeerProcess, selectedRemote string, entries []reporoute.Entry, connections map[string]PrebindingConnection) (operation.RouteResolveOutput, error) {
	return resolveRouteFromPeer(ctx, peer, selectedRemote, entries, connections, nil)
}

func resolveRouteFromPeer(ctx context.Context, peer *wipd.PeerProcess, selectedRemote string, entries []reporoute.Entry,
	connections map[string]PrebindingConnection, rememberedRoute func(string) (*reporoute.Binding, error),
) (operation.RouteResolveOutput, error) {
	var empty operation.RouteResolveOutput
	if ctx == nil || peer == nil || selectedRemote == "" {
		return empty, ErrInvalidClientState
	}
	checkout, err := peer.DiscoverGitCheckout(ctx, selectedRemote)
	if err != nil {
		return empty, err
	}
	selected, err := reporoute.Select(entries, checkout.NormalizedRemote)
	if err != nil {
		return empty, err
	}
	connection, ok := connections[selected.ConnectionProfile]
	if !ok || connection.ProfileRoot == "" || connection.AuthorityProfile.DomainID() != selected.DomainID {
		return empty, reporoute.ErrBindingUnresolved
	}
	credential, certificate, err := LoadEnvironmentCredential(
		connection.ProfileRoot, connection.AuthorityProfile, connection.OwnerRoot, connection.CADElegation, time.Now().UTC(),
	)
	if err != nil {
		return empty, err
	}
	defer clear(credential.PrivateKeyPKCS8)
	defer clear(certificate.PrivateKey.(ed25519.PrivateKey))
	client, err := connection.AuthorityProfile.HTTPClientWithCertificate(connection.AuthorityRoots, &certificate)
	if err != nil {
		return empty, err
	}
	defer client.CloseIdleConnections()
	result, err := queryRouteResolve(ctx, client, connection.AuthorityProfile, checkout.NormalizedRemote)
	if err != nil {
		return empty, err
	}
	if rememberedRoute != nil {
		remembered, rememberErr := rememberedRoute(checkout.NormalizedRemote)
		if rememberErr != nil {
			return empty, rememberErr
		}
		if remembered != nil && (remembered.DomainID != result.DomainID || remembered.Epoch != result.Epoch ||
			result.RepoID != nil && *result.RepoID != remembered.RepoID) {
			return empty, reporoute.ErrBindingDisagrees
		}
	}
	result.ConnectionProfile = selected.ConnectionProfile
	return result, nil
}

func queryRouteResolve(ctx context.Context, client *http.Client, profile wipdauthority.Profile, normalizedKey string) (operation.RouteResolveOutput, error) {
	var empty operation.RouteResolveOutput
	filter := map[string]any{"normalized_key": normalizedKey}
	queryPayload, err := wipdwire.EncodeCanonical(wipdwire.ReadQuery{
		Schema: "wipd.read-query/1", DomainID: profile.DomainID(), ExpectedEpoch: profile.Epoch(),
		Source: "authority", Filter: filter, OverlayPolicy: "folded-only", PageSize: 1,
	})
	if err != nil {
		return empty, err
	}
	queryRequest := wipdwire.QueryRequest{
		Schema: "wipd.query-request/1", Query: wipdwire.QueryOperation{Name: "route.resolve", Version: 1},
		QueryPayload: queryPayload,
	}
	requestFrame, requestID, err := encodeRequestFrame("query.request", queryRequest)
	if err != nil {
		return empty, err
	}
	limits, err := negotiateRemoteCapabilities(ctx, client, profile.Origin(), []operation.ID{operation.RouteResolveV1.Metadata().Operation}, false)
	if err != nil {
		return empty, err
	}
	if !withinSessionFrame(requestFrame, limits) {
		return empty, ErrInvalidClientState
	}
	responseFrame, err := postSingleFrame(ctx, client, profile.Origin()+"/wipd/v1/exchange", requestFrame, requestID)
	if err != nil {
		return empty, err
	}
	if responseFrame.Kind == "problem" {
		return empty, decodeProblem(responseFrame.Payload)
	}
	if responseFrame.Kind != "response.end" || responseFrame.Sequence != 0 {
		return empty, ErrInvalidClientState
	}
	var response wipdwire.NamedBatchReadResponse
	if err = wipdwire.DecodeNamedBatchReadResponse(responseFrame.Payload, &response); err != nil {
		return empty, err
	}
	if err = validateRouteResolveResponse(response, profile, filter); err != nil {
		return empty, err
	}
	result := operation.RouteResolveOutput{DomainID: profile.DomainID(), Epoch: profile.Epoch()}
	if len(response.Items) == 1 {
		var binding struct {
			RepoID   string `cbor:"repo_id"`
			DomainID string `cbor:"domain_id"`
			Epoch    uint64 `cbor:"authority_epoch"`
		}
		if err = wipdwire.DecodeCanonical(response.Items[0].Value, &binding, "repo_id", "domain_id", "authority_epoch"); err != nil ||
			!clientULIDPattern.MatchString(binding.RepoID) || binding.DomainID != profile.DomainID() || binding.Epoch != profile.Epoch() {
			return empty, ErrInvalidClientState
		}
		result.RepoID = &binding.RepoID
	}
	return result, nil
}

func validateRouteResolveResponse(response wipdwire.NamedBatchReadResponse, profile wipdauthority.Profile, filter map[string]any) error {
	filterBytes, err := wipdwire.EncodeCanonical(filter)
	if err != nil {
		return ErrInvalidClientState
	}
	digest := sha256.Sum256(filterBytes)
	now := time.Now().UTC()
	expires, parseErr := time.Parse(time.RFC3339Nano, response.Snapshot.ExpiresAt)
	if response.Schema != "wipd.read-response/1" || response.Query.Name != "route.resolve" || response.Query.Version != 1 ||
		response.FilterHash != "sha256:"+hex.EncodeToString(digest[:]) ||
		response.Snapshot.ID == "" || !clientULIDPattern.MatchString(response.Snapshot.ID) ||
		response.Snapshot.DomainID != profile.DomainID() || response.Snapshot.Epoch != profile.Epoch() ||
		response.Snapshot.OverlayPolicy != "folded-only" || !validAnchor(response.Snapshot.AsOf) ||
		response.Provenance.Source != "authority" || response.Provenance.AuthorityReachability != "reachable" ||
		response.Provenance.AuthorityAsOf == nil || !anchorEqual(*response.Provenance.AuthorityAsOf, response.Snapshot.AsOf) ||
		response.Provenance.LocalBaseAsOf != nil || response.Provenance.PendingJournalCount != 0 ||
		response.Provenance.ProvisionalItemCount != 0 || response.Provenance.QuarantinedItemCount != 0 ||
		response.Provenance.HistoryState != "current" || len(response.Items) > 1 || response.NextPageToken != nil || !response.Complete ||
		parseErr != nil || expires.UTC().Format(time.RFC3339Nano) != response.Snapshot.ExpiresAt || !expires.After(now) {
		return fmt.Errorf("%w: invalid route.resolve read response", ErrInvalidClientState)
	}
	if len(response.Items) == 1 && (response.Items[0].State != "folded" || response.Items[0].EnvironmentSequence != nil || len(response.Items[0].Value) == 0) {
		return errors.New("wipdseed: invalid route.resolve item")
	}
	return nil
}
