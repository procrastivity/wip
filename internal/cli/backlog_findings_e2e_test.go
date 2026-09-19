// End-to-end coverage of v12's widening: `wip plumbing finding add` accepts a
// backlog entry's ULID, the create-once verbs still refuse it, `wip plumbing
// backlog show` reads the accumulated findings back, and findings survive the
// entry's exits.
package cli_test

import (
	"context"
	"path/filepath"
	"strings"
	"testing"

	"github.com/procrastivity/wip/internal/store"
)

type backlogEntryJSON struct {
	ID    string `json:"id"`
	State string `json:"state"`
}

type backlogShowJSON struct {
	ID       string `json:"id"`
	State    string `json:"state"`
	Findings []struct {
		At   string `json:"at"`
		Text string `json:"text"`
	} `json:"findings"`
}

func TestBacklog_FindingAdd_AttachesAndSurvivesDecline(t *testing.T) {
	dbEnv := []string{"WIP_DB_PATH=" + filepath.Join(t.TempDir(), "wip.db")}
	dir := newGitRepo(t, "widget")
	if r := runIn(t, dir, dbEnv, "init"); r.exitCode != 0 {
		t.Fatalf("init: exit=%d stderr=%q", r.exitCode, r.stderr)
	}
	entry := mustJSON[backlogEntryJSON](t, runIn(t, dir, dbEnv,
		"plumbing", "backlog", "add", "--title", "Triage me", "--provenance", "intake", "--json").stdout)

	// finding add takes the entry's ULID like any node locator, one
	// content.appended per call.
	r1 := runIn(t, dir, dbEnv, "plumbing", "finding", "add", entry.ID, "looked into it: blocked on the seam", "--json")
	if r1.exitCode != 0 {
		t.Fatalf("finding add on an entry: exit=%d stderr=%q", r1.exitCode, r1.stderr)
	}
	s := openTestStore(t, dbEnvPath(dbEnv))
	wantAppendedEvent(t, s, entry.ID, 1, store.TypeContentAppended) // the entry already carried backlog.entered
	r2 := runIn(t, dir, dbEnv, "plumbing", "finding", "add", entry.ID, "second note", "--json")
	if r2.exitCode != 0 {
		t.Fatalf("second finding add on an entry: exit=%d stderr=%q", r2.exitCode, r2.stderr)
	}
	s = openTestStore(t, dbEnvPath(dbEnv))
	wantAppendedEvent(t, s, entry.ID, 2, store.TypeContentAppended)

	// The create-once verbs keep their node-only resolver: the entry's ULID
	// is an unknown locator to them, and nothing lands.
	refused := runInStdin(t, dir, dbEnv, "a brief", "plumbing", "brief", entry.ID)
	if refused.exitCode == 0 {
		t.Fatal("brief on a backlog entry should be refused")
	}
	if !strings.Contains(refused.stderr, "no node") {
		t.Errorf("brief on an entry refused with %q, want the node-only unknown-locator message", refused.stderr)
	}
	s = openTestStore(t, dbEnvPath(dbEnv))
	segs, err := s.ContentSegments(context.Background(), entry.ID, store.KindBrief)
	if err != nil {
		t.Fatal(err)
	}
	if len(segs) != 0 {
		t.Fatalf("a refused brief left %d segments on the entry", len(segs))
	}

	// backlog show reads the findings back, in append order.
	shown := mustJSON[backlogShowJSON](t, runIn(t, dir, dbEnv, "plumbing", "backlog", "show", entry.ID, "--json").stdout)
	if shown.ID != entry.ID || len(shown.Findings) != 2 {
		t.Fatalf("show = id %s with %d findings, want %s with 2", shown.ID, len(shown.Findings), entry.ID)
	}
	if shown.Findings[0].Text != "looked into it: blocked on the seam" || shown.Findings[1].Text != "second note" {
		t.Errorf("show findings = %+v, want the two notes in append order", shown.Findings)
	}

	// The findings stay on the entry through an exit.
	if r := runIn(t, dir, dbEnv, "plumbing", "backlog", "decline", entry.ID, "--reason", "not worth a Matter"); r.exitCode != 0 {
		t.Fatalf("decline: exit=%d stderr=%q", r.exitCode, r.stderr)
	}
	shown = mustJSON[backlogShowJSON](t, runIn(t, dir, dbEnv, "plumbing", "backlog", "show", entry.ID, "--json").stdout)
	if shown.State != "declined" || len(shown.Findings) != 2 {
		t.Errorf("after decline: state=%s findings=%d, want declined with 2", shown.State, len(shown.Findings))
	}

	// An unknown id is a refusal, and the human render carries the findings.
	if r := runIn(t, dir, dbEnv, "plumbing", "backlog", "show", "01ARZ3NDEKTSV4RRFFQ69G5FAV"); r.exitCode == 0 {
		t.Fatal("show of an unknown entry should be refused")
	}
	human := runIn(t, dir, dbEnv, "plumbing", "backlog", "show", entry.ID)
	if human.exitCode != 0 {
		t.Fatalf("human show: exit=%d stderr=%q", human.exitCode, human.stderr)
	}
	for _, want := range []string{"Triage me", "state: declined", "2 findings:", "second note"} {
		if !strings.Contains(human.stdout, want) {
			t.Errorf("human show output misses %q:\n%s", want, human.stdout)
		}
	}
}
