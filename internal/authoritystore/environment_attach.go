package authoritystore

import (
	"context"
	"crypto/ed25519"
	"database/sql"
	"encoding/base64"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/fxamacker/cbor/v2"
	"github.com/procrastivity/wip/internal/operation"
)

var environmentAttachMigrationMarker = schemaObject{"schema_migrations", "table", strings.Replace(
	strings.Replace(d115MigrationMarker.sql, "22)", "22, 23)", 1),
	"name = 'd115-create-domain-trust-and-replay'))",
	"name = 'd115-create-domain-trust-and-replay') OR (version = 23 AND name = 'm6-environment-attach-grants'))", 1)}

var environmentAttachSchema = []schemaObject{
	{"environment_attach_grant_revocations", "table", `CREATE TABLE environment_attach_grant_revocations (
  domain_id TEXT NOT NULL REFERENCES domains(domain_id) ON DELETE RESTRICT,
  grant_id TEXT NOT NULL,
  grant_digest TEXT NOT NULL,
  revocation_digest TEXT NOT NULL,
  owner_key_id TEXT NOT NULL,
  authority_epoch INTEGER NOT NULL CHECK(authority_epoch > 0),
  committed_at TEXT NOT NULL,
  PRIMARY KEY(domain_id,grant_id)
) STRICT, WITHOUT ROWID`},
	{"environment_attach_revocation_immutable", "trigger", `CREATE TRIGGER environment_attach_revocation_immutable BEFORE UPDATE ON environment_attach_grant_revocations
BEGIN SELECT RAISE(ABORT,'immutable environment attach grant revocation'); END`},
	{"environment_attach_revocation_no_delete", "trigger", `CREATE TRIGGER environment_attach_revocation_no_delete BEFORE DELETE ON environment_attach_grant_revocations
BEGIN SELECT RAISE(ABORT,'retained environment attach grant revocation'); END`},
	{"environment_attach_grant_consumptions", "table", `CREATE TABLE environment_attach_grant_consumptions (
  domain_id TEXT NOT NULL REFERENCES domains(domain_id) ON DELETE RESTRICT,
  grant_id TEXT NOT NULL,
  grant_digest TEXT NOT NULL,
  grant_artifact BLOB NOT NULL CHECK(length(grant_artifact) BETWEEN 1 AND 1048576),
  owner_key_id TEXT NOT NULL,
  command_id TEXT NOT NULL,
  request_hash TEXT NOT NULL,
  environment_id TEXT NOT NULL,
  environment_spki_digest TEXT NOT NULL,
  repo_id TEXT NOT NULL,
  normalized_repo_key TEXT NOT NULL,
  clone_id TEXT NOT NULL,
  worktree_id TEXT NOT NULL,
  authority_epoch INTEGER NOT NULL CHECK(authority_epoch > 0),
  committed_at TEXT NOT NULL,
  PRIMARY KEY(domain_id,grant_id),
  UNIQUE(domain_id,command_id),
  UNIQUE(domain_id,environment_id,normalized_repo_key),
  UNIQUE(domain_id,repo_id,clone_id),
  UNIQUE(domain_id,repo_id,worktree_id)
) STRICT, WITHOUT ROWID`},
	{"environment_attach_submission_bindings", "table", `CREATE TABLE environment_attach_submission_bindings (
  domain_id TEXT NOT NULL,
  command_id TEXT NOT NULL,
  environment_spki_digest TEXT NOT NULL,
  authority_spki_digest TEXT NOT NULL,
  PRIMARY KEY(domain_id,command_id),
  FOREIGN KEY(domain_id,command_id) REFERENCES submissions(domain_id,command_id) ON DELETE RESTRICT
) STRICT, WITHOUT ROWID`},
	{"environment_attach_submission_binding_immutable", "trigger", `CREATE TRIGGER environment_attach_submission_binding_immutable BEFORE UPDATE ON environment_attach_submission_bindings
BEGIN SELECT RAISE(ABORT,'immutable environment attach submission binding'); END`},
	{"environment_attach_submission_binding_no_delete", "trigger", `CREATE TRIGGER environment_attach_submission_binding_no_delete BEFORE DELETE ON environment_attach_submission_bindings
BEGIN SELECT RAISE(ABORT,'retained environment attach submission binding'); END`},
	{"environment_checkout_attachments", "table", `CREATE TABLE environment_checkout_attachments (
  domain_id TEXT NOT NULL,
  environment_id TEXT NOT NULL,
  repo_id TEXT NOT NULL,
  normalized_repo_key TEXT NOT NULL,
  common_dir_digest TEXT NOT NULL,
  git_dir_digest TEXT NOT NULL,
  worktree_name TEXT NOT NULL,
  clone_id TEXT NOT NULL,
  worktree_id TEXT NOT NULL,
  command_id TEXT NOT NULL,
  event_id TEXT NOT NULL,
  PRIMARY KEY(domain_id,environment_id,common_dir_digest,git_dir_digest),
  UNIQUE(domain_id,clone_id),
  UNIQUE(domain_id,worktree_id),
  UNIQUE(domain_id,command_id),
  FOREIGN KEY(domain_id,event_id) REFERENCES authority_events(domain_id,event_id) ON DELETE RESTRICT,
  FOREIGN KEY(domain_id,command_id) REFERENCES submissions(domain_id,command_id) ON DELETE RESTRICT
) STRICT, WITHOUT ROWID`},
	{"environment_checkout_attachment_immutable", "trigger", `CREATE TRIGGER environment_checkout_attachment_immutable BEFORE UPDATE ON environment_checkout_attachments
BEGIN SELECT RAISE(ABORT,'immutable environment checkout attachment'); END`},
	{"environment_checkout_attachment_no_delete", "trigger", `CREATE TRIGGER environment_checkout_attachment_no_delete BEFORE DELETE ON environment_checkout_attachments
BEGIN SELECT RAISE(ABORT,'retained environment checkout attachment'); END`},
	{"environment_attach_consumption_immutable", "trigger", `CREATE TRIGGER environment_attach_consumption_immutable BEFORE UPDATE ON environment_attach_grant_consumptions
BEGIN SELECT RAISE(ABORT,'immutable environment attach grant consumption'); END`},
	{"environment_attach_consumption_no_delete", "trigger", `CREATE TRIGGER environment_attach_consumption_no_delete BEFORE DELETE ON environment_attach_grant_consumptions
BEGIN SELECT RAISE(ABORT,'retained environment attach grant consumption'); END`},
}

func installEnvironmentAttach(db *sql.DB) error {
	tx, err := db.Begin()
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	var version int
	if err = tx.QueryRow(`PRAGMA user_version`).Scan(&version); err != nil || version != 22 {
		return fmt.Errorf("%w: environment attach requires schema 22", ErrInvalidStore)
	}
	for _, object := range environmentAttachSchema {
		if _, err = tx.Exec(object.sql); err != nil {
			return fmt.Errorf("%s: %w", object.name, err)
		}
	}
	for _, statement := range []string{
		`ALTER TABLE schema_migrations RENAME TO old_schema_migrations`, environmentAttachMigrationMarker.sql,
		`INSERT INTO schema_migrations SELECT * FROM old_schema_migrations`, `DROP TABLE old_schema_migrations`,
		`INSERT INTO schema_migrations VALUES(23,'m6-environment-attach-grants')`, `PRAGMA user_version=23`,
	} {
		if _, err = tx.Exec(statement); err != nil {
			return err
		}
	}
	return tx.Commit()
}

func checkEnvironmentAttachState(db *sql.DB) error {
	rows, err := db.Query(`SELECT r.domain_id,r.grant_id,r.grant_digest,r.revocation_digest,r.owner_key_id,r.authority_epoch,r.committed_at,
		(SELECT count(*) FROM domains d WHERE d.domain_id=r.domain_id)
		FROM environment_attach_grant_revocations r`)
	if err != nil {
		return err
	}
	defer func() { _ = rows.Close() }()
	for rows.Next() {
		var domainID, grantID, grantDigest, revocationDigest, ownerKeyID, committedAt string
		var epoch int64
		var domainCount int
		if err = rows.Scan(&domainID, &grantID, &grantDigest, &revocationDigest, &ownerKeyID, &epoch, &committedAt, &domainCount); err != nil {
			return err
		}
		if !ulid.MatchString(domainID) || !ulid.MatchString(grantID) || !validDigest(grantDigest) ||
			!validDigest(revocationDigest) || !validDigest(ownerKeyID) || epoch <= 0 || !canonicalD115Time(committedAt) {
			return fmt.Errorf("%w: invalid retained environment attach revocation", ErrInvalidStore)
		}
		if domainCount != 1 {
			return fmt.Errorf("%w: attach revocation domain missing", ErrInvalidStore)
		}
	}
	if err = rows.Err(); err != nil {
		return err
	}
	consumed, err := db.Query(`SELECT domain_id,grant_id,grant_digest,grant_artifact,owner_key_id,command_id,request_hash,
		environment_id,environment_spki_digest,repo_id,normalized_repo_key,clone_id,worktree_id,authority_epoch,committed_at
		FROM environment_attach_grant_consumptions`)
	if err != nil {
		return err
	}
	defer func() { _ = consumed.Close() }()
	for consumed.Next() {
		var domainID, grantID, grantDigest, ownerKeyID, commandID, requestHash, environmentID, environmentSPKI, repoID, routeKey, cloneID, worktreeID, committedAt string
		var artifact []byte
		var epoch int64
		if err = consumed.Scan(&domainID, &grantID, &grantDigest, &artifact, &ownerKeyID, &commandID, &requestHash,
			&environmentID, &environmentSPKI, &repoID, &routeKey, &cloneID, &worktreeID, &epoch, &committedAt); err != nil {
			return err
		}
		if !ulid.MatchString(domainID) || !ulid.MatchString(grantID) || !validDigest(grantDigest) || digestBytes(artifact) != grantDigest ||
			!validDigest(ownerKeyID) || !ulid.MatchString(commandID) || !validDigest(requestHash) || !ulid.MatchString(environmentID) ||
			!validDigest(environmentSPKI) || !ulid.MatchString(repoID) || !validNormalizedRepoKey(routeKey) || !ulid.MatchString(cloneID) ||
			!ulid.MatchString(worktreeID) || epoch <= 0 || !canonicalD115Time(committedAt) {
			return fmt.Errorf("%w: invalid retained environment attach consumption", ErrInvalidStore)
		}
	}
	if err = consumed.Err(); err != nil {
		return err
	}
	if err = consumed.Close(); err != nil {
		return err
	}
	attachments, err := db.Query(`SELECT a.domain_id,a.environment_id,a.repo_id,a.normalized_repo_key,a.common_dir_digest,a.git_dir_digest,
		a.worktree_name,a.clone_id,a.worktree_id,a.command_id,a.event_id,
		c.environment_id,c.repo_id,c.normalized_repo_key,c.clone_id,c.worktree_id,
		e.event_id,e.command_id,e.position,e.record,r.first_position,r.last_position,r.result_code
		FROM environment_checkout_attachments a
		LEFT JOIN environment_attach_grant_consumptions c ON c.domain_id=a.domain_id AND c.command_id=a.command_id
		LEFT JOIN authority_events e ON e.domain_id=a.domain_id AND e.event_id=a.event_id AND e.command_id=a.command_id
		LEFT JOIN terminal_receipts r ON r.domain_id=a.domain_id AND r.command_id=a.command_id`)
	if err != nil {
		return err
	}
	defer func() { _ = attachments.Close() }()
	for attachments.Next() {
		var domainID, environmentID, repoID, normalizedKey, commonDigest, gitDigest, worktreeName, cloneID, worktreeID, commandID, eventID string
		var consumedEnvironment, consumedRepo, consumedKey, consumedClone, consumedWorktree sql.NullString
		var storedEventID, storedEventCommand, resultCode sql.NullString
		var eventPosition, firstPosition, lastPosition sql.NullInt64
		var eventRecord []byte
		if err = attachments.Scan(&domainID, &environmentID, &repoID, &normalizedKey, &commonDigest, &gitDigest,
			&worktreeName, &cloneID, &worktreeID, &commandID, &eventID,
			&consumedEnvironment, &consumedRepo, &consumedKey, &consumedClone, &consumedWorktree,
			&storedEventID, &storedEventCommand, &eventPosition, &eventRecord, &firstPosition, &lastPosition, &resultCode); err != nil {
			return err
		}
		if !consumedEnvironment.Valid || !consumedRepo.Valid || !consumedKey.Valid || !consumedClone.Valid || !consumedWorktree.Valid ||
			!storedEventID.Valid || !storedEventCommand.Valid || !eventPosition.Valid || !firstPosition.Valid || !lastPosition.Valid || !resultCode.Valid ||
			consumedEnvironment.String != environmentID || consumedRepo.String != repoID || consumedKey.String != normalizedKey ||
			consumedClone.String != cloneID || consumedWorktree.String != worktreeID || storedEventID.String != eventID ||
			storedEventCommand.String != commandID || resultCode.String != string(operation.ResultSucceeded) ||
			firstPosition.Int64 != eventPosition.Int64 || lastPosition.Int64 != eventPosition.Int64 {
			return fmt.Errorf("%w: incomplete environment attach projection", ErrInvalidStore)
		}
		event, eventErr := parseStep12Event(eventRecord, domainID, uint64(eventPosition.Int64), eventID, commandID)
		if eventErr != nil || event.kind != "environment.checkout-attached/1" || event.environment != environmentID || event.repo != repoID ||
			!exactKeys(event.payload, "repo_id", "environment_id", "normalized_repo_key", "clone_id", "worktree_id", "common_dir_digest", "git_dir_digest", "worktree_name", "grant_id", "grant_digest") {
			return fmt.Errorf("%w: invalid environment attach event association", ErrInvalidStore)
		}
		var eventRepo, eventEnvironment, eventKey, eventClone, eventWorktree, eventCommon, eventGit, eventName string
		if artifactDecoder.Unmarshal(event.payload["repo_id"], &eventRepo) != nil ||
			artifactDecoder.Unmarshal(event.payload["environment_id"], &eventEnvironment) != nil ||
			artifactDecoder.Unmarshal(event.payload["normalized_repo_key"], &eventKey) != nil ||
			artifactDecoder.Unmarshal(event.payload["clone_id"], &eventClone) != nil ||
			artifactDecoder.Unmarshal(event.payload["worktree_id"], &eventWorktree) != nil ||
			artifactDecoder.Unmarshal(event.payload["common_dir_digest"], &eventCommon) != nil ||
			artifactDecoder.Unmarshal(event.payload["git_dir_digest"], &eventGit) != nil ||
			artifactDecoder.Unmarshal(event.payload["worktree_name"], &eventName) != nil ||
			eventRepo != repoID || eventEnvironment != environmentID || eventKey != normalizedKey || eventClone != cloneID ||
			eventWorktree != worktreeID || eventCommon != commonDigest || eventGit != gitDigest || eventName != worktreeName ||
			!validDigest(commonDigest) || !validDigest(gitDigest) || !validNormalizedRepoKey(normalizedKey) {
			return fmt.Errorf("%w: divergent environment attach projection", ErrInvalidStore)
		}
	}
	if err = attachments.Err(); err != nil {
		return err
	}
	var missingProjection int
	if err = db.QueryRow(`SELECT count(*) FROM environment_attach_grant_consumptions c
		LEFT JOIN environment_checkout_attachments a ON a.domain_id=c.domain_id AND a.command_id=c.command_id
		WHERE a.command_id IS NULL`).Scan(&missingProjection); err != nil {
		return err
	}
	if missingProjection != 0 {
		return fmt.Errorf("%w: environment attach consumption lacks checkout projection", ErrInvalidStore)
	}
	return nil
}

type attachRevocationPayload struct {
	Schema    string `cbor:"schema"`
	DomainID  string `cbor:"domain_id"`
	Epoch     uint64 `cbor:"authority_epoch"`
	OwnerKey  string `cbor:"owner_key_id"`
	GrantID   string `cbor:"grant_id"`
	GrantHash string `cbor:"grant_digest"`
}

type attachGrantPayload struct {
	Schema          string          `cbor:"schema"`
	GrantID         string          `cbor:"grant_id"`
	Nonce           []byte          `cbor:"nonce"`
	DomainID        string          `cbor:"domain_id"`
	Epoch           uint64          `cbor:"authority_epoch"`
	OwnerKey        string          `cbor:"owner_key_id"`
	Scope           string          `cbor:"scope"`
	Audience        cbor.RawMessage `cbor:"audience"`
	RepoID          string          `cbor:"repo_id"`
	NormalizedKey   string          `cbor:"normalized_repo_key"`
	EnvironmentID   string          `cbor:"environment_id"`
	EnvironmentSPKI string          `cbor:"environment_spki_digest"`
	IssuedAt        string          `cbor:"issued_at"`
	ExpiresAt       string          `cbor:"expires_at"`
}

type attachAudience struct {
	Schema string `cbor:"schema"`
	Kind   string `cbor:"kind"`
	SPKI   string `cbor:"authority_spki_digest"`
}

type attachGrant struct {
	ID          string
	Digest      string
	DomainID    string
	Epoch       uint64
	RepoID      string
	RouteKey    string
	Environment string
	EnvSPKI     string
}

// EnvironmentAttachGrantClaims is the locally verifiable, closed grant
// identity needed to bind one D115 receipt and authenticated checkout before
// constructing an M2 attempt.
type EnvironmentAttachGrantClaims struct {
	GrantID         string
	GrantDigest     string
	DomainID        string
	Epoch           uint64
	OwnerKeyID      string
	RepoID          string
	NormalizedKey   string
	EnvironmentID   string
	EnvironmentSPKI string
	AudienceSPKI    string
	IssuedAt        time.Time
	ExpiresAt       time.Time
}

// VerifyEnvironmentAttachGrantArtifact applies the same canonical wrapper,
// owner-root signature, scope, and typed-claim checks used by authority
// admission. It does not assert current route or revocation state; M2
// execution remains authoritative for those checks.
func VerifyEnvironmentAttachGrantArtifact(encoded string, ownerRoot ed25519.PublicKey, domainID string, epoch uint64, ownerKeyID string, now time.Time) (EnvironmentAttachGrantClaims, error) {
	return verifyEnvironmentAttachGrantArtifact(encoded, ownerRoot, domainID, epoch, ownerKeyID, now, true)
}

// VerifyEnvironmentAttachGrantArtifactForReplay verifies immutable grant
// signature and identity claims for an already-pinned M2 attempt. It omits
// only the first-use validity interval check; callers must have matched the
// exact grant digest to durable local attempt state first.
func VerifyEnvironmentAttachGrantArtifactForReplay(encoded string, ownerRoot ed25519.PublicKey, domainID string, epoch uint64, ownerKeyID string) (EnvironmentAttachGrantClaims, error) {
	return verifyEnvironmentAttachGrantArtifact(encoded, ownerRoot, domainID, epoch, ownerKeyID, time.Time{}, false)
}

func verifyEnvironmentAttachGrantArtifact(encoded string, ownerRoot ed25519.PublicKey, domainID string, epoch uint64, ownerKeyID string, now time.Time, checkTime bool) (EnvironmentAttachGrantClaims, error) {
	var claims EnvironmentAttachGrantClaims
	if encoded == "" || len(encoded) > 1<<20 || len(ownerRoot) != ed25519.PublicKeySize || epoch == 0 || checkTime && now.IsZero() {
		return claims, ErrInvalidProof
	}
	raw, err := base64.RawURLEncoding.DecodeString(encoded)
	if err != nil || len(raw) > 1<<20 || base64.RawURLEncoding.EncodeToString(raw) != encoded {
		return claims, ErrInvalidProof
	}
	defer clear(raw)
	var wrapper signedArtifact
	if canonicalDecode(raw, &wrapper) != nil {
		return claims, ErrInvalidProof
	}
	payloadBytes, err := ownerArtifact(raw, ownerRoot, domainID, ownerKeyID,
		"environment-attach-grant", "wipd.environment-attach-grant/1", epoch)
	if err != nil {
		return claims, ErrInvalidProof
	}
	var payload attachGrantPayload
	if closedPayload(payloadBytes, &payload, "schema", "grant_id", "nonce", "domain_id", "authority_epoch", "owner_key_id", "scope", "audience", "repo_id", "normalized_repo_key", "environment_id", "environment_spki_digest", "issued_at", "expires_at") != nil {
		return claims, ErrInvalidProof
	}
	var audience attachAudience
	var audienceFields map[string]cbor.RawMessage
	if canonicalDecode(payload.Audience, &audienceFields) != nil || !exactKeys(audienceFields, "schema", "kind", "authority_spki_digest") ||
		canonicalDecode(payload.Audience, &audience) != nil {
		return claims, ErrInvalidProof
	}
	issued, issuedErr := utcTime(payload.IssuedAt)
	expires, expiresErr := utcTime(payload.ExpiresAt)
	if payload.Schema != "wipd.environment-attach-grant/1" || !ulid.MatchString(payload.GrantID) || len(payload.Nonce) != 16 ||
		payload.DomainID != domainID || payload.Epoch != epoch || payload.OwnerKey != ownerKeyID ||
		payload.Scope != "environment.attach-once@v1" || audience.Schema != "wipd.attach-audience/1" || audience.Kind != "authority-spki" ||
		!validDigest(audience.SPKI) || !ulid.MatchString(payload.RepoID) || !validNormalizedRepoKey(payload.NormalizedKey) ||
		!ulid.MatchString(payload.EnvironmentID) || !validDigest(payload.EnvironmentSPKI) || issuedErr != nil || expiresErr != nil ||
		!issued.Before(expires) || checkTime && (now.Before(issued) || !now.Before(expires)) {
		return claims, ErrInvalidProof
	}
	return EnvironmentAttachGrantClaims{
		GrantID: payload.GrantID, GrantDigest: digestBytes(raw), DomainID: payload.DomainID, Epoch: payload.Epoch, OwnerKeyID: payload.OwnerKey,
		RepoID: payload.RepoID, NormalizedKey: payload.NormalizedKey, EnvironmentID: payload.EnvironmentID,
		EnvironmentSPKI: payload.EnvironmentSPKI, AudienceSPKI: audience.SPKI, IssuedAt: issued, ExpiresAt: expires,
	}, nil
}

// validateEnvironmentAttachGrantTx validates all first-use claims against the
// same serialized authority transaction that will consume the grant.
func validateEnvironmentAttachGrantTx(ctx context.Context, tx *sql.Tx, encoded string, proof operation.AttachCheckoutProof,
	domain Domain, serverSPKIDigest string, now time.Time,
) (attachGrant, error) {
	var grant attachGrant
	if encoded == "" || len(encoded) > 1<<20 || serverSPKIDigest == "" {
		return grant, ErrInvalidProof
	}
	raw, err := base64.RawURLEncoding.DecodeString(encoded)
	if err != nil || len(raw) > 1<<20 || base64.RawURLEncoding.EncodeToString(raw) != encoded {
		return grant, ErrInvalidProof
	}
	defer clear(raw)
	var envelope signedArtifact
	if canonicalDecode(raw, &envelope) != nil {
		return grant, ErrInvalidProof
	}
	payloadBytes, err := ownerArtifact(raw, domain.OwnerPublicKey, domain.ID, domain.OwnerKeyID,
		"environment-attach-grant", "wipd.environment-attach-grant/1", domain.ActiveEpoch)
	if err != nil {
		return grant, ErrInvalidProof
	}
	var payload attachGrantPayload
	if closedPayload(payloadBytes, &payload, "schema", "grant_id", "nonce", "domain_id", "authority_epoch", "owner_key_id", "scope", "audience", "repo_id", "normalized_repo_key", "environment_id", "environment_spki_digest", "issued_at", "expires_at") != nil {
		return grant, ErrInvalidProof
	}
	var audience attachAudience
	var audienceFields map[string]cbor.RawMessage
	if canonicalDecode(payload.Audience, &audienceFields) != nil || !exactKeys(audienceFields, "schema", "kind", "authority_spki_digest") ||
		canonicalDecode(payload.Audience, &audience) != nil {
		return grant, ErrInvalidProof
	}
	if payload.Schema != "wipd.environment-attach-grant/1" || !ulid.MatchString(payload.GrantID) || len(payload.Nonce) != 16 ||
		payload.DomainID != domain.ID || payload.Epoch != domain.ActiveEpoch || payload.OwnerKey != domain.OwnerKeyID ||
		payload.Scope != "environment.attach-once@v1" || audience.Schema != "wipd.attach-audience/1" ||
		audience.Kind != "authority-spki" || audience.SPKI != serverSPKIDigest ||
		!ulid.MatchString(payload.RepoID) || !validNormalizedRepoKey(payload.NormalizedKey) ||
		payload.EnvironmentID != proof.EnvironmentID || payload.EnvironmentSPKI != proof.EnvironmentSPKIDigest ||
		payload.NormalizedKey != proof.NormalizedRepoKey || !validDigest(payload.EnvironmentSPKI) {
		return grant, ErrInvalidProof
	}
	issued, err := utcTime(payload.IssuedAt)
	if err != nil {
		return grant, ErrInvalidProof
	}
	expires, err := utcTime(payload.ExpiresAt)
	if err != nil || !issued.Before(expires) || expires.Sub(issued) > 10*time.Minute || now.Before(issued) || !now.Before(expires) {
		return grant, ErrInvalidProof
	}
	wrapperIssued, err := utcTime(envelope.IssuedAt)
	if err != nil || wrapperIssued.After(now) {
		return grant, ErrInvalidProof
	}
	var member int
	if err = tx.QueryRowContext(ctx, `SELECT count(*) FROM repo_memberships WHERE repo_id=? AND domain_id=?`, payload.RepoID, domain.ID).Scan(&member); err != nil {
		return grant, err
	}
	if member != 1 {
		return grant, ErrFenced
	}
	var keyForRoute, routeForRepo string
	err = tx.QueryRowContext(ctx, `SELECT repo_id FROM repo_route_index WHERE normalized_key=?`, payload.NormalizedKey).Scan(&keyForRoute)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return grant, err
	}
	if err == nil && keyForRoute != payload.RepoID {
		return grant, ErrRouteKeyBoundElsewhere
	}
	err = tx.QueryRowContext(ctx, `SELECT normalized_key FROM repo_route_index WHERE repo_id=?`, payload.RepoID).Scan(&routeForRepo)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return grant, err
	}
	if err == nil && routeForRepo != payload.NormalizedKey {
		return grant, ErrRouteRepoHasKey
	}
	grant.Digest = digestBytes(raw)
	err = tx.QueryRowContext(ctx, `SELECT grant_digest FROM environment_attach_grant_revocations WHERE domain_id=? AND grant_id=?`, domain.ID, payload.GrantID).Scan(&routeForRepo)
	if err == nil {
		if routeForRepo != grant.Digest {
			return grant, ErrConflict
		}
		return grant, ErrFenced
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return grant, err
	}
	err = tx.QueryRowContext(ctx, `SELECT grant_digest FROM environment_attach_grant_consumptions WHERE domain_id=? AND grant_id=?`, domain.ID, payload.GrantID).Scan(&routeForRepo)
	if err == nil {
		if routeForRepo != grant.Digest {
			return grant, ErrConflict
		}
		return grant, ErrFenced
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return grant, err
	}
	var attached int
	if err = tx.QueryRowContext(ctx, `SELECT count(*) FROM environment_checkout_attachments
		WHERE domain_id=? AND environment_id=? AND (normalized_repo_key=? OR (common_dir_digest=? AND git_dir_digest=?))`,
		domain.ID, payload.EnvironmentID, payload.NormalizedKey, proof.CommonDirDigest, proof.GitDirDigest).Scan(&attached); err != nil {
		return grant, err
	}
	if attached != 0 {
		return grant, ErrConflict
	}
	grant.ID, grant.DomainID, grant.Epoch, grant.RepoID = payload.GrantID, domain.ID, domain.ActiveEpoch, payload.RepoID
	grant.RouteKey, grant.Environment, grant.EnvSPKI = payload.NormalizedKey, payload.EnvironmentID, payload.EnvironmentSPKI
	return grant, nil
}

// RevokeEnvironmentAttachGrant imports one current-epoch owner-root-signed
// revocation. Only the complete signed artifact is trusted as input.
func (s *Store) RevokeEnvironmentAttachGrant(artifact []byte) error {
	if len(artifact) == 0 || len(artifact) > 1<<20 {
		return ErrInvalidProof
	}
	var envelope signedArtifact
	if canonicalDecode(artifact, &envelope) != nil || envelope.Schema != "wipd.signed-artifact/1" ||
		envelope.Kind != "environment-attach-grant-revocation" || !ulid.MatchString(envelope.DomainID) || envelope.Epoch == 0 {
		return ErrInvalidProof
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.db == nil {
		return ErrInvalidStore
	}
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	domain, err := domainOwner(context.Background(), tx, envelope.DomainID)
	if err != nil || domain.ActiveEpoch != envelope.Epoch {
		return ErrFenced
	}
	// Sample only after this Store's serialized writer boundary is held.
	now := time.Now().UTC()
	payloadBytes, err := ownerArtifact(artifact, domain.OwnerPublicKey, domain.ID, domain.OwnerKeyID,
		"environment-attach-grant-revocation", "wipd.environment-attach-grant-revocation/1", domain.ActiveEpoch)
	if err != nil {
		return ErrInvalidProof
	}
	var payload attachRevocationPayload
	if closedPayload(payloadBytes, &payload, "schema", "domain_id", "authority_epoch", "owner_key_id", "grant_id", "grant_digest") != nil ||
		payload.Schema != "wipd.environment-attach-grant-revocation/1" || payload.DomainID != domain.ID ||
		payload.Epoch != domain.ActiveEpoch || payload.OwnerKey != domain.OwnerKeyID || !ulid.MatchString(payload.GrantID) ||
		!validDigest(payload.GrantHash) {
		return ErrInvalidProof
	}
	issuedAt, err := utcTime(envelope.IssuedAt)
	if err != nil || issuedAt.After(now) {
		return ErrInvalidProof
	}
	grantArtifactDigest := digestBytes(artifact)
	var storedGrant, storedArtifact string
	err = tx.QueryRow(`SELECT grant_digest,revocation_digest FROM environment_attach_grant_revocations WHERE domain_id=? AND grant_id=?`,
		domain.ID, payload.GrantID).Scan(&storedGrant, &storedArtifact)
	if err == nil {
		if storedGrant != payload.GrantHash {
			return ErrConflict
		}
		return nil
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return err
	}
	_, err = tx.Exec(`INSERT INTO environment_attach_grant_revocations(domain_id,grant_id,grant_digest,revocation_digest,owner_key_id,authority_epoch,committed_at) VALUES(?,?,?,?,?,?,?)`,
		domain.ID, payload.GrantID, payload.GrantHash, grantArtifactDigest, domain.OwnerKeyID, domain.ActiveEpoch, now.Format(time.RFC3339Nano))
	if err != nil {
		return writeError(err)
	}
	return tx.Commit()
}
