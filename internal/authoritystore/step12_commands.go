package authoritystore

import (
	"context"
	"crypto/sha256"
	"crypto/tls"
	"database/sql"
	"errors"
	"fmt"
	"math"
	"strconv"
	"strings"
	"time"

	"github.com/procrastivity/wip/internal/operation"
)

func step4Definition(id operation.ID) (operation.Definition, bool) {
	for _, definition := range operation.Step4Catalogue() {
		if definition.Metadata().Operation == id {
			return definition, true
		}
	}
	return operation.Definition{}, false
}

func (s *Store) submitStep4(ctx context.Context, command operation.Command, canonical []byte, hash string,
	peer tls.ConnectionState, at, deadline time.Time, checkContext bool, definition operation.Definition,
) (CommandStatus, error) {
	var empty CommandStatus
	metadata := definition.Metadata()
	if definition.ValidateRequest(command.Request) != nil || command.Request.Context.Repo == "" {
		return empty, ErrInvalidProof
	}
	if metadata.Operation == operation.MatterCreateV2.Metadata().Operation {
		if command.Request.Claim != nil || command.Request.Context.Clone != "" || command.Request.Context.Worktree != "" {
			return empty, ErrInvalidProof
		}
	} else if command.Request.Claim == nil || command.Request.Context.Clone == "" || command.Request.Context.Worktree == "" {
		return empty, ErrInvalidProof
	}
	before := func(tx *sql.Tx) error {
		if metadata.Operation == operation.MatterCreateV2.Metadata().Operation {
			return nil
		}
		if input, ok := command.Request.Input.(operation.StageCreateInput); ok {
			return verifyStep4MatterClaim(ctx, tx, command, input.MatterID)
		}
		return validateStep4ClaimTx(ctx, tx, command)
	}
	var beforeCommit func() error
	if checkContext {
		beforeCommit = func() error {
			if err := ctx.Err(); err != nil {
				return err
			}
			if !deadline.IsZero() && !time.Now().Before(deadline) {
				return context.DeadlineExceeded
			}
			return nil
		}
	}
	var afterInsert func(*sql.Tx) error
	if metadata.Delivery == operation.DeliveryClaim {
		afterInsert = func(tx *sql.Tx) error {
			return appendConnectedClaimJournalEntry(ctx, tx, command, canonical, hash)
		}
	}
	return s.submitIdentity(ctx, commandIdentity{
		domain: command.AuthorityDomainID, epoch: command.ExpectedAuthorityEpoch,
		environment: command.EnvironmentID, sequence: command.EnvironmentSequence,
		id: command.ID, name: metadata.Operation.Name, version: uint64(metadata.Operation.Version),
		repo: command.Request.Context.Repo, encoded: canonical, hash: hash, m1: &command,
	}, peer, at, before, beforeCommit, afterInsert)
}

func validateStep4ClaimTx(ctx context.Context, tx *sql.Tx, command operation.Command) error {
	claim := command.Request.Claim
	if claim == nil || !ulid.MatchString(claim.ID) {
		return ErrFenced
	}
	epoch, err := strconv.ParseUint(claim.Epoch, 10, 64)
	if err != nil || epoch == 0 || strconv.FormatUint(epoch, 10) != claim.Epoch {
		return ErrFenced
	}
	var matter, owner, repo, worktree, journalState string
	var storedEpoch, authorityEpoch, generation uint64
	var closed sql.NullString
	err = tx.QueryRowContext(ctx, `SELECT c.matter_id,c.owner_environment_id,c.worktree_id,c.claim_epoch,c.authority_epoch,c.close_command_id,
		m.repo_id,j.state,j.generation
		FROM claims c JOIN matters m ON m.domain_id=c.domain_id AND m.matter_id=c.matter_id
		JOIN claim_journals j ON j.domain_id=c.domain_id AND j.claim_id=c.claim_id
		WHERE c.domain_id=? AND c.claim_id=? AND j.generation=(SELECT max(current.generation) FROM claim_journals current WHERE current.domain_id=c.domain_id AND current.claim_id=c.claim_id)`,
		command.AuthorityDomainID, claim.ID).Scan(&matter, &owner, &worktree, &storedEpoch, &authorityEpoch, &closed, &repo, &journalState, &generation)
	if errors.Is(err, sql.ErrNoRows) {
		return ErrFenced
	}
	if err != nil {
		return err
	}
	if closed.Valid || journalState != "open" || generation == 0 || storedEpoch != epoch || authorityEpoch != command.ExpectedAuthorityEpoch ||
		owner != command.EnvironmentID || repo != command.Request.Context.Repo || worktree != command.Request.Context.Worktree {
		return ErrFenced
	}
	var locator string
	if err = tx.QueryRowContext(ctx, `SELECT locator FROM m6_nodes WHERE domain_id=? AND node_id=? AND kind='matter' AND tombstone_event_id IS NULL`, command.AuthorityDomainID, matter).Scan(&locator); err != nil {
		return ErrFenced
	}
	state, err := lifecycleStateTx(ctx, tx, command.AuthorityDomainID, matter, "matter")
	if err != nil {
		return err
	}
	if state != "planned" && state != "in-progress" {
		return ErrFenced
	}
	_ = locator
	return nil
}

type step4Fold struct {
	result     operation.Result
	subject    string
	eventIDs   []string
	first      any
	last       any
	rangeValue any
	output     any
}

func completeStep4Tx(ctx context.Context, tx *sql.Tx, command operation.Command, identity eventIdentity,
	result operation.Result, subjectID string, eventIDs []string, occurred time.Time,
) (step4Fold, error) {
	fold := step4Fold{result: result, subject: subjectID, eventIDs: eventIDs}
	refuse := func(code string, message string) (step4Fold, error) {
		fold.result = step4Problem(code, message)
		fold.subject, fold.eventIDs = "", nil
		return fold, nil
	}
	if len(eventIDs) == 0 {
		return fold, ErrInvalidProof
	}
	for i, id := range eventIDs {
		if !ulid.MatchString(id) || i > 0 && eventIDs[i-1] >= id {
			return fold, ErrInvalidProof
		}
	}
	if command.Request.Operation != operation.MatterCreateV2.Metadata().Operation {
		if err := validateStep4ClaimTx(ctx, tx, command); err != nil {
			if errors.Is(err, ErrFenced) {
				return refuse("refusal.claim-fenced", "the Matter claim or current journal generation is no longer active")
			}
			return fold, err
		}
	}
	domain := command.AuthorityDomainID
	getNode := func(id string) (string, string, string, string, int64, bool, error) {
		var kind, matter, parent, locator string
		var sortKey int64
		var tombstone sql.NullString
		err := tx.QueryRowContext(ctx, `SELECT kind,matter_id,coalesce(parent_id,''),locator,sort_key,tombstone_event_id FROM m6_nodes WHERE domain_id=? AND node_id=?`, domain, id).
			Scan(&kind, &matter, &parent, &locator, &sortKey, &tombstone)
		return kind, matter, parent, locator, sortKey, !tombstone.Valid, err
	}
	appendEvent := func(id, kind, subject string, payload map[string]any) (uint64, error) {
		return appendCommandEvent(ctx, tx, identity, occurred, id, kind, subject, payload)
	}
	var positions []uint64
	var resultOutput any
	switch input := command.Request.Input.(type) {
	case operation.MatterCreateInput:
		if command.Request.Operation != operation.MatterCreateV2.Metadata().Operation {
			return fold, ErrInvalidProof
		}
		if subjectID == "" || !ulid.MatchString(subjectID) {
			return fold, ErrInvalidProof
		}
		requested := input.Locator
		if requested == "" {
			requested = MatterLocator(input.Title)
		}
		if requested == "" {
			return refuse("validation.empty-locator", "Matter title does not yield a canonical locator")
		}
		assigned := requested
		var collisions int
		if err := tx.QueryRowContext(ctx, `SELECT count(*) FROM m6_nodes WHERE domain_id=? AND repo_id=? AND kind='matter' AND locator=?`, domain, command.Request.Context.Repo, requested).Scan(&collisions); err != nil {
			return fold, err
		}
		repair := collisions != 0
		if repair {
			candidate, candidateErr := locatorCollisionCandidate(ctx, tx, domain, command.Request.Context.Repo, requested, subjectID)
			if candidateErr != nil {
				return fold, candidateErr
			}
			assigned = candidate
		}
		resultOutput = operation.MatterCreateV2Output{
			ID: subjectID, Title: input.Title, RequestedLocator: requested,
			AssignedLocator: assigned, LocatorRepairRequired: repair,
		}
		if len(eventIDs) < 1 || repair && len(eventIDs) != 2 {
			return fold, ErrInvalidProof
		}
		pos, err := appendEvent(eventIDs[0], "matter.created", subjectID, map[string]any{"id": subjectID, "locator": assigned, "title": input.Title})
		if err != nil {
			return fold, err
		}
		positions = append(positions, pos)
		if repair {
			pos, err = appendEvent(eventIDs[1], "matter.locator-repair-required", subjectID, map[string]any{"requested_locator": requested, "assigned_locator": assigned})
			if err != nil {
				return fold, err
			}
			positions = append(positions, pos)
		}
		lastEvent := eventIDs[0]
		if repair {
			lastEvent = eventIDs[1]
		}
		if _, err = tx.ExecContext(ctx, `INSERT INTO matters VALUES(?,?,?,?,?,?)`, domain, subjectID, command.Request.Context.Repo, assigned, input.Title, eventIDs[0]); err != nil {
			return fold, writeError(err)
		}
		if _, err = tx.ExecContext(ctx, `INSERT INTO implicit_birth_claims VALUES(?,?,1,?,?,?)`, domain, subjectID, command.EnvironmentID, command.Request.Context.Repo, command.ID); err != nil {
			return fold, writeError(err)
		}
		_, err = tx.ExecContext(ctx, `INSERT INTO m6_nodes(domain_id,node_id,kind,repo_id,matter_id,parent_id,locator,title,sort_key,birth_event_id,last_event_id,repair_required,requested_locator)
			VALUES (?, ?, 'matter', ?, ?, NULL, ?, ?, 0, ?, ?, ?, ?)`, domain, subjectID, command.Request.Context.Repo, subjectID, assigned, input.Title, eventIDs[0], lastEvent, repair, nullableLocator(repair, requested))
		if err != nil {
			return fold, writeError(err)
		}
	case operation.StageCreateInput:
		kind, matter, _, _, _, live, err := getNode(input.MatterID)
		if err != nil || !live || kind != "matter" || matter != input.MatterID {
			return refuse("not-found.stage-parent", "Stage parent Matter is not live")
		}
		if err = verifyStep4MatterClaim(ctx, tx, command, input.MatterID); err != nil {
			if errors.Is(err, ErrFenced) {
				return refuse("refusal.claim-fenced", "Stage parent is outside the active Matter claim")
			}
			return fold, err
		}
		locator := MatterLocator(input.Title)
		if locator == "" {
			return refuse("validation.empty-locator", "Stage title does not yield a canonical locator")
		}
		var exists int
		if err = tx.QueryRowContext(ctx, `SELECT count(*) FROM m6_nodes WHERE domain_id=? AND matter_id=? AND kind='stage' AND locator=?`, domain, input.MatterID, locator).Scan(&exists); err != nil {
			return fold, err
		}
		if exists != 0 {
			return refuse("refusal.locator-conflict", "Stage locator is already assigned in this Matter")
		}
		sortKey, err := nextM6SortKey(ctx, tx, domain, input.MatterID)
		if err != nil {
			return fold, err
		}
		if len(eventIDs) != 1 {
			return fold, ErrInvalidProof
		}
		pos, err := appendEvent(eventIDs[0], "stage.created", subjectID, map[string]any{"matter_id": input.MatterID, "locator": locator, "title": input.Title, "sort_key": sortKey})
		if err != nil {
			return fold, err
		}
		positions = append(positions, pos)
		if _, err = tx.ExecContext(ctx, `INSERT INTO m6_nodes(domain_id,node_id,kind,repo_id,matter_id,parent_id,locator,title,sort_key,birth_event_id,last_event_id,repair_required)
			VALUES (?, ?, 'stage', ?, ?, ?, ?, ?, ?, ?, ?, 0)`, domain, subjectID, command.Request.Context.Repo, input.MatterID, input.MatterID, locator, input.Title, sortKey, eventIDs[0], eventIDs[0]); err != nil {
			return fold, writeError(err)
		}
		resultOutput = operation.StageCreateOutput{ID: subjectID, MatterID: input.MatterID, Locator: locator, Title: input.Title, SortKey: sortKey, State: "planned"}
	case operation.StepCreateInput:
		if command.Request.Operation != operation.StepCreateV2.Metadata().Operation {
			return fold, ErrInvalidProof
		}
		kind, matter, _, _, _, live, err := getNode(input.ParentID)
		if err != nil || !live || (kind != "matter" && kind != "stage") {
			return refuse("not-found.step-parent", "Step parent is not a live Matter or Stage")
		}
		if err = verifyStep4MatterClaim(ctx, tx, command, matter); err != nil {
			if errors.Is(err, ErrFenced) {
				return refuse("refusal.claim-fenced", "the Step parent is outside the active Matter claim")
			}
			return fold, err
		}
		return createM6Step(ctx, tx, command, eventIDs, subjectID, input.ParentID, matter, input.Title, appendEvent, &positions, &resultOutput, fold)
	case operation.StepInsertInput:
		kind, matter, _, _, _, live, err := getNode(input.ParentID)
		if err != nil || !live || (kind != "matter" && kind != "stage") {
			return refuse("not-found.step-parent", "Step parent is not a live Matter or Stage")
		}
		if err = verifyStep4MatterClaim(ctx, tx, command, matter); err != nil {
			if errors.Is(err, ErrFenced) {
				return refuse("refusal.claim-fenced", "the Step parent is outside the active Matter claim")
			}
			return fold, err
		}
		return insertM6Step(ctx, tx, command, eventIDs, subjectID, input, matter, appendEvent, &positions, &resultOutput, fold)
	case operation.StepReorderInput:
		kind, matter, _, _, _, live, err := getNode(input.ParentID)
		if err != nil || !live || (kind != "matter" && kind != "stage") {
			return refuse("not-found.step-parent", "Step parent is not a live Matter or Stage")
		}
		if err = verifyStep4MatterClaim(ctx, tx, command, matter); err != nil {
			if errors.Is(err, ErrFenced) {
				return refuse("refusal.claim-fenced", "the Step parent is outside the active Matter claim")
			}
			return fold, err
		}
		if len(eventIDs) != 1 {
			return fold, ErrInvalidProof
		}
		var current []string
		var base, maximum int64
		current, base, maximum, err = readM6StepOrder(ctx, tx, domain, input.ParentID, input.Order)
		if errors.Is(err, errStep4Rejected) {
			return refuse("validation.incomplete-order", "reorder must name every live Step exactly once")
		}
		if err != nil {
			return fold, err
		}
		position, appendErr := appendEvent(eventIDs[0], "step.reordered", input.ParentID, map[string]any{"order": stringValues(input.Order)})
		if appendErr != nil {
			return fold, appendErr
		}
		if err = applyM6StepOrder(ctx, tx, domain, current, input.Order, base, maximum, eventIDs[0]); err != nil {
			return fold, err
		}
		positions = append(positions, position)
		resultOutput = operation.StepReorderOutput{ParentID: input.ParentID, Order: append([]string(nil), input.Order...)}
	case operation.StepReplaceInput:
		kind, matter, parent, _, sortKey, live, err := getNode(input.StepID)
		if err != nil || !live || kind != "step" {
			return refuse("not-found.step", "replacement target is not a live Step")
		}
		if err = verifyStep4MatterClaim(ctx, tx, command, matter); err != nil {
			if errors.Is(err, ErrFenced) {
				return refuse("refusal.claim-fenced", "the Step is outside the active Matter claim")
			}
			return fold, err
		}
		locator, err := nextM6StepLocator(ctx, tx, domain, matter)
		if err != nil {
			return fold, err
		}
		if len(eventIDs) != 1 {
			return fold, ErrInvalidProof
		}
		payload := map[string]any{"replacement": subjectID, "title": input.Title, "locator": locator}
		pos, err := appendEvent(eventIDs[0], "step.replaced", input.StepID, payload)
		if err != nil {
			return fold, err
		}
		positions = append(positions, pos)
		if _, err = tx.ExecContext(ctx, `UPDATE m6_nodes SET tombstone_event_id=?,last_event_id=? WHERE domain_id=? AND node_id=? AND tombstone_event_id IS NULL`, eventIDs[0], eventIDs[0], domain, input.StepID); err != nil {
			return fold, writeError(err)
		}
		if _, err = tx.ExecContext(ctx, `INSERT INTO m6_nodes(domain_id,node_id,kind,repo_id,matter_id,parent_id,locator,title,sort_key,birth_event_id,last_event_id,repair_required)
			SELECT domain_id, ?,'step',repo_id,matter_id,parent_id, ?, ?, ?, ?, ?, 0 FROM m6_nodes WHERE domain_id=? AND node_id=?`, subjectID, locator, input.Title, sortKey, eventIDs[0], eventIDs[0], domain, input.StepID); err != nil {
			return fold, writeError(err)
		}
		resultOutput = operation.StepReplaceOutput{RemovedStepID: input.StepID, Replacement: operation.StepCreateOutput{
			ID: subjectID, ParentID: parent, MatterID: matter, Locator: locator, Title: input.Title, SortKey: sortKey, State: "planned",
		}}
	case operation.StepRemoveInput:
		kind, matter, _, _, _, live, err := getNode(input.StepID)
		if err != nil || !live || kind != "step" {
			return refuse("not-found.step", "removal target is not a live Step")
		}
		if err = verifyStep4MatterClaim(ctx, tx, command, matter); err != nil {
			if errors.Is(err, ErrFenced) {
				return refuse("refusal.claim-fenced", "the Step is outside the active Matter claim")
			}
			return fold, err
		}
		if len(eventIDs) != 1 {
			return fold, ErrInvalidProof
		}
		pos, err := appendEvent(eventIDs[0], "step.removed", input.StepID, map[string]any{"reason": input.Reason})
		if err != nil {
			return fold, err
		}
		positions = append(positions, pos)
		if _, err = tx.ExecContext(ctx, `UPDATE m6_nodes SET tombstone_event_id=?,last_event_id=? WHERE domain_id=? AND node_id=? AND tombstone_event_id IS NULL`, eventIDs[0], eventIDs[0], domain, input.StepID); err != nil {
			return fold, writeError(err)
		}
		resultOutput = operation.StepRemoveOutput{StepID: input.StepID}
	case operation.MatterLocatorRepairInput:
		if command.Request.Operation != operation.MatterLocatorRepairV1.Metadata().Operation {
			return fold, ErrInvalidProof
		}
		kind, matter, _, previous, _, live, err := getNode(input.MatterID)
		if err != nil || !live || kind != "matter" {
			return refuse("not-found.matter", "locator repair target is not a live Matter")
		}
		if err = verifyStep4MatterClaim(ctx, tx, command, matter); err != nil {
			if errors.Is(err, ErrFenced) {
				return refuse("refusal.claim-fenced", "locator repair is outside the active Matter claim")
			}
			return fold, err
		}
		var repair int
		var requested string
		if err = tx.QueryRowContext(ctx, `SELECT repair_required,coalesce(requested_locator,'') FROM m6_nodes WHERE domain_id=? AND node_id=?`, domain, input.MatterID).Scan(&repair, &requested); err != nil {
			return fold, err
		}
		if repair != 1 || requested == "" {
			return refuse("refusal.locator-repair-not-required", "Matter has no unresolved locator repair")
		}
		switch input.Action {
		case "accept":
			if input.AssignedLocator != previous {
				return refuse("validation.accept-current-locator", "accept must name the current assigned locator")
			}
		case "rename":
			if input.AssignedLocator == previous {
				return refuse("validation.rename-distinct-locator", "rename must choose a distinct locator")
			}
			var count int
			if err = tx.QueryRowContext(ctx, `SELECT count(*) FROM m6_nodes WHERE domain_id=? AND repo_id=? AND kind='matter' AND locator=? AND node_id!=?`, domain, command.Request.Context.Repo, input.AssignedLocator, input.MatterID).Scan(&count); err != nil {
				return fold, err
			}
			if count != 0 {
				return refuse("refusal.locator-conflict", "requested repair locator is already assigned")
			}
		default:
			return refuse("validation.locator-repair-action", "repair action must be rename or accept")
		}
		if len(eventIDs) != 1 {
			return fold, ErrInvalidProof
		}
		payload := map[string]any{"action": input.Action, "requested_locator": requested, "previous_locator": previous, "assigned_locator": input.AssignedLocator}
		pos, err := appendEvent(eventIDs[0], "matter.locator-repaired", input.MatterID, payload)
		if err != nil {
			return fold, err
		}
		positions = append(positions, pos)
		if _, err = tx.ExecContext(ctx, `UPDATE matters SET locator=? WHERE domain_id=? AND matter_id=?`, input.AssignedLocator, domain, input.MatterID); err != nil {
			return fold, writeError(err)
		}
		if _, err = tx.ExecContext(ctx, `UPDATE m6_nodes SET locator=?,repair_required=0,requested_locator=NULL,last_event_id=? WHERE domain_id=? AND node_id=?`, input.AssignedLocator, eventIDs[0], domain, input.MatterID); err != nil {
			return fold, writeError(err)
		}
		resultOutput = operation.MatterLocatorRepairOutput{ID: input.MatterID, Action: input.Action, RequestedLocator: requested, PreviousLocator: previous, AssignedLocator: input.AssignedLocator}
	default:
		return fold, ErrInvalidProof
	}
	return finishStep4Success(fold, resultOutput, subjectID, eventIDs, positions)
}

func verifyStep4MatterClaim(ctx context.Context, tx *sql.Tx, command operation.Command, matter string) error {
	if err := validateStep4ClaimTx(ctx, tx, command); err != nil {
		return err
	}
	if command.Request.Claim == nil {
		return ErrFenced
	}
	var claimed string
	if err := tx.QueryRowContext(ctx, `SELECT matter_id FROM claims WHERE domain_id=? AND claim_id=?`, command.AuthorityDomainID, command.Request.Claim.ID).Scan(&claimed); err != nil || claimed != matter {
		return ErrFenced
	}
	return nil
}

func locatorCollisionCandidate(ctx context.Context, tx *sql.Tx, domain, repo, requested, id string) (string, error) {
	for width := 6; width <= len(id); width++ {
		candidate := requested + "-" + strings.ToLower(id[:width])
		var count int
		if err := tx.QueryRowContext(ctx, `SELECT count(*) FROM m6_nodes WHERE domain_id=? AND repo_id=? AND kind='matter' AND locator=?`, domain, repo, candidate).Scan(&count); err != nil {
			return "", err
		}
		if count == 0 {
			return candidate, nil
		}
	}
	for counter := uint64(0); ; counter++ {
		candidate := requested + "-" + locatorSuffix(id, counter)
		var count int
		if err := tx.QueryRowContext(ctx, `SELECT count(*) FROM m6_nodes WHERE domain_id=? AND repo_id=? AND kind='matter' AND locator=?`, domain, repo, candidate).Scan(&count); err != nil {
			return "", err
		}
		if count == 0 {
			return candidate, nil
		}
		if counter == math.MaxUint64 {
			return "", ErrResourceLimit
		}
	}
}

func locatorSuffix(id string, counter uint64) string {
	var counterBytes [8]byte
	for i := 7; i >= 0; i-- {
		counterBytes[i] = byte(counter)
		counter >>= 8
	}
	return crockfordPrefix(sha256Bytes([]byte("wipd/locator-suffix/v1\x00"), []byte(id), counterBytes[:]), 6)
}

func nextM6SortKey(ctx context.Context, tx *sql.Tx, domain, parent string) (int64, error) {
	var maximum sql.NullInt64
	if err := tx.QueryRowContext(ctx, `SELECT max(sort_key) FROM m6_nodes WHERE domain_id=? AND parent_id=? AND tombstone_event_id IS NULL`, domain, parent).Scan(&maximum); err != nil {
		return 0, err
	}
	if maximum.Valid && maximum.Int64 > math.MaxInt64-1000 {
		return 0, ErrResourceLimit
	}
	if maximum.Valid {
		return maximum.Int64 + 1000, nil
	}
	return 1000, nil
}

func nextM6StepLocator(ctx context.Context, tx *sql.Tx, domain, matter string) (string, error) {
	var count int
	if err := tx.QueryRowContext(ctx, `SELECT count(*) FROM m6_nodes WHERE domain_id=? AND matter_id=? AND kind='step'`, domain, matter).Scan(&count); err != nil {
		return "", err
	}
	for offset := 1; ; offset++ {
		if count > math.MaxInt-offset {
			return "", ErrResourceLimit
		}
		locator := fmt.Sprintf("step-%02d", count+offset)
		var occupied int
		if err := tx.QueryRowContext(ctx, `SELECT count(*) FROM m6_nodes WHERE domain_id=? AND matter_id=? AND kind='step' AND locator=?`, domain, matter, locator).Scan(&occupied); err != nil {
			return "", err
		}
		if occupied == 0 {
			return locator, nil
		}
	}
}

var errStep4Rejected = errors.New("authoritystore: Step 4 semantic refusal")

func readM6StepOrder(ctx context.Context, tx *sql.Tx, domain, parent string, order []string) ([]string, int64, int64, error) {
	rows, err := tx.QueryContext(ctx, `SELECT node_id FROM m6_nodes WHERE domain_id=? AND parent_id=? AND kind='step' AND tombstone_event_id IS NULL ORDER BY sort_key`, domain, parent)
	if err != nil {
		return nil, 0, 0, err
	}
	var current []string
	for rows.Next() {
		var id string
		if err = rows.Scan(&id); err != nil {
			break
		}
		current = append(current, id)
	}
	if err == nil {
		err = rows.Err()
	}
	_ = rows.Close()
	if err != nil {
		return nil, 0, 0, err
	}
	if len(order) == 0 || len(order) != len(current) {
		return nil, 0, 0, errStep4Rejected
	}
	known := make(map[string]bool, len(current))
	for _, id := range current {
		known[id] = true
	}
	for _, id := range order {
		if !known[id] {
			return nil, 0, 0, errStep4Rejected
		}
		delete(known, id)
	}
	var base, maximum int64
	if err = tx.QueryRowContext(ctx, `SELECT coalesce(max(CASE WHEN kind!='step' AND tombstone_event_id IS NULL THEN sort_key ELSE 0 END),0),coalesce(max(sort_key),0)
		FROM m6_nodes WHERE domain_id=? AND parent_id=?`, domain, parent).Scan(&base, &maximum); err != nil {
		return nil, 0, 0, err
	}
	if base > math.MaxInt64-int64(len(order))*1000 || maximum > math.MaxInt64-int64(len(current))-1 {
		return nil, 0, 0, ErrResourceLimit
	}
	return current, base, maximum, nil
}

func applyM6StepOrder(ctx context.Context, tx *sql.Tx, domain string, current, order []string, base, maximum int64, eventID string) error {
	for index, id := range current {
		if _, err := tx.ExecContext(ctx, `UPDATE m6_nodes SET sort_key=?,last_event_id=? WHERE domain_id=? AND node_id=?`, maximum+int64(index)+1, eventID, domain, id); err != nil {
			return err
		}
	}
	for index, id := range order {
		if _, err := tx.ExecContext(ctx, `UPDATE m6_nodes SET sort_key=? WHERE domain_id=? AND node_id=?`, base+int64(index+1)*1000, domain, id); err != nil {
			return err
		}
	}
	return nil
}

func stringValues(values []string) []any {
	result := make([]any, len(values))
	for i, value := range values {
		result[i] = value
	}
	return result
}

func nullableLocator(repair bool, requested string) any {
	if repair {
		return requested
	}
	return nil
}

func step4Problem(code, message string) operation.Result {
	disposition := operation.ResultRefused
	if strings.HasPrefix(code, "validation.") || strings.HasPrefix(code, "not-found.") {
		disposition = operation.ResultRejected
	}
	return operation.Result{Code: disposition, Problem: &operation.Problem{Code: operation.ProblemCode(code), Message: message}}
}

func sha256Bytes(parts ...[]byte) []byte {
	h := sha256.New()
	for _, part := range parts {
		_, _ = h.Write(part)
	}
	return h.Sum(nil)
}

func crockfordPrefix(digest []byte, characters int) string {
	const alphabet = "0123456789ABCDEFGHJKMNPQRSTVWXYZ"
	out := make([]byte, characters)
	for i := 0; i < characters; i++ {
		var character byte
		for j := 0; j < 5; j++ {
			index := i*5 + j
			character <<= 1
			if index/8 < len(digest) && digest[index/8]&(1<<uint(7-index%8)) != 0 {
				character++
			}
		}
		out[i] = alphabet[character]
	}
	return string(out)
}

type step4EventAppender func(id, kind, subject string, payload map[string]any) (uint64, error)

func createM6Step(ctx context.Context, tx *sql.Tx, command operation.Command, eventIDs []string,
	subjectID, parent, matter, title string, appendEvent step4EventAppender, positions *[]uint64,
	resultOutput *any, fold step4Fold,
) (step4Fold, error) {
	if len(eventIDs) != 1 || !ulid.MatchString(subjectID) {
		return fold, ErrInvalidProof
	}
	locator, err := nextM6StepLocator(ctx, tx, command.AuthorityDomainID, matter)
	if err != nil {
		return fold, err
	}
	if MatterLocator(title) == "" {
		fold.result = step4Problem("validation.empty-locator", "Step title does not yield a canonical locator")
		fold.subject, fold.eventIDs = "", nil
		return fold, nil
	}
	sortKey, err := nextM6SortKey(ctx, tx, command.AuthorityDomainID, parent)
	if err != nil {
		return fold, err
	}
	position, err := appendEvent(eventIDs[0], "step.created", subjectID, map[string]any{
		"title": title, "locator": locator, "parent": parent, "sort_key": sortKey,
	})
	if err != nil {
		return fold, err
	}
	_, err = tx.ExecContext(ctx, `INSERT INTO m6_nodes(domain_id,node_id,kind,repo_id,matter_id,parent_id,locator,title,sort_key,birth_event_id,last_event_id,repair_required)
		VALUES (?, ?, 'step', ?, ?, ?, ?, ?, ?, ?, ?, 0)`, command.AuthorityDomainID, subjectID, command.Request.Context.Repo,
		matter, parent, locator, title, sortKey, eventIDs[0], eventIDs[0])
	if err != nil {
		return fold, writeError(err)
	}
	*positions = append(*positions, position)
	*resultOutput = operation.StepCreateOutput{ID: subjectID, ParentID: parent, MatterID: matter, Locator: locator, Title: title, SortKey: sortKey, State: "planned"}
	return finishStep4Success(fold, *resultOutput, subjectID, eventIDs, *positions)
}

func insertM6Step(ctx context.Context, tx *sql.Tx, command operation.Command, eventIDs []string, subjectID string,
	input operation.StepInsertInput, matter string, appendEvent step4EventAppender, positions *[]uint64,
	resultOutput *any, fold step4Fold,
) (step4Fold, error) {
	rows, err := tx.QueryContext(ctx, `SELECT node_id,sort_key FROM m6_nodes WHERE domain_id=? AND parent_id=? AND kind='step' AND tombstone_event_id IS NULL ORDER BY sort_key`,
		command.AuthorityDomainID, input.ParentID)
	if err != nil {
		return fold, err
	}
	type sibling struct {
		id  string
		key int64
	}
	var siblings []sibling
	for rows.Next() {
		var current sibling
		if err = rows.Scan(&current.id, &current.key); err != nil {
			break
		}
		siblings = append(siblings, current)
	}
	if err == nil {
		err = rows.Err()
	}
	_ = rows.Close()
	if err != nil {
		return fold, err
	}
	position := len(siblings)
	anchor := input.AfterID
	if input.BeforeID != "" {
		anchor = input.BeforeID
	}
	if anchor != "" {
		found := false
		for index, sibling := range siblings {
			if sibling.id == anchor {
				position, found = index, true
				if input.AfterID != "" {
					position++
				}
				break
			}
		}
		if !found {
			fold.result = step4Problem("validation.not-a-live-sibling", "insertion anchor is not a live Step child of the parent")
			fold.subject, fold.eventIDs = "", nil
			return fold, nil
		}
	}
	locator, err := nextM6StepLocator(ctx, tx, command.AuthorityDomainID, matter)
	if err != nil {
		return fold, err
	}
	if MatterLocator(input.Title) == "" {
		fold.result = step4Problem("validation.empty-locator", "Step title does not yield a canonical locator")
		fold.subject, fold.eventIDs = "", nil
		return fold, nil
	}
	var key int64
	fit := true
	switch {
	case len(siblings) == 0 || position == len(siblings):
		key, err = nextM6SortKey(ctx, tx, command.AuthorityDomainID, input.ParentID)
	case position == 0:
		key, fit = midpoint(0, siblings[0].key)
	default:
		key, fit = midpoint(siblings[position-1].key, siblings[position].key)
	}
	if err != nil {
		return fold, err
	}
	if key > 0 {
		var occupied int
		if err = tx.QueryRowContext(ctx, `SELECT count(*) FROM m6_nodes WHERE domain_id=? AND parent_id=? AND tombstone_event_id IS NULL AND sort_key=?`,
			command.AuthorityDomainID, input.ParentID, key).Scan(&occupied); err != nil {
			return fold, err
		}
		fit = fit && occupied == 0
	}
	eventIndex := 0
	if !fit {
		if len(eventIDs) != 2 {
			return fold, ErrInvalidProof
		}
		ids := make([]any, len(siblings))
		orderedIDs := make([]string, len(siblings))
		for index, sibling := range siblings {
			ids[index] = sibling.id
			orderedIDs[index] = sibling.id
		}
		positionEvent, appendErr := appendEvent(eventIDs[0], "step.reordered", input.ParentID, map[string]any{"order": ids})
		if appendErr != nil {
			return fold, appendErr
		}
		*positions = append(*positions, positionEvent)
		current, reorderBase, maximum, orderErr := readM6StepOrder(ctx, tx, command.AuthorityDomainID, input.ParentID, orderedIDs)
		if orderErr != nil {
			return fold, orderErr
		}
		if err = applyM6StepOrder(ctx, tx, command.AuthorityDomainID, current, orderedIDs, reorderBase, maximum, eventIDs[0]); err != nil {
			return fold, err
		}
		var base int64
		if err = tx.QueryRowContext(ctx, `SELECT coalesce(max(sort_key),0) FROM m6_nodes WHERE domain_id=? AND parent_id=? AND kind!='step' AND tombstone_event_id IS NULL`, command.AuthorityDomainID, input.ParentID).Scan(&base); err != nil {
			return fold, err
		}
		if base > math.MaxInt64-int64(len(siblings)+2)*1000 {
			return fold, ErrResourceLimit
		}
		key = base + int64(position)*1000 + 500
		eventIndex = 1
	}
	if eventIndex >= len(eventIDs) || !ulid.MatchString(subjectID) {
		return fold, ErrInvalidProof
	}
	pos, err := appendEvent(eventIDs[eventIndex], "step.inserted", subjectID, map[string]any{
		"title": input.Title, "locator": locator, "parent": input.ParentID, "sort_key": key,
	})
	if err != nil {
		return fold, err
	}
	*positions = append(*positions, pos)
	if _, err = tx.ExecContext(ctx, `INSERT INTO m6_nodes(domain_id,node_id,kind,repo_id,matter_id,parent_id,locator,title,sort_key,birth_event_id,last_event_id,repair_required)
		VALUES (?, ?, 'step', ?, ?, ?, ?, ?, ?, ?, ?, 0)`, command.AuthorityDomainID, subjectID, command.Request.Context.Repo,
		matter, input.ParentID, locator, input.Title, key, eventIDs[eventIndex], eventIDs[eventIndex]); err != nil {
		return fold, writeError(err)
	}
	*resultOutput = operation.StepCreateOutput{ID: subjectID, ParentID: input.ParentID, MatterID: matter, Locator: locator, Title: input.Title, SortKey: key, State: "planned"}
	return finishStep4Success(fold, *resultOutput, subjectID, eventIDs, *positions)
}

func midpoint(before, after int64) (int64, bool) {
	if before < 0 || after-before <= 1 {
		return 0, false
	}
	return before + (after-before)/2, true
}

func finishStep4Success(fold step4Fold, output any, subject string, eventIDs []string, positions []uint64) (step4Fold, error) {
	encoded, err := step4OutputBytes(output)
	if err != nil {
		return fold, err
	}
	typed, ok := output.(operation.Output)
	if !ok {
		return fold, ErrInvalidProof
	}
	fold.result = operation.Result{Code: operation.ResultSucceeded, Output: typed}
	fold.output = encoded
	fold.subject = subject
	fold.eventIDs = eventIDs[:len(positions)]
	fold.first, fold.last = positions[0], positions[len(positions)-1]
	fold.rangeValue = map[string]any{"first_event_id": eventIDs[0], "last_event_id": eventIDs[len(positions)-1], "event_count": uint64(len(positions))}
	return fold, nil
}

func step4OutputBytes(output any) ([]byte, error) {
	var value map[string]any
	switch result := output.(type) {
	case operation.MatterCreateV2Output:
		value = map[string]any{
			"id": result.ID, "title": result.Title, "requested_locator": result.RequestedLocator,
			"assigned_locator": result.AssignedLocator, "locator_repair_required": result.LocatorRepairRequired,
		}
	case operation.StageCreateOutput:
		value = map[string]any{
			"id": result.ID, "matter_id": result.MatterID, "locator": result.Locator, "title": result.Title,
			"sort_key": result.SortKey, "state": result.State,
		}
	case operation.StepCreateOutput:
		value = map[string]any{
			"id": result.ID, "parent_id": result.ParentID, "matter_id": result.MatterID, "locator": result.Locator,
			"title": result.Title, "sort_key": result.SortKey, "state": result.State,
		}
	case operation.StepReorderOutput:
		value = map[string]any{"parent_id": result.ParentID, "order": stringValues(result.Order)}
	case operation.StepReplaceOutput:
		replacement, err := step4OutputBytes(result.Replacement)
		if err != nil {
			return nil, err
		}
		var decoded map[string]any
		if err = artifactDecoder.Unmarshal(replacement, &decoded); err != nil {
			return nil, err
		}
		value = map[string]any{"removed_step_id": result.RemovedStepID, "replacement": decoded}
	case operation.StepRemoveOutput:
		value = map[string]any{"step_id": result.StepID}
	case operation.MatterLocatorRepairOutput:
		value = map[string]any{
			"id": result.ID, "action": result.Action, "requested_locator": result.RequestedLocator,
			"previous_locator": result.PreviousLocator, "assigned_locator": result.AssignedLocator,
		}
	default:
		return nil, ErrInvalidProof
	}
	return artifactEncoder.Marshal(value)
}
