package cli_test

// End-to-end coverage of `wip outbox backlog-push`: text and JSON reads and
// writes, the no-backend auto warning, and lazy config (no event, no outbox).

import (
	"context"
	"strings"
	"testing"
)

func TestOutboxBacklogPushTextJSONWarningAndLazyConfig(t *testing.T) {
	dir, dbEnv := setupRepo(t)

	initial := runIn(t, dir, dbEnv, "outbox", "backlog-push")
	if initial.exitCode != 0 || initial.stdout != "manual\n" || initial.stderr != "" {
		t.Fatalf("initial backlog-push: exit=%d stdout=%q stderr=%q", initial.exitCode, initial.stdout, initial.stderr)
	}

	initialJSON := runIn(t, dir, dbEnv, "outbox", "backlog-push", "--json")
	if initialJSON.exitCode != 0 || mustJSON[struct {
		BacklogPush string `json:"backlogPush"`
	}](t, initialJSON.stdout).BacklogPush != "manual" {
		t.Fatalf("initial backlog-push JSON: exit=%d stdout=%q stderr=%q", initialJSON.exitCode, initialJSON.stdout, initialJSON.stderr)
	}

	setAuto := runIn(t, dir, dbEnv, "outbox", "backlog-push", "auto")
	if setAuto.exitCode != 0 || setAuto.stdout != "auto\n" ||
		!strings.Contains(setAuto.stderr, "tracker.backlog-push is auto but no tracker backend is configured") {
		t.Fatalf("set auto: exit=%d stdout=%q stderr=%q", setAuto.exitCode, setAuto.stdout, setAuto.stderr)
	}

	readAuto := runIn(t, dir, dbEnv, "outbox", "backlog-push", "--json")
	if readAuto.exitCode != 0 || !strings.Contains(readAuto.stdout, `"backlogPush":"auto"`) || readAuto.stderr != "" {
		t.Fatalf("read auto: exit=%d stdout=%q stderr=%q", readAuto.exitCode, readAuto.stdout, readAuto.stderr)
	}

	setAutoJSON := runIn(t, dir, dbEnv, "outbox", "backlog-push", "auto", "--json")
	if setAutoJSON.exitCode != 0 || mustJSON[struct {
		BacklogPush string `json:"backlogPush"`
	}](t, setAutoJSON.stdout).BacklogPush != "auto" ||
		!strings.Contains(setAutoJSON.stderr, "tracker.backlog-push is auto but no tracker backend is configured") {
		t.Fatalf("set auto JSON: exit=%d stdout=%q stderr=%q", setAutoJSON.exitCode, setAutoJSON.stdout, setAutoJSON.stderr)
	}

	setManual := runIn(t, dir, dbEnv, "outbox", "backlog-push", "manual")
	if setManual.exitCode != 0 || setManual.stdout != "manual\n" || setManual.stderr != "" {
		t.Fatalf("set manual: exit=%d stdout=%q stderr=%q", setManual.exitCode, setManual.stdout, setManual.stderr)
	}

	invalid := runIn(t, dir, dbEnv, "outbox", "backlog-push", "always")
	if invalid.exitCode == 0 || !strings.Contains(invalid.stderr, "expected manual or auto") {
		t.Fatalf("invalid backlog-push: exit=%d stderr=%q", invalid.exitCode, invalid.stderr)
	}

	dbPath := strings.TrimPrefix(dbEnv[0], "WIP_DB_PATH=")
	s := openTestStore(t, dbPath)
	repos, err := s.Repos(context.Background())
	if err != nil || len(repos) != 1 {
		t.Fatalf("repos = %+v (err %v)", repos, err)
	}
	events, err := s.Events(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	entries, err := s.Outbox(context.Background(), repos[0].ID)
	if err != nil || len(entries) != 0 {
		t.Fatalf("backlog-push config queued %+v, err=%v", entries, err)
	}
	for _, event := range events {
		if strings.Contains(event.Type, "tracker") || strings.Contains(event.Type, "outbox") || strings.Contains(event.Type, "backlog") {
			t.Fatalf("backlog-push config emitted %s", event.Type)
		}
	}
}
