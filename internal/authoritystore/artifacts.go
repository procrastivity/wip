package authoritystore

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/sha256"
	"crypto/x509"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"time"

	"github.com/fxamacker/cbor/v2"
)

var (
	artifactEncoder, _ = cbor.CoreDetEncOptions().EncMode()
	artifactDecoder, _ = (cbor.DecOptions{DupMapKey: cbor.DupMapKeyEnforcedAPF, IndefLength: cbor.IndefLengthForbidden, TagsMd: cbor.TagsForbidden, MaxMapPairs: 64, MaxArrayElements: 64}).DecMode()
	// ErrInvalidProof rejects a malformed, wrongly scoped or unverifiable proof.
	ErrInvalidProof = errors.New("authoritystore: invalid signed proof")
	// ErrFenced rejects an expired, revoked or already consumed credential.
	ErrFenced = errors.New("authoritystore: revoked or fenced identity")
)

type signedArtifact struct {
	Schema        string  `cbor:"schema"`
	Kind          string  `cbor:"kind"`
	DomainID      string  `cbor:"domain_id"`
	Epoch         uint64  `cbor:"authority_epoch"`
	SignerRole    string  `cbor:"signer_role"`
	SignerKeyID   string  `cbor:"signer_key_id"`
	Generation    *uint64 `cbor:"key_generation"`
	Sequence      *uint64 `cbor:"artifact_sequence"`
	Predecessor   *string `cbor:"previous_artifact_digest"`
	IssuedAt      string  `cbor:"issued_at"`
	PayloadSchema string  `cbor:"payload_schema"`
	PayloadDigest string  `cbor:"payload_digest"`
	Payload       []byte  `cbor:"payload"`
	Signature     []byte  `cbor:"signature"`
}

type artifactKeyPayload struct {
	Schema     string `cbor:"schema"`
	DomainID   string `cbor:"domain_id"`
	Epoch      uint64 `cbor:"authority_epoch"`
	Generation uint64 `cbor:"key_generation"`
	KeyID      string `cbor:"key_id"`
	PublicKey  []byte `cbor:"ed25519_public_key"`
	NotBefore  string `cbor:"not_before"`
	NotAfter   string `cbor:"not_after"`
}

type artifactFencePayload struct {
	Schema        string  `cbor:"schema"`
	DomainID      string  `cbor:"domain_id"`
	Epoch         uint64  `cbor:"authority_epoch"`
	Generation    uint64  `cbor:"key_generation"`
	KeyID         string  `cbor:"key_id"`
	FinalSequence uint64  `cbor:"final_sequence"`
	FinalDigest   *string `cbor:"final_artifact_digest"`
	EffectiveAt   string  `cbor:"effective_at"`
	ReasonDigest  string  `cbor:"reason_digest"`
}

// No owner or authority private key enters these APIs. A signed wrapper is
// decoded as a closed deterministic map, not treated as an opaque assertion.
func ownerArtifact(raw []byte, key ed25519.PublicKey, domain, keyID, kind, payloadSchema string, epoch uint64) ([]byte, error) {
	if len(raw) > 1<<20 {
		return nil, ErrInvalidProof
	}
	var fields map[string]cbor.RawMessage
	var a signedArtifact
	if err := canonicalDecode(raw, &fields); err != nil {
		return nil, err
	}
	if !exactKeys(fields, "schema", "kind", "domain_id", "authority_epoch", "signer_role", "signer_key_id", "key_generation", "artifact_sequence", "previous_artifact_digest", "issued_at", "payload_schema", "payload_digest", "payload", "signature") {
		return nil, ErrInvalidProof
	}
	if err := artifactDecoder.Unmarshal(raw, &a); err != nil {
		return nil, fmt.Errorf("%w: wrapper: %v", ErrInvalidProof, err)
	}
	if a.Schema != "wipd.signed-artifact/1" || a.Kind != kind || a.DomainID != domain || a.Epoch != epoch || a.SignerRole != "owner" || a.SignerKeyID != keyID || a.Generation != nil || a.Sequence != nil || a.Predecessor != nil || a.PayloadSchema != payloadSchema || a.PayloadDigest != digestBytes(a.Payload) || len(a.Signature) != ed25519.SignatureSize {
		return nil, ErrInvalidProof
	}
	if _, err := utcTime(a.IssuedAt); err != nil {
		return nil, err
	}
	delete(fields, "signature")
	unsigned, err := artifactEncoder.Marshal(fields)
	if err != nil {
		return nil, err
	}
	if !ed25519.Verify(key, append([]byte("wipd/signed-artifact/v1\x00"), unsigned...), a.Signature) {
		return nil, ErrInvalidProof
	}
	return a.Payload, nil
}

func canonicalDecode(raw []byte, value any) error {
	if err := artifactDecoder.Unmarshal(raw, value); err != nil {
		return fmt.Errorf("%w: CBOR: %v", ErrInvalidProof, err)
	}
	encoded, err := artifactEncoder.Marshal(value)
	if err != nil || !bytes.Equal(encoded, raw) {
		return ErrInvalidProof
	}
	return nil
}

func exactKeys(fields map[string]cbor.RawMessage, keys ...string) bool {
	if len(fields) != len(keys) {
		return false
	}
	for _, key := range keys {
		if _, ok := fields[key]; !ok {
			return false
		}
	}
	return true
}

func closedPayload(raw []byte, value any, keys ...string) error {
	var fields map[string]cbor.RawMessage
	if err := canonicalDecode(raw, &fields); err != nil {
		return err
	}
	if !exactKeys(fields, keys...) {
		return ErrInvalidProof
	}
	return artifactDecoder.Unmarshal(raw, value)
}

func digestBytes(data []byte) string {
	sum := sha256.Sum256(data)
	return "sha256:" + hex.EncodeToString(sum[:])
}

func spkiID(public any) (string, error) {
	der, err := x509.MarshalPKIXPublicKey(public)
	if err != nil {
		return "", err
	}
	return digestBytes(der), nil
}

func utcTime(value string) (time.Time, error) {
	t, err := time.Parse(time.RFC3339Nano, value)
	if err != nil || t.Location() != time.UTC || t.Format(time.RFC3339Nano) != value {
		return time.Time{}, ErrInvalidProof
	}
	return t, nil
}

func interval(start, end string, at time.Time) error {
	a, err := utcTime(start)
	if err != nil {
		return err
	}
	b, err := utcTime(end)
	if err != nil {
		return err
	}
	if !a.Before(b) || at.Before(a) || !at.Before(b) {
		return ErrInvalidProof
	}
	return nil
}

func domainOwner(ctx context.Context, tx *sql.Tx, domain string) (Domain, error) {
	var d Domain
	var epoch int64
	err := tx.QueryRowContext(ctx, `SELECT domain_id, owner_public_key, owner_key_id, active_epoch FROM domains WHERE domain_id = ?`, domain).Scan(&d.ID, &d.OwnerPublicKey, &d.OwnerKeyID, &epoch)
	if errors.Is(err, sql.ErrNoRows) {
		return d, ErrNotFound
	}
	if err != nil {
		return d, err
	}
	d.ActiveEpoch = uint64(epoch)
	return d, nil
}

// RegisterArtifactKey retains an owner-certified generation. A predecessor
// must have its owner-signed final fence committed first.
func (s *Store) RegisterArtifactKey(ctx context.Context, domain string, wrapper []byte, at time.Time) error {
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
	payload, err := ownerArtifact(wrapper, d.OwnerPublicKey, domain, d.OwnerKeyID, "authority-artifact-key", "wipd.authority-artifact-key/1", d.ActiveEpoch)
	if err != nil {
		return err
	}
	var p artifactKeyPayload
	if err = closedPayload(payload, &p, "schema", "domain_id", "authority_epoch", "key_generation", "key_id", "ed25519_public_key", "not_before", "not_after"); err != nil {
		return err
	}
	if p.Schema != "wipd.authority-artifact-key/1" || p.DomainID != domain || p.Epoch != d.ActiveEpoch || p.Generation == 0 || len(p.PublicKey) != ed25519.PublicKeySize || interval(p.NotBefore, p.NotAfter, at) != nil {
		return ErrInvalidProof
	}
	id, err := spkiID(ed25519.PublicKey(p.PublicKey))
	if err != nil || id != p.KeyID || id == d.OwnerKeyID {
		return ErrInvalidProof
	}
	var shared int
	if err = tx.QueryRowContext(ctx, `SELECT (SELECT count(*) FROM environment_cas WHERE domain_id = ? AND key_id = ?) + (SELECT count(*) FROM environment_certificates WHERE domain_id = ? AND spki_digest = ?)`, domain, id, domain, id).Scan(&shared); err != nil || shared != 0 {
		return ErrInvalidProof
	}
	var generation sql.NullInt64
	var fence []byte
	err = tx.QueryRowContext(ctx, `SELECT generation, fence FROM artifact_keys WHERE domain_id = ? AND epoch = ? ORDER BY generation DESC LIMIT 1`, domain, d.ActiveEpoch).Scan(&generation, &fence)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return err
	}
	if (generation.Valid && (p.Generation != uint64(generation.Int64)+1 || fence == nil)) || (!generation.Valid && p.Generation != 1) {
		return ErrFenced
	}
	_, err = tx.ExecContext(ctx, `INSERT INTO artifact_keys(domain_id, epoch, generation, key_id, public_key, certificate, not_before, not_after) VALUES (?, ?, ?, ?, ?, ?, ?, ?)`, domain, p.Epoch, p.Generation, p.KeyID, p.PublicKey, wrapper, p.NotBefore, p.NotAfter)
	if err != nil {
		return writeError(err)
	}
	return tx.Commit()
}

// FenceArtifactKey commits an owner-signed final head before rotation or revocation.
func (s *Store) FenceArtifactKey(ctx context.Context, domain string, wrapper []byte) error {
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
	payload, err := ownerArtifact(wrapper, d.OwnerPublicKey, domain, d.OwnerKeyID, "authority-key-fence", "wipd.authority-key-fence/1", d.ActiveEpoch)
	if err != nil {
		return err
	}
	var p artifactFencePayload
	if err = closedPayload(payload, &p, "schema", "domain_id", "authority_epoch", "key_generation", "key_id", "final_sequence", "final_artifact_digest", "effective_at", "reason_digest"); err != nil {
		return err
	}
	if p.Schema != "wipd.authority-key-fence/1" || p.DomainID != domain || p.Epoch != d.ActiveEpoch || p.Generation == 0 || !validDigest(p.ReasonDigest) {
		return ErrInvalidProof
	}
	if _, err = utcTime(p.EffectiveAt); err != nil {
		return err
	}
	var keyID string
	var existing []byte
	if err = tx.QueryRowContext(ctx, `SELECT key_id, fence FROM artifact_keys WHERE domain_id = ? AND epoch = ? AND generation = ?`, domain, p.Epoch, p.Generation).Scan(&keyID, &existing); err != nil {
		return err
	}
	if keyID != p.KeyID || existing != nil {
		return ErrFenced
	}
	var sequence sql.NullInt64
	var head sql.NullString
	err = tx.QueryRowContext(ctx, `SELECT sequence, digest FROM authority_artifacts WHERE domain_id = ? AND epoch = ? AND generation = ? ORDER BY sequence DESC LIMIT 1`, domain, p.Epoch, p.Generation).Scan(&sequence, &head)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return err
	}
	if (!sequence.Valid && (p.FinalSequence != 0 || p.FinalDigest != nil)) || (sequence.Valid && (p.FinalSequence != uint64(sequence.Int64) || p.FinalDigest == nil || *p.FinalDigest != head.String)) {
		return ErrInvalidProof
	}
	result, err := tx.ExecContext(ctx, `UPDATE artifact_keys SET fence = ?, final_sequence = ?, final_digest = ? WHERE domain_id = ? AND epoch = ? AND generation = ? AND fence IS NULL`, wrapper, p.FinalSequence, p.FinalDigest, domain, p.Epoch, p.Generation)
	if err != nil {
		return err
	}
	n, err := result.RowsAffected()
	if err != nil || n != 1 {
		return ErrFenced
	}
	return tx.Commit()
}

// ArtifactKey is a public-only record. Historical chain verification must
// reach its exact current head or its owner-approved final fence.
type ArtifactKey struct {
	DomainID           string
	Epoch, Generation  uint64
	KeyID              string
	PublicKey          ed25519.PublicKey
	Certificate, Fence []byte
	FinalSequence      uint64
	FinalDigest        string
}

// LookupArtifactKey returns the exact retained public certificate and fence.
func (s *Store) LookupArtifactKey(ctx context.Context, domain string, epoch, generation uint64) (ArtifactKey, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	var k ArtifactKey
	if s.db == nil {
		return k, errors.New("authoritystore: closed")
	}
	var seq sql.NullInt64
	var head sql.NullString
	err := s.db.QueryRowContext(ctx, `SELECT key_id, public_key, certificate, fence, final_sequence, final_digest FROM artifact_keys WHERE domain_id = ? AND epoch = ? AND generation = ?`, domain, epoch, generation).Scan(&k.KeyID, &k.PublicKey, &k.Certificate, &k.Fence, &seq, &head)
	if errors.Is(err, sql.ErrNoRows) {
		return k, ErrNotFound
	}
	if err != nil {
		return k, err
	}
	k.DomainID, k.Epoch, k.Generation = domain, epoch, generation
	if seq.Valid {
		k.FinalSequence = uint64(seq.Int64)
	}
	k.FinalDigest = head.String
	return k, nil
}

// Kept here for schema verification: every retained wrapper is independently
// reverified on reopen, not merely trusted because it was once inserted.
func checkArtifactKeys(db *sql.DB) error {
	rows, err := db.Query(`SELECT k.domain_id, k.epoch, k.generation, k.key_id, k.public_key, k.certificate, k.not_before, k.not_after, k.fence, k.final_sequence, k.final_digest, d.owner_public_key, d.owner_key_id, d.active_epoch FROM artifact_keys k JOIN domains d USING(domain_id) ORDER BY k.domain_id, k.epoch, k.generation`)
	if err != nil {
		return err
	}
	defer func() { _ = rows.Close() }()
	var lastDomain string
	var lastEpoch, lastGeneration uint64
	var priorFenced bool
	for rows.Next() {
		var domain, keyID, before, after, ownerID string
		var epoch, gen, active int64
		var public, wrapper, fence, owner []byte
		var seq sql.NullInt64
		var final sql.NullString
		if err := rows.Scan(&domain, &epoch, &gen, &keyID, &public, &wrapper, &before, &after, &fence, &seq, &final, &owner, &ownerID, &active); err != nil {
			return err
		}
		pbytes, err := ownerArtifact(wrapper, owner, domain, ownerID, "authority-artifact-key", "wipd.authority-artifact-key/1", uint64(epoch))
		if err != nil {
			return err
		}
		var p artifactKeyPayload
		if err = closedPayload(pbytes, &p, "schema", "domain_id", "authority_epoch", "key_generation", "key_id", "ed25519_public_key", "not_before", "not_after"); err != nil {
			return err
		}
		id, err := spkiID(ed25519.PublicKey(public))
		if err != nil || p.Schema != "wipd.authority-artifact-key/1" || p.DomainID != domain || p.Epoch != uint64(epoch) || p.Generation != uint64(gen) || p.KeyID != keyID || id != keyID || !bytes.Equal(p.PublicKey, public) || p.NotBefore != before || p.NotAfter != after {
			return ErrInvalidStore
		}
		start, err := utcTime(before)
		if err != nil {
			return err
		}
		end, err := utcTime(after)
		if err != nil {
			return err
		}
		if !start.Before(end) || keyID == ownerID {
			return ErrInvalidStore
		}
		if domain != lastDomain || uint64(epoch) != lastEpoch {
			lastGeneration = 0
			priorFenced = true
		}
		if uint64(gen) != lastGeneration+1 || !priorFenced {
			return ErrInvalidStore
		}
		lastDomain, lastEpoch, lastGeneration, priorFenced = domain, uint64(epoch), uint64(gen), fence != nil
		if epoch > active {
			return ErrInvalidStore
		}
		if fence != nil {
			fbytes, err := ownerArtifact(fence, owner, domain, ownerID, "authority-key-fence", "wipd.authority-key-fence/1", uint64(epoch))
			if err != nil {
				return err
			}
			var f artifactFencePayload
			if err = closedPayload(fbytes, &f, "schema", "domain_id", "authority_epoch", "key_generation", "key_id", "final_sequence", "final_artifact_digest", "effective_at", "reason_digest"); err != nil {
				return err
			}
			if f.Schema != "wipd.authority-key-fence/1" || f.DomainID != domain || f.Epoch != uint64(epoch) || f.Generation != uint64(gen) || f.KeyID != keyID || !validDigest(f.ReasonDigest) || f.FinalSequence != uint64(seq.Int64) || (f.FinalDigest == nil) != (!final.Valid) || (f.FinalDigest != nil && *f.FinalDigest != final.String) {
				return ErrInvalidStore
			}
			if _, err = utcTime(f.EffectiveAt); err != nil {
				return err
			}
		}
	}
	return rows.Err()
}

func verifyAuthorityArtifact(raw []byte, public ed25519.PublicKey, domain, keyID string, epoch, generation, sequence uint64, predecessor *string) (string, error) {
	if len(raw) > 1<<20 {
		return "", ErrInvalidProof
	}
	var fields map[string]cbor.RawMessage
	if err := canonicalDecode(raw, &fields); err != nil {
		return "", err
	}
	if !exactKeys(fields, "schema", "kind", "domain_id", "authority_epoch", "signer_role", "signer_key_id", "key_generation", "artifact_sequence", "previous_artifact_digest", "issued_at", "payload_schema", "payload_digest", "payload", "signature") {
		return "", ErrInvalidProof
	}
	var a signedArtifact
	if err := artifactDecoder.Unmarshal(raw, &a); err != nil {
		return "", err
	}
	if a.Schema != "wipd.signed-artifact/1" || a.DomainID != domain || a.Epoch != epoch || a.SignerRole != "authority" || a.SignerKeyID != keyID || a.Generation == nil || *a.Generation != generation || a.Sequence == nil || *a.Sequence != sequence || (a.Predecessor == nil) != (predecessor == nil) || (a.Predecessor != nil && *a.Predecessor != *predecessor) || a.PayloadDigest != digestBytes(a.Payload) || len(a.Signature) != ed25519.SignatureSize {
		return "", ErrInvalidProof
	}
	if _, err := utcTime(a.IssuedAt); err != nil {
		return "", err
	}
	delete(fields, "signature")
	unsigned, err := artifactEncoder.Marshal(fields)
	if err != nil || !ed25519.Verify(public, append([]byte("wipd/signed-artifact/v1\x00"), unsigned...), a.Signature) {
		return "", ErrInvalidProof
	}
	return digestBytes(append([]byte("wipd/artifact-digest/v1\x00"), raw...)), nil
}

// Existing chain rows are verified even though Step 4 alone may append a
// signed product in its owning terminal transaction. No standalone append
// method can advance this ledger without the matching durable product.
func checkArtifactChains(db *sql.DB) error {
	rows, err := db.Query(`SELECT a.domain_id,a.epoch,a.generation,a.sequence,a.digest,a.predecessor,a.wrapper,k.key_id,k.public_key,k.not_before,k.not_after,k.fence,k.final_sequence,k.final_digest FROM authority_artifacts a JOIN artifact_keys k USING(domain_id,epoch,generation) ORDER BY a.domain_id,a.epoch,a.generation,a.sequence`)
	if err != nil {
		return err
	}
	var lastDomain, lastDigest string
	var lastEpoch, lastGen, lastSequence uint64
	for rows.Next() {
		var domain, digest, keyID, before, after string
		var epoch, gen, seq int64
		var predecessor, final sql.NullString
		var wrapper, public, fence []byte
		var finalSeq sql.NullInt64
		if err = rows.Scan(&domain, &epoch, &gen, &seq, &digest, &predecessor, &wrapper, &keyID, &public, &before, &after, &fence, &finalSeq, &final); err != nil {
			_ = rows.Close()
			return err
		}
		if domain != lastDomain || uint64(epoch) != lastEpoch || uint64(gen) != lastGen {
			lastSequence = 0
			lastDigest = ""
		}
		var prior *string
		if lastSequence != 0 {
			prior = &lastDigest
		}
		if uint64(seq) != lastSequence+1 || (prior == nil) != (!predecessor.Valid) || (prior != nil && predecessor.String != *prior) || (fence != nil && uint64(seq) > uint64(finalSeq.Int64)) {
			_ = rows.Close()
			return ErrInvalidStore
		}
		computed, err := verifyAuthorityArtifact(wrapper, public, domain, keyID, uint64(epoch), uint64(gen), uint64(seq), prior)
		if err != nil || computed != digest {
			_ = rows.Close()
			return ErrInvalidStore
		}
		var artifact signedArtifact
		if err = artifactDecoder.Unmarshal(wrapper, &artifact); err != nil {
			_ = rows.Close()
			return err
		}
		t, err := utcTime(artifact.IssuedAt)
		if err != nil || interval(before, after, t) != nil {
			_ = rows.Close()
			return ErrInvalidStore
		}
		lastDomain, lastEpoch, lastGen, lastSequence, lastDigest = domain, uint64(epoch), uint64(gen), uint64(seq), digest
	}
	if err = rows.Err(); err != nil {
		_ = rows.Close()
		return err
	}
	if err = rows.Close(); err != nil {
		return err
	}
	// A fence must be exactly the canonical head, not merely a valid prefix.
	var n int
	err = db.QueryRow(`SELECT count(*) FROM artifact_keys k WHERE k.fence IS NOT NULL AND (k.final_sequence != coalesce((SELECT max(a.sequence) FROM authority_artifacts a WHERE a.domain_id=k.domain_id AND a.epoch=k.epoch AND a.generation=k.generation),0) OR coalesce(k.final_digest,'') != coalesce((SELECT a.digest FROM authority_artifacts a WHERE a.domain_id=k.domain_id AND a.epoch=k.epoch AND a.generation=k.generation ORDER BY a.sequence DESC LIMIT 1),''))`).Scan(&n)
	if err != nil || n != 0 {
		return ErrInvalidStore
	}
	return nil
}
