package wipd

import (
	"context"
	"errors"
	"fmt"
	"sort"

	"github.com/procrastivity/wip/internal/operation"
	"github.com/procrastivity/wip/internal/wipdjournal"
	"github.com/procrastivity/wip/internal/wipdwire"
)

// SchedulerResumeResponse is the durable frontier selected by the owning
// Environment. A correction Role is present only when the retained negative
// Verifier result makes a Builder pass the next Workplan action.
type SchedulerResumeResponse struct {
	RunID           string
	MatterID        string
	Claim           wipdjournal.ClaimGrantSummary
	RoleID          string
	RoleCommandID   string
	RoleName        string
	TargetID        string
	TargetKind      string
	WorkCausationID string
	Finished        bool
	Blocked         string
}

type schedulerResumeClaim struct {
	claimID, matterID, batchID, dispatchID, worktreeID, owner string
	epoch, environmentSequence, eventPosition                 uint64
	releaseEnvironmentSequence, releaseEventPosition          uint64
	commandID                                                 string
	released                                                  bool
}

type schedulerResumeRole struct {
	roleID, commandID, matterID, dispatchID, claimID, claimEpoch, name string
}

type schedulerResumeState struct {
	runID, locator, batchID, owner, repoID, cloneID, worktreeID string
	startCommandID                                              string
	startEnvironmentSequence                                    uint64
	matters                                                     []string
	activeRoles                                                 map[string]schedulerResumeRole
	claims                                                      map[string]*schedulerResumeClaim
	statuses                                                    map[string]string
	planCommandByMatter                                         map[string]string
	planStepIDs                                                 map[string][]string
	matterGates                                                 map[string]bool
	closedGates                                                 map[string]bool
	correctionMatters, needsVerifier, planCompleted             map[string]bool
	skippedMatters                                              map[string]bool
	failedBuilderPolicyPending                                  map[string]bool
	correctionVerifierCommand, correctionStepByMatter           map[string]string
	correctionStepByVerifier                                    map[string]string
	correctionProblem, verifierCommandByMatter                  map[string]string
	policyHalt                                                  bool
	runFinished                                                 bool
	lastResumePosition, lastResumeSequence, lastReleasePosition uint64
	lastReleaseSequence                                         uint64
}

// ResumeOwnedRun is the private, authenticated owner-Environment orchestration
// boundary. It resolves retained Step 10 identities, closes old Role brackets,
// performs ordinary receipt-safe claim release, records Run resume, then
// recomputes the authoritative frontier before acquiring fresh work.
func (coordinator *CommandStartCoordinator) ResumeOwnedRun(ctx context.Context, runID string) (SchedulerResumeResponse, error) {
	var empty SchedulerResumeResponse
	if coordinator == nil || coordinator.journal == nil || coordinator.environment == nil || ctx == nil ||
		!commandStartULID.MatchString(runID) {
		return empty, ErrCommandStartIdentity
	}
	coordinator.schedulerResumeMu.Lock()
	defer coordinator.schedulerResumeMu.Unlock()
	identity := coordinator.journal.Identity()
	synthetic := operation.Command{
		AuthorityDomainID: identity.DomainID, ExpectedAuthorityEpoch: identity.AuthorityEpoch,
		EnvironmentID: identity.EnvironmentID,
		Request: operation.Request{
			Operation: operation.SchedulerResumeV1.Metadata().Operation,
			Actor:     "system:scheduler",
			Input:     operation.RunResumeInput{RunID: runID},
		},
	}
	if _, lockAcquired, err := coordinator.retainSchedulerRun(synthetic); err != nil {
		return empty, err
	} else if lockAcquired {
		return coordinator.resumeOwnedRunWithLock(ctx, runID, true)
	}
	return coordinator.resumeOwnedRunWithLock(ctx, runID, false)
}

func (coordinator *CommandStartCoordinator) resumeOwnedRunWithLock(ctx context.Context, runID string, newRunLock bool) (SchedulerResumeResponse, error) {
	var empty SchedulerResumeResponse
	identity := coordinator.journal.Identity()
	// Keep the Run lock on every post-acquisition error. A partially completed
	// recovery must not let another local coordinator mistake uncertainty for
	// free work; an exact retry on this coordinator continues from the journal.
	if err := coordinator.recoverSchedulerRun(ctx, runID); err != nil {
		return empty, fmt.Errorf("wipd: recover retained scheduler commands: %w", err)
	}
	var snapshot CommandStartSnapshot
	state, _, err := coordinator.schedulerResumeState(ctx, runID)
	if err != nil {
		return empty, fmt.Errorf("wipd: reconstruct scheduler Run prefix: %w", err)
	}
	if err = coordinator.recoverSchedulerMatterStarts(ctx, state); err != nil {
		return empty, fmt.Errorf("wipd: recover Matter starts: %w", err)
	}
	if err = coordinator.resolveSchedulerResumeAcquisitions(ctx, state); err != nil {
		return empty, fmt.Errorf("wipd: resolve retained claim acquisitions: %w", err)
	}
	state, snapshot, err = coordinator.schedulerResumeState(ctx, runID)
	if err != nil {
		return empty, fmt.Errorf("wipd: reconstruct scheduler Run after acquisition recovery: %w", err)
	}
	if state.owner != identity.EnvironmentID || state.repoID != identity.RepoID || state.runID != runID {
		return empty, ErrCommandStartIdentity
	}
	if !schedulerRunIsActive(state, snapshot) {
		coordinator.releaseSchedulerRun(runID)
		return SchedulerResumeResponse{RunID: runID, Finished: true}, nil
	}
	if matterID := schedulerResumeMissingFailedBuilderPolicy(state); matterID != "" {
		return SchedulerResumeResponse{RunID: runID, MatterID: matterID, Blocked: "policy-required"}, nil
	}
	if schedulerResumeCurrentClaimCount(state) > 1 {
		return empty, ErrCommandStartIdentity
	}

	// A Role on the current Dispatch is live only when this coordinator already
	// owned the Run lock. Acquiring the lock after process restart proves the
	// prior Role owner is gone, so recover its exact work and reap it before a
	// fresh Dispatch rather than treating In Progress as permission to relaunch.
	if claim := schedulerResumeCurrentClaim(state); claim != nil {
		grant, _, grantErr := coordinator.journal.InstalledClaimGrantForClaim(claim.claimID)
		if grantErr != nil || grant.ClaimEpoch != claim.epoch || grant.MatterID != claim.matterID ||
			grant.DispatchID != claim.dispatchID || grant.BatchID != state.batchID {
			return empty, ErrCommandStartIdentity
		}
		if len(state.activeRoles) > 1 {
			return empty, ErrCommandStartIdentity
		}
		if role := schedulerResumeRoleForClaim(state, claim.matterID, claim.claimID); role != nil && !newRunLock {
			if state.policyHalt {
				selection, selectionErr := coordinator.schedulerResumeRoleWork(state, *role)
				if selectionErr != nil {
					return empty, selectionErr
				}
				return SchedulerResumeResponse{
					RunID: runID, MatterID: claim.matterID, Claim: grant,
					RoleID: role.roleID, RoleCommandID: role.commandID, RoleName: role.name,
					TargetID: selection.targetID, TargetKind: selection.targetKind,
					WorkCausationID: selection.causationID, Blocked: "policy-halt",
				}, nil
			}
			if !state.skippedMatters[claim.matterID] {
				selection, selectionErr := coordinator.schedulerResumeRoleWork(state, *role)
				if selectionErr != nil {
					return empty, selectionErr
				}
				if selection.role != "" {
					return SchedulerResumeResponse{
						RunID: runID, MatterID: claim.matterID, Claim: grant,
						RoleID: role.roleID, RoleCommandID: role.commandID, RoleName: role.name,
						TargetID: selection.targetID, TargetKind: selection.targetKind,
						WorkCausationID: selection.causationID,
					}, nil
				}
			}
		}
		if len(state.activeRoles) == 0 {
			selection := schedulerResumeNextWork(state, claim.matterID)
			if selection.role != "" {
				if err = coordinator.startSchedulerResumeMatter(ctx, state, claim); err != nil {
					return empty, err
				}
				if err = coordinator.ensureSchedulerResumeStep(ctx, state, claim, &selection); err != nil {
					return empty, err
				}
				roleID, roleCommandID, spawnErr := coordinator.spawnSchedulerResumeRole(ctx, state, grant, selection.role)
				if spawnErr != nil {
					return empty, spawnErr
				}
				return SchedulerResumeResponse{
					RunID: runID, MatterID: claim.matterID, Claim: grant,
					RoleID: roleID, RoleCommandID: roleCommandID, RoleName: selection.role, TargetID: selection.targetID,
					TargetKind: selection.targetKind, WorkCausationID: selection.causationID,
				}, nil
			}
		}
	}

	if err = coordinator.closeSchedulerResumeRoles(ctx, state, snapshot); err != nil {
		return empty, fmt.Errorf("wipd: close prior Roles before release: %w", err)
	}
	state, _, err = coordinator.schedulerResumeState(ctx, runID)
	if err != nil {
		return empty, fmt.Errorf("wipd: reconstruct scheduler Run after Role closure: %w", err)
	}
	if len(state.activeRoles) != 0 {
		return empty, ErrCommandStartIdentity
	}
	if err = coordinator.finishReadySchedulerMatters(ctx, state); err != nil {
		return empty, fmt.Errorf("wipd: finish eligible Matter before claim release: %w", err)
	}
	state, _, err = coordinator.schedulerResumeState(ctx, runID)
	if err != nil {
		return empty, fmt.Errorf("wipd: reconstruct scheduler Run after Matter finish: %w", err)
	}
	if err = coordinator.releaseSchedulerResumeClaims(ctx, state); err != nil {
		return empty, fmt.Errorf("wipd: release old claims normally: %w", err)
	}
	state, snapshot, err = coordinator.schedulerResumeState(ctx, runID)
	if err != nil {
		return empty, fmt.Errorf("wipd: reconstruct scheduler Run after claim release: %w", err)
	}
	if len(state.activeRoles) != 0 || schedulerResumeHasOpenClaim(state) {
		return empty, ErrCommandStartIdentity
	}
	if err = coordinator.recordSchedulerResume(ctx, state, snapshot); err != nil {
		return empty, fmt.Errorf("wipd: record run.resumed after old release: %w", err)
	}
	state, snapshot, err = coordinator.schedulerResumeState(ctx, runID)
	if err != nil {
		return empty, fmt.Errorf("wipd: reconstruct scheduler frontier after run.resumed: %w", err)
	}
	frontier, disposition := schedulerResumeFrontier(state, snapshot)
	if frontier == "" {
		if disposition == "complete" {
			if err = coordinator.finishSchedulerRun(ctx, state); err != nil {
				return empty, err
			}
			coordinator.releaseSchedulerRun(runID)
			return SchedulerResumeResponse{RunID: runID, Finished: true}, nil
		}
		return SchedulerResumeResponse{RunID: runID, Blocked: disposition}, nil
	}
	claim, err := coordinator.acquireSchedulerResumeClaim(ctx, state, frontier)
	if err != nil {
		return empty, fmt.Errorf("wipd: acquire fresh frontier claim: %w", err)
	}
	selection := schedulerResumeNextWork(state, frontier)
	if selection.role == "" {
		return empty, ErrCommandStartIdentity
	}
	if err = coordinator.startSchedulerResumeMatter(ctx, state, &schedulerResumeClaim{
		claimID: claim.ClaimID, matterID: claim.MatterID, dispatchID: claim.DispatchID, epoch: claim.ClaimEpoch,
	}); err != nil {
		return empty, fmt.Errorf("wipd: start selected Matter before Role activation: %w", err)
	}
	if err = coordinator.ensureSchedulerResumeStep(ctx, state, &schedulerResumeClaim{
		claimID: claim.ClaimID, matterID: claim.MatterID, dispatchID: claim.DispatchID, epoch: claim.ClaimEpoch,
	}, &selection); err != nil {
		return empty, fmt.Errorf("wipd: prepare selected work target: %w", err)
	}
	roleID, roleCommandID, spawnErr := coordinator.spawnSchedulerResumeRole(ctx, state, claim, selection.role)
	if spawnErr != nil {
		return empty, fmt.Errorf("wipd: spawn selected %s Role: %w", selection.role, spawnErr)
	}
	return SchedulerResumeResponse{
		RunID: runID, MatterID: frontier, Claim: claim,
		RoleID: roleID, RoleCommandID: roleCommandID, RoleName: selection.role, TargetID: selection.targetID,
		TargetKind: selection.targetKind, WorkCausationID: selection.causationID,
	}, nil
}

func (coordinator *CommandStartCoordinator) schedulerResumeState(ctx context.Context, runID string) (schedulerResumeState, CommandStartSnapshot, error) {
	var state schedulerResumeState
	state.runID = runID
	snapshot, err := coordinator.environment.Snapshot(ctx)
	if err != nil || snapshot.DomainID != coordinator.domainID || !validCommandStartAnchor(snapshot.Anchor) {
		return state, CommandStartSnapshot{}, errors.Join(ErrCommandStartIdentity, err)
	}
	installed, err := coordinator.journal.InstallSnapshot(ctx)
	if err != nil || !sameCommandStartAnchor(installed.Anchor, snapshot.Anchor) || installed.ManifestDigest != snapshot.ManifestDigest {
		return state, CommandStartSnapshot{}, errors.Join(ErrCommandStartIdentity, err)
	}
	records, err := coordinator.journal.EventRecords(ctx)
	if err != nil || uint64(len(records)) != snapshot.Anchor.EventCount {
		return state, CommandStartSnapshot{}, errors.Join(ErrCommandStartIdentity, err)
	}
	identity := coordinator.journal.Identity()
	state.activeRoles = make(map[string]schedulerResumeRole)
	state.claims = make(map[string]*schedulerResumeClaim)
	state.statuses = map[string]string{}
	state.planCommandByMatter = map[string]string{}
	state.planStepIDs = map[string][]string{}
	state.matterGates = map[string]bool{}
	state.closedGates = map[string]bool{}
	state.correctionMatters = map[string]bool{}
	state.needsVerifier = map[string]bool{}
	state.planCompleted = map[string]bool{}
	state.skippedMatters = map[string]bool{}
	state.failedBuilderPolicyPending = map[string]bool{}
	state.correctionVerifierCommand = map[string]string{}
	state.correctionStepByMatter = map[string]string{}
	state.correctionStepByVerifier = map[string]string{}
	state.correctionProblem = map[string]string{}
	state.verifierCommandByMatter = map[string]string{}
	for position, record := range records {
		fields, decodeErr := wipdwire.DecodeCanonicalMap(record.Record,
			"schema", "event_id", "domain_id", "command_id", "request_hash", "environment", "acted_at", "occurred_at", "kind", "subject_id", "repo_id", "payload")
		if decodeErr != nil || fields["schema"] != "wipd.event/1" || fields["domain_id"] != identity.DomainID || fields["event_id"] != record.EventID {
			return state, CommandStartSnapshot{}, ErrCommandStartIdentity
		}
		kind, _ := fields["kind"].(string)
		subject, _ := fields["subject_id"].(string)
		commandID, _ := fields["command_id"].(string)
		payload, payloadOK := fields["payload"].(map[string]any)
		if !payloadOK {
			return state, CommandStartSnapshot{}, ErrCommandStartIdentity
		}
		environment, _ := fields["environment"].(map[string]any)
		environmentID, _ := environment["id"].(string)
		environmentSequence, _ := environment["sequence"].(uint64)
		eventPosition := uint64(position + 1)
		switch kind {
		case "run.started":
			if subject != runID || state.startCommandID != "" || !wipdwire.ExactMapKeys(payload, "batch", "locator", "matters") {
				continue
			}
			state.batchID, _ = payload["batch"].(string)
			state.locator, _ = payload["locator"].(string)
			state.matters, err = schedulerResumeStrings(payload["matters"])
			entry, entryErr := coordinator.journal.Get(commandID)
			if err != nil || entryErr != nil || state.batchID == "" || state.locator == "" {
				return state, CommandStartSnapshot{}, ErrCommandStartIdentity
			}
			input, inputOK := entry.Command.Request.Input.(operation.RoleLifecycleInput)
			if !inputOK || entry.Command.Request.Operation != operation.RoleSpawnV1.Metadata().Operation ||
				input.RunID != runID || input.RunLocator != state.locator || entry.Command.EnvironmentID != identity.EnvironmentID ||
				entry.Command.Request.Context.Repo != identity.RepoID || entry.Command.Request.Context.Clone == "" ||
				entry.Command.Request.Context.Worktree == "" || entry.Command.Request.Claim == nil {
				return state, CommandStartSnapshot{}, ErrCommandStartIdentity
			}
			state.owner, state.repoID = entry.Command.EnvironmentID, entry.Command.Request.Context.Repo
			state.cloneID, state.worktreeID = entry.Command.Request.Context.Clone, entry.Command.Request.Context.Worktree
			state.startCommandID = commandID
			state.startEnvironmentSequence = entry.EnvironmentSeq
			if environmentID != identity.EnvironmentID || environmentSequence != entry.EnvironmentSeq {
				return state, CommandStartSnapshot{}, ErrCommandStartIdentity
			}
		case "run.resumed":
			if subject == runID {
				state.lastResumePosition, state.lastResumeSequence = eventPosition, environmentSequence
			}
		case "run.finished":
			if subject == runID {
				state.runFinished = true
			}
		case "run.stood-down":
			if subject == runID {
				return state, CommandStartSnapshot{}, ErrCommandStartBlocked
			}
		case "claim.acquired":
			if !wipdwire.ExactMapKeys(payload, "claim_id", "claim_epoch", "matter_id", "batch_id", "dispatch_id", "owner_environment_id", "worktree_id") {
				continue
			}
			batchID, _ := payload["batch_id"].(string)
			claimID, _ := payload["claim_id"].(string)
			epoch, epochOK := payload["claim_epoch"].(uint64)
			matterID, _ := payload["matter_id"].(string)
			dispatchID, _ := payload["dispatch_id"].(string)
			worktreeID, _ := payload["worktree_id"].(string)
			owner, _ := payload["owner_environment_id"].(string)
			if !epochOK || claimID != subject || epoch == 0 || !commandStartULID.MatchString(matterID) ||
				!commandStartULID.MatchString(batchID) || !commandStartULID.MatchString(dispatchID) ||
				!commandStartULID.MatchString(owner) || !commandStartULID.MatchString(worktreeID) {
				return state, CommandStartSnapshot{}, ErrCommandStartIdentity
			}
			state.claims[claimID] = &schedulerResumeClaim{
				claimID: claimID, matterID: matterID, batchID: batchID,
				dispatchID: dispatchID, worktreeID: worktreeID, owner: owner, epoch: epoch,
				environmentSequence: environmentSequence, eventPosition: eventPosition, commandID: commandID,
			}
		case "claim.released":
			claimID, _ := payload["claim_id"].(string)
			claim := state.claims[claimID]
			if claim == nil {
				continue
			}
			if payloadEpoch, ok := payload["claim_epoch"].(uint64); !ok || payloadEpoch != claim.epoch || subject != claimID {
				return state, CommandStartSnapshot{}, ErrCommandStartIdentity
			}
			claim.released = true
			claim.releaseEventPosition = eventPosition
			claim.releaseEnvironmentSequence = environmentSequence
		case "role.spawned":
			if !wipdwire.ExactMapKeys(payload, "run_id", "matter_id", "dispatch_id", "claim_id", "claim_epoch", "name") {
				continue
			}
			roleRun, _ := payload["run_id"].(string)
			if roleRun != runID {
				continue
			}
			role := schedulerResumeRole{roleID: subject, commandID: commandID}
			role.matterID, _ = payload["matter_id"].(string)
			role.dispatchID, _ = payload["dispatch_id"].(string)
			role.claimID, _ = payload["claim_id"].(string)
			role.claimEpoch, _ = payload["claim_epoch"].(string)
			role.name, _ = payload["name"].(string)
			claim := state.claims[role.claimID]
			if claim == nil || claim.batchID != state.batchID || claim.released || claim.matterID != role.matterID || claim.dispatchID != role.dispatchID ||
				fmt.Sprint(claim.epoch) != role.claimEpoch || environmentID != identity.EnvironmentID {
				return state, CommandStartSnapshot{}, ErrCommandStartIdentity
			}
			state.activeRoles[role.roleID] = role
		case "role.closed":
			if payloadRun, _ := payload["run_id"].(string); payloadRun == runID {
				delete(state.activeRoles, subject)
			}
		case "matter.created", "stage.created", "step.created", "step.inserted":
			state.statuses[subject] = "planned"
		case "matter.started", "stage.started", "step.started":
			state.statuses[subject] = "in-progress"
		case "matter.paused", "stage.paused", "step.paused":
			state.statuses[subject] = "paused"
		case "matter.canceled", "stage.canceled", "step.canceled", "step.removed":
			state.statuses[subject] = "canceled"
		case "matter.finished", "stage.finished", "step.finished":
			state.statuses[subject] = "done"
		case "gate.declared":
			gate, _ := payload["gate"].(string)
			scale, _ := payload["scale"].(string)
			if gate != "" && scale == "matter" {
				state.matterGates[gate] = true
			}
		case "gate.closed":
			gate, _ := payload["gate"].(string)
			if gate != "" {
				state.closedGates[subject+"\x00"+gate] = true
			}
		}
	}
	// Claims may be acquired before run.started identifies the Run's batch.
	// Retain them during replay so a following Role can be bound, then project
	// only this Run's batch after the complete journal prefix is available.
	for claimID, claim := range state.claims {
		if claim.batchID != state.batchID {
			delete(state.claims, claimID)
			continue
		}
		if !schedulerResumeContains(state.matters, claim.matterID) || claim.owner != identity.EnvironmentID || claim.worktreeID != state.worktreeID {
			return state, CommandStartSnapshot{}, ErrCommandStartIdentity
		}
		if claim.releaseEventPosition > state.lastReleasePosition {
			state.lastReleasePosition = claim.releaseEventPosition
			state.lastReleaseSequence = claim.releaseEnvironmentSequence
		}
	}
	if state.startCommandID == "" || state.owner == "" || state.batchID == "" || state.locator == "" || len(state.matters) == 0 ||
		!sameCommandStartAnchor(installed.Anchor, snapshot.Anchor) {
		return state, CommandStartSnapshot{}, ErrCommandStartIdentity
	}
	releases, err := coordinator.journal.ClaimJournalReleaseAttempts()
	if err != nil {
		return state, CommandStartSnapshot{}, err
	}
	for _, release := range releases {
		claim := state.claims[release.Binding.ClaimID]
		if claim != nil && release.Returned && release.ResultCode == operation.ResultSucceeded {
			claim.released = true
		}
	}
	entries, err := coordinator.journal.Entries()
	if err != nil {
		return state, CommandStartSnapshot{}, err
	}
	type hookResult struct {
		entry  wipdjournal.Entry
		input  operation.SchedulerHookInput
		output operation.SchedulerHookOutput
	}
	hooks := make([]hookResult, 0)
	planMatterByCommand := make(map[string]string)
	negativeMatterByCommand := make(map[string]string)
	latestNegativeCommand := make(map[string]string)
	for _, entry := range entries {
		input, ok := entry.Command.Request.Input.(operation.SchedulerHookInput)
		if !ok || entry.Command.Request.Operation != operation.SchedulerOrchestrateV1.Metadata().Operation || input.RunID != runID {
			continue
		}
		receipt, installedReceipt := snapshot.Receipts[entry.Command.ID]
		if !installedReceipt || receipt.ResultCode != operation.ResultSucceeded {
			continue
		}
		result, resultErr := commandReceiptResult(entry, receipt.CanonicalReceipt)
		output, outputOK := result.Output.(operation.SchedulerHookOutput)
		if resultErr != nil || !outputOK || output.Kind != input.Kind {
			return state, CommandStartSnapshot{}, ErrCommandStartIdentity
		}
		hooks = append(hooks, hookResult{entry: entry, input: input, output: output})
		if input.Kind == "plan" {
			planMatterByCommand[entry.Command.ID] = input.MatterID
		}
		if input.Kind == "work" && input.Role == "verifier" && output.Outcome == "not-verified" {
			negativeMatterByCommand[entry.Command.ID] = input.MatterID
			latestNegativeCommand[input.MatterID] = entry.Command.ID
		}
	}
	for _, entry := range entries {
		if entry.Command.Request.Operation != operation.StepCreateV2.Metadata().Operation {
			continue
		}
		matterID, planned := planMatterByCommand[entry.Command.CausationCommandID]
		negativeMatter, correction := negativeMatterByCommand[entry.Command.CausationCommandID]
		if !planned && !correction {
			continue
		}
		input, ok := entry.Command.Request.Input.(operation.StepCreateInput)
		if !ok || entry.Command.Request.Actor != "role:scheduler" || entry.Command.Request.Context !=
			(operation.Context{Repo: state.repoID, Clone: state.cloneID, Worktree: state.worktreeID}) {
			return state, CommandStartSnapshot{}, ErrCommandStartIdentity
		}
		expectedMatter := negativeMatter
		expectedTitle := "Correct verification findings"
		if planned {
			expectedMatter = matterID
			expectedTitle = input.Title
			if input.ParentID != matterID || entry.Command.CorrelationCommandID != entry.Command.CausationCommandID || entry.Command.Request.Claim == nil {
				return state, CommandStartSnapshot{}, ErrCommandStartIdentity
			}
		}
		if correction && (input.ParentID != negativeMatter || input.Title != expectedTitle ||
			entry.Command.CorrelationCommandID != entry.Command.CausationCommandID || entry.Command.Request.Claim == nil) {
			return state, CommandStartSnapshot{}, ErrCommandStartIdentity
		}
		receipt, installedReceipt := snapshot.Receipts[entry.Command.ID]
		if !installedReceipt || receipt.ResultCode != operation.ResultSucceeded {
			return state, CommandStartSnapshot{}, ErrCommandStartIdentity
		}
		result, resultErr := commandReceiptResult(entry, receipt.CanonicalReceipt)
		created, createdOK := result.Output.(operation.StepCreateOutput)
		if resultErr != nil || !createdOK || created.ParentID != expectedMatter || created.MatterID != expectedMatter ||
			created.Title != input.Title || created.State != "planned" || !commandStartULID.MatchString(created.ID) {
			return state, CommandStartSnapshot{}, ErrCommandStartIdentity
		}
		if planned {
			state.planStepIDs[expectedMatter] = append(state.planStepIDs[expectedMatter], created.ID)
		}
		if correction && latestNegativeCommand[negativeMatter] == entry.Command.CausationCommandID {
			if state.correctionStepByVerifier[entry.Command.CausationCommandID] != "" {
				return state, CommandStartSnapshot{}, ErrCommandStartIdentity
			}
			state.correctionStepByVerifier[entry.Command.CausationCommandID] = created.ID
		}
	}
	for _, hook := range hooks {
		switch {
		case hook.input.Kind == "ask":
			if !schedulerResumeContains(state.matters, hook.input.MatterID) ||
				hook.input.TargetID != hook.input.MatterID || hook.input.TargetKind != "matter" ||
				hook.input.Role != "" || hook.input.RoleID != "" || hook.input.DispatchID != "" ||
				hook.entry.Command.Request.Actor != "system:scheduler" || hook.entry.Command.Request.Claim != nil ||
				hook.input.Revision != "builtin.run-policy@v1" || len(hook.input.TargetClaims) != 0 ||
				hook.output.Outcome != "" || hook.output.Revision != "" || hook.output.EvidenceRefs != nil ||
				hook.output.ProblemCode != "" || hook.output.Steps != nil || !schedulerResumeAskMatchesPolicy(hook.input, hook.output.Handling) {
				return state, CommandStartSnapshot{}, ErrCommandStartIdentity
			}
			if hook.output.Handling == "halt" {
				state.policyHalt = true
			} else {
				state.skippedMatters[hook.input.MatterID] = true
			}
			if hook.input.Reason == "failed" {
				delete(state.failedBuilderPolicyPending, hook.input.MatterID)
			}
		case hook.input.Kind == "plan":
			state.planCommandByMatter[hook.input.MatterID] = hook.entry.Command.ID
			state.planCompleted[hook.input.MatterID] = hook.output.Outcome == "planned"
		case hook.input.Kind == "work" && hook.input.Role == "builder" && hook.output.Outcome == "failed":
			state.failedBuilderPolicyPending[hook.input.MatterID] = true
		case hook.input.Kind == "work" && hook.input.Role == "verifier" && hook.output.Outcome == "not-verified":
			state.correctionMatters[hook.input.MatterID] = true
			state.needsVerifier[hook.input.MatterID] = false
			state.correctionVerifierCommand[hook.input.MatterID] = hook.entry.Command.ID
			state.correctionStepByMatter[hook.input.MatterID] = state.correctionStepByVerifier[hook.entry.Command.ID]
			state.correctionProblem[hook.input.MatterID] = hook.output.ProblemCode
		case hook.input.Kind == "work" && hook.input.Role == "builder" && hook.output.Outcome == "completed" &&
			hook.entry.Command.CausationCommandID != "" &&
			negativeMatterByCommand[hook.entry.Command.CausationCommandID] == hook.input.MatterID &&
			hook.input.TargetID == state.correctionStepByVerifier[hook.entry.Command.CausationCommandID] &&
			state.correctionVerifierCommand[hook.input.MatterID] == hook.entry.Command.CausationCommandID:
			state.correctionMatters[hook.input.MatterID] = false
			state.needsVerifier[hook.input.MatterID] = true
			state.verifierCommandByMatter[hook.input.MatterID] = hook.entry.Command.ID
		case hook.input.Kind == "work" && hook.input.Role == "verifier" && hook.output.Outcome == "verified":
			state.correctionMatters[hook.input.MatterID] = false
			state.needsVerifier[hook.input.MatterID] = false
			state.verifierCommandByMatter[hook.input.MatterID] = hook.entry.Command.ID
		}
	}
	return state, cloneCommandStartSnapshot(snapshot), nil
}

func schedulerResumeAskMatchesPolicy(input operation.SchedulerHookInput, handling string) bool {
	if handling != "skip" && handling != "halt" {
		return false
	}
	switch input.Reason {
	case "contended":
		return input.Contention == handling
	case "blocked":
		return input.Blocked == handling
	case "failed":
		return input.Failed == handling
	default:
		return false
	}
}

func schedulerResumeMissingFailedBuilderPolicy(state schedulerResumeState) string {
	for _, matterID := range state.matters {
		if state.failedBuilderPolicyPending[matterID] {
			return matterID
		}
	}
	return ""
}

func schedulerRunIsActive(state schedulerResumeState, snapshot CommandStartSnapshot) bool {
	_ = snapshot
	return !state.runFinished
}

func schedulerResumeCurrentClaim(state schedulerResumeState) *schedulerResumeClaim {
	if state.lastResumePosition == 0 {
		return nil
	}
	var current *schedulerResumeClaim
	for _, claim := range state.claims {
		if claim.released || claim.eventPosition <= state.lastResumePosition {
			continue
		}
		if current != nil {
			return nil
		}
		current = claim
	}
	return current
}

func schedulerResumeCurrentClaimCount(state schedulerResumeState) int {
	if state.lastResumePosition == 0 {
		return 0
	}
	count := 0
	for _, claim := range state.claims {
		if !claim.released && claim.eventPosition > state.lastResumePosition {
			count++
		}
	}
	return count
}

func schedulerResumeHasOpenClaim(state schedulerResumeState) bool {
	for _, claim := range state.claims {
		if !claim.released {
			return true
		}
	}
	return false
}

func (coordinator *CommandStartCoordinator) closeSchedulerResumeRoles(ctx context.Context, state schedulerResumeState, snapshot CommandStartSnapshot) error {
	roles := make([]schedulerResumeRole, 0, len(state.activeRoles))
	for _, role := range state.activeRoles {
		roles = append(roles, role)
	}
	sort.Slice(roles, func(i, j int) bool { return roles[i].roleID < roles[j].roleID })
	entries, err := coordinator.journal.Entries()
	if err != nil {
		return err
	}
	for _, role := range roles {
		claim := state.claims[role.claimID]
		if claim == nil || claim.released {
			return ErrCommandStartIdentity
		}
		var prior *wipdjournal.Entry
		for index := range entries {
			candidate := &entries[index]
			input, ok := candidate.Command.Request.Input.(operation.RoleLifecycleInput)
			if !ok || candidate.Command.Request.Operation != operation.RoleCloseV1.Metadata().Operation || input.RunID != state.runID || input.RoleID != role.roleID {
				continue
			}
			if prior != nil {
				return ErrCommandStartIdentity
			}
			prior = candidate
		}
		var result CommandStartResult
		if prior != nil {
			result, err = coordinator.runConnectedCanonicalTerminal(ctx, prior.Command, emptyConnectedGuard)
		} else {
			id, idErr := newFrameRequestID()
			if idErr != nil {
				return idErr
			}
			result, err = coordinator.RunConnectedTerminal(ctx, wipdjournal.CommandInput{
				ID: id, Request: operation.Request{
					Operation: operation.RoleCloseV1.Metadata().Operation,
					Actor:     "system:scheduler",
					Context:   operation.Context{Repo: state.repoID, Clone: state.cloneID, Worktree: state.worktreeID},
					Claim:     &operation.ClaimContext{ID: claim.claimID, Epoch: fmt.Sprint(claim.epoch)},
					Input: operation.RoleLifecycleInput{
						RunID: state.runID, RunLocator: state.locator,
						MatterID: role.matterID, DispatchID: role.dispatchID, RoleID: role.roleID, Role: role.name, Reason: "reaped",
					},
				},
			}, emptyConnectedGuard)
		}
		if err != nil {
			return fmt.Errorf("wipd: close old Role %s before claim release: %w", role.roleID, err)
		}
		closed, ok := result.SemanticResult.Output.(operation.RoleLifecycleOutput)
		if !result.Returned || result.ResultCode != operation.ResultSucceeded || result.SemanticResult.Code != operation.ResultSucceeded ||
			!ok || closed.RunID != state.runID || closed.RoleID != role.roleID || closed.State != "closed" {
			return ErrCommandStartIdentity
		}
	}
	_ = snapshot
	return nil
}

func (coordinator *CommandStartCoordinator) releaseSchedulerResumeClaims(ctx context.Context, state schedulerResumeState) error {
	claims := make([]*schedulerResumeClaim, 0, len(state.claims))
	for _, claim := range state.claims {
		if !claim.released {
			claims = append(claims, claim)
		}
	}
	sort.Slice(claims, func(i, j int) bool { return claims[i].eventPosition < claims[j].eventPosition })
	releases, err := coordinator.journal.ClaimJournalReleaseAttempts()
	if err != nil {
		return err
	}
	for _, claim := range claims {
		grant, _, grantErr := coordinator.journal.InstalledClaimGrantForClaim(claim.claimID)
		if grantErr != nil || grant.ClaimEpoch != claim.epoch || grant.MatterID != claim.matterID || grant.DispatchID != claim.dispatchID {
			return ErrCommandStartIdentity
		}
		closer, ok := coordinator.authority.(ClaimJournalCloseAuthority)
		if !ok || !closer.SupportsClaimJournalClose() {
			return errors.New("wipd: owner Resume requires authenticated claim-journal closure support")
		}
		var releaseID string
		for _, attempt := range releases {
			if attempt.Binding.ClaimID == claim.claimID {
				if releaseID != "" {
					return ErrCommandStartIdentity
				}
				releaseID = attempt.ID
				if attempt.Binding.ClaimEpoch != claim.epoch || attempt.Binding.MatterID != claim.matterID ||
					attempt.Binding.DispatchID != claim.dispatchID || attempt.RequestHash == "" {
					return ErrCommandStartIdentity
				}
			}
		}
		if releaseID == "" {
			if _, err = closer.CurrentClaimJournal(ctx, grant); err != nil {
				return err
			}
			releaseID, err = newFrameRequestID()
			if err != nil {
				return err
			}
		}
		result, err := coordinator.ReleaseClaimJournal(ctx, releaseID, claim.claimID, claim.epoch,
			claim.matterID, claim.dispatchID, operation.Actor("human"))
		if err != nil {
			return fmt.Errorf("wipd: normal receipt-safe release for Run claim %s: %w", claim.claimID, err)
		}
		if result.Code != operation.ResultSucceeded || result.Attempt.ID != releaseID || len(result.Receipt) == 0 {
			return fmt.Errorf("%w: normal receipt-safe release for Run claim %s returned %s", ErrCommandStartBlocked, claim.claimID, result.Code)
		}
	}
	return nil
}

func (coordinator *CommandStartCoordinator) recordSchedulerResume(ctx context.Context, state schedulerResumeState, snapshot CommandStartSnapshot) error {
	if state.lastResumePosition != 0 && state.lastReleasePosition <= state.lastResumePosition {
		// Recovery after run.resumed but before fresh acquisition reuses that
		// transition and proceeds directly to frontier selection.
		return nil
	}
	entries, err := coordinator.journal.Entries()
	if err != nil {
		return err
	}
	var pending *wipdjournal.Entry
	for index := range entries {
		entry := &entries[index]
		input, ok := entry.Command.Request.Input.(operation.RunResumeInput)
		if !ok || entry.Command.Request.Operation != operation.SchedulerResumeV1.Metadata().Operation || input.RunID != state.runID ||
			entry.Command.Request.Context != (operation.Context{Repo: state.repoID, Clone: state.cloneID, Worktree: state.worktreeID}) ||
			entry.EnvironmentSeq <= state.lastReleaseSequence {
			continue
		}
		if receipt, installed := snapshot.Receipts[entry.Command.ID]; installed {
			if receipt.RequestHash != entry.RequestHash || receipt.EnvironmentSeq != entry.EnvironmentSeq ||
				receipt.JournalPosition != entry.JournalPosition || receipt.ResultCode != operation.ResultSucceeded {
				return ErrCommandStartIdentity
			}
			continue
		}
		if pending != nil {
			return ErrCommandStartIdentity
		}
		pending = entry
	}
	var result CommandStartResult
	if pending != nil {
		result, err = coordinator.runConnectedCanonicalTerminal(ctx, pending.Command, emptyConnectedGuard)
	} else {
		id, idErr := newFrameRequestID()
		if idErr != nil {
			return idErr
		}
		result, err = coordinator.RunConnectedTerminal(ctx, wipdjournal.CommandInput{
			ID: id, Request: operation.Request{
				Operation: operation.SchedulerResumeV1.Metadata().Operation,
				Actor:     "system:scheduler",
				Context:   operation.Context{Repo: state.repoID, Clone: state.cloneID, Worktree: state.worktreeID},
				Input:     operation.RunResumeInput{RunID: state.runID, RunLocator: state.locator},
			},
		}, emptyConnectedGuard)
	}
	if err != nil {
		return fmt.Errorf("wipd: record owner-Environment scheduler Resume: %w", err)
	}
	resumed, ok := result.SemanticResult.Output.(operation.RunResumeOutput)
	if !result.Returned || result.ResultCode != operation.ResultSucceeded || result.SemanticResult.Code != operation.ResultSucceeded ||
		!ok || resumed.RunID != state.runID || resumed.State != "resumed" {
		return ErrCommandStartIdentity
	}
	return nil
}

func (coordinator *CommandStartCoordinator) resolveSchedulerResumeAcquisitions(ctx context.Context, state schedulerResumeState) error {
	attempts, err := coordinator.journal.ClaimAcquireAttempts()
	if err != nil {
		return err
	}
	for _, attempt := range attempts {
		if attempt.EnvironmentSeq <= state.lastResumeSequence || attempt.EnvironmentSeq <= state.startEnvironmentSequence ||
			attempt.Actor != "system:scheduler" || attempt.CloneID != state.cloneID || attempt.WorktreeID != state.worktreeID ||
			!schedulerResumeContains(state.matters, attempt.MatterID) {
			continue
		}
		result, acquireErr := coordinator.AcquireClaim(ctx, attempt.ID, attempt.MatterID, attempt.CloneID,
			attempt.WorktreeID, attempt.DispatchID, operation.Actor(attempt.Actor))
		if acquireErr != nil {
			return fmt.Errorf("wipd: recover original scheduler claim-acquire %s: %w", attempt.ID, acquireErr)
		}
		if result.Code != operation.ResultSucceeded || result.Grant == nil || result.Grant.MatterID != attempt.MatterID ||
			result.Grant.DispatchID != attempt.DispatchID || result.Grant.BatchID != state.batchID {
			return fmt.Errorf("%w: retained scheduler claim-acquire %s did not produce the original Run grant", ErrCommandStartBlocked, attempt.ID)
		}
	}
	return nil
}

func (coordinator *CommandStartCoordinator) recoverSchedulerMatterStarts(ctx context.Context, state schedulerResumeState) error {
	entries, err := coordinator.journal.Entries()
	if err != nil {
		return err
	}
	claims := make(map[string]schedulerResumeClaim, len(state.claims))
	for id, claim := range state.claims {
		claims[id] = *claim
	}
	for _, entry := range entries {
		if entry.Command.Request.Operation != operation.MatterStartV1.Metadata().Operation || entry.Command.Request.Claim == nil {
			continue
		}
		claim, ok := claims[entry.Command.Request.Claim.ID]
		input, inputOK := entry.Command.Request.Input.(operation.NodeLifecycleInput)
		if !ok || !inputOK || input.NodeID != claim.matterID || entry.Command.Request.Claim.Epoch != fmt.Sprint(claim.epoch) ||
			entry.Command.Request.Context != (operation.Context{Repo: state.repoID, Clone: state.cloneID, Worktree: state.worktreeID}) ||
			entry.Command.Request.Actor != "system:scheduler" {
			continue
		}
		if _, err = coordinator.RunConnectedCanonicalTerminal(ctx, entry.Command, emptyConnectedGuard); err != nil {
			return fmt.Errorf("wipd: recover Run Matter-start command %s before frontier selection: %w", entry.Command.ID, err)
		}
	}
	return nil
}

func (coordinator *CommandStartCoordinator) startSchedulerResumeMatter(ctx context.Context, state schedulerResumeState, claim *schedulerResumeClaim) error {
	if claim == nil || !schedulerResumeContains(state.matters, claim.matterID) || claim.claimID == "" || claim.epoch == 0 {
		return ErrCommandStartIdentity
	}
	status := state.statuses[claim.matterID]
	if status == "in-progress" {
		return nil
	}
	if status != "planned" {
		return fmt.Errorf("%w: selected Matter %s has non-startable state %q", ErrCommandStartBlocked, claim.matterID, status)
	}
	id, err := newFrameRequestID()
	if err != nil {
		return err
	}
	result, err := coordinator.RunConnectedTerminal(ctx, wipdjournal.CommandInput{
		ID: id,
		Request: operation.Request{
			Operation: operation.MatterStartV1.Metadata().Operation,
			Actor:     "system:scheduler",
			Context:   operation.Context{Repo: state.repoID, Clone: state.cloneID, Worktree: state.worktreeID},
			Claim:     &operation.ClaimContext{ID: claim.claimID, Epoch: fmt.Sprint(claim.epoch)},
			Input:     operation.NodeLifecycleInput{NodeID: claim.matterID},
		},
	}, emptyConnectedGuard)
	if err != nil {
		return fmt.Errorf("wipd: start selected Matter before Role activation: %w", err)
	}
	started, ok := result.SemanticResult.Output.(operation.MatterLifecycleOutput)
	if !result.Returned || result.ResultCode != operation.ResultSucceeded || result.SemanticResult.Code != operation.ResultSucceeded ||
		!ok || started.MatterID != claim.matterID || started.State != "in-progress" {
		return ErrCommandStartIdentity
	}
	return nil
}

func (coordinator *CommandStartCoordinator) finishReadySchedulerMatters(ctx context.Context, state schedulerResumeState) error {
	for _, matterID := range state.matters {
		if state.statuses[matterID] == "done" || state.statuses[matterID] == "canceled" ||
			state.statuses[matterID] != "in-progress" || !state.planCompleted[matterID] ||
			state.correctionMatters[matterID] || state.needsVerifier[matterID] {
			continue
		}
		ready := true
		for _, stepID := range state.planStepIDs[matterID] {
			if state.statuses[stepID] != "done" && state.statuses[stepID] != "canceled" {
				ready = false
				break
			}
		}
		if stepID := state.correctionStepByMatter[matterID]; stepID != "" && state.statuses[stepID] != "done" {
			ready = false
		}
		if !ready {
			continue
		}
		for gate := range state.matterGates {
			if !state.closedGates[matterID+"\x00"+gate] {
				ready = false
				break
			}
		}
		if !ready {
			continue
		}
		var claim *schedulerResumeClaim
		for _, candidate := range state.claims {
			if candidate.matterID == matterID && !candidate.released {
				if claim != nil {
					return ErrCommandStartIdentity
				}
				claim = candidate
			}
		}
		if claim == nil {
			continue
		}
		causation := state.verifierCommandByMatter[matterID]
		if causation == "" {
			causation = state.planCommandByMatter[matterID]
		}
		if causation == "" {
			return ErrCommandStartIdentity
		}
		entries, err := coordinator.journal.Entries()
		if err != nil {
			return err
		}
		var prior *wipdjournal.Entry
		for index := range entries {
			entry := &entries[index]
			if entry.Command.Request.Operation != operation.MatterFinishV1.Metadata().Operation {
				continue
			}
			input, ok := entry.Command.Request.Input.(operation.MatterFinishInput)
			if !ok || input.MatterID != matterID {
				continue
			}
			if entry.Command.CausationCommandID != causation || entry.Command.CorrelationCommandID != causation ||
				entry.Command.Request.Actor != "system:scheduler" || entry.Command.Request.Claim == nil ||
				entry.Command.Request.Claim.ID != claim.claimID || entry.Command.Request.Claim.Epoch != fmt.Sprint(claim.epoch) || prior != nil {
				return ErrCommandStartIdentity
			}
			prior = entry
		}
		var result CommandStartResult
		if prior != nil {
			result, err = coordinator.runConnectedCanonicalTerminal(ctx, prior.Command, emptyConnectedGuard)
		} else {
			id, idErr := newFrameRequestID()
			if idErr != nil {
				return idErr
			}
			result, err = coordinator.RunConnectedTerminal(ctx, wipdjournal.CommandInput{
				ID: id, CausationCommandID: causation, CorrelationCommandID: causation,
				Request: operation.Request{
					Operation: operation.MatterFinishV1.Metadata().Operation, Actor: "system:scheduler",
					Context: operation.Context{Repo: state.repoID, Clone: state.cloneID, Worktree: state.worktreeID},
					Claim:   &operation.ClaimContext{ID: claim.claimID, Epoch: fmt.Sprint(claim.epoch)},
					Input:   operation.MatterFinishInput{MatterID: matterID},
				},
			}, emptyConnectedGuard)
		}
		if err != nil {
			return fmt.Errorf("wipd: finish verified Matter %s before claim release: %w", matterID, err)
		}
		finished, ok := result.SemanticResult.Output.(operation.MatterFinishOutput)
		if !result.Returned || result.ResultCode != operation.ResultSucceeded || result.SemanticResult.Code != operation.ResultSucceeded ||
			!ok || finished.MatterID != matterID || finished.State != "done" {
			return fmt.Errorf("%w: ready Matter %s did not finish before claim release", ErrCommandStartBlocked, matterID)
		}
	}
	return nil
}

func (coordinator *CommandStartCoordinator) finishSchedulerRun(ctx context.Context, state schedulerResumeState) error {
	entries, err := coordinator.journal.Entries()
	if err != nil {
		return err
	}
	var pending *wipdjournal.Entry
	for index := range entries {
		entry := &entries[index]
		input, ok := entry.Command.Request.Input.(operation.RunFinishInput)
		if !ok || entry.Command.Request.Operation != operation.RunFinishV1.Metadata().Operation || input.RunID != state.runID {
			continue
		}
		if pending != nil {
			return ErrCommandStartIdentity
		}
		if entry.Command.Request.Context != (operation.Context{Repo: state.repoID, Clone: state.cloneID, Worktree: state.worktreeID}) ||
			entry.Command.Request.Actor != "system:scheduler" {
			return ErrCommandStartIdentity
		}
		pending = entry
	}
	var result CommandStartResult
	if pending != nil {
		result, err = coordinator.runConnectedCanonicalTerminal(ctx, pending.Command, emptyConnectedGuard)
	} else {
		id, idErr := newFrameRequestID()
		if idErr != nil {
			return idErr
		}
		result, err = coordinator.RunConnectedTerminal(ctx, wipdjournal.CommandInput{ID: id, Request: operation.Request{
			Operation: operation.RunFinishV1.Metadata().Operation, Actor: "system:scheduler",
			Context: operation.Context{Repo: state.repoID, Clone: state.cloneID, Worktree: state.worktreeID},
			Input:   operation.RunFinishInput{RunID: state.runID},
		}}, emptyConnectedGuard)
	}
	if err != nil {
		return fmt.Errorf("wipd: finish fully closed scheduler Run: %w", err)
	}
	finished, ok := result.SemanticResult.Output.(operation.RunFinishOutput)
	if !result.Returned || result.ResultCode != operation.ResultSucceeded || result.SemanticResult.Code != operation.ResultSucceeded ||
		!ok || finished.RunID != state.runID || finished.State != "finished" {
		return ErrCommandStartIdentity
	}
	return nil
}

func (coordinator *CommandStartCoordinator) acquireSchedulerResumeClaim(ctx context.Context, state schedulerResumeState, matterID string) (wipdjournal.ClaimGrantSummary, error) {
	attempts, err := coordinator.journal.ClaimAcquireAttempts()
	if err != nil {
		return wipdjournal.ClaimGrantSummary{}, err
	}
	var attempt *wipdjournal.ClaimAcquireAttempt
	for index := range attempts {
		candidate := &attempts[index]
		if candidate.EnvironmentSeq <= state.lastResumeSequence || candidate.Actor != "system:scheduler" ||
			candidate.MatterID != matterID || candidate.CloneID != state.cloneID || candidate.WorktreeID != state.worktreeID {
			continue
		}
		if attempt != nil {
			return wipdjournal.ClaimGrantSummary{}, ErrCommandStartIdentity
		}
		attempt = candidate
	}
	if attempt != nil {
		result, acquireErr := coordinator.AcquireClaim(ctx, attempt.ID, matterID, state.cloneID,
			state.worktreeID, attempt.DispatchID, operation.Actor("system:scheduler"))
		if acquireErr != nil || result.Code != operation.ResultSucceeded || result.Grant == nil ||
			result.Grant.MatterID != matterID || result.Grant.DispatchID != attempt.DispatchID || result.Grant.BatchID != state.batchID {
			return wipdjournal.ClaimGrantSummary{}, errors.Join(ErrCommandStartIdentity, acquireErr)
		}
		return *result.Grant, nil
	}
	dispatchID, err := newFrameRequestID()
	if err != nil {
		return wipdjournal.ClaimGrantSummary{}, err
	}
	commandID, err := newFrameRequestID()
	if err != nil {
		return wipdjournal.ClaimGrantSummary{}, err
	}
	result, err := coordinator.AcquireClaim(ctx, commandID, matterID, state.cloneID, state.worktreeID,
		dispatchID, operation.Actor("system:scheduler"))
	if err != nil || result.Code != operation.ResultSucceeded || result.Grant == nil ||
		result.Grant.MatterID != matterID || result.Grant.DispatchID != dispatchID || result.Grant.BatchID != state.batchID {
		return wipdjournal.ClaimGrantSummary{}, errors.Join(ErrCommandStartIdentity, err)
	}
	return *result.Grant, nil
}

func (coordinator *CommandStartCoordinator) spawnSchedulerResumeRole(ctx context.Context, state schedulerResumeState,
	claim wipdjournal.ClaimGrantSummary, roleName string,
) (string, string, error) {
	roleID, err := newFrameRequestID()
	if err != nil {
		return "", "", err
	}
	commandID, err := newFrameRequestID()
	if err != nil {
		return "", "", err
	}
	input := operation.RoleLifecycleInput{
		RunID: state.runID, RunLocator: state.locator, MatterID: claim.MatterID,
		DispatchID: claim.DispatchID, RoleID: roleID, Role: roleName,
	}
	result, err := coordinator.RunConnectedTerminal(ctx, wipdjournal.CommandInput{
		ID: commandID,
		Request: operation.Request{
			Operation: operation.RoleSpawnV1.Metadata().Operation, Actor: "system:scheduler",
			Context: operation.Context{Repo: state.repoID, Clone: state.cloneID, Worktree: state.worktreeID},
			Claim:   &operation.ClaimContext{ID: claim.ClaimID, Epoch: fmt.Sprint(claim.ClaimEpoch)}, Input: input,
		},
	}, emptyConnectedGuard)
	if err != nil {
		return "", "", err
	}
	output, ok := result.SemanticResult.Output.(operation.RoleLifecycleOutput)
	if !result.Returned || result.ResultCode != operation.ResultSucceeded || result.SemanticResult.Code != operation.ResultSucceeded ||
		!ok || output.RunID != state.runID || output.RoleID != roleID || output.State != "open" {
		return "", "", ErrCommandStartIdentity
	}
	return roleID, commandID, nil
}

func schedulerResumeRoleForClaim(state schedulerResumeState, matterID, claimID string) *schedulerResumeRole {
	for _, role := range state.activeRoles {
		if role.matterID == matterID && role.claimID == claimID {
			roleCopy := role
			return &roleCopy
		}
	}
	return nil
}

func (coordinator *CommandStartCoordinator) schedulerResumeRoleWork(state schedulerResumeState,
	role schedulerResumeRole,
) (schedulerResumeSelection, error) {
	entries, err := coordinator.journal.Entries()
	if err != nil {
		return schedulerResumeSelection{}, err
	}
	var selection *schedulerResumeSelection
	for _, entry := range entries {
		input, ok := entry.Command.Request.Input.(operation.SchedulerHookInput)
		if !ok || entry.Command.Request.Operation != operation.SchedulerOrchestrateV1.Metadata().Operation ||
			input.RunID != state.runID || input.RoleID != role.roleID {
			continue
		}
		if selection != nil || input.MatterID != role.matterID || input.DispatchID != role.dispatchID ||
			entry.Command.Request.Claim == nil || entry.Command.Request.Claim.ID != role.claimID ||
			entry.Command.Request.Claim.Epoch != role.claimEpoch {
			return schedulerResumeSelection{}, ErrCommandStartIdentity
		}
		selection = &schedulerResumeSelection{
			role: role.name, targetID: input.TargetID, targetKind: input.TargetKind,
			causationID: entry.Command.CausationCommandID,
		}
	}
	if selection != nil {
		return *selection, nil
	}
	next := schedulerResumeNextWork(state, role.matterID)
	if next.role == "" && state.policyHalt {
		// Preserve the exact selected target for a parked live Role. The
		// response carries Blocked=policy-halt, so this is identity only and
		// does not authorize the client to continue the work.
		next = schedulerResumeWork(state, role.matterID)
	}
	if next.role == role.name {
		if next.targetID == "" {
			return schedulerResumeSelection{}, ErrCommandStartIdentity
		}
		return next, nil
	}
	return schedulerResumeSelection{}, nil
}

type schedulerResumeSelection struct {
	role, targetID, targetKind, causationID string
}

func schedulerResumeNextWork(state schedulerResumeState, matterID string) schedulerResumeSelection {
	if state.policyHalt || state.skippedMatters[matterID] {
		return schedulerResumeSelection{}
	}
	return schedulerResumeWork(state, matterID)
}

func schedulerResumeWork(state schedulerResumeState, matterID string) schedulerResumeSelection {
	if state.correctionMatters[matterID] {
		return schedulerResumeSelection{
			role: "builder", targetID: state.correctionStepByMatter[matterID],
			targetKind: "step", causationID: state.correctionVerifierCommand[matterID],
		}
	}
	if state.needsVerifier[matterID] {
		return schedulerResumeSelection{
			role: "verifier", targetID: matterID, targetKind: "matter",
			causationID: state.verifierCommandByMatter[matterID],
		}
	}
	if !state.planCompleted[matterID] {
		return schedulerResumeSelection{role: "researcher", targetID: matterID, targetKind: "matter"}
	}
	for _, stepID := range state.planStepIDs[matterID] {
		if state.statuses[stepID] == "done" || state.statuses[stepID] == "canceled" {
			continue
		}
		return schedulerResumeSelection{
			role: "builder", targetID: stepID, targetKind: "step",
			causationID: state.planCommandByMatter[matterID],
		}
	}
	if state.statuses[matterID] == "done" || state.closedGates[matterID+"\x00verified"] {
		return schedulerResumeSelection{}
	}
	return schedulerResumeSelection{
		role: "verifier", targetID: matterID, targetKind: "matter",
		causationID: state.planCommandByMatter[matterID],
	}
}

func (coordinator *CommandStartCoordinator) ensureSchedulerResumeStep(ctx context.Context, state schedulerResumeState,
	claim *schedulerResumeClaim, selection *schedulerResumeSelection,
) error {
	if claim == nil || selection == nil || claim.claimID == "" || claim.epoch == 0 {
		return ErrCommandStartIdentity
	}
	if selection.targetKind != "step" {
		return nil
	}
	if selection.targetID == "" {
		if selection.role != "builder" || !state.correctionMatters[claim.matterID] ||
			selection.causationID == "" || selection.causationID != state.correctionVerifierCommand[claim.matterID] {
			return ErrCommandStartIdentity
		}
		stepID, err := coordinator.createSchedulerCorrectionStep(ctx, state, claim, selection.causationID)
		if err != nil {
			return err
		}
		selection.targetID = stepID
		state.statuses[stepID] = "planned"
		state.correctionStepByMatter[claim.matterID] = stepID
	}
	if !commandStartULID.MatchString(selection.targetID) || selection.causationID == "" {
		return ErrCommandStartIdentity
	}
	status := state.statuses[selection.targetID]
	if status == "in-progress" {
		return nil
	}
	if status != "planned" {
		return fmt.Errorf("%w: selected Step %s has non-startable state %q", ErrCommandStartBlocked, selection.targetID, status)
	}
	entries, err := coordinator.journal.Entries()
	if err != nil {
		return err
	}
	var prior *wipdjournal.Entry
	for index := range entries {
		entry := &entries[index]
		if entry.Command.CausationCommandID != selection.causationID ||
			entry.Command.Request.Operation != operation.StepStartV1.Metadata().Operation {
			continue
		}
		input, ok := entry.Command.Request.Input.(operation.StepLifecycleInput)
		if !ok || input.StepID != selection.targetID || entry.Command.CorrelationCommandID != selection.causationID ||
			entry.Command.Request.Actor != "role:scheduler" || entry.Command.Request.Claim == nil ||
			entry.Command.Request.Claim.ID != claim.claimID || entry.Command.Request.Claim.Epoch != fmt.Sprint(claim.epoch) ||
			entry.Command.Request.Context != (operation.Context{Repo: state.repoID, Clone: state.cloneID, Worktree: state.worktreeID}) || prior != nil {
			return ErrCommandStartIdentity
		}
		prior = entry
	}
	var result CommandStartResult
	if prior != nil {
		result, err = coordinator.runConnectedCanonicalTerminal(ctx, prior.Command, emptyConnectedGuard)
	} else {
		id, idErr := newFrameRequestID()
		if idErr != nil {
			return idErr
		}
		result, err = coordinator.RunConnectedTerminal(ctx, wipdjournal.CommandInput{
			ID: id, CausationCommandID: selection.causationID, CorrelationCommandID: selection.causationID,
			Request: operation.Request{
				Operation: operation.StepStartV1.Metadata().Operation, Actor: "role:scheduler",
				Context: operation.Context{Repo: state.repoID, Clone: state.cloneID, Worktree: state.worktreeID},
				Claim:   &operation.ClaimContext{ID: claim.claimID, Epoch: fmt.Sprint(claim.epoch)},
				Input:   operation.StepLifecycleInput{StepID: selection.targetID},
			},
		}, emptyConnectedGuard)
	}
	if err != nil {
		return fmt.Errorf("wipd: start selected scheduler Step %s: %w", selection.targetID, err)
	}
	started, ok := result.SemanticResult.Output.(operation.StepLifecycleOutput)
	if !result.Returned || result.ResultCode != operation.ResultSucceeded || result.SemanticResult.Code != operation.ResultSucceeded ||
		!ok || started.StepID != selection.targetID || started.MatterID != claim.matterID || started.State != "in-progress" {
		return ErrCommandStartIdentity
	}
	return nil
}

func (coordinator *CommandStartCoordinator) createSchedulerCorrectionStep(ctx context.Context, state schedulerResumeState,
	claim *schedulerResumeClaim, verifierCommandID string,
) (string, error) {
	entries, err := coordinator.journal.Entries()
	if err != nil {
		return "", err
	}
	var prior *wipdjournal.Entry
	for index := range entries {
		entry := &entries[index]
		if entry.Command.CausationCommandID != verifierCommandID ||
			entry.Command.Request.Operation != operation.StepCreateV2.Metadata().Operation {
			continue
		}
		input, ok := entry.Command.Request.Input.(operation.StepCreateInput)
		if !ok || input.ParentID != claim.matterID || input.Title != "Correct verification findings" ||
			entry.Command.CorrelationCommandID != verifierCommandID || entry.Command.Request.Actor != "role:scheduler" ||
			entry.Command.Request.Claim == nil || entry.Command.Request.Claim.ID != claim.claimID ||
			entry.Command.Request.Claim.Epoch != fmt.Sprint(claim.epoch) ||
			entry.Command.Request.Context != (operation.Context{Repo: state.repoID, Clone: state.cloneID, Worktree: state.worktreeID}) || prior != nil {
			return "", ErrCommandStartIdentity
		}
		prior = entry
	}
	var result CommandStartResult
	if prior != nil {
		result, err = coordinator.runConnectedCanonicalTerminal(ctx, prior.Command, emptyConnectedGuard)
	} else {
		id, idErr := newFrameRequestID()
		if idErr != nil {
			return "", idErr
		}
		result, err = coordinator.RunConnectedTerminal(ctx, wipdjournal.CommandInput{
			ID: id, CausationCommandID: verifierCommandID, CorrelationCommandID: verifierCommandID,
			Request: operation.Request{
				Operation: operation.StepCreateV2.Metadata().Operation, Actor: "role:scheduler",
				Context: operation.Context{Repo: state.repoID, Clone: state.cloneID, Worktree: state.worktreeID},
				Claim:   &operation.ClaimContext{ID: claim.claimID, Epoch: fmt.Sprint(claim.epoch)},
				Input:   operation.StepCreateInput{ParentID: claim.matterID, Title: "Correct verification findings"},
			},
		}, emptyConnectedGuard)
	}
	if err != nil {
		return "", fmt.Errorf("wipd: create correction Step for negative Verifier %s: %w", verifierCommandID, err)
	}
	created, ok := result.SemanticResult.Output.(operation.StepCreateOutput)
	if !result.Returned || result.ResultCode != operation.ResultSucceeded || result.SemanticResult.Code != operation.ResultSucceeded ||
		!ok || !commandStartULID.MatchString(created.ID) || created.ParentID != claim.matterID || created.MatterID != claim.matterID ||
		created.Title != "Correct verification findings" || created.State != "planned" {
		return "", ErrCommandStartIdentity
	}
	return created.ID, nil
}

func schedulerResumeFrontier(state schedulerResumeState, snapshot CommandStartSnapshot) (string, string) {
	if state.policyHalt {
		return "", "policy-halt"
	}
	skipped := false
	for _, matterID := range state.matters {
		if state.skippedMatters[matterID] {
			skipped = true
			continue
		}
		status := state.statuses[matterID]
		if status == "done" || status == "canceled" || status == "paused" {
			continue
		}
		blocked := false
		if snapshot.Step8Projection != nil {
			for _, edge := range snapshot.Step8Projection.Dependencies {
				if !edge.Live || edge.BlockedID != matterID {
					continue
				}
				if snapshotStatus := state.statuses[edge.BlockerID]; snapshotStatus != "done" {
					blocked = true
					break
				}
			}
		}
		if !blocked {
			return matterID, "ready"
		}
	}
	for _, matterID := range state.matters {
		if state.skippedMatters[matterID] {
			continue
		}
		if state.statuses[matterID] != "done" && state.statuses[matterID] != "canceled" {
			return "", "dependency-blocked"
		}
	}
	for gate := range state.matterGates {
		for _, matterID := range state.matters {
			if state.skippedMatters[matterID] {
				continue
			}
			if !state.closedGates[matterID+"\x00"+gate] {
				return "", "gate-pending"
			}
		}
	}
	if skipped {
		return "", "policy-skipped"
	}
	return "", "complete"
}

func schedulerResumeStrings(value any) ([]string, error) {
	values, ok := value.([]any)
	if !ok || len(values) == 0 {
		return nil, ErrCommandStartIdentity
	}
	result := make([]string, len(values))
	for index, value := range values {
		text, ok := value.(string)
		if !ok || !commandStartULID.MatchString(text) || index > 0 && result[index-1] >= text {
			return nil, ErrCommandStartIdentity
		}
		result[index] = text
	}
	return result, nil
}

func schedulerResumeContains(values []string, expected string) bool {
	for _, value := range values {
		if value == expected {
			return true
		}
	}
	return false
}

var _ = errors.Is
