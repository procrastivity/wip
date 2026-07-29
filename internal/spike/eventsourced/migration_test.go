package eventsourced

import (
	"context"
	"errors"
	"os"
	"reflect"
	"testing"

	"github.com/procrastivity/wip/internal/spike/scenario"
)

// TestV2MigrationOverV1Database is the migration exercise from
// docs/store-fork/scenario.md, done the way it is actually done: populate a
// database under v1, close it, reopen it at v2 so the numbered migration
// runs, and check that rows born before the migration still answer.
func TestV2MigrationOverV1Database(t *testing.T) {
	ctx := context.Background()

	// --- v1: a real, populated database. ---
	v1, path := openTemp(t, 1)
	if v1.Version() != 1 {
		t.Fatalf("opened at v%d, want v1", v1.Version())
	}
	if err := v1.Label(ctx, "whatever", "x"); !errors.Is(err, ErrNeedsV2) {
		t.Fatalf("Label at v1: got %v, want ErrNeedsV2", err)
	}
	if _, err := v1.InProgressLabeled(ctx); !errors.Is(err, ErrNeedsV2) {
		t.Fatalf("InProgressLabeled at v1: got %v, want ErrNeedsV2", err)
	}
	matter, _, step2 := populate(t, v1)
	v1Answer, err := v1.InProgress(ctx)
	if err != nil {
		t.Fatalf("InProgress (v1): %v", err)
	}
	v1Log, err := v1.Events(ctx)
	if err != nil {
		t.Fatalf("Events (v1): %v", err)
	}
	if err := v1.Close(); err != nil {
		t.Fatalf("Close (v1): %v", err)
	}

	// --- the migration itself: nothing but reopening. ---
	v2, err := Open(path, scenario.FixedEnv)
	if err != nil {
		t.Fatalf("Open at v2 over a v1 database: %v", err)
	}
	defer func() { _ = v2.Close() }()
	if v2.Version() != 2 {
		t.Fatalf("after migration: v%d, want v2", v2.Version())
	}

	// backup-before-migrate (PLAN 1.2): the v1 file is still on disk.
	if _, err := os.Stat(backupPath(path, 1)); err != nil {
		t.Fatalf("no pre-migration backup at %s: %v", backupPath(path, 1), err)
	}

	// The log was not touched by the migration.
	v2Log, err := v2.Events(ctx)
	if err != nil {
		t.Fatalf("Events (v2): %v", err)
	}
	if !reflect.DeepEqual(v1Log, v2Log) {
		t.Fatal("the v2 migration rewrote the log")
	}

	// Old rows still answer the old question identically...
	v2Answer, err := v2.InProgress(ctx)
	if err != nil {
		t.Fatalf("InProgress (v2): %v", err)
	}
	if !reflect.DeepEqual(v1Answer, v2Answer) {
		t.Fatalf("the v1 answer changed under v2:\n before: %+v\n after:  %+v", v1Answer, v2Answer)
	}

	// ...and the new question, with no backfill: unlabeled is empty, not null,
	// not missing.
	labeled, err := v2.InProgressLabeled(ctx)
	if err != nil {
		t.Fatalf("InProgressLabeled: %v", err)
	}
	if len(labeled) != 2 {
		t.Fatalf("InProgressLabeled: got %d nodes, want 2", len(labeled))
	}
	for _, n := range labeled {
		if n.Label != "" {
			t.Fatalf("pre-migration node %s came back labeled %q", n.ID, n.Label)
		}
	}

	// The new verb works on a node that predates the event type entirely.
	if err := v2.Label(ctx, step2, "hot"); err != nil {
		t.Fatalf("Label(step2): %v", err)
	}
	if err := v2.Label(ctx, matter, "q3"); err != nil {
		t.Fatalf("Label(matter): %v", err)
	}
	labeled, err = v2.InProgressLabeled(ctx)
	if err != nil {
		t.Fatalf("InProgressLabeled after labeling: %v", err)
	}
	want := map[string]string{matter: "q3", step2: "hot"}
	for _, n := range labeled {
		if n.Label != want[n.ID] {
			t.Fatalf("node %s: label %q, want %q", n.ID, n.Label, want[n.ID])
		}
	}

	// The new events are ordinary events under the same envelope, appended
	// after everything that came before.
	after, err := v2.Events(ctx)
	if err != nil {
		t.Fatalf("Events after labeling: %v", err)
	}
	if len(after) != len(v2Log)+2 {
		t.Fatalf("labeling appended %d events, want 2", len(after)-len(v2Log))
	}
	newest := after[len(after)-2:]
	if newest[0].Type != TypeStepLabeled || newest[0].Subject != step2 {
		t.Fatalf("first label event: %s/%s", newest[0].Type, newest[0].Subject)
	}
	if newest[1].Type != TypeMatterLabeled || newest[1].Subject != matter {
		t.Fatalf("second label event: %s/%s", newest[1].Type, newest[1].Subject)
	}
	for _, e := range newest {
		if e.Repo != scenario.FixedEnv.Repo || e.Clone != "" || e.Worktree != "" {
			t.Fatalf("new event type %s got the wrong tier dimensions: %q/%q/%q",
				e.Type, e.Repo, e.Clone, e.Worktree)
		}
	}

	// And a rebuild from the log alone still reproduces the migrated
	// projection, labels included.
	before := snapshot(t, v2)
	if err := v2.Rebuild(ctx); err != nil {
		t.Fatalf("Rebuild after migration: %v", err)
	}
	if !reflect.DeepEqual(before, snapshot(t, v2)) {
		t.Fatal("post-migration rebuild diverged from the maintained projection")
	}
}

// TestDowngradeIsRefused: the migration register only moves forward.
func TestDowngradeIsRefused(t *testing.T) {
	st, path := openTemp(t, LatestVersion)
	if err := st.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	if _, err := OpenAt(path, scenario.FixedEnv, 1); err == nil {
		t.Fatal("opening a v2 database at v1: want refusal, got nil")
	}
}

// TestNoBackupOnFirstCreate: backup-before-migrate has nothing to back up
// when the database is being born.
func TestNoBackupOnFirstCreate(t *testing.T) {
	_, path := openTemp(t, LatestVersion)
	if _, err := os.Stat(backupPath(path, 0)); !os.IsNotExist(err) {
		t.Fatalf("a fresh database left a backup behind: %v", err)
	}
}
