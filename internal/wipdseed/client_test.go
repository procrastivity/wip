package wipdseed

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/asn1"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"math/big"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/fxamacker/cbor/v2"
	"github.com/procrastivity/wip/internal/authoritystore"
	"github.com/procrastivity/wip/internal/operation"
	"github.com/procrastivity/wip/internal/wipdauthority"
	"github.com/procrastivity/wip/internal/wipdjournal"
	"github.com/procrastivity/wip/internal/wipdwire"
)

const (
	testDomainID = "01KZ7XHAQT1S46NYPN1PW1DX3B"
	testRepoID   = "01KZ7XHAQT1S46NYPN1PW1DX3C"
	testGrantID  = "01KZ7XHAQT1S46NYPN1PW1DX3D"
)

var testEncoder, _ = cbor.CoreDetEncOptions().EncMode()

func TestEnrollAndSeedInstallsOnlyVerifiedEmptyShadow(t *testing.T) {
	fixture := newClientFixture(t)
	identity := fixture.identity
	var err error
	directory := t.TempDir()
	if err = SavePending(directory, identity); err != nil {
		t.Fatal(err)
	}
	grant := fixture.grant
	state, err := EnrollAndSeed(context.Background(), fixture.profile, fixture.roots, fixture.ownerRoot, fixture.delegation, testRepoID, identity, grant, directory)
	if err != nil {
		t.Fatal(err)
	}
	if state.Schema != "wipd.m5-client-state/1" || state.RepoID != testRepoID || state.DomainID != testDomainID || state.Epoch != 1 ||
		state.EnvironmentID == "" || state.OwnerKeyID != fixture.ownerKeyID || state.SPKIDigest == "" || len(state.CertificateDER) != 2 ||
		state.Prefix.EventCount != 0 || state.Prefix.EventID != nil || state.Prefix.Digest != emptyPrefixDigest() ||
		state.ManifestDigest != emptyManifestDigest() || state.Projections == nil || len(state.Projections) != 0 {
		t.Fatalf("installed shadow does not match verified empty seed: %+v", state)
	}
	if _, err = os.Stat(filepath.Join(directory, pendingName)); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("pending identity remains after successful install: %v", err)
	}
	path := filepath.Join(directory, stateName)
	info, err := os.Stat(path)
	if err != nil || info.Mode().Perm() != 0o600 {
		t.Fatalf("installed state mode = %v, %v; want 0600", info, err)
	}
	encoded, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var persisted ClientState
	if err = json.Unmarshal(encoded, &persisted); err != nil || persisted.EnvironmentID != state.EnvironmentID || !bytes.Equal(persisted.PrivateKeyPKCS8, identity.PrivateKeyPKCS8) {
		t.Fatalf("persisted state differs from installed verified identity: %+v, %v", persisted, err)
	}
	if _, err = EnrollAndSeed(context.Background(), fixture.profile, fixture.roots, fixture.ownerRoot, fixture.delegation, testRepoID, identity, grant, directory); !errors.Is(err, ErrStateExists) {
		t.Fatalf("second install = %v, want create-only refusal", err)
	}
	recoveryDirectory := t.TempDir()
	if err = SavePending(recoveryDirectory, identity); err != nil {
		t.Fatal(err)
	}
	recovered, err := EnrollAndSeed(context.Background(), fixture.profile, fixture.roots, fixture.ownerRoot, fixture.delegation, testRepoID, identity, grant, recoveryDirectory)
	if err != nil || recovered.EnvironmentID != state.EnvironmentID || !bytes.Equal(recovered.CertificateDER[0], state.CertificateDER[0]) {
		t.Fatalf("same-grant response-loss recovery = %+v, %v; want the original issued identity/certificate", recovered, err)
	}
	if calls := fixture.signerCalls.Load(); calls != 1 {
		t.Fatalf("external CA signer was called %d times after an exact retry, want once", calls)
	}

	wrongKey, err := PrepareIdentity()
	if err != nil {
		t.Fatal(err)
	}
	if _, err = EnrollAndSeed(context.Background(), fixture.profile, fixture.roots, fixture.ownerRoot, fixture.delegation, testRepoID, wrongKey, grant, t.TempDir()); err == nil {
		t.Fatal("reusing a consumed grant for a different CSR was accepted")
	}
	wrongRepoConfig := wipdauthority.M5LabConfig{
		Store: fixture.store, RepoID: "01KZ7XHAQT1S46NYPN1PW1DX3E", EnrollmentGrant: fixture.grant,
		ExpectedCSRDER: fixture.identity.CSRDER, EnvironmentCACertificateDER: fixture.caDER,
		SignEnvironmentLeaf: func(context.Context, string, uint64, string, []byte, time.Time) ([]byte, error) {
			return nil, errors.New("unexpected signer call")
		},
	}
	if _, err = wipdauthority.NewM5LabServer(fixture.profile, tlsCertificate(t, fixture.serverCertDER, fixture.serverPrivate), wrongRepoConfig); !errors.Is(err, wipdauthority.ErrRepoMembershipMismatch) {
		t.Fatalf("wrong Repo server binding = %v, want membership refusal", err)
	}
}

func TestEnrollAndSeedRejectsRepoDifferentFromBootstrapPinBeforeNetwork(t *testing.T) {
	fixture := newClientFixture(t)
	wrongRepoID := "01KZ7XHAQT1S46NYPN1PW1DX3E"
	bootstrapRepoID, err := fixture.store.M5LabGenesisRepoID(context.Background(), testDomainID)
	if err != nil || bootstrapRepoID != testRepoID || fixture.profile.M5LabRepoID() != bootstrapRepoID {
		t.Fatalf("client Repo pin %q, persisted bootstrap Repo %q, error %v", fixture.profile.M5LabRepoID(), bootstrapRepoID, err)
	}
	directory := t.TempDir()
	if err = SavePending(directory, fixture.identity); err != nil {
		t.Fatal(err)
	}
	if _, err = EnrollAndSeed(context.Background(), fixture.profile, fixture.roots, fixture.ownerRoot,
		fixture.delegation, wrongRepoID, fixture.identity, fixture.grant, directory); !errors.Is(err, wipdauthority.ErrRepoBindingMismatch) {
		t.Fatalf("wrong client Repo = %v, want pinned Repo refusal", err)
	}
	if _, err := os.Stat(filepath.Join(directory, stateName)); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("wrong-Repo attempt installed client state: %v", err)
	}
	if _, err := os.Stat(filepath.Join(directory, pendingName)); err != nil {
		t.Fatalf("wrong-Repo attempt changed pending identity: %v", err)
	}
	if calls := fixture.signerCalls.Load(); calls != 0 {
		t.Fatalf("authority handler ran %d times for wrong client Repo, want zero", calls)
	}
}

func TestM5LabServerRejectsAnotherValidMemberAsBootstrapRepo(t *testing.T) {
	fixture := newClientFixture(t)
	const repoB = "01KZ7XHAQT1S46NYPN1PW1DX3E"
	if err := fixture.store.AttachRepo(context.Background(), testDomainID, repoB); err != nil {
		t.Fatalf("attach second valid Repo to the same domain: %v", err)
	}
	if domain, err := fixture.store.RepoDomain(context.Background(), repoB); err != nil || domain != testDomainID {
		t.Fatalf("Repo B membership = %q, %v", domain, err)
	}
	bootstrapRepoID, err := fixture.store.M5LabGenesisRepoID(context.Background(), testDomainID)
	if err != nil || bootstrapRepoID != testRepoID {
		t.Fatalf("persisted bootstrap Repo = %q, %v; want %q", bootstrapRepoID, err, testRepoID)
	}

	// These are the equivalent host inputs when --repo-id B is copied into
	// both authority serve configuration and the client profile.
	profileB, err := wipdauthority.NewProfile(fixture.profile.Origin(), testDomainID, 1, fixture.authorityPin, fixture.ownerKeyID)
	if err != nil {
		t.Fatal(err)
	}
	profileB, err = profileB.WithM5LabRepoID(repoB)
	if err != nil {
		t.Fatal(err)
	}
	_, err = wipdauthority.NewM5LabServer(profileB, tlsCertificate(t, fixture.serverCertDER, fixture.serverPrivate), wipdauthority.M5LabConfig{
		Store: fixture.store, RepoID: repoB, EnrollmentGrant: fixture.grant,
		ExpectedCSRDER: fixture.identity.CSRDER, EnvironmentCACertificateDER: fixture.caDER,
		SignEnvironmentLeaf: func(context.Context, string, uint64, string, []byte, time.Time) ([]byte, error) {
			return nil, errors.New("unexpected signer call")
		},
	})
	if !errors.Is(err, wipdauthority.ErrRepoBindingMismatch) {
		t.Fatalf("server setup for member Repo B = %v, want persisted-genesis binding refusal", err)
	}

	directory := t.TempDir()
	if err = SavePending(directory, fixture.identity); err != nil {
		t.Fatal(err)
	}
	if _, err = EnrollAndSeed(context.Background(), fixture.profile, fixture.roots, fixture.ownerRoot,
		fixture.delegation, repoB, fixture.identity, fixture.grant, directory); !errors.Is(err, wipdauthority.ErrRepoBindingMismatch) {
		t.Fatalf("client install using bootstrap pin A and requested member B = %v, want binding refusal", err)
	}
	if _, err = os.Stat(filepath.Join(directory, stateName)); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("member-B attempt installed client state: %v", err)
	}
}

func TestNegotiationIsRequiredBeforeSeedExchange(t *testing.T) {
	fixture := newClientFixture(t)
	state := enrollFixtureClient(t, fixture, t.TempDir())
	client := installedClient(t, fixture, state)
	requestFrame, requestID, err := encodeRequestFrame("seed.request", wipdwire.SeedRequest{
		Schema: "wipd.seed-request/1", DomainID: state.DomainID, Epoch: state.Epoch, StoreSchema: "wipd.store/1",
	})
	if err != nil {
		t.Fatal(err)
	}
	frames, err := postFrames(context.Background(), client, fixture.profile.Origin()+"/wipd/v1/exchange", requestFrame, requestID, 1)
	if err != nil || len(frames) != 1 || frames[0].Kind != "problem" {
		t.Fatalf("seed before negotiation = %+v, %v; want one correlated protocol problem", frames, err)
	}
	if err = decodeProblem(frames[0].Payload); err == nil || !strings.Contains(err.Error(), "protocol.out-of-order") {
		t.Fatalf("seed-before-negotiate problem = %v, want protocol.out-of-order", err)
	}
}

func TestPullRejectsAnAnchorAheadOfAuthorityAsPrefixMismatch(t *testing.T) {
	fixture := newClientFixture(t)
	state := enrollFixtureClient(t, fixture, t.TempDir())
	client := installedClient(t, fixture, state)
	limits, err := negotiateRemote(context.Background(), client, fixture.profile.Origin())
	if err != nil {
		t.Fatal(err)
	}
	futureEventID := "01KZ7XHAQT1S46NYPN1PW1DX60"
	requestFrame, requestID, err := encodeRequestFrame("pull.request", wipdwire.PullRequest{
		Schema: "wipd.pull-request/1", DomainID: state.DomainID, Epoch: state.Epoch,
		Installed: wipdwire.PrefixAnchor{EventCount: 1, EventID: &futureEventID, Digest: testDigest([]byte("future authority prefix"))},
	})
	if err != nil {
		t.Fatal(err)
	}
	frames, err := postFramesWithinSession(context.Background(), client, fixture.profile.Origin()+"/wipd/v1/exchange",
		requestFrame, requestID, maxClientTransferEvents+3, limits)
	if err != nil || len(frames) != 1 || frames[0].Kind != "problem" {
		t.Fatalf("pull from a future anchor = %+v, %v; want correlated prefix-mismatch", frames, err)
	}
	if err = decodeProblem(frames[0].Payload); err == nil || !strings.Contains(err.Error(), "transfer.prefix-mismatch") {
		t.Fatalf("future-anchor pull problem = %v, want transfer.prefix-mismatch", err)
	}
}

func installedClient(t *testing.T, fixture *clientFixture, state ClientState) *http.Client {
	t.Helper()
	key, err := x509.ParsePKCS8PrivateKey(state.PrivateKeyPKCS8)
	if err != nil {
		t.Fatal(err)
	}
	leaf, err := x509.ParseCertificate(state.CertificateDER[0])
	if err != nil {
		t.Fatal(err)
	}
	certificate := &tls.Certificate{Certificate: clone2D(state.CertificateDER), PrivateKey: key, Leaf: leaf}
	client, err := fixture.profile.HTTPClientWithCertificate(fixture.roots, certificate)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(client.CloseIdleConnections)
	return client
}

func TestPullInstallsAsymmetricAuthorityEventOrderAndProjection(t *testing.T) {
	fixture := newClientFixture(t)
	state := enrollFixtureClient(t, fixture, t.TempDir())
	artifactSigner := registerClientFixtureArtifactKey(t, fixture)
	peer := peerStateFromClient(t, state)
	ctx := context.Background()
	createFixtureMatter(t, fixture.store, peer, artifactSigner, state, "01KZ7XHAQT1S46NYPN1PW1DX40", "01KZ7XHAQT1S46NYPN1PW1DX50", "01KZ7XHAQT1S46NYPN1PW1DX60", "Zulu title", "zulu-item", 1)
	createFixtureMatter(t, fixture.store, peer, artifactSigner, state, "01KZ7XHAQT1S46NYPN1PW1DX41", "01KZ7XHAQT1S46NYPN1PW1DX51", "01KZ7XHAQT1S46NYPN1PW1DX61", "Alpha title", "alpha-item", 2)

	directory := t.TempDir()
	installed := enrollFixtureClient(t, fixture, directory)
	if installed.Prefix.EventCount != 2 || len(installed.EventRecords) != 2 || len(installed.Projections) != 2 {
		t.Fatalf("non-empty seed installed wrong authority prefix: %+v", installed)
	}
	createFixtureMatter(t, fixture.store, peer, artifactSigner, state, "01KZ7XHAQT1S46NYPN1PW1DX42", "01KZ7XHAQT1S46NYPN1PW1DX52", "01KZ7XHAQT1S46NYPN1PW1DX62", "Middle title", "middle-item", 3)
	installed, err := PullAndInstall(ctx, fixture.profile, fixture.roots, directory)
	if err != nil {
		t.Fatal(err)
	}
	if installed.Prefix.EventCount != 3 || installed.Prefix.EventID == nil || *installed.Prefix.EventID != "01KZ7XHAQT1S46NYPN1PW1DX62" ||
		len(installed.EventRecords) != 3 || len(installed.Projections) != 3 {
		t.Fatalf("pull installed wrong asymmetric prefix: %+v", installed)
	}
	if installed.EventRecords[0].EventID != "01KZ7XHAQT1S46NYPN1PW1DX60" || installed.EventRecords[1].EventID != "01KZ7XHAQT1S46NYPN1PW1DX61" || installed.EventRecords[2].EventID != "01KZ7XHAQT1S46NYPN1PW1DX62" {
		t.Fatalf("event order = %v, want original authority order [..DX60, ..DX61, ..DX62]", []string{installed.EventRecords[0].EventID, installed.EventRecords[1].EventID, installed.EventRecords[2].EventID})
	}

	now := time.Now().UTC()
	snapshot, err := fixture.store.PinSnapshot(ctx, state.DomainID, state.Epoch, authoritystore.EmptyPrefixAnchor(), "01KZ7XHAQT1S46NYPN1PW1DX70", now, time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_ = fixture.store.ReleaseSnapshot(context.Background(), state.DomainID, state.Epoch, snapshot.ID)
	})
	if len(snapshot.Delta.Events) != len(installed.EventRecords) || snapshot.Delta.End.EventCount != installed.Prefix.EventCount || snapshot.Delta.End.Digest != installed.Prefix.Digest {
		t.Fatalf("installed anchor differs from authority snapshot: client=%+v authority=%+v", installed.Prefix, snapshot.Delta.End)
	}
	for index, event := range snapshot.Delta.Events {
		if installed.EventRecords[index].EventID != event.EventID || !bytes.Equal(installed.EventRecords[index].Record, event.Record) {
			t.Fatalf("installed event %d differs byte-for-byte/order from authority: client=%+v authority=%+v", index, installed.EventRecords[index], event)
		}
	}
	if len(snapshot.Items) != 3 || len(snapshot.Items) != len(installed.Projections) {
		t.Fatalf("projection counts differ: client=%d authority=%d", len(installed.Projections), len(snapshot.Items))
	}
	for index, item := range snapshot.Items {
		var authority eventProjection
		if err = wipdwire.DecodeCanonical(item.Value, &authority, "id", "repo_id", "locator", "title", "birth_event_id"); err != nil {
			t.Fatal(err)
		}
		var clientProjection eventProjection
		if err = json.Unmarshal(installed.Projections[index], &clientProjection); err != nil {
			t.Fatal(err)
		}
		if clientProjection != authority {
			t.Fatalf("projection %d differs: client=%+v authority=%+v", index, clientProjection, authority)
		}
	}
	if got := []string{projectionLocator(t, installed.Projections[0]), projectionLocator(t, installed.Projections[1]), projectionLocator(t, installed.Projections[2])}; !reflect.DeepEqual(got, []string{"alpha-item", "middle-item", "zulu-item"}) {
		t.Fatalf("projection order = %v, want stable locator order independent of event order", got)
	}

	corrupt := installed
	corrupt.Projections = append([]json.RawMessage(nil), installed.Projections...)
	corrupt.Projections[0] = json.RawMessage(`{"id":"01KZ7XHAQT1S46NYPN1PW1DX51","repo_id":"01KZ7XHAQT1S46NYPN1PW1DX3C","locator":"wrong-item","title":"Alpha title","birth_event_id":"01KZ7XHAQT1S46NYPN1PW1DX61"}`)
	if err = validateInstalledState(corrupt, fixture.profile); !errors.Is(err, ErrInvalidClientState) {
		t.Fatalf("asymmetric persisted projection = %v, want integrity refusal", err)
	}
}

func TestPullInstallsStepProjectionWhenMatterIsInPriorPrefix(t *testing.T) {
	fixture := newClientFixture(t)
	artifactSigner := registerClientFixtureArtifactKey(t, fixture)
	directory := t.TempDir()
	state := enrollFixtureClient(t, fixture, directory)
	peer := peerStateFromClient(t, state)
	ctx := context.Background()

	matterCommandID := "01KZ7XHAQT1S46NYPN1PW1DX70"
	matterID := "01KZ7XHAQT1S46NYPN1PW1DX71"
	createFixtureMatter(t, fixture.store, peer, artifactSigner, state, matterCommandID, matterID,
		"01KZ7XHAQT1S46NYPN1PW1DX72", "Authority Matter", "authority-matter", 1)
	state, err := PullAndInstall(ctx, fixture.profile, fixture.roots, directory)
	if err != nil || state.Prefix.EventCount != 1 || len(state.Projections) != 1 || len(state.StepProjections) != 0 {
		t.Fatalf("install preceding Matter prefix: state=%+v err=%v", state.Prefix, err)
	}
	state.StepProjections = nil // A Step 7 client-state file predates this derived projection.
	legacyState, err := json.Marshal(state)
	if err != nil {
		t.Fatal(err)
	}
	var legacyFields map[string]json.RawMessage
	if err = json.Unmarshal(legacyState, &legacyFields); err != nil {
		t.Fatal(err)
	}
	delete(legacyFields, "step_projections")
	legacyState, err = json.Marshal(legacyFields)
	if err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(filepath.Join(directory, stateName), legacyState, 0o600); err != nil {
		t.Fatal(err)
	}

	stepCommandID := "01KZ7XHAQT1S46NYPN1PW1DX73"
	stepID := "01KZ7XHAQT1S46NYPN1PW1DX74"
	stepEventID := "01KZ7XHAQT1S46NYPN1PW1DX75"
	stepCommand := operation.Command{
		ID: stepCommandID, AuthorityDomainID: state.DomainID, ExpectedAuthorityEpoch: state.Epoch,
		EnvironmentID: state.EnvironmentID, EnvironmentSequence: 2, ActedAt: time.Now().UTC().Truncate(time.Second).Format(time.RFC3339Nano),
		CausationCommandID: matterCommandID, CorrelationCommandID: matterCommandID,
		Request: operation.Request{
			Operation: operation.StepCreateV1.Metadata().Operation, Actor: "human",
			Context: operation.Context{Repo: state.RepoID}, Claim: &operation.ClaimContext{ID: matterID, Epoch: "1"},
			Input: operation.StepCreateInput{ParentID: matterID, Title: "First Step"}, Blobs: []operation.BlobInput{},
		},
	}
	stepHash, err := stepCommand.RequestHash()
	if err != nil {
		t.Fatal(err)
	}
	stepSubmission, err := fixture.store.SubmitCommand(ctx, stepCommand, stepHash, peer, time.Now().UTC())
	if err != nil || stepSubmission.Owner == nil {
		t.Fatalf("submit Step birth: status=%+v err=%v", stepSubmission, err)
	}
	stepResult := operation.Result{Code: operation.ResultSucceeded, Output: operation.StepCreateOutput{
		ParentID: matterID, Title: "First Step",
	}}
	if _, err = fixture.store.CompleteCommand(ctx, stepSubmission.Owner, stepResult, stepID, stepEventID, time.Now().UTC(), func(_ context.Context, message []byte) ([]byte, error) {
		return ed25519.Sign(artifactSigner, message), nil
	}); err != nil {
		t.Fatalf("complete Step birth: %v", err)
	}

	state, err = PullAndInstall(ctx, fixture.profile, fixture.roots, directory)
	if err != nil {
		t.Fatal(err)
	}
	if state.Prefix.EventCount != 2 || len(state.EventRecords) != 2 || len(state.Projections) != 1 || len(state.StepProjections) != 1 ||
		state.EventRecords[1].EventID != stepEventID {
		t.Fatalf("incremental Step pull did not install complete birth state: prefix=%+v matters=%d steps=%d records=%+v",
			state.Prefix, len(state.Projections), len(state.StepProjections), state.EventRecords)
	}
	var projection stepProjection
	if err = json.Unmarshal(state.StepProjections[0], &projection); err != nil {
		t.Fatal(err)
	}
	if projection != (stepProjection{
		ID: stepID, RepoID: state.RepoID, MatterID: matterID, Locator: "step-01", Title: "First Step",
		SortKey: 1000, State: "planned", BirthEventID: stepEventID,
	}) {
		t.Fatalf("incremental Step projection = %+v", projection)
	}
	if err = validateInstalledState(state, fixture.profile); err != nil {
		t.Fatalf("valid pulled Matter+Step state failed integrity check: %v", err)
	}
	corrupt := state
	corrupt.StepProjections = append([]json.RawMessage(nil), state.StepProjections...)
	corrupt.StepProjections[0] = json.RawMessage(`{"id":"01KZ7XHAQT1S46NYPN1PW1DX74","repo_id":"01KZ7XHAQT1S46NYPN1PW1DX3C","matter_id":"01KZ7XHAQT1S46NYPN1PW1DX71","locator":"step-02","title":"First Step","sort_key":1000,"state":"planned","birth_event_id":"01KZ7XHAQT1S46NYPN1PW1DX75"}`)
	if err = validateInstalledState(corrupt, fixture.profile); !errors.Is(err, ErrInvalidClientState) {
		t.Fatalf("corrupt Step projection = %v, want integrity refusal", err)
	}
}

func TestPullInstallsBirthClaimReleaseAndReopensForLaterPull(t *testing.T) {
	fixture := newClientFixture(t)
	artifactSigner := registerClientFixtureArtifactKey(t, fixture)
	directory := t.TempDir()
	state := enrollFixtureClient(t, fixture, directory)
	peer := peerStateFromClient(t, state)
	ctx := context.Background()
	const (
		matterCommandID = "01KZ7XHAQT1S46NYPN1PW1DX76"
		matterID        = "01KZ7XHAQT1S46NYPN1PW1DX77"
		matterEventID   = "01KZ7XHAQT1S46NYPN1PW1DX82"
		stepCommandID   = "01KZ7XHAQT1S46NYPN1PW1DX78"
		stepID          = "01KZ7XHAQT1S46NYPN1PW1DX79"
		stepEventID     = "01KZ7XHAQT1S46NYPN1PW1DX83"
		releaseID       = "01KZ7XHAQT1S46NYPN1PW1DX80"
		releaseEventID  = "01KZ7XHAQT1S46NYPN1PW1DX84"
		laterCommandID  = "01KZ7XHAQT1S46NYPN1PW1DX81"
		laterMatterID   = "01KZ7XHAQT1S46NYPN1PW1DX85"
		laterEventID    = "01KZ7XHAQT1S46NYPN1PW1DX86"
	)

	createFixtureMatter(t, fixture.store, peer, artifactSigner, state, matterCommandID, matterID,
		matterEventID, "Birth journal Matter", "birth-journal-matter", 1)
	stepCommand := operation.Command{
		ID: stepCommandID, AuthorityDomainID: state.DomainID, ExpectedAuthorityEpoch: state.Epoch,
		EnvironmentID: state.EnvironmentID, EnvironmentSequence: 2, ActedAt: time.Now().UTC().Truncate(time.Second).Format(time.RFC3339Nano),
		CausationCommandID: matterCommandID, CorrelationCommandID: matterCommandID,
		Request: operation.Request{
			Operation: operation.StepCreateV1.Metadata().Operation, Actor: "human",
			Context: operation.Context{Repo: state.RepoID}, Claim: &operation.ClaimContext{ID: matterID, Epoch: "1"},
			Input: operation.StepCreateInput{ParentID: matterID, Title: "Birth journal Step"}, Blobs: []operation.BlobInput{},
		},
	}
	stepHash, err := stepCommand.RequestHash()
	if err != nil {
		t.Fatal(err)
	}
	stepSubmission, err := fixture.store.SubmitCommand(ctx, stepCommand, stepHash, peer, time.Now().UTC())
	if err != nil || stepSubmission.Owner == nil {
		t.Fatalf("submit birth Step: status=%+v err=%v", stepSubmission, err)
	}
	stepResult := operation.Result{Code: operation.ResultSucceeded, Output: operation.StepCreateOutput{
		ParentID: matterID, Title: "Birth journal Step",
	}}
	if _, err = fixture.store.CompleteCommand(ctx, stepSubmission.Owner, stepResult, stepID, stepEventID, time.Now().UTC(), func(_ context.Context, message []byte) ([]byte, error) {
		return ed25519.Sign(artifactSigner, message), nil
	}); err != nil {
		t.Fatalf("complete birth Step: %v", err)
	}

	state, err = PullAndInstall(ctx, fixture.profile, fixture.roots, directory)
	if err != nil || state.Prefix.EventCount != 2 || len(state.EventRecords) != 2 || state.EventRecords[1].EventID != stepEventID {
		t.Fatalf("install birth journal prefix before release: prefix=%+v records=%+v err=%v", state.Prefix, state.EventRecords, err)
	}

	queryReceipt := func(record wipdwire.EventRecord) (string, string, []byte) {
		t.Helper()
		fields, decodeErr := wipdwire.DecodeCanonicalMap(record.Record,
			"schema", "event_id", "domain_id", "command_id", "request_hash", "environment", "acted_at", "occurred_at", "kind", "subject_id", "repo_id", "payload")
		if decodeErr != nil {
			t.Fatal(decodeErr)
		}
		commandID, idOK := fields["command_id"].(string)
		requestHash, hashOK := fields["request_hash"].(string)
		if !idOK || !hashOK {
			t.Fatalf("event lacks command identity: %+v", fields)
		}
		status, queryErr := fixture.store.QueryCommand(ctx, state.DomainID, commandID, requestHash, state.Epoch, peer, state.EnvironmentID, time.Now().UTC())
		if queryErr != nil || status.Pending || len(status.Receipt) == 0 {
			t.Fatalf("query terminal event receipt: status=%+v err=%v", status, queryErr)
		}
		return commandID, requestHash, status.Receipt
	}
	barrierEntries := make([]wipdwire.JournalBarrierEntry, 0, 2)
	for index, record := range state.EventRecords {
		commandID, requestHash, receiptBytes := queryReceipt(record)
		receipt, decodeErr := wipdwire.DecodeCanonicalMap(receiptBytes,
			"schema", "domain_id", "authority_epoch", "identity_schema", "command_id", "request_hash", "operation", "environment", "result", "accepted_events")
		if decodeErr != nil || receipt["command_id"] != commandID || receipt["request_hash"] != requestHash {
			t.Fatalf("decode exact terminal receipt: receipt=%+v err=%v", receipt, decodeErr)
		}
		result, ok := receipt["result"].(map[string]any)
		if !ok || result["code"] != "result.succeeded" {
			t.Fatalf("birth journal receipt %s is not successful: %+v", commandID, result)
		}
		rangeFields, ok := receipt["accepted_events"].(map[string]any)
		if !ok || !wipdwire.ExactMapKeys(rangeFields, "first_event_id", "last_event_id", "event_count") {
			t.Fatalf("birth journal receipt %s has no exact event range: %+v", commandID, receipt["accepted_events"])
		}
		first, firstOK := rangeFields["first_event_id"].(string)
		last, lastOK := rangeFields["last_event_id"].(string)
		count, countOK := rangeFields["event_count"].(uint64)
		if !firstOK || !lastOK || !countOK || count != 1 || first != record.EventID || last != record.EventID {
			t.Fatalf("birth journal range %d does not exactly cover installed record %s: %+v", index+1, record.EventID, rangeFields)
		}
		ack := wipdwire.BirthJournalAck{
			Schema: "wipd.birth-journal-ack/1", DomainID: state.DomainID, Epoch: state.Epoch,
			MatterID: matterID, CommandID: commandID, RequestHash: requestHash,
			Receipt: bytes.Clone(receiptBytes), Installed: state.Prefix,
		}
		if err = fixture.store.AcknowledgeBirthJournalEntry(ctx, ack, peer, state.EnvironmentID, time.Now().UTC()); err != nil {
			t.Fatalf("acknowledge installed birth receipt %s: %v", commandID, err)
		}
		barrierEntries = append(barrierEntries, wipdwire.JournalBarrierEntry{
			Position: uint64(index + 1), CommandID: commandID, RequestHash: requestHash,
			ResultCode: "result.succeeded",
			Range:      &wipdwire.JournalBarrierRange{First: first, Last: last, Count: count},
		})
	}
	barrierDigest, err := wipdwire.JournalBarrierDigest(barrierEntries)
	if err != nil {
		t.Fatal(err)
	}
	barrier := map[string]any{
		"schema": "wipd.journal-barrier/1", "journal_id": matterID,
		"claim":       map[string]any{"id": matterID, "epoch": uint64(1)},
		"entry_count": uint64(2), "last_position": uint64(2), "terminal_receipt_count": uint64(2),
		"entries_digest": barrierDigest, "sealed": true, "unresolved_count": uint64(0), "quarantined_count": uint64(0),
	}
	releaseRaw, err := wipdwire.EncodeCanonical(map[string]any{
		"schema": "wipd.command/1", "command_id": releaseID,
		"authority":   map[string]any{"domain_id": state.DomainID, "expected_epoch": state.Epoch},
		"environment": map[string]any{"id": state.EnvironmentID, "sequence": uint64(3)},
		"acted_at":    time.Now().UTC().Truncate(time.Second).Format(time.RFC3339Nano), "actor": "human",
		"causation_command_id": nil, "correlation_command_id": releaseID,
		"operation": map[string]any{"name": "claim.release", "version": uint64(1)},
		"context":   map[string]any{"repo_id": state.RepoID, "clone_id": nil, "worktree_id": nil},
		"claim":     map[string]any{"id": matterID, "epoch": uint64(1)},
		"input":     map[string]any{"barrier": barrier}, "blobs": []any{},
	})
	if err != nil {
		t.Fatal(err)
	}
	releaseHash := testDigest(append([]byte("wipd/request-hash/v1\x00"), releaseRaw...))
	pending, err := fixture.store.SubmitClaimLifecycle(ctx, releaseRaw, releaseHash, peer, time.Now().UTC(), nil)
	if err != nil || pending.Owner == nil {
		t.Fatalf("submit birth claim release: status=%+v err=%v", pending, err)
	}
	released, err := fixture.store.CompleteClaimLifecycle(ctx, pending.Owner, "", []string{releaseEventID}, time.Now().UTC(), func(_ context.Context, message []byte) ([]byte, error) {
		return ed25519.Sign(artifactSigner, message), nil
	})
	if err != nil || released.Pending || len(released.Receipt) == 0 {
		t.Fatalf("commit durable birth claim release: status=%+v err=%v", released, err)
	}
	releaseReceipt, err := wipdwire.DecodeCanonicalMap(released.Receipt,
		"schema", "domain_id", "authority_epoch", "identity_schema", "command_id", "request_hash", "operation", "environment", "result", "accepted_events")
	if err != nil || releaseReceipt["command_id"] != releaseID || releaseReceipt["request_hash"] != releaseHash {
		t.Fatalf("release terminal receipt identity=%+v err=%v", releaseReceipt, err)
	}
	resultFields, ok := releaseReceipt["result"].(map[string]any)
	if !ok || resultFields["code"] != "result.succeeded" {
		t.Fatalf("release receipt is not successful: %+v", resultFields)
	}
	outputBytes, ok := resultFields["output"].([]byte)
	if !ok {
		t.Fatalf("release receipt output has type %T", resultFields["output"])
	}
	output, err := wipdwire.DecodeCanonicalMap(outputBytes, "claim_id", "claim_epoch", "dispatch_id", "barrier_digest")
	if err != nil || output["claim_id"] != matterID || output["claim_epoch"] != uint64(1) || output["dispatch_id"] != nil || output["barrier_digest"] != barrierDigest {
		t.Fatalf("birth release receipt output=%+v err=%v", output, err)
	}

	client := installedClient(t, fixture, state)
	limits, err := negotiateRemote(ctx, client, fixture.profile.Origin())
	if err != nil {
		t.Fatalf("negotiate production pull client at pre-release prefix: %v", err)
	}
	requestFrame, requestID, err := encodeRequestFrame("pull.request", wipdwire.PullRequest{
		Schema: "wipd.pull-request/1", DomainID: state.DomainID, Epoch: state.Epoch, Installed: state.Prefix,
	})
	if err != nil {
		t.Fatal(err)
	}
	frames, err := postFramesWithinSession(ctx, client, fixture.profile.Origin()+"/wipd/v1/exchange",
		requestFrame, requestID, maxClientTransferEvents+3, limits)
	if err != nil {
		t.Fatalf("pull dispatch-less release tail over production mTLS exchange: %v", err)
	}
	verifiedTransfer, _, err := VerifyPullTransfer(fixture.profile, state, state.Prefix, frames)
	client.CloseIdleConnections()
	if err != nil || verifiedTransfer.End().EventCount != 3 {
		t.Fatalf("verify production release pull tail: end=%+v err=%v", verifiedTransfer.End(), err)
	}

	state, err = PullAndInstall(ctx, fixture.profile, fixture.roots, directory)
	if err != nil || state.Prefix.EventCount != 3 || len(state.EventRecords) != 3 || state.EventRecords[2].EventID != releaseEventID {
		t.Fatalf("pull/install dispatch-less claim.released: prefix=%+v records=%+v err=%v", state.Prefix, state.EventRecords, err)
	}
	if err = validateInstalledState(state, fixture.profile); err != nil {
		t.Fatalf("validate installed release prefix: %v", err)
	}
	reopened, _, err := loadInstalledState(directory)
	if err != nil || reopened.Prefix.EventCount != state.Prefix.EventCount || !reflect.DeepEqual(reopened.EventRecords, state.EventRecords) ||
		!reflect.DeepEqual(reopened.Projections, state.Projections) || !reflect.DeepEqual(reopened.StepProjections, state.StepProjections) {
		t.Fatalf("reopen did not retain verified release prefix/projection: state=%+v err=%v", reopened.Prefix, err)
	}

	for name, mutate := range map[string]func(map[string]any, map[string]any){
		"dispatch must be null": func(_ map[string]any, payload map[string]any) { payload["dispatch_id"] = laterCommandID },
		"claim epoch is fixed":  func(_ map[string]any, payload map[string]any) { payload["claim_epoch"] = uint64(2) },
		"subject is the claim":  func(event map[string]any, _ map[string]any) { event["subject_id"] = laterMatterID },
		"barrier digest is exact": func(_ map[string]any, payload map[string]any) {
			payload["barrier_digest"] = testDigest([]byte("wrong birth receipt barrier"))
		},
	} {
		t.Run(name, func(t *testing.T) {
			candidate := append([]wipdwire.EventRecord(nil), state.EventRecords...)
			event, decodeErr := wipdwire.DecodeCanonicalMap(candidate[2].Record,
				"schema", "event_id", "domain_id", "command_id", "request_hash", "environment", "acted_at", "occurred_at", "kind", "subject_id", "repo_id", "payload")
			if decodeErr != nil {
				t.Fatal(decodeErr)
			}
			payload, ok := event["payload"].(map[string]any)
			if !ok {
				t.Fatalf("release event payload type %T", event["payload"])
			}
			mutate(event, payload)
			candidate[2].Record, decodeErr = wipdwire.EncodeCanonical(event)
			if decodeErr != nil {
				t.Fatal(decodeErr)
			}
			if _, _, _, foldErr := foldEventRecords(candidate, state.DomainID); !errors.Is(foldErr, ErrInvalidClientState) {
				t.Fatalf("malformed release event folded: %v", foldErr)
			}
		})
	}

	createFixtureMatter(t, fixture.store, peer, artifactSigner, state, laterCommandID, laterMatterID,
		laterEventID, "After release", "after-birth-release", 4)
	state, err = PullAndInstall(ctx, fixture.profile, fixture.roots, directory)
	if err != nil || state.Prefix.EventCount != 4 || len(state.EventRecords) != 4 || state.EventRecords[3].EventID != laterEventID ||
		len(state.Projections) != 2 || len(state.StepProjections) != 1 {
		t.Fatalf("later pull from reopened release prefix: prefix=%+v records=%+v matters=%d steps=%d err=%v",
			state.Prefix, state.EventRecords, len(state.Projections), len(state.StepProjections), err)
	}
}

func TestClaimGrantManifestPinsOnlyAcquiredMatterContent(t *testing.T) {
	matterWithContent := "01KZ7XHAQT1S46NYPN1PW1DX90"
	claimedMatter := "01KZ7XHAQT1S46NYPN1PW1DX91"
	claimID := "01KZ7XHAQT1S46NYPN1PW1DX92"
	otherBytes := []byte("earlier matter content")
	claimBytes := []byte("claimed matter content has another length")
	otherDigest := testDigest(otherBytes)
	claimDigest := testDigest(claimBytes)
	content := []json.RawMessage{}
	for _, projection := range []contentProjection{
		{
			ID: "01KZ7XHAQT1S46NYPN1PW1DX93", SubjectID: matterWithContent, MatterID: matterWithContent,
			RepoID: testRepoID, Kind: "brief", BlobDigest: otherDigest, ByteLength: uint64(len(otherBytes)),
		},
		{
			ID: "01KZ7XHAQT1S46NYPN1PW1DX94", SubjectID: claimedMatter, MatterID: claimedMatter,
			RepoID: testRepoID, Kind: "findings", BlobDigest: claimDigest, ByteLength: uint64(len(claimBytes)),
		},
	} {
		raw, err := json.Marshal(projection)
		if err != nil {
			t.Fatal(err)
		}
		content = append(content, raw)
	}
	const eventID = "01KZ7XHAQT1S46NYPN1PW1DX95"
	const commandID = "01KZ7XHAQT1S46NYPN1PW1DX96"
	const environmentID = "01KZ7XHAQT1S46NYPN1PW1DX97"
	const actedAt = "2026-09-23T11:59:00Z"
	acquired, err := wipdwire.EncodeCanonical(map[string]any{
		"schema": "wipd.event/1", "event_id": eventID, "domain_id": testDomainID,
		"command_id": commandID, "request_hash": testDigest([]byte("acquire")),
		"environment": map[string]any{"id": environmentID, "sequence": uint64(3)},
		"acted_at":    actedAt, "occurred_at": actedAt, "kind": "claim.acquired",
		"subject_id": claimID, "repo_id": testRepoID, "payload": map[string]any{"matter_id": claimedMatter},
	})
	if err != nil {
		t.Fatal(err)
	}
	records := []wipdwire.EventRecord{{EventID: eventID, Record: acquired}}
	manifest := []wipdwire.BlobManifestEntry{
		{Digest: otherDigest, ByteLength: uint64(len(otherBytes)), Requirement: "lazy"},
		{Digest: claimDigest, ByteLength: uint64(len(claimBytes)), Requirement: "pin-before-use"},
	}
	if err = validateClaimContentManifest(content, records, manifest); err != nil {
		t.Fatalf("valid domain manifest with unrelated lazy content rejected: %v", err)
	}

	for name, mutate := range map[string]func([]wipdwire.BlobManifestEntry){
		"unrelated content marked required": func(entries []wipdwire.BlobManifestEntry) { entries[0].Requirement = "pin-before-use" },
		"claimed content left lazy":         func(entries []wipdwire.BlobManifestEntry) { entries[1].Requirement = "lazy" },
		"unbacked digest": func(entries []wipdwire.BlobManifestEntry) {
			entries[0].Digest = testDigest([]byte("not in accepted content"))
		},
		"wrong byte length": func(entries []wipdwire.BlobManifestEntry) { entries[1].ByteLength++ },
	} {
		t.Run(name, func(t *testing.T) {
			candidate := append([]wipdwire.BlobManifestEntry(nil), manifest...)
			mutate(candidate)
			if err := validateClaimContentManifest(content, records, candidate); !errors.Is(err, ErrInvalidClientState) {
				t.Fatalf("invalid claim-scoped manifest accepted: %v", err)
			}
		})
	}
}

func TestLifecycleFoldAllowsIncompleteMatterFinishBeforeStepCompletion(t *testing.T) {
	domainID, repoID, environmentID := testDomainID, testRepoID, "01KZ7XHAQT1S46NYPN1PW1DX3A"
	matterID, stepID := "01KZ7XHAQT1S46NYPN1PW1DX90", "01KZ7XHAQT1S46NYPN1PW1DX91"
	batchID, claimID := "01KZ7XHAQT1S46NYPN1PW1DX92", "01KZ7XHAQT1S46NYPN1PW1DX93"
	dispatchID, worktreeID := "01KZ7XHAQT1S46NYPN1PW1DX94", "01KZ7XHAQT1S46NYPN1PW1DX95"
	const actedAt = "2026-09-23T11:59:00Z"
	event := func(eventNumber, commandNumber int, sequence uint64, kind, subject string, payload any) wipdwire.EventRecord {
		t.Helper()
		eventID := fmt.Sprintf("%026d", eventNumber)
		commandID := fmt.Sprintf("%026d", commandNumber)
		hash := testDigest([]byte("lifecycle-fold-command:" + commandID))
		record, err := wipdwire.EncodeCanonical(map[string]any{
			"schema": "wipd.event/1", "event_id": eventID, "domain_id": domainID,
			"command_id": commandID, "request_hash": hash,
			"environment": map[string]any{"id": environmentID, "sequence": sequence},
			"acted_at":    actedAt, "occurred_at": actedAt, "kind": kind,
			"subject_id": subject, "repo_id": repoID, "payload": payload,
		})
		if err != nil {
			t.Fatal(err)
		}
		return wipdwire.EventRecord{EventID: eventID, Record: record}
	}
	records := []wipdwire.EventRecord{
		event(500, 400, 1, "matter.created", matterID, map[string]any{"id": matterID, "locator": "incomplete", "title": "Incomplete matter"}),
		event(501, 401, 2, "step.created", stepID, map[string]any{"title": "Unfinished child", "locator": "step-01", "parent": matterID, "sort_key": int64(1000)}),
		event(502, 402, 3, "batch.anonymous-created", batchID, map[string]any{"batch_id": batchID, "matter_id": matterID}),
		event(503, 402, 3, "claim.acquired", claimID, map[string]any{
			"claim_id": claimID, "claim_epoch": uint64(1), "matter_id": matterID,
			"batch_id": batchID, "dispatch_id": dispatchID, "owner_environment_id": environmentID, "worktree_id": worktreeID,
		}),
		event(504, 402, 3, "dispatch.opened", dispatchID, map[string]any{
			"dispatch_id": dispatchID, "matter_id": matterID, "batch_id": batchID,
			"claim_id": claimID, "worktree_id": worktreeID,
		}),
		event(505, 403, 4, "matter.started", matterID, map[string]any{"from": "planned", "to": "in-progress", "cascade": true}),
		event(506, 403, 4, "step.started", stepID, map[string]any{
			"from": "planned", "to": "in-progress", "cause_event_id": fmt.Sprintf("%026d", 505),
		}),
		event(507, 404, 5, "matter.finished", matterID, map[string]any{"from": "in-progress", "to": "done"}),
	}
	_, projections, steps, err := foldEventRecords(records, domainID)
	if err != nil || len(projections) != 1 || len(steps) != 1 {
		t.Fatalf("fold incomplete Matter finish: matters=%d steps=%d err=%v", len(projections), len(steps), err)
	}
	wrongCause := append([]wipdwire.EventRecord(nil), records...)
	wrongCause[6] = event(506, 403, 4, "step.started", stepID, map[string]any{
		"from": "planned", "to": "in-progress", "cause_event_id": fmt.Sprintf("%026d", 499),
	})
	if _, _, _, err = foldEventRecords(wrongCause, domainID); !errors.Is(err, ErrInvalidClientState) {
		t.Fatalf("Step start with a cause unrelated to the preceding cascade event folded: %v", err)
	}
	var matter eventProjection
	var step stepProjection
	if json.Unmarshal(projections[0], &matter) != nil || json.Unmarshal(steps[0], &step) != nil ||
		matter.ID != matterID || matter.State != "done" || step.ID != stepID || step.State != "in-progress" {
		t.Fatalf("incomplete finish projections = matter %+v, Step %+v", matter, step)
	}

	records = append(records, event(508, 405, 6, "step.finished", stepID, map[string]any{"from": "in-progress", "to": "done"}))
	_, projections, steps, err = foldEventRecords(records, domainID)
	if err != nil || json.Unmarshal(projections[0], &matter) != nil || json.Unmarshal(steps[0], &step) != nil ||
		matter.State != "done" || step.State != "done" {
		t.Fatalf("fold Step completion after incomplete Matter finish: matter=%+v step=%+v err=%v", matter, step, err)
	}
	if _, _, _, err := foldEventRecords(append(append([]wipdwire.EventRecord(nil), records...),
		event(509, 404, 5, "batch.swept", batchID, map[string]any{})), domainID); !errors.Is(err, ErrInvalidClientState) {
		t.Fatalf("sweep after a Matter finish with an incomplete child folded: %v", err)
	}

	barrierDigest := testDigest([]byte("acquired claim close barrier"))
	closed := append(append([]wipdwire.EventRecord(nil), records...),
		event(509, 406, 7, "dispatch.closed", dispatchID, map[string]any{
			"dispatch_id": dispatchID, "claim_id": claimID, "claim_epoch": uint64(1),
		}),
		event(510, 406, 7, "claim.released", claimID, map[string]any{
			"claim_id": claimID, "claim_epoch": uint64(1), "dispatch_id": dispatchID, "barrier_digest": barrierDigest,
		}))
	if _, _, _, err = foldEventRecords(closed, domainID); err != nil {
		t.Fatalf("fold exact acquired Dispatch/claim release pair: %v", err)
	}
	for name, mutate := range map[string]func(map[string]any, map[string]any){
		"wrong closed Dispatch": func(_ map[string]any, payload map[string]any) { payload["dispatch_id"] = batchID },
		"wrong claim epoch":     func(_ map[string]any, payload map[string]any) { payload["claim_epoch"] = uint64(2) },
	} {
		t.Run(name, func(t *testing.T) {
			candidate := append([]wipdwire.EventRecord(nil), closed...)
			fields, decodeErr := wipdwire.DecodeCanonicalMap(candidate[len(candidate)-2].Record,
				"schema", "event_id", "domain_id", "command_id", "request_hash", "environment", "acted_at", "occurred_at", "kind", "subject_id", "repo_id", "payload")
			if decodeErr != nil {
				t.Fatal(decodeErr)
			}
			payload, ok := fields["payload"].(map[string]any)
			if !ok {
				t.Fatalf("dispatch.closed payload has type %T", fields["payload"])
			}
			mutate(fields, payload)
			candidate[len(candidate)-2].Record, decodeErr = wipdwire.EncodeCanonical(fields)
			if decodeErr != nil {
				t.Fatal(decodeErr)
			}
			if _, _, _, foldErr := foldEventRecords(candidate, domainID); !errors.Is(foldErr, ErrInvalidClientState) {
				t.Fatalf("mismatched acquired Dispatch close folded: %v", foldErr)
			}
		})
	}
	if _, _, _, err = foldEventRecords(closed[:len(closed)-1], domainID); !errors.Is(err, ErrInvalidClientState) {
		t.Fatalf("unpaired acquired Dispatch close folded: %v", err)
	}
}

func TestAuthenticatedAcquisitionGrantInstallAllowsLaterPullAndReopen(t *testing.T) {
	fixture := newClientFixture(t)
	artifactSigner, artifactCertificate := registerClientFixtureArtifactKeyWithCertificate(t, fixture)
	directory := t.TempDir()
	initial := enrollFixtureClient(t, fixture, directory)
	peer := peerStateFromClient(t, initial)
	ctx := context.Background()
	const (
		matterCommandID = "01KZ7XHAQT1S46NYPN1PW1DX51"
		matterID        = "01KZ7XHAQT1S46NYPN1PW1DX52"
		matterEventID   = "01KZ7XHAQT1S46NYPN1PW1DX60"
		releaseID       = "01KZ7XHAQT1S46NYPN1PW1DX5C"
		releaseEventID  = "01KZ7XHAQT1S46NYPN1PW1DX65"
		acquireID       = "01KZ7XHAQT1S46NYPN1PW1DX53"
		worktreeID      = "01KZ7XHAQT1S46NYPN1PW1DX54"
		dispatchID      = "01KZ7XHAQT1S46NYPN1PW1DX55"
		claimID         = "01KZ7XHAQT1S46NYPN1PW1DX56"
		batchID         = "01KZ7XHAQT1S46NYPN1PW1DX57"
		grantID         = "01KZ7XHAQT1S46NYPN1PW1DX58"
		snapshotID      = "01KZ7XHAQT1S46NYPN1PW1DX59"
		journalID       = "01KZ7XHAQT1S46NYPN1PW1DX5A"
		batchEventID    = "01KZ7XHAQT1S46NYPN1PW1DX70"
		claimEventID    = "01KZ7XHAQT1S46NYPN1PW1DX71"
		dispatchEventID = "01KZ7XHAQT1S46NYPN1PW1DX72"
		laterCommandID  = "01KZ7XHAQT1S46NYPN1PW1DX73"
		laterMatterID   = "01KZ7XHAQT1S46NYPN1PW1DX74"
		laterEventID    = "01KZ7XHAQT1S46NYPN1PW1DX80"
	)
	createFixtureMatter(t, fixture.store, peer, artifactSigner, initial, matterCommandID, matterID,
		matterEventID, "Acquisition source", "acquisition-source", 1)
	initialTransfer, _, err := VerifyPullTransfer(fixture.profile, initial, initial.Prefix,
		authenticatedPullFrames(t, fixture, initial))
	if err != nil || initialTransfer.End().EventCount != 1 {
		t.Fatalf("verify authenticated initial Matter pull: end=%+v err=%v", initialTransfer.End(), err)
	}
	state, err := PullAndInstall(ctx, fixture.profile, fixture.roots, directory)
	if err != nil || state.Prefix.EventCount != 1 {
		t.Fatalf("install Matter prefix before acquisition: %+v %v", state.Prefix, err)
	}

	journalIdentity := wipdjournal.Identity{
		RepoID: state.RepoID, DomainID: state.DomainID, AuthorityEpoch: state.Epoch,
		EnvironmentID: state.EnvironmentID, OwnerRootSPKI: state.OwnerKeyID,
	}
	journalRoot := filepath.Join(t.TempDir(), "environment-journal")
	journal, err := wipdjournal.Open(journalRoot, journalIdentity)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if journal != nil {
			_ = journal.Close()
		}
	})
	initialSnapshot, err := journal.InstallPull(ctx, installSnapshotExpectation(t, journal), initialTransfer)
	if err != nil || initialSnapshot.Anchor.EventCount != 1 {
		t.Fatalf("install initial Matter prefix into journal: %+v %v", initialSnapshot.Anchor, err)
	}
	completeFixtureBirthRelease(t, fixture, peer, state, matterID, releaseID, releaseEventID, artifactSigner)
	releaseTransfer, _, err := VerifyPullTransfer(fixture.profile, state, state.Prefix,
		authenticatedPullFrames(t, fixture, state))
	if err != nil || releaseTransfer.End().EventCount != 2 || len(releaseTransfer.Records()) != 1 {
		t.Fatalf("verify authenticated birth release before acquisition: end=%+v err=%v", releaseTransfer.End(), err)
	}
	initialSnapshot, err = journal.InstallPull(ctx, initialSnapshot.Expectation(), releaseTransfer)
	if err != nil || initialSnapshot.Anchor.EventCount != 2 {
		t.Fatalf("install released birth prefix before acquisition: %+v %v", initialSnapshot.Anchor, err)
	}
	state, err = PullAndInstall(ctx, fixture.profile, fixture.roots, directory)
	if err != nil || state.Prefix.EventCount != 2 {
		t.Fatalf("persist released birth prefix before acquisition: %+v %v", state.Prefix, err)
	}

	authorityAnchor, err := fixture.store.CurrentPrefixAnchor(ctx, state.DomainID)
	if err != nil || authorityAnchor.EventCount != state.Prefix.EventCount || authorityAnchor.Digest != state.Prefix.Digest {
		t.Fatalf("authority/client pre-acquisition anchors disagree: authority=%+v client=%+v err=%v", authorityAnchor, state.Prefix, err)
	}
	installed := wipdwire.PrefixAnchor{EventCount: authorityAnchor.EventCount, Digest: authorityAnchor.Digest}
	if authorityAnchor.EventID != "" {
		id := authorityAnchor.EventID
		installed.EventID = &id
	}
	acquireRaw, err := wipdwire.EncodeCanonical(map[string]any{
		"schema": "wipd.command/1", "command_id": acquireID,
		"authority":   map[string]any{"domain_id": state.DomainID, "expected_epoch": state.Epoch},
		"environment": map[string]any{"id": state.EnvironmentID, "sequence": uint64(3)},
		"acted_at":    time.Now().UTC().Truncate(time.Second).Format(time.RFC3339Nano), "actor": "human",
		"causation_command_id": nil, "correlation_command_id": acquireID,
		"operation": map[string]any{"name": "claim.acquire", "version": uint64(1)},
		"context":   map[string]any{"repo_id": state.RepoID, "clone_id": "01KZ7XHAQT1S46NYPN1PW1DX5B", "worktree_id": worktreeID},
		"claim":     nil,
		"input": map[string]any{
			"matter_id": matterID, "worktree_id": worktreeID, "dispatch_mode": "anonymous-matter",
			"requested_dispatch_id": dispatchID,
		},
		"blobs": []any{},
	})
	if err != nil {
		t.Fatal(err)
	}
	requestHash := testDigest(append([]byte("wipd/request-hash/v1\x00"), acquireRaw...))
	pending, err := fixture.store.SubmitClaimAcquire(ctx, acquireRaw, requestHash, authorityAnchor, peer, time.Now().UTC())
	if err != nil || !pending.Pending || pending.Owner == nil {
		t.Fatalf("submit authenticated acquisition: status=%+v err=%v", pending, err)
	}
	completed, authorityGrant, err := fixture.store.CompleteClaimAcquire(ctx, pending.Owner, authoritystore.AcquireAllocation{
		ClaimID: claimID, BatchID: batchID, GrantID: grantID, SnapshotID: snapshotID, JournalID: journalID,
		Installed: authorityAnchor, EventIDs: []string{batchEventID, claimEventID, dispatchEventID},
	}, time.Now().UTC(), func(_ context.Context, message []byte) ([]byte, error) {
		return ed25519.Sign(artifactSigner, message), nil
	})
	if err != nil || completed.Pending || len(completed.Receipt) == 0 || authorityGrant.ID != grantID {
		t.Fatalf("complete real claim acquisition: status=%+v grant=%+v err=%v", completed, authorityGrant, err)
	}

	grantTransfer, grantManifest, err := VerifyPullTransfer(fixture.profile, state, state.Prefix,
		authenticatedPullFrames(t, fixture, state))
	if err != nil || grantTransfer.End().EventCount != 5 || len(grantTransfer.Records()) != 3 {
		t.Fatalf("verify authenticated acquisition grant pull: end=%+v records=%d err=%v", grantTransfer.End(), len(grantTransfer.Records()), err)
	}
	fullAcquisitionPrefix := append(cloneEventRecords(state.EventRecords), grantTransfer.Records()...)
	truncated := cloneEventRecords(fullAcquisitionPrefix[:len(fullAcquisitionPrefix)-1])
	if _, _, _, err = foldEventRecords(truncated, state.DomainID); !errors.Is(err, ErrInvalidClientState) {
		t.Fatalf("incomplete acquisition event sequence folded: %v", err)
	}
	mislinked := cloneEventRecords(fullAcquisitionPrefix)
	dispatchFields, err := wipdwire.DecodeCanonicalMap(mislinked[4].Record,
		"schema", "event_id", "domain_id", "command_id", "request_hash", "environment", "acted_at", "occurred_at", "kind", "subject_id", "repo_id", "payload")
	if err != nil {
		t.Fatal(err)
	}
	dispatchPayload := dispatchFields["payload"].(map[string]any)
	dispatchPayload["claim_id"] = matterID
	mislinked[4].Record, err = wipdwire.EncodeCanonical(dispatchFields)
	if err != nil {
		t.Fatal(err)
	}
	if _, _, _, err = foldEventRecords(mislinked, state.DomainID); !errors.Is(err, ErrInvalidClientState) {
		t.Fatalf("dispatch.opened with a mismatched acquisition claim folded: %v", err)
	}
	differentActedAt := cloneEventRecords(grantTransfer.Records())
	dispatchFields, err = wipdwire.DecodeCanonicalMap(differentActedAt[2].Record,
		"schema", "event_id", "domain_id", "command_id", "request_hash", "environment", "acted_at", "occurred_at", "kind", "subject_id", "repo_id", "payload")
	if err != nil {
		t.Fatal(err)
	}
	dispatchFields["acted_at"] = "2025-01-02T03:04:06Z"
	differentActedAt[2].Record, err = wipdwire.EncodeCanonical(dispatchFields)
	if err != nil {
		t.Fatal(err)
	}
	chain, err := hex.DecodeString(strings.TrimPrefix(state.Prefix.Digest, "sha256:"))
	if err != nil {
		t.Fatal(err)
	}
	for _, event := range differentActedAt {
		var length [8]byte
		binary.BigEndian.PutUint64(length[:], uint64(len(event.Record)))
		hash := sha256.New()
		_, _ = hash.Write([]byte("wipd/event-prefix-step/v1\x00"))
		_, _ = hash.Write(chain)
		_, _ = hash.Write(length[:])
		_, _ = hash.Write(event.Record)
		chain = hash.Sum(nil)
	}
	lastEventID := differentActedAt[len(differentActedAt)-1].EventID
	differentEnd := wipdwire.PrefixAnchor{
		EventCount: state.Prefix.EventCount + uint64(len(differentActedAt)), EventID: &lastEventID,
		Digest: "sha256:" + hex.EncodeToString(chain),
	}
	differentManifest := grantManifest
	differentManifest.AsOf = differentEnd
	if _, err = wipdjournal.VerifyTransfer(state.DomainID, state.Epoch, state.Prefix, differentEnd, differentActedAt, differentManifest); err != nil {
		t.Fatalf("mutated acted_at transfer should remain valid before semantic fold: %v", err)
	}
	differentActedAtPrefix := append(cloneEventRecords(state.EventRecords), differentActedAt...)
	if _, _, _, err = foldEventRecords(differentActedAtPrefix, state.DomainID); !errors.Is(err, ErrInvalidClientState) {
		t.Fatalf("acquisition range with a different dispatch.opened acted_at folded: %v", err)
	}
	openPayload := map[string]any{"unexpected": true}
	closedPayload, err := wipdwire.DecodeCanonicalMap(fullAcquisitionPrefix[3].Record,
		"schema", "event_id", "domain_id", "command_id", "request_hash", "environment", "acted_at", "occurred_at", "kind", "subject_id", "repo_id", "payload")
	if err != nil {
		t.Fatal(err)
	}
	for key, value := range closedPayload["payload"].(map[string]any) {
		openPayload[key] = value
	}
	closedPayload["payload"] = openPayload
	unknownField := cloneEventRecords(fullAcquisitionPrefix)
	unknownField[3].Record, err = wipdwire.EncodeCanonical(closedPayload)
	if err != nil {
		t.Fatal(err)
	}
	if _, _, _, err = foldEventRecords(unknownField, state.DomainID); !errors.Is(err, ErrInvalidClientState) {
		t.Fatalf("claim.acquired with an unrecognized payload field folded: %v", err)
	}
	trust := wipdjournal.ClaimGrantTrust{
		OwnerRootPublicKey: fixture.ownerRoot, OwnerRootSPKI: fixture.ownerKeyID, VerifiedAt: time.Now().UTC(),
	}
	verifiedGrant, err := wipdjournal.VerifyClaimGrant(journalIdentity, trust, wipdjournal.ClaimGrantEvidence{
		ArtifactKeyCertificate: artifactCertificate, Wrapper: authorityGrant.Wrapper,
		Start: authorityGrant.Start, End: authorityGrant.End, Transfer: grantTransfer,
	})
	if err != nil {
		t.Fatalf("verify real signed acquisition grant: %v", err)
	}
	installedGrant, err := journal.InstallClaimGrant(ctx, initialSnapshot.Expectation(), verifiedGrant)
	if err != nil || installedGrant.Anchor.EventCount != 5 || installedGrant.Anchor.Digest != grantTransfer.End().Digest {
		t.Fatalf("atomically install verified acquisition grant: anchor=%+v err=%v", installedGrant.Anchor, err)
	}
	hydration, err := journal.BeginClaimHydration(ctx, grantID)
	if err != nil || hydration.State != "offline-ready" || hydration.ClaimID != claimID {
		t.Fatalf("installed grant did not bind exact acquired claim: %+v %v", hydration, err)
	}
	state, err = PullAndInstall(ctx, fixture.profile, fixture.roots, directory)
	if err != nil || state.Prefix.EventCount != 5 || len(state.EventRecords) != 5 {
		t.Fatalf("persist acquisition prefix through authenticated client: %+v %v", state.Prefix, err)
	}
	if err = journal.Close(); err != nil {
		t.Fatal(err)
	}
	journal, err = wipdjournal.Open(journalRoot, journalIdentity)
	if err != nil {
		t.Fatalf("reopen journal containing acquisition grant: %v", err)
	}
	reopenedSnapshot, err := journal.InstallSnapshot(ctx)
	if err != nil || reopenedSnapshot.Anchor.EventCount != 5 {
		t.Fatalf("reopened grant prefix = %+v, %v", reopenedSnapshot.Anchor, err)
	}

	createFixtureMatter(t, fixture.store, peer, artifactSigner, state, laterCommandID, laterMatterID,
		laterEventID, "After acquisition", "after-acquisition", 4)
	laterTransfer, laterManifest, err := VerifyPullTransfer(fixture.profile, state, state.Prefix,
		authenticatedPullFrames(t, fixture, state))
	if err != nil || laterTransfer.End().EventCount != 6 || len(laterTransfer.Records()) != 1 || len(laterManifest.Entries) != len(grantManifest.Entries) {
		t.Fatalf("verify later authenticated pull from acquisition prefix: end=%+v records=%d err=%v", laterTransfer.End(), len(laterTransfer.Records()), err)
	}
	installedLater, err := journal.InstallPull(ctx, reopenedSnapshot.Expectation(), laterTransfer)
	if err != nil || installedLater.Anchor.EventCount != 6 {
		t.Fatalf("install later pull after reopening acquisition grant: anchor=%+v err=%v", installedLater.Anchor, err)
	}
	state, err = PullAndInstall(ctx, fixture.profile, fixture.roots, directory)
	if err != nil || state.Prefix.EventCount != 6 || len(state.EventRecords) != 6 || len(state.Projections) != 2 {
		t.Fatalf("persist/reopen client after later acquisition-prefix pull: prefix=%+v matters=%d err=%v", state.Prefix, len(state.Projections), err)
	}
}

func authenticatedPullFrames(t *testing.T, fixture *clientFixture, state ClientState) []wipdwire.Frame {
	t.Helper()
	client := installedClient(t, fixture, state)
	defer client.CloseIdleConnections()
	ctx := context.Background()
	limits, err := negotiateRemote(ctx, client, fixture.profile.Origin())
	if err != nil {
		t.Fatal(err)
	}
	requestFrame, requestID, err := encodeRequestFrame("pull.request", wipdwire.PullRequest{
		Schema: "wipd.pull-request/1", DomainID: state.DomainID, Epoch: state.Epoch, Installed: state.Prefix,
	})
	if err != nil {
		t.Fatal(err)
	}
	frames, err := postFramesWithinSession(ctx, client, fixture.profile.Origin()+"/wipd/v1/exchange",
		requestFrame, requestID, maxClientTransferEvents+3, limits)
	if err != nil {
		t.Fatal(err)
	}
	return frames
}

func completeFixtureBirthRelease(t *testing.T, fixture *clientFixture, peer tls.ConnectionState, state ClientState, matterID, commandID, eventID string, signer ed25519.PrivateKey) {
	t.Helper()
	ctx := context.Background()
	if len(state.EventRecords) == 0 {
		t.Fatal("birth release requires the Matter birth record in the installed prefix")
	}
	record := state.EventRecords[len(state.EventRecords)-1]
	fields, err := wipdwire.DecodeCanonicalMap(record.Record,
		"schema", "event_id", "domain_id", "command_id", "request_hash", "environment", "acted_at", "occurred_at", "kind", "subject_id", "repo_id", "payload")
	if err != nil || fields["kind"] != "matter.created" || fields["subject_id"] != matterID {
		t.Fatalf("birth release source event does not match Matter %s: fields=%+v err=%v", matterID, fields, err)
	}
	birthCommandID, ok := fields["command_id"].(string)
	birthRequestHash, hashOK := fields["request_hash"].(string)
	if !ok || !hashOK {
		t.Fatal("Matter birth event lacks command identity")
	}
	status, err := fixture.store.QueryCommand(ctx, state.DomainID, birthCommandID, birthRequestHash, state.Epoch,
		peer, state.EnvironmentID, time.Now().UTC())
	if err != nil || status.Pending || len(status.Receipt) == 0 {
		t.Fatalf("query Matter birth receipt: status=%+v err=%v", status, err)
	}
	receipt, err := wipdwire.DecodeCanonicalMap(status.Receipt,
		"schema", "domain_id", "authority_epoch", "identity_schema", "command_id", "request_hash", "operation", "environment", "result", "accepted_events")
	if err != nil || receipt["command_id"] != birthCommandID || receipt["request_hash"] != birthRequestHash {
		t.Fatalf("Matter birth receipt identity=%+v err=%v", receipt, err)
	}
	result, resultOK := receipt["result"].(map[string]any)
	accepted, acceptedOK := receipt["accepted_events"].(map[string]any)
	if !resultOK || result["code"] != string(operation.ResultSucceeded) || !acceptedOK ||
		accepted["first_event_id"] != record.EventID || accepted["last_event_id"] != record.EventID || accepted["event_count"] != uint64(1) {
		t.Fatalf("Matter birth receipt is not an exact successful one-event range: %+v", receipt)
	}
	ack := wipdwire.BirthJournalAck{
		Schema: "wipd.birth-journal-ack/1", DomainID: state.DomainID, Epoch: state.Epoch, MatterID: matterID,
		CommandID: birthCommandID, RequestHash: birthRequestHash, Receipt: status.Receipt, Installed: state.Prefix,
	}
	if err = fixture.store.AcknowledgeBirthJournalEntry(ctx, ack, peer, state.EnvironmentID, time.Now().UTC()); err != nil {
		t.Fatalf("acknowledge installed Matter birth receipt: %v", err)
	}
	barrierEntry := wipdwire.JournalBarrierEntry{
		Position: 1, CommandID: birthCommandID, RequestHash: birthRequestHash, ResultCode: string(operation.ResultSucceeded),
		Range: &wipdwire.JournalBarrierRange{First: record.EventID, Last: record.EventID, Count: 1},
	}
	barrierDigest, err := wipdwire.JournalBarrierDigest([]wipdwire.JournalBarrierEntry{barrierEntry})
	if err != nil {
		t.Fatal(err)
	}
	barrier := map[string]any{
		"schema": "wipd.journal-barrier/1", "journal_id": matterID,
		"claim":       map[string]any{"id": matterID, "epoch": uint64(1)},
		"entry_count": uint64(1), "last_position": uint64(1), "terminal_receipt_count": uint64(1),
		"entries_digest": barrierDigest, "sealed": true, "unresolved_count": uint64(0), "quarantined_count": uint64(0),
	}
	release, err := wipdwire.EncodeCanonical(map[string]any{
		"schema": "wipd.command/1", "command_id": commandID,
		"authority":   map[string]any{"domain_id": state.DomainID, "expected_epoch": state.Epoch},
		"environment": map[string]any{"id": state.EnvironmentID, "sequence": uint64(2)},
		"acted_at":    time.Now().UTC().Truncate(time.Second).Format(time.RFC3339Nano), "actor": "human",
		"causation_command_id": nil, "correlation_command_id": commandID,
		"operation": map[string]any{"name": "claim.release", "version": uint64(1)},
		"context":   map[string]any{"repo_id": state.RepoID, "clone_id": nil, "worktree_id": nil},
		"claim":     map[string]any{"id": matterID, "epoch": uint64(1)},
		"input":     map[string]any{"barrier": barrier}, "blobs": []any{},
	})
	if err != nil {
		t.Fatal(err)
	}
	requestHash := testDigest(append([]byte("wipd/request-hash/v1\x00"), release...))
	pending, err := fixture.store.SubmitClaimLifecycle(ctx, release, requestHash, peer, time.Now().UTC(), nil)
	if err != nil || pending.Owner == nil {
		t.Fatalf("submit fixture birth release: status=%+v err=%v", pending, err)
	}
	completed, err := fixture.store.CompleteClaimLifecycle(ctx, pending.Owner, "", []string{eventID}, time.Now().UTC(),
		func(_ context.Context, message []byte) ([]byte, error) { return ed25519.Sign(signer, message), nil })
	if err != nil || completed.Pending || len(completed.Receipt) == 0 {
		t.Fatalf("complete fixture birth release: status=%+v err=%v", completed, err)
	}
}

func installSnapshotExpectation(t *testing.T, journal *wipdjournal.Journal) wipdjournal.InstallExpectation {
	t.Helper()
	snapshot, err := journal.InstallSnapshot(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	return snapshot.Expectation()
}

func enrollFixtureClient(t *testing.T, fixture *clientFixture, directory string) ClientState {
	t.Helper()
	if err := SavePending(directory, fixture.identity); err != nil {
		t.Fatal(err)
	}
	state, err := EnrollAndSeed(context.Background(), fixture.profile, fixture.roots, fixture.ownerRoot, fixture.delegation,
		testRepoID, fixture.identity, fixture.grant, directory)
	if err != nil {
		t.Fatal(err)
	}
	return state
}

func registerClientFixtureArtifactKey(t *testing.T, fixture *clientFixture) ed25519.PrivateKey {
	private, _ := registerClientFixtureArtifactKeyWithCertificate(t, fixture)
	return private
}

func registerClientFixtureArtifactKeyWithCertificate(t *testing.T, fixture *clientFixture) (ed25519.PrivateKey, []byte) {
	t.Helper()
	public, private, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	keyID := testSPKIID(t, public)
	now := time.Now().UTC().Truncate(time.Second)
	certificate := signOwnerArtifact(t, fixture.owner, "authority-artifact-key", "wipd.authority-artifact-key/1", testDomainID, fixture.ownerKeyID, 1, map[string]any{
		"schema": "wipd.authority-artifact-key/1", "domain_id": testDomainID, "authority_epoch": uint64(1),
		"key_generation": uint64(1), "key_id": keyID, "ed25519_public_key": []byte(public),
		"not_before": now.Add(-time.Hour).Format(time.RFC3339Nano), "not_after": now.Add(time.Hour).Format(time.RFC3339Nano),
	})
	if err = fixture.store.RegisterArtifactKey(context.Background(), testDomainID, certificate, now); err != nil {
		clear(private)
		t.Fatal(err)
	}
	t.Cleanup(func() { clear(private) })
	return private, bytes.Clone(certificate)
}

func peerStateFromClient(t *testing.T, state ClientState) tls.ConnectionState {
	t.Helper()
	certificates := make([]*x509.Certificate, 0, len(state.CertificateDER))
	for _, der := range state.CertificateDER {
		certificate, err := x509.ParseCertificate(der)
		if err != nil {
			t.Fatal(err)
		}
		certificates = append(certificates, certificate)
	}
	return tls.ConnectionState{HandshakeComplete: true, PeerCertificates: certificates}
}

func createFixtureMatter(t *testing.T, store *authoritystore.Store, peer tls.ConnectionState, signer ed25519.PrivateKey, state ClientState, commandID, matterID, eventID, title, locator string, sequence uint64) {
	t.Helper()
	now := time.Now().UTC().Truncate(time.Second)
	command := operation.Command{
		ID: commandID, AuthorityDomainID: state.DomainID, ExpectedAuthorityEpoch: state.Epoch,
		EnvironmentID: state.EnvironmentID, EnvironmentSequence: sequence, ActedAt: now.Format(time.RFC3339Nano),
		CorrelationCommandID: commandID,
		Request: operation.Request{
			Operation: operation.MatterCreateV1.Metadata().Operation, Actor: "human",
			Context: operation.Context{Repo: state.RepoID}, Input: operation.MatterCreateInput{Title: title, Locator: locator}, Blobs: []operation.BlobInput{},
		},
	}
	hash, err := command.RequestHash()
	if err != nil {
		t.Fatal(err)
	}
	status, err := store.SubmitCommand(context.Background(), command, hash, peer, now)
	if err != nil || status.Owner == nil {
		t.Fatalf("submit fixture mutation: status=%+v err=%v", status, err)
	}
	result := operation.Result{Code: operation.ResultSucceeded, Output: operation.MatterCreateOutput{ID: matterID, Locator: locator, Title: title}}
	if _, err = store.CompleteCommand(context.Background(), status.Owner, result, matterID, eventID, now, func(_ context.Context, message []byte) ([]byte, error) {
		return ed25519.Sign(signer, message), nil
	}); err != nil {
		t.Fatalf("complete fixture mutation: %v", err)
	}
}

func projectionLocator(t *testing.T, projection json.RawMessage) string {
	t.Helper()
	var value eventProjection
	if err := json.Unmarshal(projection, &value); err != nil {
		t.Fatal(err)
	}
	return value.Locator
}

func TestEnrollAndSeedWrongDomainOrGrantLeavesNoClientState(t *testing.T) {
	fixture := newClientFixture(t)
	identity := fixture.identity
	var err error
	wrongDomain, err := wipdauthority.NewProfile(fixture.profile.Origin(), "01KZ7XHAQT1S46NYPN1PW1DX3E", 1, fixture.authorityPin, fixture.ownerKeyID)
	if err != nil {
		t.Fatal(err)
	}
	wrongDomain, err = wrongDomain.WithM5LabRepoID(testRepoID)
	if err != nil {
		t.Fatal(err)
	}
	directory := t.TempDir()
	if _, err = EnrollAndSeed(context.Background(), wrongDomain, fixture.roots, fixture.ownerRoot, fixture.delegation, testRepoID, identity, fixture.grant, directory); err == nil {
		t.Fatal("wrong authority domain was accepted")
	}
	if _, err = os.Stat(filepath.Join(directory, stateName)); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("wrong-domain attempt installed state: %v", err)
	}
	wrongOwner, _, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	directory = t.TempDir()
	if _, err = EnrollAndSeed(context.Background(), fixture.profile, fixture.roots, wrongOwner, fixture.delegation, testRepoID, identity, fixture.grant, directory); err == nil {
		t.Fatal("delegation signed by a different owner root was accepted")
	}
	if _, err = os.Stat(filepath.Join(directory, stateName)); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("wrong-owner attempt installed state: %v", err)
	}

	wrongKey, err := PrepareIdentity()
	if err != nil {
		t.Fatal(err)
	}
	badGrant := fixture.enrollmentGrant(identity.CSRDER)
	directory = t.TempDir()
	if _, err = EnrollAndSeed(context.Background(), fixture.profile, fixture.roots, fixture.ownerRoot, fixture.delegation, testRepoID, wrongKey, badGrant, directory); err == nil {
		t.Fatal("grant bound to a different CSR was accepted")
	}
	if _, err = os.Stat(filepath.Join(directory, stateName)); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("wrong-grant attempt installed state: %v", err)
	}
}

func TestExpiredEnrollmentGrantRefusesBeforeExternalCASigning(t *testing.T) {
	fixture := newClientFixture(t)
	now := time.Now().UTC().Truncate(time.Second)
	expiredGrant := fixture.enrollmentGrantAt(fixture.identity.CSRDER, now.Add(-20*time.Minute))
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = listener.Close() })
	port := listener.Addr().(*net.TCPAddr).Port
	serverPublic, serverPrivate, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	serverDER := testAuthorityCertificate(t, serverPrivate, testDomainID, fixture.ownerKeyID, now)
	serverCert, err := x509.ParseCertificate(serverDER)
	if err != nil {
		t.Fatal(err)
	}
	serverSPKI, err := x509.MarshalPKIXPublicKey(serverPublic)
	if err != nil {
		t.Fatal(err)
	}
	profile, err := wipdauthority.NewProfile(fmt.Sprintf("https://localhost:%d", port), testDomainID, 1, testDigest(serverSPKI), fixture.ownerKeyID)
	if err != nil {
		t.Fatal(err)
	}
	profile, err = profile.WithM5LabRepoID(testRepoID)
	if err != nil {
		t.Fatal(err)
	}
	var signerCalls atomic.Int32
	server, err := wipdauthority.NewM5LabServer(profile, tlsCertificate(t, serverDER, serverPrivate), wipdauthority.M5LabConfig{
		Store: fixture.store, RepoID: testRepoID, EnrollmentGrant: expiredGrant,
		ExpectedCSRDER: fixture.identity.CSRDER, EnvironmentCACertificateDER: fixture.caDER,
		SignEnvironmentLeaf: func(_ context.Context, domain string, epoch uint64, environment string, csrDER []byte, at time.Time) ([]byte, error) {
			signerCalls.Add(1)
			return testEnvironmentLeaf(t, fixture.caKey, fixture.caDER, csrDER, domain, environment, fixture.ownerKeyID, epoch, at)
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	serverCtx, stopServer := context.WithCancel(context.Background())
	serverDone := make(chan error, 1)
	go func() { serverDone <- server.Serve(serverCtx, listener) }()
	t.Cleanup(func() {
		stopServer()
		select {
		case <-serverDone:
		case <-time.After(6 * time.Second):
			t.Error("expired-grant authority did not stop")
		}
	})
	roots := x509.NewCertPool()
	roots.AddCert(serverCert)
	directory := t.TempDir()
	if err = SavePending(directory, fixture.identity); err != nil {
		t.Fatal(err)
	}
	if _, err = EnrollAndSeed(context.Background(), profile, roots, fixture.ownerRoot, fixture.delegation, testRepoID, fixture.identity, expiredGrant, directory); err == nil {
		t.Fatal("expired enrollment grant was accepted")
	}
	if calls := signerCalls.Load(); calls != 0 {
		t.Fatalf("restricted CA signer was called %d times for an expired grant, want zero", calls)
	}
	if _, err = os.Stat(filepath.Join(directory, stateName)); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("expired-grant attempt installed client state: %v", err)
	}
}

func TestVerifyEmptySeedRejectsTruncatedReorderedOrMismatchedProduct(t *testing.T) {
	fixture := newClientFixture(t)
	frames := fixture.seedFrames(t)
	for _, test := range []struct {
		name   string
		mutate func([]wipdwire.Frame) []wipdwire.Frame
	}{
		{name: "truncated", mutate: func(frames []wipdwire.Frame) []wipdwire.Frame { return frames[:2] }},
		{name: "reordered", mutate: func(frames []wipdwire.Frame) []wipdwire.Frame {
			return []wipdwire.Frame{frames[1], frames[0], frames[2]}
		}},
		{name: "different request", mutate: func(frames []wipdwire.Frame) []wipdwire.Frame {
			frames[2].RequestID = "00000000000000000000000000"
			return frames
		}},
		{name: "incomplete", mutate: func(frames []wipdwire.Frame) []wipdwire.Frame {
			frames[2].Payload = mustEncode(t, wipdwire.SeedEnd{Schema: "wipd.seed-end/1", TransferID: "01KZ7XHAQT1S46NYPN1PW1DX3D", VerifiedPrefix: wipdwire.PrefixAnchor{Digest: emptyPrefixDigest()}, ManifestDigest: emptyManifestDigest(), Complete: false})
			return frames
		}},
		{name: "unknown prefix key", mutate: func(frames []wipdwire.Frame) []wipdwire.Frame {
			fields, err := wipdwire.DecodeCanonicalMap(frames[0].Payload, "schema", "transfer_id", "domain_id", "authority_epoch", "store_schema", "snapshot_id", "prefix", "event_count", "event_byte_length", "blob_manifest_digest")
			if err != nil {
				t.Fatal(err)
			}
			prefix := fields["prefix"].(map[string]any)
			prefix["unexpected"] = true
			frames[0].Payload = mustEncode(t, fields)
			return frames
		}},
		{name: "unknown prefix-anchor key", mutate: func(frames []wipdwire.Frame) []wipdwire.Frame {
			fields, err := wipdwire.DecodeCanonicalMap(frames[2].Payload, "schema", "transfer_id", "verified_prefix", "verified_blob_manifest_digest", "complete")
			if err != nil {
				t.Fatal(err)
			}
			anchor := fields["verified_prefix"].(map[string]any)
			anchor["unexpected"] = true
			frames[2].Payload = mustEncode(t, fields)
			return frames
		}},
		{name: "unknown manifest-anchor key", mutate: func(frames []wipdwire.Frame) []wipdwire.Frame {
			fields, err := wipdwire.DecodeCanonicalMap(frames[1].Payload, "schema", "domain_id", "authority_epoch", "as_of", "entries", "manifest_digest")
			if err != nil {
				t.Fatal(err)
			}
			anchor := fields["as_of"].(map[string]any)
			anchor["unexpected"] = true
			frames[1].Payload = mustEncode(t, fields)
			return frames
		}},
	} {
		t.Run(test.name, func(t *testing.T) {
			candidate := cloneFrames(frames)
			if _, err := verifyTransferFrames(test.mutate(candidate), "seed", fixture.profile, testRepoID,
				"01KZ7XHAQT1S46NYPN1PW1DX3E", "sha256:"+strings.Repeat("a", 64), emptyWireAnchor(), nil); !errors.Is(err, ErrInvalidClientState) {
				t.Fatalf("mutated seed = %v, want invalid-state refusal", err)
			}
		})
	}
}

func TestSavePendingIsCreateOnlyAndRejectsPermissiveFiles(t *testing.T) {
	directory := t.TempDir()
	identity, err := PrepareIdentity()
	if err != nil {
		t.Fatal(err)
	}
	if err = SavePending(directory, identity); err != nil {
		t.Fatal(err)
	}
	if err = SavePending(directory, identity); !errors.Is(err, os.ErrExist) {
		t.Fatalf("pending overwrite = %v, want already-exists", err)
	}
	if err = os.Chmod(filepath.Join(directory, pendingName), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err = LoadPending(directory); !errors.Is(err, ErrInvalidClientState) {
		t.Fatalf("permissive pending file = %v, want invalid-state refusal", err)
	}
}

type clientFixture struct {
	profile        wipdauthority.Profile
	roots          *x509.CertPool
	owner          ed25519.PrivateKey
	ownerRoot      ed25519.PublicKey
	ownerKeyID     string
	authorityPin   string
	caKey          ed25519.PrivateKey
	caDER          []byte
	store          *authoritystore.Store
	listener       net.Listener
	server         *wipdauthority.Server
	serverCertDER  []byte
	serverPrivate  ed25519.PrivateKey
	serverStop     context.CancelFunc
	serverComplete chan error
	signerCalls    *atomic.Int32
	identity       PreparedIdentity
	grant          []byte
	delegation     []byte
}

func newClientFixture(t *testing.T) *clientFixture {
	t.Helper()
	now := time.Now().UTC().Truncate(time.Second)
	ownerPublic, owner, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	ownerKeyID := testSPKIID(t, ownerPublic)
	domain := authoritystore.Domain{ID: testDomainID, OwnerPublicKey: ownerPublic, OwnerKeyID: ownerKeyID, ActiveEpoch: 1}
	setupPublic, setupPrivate, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	genesisNonce := bytes.Repeat([]byte{0x41}, 16)
	genesisGrant, err := authoritystore.CreateM5LabGenesisGrant(setupPrivate, domain, testRepoID, genesisNonce, now, now.Add(time.Minute))
	clear(setupPrivate)
	if err != nil {
		t.Fatal(err)
	}
	store, err := authoritystore.CreateEmpty(filepath.Join(t.TempDir(), "authority"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	if err = store.BootstrapDomainWithM5LabGrant(context.Background(), domain, testRepoID, setupPublic, genesisGrant, now); err != nil {
		t.Fatal(err)
	}

	caPublic, caPrivate, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	caDER := testCACertificate(t, caPrivate, now)
	delegation := signOwnerArtifact(t, owner, "environment-ca-delegation", "wipd.environment-ca-delegation/1", domain.ID, ownerKeyID, 1, map[string]any{
		"schema": "wipd.environment-ca-delegation/1", "domain_id": domain.ID, "authority_epoch": uint64(1), "owner_key_id": ownerKeyID,
		"ca_generation": uint64(1), "ca_key_id": testSPKIID(t, caPublic), "ca_certificate_der": caDER,
		"not_before": now.Add(-time.Hour).Format(time.RFC3339Nano), "not_after": now.Add(24 * time.Hour).Format(time.RFC3339Nano),
	})
	if err = store.InstallEnvironmentCA(context.Background(), domain.ID, delegation, now); err != nil {
		t.Fatal(err)
	}
	identity, err := PrepareIdentity()
	if err != nil {
		t.Fatal(err)
	}
	grantSource := &clientFixture{owner: owner, ownerKeyID: ownerKeyID}
	enrollmentGrant := grantSource.enrollmentGrant(identity.CSRDER)

	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = listener.Close() })
	port := listener.Addr().(*net.TCPAddr).Port
	serverPublic, serverPrivate, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	serverDER := testAuthorityCertificate(t, serverPrivate, domain.ID, ownerKeyID, now)
	serverCert, err := x509.ParseCertificate(serverDER)
	if err != nil {
		t.Fatal(err)
	}
	serverSPKI, err := x509.MarshalPKIXPublicKey(serverPublic)
	if err != nil {
		t.Fatal(err)
	}
	serverPin := testDigest(serverSPKI)
	profile, err := wipdauthority.NewProfile(fmt.Sprintf("https://localhost:%d", port), domain.ID, 1, serverPin, ownerKeyID)
	if err != nil {
		t.Fatal(err)
	}
	profile, err = profile.WithM5LabRepoID(testRepoID)
	if err != nil {
		t.Fatal(err)
	}
	serverKeyPair := tlsCertificate(t, serverDER, serverPrivate)
	signerCalls := &atomic.Int32{}
	server, err := wipdauthority.NewM5LabServer(profile, serverKeyPair, wipdauthority.M5LabConfig{
		Store: store, RepoID: testRepoID, EnrollmentGrant: enrollmentGrant,
		ExpectedCSRDER: identity.CSRDER, EnvironmentCACertificateDER: caDER,
		SignEnvironmentLeaf: func(_ context.Context, gotDomain string, epoch uint64, environment string, csrDER []byte, at time.Time) ([]byte, error) {
			signerCalls.Add(1)
			return testEnvironmentLeaf(t, caPrivate, caDER, csrDER, gotDomain, environment, ownerKeyID, epoch, at)
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	serverCtx, serverStop := context.WithCancel(context.Background())
	serverComplete := make(chan error, 1)
	go func() { serverComplete <- server.Serve(serverCtx, listener) }()
	t.Cleanup(func() {
		serverStop()
		select {
		case <-serverComplete:
		case <-time.After(6 * time.Second):
			t.Error("authority server did not stop after cancellation")
		}
	})
	roots := x509.NewCertPool()
	roots.AddCert(serverCert)
	return &clientFixture{
		profile: profile, roots: roots, owner: owner, ownerKeyID: ownerKeyID,
		authorityPin: serverPin, caKey: caPrivate, caDER: caDER, store: store,
		identity: identity, grant: enrollmentGrant,
		ownerRoot: ownerPublic, delegation: delegation,
		listener: listener, server: server, serverCertDER: serverDER, serverPrivate: serverPrivate,
		serverStop: serverStop, serverComplete: serverComplete, signerCalls: signerCalls,
	}
}

func (fixture *clientFixture) enrollmentGrant(csrDER []byte) []byte {
	return fixture.enrollmentGrantAt(csrDER, time.Now().UTC().Truncate(time.Second))
}

func (fixture *clientFixture) enrollmentGrantAt(csrDER []byte, now time.Time) []byte {
	csr, err := x509.ParseCertificateRequest(csrDER)
	if err != nil {
		panic(err)
	}
	ownerKeyID := fixture.ownerKeyID
	spkiDigest := testSPKIID(nil, csr.PublicKey)
	return signOwnerArtifact(nil, fixture.owner, "enrollment-grant", "wipd.enrollment-grant/1", testDomainID, ownerKeyID, 1, map[string]any{
		"schema": "wipd.enrollment-grant/1", "grant_id": testGrantID, "domain_id": testDomainID,
		"authority_epoch": uint64(1), "owner_key_id": ownerKeyID, "scope": "environment-enroll",
		"requested_spki_digest": spkiDigest, "prior_environment_id": nil, "nonce": bytes.Repeat([]byte{0x62}, 16),
		"issued_at": now.Format(time.RFC3339Nano), "expires_at": now.Add(5 * time.Minute).Format(time.RFC3339Nano),
	})
}

func (fixture *clientFixture) seedFrames(t *testing.T) []wipdwire.Frame {
	t.Helper()
	requestID := "01KZ7XHAQT1S46NYPN1PW1DX3D"
	empty := wipdwire.PrefixAnchor{Digest: emptyPrefixDigest()}
	manifestDigest := emptyManifestDigest()
	start := wipdwire.SeedStart{
		Schema: "wipd.seed-start/1", TransferID: "01KZ7XHAQT1S46NYPN1PW1DX3E", DomainID: testDomainID, Epoch: 1,
		StoreSchema: "wipd.store/1", SnapshotID: "01KZ7XHAQT1S46NYPN1PW1DX3F", Prefix: struct {
			Start wipdwire.PrefixAnchor `cbor:"start"`
			End   wipdwire.PrefixAnchor `cbor:"end"`
		}{Start: empty, End: empty}, ManifestDigest: manifestDigest,
	}
	manifest := wipdwire.BlobManifest{Schema: "wipd.blob-manifest/1", DomainID: testDomainID, Epoch: 1, AsOf: empty, Entries: []wipdwire.BlobManifestEntry{}, Digest: manifestDigest}
	end := wipdwire.SeedEnd{Schema: "wipd.seed-end/1", TransferID: start.TransferID, VerifiedPrefix: empty, ManifestDigest: manifestDigest, Complete: true}
	return []wipdwire.Frame{
		{RequestID: requestID, Sequence: 0, Kind: "seed.start", Payload: mustEncode(t, start)},
		{RequestID: requestID, Sequence: 1, Kind: "blob.manifest", Payload: mustEncode(t, manifest)},
		{RequestID: requestID, Sequence: 2, Kind: "seed.end", Payload: mustEncode(t, end)},
	}
}

func testCACertificate(t *testing.T, private ed25519.PrivateKey, now time.Time) []byte {
	t.Helper()
	public := private.Public().(ed25519.PublicKey)
	template := &x509.Certificate{
		SerialNumber: big.NewInt(10), Subject: pkix.Name{CommonName: "M5 delegated Environment CA"},
		NotBefore: now.Add(-time.Hour), NotAfter: now.Add(24 * time.Hour), IsCA: true,
		BasicConstraintsValid: true, MaxPathLen: 0, MaxPathLenZero: true, KeyUsage: x509.KeyUsageCertSign,
	}
	der, err := x509.CreateCertificate(rand.Reader, template, template, public, private)
	if err != nil {
		t.Fatal(err)
	}
	return der
}

func testAuthorityCertificate(t *testing.T, private ed25519.PrivateKey, domain, owner string, now time.Time) []byte {
	t.Helper()
	uri, err := url.Parse(fmt.Sprintf("wipd://authority/%s?epoch=1&owner=%s", domain, strings.TrimPrefix(owner, "sha256:")))
	if err != nil {
		t.Fatal(err)
	}
	san, err := asn1.Marshal([]asn1.RawValue{
		{Class: asn1.ClassContextSpecific, Tag: 2, Bytes: []byte("localhost")},
		{Class: asn1.ClassContextSpecific, Tag: 6, Bytes: []byte(uri.String())},
	})
	if err != nil {
		t.Fatal(err)
	}
	public := private.Public().(ed25519.PublicKey)
	template := &x509.Certificate{
		SerialNumber: big.NewInt(20), Subject: pkix.Name{CommonName: "M5 lab authority"},
		NotBefore: now.Add(-time.Hour), NotAfter: now.Add(time.Hour), IsCA: true,
		BasicConstraintsValid: true, MaxPathLen: 0, MaxPathLenZero: true,
		KeyUsage:        x509.KeyUsageCertSign | x509.KeyUsageDigitalSignature,
		ExtKeyUsage:     []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
		ExtraExtensions: []pkix.Extension{{Id: asn1.ObjectIdentifier{2, 5, 29, 17}, Critical: true, Value: san}},
	}
	der, err := x509.CreateCertificate(rand.Reader, template, template, public, private)
	if err != nil {
		t.Fatal(err)
	}
	return der
}

func testEnvironmentLeaf(t *testing.T, caPrivate ed25519.PrivateKey, caDER, csrDER []byte, domain, environment, owner string, epoch uint64, now time.Time) ([]byte, error) {
	t.Helper()
	csr, err := x509.ParseCertificateRequest(csrDER)
	if err != nil || csr.CheckSignature() != nil {
		return nil, errors.New("invalid CSR")
	}
	uri := fmt.Sprintf("wipd://environment/%s?domain=%s&epoch=%d&owner=%s", environment, domain, epoch, strings.TrimPrefix(owner, "sha256:"))
	san, err := asn1.Marshal([]asn1.RawValue{{Class: asn1.ClassContextSpecific, Tag: 6, Bytes: []byte(uri)}})
	if err != nil {
		return nil, err
	}
	ca, err := x509.ParseCertificate(caDER)
	if err != nil {
		return nil, err
	}
	template := &x509.Certificate{
		SerialNumber: big.NewInt(30), Subject: pkix.Name{CommonName: "M5 Environment"},
		NotBefore: now.Add(-time.Minute), NotAfter: now.Add(time.Hour), BasicConstraintsValid: true,
		KeyUsage: x509.KeyUsageDigitalSignature, ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageClientAuth},
		ExtraExtensions: []pkix.Extension{
			{Id: asn1.ObjectIdentifier{2, 5, 29, 19}, Critical: true, Value: []byte{0x30, 0x00}},
			{Id: asn1.ObjectIdentifier{2, 5, 29, 15}, Critical: true, Value: []byte{0x03, 0x02, 0x07, 0x80}},
			{Id: asn1.ObjectIdentifier{2, 5, 29, 17}, Critical: true, Value: san},
		},
	}
	return x509.CreateCertificate(rand.Reader, template, ca, csr.PublicKey, caPrivate)
}

func signOwnerArtifact(t *testing.T, owner ed25519.PrivateKey, kind, payloadSchema, domain, ownerID string, epoch uint64, payload map[string]any) []byte {
	if t != nil {
		t.Helper()
	}
	payloadBytes, err := testEncoder.Marshal(payload)
	if err != nil {
		if t != nil {
			t.Fatal(err)
		}
		panic(err)
	}
	unsigned := map[string]any{
		"schema": "wipd.signed-artifact/1", "kind": kind, "domain_id": domain, "authority_epoch": epoch,
		"signer_role": "owner", "signer_key_id": ownerID, "key_generation": nil, "artifact_sequence": nil,
		"previous_artifact_digest": nil, "issued_at": time.Now().UTC().Format(time.RFC3339Nano),
		"payload_schema": payloadSchema, "payload_digest": testDigest(payloadBytes), "payload": payloadBytes,
	}
	preimage, err := testEncoder.Marshal(unsigned)
	if err != nil {
		if t != nil {
			t.Fatal(err)
		}
		panic(err)
	}
	unsigned["signature"] = ed25519.Sign(owner, append([]byte("wipd/signed-artifact/v1\x00"), preimage...))
	encoded, err := testEncoder.Marshal(unsigned)
	if err != nil {
		if t != nil {
			t.Fatal(err)
		}
		panic(err)
	}
	return encoded
}

func testSPKIID(t *testing.T, public any) string {
	if t != nil {
		t.Helper()
	}
	der, err := x509.MarshalPKIXPublicKey(public)
	if err != nil {
		if t != nil {
			t.Fatal(err)
		}
		panic(err)
	}
	return testDigest(der)
}

func testDigest(value []byte) string {
	sum := sha256.Sum256(value)
	return "sha256:" + hex.EncodeToString(sum[:])
}

func tlsCertificate(t *testing.T, der []byte, key ed25519.PrivateKey) tls.Certificate {
	t.Helper()
	return tls.Certificate{Certificate: [][]byte{der}, PrivateKey: key}
}

func mustEncode(t *testing.T, value any) []byte {
	t.Helper()
	encoded, err := wipdwire.EncodeCanonical(value)
	if err != nil {
		t.Fatal(err)
	}
	return encoded
}

func cloneFrames(frames []wipdwire.Frame) []wipdwire.Frame {
	result := make([]wipdwire.Frame, len(frames))
	for i, frame := range frames {
		result[i] = frame
		result[i].Payload = bytes.Clone(frame.Payload)
	}
	return result
}
