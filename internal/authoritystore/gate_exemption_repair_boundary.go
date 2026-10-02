package authoritystore

import (
	"context"
	"database/sql"
	"errors"
	"fmt"

	"github.com/fxamacker/cbor/v2"
)

const gateRepairBoundaryWitnessSchema = "wipd.gate-exemption-repair-terminal-boundary/1"

type gateRepairBoundaryWitness struct {
	Schema              string `cbor:"schema"`
	DomainID            string `cbor:"domain_id"`
	CommandID           string `cbor:"command_id"`
	RequestHash         string `cbor:"request_hash"`
	Position            uint64 `cbor:"event_count"`
	EventID             string `cbor:"high_water_event_id"`
	PrefixDigest        string `cbor:"prefix_digest"`
	ResultCode          string `cbor:"result_code"`
	RefusalCode         string `cbor:"refusal_code"`
	RefusalMessage      string `cbor:"refusal_message"`
	TerminalEventID     string `cbor:"terminal_event_id"`
	OccurredAt          string `cbor:"occurred_at"`
	Fence               string `cbor:"fence"`
	ActiveEpoch         uint64 `cbor:"active_epoch"`
	ClaimID             string `cbor:"claim_id"`
	ClaimAuthorityEpoch uint64 `cbor:"claim_authority_epoch"`
	ClaimEpoch          uint64 `cbor:"claim_epoch"`
	OwnerEnvironmentID  string `cbor:"owner_environment_id"`
	RepoID              string `cbor:"repo_id"`
	WorktreeID          string `cbor:"worktree_id"`
	MatterID            string `cbor:"matter_id"`
	CloseCommandID      string `cbor:"close_command_id"`
	ClosePosition       uint64 `cbor:"close_position"`
	JournalID           string `cbor:"journal_id"`
	JournalGeneration   uint64 `cbor:"journal_generation"`
	JournalState        string `cbor:"journal_state"`
	MatterLive          bool   `cbor:"matter_live"`
	MatterLifecycle     string `cbor:"matter_lifecycle"`
}

type gateRepairFenceSnapshot struct {
	activeEpoch         uint64
	claimID             string
	claimAuthorityEpoch uint64
	claimEpoch          uint64
	ownerEnvironmentID  string
	repoID              string
	worktreeID          string
	matterID            string
	closeCommandID      string
	closePosition       uint64
	journalID           string
	journalGeneration   uint64
	journalState        string
	matterLive          bool
	matterLifecycle     string
	fence               string
}

func (snapshot gateRepairFenceSnapshot) witness(terminal gateExemptionRepairTerminal) gateRepairBoundaryWitness {
	return gateRepairBoundaryWitness{
		Schema: gateRepairBoundaryWitnessSchema, DomainID: terminal.DomainID, CommandID: terminal.CommandID,
		RequestHash: terminal.RequestHash, Position: terminal.ObservedPosition, EventID: terminal.ObservedEventID,
		PrefixDigest: terminal.ObservedPrefixDigest, ResultCode: terminal.ResultCode, RefusalCode: terminal.RefusalCode,
		RefusalMessage: terminal.RefusalMessage, TerminalEventID: terminal.EventID, OccurredAt: terminal.OccurredAt,
		Fence: snapshot.fence, ActiveEpoch: snapshot.activeEpoch, ClaimID: snapshot.claimID,
		ClaimAuthorityEpoch: snapshot.claimAuthorityEpoch, ClaimEpoch: snapshot.claimEpoch,
		OwnerEnvironmentID: snapshot.ownerEnvironmentID, RepoID: snapshot.repoID, WorktreeID: snapshot.worktreeID,
		MatterID: snapshot.matterID, CloseCommandID: snapshot.closeCommandID, ClosePosition: snapshot.closePosition,
		JournalID: snapshot.journalID, JournalGeneration: snapshot.journalGeneration, JournalState: snapshot.journalState,
		MatterLive: snapshot.matterLive, MatterLifecycle: snapshot.matterLifecycle,
	}
}

func legacyGateRepairBoundaryWitness(terminal gateExemptionRepairTerminal) gateRepairBoundaryWitness {
	return gateRepairBoundaryWitness{
		Schema: gateRepairBoundaryWitnessSchema, DomainID: terminal.DomainID, CommandID: terminal.CommandID,
		RequestHash: terminal.RequestHash, Position: terminal.ObservedPosition, EventID: terminal.ObservedEventID,
		PrefixDigest: terminal.ObservedPrefixDigest, ResultCode: terminal.ResultCode, RefusalCode: terminal.RefusalCode,
		RefusalMessage: terminal.RefusalMessage, TerminalEventID: terminal.EventID, OccurredAt: terminal.OccurredAt,
		Fence: "legacy",
	}
}

func migratedGateRepairBoundaryWitness(ctx context.Context, queryer gateRepairQueryer, command gateExemptionRepairCommand,
	terminal gateExemptionRepairTerminal,
) (gateRepairBoundaryWitness, error) {
	if terminal.RefusalCode != "refusal.claim-fenced" {
		return legacyGateRepairBoundaryWitness(terminal), nil
	}
	closeCommand, closePosition, err := claimCloseAtBoundary(ctx, queryer, command.DomainID, command.ClaimID, terminal.ObservedPosition)
	if err != nil || closeCommand == "" {
		return gateRepairBoundaryWitness{}, fmt.Errorf("%w: v14 claim-fence terminal has no close event at its observed prefix", ErrInvalidStore)
	}
	var activeEpoch, claimAuthorityEpoch, claimEpoch, journalGeneration uint64
	var owner, repo, worktree, matter, storedClose, journalID, journalState string
	err = queryer.QueryRowContext(ctx, `SELECT d.active_epoch,c.authority_epoch,c.claim_epoch,c.owner_environment_id,m.repo_id,c.worktree_id,
		c.matter_id,coalesce(c.close_command_id,''),j.journal_id,j.generation,j.state
		FROM domains d JOIN claims c ON c.domain_id=d.domain_id JOIN matters m ON m.domain_id=c.domain_id AND m.matter_id=c.matter_id
		JOIN claim_journals j ON j.domain_id=c.domain_id AND j.claim_id=c.claim_id
		WHERE d.domain_id=? AND c.claim_id=? AND j.generation=(SELECT max(generation) FROM claim_journals WHERE domain_id=? AND claim_id=?)`,
		command.DomainID, command.ClaimID, command.DomainID, command.ClaimID).Scan(
		&activeEpoch, &claimAuthorityEpoch, &claimEpoch, &owner, &repo, &worktree, &matter, &storedClose,
		&journalID, &journalGeneration, &journalState)
	if err != nil || activeEpoch < command.AuthorityEpoch || claimAuthorityEpoch != command.AuthorityEpoch || claimEpoch != command.ClaimEpoch ||
		owner != command.EnvironmentID || repo != command.RepoID || worktree != command.WorktreeID || storedClose != closeCommand ||
		journalGeneration == 0 || !ulid.MatchString(journalID) || (journalState != "sealed" && journalState != "quarantined") {
		return gateRepairBoundaryWitness{}, fmt.Errorf("%w: v14 claim-close witness does not match immutable claim history", ErrInvalidStore)
	}
	return gateRepairBoundaryWitness{
		Schema: gateRepairBoundaryWitnessSchema, DomainID: terminal.DomainID, CommandID: terminal.CommandID,
		RequestHash: terminal.RequestHash, Position: terminal.ObservedPosition, EventID: terminal.ObservedEventID,
		PrefixDigest: terminal.ObservedPrefixDigest, ResultCode: terminal.ResultCode, RefusalCode: terminal.RefusalCode,
		RefusalMessage: terminal.RefusalMessage, TerminalEventID: terminal.EventID, OccurredAt: terminal.OccurredAt,
		Fence: "claim-closed", ActiveEpoch: command.AuthorityEpoch, ClaimID: command.ClaimID,
		ClaimAuthorityEpoch: claimAuthorityEpoch, ClaimEpoch: claimEpoch, OwnerEnvironmentID: owner,
		RepoID: repo, WorktreeID: worktree, MatterID: matter, CloseCommandID: closeCommand, ClosePosition: closePosition,
		JournalID: journalID, JournalGeneration: journalGeneration, JournalState: journalState,
	}, nil
}

func encodeGateRepairBoundaryWitness(witness gateRepairBoundaryWitness) ([]byte, error) {
	encoded, err := artifactEncoder.Marshal(witness)
	if err != nil || len(encoded) == 0 || len(encoded) > 4096 {
		return nil, ErrInvalidStore
	}
	return encoded, nil
}

func decodeGateRepairBoundaryWitness(raw []byte) (gateRepairBoundaryWitness, error) {
	var witness gateRepairBoundaryWitness
	if len(raw) == 0 || len(raw) > 4096 {
		return witness, ErrInvalidStore
	}
	var fields map[string]cbor.RawMessage
	if canonicalDecode(raw, &fields) != nil || !exactKeys(fields,
		"schema", "domain_id", "command_id", "request_hash", "event_count", "high_water_event_id", "prefix_digest",
		"result_code", "refusal_code", "refusal_message", "terminal_event_id", "occurred_at", "fence", "active_epoch",
		"claim_id", "claim_authority_epoch", "claim_epoch", "owner_environment_id", "repo_id", "worktree_id", "matter_id",
		"close_command_id", "close_position", "journal_id", "journal_generation", "journal_state", "matter_live", "matter_lifecycle") ||
		artifactDecoder.Unmarshal(raw, &witness) != nil {
		return gateRepairBoundaryWitness{}, ErrInvalidStore
	}
	return witness, nil
}

func captureGateRepairFence(ctx context.Context, tx *sql.Tx, command gateExemptionRepairCommand, anchor PrefixAnchor) (gateRepairFenceSnapshot, *gateRepairRefusal, error) {
	var snapshot gateRepairFenceSnapshot
	var active int64
	if err := tx.QueryRowContext(ctx, `SELECT active_epoch FROM domains WHERE domain_id=?`, command.DomainID).Scan(&active); err != nil || active <= 0 {
		return snapshot, nil, ErrInvalidStore
	}
	snapshot.activeEpoch = uint64(active)
	var close sql.NullString
	if err := tx.QueryRowContext(ctx, `SELECT c.authority_epoch,c.claim_epoch,c.owner_environment_id,m.repo_id,c.worktree_id,c.matter_id,c.close_command_id
		FROM claims c JOIN matters m ON m.domain_id=c.domain_id AND m.matter_id=c.matter_id
		WHERE c.domain_id=? AND c.claim_id=?`, command.DomainID, command.ClaimID).Scan(
		&snapshot.claimAuthorityEpoch, &snapshot.claimEpoch, &snapshot.ownerEnvironmentID, &snapshot.repoID,
		&snapshot.worktreeID, &snapshot.matterID, &close); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return snapshot, nil, ErrInvalidStore
		}
		return snapshot, nil, err
	}
	snapshot.closeCommandID = close.String
	if err := tx.QueryRowContext(ctx, `SELECT journal_id,generation,state FROM claim_journals
		WHERE domain_id=? AND claim_id=? AND generation=(SELECT max(generation) FROM claim_journals WHERE domain_id=? AND claim_id=?)`,
		command.DomainID, command.ClaimID, command.DomainID, command.ClaimID).Scan(
		&snapshot.journalID, &snapshot.journalGeneration, &snapshot.journalState); err != nil {
		return snapshot, nil, ErrInvalidStore
	}
	if snapshot.journalGeneration == 0 || !ulid.MatchString(snapshot.journalID) {
		return snapshot, nil, ErrInvalidStore
	}
	snapshot.claimID = command.ClaimID
	closeCommand, closePosition, err := claimCloseAtBoundary(ctx, tx, command.DomainID, command.ClaimID, anchor.EventCount)
	if err != nil || closeCommand != snapshot.closeCommandID {
		return snapshot, nil, ErrInvalidStore
	}
	snapshot.closePosition = closePosition
	switch {
	case snapshot.activeEpoch != command.AuthorityEpoch:
		snapshot.fence = "authority-epoch"
	case snapshot.closePosition != 0:
		snapshot.fence = "claim-closed"
	case snapshot.journalState != "open":
		snapshot.fence = "journal-state"
	default:
		err := validateGateClaimTx(ctx, tx, command.claimCommand())
		if err == nil {
			snapshot.fence = "clear"
			return snapshot, nil, nil
		}
		if !errors.Is(err, ErrFenced) {
			return snapshot, nil, err
		}
		if snapshot.claimAuthorityEpoch != command.AuthorityEpoch || snapshot.claimEpoch != command.ClaimEpoch ||
			snapshot.ownerEnvironmentID != command.EnvironmentID || snapshot.repoID != command.RepoID || snapshot.worktreeID != command.WorktreeID {
			snapshot.fence = "claim-context"
		} else {
			history, historyErr := gateRepairSnapshotAt(ctx, tx, command.DomainID, anchor.EventCount)
			if historyErr != nil {
				return snapshot, nil, historyErr
			}
			matterKey := ownerKey(command.DomainID, snapshot.matterID)
			matter, exists := history.nodes[matterKey]
			snapshot.matterLive = exists && step13NodeLiveAt(matter, anchor.EventCount)
			snapshot.matterLifecycle = history.lifecycle[matterKey]
			if snapshot.matterLive && (snapshot.matterLifecycle == "planned" || snapshot.matterLifecycle == "in-progress" || snapshot.matterLifecycle == "done") {
				return snapshot, nil, fmt.Errorf("%w: claim fence lacks a boundary witness", ErrInvalidStore)
			}
			snapshot.fence = "matter-state"
		}
	}
	return snapshot, &gateRepairRefusal{"refusal.claim-fenced", gateRepairClaimFencedMessage}, nil
}

func validateGateRepairBoundaryWitness(ctx context.Context, queryer gateRepairQueryer, command gateExemptionRepairCommand,
	terminal gateExemptionRepairTerminal, raw []byte,
) error {
	witness, err := decodeGateRepairBoundaryWitness(raw)
	if err != nil {
		return err
	}
	if witness.Schema != gateRepairBoundaryWitnessSchema || witness.DomainID != terminal.DomainID || witness.CommandID != terminal.CommandID ||
		witness.RequestHash != terminal.RequestHash || witness.Position != terminal.ObservedPosition || witness.EventID != terminal.ObservedEventID ||
		witness.PrefixDigest != terminal.ObservedPrefixDigest || witness.ResultCode != terminal.ResultCode ||
		witness.RefusalCode != terminal.RefusalCode || witness.RefusalMessage != terminal.RefusalMessage ||
		witness.TerminalEventID != terminal.EventID || witness.OccurredAt != terminal.OccurredAt {
		return fmt.Errorf("%w: private repair terminal does not match its boundary witness", ErrInvalidStore)
	}
	if terminal.ResultCode == "result.refused" && terminal.RefusalCode == "refusal.claim-fenced" {
		if terminal.RefusalMessage != gateRepairClaimFencedMessage || witness.Fence == "clear" || witness.Fence == "legacy" {
			return fmt.Errorf("%w: private repair claim-fence refusal lacks a terminal-boundary cause", ErrInvalidStore)
		}
	} else if witness.Fence != "clear" && witness.Fence != "legacy" {
		return fmt.Errorf("%w: private repair non-fence terminal has a claim-fence witness", ErrInvalidStore)
	}
	if witness.Fence == "legacy" {
		return nil
	}
	if witness.ClaimID != command.ClaimID || witness.ActiveEpoch == 0 || witness.ClaimEpoch == 0 ||
		!ulid.MatchString(witness.JournalID) || witness.JournalGeneration == 0 || witness.JournalState == "" {
		return fmt.Errorf("%w: private repair claim witness is incomplete", ErrInvalidStore)
	}
	var initialEpoch, activeEpoch int64
	if err = queryer.QueryRowContext(ctx, `SELECT initial_epoch,active_epoch FROM domains WHERE domain_id=?`, command.DomainID).Scan(&initialEpoch, &activeEpoch); err != nil ||
		initialEpoch <= 0 || activeEpoch < int64(witness.ActiveEpoch) || int64(witness.ActiveEpoch) < initialEpoch {
		return fmt.Errorf("%w: private repair epoch witness is not in domain history", ErrInvalidStore)
	}
	var claimAuthority, claimEpoch uint64
	var owner, repo, worktree, matter string
	var currentClose sql.NullString
	if err = queryer.QueryRowContext(ctx, `SELECT c.authority_epoch,c.claim_epoch,c.owner_environment_id,m.repo_id,c.worktree_id,c.matter_id,c.close_command_id
		FROM claims c JOIN matters m ON m.domain_id=c.domain_id AND m.matter_id=c.matter_id
		WHERE c.domain_id=? AND c.claim_id=?`, command.DomainID, command.ClaimID).Scan(
		&claimAuthority, &claimEpoch, &owner, &repo, &worktree, &matter, &currentClose); err != nil ||
		claimAuthority != witness.ClaimAuthorityEpoch || claimEpoch != witness.ClaimEpoch || owner != witness.OwnerEnvironmentID ||
		repo != witness.RepoID || worktree != witness.WorktreeID || matter != witness.MatterID {
		return fmt.Errorf("%w: private repair claim identity witness mismatch", ErrInvalidStore)
	}
	var journalGeneration uint64
	var journalState string
	if err = queryer.QueryRowContext(ctx, `SELECT generation,state FROM claim_journals WHERE domain_id=? AND claim_id=? AND journal_id=?`,
		command.DomainID, command.ClaimID, witness.JournalID).Scan(&journalGeneration, &journalState); err != nil || journalGeneration != witness.JournalGeneration ||
		!journalStateCanFollow(witness.JournalState, journalState) {
		return fmt.Errorf("%w: private repair journal witness mismatch", ErrInvalidStore)
	}
	closeCommand, closePosition, err := claimCloseAtBoundary(ctx, queryer, command.DomainID, command.ClaimID, witness.Position)
	if err != nil {
		return err
	}
	if witness.CloseCommandID == "" {
		if closeCommand != "" || witness.ClosePosition != 0 {
			return fmt.Errorf("%w: unexpected claim-close at terminal prefix", ErrInvalidStore)
		}
	} else if witness.CloseCommandID != closeCommand || witness.ClosePosition != closePosition || witness.ClosePosition == 0 || currentClose.String != witness.CloseCommandID {
		return fmt.Errorf("%w: claim-close witness does not match terminal prefix", ErrInvalidStore)
	}
	switch witness.Fence {
	case "clear":
		if witness.ActiveEpoch != command.AuthorityEpoch || witness.ClaimAuthorityEpoch != command.AuthorityEpoch || witness.ClaimEpoch != command.ClaimEpoch ||
			witness.OwnerEnvironmentID != command.EnvironmentID || witness.RepoID != command.RepoID || witness.WorktreeID != command.WorktreeID ||
			witness.JournalState != "open" || closeCommand != "" || witness.CloseCommandID != "" || witness.ClosePosition != 0 {
			return fmt.Errorf("%w: clear claim witness contradicts its terminal prefix", ErrInvalidStore)
		}
	case "authority-epoch":
		if witness.ActiveEpoch == command.AuthorityEpoch {
			return fmt.Errorf("%w: authority fence was not present at terminal prefix", ErrInvalidStore)
		}
	case "claim-closed":
		if witness.ActiveEpoch != command.AuthorityEpoch || witness.CloseCommandID == "" {
			return fmt.Errorf("%w: claim-close fence was not present at terminal prefix", ErrInvalidStore)
		}
	case "journal-state":
		if witness.ActiveEpoch != command.AuthorityEpoch || closeCommand != "" || witness.JournalState == "open" ||
			witness.JournalState != "sealed" && witness.JournalState != "quarantined" {
			return fmt.Errorf("%w: journal fence was not present at terminal prefix", ErrInvalidStore)
		}
	case "claim-context":
		if witness.ActiveEpoch != command.AuthorityEpoch || closeCommand != "" || witness.ClaimAuthorityEpoch == command.AuthorityEpoch &&
			witness.ClaimEpoch == command.ClaimEpoch && witness.OwnerEnvironmentID == command.EnvironmentID &&
			witness.RepoID == command.RepoID && witness.WorktreeID == command.WorktreeID {
			return fmt.Errorf("%w: claim-context fence was not present at terminal prefix", ErrInvalidStore)
		}
	case "matter-state":
		if witness.ActiveEpoch != command.AuthorityEpoch || closeCommand != "" || witness.JournalState != "open" ||
			witness.ClaimAuthorityEpoch != command.AuthorityEpoch || witness.ClaimEpoch != command.ClaimEpoch ||
			witness.OwnerEnvironmentID != command.EnvironmentID || witness.RepoID != command.RepoID || witness.WorktreeID != command.WorktreeID {
			return fmt.Errorf("%w: Matter-state fence contradicts terminal prefix", ErrInvalidStore)
		}
		history, historyErr := gateRepairSnapshotAt(ctx, queryer, command.DomainID, witness.Position)
		if historyErr != nil {
			return historyErr
		}
		matterKey := ownerKey(command.DomainID, witness.MatterID)
		matter, exists := history.nodes[matterKey]
		live := exists && step13NodeLiveAt(matter, witness.Position)
		lifecycle := history.lifecycle[matterKey]
		if live != witness.MatterLive || lifecycle != witness.MatterLifecycle ||
			live && (lifecycle == "planned" || lifecycle == "in-progress" || lifecycle == "done") {
			return fmt.Errorf("%w: Matter-state fence was not present at terminal prefix", ErrInvalidStore)
		}
	default:
		return fmt.Errorf("%w: unknown private repair terminal fence %q", ErrInvalidStore, witness.Fence)
	}
	return nil
}

func journalStateCanFollow(atBoundary, current string) bool {
	switch atBoundary {
	case "open":
		return current == "open" || current == "sealed" || current == "quarantined"
	case "sealed":
		return current == "sealed" || current == "quarantined"
	case "quarantined":
		return current == "quarantined"
	default:
		return false
	}
}

func claimCloseAtBoundary(ctx context.Context, queryer gateRepairQueryer, domain, claim string, position uint64) (string, uint64, error) {
	rows, err := queryer.QueryContext(ctx, `SELECT command_id,position,event_id,record FROM authority_events
		WHERE domain_id=? AND position<=? ORDER BY position`, domain, position)
	if err != nil {
		return "", 0, err
	}
	defer func() { _ = rows.Close() }()
	var command string
	var closePosition uint64
	for rows.Next() {
		var eventCommand, eventID string
		var eventPosition uint64
		var raw []byte
		if err = rows.Scan(&eventCommand, &eventPosition, &eventID, &raw); err != nil {
			return "", 0, err
		}
		event, parseErr := parseStep12Event(raw, domain, eventPosition, eventID, eventCommand)
		if parseErr != nil {
			return "", 0, fmt.Errorf("%w: malformed claim-close history", ErrInvalidStore)
		}
		if event.subject != claim || (event.kind != "claim.released" && event.kind != "claim.stood-down") {
			continue
		}
		if command != "" {
			return "", 0, fmt.Errorf("%w: multiple claim closes at terminal prefix", ErrInvalidStore)
		}
		command, closePosition = eventCommand, eventPosition
	}
	if err = rows.Err(); err != nil {
		return "", 0, err
	}
	return command, closePosition, nil
}
