package wipdseed

import (
	"crypto/ed25519"
	"crypto/rand"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/procrastivity/wip/internal/d115"
)

func TestD115TrustBundleInstallPublicationDurabilityAndRecovery(t *testing.T) {
	directory := t.TempDir()
	if err := os.Chmod(directory, 0o700); err != nil {
		t.Fatal(err)
	}
	first := testD115TrustBundle(t, 1)
	firstBytes, err := d115.EncodeOperatorTrustBundle(first)
	if err != nil {
		t.Fatal(err)
	}
	if err = InstallD115OperatorTrustBundle(directory, firstBytes, first.RealmID, D115BundleDigest(firstBytes)); err != nil {
		t.Fatal(err)
	}
	installedPath := filepath.Join(directory, d115BundleName)
	installed, err := os.ReadFile(installedPath)
	if err != nil {
		t.Fatal(err)
	}
	if string(installed) != string(firstBytes) {
		t.Fatal("first installed snapshot differs")
	}
	if err = InstallD115OperatorTrustBundle(directory, firstBytes, first.RealmID, D115BundleDigest(firstBytes)); err != nil {
		t.Fatalf("exact retry: %v", err)
	}

	second := testD115TrustBundle(t, 2)
	second.RealmID = first.RealmID
	second.Keys[0].State = "revoked"
	second.Keys[0].Audience = first.RealmID
	newPublic, newPrivate, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	_ = newPrivate
	newID, err := d115.KeyID(newPublic)
	if err != nil {
		t.Fatal(err)
	}
	second.Keys = append(second.Keys, d115.OperatorTrustKey{
		KeyID: newID, PublicKey: newPublic, Scope: d115.CreateDomainScope, Audience: first.RealmID,
		NotBefore: second.Keys[0].NotBefore, NotAfter: second.Keys[0].NotAfter, State: "active",
	})
	// The canonical array is ordered by key ID, independent of rotation order.
	if second.Keys[1].KeyID < second.Keys[0].KeyID {
		second.Keys[0], second.Keys[1] = second.Keys[1], second.Keys[0]
	}
	secondBytes, err := d115.EncodeOperatorTrustBundle(second)
	if err != nil {
		t.Fatal(err)
	}
	if err = InstallD115OperatorTrustBundle(directory, secondBytes, second.RealmID, D115BundleDigest(firstBytes)); err == nil || err.Error() != "trust-bundle.confirmation-mismatch" {
		t.Fatalf("substituted bundle with the separately exported digest = %v", err)
	}
	writeFail := d115BundleInstallOps{writeTemp: func(string, []byte) (string, error) { return "", errors.New("injected temp write failure") }, rename: os.Rename, syncDirectory: syncD115BundleDirectory}
	if err = installD115OperatorTrustBundle(directory, secondBytes, second.RealmID, D115BundleDigest(secondBytes), writeFail); err == nil {
		t.Fatal("write failure accepted")
	}
	assertInstalledD115Bundle(t, installedPath, firstBytes)
	renameFail := d115BundleInstallOps{writeTemp: writeD115BundleTemp, rename: func(string, string) error { return errors.New("injected rename failure") }, syncDirectory: syncD115BundleDirectory}
	if err = installD115OperatorTrustBundle(directory, secondBytes, second.RealmID, D115BundleDigest(secondBytes), renameFail); err == nil {
		t.Fatal("rename failure accepted")
	}
	assertInstalledD115Bundle(t, installedPath, firstBytes)
	dirSyncFail := d115BundleInstallOps{writeTemp: writeD115BundleTemp, rename: os.Rename, syncDirectory: func(string) error { return errors.New("injected directory fsync failure") }}
	if err = installD115OperatorTrustBundle(directory, secondBytes, second.RealmID, D115BundleDigest(secondBytes), dirSyncFail); !errors.Is(err, ErrD115BundleDurabilityUnknown) {
		t.Fatalf("post-rename failure=%v", err)
	}
	assertInstalledD115Bundle(t, installedPath, secondBytes)
	if err = InstallD115OperatorTrustBundle(directory, secondBytes, second.RealmID, D115BundleDigest(secondBytes)); err != nil {
		t.Fatalf("durability-unknown exact recovery retry: %v", err)
	}
	if err = InstallD115OperatorTrustBundle(directory, firstBytes, first.RealmID, D115BundleDigest(firstBytes)); err == nil {
		t.Fatal("generation rollback accepted")
	}
	assertInstalledD115Bundle(t, installedPath, secondBytes)
	loaded, decoded, err := LoadD115OperatorTrustBundle(directory)
	if err != nil || decoded.Generation != 2 || len(loaded) == 0 {
		t.Fatalf("load installed bundle generation=%d err=%v", decoded.Generation, err)
	}
	info, err := os.Stat(installedPath)
	if err != nil || info.Mode().Perm() != 0o600 {
		t.Fatalf("installed mode=%v err=%v", info, err)
	}
}

func TestD115TrustBundleAndOwnerFilesRejectUnsafePaths(t *testing.T) {
	parent := t.TempDir()
	bundle := testD115TrustBundle(t, 1)
	data, err := d115.EncodeOperatorTrustBundle(bundle)
	if err != nil {
		t.Fatal(err)
	}
	digest := D115BundleDigest(data)
	loose := filepath.Join(parent, "loose")
	if err = os.Mkdir(loose, 0o755); err != nil {
		t.Fatal(err)
	}
	if err = InstallD115OperatorTrustBundle(loose, data, bundle.RealmID, digest); !errors.Is(err, ErrInvalidClientState) {
		t.Fatalf("world-accessible bundle store accepted: %v", err)
	}
	private := filepath.Join(parent, "private")
	if err = os.Mkdir(private, 0o700); err != nil {
		t.Fatal(err)
	}
	linked := filepath.Join(parent, "linked")
	if err = os.Symlink(private, linked); err != nil {
		t.Fatal(err)
	}
	if err = InstallD115OperatorTrustBundle(linked, data, bundle.RealmID, digest); !errors.Is(err, ErrInvalidClientState) {
		t.Fatalf("symlinked bundle store accepted: %v", err)
	}
	lockPath := filepath.Join(private, ".d115-operator-trust.lock")
	target := filepath.Join(parent, "lock-target")
	if err = os.WriteFile(target, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	if err = os.Symlink(target, lockPath); err != nil {
		t.Fatal(err)
	}
	if err = InstallD115OperatorTrustBundle(private, data, bundle.RealmID, digest); err == nil {
		t.Fatal("symlinked bundle lock accepted")
	}
	if err = os.Remove(lockPath); err != nil {
		t.Fatal(err)
	}
	if err = InstallD115OperatorTrustBundle(private, data, bundle.RealmID, digest); err != nil {
		t.Fatal(err)
	}
	installedPath := filepath.Join(private, d115BundleName)
	if err = os.Remove(installedPath); err != nil {
		t.Fatal(err)
	}
	if err = os.Symlink(target, installedPath); err != nil {
		t.Fatal(err)
	}
	if err = InstallD115OperatorTrustBundle(private, data, bundle.RealmID, digest); err == nil {
		t.Fatal("symlinked installed bundle accepted")
	}
	if _, _, err = LoadD115OperatorTrustBundle(private); err == nil {
		t.Fatal("symlinked installed bundle loaded")
	}
	ownerFile := filepath.Join(private, "owner-artifact")
	if err = os.Symlink(target, ownerFile); err != nil {
		t.Fatal(err)
	}
	if _, err = ReadD115OwnerFile(ownerFile, 1024); err == nil {
		t.Fatal("symlinked owner artifact read")
	}
	if err = WriteD115OwnerFile(ownerFile, []byte("replacement")); err == nil {
		t.Fatal("owner artifact writer replaced an existing path")
	}
	output := filepath.Join(private, "owner-output")
	if err = WriteD115OwnerFile(output, []byte("exact")); err != nil {
		t.Fatal(err)
	}
	if err = WriteD115OwnerFile(output, []byte("exact")); err != nil {
		t.Fatalf("exact existing owner output was not idempotent: %v", err)
	}
	if err = WriteD115OwnerFile(output, []byte("different")); err == nil {
		t.Fatal("conflicting owner output accepted")
	}
}

func testD115TrustBundle(t *testing.T, generation uint64) d115.OperatorTrustBundle {
	t.Helper()
	realm, err := d115.NewULID(time.Now().UTC())
	if err != nil {
		t.Fatal(err)
	}
	public, _, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	keyID, err := d115.KeyID(public)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC().Truncate(time.Second)
	return d115.OperatorTrustBundle{Schema: d115.TrustBundleSchema, RealmID: realm, Generation: generation, Enabled: true, Keys: []d115.OperatorTrustKey{{
		KeyID: keyID, PublicKey: public, Scope: d115.CreateDomainScope, Audience: realm, NotBefore: now.Add(-time.Minute).Format(time.RFC3339Nano), NotAfter: now.Add(time.Hour).Format(time.RFC3339Nano), State: "active",
	}}}
}

func assertInstalledD115Bundle(t *testing.T, path string, want []byte) {
	t.Helper()
	got, err := os.ReadFile(path)
	if err != nil || string(got) != string(want) {
		t.Fatalf("installed bundle bytes differ: err=%v", err)
	}
}
