package wipdjournal

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"testing"

	"github.com/procrastivity/wip/internal/operation"
)

func TestClaimJournalCloseBlocksUnresolvedAndQuarantinedWork(t *testing.T) {
	t.Run("unresolved claim command", func(t *testing.T) {
		journal, binding, acquired := openClaimJournalCloseTestFixture(t, 30)
		defer func() { _ = journal.Close() }()

		entry := prepareClaimJournalCloseTestCommand(t, journal, acquired, binding.ClaimID, testCommandPrefix+"98")
		if entry.State != StatePreAdmission {
			t.Fatalf("prepared command state = %q; want %q", entry.State, StatePreAdmission)
		}
		if _, _, err := journal.ClaimJournal(binding); !errors.Is(err, ErrClaimJournalIncomplete) {
			t.Fatalf("acquired-claim close accepted unresolved command: %v", err)
		}
		var attempts int
		if err := journal.db.QueryRow(`SELECT count(*) FROM claim_journal_release_attempts`).Scan(&attempts); err != nil || attempts != 0 {
			t.Fatalf("unresolved command created release attempts=%d, err=%v", attempts, err)
		}
	})

	t.Run("terminal non-success claim command", func(t *testing.T) {
		journal, binding, acquired := openClaimJournalCloseTestFixture(t, 50)
		defer func() { _ = journal.Close() }()

		entry := prepareClaimJournalCloseTestCommand(t, journal, acquired, binding.ClaimID, testCommandPrefix+"98")
		installed, err := journal.InstallSnapshot(context.Background())
		if err != nil {
			t.Fatal(err)
		}
		installed, err = journal.AdmitPending(context.Background(), installed.Expectation(), entry.Command.ID)
		if err != nil {
			t.Fatalf("admit claim command for exact terminal refusal: %v", err)
		}
		entry, err = journal.Get(entry.Command.ID)
		if err != nil || entry.State != StatePendingReturn {
			t.Fatalf("pending returned-command identity = %+v, %v", entry, err)
		}
		receipt := installTestReceipt(t, entry, operation.ResultRefused, nil)
		if _, err = journal.InstallFold(context.Background(), installed.Expectation(), entry, operation.ResultRefused,
			receipt, emptyInstallTestTransfer(t, installed.Anchor)); err != nil {
			t.Fatalf("install exact terminal refusal: %v", err)
		}
		if _, _, err = journal.ClaimJournal(binding); !errors.Is(err, ErrClaimJournalIncomplete) {
			t.Fatalf("acquired-claim close accepted terminal non-success receipt: %v", err)
		}
		var attempts int
		if err = journal.db.QueryRow(`SELECT count(*) FROM claim_journal_release_attempts`).Scan(&attempts); err != nil || attempts != 0 {
			t.Fatalf("quarantined command created release attempts=%d, err=%v", attempts, err)
		}
	})
}

func openClaimJournalCloseTestFixture(t *testing.T, base int) (*Journal, ClaimJournalBinding, ClaimAcquireAttempt) {
	t.Helper()
	fixture := makeHydrationGrantFixture(t, nil, base)
	journal, err := Open(filepath.Join(t.TempDir(), "journal"), fixture.identity)
	if err != nil {
		t.Fatal(err)
	}
	installed, err := journal.InstallClaimGrant(context.Background(), mustInstallSnapshot(t, journal).Expectation(), fixture.grant)
	if err != nil {
		_ = journal.Close()
		t.Fatalf("install verified claim grant: %v", err)
	}
	status, err := journal.BeginClaimHydration(context.Background(), fixture.grantID)
	if err != nil || status.State != "offline-ready" {
		_ = journal.Close()
		t.Fatalf("empty verified grant hydration status = %+v, %v", status, err)
	}
	if installed.Anchor.EventCount != 3 {
		_ = journal.Close()
		t.Fatalf("installed acquisition anchor = %+v; want three claim acquisition events", installed.Anchor)
	}
	acquired := ClaimAcquireAttempt{CloneID: testCommandPrefix + "97", WorktreeID: fixture.grant.WorktreeID()}
	if acquired.WorktreeID == "" {
		_ = journal.Close()
		t.Fatal("verified grant fixture omitted its Worktree identity")
	}
	binding := ClaimJournalBinding{
		ClaimID: fixture.claimID, ClaimEpoch: 1, MatterID: fixture.grant.matterID, DispatchID: fixture.grant.dispatchID,
		JournalID: testCommandPrefix + fmt.Sprintf("%02d", base+11), Generation: 1, State: "open",
	}
	if _, err = journal.PinClaimJournalIdentity(context.Background(), binding); err != nil {
		_ = journal.Close()
		t.Fatalf("pin authority-verified fixture generation: %v", err)
	}
	return journal, binding, acquired
}

func prepareClaimJournalCloseTestCommand(t *testing.T, journal *Journal, acquired ClaimAcquireAttempt, claimID, id string) Entry {
	t.Helper()
	entry, err := journal.PrepareCommand(CommandInput{
		ID: id,
		Request: operation.Request{
			Operation: operation.StepStartV1.Metadata().Operation,
			Actor:     "human",
			Context: operation.Context{
				Repo: journal.identity.RepoID, Clone: acquired.CloneID, Worktree: acquired.WorktreeID,
			},
			Claim: &operation.ClaimContext{ID: claimID, Epoch: "1"},
			Input: operation.StepLifecycleInput{StepID: testCommandPrefix + "99"},
		},
	})
	if err != nil {
		t.Fatalf("prepare claim lifecycle command: %v", err)
	}
	if entry.Delivery != operation.DeliveryClaim {
		t.Fatalf("claim lifecycle delivery = %q; want claim", entry.Delivery)
	}
	return entry
}
