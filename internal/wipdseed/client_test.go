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

func privateTempDir(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	if err := os.Chmod(dir, 0o700); err != nil {
		t.Fatalf("make test temp directory private: %v", err)
	}
	info, err := os.Stat(dir)
	if err != nil {
		t.Fatalf("stat private test temp directory: %v", err)
	}
	if info.Mode().Perm()&0o077 != 0 {
		t.Fatalf("test temp directory mode = %04o, want owner-only", info.Mode().Perm())
	}
	return dir
}

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

func TestPullAndInstallM6LifecycleEventSet(t *testing.T) {
	fixture := newClientFixture(t)
	artifactSigner := registerClientFixtureArtifactKey(t, fixture)
	directory := t.TempDir()
	state := enrollFixtureClient(t, fixture, directory)
	peer := peerStateFromClient(t, state)
	ctx := context.Background()
	const (
		matterA        = "01KZ7XHAQT1S46NYPN1PW1DX90"
		matterB        = "01KZ7XHAQT1S46NYPN1PW1DX91"
		stageB         = "01KZ7XHAQT1S46NYPN1PW1DX92"
		stepB          = "01KZ7XHAQT1S46NYPN1PW1DX93"
		claimA         = "01KZ7XHAQT1S46NYPN1PW1DX94"
		claimB         = "01KZ7XHAQT1S46NYPN1PW1DX95"
		batchA         = "01KZ7XHAQT1S46NYPN1PW1DX96"
		batchB         = "01KZ7XHAQT1S46NYPN1PW1DX97"
		grantA         = "01KZ7XHAQT1S46NYPN1PW1DX98"
		grantB         = "01KZ7XHAQT1S46NYPN1PW1DX99"
		snapshotA      = "01KZ7XHAQT1S46NYPN1PW1DYA0"
		snapshotB      = "01KZ7XHAQT1S46NYPN1PW1DYA1"
		journalA       = "01KZ7XHAQT1S46NYPN1PW1DYA2"
		journalB       = "01KZ7XHAQT1S46NYPN1PW1DYA3"
		worktreeA      = "01KZ7XHAQT1S46NYPN1PW1DYA4"
		worktreeB      = "01KZ7XHAQT1S46NYPN1PW1DYA5"
		cloneID        = "01KZ7XHAQT1S46NYPN1PW1DYA6"
		dispatchA      = "01KZ7XHAQT1S46NYPN1PW1DYA7"
		dispatchB      = "01KZ7XHAQT1S46NYPN1PW1DYA8"
		matterACommand = "01KZ7XHAQT1S46NYPN1PW1DYA9"
		matterBCommand = "01KZ7XHAQT1S46NYPN1PW1WY00"
		stageCommand   = "01KZ7XHAQT1S46NYPN1PW1WY01"
		stepCommand    = "01KZ7XHAQT1S46NYPN1PW1WY02"
	)
	createFixtureMatter(t, fixture.store, peer, artifactSigner, state,
		"01KZ7XHAQT1S46NYPN1PW1WY10", matterA, "01KZ7XHAQT1S46NYPN1PW1WY20", "Standalone", "standalone", 1)
	createFixtureMatter(t, fixture.store, peer, artifactSigner, state,
		"01KZ7XHAQT1S46NYPN1PW1WY11", matterB, "01KZ7XHAQT1S46NYPN1PW1WY21", "Nested", "nested", 2)
	state, err := PullAndInstall(ctx, fixture.profile, fixture.roots, directory)
	if err != nil || state.Prefix.EventCount != 2 {
		t.Fatalf("install initial M6 Matter identities: prefix=%+v err=%v", state.Prefix, err)
	}

	completeM6ClaimForPull(t, fixture, state, peer, artifactSigner, matterA, claimA, batchA, grantA, snapshotA, journalA, worktreeA, dispatchA, 3, 22)
	completeM6ClaimForPull(t, fixture, state, peer, artifactSigner, matterB, claimB, batchB, grantB, snapshotB, journalB, worktreeB, dispatchB, 4, 25)
	state, err = PullAndInstall(ctx, fixture.profile, fixture.roots, directory)
	if err != nil || state.Prefix.EventCount != 8 {
		t.Fatalf("install both acquired claim grants before lifecycle commands: prefix=%+v err=%v", state.Prefix, err)
	}
	claimContextA := &operation.ClaimContext{ID: claimA, Epoch: "1"}
	claimContextB := &operation.ClaimContext{ID: claimB, Epoch: "1"}
	signer := func(_ context.Context, message []byte) ([]byte, error) {
		return ed25519.Sign(artifactSigner, message), nil
	}

	completeM6LifecycleForPull(t, fixture, peer, artifactSigner, state, directory, journalA, 1, matterACommand, 5, cloneID, worktreeA, claimContextA,
		operation.MatterStartV1, operation.NodeLifecycleInput{NodeID: matterA}, m6PullEventID(28))
	state, err = PullAndInstall(ctx, fixture.profile, fixture.roots, directory)
	if err != nil || projectedMatterState(t, state, matterA) != "in-progress" {
		t.Fatalf("PullAndInstall standalone Matter start projection: state=%+v err=%v", state, err)
	}
	completeM6LifecycleForPull(t, fixture, peer, artifactSigner, state, directory, journalA, 2, "01KZ7XHAQT1S46NYPN1PW1WY03", 6, cloneID, worktreeA, claimContextA,
		operation.MatterPauseV1, operation.NodeLifecycleInput{NodeID: matterA}, m6PullEventID(29))
	completeM6LifecycleForPull(t, fixture, peer, artifactSigner, state, directory, journalA, 3, "01KZ7XHAQT1S46NYPN1PW1WY04", 7, cloneID, worktreeA, claimContextA,
		operation.MatterResumeV1, operation.NodeLifecycleInput{NodeID: matterA}, m6PullEventID(30))
	completeM6LifecycleForPull(t, fixture, peer, artifactSigner, state, directory, journalA, 4, "01KZ7XHAQT1S46NYPN1PW1WY05", 8, cloneID, worktreeA, claimContextA,
		operation.MatterCancelV1, operation.NodeLifecycleInput{NodeID: matterA}, m6PullEventID(31))

	stageDefinition := operation.StageCreateV1
	stageCreate := m6PullCommand(state, stageCommand, 9, cloneID, worktreeB, claimContextB, stageDefinition,
		operation.StageCreateInput{MatterID: matterB, Title: "Track"})
	stagePending := submitM6PullCommand(t, fixture.store, peer, stageCreate)
	stageResult := operation.Result{Code: operation.ResultSucceeded, Output: operation.StageCreateOutput{
		ID: stageB, MatterID: matterB, Locator: "track", Title: "Track", SortKey: 1000, State: "planned",
	}}
	stageStatus, completeErr := fixture.store.CompleteCommand(ctx, stagePending, stageResult, stageB, m6PullEventID(32), time.Now().UTC(), signer)
	if completeErr != nil {
		t.Fatalf("complete M6 Stage create: %v", completeErr)
	}
	installAndAcknowledgeM6Claim(t, fixture, state, directory, journalB, 1, stageStatus.Receipt)
	stepCreate := m6PullCommand(state, stepCommand, 10, cloneID, worktreeB, claimContextB, operation.StepCreateV2,
		operation.StepCreateInput{ParentID: stageB, Title: "Child"})
	stepPending := submitM6PullCommand(t, fixture.store, peer, stepCreate)
	stepResult := operation.Result{Code: operation.ResultSucceeded, Output: operation.StepCreateOutput{
		ID: stepB, ParentID: stageB, MatterID: matterB, Locator: "step-01", Title: "Child", SortKey: 1000, State: "planned",
	}}
	stepStatus, completeErr := fixture.store.CompleteCommand(ctx, stepPending, stepResult, stepB, m6PullEventID(33), time.Now().UTC(), signer)
	if completeErr != nil {
		t.Fatalf("complete M6 Step create under Stage: %v", completeErr)
	}
	installAndAcknowledgeM6Claim(t, fixture, state, directory, journalB, 2, stepStatus.Receipt)
	completeM6LifecycleForPull(t, fixture, peer, artifactSigner, state, directory, journalB, 3, "01KZ7XHAQT1S46NYPN1PW1WY06", 11, cloneID, worktreeB, claimContextB,
		operation.StepStartV1, operation.StepLifecycleInput{StepID: stepB}, m6PullEventID(34), m6PullEventID(35), m6PullEventID(36))
	completeM6LifecycleForPull(t, fixture, peer, artifactSigner, state, directory, journalB, 4, "01KZ7XHAQT1S46NYPN1PW1WY07", 12, cloneID, worktreeB, claimContextB,
		operation.StepPauseV1, operation.StepLifecycleInput{StepID: stepB}, m6PullEventID(37))
	state, err = PullAndInstall(ctx, fixture.profile, fixture.roots, directory)
	if err != nil {
		t.Fatalf("PullAndInstall paused nested Step projection: %v", err)
	}
	var pausedStep stepProjection
	foundPausedStep := false
	for _, raw := range state.StepProjections {
		var projection stepProjection
		if err = json.Unmarshal(raw, &projection); err != nil {
			t.Fatal(err)
		}
		if projection.ID == stepB {
			pausedStep, foundPausedStep = projection, true
		}
	}
	if !foundPausedStep || pausedStep.State != "paused" || pausedStep.MatterID != matterB || projectedMatterState(t, state, matterB) != "in-progress" {
		t.Fatalf("PullAndInstall folded nested Matter/Step projections: Matter=%q Step=%+v found=%t", projectedMatterState(t, state, matterB), pausedStep, foundPausedStep)
	}
	var stageParentFolded, stepParentFolded bool
	for _, record := range state.EventRecords {
		fields, decodeErr := wipdwire.DecodeCanonicalMap(record.Record,
			"schema", "event_id", "domain_id", "command_id", "request_hash", "environment", "acted_at", "occurred_at", "kind", "subject_id", "repo_id", "payload")
		if decodeErr != nil {
			t.Fatal(decodeErr)
		}
		payload, payloadOK := fields["payload"].(map[string]any)
		if fields["kind"] == "stage.created" && fields["subject_id"] == stageB && payloadOK && payload["matter_id"] == matterB {
			stageParentFolded = true
		}
		if fields["kind"] == "step.created" && fields["subject_id"] == stepB && payloadOK && payload["parent"] == stageB {
			stepParentFolded = true
		}
	}
	if !stageParentFolded || !stepParentFolded {
		t.Fatalf("PullAndInstall nested event lineage: Stage→Matter=%t Step→Stage=%t", stageParentFolded, stepParentFolded)
	}
	completeM6LifecycleForPull(t, fixture, peer, artifactSigner, state, directory, journalB, 5, "01KZ7XHAQT1S46NYPN1PW1WY08", 13, cloneID, worktreeB, claimContextB,
		operation.StepResumeV1, operation.StepLifecycleInput{StepID: stepB}, m6PullEventID(38))
	completeM6LifecycleForPull(t, fixture, peer, artifactSigner, state, directory, journalB, 6, "01KZ7XHAQT1S46NYPN1PW1WY09", 14, cloneID, worktreeB, claimContextB,
		operation.StepFinishV1, operation.StepLifecycleInput{StepID: stepB}, m6PullEventID(39))
	completeM6LifecycleForPull(t, fixture, peer, artifactSigner, state, directory, journalB, 7, "01KZ7XHAQT1S46NYPN1PW1WY12", 15, cloneID, worktreeB, claimContextB,
		operation.StagePauseV1, operation.NodeLifecycleInput{NodeID: stageB}, m6PullEventID(40))
	completeM6LifecycleForPull(t, fixture, peer, artifactSigner, state, directory, journalB, 8, "01KZ7XHAQT1S46NYPN1PW1WY13", 16, cloneID, worktreeB, claimContextB,
		operation.StageResumeV1, operation.NodeLifecycleInput{NodeID: stageB}, m6PullEventID(41))
	completeM6LifecycleForPull(t, fixture, peer, artifactSigner, state, directory, journalB, 9, "01KZ7XHAQT1S46NYPN1PW1WY14", 17, cloneID, worktreeB, claimContextB,
		operation.StageFinishV1, operation.NodeLifecycleInput{NodeID: stageB}, m6PullEventID(42))
	completeM6LifecycleForPull(t, fixture, peer, artifactSigner, state, directory, journalB, 0, matterBCommand, 18, cloneID, worktreeB, claimContextB,
		operation.MatterFinishV1, operation.MatterFinishInput{MatterID: matterB}, m6PullEventID(43), m6PullEventID(44))

	installed, err := PullAndInstall(ctx, fixture.profile, fixture.roots, directory)
	if err != nil {
		t.Fatalf("pull and install M6 lifecycle event history: %v", err)
	}
	if installed.Prefix.EventCount != uint64(len(installed.EventRecords)) || len(installed.Projections) != 2 || len(installed.StepProjections) != 1 {
		t.Fatalf("installed M6 state counts: prefix=%+v matters=%d steps=%d", installed.Prefix, len(installed.Projections), len(installed.StepProjections))
	}
	states := map[string]string{}
	for _, raw := range installed.Projections {
		var projection eventProjection
		if err = json.Unmarshal(raw, &projection); err != nil {
			t.Fatal(err)
		}
		states[projection.ID] = projection.State
	}
	var stepProjectionValue stepProjection
	if err = json.Unmarshal(installed.StepProjections[0], &stepProjectionValue); err != nil {
		t.Fatal(err)
	}
	if states[matterA] != "canceled" || states[matterB] != "done" || stepProjectionValue.State != "done" || stepProjectionValue.MatterID != matterB {
		t.Fatalf("installed M6 lifecycle states: Matter A=%q Matter B=%q Step=%+v", states[matterA], states[matterB], stepProjectionValue)
	}
	var sawStandaloneMatterStart, sawStageFinish, sawInlineBatchSweep bool
	var cascadeKinds, cascadeSubjects, cascadeEvents []string
	var cascadeFlags []bool
	var cascadeCauses []*string
	for _, event := range installed.EventRecords {
		fields, decodeErr := wipdwire.DecodeCanonicalMap(event.Record,
			"schema", "event_id", "domain_id", "command_id", "request_hash", "environment", "acted_at", "occurred_at", "kind", "subject_id", "repo_id", "payload")
		if decodeErr != nil {
			t.Fatal(decodeErr)
		}
		payload := fields["payload"].(map[string]any)
		switch fields["kind"] {
		case "matter.started":
			if fields["subject_id"] == matterA && wipdwire.ExactMapKeys(payload, "from", "to") {
				sawStandaloneMatterStart = true
			}
		case "stage.finished":
			sawStageFinish = true
		case "batch.swept":
			sawInlineBatchSweep = true
		}
		if fields["command_id"] == "01KZ7XHAQT1S46NYPN1PW1WY06" &&
			(fields["kind"] == "matter.started" || fields["kind"] == "stage.started" || fields["kind"] == "step.started") {
			cause, _ := payload["cause_event_id"].(string)
			var causePointer *string
			if cause != "" {
				causePointer = &cause
			}
			cascadeKinds = append(cascadeKinds, asString(fields["kind"]))
			cascadeSubjects = append(cascadeSubjects, asString(fields["subject_id"]))
			cascadeEvents = append(cascadeEvents, event.EventID)
			cascadeFlags = append(cascadeFlags, payload["cascade"] == true)
			cascadeCauses = append(cascadeCauses, causePointer)
		}
	}
	if len(cascadeKinds) != 3 || cascadeKinds[0] != "matter.started" || cascadeKinds[1] != "stage.started" || cascadeKinds[2] != "step.started" ||
		cascadeSubjects[0] != matterB || cascadeSubjects[1] != stageB || cascadeSubjects[2] != stepB ||
		!cascadeFlags[0] || !cascadeFlags[1] || cascadeFlags[2] || cascadeCauses[0] != nil ||
		cascadeCauses[1] == nil || *cascadeCauses[1] != cascadeEvents[0] ||
		cascadeCauses[2] == nil || *cascadeCauses[2] != cascadeEvents[1] {
		t.Fatalf("installed Matter→Stage→Step start causation: kinds=%v subjects=%v events=%v cascade=%v causes=%v",
			cascadeKinds, cascadeSubjects, cascadeEvents, cascadeFlags, cascadeCauses)
	}
	if !sawStandaloneMatterStart || !sawStageFinish || sawInlineBatchSweep {
		t.Fatalf("installed lifecycle event evidence: standalone-matter-start=%t stage-finish=%t inline-batch-sweep=%t",
			sawStandaloneMatterStart, sawStageFinish, sawInlineBatchSweep)
	}
}

func projectedMatterState(t *testing.T, state ClientState, matterID string) string {
	t.Helper()
	var lifecycle string
	found := false
	for _, raw := range state.Projections {
		var projection eventProjection
		if err := json.Unmarshal(raw, &projection); err != nil {
			t.Fatalf("decode client Matter projection: %v", err)
		}
		if projection.ID == matterID {
			if found {
				t.Fatalf("duplicate client Matter projection for %s", matterID)
			}
			lifecycle, found = projection.State, true
		}
	}
	if !found {
		t.Fatalf("client Matter projection for %s is missing", matterID)
	}
	return lifecycle
}

func completeM6ClaimForPull(t *testing.T, fixture *clientFixture, state ClientState, peer tls.ConnectionState, signer ed25519.PrivateKey,
	matter, claim, batch, grant, snapshot, journal, worktree, dispatch string, sequence uint64, firstEvent int,
) {
	t.Helper()
	commandID := fmt.Sprintf("%026d", 200+sequence)
	cloneID := "01KZ7XHAQT1S46NYPN1PW1DYA6"
	now := time.Now().UTC().Truncate(time.Second)
	raw, err := wipdwire.EncodeCanonical(map[string]any{
		"schema": "wipd.command/1", "command_id": commandID,
		"authority":   map[string]any{"domain_id": state.DomainID, "expected_epoch": state.Epoch},
		"environment": map[string]any{"id": state.EnvironmentID, "sequence": sequence},
		"acted_at":    now.Format(time.RFC3339Nano), "actor": "human",
		"causation_command_id": nil, "correlation_command_id": commandID,
		"operation": map[string]any{"name": "claim.acquire", "version": uint64(1)},
		"context":   map[string]any{"repo_id": state.RepoID, "clone_id": cloneID, "worktree_id": worktree},
		"claim":     nil,
		"input":     map[string]any{"matter_id": matter, "worktree_id": worktree, "dispatch_mode": "anonymous-matter", "requested_dispatch_id": dispatch},
		"blobs":     []any{},
	})
	if err != nil {
		t.Fatal(err)
	}
	hash := testDigest(append([]byte("wipd/request-hash/v1\x00"), raw...))
	anchor, err := fixture.store.CurrentPrefixAnchor(context.Background(), state.DomainID)
	if err != nil {
		t.Fatal(err)
	}
	pending, err := fixture.store.SubmitClaimAcquire(context.Background(), raw, hash, anchor, peer, now)
	if err != nil || pending.Owner == nil {
		t.Fatalf("submit isolated M6 claim acquisition for %s: %+v %v", matter, pending, err)
	}
	ids := []string{m6PullEventID(firstEvent), m6PullEventID(firstEvent + 1), m6PullEventID(firstEvent + 2)}
	allocation := authoritystore.AcquireAllocation{
		ClaimID: claim, BatchID: batch, GrantID: grant, SnapshotID: snapshot, JournalID: journal,
		Installed: anchor, EventIDs: ids,
	}
	completed, _, err := fixture.store.CompleteClaimAcquire(context.Background(), pending.Owner, allocation,
		now, func(_ context.Context, message []byte) ([]byte, error) { return ed25519.Sign(signer, message), nil })
	if err != nil || completed.Pending || len(completed.Receipt) == 0 {
		t.Fatalf("complete isolated M6 claim acquisition for %s: allocation=%+v status=%+v err=%v",
			matter, allocation, completed, err)
	}
}

func m6PullCommand(state ClientState, id string, sequence uint64, clone, worktree string, claim *operation.ClaimContext,
	definition operation.Definition, input operation.Input,
) operation.Command {
	return operation.Command{
		ID: id, AuthorityDomainID: state.DomainID, ExpectedAuthorityEpoch: state.Epoch,
		EnvironmentID: state.EnvironmentID, EnvironmentSequence: sequence, ActedAt: time.Now().UTC().Truncate(time.Second).Format(time.RFC3339Nano),
		CorrelationCommandID: id,
		Request: operation.Request{
			Operation: definition.Metadata().Operation, Actor: "human",
			Context: operation.Context{Repo: state.RepoID, Clone: clone, Worktree: worktree},
			Claim:   claim, Input: input, Blobs: []operation.BlobInput{},
		},
	}
}

func submitM6PullCommand(t *testing.T, store *authoritystore.Store, peer tls.ConnectionState, command operation.Command) *authoritystore.Execution {
	t.Helper()
	hash, err := command.RequestHash()
	if err != nil {
		t.Fatal(err)
	}
	pending, err := store.SubmitCommand(context.Background(), command, hash, peer, time.Now().UTC())
	if err != nil || pending.Owner == nil {
		t.Fatalf("submit M6 pull command %s (id=%s sequence=%d): %+v %v", command.Request.Operation, command.ID, command.EnvironmentSequence, pending, err)
	}
	return pending.Owner
}

func completeM6LifecycleForPull(t *testing.T, fixture *clientFixture, peer tls.ConnectionState, signer ed25519.PrivateKey,
	state ClientState, directory, journal string, journalPosition uint64, id string, sequence uint64, clone, worktree string, claim *operation.ClaimContext,
	definition operation.Definition, input operation.Input, eventIDs ...string,
) {
	t.Helper()
	command := m6PullCommand(state, id, sequence, clone, worktree, claim, definition, input)
	owner := submitM6PullCommand(t, fixture.store, peer, command)
	status, err := fixture.store.CompleteConnectedLifecycle(context.Background(), owner, eventIDs, time.Now().UTC(),
		func(_ context.Context, message []byte) ([]byte, error) { return ed25519.Sign(signer, message), nil })
	if err != nil || status.Pending || len(status.Receipt) == 0 {
		t.Fatalf("complete M6 pull lifecycle %s: %+v %v", definition.Metadata().Operation, status, err)
	}
	if definition.Metadata().Delivery == operation.DeliveryClaim {
		installAndAcknowledgeM6Claim(t, fixture, state, directory, journal, journalPosition, status.Receipt)
	}
}

func installAndAcknowledgeM6Claim(t *testing.T, fixture *clientFixture, state ClientState, directory, journal string, position uint64, receipt []byte) {
	t.Helper()
	installed, err := PullAndInstall(context.Background(), fixture.profile, fixture.roots, directory)
	if err != nil {
		t.Fatalf("install terminal claim event prefix before next command: %v", err)
	}
	anchor, err := fixture.store.CurrentPrefixAnchor(context.Background(), state.DomainID)
	if err != nil {
		t.Fatal(err)
	}
	if installed.Prefix.EventCount != anchor.EventCount || installed.Prefix.Digest != anchor.Digest {
		t.Fatalf("installed claim receipt prefix differs from authority: client=%+v authority=%+v", installed.Prefix, anchor)
	}
	if err = fixture.store.AcknowledgeClaimJournalEntry(context.Background(), state.DomainID, journal, position, receipt, anchor); err != nil {
		t.Fatalf("acknowledge installed terminal claim receipt at journal position %d: %v", position, err)
	}
}

func m6PullEventID(sequence int) string {
	return fmt.Sprintf("01KZ7XHAQT1S46NYPN1PW1WY%02d", sequence)
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
	legacyFinishPrefix := append([]wipdwire.EventRecord(nil), records[:7]...)
	legacyFinishPrefix = append(legacyFinishPrefix,
		event(507, 405, 5, "step.finished", stepID, map[string]any{"from": "in-progress", "to": "done"}),
		event(508, 406, 6, "matter.finished", matterID, map[string]any{"from": "in-progress", "to": "done"}))
	if _, _, _, err = foldEventRecords(legacyFinishPrefix, domainID); err != nil {
		t.Fatalf("fold completed Matter finish without legacy inline sweep: %v", err)
	}
	legacyInlineSweep := append(append([]wipdwire.EventRecord(nil), legacyFinishPrefix...),
		event(509, 406, 6, "batch.swept", batchID, map[string]any{}))
	if _, _, _, err = foldEventRecords(legacyInlineSweep, domainID); err != nil {
		t.Fatalf("fold compatible historical inline-sweep Matter finish: %v", err)
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
	journalRoot := filepath.Join(privateTempDir(t), "environment-journal")
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

func step7ClientTestEvent(t *testing.T, eventNumber, commandNumber int, sequence uint64, kind, subject string, payload map[string]any) wipdwire.EventRecord {
	t.Helper()
	eventID := fmt.Sprintf("%026d", eventNumber)
	commandID := fmt.Sprintf("%026d", commandNumber)
	actedAt := "2026-09-23T11:59:00Z"
	record, err := wipdwire.EncodeCanonical(map[string]any{
		"schema": "wipd.event/1", "event_id": eventID, "domain_id": testDomainID,
		"command_id": commandID, "request_hash": testDigest([]byte("client-step7:" + commandID)),
		"environment": map[string]any{"id": "01KZ7XHAQT1S46NYPN1PW1DX3A", "sequence": sequence},
		"acted_at":    actedAt, "occurred_at": actedAt, "kind": kind,
		"subject_id": subject, "repo_id": testRepoID, "payload": payload,
	})
	if err != nil {
		t.Fatal(err)
	}
	return wipdwire.EventRecord{EventID: eventID, Record: record}
}

func step7ClientGateHistory(t *testing.T) []wipdwire.EventRecord {
	t.Helper()
	matterID := "00000000000000000000000041"
	batchID := "00000000000000000000000042"
	claimID := "00000000000000000000000043"
	dispatchID := "00000000000000000000000044"
	return []wipdwire.EventRecord{
		step7ClientTestEvent(t, 500, 400, 1, "matter.created", matterID, map[string]any{
			"id": matterID, "locator": "gate-client", "title": "Gate client",
		}),
		step7ClientTestEvent(t, 501, 401, 2, "batch.anonymous-created", batchID, map[string]any{
			"batch_id": batchID, "matter_id": matterID,
		}),
		step7ClientTestEvent(t, 502, 401, 2, "claim.acquired", claimID, map[string]any{
			"claim_id": claimID, "claim_epoch": uint64(1), "matter_id": matterID,
			"batch_id": batchID, "dispatch_id": dispatchID,
			"owner_environment_id": "01KZ7XHAQT1S46NYPN1PW1DX3A", "worktree_id": "00000000000000000000000045",
		}),
		step7ClientTestEvent(t, 503, 401, 2, "dispatch.opened", dispatchID, map[string]any{
			"dispatch_id": dispatchID, "matter_id": matterID, "batch_id": batchID,
			"claim_id": claimID, "worktree_id": "00000000000000000000000045",
		}),
		step7ClientTestEvent(t, 504, 402, 3, "matter.started", matterID, map[string]any{
			"from": "planned", "to": "in-progress",
		}),
		step7ClientTestEvent(t, 505, 403, 4, "matter.finished", matterID, map[string]any{
			"from": "in-progress", "to": "done",
		}),
		step7ClientTestEvent(t, 506, 404, 5, "gate.declared", testRepoID, map[string]any{
			"gate": "snapshot", "scale": "matter", "exempt": []any{matterID},
		}),
		step7ClientTestEvent(t, 507, 405, 6, "gate.declared", testRepoID, map[string]any{
			"gate": "reviewed", "scale": "matter",
		}),
		step7ClientTestEvent(t, 508, 406, 7, "gate.closed", matterID, map[string]any{
			"gate": "reviewed", "scale": "matter", "tracker_push_level": "off",
		}),
		step7ClientTestEvent(t, 509, 407, 8, "gate.declared", testRepoID, map[string]any{
			"gate": "waived", "scale": "matter",
		}),
		step7ClientTestEvent(t, 510, 408, 9, "gate.dismissed", matterID, map[string]any{
			"gate": "waived", "scale": "matter", "reason": "accepted exception", "tracker_push_level": "off",
		}),
		step7ClientTestEvent(t, 511, 409, 10, "gate.declared", testRepoID, map[string]any{
			"gate": "restored", "scale": "matter",
		}),
		step7ClientTestEvent(t, 512, 410, 11, "gate.exemption-repaired", matterID, map[string]any{
			"gate": "restored",
		}),
	}
}

func TestClientSeedFoldPersistsStrictGateProjectionAndFinishOrdering(t *testing.T) {
	records := step7ClientGateHistory(t)
	anchor, projections, stepProjections, gates, err := foldEventRecordsWithGateProjection(records, testDomainID)
	if err != nil || anchor.EventCount != uint64(len(records)) || gates == nil || len(gates.Declarations) != 4 || len(gates.States) != 4 {
		t.Fatalf("fold ordinary gate/FINISH-A history: anchor=%+v gates=%+v err=%v", anchor, gates, err)
	}
	states := make(map[string]step7GateState)
	for _, state := range gates.States {
		states[state.Gate] = state
	}
	if states["snapshot"].State != "exempt" || states["snapshot"].SourceEventID != records[6].EventID ||
		states["reviewed"].State != "closed" || states["reviewed"].SourceEventID != records[8].EventID ||
		states["waived"].State != "dismissed" || states["waived"].Reason != "accepted exception" ||
		states["restored"].State != "exempt" || states["restored"].SourceEventID != records[12].EventID {
		t.Fatalf("client gate projection lost snapshot/close/dismiss/repair distinction: %+v", states)
	}
	fixture := newClientFixture(t)
	if fixture.profile.DomainID() != testDomainID || fixture.profile.M5LabRepoID() != testRepoID {
		t.Fatalf("client fixture identity does not match transfer history: domain=%s repo=%s", fixture.profile.DomainID(), fixture.profile.M5LabRepoID())
	}
	contentProjections, err := foldContentEvents(records, testDomainID)
	if err != nil {
		t.Fatalf("fold client-state content projection: %v", err)
	}
	state := ClientState{
		Schema: "wipd.m5-client-state/1", DomainID: testDomainID, Epoch: fixture.profile.Epoch(), RepoID: testRepoID,
		EnvironmentID: "01KZ7XHAQT1S46NYPN1PW1DX3A", OwnerKeyID: fixture.profile.OwnerRootSPKI(),
		SPKIDigest:   testDigest([]byte("client-state-spki")),
		Prefix:       anchor,
		EventRecords: cloneEventRecords(records), Projections: projections, StepProjections: stepProjections,
		ContentProjections: contentProjections, GateProjection: gates, ManifestDigest: emptyManifestDigest(),
	}
	if err = validateInstalledState(state, fixture.profile); err != nil {
		t.Fatalf("validate client state with persisted gate projection: %v", err)
	}
	legacyState := state
	legacyState.GateProjection = nil
	if err = validateInstalledState(legacyState, fixture.profile); err != nil {
		t.Fatalf("validate pre-slice client state with absent derived gate cache: %v", err)
	}
	directory := t.TempDir()
	if err = installState(directory, state); err != nil {
		t.Fatalf("install client state with gate projection: %v", err)
	}
	restored, raw, err := loadInstalledState(directory)
	if err != nil {
		t.Fatalf("reload client state with gate projection: %v", err)
	}
	defer clear(raw)
	if !reflect.DeepEqual(restored.GateProjection, state.GateProjection) ||
		!reflect.DeepEqual(restored.EventRecords, state.EventRecords) || !anchorEqual(restored.Prefix, state.Prefix) {
		t.Fatalf("seed/pull client-state persistence changed gate projection or exact event prefix: restored=%+v", restored)
	}
	if bytes.Contains(bytes.Join(func() [][]byte {
		out := make([][]byte, len(records))
		for index := range records {
			out[index] = records[index].Record
		}
		return out
	}(), nil), []byte("detached-owner-proof")) {
		t.Fatal("private repair proof bytes were folded into transferred event history")
	}

	closeBeforeFinish := step7ClientGateHistory(t)[:5]
	closeBeforeFinish = append(closeBeforeFinish,
		step7ClientTestEvent(t, 520, 420, 5, "gate.declared", testRepoID, map[string]any{"gate": "early", "scale": "matter"}),
		step7ClientTestEvent(t, 521, 421, 6, "gate.closed", "00000000000000000000000041", map[string]any{
			"gate": "early", "scale": "matter",
		}),
		step7ClientTestEvent(t, 522, 422, 7, "matter.finished", "00000000000000000000000041", map[string]any{
			"from": "in-progress", "to": "done",
		}))
	if _, _, _, gates, err = foldEventRecordsWithGateProjection(closeBeforeFinish, testDomainID); err != nil || gates == nil ||
		len(gates.States) != 1 || gates.States[0].Gate != "early" || gates.States[0].State != "closed" {
		t.Fatalf("fold close-before-finish history: gates=%+v err=%v", gates, err)
	}
}

func TestClientSeedFoldRepresentsStep13EffectsAndRejectsFalseSnapshot(t *testing.T) {
	records := step7ClientGateHistory(t)
	represented := []struct {
		name   string
		record wipdwire.EventRecord
	}{
		{
			name:   "Repo config",
			record: step7ClientTestEvent(t, 513, 411, 12, "config.set", testRepoID, map[string]any{"key": "tracker.push-level", "value": "narrated"}),
		},
		{
			name:   "shared reference",
			record: step7ClientTestEvent(t, 513, 411, 12, "reference.added", "00000000000000000000000041", map[string]any{"ref": "TRACKER-17"}),
		},
	}
	for _, test := range represented {
		t.Run(test.name, func(t *testing.T) {
			candidate := append(cloneEventRecords(records), test.record)
			if _, _, _, err := foldEventRecords(candidate, testDomainID); err != nil {
				t.Fatalf("represented Step 13 effect refused: %v", err)
			}
		})
	}
	trackerCandidate := append(cloneEventRecords(records[:8]), step7ClientTestEvent(t, 508, 406, 7, "gate.closed",
		"00000000000000000000000041", map[string]any{"gate": "reviewed", "scale": "matter", "tracker_push_level": "narrated"}))
	if _, _, _, err := foldEventRecords(trackerCandidate, testDomainID); !errors.Is(err, ErrInvalidClientState) {
		t.Fatalf("gate snapshot without matching config accepted: %v", err)
	}
}

func TestClientGateRepairUsesNodeLivenessAtItsEventBoundary(t *testing.T) {
	matterID := "00000000000000000000000041"
	stageID := "00000000000000000000000042"
	stepID := "00000000000000000000000043"
	base := []wipdwire.EventRecord{
		step7ClientTestEvent(t, 600, 500, 1, "matter.created", matterID, map[string]any{
			"id": matterID, "locator": "repair-live", "title": "Repair liveness",
		}),
		step7ClientTestEvent(t, 601, 501, 2, "stage.created", stageID, map[string]any{
			"matter_id": matterID, "locator": "stage", "title": "Stage", "sort_key": uint64(1000),
		}),
		step7ClientTestEvent(t, 602, 502, 3, "step.created", stepID, map[string]any{
			"parent": stageID, "title": "Step",
		}),
		step7ClientTestEvent(t, 603, 503, 4, "step.started", stepID, map[string]any{
			"from": "planned", "to": "in-progress",
		}),
		step7ClientTestEvent(t, 604, 504, 5, "step.finished", stepID, map[string]any{
			"from": "in-progress", "to": "done",
		}),
		step7ClientTestEvent(t, 605, 505, 6, "gate.declared", testRepoID, map[string]any{
			"gate": "repair-target", "scale": "step",
		}),
	}
	closeBeforeRemoval := cloneEventRecords(base)
	closeBeforeRemoval = append(closeBeforeRemoval,
		step7ClientTestEvent(t, 606, 506, 7, "gate.exemption-repaired", stepID, map[string]any{"gate": "repair-target"}),
		step7ClientTestEvent(t, 607, 507, 8, "step.removed", stepID, map[string]any{"reason": "no longer needed"}),
	)
	projection, err := foldStep7GateProjection(closeBeforeRemoval, testDomainID)
	if err != nil || projection == nil || len(projection.States) != 1 || projection.States[0].State != "exempt" {
		t.Fatalf("valid repair before later node removal did not remain in history: projection=%+v err=%v", projection, err)
	}
	removalBeforeRepair := cloneEventRecords(base)
	removalBeforeRepair = append(removalBeforeRepair,
		step7ClientTestEvent(t, 608, 506, 7, "step.removed", stepID, map[string]any{"reason": "no longer needed"}),
		step7ClientTestEvent(t, 609, 507, 8, "gate.exemption-repaired", stepID, map[string]any{"gate": "repair-target"}),
	)
	if _, err = foldStep7GateProjection(removalBeforeRepair, testDomainID); !errors.Is(err, ErrInvalidClientState) {
		t.Fatalf("repair after target removal was accepted: %v", err)
	}
}

func TestClientGateRepairRequiresEligibilityAtDeclarationBoundaryInCompleteTransfers(t *testing.T) {
	fixture := newClientFixture(t)
	tests := []struct {
		name          string
		makeRecords   func(*testing.T) []wipdwire.EventRecord
		pullPrefixLen int
		wantAccepted  bool
	}{
		{
			name: "done only after declaration",
			makeRecords: func(t *testing.T) []wipdwire.EventRecord {
				return step7ClientRepairEligibilityHistory(t, "late-done")
			},
			pullPrefixLen: 6,
		},
		{
			name: "own prerequisite closed only after declaration",
			makeRecords: func(t *testing.T) []wipdwire.EventRecord {
				return step7ClientRepairEligibilityHistory(t, "late-own-close")
			},
			pullPrefixLen: 8,
		},
		{
			name: "enclosing prerequisite closed only after declaration",
			makeRecords: func(t *testing.T) []wipdwire.EventRecord {
				return step7ClientEnclosingRepairEligibilityHistory(t)
			},
			pullPrefixLen: 10,
		},
		{
			name: "done and own prerequisite satisfied at declaration",
			makeRecords: func(t *testing.T) []wipdwire.EventRecord {
				return step7ClientRepairEligibilityHistory(t, "eligible")
			},
			pullPrefixLen: 9,
			wantAccepted:  true,
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			records := test.makeRecords(t)
			for _, kind := range []string{"seed", "pull"} {
				t.Run(kind, func(t *testing.T) {
					prior := []wipdwire.EventRecord(nil)
					if kind == "pull" {
						prior = cloneEventRecords(records[:test.pullPrefixLen])
					}
					frames, installed := step7ClientRepairTransferFrames(t, kind, records, prior)
					state, err := verifyTransferFrames(frames, kind, fixture.profile, testRepoID,
						"01KZ7XHAQT1S46NYPN1PW1DX3A", "sha256:"+strings.Repeat("a", 64), installed, prior)
					if test.wantAccepted {
						if err != nil || state.GateProjection == nil || !anchorEqual(state.Prefix, step7IndependentPrefixAnchor(records)) {
							t.Fatalf("eligible complete %s was refused or partially folded: state=%+v err=%v", kind, state, err)
						}
						for _, gateState := range state.GateProjection.States {
							if gateState.Gate == "repair-target" && gateState.State == "exempt" && gateState.SourceEventID == records[len(records)-1].EventID {
								return
							}
						}
						t.Fatalf("eligible %s omitted the repaired projection: %+v", kind, state.GateProjection)
					}
					if !errors.Is(err, ErrInvalidClientState) || state.Prefix.EventCount != 0 || state.EventRecords != nil || state.GateProjection != nil {
						t.Fatalf("ineligible complete %s did not fail closed without partial state: state=%+v err=%v", kind, state, err)
					}
				})
			}
		})
	}
}

func step7ClientRepairEligibilityHistory(t *testing.T, scenario string) []wipdwire.EventRecord {
	t.Helper()
	matterID := "00000000000000000000000041"
	records := cloneEventRecords(step7ClientGateHistory(t)[:5])
	appendEvent := func(eventNumber, commandNumber int, sequence uint64, kind, subject string, payload map[string]any) {
		records = append(records, step7ClientTestEvent(t, eventNumber, commandNumber, sequence, kind, subject, payload))
	}
	sequence := uint64(4)
	eventNumber, commandNumber := 520, 420
	appendEventAt := func(kind, subject string, payload map[string]any) {
		appendEvent(eventNumber, commandNumber, sequence, kind, subject, payload)
		eventNumber++
		commandNumber++
		sequence++
	}
	finish := func() {
		appendEventAt("matter.finished", matterID, map[string]any{"from": "in-progress", "to": "done"})
	}
	declare := func(gate string, scale string) {
		appendEventAt("gate.declared", testRepoID, map[string]any{"gate": gate, "scale": scale})
	}
	closeGate := func(gate, node, scale string) {
		appendEventAt("gate.closed", node, map[string]any{"gate": gate, "scale": scale, "tracker_push_level": "off"})
	}
	switch scenario {
	case "late-done":
		declare("repair-target", "matter")
		finish()
	case "late-own-close":
		finish()
		declare("other-own", "matter")
		declare("repair-target", "matter")
		closeGate("other-own", matterID, "matter")
	case "eligible":
		finish()
		declare("other-own", "matter")
		closeGate("other-own", matterID, "matter")
		declare("repair-target", "matter")
	default:
		t.Fatalf("unknown repair eligibility scenario %q", scenario)
	}
	appendEventAt("gate.exemption-repaired", matterID, map[string]any{"gate": "repair-target"})
	return records
}

func step7ClientEnclosingRepairEligibilityHistory(t *testing.T) []wipdwire.EventRecord {
	t.Helper()
	matterID := "00000000000000000000000041"
	stageID := "00000000000000000000000046"
	records := cloneEventRecords(step7ClientGateHistory(t)[:5])
	eventNumber, commandNumber := 540, 440
	sequence := uint64(4)
	appendEvent := func(kind, subject string, payload map[string]any) {
		records = append(records, step7ClientTestEvent(t, eventNumber, commandNumber, sequence, kind, subject, payload))
		eventNumber++
		commandNumber++
		sequence++
	}
	appendEvent("stage.created", stageID, map[string]any{
		"matter_id": matterID, "locator": "repair-stage", "title": "Repair stage", "sort_key": uint64(1000),
	})
	appendEvent("stage.started", stageID, map[string]any{"from": "planned", "to": "in-progress"})
	appendEvent("stage.finished", stageID, map[string]any{"from": "in-progress", "to": "done"})
	appendEvent("gate.declared", testRepoID, map[string]any{"gate": "enclosing-open", "scale": "matter"})
	appendEvent("gate.declared", testRepoID, map[string]any{"gate": "repair-target", "scale": "stage"})
	appendEvent("gate.closed", matterID, map[string]any{
		"gate": "enclosing-open", "scale": "matter", "tracker_push_level": "off",
	})
	appendEvent("gate.exemption-repaired", stageID, map[string]any{"gate": "repair-target"})
	return records
}

func step7ClientRepairTransferFrames(t *testing.T, kind string, all, prior []wipdwire.EventRecord) ([]wipdwire.Frame, wipdwire.PrefixAnchor) {
	t.Helper()
	if kind != "seed" && kind != "pull" || kind == "seed" && len(prior) != 0 || len(prior) > len(all) {
		t.Fatalf("invalid %s test transfer prefix: prior=%d records=%d", kind, len(prior), len(all))
	}
	startAnchor := step7IndependentPrefixAnchor(prior)
	endAnchor := step7IndependentPrefixAnchor(all)
	delta := all[len(prior):]
	var eventBytes uint64
	for _, record := range delta {
		eventBytes += uint64(len(record.Record))
	}
	const requestID = "01KZ7XHAQT1S46NYPN1PW1DX3D"
	const transferID = "01KZ7XHAQT1S46NYPN1PW1DX3E"
	const snapshotID = "01KZ7XHAQT1S46NYPN1PW1DX3F"
	manifestDigest := emptyManifestDigest()
	frames := []wipdwire.Frame{}
	if kind == "seed" {
		start := wipdwire.SeedStart{
			Schema: "wipd.seed-start/1", TransferID: transferID, DomainID: testDomainID, Epoch: 1,
			StoreSchema: "wipd.store/1", SnapshotID: snapshotID,
			Prefix: struct {
				Start wipdwire.PrefixAnchor `cbor:"start"`
				End   wipdwire.PrefixAnchor `cbor:"end"`
			}{Start: startAnchor, End: endAnchor},
			EventCount: uint64(len(delta)), EventByteLength: eventBytes, ManifestDigest: manifestDigest,
		}
		frames = append(frames, wipdwire.Frame{RequestID: requestID, Sequence: 0, Kind: "seed.start", Payload: mustEncode(t, start)})
	} else {
		start := wipdwire.PullStart{
			Schema: "wipd.pull-start/1", TransferID: transferID, DomainID: testDomainID, Epoch: 1,
			Prefix: struct {
				Start wipdwire.PrefixAnchor `cbor:"start"`
				End   wipdwire.PrefixAnchor `cbor:"end"`
			}{Start: startAnchor, End: endAnchor},
			EventCount: uint64(len(delta)), EventByteLength: eventBytes, ManifestDigest: manifestDigest,
		}
		frames = append(frames, wipdwire.Frame{RequestID: requestID, Sequence: 0, Kind: "pull.start", Payload: mustEncode(t, start)})
	}
	for index, record := range delta {
		frames = append(frames, wipdwire.Frame{
			RequestID: requestID, Sequence: uint64(index + 1), Kind: "event.record",
			Payload: mustEncode(t, record),
		})
	}
	manifest := wipdwire.BlobManifest{
		Schema: "wipd.blob-manifest/1", DomainID: testDomainID, Epoch: 1, AsOf: endAnchor,
		Entries: []wipdwire.BlobManifestEntry{}, Digest: manifestDigest,
	}
	frames = append(frames, wipdwire.Frame{RequestID: requestID, Sequence: uint64(len(frames)), Kind: "blob.manifest", Payload: mustEncode(t, manifest)})
	if kind == "seed" {
		end := wipdwire.SeedEnd{
			Schema: "wipd.seed-end/1", TransferID: transferID,
			VerifiedPrefix: endAnchor, ManifestDigest: manifestDigest, Complete: true,
		}
		frames = append(frames, wipdwire.Frame{RequestID: requestID, Sequence: uint64(len(frames)), Kind: "seed.end", Payload: mustEncode(t, end)})
	} else {
		end := wipdwire.PullEnd{TransferID: transferID, VerifiedPrefix: endAnchor, ManifestDigest: manifestDigest, Complete: true}
		frames = append(frames, wipdwire.Frame{RequestID: requestID, Sequence: uint64(len(frames)), Kind: "pull.end", Payload: mustEncode(t, end)})
	}
	return frames, startAnchor
}

func step7IndependentPrefixAnchor(records []wipdwire.EventRecord) wipdwire.PrefixAnchor {
	chain := sha256.Sum256([]byte("wipd/event-prefix/v1\x00"))
	for _, record := range records {
		var length [8]byte
		binary.BigEndian.PutUint64(length[:], uint64(len(record.Record)))
		hash := sha256.New()
		_, _ = hash.Write([]byte("wipd/event-prefix-step/v1\x00"))
		_, _ = hash.Write(chain[:])
		_, _ = hash.Write(length[:])
		_, _ = hash.Write(record.Record)
		copy(chain[:], hash.Sum(nil))
	}
	anchor := wipdwire.PrefixAnchor{EventCount: uint64(len(records)), Digest: "sha256:" + hex.EncodeToString(chain[:])}
	if len(records) > 0 {
		last := records[len(records)-1].EventID
		anchor.EventID = &last
	}
	return anchor
}
