package wipdjournal

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"database/sql"
	"errors"
	"fmt"
	"io"
	"path/filepath"
	"strconv"
	"strings"
	"time"

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

func createInstalledClaimGrantSchema(executor sqlExecutor) error {
	for _, statement := range []string{
		`CREATE TABLE installed_claim_grants(
			grant_id TEXT PRIMARY KEY,
			acquire_command_id TEXT NOT NULL UNIQUE,
			acquire_request_hash TEXT NOT NULL CHECK(length(acquire_request_hash)=71),
			claim_id TEXT NOT NULL,
			claim_epoch INTEGER NOT NULL CHECK(claim_epoch>0),
			matter_id TEXT NOT NULL,
			batch_id TEXT NOT NULL,
			dispatch_id TEXT NOT NULL,
			owner_root_spki TEXT NOT NULL CHECK(length(owner_root_spki)=71),
			owner_root_public_key BLOB NOT NULL CHECK(length(owner_root_public_key)=32),
			artifact_key_certificate BLOB NOT NULL,
			verified_at TEXT NOT NULL,
			start_count INTEGER NOT NULL CHECK(start_count>=0),
			start_event_id TEXT,
			start_digest TEXT NOT NULL CHECK(length(start_digest)=71),
			end_count INTEGER NOT NULL CHECK(end_count>=start_count),
			end_event_id TEXT,
			end_digest TEXT NOT NULL CHECK(length(end_digest)=71),
			manifest_digest TEXT NOT NULL CHECK(length(manifest_digest)=71),
			grant_wrapper BLOB NOT NULL,
			grant_start BLOB NOT NULL,
			grant_end BLOB NOT NULL,
			acquire_receipt BLOB NOT NULL,
			manifest BLOB NOT NULL,
			CHECK((start_count=0 AND start_event_id IS NULL) OR (start_count>0 AND start_event_id IS NOT NULL)),
			CHECK((end_count=0 AND end_event_id IS NULL) OR (end_count>0 AND end_event_id IS NOT NULL))
		) STRICT, WITHOUT ROWID`,
		`CREATE TRIGGER installed_claim_grant_no_update BEFORE UPDATE ON installed_claim_grants
			BEGIN SELECT RAISE(ABORT,'immutable installed claim grant'); END`,
		`CREATE TRIGGER installed_claim_grant_no_delete BEFORE DELETE ON installed_claim_grants
			BEGIN SELECT RAISE(ABORT,'retained installed claim grant'); END`,
		`CREATE TRIGGER hydration_grant_requires_installed BEFORE INSERT ON hydration_grants
			WHEN NOT EXISTS(SELECT 1 FROM installed_claim_grants g WHERE g.grant_id=NEW.grant_id AND g.claim_id=NEW.claim_id AND
				g.claim_epoch=NEW.claim_epoch AND g.end_count=NEW.as_of_count AND g.end_event_id IS NEW.as_of_event_id AND
				g.end_digest=NEW.as_of_digest AND g.manifest_digest=NEW.manifest_digest AND g.manifest=NEW.manifest)
			BEGIN SELECT RAISE(ABORT,'hydration requires an atomically installed claim grant'); END`,
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

// upgradeSchemaV4 adds the acquisition proof table. v4 readiness rows were
// created from caller-supplied claim IDs and cannot be promoted to verified
// acquisition evidence; fail closed rather than recovering them as ready.
func upgradeSchemaV4(db *sql.DB, identity Identity) error {
	var version int
	var repoID, domainID, environmentID, marker string
	var epoch int64
	if err := db.QueryRow(`PRAGMA user_version`).Scan(&version); err != nil || version != 4 {
		return fmt.Errorf("%w: Environment claim-grant upgrade requires v4, got %d (%v)", ErrInvalidJournal, version, err)
	}
	if err := db.QueryRow(`SELECT repo_id,domain_id,authority_epoch,environment_id FROM environment_state WHERE singleton=1`).Scan(&repoID, &domainID, &epoch, &environmentID); err != nil ||
		repoID != identity.RepoID || domainID != identity.DomainID || epoch != int64(identity.AuthorityEpoch) || environmentID != identity.EnvironmentID {
		return ErrInvalidIdentity
	}
	if err := db.QueryRow(`SELECT name FROM schema_migrations WHERE version=4`).Scan(&marker); err != nil || marker != "environment-claim-hydration-pins" {
		return fmt.Errorf("%w: v4 claim-grant-upgrade marker %q: %v", ErrInvalidJournal, marker, err)
	}
	if err := checkSchemaObjectsV4(db); err != nil {
		return fmt.Errorf("%w: v4 claim-grant-upgrade schema: %v", ErrInvalidJournal, err)
	}
	var oldReadiness int
	if err := db.QueryRow(`SELECT count(*) FROM hydration_grants`).Scan(&oldReadiness); err != nil {
		return err
	}
	if oldReadiness != 0 {
		return fmt.Errorf("%w: v4 contains hydration readiness without acquisition proof", ErrInvalidJournal)
	}
	tx, err := db.BeginTx(context.Background(), nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	if _, err = tx.Exec(`DROP TABLE schema_migrations`); err != nil {
		return err
	}
	if _, err = tx.Exec(`CREATE TABLE schema_migrations(version INTEGER PRIMARY KEY CHECK(version=5),name TEXT NOT NULL CHECK(name='environment-verified-claim-grants')) STRICT`); err != nil {
		return err
	}
	if _, err = tx.Exec(`INSERT INTO schema_migrations(version,name) VALUES(5,'environment-verified-claim-grants')`); err != nil {
		return err
	}
	if err = createInstalledClaimGrantSchema(tx); err != nil {
		return err
	}
	if _, err = tx.Exec(`PRAGMA user_version=5`); err != nil {
		return err
	}
	return tx.Commit()
}

// BeginClaimHydration returns readiness created atomically with an installed,
// signature-verified acquisition grant. It accepts no caller claim IDs or
// manifest that could create an unbound empty-closure success.
func (j *Journal) BeginClaimHydration(ctx context.Context, grantID string) (ClaimHydration, error) {
	if j == nil || ctx == nil || !transferULID.MatchString(grantID) {
		return ClaimHydration{}, ErrInvalidTransfer
	}
	j.mu.Lock()
	defer j.mu.Unlock()
	if j.db == nil {
		return ClaimHydration{}, ErrClosed
	}
	tx, err := j.db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return ClaimHydration{}, err
	}
	defer func() { _ = tx.Rollback() }()
	var installed bool
	if err = tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM installed_claim_grants WHERE grant_id=?)`, grantID).Scan(&installed); err != nil {
		return ClaimHydration{}, err
	}
	if !installed {
		return ClaimHydration{}, ErrNotFound
	}
	actual, err := readClaimHydration(tx, grantID)
	if errors.Is(err, sql.ErrNoRows) {
		return ClaimHydration{}, ErrInvalidJournal
	}
	if err != nil {
		return ClaimHydration{}, err
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

// ValidateInstalledClaimContext binds a claim-delivery command to the exact
// locally installed acquisition grant, its Worktree, its Matter, and a
// currently offline-ready pin set. It also rejects a grant closed or replaced
// by an already installed authority tail.
func (j *Journal) ValidateInstalledClaimContext(ctx context.Context, claim *operation.ClaimContext, matterID, worktreeID string) error {
	if j == nil || ctx == nil || claim == nil || !transferULID.MatchString(claim.ID) ||
		!transferULID.MatchString(matterID) || !transferULID.MatchString(worktreeID) {
		return ErrClaimNotReady
	}
	j.mu.Lock()
	defer j.mu.Unlock()
	if j.db == nil {
		return ErrClosed
	}
	return j.validateInstalledClaimContextLocked(ctx, claim, matterID, worktreeID)
}

// ValidateCommandClaimReadiness resolves the command's Matter through the
// installed immutable event prefix, then validates the exact grant and
// hydration product.
func (j *Journal) ValidateCommandClaimReadiness(ctx context.Context, request operation.Request) error {
	if j == nil || ctx == nil || request.Context.Repo != j.identity.RepoID || !transferULID.MatchString(request.Context.Clone) {
		return ErrClaimNotReady
	}
	j.mu.Lock()
	defer j.mu.Unlock()
	if j.db == nil {
		return ErrClosed
	}
	input := request.Input
	if _, declaration := input.(operation.GateDeclareInput); declaration {
		// Repo declarations have no node input. Resolve the exact grant's
		// Matter, then apply the same installed lineage and readiness fences.
		if request.Claim == nil {
			return ErrClaimNotReady
		}
		var matterID string
		err := j.db.QueryRowContext(ctx, `SELECT matter_id FROM installed_claim_grants WHERE claim_id=? AND claim_epoch=?`,
			request.Claim.ID, request.Claim.Epoch).Scan(&matterID)
		if errors.Is(err, sql.ErrNoRows) {
			return ErrClaimNotReady
		}
		if err != nil {
			return err
		}
		input = operation.MatterFinishInput{MatterID: matterID}
	}
	matterID, ready, err := installedClaimMatter(ctx, j.db, j.identity.RepoID, input)
	if err != nil {
		return err
	}
	if !ready {
		return ErrClaimNotReady
	}
	return j.validateInstalledClaimContextLocked(ctx, request.Claim, matterID, request.Context.Worktree)
}

type installedClaimNode struct {
	kind, matterID, parentID string
	live                     bool
}

func installedClaimMatter(ctx context.Context, db *sql.DB, repoID string, input operation.Input) (string, bool, error) {
	targetID := ""
	parentKinds := map[string]bool{}
	targetKinds := map[string]bool{}
	switch value := input.(type) {
	case operation.MatterFinishInput:
		targetID, targetKinds[value.MatterID] = value.MatterID, true
	case operation.MatterLocatorRepairInput:
		targetID, targetKinds[value.MatterID] = value.MatterID, true
	case operation.NodeLifecycleInput:
		targetID, targetKinds[value.NodeID] = value.NodeID, true
	case operation.GateExemptionRepairInput:
		targetID, targetKinds[value.NodeID] = value.NodeID, true
	case operation.GateCloseInput:
		targetID, targetKinds[value.NodeID] = value.NodeID, true
	case operation.GateDismissInput:
		targetID, targetKinds[value.NodeID] = value.NodeID, true
	case operation.StageCreateInput:
		targetID, targetKinds[value.MatterID] = value.MatterID, true
	case operation.StepLifecycleInput:
		targetID, targetKinds[value.StepID] = value.StepID, true
	case operation.StepCancelInput:
		targetID, targetKinds[value.StepID] = value.StepID, true
	case operation.StepCreateInput:
		targetID, parentKinds[value.ParentID] = value.ParentID, true
	case operation.StepInsertInput:
		targetID, parentKinds[value.ParentID] = value.ParentID, true
	case operation.StepReorderInput:
		targetID, parentKinds[value.ParentID] = value.ParentID, true
	case operation.StepReplaceInput:
		targetID, targetKinds[value.StepID] = value.StepID, true
	case operation.StepRemoveInput:
		targetID, targetKinds[value.StepID] = value.StepID, true
	case operation.ContentWriteInput:
		targetID, targetKinds[value.SubjectID] = value.SubjectID, true
	case operation.FindingAppendInput:
		targetID, targetKinds[value.SubjectID] = value.SubjectID, true
	default:
		return "", false, nil
	}
	if !transferULID.MatchString(targetID) {
		return "", false, nil
	}
	rows, err := db.QueryContext(ctx, `SELECT record FROM installed_events ORDER BY position`)
	if err != nil {
		return "", false, err
	}
	nodes := make(map[string]installedClaimNode)
	for rows.Next() {
		var record []byte
		if err = rows.Scan(&record); err != nil {
			break
		}
		fields, decodeErr := wipdwire.DecodeCanonicalMap(record,
			"schema", "event_id", "domain_id", "command_id", "request_hash", "environment", "acted_at", "occurred_at", "kind", "subject_id", "repo_id", "payload")
		if decodeErr != nil {
			err = ErrInvalidJournal
			break
		}
		if fields["repo_id"] != repoID {
			continue
		}
		payload, _ := fields["payload"].(map[string]any)
		subject := asString(fields["subject_id"])
		switch fields["kind"] {
		case "matter.created":
			if payload["id"] == subject {
				nodes[subject] = installedClaimNode{kind: "matter", matterID: subject, live: true}
			}
		case "stage.created", "step.created", "step.inserted":
			parentID := asString(payload["parent"])
			kind := "step"
			if fields["kind"] == "stage.created" {
				parentID = asString(payload["matter_id"])
				kind = "stage"
			}
			matterID := parentID
			parent := nodes[parentID]
			if parent.kind == "stage" {
				matterID = parent.matterID
			}
			if (kind == "stage" && nodes[matterID].kind == "matter" && nodes[matterID].live) ||
				(kind == "step" && parent.live && (parent.kind == "matter" || parent.kind == "stage")) {
				nodes[subject] = installedClaimNode{kind: kind, matterID: matterID, parentID: parentID, live: true}
			}
		case "step.replaced":
			previous := nodes[subject]
			replacement := asString(payload["replacement"])
			if previous.kind == "step" && previous.live && transferULID.MatchString(replacement) {
				previous.live = false
				nodes[subject] = previous
				nodes[replacement] = installedClaimNode{kind: "step", matterID: previous.matterID, parentID: previous.parentID, live: true}
			}
		case "step.removed":
			previous := nodes[subject]
			previous.live = false
			nodes[subject] = previous
		}
	}
	if err == nil {
		err = rows.Err()
	}
	_ = rows.Close()
	if err != nil {
		return "", false, err
	}
	node := nodes[targetID]
	if !node.live || parentKinds[targetID] && node.kind != "matter" && node.kind != "stage" ||
		targetKinds[targetID] && node.kind != "matter" && node.kind != "stage" && node.kind != "step" {
		return "", false, nil
	}
	return node.matterID, true, nil
}

func (j *Journal) validateInstalledClaimContextLocked(ctx context.Context, claim *operation.ClaimContext, matterID, worktreeID string) error {
	if claim == nil || !transferULID.MatchString(claim.ID) {
		return ErrClaimNotReady
	}
	epoch, err := strconv.ParseUint(claim.Epoch, 10, 64)
	if err != nil || epoch == 0 || strconv.FormatUint(epoch, 10) != claim.Epoch {
		return ErrClaimNotReady
	}
	var grantID string
	if err = j.db.QueryRowContext(ctx, `SELECT grant_id FROM installed_claim_grants WHERE claim_id=? AND claim_epoch=?`, claim.ID, epoch).Scan(&grantID); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return ErrClaimNotReady
		}
		return err
	}
	grant, err := verifyInstalledClaimGrant(j.db, j.identity, grantID)
	if err != nil || grant.claimID != claim.ID || grant.claimEpoch != epoch || grant.matterID != matterID || grant.WorktreeID() != worktreeID {
		return ErrClaimNotReady
	}
	if err = claimHydrationReady(j.db, claim, j.blobsDir); err != nil {
		return err
	}
	rows, err := j.db.QueryContext(ctx, `SELECT record FROM installed_events WHERE position>? ORDER BY position`, grant.end.EventCount)
	if err != nil {
		return err
	}
	defer func() { _ = rows.Close() }()
	for rows.Next() {
		var record []byte
		if err = rows.Scan(&record); err != nil {
			return err
		}
		fields, decodeErr := wipdwire.DecodeCanonicalMap(record,
			"schema", "event_id", "domain_id", "command_id", "request_hash", "environment", "acted_at", "occurred_at", "kind", "subject_id", "repo_id", "payload")
		if decodeErr != nil {
			return ErrInvalidJournal
		}
		payload, _ := fields["payload"].(map[string]any)
		switch fields["kind"] {
		case "claim.released", "claim.stood-down":
			if payload["claim_id"] == claim.ID {
				return ErrClaimNotReady
			}
		case "claim.acquired":
			if payload["matter_id"] == matterID && payload["claim_id"] != claim.ID {
				return ErrClaimNotReady
			}
		}
	}
	if err = rows.Err(); err != nil {
		return err
	}
	return nil
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
	var installedCount, hydrationCount int
	if err := db.QueryRow(`SELECT count(*) FROM installed_claim_grants`).Scan(&installedCount); err != nil {
		return err
	}
	if err := db.QueryRow(`SELECT count(*) FROM hydration_grants`).Scan(&hydrationCount); err != nil || installedCount != hydrationCount {
		return ErrInvalidJournal
	}
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
		installed, grantErr := verifyInstalledClaimGrant(db, identity, item.id)
		if grantErr != nil || installed.claimID != item.claimID || installed.claimEpoch != item.claimEpoch ||
			!sameTransferAnchor(installed.end, wipdwire.PrefixAnchor{EventCount: item.asOfCount, EventID: optionalString(item.eventID), Digest: item.asOfDigest}) ||
			installed.manifest.Digest != item.manifestDigest {
			return ErrInvalidJournal
		}
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

func verifyInstalledClaimGrant(db *sql.DB, identity Identity, grantID string) (VerifiedClaimGrant, error) {
	var grant VerifiedClaimGrant
	var acquireID, requestHash, claimID, matterID, batchID, dispatchID, ownerSPKI, verifiedAt string
	var ownerPublic, certificate, wrapper, startBytes, endBytes, receiptBytes, manifestBytes []byte
	var startCount, endCount, claimEpoch int64
	var startEventID, endEventID sql.NullString
	var startDigest, endDigest, manifestDigest string
	err := db.QueryRow(`SELECT acquire_command_id,acquire_request_hash,claim_id,claim_epoch,matter_id,batch_id,dispatch_id,
		owner_root_spki,owner_root_public_key,artifact_key_certificate,verified_at,start_count,start_event_id,start_digest,
		end_count,end_event_id,end_digest,manifest_digest,grant_wrapper,grant_start,grant_end,acquire_receipt,manifest
		FROM installed_claim_grants WHERE grant_id=?`, grantID).Scan(
		&acquireID, &requestHash, &claimID, &claimEpoch, &matterID, &batchID, &dispatchID,
		&ownerSPKI, &ownerPublic, &certificate, &verifiedAt, &startCount, &startEventID, &startDigest,
		&endCount, &endEventID, &endDigest, &manifestDigest, &wrapper, &startBytes, &endBytes, &receiptBytes, &manifestBytes)
	if err != nil {
		return grant, err
	}
	if identity.OwnerRootSPKI == "" || ownerSPKI != identity.OwnerRootSPKI || startCount < 0 || endCount < startCount || claimEpoch <= 0 ||
		uint64(endCount-startCount) > maxVerifiedEvents || startEventID.Valid != (startCount > 0) || endEventID.Valid != (endCount > 0) {
		return grant, ErrInvalidJournal
	}
	var currentCount int64
	if err = db.QueryRow(`SELECT event_count FROM environment_install WHERE singleton=1`).Scan(&currentCount); err != nil || currentCount < endCount {
		return grant, ErrInvalidJournal
	}
	start, startOK := storedGrantAnchor(uint64(startCount), startEventID, startDigest)
	end, endOK := storedGrantAnchor(uint64(endCount), endEventID, endDigest)
	if !startOK || !endOK {
		return grant, ErrInvalidJournal
	}
	var manifest wipdwire.BlobManifest
	if wipdwire.DecodeCanonical(manifestBytes, &manifest, "schema", "domain_id", "authority_epoch", "as_of", "entries", "manifest_digest") != nil ||
		manifest.Digest != manifestDigest || !sameTransferAnchor(manifest.AsOf, end) || manifest.DomainID != identity.DomainID ||
		manifest.Epoch != identity.AuthorityEpoch || verifyManifest(manifest) != nil {
		return grant, ErrInvalidJournal
	}
	rows, err := db.Query(`SELECT event_id,record FROM installed_events WHERE position>? AND position<=? ORDER BY position`, startCount, endCount)
	if err != nil {
		return grant, err
	}
	records := make([]wipdwire.EventRecord, 0, endCount-startCount)
	for rows.Next() {
		var record wipdwire.EventRecord
		if err = rows.Scan(&record.EventID, &record.Record); err != nil {
			break
		}
		records = append(records, record)
	}
	if err == nil {
		err = rows.Err()
	}
	_ = rows.Close()
	if err != nil || uint64(len(records)) != uint64(endCount-startCount) {
		return grant, ErrInvalidJournal
	}
	transfer, err := VerifyTransfer(identity.DomainID, identity.AuthorityEpoch, start, end, records, manifest)
	if err != nil {
		return grant, ErrInvalidJournal
	}
	verifiedAtTime, parseErr := time.Parse(time.RFC3339Nano, verifiedAt)
	if parseErr != nil || verifiedAtTime.UTC().Format(time.RFC3339Nano) != verifiedAt {
		return grant, ErrInvalidJournal
	}
	grant, err = VerifyClaimGrant(identity, ClaimGrantTrust{
		OwnerRootPublicKey: ed25519.PublicKey(ownerPublic), OwnerRootSPKI: ownerSPKI, VerifiedAt: verifiedAtTime,
	}, ClaimGrantEvidence{
		ArtifactKeyCertificate: certificate, Wrapper: wrapper, Start: startBytes, End: endBytes, Transfer: transfer,
	})
	encodedManifest, encodeErr := encodeManifest(grant.manifest)
	if err != nil || encodeErr != nil || grant.acquireCommandID != acquireID || grant.acquireRequestHash != requestHash || grant.claimID != claimID ||
		grant.claimEpoch != uint64(claimEpoch) || grant.matterID != matterID || grant.batchID != batchID || grant.dispatchID != dispatchID ||
		!bytes.Equal(grant.receipt, receiptBytes) || !bytes.Equal(encodedManifest, manifestBytes) {
		return VerifiedClaimGrant{}, ErrInvalidJournal
	}
	return grant, nil
}

func storedGrantAnchor(count uint64, eventID sql.NullString, digest string) (wipdwire.PrefixAnchor, bool) {
	anchor := wipdwire.PrefixAnchor{EventCount: count, Digest: digest}
	if eventID.Valid {
		anchor.EventID = &eventID.String
	}
	return anchor, validTransferAnchor(anchor) && eventID.Valid == (count > 0)
}

func optionalString(value sql.NullString) *string {
	if !value.Valid {
		return nil
	}
	text := value.String
	return &text
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
	if err = db.QueryRow(`SELECT h.grant_id FROM hydration_grants h JOIN installed_claim_grants g ON g.grant_id=h.grant_id
		WHERE h.claim_id=? AND h.claim_epoch=? AND h.state='offline-ready' AND g.claim_id=h.claim_id AND g.claim_epoch=h.claim_epoch AND
		g.end_count=h.as_of_count AND g.end_event_id IS h.as_of_event_id AND g.end_digest=h.as_of_digest AND g.manifest_digest=h.manifest_digest AND g.manifest=h.manifest`,
		claim.ID, epoch).Scan(&grantID); err != nil {
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
	if err := db.QueryRow(`SELECT h.manifest,h.required_entry_count,h.state FROM hydration_grants h JOIN installed_claim_grants g ON g.grant_id=h.grant_id
		WHERE h.grant_id=? AND g.claim_id=h.claim_id AND g.claim_epoch=h.claim_epoch AND g.end_count=h.as_of_count AND
		g.end_event_id IS h.as_of_event_id AND g.end_digest=h.as_of_digest AND g.manifest_digest=h.manifest_digest AND g.manifest=h.manifest`,
		grantID).Scan(&manifestBytes, &required, &state); err != nil {
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
