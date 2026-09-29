package wipdjournal

import (
	"bytes"
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"time"

	"github.com/procrastivity/wip/internal/operation"
	"github.com/procrastivity/wip/internal/wipdwire"
)

// ClaimAcquireAttempt is the exact Environment-local identity and resolution
// state for one claim.acquire@v1 lifecycle command.
type ClaimAcquireAttempt struct {
	ID             string
	RequestHash    string
	CanonicalBytes []byte
	EnvironmentSeq uint64
	MatterID       string
	CloneID        string
	WorktreeID     string
	DispatchID     string
	Actor          string
	Installed      wipdwire.PrefixAnchor
	Returned       bool
	Receipt        []byte
	ResultCode     operation.ResultCode
	GrantID        string
}

// ClaimGrantSummary is the immutable identity installed with a verified grant.
type ClaimGrantSummary struct {
	GrantID     string
	CommandID   string
	RequestHash string
	ClaimID     string
	ClaimEpoch  uint64
	MatterID    string
	BatchID     string
	DispatchID  string
	AsOf        wipdwire.PrefixAnchor
	Manifest    string
}

func createClaimAcquireSchema(executor sqlExecutor) error {
	for _, statement := range []string{
		`CREATE TABLE claim_acquire_attempts(
			command_id TEXT PRIMARY KEY,
			environment_sequence INTEGER NOT NULL UNIQUE CHECK(environment_sequence>0),
			request_hash TEXT NOT NULL CHECK(length(request_hash)=71),
			canonical_bytes BLOB NOT NULL,
			installed_count INTEGER NOT NULL CHECK(installed_count>=0),
			installed_event_id TEXT,
			installed_digest TEXT NOT NULL CHECK(length(installed_digest)=71),
			state TEXT NOT NULL CHECK(state IN ('attempt-prepared','returned')),
			canonical_receipt BLOB,
			result_code TEXT CHECK(result_code IS NULL OR result_code IN ('result.succeeded','result.rejected','result.refused','result.failed')),
			grant_id TEXT,
			CHECK((installed_count=0 AND installed_event_id IS NULL) OR (installed_count>0 AND installed_event_id IS NOT NULL)),
			CHECK((state='attempt-prepared' AND canonical_receipt IS NULL AND result_code IS NULL AND grant_id IS NULL) OR
				(state='returned' AND canonical_receipt IS NOT NULL AND result_code IS NOT NULL AND
				((result_code='result.succeeded' AND grant_id IS NOT NULL) OR (result_code!='result.succeeded' AND grant_id IS NULL))))
		) STRICT, WITHOUT ROWID`,
		`CREATE TRIGGER claim_acquire_identity_immutable BEFORE UPDATE OF command_id,environment_sequence,request_hash,canonical_bytes,installed_count,installed_event_id,installed_digest ON claim_acquire_attempts
			BEGIN SELECT RAISE(ABORT,'immutable claim-acquire identity'); END`,
		`CREATE TRIGGER claim_acquire_no_delete BEFORE DELETE ON claim_acquire_attempts
			BEGIN SELECT RAISE(ABORT,'retained claim-acquire identity'); END`,
		`CREATE TRIGGER claim_acquire_state_transition BEFORE UPDATE OF state ON claim_acquire_attempts
			WHEN NOT (OLD.state='attempt-prepared' AND NEW.state='returned')
			BEGIN SELECT RAISE(ABORT,'invalid claim-acquire transition'); END`,
		`CREATE TRIGGER claim_acquire_before_insert BEFORE INSERT ON claim_acquire_attempts
			WHEN EXISTS(SELECT 1 FROM commands WHERE state!='returned') OR
				EXISTS(SELECT 1 FROM birth_release_attempts WHERE state='attempt-prepared') OR
				EXISTS(SELECT 1 FROM claim_acquire_attempts WHERE state='attempt-prepared')
			BEGIN SELECT RAISE(ABORT,'Environment work is unresolved'); END`,
		`CREATE TRIGGER claim_acquire_command_id_conflict BEFORE INSERT ON claim_acquire_attempts
			WHEN EXISTS(SELECT 1 FROM commands WHERE command_id=NEW.command_id) OR
				EXISTS(SELECT 1 FROM birth_release_attempts WHERE command_id=NEW.command_id)
			BEGIN SELECT RAISE(ABORT,'claim-acquire command ID conflicts with journal identity'); END`,
		`CREATE TRIGGER command_after_pending_claim_acquire BEFORE INSERT ON commands
			WHEN EXISTS(SELECT 1 FROM claim_acquire_attempts WHERE state='attempt-prepared')
			BEGIN SELECT RAISE(ABORT,'claim acquisition outcome is unresolved'); END`,
		`CREATE TRIGGER command_claim_acquire_id_conflict BEFORE INSERT ON commands
			WHEN EXISTS(SELECT 1 FROM claim_acquire_attempts WHERE command_id=NEW.command_id)
			BEGIN SELECT RAISE(ABORT,'journal command ID conflicts with claim acquisition'); END`,
		`CREATE TRIGGER birth_release_after_pending_claim_acquire BEFORE INSERT ON birth_release_attempts
			WHEN EXISTS(SELECT 1 FROM claim_acquire_attempts WHERE state='attempt-prepared')
			BEGIN SELECT RAISE(ABORT,'claim acquisition outcome is unresolved'); END`,
	} {
		if _, err := executor.Exec(statement); err != nil {
			return err
		}
	}
	return nil
}

// upgradeSchemaV5 adds the exact local attempt needed to resolve an authority
// acquisition and bind its returned grant before it becomes usable.
func upgradeSchemaV5(db *sql.DB, identity Identity) error {
	var version int
	var repoID, domainID, environmentID, marker string
	var epoch int64
	if err := db.QueryRow(`PRAGMA user_version`).Scan(&version); err != nil || version != 5 {
		return fmt.Errorf("%w: claim-acquire upgrade requires v5, got %d (%v)", ErrInvalidJournal, version, err)
	}
	if err := db.QueryRow(`SELECT repo_id,domain_id,authority_epoch,environment_id FROM environment_state WHERE singleton=1`).Scan(&repoID, &domainID, &epoch, &environmentID); err != nil ||
		repoID != identity.RepoID || domainID != identity.DomainID || epoch != int64(identity.AuthorityEpoch) || environmentID != identity.EnvironmentID {
		return ErrInvalidIdentity
	}
	if err := db.QueryRow(`SELECT name FROM schema_migrations WHERE version=5`).Scan(&marker); err != nil || marker != "environment-verified-claim-grants" {
		return fmt.Errorf("%w: v5 claim-acquire-upgrade marker %q: %v", ErrInvalidJournal, marker, err)
	}
	if err := checkSchemaObjectsVersion(db, true, true, false); err != nil {
		return fmt.Errorf("%w: v5 claim-acquire-upgrade schema: %v", ErrInvalidJournal, err)
	}
	tx, err := db.BeginTx(context.Background(), nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	if _, err = tx.Exec(`DROP TABLE schema_migrations`); err != nil {
		return err
	}
	if _, err = tx.Exec(`CREATE TABLE schema_migrations(version INTEGER PRIMARY KEY CHECK(version=6),name TEXT NOT NULL CHECK(name='environment-claim-acquire-attempts')) STRICT`); err != nil {
		return err
	}
	if _, err = tx.Exec(`INSERT INTO schema_migrations(version,name) VALUES(6,'environment-claim-acquire-attempts')`); err != nil {
		return err
	}
	if err = createClaimAcquireSchema(tx); err != nil {
		return err
	}
	if _, err = tx.Exec(`PRAGMA user_version=6`); err != nil {
		return err
	}
	return tx.Commit()
}

// PrepareClaimAcquire allocates and persists the lifecycle command identity at
// the Environment's next sequence. Retries retain the original bytes/time and
// reject any changed Matter, Worktree, or Dispatch intent.
func (j *Journal) PrepareClaimAcquire(commandID, matterID, cloneID, worktreeID, dispatchID, actor string, installed wipdwire.PrefixAnchor) (ClaimAcquireAttempt, error) {
	if j == nil || !identityPattern.MatchString(commandID) || !identityPattern.MatchString(matterID) ||
		!identityPattern.MatchString(cloneID) || !identityPattern.MatchString(worktreeID) ||
		!identityPattern.MatchString(dispatchID) || actor == "" || !validTransferAnchor(installed) {
		return ClaimAcquireAttempt{}, ErrInvalidCommand
	}
	j.mu.Lock()
	defer j.mu.Unlock()
	if j.db == nil {
		return ClaimAcquireAttempt{}, ErrClosed
	}
	if existing, err := readClaimAcquireAttempt(j.db, commandID); err == nil {
		if existing.MatterID != matterID || existing.CloneID != cloneID || existing.WorktreeID != worktreeID ||
			existing.DispatchID != dispatchID || existing.Actor != actor {
			return ClaimAcquireAttempt{}, ErrCommandIDConflict
		}
		return existing, nil
	} else if !errors.Is(err, ErrNotFound) {
		return ClaimAcquireAttempt{}, err
	}
	tx, err := j.db.BeginTx(context.Background(), nil)
	if err != nil {
		return ClaimAcquireAttempt{}, err
	}
	defer func() { _ = tx.Rollback() }()
	var nextSequence int64
	if err = tx.QueryRow(`SELECT next_environment_sequence FROM environment_state WHERE singleton=1`).Scan(&nextSequence); err != nil {
		return ClaimAcquireAttempt{}, err
	}
	if nextSequence < 1 || nextSequence == int64(^uint64(0)>>1) {
		return ClaimAcquireAttempt{}, ErrInvalidJournal
	}
	current, err := readInstallState(tx)
	if err != nil || !sameTransferAnchor(current.anchor, installed) {
		return ClaimAcquireAttempt{}, ErrInvalidTransfer
	}
	command, err := canonicalClaimAcquire(j.identity, uint64(nextSequence), commandID, matterID, cloneID, worktreeID, dispatchID, actor)
	if err != nil {
		return ClaimAcquireAttempt{}, err
	}
	hash := requestHash(command)
	var installedID any
	if installed.EventID != nil {
		installedID = *installed.EventID
	}
	if _, err = tx.Exec(`INSERT INTO claim_acquire_attempts(command_id,environment_sequence,request_hash,canonical_bytes,installed_count,installed_event_id,installed_digest,state)
		VALUES(?,?,?,?,?,?,?,'attempt-prepared')`, commandID, nextSequence, hash, command, installed.EventCount, installedID, installed.Digest); err != nil {
		return ClaimAcquireAttempt{}, err
	}
	if _, err = tx.Exec(`UPDATE environment_state SET next_environment_sequence=? WHERE singleton=1 AND next_environment_sequence=?`, nextSequence+1, nextSequence); err != nil {
		return ClaimAcquireAttempt{}, err
	}
	if err = tx.Commit(); err != nil {
		return ClaimAcquireAttempt{}, err
	}
	return decodeClaimAcquireAttempt(commandID, uint64(nextSequence), hash, command,
		installed.EventCount, eventIDValue(installed.EventID), installed.Digest, "attempt-prepared", nil, "", "")
}

// ClaimAcquireAttempt returns the immutable request and its terminal or
// unresolved authority disposition for exact retry/recovery.
func (j *Journal) ClaimAcquireAttempt(commandID string) (ClaimAcquireAttempt, error) {
	if j == nil || !identityPattern.MatchString(commandID) {
		return ClaimAcquireAttempt{}, ErrInvalidCommand
	}
	j.mu.Lock()
	defer j.mu.Unlock()
	if j.db == nil {
		return ClaimAcquireAttempt{}, ErrClosed
	}
	return readClaimAcquireAttempt(j.db, commandID)
}

func readClaimAcquireAttempt(queryer interface {
	QueryRow(string, ...any) *sql.Row
}, commandID string,
) (ClaimAcquireAttempt, error) {
	var attempt ClaimAcquireAttempt
	var sequence, installedCount int64
	var installedID sql.NullString
	var state, resultCode, grantID string
	var receipt []byte
	err := queryer.QueryRow(`SELECT command_id,environment_sequence,request_hash,canonical_bytes,installed_count,installed_event_id,installed_digest,state,canonical_receipt,COALESCE(result_code,''),COALESCE(grant_id,'') FROM claim_acquire_attempts WHERE command_id=?`, commandID).
		Scan(&attempt.ID, &sequence, &attempt.RequestHash, &attempt.CanonicalBytes, &installedCount, &installedID, &attempt.Installed.Digest, &state, &receipt, &resultCode, &grantID)
	if errors.Is(err, sql.ErrNoRows) {
		return ClaimAcquireAttempt{}, ErrNotFound
	}
	if err != nil {
		return ClaimAcquireAttempt{}, err
	}
	if sequence <= 0 || installedCount < 0 || installedID.Valid != (installedCount > 0) {
		return ClaimAcquireAttempt{}, ErrInvalidJournal
	}
	attempt.EnvironmentSeq = uint64(sequence)
	attempt.Installed.EventCount = uint64(installedCount)
	if installedID.Valid {
		id := installedID.String
		attempt.Installed.EventID = &id
	}
	attempt.Returned = state == "returned"
	attempt.Receipt = bytes.Clone(receipt)
	attempt.ResultCode = operation.ResultCode(resultCode)
	attempt.GrantID = grantID
	parsed, err := parseClaimAcquire(attempt.CanonicalBytes, attempt.RequestHash, Identity{})
	if err != nil {
		return ClaimAcquireAttempt{}, ErrInvalidJournal
	}
	attempt.MatterID, attempt.CloneID, attempt.WorktreeID = parsed.MatterID, parsed.CloneID, parsed.WorktreeID
	attempt.DispatchID, attempt.Actor = parsed.DispatchID, parsed.Actor
	if parsed.ID != attempt.ID || parsed.Sequence != attempt.EnvironmentSeq || state != "attempt-prepared" && state != "returned" {
		return ClaimAcquireAttempt{}, ErrInvalidJournal
	}
	return attempt, nil
}

func eventIDValue(eventID *string) string {
	if eventID == nil {
		return ""
	}
	return *eventID
}

type claimAcquireIdentity struct {
	ID, MatterID, CloneID, WorktreeID, DispatchID, Actor string
	Sequence                                             uint64
}

func canonicalClaimAcquire(identity Identity, sequence uint64, commandID, matterID, cloneID, worktreeID, dispatchID, actor string) ([]byte, error) {
	return wipdwire.EncodeCanonical(map[string]any{
		"schema": "wipd.command/1", "command_id": commandID,
		"authority":   map[string]any{"domain_id": identity.DomainID, "expected_epoch": identity.AuthorityEpoch},
		"environment": map[string]any{"id": identity.EnvironmentID, "sequence": sequence},
		"acted_at":    time.Now().UTC().Format(time.RFC3339Nano), "actor": actor,
		"causation_command_id": nil, "correlation_command_id": commandID,
		"operation": map[string]any{"name": "claim.acquire", "version": uint64(1)},
		"context":   map[string]any{"repo_id": identity.RepoID, "clone_id": cloneID, "worktree_id": worktreeID},
		"claim":     nil,
		"input": map[string]any{
			"matter_id": matterID, "worktree_id": worktreeID,
			"dispatch_mode": "anonymous-matter", "requested_dispatch_id": dispatchID,
		},
		"blobs": []any{},
	})
}

func parseClaimAcquire(raw []byte, hash string, expected Identity) (claimAcquireIdentity, error) {
	var result claimAcquireIdentity
	if len(raw) == 0 || !requestHashMatches(raw, hash) {
		return result, ErrInvalidCommand
	}
	fields, err := wipdwire.DecodeCanonicalMap(raw,
		"schema", "command_id", "authority", "environment", "acted_at", "actor", "causation_command_id", "correlation_command_id", "operation", "context", "claim", "input", "blobs")
	if err != nil || fields["schema"] != "wipd.command/1" || fields["causation_command_id"] != nil || fields["claim"] != nil {
		return result, ErrInvalidCommand
	}
	commandID, idOK := fields["command_id"].(string)
	correlation, correlationOK := fields["correlation_command_id"].(string)
	actor, actorOK := fields["actor"].(string)
	actedAt, timeOK := fields["acted_at"].(string)
	parsedTime, timeErr := time.Parse(time.RFC3339Nano, actedAt)
	if !idOK || !identityPattern.MatchString(commandID) || !correlationOK || correlation != commandID || !actorOK || actor == "" ||
		!timeOK || timeErr != nil || parsedTime.UTC().Format(time.RFC3339Nano) != actedAt || !bytes.HasSuffix([]byte(actedAt), []byte("Z")) {
		return result, ErrInvalidCommand
	}
	authority, ok := fields["authority"].(map[string]any)
	if !ok || !wipdwire.ExactMapKeys(authority, "domain_id", "expected_epoch") {
		return result, ErrInvalidCommand
	}
	domainID, domainOK := authority["domain_id"].(string)
	epoch, epochOK := authority["expected_epoch"].(uint64)
	environment, ok := fields["environment"].(map[string]any)
	if !ok || !wipdwire.ExactMapKeys(environment, "id", "sequence") {
		return result, ErrInvalidCommand
	}
	environmentID, environmentOK := environment["id"].(string)
	sequence, sequenceOK := environment["sequence"].(uint64)
	operationFields, ok := fields["operation"].(map[string]any)
	if !ok || !wipdwire.ExactMapKeys(operationFields, "name", "version") || operationFields["name"] != "claim.acquire" || operationFields["version"] != uint64(1) {
		return result, ErrInvalidCommand
	}
	contextFields, ok := fields["context"].(map[string]any)
	if !ok || !wipdwire.ExactMapKeys(contextFields, "repo_id", "clone_id", "worktree_id") {
		return result, ErrInvalidCommand
	}
	repoID, repoOK := contextFields["repo_id"].(string)
	cloneID, cloneOK := contextFields["clone_id"].(string)
	worktreeID, worktreeOK := contextFields["worktree_id"].(string)
	input, ok := fields["input"].(map[string]any)
	if !ok || !wipdwire.ExactMapKeys(input, "matter_id", "worktree_id", "dispatch_mode", "requested_dispatch_id") {
		return result, ErrInvalidCommand
	}
	matterID, matterOK := input["matter_id"].(string)
	inputWorktree, inputWorktreeOK := input["worktree_id"].(string)
	dispatchID, dispatchOK := input["requested_dispatch_id"].(string)
	blobs, blobsOK := fields["blobs"].([]any)
	if !domainOK || !identityPattern.MatchString(domainID) || !epochOK || epoch == 0 || !environmentOK || !identityPattern.MatchString(environmentID) || !sequenceOK || sequence == 0 ||
		!repoOK || !identityPattern.MatchString(repoID) || !cloneOK || !identityPattern.MatchString(cloneID) || !worktreeOK || !identityPattern.MatchString(worktreeID) ||
		!matterOK || !identityPattern.MatchString(matterID) || !inputWorktreeOK || inputWorktree != worktreeID || input["dispatch_mode"] != "anonymous-matter" ||
		!dispatchOK || !identityPattern.MatchString(dispatchID) || !blobsOK || len(blobs) != 0 ||
		(expected.DomainID != "" && (domainID != expected.DomainID || epoch != expected.AuthorityEpoch || environmentID != expected.EnvironmentID || repoID != expected.RepoID)) {
		return result, ErrInvalidCommand
	}
	return claimAcquireIdentity{
		ID: commandID, MatterID: matterID, CloneID: cloneID, WorktreeID: worktreeID,
		DispatchID: dispatchID, Actor: actor, Sequence: sequence,
	}, nil
}

func requestHash(raw []byte) string {
	sum := sha256.Sum256(append([]byte("wipd/request-hash/v1\x00"), raw...))
	return "sha256:" + hex.EncodeToString(sum[:])
}

func requestHashMatches(raw []byte, hash string) bool { return requestHash(raw) == hash }

func decodeClaimAcquireAttempt(id string, sequence uint64, hash string, raw []byte, count uint64, eventID, digest, state string, receipt []byte, code, grantID string) (ClaimAcquireAttempt, error) {
	attempt := ClaimAcquireAttempt{
		ID: id, EnvironmentSeq: sequence, RequestHash: hash, CanonicalBytes: bytes.Clone(raw),
		Installed: wipdwire.PrefixAnchor{EventCount: count, Digest: digest}, Returned: state == "returned",
		Receipt: bytes.Clone(receipt), ResultCode: operation.ResultCode(code), GrantID: grantID,
	}
	if eventID != "" {
		attempt.Installed.EventID = &eventID
	}
	parsed, err := parseClaimAcquire(raw, hash, Identity{})
	if err != nil || parsed.ID != id || parsed.Sequence != sequence {
		return ClaimAcquireAttempt{}, ErrInvalidJournal
	}
	attempt.MatterID, attempt.CloneID, attempt.WorktreeID = parsed.MatterID, parsed.CloneID, parsed.WorktreeID
	attempt.DispatchID, attempt.Actor = parsed.DispatchID, parsed.Actor
	return attempt, nil
}

func checkClaimAcquireAttempts(db *sql.DB, identity Identity) ([]uint64, []string, error) {
	rows, err := db.Query(`SELECT a.command_id,a.environment_sequence,a.request_hash,a.canonical_bytes,a.installed_count,a.installed_event_id,a.installed_digest,a.state,a.canonical_receipt,COALESCE(a.result_code,''),COALESCE(a.grant_id,''),COALESCE(g.grant_id,'') FROM claim_acquire_attempts a LEFT JOIN installed_claim_grants g ON g.acquire_command_id=a.command_id ORDER BY a.environment_sequence`)
	if err != nil {
		return nil, nil, err
	}
	defer func() { _ = rows.Close() }()
	var sequences []uint64
	var ids []string
	for rows.Next() {
		var id, hash, digest, state, code, grantID, installedGrantID string
		var sequence, count int64
		var eventID sql.NullString
		var raw, receipt []byte
		if err = rows.Scan(&id, &sequence, &hash, &raw, &count, &eventID, &digest, &state, &receipt, &code, &grantID, &installedGrantID); err != nil {
			return nil, nil, err
		}
		if sequence <= 0 || count < 0 || eventID.Valid != (count > 0) || !validTransferDigest(digest) {
			return nil, nil, ErrInvalidJournal
		}
		parsed, parseErr := parseClaimAcquire(raw, hash, identity)
		if parseErr != nil || parsed.ID != id || parsed.Sequence != uint64(sequence) {
			return nil, nil, ErrInvalidJournal
		}
		if state == "returned" {
			if err = validateClaimAcquireReceipt(receipt, id, hash, identity, uint64(sequence), operation.ResultCode(code)); err != nil ||
				(code == string(operation.ResultSucceeded)) != (grantID != "") {
				return nil, nil, ErrInvalidJournal
			}
			if installedGrantID != grantID {
				return nil, nil, ErrInvalidJournal
			}
		} else if state != "attempt-prepared" || receipt != nil || code != "" || grantID != "" || installedGrantID != "" {
			return nil, nil, ErrInvalidJournal
		}
		sequences = append(sequences, uint64(sequence))
		ids = append(ids, id)
	}
	return sequences, ids, rows.Err()
}

func validateClaimAcquireReceipt(raw []byte, id, hash string, identity Identity, sequence uint64, expectedCode operation.ResultCode) error {
	fields, err := wipdwire.DecodeCanonicalMap(raw,
		"schema", "domain_id", "authority_epoch", "identity_schema", "command_id", "request_hash", "operation", "environment", "result", "accepted_events")
	if err != nil || fields["schema"] != "wipd.terminal-receipt/1" || fields["domain_id"] != identity.DomainID ||
		fields["authority_epoch"] != identity.AuthorityEpoch || fields["identity_schema"] != "wipd.command/1" ||
		fields["command_id"] != id || fields["request_hash"] != hash {
		return ErrInvalidTransfer
	}
	operationFields, ok := fields["operation"].(map[string]any)
	if !ok || !wipdwire.ExactMapKeys(operationFields, "name", "version") || operationFields["name"] != "claim.acquire" || operationFields["version"] != uint64(1) {
		return ErrInvalidTransfer
	}
	environment, ok := fields["environment"].(map[string]any)
	if !ok || !wipdwire.ExactMapKeys(environment, "id", "sequence") || environment["id"] != identity.EnvironmentID || environment["sequence"] != sequence {
		return ErrInvalidTransfer
	}
	result, ok := fields["result"].(map[string]any)
	if !ok || !wipdwire.ExactMapKeys(result, "code", "output", "problem_code") || result["code"] != string(expectedCode) ||
		(expectedCode == operation.ResultSucceeded && (result["output"] == nil || result["problem_code"] != nil)) ||
		(expectedCode != operation.ResultSucceeded && (result["output"] != nil || result["problem_code"] == nil)) {
		return ErrInvalidTransfer
	}
	accepted, ok := fields["accepted_events"].(map[string]any)
	if expectedCode != operation.ResultSucceeded {
		if fields["accepted_events"] != nil {
			return ErrInvalidTransfer
		}
		return nil
	}
	if !ok || !wipdwire.ExactMapKeys(accepted, "first_event_id", "last_event_id", "event_count") {
		return ErrInvalidTransfer
	}
	count, countOK := accepted["event_count"].(uint64)
	first, firstOK := accepted["first_event_id"].(string)
	last, lastOK := accepted["last_event_id"].(string)
	if !countOK || count == 0 || !firstOK || !identityPattern.MatchString(first) || !lastOK || !identityPattern.MatchString(last) {
		return ErrInvalidTransfer
	}
	return nil
}

// InstallClaimAcquireRefusal commits the exact terminal non-success receipt for
// a durable acquisition attempt without installing a grant.
func (j *Journal) InstallClaimAcquireRefusal(commandID string, receipt []byte, code operation.ResultCode) error {
	if j == nil || code == operation.ResultSucceeded {
		return ErrInvalidTransfer
	}
	j.mu.Lock()
	defer j.mu.Unlock()
	if j.db == nil {
		return ErrClosed
	}
	tx, err := j.db.BeginTx(context.Background(), nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	attempt, err := readClaimAcquireAttempt(tx, commandID)
	if err != nil {
		return err
	}
	if attempt.Returned {
		if attempt.ResultCode == code && bytes.Equal(attempt.Receipt, receipt) {
			return tx.Commit()
		}
		return ErrCommandIDConflict
	}
	if err = validateClaimAcquireReceipt(receipt, attempt.ID, attempt.RequestHash, j.identity, attempt.EnvironmentSeq, code); err != nil {
		return err
	}
	if _, err = tx.Exec(`UPDATE claim_acquire_attempts SET state='returned',canonical_receipt=?,result_code=? WHERE command_id=? AND state='attempt-prepared'`, receipt, string(code), commandID); err != nil {
		return err
	}
	return tx.Commit()
}

// InstalledClaimGrantByCommand resolves a successful locally committed
// acquisition to its immutable grant descriptor.
func (j *Journal) InstalledClaimGrantByCommand(commandID string) (ClaimGrantSummary, error) {
	if j == nil || !identityPattern.MatchString(commandID) {
		return ClaimGrantSummary{}, ErrInvalidCommand
	}
	j.mu.Lock()
	defer j.mu.Unlock()
	if j.db == nil {
		return ClaimGrantSummary{}, ErrClosed
	}
	var summary ClaimGrantSummary
	var epoch, count int64
	var eventID sql.NullString
	err := j.db.QueryRow(`SELECT grant_id,acquire_command_id,acquire_request_hash,claim_id,claim_epoch,matter_id,batch_id,dispatch_id,end_count,end_event_id,end_digest,manifest_digest FROM installed_claim_grants WHERE acquire_command_id=?`, commandID).
		Scan(&summary.GrantID, &summary.CommandID, &summary.RequestHash, &summary.ClaimID, &epoch, &summary.MatterID, &summary.BatchID, &summary.DispatchID, &count, &eventID, &summary.AsOf.Digest, &summary.Manifest)
	if errors.Is(err, sql.ErrNoRows) {
		return ClaimGrantSummary{}, ErrNotFound
	}
	if err != nil {
		return ClaimGrantSummary{}, err
	}
	if epoch <= 0 || count < 0 || eventID.Valid != (count > 0) {
		return ClaimGrantSummary{}, ErrInvalidJournal
	}
	summary.ClaimEpoch, summary.AsOf.EventCount = uint64(epoch), uint64(count)
	if eventID.Valid {
		id := eventID.String
		summary.AsOf.EventID = &id
	}
	return summary, nil
}

func verifyClaimAcquireGrantAttempt(tx *sql.Tx, identity Identity, grant VerifiedClaimGrant) (ClaimAcquireAttempt, error) {
	attempt, err := readClaimAcquireAttempt(tx, grant.acquireCommandID)
	if err != nil || attempt.Returned || attempt.RequestHash != grant.acquireRequestHash || !sameTransferAnchor(attempt.Installed, grant.start) {
		return ClaimAcquireAttempt{}, ErrInvalidTransfer
	}
	parsed, err := parseClaimAcquire(attempt.CanonicalBytes, attempt.RequestHash, identity)
	if err != nil || parsed.MatterID != grant.matterID || parsed.DispatchID != grant.dispatchID {
		return ClaimAcquireAttempt{}, ErrInvalidTransfer
	}
	if err = validateClaimAcquireReceipt(grant.receipt, attempt.ID, attempt.RequestHash, identity, attempt.EnvironmentSeq, operation.ResultSucceeded); err != nil {
		return ClaimAcquireAttempt{}, err
	}
	return attempt, nil
}

func completeClaimAcquireGrantAttempt(tx *sql.Tx, attempt ClaimAcquireAttempt, grant VerifiedClaimGrant) error {
	if _, err := tx.Exec(`UPDATE claim_acquire_attempts SET state='returned',canonical_receipt=?,result_code='result.succeeded',grant_id=? WHERE command_id=? AND state='attempt-prepared'`,
		grant.receipt, grant.grantID, attempt.ID); err != nil {
		return err
	}
	return nil
}

func validTransferDigest(digest string) bool { return transferHash.MatchString(digest) }
