package authoritystore

import (
	"context"
	"crypto/ed25519"
	"database/sql"
	"errors"
	"time"

	"github.com/fxamacker/cbor/v2"
)

const (
	// M5LabGenesisGrantMaxLifetime bounds the isolated harness grant.
	M5LabGenesisGrantMaxLifetime = 10 * time.Minute
	maxM5LabGenesisGrantBytes    = 4096
	m5LabGenesisGrantPreimage    = "wip/m5-test-lab/create-domain-grant\x00"
)

var (
	// ErrM5LabGenesisGrantInvalid rejects an untrusted, expired, or wrongly
	// scoped authority-env setup grant. This is not an M2 wire problem code.
	ErrM5LabGenesisGrantInvalid = errors.New("authoritystore: invalid M5 lab genesis grant")
	// ErrM5LabGenesisGrantConsumed means the grant nonce was already consumed.
	ErrM5LabGenesisGrantConsumed = errors.New("authoritystore: M5 lab genesis grant already consumed")
)

type m5LabGenesisGrantPayload struct {
	Scope               string `cbor:"scope"`
	DomainID            string `cbor:"domain_id"`
	RepoID              string `cbor:"initial_repo_id"`
	Epoch               uint64 `cbor:"epoch"`
	OwnerRootSPKIDigest string `cbor:"owner_root_spki_digest"`
	Nonce               []byte `cbor:"nonce"`
	IssuedAt            string `cbor:"issued_at"`
	ExpiresAt           string `cbor:"expires_at"`
}

type m5LabGenesisGrantEnvelope struct {
	Payload   []byte `cbor:"payload"`
	Signature []byte `cbor:"signature"`
}

type verifiedM5LabGenesisGrant struct {
	nonce                 []byte
	grantDigest           string
	setupSignerSPKIDigest string
	ownerRootSPKIDigest   string
}

// CreateM5LabGenesisGrant creates the private, authority-env lab-only grant
// format. The caller supplies the setup signer and independently pins its
// public key for verification; this is not an M2 wire schema or enrollment
// grant. The harness is responsible for generating a random 128-bit nonce.
func CreateM5LabGenesisGrant(signer ed25519.PrivateKey, domain Domain, repoID string, nonce []byte, issuedAt, expiresAt time.Time) ([]byte, error) {
	if len(signer) != ed25519.PrivateKeySize || validDomain(domain) != nil || domain.ActiveEpoch != 1 ||
		!ulid.MatchString(repoID) || len(nonce) != 16 {
		return nil, ErrM5LabGenesisGrantInvalid
	}
	setupSignerSPKIDigest, err := spkiID(signer.Public())
	if err != nil || setupSignerSPKIDigest == domain.OwnerKeyID {
		return nil, ErrM5LabGenesisGrantInvalid
	}
	issued := issuedAt.UTC()
	expires := expiresAt.UTC()
	if issuedAt.IsZero() || expiresAt.IsZero() || !issued.Before(expires) || expires.Sub(issued) > M5LabGenesisGrantMaxLifetime {
		return nil, ErrM5LabGenesisGrantInvalid
	}
	payload := m5LabGenesisGrantPayload{
		Scope: "create-domain", DomainID: domain.ID, RepoID: repoID, Epoch: 1,
		OwnerRootSPKIDigest: domain.OwnerKeyID, Nonce: append([]byte(nil), nonce...),
		IssuedAt: issued.Format(time.RFC3339Nano), ExpiresAt: expires.Format(time.RFC3339Nano),
	}
	payloadBytes, err := artifactEncoder.Marshal(payload)
	if err != nil {
		return nil, err
	}
	signature := ed25519.Sign(signer, append([]byte(m5LabGenesisGrantPreimage), payloadBytes...))
	grant, err := artifactEncoder.Marshal(m5LabGenesisGrantEnvelope{Payload: payloadBytes, Signature: signature})
	if err != nil {
		return nil, err
	}
	if len(grant) > maxM5LabGenesisGrantBytes {
		return nil, ErrM5LabGenesisGrantInvalid
	}
	return grant, nil
}

// VerifyM5LabGenesisGrant validates a grant against an out-of-band pinned
// setup signer key without changing authority state.
func VerifyM5LabGenesisGrant(grant []byte, pinnedSetupSigner ed25519.PublicKey, domain Domain, repoID string, now time.Time) error {
	_, err := verifyM5LabGenesisGrant(grant, pinnedSetupSigner, domain, repoID, now)
	return err
}

func verifyM5LabGenesisGrant(grant []byte, pinnedSetupSigner ed25519.PublicKey, domain Domain, repoID string, now time.Time) (verifiedM5LabGenesisGrant, error) {
	var verified verifiedM5LabGenesisGrant
	if len(grant) == 0 || len(grant) > maxM5LabGenesisGrantBytes || len(pinnedSetupSigner) != ed25519.PublicKeySize ||
		validDomain(domain) != nil || domain.ActiveEpoch != 1 || !ulid.MatchString(repoID) || now.IsZero() {
		return verified, ErrM5LabGenesisGrantInvalid
	}
	setupSignerSPKIDigest, err := spkiID(pinnedSetupSigner)
	if err != nil || setupSignerSPKIDigest == domain.OwnerKeyID {
		return verified, ErrM5LabGenesisGrantInvalid
	}
	var fields map[string]cbor.RawMessage
	if err = canonicalDecode(grant, &fields); err != nil || !exactKeys(fields, "payload", "signature") {
		return verified, ErrM5LabGenesisGrantInvalid
	}
	var envelope m5LabGenesisGrantEnvelope
	if err = artifactDecoder.Unmarshal(grant, &envelope); err != nil || len(envelope.Signature) != ed25519.SignatureSize {
		return verified, ErrM5LabGenesisGrantInvalid
	}
	if !ed25519.Verify(pinnedSetupSigner, append([]byte(m5LabGenesisGrantPreimage), envelope.Payload...), envelope.Signature) {
		return verified, ErrM5LabGenesisGrantInvalid
	}
	var payloadFields map[string]cbor.RawMessage
	var payload m5LabGenesisGrantPayload
	if err = canonicalDecode(envelope.Payload, &payloadFields); err != nil ||
		!exactKeys(payloadFields, "scope", "domain_id", "initial_repo_id", "epoch", "owner_root_spki_digest", "nonce", "issued_at", "expires_at") ||
		artifactDecoder.Unmarshal(envelope.Payload, &payload) != nil {
		return verified, ErrM5LabGenesisGrantInvalid
	}
	ownerRootSPKIDigest, err := spkiID(domain.OwnerPublicKey)
	if err != nil || payload.Scope != "create-domain" || payload.DomainID != domain.ID || payload.RepoID != repoID ||
		payload.Epoch != 1 || payload.OwnerRootSPKIDigest != ownerRootSPKIDigest || len(payload.Nonce) != 16 {
		return verified, ErrM5LabGenesisGrantInvalid
	}
	issued, err := utcTime(payload.IssuedAt)
	if err != nil {
		return verified, ErrM5LabGenesisGrantInvalid
	}
	expires, err := utcTime(payload.ExpiresAt)
	if err != nil || !issued.Before(expires) || expires.Sub(issued) > M5LabGenesisGrantMaxLifetime {
		return verified, ErrM5LabGenesisGrantInvalid
	}
	at := now.UTC()
	if at.Before(issued) || !at.Before(expires) {
		return verified, ErrM5LabGenesisGrantInvalid
	}
	return verifiedM5LabGenesisGrant{
		nonce: append([]byte(nil), payload.Nonce...), grantDigest: digestBytes(grant),
		setupSignerSPKIDigest: setupSignerSPKIDigest, ownerRootSPKIDigest: ownerRootSPKIDigest,
	}, nil
}

// BootstrapDomainWithM5LabGrant verifies the pinned harness signer and
// atomically creates the initial epoch-1 domain, its first Repo membership,
// and an immutable one-use nonce-consumption record.
func (s *Store) BootstrapDomainWithM5LabGrant(ctx context.Context, domain Domain, repoID string, pinnedSetupSigner ed25519.PublicKey, grant []byte, now time.Time) error {
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
	verified, err := verifyM5LabGenesisGrant(grant, pinnedSetupSigner, domain, repoID, now)
	if err != nil {
		return err
	}
	var consumed int
	err = tx.QueryRowContext(ctx, `SELECT count(*) FROM m5_lab_genesis_grant_consumptions WHERE nonce=?`, verified.nonce).Scan(&consumed)
	if err != nil {
		return err
	}
	if consumed != 0 {
		return ErrM5LabGenesisGrantConsumed
	}
	if _, err = tx.ExecContext(ctx, `INSERT INTO domains(domain_id, owner_public_key, owner_key_id, initial_epoch, active_epoch) VALUES (?, ?, ?, ?, ?)`, domain.ID, []byte(domain.OwnerPublicKey), domain.OwnerKeyID, domain.ActiveEpoch, domain.ActiveEpoch); err != nil {
		return writeError(err)
	}
	if _, err = tx.ExecContext(ctx, `INSERT INTO repo_memberships(repo_id, domain_id) VALUES (?, ?)`, repoID, domain.ID); err != nil {
		return writeError(err)
	}
	_, err = tx.ExecContext(ctx, `INSERT INTO m5_lab_genesis_grant_consumptions(nonce,grant_digest,setup_signer_spki_digest,domain_id,repo_id,owner_root_spki_digest,epoch,consumed_at) VALUES(?,?,?,?,?,?,1,?)`, verified.nonce, verified.grantDigest, verified.setupSignerSPKIDigest, domain.ID, repoID, verified.ownerRootSPKIDigest, now.UTC().Format(time.RFC3339Nano))
	if err != nil {
		if errors.Is(writeError(err), ErrExists) {
			return ErrM5LabGenesisGrantConsumed
		}
		return err
	}
	return tx.Commit()
}

// M5LabGenesisRepoID returns the immutable initial Repo recorded by the
// consumed test-lab genesis grant. Later Repo memberships do not change it.
func (s *Store) M5LabGenesisRepoID(ctx context.Context, domainID string) (string, error) {
	if !ulid.MatchString(domainID) {
		return "", errors.New("authoritystore: invalid domain ID")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.db == nil {
		return "", errors.New("authoritystore: closed")
	}
	var repoID string
	err := s.db.QueryRowContext(ctx, `SELECT repo_id FROM m5_lab_genesis_grant_consumptions WHERE domain_id=?`, domainID).Scan(&repoID)
	if errors.Is(err, sql.ErrNoRows) {
		return "", ErrNotFound
	}
	if err != nil {
		return "", err
	}
	if !ulid.MatchString(repoID) {
		return "", ErrInvalidStore
	}
	return repoID, nil
}
