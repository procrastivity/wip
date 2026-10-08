package authoritystore

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/fxamacker/cbor/v2"
	"github.com/procrastivity/wip/internal/operation"
)

func TestNamedBatchReadPagesPinEventPrefixAcrossMutationAndReopen(t *testing.T) {
	store, root, peer, artifactKey, now := commandFixture(t)
	ctx := context.Background()
	if err := store.AttachRepo(ctx, domainA, repoB); err != nil {
		t.Fatal(err)
	}
	batchID := claimTestID(1810)
	batchCommand := namedBatchCommand(claimTestID(1811), domainA, envA, repoA, 1, "pinned batch")
	completeNamedBatchForTest(t, store, batchCommand, peer, artifactKey, now, batchID, claimTestID(1812))
	matterACommand := matterCommand(claimTestID(1818), 2, "s9c-first-member")
	completeNamedBatchMatterForTest(t, store, matterACommand, peer, artifactKey, now, 1819, 1823)
	matterID := claimTestID(1819)
	secondMatterCommand := matterCommand(claimTestID(1828), 3, "s9c-cross-repo-member")
	secondMatterCommand.Request.Context.Repo = repoB
	completeNamedBatchMatterForTest(t, store, secondMatterCommand, peer, artifactKey, now, 1829, 1830)
	secondMatterID := claimTestID(1829)
	join := namedBatchMembershipCommand(claimTestID(1813), domainA, envA, repoA, 4,
		operation.BatchJoinV1.Metadata().Operation, operation.BatchMembershipInput{BatchID: batchID, MatterID: matterID})
	completeNamedBatchMembershipForTest(t, store, join, peer, artifactKey, now, 1831)
	secondJoin := namedBatchMembershipCommand(claimTestID(1826), domainA, envA, repoA, 5,
		operation.BatchJoinV1.Metadata().Operation, operation.BatchMembershipInput{BatchID: batchID, MatterID: secondMatterID})
	secondJoin.Request.Context.Repo = repoB
	completeNamedBatchMembershipForTest(t, store, secondJoin, peer, artifactKey, now, 1832)

	first, err := store.ReadNamedBatch(ctx, domainA, 7, batchID, 1, "", now)
	if err != nil || first.Complete || len(first.Items) != 1 || first.NextPageToken == "" || first.SnapshotID == "" ||
		first.ManifestDigest == "" || first.FilterHash == "" || first.Source != "authority" ||
		first.Reachability != "reachable" || first.HistoryState != "current" {
		t.Fatalf("first named-Batch page: %+v, %v", first, err)
	}
	var header map[string]cbor.RawMessage
	if err = canonicalDecode(first.Items[0].Value, &header); err != nil || !exactKeys(header, "kind", "batch_id", "name", "birth_event_id", "dismissed_event_id") {
		t.Fatalf("named-Batch header has wrong closed shape: %x (%v)", first.Items[0].Value, err)
	}
	var decodedHeader struct {
		Kind       string `cbor:"kind"`
		BatchID    string `cbor:"batch_id"`
		Name       string `cbor:"name"`
		BirthEvent string `cbor:"birth_event_id"`
	}
	if err = artifactDecoder.Unmarshal(first.Items[0].Value, &decodedHeader); err != nil || decodedHeader.Kind != "batch" ||
		decodedHeader.BatchID != batchID || decodedHeader.Name != "pinned batch" || decodedHeader.BirthEvent == "" {
		t.Fatalf("named-Batch header = %+v, %v", decodedHeader, err)
	}
	token := first.NextPageToken
	if _, err = store.ReadNamedBatch(ctx, domainA, 7, claimTestID(1899), 1, token, now); !errors.Is(err, ErrPageTokenScope) {
		t.Fatalf("continuation token accepted a changed Batch filter: %v", err)
	}
	if _, err = store.ReadNamedBatch(ctx, domainA, 8, batchID, 1, token, now); !errors.Is(err, ErrPageTokenScope) {
		t.Fatalf("continuation token accepted a changed authority epoch: %v", err)
	}
	if _, err = store.ReadNamedBatchPage(ctx, domainA, 7, claimTestID(1898), batchID, 1, token, now); !errors.Is(err, ErrPageTokenScope) {
		t.Fatalf("continuation token accepted a changed snapshot: %v", err)
	}
	secret, err := transferSecret(ctx, store.db)
	if err != nil {
		t.Fatalf("read authority page-token key for negative vectors: %v", err)
	}
	claims, err := verifyPageToken(token, derivePageTokenKey(secret))
	if err != nil {
		t.Fatalf("decode authority continuation for negative vectors: %v", err)
	}
	for _, test := range []struct {
		name   string
		mutate func(*pageTokenClaims)
	}{
		{name: "query", mutate: func(claims *pageTokenClaims) { claims.QueryName = "matter.list" }},
		{name: "version", mutate: func(claims *pageTokenClaims) { claims.QueryVersion = 2 }},
		{name: "overlay/source", mutate: func(claims *pageTokenClaims) { claims.Overlay = "folded-and-provisional" }},
	} {
		altered := claims
		test.mutate(&altered)
		validMACToken, signErr := signPageToken(altered, derivePageTokenKey(secret))
		if signErr != nil {
			t.Fatalf("sign %s scope vector: %v", test.name, signErr)
		}
		if _, err = store.ReadNamedBatchPage(ctx, domainA, 7, first.SnapshotID, batchID, 1, validMACToken, now); !errors.Is(err, ErrPageTokenScope) {
			t.Fatalf("continuation token accepted changed %s scope: %v", test.name, err)
		}
	}
	wrongIssuer := claims
	wrongIssuer.Issuer = "environment"
	wrongIssuerToken, err := signPageToken(wrongIssuer, derivePageTokenKey(secret))
	if err != nil {
		t.Fatalf("sign wrong-issuer token vector: %v", err)
	}
	if _, err = store.ReadNamedBatch(ctx, domainA, 7, batchID, 1, wrongIssuerToken, now); !errors.Is(err, ErrInvalidPageToken) {
		t.Fatalf("correctly signed wrong-issuer token did not use invalid-page-token boundary: %v", err)
	}

	leave := namedBatchMembershipCommand(claimTestID(1816), domainA, envA, repoA, 6,
		operation.BatchLeaveV1.Metadata().Operation, operation.BatchMembershipInput{BatchID: batchID, MatterID: matterID})
	completeNamedBatchMembershipForTest(t, store, leave, peer, artifactKey, now, 1833)
	advanced, err := store.CurrentPrefixAnchor(ctx, domainA)
	if err != nil || advanced.EventCount <= first.AsOf.EventCount {
		t.Fatalf("authority prefix failed to advance: %+v, %v", advanced, err)
	}

	if err = store.Close(); err != nil {
		t.Fatal(err)
	}
	store, err = OpenExisting(root)
	if err != nil {
		t.Fatalf("reopen authority with pinned named-Batch read: %v", err)
	}
	defer func() { _ = store.Close() }()
	second, err := store.ReadNamedBatch(ctx, domainA, 7, batchID, 100, token, now)
	if err != nil || second.Complete || second.AsOf != first.AsOf || second.ManifestDigest != first.ManifestDigest ||
		second.FilterHash != first.FilterHash || len(second.Items) != 1 || second.Items[0].ID != matterID || second.NextPageToken == "" {
		t.Fatalf("continuation did not preserve the pre-leave snapshot after reopen: %+v, %v", second, err)
	}
	third, err := store.ReadNamedBatch(ctx, domainA, 7, batchID, 100, second.NextPageToken, now)
	if err != nil || !third.Complete || third.AsOf != first.AsOf || third.ManifestDigest != first.ManifestDigest ||
		third.FilterHash != first.FilterHash || len(third.Items) != 1 || third.Items[0].ID != secondMatterID {
		t.Fatalf("final continuation did not preserve the pre-leave cross-Repo membership: %+v, %v", third, err)
	}
	memberships := append(append([]SnapshotItem(nil), second.Items...), third.Items...)
	for _, item := range memberships {
		var member map[string]cbor.RawMessage
		if err = canonicalDecode(item.Value, &member); err != nil || !exactKeys(member, "kind", "batch_id", "matter_id", "repo_id", "joined_event_id") {
			t.Fatalf("membership item has wrong closed shape: %x (%v)", item.Value, err)
		}
	}
	var decodedMember struct {
		Kind     string `cbor:"kind"`
		MatterID string `cbor:"matter_id"`
		RepoID   string `cbor:"repo_id"`
	}
	if err = artifactDecoder.Unmarshal(memberships[0].Value, &decodedMember); err != nil || decodedMember.Kind != "membership" ||
		decodedMember.MatterID != matterID || decodedMember.RepoID != repoA {
		t.Fatalf("pinned membership = %+v, %v", decodedMember, err)
	}
	if err = artifactDecoder.Unmarshal(memberships[1].Value, &decodedMember); err != nil || decodedMember.Kind != "membership" ||
		decodedMember.MatterID != secondMatterID || decodedMember.RepoID != repoB {
		t.Fatalf("pinned cross-Repo membership = %+v, %v", decodedMember, err)
	}

	if _, err = store.ReadNamedBatchPage(ctx, domainA, 7, first.SnapshotID, claimTestID(1899), 1, "", now); !errors.Is(err, ErrNamedBatchNotFound) {
		t.Fatalf("unknown Batch did not use the uniform named-Batch refusal: %v", err)
	}
	anonymousFixture := newSweepFixture(t, false, false, false)
	var anonymousBatchID string
	if err = anonymousFixture.f.s.db.QueryRow(`SELECT batch_id FROM anonymous_batches WHERE domain_id=? AND matter_id=?`, domainA, anonymousFixture.f.matter).Scan(&anonymousBatchID); err != nil {
		t.Fatalf("read fixture anonymous Batch identity: %v", err)
	}
	if _, err = anonymousFixture.f.s.ReadNamedBatch(ctx, domainA, 7, anonymousBatchID, 1, "", anonymousFixture.f.now); !errors.Is(err, ErrNamedBatchNotFound) {
		t.Fatalf("anonymous Batch did not use the uniform named-Batch refusal: %v", err)
	}
	if _, err = store.ReadNamedBatchPage(ctx, domainB, 7, first.SnapshotID, batchID, 1, "", now); !errors.Is(err, ErrFenced) {
		t.Fatalf("snapshot accepted another authenticated domain: %v", err)
	}
	separator := strings.LastIndexByte(token, '.')
	if separator < 0 {
		t.Fatalf("unexpected token format: %q", token)
	}
	tampered := token[:separator+1] + strings.Repeat("A", len(token)-separator-1)
	if _, err = store.ReadNamedBatchPage(ctx, domainA, 7, first.SnapshotID, batchID, 1, tampered, now); !errors.Is(err, ErrInvalidPageToken) {
		t.Fatalf("tampered page token accepted: %v", err)
	}
	if _, err = store.ReadNamedBatch(ctx, domainA, 7, batchID, 1, token, now.Add(6*time.Minute)); !errors.Is(err, ErrSnapshotExpired) {
		t.Fatalf("continuation token accepted an expired snapshot: %v", err)
	}

	emptyBatchID := claimTestID(1820)
	emptyBatch := namedBatchCommand(claimTestID(1821), domainA, envA, repoA, 7, "empty batch")
	completeNamedBatchForTest(t, store, emptyBatch, peer, artifactKey, now, emptyBatchID, claimTestID(1834))
	emptyPage, err := store.ReadNamedBatch(ctx, domainA, 7, emptyBatchID, 1, "", now)
	if err != nil || !emptyPage.Complete || len(emptyPage.Items) != 1 {
		t.Fatalf("empty named Batch was not represented by its complete header: %+v, %v", emptyPage, err)
	}
	var emptyHeader struct {
		Kind             string  `cbor:"kind"`
		DismissedEventID *string `cbor:"dismissed_event_id"`
	}
	if err = artifactDecoder.Unmarshal(emptyPage.Items[0].Value, &emptyHeader); err != nil || emptyHeader.Kind != "batch" || emptyHeader.DismissedEventID != nil {
		t.Fatalf("empty named-Batch header = %+v, %v", emptyHeader, err)
	}

	dismiss := namedBatchMembershipCommand(claimTestID(1824), domainA, envA, repoA, 8,
		operation.BatchDismissV1.Metadata().Operation, operation.BatchDismissInput{BatchID: emptyBatchID})
	completeNamedBatchMembershipForTest(t, store, dismiss, peer, artifactKey, now, 1835)
	dismissedPage, err := store.ReadNamedBatch(ctx, domainA, 7, emptyBatchID, 1, "", now)
	if err != nil || !dismissedPage.Complete || len(dismissedPage.Items) != 1 {
		t.Fatalf("dismissed named Batch read = %+v, %v", dismissedPage, err)
	}
	var dismissedHeader struct {
		DismissedEventID *string `cbor:"dismissed_event_id"`
	}
	if err = artifactDecoder.Unmarshal(dismissedPage.Items[0].Value, &dismissedHeader); err != nil || dismissedHeader.DismissedEventID == nil ||
		!ulid.MatchString(*dismissedHeader.DismissedEventID) {
		t.Fatalf("explicit dismissal was not visible in named-Batch header: %+v, %v", dismissedHeader, err)
	}
	if err = store.ReleaseSnapshot(ctx, domainA, 7, first.SnapshotID); err != nil {
		t.Fatal(err)
	}
	if _, err = store.ReadNamedBatch(ctx, domainA, 7, batchID, 1, token, now); !errors.Is(err, ErrSnapshotExpired) {
		t.Fatalf("continuation token accepted a released snapshot: %v", err)
	}
}

func TestNamedBatchReadIncludesSealedMember(t *testing.T) {
	fixture := newSweepFixture(t, false, false, false)
	store, now := fixture.f.s, fixture.f.now
	var sequence uint64
	if err := store.db.QueryRow(`SELECT sequence_head FROM environments WHERE domain_id=? AND environment_id=?`, domainA, envA).Scan(&sequence); err != nil {
		t.Fatal(err)
	}
	batchID := claimTestID(1860)
	createBatch := namedBatchCommand(claimTestID(1859), domainA, envA, repoA, sequence+1, "sealed member")
	completeNamedBatchForTest(t, store, createBatch, fixture.f.peer, fixture.f.key, now, batchID, claimTestID(1861))
	join := namedBatchMembershipCommand(claimTestID(1862), domainA, envA, repoA, sequence+2,
		operation.BatchJoinV1.Metadata().Operation, operation.BatchMembershipInput{BatchID: batchID, MatterID: fixture.f.matter})
	completeNamedBatchMembershipForTest(t, store, join, fixture.f.peer, fixture.f.key, now, 1863)
	page, err := store.ReadNamedBatch(context.Background(), domainA, 7, batchID, 100, "", now)
	if err != nil || !page.Complete || len(page.Items) != 2 || page.Items[0].ID != batchID || page.Items[1].ID != fixture.f.matter {
		t.Fatalf("all-sealed named-Batch membership was not retained: page=%+v err=%v", page, err)
	}
}
