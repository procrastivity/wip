package cli_test

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/procrastivity/wip/internal/store"
)

type outboxPayload struct {
	ID             string          `json:"id"`
	Kind           string          `json:"kind"`
	State          string          `json:"state"`
	Subject        string          `json:"subject"`
	Reference      string          `json:"reference"`
	IdempotencyKey string          `json:"idempotencyKey"`
	Payload        json.RawMessage `json:"payload"`
	Reason         string          `json:"reason"`
	Attempts       int             `json:"attempts"`
}

func TestOutboxCLIExposesHumanAndJSONLifecycleActions(t *testing.T) {
	dir, dbEnv := setupRepo(t)
	delegate := func(title string) string {
		t.Helper()
		added := mustJSON[struct {
			ID string `json:"id"`
		}](t, runIn(t, dir, dbEnv, "backlog", "add", "--title", title, "--provenance", "intake", "--json").stdout)
		result := runIn(t, dir, dbEnv, "backlog", "delegate", added.ID, "--json")
		if result.exitCode != 0 {
			t.Fatalf("delegate: exit=%d stderr=%q", result.exitCode, result.stderr)
		}
		return mustJSON[struct {
			Outbox string `json:"outbox"`
		}](t, result.stdout).Outbox
	}

	approvedID := delegate("Approve me")
	declinedID := delegate("Decline me")
	listed := runIn(t, dir, dbEnv, "outbox", "list", "--json")
	if listed.exitCode != 0 {
		t.Fatalf("outbox list: exit=%d stderr=%q", listed.exitCode, listed.stderr)
	}
	var before struct {
		Entries []outboxPayload `json:"entries"`
	}
	if err := json.Unmarshal([]byte(listed.stdout), &before); err != nil || len(before.Entries) != 2 {
		t.Fatalf("outbox list = %q (err %v)", listed.stdout, err)
	}
	for _, entry := range before.Entries {
		if entry.Kind != "create" || entry.State != "queued" || entry.Subject == "" || entry.IdempotencyKey == "" || entry.Attempts != 0 || len(entry.Payload) == 0 {
			t.Fatalf("incomplete queued output: %+v", entry)
		}
	}

	approved := runIn(t, dir, dbEnv, "outbox", "approve", approvedID, "--json")
	if approved.exitCode != 0 || mustJSON[outboxPayload](t, approved.stdout).State != "approved" {
		t.Fatalf("approve: exit=%d stdout=%q stderr=%q", approved.exitCode, approved.stdout, approved.stderr)
	}
	declined := runIn(t, dir, dbEnv, "outbox", "decline", declinedID, "--reason", "not sending", "--json")
	declinedEntry := mustJSON[outboxPayload](t, declined.stdout)
	if declined.exitCode != 0 || declinedEntry.State != "declined" || declinedEntry.Reason != "not sending" {
		t.Fatalf("decline: exit=%d payload=%+v stderr=%q", declined.exitCode, declinedEntry, declined.stderr)
	}

	// The stock binary deliberately has no provider adapter. Invoking flush
	// refuses without changing durable approved work.
	flush := runIn(t, dir, dbEnv, "outbox", "flush", "--json")
	if flush.exitCode == 0 || !strings.Contains(flush.stderr, "no provider seam configured") {
		t.Fatalf("flush without seam: exit=%d stderr=%q", flush.exitCode, flush.stderr)
	}
	dbPath := strings.TrimPrefix(dbEnv[0], "WIP_DB_PATH=")
	s := openTestStore(t, dbPath)
	repo, err := s.Repos(context.Background())
	if err != nil || len(repo) != 1 {
		t.Fatalf("repos = %+v (err %v)", repo, err)
	}
	entry, err := s.OutboxEntry(context.Background(), repo[0].ID, approvedID)
	if err != nil || entry.State != "approved" || entry.Attempts != 0 {
		t.Fatalf("approved entry after refused flush = %+v (err %v)", entry, err)
	}
	events, err := s.EventsOfSubject(context.Background(), approvedID)
	if err != nil || len(events) != 1 || events[0].Type != store.TypeOutboxApproved {
		t.Fatalf("approval events = %+v (err %v)", events, err)
	}

	retry := runIn(t, dir, dbEnv, "outbox", "retry", declinedID, "--json")
	if retry.exitCode == 0 {
		t.Fatalf("retry of declined work unexpectedly succeeded: %q", retry.stdout)
	}
}

func TestManifestIncludesOutboxPlumbing(t *testing.T) {
	dir, dbEnv := setupRepo(t)
	manifest := runIn(t, dir, dbEnv, "manifest", "--json")
	if manifest.exitCode != 0 {
		t.Fatalf("manifest: exit=%d stderr=%q", manifest.exitCode, manifest.stderr)
	}
	for _, name := range []string{"outbox list", "outbox level", "outbox approve", "outbox decline", "outbox retry", "outbox flush"} {
		if !strings.Contains(manifest.stdout, `"name":"`+name+`"`) {
			t.Errorf("manifest is missing %q", name)
		}
	}
}

func TestOutboxLevelTextJSONValidationAndLazyConfig(t *testing.T) {
	dir, dbEnv := setupRepo(t)
	initial := runIn(t, dir, dbEnv, "outbox", "level")
	if initial.exitCode != 0 || initial.stdout != "off\n" {
		t.Fatalf("initial level: exit=%d stdout=%q stderr=%q", initial.exitCode, initial.stdout, initial.stderr)
	}
	set := runIn(t, dir, dbEnv, "outbox", "level", "boundary", "--json")
	if set.exitCode != 0 || mustJSON[struct {
		Level string `json:"level"`
	}](t, set.stdout).Level != "boundary" {
		t.Fatalf("set level: exit=%d stdout=%q stderr=%q", set.exitCode, set.stdout, set.stderr)
	}
	read := runIn(t, dir, dbEnv, "outbox", "level", "--json")
	if read.exitCode != 0 || !strings.Contains(read.stdout, `"level":"boundary"`) {
		t.Fatalf("read level: exit=%d stdout=%q stderr=%q", read.exitCode, read.stdout, read.stderr)
	}
	invalid := runIn(t, dir, dbEnv, "outbox", "level", "verbose")
	if invalid.exitCode == 0 || !strings.Contains(invalid.stderr, "expected off, boundary, or narrated") {
		t.Fatalf("invalid level: exit=%d stderr=%q", invalid.exitCode, invalid.stderr)
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
		t.Fatalf("level config queued %+v, err=%v", entries, err)
	}
	for _, event := range events {
		if strings.Contains(event.Type, "tracker") || strings.Contains(event.Type, "outbox") {
			t.Fatalf("level config emitted %s", event.Type)
		}
	}
}
