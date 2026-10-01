package wipdremote

import (
	"crypto/ed25519"
	"crypto/rand"
	"crypto/sha256"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/hex"
	"math/big"
	"os"
	"path/filepath"
	"reflect"
	"testing"
	"time"

	"github.com/procrastivity/wip/internal/operation"
)

func TestSaveConfigExactRetryAndConflict(t *testing.T) {
	profileRoot := filepath.Join(t.TempDir(), "profile")
	if err := os.Mkdir(profileRoot, 0o700); err != nil {
		t.Fatal(err)
	}
	_, private, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	template := &x509.Certificate{
		SerialNumber: big.NewInt(1), Subject: pkix.Name{CommonName: "connected authority test"},
		NotBefore: now.Add(-time.Minute), NotAfter: now.Add(time.Hour), BasicConstraintsValid: true,
		IsCA: true, KeyUsage: x509.KeyUsageCertSign,
	}
	certificateDER, err := x509.CreateCertificate(rand.Reader, template, template, private.Public(), private)
	if err != nil {
		t.Fatal(err)
	}
	spki, err := x509.MarshalPKIXPublicKey(private.Public())
	if err != nil {
		t.Fatal(err)
	}
	pin := sha256.Sum256(spki)
	config := Config{
		Schema: "wipd.connected-authority-profile/2", Origin: "https://authority.example:8443",
		DomainID: "01KZ7XHAQT1S46NYPN1PW1DX3A", Epoch: 1, RepoID: "01KZ7XHAQT1S46NYPN1PW1DX3B",
		OwnerRootSPKI: "sha256:" + hex.EncodeToString(pin[:]), AuthoritySPKIPin: "sha256:" + hex.EncodeToString(pin[:]),
		AuthorityCertificateDER: certificateDER, OwnerRootPublicKey: private.Public().(ed25519.PublicKey),
		ArtifactKeyCertificate: []byte{1}, ClientStateDirectory: filepath.Join(t.TempDir(), "client-state"),
	}
	if err = SaveConfig(profileRoot, config); err != nil {
		t.Fatalf("save connected authority profile: %v", err)
	}
	if err = SaveConfig(profileRoot, config); err != nil {
		t.Fatalf("exact profile retry: %v", err)
	}
	loaded, err := LoadConfig(profileRoot)
	if err != nil || !reflect.DeepEqual(loaded, config) {
		t.Fatalf("loaded profile = %+v, %v; want %+v", loaded, err, config)
	}
	info, err := os.Stat(filepath.Join(profileRoot, configName))
	if err != nil || info.Mode().Perm() != 0o600 {
		t.Fatalf("profile file mode = %v, %v; want 0600", info, err)
	}

	conflicting := config
	conflicting.RepoID = "01KZ7XHAQT1S46NYPN1PW1DX3E"
	if err = SaveConfig(profileRoot, conflicting); err == nil {
		t.Fatal("conflicting profile replaced the pinned connected-authority configuration")
	}
	loaded, err = LoadConfig(profileRoot)
	if err != nil || !reflect.DeepEqual(loaded, config) {
		t.Fatalf("conflict changed saved profile = %+v, %v", loaded, err)
	}

	legacyRoot := filepath.Join(t.TempDir(), "legacy-profile")
	legacy := config
	legacy.Schema = "wipd.connected-authority-profile/1"
	legacy.OwnerRootPublicKey = nil
	legacy.ArtifactKeyCertificate = nil
	if err = SaveConfig(legacyRoot, legacy); err != nil {
		t.Fatalf("save legacy connected profile: %v", err)
	}
	loadedLegacy, err := LoadConfig(legacyRoot)
	if err != nil || !reflect.DeepEqual(loadedLegacy, legacy) {
		t.Fatalf("reopen legacy connected profile = %+v, %v; want %+v", loadedLegacy, err, legacy)
	}
	if (&Runtime{config: loadedLegacy}).SupportsClaimAcquisition() {
		t.Fatal("legacy connected profile enabled acquisition without grant-verification trust")
	}
}

func TestConnectedCommandCatalogueIsExplicitAndClosed(t *testing.T) {
	m5, err := registryForConfig(Config{})
	if err != nil {
		t.Fatal(err)
	}
	if hasCommand(m5, operation.MatterStartV1.Metadata().Operation) {
		t.Fatal("default M5 connected profile unexpectedly advertises M6 lifecycle commands")
	}
	m6, err := registryForConfig(Config{CommandCatalogue: "m6-step5"})
	if err != nil {
		t.Fatal(err)
	}
	if !hasCommand(m6, operation.MatterStartV1.Metadata().Operation) {
		t.Fatal("explicit M6 connected profile omitted matter.start")
	}
	if !hasCommand(m6, operation.StepCancelV1.Metadata().Operation) {
		t.Fatal("explicit M6 connected profile omitted step.cancel")
	}
	if !hasCommand(m6, operation.ContentWriteOnceV1.Metadata().Operation) ||
		!hasCommand(m6, operation.FindingAppendV1.Metadata().Operation) {
		t.Fatal("explicit M6 connected profile omitted content.write-once or finding.append")
	}
	if err = validateConfig(Config{Schema: "wipd.connected-authority-profile/2", CommandCatalogue: "m6-step4"}); err == nil {
		t.Fatal("accepted an unrecognized connected command catalogue")
	}
}

func hasCommand(registry *operation.Registry, command operation.ID) bool {
	for _, definition := range registry.Definitions() {
		if definition.Metadata().Operation == command {
			return true
		}
	}
	return false
}
