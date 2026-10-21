package authoritystore

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/procrastivity/wip/internal/operation"
)

func TestRepoRouteIndexReadOnlyLookupAndAuthorityWideUniqueness(t *testing.T) {
	ctx := context.Background()
	s, _ := fresh(t)
	defer func() { _ = s.Close() }()
	domain, _ := identity(domainA, 7)
	if err := s.BootstrapDomain(ctx, domain, repoA); err != nil {
		t.Fatal(err)
	}
	if _, err := s.LookupRepoRoute(ctx, "github.com/acme/widget"); !errors.Is(err, ErrRouteUnbound) {
		t.Fatalf("unbound lookup = %v, want ErrRouteUnbound", err)
	}
	var before int
	if err := s.db.QueryRow(`SELECT count(*) FROM repo_route_index`).Scan(&before); err != nil || before != 0 {
		t.Fatalf("read-only miss changed index: count=%d err=%v", before, err)
	}
	bind := func(key, repo, domainID string) error {
		tx, err := s.db.BeginTx(ctx, nil)
		if err != nil {
			return err
		}
		defer func() { _ = tx.Rollback() }()
		if err = bindRepoRouteTx(ctx, tx, key, repo, domainID); err != nil {
			return err
		}
		return tx.Commit()
	}
	if err := bind("github.com/acme/widget", repoA, domainA); err != nil {
		t.Fatal(err)
	}
	if err := checkRouteIndexState(s.db); err != nil {
		t.Fatalf("validate a nonempty route index without nested-query deadlock: %v", err)
	}
	if err := bind("github.com/acme/widget", repoA, domainA); err != nil {
		t.Fatalf("exact binding retry = %v", err)
	}
	otherDomain, _ := identity(domainB, 2)
	if err := s.BootstrapDomain(ctx, otherDomain, repoB); err != nil {
		t.Fatal(err)
	}
	got, err := s.LookupRepoRoute(ctx, "github.com/acme/widget")
	if err != nil || got.RepoID != repoA || got.DomainID != domainA || got.ActiveEpoch != 7 {
		t.Fatalf("authority-derived lookup = %+v, %v", got, err)
	}
	if _, err = s.LookupRepoRouteForDomain(ctx, "github.com/acme/widget", domainA, 7); err != nil {
		t.Fatalf("selected-domain lookup = %v", err)
	}
	if _, err = s.LookupRepoRouteForDomain(ctx, "github.com/acme/widget", domainB, 2); !errors.Is(err, ErrRouteDomainMismatch) {
		t.Fatalf("foreign-domain lookup = %v, want opaque ErrRouteDomainMismatch", err)
	}
	if _, err = s.LookupRepoRouteForDomain(ctx, "github.com/acme/unbound", domainA, 7); !errors.Is(err, ErrRouteUnbound) {
		t.Fatalf("fresh selected-domain miss = %v, want ErrRouteUnbound", err)
	}
	missBinding, missAnchor, err := s.LookupRepoRouteSnapshotForDomain(ctx, "github.com/acme/unbound", domainA, 7)
	if !errors.Is(err, ErrRouteUnbound) || missBinding.RepoID != "" || missAnchor.Digest == "" {
		t.Fatalf("fresh snapshot miss = binding=%+v anchor=%+v err=%v", missBinding, missAnchor, err)
	}
	hitBinding, hitAnchor, err := s.LookupRepoRouteSnapshotForDomain(ctx, "github.com/acme/widget", domainA, 7)
	if err != nil || hitBinding != got || hitAnchor.Digest == "" {
		t.Fatalf("route hit snapshot = binding=%+v anchor=%+v err=%v", hitBinding, hitAnchor, err)
	}
	_, foreignAnchor, err := s.LookupRepoRouteSnapshotForDomain(ctx, "github.com/acme/widget", domainB, 2)
	if !errors.Is(err, ErrRouteDomainMismatch) || foreignAnchor != (PrefixAnchor{}) {
		t.Fatalf("foreign-domain snapshot leaked result: anchor=%+v err=%v", foreignAnchor, err)
	}
	if err := bind("github.com/acme/widget", repoB, domainB); !errors.Is(err, ErrRouteKeyBoundElsewhere) {
		t.Fatalf("cross-domain duplicate key = %v, want ErrRouteKeyBoundElsewhere", err)
	}
	if err := bind("github.com/acme/other", repoA, domainA); !errors.Is(err, ErrRouteRepoHasKey) {
		t.Fatalf("second key for Repo = %v, want ErrRouteRepoHasKey", err)
	}
	got, err = s.LookupRepoRoute(ctx, "github.com/acme/widget")
	if err != nil || got.RepoID != repoA || got.DomainID != domainA || got.ActiveEpoch != 7 {
		t.Fatalf("conflict changed existing binding: %+v, %v", got, err)
	}
}

func TestRepoRouteBindCommitsIndexAndReceiptAtomicallyAndReplaysExactly(t *testing.T) {
	ctx := context.Background()
	s, _, peer, artifactKey, now := commandFixture(t)
	defer func() { _ = s.Close() }()
	cloneID, worktreeID := registerTestCheckout(t, s, peer, artifactKey, now, repoA, claimTestID(32), "github.com/acme/widget", claimTestID(29))
	command := operation.Command{
		ID: claimTestID(800), AuthorityDomainID: domainA, ExpectedAuthorityEpoch: 7,
		EnvironmentID: envA, EnvironmentSequence: 2, ActedAt: "2026-09-23T11:59:00Z",
		CorrelationCommandID: claimTestID(800),
		Request: operation.Request{
			Operation: operation.RepoRouteBindV1.Metadata().Operation, Actor: "human",
			Context: operation.Context{Repo: repoA, Clone: cloneID, Worktree: worktreeID},
			Input:   operation.RepoRouteBindInput{NormalizedKey: "github.com/acme/widget"},
		},
	}
	hash := hashCommand(t, command)
	status, err := s.SubmitCommand(ctx, command, hash, peer, now)
	if err != nil || status.Owner == nil {
		t.Fatalf("submit route bind: status=%+v err=%v", status, err)
	}
	result := operation.Result{Code: operation.ResultSucceeded, Output: operation.RepoRouteBindOutput{
		RepoID: repoA, DomainID: domainA, Epoch: 7,
	}}
	completed, err := s.CompleteCommand(ctx, status.Owner, result, "", "", now.Add(time.Second), signWith(artifactKey))
	if err != nil || len(completed.Receipt) == 0 {
		t.Fatalf("complete route bind: status=%+v err=%v", completed, err)
	}
	binding, err := s.LookupRepoRoute(ctx, "github.com/acme/widget")
	if err != nil || binding.RepoID != repoA || binding.DomainID != domainA || binding.ActiveEpoch != 7 {
		t.Fatalf("route index after terminal commit: binding=%+v err=%v", binding, err)
	}
	replayed, err := s.SubmitCommand(ctx, command, hash, peer, now.Add(2*time.Second))
	if err != nil || replayed.Pending || string(replayed.Receipt) != string(completed.Receipt) {
		t.Fatalf("exact route-bind replay: status=%+v err=%v", replayed, err)
	}
	var eventCount int
	if err = s.db.QueryRow(`SELECT count(*) FROM authority_events WHERE domain_id=? AND command_id=?`, domainA, command.ID).Scan(&eventCount); err != nil || eventCount != 0 {
		t.Fatalf("route binding emitted authority events: count=%d err=%v", eventCount, err)
	}
}

func TestRepoAdoptRemoteBindsOnlyNullKeyRepoAndReplaysReceipt(t *testing.T) {
	ctx := context.Background()
	s, _, peer, artifactKey, now := commandFixture(t)
	defer func() { _ = s.Close() }()
	cloneID, worktreeID := registerTestCheckout(t, s, peer, artifactKey, now, repoA, claimTestID(33), "github.com/acme/widget", claimTestID(28))
	command := operation.Command{
		ID: claimTestID(810), AuthorityDomainID: domainA, ExpectedAuthorityEpoch: 7,
		EnvironmentID: envA, EnvironmentSequence: 2, ActedAt: "2026-09-23T11:59:00Z",
		CorrelationCommandID: claimTestID(810),
		Request: operation.Request{
			Operation: operation.RepoAdoptRemoteV1.Metadata().Operation, Actor: "human",
			Context: operation.Context{Repo: repoA, Clone: cloneID, Worktree: worktreeID},
			Input:   operation.RepoAdoptRemoteInput{NormalizedKey: "github.com/acme/widget"},
		},
	}
	mismatched := command
	mismatched.ID = claimTestID(812)
	mismatched.CorrelationCommandID = mismatched.ID
	mismatched.Request.Input = operation.RepoAdoptRemoteInput{NormalizedKey: "github.com/acme/other"}
	if status, submitErr := s.SubmitCommand(ctx, mismatched, hashCommand(t, mismatched), peer, now); !errors.Is(submitErr, ErrFenced) || status.Owner != nil {
		t.Fatalf("adoption of an unregistered normalized key = %+v, %v; want refusal before submission", status, submitErr)
	}
	hash := hashCommand(t, command)
	status, err := s.SubmitCommand(ctx, command, hash, peer, now)
	if err != nil || status.Owner == nil {
		t.Fatalf("submit remote adoption: status=%+v err=%v", status, err)
	}
	result := operation.Result{Code: operation.ResultSucceeded, Output: operation.RepoAdoptRemoteOutput{
		RepoID: repoA, DomainID: domainA, Epoch: 7,
	}}
	completed, err := s.CompleteCommand(ctx, status.Owner, result, "", "", now.Add(time.Second), signWith(artifactKey))
	if err != nil || len(completed.Receipt) == 0 {
		t.Fatalf("complete remote adoption: status=%+v err=%v", completed, err)
	}
	binding, err := s.LookupRepoRoute(ctx, "github.com/acme/widget")
	if err != nil || binding != (RepoRouteBinding{
		NormalizedKey: "github.com/acme/widget", RepoID: repoA, DomainID: domainA, ActiveEpoch: 7,
	}) {
		t.Fatalf("adoption did not preserve Repo/domain/epoch identity: %+v err=%v", binding, err)
	}
	replayed, err := s.SubmitCommand(ctx, command, hash, peer, now.Add(2*time.Second))
	if err != nil || replayed.Pending || string(replayed.Receipt) != string(completed.Receipt) {
		t.Fatalf("exact adoption retry: status=%+v err=%v", replayed, err)
	}
	if binding, lookupErr := s.LookupRepoRoute(ctx, "github.com/acme/widget"); lookupErr != nil || binding.RepoID != repoA {
		t.Fatalf("adoption retry changed original binding: binding=%+v err=%v", binding, lookupErr)
	}
}

func TestRepoRouteBindRejectsUnregisteredCheckoutWithoutChangingIndex(t *testing.T) {
	ctx := context.Background()
	s, _, peer, artifactKey, now := commandFixture(t)
	defer func() { _ = s.Close() }()
	cloneID, worktreeID := registerTestCheckout(t, s, peer, artifactKey, now, repoA, claimTestID(34), "github.com/acme/widget", claimTestID(27))
	first := operation.Command{
		ID: claimTestID(801), AuthorityDomainID: domainA, ExpectedAuthorityEpoch: 7,
		EnvironmentID: envA, EnvironmentSequence: 2, ActedAt: "2026-09-23T11:59:00Z", CorrelationCommandID: claimTestID(801),
		Request: operation.Request{
			Operation: operation.RepoRouteBindV1.Metadata().Operation,
			Actor:     "human",
			Context:   operation.Context{Repo: repoA, Clone: cloneID, Worktree: worktreeID},
			Input:     operation.RepoRouteBindInput{NormalizedKey: "github.com/acme/widget"},
		},
	}
	firstStatus, err := s.SubmitCommand(ctx, first, hashCommand(t, first), peer, now)
	if err != nil {
		t.Fatal(err)
	}
	firstResult := operation.Result{Code: operation.ResultSucceeded, Output: operation.RepoRouteBindOutput{RepoID: repoA, DomainID: domainA, Epoch: 7}}
	if _, err = s.CompleteCommand(ctx, firstStatus.Owner, firstResult, "", "", now.Add(time.Second), signWith(artifactKey)); err != nil {
		t.Fatalf("first bind: %v", err)
	}
	conflict := first
	conflict.ID = claimTestID(802)
	conflict.CorrelationCommandID = conflict.ID
	conflict.EnvironmentSequence = 3
	conflict.Request.Context.Clone = repoB
	conflict.Request.Context.Worktree = repoC
	conflictStatus, err := s.SubmitCommand(ctx, conflict, hashCommand(t, conflict), peer, now.Add(2*time.Second))
	if !errors.Is(err, ErrFenced) || conflictStatus.Owner != nil {
		t.Fatalf("unregistered conflicting checkout admission = %+v, %v; want refusal before submission", conflictStatus, err)
	}
	binding, err := s.LookupRepoRoute(ctx, "github.com/acme/widget")
	if err != nil || binding.RepoID != repoA {
		t.Fatalf("conflicting bind changed route index: binding=%+v err=%v", binding, err)
	}
}
