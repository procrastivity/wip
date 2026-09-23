package authoritystore

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/x509"
	"database/sql"
	"encoding/asn1"
	"errors"
	"time"
)

type caDelegationPayload struct {
	Schema      string `cbor:"schema"`
	DomainID    string `cbor:"domain_id"`
	Epoch       uint64 `cbor:"authority_epoch"`
	OwnerKeyID  string `cbor:"owner_key_id"`
	Generation  uint64 `cbor:"ca_generation"`
	KeyID       string `cbor:"ca_key_id"`
	Certificate []byte `cbor:"ca_certificate_der"`
	NotBefore   string `cbor:"not_before"`
	NotAfter    string `cbor:"not_after"`
}

type caFencePayload struct {
	Schema           string `cbor:"schema"`
	DomainID         string `cbor:"domain_id"`
	Epoch            uint64 `cbor:"authority_epoch"`
	OwnerKeyID       string `cbor:"owner_key_id"`
	Generation       uint64 `cbor:"ca_generation"`
	KeyID            string `cbor:"ca_key_id"`
	DelegationDigest string `cbor:"delegation_artifact_digest"`
	EffectiveAt      string `cbor:"effective_at"`
	ReasonDigest     string `cbor:"reason_digest"`
}

var (
	basicConstraintsOID = asn1.ObjectIdentifier{2, 5, 29, 19}
	keyUsageOID         = asn1.ObjectIdentifier{2, 5, 29, 15}
	sanOID              = asn1.ObjectIdentifier{2, 5, 29, 17}
)

func hasCritical(cert *x509.Certificate, oid asn1.ObjectIdentifier) bool {
	for _, ext := range cert.Extensions {
		if ext.Id.Equal(oid) {
			return ext.Critical
		}
	}
	return false
}

func hasExtension(cert *x509.Certificate, oid asn1.ObjectIdentifier) bool {
	for _, ext := range cert.Extensions {
		if ext.Id.Equal(oid) {
			return true
		}
	}
	return false
}

func hasOnlyCriticalURISAN(cert *x509.Certificate, want string) bool {
	var raw []byte
	count := 0
	for _, ext := range cert.Extensions {
		if ext.Id.Equal(sanOID) {
			count++
			if !ext.Critical {
				return false
			}
			raw = ext.Value
		}
	}
	if count != 1 {
		return false
	}
	var names []asn1.RawValue
	rest, err := asn1.Unmarshal(raw, &names)
	if err != nil || len(rest) != 0 || len(names) != 1 {
		return false
	}
	name := names[0]
	if name.Class != asn1.ClassContextSpecific || name.Tag != 6 || name.IsCompound || string(name.Bytes) != want {
		return false
	}
	name.FullBytes = nil
	canonical, err := asn1.Marshal([]asn1.RawValue{name})
	return err == nil && bytes.Equal(canonical, raw)
}

func verifyCA(p caDelegationPayload, owner ed25519.PublicKey) (*x509.Certificate, error) {
	cert, err := x509.ParseCertificate(p.Certificate)
	if err != nil {
		return nil, ErrInvalidProof
	}
	id, err := spkiID(cert.PublicKey)
	if err != nil || id != p.KeyID || p.KeyID == digestBytes(mustSPKI(owner)) || len(p.Certificate) == 0 || !cert.IsCA || !cert.BasicConstraintsValid || !cert.MaxPathLenZero || cert.MaxPathLen != 0 || !hasCritical(cert, basicConstraintsOID) || !hasCritical(cert, keyUsageOID) || cert.KeyUsage != x509.KeyUsageCertSign || len(cert.ExtKeyUsage) != 0 || len(cert.UnknownExtKeyUsage) != 0 || hasExtension(cert, sanOID) || !bytes.Equal(cert.RawSubject, cert.RawIssuer) || cert.CheckSignatureFrom(cert) != nil {
		return nil, ErrInvalidProof
	}
	if _, ok := cert.PublicKey.(ed25519.PublicKey); !ok {
		return nil, ErrInvalidProof
	}
	a, err := utcTime(p.NotBefore)
	if err != nil {
		return nil, err
	}
	b, err := utcTime(p.NotAfter)
	if err != nil || !a.Before(b) || !cert.NotBefore.Equal(a) || !cert.NotAfter.Equal(b) {
		return nil, ErrInvalidProof
	}
	return cert, nil
}

func mustSPKI(key ed25519.PublicKey) []byte { der, _ := x509.MarshalPKIXPublicKey(key); return der }

// InstallEnvironmentCA verifies the offline owner's direct signed delegation
// and the distinct self-signed, pathLen=0 CA. It cannot issue a leaf by itself.
func (s *Store) InstallEnvironmentCA(ctx context.Context, domain string, wrapper []byte, at time.Time) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.db == nil {
		return errors.New("authoritystore: closed")
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
	payload, err := ownerArtifact(wrapper, d.OwnerPublicKey, domain, d.OwnerKeyID, "environment-ca-delegation", "wipd.environment-ca-delegation/1", d.ActiveEpoch)
	if err != nil {
		return err
	}
	var p caDelegationPayload
	if err = closedPayload(payload, &p, "schema", "domain_id", "authority_epoch", "owner_key_id", "ca_generation", "ca_key_id", "ca_certificate_der", "not_before", "not_after"); err != nil {
		return err
	}
	if p.Schema != "wipd.environment-ca-delegation/1" || p.DomainID != domain || p.Epoch != d.ActiveEpoch || p.OwnerKeyID != d.OwnerKeyID || p.Generation == 0 || interval(p.NotBefore, p.NotAfter, at) != nil {
		return ErrInvalidProof
	}
	if _, err = verifyCA(p, d.OwnerPublicKey); err != nil {
		return err
	}
	var n sql.NullInt64
	var fenced []byte
	err = tx.QueryRowContext(ctx, `SELECT generation, fence FROM environment_cas WHERE domain_id = ? AND epoch = ? ORDER BY generation DESC LIMIT 1`, domain, p.Epoch).Scan(&n, &fenced)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return err
	}
	if (n.Valid && (p.Generation != uint64(n.Int64)+1 || fenced == nil)) || (!n.Valid && p.Generation != 1) {
		return ErrFenced
	}
	var count int
	if err = tx.QueryRowContext(ctx, `SELECT (SELECT count(*) FROM artifact_keys WHERE domain_id = ? AND key_id = ?) + (SELECT count(*) FROM environment_certificates WHERE domain_id = ? AND spki_digest = ?) + (SELECT count(*) FROM environment_cas WHERE domain_id = ? AND key_id = ?)`, domain, p.KeyID, domain, p.KeyID, domain, p.KeyID).Scan(&count); err != nil {
		return err
	}
	if count != 0 {
		return ErrInvalidProof
	}
	_, err = tx.ExecContext(ctx, `INSERT INTO environment_cas(domain_id,epoch,generation,key_id,certificate,delegation,not_before,not_after) VALUES(?,?,?,?,?,?,?,?)`, domain, p.Epoch, p.Generation, p.KeyID, p.Certificate, wrapper, p.NotBefore, p.NotAfter)
	if err != nil {
		return writeError(err)
	}
	return tx.Commit()
}

// FenceEnvironmentCA immediately stops all leaf certificates issued under
// that CA; the signed effective time is no later than the commit time.
func (s *Store) FenceEnvironmentCA(ctx context.Context, domain string, wrapper []byte, at time.Time) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.db == nil {
		return errors.New("authoritystore: closed")
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
	payload, err := ownerArtifact(wrapper, d.OwnerPublicKey, domain, d.OwnerKeyID, "environment-ca-fence", "wipd.environment-ca-fence/1", d.ActiveEpoch)
	if err != nil {
		return err
	}
	var p caFencePayload
	if err = closedPayload(payload, &p, "schema", "domain_id", "authority_epoch", "owner_key_id", "ca_generation", "ca_key_id", "delegation_artifact_digest", "effective_at", "reason_digest"); err != nil {
		return err
	}
	effective, err := utcTime(p.EffectiveAt)
	if err != nil || effective.After(at.UTC()) || p.Schema != "wipd.environment-ca-fence/1" || p.DomainID != domain || p.Epoch != d.ActiveEpoch || p.OwnerKeyID != d.OwnerKeyID || p.Generation == 0 || !validDigest(p.ReasonDigest) {
		return ErrInvalidProof
	}
	var id string
	var delegation, existing []byte
	if err = tx.QueryRowContext(ctx, `SELECT key_id,delegation,fence FROM environment_cas WHERE domain_id = ? AND epoch = ? AND generation = ?`, domain, p.Epoch, p.Generation).Scan(&id, &delegation, &existing); err != nil {
		return err
	}
	if existing != nil || p.KeyID != id || p.DelegationDigest != digestBytes(append([]byte("wipd/artifact-digest/v1\x00"), delegation...)) {
		return ErrInvalidProof
	}
	result, err := tx.ExecContext(ctx, `UPDATE environment_cas SET fence = ? WHERE domain_id = ? AND epoch = ? AND generation = ? AND fence IS NULL`, wrapper, domain, p.Epoch, p.Generation)
	if err != nil {
		return err
	}
	n, err := result.RowsAffected()
	if err != nil || n != 1 {
		return ErrFenced
	}
	return tx.Commit()
}

func checkCAs(db *sql.DB) error {
	rows, err := db.Query(`SELECT c.domain_id,c.epoch,c.generation,c.key_id,c.certificate,c.delegation,c.not_before,c.not_after,c.fence,d.owner_public_key,d.owner_key_id,d.active_epoch FROM environment_cas c JOIN domains d USING(domain_id) ORDER BY c.domain_id,c.epoch,c.generation`)
	if err != nil {
		return err
	}
	defer func() { _ = rows.Close() }()
	var lastDomain string
	var lastEpoch, lastGeneration uint64
	var priorFenced bool
	for rows.Next() {
		var domain, id, before, after, ownerID string
		var epoch, gen, active int64
		var cert, wrapper, fence, owner []byte
		if err := rows.Scan(&domain, &epoch, &gen, &id, &cert, &wrapper, &before, &after, &fence, &owner, &ownerID, &active); err != nil {
			return err
		}
		payload, err := ownerArtifact(wrapper, owner, domain, ownerID, "environment-ca-delegation", "wipd.environment-ca-delegation/1", uint64(epoch))
		if err != nil {
			return err
		}
		var p caDelegationPayload
		if err = closedPayload(payload, &p, "schema", "domain_id", "authority_epoch", "owner_key_id", "ca_generation", "ca_key_id", "ca_certificate_der", "not_before", "not_after"); err != nil {
			return err
		}
		if p.Schema != "wipd.environment-ca-delegation/1" || p.DomainID != domain || p.Epoch != uint64(epoch) || p.OwnerKeyID != ownerID || p.Generation != uint64(gen) || p.KeyID != id || !bytes.Equal(p.Certificate, cert) || p.NotBefore != before || p.NotAfter != after || epoch > active {
			return ErrInvalidStore
		}
		if _, err = verifyCA(p, owner); err != nil {
			return err
		}
		if domain != lastDomain || uint64(epoch) != lastEpoch {
			lastGeneration = 0
			priorFenced = true
		}
		if uint64(gen) != lastGeneration+1 || !priorFenced {
			return ErrInvalidStore
		}
		lastDomain, lastEpoch, lastGeneration, priorFenced = domain, uint64(epoch), uint64(gen), fence != nil
		if fence != nil {
			fbytes, err := ownerArtifact(fence, owner, domain, ownerID, "environment-ca-fence", "wipd.environment-ca-fence/1", uint64(epoch))
			if err != nil {
				return err
			}
			var f caFencePayload
			if err = closedPayload(fbytes, &f, "schema", "domain_id", "authority_epoch", "owner_key_id", "ca_generation", "ca_key_id", "delegation_artifact_digest", "effective_at", "reason_digest"); err != nil {
				return err
			}
			if f.Schema != "wipd.environment-ca-fence/1" || f.DomainID != domain || f.Epoch != uint64(epoch) || f.OwnerKeyID != ownerID || f.Generation != uint64(gen) || f.KeyID != id || f.DelegationDigest != digestBytes(append([]byte("wipd/artifact-digest/v1\x00"), wrapper...)) || !validDigest(f.ReasonDigest) {
				return ErrInvalidStore
			}
			if _, err = utcTime(f.EffectiveAt); err != nil {
				return err
			}
		}
	}
	return rows.Err()
}
