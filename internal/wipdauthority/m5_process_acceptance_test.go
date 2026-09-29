package wipdauthority

import (
	"bytes"
	"context"
	"crypto/sha256"
	"crypto/x509"
	"encoding/hex"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"syscall"
	"testing"
	"time"

	"github.com/procrastivity/wip/internal/operation"
	"github.com/procrastivity/wip/internal/wipd"
	"github.com/procrastivity/wip/internal/wipdjournal"
	"github.com/procrastivity/wip/internal/wipdwire"
)

func TestM5AuthorityBackedMatterAndStepBirthThroughWipdProcess(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("wipd's authenticated local socket process is Linux-only")
	}
	fixture := newM5CommandFixture(t)
	registry, err := NewM5BirthRegistry()
	if err != nil {
		t.Fatal(err)
	}
	fixture.config.Registry = registry
	fixture.server = fixture.serverForStore(t, fixture.store)

	authorityCtx, stopAuthority := context.WithCancel(context.Background())
	authorityDone := make(chan error, 1)
	go func() { authorityDone <- fixture.server.Serve(authorityCtx, fixture.listener) }()
	t.Cleanup(func() {
		stopAuthority()
		select {
		case err := <-authorityDone:
			if err != nil {
				t.Errorf("authority server stopped with error: %v", err)
			}
		case <-time.After(3 * time.Second):
			t.Error("authority server did not stop")
		}
	})

	clientStateRoot := filepath.Join(t.TempDir(), "client-state")
	if err = os.Mkdir(clientStateRoot, 0o700); err != nil {
		t.Fatal(err)
	}
	privateDER, err := x509.MarshalPKCS8PrivateKey(fixture.environment)
	if err != nil {
		t.Fatal(err)
	}
	defer clear(privateDER)
	leaf, err := x509.ParseCertificate(fixture.clientCert.Certificate[0])
	if err != nil {
		t.Fatal(err)
	}
	spki := sha256.Sum256(leaf.RawSubjectPublicKeyInfo)
	authorityLeaf, err := x509.ParseCertificate(fixture.serverCert.Certificate[0])
	if err != nil {
		t.Fatal(err)
	}
	authoritySPKI := sha256.Sum256(authorityLeaf.RawSubjectPublicKeyInfo)
	state := struct {
		Schema          string                       `json:"schema"`
		RepoID          string                       `json:"repo_id"`
		DomainID        string                       `json:"domain_id"`
		Epoch           uint64                       `json:"authority_epoch"`
		EnvironmentID   string                       `json:"environment_id"`
		OwnerKeyID      string                       `json:"owner_key_id"`
		SPKIDigest      string                       `json:"spki_digest"`
		PrivateKeyPKCS8 []byte                       `json:"private_key_pkcs8"`
		CertificateDER  [][]byte                     `json:"certificate_chain_der"`
		Prefix          wipdwire.PrefixAnchor        `json:"prefix"`
		EventRecords    []wipdwire.EventRecord       `json:"event_records"`
		ManifestDigest  string                       `json:"manifest_digest"`
		ManifestEntries []wipdwire.BlobManifestEntry `json:"manifest_entries"`
		Projections     []json.RawMessage            `json:"projections"`
		StepProjections []json.RawMessage            `json:"step_projections"`
	}{
		Schema: "wipd.m5-client-state/1", RepoID: m5TestRepo, DomainID: m5TestDomain, Epoch: 1,
		EnvironmentID: m5TestEnv, OwnerKeyID: fixture.profile.OwnerRootSPKI(),
		SPKIDigest: "sha256:" + hex.EncodeToString(spki[:]), PrivateKeyPKCS8: privateDER,
		CertificateDER: [][]byte{leaf.Raw, fixture.clientCert.Certificate[1]},
		Prefix:         wipdwire.PrefixAnchor{Digest: emptyPrefixDigest()},
		EventRecords:   []wipdwire.EventRecord{}, ManifestDigest: emptyManifestDigest(),
		ManifestEntries: []wipdwire.BlobManifestEntry{}, Projections: []json.RawMessage{},
		StepProjections: []json.RawMessage{},
	}
	stateBytes, err := json.Marshal(state)
	if err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(filepath.Join(clientStateRoot, "client-state.json"), stateBytes, 0o600); err != nil {
		t.Fatalf("write authenticated empty-prefix client state: %v", err)
	}
	profileRoot, err := os.MkdirTemp("/tmp", "w8-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(profileRoot) })
	if err = os.Chmod(profileRoot, 0o700); err != nil {
		t.Fatal(err)
	}
	config := struct {
		Schema                  string `json:"schema"`
		Origin                  string `json:"origin"`
		DomainID                string `json:"domain_id"`
		Epoch                   uint64 `json:"authority_epoch"`
		RepoID                  string `json:"repo_id"`
		OwnerRootSPKI           string `json:"owner_root_spki"`
		AuthoritySPKIPin        string `json:"authority_spki_pin"`
		AuthorityCertificateDER []byte `json:"authority_certificate_der"`
		OwnerRootPublicKey      []byte `json:"owner_root_public_key"`
		ArtifactKeyCertificate  []byte `json:"artifact_key_certificate"`
		ClientStateDirectory    string `json:"client_state_directory"`
	}{
		Schema: "wipd.connected-authority-profile/2", Origin: fixture.profile.Origin(),
		DomainID: m5TestDomain, Epoch: 1, RepoID: m5TestRepo,
		OwnerRootSPKI: fixture.profile.OwnerRootSPKI(), AuthoritySPKIPin: "sha256:" + hex.EncodeToString(authoritySPKI[:]),
		AuthorityCertificateDER: fixture.serverCert.Certificate[1], OwnerRootPublicKey: fixture.ownerRoot,
		ArtifactKeyCertificate: fixture.config.ArtifactKeyCertificate, ClientStateDirectory: clientStateRoot,
	}
	configBytes, err := json.Marshal(config)
	if err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(filepath.Join(profileRoot, "connected-authority.json"), configBytes, 0o600); err != nil {
		t.Fatalf("write connected authority profile: %v", err)
	}

	_, source, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("resolve repository root for wipd process build")
	}
	repoRoot := filepath.Clean(filepath.Join(filepath.Dir(source), "../.."))
	binary := filepath.Join(t.TempDir(), "wipd")
	build := exec.Command("go", "build", "-trimpath", "-o", binary, "./cmd/wipd")
	build.Dir = repoRoot
	if output, buildErr := build.CombinedOutput(); buildErr != nil {
		t.Fatalf("build wipd process: %v\n%s", buildErr, output)
	}
	processCtx, stopProcess := context.WithCancel(context.Background())
	defer stopProcess()
	process := exec.CommandContext(processCtx, binary, "--profile-root", profileRoot)
	var processOutput bytes.Buffer
	process.Stdout, process.Stderr = &processOutput, &processOutput
	if err = process.Start(); err != nil {
		t.Fatal(err)
	}
	processDone := make(chan struct{})
	var processErr error
	go func() {
		processErr = process.Wait()
		close(processDone)
	}()
	processStopped := false
	stopDaemon := func() {
		if processStopped {
			return
		}
		processStopped = true
		if process.Process != nil {
			_ = process.Process.Signal(syscall.SIGTERM)
		}
		select {
		case <-processDone:
		case <-time.After(5 * time.Second):
			stopProcess()
			<-processDone
		}
	}
	t.Cleanup(stopDaemon)

	var client *wipd.Client
	connectDeadline := time.Now().Add(12 * time.Second)
	for time.Now().Before(connectDeadline) {
		connectCtx, cancel := context.WithTimeout(context.Background(), time.Second)
		client, err = wipd.Connect(connectCtx, profileRoot)
		cancel()
		if err == nil {
			break
		}
		select {
		case <-processDone:
			t.Fatalf("wipd process exited before readiness: %v: %s", processErr, processOutput.String())
		default:
		}
		time.Sleep(25 * time.Millisecond)
	}
	if err != nil || client == nil {
		t.Fatalf("connect to configured wipd process: %v; output: %s", err, processOutput.String())
	}
	defer func() { _ = client.Close() }()

	actedAt := time.Now().UTC().Format(time.RFC3339Nano)
	matter := operation.Command{
		ID: "01KZ7XHAQT1S46NYPN1PW1DX4A", AuthorityDomainID: m5TestDomain, ExpectedAuthorityEpoch: 1,
		EnvironmentID: m5TestEnv, EnvironmentSequence: 1, ActedAt: actedAt, CorrelationCommandID: "01KZ7XHAQT1S46NYPN1PW1DX4A",
		Request: operation.Request{
			Operation: operation.MatterCreateV1.Metadata().Operation, Actor: "human",
			Context: operation.Context{Repo: m5TestRepo}, Input: operation.MatterCreateInput{Title: "Process Acceptance Matter", Locator: "process-acceptance"},
		},
	}
	matterResult, err := client.ExecuteCommand(context.Background(), matter)
	if err != nil || matterResult.Code != operation.ResultSucceeded {
		t.Fatalf("Matter birth through daemon process = %+v, %v", matterResult, err)
	}
	matterOutput, ok := matterResult.Output.(operation.MatterCreateOutput)
	if !ok || matterOutput.ID == "" || matterOutput.Locator != "process-acceptance" {
		t.Fatalf("Matter terminal output = %#v", matterResult.Output)
	}

	step := operation.Command{
		ID: "01KZ7XHAQT1S46NYPN1PW1DX4B", AuthorityDomainID: m5TestDomain, ExpectedAuthorityEpoch: 1,
		EnvironmentID: m5TestEnv, EnvironmentSequence: 2, ActedAt: time.Now().UTC().Format(time.RFC3339Nano),
		CausationCommandID: matter.ID, CorrelationCommandID: matter.ID,
		Request: operation.Request{
			Operation: operation.StepCreateV1.Metadata().Operation, Actor: "human",
			Context: operation.Context{Repo: m5TestRepo}, Claim: &operation.ClaimContext{ID: matterOutput.ID, Epoch: "1"},
			Input: operation.StepCreateInput{ParentID: matterOutput.ID, Title: "Process Acceptance Step"},
		},
	}
	stepResult, err := client.ExecuteCommand(context.Background(), step)
	if err != nil || stepResult.Code != operation.ResultSucceeded {
		t.Fatalf("Step birth through daemon process = %+v, %v", stepResult, err)
	}
	stepOutput, ok := stepResult.Output.(operation.StepCreateOutput)
	if !ok || stepOutput.ID == "" || stepOutput.ParentID != matterOutput.ID || stepOutput.MatterID != matterOutput.ID ||
		stepOutput.Locator != "step-01" || stepOutput.State != "planned" || stepOutput.SortKey != 1000 {
		t.Fatalf("Step terminal output = %#v", stepResult.Output)
	}

	replay, err := client.ExecuteCommand(context.Background(), step)
	if err != nil || replay.Code != operation.ResultSucceeded || replay.Output != stepResult.Output {
		t.Fatalf("same-ID Step replay = %+v, %v; original=%+v", replay, err, stepResult)
	}
	anchor, err := fixture.store.CurrentPrefixAnchor(context.Background(), m5TestDomain)
	if err != nil || anchor.EventCount != 2 {
		t.Fatalf("authority event range after exact replay = %+v, %v; want exactly two birth events", anchor, err)
	}
	matterStatus, err := fixture.store.QueryCommand(context.Background(), m5TestDomain, matter.ID, m5CommandHash(t, matter), 1, fixture.peer, m5TestEnv, time.Now().UTC())
	if err != nil || matterStatus.Pending || len(matterStatus.Receipt) == 0 {
		t.Fatalf("Matter terminal authority status = %+v, %v", matterStatus, err)
	}
	stepStatus, err := fixture.store.QueryCommand(context.Background(), m5TestDomain, step.ID, m5CommandHash(t, step), 1, fixture.peer, m5TestEnv, time.Now().UTC())
	if err != nil || stepStatus.Pending || len(stepStatus.Receipt) == 0 {
		t.Fatalf("Step terminal authority status = %+v, %v", stepStatus, err)
	}
	for label, receipt := range map[string][]byte{"Matter": matterStatus.Receipt, "Step": stepStatus.Receipt} {
		fields, decodeErr := wipdwire.DecodeCanonicalMap(receipt,
			"schema", "domain_id", "authority_epoch", "identity_schema", "command_id", "request_hash", "operation", "environment", "result", "accepted_events")
		events, ok := fields["accepted_events"].(map[string]any)
		if decodeErr != nil || !ok || events["event_count"] != uint64(1) || events["first_event_id"] != events["last_event_id"] {
			t.Fatalf("%s authority receipt does not contain one event: %#v (%v)", label, fields, decodeErr)
		}
	}
	releaseID := "01KZ7XHAQT1S46NYPN1PW1DX4C"
	release, err := client.ReleaseBirthClaim(context.Background(), matterOutput.ID, releaseID, operation.Actor("human"))
	if err != nil || release.Code != operation.ResultSucceeded || release.CommandID != releaseID || len(release.Receipt) == 0 {
		t.Fatalf("birth-claim release through daemon process = %+v, %v", release, err)
	}
	releaseReplay, err := client.ReleaseBirthClaim(context.Background(), matterOutput.ID, releaseID, operation.Actor("human"))
	if err != nil || releaseReplay.Code != operation.ResultSucceeded || !bytes.Equal(releaseReplay.Receipt, release.Receipt) {
		t.Fatalf("same-ID birth-release replay = %+v, %v", releaseReplay, err)
	}
	releaseFields, err := wipdwire.DecodeCanonicalMap(release.Receipt,
		"schema", "domain_id", "authority_epoch", "identity_schema", "command_id", "request_hash", "operation", "environment", "result", "accepted_events")
	if err != nil || releaseFields["command_id"] != releaseID {
		t.Fatalf("release terminal receipt identity = %#v, %v", releaseFields, err)
	}
	releaseHash, ok := releaseFields["request_hash"].(string)
	if !ok {
		t.Fatalf("release receipt request hash has type %T", releaseFields["request_hash"])
	}
	releaseStatus, err := fixture.store.QueryCommand(context.Background(), m5TestDomain, releaseID, releaseHash, 1,
		fixture.peer, m5TestEnv, time.Now().UTC())
	if err != nil || releaseStatus.Pending || !bytes.Equal(releaseStatus.Receipt, release.Receipt) {
		t.Fatalf("authority release receipt query = %+v, %v", releaseStatus, err)
	}
	_ = client.Close()
	stopDaemon()
	recoveredClient, stopRecoveredDaemon, recoveredOutput := startWipdForBirthReleaseRecovery(t, binary, profileRoot)
	recoveredRelease, err := recoveredClient.ReleaseBirthClaim(context.Background(), matterOutput.ID, releaseID, operation.Actor("human"))
	if err != nil || recoveredRelease.Code != operation.ResultSucceeded || !bytes.Equal(recoveredRelease.Receipt, release.Receipt) {
		t.Fatalf("same-ID birth-release recovery through restarted wipd = %+v, %v", recoveredRelease, err)
	}
	acquireID := "01KZ7XHAQT1S46NYPN1PW1DX4D"
	cloneID := "01KZ7XHAQT1S46NYPN1PW1DX4E"
	worktreeID := "01KZ7XHAQT1S46NYPN1PW1DX4F"
	dispatchID := "01KZ7XHAQT1S46NYPN1PW1DX4J"
	acquireStart, err := fixture.store.CurrentPrefixAnchor(context.Background(), m5TestDomain)
	if err != nil || acquireStart.EventCount != 3 {
		t.Fatalf("authority prefix before acquisition = %+v, %v", acquireStart, err)
	}
	acquired, err := recoveredClient.AcquireClaim(context.Background(), acquireID, matterOutput.ID, cloneID, worktreeID, dispatchID, operation.Actor("human"))
	if err != nil || acquired.Code != operation.ResultSucceeded || acquired.Grant == nil || len(acquired.Receipt) == 0 ||
		acquired.Hydration.State != "offline-ready" || acquired.Hydration.RequiredEntryCount != 0 || acquired.Hydration.VerifiedPinnedEntryCount != 0 {
		t.Fatalf("existing Matter claim acquisition through restarted wipd = %+v, %v; daemon=%s", acquired, err, recoveredOutput.String())
	}
	if acquired.Grant.ClaimID == matterOutput.ID || acquired.Grant.ClaimEpoch != 1 || acquired.Grant.DispatchID != dispatchID ||
		acquired.Grant.MatterID != matterOutput.ID || acquired.Grant.BatchID == "" ||
		acquired.Installed.EventCount != 6 || acquired.Grant.AsOf.EventCount != 6 || !sameAuthorityAnchor(acquired.Installed, acquired.Grant.AsOf) {
		t.Fatalf("installed acquisition identity/as-of prefix = %+v, installed=%+v", acquired.Grant, acquired.Installed)
	}
	acquireReceiptFields, err := wipdwire.DecodeCanonicalMap(acquired.Receipt,
		"schema", "domain_id", "authority_epoch", "identity_schema", "command_id", "request_hash", "operation", "environment", "result", "accepted_events")
	acquireRequestHash, requestHashOK := acquireReceiptFields["request_hash"].(string)
	if err != nil || !requestHashOK {
		t.Fatalf("acquisition receipt request hash: %#v, %v", acquireReceiptFields, err)
	}
	authorityStatus, authorityGrant, err := fixture.store.QueryClaimGrant(context.Background(), m5TestDomain, acquireID,
		acquireRequestHash, 1, fixture.peer, m5TestEnv, time.Now().UTC())
	if err != nil || !bytes.Equal(authorityStatus.Receipt, acquired.Receipt) || authorityGrant.ID != acquired.Grant.GrantID ||
		len(authorityGrant.Start) == 0 || len(authorityGrant.Delta) == 0 || len(authorityGrant.End) == 0 {
		t.Fatalf("authority retained grant differs from installed acquisition: %+v, %v", authorityGrant, err)
	}
	var authorityStart struct {
		Claim struct {
			ID    string `cbor:"id"`
			Epoch uint64 `cbor:"epoch"`
		} `cbor:"claim"`
		MatterID           string `cbor:"matter_id"`
		BatchID            string `cbor:"batch_id"`
		DispatchID         string `cbor:"dispatch_id"`
		BlobManifestDigest string `cbor:"blob_manifest_digest"`
		Prefix             struct {
			Start wipdwire.PrefixAnchor `cbor:"start"`
			End   wipdwire.PrefixAnchor `cbor:"end"`
		} `cbor:"prefix"`
	}
	if err = wipdwire.DecodeCanonical(authorityGrant.Start, &authorityStart,
		"schema", "grant_id", "acquire_command_id", "acquire_request_hash", "domain_id", "authority_epoch", "owner_environment_id",
		"claim", "matter_id", "batch_id", "dispatch_id", "receipt", "prefix", "blob_manifest_digest"); err != nil ||
		authorityStart.Claim.ID != acquired.Grant.ClaimID || authorityStart.Claim.Epoch != acquired.Grant.ClaimEpoch ||
		authorityStart.MatterID != matterOutput.ID || authorityStart.BatchID != acquired.Grant.BatchID || authorityStart.DispatchID != dispatchID ||
		!sameAuthorityAnchor(authorityStart.Prefix.Start, wireAnchor(acquireStart)) || !sameAuthorityAnchor(authorityStart.Prefix.End, acquired.Grant.AsOf) ||
		authorityStart.BlobManifestDigest != acquired.Grant.Manifest {
		t.Fatalf("authority grant identity/as-of differs from daemon-installed grant: %+v, %v", authorityStart, err)
	}
	var authorityManifest wipdwire.BlobManifest
	if err = wipdwire.DecodeCanonical(authorityGrant.Manifest, &authorityManifest,
		"schema", "domain_id", "authority_epoch", "as_of", "entries", "manifest_digest"); err != nil ||
		authorityManifest.Schema != "wipd.blob-manifest/1" || authorityManifest.DomainID != m5TestDomain || authorityManifest.Epoch != 1 ||
		!sameAuthorityAnchor(authorityManifest.AsOf, acquired.Grant.AsOf) || authorityManifest.Digest != acquired.Grant.Manifest || len(authorityManifest.Entries) != 0 {
		t.Fatalf("authority complete manifest is not pinned to the installed grant prefix: %+v, %v", authorityManifest, err)
	}
	var authorityDelta struct {
		Start  wipdwire.PrefixAnchor  `cbor:"start"`
		End    wipdwire.PrefixAnchor  `cbor:"end"`
		Events []wipdwire.EventRecord `cbor:"events"`
	}
	if err = wipdwire.DecodeCanonical(authorityGrant.Delta, &authorityDelta, "start", "end", "events"); err != nil ||
		!sameAuthorityAnchor(authorityDelta.Start, wireAnchor(acquireStart)) || !sameAuthorityAnchor(authorityDelta.End, acquired.Grant.AsOf) ||
		len(authorityDelta.Events) != 3 {
		t.Fatalf("authority grant delta does not contain the complete acquisition range: %+v, %v", authorityDelta, err)
	}
	var authorityEnd struct {
		GrantID        string                `cbor:"grant_id"`
		VerifiedPrefix wipdwire.PrefixAnchor `cbor:"verified_prefix"`
		ManifestDigest string                `cbor:"verified_blob_manifest_digest"`
		Complete       bool                  `cbor:"complete"`
	}
	if err = wipdwire.DecodeCanonical(authorityGrant.End, &authorityEnd,
		"schema", "grant_id", "verified_prefix", "verified_blob_manifest_digest", "complete"); err != nil ||
		authorityEnd.GrantID != acquired.Grant.GrantID || !authorityEnd.Complete ||
		!sameAuthorityAnchor(authorityEnd.VerifiedPrefix, acquired.Grant.AsOf) || authorityEnd.ManifestDigest != acquired.Grant.Manifest {
		t.Fatalf("authority grant end does not close the exact installed prefix/manifest: %+v, %v", authorityEnd, err)
	}
	acquireReplay, err := recoveredClient.AcquireClaim(context.Background(), acquireID, matterOutput.ID, cloneID, worktreeID, dispatchID, operation.Actor("human"))
	if err != nil || acquireReplay.Code != operation.ResultSucceeded || !bytes.Equal(acquireReplay.Receipt, acquired.Receipt) ||
		acquireReplay.Grant == nil || !sameClaimGrantSummary(*acquireReplay.Grant, *acquired.Grant) || !sameAuthorityAnchor(acquireReplay.Installed, acquired.Installed) {
		t.Fatalf("same-ID claim-acquire replay changed its pinned product = %+v, %v; original=%+v", acquireReplay, err, acquired)
	}
	claimContext := &operation.ClaimContext{ID: acquired.Grant.ClaimID, Epoch: "1"}
	commandContext := operation.Context{Repo: m5TestRepo, Clone: cloneID, Worktree: worktreeID}
	wrongClaim := operation.Command{
		ID: "01KZ7XHAQT1S46NYPN1PW1DX4P", AuthorityDomainID: m5TestDomain, ExpectedAuthorityEpoch: 1,
		EnvironmentID: m5TestEnv, EnvironmentSequence: 5, ActedAt: time.Now().UTC().Format(time.RFC3339Nano),
		CorrelationCommandID: "01KZ7XHAQT1S46NYPN1PW1DX4P",
		Request: operation.Request{
			Operation: operation.StepStartV1.Metadata().Operation, Actor: "human",
			Context: commandContext, Claim: &operation.ClaimContext{ID: "01KZ7XHAQT1S46NYPN1PW1DX4Q", Epoch: "1"},
			Input: operation.StepLifecycleInput{StepID: stepOutput.ID},
		},
	}
	if _, err = recoveredClient.ExecuteCommand(context.Background(), wrongClaim); err == nil {
		t.Fatal("wrong claim identity was admitted")
	}
	anchorAfterWrongClaim, anchorErr := fixture.store.CurrentPrefixAnchor(context.Background(), m5TestDomain)
	if anchorErr != nil || !sameAuthorityAnchor(acquired.Installed, wireAnchor(anchorAfterWrongClaim)) {
		t.Fatalf("wrong claim context mutated authority events: anchor=%+v err=%v", anchorAfterWrongClaim, anchorErr)
	}
	stepStart := operation.Command{
		ID: "01KZ7XHAQT1S46NYPN1PW1DX4V", AuthorityDomainID: m5TestDomain, ExpectedAuthorityEpoch: 1,
		EnvironmentID: m5TestEnv, EnvironmentSequence: 5, ActedAt: time.Now().UTC().Format(time.RFC3339Nano),
		CorrelationCommandID: "01KZ7XHAQT1S46NYPN1PW1DX4V",
		Request: operation.Request{
			Operation: operation.StepStartV1.Metadata().Operation, Actor: "human",
			Context: commandContext, Claim: claimContext, Input: operation.StepLifecycleInput{StepID: stepOutput.ID},
		},
	}
	started, err := recoveredClient.ExecuteCommand(context.Background(), stepStart)
	if err != nil || started.Code != operation.ResultSucceeded || started.Output != (operation.StepLifecycleOutput{
		StepID: stepOutput.ID, MatterID: matterOutput.ID, State: "in-progress",
	}) {
		stopRecoveredDaemon()
		t.Fatalf("claim-scoped Step start through authenticated wipd = %+v, %v; daemon=%s", started, err, recoveredOutput.String())
	}
	stepFinish := operation.Command{
		ID: "01KZ7XHAQT1S46NYPN1PW1DX4M", AuthorityDomainID: m5TestDomain, ExpectedAuthorityEpoch: 1,
		EnvironmentID: m5TestEnv, EnvironmentSequence: 6, ActedAt: time.Now().UTC().Format(time.RFC3339Nano),
		CorrelationCommandID: "01KZ7XHAQT1S46NYPN1PW1DX4M",
		Request: operation.Request{
			Operation: operation.StepFinishV1.Metadata().Operation, Actor: "human",
			Context: commandContext, Claim: claimContext, Input: operation.StepLifecycleInput{StepID: stepOutput.ID},
		},
	}
	finishedStep, err := recoveredClient.ExecuteCommand(context.Background(), stepFinish)
	if err != nil || finishedStep.Code != operation.ResultSucceeded || finishedStep.Output != (operation.StepLifecycleOutput{
		StepID: stepOutput.ID, MatterID: matterOutput.ID, State: "done",
	}) {
		t.Fatalf("claim-scoped Step finish through authenticated wipd = %+v, %v", finishedStep, err)
	}
	matterFinish := operation.Command{
		ID: "01KZ7XHAQT1S46NYPN1PW1DX4N", AuthorityDomainID: m5TestDomain, ExpectedAuthorityEpoch: 1,
		EnvironmentID: m5TestEnv, EnvironmentSequence: 7, ActedAt: time.Now().UTC().Format(time.RFC3339Nano),
		CorrelationCommandID: "01KZ7XHAQT1S46NYPN1PW1DX4N",
		Request: operation.Request{
			Operation: operation.MatterFinishV1.Metadata().Operation, Actor: "human",
			Context: commandContext, Claim: claimContext, Input: operation.MatterFinishInput{MatterID: matterOutput.ID},
		},
	}
	finishedMatter, err := recoveredClient.ExecuteCommand(context.Background(), matterFinish)
	if err != nil || finishedMatter.Code != operation.ResultSucceeded || finishedMatter.Output != (operation.MatterFinishOutput{
		MatterID: matterOutput.ID, State: "done", BecameSealed: true,
	}) {
		t.Fatalf("authority-class Matter finish through authenticated wipd = %+v, %v", finishedMatter, err)
	}
	for label, command := range map[string]operation.Command{"Step start": stepStart, "Step finish": stepFinish, "Matter finish": matterFinish} {
		status, queryErr := fixture.store.QueryCommand(context.Background(), m5TestDomain, command.ID, m5CommandHash(t, command), 1,
			fixture.peer, m5TestEnv, time.Now().UTC())
		if queryErr != nil || status.Pending || len(status.Receipt) == 0 {
			t.Fatalf("%s durable authority receipt = %+v, %v", label, status, queryErr)
		}
	}
	matterFinishReplay, err := recoveredClient.ExecuteCommand(context.Background(), matterFinish)
	if err != nil || matterFinishReplay.Code != operation.ResultSucceeded || matterFinishReplay.Output != finishedMatter.Output {
		t.Fatalf("same-ID Matter finish replay changed its typed result = %+v, %v", matterFinishReplay, err)
	}
	_ = recoveredClient.Close()
	stopRecoveredDaemon()
	anchor, err = fixture.store.CurrentPrefixAnchor(context.Background(), m5TestDomain)
	if err != nil || anchor.EventCount != 11 {
		t.Fatalf("authority event range after lifecycle completion = %+v, %v; want eleven events", anchor, err)
	}
	journal, err := wipdjournal.Open(filepath.Join(profileRoot, "environment-journal"), wipdjournal.Identity{
		RepoID: m5TestRepo, DomainID: m5TestDomain, AuthorityEpoch: 1, EnvironmentID: m5TestEnv,
		OwnerRootSPKI: fixture.profile.OwnerRootSPKI(),
	})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = journal.Close() }()
	snapshot, err := journal.InstallSnapshot(context.Background())
	if err != nil || snapshot.Anchor.EventCount != 11 || len(snapshot.Receipts) != 5 {
		t.Fatalf("durable Environment authority fold/replay state = %+v, %v", snapshot, err)
	}
	releaseAttempt, err := journal.BirthReleaseAttempt(releaseID)
	if err != nil || !releaseAttempt.Returned || releaseAttempt.ResultCode != operation.ResultSucceeded ||
		!bytes.Equal(releaseAttempt.Receipt, release.Receipt) || releaseAttempt.Barrier.Journal != matterOutput.ID ||
		releaseAttempt.Barrier.Count != 2 || releaseAttempt.Barrier.Receipts != 2 {
		t.Fatalf("durable birth-release receipt/barrier = %+v, %v", releaseAttempt, err)
	}
	entries, err := journal.Entries()
	if err != nil || len(entries) != 5 || entries[0].State != wipdjournal.StateReturned || entries[1].State != wipdjournal.StateReturned ||
		entries[2].State != wipdjournal.StateReturned || entries[3].State != wipdjournal.StateReturned ||
		entries[4].Delivery != operation.DeliveryAuthority || entries[4].JournalPosition != 0 || entries[4].State != wipdjournal.StateAttemptPrepared {
		t.Fatalf("durable journal dispositions = %+v, %v", entries, err)
	}
	finishStatus, finishStatusErr := fixture.store.QueryCommand(context.Background(), m5TestDomain, matterFinish.ID,
		m5CommandHash(t, matterFinish), 1, fixture.peer, m5TestEnv, time.Now().UTC())
	if outcome, ok := snapshot.Receipts[matterFinish.ID]; finishStatusErr != nil || !ok || outcome.ResultCode != operation.ResultSucceeded ||
		!bytes.Equal(outcome.CanonicalReceipt, finishStatus.Receipt) {
		t.Fatalf("authority-class Matter-finish outcome was not durably retained for replay: %+v, present=%v", outcome, ok)
	}
	acquireAttempt, err := journal.ClaimAcquireAttempt(acquireID)
	if err != nil || !acquireAttempt.Returned || acquireAttempt.ResultCode != operation.ResultSucceeded ||
		!bytes.Equal(acquireAttempt.Receipt, acquired.Receipt) || acquireAttempt.GrantID != acquired.Grant.GrantID {
		t.Fatalf("durable acquisition receipt/grant identity = %+v, %v", acquireAttempt, err)
	}
	installedGrant, err := journal.InstalledClaimGrantByCommand(acquireID)
	if err != nil || installedGrant.GrantID != acquired.Grant.GrantID || installedGrant.ClaimID != acquired.Grant.ClaimID ||
		installedGrant.ClaimEpoch != acquired.Grant.ClaimEpoch || installedGrant.DispatchID != dispatchID ||
		!sameAuthorityAnchor(installedGrant.AsOf, acquired.Grant.AsOf) {
		t.Fatalf("durable installed grant identity/as-of = %+v, %v", installedGrant, err)
	}
	records, err := journal.EventRecords(context.Background())
	if err != nil || len(records) != 11 {
		t.Fatalf("durable installed event records = %d, %v", len(records), err)
	}
	if records[0].EventID == records[1].EventID {
		t.Fatalf("Matter and Step folded to the same authority event ID: %s", records[0].EventID)
	}
	var releaseEvent map[string]any
	if err = wipdwire.DecodeCanonical(records[2].Record, &releaseEvent,
		"schema", "event_id", "domain_id", "command_id", "request_hash", "environment", "acted_at", "occurred_at", "kind", "subject_id", "repo_id", "payload"); err != nil ||
		releaseEvent["kind"] != "claim.released" || releaseEvent["subject_id"] != matterOutput.ID {
		t.Fatalf("reopened release tail event = %#v, %v", releaseEvent, err)
	}
	if err = journal.Close(); err != nil {
		t.Fatal(err)
	}
	assertClient, stopAssertDaemon, assertOutput := startWipdForBirthReleaseRecovery(t, binary, profileRoot)
	defer stopAssertDaemon()
	defer func() { _ = assertClient.Close() }()
	replayAfterReopen, err := assertClient.AcquireClaim(context.Background(), acquireID, matterOutput.ID, cloneID, worktreeID, dispatchID, operation.Actor("human"))
	if err != nil || replayAfterReopen.Code != operation.ResultSucceeded || !bytes.Equal(replayAfterReopen.Receipt, acquired.Receipt) ||
		replayAfterReopen.Grant == nil || !sameClaimGrantSummary(*replayAfterReopen.Grant, *acquired.Grant) {
		t.Fatalf("same-ID acquisition replay after daemon/journal reopen = %+v, %v", replayAfterReopen, err)
	}
	if sameAuthorityAnchor(replayAfterReopen.Installed, replayAfterReopen.Grant.AsOf) ||
		!sameAuthorityAnchor(replayAfterReopen.Installed, wireAnchor(anchor)) {
		t.Fatalf("reopened acquisition replay did not preserve its older grant as-of while reporting the newer installed prefix: installed=%+v grant-as-of=%+v current=%+v",
			replayAfterReopen.Installed, replayAfterReopen.Grant.AsOf, anchor)
	}
	finishAfterReopen, err := assertClient.ExecuteCommand(context.Background(), matterFinish)
	if err != nil || finishAfterReopen.Code != operation.ResultSucceeded || finishAfterReopen.Output != finishedMatter.Output {
		t.Fatalf("same-ID lifecycle receipt replay after daemon/journal reopen = %+v, %v", finishAfterReopen, err)
	}
	postReopenStart := operation.Command{
		ID: "01KZ7XHAQT1S46NYPN1PW1DX4R", AuthorityDomainID: m5TestDomain, ExpectedAuthorityEpoch: 1,
		EnvironmentID: m5TestEnv, EnvironmentSequence: 8, ActedAt: time.Now().UTC().Format(time.RFC3339Nano),
		CorrelationCommandID: "01KZ7XHAQT1S46NYPN1PW1DX4R",
		Request: operation.Request{
			Operation: operation.StepStartV1.Metadata().Operation, Actor: "human",
			Context: commandContext, Claim: claimContext, Input: operation.StepLifecycleInput{StepID: stepOutput.ID},
		},
	}
	postReopenResult, err := assertClient.ExecuteCommand(context.Background(), postReopenStart)
	if err != nil || postReopenResult.Code != operation.ResultRefused || postReopenResult.Output != nil {
		t.Fatalf("subsequent authenticated post-reopen lifecycle behavior = %+v, %v; daemon=%s", postReopenResult, err, assertOutput.String())
	}
	postLifecycleAnchor, err := fixture.store.CurrentPrefixAnchor(context.Background(), m5TestDomain)
	if err != nil || postLifecycleAnchor.EventCount != 11 {
		t.Fatalf("subsequent authenticated pull changed lifecycle event tail = %+v, %v", postLifecycleAnchor, err)
	}
	if err = assertClient.Close(); err != nil {
		t.Fatal(err)
	}
	stopAssertDaemon()
	legacyConfig := map[string]any{
		"schema": "wipd.connected-authority-profile/1", "origin": config.Origin, "domain_id": config.DomainID,
		"authority_epoch": config.Epoch, "repo_id": config.RepoID, "owner_root_spki": config.OwnerRootSPKI,
		"authority_spki_pin": config.AuthoritySPKIPin, "authority_certificate_der": config.AuthorityCertificateDER,
		"client_state_directory": config.ClientStateDirectory,
	}
	legacyConfigBytes, err := json.Marshal(legacyConfig)
	if err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(filepath.Join(profileRoot, "connected-authority.json"), legacyConfigBytes, 0o600); err != nil {
		t.Fatalf("restore legacy v1 connected profile: %v", err)
	}
	legacyClient, stopLegacyDaemon, legacyOutput := startWipdForBirthReleaseRecovery(t, binary, profileRoot)
	defer stopLegacyDaemon()
	defer func() { _ = legacyClient.Close() }()
	legacyReplay, err := legacyClient.ExecuteCommand(context.Background(), step)
	if err != nil || legacyReplay.Code != operation.ResultSucceeded || legacyReplay.Output != stepResult.Output {
		t.Fatalf("existing connected Step replay through reopened v1 profile = %+v, %v; daemon=%s", legacyReplay, err, legacyOutput.String())
	}
	legacyReleaseReplay, err := legacyClient.ReleaseBirthClaim(context.Background(), matterOutput.ID, releaseID, operation.Actor("human"))
	if err != nil || legacyReleaseReplay.Code != operation.ResultSucceeded || !bytes.Equal(legacyReleaseReplay.Receipt, release.Receipt) {
		t.Fatalf("existing birth-release replay through reopened v1 profile = %+v, %v; daemon=%s", legacyReleaseReplay, err, legacyOutput.String())
	}
	if _, err = legacyClient.AcquireClaim(context.Background(), acquireID, matterOutput.ID, cloneID, worktreeID, dispatchID, operation.Actor("human")); err == nil {
		t.Fatal("legacy v1 profile accepted claim acquisition without negotiated trust")
	} else {
		var exchangeErr *wipd.ExchangeError
		if !errors.As(err, &exchangeErr) || exchangeErr.Code != "protocol.unsupported-extension" {
			t.Fatalf("legacy acquisition refusal = %v; want feature-not-negotiated", err)
		}
	}
}

func sameClaimGrantSummary(left, right wipdjournal.ClaimGrantSummary) bool {
	return left.GrantID == right.GrantID && left.CommandID == right.CommandID && left.RequestHash == right.RequestHash &&
		left.ClaimID == right.ClaimID && left.ClaimEpoch == right.ClaimEpoch && left.MatterID == right.MatterID &&
		left.BatchID == right.BatchID && left.DispatchID == right.DispatchID && left.Manifest == right.Manifest &&
		sameAuthorityAnchor(left.AsOf, right.AsOf)
}

func startWipdForBirthReleaseRecovery(t *testing.T, binary, profileRoot string) (*wipd.Client, func(), *bytes.Buffer) {
	t.Helper()
	processCtx, cancelProcess := context.WithCancel(context.Background())
	process := exec.CommandContext(processCtx, binary, "--profile-root", profileRoot)
	var output bytes.Buffer
	process.Stdout, process.Stderr = &output, &output
	if err := process.Start(); err != nil {
		cancelProcess()
		t.Fatalf("restart wipd for birth-release recovery: %v", err)
	}
	done := make(chan struct{})
	var processErr error
	go func() {
		processErr = process.Wait()
		close(done)
	}()
	stopped := false
	stop := func() {
		if stopped {
			return
		}
		stopped = true
		_ = process.Process.Signal(syscall.SIGTERM)
		select {
		case <-done:
		case <-time.After(5 * time.Second):
			cancelProcess()
			<-done
		}
		cancelProcess()
	}
	t.Cleanup(stop)
	deadline := time.Now().Add(12 * time.Second)
	for time.Now().Before(deadline) {
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		client, err := wipd.Connect(ctx, profileRoot)
		cancel()
		if err == nil {
			return client, stop, &output
		}
		select {
		case <-done:
			t.Fatalf("restarted wipd exited before recovery: %v: %s", processErr, output.String())
		default:
		}
		time.Sleep(25 * time.Millisecond)
	}
	t.Fatalf("connect to restarted wipd for birth-release recovery: %s", output.String())
	return nil, stop, &output
}

func emptyPrefixDigest() string {
	sum := sha256.Sum256([]byte("wipd/event-prefix/v1\x00"))
	return "sha256:" + hex.EncodeToString(sum[:])
}

func emptyManifestDigest() string {
	sum := sha256.Sum256([]byte("wipd/blob-manifest/v1\x00"))
	return "sha256:" + hex.EncodeToString(sum[:])
}
