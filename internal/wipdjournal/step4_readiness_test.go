package wipdjournal

import (
	"context"
	"fmt"
	"path/filepath"
	"testing"

	"github.com/procrastivity/wip/internal/operation"
	"github.com/procrastivity/wip/internal/wipdwire"
)

func TestInstalledClaimMatterResolvesM6StageAndStepLineage(t *testing.T) {
	ctx := context.Background()
	fixture := makeHydrationGrantFixture(t, nil, 30)
	journal, err := Open(filepath.Join(t.TempDir(), "journal"), fixture.identity)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = journal.Close() })
	installed, err := journal.InstallClaimGrant(ctx, mustInstallSnapshot(t, journal).Expectation(), fixture.grant)
	if err != nil {
		t.Fatalf("install the exact signed grant and Matter prefix: %v", err)
	}
	const (
		stage               = "01KZ7XHAQT1S46NYPN1PW1DX70"
		stepRemoved         = "01KZ7XHAQT1S46NYPN1PW1DX71"
		stepInserted        = "01KZ7XHAQT1S46NYPN1PW1DX72"
		stepLive            = "01KZ7XHAQT1S46NYPN1PW1DX73"
		otherMatter         = "01KZ7XHAQT1S46NYPN1PW1DX74"
		otherMatterSameRepo = "01KZ7XHAQT1S46NYPN1PW1DX75"
		otherStepSameRepo   = "01KZ7XHAQT1S46NYPN1PW1DX76"
	)
	matter := fixture.grant.matterID
	start := installed.Anchor
	specs := []struct {
		kind, subject, repo string
		payload             map[string]any
	}{
		{
			"stage.created", stage, fixture.identity.RepoID,
			map[string]any{"matter_id": matter, "locator": "planning", "title": "Planning", "sort_key": uint64(2000)},
		},
		{
			"step.created", stepRemoved, fixture.identity.RepoID,
			map[string]any{"parent": stage, "locator": "step-01", "title": "Old", "sort_key": uint64(1000)},
		},
		{
			"step.inserted", stepInserted, fixture.identity.RepoID,
			map[string]any{"parent": stage, "locator": "step-02", "title": "Inserted", "sort_key": uint64(1500)},
		},
		{"step.reordered", stage, fixture.identity.RepoID, map[string]any{"order": []any{stepInserted, stepRemoved}}},
		{
			"step.replaced", stepRemoved, fixture.identity.RepoID,
			map[string]any{"replacement": stepLive, "locator": "step-03", "title": "Live replacement"},
		},
		{"step.removed", stepInserted, fixture.identity.RepoID, map[string]any{"reason": "superseded"}},
		{
			"matter.created", otherMatter, testCommandPrefix + "99",
			map[string]any{"id": otherMatter, "locator": "other", "title": "Other repo"},
		},
		{
			"matter.created", otherMatterSameRepo, fixture.identity.RepoID,
			map[string]any{"id": otherMatterSameRepo, "locator": "other-in-repo", "title": "Other Matter"},
		},
		{
			"step.created", otherStepSameRepo, fixture.identity.RepoID,
			map[string]any{"parent": otherMatterSameRepo, "locator": "step-01", "title": "Other Step", "sort_key": uint64(1000)},
		},
	}
	records := make([]wipdwire.EventRecord, 0, len(specs))
	for index, spec := range specs {
		eventID := testCommandPrefix + fmt.Sprintf("%02d", 80+index)
		record, encodeErr := wipdwire.EncodeCanonical(map[string]any{
			"schema": "wipd.event/1", "event_id": eventID, "domain_id": fixture.identity.DomainID,
			"command_id": testCommandPrefix + "90", "request_hash": hydrationDigest([]byte(fmt.Sprintf("readiness tail %d", index))),
			"environment": map[string]any{"id": fixture.identity.EnvironmentID, "sequence": uint64(3)},
			"acted_at":    "2026-09-29T00:00:00Z", "occurred_at": "2026-09-29T00:00:00Z",
			"kind": spec.kind, "subject_id": spec.subject, "repo_id": spec.repo, "payload": spec.payload,
		})
		if encodeErr != nil {
			t.Fatal(encodeErr)
		}
		records = append(records, wipdwire.EventRecord{EventID: eventID, Record: record})
	}
	transfer := verifiedInstallTestTransfer(t, start, records)
	installed, err = journal.InstallPull(ctx, installed.Expectation(), transfer)
	if err != nil {
		t.Fatalf("install verified Stage/Step tail: %v", err)
	}
	claim := &operation.ClaimContext{ID: fixture.claimID, Epoch: "1"}
	request := func(input operation.Input) operation.Request {
		return operation.Request{
			Context: operation.Context{Repo: fixture.identity.RepoID, Clone: testCommandPrefix + "95", Worktree: fixture.grant.WorktreeID()},
			Claim:   claim, Input: input,
		}
	}

	for _, test := range []struct {
		name       string
		input      operation.Input
		wantMatter string
	}{
		{"Stage birth parent", operation.StageCreateInput{MatterID: matter, Title: "Next"}, matter},
		{"Step birth under Stage", operation.StepCreateInput{ParentID: stage, Title: "Next"}, matter},
		{"Step insert under Stage", operation.StepInsertInput{ParentID: stage, Title: "Next"}, matter},
		{"Step reorder under Stage", operation.StepReorderInput{ParentID: stage, Order: []string{stepLive}}, matter},
		{"Step lifecycle resolves Stage ancestry", operation.StepLifecycleInput{StepID: stepLive}, matter},
		{"Step cancel resolves Stage ancestry", operation.StepCancelInput{StepID: stepLive, Reason: "obsolete"}, matter},
		{"Matter lifecycle target", operation.NodeLifecycleInput{NodeID: matter}, matter},
		{"Stage lifecycle target resolves Matter ancestry", operation.NodeLifecycleInput{NodeID: stage}, matter},
		{"Step replacement target", operation.StepReplaceInput{StepID: stepLive, Title: "Again"}, matter},
		{"Step removal target", operation.StepRemoveInput{StepID: stepLive, Reason: "done"}, matter},
		{"Matter finish", operation.MatterFinishInput{MatterID: matter}, matter},
		{"Matter locator repair", operation.MatterLocatorRepairInput{MatterID: matter, Action: "accept", AssignedLocator: "readiness"}, matter},
		{"content on Matter", operation.ContentWriteInput{SubjectID: matter, Kind: "brief"}, matter},
		{"finding on Step", operation.FindingAppendInput{SubjectID: stepLive}, matter},
		{"removed Step is not ready", operation.StepLifecycleInput{StepID: stepInserted}, ""},
		{"missing parent is not ready", operation.StepCreateInput{ParentID: testCommandPrefix + "98", Title: "Missing"}, ""},
		{"other Repo Matter is not ready", operation.StageCreateInput{MatterID: otherMatter, Title: "Wrong Repo"}, ""},
	} {
		t.Run(test.name, func(t *testing.T) {
			gotMatter, ready, err := installedClaimMatter(ctx, journal.db, fixture.identity.RepoID, test.input)
			if err != nil || (gotMatter != "") != (test.wantMatter != "") || gotMatter != test.wantMatter {
				t.Fatalf("installedClaimMatter(%T) = matter %q ready %t err %v; want matter %q", test.input, gotMatter, ready, err, test.wantMatter)
			}
			if test.wantMatter != "" {
				if err = journal.ValidateCommandClaimReadiness(ctx, request(test.input)); err != nil {
					t.Fatalf("public claim-readiness validation rejected %T: %v", test.input, err)
				}
			}
		})
	}
	for name, invalid := range map[string]operation.Request{
		"wrong Repo":                   {Context: operation.Context{Repo: testCommandPrefix + "99", Clone: testCommandPrefix + "95", Worktree: fixture.grant.WorktreeID()}, Claim: claim, Input: operation.StageCreateInput{MatterID: matter, Title: "No"}},
		"wrong Worktree":               {Context: operation.Context{Repo: fixture.identity.RepoID, Clone: testCommandPrefix + "95", Worktree: testCommandPrefix + "96"}, Claim: claim, Input: operation.StageCreateInput{MatterID: matter, Title: "No"}},
		"wrong Claim":                  {Context: operation.Context{Repo: fixture.identity.RepoID, Clone: testCommandPrefix + "95", Worktree: fixture.grant.WorktreeID()}, Claim: &operation.ClaimContext{ID: testCommandPrefix + "97", Epoch: "1"}, Input: operation.StageCreateInput{MatterID: matter, Title: "No"}},
		"removed Step":                 request(operation.StepLifecycleInput{StepID: stepInserted}),
		"removed Step cancel":          request(operation.StepCancelInput{StepID: stepInserted, Reason: "obsolete"}),
		"different Matter Step cancel": request(operation.StepCancelInput{StepID: otherStepSameRepo, Reason: "wrong claim scope"}),
	} {
		t.Run(name, func(t *testing.T) {
			if err = journal.ValidateCommandClaimReadiness(ctx, invalid); err == nil {
				t.Fatalf("claim readiness accepted %s", name)
			}
		})
	}
}
