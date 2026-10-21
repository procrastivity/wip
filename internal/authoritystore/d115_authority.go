package authoritystore

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/sha256"
	"crypto/subtle"
	"database/sql"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"syscall"
	"time"

	"github.com/procrastivity/wip/internal/d115"
)

var (
	// ErrD115Refused is the stable refusal for invalid D115 create-domain authorization.
	ErrD115Refused = errors.New("authoritystore: D115 authorization refused")
	// ErrD115Conflict means a command ID was replayed with different identity bytes.
	ErrD115Conflict = errors.New("authoritystore: D115 command identity conflict")
	// ErrD115GrantConsumed means a different command already consumed the grant.
	ErrD115GrantConsumed = errors.New("authoritystore: D115 grant already consumed")
	// ErrD115Generation means the expected store-owner trust generation is stale.
	ErrD115Generation = errors.New("authoritystore: D115 trust generation conflict")
)

// D115TrustState exposes the one-realm D115 enablement generation without keys.
type D115TrustState struct {
	RealmID    string
	Enabled    bool
	Generation uint64
}

// D115OperatorKey is the public verification-key record admitted by a store owner.
type D115OperatorKey struct {
	KeyID     string
	PublicKey ed25519.PublicKey
	NotBefore string
	NotAfter  string
	State     string
}

// D115Trust returns the current realm generation without exposing key bytes.
func (s *Store) D115Trust(ctx context.Context) (D115TrustState, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.db == nil {
		return D115TrustState{}, ErrInvalidStore
	}
	var state D115TrustState
	var enabled, generation int64
	err := s.db.QueryRowContext(ctx, `SELECT provisioning_realm_id,enabled,generation FROM d115_provisioning_realms`).Scan(&state.RealmID, &enabled, &generation)
	if err != nil || generation < 1 {
		return D115TrustState{}, fmt.Errorf("%w: D115 trust registry unavailable", ErrInvalidStore)
	}
	state.Enabled, state.Generation = enabled == 1, uint64(generation)
	return state, nil
}

// ExportD115OperatorTrustBundle emits one consistent public-only registry
// snapshot for an owner-controlled offline handoff. It is read-only and
// requires the same private store-root owner boundary as trust updates.
func (s *Store) ExportD115OperatorTrustBundle(ctx context.Context) ([]byte, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.db == nil {
		return nil, ErrInvalidStore
	}
	if err := s.requireStoreOwner(); err != nil {
		return nil, err
	}
	tx, err := s.db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return nil, err
	}
	defer func() { _ = tx.Rollback() }()
	var realm string
	var enabled int
	var generation uint64
	if err = tx.QueryRowContext(ctx, `SELECT provisioning_realm_id,enabled,generation FROM d115_provisioning_realms`).Scan(&realm, &enabled, &generation); err != nil {
		return nil, ErrInvalidStore
	}
	rows, err := tx.QueryContext(ctx, `SELECT key_id,public_key,scope,audience,not_before,not_after,state FROM d115_operator_keys WHERE provisioning_realm_id=? ORDER BY key_id`, realm)
	if err != nil {
		return nil, err
	}
	bundle := d115.OperatorTrustBundle{Schema: d115.TrustBundleSchema, RealmID: realm, Generation: generation, Enabled: enabled == 1, Keys: []d115.OperatorTrustKey{}}
	for rows.Next() {
		var key d115.OperatorTrustKey
		if err = rows.Scan(&key.KeyID, &key.PublicKey, &key.Scope, &key.Audience, &key.NotBefore, &key.NotAfter, &key.State); err != nil {
			_ = rows.Close()
			return nil, err
		}
		bundle.Keys = append(bundle.Keys, key)
	}
	if err = errors.Join(rows.Err(), rows.Close()); err != nil {
		return nil, err
	}
	if err = tx.Commit(); err != nil {
		return nil, err
	}
	return d115.EncodeOperatorTrustBundle(bundle)
}

// UpdateD115Trust performs an owner-UID-authenticated generation-CAS update.
// The Store's exclusive writer lease and BEGIN IMMEDIATE serialize it with
// first-use create-domain admission.
func (s *Store) UpdateD115Trust(ctx context.Context, expectedGeneration uint64, enabled bool, add *D115OperatorKey, revokeKeyID string) (D115TrustState, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.db == nil {
		return D115TrustState{}, ErrInvalidStore
	}
	if err := s.requireStoreOwner(); err != nil {
		return D115TrustState{}, err
	}
	if add != nil && add.KeyID == revokeKeyID && revokeKeyID != "" {
		return D115TrustState{}, errors.New("authoritystore: D115 rotation must use distinct key IDs")
	}
	conn, err := s.db.Conn(ctx)
	if err != nil {
		return D115TrustState{}, err
	}
	defer func() { _ = conn.Close() }()
	if _, err = conn.ExecContext(ctx, "BEGIN IMMEDIATE"); err != nil {
		return D115TrustState{}, err
	}
	defer func() { _, _ = conn.ExecContext(context.Background(), "ROLLBACK") }()
	var realmID string
	var currentEnabled int
	var generation uint64
	if err = conn.QueryRowContext(ctx, `SELECT provisioning_realm_id,enabled,generation FROM d115_provisioning_realms`).Scan(&realmID, &currentEnabled, &generation); err != nil {
		return D115TrustState{}, err
	}
	if generation != expectedGeneration {
		return D115TrustState{}, ErrD115Generation
	}
	if revokeKeyID != "" {
		result, updateErr := conn.ExecContext(ctx, `UPDATE d115_operator_keys SET state='revoked' WHERE key_id=? AND provisioning_realm_id=? AND state='active'`, revokeKeyID, realmID)
		if updateErr != nil {
			return D115TrustState{}, updateErr
		}
		rows, rowsErr := result.RowsAffected()
		if rowsErr != nil || rows != 1 {
			return D115TrustState{}, errors.New("authoritystore: D115 operator key is not active")
		}
	}
	if add != nil {
		if err = validateD115OperatorKey(realmID, *add); err != nil {
			return D115TrustState{}, err
		}
		if _, err = conn.ExecContext(ctx, `INSERT INTO d115_operator_keys(key_id,provisioning_realm_id,public_key,scope,audience,not_before,not_after,state) VALUES(?,?,?,?,?,?,?,?)`,
			add.KeyID, realmID, []byte(add.PublicKey), d115.CreateDomainScope, realmID, add.NotBefore, add.NotAfter, add.State); err != nil {
			return D115TrustState{}, ErrD115Generation
		}
	}
	var active int
	if err = conn.QueryRowContext(ctx, `SELECT count(*) FROM d115_operator_keys WHERE provisioning_realm_id=? AND state='active' AND scope=? AND audience=?`, realmID, d115.CreateDomainScope, realmID).Scan(&active); err != nil {
		return D115TrustState{}, err
	}
	if (enabled && active != 1) || active > 1 {
		return D115TrustState{}, errors.New("authoritystore: D115 requires at most one active key and exactly one when enabled")
	}
	if _, err = conn.ExecContext(ctx, `UPDATE d115_provisioning_realms SET enabled=?,generation=generation+1 WHERE provisioning_realm_id=? AND generation=?`, boolInt(enabled), realmID, expectedGeneration); err != nil {
		return D115TrustState{}, err
	}
	if _, err = conn.ExecContext(ctx, "COMMIT"); err != nil {
		return D115TrustState{}, err
	}
	return D115TrustState{RealmID: realmID, Enabled: enabled, Generation: expectedGeneration + 1}, nil
}

func (s *Store) requireStoreOwner() error {
	root := filepath.Dir(s.blobs)
	info, err := os.Stat(root)
	if err != nil {
		return err
	}
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok || uint32(os.Geteuid()) != stat.Uid {
		return errors.New("authoritystore: D115 trust administration requires store-owner UID")
	}
	if info.Mode().Perm()&0o077 != 0 {
		return errors.New("authoritystore: D115 trust administration requires a private store root")
	}
	return nil
}

func validateD115OperatorKey(realmID string, key D115OperatorKey) error {
	keyID, err := d115.KeyID(key.PublicKey)
	if err != nil || keyID != key.KeyID || key.State != "active" || !canonicalD115Time(key.NotBefore) || !canonicalD115Time(key.NotAfter) {
		return errors.New("authoritystore: invalid D115 operator key")
	}
	start, _ := time.Parse(time.RFC3339Nano, key.NotBefore)
	end, _ := time.Parse(time.RFC3339Nano, key.NotAfter)
	if !start.Before(end) || !ulid.MatchString(realmID) {
		return errors.New("authoritystore: invalid D115 operator key interval or audience")
	}
	return nil
}

func canonicalD115Time(value string) bool {
	tm, err := time.Parse(time.RFC3339Nano, value)
	return err == nil && tm.UTC().Format(time.RFC3339Nano) == value && len(value) > 0 && value[len(value)-1] == 'Z'
}

func boolInt(value bool) int {
	if value {
		return 1
	}
	return 0
}

// SubmitCreateDomain admits a bounded D115 submit and atomically commits the
// new default domain, initial Repo membership, one-use grant record, and exact
// replay receipt. Exact committed replay is classified before current trust.
func (s *Store) SubmitCreateDomain(ctx context.Context, encoded []byte) ([]byte, error) {
	return s.submitCreateDomain(ctx, encoded, time.Now)
}

// SubmitCreateDomainAt is the deterministic-clock form used by isolated tests.
func (s *Store) SubmitCreateDomainAt(ctx context.Context, encoded []byte, now time.Time) ([]byte, error) {
	return s.submitCreateDomain(ctx, encoded, func() time.Time { return now })
}

// VerifyD115CreateDomainReceipt verifies that receiptBytes are the exact
// canonical terminal receipt retained by this store for a committed D115
// create-domain command. Lab composition uses this to bind an M2 fixture to
// the real D115-created domain and Repo without borrowing the M5 genesis row.
func (s *Store) VerifyD115CreateDomainReceipt(ctx context.Context, receiptBytes []byte) (d115.Receipt, error) {
	var receipt d115.Receipt
	if s == nil || len(receiptBytes) == 0 {
		return receipt, ErrD115Refused
	}
	decoded, err := d115.DecodeReceipt(receiptBytes)
	if err != nil || decoded.Operation.Name != "repo.create-local" || decoded.Operation.Version != 1 || decoded.ResultCode != "result.succeeded" {
		return receipt, ErrD115Refused
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.db == nil {
		return receipt, errors.New("authoritystore: closed")
	}
	var retained []byte
	err = s.db.QueryRowContext(ctx, `SELECT receipt FROM d115_create_domain_commands
		WHERE provisioning_realm_id=? AND command_id=? AND request_hash=?`,
		decoded.RealmID, decoded.CommandID, decoded.RequestHash).Scan(&retained)
	if err != nil || !bytes.Equal(retained, receiptBytes) {
		return receipt, ErrD115Refused
	}
	var domainID string
	err = s.db.QueryRowContext(ctx, `SELECT domain_id FROM d115_domain_genesis WHERE domain_id=?`, decoded.DomainID).Scan(&domainID)
	if err != nil || domainID != decoded.DomainID {
		return receipt, ErrD115Refused
	}
	var repoDomain string
	err = s.db.QueryRowContext(ctx, `SELECT domain_id FROM repo_memberships WHERE repo_id=?`, decoded.RepoID).Scan(&repoDomain)
	if err != nil || repoDomain != decoded.DomainID {
		return receipt, ErrD115Refused
	}
	return decoded, nil
}

func (s *Store) submitCreateDomain(ctx context.Context, encoded []byte, clock func() time.Time) ([]byte, error) {
	submit, err := d115.DecodeSubmit(encoded)
	if err != nil {
		return nil, ErrD115Refused
	}
	grantDigest := d115.GrantDigest(submit.Grant)
	expectedHash, err := d115.RequestHash(submit.RealmID, submit.CommandID, grantDigest)
	if err != nil || subtle.ConstantTimeCompare([]byte(expectedHash), []byte(submit.RequestHash)) != 1 {
		return nil, ErrD115Refused
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.db == nil {
		return nil, ErrInvalidStore
	}
	conn, err := s.db.Conn(ctx)
	if err != nil {
		return nil, err
	}
	defer func() { _ = conn.Close() }()
	if _, err = conn.ExecContext(ctx, "BEGIN IMMEDIATE"); err != nil {
		return nil, err
	}
	// Sample validity only after both the store mutex and SQLite's writer lock
	// are held, so contention cannot admit an expired grant using stale time.
	now := clock().UTC()
	committed := false
	defer func() {
		if !committed {
			_, _ = conn.ExecContext(context.Background(), "ROLLBACK")
		}
	}()
	// Replay classification is deliberately the first database read in this
	// transaction; current enable/key/expiry state cannot block recovery.
	var storedHash, storedGrantDigest string
	var ownerKeyID string
	var ownerPublicKey, storedReceipt []byte
	err = conn.QueryRowContext(ctx, `SELECT request_hash,grant_digest,owner_root_public_key,owner_root_key_id,receipt FROM d115_create_domain_commands WHERE provisioning_realm_id=? AND command_id=?`,
		submit.RealmID, submit.CommandID).Scan(&storedHash, &storedGrantDigest, &ownerPublicKey, &ownerKeyID, &storedReceipt)
	if err == nil {
		preimage, proofErr := d115.OwnerProofPreimage(submit.RealmID, submit.CommandID, submit.RequestHash, ownerKeyID)
		if proofErr != nil || !ed25519.Verify(ed25519.PublicKey(ownerPublicKey), preimage, submit.OwnerProof) {
			return nil, ErrD115Refused
		}
		if subtle.ConstantTimeCompare([]byte(storedHash), []byte(submit.RequestHash)) != 1 || subtle.ConstantTimeCompare([]byte(storedGrantDigest), []byte(grantDigest)) != 1 {
			return nil, ErrD115Conflict
		}
		if _, err = conn.ExecContext(ctx, "ROLLBACK"); err != nil {
			return nil, err
		}
		committed = true
		return append([]byte(nil), storedReceipt...), nil
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return nil, err
	}
	grant, claims, err := d115.DecodeGrant(submit.Grant)
	if err != nil || submit.RealmID != claims.Audience {
		return nil, ErrD115Refused
	}
	var enabled int
	var generation uint64
	var registryRealm string
	if err = conn.QueryRowContext(ctx, `SELECT provisioning_realm_id,enabled,generation FROM d115_provisioning_realms`).Scan(&registryRealm, &enabled, &generation); err != nil || registryRealm != submit.RealmID || enabled != 1 || generation == 0 {
		return nil, ErrD115Refused
	}
	var operatorPublicKey []byte
	var keyState, keyScope, audience, keyNotBefore, keyNotAfter string
	if err = conn.QueryRowContext(ctx, `SELECT public_key,state,scope,audience,not_before,not_after FROM d115_operator_keys WHERE provisioning_realm_id=? AND key_id=?`, submit.RealmID, grant.SignerKeyID).Scan(&operatorPublicKey, &keyState, &keyScope, &audience, &keyNotBefore, &keyNotAfter); err != nil {
		return nil, ErrD115Refused
	}
	ownerID, _ := d115.KeyID(ed25519.PublicKey(claims.OwnerPublicKey))
	proofPreimage, proofErr := d115.OwnerProofPreimage(submit.RealmID, submit.CommandID, submit.RequestHash, claims.OwnerKeyID)
	if proofErr != nil || !ed25519.Verify(ed25519.PublicKey(claims.OwnerPublicKey), proofPreimage, submit.OwnerProof) ||
		grant.SignerKeyID == claims.OwnerKeyID || !d115.VerifyGrantSignature(grant, ed25519.PublicKey(operatorPublicKey)) ||
		ownerID != claims.OwnerKeyID || keyState != "active" || keyScope != d115.CreateDomainScope || audience != submit.RealmID ||
		!canonicalD115Time(keyNotBefore) || !canonicalD115Time(keyNotAfter) {
		return nil, ErrD115Refused
	}
	keyStart, _ := time.Parse(time.RFC3339Nano, keyNotBefore)
	keyEnd, _ := time.Parse(time.RFC3339Nano, keyNotAfter)
	issued, _ := time.Parse(time.RFC3339Nano, claims.IssuedAt)
	notBefore, _ := time.Parse(time.RFC3339Nano, claims.NotBefore)
	expires, _ := time.Parse(time.RFC3339Nano, claims.ExpiresAt)
	if now.Before(keyStart) || !now.Before(keyEnd) || now.Before(notBefore) || !now.Before(expires) ||
		issued.After(now) || issued.After(notBefore) || !notBefore.Before(expires) || expires.Sub(issued) > 10*time.Minute ||
		now.Before(issued) {
		return nil, ErrD115Refused
	}
	var consumed int
	if err = conn.QueryRowContext(ctx, `SELECT count(*) FROM d115_grant_consumptions WHERE provisioning_realm_id=? AND grant_id=?`, submit.RealmID, claims.GrantID).Scan(&consumed); err != nil {
		return nil, err
	}
	if consumed != 0 {
		return nil, ErrD115GrantConsumed
	}
	domainID, err := authorityULID(now)
	if err != nil {
		return nil, err
	}
	repoID, err := authorityULID(now.Add(time.Millisecond))
	if err != nil {
		return nil, err
	}
	for tries := 0; tries < 4; tries++ {
		var exists int
		if err = conn.QueryRowContext(ctx, `SELECT (SELECT count(*) FROM domains WHERE domain_id=?)+(SELECT count(*) FROM repo_memberships WHERE repo_id=?)`, domainID, repoID).Scan(&exists); err != nil {
			return nil, err
		}
		if exists == 0 {
			break
		}
		domainID, err = authorityULID(now.Add(time.Duration(tries + 2)))
		if err != nil {
			return nil, err
		}
		repoID, err = authorityULID(now.Add(time.Duration(tries + 3)))
		if err != nil {
			return nil, err
		}
		if tries == 3 {
			return nil, errors.New("authoritystore: cannot mint unique D115 identities")
		}
	}
	committedAt := now.UTC().Format(time.RFC3339Nano)
	receipt := d115.Receipt{
		Schema: d115.ReceiptSchema, RealmID: submit.RealmID, CommandID: submit.CommandID,
		RequestHash: submit.RequestHash, Operation: d115.Operation{Name: "repo.create-local", Version: 1}, ResultCode: "result.succeeded",
		DomainID: domainID, RepoID: repoID, InitialEpoch: 1, OwnerKeyID: claims.OwnerKeyID, GrantID: claims.GrantID, CommittedAt: committedAt,
	}
	receiptBytes, err := d115.EncodeReceipt(receipt)
	if err != nil {
		return nil, err
	}
	if _, err = conn.ExecContext(ctx, `INSERT INTO domains(domain_id,owner_public_key,owner_key_id,initial_epoch,active_epoch) VALUES(?,?,?,?,1)`, domainID, claims.OwnerPublicKey, claims.OwnerKeyID, 1); err != nil {
		return nil, writeError(err)
	}
	if _, err = conn.ExecContext(ctx, `INSERT INTO repo_memberships(repo_id,domain_id) VALUES(?,?)`, repoID, domainID); err != nil {
		return nil, writeError(err)
	}
	emptyDigest := sha256.Sum256([]byte("wipd/event-prefix/v1\x00"))
	if _, err = conn.ExecContext(ctx, `INSERT INTO d115_domain_genesis(domain_id,event_count,event_id,prefix_digest) VALUES(?,0,NULL,?)`, domainID, "sha256:"+fmt.Sprintf("%x", emptyDigest[:])); err != nil {
		return nil, err
	}
	if _, err = conn.ExecContext(ctx, `INSERT INTO d115_grant_consumptions(provisioning_realm_id,grant_id,grant_digest,signer_key_id,owner_root_key_id,command_id,request_hash,committed_at) VALUES(?,?,?,?,?,?,?,?)`,
		submit.RealmID, claims.GrantID, grantDigest, grant.SignerKeyID, claims.OwnerKeyID, submit.CommandID, submit.RequestHash, committedAt); err != nil {
		return nil, ErrD115GrantConsumed
	}
	if _, err = conn.ExecContext(ctx, `INSERT INTO d115_create_domain_commands(provisioning_realm_id,command_id,request_hash,grant_digest,owner_root_public_key,owner_root_key_id,receipt) VALUES(?,?,?,?,?,?,?)`,
		submit.RealmID, submit.CommandID, submit.RequestHash, grantDigest, claims.OwnerPublicKey, claims.OwnerKeyID, receiptBytes); err != nil {
		return nil, writeError(err)
	}
	if _, err = conn.ExecContext(ctx, "COMMIT"); err != nil {
		return nil, err
	}
	return receiptBytes, nil
}
