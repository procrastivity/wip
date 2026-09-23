package protocol

import (
	"bytes"
	"encoding/binary"
	"encoding/json"
	"io"
	"os"
	"reflect"
	"strings"
	"testing"
)

const step7VectorsPath = "../../docs/wipd/claim-lifecycle-vectors.json"

type step7Fixture struct {
	Notation               string                `json:"notation"`
	Schemas                step7Schemas          `json:"schemas"`
	Identity               step7Identity         `json:"identity"`
	LifecycleSuccess       json.RawMessage       `json:"lifecycle_success_contract"`
	AcquireGrant           step7AcquireGrant     `json:"acquire_grant"`
	AcquireRefusals        []step7AcquireRefusal `json:"acquire_refusals"`
	Hydration              step7Hydration        `json:"hydration"`
	ClaimCommand           step7ClaimCommand     `json:"claim_command"`
	ExactClaimGuards       []step7ClaimGuard     `json:"exact_claim_guards"`
	ReturnRefusalAndRepair step7ReturnRepair     `json:"return_refusal_and_repair"`
	Release                step7Release          `json:"release"`
	StandDown              step7StandDown        `json:"stand_down"`
	HandoffPromotion       step7HandoffPromotion `json:"handoff_promotion"`
	Cancellation           []step7Cancellation   `json:"cancellation"`
}

type step7Schemas struct {
	Identity      string `json:"identity"`
	Receipt       string `json:"receipt"`
	Acquire       string `json:"acquire"`
	GrantStart    string `json:"grant_start"`
	GrantEnd      string `json:"grant_end"`
	Readiness     string `json:"readiness"`
	ReturnCommand string `json:"return_command"`
	FoldResult    string `json:"fold_result"`
	Release       string `json:"release"`
	Barrier       string `json:"barrier"`
	StandDown     string `json:"stand_down"`
}

type step7Identity struct {
	DomainID            string `json:"domain_id"`
	AuthorityEpoch      uint64 `json:"authority_epoch"`
	OwnerEnvironmentID  string `json:"owner_environment_id"`
	ActingEnvironmentID string `json:"acting_environment_id"`
	RepoID              string `json:"repo_id"`
	WorktreeID          string `json:"worktree_id"`
	MatterID            string `json:"matter_id"`
	BatchID             string `json:"batch_id"`
	DispatchID          string `json:"dispatch_id"`
	ClaimID             string `json:"claim_id"`
	ClaimEpoch          uint64 `json:"claim_epoch"`
	JournalID           string `json:"journal_id"`
}

type step7AcquireGrant struct {
	Name                 string       `json:"name"`
	CommandID            string       `json:"command_id"`
	RequestHash          string       `json:"request_hash"`
	Operation            string       `json:"operation"`
	ClaimField           any          `json:"claim_field"`
	InstalledAnchor      string       `json:"installed_anchor"`
	AsOfAnchor           string       `json:"as_of_anchor"`
	GrantID              string       `json:"grant_id"`
	AnonymousBatchAction string       `json:"anonymous_batch_action"`
	DispatchMode         string       `json:"dispatch_mode"`
	MessageOrder         []string     `json:"message_order"`
	Receipt              step7Receipt `json:"receipt"`
	ManifestDigest       string       `json:"manifest_digest"`
	PinBeforeUseDigests  []string     `json:"pin_before_use_digests"`
	AtomicLocalInstall   []string     `json:"atomic_local_install"`
	ExpectedState        string       `json:"expected_state"`
}

type step7Receipt struct {
	Schema       string `json:"schema"`
	CommandID    string `json:"command_id"`
	RequestHash  string `json:"request_hash"`
	Operation    string `json:"operation"`
	Result       string `json:"result"`
	FirstEventID string `json:"first_event_id"`
	LastEventID  string `json:"last_event_id"`
	EventCount   uint64 `json:"event_count"`
}

type step7AcquireRefusal struct {
	Name               string `json:"name"`
	PresentedLocalLock bool   `json:"presented_local_lock"`
	Footprint          string `json:"footprint"`
	Result             string `json:"result"`
	ProblemCode        string `json:"problem_code"`
	TerminalReceipt    bool   `json:"terminal_receipt"`
	Effects            string `json:"effects"`
}

type step7Hydration struct {
	Name                       string                     `json:"name"`
	GrantID                    string                     `json:"grant_id"`
	AsOfAnchor                 string                     `json:"as_of_anchor"`
	ManifestDigest             string                     `json:"manifest_digest"`
	RequiredEntryCount         uint64                     `json:"required_entry_count"`
	Transitions                []step7HydrationTransition `json:"transitions"`
	CancelDuringHydrationState string                     `json:"cancel_during_hydration_state"`
}

type step7HydrationTransition struct {
	Evidence                 string `json:"evidence"`
	VerifiedPinnedEntryCount uint64 `json:"verified_pinned_entry_count"`
	State                    string `json:"state"`
	ClaimCommandAdmitted     bool   `json:"claim_command_admitted"`
}

type step7ClaimCommand struct {
	Name                string             `json:"name"`
	CommandID           string             `json:"command_id"`
	RequestHash         string             `json:"request_hash"`
	Operation           string             `json:"operation"`
	ClaimID             string             `json:"claim_id"`
	ClaimEpoch          uint64             `json:"claim_epoch"`
	EnvironmentSequence uint64             `json:"environment_sequence"`
	JournalPosition     uint64             `json:"journal_position"`
	LocalAcceptance     []string           `json:"local_acceptance"`
	Return              step7ReturnCommand `json:"return"`
	Fold                step7FoldResult    `json:"fold"`
}

type step7ReturnCommand struct {
	Schema         string `json:"schema"`
	JournalID      string `json:"journal_id"`
	Delivery       string `json:"delivery"`
	Position       uint64 `json:"position"`
	BaseAsOfAnchor string `json:"base_as_of_anchor"`
}

type step7FoldResult struct {
	Schema            string       `json:"schema"`
	Receipt           step7Receipt `json:"receipt"`
	PrefixStartAnchor string       `json:"prefix_start_anchor"`
	PrefixEnd         step6Anchor  `json:"prefix_end"`
	ManifestDigest    string       `json:"manifest_digest"`
	Continue          bool         `json:"continue"`
	Install           string       `json:"install"`
}

type step7ClaimGuard struct {
	Name                string `json:"name"`
	ClaimID             string `json:"claim_id"`
	ClaimEpoch          uint64 `json:"claim_epoch"`
	AuthoritySubmission bool   `json:"authority_submission"`
	TerminalReceipt     bool   `json:"terminal_receipt"`
	ProblemCode         string `json:"problem_code"`
}

type step7ReturnRepair struct {
	Name                 string               `json:"name"`
	JournalID            string               `json:"journal_id"`
	HeadPosition         uint64               `json:"head_position"`
	HeadResult           string               `json:"head_result"`
	FoldContinue         bool                 `json:"fold_continue"`
	QuarantinedPositions []uint64             `json:"quarantined_positions"`
	Blocked              []string             `json:"blocked"`
	Controls             []step7RepairControl `json:"controls"`
	Forbidden            []string             `json:"forbidden"`
}

type step7RepairControl struct {
	Action        string `json:"action"`
	SafeProof     string `json:"safe_proof"`
	OldGeneration string `json:"old_generation"`
	NewGeneration string `json:"new_generation"`
}

type step7Release struct {
	Name                     string                `json:"name"`
	CommandID                string                `json:"command_id"`
	RequestHash              string                `json:"request_hash"`
	ClaimID                  string                `json:"claim_id"`
	ClaimEpoch               uint64                `json:"claim_epoch"`
	Attempts                 []step7ReleaseAttempt `json:"attempts"`
	PostCloseOldEpochProblem string                `json:"post_close_old_epoch_problem"`
}

type step7ReleaseAttempt struct {
	Name                 string  `json:"name"`
	EntryCount           uint64  `json:"entry_count"`
	TerminalReceiptCount uint64  `json:"terminal_receipt_count"`
	Sealed               bool    `json:"sealed"`
	UnresolvedCount      uint64  `json:"unresolved_count"`
	QuarantinedCount     uint64  `json:"quarantined_count"`
	EntriesDigest        *string `json:"entries_digest"`
	Result               string  `json:"result"`
	ProblemCode          *string `json:"problem_code"`
	ClaimStateAfter      string  `json:"claim_state_after"`
}

type step7StandDown struct {
	Name                string                  `json:"name"`
	OwnerEnvironmentID  string                  `json:"owner_environment_id"`
	ActingEnvironmentID string                  `json:"acting_environment_id"`
	ClaimID             string                  `json:"claim_id"`
	ClaimEpoch          uint64                  `json:"claim_epoch"`
	Attempts            []step7StandDownAttempt `json:"attempts"`
	SuccessRecords      []string                `json:"success_records"`
	OldOwnerEvidence    string                  `json:"old_owner_evidence"`
}

type step7StandDownAttempt struct {
	Name                      string  `json:"name"`
	PresentedEpoch            uint64  `json:"presented_epoch"`
	OwnerAuthorizationPresent bool    `json:"owner_authorization_present"`
	ReasonPresent             bool    `json:"reason_present"`
	LossAcknowledgmentPresent bool    `json:"loss_acknowledgment_present"`
	LossAcknowledgment        bool    `json:"loss_acknowledgment"`
	Result                    string  `json:"result"`
	ProblemCode               *string `json:"problem_code"`
	ClaimStateAfter           string  `json:"claim_state_after"`
}

type step7HandoffPromotion struct {
	Name                          string `json:"name"`
	PlannedHandoffWithActiveClaim string `json:"planned_handoff_with_active_claim"`
	PlannedHandoffAfterClose      string `json:"planned_handoff_after_close"`
	SourceAuthorityEpoch          uint64 `json:"source_authority_epoch"`
	PromotedAuthorityEpoch        uint64 `json:"promoted_authority_epoch"`
	OldClaimID                    string `json:"old_claim_id"`
	OldClaimEpoch                 uint64 `json:"old_claim_epoch"`
	OldReturnProblem              string `json:"old_return_problem"`
	OldReleaseProblem             string `json:"old_release_problem"`
	OldGrantState                 string `json:"old_grant_state"`
	UnresolvedOldCommand          string `json:"unresolved_old_command"`
	ReacquiredClaimID             string `json:"reacquired_claim_id"`
	ReacquiredClaimEpoch          uint64 `json:"reacquired_claim_epoch"`
}

type step7Cancellation struct {
	Name            string `json:"name"`
	BoundaryCrossed bool   `json:"boundary_crossed"`
	Outcome         string `json:"outcome"`
	DurableState    string `json:"durable_state"`
}

func loadStep7Fixture(t *testing.T) step7Fixture {
	t.Helper()

	f, err := os.Open(step7VectorsPath)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = f.Close() }()

	var got step7Fixture
	decoder := json.NewDecoder(f)
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&got); err != nil {
		t.Fatalf("decode vectors: %v", err)
	}
	if err := decoder.Decode(&struct{}{}); err != io.EOF {
		t.Fatalf("unexpected trailing JSON: %v", err)
	}
	return got
}

func TestStep7AcquireGrantExclusionAndHydration(t *testing.T) {
	fixture := loadStep7Fixture(t)
	step6 := loadStep6Fixture(t)
	if fixture.Notation != "wipd.claim-lifecycle-vector/1" || fixture.Schemas != (step7Schemas{
		Identity: "wipd.command/1", Receipt: "wipd.terminal-receipt/1",
		Acquire: "wipd.claim-acquire/1", GrantStart: "wipd.claim-grant-start/1",
		GrantEnd: "wipd.claim-grant-end/1", Readiness: "wipd.claim-readiness/1",
		ReturnCommand: "wipd.return-command/1", FoldResult: "wipd.fold-result/1",
		Release: "wipd.claim-release/1", Barrier: "wipd.journal-barrier/1",
		StandDown: "wipd.claim-stand-down/1",
	}) {
		t.Fatalf("fixture headers = %#v / %#v", fixture.Notation, fixture.Schemas)
	}
	for field, value := range map[string]string{
		"domain": fixture.Identity.DomainID, "owner": fixture.Identity.OwnerEnvironmentID,
		"actor": fixture.Identity.ActingEnvironmentID, "matter": fixture.Identity.MatterID,
		"batch": fixture.Identity.BatchID, "dispatch": fixture.Identity.DispatchID,
		"claim": fixture.Identity.ClaimID, "journal": fixture.Identity.JournalID,
	} {
		if !step6ULIDPattern.MatchString(value) {
			t.Errorf("identity %s = %q", field, value)
		}
	}
	if fixture.Identity.AuthorityEpoch == 0 || fixture.Identity.ClaimEpoch == 0 ||
		fixture.Identity.OwnerEnvironmentID == fixture.Identity.ActingEnvironmentID {
		t.Fatalf("authority/claim identity = %#v", fixture.Identity)
	}

	grant := fixture.AcquireGrant
	if grant.Operation != "claim.acquire@v1" || grant.ClaimField != nil ||
		grant.DispatchMode != "anonymous-matter" || grant.AnonymousBatchAction != "reuse-or-create-atomically" ||
		grant.InstalledAnchor != "one" || grant.AsOfAnchor != "three" ||
		grant.ManifestDigest != step6.Manifest.ManifestDigest || grant.ExpectedState != "hydrating" ||
		!step6ULIDPattern.MatchString(grant.GrantID) || !step6DigestPattern.MatchString(grant.RequestHash) {
		t.Fatalf("acquire/grant identity = %#v", grant)
	}
	wantOrder := []string{
		"submission.accepted", "claim.grant-start",
		"event:" + step6.Events[1].ID, "event:" + step6.Events[2].ID,
		"blob.manifest", "claim.grant-end",
	}
	if !reflect.DeepEqual(grant.MessageOrder, wantOrder) || grant.Receipt.CommandID != grant.CommandID ||
		grant.Receipt.RequestHash != grant.RequestHash || grant.Receipt.Result != "result.succeeded" ||
		grant.Receipt.FirstEventID != step6.Events[1].ID || grant.Receipt.LastEventID != step6.Events[2].ID ||
		grant.Receipt.EventCount != 2 || !contains(grant.AtomicLocalInstall, "empty-journal") {
		t.Fatalf("grant order/receipt/install = %#v", grant)
	}
	wantPins := []string{}
	for _, entry := range step6.Manifest.Entries {
		if entry.Requirement == "pin-before-use" {
			wantPins = append(wantPins, entry.Digest)
		}
	}
	if !reflect.DeepEqual(grant.PinBeforeUseDigests, wantPins) {
		t.Fatalf("grant pins = %v, want %v", grant.PinBeforeUseDigests, wantPins)
	}

	refusals := make(map[string]step7AcquireRefusal, len(fixture.AcquireRefusals))
	for _, refusal := range fixture.AcquireRefusals {
		refusals[refusal.Name] = refusal
		if !refusal.PresentedLocalLock || refusal.Result != "result.refused" ||
			!refusal.TerminalReceipt || refusal.Effects != "none" {
			t.Errorf("acquire refusal = %#v", refusal)
		}
	}
	if got := sortedMapKeys(refusals); !reflect.DeepEqual(got, []string{
		"batch-aggregate-footprint", "contended-matter", "cross-matter-footprint",
	}) || refusals["contended-matter"].ProblemCode != "refusal.claim-contended" ||
		refusals["cross-matter-footprint"].ProblemCode != "refusal.claim-cross-boundary" ||
		refusals["batch-aggregate-footprint"].ProblemCode != "refusal.claim-aggregate-authority-required" {
		t.Fatalf("exclusion/footprint refusals = %#v", refusals)
	}

	hydration := fixture.Hydration
	if hydration.GrantID != grant.GrantID || hydration.AsOfAnchor != grant.AsOfAnchor ||
		hydration.ManifestDigest != grant.ManifestDigest || hydration.RequiredEntryCount != uint64(len(wantPins)) ||
		len(hydration.Transitions) != 3 || hydration.CancelDuringHydrationState != "held-not-ready" {
		t.Fatalf("hydration binding = %#v", hydration)
	}
	for i, transition := range hydration.Transitions {
		if i < 2 && (transition.State != "hydrating" || transition.ClaimCommandAdmitted) {
			t.Errorf("premature hydration readiness at %d = %#v", i, transition)
		}
	}
	ready := hydration.Transitions[2]
	if ready.State != "offline-ready" || !ready.ClaimCommandAdmitted ||
		ready.VerifiedPinnedEntryCount != hydration.RequiredEntryCount || !strings.Contains(ready.Evidence, "durably-pinned") {
		t.Fatalf("final hydration readiness = %#v", ready)
	}
}

func TestStep7ClaimCommandReturnFoldAndRepair(t *testing.T) {
	fixture := loadStep7Fixture(t)
	command := fixture.ClaimCommand
	if command.ClaimID != fixture.Identity.ClaimID || command.ClaimEpoch != fixture.Identity.ClaimEpoch ||
		command.Return.JournalID != fixture.Identity.JournalID || command.Return.Delivery != "claim" ||
		command.Return.Position != command.JournalPosition || command.Return.Schema != fixture.Schemas.ReturnCommand ||
		command.Fold.Schema != fixture.Schemas.FoldResult || command.Fold.Continue ||
		command.Fold.PrefixStartAnchor != command.Return.BaseAsOfAnchor ||
		command.Fold.Receipt.CommandID != command.CommandID || command.Fold.Receipt.RequestHash != command.RequestHash ||
		command.Fold.Receipt.Result != "result.succeeded" || command.Fold.Receipt.EventCount != 1 ||
		command.Fold.Install != "receipt-prefix-manifest-atomic-before-journal-ack" {
		t.Fatalf("claim return/fold = %#v", command)
	}
	if !step6DigestPattern.MatchString(command.Fold.PrefixEnd.PrefixDigest) ||
		!step6DigestPattern.MatchString(command.Fold.ManifestDigest) ||
		!reflect.DeepEqual(command.LocalAcceptance, []string{
			"readiness-verified", "exact-claim-verified", "command-and-overlay-durable", "pending-return",
		}) {
		t.Fatalf("claim fold integrity/local boundary = %#v", command)
	}

	guards := make(map[string]step7ClaimGuard, len(fixture.ExactClaimGuards))
	for _, guard := range fixture.ExactClaimGuards {
		guards[guard.Name] = guard
		if guard.AuthoritySubmission || guard.TerminalReceipt || guard.ProblemCode != "claim.fenced" {
			t.Errorf("claim guard crossed submission = %#v", guard)
		}
	}
	if got := sortedMapKeys(guards); !reflect.DeepEqual(got, []string{"wrong-claim-epoch", "wrong-claim-id", "wrong-holder"}) ||
		guards["wrong-claim-id"].ClaimID == fixture.Identity.ClaimID ||
		guards["wrong-claim-epoch"].ClaimEpoch == fixture.Identity.ClaimEpoch {
		t.Fatalf("exact claim guard vectors = %#v", guards)
	}

	repair := fixture.ReturnRefusalAndRepair
	if repair.HeadResult != "result.refused" || repair.FoldContinue || repair.HeadPosition != 2 ||
		!reflect.DeepEqual(repair.QuarantinedPositions, []uint64{2, 3}) ||
		!contains(repair.Blocked, "return-position-3") || !contains(repair.Blocked, "release") ||
		!contains(repair.Forbidden, "skip-head") || !contains(repair.Forbidden, "copy-dependent-suffix") {
		t.Fatalf("return stop/quarantine = %#v", repair)
	}
	if len(repair.Controls) != 3 || repair.Controls[0] != (step7RepairControl{
		Action: "abandon", SafeProof: "terminal-non-success-receipt",
		OldGeneration: "archived-immutable", NewGeneration: "empty",
	}) || repair.Controls[1].Action != "replace" ||
		repair.Controls[1].NewGeneration != "replacement-at-position-1-new-id-hash-sequence" ||
		repair.Controls[2].SafeProof != "outcome-unknown" ||
		!strings.Contains(repair.Controls[2].NewGeneration, "refusal.journal-repair-proof") {
		t.Fatalf("repair controls = %#v", repair.Controls)
	}
}

func TestStep7ReleaseStandDownAndPromotionFencing(t *testing.T) {
	fixture := loadStep7Fixture(t)
	release := fixture.Release
	if release.ClaimID != fixture.Identity.ClaimID || release.ClaimEpoch != fixture.Identity.ClaimEpoch ||
		!step6ULIDPattern.MatchString(release.CommandID) || !step6DigestPattern.MatchString(release.RequestHash) ||
		len(release.Attempts) != 3 || release.PostCloseOldEpochProblem != "claim.fenced" {
		t.Fatalf("release identity = %#v", release)
	}
	for i, attempt := range release.Attempts {
		if !attempt.Sealed || attempt.EntryCount != 1 {
			t.Errorf("release attempt shape %d = %#v", i, attempt)
		}
		if i < 2 && (attempt.Result != "result.refused" || attempt.ProblemCode == nil ||
			*attempt.ProblemCode != "refusal.claim-release-barrier" || attempt.ClaimStateAfter != "active") {
			t.Errorf("release barrier refusal %d = %#v", i, attempt)
		}
	}
	complete := release.Attempts[2]
	if complete.TerminalReceiptCount != complete.EntryCount || complete.UnresolvedCount != 0 ||
		complete.QuarantinedCount != 0 || complete.EntriesDigest == nil ||
		!step6DigestPattern.MatchString(*complete.EntriesDigest) || complete.Result != "result.succeeded" ||
		complete.ProblemCode != nil || complete.ClaimStateAfter != "closed-epoch-3-invalid" {
		t.Fatalf("complete release barrier = %#v", complete)
	}
	var position [8]byte
	binary.BigEndian.PutUint64(position[:], fixture.ClaimCommand.JournalPosition)
	requestHash := digestBytes(t, fixture.ClaimCommand.RequestHash)
	var eventCount [8]byte
	binary.BigEndian.PutUint64(eventCount[:], fixture.ClaimCommand.Fold.Receipt.EventCount)
	barrier := domainHash("wipd/journal-barrier/v1", nil)
	barrier = domainHash("wipd/journal-barrier-step/v1", bytes.Join([][]byte{
		barrier,
		position[:],
		[]byte(fixture.ClaimCommand.CommandID),
		requestHash,
		{0, 1},
		[]byte(fixture.ClaimCommand.Fold.Receipt.FirstEventID),
		[]byte(fixture.ClaimCommand.Fold.Receipt.LastEventID),
		eventCount[:],
	}, nil))
	assertDigest(t, *complete.EntriesDigest, barrier)

	standDown := fixture.StandDown
	if standDown.OwnerEnvironmentID != fixture.Identity.OwnerEnvironmentID ||
		standDown.ActingEnvironmentID != fixture.Identity.ActingEnvironmentID ||
		standDown.OwnerEnvironmentID == standDown.ActingEnvironmentID ||
		standDown.ClaimID != fixture.Identity.ClaimID || standDown.ClaimEpoch != fixture.Identity.ClaimEpoch ||
		len(standDown.Attempts) != 4 || standDown.OldOwnerEvidence != "preserved-not-executed" {
		t.Fatalf("stand-down identity = %#v", standDown)
	}
	attempts := make(map[string]step7StandDownAttempt, len(standDown.Attempts))
	for _, attempt := range standDown.Attempts {
		attempts[attempt.Name] = attempt
	}
	wrong := attempts["wrong-epoch"]
	missing := attempts["missing-reason"]
	noLoss := attempts["loss-not-acknowledged"]
	accepted := attempts["accepted-loss-close"]
	if wrong.PresentedEpoch == standDown.ClaimEpoch || wrong.Result != "result.refused" ||
		wrong.ProblemCode == nil || *wrong.ProblemCode != "refusal.claim-stand-down-fenced" ||
		!wrong.OwnerAuthorizationPresent || !missing.OwnerAuthorizationPresent ||
		!noLoss.OwnerAuthorizationPresent || !accepted.OwnerAuthorizationPresent ||
		missing.ReasonPresent || missing.Result != "no-submission" || missing.ProblemCode == nil ||
		*missing.ProblemCode != "protocol.malformed-message" ||
		!noLoss.LossAcknowledgmentPresent || noLoss.LossAcknowledgment || noLoss.ProblemCode == nil ||
		*noLoss.ProblemCode != "refusal.claim-loss-not-acknowledged" ||
		!accepted.ReasonPresent || !accepted.LossAcknowledgment || accepted.Result != "result.succeeded" ||
		accepted.ProblemCode != nil || accepted.ClaimStateAfter != "closed-epoch-3-invalid" ||
		!contains(standDown.SuccessRecords, "owner-environment") ||
		!contains(standDown.SuccessRecords, "acting-environment") ||
		!contains(standDown.SuccessRecords, "owner-attestation") ||
		!contains(standDown.SuccessRecords, "reason-digest") ||
		!contains(standDown.SuccessRecords, "accepted-loss-acknowledgment") {
		t.Fatalf("stand-down refusal/success boundary = %#v", attempts)
	}

	promotion := fixture.HandoffPromotion
	if promotion.SourceAuthorityEpoch != fixture.Identity.AuthorityEpoch ||
		promotion.PromotedAuthorityEpoch != promotion.SourceAuthorityEpoch+1 ||
		promotion.PlannedHandoffWithActiveClaim != "refused-not-quiescent" ||
		promotion.OldReturnProblem != "claim.fenced" || promotion.OldReleaseProblem != "claim.fenced" ||
		promotion.UnresolvedOldCommand != "never-execute-after-promotion" ||
		promotion.ReacquiredClaimID == promotion.OldClaimID ||
		promotion.ReacquiredClaimEpoch <= promotion.OldClaimEpoch {
		t.Fatalf("handoff/promotion fencing = %#v", promotion)
	}
}

func TestStep7CancellationBoundaries(t *testing.T) {
	fixture := loadStep7Fixture(t)
	cases := make(map[string]step7Cancellation, len(fixture.Cancellation))
	for _, cancellation := range fixture.Cancellation {
		if _, exists := cases[cancellation.Name]; exists {
			t.Fatalf("duplicate cancellation case %q", cancellation.Name)
		}
		cases[cancellation.Name] = cancellation
	}
	wantNames := []string{
		"acquire-after-authority-submission-before-grant-install",
		"acquire-before-authority-submission",
		"claim-command-after-journal-commit",
		"claim-command-before-journal-commit",
		"hydration-cancelled",
		"release-after-authority-submission",
		"return-after-possible-authority-submission",
	}
	if got := sortedMapKeys(cases); !reflect.DeepEqual(got, wantNames) {
		t.Fatalf("cancellation cases = %v", got)
	}
	for _, name := range []string{"acquire-before-authority-submission", "claim-command-before-journal-commit"} {
		got := cases[name]
		if got.BoundaryCrossed || got.Outcome != "transport.cancelled-before-submission" ||
			(!strings.Contains(got.DurableState, "no-claim") && !strings.Contains(got.DurableState, "no-entry")) {
			t.Errorf("pre-submission cancellation %s = %#v", name, got)
		}
	}
	for _, name := range []string{
		"acquire-after-authority-submission-before-grant-install",
		"claim-command-after-journal-commit",
		"return-after-possible-authority-submission",
		"release-after-authority-submission",
	} {
		if !cases[name].BoundaryCrossed {
			t.Errorf("post-boundary cancellation %s did not preserve durable state: %#v", name, cases[name])
		}
	}
	if cases["return-after-possible-authority-submission"].Outcome != "outcome-unknown" ||
		!strings.Contains(cases["return-after-possible-authority-submission"].DurableState, "release-blocked") ||
		cases["hydration-cancelled"].BoundaryCrossed ||
		!strings.Contains(cases["hydration-cancelled"].DurableState, "not-ready") {
		t.Fatalf("return/hydration cancellation = %#v / %#v",
			cases["return-after-possible-authority-submission"], cases["hydration-cancelled"])
	}
}

func TestStep7ContractMapsDecisionsQuestionsAndBoundaries(t *testing.T) {
	contract, err := os.ReadFile("../../docs/wipd/claim-lifecycle-contract.md")
	if err != nil {
		t.Fatal(err)
	}
	for _, token := range []string{
		"wipd.command/1", "wipd.terminal-receipt/1", "ClaimAcquire", "ClaimGrantStart",
		"ClaimGrantQuery", "ClaimReadiness", "ReturnCommand", "FoldResult", "JournalBarrier",
		"ClaimRelease", "ClaimStandDown", "PrefixDelta", "BlobManifest", "pin-before-use",
		"D121", "D122", "D123", "D125", "D126", "D128", "D129", "D130", "D131",
		"Q12", "Q15", "Q16", "Q17", "Q20", "Q21", "Q23", "Q24", "Q25",
		"There are no unresolved Step 7 decisions",
	} {
		if !bytes.Contains(contract, []byte(token)) {
			t.Errorf("contract does not map %q", token)
		}
	}
	for _, forbidden := range []string{"CREATE TABLE", "net.Listen", "cobra.Command"} {
		if bytes.Contains(contract, []byte(forbidden)) {
			t.Errorf("contract crosses implementation boundary with %q", forbidden)
		}
	}
}
