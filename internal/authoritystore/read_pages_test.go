package authoritystore

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"
)

func TestAuthorityMatterPagesRemainPinnedAndTokensRemainScopedAcrossReopen(t *testing.T) {
	store, root, state, key, now := commandFixture(t)
	peer := fixturePeer{state: state, key: key}
	ctx := context.Background()
	completedMatter(t, store, now, 1, domainB, repoB, "alpha", peer)
	completedMatter(t, store, now, 2, repoC, repoC, "bravo", peer)
	pinned, err := store.PinSnapshot(ctx, domainA, 7, emptyAnchor(), grantA, now, 5*time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	first, err := store.ReadMatterPage(ctx, domainA, 7, pinned.ID, repoA, 1, "", now)
	if err != nil || first.Complete || len(first.Items) != 1 || first.AsOf != pinned.Delta.End ||
		first.ManifestDigest != pinned.Manifest.Digest || first.FilterHash != "sha256:867ef79181eee4c4c816695a4ac4b607b81d51a824100b586a9295996f3ee96a" ||
		first.Source != "authority" || first.Reachability != "reachable" || first.HistoryState != "current" {
		t.Fatalf("first pinned page: %+v, %v", first, err)
	}
	var item struct {
		Locator string `cbor:"locator"`
	}
	if err = artifactDecoder.Unmarshal(first.Items[0].Value, &item); err != nil || item.Locator != "alpha" || first.NextPageToken == "" {
		t.Fatalf("first page item/token = %+v %q %v; want alpha and continuation", item, first.NextPageToken, err)
	}
	currentToken := first.NextPageToken
	if _, err = store.ReadMatterPage(ctx, domainA, 7, pinned.ID, repoB, 1, currentToken, now); !errors.Is(err, ErrPageTokenScope) {
		t.Fatalf("token accepted a different filter: %v", err)
	}
	separator := strings.LastIndexByte(currentToken, '.')
	if separator < 0 || separator == len(currentToken)-1 {
		t.Fatalf("unexpected page token encoding: %q", currentToken)
	}
	tamperedMAC := currentToken[separator+1:]
	if tamperedMAC[0] == 'A' {
		tamperedMAC = "B" + tamperedMAC[1:]
	} else {
		tamperedMAC = "A" + tamperedMAC[1:]
	}
	tampered := currentToken[:separator+1] + tamperedMAC
	if _, err = store.ReadMatterPage(ctx, domainA, 7, pinned.ID, repoA, 1, tampered, now); !errors.Is(err, ErrInvalidPageToken) {
		t.Fatalf("tampered token accepted: %v", err)
	}
	completedMatter(t, store, now, 3, grantA, grantA, "zulu", peer)
	advanced, err := store.CurrentPrefixAnchor(ctx, domainA)
	if err != nil || advanced.EventCount != pinned.Delta.End.EventCount+1 {
		t.Fatalf("authority did not advance asymmetrically: %+v, %v", advanced, err)
	}
	if err = store.Close(); err != nil {
		t.Fatal(err)
	}
	store, err = OpenExisting(root)
	if err != nil {
		t.Fatalf("reopen authority with a live pinned read: %v", err)
	}
	defer func() { _ = store.Close() }()
	second, err := store.ReadMatterPage(ctx, domainA, 7, pinned.ID, repoA, 100, currentToken, now)
	if err != nil || !second.Complete || second.SnapshotID != first.SnapshotID || second.DomainID != first.DomainID || second.Epoch != first.Epoch ||
		second.AsOf != first.AsOf || second.ManifestDigest != first.ManifestDigest || second.FilterHash != first.FilterHash || second.ExpiresAt != first.ExpiresAt ||
		second.Source != first.Source || second.Reachability != first.Reachability || second.HistoryState != first.HistoryState || len(second.Items) != 1 {
		t.Fatalf("continuation changed page size/as-of after authority advance and reopen: %+v, %v", second, err)
	}
	if err = artifactDecoder.Unmarshal(second.Items[0].Value, &item); err != nil || item.Locator != "bravo" || second.NextPageToken != "" {
		t.Fatalf("second pinned page = %+v token=%q err=%v; expected only bravo", item, second.NextPageToken, err)
	}
	if _, err = store.ReadMatterPage(ctx, domainA, 7, pinned.ID, repoA, 1, currentToken, pinned.ExpiresAt); !errors.Is(err, ErrSnapshotExpired) {
		t.Fatalf("expired snapshot token accepted: %v", err)
	}
}
