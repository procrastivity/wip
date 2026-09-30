package wipdjournal

import (
	"bytes"
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"errors"
	"strconv"
	"time"

	"github.com/procrastivity/wip/internal/operation"
	"github.com/procrastivity/wip/internal/wipdwire"
)

// ClaimJournalReceipt is one successfully installed receipt in the exact
// authority-assigned generation. Position is the acquired journal position,
// not the Environment's global journal position.
type ClaimJournalReceipt struct {
	Position uint64
	Entry    Entry
	Receipt  InstalledReceipt
}

// ClaimJournalReleaseCommand is the durable identity of an acquired claim's
// existing claim.release@v1 command. It is kept separate from the birth-only
// release table so neither path can reinterpret the other's context.
type ClaimJournalReleaseCommand struct {
	ID             string
	RequestHash    string
	EnvironmentSeq uint64
	CanonicalBytes []byte
	Binding        ClaimJournalBinding
	Barrier        wipdwire.JournalBarrier
	Returned       bool
	Receipt        []byte
	ResultCode     operation.ResultCode
}

// ErrClaimJournalIncomplete blocks close while any claim-local command lacks
// one successful installed receipt and an installed accepted range.
var ErrClaimJournalIncomplete = errors.New("wipdjournal: acquired claim journal is unresolved or quarantined")

// InstalledClaimGrantForClaim binds close context to the durable acquisition
// identity instead of caller-supplied claim, Matter, Dispatch, or worktree IDs.
func (j *Journal) InstalledClaimGrantForClaim(claimID string) (ClaimGrantSummary, ClaimAcquireAttempt, error) {
	var grant ClaimGrantSummary
	if j == nil || !identityPattern.MatchString(claimID) {
		return grant, ClaimAcquireAttempt{}, ErrInvalidCommand
	}
	j.mu.Lock()
	defer j.mu.Unlock()
	if j.db == nil {
		return grant, ClaimAcquireAttempt{}, ErrClosed
	}
	var epoch, count int64
	var eventID sql.NullString
	err := j.db.QueryRow(`SELECT grant_id,acquire_command_id,acquire_request_hash,claim_id,claim_epoch,matter_id,batch_id,dispatch_id,end_count,end_event_id,end_digest,manifest_digest
		FROM installed_claim_grants WHERE claim_id=?`, claimID).Scan(&grant.GrantID, &grant.CommandID, &grant.RequestHash,
		&grant.ClaimID, &epoch, &grant.MatterID, &grant.BatchID, &grant.DispatchID, &count, &eventID, &grant.AsOf.Digest, &grant.Manifest)
	if errors.Is(err, sql.ErrNoRows) {
		return ClaimGrantSummary{}, ClaimAcquireAttempt{}, ErrNotFound
	}
	if err != nil || epoch <= 0 || count < 0 || eventID.Valid != (count > 0) {
		return ClaimGrantSummary{}, ClaimAcquireAttempt{}, ErrInvalidJournal
	}
	grant.ClaimEpoch, grant.AsOf.EventCount = uint64(epoch), uint64(count)
	if eventID.Valid {
		id := eventID.String
		grant.AsOf.EventID = &id
	}
	attempt, err := readClaimAcquireAttempt(j.db, grant.CommandID)
	if err != nil || !attempt.Returned || attempt.ResultCode != operation.ResultSucceeded || attempt.GrantID != grant.GrantID ||
		attempt.MatterID != grant.MatterID || attempt.DispatchID != grant.DispatchID {
		return ClaimGrantSummary{}, ClaimAcquireAttempt{}, ErrInvalidJournal
	}
	return grant, attempt, nil
}

// ClaimJournal constructs the local candidate barrier only from commands
// attributable to this acquired claim and their exact installed successful
// receipts. The authority remains the source of the sealed state and digest.
func (j *Journal) ClaimJournal(binding ClaimJournalBinding) (wipdwire.JournalBarrier, []ClaimJournalReceipt, error) {
	var empty wipdwire.JournalBarrier
	if j == nil || !validClaimJournalBinding(binding) || binding.State != "open" && binding.State != "sealed" {
		return empty, nil, ErrInvalidCommand
	}
	j.mu.Lock()
	defer j.mu.Unlock()
	if j.db == nil {
		return empty, nil, ErrClosed
	}
	tx, err := j.db.BeginTx(context.Background(), &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return empty, nil, err
	}
	defer func() { _ = tx.Rollback() }()
	var stored ClaimJournalBinding
	err = tx.QueryRow(`SELECT claim_id,claim_epoch,matter_id,dispatch_id,journal_id,generation,state FROM installed_claim_journals WHERE claim_id=?`, binding.ClaimID).
		Scan(&stored.ClaimID, &stored.ClaimEpoch, &stored.MatterID, &stored.DispatchID, &stored.JournalID, &stored.Generation, &stored.State)
	if err != nil || stored != binding {
		return empty, nil, ErrInvalidJournal
	}
	var grant string
	if err = tx.QueryRow(`SELECT grant_id FROM installed_claim_grants WHERE claim_id=? AND claim_epoch=? AND matter_id=? AND dispatch_id=?`,
		binding.ClaimID, binding.ClaimEpoch, binding.MatterID, binding.DispatchID).Scan(&grant); err != nil || grant == "" {
		return empty, nil, ErrInvalidJournal
	}
	entries, err := listEntries(tx, `SELECT `+commandColumns+` FROM commands ORDER BY environment_sequence`)
	if err != nil {
		return empty, nil, err
	}
	installed, err := loadInstallSnapshot(tx, j.identity)
	if err != nil {
		return empty, nil, err
	}
	var members []Entry
	for _, entry := range entries {
		claim := entry.Command.Request.Claim
		if entry.Delivery != operation.DeliveryClaim || claim == nil || claim.ID != binding.ClaimID {
			continue
		}
		if claim.Epoch != strconv.FormatUint(binding.ClaimEpoch, 10) || entry.Command.Request.Context.Repo != j.identity.RepoID ||
			entry.Command.Request.Context.Clone == "" || entry.Command.Request.Context.Worktree == "" {
			return empty, nil, ErrClaimJournalIncomplete
		}
		if entry.State != StateReturned {
			return empty, nil, ErrClaimJournalIncomplete
		}
		members = append(members, entry)
	}
	receipts := make([]ClaimJournalReceipt, 0, len(members))
	barrierEntries := make([]wipdwire.JournalBarrierEntry, 0, len(members))
	for index, entry := range members {
		if index > 0 && entry.EnvironmentSeq <= members[index-1].EnvironmentSeq {
			return empty, nil, ErrClaimJournalIncomplete
		}
		receipt, ok := installed.Receipts[entry.Command.ID]
		if !ok || receipt.RequestHash != entry.RequestHash || receipt.EnvironmentSeq != entry.EnvironmentSeq ||
			receipt.JournalPosition != entry.JournalPosition || receipt.ResultCode != operation.ResultSucceeded {
			return empty, nil, ErrClaimJournalIncomplete
		}
		rangeValue, rangeErr := installedClaimReceiptRange(tx, installed.Anchor, entry, receipt)
		if rangeErr != nil {
			return empty, nil, ErrClaimJournalIncomplete
		}
		position := uint64(index + 1)
		receipts = append(receipts, ClaimJournalReceipt{Position: position, Entry: entry, Receipt: receipt})
		barrierEntries = append(barrierEntries, wipdwire.JournalBarrierEntry{
			Position: position, CommandID: entry.Command.ID, RequestHash: entry.RequestHash,
			ResultCode: string(receipt.ResultCode), Range: rangeValue,
		})
	}
	digest, err := wipdwire.JournalBarrierDigest(barrierEntries)
	if err != nil {
		return empty, nil, ErrInvalidJournal
	}
	barrier := wipdwire.JournalBarrier{
		Schema: "wipd.journal-barrier/1", Journal: binding.JournalID,
		Claim: wipdwire.ClaimRef{ID: binding.ClaimID, Epoch: binding.ClaimEpoch},
		Count: uint64(len(receipts)), Last: uint64(len(receipts)), Receipts: uint64(len(receipts)),
		Digest: digest, Sealed: binding.State == "sealed",
	}
	if err = tx.Commit(); err != nil {
		return empty, nil, err
	}
	return barrier, receipts, nil
}

// PrepareClaimJournalRelease retains one exact acquired-claim release command
// after the shared coordinator has returned work, installed its tail, ACKed
// receipts, and obtained authority seal confirmation.
func (j *Journal) PrepareClaimJournalRelease(commandID string, binding ClaimJournalBinding, barrier wipdwire.JournalBarrier,
	cloneID, worktreeID, actor string,
) (ClaimJournalReleaseCommand, error) {
	if j == nil || !identityPattern.MatchString(commandID) || !validClaimJournalBinding(binding) || binding.State != "sealed" ||
		!validClaimJournalBarrier(binding, barrier) || !identityPattern.MatchString(cloneID) || !identityPattern.MatchString(worktreeID) || actor == "" {
		return ClaimJournalReleaseCommand{}, ErrInvalidCommand
	}
	j.mu.Lock()
	defer j.mu.Unlock()
	if j.db == nil {
		return ClaimJournalReleaseCommand{}, ErrClosed
	}
	if existing, err := readClaimJournalReleaseAttempt(j.db.QueryRow(`SELECT command_id,environment_sequence,request_hash,canonical_bytes,claim_id,claim_epoch,matter_id,dispatch_id,journal_id,generation,barrier,state,canonical_receipt,result_code
		FROM claim_journal_release_attempts WHERE command_id=?`, commandID)); err == nil {
		if !sameClaimJournalReleaseIntent(existing, binding, barrier, cloneID, worktreeID, actor) {
			return ClaimJournalReleaseCommand{}, ErrCommandIDConflict
		}
		return existing, nil
	} else if !errors.Is(err, sql.ErrNoRows) {
		return ClaimJournalReleaseCommand{}, err
	}
	var stored ClaimJournalBinding
	err := j.db.QueryRow(`SELECT claim_id,claim_epoch,matter_id,dispatch_id,journal_id,generation,state FROM installed_claim_journals WHERE claim_id=?`, binding.ClaimID).
		Scan(&stored.ClaimID, &stored.ClaimEpoch, &stored.MatterID, &stored.DispatchID, &stored.JournalID, &stored.Generation, &stored.State)
	if err != nil || stored != binding {
		return ClaimJournalReleaseCommand{}, ErrInvalidJournal
	}
	var unresolved int
	err = j.db.QueryRow(`SELECT count(*) FROM commands c LEFT JOIN authority_command_outcomes o USING(command_id)
		WHERE c.state!='returned' AND (c.delivery!='authority' OR o.command_id IS NULL)`).Scan(&unresolved)
	if err != nil || unresolved != 0 {
		return ClaimJournalReleaseCommand{}, ErrClaimJournalIncomplete
	}
	var pending int
	err = j.db.QueryRow(`SELECT (SELECT count(*) FROM claim_journal_release_attempts WHERE state='attempt-prepared')+
		(SELECT count(*) FROM birth_release_attempts WHERE state='attempt-prepared')+
		(SELECT count(*) FROM claim_acquire_attempts WHERE state='attempt-prepared')`).Scan(&pending)
	if err != nil || pending != 0 {
		return ClaimJournalReleaseCommand{}, ErrClaimJournalIncomplete
	}
	for _, table := range []string{"commands", "birth_release_attempts", "claim_acquire_attempts"} {
		var conflicts int
		if err = j.db.QueryRow(`SELECT count(*) FROM `+table+` WHERE command_id=?`, commandID).Scan(&conflicts); err != nil || conflicts != 0 {
			return ClaimJournalReleaseCommand{}, ErrCommandIDConflict
		}
	}
	tx, err := j.db.BeginTx(context.Background(), nil)
	if err != nil {
		return ClaimJournalReleaseCommand{}, err
	}
	defer func() { _ = tx.Rollback() }()
	var nextSequence int64
	if err = tx.QueryRow(`SELECT next_environment_sequence FROM environment_state WHERE singleton=1`).Scan(&nextSequence); err != nil || nextSequence <= 0 {
		return ClaimJournalReleaseCommand{}, ErrInvalidJournal
	}
	identity := j.identity
	canonical, err := wipdwire.EncodeCanonical(map[string]any{
		"schema": "wipd.command/1", "command_id": commandID,
		"authority":   map[string]any{"domain_id": identity.DomainID, "expected_epoch": identity.AuthorityEpoch},
		"environment": map[string]any{"id": identity.EnvironmentID, "sequence": uint64(nextSequence)},
		"acted_at":    time.Now().UTC().Format(time.RFC3339Nano), "actor": actor,
		"causation_command_id": nil, "correlation_command_id": commandID,
		"operation": map[string]any{"name": "claim.release", "version": uint64(1)},
		"context":   map[string]any{"repo_id": identity.RepoID, "clone_id": cloneID, "worktree_id": worktreeID},
		"claim":     map[string]any{"id": binding.ClaimID, "epoch": binding.ClaimEpoch},
		"input":     map[string]any{"barrier": barrier}, "blobs": []any{},
	})
	if err != nil {
		return ClaimJournalReleaseCommand{}, ErrInvalidCommand
	}
	hashBytes := sha256.Sum256(append([]byte("wipd/request-hash/v1\x00"), canonical...))
	hash := "sha256:" + hex.EncodeToString(hashBytes[:])
	if _, err = tx.Exec(`INSERT INTO claim_journal_release_attempts(command_id,environment_sequence,request_hash,canonical_bytes,claim_id,claim_epoch,matter_id,dispatch_id,journal_id,generation,barrier,state)
		VALUES(?,?,?,?,?,?,?,?,?,?,?,'attempt-prepared')`, commandID, nextSequence, hash, canonical, binding.ClaimID, binding.ClaimEpoch,
		binding.MatterID, binding.DispatchID, binding.JournalID, binding.Generation, encodeClaimJournalBarrier(barrier)); err != nil {
		return ClaimJournalReleaseCommand{}, err
	}
	if _, err = tx.Exec(`UPDATE environment_state SET next_environment_sequence=? WHERE singleton=1 AND next_environment_sequence=?`, nextSequence+1, nextSequence); err != nil {
		return ClaimJournalReleaseCommand{}, err
	}
	if err = tx.Commit(); err != nil {
		return ClaimJournalReleaseCommand{}, err
	}
	return ClaimJournalReleaseCommand{
		ID: commandID, RequestHash: hash, EnvironmentSeq: uint64(nextSequence), CanonicalBytes: bytes.Clone(canonical),
		Binding: binding, Barrier: barrier,
	}, nil
}

// ClaimJournalReleaseAttempt returns the durable acquired-claim release intent
// and any terminal outcome retained for its command ID.
func (j *Journal) ClaimJournalReleaseAttempt(commandID string) (ClaimJournalReleaseCommand, error) {
	var empty ClaimJournalReleaseCommand
	if j == nil || !identityPattern.MatchString(commandID) {
		return empty, ErrNotFound
	}
	j.mu.Lock()
	defer j.mu.Unlock()
	if j.db == nil {
		return empty, ErrClosed
	}
	attempt, err := readClaimJournalReleaseAttempt(j.db.QueryRow(`SELECT command_id,environment_sequence,request_hash,canonical_bytes,claim_id,claim_epoch,matter_id,dispatch_id,journal_id,generation,barrier,state,canonical_receipt,result_code
		FROM claim_journal_release_attempts WHERE command_id=?`, commandID))
	if errors.Is(err, sql.ErrNoRows) {
		return empty, ErrNotFound
	}
	if err != nil || validateClaimJournalReleaseCommand(j.identity, attempt) != nil {
		return empty, ErrInvalidJournal
	}
	return attempt, nil
}

// HasPendingClaimJournalRelease reports whether a release attempt is unresolved.
func (j *Journal) HasPendingClaimJournalRelease() (bool, error) {
	if j == nil {
		return false, ErrClosed
	}
	j.mu.Lock()
	defer j.mu.Unlock()
	if j.db == nil {
		return false, ErrClosed
	}
	var count int
	err := j.db.QueryRow(`SELECT count(*) FROM claim_journal_release_attempts WHERE state='attempt-prepared'`).Scan(&count)
	return count != 0, err
}

// HasQuarantinedClaimJournalRelease prevents later local writes after a
// terminal non-success release result, while leaving exact same-ID replay
// available through ClaimJournalReleaseAttempt.
func (j *Journal) HasQuarantinedClaimJournalRelease() (bool, error) {
	if j == nil {
		return false, ErrClosed
	}
	j.mu.Lock()
	defer j.mu.Unlock()
	if j.db == nil {
		return false, ErrClosed
	}
	var count int
	err := j.db.QueryRow(`SELECT count(*) FROM claim_journal_release_attempts WHERE state='returned' AND result_code!='result.succeeded'`).Scan(&count)
	return count != 0, err
}

// HasPendingClaimAcquire reports whether an acquisition submission has an
// unresolved terminal result and must fence this later lifecycle command.
func (j *Journal) HasPendingClaimAcquire() (bool, error) {
	if j == nil {
		return false, ErrClosed
	}
	j.mu.Lock()
	defer j.mu.Unlock()
	if j.db == nil {
		return false, ErrClosed
	}
	var count int
	err := j.db.QueryRow(`SELECT count(*) FROM claim_acquire_attempts WHERE state='attempt-prepared'`).Scan(&count)
	return count != 0, err
}

func validClaimJournalBinding(binding ClaimJournalBinding) bool {
	return identityPattern.MatchString(binding.ClaimID) && identityPattern.MatchString(binding.MatterID) &&
		identityPattern.MatchString(binding.DispatchID) && identityPattern.MatchString(binding.JournalID) &&
		binding.ClaimEpoch > 0 && binding.Generation > 0 &&
		(binding.State == "open" || binding.State == "sealed" || binding.State == "released")
}

func validClaimJournalBarrier(binding ClaimJournalBinding, barrier wipdwire.JournalBarrier) bool {
	return barrier.Schema == "wipd.journal-barrier/1" && barrier.Journal == binding.JournalID &&
		barrier.Claim.ID == binding.ClaimID && barrier.Claim.Epoch == binding.ClaimEpoch &&
		barrier.Count == barrier.Last && barrier.Count == barrier.Receipts && barrier.Sealed &&
		barrier.Unresolved == 0 && barrier.Quarantined == 0 && validDigest(barrier.Digest)
}

func encodeClaimJournalBarrier(barrier wipdwire.JournalBarrier) []byte {
	encoded, _ := wipdwire.EncodeCanonical(barrier)
	return encoded
}

func installedClaimReceiptRange(tx *sql.Tx, installed wipdwire.PrefixAnchor, entry Entry, receipt InstalledReceipt) (*wipdwire.JournalBarrierRange, error) {
	fields, err := wipdwire.DecodeCanonicalMap(receipt.CanonicalReceipt,
		"schema", "domain_id", "authority_epoch", "identity_schema", "command_id", "request_hash", "operation", "environment", "result", "accepted_events")
	if err != nil {
		return nil, ErrInvalidJournal
	}
	if fields["accepted_events"] == nil {
		var firstPosition, lastPosition sql.NullInt64
		var count int64
		if err = tx.QueryRow(`SELECT event_count,first_position,last_position FROM installed_receipts WHERE command_id=? AND request_hash=?`, entry.Command.ID, entry.RequestHash).
			Scan(&count, &firstPosition, &lastPosition); err != nil || count != 0 || firstPosition.Valid || lastPosition.Valid {
			return nil, ErrInvalidJournal
		}
		events, eventErr := installedEventCountForCommand(tx, entry.Command.ID)
		if eventErr != nil || events != 0 {
			return nil, ErrInvalidJournal
		}
		return nil, nil
	}
	return installedBirthReceiptRange(tx, installed, entry, receipt)
}

func sameClaimJournalReleaseIntent(attempt ClaimJournalReleaseCommand, binding ClaimJournalBinding, barrier wipdwire.JournalBarrier,
	cloneID, worktreeID, actor string,
) bool {
	fields, err := wipdwire.DecodeCanonicalMap(attempt.CanonicalBytes,
		"schema", "command_id", "authority", "environment", "acted_at", "actor", "causation_command_id", "correlation_command_id", "operation", "context", "claim", "input", "blobs")
	contextFields, contextOK := fields["context"].(map[string]any)
	return err == nil && attempt.Binding.ClaimID == binding.ClaimID && attempt.Binding.ClaimEpoch == binding.ClaimEpoch &&
		attempt.Binding.MatterID == binding.MatterID && attempt.Binding.DispatchID == binding.DispatchID &&
		attempt.Binding.JournalID == binding.JournalID && attempt.Binding.Generation == binding.Generation &&
		bytes.Equal(encodeClaimJournalBarrier(attempt.Barrier), encodeClaimJournalBarrier(barrier)) && contextOK &&
		contextFields["clone_id"] == cloneID && contextFields["worktree_id"] == worktreeID && fields["actor"] == actor
}

func readClaimJournalReleaseAttempt(row scanner) (ClaimJournalReleaseCommand, error) {
	var attempt ClaimJournalReleaseCommand
	var sequence, epoch, generation int64
	var state string
	var rawBarrier, receipt []byte
	var code sql.NullString
	if err := row.Scan(&attempt.ID, &sequence, &attempt.RequestHash, &attempt.CanonicalBytes,
		&attempt.Binding.ClaimID, &epoch, &attempt.Binding.MatterID, &attempt.Binding.DispatchID,
		&attempt.Binding.JournalID, &generation, &rawBarrier, &state, &receipt, &code); err != nil {
		return attempt, err
	}
	if sequence <= 0 || epoch <= 0 || generation <= 0 || !identityPattern.MatchString(attempt.ID) || !validDigest(attempt.RequestHash) ||
		wipdwire.DecodeCanonical(rawBarrier, &attempt.Barrier,
			"schema", "journal_id", "claim", "entry_count", "last_position", "terminal_receipt_count", "entries_digest", "sealed", "unresolved_count", "quarantined_count") != nil {
		return ClaimJournalReleaseCommand{}, ErrInvalidJournal
	}
	attempt.EnvironmentSeq, attempt.Binding.ClaimEpoch, attempt.Binding.Generation = uint64(sequence), uint64(epoch), uint64(generation)
	attempt.Binding.State = "sealed"
	attempt.Returned, attempt.Receipt = state == "returned", bytes.Clone(receipt)
	if code.Valid {
		attempt.ResultCode = operation.ResultCode(code.String)
	}
	hash := sha256.Sum256(append([]byte("wipd/request-hash/v1\x00"), attempt.CanonicalBytes...))
	if attempt.RequestHash != "sha256:"+hex.EncodeToString(hash[:]) ||
		(state != "attempt-prepared" && state != "returned") ||
		(attempt.Returned && (len(attempt.Receipt) == 0 || attempt.ResultCode == "")) ||
		(!attempt.Returned && (len(attempt.Receipt) != 0 || code.Valid)) {
		return ClaimJournalReleaseCommand{}, ErrInvalidJournal
	}
	return attempt, nil
}

func validateClaimJournalReleaseCommand(identity Identity, attempt ClaimJournalReleaseCommand) error {
	fields, err := wipdwire.DecodeCanonicalMap(attempt.CanonicalBytes,
		"schema", "command_id", "authority", "environment", "acted_at", "actor", "causation_command_id", "correlation_command_id", "operation", "context", "claim", "input", "blobs")
	if err != nil || fields["schema"] != "wipd.command/1" || fields["command_id"] != attempt.ID ||
		fields["causation_command_id"] != nil || fields["correlation_command_id"] != attempt.ID {
		return ErrInvalidJournal
	}
	authority, ok := fields["authority"].(map[string]any)
	if !ok || !wipdwire.ExactMapKeys(authority, "domain_id", "expected_epoch") || authority["domain_id"] != identity.DomainID || authority["expected_epoch"] != identity.AuthorityEpoch {
		return ErrInvalidJournal
	}
	environment, ok := fields["environment"].(map[string]any)
	if !ok || !wipdwire.ExactMapKeys(environment, "id", "sequence") || environment["id"] != identity.EnvironmentID || environment["sequence"] != attempt.EnvironmentSeq {
		return ErrInvalidJournal
	}
	operationFields, ok := fields["operation"].(map[string]any)
	if !ok || !wipdwire.ExactMapKeys(operationFields, "name", "version") || operationFields["name"] != "claim.release" || operationFields["version"] != uint64(1) {
		return ErrInvalidJournal
	}
	contextFields, ok := fields["context"].(map[string]any)
	if !ok || !wipdwire.ExactMapKeys(contextFields, "repo_id", "clone_id", "worktree_id") || contextFields["repo_id"] != identity.RepoID ||
		!identityPattern.MatchString(canonicalID(contextFields["clone_id"])) || !identityPattern.MatchString(canonicalID(contextFields["worktree_id"])) {
		return ErrInvalidJournal
	}
	claim, ok := fields["claim"].(map[string]any)
	if !ok || !wipdwire.ExactMapKeys(claim, "id", "epoch") || claim["id"] != attempt.Binding.ClaimID || claim["epoch"] != attempt.Binding.ClaimEpoch {
		return ErrInvalidJournal
	}
	input, ok := fields["input"].(map[string]any)
	if !ok || !wipdwire.ExactMapKeys(input, "barrier") {
		return ErrInvalidJournal
	}
	barrierBytes, err := wipdwire.EncodeCanonical(input["barrier"])
	if err != nil || !bytes.Equal(barrierBytes, encodeClaimJournalBarrier(attempt.Barrier)) || !validClaimJournalBarrier(attempt.Binding, attempt.Barrier) {
		return ErrInvalidJournal
	}
	blobs, ok := fields["blobs"].([]any)
	actor, actorOK := fields["actor"].(string)
	actedAt, timeOK := fields["acted_at"].(string)
	parsed, timeErr := time.Parse(time.RFC3339Nano, actedAt)
	if !ok || len(blobs) != 0 || !actorOK || actor == "" || !timeOK || timeErr != nil ||
		parsed.UTC().Format(time.RFC3339Nano) != actedAt || !bytes.HasSuffix([]byte(actedAt), []byte("Z")) {
		return ErrInvalidJournal
	}
	return nil
}

func canonicalID(value any) string {
	text, _ := value.(string)
	return text
}

func checkClaimJournalCloseState(db *sql.DB, identity Identity) error {
	rows, err := db.Query(`SELECT j.claim_id,j.claim_epoch,j.matter_id,j.dispatch_id,j.journal_id,j.generation,j.state,g.grant_id
		FROM installed_claim_journals j LEFT JOIN installed_claim_grants g
		ON g.claim_id=j.claim_id AND g.claim_epoch=j.claim_epoch AND g.matter_id=j.matter_id AND g.dispatch_id=j.dispatch_id
		ORDER BY j.claim_id`)
	if err != nil {
		return err
	}
	for rows.Next() {
		var binding ClaimJournalBinding
		var grant sql.NullString
		if err = rows.Scan(&binding.ClaimID, &binding.ClaimEpoch, &binding.MatterID, &binding.DispatchID,
			&binding.JournalID, &binding.Generation, &binding.State, &grant); err != nil ||
			!validClaimJournalBinding(binding) || !grant.Valid || grant.String == "" {
			err = ErrInvalidJournal
			break
		}
	}
	if err == nil {
		err = rows.Err()
	}
	_ = rows.Close()
	if err != nil {
		return err
	}
	_, _, err = checkClaimJournalReleaseAttempts(db, identity)
	return err
}

func checkClaimJournalReleaseAttempts(db *sql.DB, identity Identity) ([]uint64, []string, error) {
	rows, err := db.Query(`SELECT command_id,environment_sequence,request_hash,canonical_bytes,claim_id,claim_epoch,matter_id,dispatch_id,journal_id,generation,barrier,state,canonical_receipt,result_code
		FROM claim_journal_release_attempts ORDER BY environment_sequence`)
	if err != nil {
		return nil, nil, err
	}
	var attempts []ClaimJournalReleaseCommand
	for rows.Next() {
		attempt, readErr := readClaimJournalReleaseAttempt(rows)
		if readErr != nil || validateClaimJournalReleaseCommand(identity, attempt) != nil {
			_ = rows.Close()
			return nil, nil, ErrInvalidJournal
		}
		attempts = append(attempts, attempt)
	}
	if err = rows.Err(); err != nil {
		_ = rows.Close()
		return nil, nil, err
	}
	if err = rows.Close(); err != nil {
		return nil, nil, err
	}
	sequences := make([]uint64, 0, len(attempts))
	ids := make([]string, 0, len(attempts))
	for _, attempt := range attempts {
		if attempt.Returned {
			result, eventIDs, output, receiptErr := validateClaimJournalReleaseReceipt(attempt, identity, attempt.Receipt)
			if receiptErr != nil || validateInstalledClaimJournalRelease(db, identity, attempt, eventIDs, output, result) != nil {
				return nil, nil, ErrInvalidJournal
			}
			var bindingState string
			if err = db.QueryRow(`SELECT state FROM installed_claim_journals WHERE claim_id=? AND generation=? AND journal_id=?`,
				attempt.Binding.ClaimID, attempt.Binding.Generation, attempt.Binding.JournalID).Scan(&bindingState); err != nil {
				return nil, nil, ErrInvalidJournal
			}
			if result == operation.ResultSucceeded && bindingState != "released" || result != operation.ResultSucceeded && bindingState != "sealed" {
				return nil, nil, ErrInvalidJournal
			}
		}
		sequences = append(sequences, attempt.EnvironmentSeq)
		ids = append(ids, attempt.ID)
	}
	return sequences, ids, nil
}
