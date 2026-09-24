package authoritystore

import (
	"bytes"
	"context"
	"crypto/tls"
	"crypto/x509"
	"database/sql"
	"errors"
	"fmt"
	"time"
)

const maxLeafLifetime = 24 * time.Hour

type enrollmentGrant struct {
	Schema     string  `cbor:"schema"`
	ID         string  `cbor:"grant_id"`
	DomainID   string  `cbor:"domain_id"`
	Epoch      uint64  `cbor:"authority_epoch"`
	OwnerKeyID string  `cbor:"owner_key_id"`
	Scope      string  `cbor:"scope"`
	SPKIDigest string  `cbor:"requested_spki_digest"`
	PriorID    *string `cbor:"prior_environment_id"`
	Nonce      []byte  `cbor:"nonce"`
	IssuedAt   string  `cbor:"issued_at"`
	ExpiresAt  string  `cbor:"expires_at"`
}

// EnvironmentCertificate is a copy of the exact issued leaf and its one CA
// certificate, in the protocol's leaf-then-CA order.
type EnvironmentCertificate struct {
	DomainID, EnvironmentID         string
	Epoch, Generation, CAGeneration uint64
	SPKIDigest                      string
	Chain                           [][]byte
	Revoked                         bool
}

func certificateURI(domain, environment string, epoch uint64, ownerKeyID string) string {
	return fmt.Sprintf("wipd://environment/%s?domain=%s&epoch=%d&owner=%s", environment, domain, epoch, ownerKeyID[len("sha256:"):])
}

func verifyLeaf(leafDER, caDER []byte, domain, environment, ownerKeyID string, epoch uint64, at time.Time) (*x509.Certificate, string, error) {
	leaf, err := x509.ParseCertificate(leafDER)
	if err != nil {
		return nil, "", ErrInvalidProof
	}
	ca, err := x509.ParseCertificate(caDER)
	if err != nil {
		return nil, "", ErrInvalidProof
	}
	if leaf.IsCA || !leaf.BasicConstraintsValid || !hasCritical(leaf, basicConstraintsOID) || !hasCritical(leaf, keyUsageOID) || leaf.KeyUsage != x509.KeyUsageDigitalSignature || len(leaf.ExtKeyUsage) != 1 || leaf.ExtKeyUsage[0] != x509.ExtKeyUsageClientAuth || len(leaf.UnknownExtKeyUsage) != 0 || len(leaf.URIs) != 1 || len(leaf.DNSNames) != 0 || len(leaf.EmailAddresses) != 0 || len(leaf.IPAddresses) != 0 || !hasOnlyCriticalURISAN(leaf, certificateURI(domain, environment, epoch, ownerKeyID)) || leaf.CheckSignatureFrom(ca) != nil || len(leaf.RawSubjectPublicKeyInfo) == 0 || bytes.Equal(leaf.RawSubjectPublicKeyInfo, ca.RawSubjectPublicKeyInfo) || leaf.NotBefore.Before(ca.NotBefore) || leaf.NotAfter.After(ca.NotAfter) || !leaf.NotBefore.Before(leaf.NotAfter) || leaf.NotAfter.Sub(leaf.NotBefore) > maxLeafLifetime {
		return nil, "", ErrInvalidProof
	}
	if !at.IsZero() {
		if at.Before(leaf.NotBefore) || !at.Before(leaf.NotAfter) || at.Before(ca.NotBefore) || !at.Before(ca.NotAfter) {
			return nil, "", ErrFenced
		}
	}
	roots := x509.NewCertPool()
	roots.AddCert(ca)
	if _, err = leaf.Verify(x509.VerifyOptions{Roots: roots, CurrentTime: leaf.NotBefore, KeyUsages: []x509.ExtKeyUsage{x509.ExtKeyUsageClientAuth}}); err != nil {
		return nil, "", ErrInvalidProof
	}
	return leaf, digestBytes(leaf.RawSubjectPublicKeyInfo), nil
}

func csrProof(der []byte) (*x509.CertificateRequest, string, error) {
	csr, err := x509.ParseCertificateRequest(der)
	if err != nil || csr.CheckSignature() != nil || !bytes.Equal(der, csr.Raw) {
		return nil, "", ErrInvalidProof
	}
	return csr, digestBytes(csr.RawSubjectPublicKeyInfo), nil
}

// IssueEnvironmentCertificate binds a verified owner grant to one exact CSR
// and one leaf-then-delegated-CA chain. The restricted issuer signs externally;
// the store verifies the produced certificate before consuming the grant.
// Identical grant+CSR retry returns the persisted chain, never a replacement.
func (s *Store) IssueEnvironmentCertificate(ctx context.Context, domain, environment string, grant, csrDER []byte, chain [][]byte, at time.Time) (EnvironmentCertificate, error) {
	var out EnvironmentCertificate
	if !ulid.MatchString(environment) {
		return out, ErrInvalidProof
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.db == nil {
		return out, errors.New("authoritystore: closed")
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return out, err
	}
	defer func() { _ = tx.Rollback() }()
	d, err := domainOwner(ctx, tx, domain)
	if err != nil {
		return out, err
	}
	payload, err := ownerArtifact(grant, d.OwnerPublicKey, domain, d.OwnerKeyID, "enrollment-grant", "wipd.enrollment-grant/1", d.ActiveEpoch)
	if err != nil {
		return out, err
	}
	var p enrollmentGrant
	if err = closedPayload(payload, &p, "schema", "grant_id", "domain_id", "authority_epoch", "owner_key_id", "scope", "requested_spki_digest", "prior_environment_id", "nonce", "issued_at", "expires_at"); err != nil {
		return out, err
	}
	if p.Schema != "wipd.enrollment-grant/1" || !ulid.MatchString(p.ID) || p.DomainID != domain || p.Epoch != d.ActiveEpoch || p.OwnerKeyID != d.OwnerKeyID || len(p.Nonce) != 16 || !validDigest(p.SPKIDigest) {
		return out, ErrInvalidProof
	}
	_, spki, err := csrProof(csrDER)
	if err != nil || spki != p.SPKIDigest {
		return out, ErrInvalidProof
	}
	csrDigest := digestBytes(csrDER)
	var usedDigest, usedCSR, usedID string
	var usedGen uint64
	err = tx.QueryRowContext(ctx, `SELECT grant_digest,csr_digest,environment_id,generation FROM enrollment_consumptions WHERE domain_id = ? AND grant_id = ?`, domain, p.ID).Scan(&usedDigest, &usedCSR, &usedID, &usedGen)
	if err == nil {
		if usedDigest != digestBytes(grant) || usedCSR != csrDigest || usedID != environment {
			return out, ErrFenced
		}
		out, err = readCertificate(ctx, tx, domain, environment, usedGen)
		if err != nil {
			return out, err
		}
		return out, tx.Commit()
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return out, err
	}
	if err = checkWriteAdmission(ctx, tx, domain, d.ActiveEpoch); err != nil {
		return out, err
	}
	if interval(p.IssuedAt, p.ExpiresAt, at.UTC()) != nil {
		return out, ErrFenced
	}
	start, _ := utcTime(p.IssuedAt)
	end, _ := utcTime(p.ExpiresAt)
	if end.Sub(start) > 10*time.Minute {
		return out, ErrInvalidProof
	}
	if (p.Scope == "environment-enroll" && p.PriorID != nil) || (p.Scope == "environment-rotate" && (p.PriorID == nil || *p.PriorID != environment)) || (p.Scope != "environment-enroll" && p.Scope != "environment-rotate") {
		return out, ErrInvalidProof
	}
	var oldGen uint64
	err = tx.QueryRowContext(ctx, `SELECT generation FROM environments WHERE domain_id = ? AND environment_id = ?`, domain, environment).Scan(&oldGen)
	if p.Scope == "environment-enroll" && err == nil {
		return out, ErrExists
	}
	if p.Scope == "environment-rotate" && errors.Is(err, sql.ErrNoRows) {
		return out, ErrNotFound
	}
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return out, err
	}
	if p.Scope == "environment-rotate" {
		previous, err := readCertificate(ctx, tx, domain, environment, oldGen)
		if err != nil {
			return out, err
		}
		if previous.Revoked || previous.Epoch != d.ActiveEpoch || previous.SPKIDigest == spki {
			return out, ErrFenced
		}
	}
	caEpoch, caGeneration, caDER, err := currentCA(ctx, tx, domain, d.ActiveEpoch, at)
	if err != nil {
		return out, err
	}
	if len(chain) != 2 || !bytes.Equal(chain[1], caDER) {
		return out, ErrInvalidProof
	}
	leaf, leafSPKI, err := verifyLeaf(chain[0], caDER, domain, environment, d.OwnerKeyID, d.ActiveEpoch, at)
	if err != nil || leafSPKI != spki || restrictedKey(ctx, tx, domain, leafSPKI, d.OwnerKeyID) {
		return out, ErrInvalidProof
	}
	if oldGen == 0 {
		if _, err = tx.ExecContext(ctx, `INSERT INTO environments(domain_id,environment_id,epoch,generation) VALUES(?,?,?,1)`, domain, environment, d.ActiveEpoch); err != nil {
			return out, writeError(err)
		}
	} else {
		if _, err = tx.ExecContext(ctx, `UPDATE environments SET generation = ? WHERE domain_id = ? AND environment_id = ?`, oldGen+1, domain, environment); err != nil {
			return out, err
		}
	}
	gen := oldGen + 1
	if err = insertLeaf(ctx, tx, domain, environment, gen, caEpoch, caGeneration, leaf, chain, leafSPKI); err != nil {
		return out, err
	}
	if oldGen != 0 {
		if _, err = tx.ExecContext(ctx, `UPDATE environment_certificates SET revoked_at = ? WHERE domain_id = ? AND environment_id = ? AND generation = ? AND revoked_at IS NULL`, at.UTC().Format(time.RFC3339Nano), domain, environment, oldGen); err != nil {
			return out, err
		}
	}
	if _, err = tx.ExecContext(ctx, `INSERT INTO enrollment_consumptions(domain_id,grant_id,nonce,grant_digest,scope,csr_digest,environment_id,generation) VALUES(?,?,?,?,?,?,?,?)`, domain, p.ID, p.Nonce, digestBytes(grant), p.Scope, csrDigest, environment, gen); err != nil {
		return out, writeError(err)
	}
	if err = tx.Commit(); err != nil {
		return out, err
	}
	return EnvironmentCertificate{domain, environment, d.ActiveEpoch, gen, caGeneration, spki, [][]byte{bytes.Clone(chain[0]), bytes.Clone(chain[1])}, false}, nil
}

func currentCA(ctx context.Context, tx *sql.Tx, domain string, epoch uint64, at time.Time) (uint64, uint64, []byte, error) {
	var caEpoch, gen uint64
	var cert, fence []byte
	var start, end string
	err := tx.QueryRowContext(ctx, `SELECT epoch,generation,certificate,fence,not_before,not_after FROM environment_cas WHERE domain_id = ? AND epoch = ? ORDER BY generation DESC LIMIT 1`, domain, epoch).Scan(&caEpoch, &gen, &cert, &fence, &start, &end)
	if errors.Is(err, sql.ErrNoRows) {
		return 0, 0, nil, ErrFenced
	}
	if err != nil {
		return 0, 0, nil, err
	}
	if fence != nil || interval(start, end, at.UTC()) != nil {
		return 0, 0, nil, ErrFenced
	}
	return caEpoch, gen, cert, nil
}

func insertLeaf(ctx context.Context, tx *sql.Tx, domain, environment string, gen, caEpoch, caGen uint64, leaf *x509.Certificate, chain [][]byte, spki string) error {
	_, err := tx.ExecContext(ctx, `INSERT INTO environment_certificates(domain_id,environment_id,generation,ca_epoch,ca_generation,serial,spki_digest,certificate,ca_certificate,not_before,not_after) VALUES(?,?,?,?,?,?,?,?,?,?,?)`, domain, environment, gen, caEpoch, caGen, leaf.SerialNumber.Text(16), spki, chain[0], chain[1], leaf.NotBefore.UTC().Format(time.RFC3339Nano), leaf.NotAfter.UTC().Format(time.RFC3339Nano))
	return writeError(err)
}

func restrictedKey(ctx context.Context, tx *sql.Tx, domain, id, ownerID string) bool {
	if id == ownerID {
		return true
	}
	var n int
	err := tx.QueryRowContext(ctx, `SELECT (SELECT count(*) FROM artifact_keys WHERE domain_id = ? AND key_id = ?) + (SELECT count(*) FROM environment_cas WHERE domain_id = ? AND key_id = ?)`, domain, id, domain, id).Scan(&n)
	return err != nil || n != 0
}

func readCertificate(ctx context.Context, tx *sql.Tx, domain, environment string, gen uint64) (EnvironmentCertificate, error) {
	var out EnvironmentCertificate
	var leaf, ca []byte
	var revoked sql.NullString
	err := tx.QueryRowContext(ctx, `SELECT e.epoch,c.generation,c.ca_generation,c.spki_digest,c.certificate,c.ca_certificate,c.revoked_at FROM environments e JOIN environment_certificates c USING(domain_id,environment_id) WHERE e.domain_id = ? AND e.environment_id = ? AND c.generation = ?`, domain, environment, gen).Scan(&out.Epoch, &out.Generation, &out.CAGeneration, &out.SPKIDigest, &leaf, &ca, &revoked)
	if errors.Is(err, sql.ErrNoRows) {
		return out, ErrNotFound
	}
	if err != nil {
		return out, err
	}
	out.DomainID, out.EnvironmentID, out.Chain, out.Revoked = domain, environment, [][]byte{leaf, ca}, revoked.Valid
	return out, nil
}

// VerifyEnvironmentPeer is called before *each* exchange, not only once at
// handshake. The caller must pass the actual completed TLS connection state:
// TLS establishes private-key possession, while the store checks current
// registry, certificate and CA fences under the same serialization lane.
func (s *Store) VerifyEnvironmentPeer(ctx context.Context, domain, environment string, epoch uint64, peer tls.ConnectionState, at time.Time) (EnvironmentCertificate, error) {
	var out EnvironmentCertificate
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.db == nil {
		return out, errors.New("authoritystore: closed")
	}
	if !peer.HandshakeComplete || len(peer.PeerCertificates) != 2 {
		return out, ErrInvalidProof
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return out, err
	}
	defer func() { _ = tx.Rollback() }()
	out, err = verifyPeer(ctx, tx, domain, environment, epoch, peer, at)
	if err != nil {
		return out, err
	}
	return out, tx.Commit()
}

func verifyPeer(ctx context.Context, tx *sql.Tx, domain, environment string, epoch uint64, peer tls.ConnectionState, at time.Time) (EnvironmentCertificate, error) {
	var out EnvironmentCertificate
	d, err := domainOwner(ctx, tx, domain)
	if err != nil {
		return out, err
	}
	if d.ActiveEpoch != epoch {
		return out, ErrFenced
	}
	var active, registered uint64
	err = tx.QueryRowContext(ctx, `SELECT epoch,generation FROM environments WHERE domain_id = ? AND environment_id = ?`, domain, environment).Scan(&active, &registered)
	if err != nil {
		return out, err
	}
	if active != epoch {
		return out, ErrFenced
	}
	out, err = readCertificate(ctx, tx, domain, environment, registered)
	if err != nil {
		return out, err
	}
	if out.Revoked || !peer.HandshakeComplete || len(peer.PeerCertificates) != 2 || !bytes.Equal(out.Chain[0], peer.PeerCertificates[0].Raw) || !bytes.Equal(out.Chain[1], peer.PeerCertificates[1].Raw) {
		return out, ErrFenced
	}
	_, caGeneration, caDER, err := currentCA(ctx, tx, domain, epoch, at)
	if err != nil || caGeneration != out.CAGeneration || !bytes.Equal(caDER, out.Chain[1]) {
		return out, ErrFenced
	}
	_, spki, err := verifyLeaf(out.Chain[0], out.Chain[1], domain, environment, d.OwnerKeyID, epoch, at)
	if err != nil || spki != out.SPKIDigest {
		return out, ErrInvalidProof
	}
	return out, nil
}

// RenewEnvironmentCertificate requires current mTLS possession and a fresh
// CSR with the same SPKI. It issues a new registry generation atomically with
// revocation of the prior leaf. The grant path, not renewal, permits a
// response-lost retry with a revoked predecessor.
func (s *Store) RenewEnvironmentCertificate(ctx context.Context, domain, environment string, epoch uint64, peer tls.ConnectionState, csrDER []byte, chain [][]byte, at time.Time) (EnvironmentCertificate, error) {
	var out EnvironmentCertificate
	if !peer.HandshakeComplete || len(peer.PeerCertificates) != 2 {
		return out, ErrInvalidProof
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.db == nil {
		return out, errors.New("authoritystore: closed")
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return out, err
	}
	defer func() { _ = tx.Rollback() }()
	previous, err := verifyPeer(ctx, tx, domain, environment, epoch, peer, at)
	if err != nil {
		return out, err
	}
	_, spki, err := csrProof(csrDER)
	if err != nil || spki != previous.SPKIDigest {
		return out, ErrInvalidProof
	}
	csrDigest := digestBytes(csrDER)
	var used int
	if err = tx.QueryRowContext(ctx, `SELECT count(*) FROM environment_renewals WHERE domain_id = ? AND environment_id = ? AND csr_digest = ?`, domain, environment, csrDigest).Scan(&used); err != nil {
		return out, err
	}
	if used != 0 {
		return out, ErrFenced
	}
	d, err := domainOwner(ctx, tx, domain)
	if err != nil {
		return out, err
	}
	caEpoch, caGen, caDER, err := currentCA(ctx, tx, domain, epoch, at)
	if err != nil {
		return out, err
	}
	if len(chain) != 2 || !bytes.Equal(chain[1], caDER) {
		return out, ErrInvalidProof
	}
	leaf, leafSPKI, err := verifyLeaf(chain[0], caDER, domain, environment, d.OwnerKeyID, epoch, at)
	if err != nil || leafSPKI != spki || restrictedKey(ctx, tx, domain, leafSPKI, d.OwnerKeyID) {
		return out, ErrInvalidProof
	}
	gen := previous.Generation + 1
	if _, err = tx.ExecContext(ctx, `UPDATE environments SET generation = ? WHERE domain_id = ? AND environment_id = ?`, gen, domain, environment); err != nil {
		return out, err
	}
	if err = insertLeaf(ctx, tx, domain, environment, gen, caEpoch, caGen, leaf, chain, spki); err != nil {
		return out, err
	}
	if _, err = tx.ExecContext(ctx, `UPDATE environment_certificates SET revoked_at = ? WHERE domain_id = ? AND environment_id = ? AND generation = ? AND revoked_at IS NULL`, at.UTC().Format(time.RFC3339Nano), domain, environment, previous.Generation); err != nil {
		return out, err
	}
	if _, err = tx.ExecContext(ctx, `INSERT INTO environment_renewals(domain_id,environment_id,predecessor,csr_digest,generation) VALUES(?,?,?,?,?)`, domain, environment, digestBytes(peer.PeerCertificates[0].Raw), csrDigest, gen); err != nil {
		return out, writeError(err)
	}
	if err = tx.Commit(); err != nil {
		return out, err
	}
	return EnvironmentCertificate{domain, environment, epoch, gen, caGen, spki, [][]byte{bytes.Clone(chain[0]), bytes.Clone(chain[1])}, false}, nil
}

// RevokeEnvironmentCertificate is explicit leaf revocation; it cannot silently
// alter any other Environment generation or the historical issued chain.
func (s *Store) RevokeEnvironmentCertificate(ctx context.Context, domain, environment string, epoch, generation uint64, at time.Time) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.db == nil {
		return errors.New("authoritystore: closed")
	}
	if at.IsZero() {
		return ErrInvalidProof
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	d, err := domainOwner(ctx, tx, domain)
	if err != nil {
		return err
	}
	if d.ActiveEpoch != epoch {
		return ErrFenced
	}
	result, err := tx.ExecContext(ctx, `UPDATE environment_certificates SET revoked_at = ? WHERE domain_id = ? AND environment_id = ? AND generation = ? AND revoked_at IS NULL AND EXISTS(SELECT 1 FROM environments e WHERE e.domain_id = ? AND e.environment_id = ? AND e.epoch = ? AND e.generation = ?)`, at.UTC().Format(time.RFC3339Nano), domain, environment, generation, domain, environment, epoch, generation)
	if err != nil {
		return err
	}
	n, err := result.RowsAffected()
	if err != nil || n != 1 {
		return ErrFenced
	}
	return tx.Commit()
}

func checkEnvironments(db *sql.DB) error {
	rows, err := db.Query(`SELECT e.domain_id,e.environment_id,e.epoch,e.generation,e.sequence_head,c.generation,c.ca_epoch,c.ca_generation,c.serial,c.spki_digest,c.certificate,c.ca_certificate,c.not_before,c.not_after,c.revoked_at,a.certificate,d.owner_key_id,d.active_epoch FROM environments e JOIN environment_certificates c USING(domain_id,environment_id) JOIN environment_cas a ON a.domain_id = c.domain_id AND a.epoch = c.ca_epoch AND a.generation = c.ca_generation JOIN domains d ON d.domain_id = e.domain_id ORDER BY e.domain_id,e.environment_id,c.generation`)
	if err != nil {
		return err
	}
	defer func() { _ = rows.Close() }()
	var lastDomain, lastID string
	var lastGen uint64
	var previousRevoked bool
	for rows.Next() {
		var domain, id, serial, spki, before, after, ownerID string
		var epoch, current, head, gen, caEpoch, caGen, active int64
		var leaf, ca, retainedCA []byte
		var revoked sql.NullString
		if err := rows.Scan(&domain, &id, &epoch, &current, &head, &gen, &caEpoch, &caGen, &serial, &spki, &leaf, &ca, &before, &after, &revoked, &retainedCA, &ownerID, &active); err != nil {
			return err
		}
		if !ulid.MatchString(id) || head < 0 || epoch > active || caEpoch != epoch || !bytes.Equal(ca, retainedCA) || (lastDomain == domain && lastID == id && (!previousRevoked || gen != int64(lastGen)+1)) || ((lastDomain != domain || lastID != id) && gen != 1) {
			return ErrInvalidStore
		}
		cert, gotSPKI, err := verifyLeaf(leaf, ca, domain, id, ownerID, uint64(epoch), time.Time{})
		if err != nil || gotSPKI != spki || cert.SerialNumber.Text(16) != serial || cert.NotBefore.UTC().Format(time.RFC3339Nano) != before || cert.NotAfter.UTC().Format(time.RFC3339Nano) != after || gen > current {
			return ErrInvalidStore
		}
		if revoked.Valid {
			if _, err = utcTime(revoked.String); err != nil {
				return err
			}
		}
		lastDomain, lastID, lastGen, previousRevoked = domain, id, uint64(gen), revoked.Valid
	}
	if err = rows.Err(); err != nil {
		return err
	}
	if err = rows.Close(); err != nil {
		return err
	}
	var orphan int
	if err = db.QueryRow(`SELECT count(*) FROM environments e WHERE NOT EXISTS(SELECT 1 FROM environment_certificates c WHERE c.domain_id=e.domain_id AND c.environment_id=e.environment_id AND c.generation=e.generation)`).Scan(&orphan); err != nil || orphan != 0 {
		return ErrInvalidStore
	}
	if err = db.QueryRow(`SELECT count(*) FROM enrollment_consumptions u LEFT JOIN environment_certificates c ON c.domain_id=u.domain_id AND c.environment_id=u.environment_id AND c.generation=u.generation WHERE c.generation IS NULL OR (u.scope='environment-enroll' AND u.generation!=1) OR (u.scope='environment-rotate' AND u.generation<=1) OR u.scope NOT IN ('environment-enroll','environment-rotate') OR length(u.csr_digest)!=71 OR length(u.grant_digest)!=71`).Scan(&orphan); err != nil || orphan != 0 {
		return ErrInvalidStore
	}
	if err = db.QueryRow(`SELECT count(*) FROM environments e WHERE NOT EXISTS(SELECT 1 FROM enrollment_consumptions u WHERE u.domain_id=e.domain_id AND u.environment_id=e.environment_id AND u.generation=1)`).Scan(&orphan); err != nil || orphan != 0 {
		return ErrInvalidStore
	}
	if err = checkRenewals(db); err != nil {
		return ErrInvalidStore
	}
	if err = db.QueryRow(`SELECT count(*) FROM environment_cas a JOIN domains d USING(domain_id)
WHERE a.key_id=d.owner_key_id
 OR EXISTS(SELECT 1 FROM artifact_keys k WHERE k.domain_id=a.domain_id AND k.key_id=a.key_id)
 OR EXISTS(SELECT 1 FROM environment_certificates c WHERE c.domain_id=a.domain_id AND c.spki_digest=a.key_id)
 OR EXISTS(SELECT 1 FROM environment_cas b WHERE b.domain_id=a.domain_id AND b.key_id=a.key_id AND (b.epoch!=a.epoch OR b.generation!=a.generation))`).Scan(&orphan); err != nil || orphan != 0 {
		return ErrInvalidStore
	}
	if err = db.QueryRow(`SELECT count(*) FROM environment_certificates c JOIN domains d USING(domain_id)
WHERE c.spki_digest=d.owner_key_id OR EXISTS(SELECT 1 FROM artifact_keys k WHERE k.domain_id=c.domain_id AND k.key_id=c.spki_digest)`).Scan(&orphan); err != nil || orphan != 0 {
		return ErrInvalidStore
	}
	return nil
}

func checkRenewals(db *sql.DB) error {
	rows, err := db.Query(`SELECT r.predecessor,r.csr_digest,p.certificate,c.generation FROM environment_renewals r LEFT JOIN environment_certificates c ON c.domain_id=r.domain_id AND c.environment_id=r.environment_id AND c.generation=r.generation LEFT JOIN environment_certificates p ON p.domain_id=r.domain_id AND p.environment_id=r.environment_id AND p.generation=r.generation-1`)
	if err != nil {
		return err
	}
	defer func() { _ = rows.Close() }()
	for rows.Next() {
		var predecessor, csr string
		var prior []byte
		var current sql.NullInt64
		if err = rows.Scan(&predecessor, &csr, &prior, &current); err != nil {
			return err
		}
		if !current.Valid || predecessor != digestBytes(prior) || !validDigest(csr) {
			return ErrInvalidStore
		}
	}
	return rows.Err()
}

func checkStep3State(db *sql.DB) error {
	if err := checkArtifactKeys(db); err != nil {
		return err
	}
	if err := checkArtifactChains(db); err != nil {
		return err
	}
	if err := checkCAs(db); err != nil {
		return err
	}
	return checkEnvironments(db)
}
