package wipdauthority

import (
	"bytes"
	"context"
	"crypto/sha256"
	"crypto/x509"
	"encoding/hex"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/procrastivity/wip/internal/authoritystore"
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
		ClientStateDirectory    string `json:"client_state_directory"`
	}{
		Schema: "wipd.connected-authority-profile/1", Origin: fixture.profile.Origin(),
		DomainID: m5TestDomain, Epoch: 1, RepoID: m5TestRepo,
		OwnerRootSPKI: fixture.profile.OwnerRootSPKI(), AuthoritySPKIPin: "sha256:" + hex.EncodeToString(authoritySPKI[:]),
		AuthorityCertificateDER: fixture.serverCert.Certificate[1], ClientStateDirectory: clientStateRoot,
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
	recoveredClient, stopRecoveredDaemon := startWipdForBirthReleaseRecovery(t, binary, profileRoot)
	recoveredRelease, err := recoveredClient.ReleaseBirthClaim(context.Background(), matterOutput.ID, releaseID, operation.Actor("human"))
	if err != nil || recoveredRelease.Code != operation.ResultSucceeded || !bytes.Equal(recoveredRelease.Receipt, release.Receipt) {
		t.Fatalf("same-ID birth-release recovery through restarted wipd = %+v, %v", recoveredRelease, err)
	}
	_ = recoveredClient.Close()
	stopRecoveredDaemon()
	anchor, err = fixture.store.CurrentPrefixAnchor(context.Background(), m5TestDomain)
	if err != nil || anchor.EventCount != 3 {
		t.Fatalf("authority event range after release recovery = %+v, %v; want three events", anchor, err)
	}
	journal, err := wipdjournal.Open(filepath.Join(profileRoot, "environment-journal"), wipdjournal.Identity{
		RepoID: m5TestRepo, DomainID: m5TestDomain, AuthorityEpoch: 1, EnvironmentID: m5TestEnv,
	})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = journal.Close() }()
	snapshot, err := journal.InstallSnapshot(context.Background())
	if err != nil || snapshot.Anchor.EventCount != 3 || len(snapshot.Receipts) != 2 {
		t.Fatalf("durable Environment authority fold/replay state = %+v, %v", snapshot, err)
	}
	releaseAttempt, err := journal.BirthReleaseAttempt(releaseID)
	if err != nil || !releaseAttempt.Returned || releaseAttempt.ResultCode != operation.ResultSucceeded ||
		!bytes.Equal(releaseAttempt.Receipt, release.Receipt) || releaseAttempt.Barrier.Journal != matterOutput.ID ||
		releaseAttempt.Barrier.Count != 2 || releaseAttempt.Barrier.Receipts != 2 {
		t.Fatalf("durable birth-release receipt/barrier = %+v, %v", releaseAttempt, err)
	}
	entries, err := journal.Entries()
	if err != nil || len(entries) != 2 || entries[0].State != wipdjournal.StateReturned || entries[1].State != wipdjournal.StateReturned {
		t.Fatalf("durable journal dispositions = %+v, %v", entries, err)
	}
	records, err := journal.EventRecords(context.Background())
	if err != nil || len(records) != 3 {
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
	assertBirthMatterCanBeAcquiredAgain(t, fixture, matterOutput.ID)
}

func assertBirthMatterCanBeAcquiredAgain(t *testing.T, fixture *m5CommandFixture, matterID string) {
	t.Helper()
	ctx := context.Background()
	anchor, err := fixture.store.CurrentPrefixAnchor(ctx, m5TestDomain)
	if err != nil || anchor.EventCount != 3 {
		t.Fatalf("birth-release prefix before later acquisition = %+v, %v", anchor, err)
	}
	commandID := "01KZ7XHAQT1S46NYPN1PW1DX4D"
	cloneID := "01KZ7XHAQT1S46NYPN1PW1DX4E"
	worktreeID := "01KZ7XHAQT1S46NYPN1PW1DX4F"
	dispatchID := "01KZ7XHAQT1S46NYPN1PW1DX4J"
	canonical, err := wipdwire.EncodeCanonical(map[string]any{
		"schema": "wipd.command/1", "command_id": commandID,
		"authority":   map[string]any{"domain_id": m5TestDomain, "expected_epoch": uint64(1)},
		"environment": map[string]any{"id": m5TestEnv, "sequence": uint64(4)},
		"acted_at":    fixture.now.Format(time.RFC3339Nano), "actor": "human",
		"causation_command_id": nil, "correlation_command_id": commandID,
		"operation": map[string]any{"name": "claim.acquire", "version": uint64(1)},
		"context":   map[string]any{"repo_id": m5TestRepo, "clone_id": cloneID, "worktree_id": worktreeID},
		"claim":     nil,
		"input": map[string]any{"matter_id": matterID, "worktree_id": worktreeID,
			"dispatch_mode": "anonymous-matter", "requested_dispatch_id": dispatchID},
		"blobs": []any{},
	})
	if err != nil {
		t.Fatal(err)
	}
	hash := sha256.Sum256(append([]byte("wipd/request-hash/v1\x00"), canonical...))
	requestHash := "sha256:" + hex.EncodeToString(hash[:])
	pending, err := fixture.store.SubmitClaimAcquire(ctx, canonical, requestHash, anchor, fixture.peer, fixture.now)
	if err != nil || !pending.Pending || pending.Owner == nil {
		t.Fatalf("claim.acquire after birth-claim release = %+v, %v", pending, err)
	}
	allocation := authoritystore.AcquireAllocation{
		ClaimID: "01KZ7XHAQT1S46NYPN1PW1DX4K", BatchID: "01KZ7XHAQT1S46NYPN1PW1DX4M",
		GrantID: "01KZ7XHAQT1S46NYPN1PW1DX4N", SnapshotID: "01KZ7XHAQT1S46NYPN1PW1DX4P",
		JournalID: "01KZ7XHAQT1S46NYPN1PW1DX4R", Installed: anchor,
		EventIDs: []string{"7" + strings.Repeat("Z", 24) + "X", "7" + strings.Repeat("Z", 24) + "Y", "7" + strings.Repeat("Z", 24) + "Z"},
	}
	completed, grant, err := fixture.store.CompleteClaimAcquire(ctx, pending.Owner, allocation, fixture.now, fixture.config.SignArtifact)
	if err != nil || completed.Pending || len(completed.Receipt) == 0 || grant.ID != allocation.GrantID {
		t.Fatalf("complete later acquisition after birth release = %+v grant=%+v err=%v", completed, grant, err)
	}
	receipt, err := wipdwire.DecodeCanonicalMap(completed.Receipt,
		"schema", "domain_id", "authority_epoch", "identity_schema", "command_id", "request_hash", "operation", "environment", "result", "accepted_events")
	result, ok := receipt["result"].(map[string]any)
	if err != nil || !ok || result["code"] != "result.succeeded" {
		t.Fatalf("later claim acquisition terminal receipt = %#v, %v", receipt, err)
	}
}

func startWipdForBirthReleaseRecovery(t *testing.T, binary, profileRoot string) (*wipd.Client, func()) {
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
			return client, stop
		}
		select {
		case <-done:
			t.Fatalf("restarted wipd exited before recovery: %v: %s", processErr, output.String())
		default:
		}
		time.Sleep(25 * time.Millisecond)
	}
	t.Fatalf("connect to restarted wipd for birth-release recovery: %s", output.String())
	return nil, stop
}

func emptyPrefixDigest() string {
	sum := sha256.Sum256([]byte("wipd/event-prefix/v1\x00"))
	return "sha256:" + hex.EncodeToString(sum[:])
}

func emptyManifestDigest() string {
	sum := sha256.Sum256([]byte("wipd/blob-manifest/v1\x00"))
	return "sha256:" + hex.EncodeToString(sum[:])
}
