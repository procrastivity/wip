package wipdseed

import (
	"bytes"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/sha256"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/asn1"
	"encoding/hex"
	"math/big"
	"net/url"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestEnvironmentCredentialCreateOnlyPrivatePersistence(t *testing.T) {
	directory := t.TempDir()
	if err := os.Chmod(directory, 0o700); err != nil {
		t.Fatal(err)
	}
	credential := EnvironmentCredential{
		Schema: "wipd.environment-credential/1", DomainID: "domain", Epoch: 1,
		EnvironmentID: "environment", OwnerKeyID: "owner", SPKIDigest: "spki",
		PrivateKeyPKCS8: []byte("secret-key"), CertificateDER: [][]byte{[]byte("leaf"), []byte("ca")},
	}
	if err := installEnvironmentCredential(directory, credential); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(directory, environmentCredentialName)
	info, err := os.Lstat(path)
	if err != nil || !info.Mode().IsRegular() || info.Mode().Perm() != 0o600 {
		t.Fatalf("credential file info = %v, %v", info, err)
	}
	loaded, err := loadEnvironmentCredential(directory)
	if err != nil || !bytes.Equal(loaded.PrivateKeyPKCS8, credential.PrivateKeyPKCS8) {
		t.Fatalf("load credential = %#v, %v", loaded, err)
	}
	clear(loaded.PrivateKeyPKCS8)
	state := ClientState{
		DomainID: credential.DomainID, Epoch: credential.Epoch, EnvironmentID: credential.EnvironmentID,
		OwnerKeyID: credential.OwnerKeyID, SPKIDigest: credential.SPKIDigest,
		PrivateKeyPKCS8: bytes.Clone(credential.PrivateKeyPKCS8), CertificateDER: clone2D(credential.CertificateDER),
	}
	if err = validateStateCredential(directory, state); err != nil {
		t.Fatalf("matching Repo-bound state rejected: %v", err)
	}
	state.EnvironmentID = "different-environment"
	if err = validateStateCredential(directory, state); err == nil {
		t.Fatal("Repo-bound state drift from canonical credential was accepted")
	}
	clear(state.PrivateKeyPKCS8)
	if err = installEnvironmentCredential(directory, credential); err != nil {
		t.Fatalf("exact retry: %v", err)
	}
	conflict := credential
	conflict.PrivateKeyPKCS8 = []byte("different-secret")
	if err = installEnvironmentCredential(directory, conflict); err != ErrStateExists {
		t.Fatalf("conflicting retry error = %v, want ErrStateExists", err)
	}

	linkDirectory := t.TempDir()
	if err = os.Symlink(path, filepath.Join(linkDirectory, environmentCredentialName)); err != nil {
		t.Fatal(err)
	}
	if _, err = loadEnvironmentCredential(linkDirectory); err == nil {
		t.Fatal("credential symlink was accepted")
	}
}

func TestEnvironmentCredentialCertificateValidatesIdentityAndKey(t *testing.T) {
	now := time.Now().UTC().Truncate(time.Second)
	caPublic, caPrivate, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	caTemplate := &x509.Certificate{
		SerialNumber: big.NewInt(1), Subject: pkix.Name{CommonName: "environment CA"},
		NotBefore: now.Add(-time.Hour), NotAfter: now.Add(time.Hour), IsCA: true,
		BasicConstraintsValid: true, MaxPathLenZero: true, KeyUsage: x509.KeyUsageCertSign,
	}
	caDER, err := x509.CreateCertificate(rand.Reader, caTemplate, caTemplate, caPublic, caPrivate)
	if err != nil {
		t.Fatal(err)
	}
	ca, err := x509.ParseCertificate(caDER)
	if err != nil {
		t.Fatal(err)
	}
	public, private, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	keyDER, err := x509.MarshalPKCS8PrivateKey(private)
	if err != nil {
		t.Fatal(err)
	}
	publicDER, err := x509.MarshalPKIXPublicKey(public)
	if err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256(publicDER)
	ownerDigest := sha256.Sum256([]byte("owner"))
	ownerKeyID := "sha256:" + hex.EncodeToString(ownerDigest[:])
	environmentID, domainID, epoch := "01ARZ3NDEKTSV4RRFFQ69G5FAV", "01ARZ3NDEKTSV4RRFFQ69G5FAV", uint64(1)
	uri, err := url.Parse("wipd://environment/" + environmentID + "?domain=" + domainID + "&epoch=1&owner=" + hex.EncodeToString(ownerDigest[:]))
	if err != nil {
		t.Fatal(err)
	}
	leafTemplate := &x509.Certificate{
		SerialNumber: big.NewInt(2), Subject: pkix.Name{CommonName: "wipd Environment"},
		NotBefore: now.Add(-time.Minute), NotAfter: now.Add(30 * time.Minute),
		BasicConstraintsValid: true, KeyUsage: x509.KeyUsageDigitalSignature,
		ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageClientAuth}, URIs: []*url.URL{uri},
		ExtraExtensions: []pkix.Extension{{Id: []int{2, 5, 29, 17}, Critical: true, Value: mustMarshalSAN(t, uri)}},
	}
	leafDER, err := x509.CreateCertificate(rand.Reader, leafTemplate, ca, public, caPrivate)
	if err != nil {
		t.Fatal(err)
	}
	leaf, err := x509.ParseCertificate(leafDER)
	if err != nil {
		t.Fatal(err)
	}
	credential := EnvironmentCredential{
		Schema: "wipd.environment-credential/1", DomainID: domainID, Epoch: epoch,
		EnvironmentID: environmentID, OwnerKeyID: ownerKeyID,
		SPKIDigest: "sha256:" + hex.EncodeToString(sum[:]), PrivateKeyPKCS8: keyDER,
		CertificateDER: [][]byte{leafDER, caDER},
	}
	key, gotLeaf, digest, err := validateCredentialCertificate(credential, caDER, now)
	if err != nil || gotLeaf.SerialNumber.Cmp(leaf.SerialNumber) != 0 || digest != credential.SPKIDigest {
		t.Fatalf("valid certificate rejected: leaf=%v digest=%q err=%v", gotLeaf, digest, err)
	}
	clear(key)
	wrongKey := credential
	_, otherPrivate, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	wrongKey.PrivateKeyPKCS8, err = x509.MarshalPKCS8PrivateKey(otherPrivate)
	if err != nil {
		t.Fatal(err)
	}
	if _, _, _, err = validateCredentialCertificate(wrongKey, caDER, now); err == nil {
		t.Fatal("mismatched private key was accepted")
	}
	wrongIdentity := credential
	wrongIdentity.EnvironmentID = "01ARZ3NDEKTSV4RRFFQ69G5FAW"
	if _, _, _, err = validateCredentialCertificate(wrongIdentity, caDER, now); err == nil {
		t.Fatal("certificate for a different Environment was accepted")
	}
}

func mustMarshalSAN(t *testing.T, uri *url.URL) []byte {
	t.Helper()
	value, err := asn1.Marshal([]asn1.RawValue{{Class: asn1.ClassContextSpecific, Tag: 6, Bytes: []byte(uri.String())}})
	if err != nil {
		t.Fatal(err)
	}
	return value
}
