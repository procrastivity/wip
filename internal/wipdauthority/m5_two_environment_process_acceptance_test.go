package wipdauthority

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/sha256"
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
	fixture := newM5CommandFixture(t)
	registry, err := NewM5BirthRegistry()
	if err != nil {
		t.Fatal(err)
	}
	configA := fixture.config
	configA.Registry = registry
	fixture.config = configA
	fixture.server = fixture.serverForStore(t, fixture.store)

	owner := m5TestKey("owner-root")
	ownerID := fixture.profile.OwnerRootSPKI()
	privateB := m5TestKey("step19-environment-b")
	csrB, err := x509.CreateCertificateRequest(rand.Reader, &x509.CertificateRequest{
		Subject: pkix.Name{CommonName: "M5 Step 19 Environment B"},
	}, privateB)
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
	if _, err = fixture.store.IssueEnvironmentCertificate(ctx, m5TestDomain, environmentBID, grantB, csrB,
		[][]byte{leafB, fixture.config.EnvironmentCACertificateDER}, fixture.now); err != nil {
		t.Fatalf("issue second authenticated fixture Environment certificate: %v", err)
	}

	trace := &m5TwoEnvironmentTrace{}
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

	root, err := os.MkdirTemp("", "w19-")
	if err != nil {
		t.Fatal(err)
	}
	if err = os.Chmod(root, 0o700); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(root) })
	a, err := prepareM5ProcessEnvironment(t, fixture, root, "environment-a", m5TestEnv,
		fixture.environment, fixture.clientCert.Certificate[0], fixture.clientCert.Certificate[1])
	if err != nil {
		t.Fatal(err)
	}
	defer clear(a.state.PrivateKeyPKCS8)
	b, err := prepareM5ProcessEnvironment(t, fixture, root, "environment-b", environmentBID,
		privateB, leafB, fixture.config.EnvironmentCACertificateDER)
	if err != nil {
		t.Fatal(err)
	}
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
		assertM5ClaimCloseTrace(t, trace.forEnvironment(b.state.EnvironmentID), b.state.EnvironmentID, claimB.ClaimID)
		replay, replayErr := clientB.ReleaseClaimJournal(ctx, closed.CommandID, claimB.ClaimID, claimB.ClaimEpoch,
			matterB.ID, claimB.DispatchID, operation.Actor("human"))
		if replayErr != nil || replay.Code != operation.ResultSucceeded || !bytes.Equal(replay.Receipt, closed.Receipt) {
			t.Fatalf("Environment B acquired-claim close replay = %+v, %v", replay, replayErr)
		}
	})

	runScenario("09-contention-refuses-without-closing-environment-a", func(t *testing.T) {
		beforeAnchor, anchorErr := fixture.store.CurrentPrefixAnchor(ctx, m5TestDomain)
		if anchorErr != nil {
			t.Fatal(anchorErr)
		}
		beforeJournal, journalErr := fixture.store.GetCurrentClaimJournal(ctx, m5TestDomain, 1, a.state.EnvironmentID,
			claimA.ClaimID, claimA.ClaimEpoch, matterA.ID, claimA.DispatchID)
		if journalErr != nil || beforeJournal.State != "open" {
			t.Fatalf("Environment A journal before contention = %+v, %v", beforeJournal, journalErr)
		}
		refused, acquireErr := clientB.AcquireClaim(ctx, m5TwoEnvironmentID(26), matterA.ID,
			m5TwoEnvironmentID(27), m5TwoEnvironmentID(28), m5TwoEnvironmentID(29), operation.Actor("human"))
		if acquireErr != nil || refused.Code != operation.ResultRefused || refused.Grant != nil || len(refused.Receipt) == 0 {
			t.Fatalf("Environment B acquisition of A's active Matter = %+v, %v; want stable refusal", refused, acquireErr)
		}
		afterAnchor, anchorErr := fixture.store.CurrentPrefixAnchor(ctx, m5TestDomain)
		if anchorErr != nil || afterAnchor != beforeAnchor {
			t.Fatalf("contention refusal changed authority event prefix: before=%+v after=%+v err=%v", beforeAnchor, afterAnchor, anchorErr)
		}
		afterJournal, journalErr := fixture.store.GetCurrentClaimJournal(ctx, m5TestDomain, 1, a.state.EnvironmentID,
			claimA.ClaimID, claimA.ClaimEpoch, matterA.ID, claimA.DispatchID)
		if journalErr != nil || afterJournal != beforeJournal {
			t.Fatalf("contention refusal changed A's open claim journal: before=%+v after=%+v err=%v", beforeJournal, afterJournal, journalErr)
		}
	})

	runScenario("10-release-a-then-authenticated-pulls-converge-with-authority-provenance", func(t *testing.T) {
		trace.clear()
		closed, closeErr := clientA.ReleaseClaimJournal(ctx, m5TwoEnvironmentID(30), claimA.ClaimID, claimA.ClaimEpoch,
			matterA.ID, claimA.DispatchID, operation.Actor("human"))
		if closeErr != nil || closed.Code != operation.ResultSucceeded || len(closed.Receipt) == 0 {
			t.Fatalf("Environment A acquired-claim close = %+v, %v", closed, closeErr)
		}
		assertM5ClaimCloseTrace(t, trace.forEnvironment(a.state.EnvironmentID), a.state.EnvironmentID, claimA.ClaimID)
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
		if uint64(len(eventsA)) != page.AsOf.EventCount || len(overlayA) < 8 || finalA.ManifestDigest == emptyManifestDigest() {
			t.Fatalf("final pull omitted complete event/projection/content/manifest state: events=%d overlay=%d manifest=%s",
				len(eventsA), len(overlayA), finalA.ManifestDigest)
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
	})
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
	private ed25519.PrivateKey, leafDER, caDER []byte,
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
	payload     []byte
}

type m5TwoEnvironmentTrace struct {
	mu      sync.Mutex
	entries []m5TwoEnvironmentTraceEntry
}

func (trace *m5TwoEnvironmentTrace) wrap(next http.Handler) http.Handler {
	return http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if request.URL.Path == "/wipd/v1/exchange" {
			body, err := io.ReadAll(request.Body)
			if err == nil {
				_ = request.Body.Close()
				request.Body = io.NopCloser(bytes.NewReader(body))
				if frame, frameErr := wipdwire.ReadFrame(bytes.NewReader(body)); frameErr == nil {
					trace.add(m5TwoEnvironmentIDFromTLS(request), frame.Kind, frame.Payload)
				}
			}
		}
		next.ServeHTTP(writer, request)
	})
}

func (trace *m5TwoEnvironmentTrace) add(environment, kind string, payload []byte) {
	trace.mu.Lock()
	defer trace.mu.Unlock()
	trace.entries = append(trace.entries, m5TwoEnvironmentTraceEntry{environment: environment, kind: kind, payload: bytes.Clone(payload)})
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
			out = append(out, m5TwoEnvironmentTraceEntry{environment: entry.environment, kind: entry.kind, payload: bytes.Clone(entry.payload)})
		}
	}
	return out
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
