package wipdjournal

import (
	"bytes"
	"context"
	"database/sql"
	"errors"
	"fmt"
	"io"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/procrastivity/wip/internal/operation"
	"github.com/procrastivity/wip/internal/wipdwire"
)

// ClaimHydration is the durable local readiness product for one exact grant.
type ClaimHydration struct {
	GrantID                  string
	ClaimID                  string
	ClaimEpoch               uint64
	AsOf                     wipdwire.PrefixAnchor
	ManifestDigest           string
	RequiredEntryCount       uint64
	VerifiedPinnedEntryCount uint64
	State                    string
}

func createClaimHydrationSchema(executor sqlExecutor) error {
	for _, statement := range []string{
		`CREATE TABLE hydration_grants(
			grant_id TEXT PRIMARY KEY,
			claim_id TEXT NOT NULL,
			claim_epoch INTEGER NOT NULL CHECK(claim_epoch>0),
			as_of_count INTEGER NOT NULL CHECK(as_of_count>=0),
			as_of_event_id TEXT,
			as_of_digest TEXT NOT NULL CHECK(length(as_of_digest)=71),
			manifest_digest TEXT NOT NULL CHECK(length(manifest_digest)=71),
			manifest BLOB NOT NULL,
			required_entry_count INTEGER NOT NULL CHECK(required_entry_count>=0),
			state TEXT NOT NULL CHECK(state IN ('hydrating','offline-ready')),
			CHECK((as_of_count=0 AND as_of_event_id IS NULL) OR (as_of_count>0 AND as_of_event_id IS NOT NULL))
		) STRICT, WITHOUT ROWID`,
		`CREATE TRIGGER hydration_grant_identity_immutable BEFORE UPDATE OF grant_id,claim_id,claim_epoch,as_of_count,as_of_event_id,as_of_digest,manifest_digest,manifest,required_entry_count ON hydration_grants
			BEGIN SELECT RAISE(ABORT,'immutable claim hydration identity'); END`,
		`CREATE TRIGGER hydration_grant_state_transition BEFORE UPDATE OF state ON hydration_grants
			WHEN NOT (OLD.state='hydrating' AND NEW.state='offline-ready')
			BEGIN SELECT RAISE(ABORT,'invalid claim hydration transition'); END`,
		`CREATE TRIGGER hydration_grant_no_delete BEFORE DELETE ON hydration_grants
			BEGIN SELECT RAISE(ABORT,'retained claim hydration evidence'); END`,
		`CREATE UNIQUE INDEX hydration_grant_claim_unique ON hydration_grants(claim_id,claim_epoch)`,
		`CREATE TABLE hydration_pins(
			grant_id TEXT NOT NULL REFERENCES hydration_grants(grant_id),
			digest TEXT NOT NULL CHECK(length(digest)=71),
			byte_length INTEGER NOT NULL CHECK(byte_length>=0 AND byte_length<=1099511627776),
			PRIMARY KEY(grant_id,digest)
		) STRICT, WITHOUT ROWID`,
		`CREATE TRIGGER hydration_pin_no_update BEFORE UPDATE ON hydration_pins
			BEGIN SELECT RAISE(ABORT,'immutable claim hydration pin'); END`,
		`CREATE TRIGGER hydration_pin_no_delete BEFORE DELETE ON hydration_pins
			BEGIN SELECT RAISE(ABORT,'retained claim hydration pin'); END`,
	} {
		if _, err := executor.Exec(statement); err != nil {
			return err
		}
	}
	return nil
}

// upgradeSchemaV3 adds only the durable grant-bound hydration index. Blob
// bytes and command/receipt evidence remain untouched.
func upgradeSchemaV3(db *sql.DB, identity Identity) error {
	var version int
	var repoID, domainID, environmentID, marker string
	var epoch int64
	if err := db.QueryRow(`PRAGMA user_version`).Scan(&version); err != nil || version != 3 {
		return fmt.Errorf("%w: Environment hydration upgrade requires v3, got %d (%v)", ErrInvalidJournal, version, err)
	}
	if err := db.QueryRow(`SELECT repo_id,domain_id,authority_epoch,environment_id FROM environment_state WHERE singleton=1`).Scan(&repoID, &domainID, &epoch, &environmentID); err != nil ||
		repoID != identity.RepoID || domainID != identity.DomainID || epoch != int64(identity.AuthorityEpoch) || environmentID != identity.EnvironmentID {
		return ErrInvalidIdentity
	}
	if err := db.QueryRow(`SELECT name FROM schema_migrations WHERE version=3`).Scan(&marker); err != nil || marker != "environment-birth-release-receipt-barrier" {
		return fmt.Errorf("%w: v3 hydration-upgrade marker %q: %v", ErrInvalidJournal, marker, err)
	}
	if err := checkSchemaObjectsV3(db); err != nil {
		return fmt.Errorf("%w: v3 hydration-upgrade schema: %v", ErrInvalidJournal, err)
	}
	tx, err := db.BeginTx(context.Background(), nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	if _, err = tx.Exec(`DROP TABLE schema_migrations`); err != nil {
		return err
	}
	if _, err = tx.Exec(`CREATE TABLE schema_migrations(version INTEGER PRIMARY KEY CHECK(version=4),name TEXT NOT NULL CHECK(name='environment-claim-hydration-pins')) STRICT`); err != nil {
		return err
	}
	if _, err = tx.Exec(`INSERT INTO schema_migrations(version,name) VALUES(4,'environment-claim-hydration-pins')`); err != nil {
		return err
	}
	if err = createClaimHydrationSchema(tx); err != nil {
		return err
	}
	if _, err = tx.Exec(`PRAGMA user_version=4`); err != nil {
		return err
	}
	return tx.Commit()
}

// BeginClaimHydration binds the complete, previously installed authority
// manifest to one grant. It does not trust an uninstalled caller manifest.
func (j *Journal) BeginClaimHydration(ctx context.Context, grantID, claimID string, claimEpoch uint64, manifest wipdwire.BlobManifest) (ClaimHydration, error) {
	if j == nil || ctx == nil || !transferULID.MatchString(grantID) || !transferULID.MatchString(claimID) || claimEpoch == 0 ||
		manifest.DomainID != j.identity.DomainID || manifest.Epoch != j.identity.AuthorityEpoch || verifyManifest(manifest) != nil {
		return ClaimHydration{}, ErrInvalidTransfer
	}
	encoded, err := encodeManifest(manifest)
	if err != nil {
		return ClaimHydration{}, err
	}
	required := requiredManifestEntries(manifest)
	state := "hydrating"
	if required == 0 {
		state = "offline-ready"
	}
	j.mu.Lock()
	defer j.mu.Unlock()
	if j.db == nil {
		return ClaimHydration{}, ErrClosed
	}
	tx, err := j.db.BeginTx(ctx, nil)
	if err != nil {
		return ClaimHydration{}, err
	}
	defer func() { _ = tx.Rollback() }()
	installed, err := readInstallState(tx)
	if err != nil {
		return ClaimHydration{}, err
	}
	if installed.manifestDigest != manifest.Digest || !bytes.Equal(installed.manifest, encoded) || !sameTransferAnchor(installed.anchor, manifest.AsOf) {
		return ClaimHydration{}, ErrInvalidTransfer
	}
	var eventID any
	if manifest.AsOf.EventID != nil {
		eventID = *manifest.AsOf.EventID
	}
	if _, err = tx.ExecContext(ctx, `INSERT INTO hydration_grants(grant_id,claim_id,claim_epoch,as_of_count,as_of_event_id,as_of_digest,manifest_digest,manifest,required_entry_count,state)
		VALUES(?,?,?,?,?,?,?,?,?,?) ON CONFLICT(grant_id) DO NOTHING`, grantID, claimID, claimEpoch, manifest.AsOf.EventCount, eventID,
		manifest.AsOf.Digest, manifest.Digest, encoded, required, state); err != nil {
		return ClaimHydration{}, err
	}
	actual, err := readClaimHydration(tx, grantID)
	if err != nil || actual.ClaimID != claimID || actual.ClaimEpoch != claimEpoch || actual.ManifestDigest != manifest.Digest || !sameTransferAnchor(actual.AsOf, manifest.AsOf) {
		return ClaimHydration{}, ErrInvalidTransfer
	}
	if err = tx.Commit(); err != nil {
		return ClaimHydration{}, err
	}
	return actual, nil
}

// HydrateClaimBlob records a durable grant-bound pin only after StageBlob has
// fsynced and independently verified the complete expected digest and length.
func (j *Journal) HydrateClaimBlob(ctx context.Context, grantID, digest string, reader io.Reader, byteLength int64) (ClaimHydration, error) {
	if j == nil || ctx == nil || reader == nil || !transferULID.MatchString(grantID) || !validDigest(digest) || byteLength < 0 {
		return ClaimHydration{}, ErrInvalidTransfer
	}
	j.mu.Lock()
	if j.db == nil {
		j.mu.Unlock()
		return ClaimHydration{}, ErrClosed
	}
	var manifestBytes []byte
	if err := j.db.QueryRowContext(ctx, `SELECT manifest FROM hydration_grants WHERE grant_id=?`, grantID).Scan(&manifestBytes); err != nil {
		j.mu.Unlock()
		if errors.Is(err, sql.ErrNoRows) {
			return ClaimHydration{}, ErrNotFound
		}
		return ClaimHydration{}, err
	}
	j.mu.Unlock()
	var manifest wipdwire.BlobManifest
	if err := wipdwire.DecodeCanonical(manifestBytes, &manifest, "schema", "domain_id", "authority_epoch", "as_of", "entries", "manifest_digest"); err != nil || verifyManifest(manifest) != nil {
		return ClaimHydration{}, ErrInvalidJournal
	}
	var expected *wipdwire.BlobManifestEntry
	for index := range manifest.Entries {
		if manifest.Entries[index].Digest == digest {
			expected = &manifest.Entries[index]
			break
		}
	}
	if expected == nil || expected.Requirement != "pin-before-use" || expected.ByteLength != uint64(byteLength) {
		return ClaimHydration{}, ErrInvalidTransfer
	}
	staged, err := j.StageBlob(claimHydrationReader{ctx: ctx, reader: reader}, byteLength)
	if err != nil {
		return ClaimHydration{}, err
	}
	if err = ctx.Err(); err != nil {
		return ClaimHydration{}, err
	}
	if staged.Digest != expected.Digest || uint64(staged.Size) != expected.ByteLength {
		return ClaimHydration{}, ErrBlobDigest
	}
	return j.persistClaimHydrationPin(ctx, grantID, *expected, manifest)
}

type claimHydrationReader struct {
	ctx    context.Context
	reader io.Reader
}

func (reader claimHydrationReader) Read(buffer []byte) (int, error) {
	if err := reader.ctx.Err(); err != nil {
		return 0, err
	}
	return reader.reader.Read(buffer)
}

func (j *Journal) persistClaimHydrationPin(ctx context.Context, grantID string, entry wipdwire.BlobManifestEntry, manifest wipdwire.BlobManifest) (ClaimHydration, error) {
	j.mu.Lock()
	defer j.mu.Unlock()
	if j.db == nil {
		return ClaimHydration{}, ErrClosed
	}
	tx, err := j.db.BeginTx(ctx, nil)
	if err != nil {
		return ClaimHydration{}, err
	}
	defer func() { _ = tx.Rollback() }()
	var storedManifest []byte
	if err = tx.QueryRowContext(ctx, `SELECT manifest FROM hydration_grants WHERE grant_id=?`, grantID).Scan(&storedManifest); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return ClaimHydration{}, ErrNotFound
		}
		return ClaimHydration{}, err
	}
	encoded, err := encodeManifest(manifest)
	if err != nil || !bytes.Equal(storedManifest, encoded) {
		return ClaimHydration{}, ErrInvalidTransfer
	}
	var stagedSize int64
	if err = tx.QueryRowContext(ctx, `SELECT byte_length FROM staged_blobs WHERE digest=?`, entry.Digest).Scan(&stagedSize); err != nil || stagedSize != int64(entry.ByteLength) {
		return ClaimHydration{}, ErrBlobDigest
	}
	if err = verifyBlobFile(filepath.Join(j.blobsDir, strings.TrimPrefix(entry.Digest, digestPrefix)), entry.Digest, stagedSize); err != nil {
		return ClaimHydration{}, ErrInvalidJournal
	}
	if _, err = tx.ExecContext(ctx, `INSERT INTO hydration_pins(grant_id,digest,byte_length) VALUES(?,?,?) ON CONFLICT(grant_id,digest) DO NOTHING`, grantID, entry.Digest, entry.ByteLength); err != nil {
		return ClaimHydration{}, err
	}
	var pinLength int64
	if err = tx.QueryRowContext(ctx, `SELECT byte_length FROM hydration_pins WHERE grant_id=? AND digest=?`, grantID, entry.Digest).Scan(&pinLength); err != nil || pinLength != int64(entry.ByteLength) {
		return ClaimHydration{}, ErrInvalidJournal
	}
	var pinned uint64
	if err = tx.QueryRowContext(ctx, `SELECT count(*) FROM hydration_pins WHERE grant_id=?`, grantID).Scan(&pinned); err != nil {
		return ClaimHydration{}, err
	}
	var required uint64
	if err = tx.QueryRowContext(ctx, `SELECT required_entry_count FROM hydration_grants WHERE grant_id=?`, grantID).Scan(&required); err != nil {
		return ClaimHydration{}, err
	}
	if pinned == required {
		if _, err = tx.ExecContext(ctx, `UPDATE hydration_grants SET state='offline-ready' WHERE grant_id=? AND state='hydrating'`, grantID); err != nil {
			return ClaimHydration{}, err
		}
	}
	actual, err := readClaimHydration(tx, grantID)
	if err != nil {
		return ClaimHydration{}, err
	}
	if err = tx.Commit(); err != nil {
		return ClaimHydration{}, err
	}
	return actual, nil
}

// ClaimHydrationStatus rechecks the durable pins before exposing readiness.
func (j *Journal) ClaimHydrationStatus(ctx context.Context, grantID string) (ClaimHydration, error) {
	if j == nil || ctx == nil || !transferULID.MatchString(grantID) {
		return ClaimHydration{}, ErrInvalidTransfer
	}
	j.mu.Lock()
	defer j.mu.Unlock()
	if j.db == nil {
		return ClaimHydration{}, ErrClosed
	}
	if err := checkClaimHydrationState(j.db, j.identity, j.blobsDir); err != nil {
		return ClaimHydration{}, err
	}
	tx, err := j.db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return ClaimHydration{}, err
	}
	defer func() { _ = tx.Rollback() }()
	status, err := readClaimHydration(tx, grantID)
	if err != nil {
		return ClaimHydration{}, err
	}
	if status.State == "offline-ready" && status.VerifiedPinnedEntryCount != status.RequiredEntryCount {
		return ClaimHydration{}, ErrInvalidJournal
	}
	if err = tx.Commit(); err != nil {
		return ClaimHydration{}, err
	}
	return status, nil
}

func readClaimHydration(tx *sql.Tx, grantID string) (ClaimHydration, error) {
	var result ClaimHydration
	var epoch, count int64
	var eventID sql.NullString
	err := tx.QueryRow(`SELECT grant_id,claim_id,claim_epoch,as_of_count,as_of_event_id,as_of_digest,manifest_digest,required_entry_count,state FROM hydration_grants WHERE grant_id=?`, grantID).
		Scan(&result.GrantID, &result.ClaimID, &epoch, &count, &eventID, &result.AsOf.Digest, &result.ManifestDigest, &result.RequiredEntryCount, &result.State)
	if err != nil {
		return result, err
	}
	if !transferULID.MatchString(result.GrantID) || !transferULID.MatchString(result.ClaimID) || epoch <= 0 || count < 0 ||
		!validDigest(result.AsOf.Digest) || !validDigest(result.ManifestDigest) || eventID.Valid != (count > 0) {
		return result, ErrInvalidJournal
	}
	result.ClaimEpoch = uint64(epoch)
	result.AsOf.EventCount = uint64(count)
	if eventID.Valid {
		if !transferULID.MatchString(eventID.String) {
			return result, ErrInvalidJournal
		}
		id := eventID.String
		result.AsOf.EventID = &id
	}
	if err = tx.QueryRow(`SELECT count(*) FROM hydration_pins WHERE grant_id=?`, grantID).Scan(&result.VerifiedPinnedEntryCount); err != nil {
		return result, err
	}
	return result, nil
}

func requiredManifestEntries(manifest wipdwire.BlobManifest) uint64 {
	var count uint64
	for _, entry := range manifest.Entries {
		if entry.Requirement == "pin-before-use" {
			count++
		}
	}
	return count
}

func checkClaimHydrationState(db *sql.DB, identity Identity, blobDir string) error {
	rows, err := db.Query(`SELECT grant_id,claim_id,claim_epoch,as_of_count,as_of_event_id,as_of_digest,manifest_digest,manifest,required_entry_count,state FROM hydration_grants ORDER BY grant_id`)
	if err != nil {
		return err
	}
	type grant struct {
		id, claimID, state, asOfDigest, manifestDigest string
		claimEpoch, asOfCount, required                uint64
		eventID                                        sql.NullString
		manifest                                       wipdwire.BlobManifest
	}
	var grants []grant
	for rows.Next() {
		var item grant
		var manifestBytes []byte
		if err = rows.Scan(&item.id, &item.claimID, &item.claimEpoch, &item.asOfCount, &item.eventID, &item.asOfDigest,
			&item.manifestDigest, &manifestBytes, &item.required, &item.state); err != nil {
			break
		}
		if !transferULID.MatchString(item.id) || !transferULID.MatchString(item.claimID) || item.claimEpoch == 0 ||
			item.eventID.Valid != (item.asOfCount > 0) || !validDigest(item.asOfDigest) ||
			(item.eventID.Valid && !transferULID.MatchString(item.eventID.String)) ||
			wipdwire.DecodeCanonical(manifestBytes, &item.manifest, "schema", "domain_id", "authority_epoch", "as_of", "entries", "manifest_digest") != nil ||
			item.manifest.DomainID != identity.DomainID || item.manifest.Epoch != identity.AuthorityEpoch || verifyManifest(item.manifest) != nil ||
			item.manifest.Digest != item.manifestDigest || item.manifest.AsOf.EventCount != item.asOfCount || item.manifest.AsOf.Digest != item.asOfDigest ||
			(item.manifest.AsOf.EventID == nil) != !item.eventID.Valid ||
			(item.eventID.Valid && *item.manifest.AsOf.EventID != item.eventID.String) ||
			item.required != requiredManifestEntries(item.manifest) || (item.state != "hydrating" && item.state != "offline-ready") {
			err = ErrInvalidJournal
			break
		}
		grants = append(grants, item)
	}
	if err == nil {
		err = rows.Err()
	}
	_ = rows.Close()
	if err != nil {
		return err
	}
	for _, item := range grants {
		pinned, pinErr := verifyClaimPins(db, item.id, item.manifest, blobDir)
		if pinErr != nil {
			return pinErr
		}
		if item.state == "offline-ready" && pinned != item.required || item.state == "hydrating" && (item.required == 0 || pinned >= item.required) {
			return fmt.Errorf("%w: claim hydration state disagrees with pins", ErrInvalidJournal)
		}
	}
	return nil
}

func claimHydrationReady(db *sql.DB, claim *operation.ClaimContext, blobDir string) error {
	if claim == nil || claim.ID == "" || claim.Epoch == "" {
		return ErrClaimNotReady
	}
	epoch, err := strconv.ParseUint(claim.Epoch, 10, 64)
	if err != nil || epoch == 0 || strconv.FormatUint(epoch, 10) != claim.Epoch {
		return ErrClaimNotReady
	}
	var grantID string
	if err = db.QueryRow(`SELECT grant_id FROM hydration_grants WHERE claim_id=? AND claim_epoch=? AND state='offline-ready'`, claim.ID, epoch).Scan(&grantID); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return ErrClaimNotReady
		}
		return err
	}
	if err = checkOneClaimHydration(db, grantID, blobDir); err != nil {
		return err
	}
	return nil
}

func checkOneClaimHydration(db *sql.DB, grantID, blobDir string) error {
	var manifestBytes []byte
	var required uint64
	var state string
	if err := db.QueryRow(`SELECT manifest,required_entry_count,state FROM hydration_grants WHERE grant_id=?`, grantID).Scan(&manifestBytes, &required, &state); err != nil {
		return err
	}
	var manifest wipdwire.BlobManifest
	if wipdwire.DecodeCanonical(manifestBytes, &manifest, "schema", "domain_id", "authority_epoch", "as_of", "entries", "manifest_digest") != nil || verifyManifest(manifest) != nil ||
		required != requiredManifestEntries(manifest) || state != "offline-ready" {
		return ErrClaimNotReady
	}
	count, err := verifyClaimPins(db, grantID, manifest, blobDir)
	if err != nil {
		return err
	}
	if count != required {
		return ErrClaimNotReady
	}
	return nil
}

func verifyClaimPins(db *sql.DB, grantID string, manifest wipdwire.BlobManifest, blobDir string) (uint64, error) {
	entries := make(map[string]wipdwire.BlobManifestEntry)
	for _, entry := range manifest.Entries {
		if entry.Requirement == "pin-before-use" {
			entries[entry.Digest] = entry
		}
	}
	rows, err := db.Query(`SELECT digest,byte_length FROM hydration_pins WHERE grant_id=?`, grantID)
	if err != nil {
		return 0, err
	}
	type pin struct {
		digest string
		length int64
	}
	var pins []pin
	for rows.Next() {
		var item pin
		if err = rows.Scan(&item.digest, &item.length); err != nil {
			break
		}
		pins = append(pins, item)
	}
	if err == nil {
		err = rows.Err()
	}
	_ = rows.Close()
	if err != nil {
		return 0, err
	}
	for _, item := range pins {
		entry, ok := entries[item.digest]
		if !ok || item.length < 0 || uint64(item.length) != entry.ByteLength {
			return 0, ErrInvalidJournal
		}
		var staged int64
		if err = db.QueryRow(`SELECT byte_length FROM staged_blobs WHERE digest=?`, item.digest).Scan(&staged); err != nil || staged != item.length {
			return 0, ErrInvalidJournal
		}
		if err = verifyBlobFile(filepath.Join(blobDir, strings.TrimPrefix(item.digest, digestPrefix)), item.digest, item.length); err != nil {
			return 0, err
		}
		delete(entries, item.digest)
	}
	if len(entries) != 0 && uint64(len(pins)) == requiredManifestEntries(manifest) {
		return 0, ErrInvalidJournal
	}
	return uint64(len(pins)), nil
}
