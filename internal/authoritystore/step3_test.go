package authoritystore

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/asn1"
	"encoding/hex"
	"encoding/json"
	"errors"
	"math/big"
	"os"
	"path/filepath"
	"strconv"
	"sync"
	"testing"
	"time"
)

const (
	envA   = "01KZ7XHAQT1S46NYPN1PW1DX3B"
	envB   = "01KZ7XHAQT1S46NYPN1PW1DX3C"
	grantA = "01KZ7XHAQT1S46NYPN1PW1DX3D"
)

func key(label string) ed25519.PrivateKey {
	seed := sha256.Sum256([]byte(label))
	return ed25519.NewKeyFromSeed(seed[:])
}

func encodeTest(t *testing.T, value any) []byte {
	t.Helper()
	b, err := artifactEncoder.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func signedTest(t *testing.T, private ed25519.PrivateKey, kind, schema, domain, ownerID string, epoch uint64, payload map[string]any) []byte {
	t.Helper()
	p := encodeTest(t, payload)
	unsigned := map[string]any{
		"schema": "wipd.signed-artifact/1", "kind": kind, "domain_id": domain, "authority_epoch": epoch,
		"signer_role": "owner", "signer_key_id": ownerID, "key_generation": nil, "artifact_sequence": nil,
		"previous_artifact_digest": nil, "issued_at": "2026-09-23T12:00:00Z", "payload_schema": schema,
		"payload_digest": digestBytes(p), "payload": p,
	}
	preimage := append([]byte("wipd/signed-artifact/v1\x00"), encodeTest(t, unsigned)...)
	unsigned["signature"] = ed25519.Sign(private, preimage)
	return encodeTest(t, unsigned)
}

func caFixture(t *testing.T, private ed25519.PrivateKey, now time.Time) []byte {
	return caFixtureWithSAN(t, private, now, nil)
}

func caFixtureWithSAN(t *testing.T, private ed25519.PrivateKey, now time.Time, names []asn1.RawValue) []byte {
	t.Helper()
	cert := &x509.Certificate{
		SerialNumber: big.NewInt(11), Subject: pkix.Name{CommonName: "delegated environment CA"},
		NotBefore: now.Add(-time.Hour), NotAfter: now.Add(7 * 24 * time.Hour),
		BasicConstraintsValid: true, IsCA: true, MaxPathLen: 0, MaxPathLenZero: true,
		KeyUsage: x509.KeyUsageCertSign,
	}
	if len(names) != 0 {
		san, err := asn1.Marshal(names)
		if err != nil {
			t.Fatal(err)
		}
		cert.ExtraExtensions = []pkix.Extension{{Id: sanOID, Critical: true, Value: san}}
	}
	der, err := x509.CreateCertificate(nil, cert, cert, private.Public(), private)
	if err != nil {
		t.Fatal(err)
	}
	return der
}

func leafFixture(t *testing.T, private, caPrivate ed25519.PrivateKey, caDER []byte, domain, environment, ownerID string, epoch uint64, now time.Time, serial int64) []byte {
	return leafFixtureCustom(t, private, caPrivate, caDER, certificateURI(domain, environment, epoch, ownerID), now, now.Add(time.Hour), serial, true)
}

func leafFixtureCustom(t *testing.T, private, caPrivate ed25519.PrivateKey, caDER []byte, uri string, now, notAfter time.Time, serial int64, critical bool) []byte {
	return leafFixtureCustomSAN(t, private, caPrivate, caDER, []asn1.RawValue{{Class: 2, Tag: 6, Bytes: []byte(uri)}}, now, notAfter, serial, critical)
}

func leafFixtureCustomSAN(t *testing.T, private, caPrivate ed25519.PrivateKey, caDER []byte, names []asn1.RawValue, now, notAfter time.Time, serial int64, critical bool) []byte {
	t.Helper()
	ca, err := x509.ParseCertificate(caDER)
	if err != nil {
		t.Fatal(err)
	}
	san, err := asn1.Marshal(names)
	if err != nil {
		t.Fatal(err)
	}
	cert := &x509.Certificate{
		SerialNumber: big.NewInt(serial), Subject: pkix.Name{CommonName: "environment"},
		NotBefore: now.Add(-time.Minute), NotAfter: notAfter, BasicConstraintsValid: true,
		KeyUsage: x509.KeyUsageDigitalSignature, ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageClientAuth},
		ExtraExtensions: []pkix.Extension{
			{Id: basicConstraintsOID, Critical: true, Value: []byte{0x30, 0x00}},
			{Id: keyUsageOID, Critical: true, Value: []byte{0x03, 0x02, 0x07, 0x80}},
			{Id: sanOID, Critical: critical, Value: san},
		},
	}
	der, err := x509.CreateCertificate(nil, cert, ca, private.Public(), caPrivate)
	if err != nil {
		t.Fatal(err)
	}
	return der
}

func TestCertificateProfilesRejectOtherSANGeneralNames(t *testing.T) {
	now := time.Date(2026, 9, 23, 12, 0, 0, 0, time.UTC)
	owner := key("strict-san-owner")
	caPrivate := key("strict-san-ca")
	otherName := asn1.RawValue{
		Class: 2, Tag: 0, IsCompound: true,
		Bytes: []byte{0x06, 0x02, 0x2a, 0x03, 0xa0, 0x03, 0x02, 0x01, 0x01},
	}
	caDER := caFixtureWithSAN(t, caPrivate, now, []asn1.RawValue{otherName})
	ca := mustCert(t, caDER)
	caID, err := spkiID(ca.PublicKey)
	if err != nil {
		t.Fatal(err)
	}
	_, err = verifyCA(caDelegationPayload{
		KeyID: caID, Certificate: caDER,
		NotBefore: ca.NotBefore.Format(time.RFC3339Nano), NotAfter: ca.NotAfter.Format(time.RFC3339Nano),
	}, owner.Public().(ed25519.PublicKey))
	if !errors.Is(err, ErrInvalidProof) {
		t.Fatalf("delegated CA accepted an otherwise-unparsed SAN name: %v", err)
	}

	validCA := caFixture(t, caPrivate, now)
	leafPrivate := key("strict-san-leaf")
	uri := certificateURI(domainA, envA, 7, digestBytes(mustSPKI(owner.Public().(ed25519.PublicKey))))
	leafDER := leafFixtureCustomSAN(t, leafPrivate, caPrivate, validCA, []asn1.RawValue{
		{Class: 2, Tag: 6, Bytes: []byte(uri)}, otherName,
	}, now, now.Add(time.Hour), 111, true)
	if _, _, err = verifyLeaf(leafDER, validCA, domainA, envA, digestBytes(mustSPKI(owner.Public().(ed25519.PublicKey))), 7, now); !errors.Is(err, ErrInvalidProof) {
		t.Fatalf("Environment leaf accepted an otherwise-unparsed SAN name: %v", err)
	}
}

func csrFixture(t *testing.T, private ed25519.PrivateKey, subject string) []byte {
	t.Helper()
	der, err := x509.CreateCertificateRequest(nil, &x509.CertificateRequest{Subject: pkix.Name{CommonName: subject}}, private)
	if err != nil {
		t.Fatal(err)
	}
	return der
}

func grantFixture(t *testing.T, owner ed25519.PrivateKey, d Domain, scope, id, environment string, private ed25519.PrivateKey, nonce byte) []byte {
	t.Helper()
	keyID, err := spkiID(private.Public())
	if err != nil {
		t.Fatal(err)
	}
	var prior any
	if scope == "environment-rotate" {
		prior = environment
	}
	return signedTest(t, owner, "enrollment-grant", "wipd.enrollment-grant/1", d.ID, d.OwnerKeyID, d.ActiveEpoch, map[string]any{
		"schema": "wipd.enrollment-grant/1", "grant_id": id, "domain_id": d.ID, "authority_epoch": d.ActiveEpoch,
		"owner_key_id": d.OwnerKeyID, "scope": scope, "requested_spki_digest": keyID, "prior_environment_id": prior,
		"nonce": bytes.Repeat([]byte{nonce}, 16), "issued_at": "2026-09-23T11:59:00Z", "expires_at": "2026-09-23T12:09:00Z",
	})
}

func TestV1ExplicitUpgradeAndRecovery(t *testing.T) {
	root := filepath.Join(t.TempDir(), "authority")
	if err := os.Mkdir(root, 0o700); err != nil {
		t.Fatal(err)
	}
	lock, err := acquire(root)
	if err != nil {
		t.Fatal(err)
	}
	db, err := connect(filepath.Join(root, "authority.db"), "rwc", true)
	if err != nil {
		t.Fatal(err)
	}
	if err = os.Chmod(filepath.Join(root, "authority.db"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err = installBaseline(db); err != nil {
		t.Fatal(err)
	}
	d, _ := identity(domainA, 7)
	if _, err = db.Exec(`INSERT INTO domains VALUES(?,?,?,?,?)`, d.ID, []byte(d.OwnerPublicKey), d.OwnerKeyID, 7, 7); err != nil {
		t.Fatal(err)
	}
	if _, err = db.Exec(`INSERT INTO repo_memberships VALUES(?,?)`, repoA, d.ID); err != nil {
		t.Fatal(err)
	}
	if err = db.Close(); err != nil {
		t.Fatal(err)
	}
	_ = lock.Close()
	if _, err = OpenExisting(root); !errors.Is(err, ErrInvalidStore) {
		t.Fatalf("ordinary open upgraded v1: %v", err)
	}
	backup := filepath.Join(root, "authority-v1.backup.db")
	if err = os.WriteFile(backup, nil, 0o600); err != nil {
		t.Fatal(err)
	} // crash after private file creation
	if err = UpgradeV1(root); !errors.Is(err, ErrInvalidStore) {
		t.Fatalf("accepted incomplete backup: %v", err)
	}
	if _, err = OpenExisting(root); !errors.Is(err, ErrInvalidStore) {
		t.Fatalf("failed upgrade altered v1: %v", err)
	}
	if err = os.Remove(backup); err != nil {
		t.Fatal(err)
	} // explicit inspection/recovery
	if err = UpgradeV1(root); err != nil {
		t.Fatal(err)
	}
	if err = UpgradeV1(root); !errors.Is(err, ErrInvalidStore) {
		t.Fatalf("second upgrade: %v", err)
	}
	b, err := connect(filepath.Join(root, "authority-v1.backup.db"), "rw", false)
	if err != nil {
		t.Fatal(err)
	}
	if err = checkSchemaVersion(b, 1); err != nil {
		t.Fatalf("v1 backup: %v", err)
	}
	_ = b.Close()
	if err = UpgradeV2(root); err != nil {
		t.Fatal(err)
	}
	if err = checkV3Root(root); err != nil {
		t.Fatal(err)
	}
	if err = UpgradeV3(root); err != nil {
		t.Fatal(err)
	}
	s, err := OpenExisting(root)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = s.Close() }()
	got, err := s.LookupDomain(context.Background(), d.ID)
	if err != nil || got.ActiveEpoch != 7 || got.OwnerKeyID != d.OwnerKeyID {
		t.Fatalf("upgraded identity: %+v: %v", got, err)
	}
	if _, err = s.db.Exec(`DROP TRIGGER environment_cert_no_delete`); err != nil {
		t.Fatal(err)
	}
	_ = s.Close()
	if _, err = OpenExisting(root); !errors.Is(err, ErrInvalidStore) {
		t.Fatalf("accepted altered v2 schema: %v", err)
	}
}

func TestStep3MigrationTransactionRollback(t *testing.T) {
	db, err := connect(filepath.Join(t.TempDir(), "v1.db"), "rwc", true)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = db.Close() }()
	if err = installBaseline(db); err != nil {
		t.Fatal(err)
	}
	if _, err = db.Exec(`CREATE TABLE environment_cas (wrong TEXT)`); err != nil {
		t.Fatal(err)
	}
	if err = installStep3(db); err == nil {
		t.Fatal("expected migration conflict")
	}
	var version int
	if err = db.QueryRow(`PRAGMA user_version`).Scan(&version); err != nil || version != 1 {
		t.Fatalf("partial version %d: %v", version, err)
	}
	var marker string
	if err = db.QueryRow(`SELECT name FROM schema_migrations WHERE version=1`).Scan(&marker); err != nil || marker != "baseline" {
		t.Fatalf("partial marker %s: %v", marker, err)
	}
	if err = db.QueryRow(`SELECT count(*) FROM schema_migrations WHERE version=2`).Scan(&version); err != nil || version != 0 {
		t.Fatalf("partial migration row %d: %v", version, err)
	}
}

func TestStep3DelegationEnrollmentAndFences(t *testing.T) {
	s, root := fresh(t)
	ctx := context.Background()
	now := time.Date(2026, 9, 23, 12, 0, 0, 0, time.UTC)
	d, owner := identity(domainA, 7)
	if err := s.BootstrapDomain(ctx, d, repoA); err != nil {
		t.Fatal(err)
	}
	caPrivate := key("CA-1")
	caDER := caFixture(t, caPrivate, now)
	caKeyID, _ := spkiID(caPrivate.Public())
	delegation := signedTest(t, owner, "environment-ca-delegation", "wipd.environment-ca-delegation/1", domainA, d.OwnerKeyID, 7, map[string]any{
		"schema": "wipd.environment-ca-delegation/1", "domain_id": domainA, "authority_epoch": uint64(7), "owner_key_id": d.OwnerKeyID,
		"ca_generation": uint64(1), "ca_key_id": caKeyID, "ca_certificate_der": caDER,
		"not_before": now.Add(-time.Hour).Format(time.RFC3339Nano), "not_after": now.Add(7 * 24 * time.Hour).Format(time.RFC3339Nano),
	})
	if err := s.InstallEnvironmentCA(ctx, domainA, delegation, now); err != nil {
		t.Fatalf("delegation: %v", err)
	}
	if err := s.InstallEnvironmentCA(ctx, domainB, delegation, now); err == nil {
		t.Fatal("cross-domain delegation accepted")
	}
	changed := bytes.Clone(delegation)
	changed[len(changed)-1] ^= 1
	if err := s.InstallEnvironmentCA(ctx, domainA, changed, now); !errors.Is(err, ErrInvalidProof) {
		t.Fatalf("changed signed field: %v", err)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	s, err := OpenExisting(root)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = s.Close() }()
	leafPrivate := key("leaf-1")
	csr := csrFixture(t, leafPrivate, "A")
	leaf := leafFixture(t, leafPrivate, caPrivate, caDER, domainA, envA, d.OwnerKeyID, 7, now, 101)
	grant := grantFixture(t, owner, d, "environment-enroll", grantA, envA, leafPrivate, 1)
	uri := certificateURI(domainA, envA, 7, d.OwnerKeyID)
	for _, tc := range []struct {
		name, uri string
		expires   time.Time
		critical  bool
	}{
		{"wrong-domain", certificateURI(domainB, envA, 7, d.OwnerKeyID), now.Add(time.Hour), true},
		{"wrong-epoch", certificateURI(domainA, envA, 8, d.OwnerKeyID), now.Add(time.Hour), true},
		{"wrong-owner", certificateURI(domainA, envA, 7, "sha256:"+string(bytes.Repeat([]byte{'a'}, 64))), now.Add(time.Hour), true},
		{"unknown-query", uri + "&x=1", now.Add(time.Hour), true},
		{"noncritical-san", uri, now.Add(time.Hour), false},
		{"overlong-leaf", uri, now.Add(25 * time.Hour), true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			bad := leafFixtureCustom(t, leafPrivate, caPrivate, caDER, tc.uri, now, tc.expires, 105, tc.critical)
			if _, err := s.IssueEnvironmentCertificate(ctx, domainA, envA, grant, csr, [][]byte{bad, caDER}, now); err == nil {
				t.Fatal("invalid certificate issued")
			}
		})
	}
	if _, err := s.IssueEnvironmentCertificate(ctx, domainA, envA, grant, csr, [][]byte{leaf, caDER}, now.Add(11*time.Minute)); !errors.Is(err, ErrFenced) {
		t.Fatalf("expired grant: %v", err)
	}
	var countBefore int
	if err := s.db.QueryRow(`SELECT count(*) FROM enrollment_consumptions`).Scan(&countBefore); err != nil || countBefore != 0 {
		t.Fatalf("failed issuance consumed grant: %d: %v", countBefore, err)
	}
	issued, err := s.IssueEnvironmentCertificate(ctx, domainA, envA, grant, csr, [][]byte{leaf, caDER}, now)
	if err != nil || issued.Generation != 1 || issued.CAGeneration != 1 || !bytes.Equal(issued.Chain[0], leaf) {
		t.Fatalf("issued: %+v: %v", issued, err)
	}
	if _, err = s.db.Exec(`UPDATE environments SET sequence_head = 1 WHERE domain_id = ? AND environment_id = ?`, domainA, envA); err == nil {
		t.Fatal("ack-only Environment head advancement accepted")
	}
	var head int
	if err = s.db.QueryRow(`SELECT sequence_head FROM environments WHERE domain_id = ? AND environment_id = ?`, domainA, envA).Scan(&head); err != nil || head != 0 {
		t.Fatalf("sequence head after rejected advance: %d: %v", head, err)
	}
	peer := tls.ConnectionState{HandshakeComplete: true, PeerCertificates: []*x509.Certificate{mustCert(t, leaf), mustCert(t, caDER)}}
	if _, err = s.VerifyEnvironmentPeer(ctx, domainA, envA, 7, peer, now); err != nil {
		t.Fatalf("exchange: %v", err)
	}
	replayed, err := s.IssueEnvironmentCertificate(ctx, domainA, envA, grant, csr, nil, now.Add(24*time.Hour))
	if err != nil || !bytes.Equal(replayed.Chain[0], leaf) || replayed.Generation != 1 {
		t.Fatalf("lost response: %+v: %v", replayed, err)
	}
	changedCSR := csrFixture(t, leafPrivate, "B")
	if _, err = s.IssueEnvironmentCertificate(ctx, domainA, envA, grant, changedCSR, nil, now); !errors.Is(err, ErrFenced) {
		t.Fatalf("reused grant with different CSR: %v", err)
	}
	if _, err = s.IssueEnvironmentCertificate(ctx, domainA, envB, grant, csr, nil, now); !errors.Is(err, ErrFenced) {
		t.Fatalf("reused grant different Environment: %v", err)
	}
	otherKey := key("duplicate-ID-leaf")
	otherGrant := grantFixture(t, owner, d, "environment-enroll", repoB, envA, otherKey, 4)
	otherCSR := csrFixture(t, otherKey, "duplicate")
	otherLeaf := leafFixture(t, otherKey, caPrivate, caDER, domainA, envA, d.OwnerKeyID, 7, now, 106)
	if _, err = s.IssueEnvironmentCertificate(ctx, domainA, envA, otherGrant, otherCSR, [][]byte{otherLeaf, caDER}, now); !errors.Is(err, ErrExists) {
		t.Fatalf("duplicate Environment ID: %v", err)
	}
	var consumed int
	if err = s.db.QueryRow(`SELECT count(*) FROM enrollment_consumptions`).Scan(&consumed); err != nil || consumed != 1 {
		t.Fatalf("duplicate consumed grant: %d: %v", consumed, err)
	}
	if _, err = s.VerifyEnvironmentPeer(ctx, domainA, envA, 8, peer, now); !errors.Is(err, ErrFenced) {
		t.Fatalf("wrong epoch: %v", err)
	}
	if _, err = s.VerifyEnvironmentPeer(ctx, domainA, envA, 7, peer, now.Add(2*time.Hour)); err == nil {
		t.Fatal("expired leaf accepted")
	}
	renewalCSR := csrFixture(t, leafPrivate, "renewed")
	renewalLeaf := leafFixture(t, leafPrivate, caPrivate, caDER, domainA, envA, d.OwnerKeyID, 7, now, 103)
	renewed, err := s.RenewEnvironmentCertificate(ctx, domainA, envA, 7, peer, renewalCSR, [][]byte{renewalLeaf, caDER}, now)
	if err != nil || renewed.Generation != 2 || renewed.SPKIDigest != issued.SPKIDigest || !bytes.Equal(renewed.Chain[0], renewalLeaf) {
		t.Fatalf("same-key renewal: %+v: %v", renewed, err)
	}
	if _, err = s.VerifyEnvironmentPeer(ctx, domainA, envA, 7, peer, now); !errors.Is(err, ErrFenced) {
		t.Fatalf("old cert after renewal: %v", err)
	}
	peer = tls.ConnectionState{HandshakeComplete: true, PeerCertificates: []*x509.Certificate{mustCert(t, renewalLeaf), mustCert(t, caDER)}}
	if _, err = s.VerifyEnvironmentPeer(ctx, domainA, envA, 7, peer, now); err != nil {
		t.Fatalf("renewed peer: %v", err)
	}
	rotatedKey := key("leaf-rotated")
	rotationCSR := csrFixture(t, rotatedKey, "rotated")
	rotationLeaf := leafFixture(t, rotatedKey, caPrivate, caDER, domainA, envA, d.OwnerKeyID, 7, now, 104)
	rotationGrant := grantFixture(t, owner, d, "environment-rotate", envB, envA, rotatedKey, 2)
	rotated, err := s.IssueEnvironmentCertificate(ctx, domainA, envA, rotationGrant, rotationCSR, [][]byte{rotationLeaf, caDER}, now)
	if err != nil || rotated.Generation != 3 || rotated.SPKIDigest == issued.SPKIDigest || !bytes.Equal(rotated.Chain[0], rotationLeaf) {
		t.Fatalf("owner-authorized rotation: %+v: %v", rotated, err)
	}
	if _, err = s.VerifyEnvironmentPeer(ctx, domainA, envA, 7, peer, now); !errors.Is(err, ErrFenced) {
		t.Fatalf("old cert after rotation: %v", err)
	}
	peer = tls.ConnectionState{HandshakeComplete: true, PeerCertificates: []*x509.Certificate{mustCert(t, rotationLeaf), mustCert(t, caDER)}}
	if _, err = s.VerifyEnvironmentPeer(ctx, domainA, envA, 7, peer, now); err != nil {
		t.Fatalf("rotated peer: %v", err)
	}
	for _, file := range []string{"authority.db", "authority.db-wal"} {
		content, err := os.ReadFile(filepath.Join(root, file))
		if errors.Is(err, os.ErrNotExist) {
			continue
		}
		if err != nil {
			t.Fatal(err)
		}
		for _, private := range []ed25519.PrivateKey{owner, caPrivate, leafPrivate, rotatedKey} {
			if bytes.Contains(content, private) || bytes.Contains(content, private.Seed()) {
				t.Fatalf("private key in %s", file)
			}
		}
	}
	if err = s.Close(); err != nil {
		t.Fatal(err)
	}
	s, err = OpenExisting(root)
	if err != nil {
		t.Fatalf("reopen with leaf: %v", err)
	}
	defer func() { _ = s.Close() }()
	fence := signedTest(t, owner, "environment-ca-fence", "wipd.environment-ca-fence/1", domainA, d.OwnerKeyID, 7, map[string]any{
		"schema": "wipd.environment-ca-fence/1", "domain_id": domainA, "authority_epoch": uint64(7), "owner_key_id": d.OwnerKeyID,
		"ca_generation": uint64(1), "ca_key_id": caKeyID, "delegation_artifact_digest": digestBytes(append([]byte("wipd/artifact-digest/v1\x00"), delegation...)),
		"effective_at": now.Format(time.RFC3339Nano), "reason_digest": digest('a'),
	})
	if err = s.FenceEnvironmentCA(ctx, domainA, fence, now); err != nil {
		t.Fatal(err)
	}
	if _, err = s.VerifyEnvironmentPeer(ctx, domainA, envA, 7, peer, now); !errors.Is(err, ErrFenced) {
		t.Fatalf("fenced CA exchange: %v", err)
	}
	secondCA := key("CA-2")
	secondDER := caFixture(t, secondCA, now)
	secondID, _ := spkiID(secondCA.Public())
	secondDelegation := signedTest(t, owner, "environment-ca-delegation", "wipd.environment-ca-delegation/1", domainA, d.OwnerKeyID, 7, map[string]any{
		"schema": "wipd.environment-ca-delegation/1", "domain_id": domainA, "authority_epoch": uint64(7), "owner_key_id": d.OwnerKeyID,
		"ca_generation": uint64(2), "ca_key_id": secondID, "ca_certificate_der": secondDER,
		"not_before": now.Add(-time.Hour).Format(time.RFC3339Nano), "not_after": now.Add(7 * 24 * time.Hour).Format(time.RFC3339Nano),
	})
	if err = s.InstallEnvironmentCA(ctx, domainA, secondDelegation, now); err != nil {
		t.Fatalf("fenced CA rotation: %v", err)
	}
	newKey := key("new-environment")
	newCSR := csrFixture(t, newKey, "new")
	newLeaf := leafFixture(t, newKey, secondCA, secondDER, domainA, envB, d.OwnerKeyID, 7, now, 107)
	newGrant := grantFixture(t, owner, d, "environment-enroll", repoC, envB, newKey, 5)
	newCert, err := s.IssueEnvironmentCertificate(ctx, domainA, envB, newGrant, newCSR, [][]byte{newLeaf, secondDER}, now)
	if err != nil || newCert.CAGeneration != 2 || newCert.Generation != 1 {
		t.Fatalf("new CA issuance: %+v: %v", newCert, err)
	}
	newPeer := tls.ConnectionState{HandshakeComplete: true, PeerCertificates: []*x509.Certificate{mustCert(t, newLeaf), mustCert(t, secondDER)}}
	if _, err = s.VerifyEnvironmentPeer(ctx, domainA, envB, 7, newPeer, now); err != nil {
		t.Fatalf("new CA exchange: %v", err)
	}
	if _, err = s.VerifyEnvironmentPeer(ctx, domainA, envA, 7, peer, now); !errors.Is(err, ErrFenced) {
		t.Fatalf("prior CA after rotation: %v", err)
	}
	if err = s.RevokeEnvironmentCertificate(ctx, domainA, envA, 7, 3, now); err != nil {
		t.Fatal(err)
	}
	if err = s.RevokeEnvironmentCertificate(ctx, domainA, envA, 7, 3, now); !errors.Is(err, ErrFenced) {
		t.Fatalf("leaf revocation replay: %v", err)
	}
	if err = s.Close(); err != nil {
		t.Fatal(err)
	}
	s, err = OpenExisting(root)
	if err != nil {
		t.Fatalf("reopen CA rotation and leaf revocation: %v", err)
	}
	defer func() { _ = s.Close() }()
	if _, err = s.VerifyEnvironmentPeer(ctx, domainA, envB, 7, newPeer, now); err != nil {
		t.Fatalf("reopened current CA: %v", err)
	}
}

func mustCert(t *testing.T, der []byte) *x509.Certificate {
	t.Helper()
	c, err := x509.ParseCertificate(der)
	if err != nil {
		t.Fatal(err)
	}
	return c
}

func TestConcurrentGrantConsumption(t *testing.T) {
	s, _ := fresh(t)
	ctx := context.Background()
	now := time.Date(2026, 9, 23, 12, 0, 0, 0, time.UTC)
	d, owner := identity(domainA, 7)
	if err := s.BootstrapDomain(ctx, d, repoA); err != nil {
		t.Fatal(err)
	}
	caKey := key("concurrent-CA")
	caDER := caFixture(t, caKey, now)
	caID, _ := spkiID(caKey.Public())
	delegation := signedTest(t, owner, "environment-ca-delegation", "wipd.environment-ca-delegation/1", domainA, d.OwnerKeyID, 7, map[string]any{
		"schema": "wipd.environment-ca-delegation/1", "domain_id": domainA, "authority_epoch": uint64(7), "owner_key_id": d.OwnerKeyID,
		"ca_generation": uint64(1), "ca_key_id": caID, "ca_certificate_der": caDER,
		"not_before": now.Add(-time.Hour).Format(time.RFC3339Nano), "not_after": now.Add(7 * 24 * time.Hour).Format(time.RFC3339Nano),
	})
	if err := s.InstallEnvironmentCA(ctx, domainA, delegation, now); err != nil {
		t.Fatal(err)
	}
	leafKey := key("concurrent-leaf")
	csr := csrFixture(t, leafKey, "A")
	other := csrFixture(t, leafKey, "B")
	leaf := leafFixture(t, leafKey, caKey, caDER, domainA, envA, d.OwnerKeyID, 7, now, 102)
	grant := grantFixture(t, owner, d, "environment-enroll", grantA, envA, leafKey, 3)
	start := make(chan struct{})
	results := make(chan error, 20)
	var wg sync.WaitGroup
	for i := 0; i < 20; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			<-start
			input := csr
			if i%2 != 0 {
				input = other
			}
			_, err := s.IssueEnvironmentCertificate(ctx, domainA, envA, grant, input, [][]byte{leaf, caDER}, now)
			results <- err
		}(i)
	}
	close(start)
	wg.Wait()
	close(results)
	var succeeded int
	for err := range results {
		if err == nil {
			succeeded++
		} else if !errors.Is(err, ErrFenced) && !errors.Is(err, ErrInvalidProof) {
			t.Fatal(err)
		}
	}
	if succeeded == 0 {
		t.Fatal("no successful consumption")
	}
	var count int
	if err := s.db.QueryRow(`SELECT count(*) FROM environment_certificates`).Scan(&count); err != nil || count != 1 {
		t.Fatalf("raced cert count %d: %v", count, err)
	}
}

func TestArtifactKeyGenerationFenceAndSequenceBarrier(t *testing.T) {
	s, root := fresh(t)
	ctx := context.Background()
	now := time.Date(2026, 9, 23, 12, 0, 0, 0, time.UTC)
	d, owner := identity(domainA, 7)
	if err := s.BootstrapDomain(ctx, d, repoA); err != nil {
		t.Fatal(err)
	}
	certificate := func(generation uint64, private ed25519.PrivateKey) []byte {
		id, _ := spkiID(private.Public())
		return signedTest(t, owner, "authority-artifact-key", "wipd.authority-artifact-key/1", domainA, d.OwnerKeyID, 7, map[string]any{
			"schema": "wipd.authority-artifact-key/1", "domain_id": domainA, "authority_epoch": uint64(7), "key_generation": generation,
			"key_id": id, "ed25519_public_key": []byte(private.Public().(ed25519.PublicKey)),
			"not_before": now.Add(-time.Hour).Format(time.RFC3339Nano), "not_after": now.Add(time.Hour).Format(time.RFC3339Nano),
		})
	}
	first := key("artifact-1")
	cert1 := certificate(1, first)
	if err := s.RegisterArtifactKey(ctx, domainA, cert1, now); err != nil {
		t.Fatal(err)
	}
	if err := s.RegisterArtifactKey(ctx, domainA, certificate(3, key("artifact-3")), now); !errors.Is(err, ErrFenced) {
		t.Fatalf("generation gap: %v", err)
	}
	if err := s.RegisterArtifactKey(ctx, domainA, certificate(2, key("artifact-2")), now); !errors.Is(err, ErrFenced) {
		t.Fatalf("unfenced rotation: %v", err)
	}
	firstID, _ := spkiID(first.Public())
	fence := signedTest(t, owner, "authority-key-fence", "wipd.authority-key-fence/1", domainA, d.OwnerKeyID, 7, map[string]any{
		"schema": "wipd.authority-key-fence/1", "domain_id": domainA, "authority_epoch": uint64(7), "key_generation": uint64(1),
		"key_id": firstID, "final_sequence": uint64(0), "final_artifact_digest": nil,
		"effective_at": now.Format(time.RFC3339Nano), "reason_digest": digest('b'),
	})
	if err := s.FenceArtifactKey(ctx, domainA, fence); err != nil {
		t.Fatal(err)
	}
	if err := s.FenceArtifactKey(ctx, domainA, fence); !errors.Is(err, ErrFenced) {
		t.Fatalf("fence replay: %v", err)
	}
	second := certificate(2, key("artifact-2"))
	results := make(chan error, 2)
	for i := 0; i < 2; i++ {
		go func() { results <- s.RegisterArtifactKey(ctx, domainA, second, now) }()
	}
	a, b := <-results, <-results
	if (a == nil) == (b == nil) {
		t.Fatalf("rotation race: %v, %v", a, b)
	}
	k, err := s.LookupArtifactKey(ctx, domainA, 7, 2)
	if err != nil || k.Generation != 2 || !bytes.Equal(k.Certificate, second) {
		t.Fatalf("exact persisted generation: %+v: %v", k, err)
	}
	if err = s.Close(); err != nil {
		t.Fatal(err)
	}
	s, err = OpenExisting(root)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = s.Close() }()
	k, err = s.LookupArtifactKey(ctx, domainA, 7, 1)
	if err != nil || k.FinalSequence != 0 || k.FinalDigest != "" || !bytes.Equal(k.Fence, fence) {
		t.Fatalf("historical fence: %+v: %v", k, err)
	}
}

func normalizeVector(t *testing.T, value any) any {
	t.Helper()
	switch v := value.(type) {
	case json.Number:
		n, err := strconv.ParseUint(v.String(), 10, 64)
		if err != nil {
			t.Fatal(err)
		}
		return n
	case map[string]any:
		for k, child := range v {
			v[k] = normalizeVector(t, child)
		}
		return v
	case []any:
		for i, child := range v {
			v[i] = normalizeVector(t, child)
		}
		return v
	default:
		return value
	}
}

func TestSignedArtifactSealedGolden(t *testing.T) {
	read := func(path string) map[string]any {
		f, err := os.Open(path)
		if err != nil {
			t.Fatal(err)
		}
		defer func() { _ = f.Close() }()
		dec := json.NewDecoder(f)
		dec.UseNumber()
		var root map[string]any
		if err = dec.Decode(&root); err != nil {
			t.Fatal(err)
		}
		return root
	}
	receipt := read("../../docs/wipd/result-receipt-idempotency-vectors.json")["receipts"].(map[string]any)["accepted"].(map[string]any)
	result := receipt["result"].(map[string]any)
	output, err := hex.DecodeString(result["output_cbor_hex"].(string))
	if err != nil {
		t.Fatal(err)
	}
	delete(result, "output_cbor_hex")
	result["output"] = output
	payload := encodeTest(t, normalizeVector(t, receipt))
	vector := read("../../docs/wipd/design-security-privacy-vectors.json")["portable_signature"].(map[string]any)
	env := vector["envelope"].(map[string]any)
	if digestBytes(payload) != vector["payload_digest"] {
		t.Fatalf("fixture payload digest %s", digestBytes(payload))
	}
	sig, err := hex.DecodeString(vector["signature_hex"].(string))
	if err != nil {
		t.Fatal(err)
	}
	public, err := hex.DecodeString(vector["public_key_hex"].(string))
	if err != nil {
		t.Fatal(err)
	}
	fields := map[string]any{
		"schema": env["schema"], "kind": env["kind"], "domain_id": env["domain_id"], "authority_epoch": uint64(7),
		"signer_role": env["signer_role"], "signer_key_id": vector["signer_key_id"], "key_generation": uint64(2),
		"artifact_sequence": uint64(41), "previous_artifact_digest": env["previous_artifact_digest"],
		"issued_at": env["issued_at"], "payload_schema": env["payload_schema"], "payload_digest": vector["payload_digest"], "payload": payload, "signature": sig,
	}
	wrapper := encodeTest(t, fields)
	prior := env["previous_artifact_digest"].(string)
	domain := env["domain_id"].(string)
	id := vector["signer_key_id"].(string)
	got, err := verifyAuthorityArtifact(wrapper, public, domain, id, 7, 2, 41, &prior)
	if err != nil || got != vector["artifact_digest"] {
		t.Fatalf("golden digest %s: %v", got, err)
	}
	for _, tc := range []struct {
		name            string
		epoch, gen, seq uint64
		previous        *string
	}{
		{"wrong-epoch", 8, 2, 41, &prior}, {"wrong-generation", 7, 1, 41, &prior}, {"sequence-gap", 7, 2, 42, &prior}, {"wrong-predecessor", 7, 2, 41, nil},
	} {
		if _, err := verifyAuthorityArtifact(wrapper, public, domain, id, tc.epoch, tc.gen, tc.seq, tc.previous); !errors.Is(err, ErrInvalidProof) {
			t.Fatalf("%s: %v", tc.name, err)
		}
	}
	fields["domain_id"] = domainA
	if _, err := verifyAuthorityArtifact(encodeTest(t, fields), public, domainA, id, 7, 2, 41, &prior); !errors.Is(err, ErrInvalidProof) {
		t.Fatalf("changed covered domain: %v", err)
	}
}

func TestRetainedArtifactChainAndFinalFence(t *testing.T) {
	s, root := fresh(t)
	ctx := context.Background()
	now := time.Date(2026, 9, 23, 12, 0, 0, 0, time.UTC)
	d, owner := identity(domainA, 7)
	if err := s.BootstrapDomain(ctx, d, repoA); err != nil {
		t.Fatal(err)
	}
	signer := key("chain-signer")
	id, _ := spkiID(signer.Public())
	certificate := signedTest(t, owner, "authority-artifact-key", "wipd.authority-artifact-key/1", domainA, d.OwnerKeyID, 7, map[string]any{
		"schema": "wipd.authority-artifact-key/1", "domain_id": domainA, "authority_epoch": uint64(7), "key_generation": uint64(1),
		"key_id": id, "ed25519_public_key": []byte(signer.Public().(ed25519.PublicKey)),
		"not_before": now.Add(-time.Hour).Format(time.RFC3339Nano), "not_after": now.Add(time.Hour).Format(time.RFC3339Nano),
	})
	if err := s.RegisterArtifactKey(ctx, domainA, certificate, now); err != nil {
		t.Fatal(err)
	}
	sign := func(sequence uint64, predecessor any) []byte {
		payload := encodeTest(t, map[string]any{"schema": "wipd.blob-manifest/1"})
		fields := map[string]any{
			"schema": "wipd.signed-artifact/1", "kind": "blob-manifest", "domain_id": domainA, "authority_epoch": uint64(7),
			"signer_role": "authority", "signer_key_id": id, "key_generation": uint64(1), "artifact_sequence": sequence,
			"previous_artifact_digest": predecessor, "issued_at": now.Format(time.RFC3339Nano),
			"payload_schema": "wipd.blob-manifest/1", "payload_digest": digestBytes(payload), "payload": payload,
		}
		fields["signature"] = ed25519.Sign(signer, append([]byte("wipd/signed-artifact/v1\x00"), encodeTest(t, fields)...))
		return encodeTest(t, fields)
	}
	insert := func(seq uint64, predecessor any, wrapper []byte) error {
		var prior any
		if predecessor != nil {
			prior = predecessor
		}
		_, err := s.db.Exec(`INSERT INTO authority_artifacts(domain_id,epoch,generation,sequence,digest,predecessor,wrapper) VALUES(?,?,?,?,?,?,?)`, domainA, 7, 1, seq, digestBytes(append([]byte("wipd/artifact-digest/v1\x00"), wrapper...)), prior, wrapper)
		return err
	}
	one := sign(1, nil)
	if err := insert(1, nil, one); err != nil {
		t.Fatal(err)
	}
	head := digestBytes(append([]byte("wipd/artifact-digest/v1\x00"), one...))
	if err := insert(3, head, sign(3, head)); err == nil {
		t.Fatal("accepted artifact sequence gap")
	}
	two := sign(2, head)
	if err := insert(2, head, two); err != nil {
		t.Fatal(err)
	}
	final := digestBytes(append([]byte("wipd/artifact-digest/v1\x00"), two...))
	fence := signedTest(t, owner, "authority-key-fence", "wipd.authority-key-fence/1", domainA, d.OwnerKeyID, 7, map[string]any{
		"schema": "wipd.authority-key-fence/1", "domain_id": domainA, "authority_epoch": uint64(7), "key_generation": uint64(1),
		"key_id": id, "final_sequence": uint64(2), "final_artifact_digest": final,
		"effective_at": now.Format(time.RFC3339Nano), "reason_digest": digest('c'),
	})
	if err := s.FenceArtifactKey(ctx, domainA, fence); err != nil {
		t.Fatal(err)
	}
	if err := insert(3, final, sign(3, final)); err == nil {
		t.Fatal("accepted post-fence artifact")
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	// Step 4 refuses a standalone signed product, even one with a valid
	// signature and fence, because it has no owning terminal transaction.
	if _, err := OpenExisting(root); !errors.Is(err, ErrInvalidStore) {
		t.Fatalf("accepted orphan chain: %v", err)
	}
}

func TestReopenRejectsTamperedOwnerDelegationWithExactSchema(t *testing.T) {
	s, root := fresh(t)
	ctx := context.Background()
	now := time.Date(2026, 9, 23, 12, 0, 0, 0, time.UTC)
	d, owner := identity(domainA, 7)
	if err := s.BootstrapDomain(ctx, d, repoA); err != nil {
		t.Fatal(err)
	}
	caPrivate := key("corruption-CA")
	caDER := caFixture(t, caPrivate, now)
	caID, _ := spkiID(caPrivate.Public())
	delegation := signedTest(t, owner, "environment-ca-delegation", "wipd.environment-ca-delegation/1", domainA, d.OwnerKeyID, 7, map[string]any{
		"schema": "wipd.environment-ca-delegation/1", "domain_id": domainA, "authority_epoch": uint64(7), "owner_key_id": d.OwnerKeyID,
		"ca_generation": uint64(1), "ca_key_id": caID, "ca_certificate_der": caDER,
		"not_before": now.Add(-time.Hour).Format(time.RFC3339Nano), "not_after": now.Add(7 * 24 * time.Hour).Format(time.RFC3339Nano),
	})
	if err := s.InstallEnvironmentCA(ctx, domainA, delegation, now); err != nil {
		t.Fatal(err)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	db, err := connect(filepath.Join(root, "authority.db"), "rw", false)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = db.Exec(`DROP TRIGGER environment_ca_identity_immutable`); err != nil {
		t.Fatal(err)
	}
	changed := bytes.Clone(delegation)
	changed[len(changed)-1] ^= 1
	if _, err = db.Exec(`UPDATE environment_cas SET delegation=? WHERE domain_id=?`, changed, domainA); err != nil {
		t.Fatal(err)
	}
	for _, object := range step3Schema {
		if object.name == "environment_ca_identity_immutable" {
			if _, err = db.Exec(object.sql); err != nil {
				t.Fatal(err)
			}
		}
	}
	if err = checkSchemaVersion(db, 3); err == nil {
		t.Fatal("accepted tampered delegation under exact schema")
	}
	if err = db.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err = OpenExisting(root); !errors.Is(err, ErrInvalidStore) {
		t.Fatalf("opened tampered owner proof: %v", err)
	}
}
