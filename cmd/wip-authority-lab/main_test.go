package main

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/procrastivity/wip/internal/authoritystore"
)

func TestBootstrapFreshDomainPersistsAuthorizedIdentityAndEmptyHighWater(t *testing.T) {
	ownerPublic, ownerPrivate, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	setupPublic, setupPrivate, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	domainID := "01ARZ3NDEKTSV4RRFFQ69G5FAV"
	repoID := "01ARZ3NDEKTSV4RRFFQ69G5FAW"
	suppliedOwnerRoot, err := decodePublicKey(base64.StdEncoding.EncodeToString(ownerPublic))
	if err != nil || !bytes.Equal(suppliedOwnerRoot, ownerPublic) {
		t.Fatalf("decode trusted offline owner root: %v", err)
	}
	domain, err := makeDomain(domainID, suppliedOwnerRoot)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 9, 27, 12, 0, 0, 0, time.UTC)
	nonce := make([]byte, 16)
	if _, err := rand.Read(nonce); err != nil {
		t.Fatal(err)
	}
	grant, err := authoritystore.CreateM5LabGenesisGrant(setupPrivate, domain, repoID, nonce, now, now.Add(10*time.Minute))
	if err != nil {
		t.Fatal(err)
	}
	root := filepath.Join(t.TempDir(), "authority")
	record, err := bootstrapFreshDomain(context.Background(), root, domain, repoID, setupPublic, grant, now)
	if err != nil {
		t.Fatal(err)
	}
	wantPrefix := sha256.Sum256([]byte("wipd/event-prefix/v1\x00"))
	if record.DomainID != domainID || record.Epoch != 1 || record.RepoID != repoID ||
		record.HighWater.EventCount != 0 || record.HighWater.EventID != nil ||
		record.HighWater.PrefixDigest != "sha256:"+hex.EncodeToString(wantPrefix[:]) {
		t.Fatalf("bootstrap record = %+v", record)
	}

	store, err := authoritystore.OpenExisting(root)
	if err != nil {
		t.Fatal(err)
	}
	gotDomain, err := store.LookupDomain(context.Background(), domainID)
	if err != nil || gotDomain.ID != domainID || gotDomain.ActiveEpoch != 1 || gotDomain.OwnerKeyID != domain.OwnerKeyID {
		t.Fatalf("persisted identity = %+v, %v", gotDomain, err)
	}
	if gotRepo, err := store.RepoDomain(context.Background(), repoID); err != nil || gotRepo != domainID {
		t.Fatalf("persisted Repo = %q, %v", gotRepo, err)
	}
	anchor, err := store.CurrentPrefixAnchor(context.Background(), domainID)
	if err != nil || anchor.EventCount != 0 || anchor.EventID != "" || anchor.Digest != "sha256:"+hex.EncodeToString(wantPrefix[:]) {
		t.Fatalf("persisted initial high-water = %+v, %v", anchor, err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}

	for _, name := range []string{"authority.db", "authority.db-wal"} {
		path := filepath.Join(root, name)
		data, err := os.ReadFile(path)
		if errors.Is(err, os.ErrNotExist) {
			continue
		}
		if err != nil {
			t.Fatal(err)
		}
		for label, secret := range map[string][]byte{
			"owner private key": ownerPrivate,
			"setup private key": setupPrivate,
			"grant bytes":       grant,
		} {
			if bytes.Contains(data, secret) {
				t.Errorf("%s persisted in %s", label, name)
			}
		}
	}
}

func TestBootstrapFreshDomainRejectsBeforeCreatingAuthorityRoot(t *testing.T) {
	ownerPublic, _, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	_, setupPrivate, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	domain, err := makeDomain("01ARZ3NDEKTSV4RRFFQ69G5FAV", ownerPublic)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 9, 27, 12, 0, 0, 0, time.UTC)
	grant, err := authoritystore.CreateM5LabGenesisGrant(setupPrivate, domain, "01ARZ3NDEKTSV4RRFFQ69G5FAW", bytes.Repeat([]byte{0x5a}, 16), now, now.Add(time.Minute))
	if err != nil {
		t.Fatal(err)
	}
	wrongPin := make(ed25519.PublicKey, ed25519.PublicKeySize)
	root := filepath.Join(t.TempDir(), "authority")
	if _, err := bootstrapFreshDomain(context.Background(), root, domain, "01ARZ3NDEKTSV4RRFFQ69G5FAW", wrongPin, grant, now); !errors.Is(err, authoritystore.ErrM5LabGenesisGrantInvalid) {
		t.Fatalf("wrong setup pin = %v, want grant refusal", err)
	}
	if _, err := os.Lstat(root); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("rejected grant created authority root: %v", err)
	}
}

func TestHostBootstrapRequiresOfflineOwnerRootInputBeforeDocker(t *testing.T) {
	err := runHostBootstrap([]string{"--container-id", "container", "--project", "wip-authority-proof-test"})
	if err == nil || !strings.Contains(err.Error(), "--owner-root-public-key") {
		t.Fatalf("bootstrap without retained owner public key = %v, want usage refusal", err)
	}
	err = runHostBootstrap([]string{
		"--container-id", "container", "--project", "wip-authority-proof-test", "--owner-root-public-key", "not-base64",
	})
	if err == nil || !strings.Contains(err.Error(), "invalid offline owner-root public key") {
		t.Fatalf("bootstrap with invalid owner public key = %v, want local refusal", err)
	}
}

func TestNewULIDProducesCanonicalAsymmetricIdentities(t *testing.T) {
	now := time.Date(2026, 9, 27, 12, 0, 0, 0, time.UTC)
	first, err := newULID(now)
	if err != nil {
		t.Fatal(err)
	}
	second, err := newULID(now)
	if err != nil {
		t.Fatal(err)
	}
	if first == second || len(first) != 26 || len(second) != 26 || first[0] > '7' || second[0] > '7' {
		t.Fatalf("generated ULIDs are not unique canonical IDs: %q %q", first, second)
	}
	for _, id := range []string{first, second} {
		for i := range id {
			if !bytes.ContainsRune([]byte(ulidAlphabet), rune(id[i])) {
				t.Fatalf("ULID contains non-Crockford character: %q", id)
			}
		}
	}
}

func TestLinuxGOARCHFollowsAuthorityImageAndRejectsUnsupportedPlatforms(t *testing.T) {
	for _, test := range []struct {
		os, architecture, want string
		valid                  bool
	}{
		{os: "linux", architecture: "amd64", want: "amd64", valid: true},
		{os: "linux", architecture: "arm64", want: "arm64", valid: true},
		{os: "darwin", architecture: "arm64", valid: false},
		{os: "linux", architecture: "unknown", valid: false},
	} {
		got, err := linuxGOARCH(test.os, test.architecture)
		if test.valid {
			if err != nil || got != test.want {
				t.Errorf("linuxGOARCH(%q, %q) = %q, %v; want %q", test.os, test.architecture, got, err, test.want)
			}
		} else if err == nil {
			t.Errorf("linuxGOARCH(%q, %q) = %q, want rejection", test.os, test.architecture, got)
		}
	}
}

func TestPostCommitLostWorkerResultIsOutcomeUnknownWithReconciliationIdentity(t *testing.T) {
	ownerPublic, _, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	setupPublic, setupPrivate, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	domainID := "01ARZ3NDEKTSV4RRFFQ69G5FAV"
	repoID := "01ARZ3NDEKTSV4RRFFQ69G5FAW"
	domain, err := makeDomain(domainID, ownerPublic)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 9, 27, 12, 0, 0, 0, time.UTC)
	grant, err := authoritystore.CreateM5LabGenesisGrant(setupPrivate, domain, repoID, []byte("lost-result-0001"), now, now.Add(time.Minute))
	if err != nil {
		t.Fatal(err)
	}
	root := filepath.Join(t.TempDir(), "authority")
	if _, err := bootstrapFreshDomain(context.Background(), root, domain, repoID, setupPublic, grant, now); err != nil {
		t.Fatalf("simulated worker commit: %v", err)
	}
	resultLost := errors.New("docker exec stream lost after worker commit")
	err = classifyBootstrapExecFailure(-1, "", domainID, repoID, resultLost)
	var unknown *bootstrapOutcomeUnknownError
	if !errors.As(err, &unknown) || unknown.DomainID != domainID || unknown.RepoID != repoID || !strings.Contains(err.Error(), "outcome-unknown") {
		t.Fatalf("post-commit lost result classification = %#v, %v", unknown, err)
	}
	store, err := authoritystore.OpenExisting(root)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = store.Close() }()
	if persisted, err := store.LookupDomain(context.Background(), domainID); err != nil || persisted.ID != domainID {
		t.Fatalf("committed domain unavailable for reconciliation: %+v, %v", persisted, err)
	}
	if persistedRepo, err := store.RepoDomain(context.Background(), repoID); err != nil || persistedRepo != domainID {
		t.Fatalf("committed Repo unavailable for reconciliation: %q, %v", persistedRepo, err)
	}
}

func TestWorkerSemanticRefusalIsNotClassifiedAsOutcomeUnknown(t *testing.T) {
	err := classifyBootstrapExecFailure(refusalExitCode, refusalMarker+"\ninvalid exact-scope grant", "01ARZ3NDEKTSV4RRFFQ69G5FAV", "01ARZ3NDEKTSV4RRFFQ69G5FAW", errors.New("exit status 3"))
	var refusal *semanticRefusalError
	if !errors.As(err, &refusal) || !strings.Contains(err.Error(), "refused") {
		t.Fatalf("semantic refusal classification = %T %v", err, err)
	}
	var unknown *bootstrapOutcomeUnknownError
	if errors.As(err, &unknown) {
		t.Fatalf("semantic refusal mislabeled outcome-unknown: %+v", unknown)
	}
}
