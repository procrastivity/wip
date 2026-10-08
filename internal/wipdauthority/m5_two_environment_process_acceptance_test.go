package wipdauthority

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
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"math/big"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/procrastivity/wip/internal/authoritystore"
	"github.com/procrastivity/wip/internal/operation"
	"github.com/procrastivity/wip/internal/wipd"
	"github.com/procrastivity/wip/internal/wipdjournal"
	"github.com/procrastivity/wip/internal/wipdwire"
)

// This acceptance test keeps the authority in one store/server while running
// two real wipd processes with separately provisioned identities, client-state
// directories, profiles, and Environment journals.
func TestM5TwoEnvironmentClaimCloseAndFinalPullSpine(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("wipd's authenticated local socket process is Linux-only")
	}
	ctx := context.Background()
	crossContainerBundle := os.Getenv("WIP_M5_CROSS_CONTAINER_BUNDLE")
	crossContainer := crossContainerBundle != ""
	var fixture *m5CommandFixture
	var a, b m5ProcessEnvironment
	var trace *m5TwoEnvironmentTrace
	root, err := os.MkdirTemp("", "w19-")
	if err != nil {
		t.Fatal(err)
	}
	if err = os.Chmod(root, 0o700); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(root) })
	if crossContainer {
		bundle, bundleErr := readM5CrossContainerBundle(crossContainerBundle)
		if bundleErr != nil {
			t.Fatal(bundleErr)
		}
		fixture, err = m5FixtureFromCrossContainerBundle(bundle)
		if err != nil {
			t.Fatal(err)
		}
		a, err = installM5CrossContainerEnvironment(t, fixture, root, "environment-a", bundle.EnvironmentA)
		if err != nil {
			t.Fatal(err)
		}
		b, err = installM5CrossContainerEnvironment(t, fixture, root, "environment-b", bundle.EnvironmentB)
		if err != nil {
			t.Fatal(err)
		}
		trace = &m5TwoEnvironmentTrace{}
	} else {
		fixture = newM5CommandFixture(t)
		registry, registryErr := NewM5BirthRegistry()
		if registryErr != nil {
			t.Fatal(registryErr)
		}
		config := fixture.config
		config.Registry = registry
		fixture.config = config
		fixture.server = fixture.serverForStore(t, fixture.store)
		a, err = prepareM5ProcessEnvironment(t, fixture, root, "environment-a", m5TestEnv,
			fixture.environment, fixture.clientCert.Certificate[0], fixture.clientCert.Certificate[1])
		if err != nil {
			t.Fatal(err)
		}
		owner := m5TestKey("owner-root")
		ownerID := fixture.profile.OwnerRootSPKI()
		privateB := m5TestKey("step19-environment-b")
		csrB, csrErr := x509.CreateCertificateRequest(rand.Reader, &x509.CertificateRequest{
			Subject: pkix.Name{CommonName: "M5 Step 19 Environment B"},
		}, privateB)
		if csrErr != nil {
			t.Fatal(csrErr)
		}
		grantB := m5TestOwnerArtifact(t, owner, ownerID, m5TestDomain, "enrollment-grant", "wipd.enrollment-grant/1", map[string]any{
			"schema": "wipd.enrollment-grant/1", "grant_id": m5TwoEnvironmentID(80), "domain_id": m5TestDomain,
			"authority_epoch": uint64(1), "owner_key_id": ownerID, "scope": "environment-enroll",
			"requested_spki_digest": m5TestPublicDigest(privateB.Public().(ed25519.PublicKey)),
			"prior_environment_id":  nil, "nonce": bytes.Repeat([]byte{0x62}, 16),
			"issued_at":  fixture.now.Add(-time.Minute).Format(time.RFC3339Nano),
			"expires_at": fixture.now.Add(5 * time.Minute).Format(time.RFC3339Nano),
		}, fixture.now)
		environmentBID, idErr := stableEnvironmentID(m5TestDomain, grantB)
		if idErr != nil {
			t.Fatal(idErr)
		}
		leafB, certErr := m5EnvironmentLeafCertificate(privateB, m5TestKey("environment-ca"), fixture.config.EnvironmentCACertificateDER,
			m5TestDomain, environmentBID, ownerID, 102, fixture.now)
		if certErr != nil {
			t.Fatal(certErr)
		}
		if _, err = fixture.store.IssueEnvironmentCertificate(ctx, m5TestDomain, environmentBID, grantB, csrB,
			[][]byte{leafB, fixture.config.EnvironmentCACertificateDER}, fixture.now); err != nil {
			t.Fatalf("issue second authenticated fixture Environment certificate: %v", err)
		}
		b, err = prepareM5ProcessEnvironment(t, fixture, root, "environment-b", environmentBID,
			privateB, leafB, fixture.config.EnvironmentCACertificateDER)
		if err != nil {
			t.Fatal(err)
		}
		trace = &m5TwoEnvironmentTrace{}
		fixture.server.http.Handler = trace.wrap(fixture.server.http.Handler)
		authorityCtx, stopAuthority := context.WithCancel(ctx)
		authorityDone := make(chan error, 1)
		go func() { authorityDone <- fixture.server.Serve(authorityCtx, fixture.listener) }()
		t.Cleanup(func() {
			stopAuthority()
			select {
			case serveErr := <-authorityDone:
				if serveErr != nil {
					t.Errorf("M5 authority server stopped with error: %v", serveErr)
				}
			case <-time.After(3 * time.Second):
				t.Error("M5 authority server did not stop")
			}
		})
	}
	defer clear(a.state.PrivateKeyPKCS8)
	defer clear(b.state.PrivateKeyPKCS8)
	if a.state.EnvironmentID == b.state.EnvironmentID {
		t.Fatal("independent enrollment grants produced the same Environment identity")
	}

	contentBytes := []byte("one verified M5 acceptance content segment\n")
	stagedA := stageM5ProcessBlob(t, a, contentBytes)
	stagedB := stageM5ProcessBlob(t, b, contentBytes)
	if stagedA.Digest != stagedB.Digest || stagedA.Size != stagedB.Size {
		t.Fatalf("independent Environment staging changed identical content identity: A=%+v B=%+v", stagedA, stagedB)
	}

	binary := m5WipdBinary(t)
	clientA, stopA, outputA := startWipdForBirthReleaseRecovery(t, binary, a.profileRoot)
	clientB, stopB, outputB := startWipdForBirthReleaseRecovery(t, binary, b.profileRoot)
	defer func() {
		_ = clientA.Close()
		_ = clientB.Close()
		stopA()
		stopB()
	}()

	var matterA, matterB operation.MatterCreateOutput
	var stepA, stepB operation.StepCreateOutput
	var claimA, claimB *wipdjournal.ClaimGrantSummary
	var commandsA, commandsB map[string]operation.Command
	commandsA, commandsB = make(map[string]operation.Command), make(map[string]operation.Command)
	command := func(environment m5ProcessClientState, id string, sequence uint64, request operation.Request) operation.Command {
		return operation.Command{
			ID: id, AuthorityDomainID: environment.DomainID, ExpectedAuthorityEpoch: environment.Epoch,
			EnvironmentID: environment.EnvironmentID, EnvironmentSequence: sequence,
			ActedAt: time.Now().UTC().Format(time.RFC3339Nano), CorrelationCommandID: id,
			Request: request,
		}
	}
	runScenario := func(name string, scenario func(*testing.T)) {
		if !t.Run(name, scenario) {
			t.FailNow()
		}
	}

	runScenario("01-fresh-disposable-independent-environments", func(t *testing.T) {
		for name, environment := range map[string]m5ProcessEnvironment{"A": a, "B": b} {
			if environment.state.DomainID != m5TestDomain || environment.state.Epoch != 1 || environment.state.RepoID != m5TestRepo ||
				environment.state.Prefix.EventCount != 0 || environment.state.Prefix.Digest != emptyPrefixDigest() {
				t.Fatalf("Environment %s did not start at its independently provisioned empty prefix: domain=%s epoch=%d repo=%s prefix=%+v",
					name, environment.state.DomainID, environment.state.Epoch, environment.state.RepoID, environment.state.Prefix)
			}
		}
		if a.profileRoot == b.profileRoot || a.clientStateRoot == b.clientStateRoot ||
			filepath.Join(a.profileRoot, "environment-journal") == filepath.Join(b.profileRoot, "environment-journal") {
			t.Fatal("Environment profiles, client states, or journals share a local durable root")
		}
	})

	runScenario("02-birth-in-both-environments", func(t *testing.T) {
		matterCommandA := command(a.state, m5TwoEnvironmentID(1), 1, operation.Request{
			Operation: operation.MatterCreateV1.Metadata().Operation, Actor: "human", Context: operation.Context{Repo: m5TestRepo},
			Input: operation.MatterCreateInput{Title: "Environment A Matter", Locator: "step19-a"},
		})
		matterCommandB := command(b.state, m5TwoEnvironmentID(2), 1, operation.Request{
			Operation: operation.MatterCreateV1.Metadata().Operation, Actor: "human", Context: operation.Context{Repo: m5TestRepo},
			Input: operation.MatterCreateInput{Title: "Environment B Matter", Locator: "step19-b"},
		})
		resultA, submitErr := clientA.ExecuteCommand(ctx, matterCommandA)
		var ok bool
		matterA, ok = resultA.Output.(operation.MatterCreateOutput)
		if submitErr != nil || resultA.Code != operation.ResultSucceeded || !ok || matterA.ID == "" {
			t.Fatalf("Environment A Matter birth = %+v (%T), %v; daemon=%s", resultA, resultA.Output, submitErr, outputA.String())
		}
		commandsA["matter"] = matterCommandA
		resultB, submitErr := clientB.ExecuteCommand(ctx, matterCommandB)
		matterB, ok = resultB.Output.(operation.MatterCreateOutput)
		if submitErr != nil || resultB.Code != operation.ResultSucceeded || !ok || matterB.ID == "" || matterA.ID == matterB.ID {
			t.Fatalf("Environment B Matter birth = %+v (%T), %v; daemon=%s", resultB, resultB.Output, submitErr, outputB.String())
		}
		commandsB["matter"] = matterCommandB
		stepCommandA := command(a.state, m5TwoEnvironmentID(3), 2, operation.Request{
			Operation: operation.StepCreateV1.Metadata().Operation, Actor: "human", Context: operation.Context{Repo: m5TestRepo},
			Claim: &operation.ClaimContext{ID: matterA.ID, Epoch: "1"},
			Input: operation.StepCreateInput{ParentID: matterA.ID, Title: "Environment A Step"},
		})
		stepCommandA.CausationCommandID, stepCommandA.CorrelationCommandID = matterCommandA.ID, matterCommandA.ID
		resultA, submitErr = clientA.ExecuteCommand(ctx, stepCommandA)
		stepA, ok = resultA.Output.(operation.StepCreateOutput)
		if submitErr != nil || resultA.Code != operation.ResultSucceeded || !ok || stepA.ID == "" || stepA.MatterID != matterA.ID {
			t.Fatalf("Environment A Step birth = %+v (%T), %v; daemon=%s", resultA, resultA.Output, submitErr, outputA.String())
		}
		commandsA["step"] = stepCommandA
		stepCommandB := command(b.state, m5TwoEnvironmentID(4), 2, operation.Request{
			Operation: operation.StepCreateV1.Metadata().Operation, Actor: "human", Context: operation.Context{Repo: m5TestRepo},
			Claim: &operation.ClaimContext{ID: matterB.ID, Epoch: "1"},
			Input: operation.StepCreateInput{ParentID: matterB.ID, Title: "Environment B Step"},
		})
		stepCommandB.CausationCommandID, stepCommandB.CorrelationCommandID = matterCommandB.ID, matterCommandB.ID
		resultB, submitErr = clientB.ExecuteCommand(ctx, stepCommandB)
		stepB, ok = resultB.Output.(operation.StepCreateOutput)
		if submitErr != nil || resultB.Code != operation.ResultSucceeded || !ok || stepB.ID == "" || stepB.MatterID != matterB.ID {
			t.Fatalf("Environment B Step birth = %+v (%T), %v; daemon=%s", resultB, resultB.Output, submitErr, outputB.String())
		}
		commandsB["step"] = stepCommandB
	})

	runScenario("03-release-both-provisional-birth-claims", func(t *testing.T) {
		for _, item := range []struct {
			client *wipd.Client
			matter operation.MatterCreateOutput
			id     string
			name   string
		}{{clientA, matterA, m5TwoEnvironmentID(5), "A"}, {clientB, matterB, m5TwoEnvironmentID(6), "B"}} {
			released, releaseErr := item.client.ReleaseBirthClaim(ctx, item.matter.ID, item.id, operation.Actor("human"))
			if releaseErr != nil || released.Code != operation.ResultSucceeded || released.CommandID != item.id || len(released.Receipt) == 0 {
				t.Fatalf("Environment %s provisional birth release = %+v, %v", item.name, released, releaseErr)
			}
			replay, replayErr := item.client.ReleaseBirthClaim(ctx, item.matter.ID, item.id, operation.Actor("human"))
			if replayErr != nil || replay.Code != operation.ResultSucceeded || !bytes.Equal(replay.Receipt, released.Receipt) {
				t.Fatalf("Environment %s birth-release exact replay = %+v, %v", item.name, replay, replayErr)
			}
		}
	})

	runScenario("04-acquire-disjoint-matter-claims", func(t *testing.T) {
		acquiredA, acquireErr := clientA.AcquireClaim(ctx, m5TwoEnvironmentID(7), matterA.ID,
			m5TwoEnvironmentID(8), m5TwoEnvironmentID(9), m5TwoEnvironmentID(10), operation.Actor("human"))
		if acquireErr != nil || acquiredA.Code != operation.ResultSucceeded || acquiredA.Grant == nil || acquiredA.Grant.MatterID != matterA.ID {
			t.Fatalf("Environment A acquired claim = %+v, %v", acquiredA, acquireErr)
		}
		claimA = acquiredA.Grant
		acquiredB, acquireErr := clientB.AcquireClaim(ctx, m5TwoEnvironmentID(11), matterB.ID,
			m5TwoEnvironmentID(12), m5TwoEnvironmentID(13), m5TwoEnvironmentID(14), operation.Actor("human"))
		if acquireErr != nil || acquiredB.Code != operation.ResultSucceeded || acquiredB.Grant == nil || acquiredB.Grant.MatterID != matterB.ID ||
			acquiredA.Grant.ClaimID == acquiredB.Grant.ClaimID {
			t.Fatalf("Environment B acquired claim = %+v, %v", acquiredB, acquireErr)
		}
		claimB = acquiredB.Grant
	})

	runScenario("05-claim-scoped-step-lifecycle-in-both-environments", func(t *testing.T) {
		for _, item := range []struct {
			client      *wipd.Client
			environment m5ProcessClientState
			step        operation.StepCreateOutput
			claim       *wipdjournal.ClaimGrantSummary
			commands    map[string]operation.Command
			firstID     string
			secondID    string
			cloneID     string
			worktreeID  string
			name        string
		}{
			{clientA, a.state, stepA, claimA, commandsA, m5TwoEnvironmentID(15), m5TwoEnvironmentID(16), m5TwoEnvironmentID(8), m5TwoEnvironmentID(9), "A"},
			{clientB, b.state, stepB, claimB, commandsB, m5TwoEnvironmentID(17), m5TwoEnvironmentID(18), m5TwoEnvironmentID(12), m5TwoEnvironmentID(13), "B"},
		} {
			claim := &operation.ClaimContext{ID: item.claim.ClaimID, Epoch: fmt.Sprint(item.claim.ClaimEpoch)}
			commandContext := operation.Context{Repo: m5TestRepo, Clone: item.cloneID, Worktree: item.worktreeID}
			start := command(item.environment, item.firstID, 5, operation.Request{
				Operation: operation.StepStartV1.Metadata().Operation, Actor: "human", Context: commandContext,
				Claim: claim, Input: operation.StepLifecycleInput{StepID: item.step.ID},
			})
			started, submitErr := item.client.ExecuteCommand(ctx, start)
			if submitErr != nil || started.Code != operation.ResultSucceeded || started.Output != (operation.StepLifecycleOutput{
				StepID: item.step.ID, MatterID: item.step.MatterID, State: "in-progress",
			}) {
				t.Fatalf("Environment %s claim-scoped Step start = %+v, %v", item.name, started, submitErr)
			}
			finish := command(item.environment, item.secondID, 6, operation.Request{
				Operation: operation.StepFinishV1.Metadata().Operation, Actor: "human", Context: commandContext,
				Claim: claim, Input: operation.StepLifecycleInput{StepID: item.step.ID},
			})
			finished, submitErr := item.client.ExecuteCommand(ctx, finish)
			if submitErr != nil || finished.Code != operation.ResultSucceeded || finished.Output != (operation.StepLifecycleOutput{
				StepID: item.step.ID, MatterID: item.step.MatterID, State: "done",
			}) {
				t.Fatalf("Environment %s claim-scoped Step finish = %+v, %v", item.name, finished, submitErr)
			}
			item.commands["start"], item.commands["finish"] = start, finish
		}
	})

	runScenario("06-claim-scoped-content-and-findings-in-both-environments", func(t *testing.T) {
		for _, item := range []struct {
			client      *wipd.Client
			environment m5ProcessClientState
			matter      operation.MatterCreateOutput
			step        operation.StepCreateOutput
			claim       *wipdjournal.ClaimGrantSummary
			staged      wipdjournal.StagedBlob
			commands    map[string]operation.Command
			firstID     string
			secondID    string
			cloneID     string
			worktreeID  string
			name        string
		}{
			{clientA, a.state, matterA, stepA, claimA, stagedA, commandsA, m5TwoEnvironmentID(19), m5TwoEnvironmentID(20), m5TwoEnvironmentID(8), m5TwoEnvironmentID(9), "A"},
			{clientB, b.state, matterB, stepB, claimB, stagedB, commandsB, m5TwoEnvironmentID(21), m5TwoEnvironmentID(22), m5TwoEnvironmentID(12), m5TwoEnvironmentID(13), "B"},
		} {
			claim := &operation.ClaimContext{ID: item.claim.ClaimID, Epoch: fmt.Sprint(item.claim.ClaimEpoch)}
			commandContext := operation.Context{Repo: m5TestRepo, Clone: item.cloneID, Worktree: item.worktreeID}
			content := command(item.environment, item.firstID, 7, operation.Request{
				Operation: operation.ContentWriteOnceV1.Metadata().Operation, Actor: "human", Context: commandContext, Claim: claim,
				Input: operation.ContentWriteInput{SubjectID: item.matter.ID, Kind: "brief"},
				Blobs: []operation.BlobInput{{Name: "content", Digest: item.staged.Digest, Size: item.staged.Size}},
			})
			contentResult, submitErr := item.client.ExecuteCommand(ctx, content)
			contentOutput, contentOK := contentResult.Output.(operation.ContentSegmentOutput)
			if submitErr != nil || contentResult.Code != operation.ResultSucceeded || !contentOK || contentOutput.SubjectID != item.matter.ID ||
				contentOutput.Kind != "brief" || contentOutput.BlobDigest != item.staged.Digest || contentOutput.ByteLength != item.staged.Size {
				t.Fatalf("Environment %s content write = %+v (%T), %v", item.name, contentResult, contentResult.Output, submitErr)
			}
			finding := command(item.environment, item.secondID, 8, operation.Request{
				Operation: operation.FindingAppendV1.Metadata().Operation, Actor: "human", Context: commandContext, Claim: claim,
				Input: operation.FindingAppendInput{SubjectID: item.step.ID},
				Blobs: []operation.BlobInput{{Name: "content", Digest: item.staged.Digest, Size: item.staged.Size}},
			})
			findingResult, submitErr := item.client.ExecuteCommand(ctx, finding)
			findingOutput, findingOK := findingResult.Output.(operation.ContentSegmentOutput)
			if submitErr != nil || findingResult.Code != operation.ResultSucceeded || !findingOK || findingOutput.SubjectID != item.step.ID ||
				findingOutput.Kind != "findings" || findingOutput.BlobDigest != item.staged.Digest || findingOutput.ByteLength != item.staged.Size {
				t.Fatalf("Environment %s finding append = %+v (%T), %v", item.name, findingResult, findingResult.Output, submitErr)
			}
			item.commands["content"], item.commands["finding"] = content, finding
		}
	})

	runScenario("07-matter-finish-seals-after-step-completion", func(t *testing.T) {
		for _, item := range []struct {
			client      *wipd.Client
			environment m5ProcessClientState
			matter      operation.MatterCreateOutput
			claim       *wipdjournal.ClaimGrantSummary
			commands    map[string]operation.Command
			id          string
			cloneID     string
			worktreeID  string
			name        string
		}{
			{clientA, a.state, matterA, claimA, commandsA, m5TwoEnvironmentID(23), m5TwoEnvironmentID(8), m5TwoEnvironmentID(9), "A"},
			{clientB, b.state, matterB, claimB, commandsB, m5TwoEnvironmentID(24), m5TwoEnvironmentID(12), m5TwoEnvironmentID(13), "B"},
		} {
			finish := command(item.environment, item.id, 9, operation.Request{
				Operation: operation.MatterFinishV1.Metadata().Operation, Actor: "human",
				Context: operation.Context{Repo: m5TestRepo, Clone: item.cloneID, Worktree: item.worktreeID},
				Claim:   &operation.ClaimContext{ID: item.claim.ClaimID, Epoch: fmt.Sprint(item.claim.ClaimEpoch)},
				Input:   operation.MatterFinishInput{MatterID: item.matter.ID},
			})
			result, submitErr := item.client.ExecuteCommand(ctx, finish)
			if submitErr != nil || result.Code != operation.ResultSucceeded || result.Output != (operation.MatterFinishOutput{
				MatterID: item.matter.ID, State: "done", BecameSealed: true,
			}) {
				t.Fatalf("Environment %s Matter finish/seal = %+v, %v", item.name, result, submitErr)
			}
			item.commands["matter-finish"] = finish
		}
	})

	runScenario("08-ordered-receipt-barrier-before-environment-b-release", func(t *testing.T) {
		trace.clear()
		closed, closeErr := clientB.ReleaseClaimJournal(ctx, m5TwoEnvironmentID(25), claimB.ClaimID, claimB.ClaimEpoch,
			matterB.ID, claimB.DispatchID, operation.Actor("human"))
		if closeErr != nil || closed.Code != operation.ResultSucceeded || closed.CommandID != m5TwoEnvironmentID(25) || len(closed.Receipt) == 0 {
			t.Fatalf("Environment B acquired-claim close = %+v, %v", closed, closeErr)
		}
		if !crossContainer {
			assertM5ClaimCloseTrace(t, trace.forEnvironment(b.state.EnvironmentID), b.state.EnvironmentID, claimB.ClaimID)
		}
		replay, replayErr := clientB.ReleaseClaimJournal(ctx, closed.CommandID, claimB.ClaimID, claimB.ClaimEpoch,
			matterB.ID, claimB.DispatchID, operation.Actor("human"))
		if replayErr != nil || replay.Code != operation.ResultSucceeded || !bytes.Equal(replay.Receipt, closed.Receipt) {
			t.Fatalf("Environment B acquired-claim close replay = %+v, %v", replay, replayErr)
		}
	})

	runScenario("09-contention-refuses-without-closing-environment-a", func(t *testing.T) {
		var beforeAnchor authoritystore.PrefixAnchor
		var beforeJournal authoritystore.CurrentClaimJournal
		if !crossContainer {
			var anchorErr, journalErr error
			beforeAnchor, anchorErr = fixture.store.CurrentPrefixAnchor(ctx, m5TestDomain)
			if anchorErr != nil {
				t.Fatal(anchorErr)
			}
			beforeJournal, journalErr = fixture.store.GetCurrentClaimJournal(ctx, m5TestDomain, 1, a.state.EnvironmentID,
				claimA.ClaimID, claimA.ClaimEpoch, matterA.ID, claimA.DispatchID)
			if journalErr != nil || beforeJournal.State != "open" {
				t.Fatalf("Environment A journal before contention = %+v, %v", beforeJournal, journalErr)
			}
		}
		refused, acquireErr := clientB.AcquireClaim(ctx, m5TwoEnvironmentID(26), matterA.ID,
			m5TwoEnvironmentID(27), m5TwoEnvironmentID(28), m5TwoEnvironmentID(29), operation.Actor("human"))
		if acquireErr != nil || refused.Code != operation.ResultRefused || refused.Grant != nil || len(refused.Receipt) == 0 {
			t.Fatalf("Environment B acquisition of A's active Matter = %+v, %v; want stable refusal", refused, acquireErr)
		}
		if !crossContainer {
			afterAnchor, anchorErr := fixture.store.CurrentPrefixAnchor(ctx, m5TestDomain)
			if anchorErr != nil || afterAnchor != beforeAnchor {
				t.Fatalf("contention refusal changed authority event prefix: before=%+v after=%+v err=%v", beforeAnchor, afterAnchor, anchorErr)
			}
			afterJournal, journalErr := fixture.store.GetCurrentClaimJournal(ctx, m5TestDomain, 1, a.state.EnvironmentID,
				claimA.ClaimID, claimA.ClaimEpoch, matterA.ID, claimA.DispatchID)
			if journalErr != nil || afterJournal != beforeJournal {
				t.Fatalf("contention refusal changed A's open claim journal: before=%+v after=%+v err=%v", beforeJournal, afterJournal, journalErr)
			}
		}
	})

	runScenario("10-release-a-then-authenticated-pulls-converge-with-authority-provenance", func(t *testing.T) {
		trace.clear()
		closed, closeErr := clientA.ReleaseClaimJournal(ctx, m5TwoEnvironmentID(30), claimA.ClaimID, claimA.ClaimEpoch,
			matterA.ID, claimA.DispatchID, operation.Actor("human"))
		if closeErr != nil || closed.Code != operation.ResultSucceeded || len(closed.Receipt) == 0 {
			t.Fatalf("Environment A acquired-claim close = %+v, %v", closed, closeErr)
		}
		if !crossContainer {
			assertM5ClaimCloseTrace(t, trace.forEnvironment(a.state.EnvironmentID), a.state.EnvironmentID, claimA.ClaimID)
		}
		replay, replayErr := clientA.ReleaseClaimJournal(ctx, closed.CommandID, claimA.ClaimID, claimA.ClaimEpoch,
			matterA.ID, claimA.DispatchID, operation.Actor("human"))
		if replayErr != nil || replay.Code != operation.ResultSucceeded || !bytes.Equal(replay.Receipt, closed.Receipt) {
			t.Fatalf("Environment A acquired-claim close replay = %+v, %v", replay, replayErr)
		}
		// Replaying each exact successful birth Step forces the shared command
		// coordinator to perform its normal authenticated pull after both claims
		// have released, including Environment B after A's final release.
		for _, item := range []struct {
			client  *wipd.Client
			command operation.Command
			name    string
			process *bytes.Buffer
		}{{clientA, commandsA["step"], "A", outputA}, {clientB, commandsB["step"], "B", outputB}} {
			result, replayErr := item.client.ExecuteCommand(ctx, item.command)
			if replayErr != nil || result.Code != operation.ResultSucceeded || result.Output != (operation.StepCreateOutput{
				ID:       map[string]operation.StepCreateOutput{"A": stepA, "B": stepB}[item.name].ID,
				ParentID: map[string]operation.StepCreateOutput{"A": stepA, "B": stepB}[item.name].ParentID,
				MatterID: map[string]operation.StepCreateOutput{"A": stepA, "B": stepB}[item.name].MatterID,
				Locator:  map[string]operation.StepCreateOutput{"A": stepA, "B": stepB}[item.name].Locator,
				Title:    map[string]operation.StepCreateOutput{"A": stepA, "B": stepB}[item.name].Title,
				SortKey:  map[string]operation.StepCreateOutput{"A": stepA, "B": stepB}[item.name].SortKey,
				State:    map[string]operation.StepCreateOutput{"A": stepA, "B": stepB}[item.name].State,
			}) {
				t.Fatalf("Environment %s post-close exact birth-Step pull/replay = %+v, %v; daemon=%s", item.name, result, replayErr, item.process.String())
			}
		}
		_ = clientA.Close()
		_ = clientB.Close()
		stopA()
		stopB()

		journalA, err := wipdjournal.Open(filepath.Join(a.profileRoot, "environment-journal"), wipdjournal.Identity{
			RepoID: m5TestRepo, DomainID: m5TestDomain, AuthorityEpoch: 1, EnvironmentID: a.state.EnvironmentID, OwnerRootSPKI: a.state.OwnerKeyID,
		})
		if err != nil {
			t.Fatalf("reopen Environment A journal after final authenticated pull: %v", err)
		}
		defer func() { _ = journalA.Close() }()
		journalB, err := wipdjournal.Open(filepath.Join(b.profileRoot, "environment-journal"), wipdjournal.Identity{
			RepoID: m5TestRepo, DomainID: m5TestDomain, AuthorityEpoch: 1, EnvironmentID: b.state.EnvironmentID, OwnerRootSPKI: b.state.OwnerKeyID,
		})
		if err != nil {
			t.Fatalf("reopen Environment B journal after final authenticated pull: %v", err)
		}
		defer func() { _ = journalB.Close() }()
		finalA, err := journalA.InstallSnapshot(ctx)
		if err != nil {
			t.Fatalf("read final Environment A installed snapshot: %v", err)
		}
		finalB, err := journalB.InstallSnapshot(ctx)
		if err != nil {
			t.Fatalf("read final Environment B installed snapshot: %v", err)
		}
		eventsA, err := journalA.EventRecords(ctx)
		if err != nil {
			t.Fatal(err)
		}
		eventsB, err := journalB.EventRecords(ctx)
		if err != nil {
			t.Fatal(err)
		}
		overlayA, err := journalA.Overlay(ctx)
		if err != nil {
			t.Fatal(err)
		}
		overlayB, err := journalB.Overlay(ctx)
		if err != nil {
			t.Fatal(err)
		}
		if !sameAuthorityAnchor(finalA.Anchor, finalB.Anchor) || !reflect.DeepEqual(eventsA, eventsB) ||
			!reflect.DeepEqual(overlayA, overlayB) || finalA.ManifestDigest != finalB.ManifestDigest {
			t.Fatalf("final authenticated Environment pulls diverged: A=%+v B=%+v overlayA=%+v overlayB=%+v", finalA, finalB, overlayA, overlayB)
		}

		if uint64(len(eventsA)) != finalA.Anchor.EventCount || len(overlayA) < 8 || finalA.ManifestDigest == emptyManifestDigest() {
			t.Fatalf("final pull omitted complete event/projection/content/manifest state: events=%d overlay=%d manifest=%s",
				len(eventsA), len(overlayA), finalA.ManifestDigest)
		}
		if crossContainer {
			path := os.Getenv("WIP_M5_CROSS_CONTAINER_CLIENT_EVIDENCE")
			if path == "" {
				t.Fatal("cross-container client evidence path was not configured")
			}
			projectionPath := filepath.Join(root, "cross-container-projections.json")
			projectionEvidence := runM5CrossContainerProjectionPulls(t, projectionPath, a, b)
			matterProjectionsA, projectionErr := m5ClientMatterProjections(projectionEvidence.EnvironmentA.MatterProjections)
			if projectionErr != nil {
				t.Fatalf("decode Environment A Matter projections: %v", projectionErr)
			}
			matterProjectionsB, projectionErr := m5ClientMatterProjections(projectionEvidence.EnvironmentB.MatterProjections)
			if projectionErr != nil {
				t.Fatalf("decode Environment B Matter projections: %v", projectionErr)
			}
			if projectionEvidence.Schema != "wipd.m5-cross-container-installed-projections/1" ||
				projectionEvidence.EnvironmentA.EnvironmentID != a.state.EnvironmentID ||
				projectionEvidence.EnvironmentB.EnvironmentID != b.state.EnvironmentID ||
				!sameAuthorityAnchor(projectionEvidence.EnvironmentA.Prefix, finalA.Anchor) ||
				!sameAuthorityAnchor(projectionEvidence.EnvironmentB.Prefix, finalB.Anchor) ||
				projectionEvidence.EnvironmentA.ManifestDigest != finalA.ManifestDigest ||
				projectionEvidence.EnvironmentB.ManifestDigest != finalB.ManifestDigest ||
				!reflect.DeepEqual(projectionEvidence.EnvironmentA.EventRecords, eventsA) ||
				!reflect.DeepEqual(projectionEvidence.EnvironmentB.EventRecords, eventsB) ||
				len(matterProjectionsA) != 2 || !reflect.DeepEqual(matterProjectionsA, matterProjectionsB) ||
				!reflect.DeepEqual(projectionEvidence.EnvironmentA.StepProjections, projectionEvidence.EnvironmentB.StepProjections) ||
				!reflect.DeepEqual(projectionEvidence.EnvironmentA.ContentProjections, projectionEvidence.EnvironmentB.ContentProjections) {
				t.Fatalf("authenticated post-reopen client projections differ from the final journal prefix or each other: A=%+v B=%+v",
					projectionEvidence.EnvironmentA, projectionEvidence.EnvironmentB)
			}
			evidence := m5CrossContainerClientEvidence{
				Schema: "wipd.m5-cross-container-client-evidence/2", DomainID: m5TestDomain, Epoch: 1,
				EnvironmentA: a.state.EnvironmentID, EnvironmentB: b.state.EnvironmentID,
				AsOf: finalA.Anchor, ManifestDigest: finalA.ManifestDigest,
				EventRecords: eventsA, OverlayCount: len(overlayA),
				EnvironmentAMatterProjections:  matterProjectionsA,
				EnvironmentBMatterProjections:  matterProjectionsB,
				EnvironmentAStepProjections:    projectionEvidence.EnvironmentA.StepProjections,
				EnvironmentBStepProjections:    projectionEvidence.EnvironmentB.StepProjections,
				EnvironmentAContentProjections: projectionEvidence.EnvironmentA.ContentProjections,
				EnvironmentBContentProjections: projectionEvidence.EnvironmentB.ContentProjections,
			}
			encoded, encodeErr := json.Marshal(evidence)
			if encodeErr != nil {
				t.Fatal(encodeErr)
			}
			if writeErr := os.WriteFile(path, encoded, 0o600); writeErr != nil {
				t.Fatalf("write cross-container client convergence evidence: %v", writeErr)
			}
		} else {
			// M5 has no per-Environment read endpoint; this common pinned page is
			// provenance for both installed pulls because its exact as-of prefix
			// and manifest match each independently authenticated journal.
			now := time.Now().UTC()
			pinned, pinErr := fixture.store.PinSnapshot(ctx, m5TestDomain, 1, authoritystore.EmptyPrefixAnchor(),
				m5TwoEnvironmentID(90), now, time.Minute)
			if pinErr != nil {
				t.Fatalf("pin common final authority read snapshot: %v", pinErr)
			}
			page, readErr := fixture.store.ReadMatterPage(ctx, m5TestDomain, 1, pinned.ID, m5TestRepo, 100, "", now)
			if readErr != nil || page.Source != "authority" || page.Reachability != "reachable" || page.HistoryState != "current" ||
				!sameAuthorityAnchor(finalA.Anchor, wireAnchor(page.AsOf)) || page.ManifestDigest != finalA.ManifestDigest || len(page.Items) != 2 {
				t.Fatalf("authority read provenance/as-of differs from both authenticated pulls: page=%+v err=%v stateA=%+v", page, readErr, finalA.Anchor)
			}
			if len(pinned.Delta.Events) != len(eventsA) {
				t.Fatalf("authority final snapshot has %d events, both pulls have %d", len(pinned.Delta.Events), len(eventsA))
			}
			for index, event := range pinned.Delta.Events {
				if index >= len(eventsA) || eventsA[index].EventID != event.EventID || !bytes.Equal(eventsA[index].Record, event.Record) {
					t.Fatalf("Environment pulls differ from authority event bytes/order at position %d: pull=%+v authority=%+v",
						index, eventsA[index], event)
				}
			}
		}
	})
}

type m5CrossContainerBundle struct {
	Schema                  string               `json:"schema"`
	Origin                  string               `json:"origin"`
	DomainID                string               `json:"domain_id"`
	RepoID                  string               `json:"repo_id"`
	Epoch                   uint64               `json:"authority_epoch"`
	OwnerRootPublicKey      []byte               `json:"owner_root_public_key"`
	AuthorityCertificateDER [][]byte             `json:"authority_certificate_chain_der"`
	ArtifactKeyCertificate  []byte               `json:"artifact_key_certificate"`
	EnvironmentA            m5ProcessClientState `json:"environment_a"`
	EnvironmentB            m5ProcessClientState `json:"environment_b"`
}

type m5CrossContainerTraffic struct {
	Environment string `json:"environment_id"`
	Kind        string `json:"frame_kind"`
	Host        string `json:"host"`
	RemoteAddr  string `json:"remote_addr"`
	At          string `json:"at"`
}

type m5CrossContainerRefusalEvidence struct {
	Environment string                `json:"environment_id"`
	MatterID    string                `json:"matter_id"`
	Before      wipdwire.PrefixAnchor `json:"before"`
	After       wipdwire.PrefixAnchor `json:"after"`
	Unchanged   bool                  `json:"prefix_unchanged"`
}

type m5CrossContainerAuthorityEvidence struct {
	Schema         string                             `json:"schema"`
	DomainID       string                             `json:"domain_id"`
	Epoch          uint64                             `json:"authority_epoch"`
	EnvironmentA   string                             `json:"environment_a"`
	EnvironmentB   string                             `json:"environment_b"`
	Source         string                             `json:"source"`
	Reachability   string                             `json:"reachability"`
	HistoryState   string                             `json:"history_state"`
	AsOf           wipdwire.PrefixAnchor              `json:"as_of"`
	ManifestDigest string                             `json:"manifest_digest"`
	Items          int                                `json:"matter_count"`
	Matter         []m5CrossContainerMatterProjection `json:"matter_projections"`
	Events         []wipdwire.EventRecord             `json:"events"`
	Traffic        []m5CrossContainerTraffic          `json:"traffic"`
	Contention     m5CrossContainerRefusalEvidence    `json:"contention_refusal"`
}

type m5CrossContainerClientEvidence struct {
	Schema                         string                             `json:"schema"`
	DomainID                       string                             `json:"domain_id"`
	Epoch                          uint64                             `json:"authority_epoch"`
	EnvironmentA                   string                             `json:"environment_a"`
	EnvironmentB                   string                             `json:"environment_b"`
	AsOf                           wipdwire.PrefixAnchor              `json:"as_of"`
	ManifestDigest                 string                             `json:"manifest_digest"`
	EventRecords                   []wipdwire.EventRecord             `json:"event_records"`
	OverlayCount                   int                                `json:"overlay_count"`
	EnvironmentAMatterProjections  []m5CrossContainerMatterProjection `json:"environment_a_matter_projections"`
	EnvironmentBMatterProjections  []m5CrossContainerMatterProjection `json:"environment_b_matter_projections"`
	EnvironmentAStepProjections    []json.RawMessage                  `json:"environment_a_step_projections"`
	EnvironmentBStepProjections    []json.RawMessage                  `json:"environment_b_step_projections"`
	EnvironmentAContentProjections []json.RawMessage                  `json:"environment_a_content_projections"`
	EnvironmentBContentProjections []json.RawMessage                  `json:"environment_b_content_projections"`
}

type m5CrossContainerMatterProjection struct {
	ID           string `cbor:"id" json:"id"`
	RepoID       string `cbor:"repo_id" json:"repo_id"`
	Locator      string `cbor:"locator" json:"locator"`
	Title        string `cbor:"title" json:"title"`
	BirthEventID string `cbor:"birth_event_id" json:"birth_event_id"`
}

type m5CrossContainerInstalledProjection struct {
	EnvironmentID      string                 `json:"environment_id"`
	Prefix             wipdwire.PrefixAnchor  `json:"prefix"`
	ManifestDigest     string                 `json:"manifest_digest"`
	EventRecords       []wipdwire.EventRecord `json:"event_records"`
	MatterProjections  []json.RawMessage      `json:"matter_projections"`
	StepProjections    []json.RawMessage      `json:"step_projections"`
	ContentProjections []json.RawMessage      `json:"content_projections"`
}

type m5CrossContainerInstalledProjections struct {
	Schema       string                              `json:"schema"`
	EnvironmentA m5CrossContainerInstalledProjection `json:"environment_a"`
	EnvironmentB m5CrossContainerInstalledProjection `json:"environment_b"`
}

func runM5CrossContainerProjectionPulls(t *testing.T, output string, a, b m5ProcessEnvironment) m5CrossContainerInstalledProjections {
	t.Helper()
	command := exec.Command(os.Args[0], "-test.v", "-test.run=^TestM5CrossContainerClientProjectionEvidence$", "-test.count=1")
	command.Env = append(os.Environ(),
		"WIP_M5_CROSS_CONTAINER_PROJECTION_PROFILE_A="+filepath.Join(a.profileRoot, "connected-authority.json"),
		"WIP_M5_CROSS_CONTAINER_PROJECTION_STATE_A="+a.clientStateRoot,
		"WIP_M5_CROSS_CONTAINER_PROJECTION_PROFILE_B="+filepath.Join(b.profileRoot, "connected-authority.json"),
		"WIP_M5_CROSS_CONTAINER_PROJECTION_STATE_B="+b.clientStateRoot,
		"WIP_M5_CROSS_CONTAINER_PROJECTION_OUTPUT="+output,
	)
	commandOutput, err := command.CombinedOutput()
	if err != nil {
		t.Fatalf("run authenticated client projection pulls after journal reopen: %v\n%s", err, commandOutput)
	}
	defer func() { _ = os.Remove(output) }()
	data, err := os.ReadFile(output)
	if err != nil {
		t.Fatalf("read authenticated client projection evidence: %v", err)
	}
	var evidence m5CrossContainerInstalledProjections
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if err = decoder.Decode(&evidence); err != nil || decoder.Decode(new(any)) != io.EOF {
		t.Fatalf("decode authenticated client projection evidence: %v", err)
	}
	return evidence
}

func m5ClientMatterProjections(values []json.RawMessage) ([]m5CrossContainerMatterProjection, error) {
	projections := make([]m5CrossContainerMatterProjection, 0, len(values))
	for _, raw := range values {
		var fields map[string]json.RawMessage
		if err := json.Unmarshal(raw, &fields); err != nil || len(fields) < 5 || len(fields) > 6 {
			return nil, fmt.Errorf("invalid client Matter projection fields")
		}
		for _, key := range []string{"id", "repo_id", "locator", "title", "birth_event_id"} {
			if _, ok := fields[key]; !ok {
				return nil, fmt.Errorf("client Matter projection omits %q", key)
			}
		}
		if state, ok := fields["state"]; ok {
			var value string
			if len(fields) != 6 || json.Unmarshal(state, &value) != nil || value == "" {
				return nil, fmt.Errorf("client Matter projection has invalid state")
			}
		}
		var projection m5CrossContainerMatterProjection
		if err := json.Unmarshal(raw, &projection); err != nil || projection.ID == "" || projection.RepoID == "" ||
			projection.Locator == "" || projection.Title == "" || projection.BirthEventID == "" {
			return nil, fmt.Errorf("client Matter projection has invalid values")
		}
		projections = append(projections, projection)
	}
	return projections, nil
}

// TestM5CrossContainerAuthorityFixtureWorker owns the disposable authority
// store and HTTPS service for the Compose cross-container acceptance run.
func TestM5CrossContainerAuthorityFixtureWorker(t *testing.T) {
	bundlePath := os.Getenv("WIP_M5_CROSS_CONTAINER_BUNDLE_OUTPUT")
	stopPath := os.Getenv("WIP_M5_CROSS_CONTAINER_STOP")
	evidencePath := os.Getenv("WIP_M5_CROSS_CONTAINER_AUTHORITY_EVIDENCE")
	if bundlePath == "" || stopPath == "" || evidencePath == "" {
		t.Skip("cross-container authority worker is enabled only by the isolated runtime harness")
	}
	if filepath.Dir(bundlePath) != filepath.Dir(stopPath) || filepath.Dir(bundlePath) != filepath.Dir(evidencePath) {
		t.Fatal("cross-container worker artifacts must share the run-owned authority state directory")
	}
	fixture := newM5CommandFixtureAt(t, "0.0.0.0:8443", "authority-env", "authority-env")
	registry, err := NewM5BirthRegistry()
	if err != nil {
		t.Fatal(err)
	}
	config := fixture.config
	config.Registry = registry
	fixture.config = config
	fixture.server = fixture.serverForStore(t, fixture.store)

	owner := m5TestKey("owner-root")
	ownerID := fixture.profile.OwnerRootSPKI()
	privateB := m5TestKey("step19-environment-b")
	csrB, err := x509.CreateCertificateRequest(rand.Reader, &x509.CertificateRequest{Subject: pkix.Name{CommonName: "M5 Step 19 Environment B"}}, privateB)
	if err != nil {
		t.Fatal(err)
	}
	grantB := m5TestOwnerArtifact(t, owner, ownerID, m5TestDomain, "enrollment-grant", "wipd.enrollment-grant/1", map[string]any{
		"schema": "wipd.enrollment-grant/1", "grant_id": m5TwoEnvironmentID(80), "domain_id": m5TestDomain,
		"authority_epoch": uint64(1), "owner_key_id": ownerID, "scope": "environment-enroll",
		"requested_spki_digest": m5TestPublicDigest(privateB.Public().(ed25519.PublicKey)),
		"prior_environment_id":  nil, "nonce": bytes.Repeat([]byte{0x62}, 16),
		"issued_at":  fixture.now.Add(-time.Minute).Format(time.RFC3339Nano),
		"expires_at": fixture.now.Add(5 * time.Minute).Format(time.RFC3339Nano),
	}, fixture.now)
	environmentBID, err := stableEnvironmentID(m5TestDomain, grantB)
	if err != nil {
		t.Fatal(err)
	}
	leafB, err := m5EnvironmentLeafCertificate(privateB, m5TestKey("environment-ca"), fixture.config.EnvironmentCACertificateDER,
		m5TestDomain, environmentBID, ownerID, 102, fixture.now)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = fixture.store.IssueEnvironmentCertificate(context.Background(), m5TestDomain, environmentBID, grantB, csrB,
		[][]byte{leafB, fixture.config.EnvironmentCACertificateDER}, fixture.now); err != nil {
		t.Fatalf("authority-side test provisioning for Environment B: %v", err)
	}
	clientRoot := t.TempDir()
	environmentA, err := prepareM5ProcessEnvironment(t, fixture, clientRoot, "environment-a", m5TestEnv,
		fixture.environment, fixture.clientCert.Certificate[0], fixture.clientCert.Certificate[1])
	if err != nil {
		t.Fatal(err)
	}
	environmentB, err := prepareM5ProcessEnvironment(t, fixture, clientRoot, "environment-b", environmentBID,
		privateB, leafB, fixture.config.EnvironmentCACertificateDER)
	if err != nil {
		t.Fatal(err)
	}
	bundle := m5CrossContainerBundle{
		Schema: "wipd.m5-cross-container-bundle/1", Origin: fixture.profile.Origin(), DomainID: m5TestDomain,
		RepoID: m5TestRepo, Epoch: 1, OwnerRootPublicKey: bytes.Clone(fixture.ownerRoot),
		AuthorityCertificateDER: cloneByteSlices(fixture.serverCert.Certificate),
		ArtifactKeyCertificate:  bytes.Clone(fixture.config.ArtifactKeyCertificate),
		EnvironmentA:            environmentA.state, EnvironmentB: environmentB.state,
	}
	bundleBytes, err := json.Marshal(bundle)
	if err != nil {
		t.Fatal(err)
	}
	defer clear(bundleBytes)
	trace := &m5TwoEnvironmentTrace{}
	fixture.server.http.Handler = trace.wrapWithAuthorityStore(fixture.server.http.Handler, fixture.store)
	serveCtx, stopServer := context.WithCancel(context.Background())
	serveDone := make(chan error, 1)
	go func() { serveDone <- fixture.server.Serve(serveCtx, fixture.listener) }()
	if err = waitForM5AuthorityReady(fixture, 10*time.Second); err != nil {
		stopServer()
		t.Fatalf("cross-container authority did not become ready: %v", err)
	}
	if err = os.WriteFile(bundlePath, bundleBytes, 0o600); err != nil {
		stopServer()
		t.Fatalf("write cross-container client fixture bundle: %v", err)
	}
	defer clear(environmentA.state.PrivateKeyPKCS8)
	defer clear(environmentB.state.PrivateKeyPKCS8)
	if err = waitForM5File(stopPath, 5*time.Minute); err != nil {
		stopServer()
		t.Fatal(err)
	}
	stopServer()
	if serveErr := <-serveDone; serveErr != nil {
		t.Fatalf("cross-container authority server shutdown: %v", serveErr)
	}
	evidence, err := m5BuildCrossContainerAuthorityEvidence(context.Background(), fixture, trace, environmentBID)
	if err != nil {
		t.Fatal(err)
	}
	evidenceBytes, err := json.Marshal(evidence)
	if err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(evidencePath, evidenceBytes, 0o600); err != nil {
		t.Fatalf("write typed authority-side M5 evidence: %v", err)
	}
}

func waitForM5AuthorityReady(fixture *m5CommandFixture, timeout time.Duration) error {
	client, err := fixture.profile.HTTPClient(fixture.serverRoots)
	if err != nil {
		return err
	}
	defer client.CloseIdleConnections()
	deadline := time.Now().Add(timeout)
	var lastRequestErr error
	for time.Now().Before(deadline) {
		response, requestErr := client.Get(fixture.profile.HealthURL())
		if requestErr == nil {
			_ = response.Body.Close()
			if response.ProtoMajor == 2 && response.StatusCode == http.StatusNoContent {
				return nil
			}
			return fmt.Errorf("unexpected health response: protocol=%s status=%s", response.Proto, response.Status)
		}
		lastRequestErr = requestErr
		time.Sleep(50 * time.Millisecond)
	}
	return fmt.Errorf("health endpoint did not answer before timeout: %w", lastRequestErr)
}

func readM5CrossContainerBundle(path string) (bundle m5CrossContainerBundle, retErr error) {
	file, err := os.Open(path)
	if err != nil {
		return bundle, err
	}
	defer func() {
		if closeErr := file.Close(); closeErr != nil && retErr == nil {
			retErr = fmt.Errorf("close cross-container M5 bundle: %w", closeErr)
		}
	}()
	decoder := json.NewDecoder(io.LimitReader(file, 4<<20))
	decoder.DisallowUnknownFields()
	if err = decoder.Decode(&bundle); err != nil {
		return bundle, err
	}
	if bundle.Schema != "wipd.m5-cross-container-bundle/1" || bundle.DomainID != m5TestDomain || bundle.RepoID != m5TestRepo ||
		bundle.Epoch != 1 || len(bundle.OwnerRootPublicKey) != ed25519.PublicKeySize || len(bundle.AuthorityCertificateDER) != 2 ||
		len(bundle.ArtifactKeyCertificate) == 0 || bundle.EnvironmentA.EnvironmentID == bundle.EnvironmentB.EnvironmentID {
		return bundle, fmt.Errorf("invalid cross-container M5 client bundle")
	}
	var trailing any
	if err = decoder.Decode(&trailing); err != io.EOF {
		return bundle, fmt.Errorf("cross-container M5 bundle has trailing data")
	}
	return bundle, nil
}

func m5FixtureFromCrossContainerBundle(bundle m5CrossContainerBundle) (*m5CommandFixture, error) {
	serverLeaf, err := x509.ParseCertificate(bundle.AuthorityCertificateDER[0])
	if err != nil {
		return nil, err
	}
	serverSPKI := sha256.Sum256(serverLeaf.RawSubjectPublicKeyInfo)
	ownerSPKI, err := x509.MarshalPKIXPublicKey(ed25519.PublicKey(bundle.OwnerRootPublicKey))
	if err != nil {
		return nil, err
	}
	ownerDigest := sha256.Sum256(ownerSPKI)
	ownerID := "sha256:" + hex.EncodeToString(ownerDigest[:])
	profile, err := NewProfile(bundle.Origin, bundle.DomainID, bundle.Epoch,
		"sha256:"+hex.EncodeToString(serverSPKI[:]), ownerID)
	if err != nil {
		return nil, err
	}
	profile, err = profile.WithM5LabRepoID(bundle.RepoID)
	if err != nil {
		return nil, err
	}
	return &m5CommandFixture{
		profile: profile, serverCert: tls.Certificate{Certificate: cloneByteSlices(bundle.AuthorityCertificateDER)},
		ownerRoot: bytes.Clone(bundle.OwnerRootPublicKey), now: time.Now().UTC(),
		config: M5LabConfig{ArtifactKeyCertificate: bytes.Clone(bundle.ArtifactKeyCertificate)},
	}, nil
}

func installM5CrossContainerEnvironment(t *testing.T, fixture *m5CommandFixture, parent, name string,
	state m5ProcessClientState,
) (m5ProcessEnvironment, error) {
	t.Helper()
	var environment m5ProcessEnvironment
	root := filepath.Join(parent, name)
	environment.profileRoot = filepath.Join(root, "profile")
	environment.clientStateRoot = filepath.Join(root, "client-state")
	environment.state = state
	if state.Schema != "wipd.m5-client-state/1" || state.DomainID != m5TestDomain || state.RepoID != m5TestRepo || state.Epoch != 1 ||
		state.EnvironmentID == "" || len(state.PrivateKeyPKCS8) == 0 || len(state.CertificateDER) != 2 {
		return environment, fmt.Errorf("invalid Environment state in cross-container bundle")
	}
	if err := os.MkdirAll(environment.profileRoot, 0o700); err != nil {
		return environment, err
	}
	if err := os.MkdirAll(environment.clientStateRoot, 0o700); err != nil {
		return environment, err
	}
	stateBytes, err := json.Marshal(state)
	if err != nil {
		return environment, err
	}
	if err = os.WriteFile(filepath.Join(environment.clientStateRoot, "client-state.json"), stateBytes, 0o600); err != nil {
		return environment, err
	}
	serverLeaf, err := x509.ParseCertificate(fixture.serverCert.Certificate[0])
	if err != nil {
		return environment, err
	}
	serverSPKI := sha256.Sum256(serverLeaf.RawSubjectPublicKeyInfo)
	profileConfig := struct {
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
		Schema: "wipd.connected-authority-profile/2", Origin: fixture.profile.Origin(), DomainID: m5TestDomain,
		Epoch: 1, RepoID: m5TestRepo, OwnerRootSPKI: fixture.profile.OwnerRootSPKI(),
		AuthoritySPKIPin:        "sha256:" + hex.EncodeToString(serverSPKI[:]),
		AuthorityCertificateDER: bytes.Clone(fixture.serverCert.Certificate[1]),
		OwnerRootPublicKey:      bytes.Clone(fixture.ownerRoot), ArtifactKeyCertificate: bytes.Clone(fixture.config.ArtifactKeyCertificate),
		ClientStateDirectory: environment.clientStateRoot,
	}
	configBytes, err := json.Marshal(profileConfig)
	if err != nil {
		return environment, err
	}
	return environment, os.WriteFile(filepath.Join(environment.profileRoot, "connected-authority.json"), configBytes, 0o600)
}

func cloneByteSlices(values [][]byte) [][]byte {
	cloned := make([][]byte, len(values))
	for index, value := range values {
		cloned[index] = bytes.Clone(value)
	}
	return cloned
}

func waitForM5File(path string, timeout time.Duration) error {
	deadline := time.NewTimer(timeout)
	defer deadline.Stop()
	ticker := time.NewTicker(50 * time.Millisecond)
	defer ticker.Stop()
	for {
		if _, err := os.Lstat(path); err == nil {
			return nil
		} else if !os.IsNotExist(err) {
			return err
		}
		select {
		case <-deadline.C:
			return fmt.Errorf("cross-container authority worker timed out waiting for stop signal")
		case <-ticker.C:
		}
	}
}

func m5BuildCrossContainerAuthorityEvidence(ctx context.Context, fixture *m5CommandFixture, trace *m5TwoEnvironmentTrace,
	environmentBID string,
) (m5CrossContainerAuthorityEvidence, error) {
	var evidence m5CrossContainerAuthorityEvidence
	now := time.Now().UTC()
	pinned, err := fixture.store.PinSnapshot(ctx, m5TestDomain, 1, authoritystore.EmptyPrefixAnchor(), m5TwoEnvironmentID(91), now, time.Minute)
	if err != nil {
		return evidence, err
	}
	page, err := fixture.store.ReadMatterPage(ctx, m5TestDomain, 1, pinned.ID, m5TestRepo, 100, "", now)
	if err != nil {
		return evidence, err
	}
	if page.Source != "authority" || page.Reachability != "reachable" || page.HistoryState != "current" || len(page.Items) != 2 ||
		page.ManifestDigest != pinned.Manifest.Digest || !sameAuthorityAnchor(wireAnchor(page.AsOf), wireAnchor(pinned.Delta.End)) {
		return evidence, fmt.Errorf("authority-side M5 evidence is not one current pinned prefix: page=%+v pin=%+v", page, pinned)
	}
	matterProjections := make([]m5CrossContainerMatterProjection, 0, len(page.Items))
	for _, item := range page.Items {
		var projection m5CrossContainerMatterProjection
		if err = wipdwire.DecodeCanonical(item.Value, &projection, "id", "repo_id", "locator", "title", "birth_event_id"); err != nil || projection.ID != item.ID {
			return evidence, fmt.Errorf("authority Matter projection is invalid at its pinned as-of prefix: item=%s err=%v", item.ID, err)
		}
		matterProjections = append(matterProjections, projection)
	}
	var traffic []m5CrossContainerTraffic
	for _, entry := range trace.all() {
		traffic = append(traffic, m5CrossContainerTraffic{
			Environment: entry.environment, Kind: entry.kind, Host: entry.host, RemoteAddr: entry.remoteAddr,
			At: entry.at.Format(time.RFC3339Nano),
		})
	}
	if len(traffic) == 0 {
		return evidence, fmt.Errorf("authority worker observed no authenticated M5 network frames")
	}
	refusals := trace.refusals()
	if len(refusals) != 1 || refusals[0].Environment != environmentBID || !refusals[0].Unchanged {
		return evidence, fmt.Errorf("authority-side typed contention evidence does not prove a stable refusal: %+v", refusals)
	}
	requiredKinds := map[string]bool{
		"claim.acquire": false, "command.submit": false, "claim-journal.ack": false,
		"claim-journal.seal": false, "claim.release": false, "pull.request": false,
	}
	environments := map[string]bool{m5TestEnv: false, environmentBID: false}
	for _, item := range traffic {
		if _, ok := environments[item.Environment]; !ok || item.Host != "authority-env:8443" || item.RemoteAddr == "" ||
			strings.HasPrefix(item.RemoteAddr, "127.") || strings.HasPrefix(item.RemoteAddr, "[::1]:") {
			return evidence, fmt.Errorf("authority worker saw unexpected client traffic endpoint: %+v", item)
		}
		environments[item.Environment] = true
		if _, ok := requiredKinds[item.Kind]; ok {
			requiredKinds[item.Kind] = true
		}
	}
	for kind, seen := range requiredKinds {
		if !seen {
			return evidence, fmt.Errorf("authority worker did not observe required remote frame %q", kind)
		}
	}
	for environment, seen := range environments {
		if !seen {
			return evidence, fmt.Errorf("authority worker did not observe authenticated traffic from Environment %s", environment)
		}
	}
	var finalRelease time.Time
	finalPulls := map[string]bool{m5TestEnv: false, environmentBID: false}
	for _, entry := range trace.all() {
		if entry.environment == m5TestEnv && entry.kind == "claim.release" && entry.at.After(finalRelease) {
			finalRelease = entry.at
		}
	}
	if finalRelease.IsZero() {
		return evidence, fmt.Errorf("authority worker observed no Environment A claim release")
	}
	for _, entry := range trace.all() {
		if entry.kind == "pull.request" && entry.at.After(finalRelease) {
			if _, ok := finalPulls[entry.environment]; ok {
				finalPulls[entry.environment] = true
			}
		}
	}
	for environment, seen := range finalPulls {
		if !seen {
			return evidence, fmt.Errorf("authority worker did not observe Environment %s authenticated pull after final claim release", environment)
		}
	}
	events := make([]wipdwire.EventRecord, len(pinned.Delta.Events))
	for index, event := range pinned.Delta.Events {
		events[index] = wipdwire.EventRecord{EventID: event.EventID, Record: bytes.Clone(event.Record)}
	}
	evidence = m5CrossContainerAuthorityEvidence{
		Schema: "wipd.m5-cross-container-authority-evidence/2", DomainID: m5TestDomain, Epoch: 1,
		EnvironmentA: m5TestEnv, EnvironmentB: environmentBID,
		Source: page.Source, Reachability: page.Reachability, HistoryState: page.HistoryState,
		AsOf: wireAnchor(page.AsOf), ManifestDigest: page.ManifestDigest, Items: len(page.Items), Matter: matterProjections,
		Events: events, Traffic: traffic,
		Contention: refusals[0],
	}
	return evidence, nil
}

type m5ProcessEnvironment struct {
	profileRoot     string
	clientStateRoot string
	state           m5ProcessClientState
}

type m5ProcessClientState struct {
	Schema             string                       `json:"schema"`
	RepoID             string                       `json:"repo_id"`
	DomainID           string                       `json:"domain_id"`
	Epoch              uint64                       `json:"authority_epoch"`
	EnvironmentID      string                       `json:"environment_id"`
	OwnerKeyID         string                       `json:"owner_key_id"`
	SPKIDigest         string                       `json:"spki_digest"`
	PrivateKeyPKCS8    []byte                       `json:"private_key_pkcs8"`
	CertificateDER     [][]byte                     `json:"certificate_chain_der"`
	Prefix             wipdwire.PrefixAnchor        `json:"prefix"`
	EventRecords       []wipdwire.EventRecord       `json:"event_records"`
	ManifestDigest     string                       `json:"manifest_digest"`
	ManifestEntries    []wipdwire.BlobManifestEntry `json:"manifest_entries"`
	Projections        []json.RawMessage            `json:"projections"`
	StepProjections    []json.RawMessage            `json:"step_projections"`
	ContentProjections []json.RawMessage            `json:"content_projections,omitempty"`
}

func prepareM5ProcessEnvironment(t *testing.T, fixture *m5CommandFixture, parent, name, environmentID string,
	private ed25519.PrivateKey, leafDER, caDER []byte, catalogue ...string,
) (m5ProcessEnvironment, error) {
	t.Helper()
	root := filepath.Join(parent, name)
	environment := m5ProcessEnvironment{
		profileRoot: filepath.Join(root, "profile"), clientStateRoot: filepath.Join(root, "client-state"),
	}
	if err := os.MkdirAll(environment.profileRoot, 0o700); err != nil {
		return environment, err
	}
	if err := os.MkdirAll(environment.clientStateRoot, 0o700); err != nil {
		return environment, err
	}
	leaf, err := x509.ParseCertificate(leafDER)
	if err != nil {
		return environment, err
	}
	privateDER, err := x509.MarshalPKCS8PrivateKey(private)
	if err != nil {
		return environment, err
	}
	defer clear(privateDER)
	spki := sha256.Sum256(leaf.RawSubjectPublicKeyInfo)
	environment.state = m5ProcessClientState{
		Schema: "wipd.m5-client-state/1", RepoID: m5TestRepo, DomainID: m5TestDomain, Epoch: 1,
		EnvironmentID: environmentID, OwnerKeyID: fixture.profile.OwnerRootSPKI(),
		SPKIDigest: "sha256:" + hex.EncodeToString(spki[:]), PrivateKeyPKCS8: bytes.Clone(privateDER),
		CertificateDER: [][]byte{bytes.Clone(leafDER), bytes.Clone(caDER)},
		Prefix:         wipdwire.PrefixAnchor{Digest: emptyPrefixDigest()}, EventRecords: []wipdwire.EventRecord{},
		ManifestDigest: emptyManifestDigest(), ManifestEntries: []wipdwire.BlobManifestEntry{},
		Projections: []json.RawMessage{}, StepProjections: []json.RawMessage{}, ContentProjections: []json.RawMessage{},
	}
	stateBytes, err := json.Marshal(environment.state)
	if err != nil {
		return environment, err
	}
	if err = os.WriteFile(filepath.Join(environment.clientStateRoot, "client-state.json"), stateBytes, 0o600); err != nil {
		return environment, err
	}
	authorityLeaf, err := x509.ParseCertificate(fixture.serverCert.Certificate[0])
	if err != nil {
		return environment, err
	}
	authoritySPKI := sha256.Sum256(authorityLeaf.RawSubjectPublicKeyInfo)
	profileConfig := struct {
		Schema                  string `json:"schema"`
		CommandCatalogue        string `json:"command_catalogue,omitempty"`
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
		DomainID: m5TestDomain, Epoch: 1, RepoID: m5TestRepo, OwnerRootSPKI: fixture.profile.OwnerRootSPKI(),
		AuthoritySPKIPin: "sha256:" + hex.EncodeToString(authoritySPKI[:]), AuthorityCertificateDER: bytes.Clone(fixture.serverCert.Certificate[1]),
		OwnerRootPublicKey: bytes.Clone(fixture.ownerRoot), ArtifactKeyCertificate: bytes.Clone(fixture.config.ArtifactKeyCertificate),
		ClientStateDirectory: environment.clientStateRoot,
	}
	if len(catalogue) > 1 || len(catalogue) == 1 && catalogue[0] != "m6-step5" && catalogue[0] != "m6-step7" && catalogue[0] != "m6-step8" && catalogue[0] != "m6-step9a" && catalogue[0] != "m6-step9b" && catalogue[0] != "m6-step9c" {
		return environment, fmt.Errorf("unsupported connected command catalogue %q", catalogue)
	}
	if len(catalogue) == 1 {
		profileConfig.CommandCatalogue = catalogue[0]
	}
	configBytes, err := json.Marshal(profileConfig)
	if err != nil {
		return environment, err
	}
	return environment, os.WriteFile(filepath.Join(environment.profileRoot, "connected-authority.json"), configBytes, 0o600)
}

func stageM5ProcessBlob(t *testing.T, environment m5ProcessEnvironment, content []byte) wipdjournal.StagedBlob {
	t.Helper()
	journal, err := wipdjournal.Open(filepath.Join(environment.profileRoot, "environment-journal"), wipdjournal.Identity{
		RepoID: environment.state.RepoID, DomainID: environment.state.DomainID, AuthorityEpoch: environment.state.Epoch,
		EnvironmentID: environment.state.EnvironmentID, OwnerRootSPKI: environment.state.OwnerKeyID,
	})
	if err != nil {
		t.Fatal(err)
	}
	staged, stageErr := journal.StageBlob(bytes.NewReader(content), int64(len(content)))
	closeErr := journal.Close()
	if stageErr != nil || closeErr != nil {
		t.Fatalf("stage fresh Environment blob: stage=%v close=%v", stageErr, closeErr)
	}
	return staged
}

func m5TwoEnvironmentID(number int) string {
	return fmt.Sprintf("01KZ7XHAQT1S46NYPN1PW1DY%02d", number)
}

func m5EnvironmentLeafCertificate(leafPrivate, caPrivate ed25519.PrivateKey, caDER []byte, domain, environment, ownerID string,
	serial int64, now time.Time,
) ([]byte, error) {
	if domain != m5TestDomain || environment == "" || ownerID == "" || now.IsZero() {
		return nil, fmt.Errorf("invalid test Environment leaf identity")
	}
	caCertificate, err := x509.ParseCertificate(caDER)
	if err != nil {
		return nil, err
	}
	uri := fmt.Sprintf("wipd://environment/%s?domain=%s&epoch=1&owner=%s", environment, domain, strings.TrimPrefix(ownerID, "sha256:"))
	san, err := asn1.Marshal([]asn1.RawValue{{Class: asn1.ClassContextSpecific, Tag: 6, Bytes: []byte(uri)}})
	if err != nil {
		return nil, err
	}
	template := &x509.Certificate{
		SerialNumber: big.NewInt(serial), Subject: pkix.Name{CommonName: "M5 Step 19 Environment"},
		NotBefore: now.Add(-time.Minute), NotAfter: now.Add(time.Hour), BasicConstraintsValid: true,
		KeyUsage: x509.KeyUsageDigitalSignature, ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageClientAuth},
		ExtraExtensions: []pkix.Extension{
			{Id: []int{2, 5, 29, 19}, Critical: true, Value: []byte{0x30, 0x00}},
			{Id: []int{2, 5, 29, 15}, Critical: true, Value: []byte{0x03, 0x02, 0x07, 0x80}},
			{Id: []int{2, 5, 29, 17}, Critical: true, Value: san},
		},
	}
	return x509.CreateCertificate(rand.Reader, template, caCertificate, leafPrivate.Public(), caPrivate)
}

type m5TwoEnvironmentTraceEntry struct {
	environment string
	kind        string
	host        string
	remoteAddr  string
	at          time.Time
	payload     []byte
}

type m5TwoEnvironmentTrace struct {
	mu              sync.Mutex
	entries         []m5TwoEnvironmentTraceEntry
	refusalEvidence []m5CrossContainerRefusalEvidence
}

func (trace *m5TwoEnvironmentTrace) wrap(next http.Handler) http.Handler {
	return trace.wrapWithAuthorityStore(next, nil)
}

func (trace *m5TwoEnvironmentTrace) wrapWithAuthorityStore(next http.Handler, store *authoritystore.Store) http.Handler {
	return http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		var acquire *wipdwire.ClaimAcquire
		var environmentID, matterID string
		var before authoritystore.PrefixAnchor
		if request.URL.Path == "/wipd/v1/exchange" {
			body, err := io.ReadAll(request.Body)
			if err == nil {
				_ = request.Body.Close()
				request.Body = io.NopCloser(bytes.NewReader(body))
				if frame, frameErr := wipdwire.ReadFrame(bytes.NewReader(body)); frameErr == nil {
					environmentID = m5TwoEnvironmentIDFromTLS(request)
					trace.add(environmentID, frame.Kind, request.Host, request.RemoteAddr, frame.Payload)
					if store != nil && frame.Kind == "claim.acquire" {
						var decoded wipdwire.ClaimAcquire
						if wipdwire.DecodeCanonical(frame.Payload, &decoded, "schema", "canonical_command", "request_hash", "installed", "deadline") == nil {
							if fields, decodeErr := wipdwire.DecodeCanonicalMap(decoded.CanonicalCommand,
								"schema", "command_id", "authority", "environment", "acted_at", "actor", "causation_command_id", "correlation_command_id", "operation", "context", "claim", "input", "blobs"); decodeErr == nil {
								if identity, ok := fields["environment"].(map[string]any); ok {
									environmentID, _ = identity["id"].(string)
								}
								if input, ok := fields["input"].(map[string]any); ok {
									matterID, _ = input["matter_id"].(string)
								}
								if matterID != "" && environmentID != "" {
									acquire = &decoded
									before, _ = store.CurrentPrefixAnchor(request.Context(), m5TestDomain)
								}
							}
						}
					}
				}
			}
		}
		if acquire == nil {
			next.ServeHTTP(writer, request)
			return
		}
		captured := &m5ResponseCapture{ResponseWriter: writer}
		next.ServeHTTP(captured, request)
		responseFrames, frameErr := wipdwire.ReadFrames(captured.body.Bytes(), 4)
		if frameErr != nil || len(responseFrames) != 2 || responseFrames[0].Kind != "submission.accepted" ||
			responseFrames[1].Kind != "command.terminal" {
			return
		}
		fields, decodeErr := wipdwire.DecodeCanonicalMap(responseFrames[1].Payload,
			"schema", "domain_id", "authority_epoch", "identity_schema", "command_id", "request_hash", "operation", "environment", "result", "accepted_events")
		if decodeErr != nil {
			return
		}
		result, ok := fields["result"].(map[string]any)
		if !ok || result["code"] != string(operation.ResultRefused) {
			return
		}
		after, anchorErr := store.CurrentPrefixAnchor(request.Context(), m5TestDomain)
		if anchorErr != nil {
			return
		}
		beforeWire, afterWire := wireAnchor(before), wireAnchor(after)
		control := m5CrossContainerRefusalEvidence{
			Environment: environmentID, MatterID: matterID, Before: beforeWire, After: afterWire,
			Unchanged: before == after && sameAuthorityAnchor(beforeWire, acquire.Installed),
		}
		trace.mu.Lock()
		trace.refusalEvidence = append(trace.refusalEvidence, control)
		trace.mu.Unlock()
	})
}

type m5ResponseCapture struct {
	http.ResponseWriter
	body bytes.Buffer
}

func (capture *m5ResponseCapture) Write(data []byte) (int, error) {
	_, _ = capture.body.Write(data)
	return capture.ResponseWriter.Write(data)
}

func (trace *m5TwoEnvironmentTrace) add(environment, kind, host, remoteAddr string, payload []byte) {
	trace.mu.Lock()
	defer trace.mu.Unlock()
	trace.entries = append(trace.entries, m5TwoEnvironmentTraceEntry{
		environment: environment, kind: kind, host: host, remoteAddr: remoteAddr,
		at: time.Now().UTC(), payload: bytes.Clone(payload),
	})
}

func (trace *m5TwoEnvironmentTrace) clear() {
	trace.mu.Lock()
	defer trace.mu.Unlock()
	trace.entries = nil
}

func (trace *m5TwoEnvironmentTrace) forEnvironment(environment string) []m5TwoEnvironmentTraceEntry {
	trace.mu.Lock()
	defer trace.mu.Unlock()
	var out []m5TwoEnvironmentTraceEntry
	for _, entry := range trace.entries {
		if entry.environment == environment {
			out = append(out, m5TwoEnvironmentTraceEntry{
				environment: entry.environment, kind: entry.kind, host: entry.host, remoteAddr: entry.remoteAddr,
				at: entry.at, payload: bytes.Clone(entry.payload),
			})
		}
	}
	return out
}

func (trace *m5TwoEnvironmentTrace) all() []m5TwoEnvironmentTraceEntry {
	trace.mu.Lock()
	defer trace.mu.Unlock()
	out := make([]m5TwoEnvironmentTraceEntry, 0, len(trace.entries))
	for _, entry := range trace.entries {
		out = append(out, m5TwoEnvironmentTraceEntry{
			environment: entry.environment, kind: entry.kind, host: entry.host, remoteAddr: entry.remoteAddr,
			at: entry.at, payload: bytes.Clone(entry.payload),
		})
	}
	return out
}

func (trace *m5TwoEnvironmentTrace) refusals() []m5CrossContainerRefusalEvidence {
	trace.mu.Lock()
	defer trace.mu.Unlock()
	return append([]m5CrossContainerRefusalEvidence(nil), trace.refusalEvidence...)
}

func m5TwoEnvironmentIDFromTLS(request *http.Request) string {
	if request.TLS == nil || len(request.TLS.PeerCertificates) == 0 {
		return ""
	}
	for _, uri := range request.TLS.PeerCertificates[0].URIs {
		if uri.Scheme == "wipd" && uri.Host == "environment" {
			return strings.TrimPrefix(uri.Path, "/")
		}
	}
	return ""
}

func assertM5ClaimCloseTrace(t *testing.T, entries []m5TwoEnvironmentTraceEntry, environment, claimID string) {
	t.Helper()
	var positions []uint64
	ackIndex, sealIndex, releaseIndex := -1, -1, -1
	sealCount, releaseCount := 0, 0
	for index, entry := range entries {
		switch entry.kind {
		case "claim-journal.ack":
			var ack wipdwire.ClaimJournalReceiptAck
			if err := wipdwire.DecodeCanonical(entry.payload, &ack,
				"schema", "domain_id", "authority_epoch", "environment_id", "claim_id", "claim_epoch", "matter_id", "dispatch_id",
				"journal_id", "generation", "position", "terminal_receipt", "installed_prefix"); err != nil ||
				ack.EnvironmentID != environment || ack.ClaimID != claimID {
				t.Fatalf("close trace contains a misbound receipt ACK: %+v, %v", ack, err)
			}
			positions = append(positions, ack.Position)
			ackIndex = index
		case "claim-journal.seal":
			var seal wipdwire.ClaimJournalSeal
			if err := wipdwire.DecodeCanonical(entry.payload, &seal,
				"schema", "domain_id", "authority_epoch", "environment_id", "claim_id", "claim_epoch", "matter_id", "dispatch_id",
				"journal_id", "generation"); err != nil || seal.EnvironmentID != environment || seal.ClaimID != claimID {
				t.Fatalf("close trace contains a misbound seal: %+v, %v", seal, err)
			}
			sealIndex = index
			sealCount++
		case "claim.release":
			var release wipdwire.ClaimRelease
			if err := wipdwire.DecodeCanonical(entry.payload, &release, "schema", "canonical_command", "request_hash", "barrier", "deadline"); err != nil ||
				release.Barrier.Claim.ID != claimID || !release.Barrier.Sealed {
				t.Fatalf("claim release preceded or lost its sealed receipt barrier: %+v, %v", release, err)
			}
			releaseIndex = index
			releaseCount++
		}
	}
	if len(positions) != 4 || sealCount != 1 || releaseCount != 1 || sealIndex < 0 || releaseIndex < 0 ||
		ackIndex >= sealIndex || sealIndex >= releaseIndex {
		t.Fatalf("claim close trace is not receipt ACK(s) -> seal -> release: entries=%+v positions=%v", entries, positions)
	}
	for index, position := range positions {
		if position != uint64(index+1) {
			t.Fatalf("claim close receipt ACKs are not in contiguous journal order: %v", positions)
		}
	}
}
