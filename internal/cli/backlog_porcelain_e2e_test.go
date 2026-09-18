package cli_test

import (
	"context"
	"strings"
	"testing"
)

func TestBacklogPorcelain_EmptyIsClear(t *testing.T) {
	dir, dbEnv := setupRepo(t)

	for _, args := range [][]string{{"backlog"}, {"backlog", "--full"}} {
		r := runIn(t, dir, dbEnv, args...)
		if r.exitCode != 0 || r.stderr != "" || r.stdout != "backlog is empty\n" {
			t.Fatalf("%v: exit=%d stdout=%q stderr=%q", args, r.exitCode, r.stdout, r.stderr)
		}
	}
	porcelainJSON := runIn(t, dir, dbEnv, "backlog", "--json")
	fullJSON := runIn(t, dir, dbEnv, "backlog", "--full", "--json")
	plumbingJSON := runIn(t, dir, dbEnv, "plumbing", "backlog", "list", "--json")
	if porcelainJSON.exitCode != 0 || fullJSON.exitCode != 0 || plumbingJSON.exitCode != 0 ||
		porcelainJSON.stdout != "{\"entries\":[]}\n" || porcelainJSON.stdout != fullJSON.stdout || porcelainJSON.stdout != plumbingJSON.stdout {
		t.Fatalf("empty JSON differs:\nporcelain: %#v\nfull: %#v\nplumbing: %#v", porcelainJSON, fullJSON, plumbingJSON)
	}
}

func TestBacklogPorcelain_ActiveSetCompactFullJSONAndReadOnly(t *testing.T) {
	dbPath, providers, _ := backlogPushRepo(t, "backlog-porcelain")
	if r := runWithProviders(t, providers, "plumbing", "outbox", "backend", "fake"); r.exitCode != 0 {
		t.Fatalf("set backend: exit=%d stderr=%q", r.exitCode, r.stderr)
	}
	target := mustJSON[nodePayload](t, runWithProviders(t, providers,
		"plumbing", "matter", "create", "--title", "Promotion target", "--json").stdout)

	add := func(title, provenance string, extra ...string) backlogAddPayload {
		t.Helper()
		args := []string{"plumbing", "backlog", "add", "--title", title, "--provenance", provenance, "--json"}
		args = append(args, extra...)
		r := runWithProviders(t, providers, args...)
		if r.exitCode != 0 {
			t.Fatalf("add %q: exit=%d stderr=%q", title, r.exitCode, r.stderr)
		}
		return mustJSON[backlogAddPayload](t, r.stdout)
	}

	entered := add("Active entered", "intake", "--detail", "reproduce after restart")
	pending := add("Pending delivery", "found", "--origin", target.ID, "--detail", "blocked on API")
	pendingResult := runWithProviders(t, providers, "plumbing", "backlog", "delegate", pending.ID, "--json")
	if pendingResult.exitCode != 0 {
		t.Fatalf("delegate pending: exit=%d stderr=%q", pendingResult.exitCode, pendingResult.stderr)
	}
	pending = mustJSON[backlogAddPayload](t, pendingResult.stdout)

	planned := add("Already planned", "intake")
	if r := runWithProviders(t, providers, "plumbing", "backlog", "plan", planned.ID, target.Locator); r.exitCode != 0 {
		t.Fatalf("plan: exit=%d stderr=%q", r.exitCode, r.stderr)
	}
	declined := add("Already declined", "intake")
	if r := runWithProviders(t, providers, "plumbing", "backlog", "decline", declined.ID, "--reason", "not useful"); r.exitCode != 0 {
		t.Fatalf("decline: exit=%d stderr=%q", r.exitCode, r.stderr)
	}
	flushed := add("Already flushed", "intake")
	delegated := mustJSON[backlogAddPayload](t, runWithProviders(t, providers,
		"plumbing", "backlog", "delegate", flushed.ID, "--json").stdout)
	if r := runWithProviders(t, providers, "plumbing", "outbox", "approve", delegated.Outbox); r.exitCode != 0 {
		t.Fatalf("approve: exit=%d stderr=%q", r.exitCode, r.stderr)
	}
	if r := runWithProviders(t, providers, "plumbing", "outbox", "flush"); r.exitCode != 0 {
		t.Fatalf("flush: exit=%d stderr=%q", r.exitCode, r.stderr)
	}

	s := openTestStore(t, dbPath)
	eventsBefore, err := s.Events(context.Background())
	if err != nil {
		t.Fatal(err)
	}

	compact := runWithProviders(t, providers, "backlog")
	if compact.exitCode != 0 || compact.stderr != "" {
		t.Fatalf("backlog: exit=%d stdout=%q stderr=%q", compact.exitCode, compact.stdout, compact.stderr)
	}
	wantCompact := "2 backlog entries:\n  Active entered\n  Pending delivery · delegated\n"
	if compact.stdout != wantCompact {
		t.Errorf("backlog = %q, want %q", compact.stdout, wantCompact)
	}
	for _, absent := range []string{entered.ID, pending.ID, "reproduce after restart", "Already planned", "Already declined", "Already flushed"} {
		if strings.Contains(compact.stdout, absent) {
			t.Errorf("compact backlog = %q, want %q absent", compact.stdout, absent)
		}
	}

	full := runWithProviders(t, providers, "backlog", "--full")
	if full.exitCode != 0 || full.stderr != "" {
		t.Fatalf("backlog --full: exit=%d stdout=%q stderr=%q", full.exitCode, full.stdout, full.stderr)
	}
	for _, want := range []string{
		"2 backlog entries:",
		"Active entered", "id: " + entered.ID, "provenance: intake", "state: entered", "detail: reproduce after restart",
		"Pending delivery", "id: " + pending.ID, "provenance: found", "state: delegated", "detail: blocked on API",
		"origin: " + target.ID, "outbox: " + pending.Outbox,
	} {
		if !strings.Contains(full.stdout, want) {
			t.Errorf("backlog --full = %q, want it to contain %q", full.stdout, want)
		}
	}
	for _, absent := range []string{"Already planned", "Already declined", "Already flushed"} {
		if strings.Contains(full.stdout, absent) {
			t.Errorf("backlog --full = %q, want %q absent", full.stdout, absent)
		}
	}

	porcelainJSON := runWithProviders(t, providers, "backlog", "--json")
	plumbingJSON := runWithProviders(t, providers, "plumbing", "backlog", "list", "--json")
	if porcelainJSON.exitCode != 0 || plumbingJSON.exitCode != 0 || porcelainJSON.stdout != plumbingJSON.stdout {
		t.Fatalf("backlog JSON differs:\nporcelain: exit=%d %q %q\nplumbing: exit=%d %q %q",
			porcelainJSON.exitCode, porcelainJSON.stdout, porcelainJSON.stderr,
			plumbingJSON.exitCode, plumbingJSON.stdout, plumbingJSON.stderr)
	}
	entries := mustJSON[struct {
		Entries []struct {
			ID string `json:"id"`
		} `json:"entries"`
	}](t, porcelainJSON.stdout).Entries
	if len(entries) != 2 || entries[0].ID != entered.ID || entries[1].ID != pending.ID {
		t.Fatalf("active JSON entries = %+v, want entered then pending delegated", entries)
	}

	eventsAfter, err := s.Events(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(eventsAfter) != len(eventsBefore) {
		t.Fatalf("backlog reads appended %d event(s)", len(eventsAfter)-len(eventsBefore))
	}
}

func TestStatusPorcelain_BacklogCountsArePerRepoAndNonEmptyOnly(t *testing.T) {
	dbEnv := []string{"WIP_DB_PATH=" + t.TempDir() + "/wip.db"}
	alpha := newGitRepo(t, "alpha")
	beta := newGitRepo(t, "beta")
	gamma := newGitRepo(t, "gamma")
	gitIn(t, alpha, "remote", "add", "origin", "git@github.com:acme/alpha.git")
	gitIn(t, beta, "remote", "add", "origin", "git@github.com:acme/beta.git")
	gitIn(t, gamma, "remote", "add", "origin", "git@github.com:acme/gamma.git")
	for _, dir := range []string{alpha, beta, gamma} {
		if r := runIn(t, dir, dbEnv, "init"); r.exitCode != 0 {
			t.Fatalf("init %s: exit=%d stderr=%q", dir, r.exitCode, r.stderr)
		}
	}
	for _, title := range []string{"Alpha one", "Alpha two", "Alpha three"} {
		if r := runIn(t, alpha, dbEnv, "plumbing", "backlog", "add", "--title", title, "--provenance", "intake"); r.exitCode != 0 {
			t.Fatalf("add %q: exit=%d stderr=%q", title, r.exitCode, r.stderr)
		}
	}
	if r := runIn(t, beta, dbEnv, "plumbing", "backlog", "add", "--title", "Beta one", "--provenance", "intake"); r.exitCode != 0 {
		t.Fatalf("add beta: exit=%d stderr=%q", r.exitCode, r.stderr)
	}
	if r := runIn(t, beta, dbEnv, "plumbing", "backlog", "add", "--title", "Beta delegated", "--provenance", "intake", "--json"); r.exitCode != 0 {
		t.Fatalf("add delegated beta: exit=%d stderr=%q", r.exitCode, r.stderr)
	} else {
		entry := mustJSON[backlogAddPayload](t, r.stdout)
		if delegated := runIn(t, beta, dbEnv, "plumbing", "backlog", "delegate", entry.ID); delegated.exitCode != 0 {
			t.Fatalf("delegate beta: exit=%d stderr=%q", delegated.exitCode, delegated.stderr)
		}
	}
	gammaTarget := mustJSON[nodePayload](t, runIn(t, gamma, dbEnv, "plumbing", "matter", "create", "--title", "Gamma target", "--json").stdout)
	if r := runIn(t, gamma, dbEnv, "plumbing", "backlog", "add", "--title", "Planned gamma", "--provenance", "intake", "--json"); r.exitCode != 0 {
		t.Fatalf("add planned gamma: exit=%d stderr=%q", r.exitCode, r.stderr)
	} else {
		entry := mustJSON[backlogAddPayload](t, r.stdout)
		if planned := runIn(t, gamma, dbEnv, "plumbing", "backlog", "plan", entry.ID, gammaTarget.Locator); planned.exitCode != 0 {
			t.Fatalf("plan gamma: exit=%d stderr=%q", planned.exitCode, planned.stderr)
		}
	}
	if r := runIn(t, gamma, dbEnv, "plumbing", "backlog", "add", "--title", "Declined gamma", "--provenance", "intake", "--json"); r.exitCode != 0 {
		t.Fatalf("add declined gamma: exit=%d stderr=%q", r.exitCode, r.stderr)
	} else {
		entry := mustJSON[backlogAddPayload](t, r.stdout)
		if declined := runIn(t, gamma, dbEnv, "plumbing", "backlog", "decline", entry.ID, "--reason", "done elsewhere"); declined.exitCode != 0 {
			t.Fatalf("decline gamma: exit=%d stderr=%q", declined.exitCode, declined.stderr)
		}
	}
	s := openTestStore(t, dbEnvPath(dbEnv))
	eventsBefore, err := s.Events(context.Background())
	if err != nil {
		t.Fatal(err)
	}

	alphaStatus := runIn(t, alpha, dbEnv, "status")
	if alphaStatus.exitCode != 0 || !strings.Contains(alphaStatus.stdout, "… 3 backlog — wip backlog") {
		t.Fatalf("alpha status: exit=%d stdout=%q stderr=%q", alphaStatus.exitCode, alphaStatus.stdout, alphaStatus.stderr)
	}
	for _, args := range [][]string{{"status", "--full"}, {"plumbing", "status"}} {
		r := runIn(t, alpha, dbEnv, args...)
		if r.exitCode != 0 || strings.Contains(r.stdout, "backlog —") {
			t.Fatalf("%v should retain its existing human contract: exit=%d stdout=%q stderr=%q", args, r.exitCode, r.stdout, r.stderr)
		}
	}
	statusJSON := runIn(t, alpha, dbEnv, "status", "--json")
	plumbingStatusJSON := runIn(t, alpha, dbEnv, "plumbing", "status", "--json")
	if statusJSON.exitCode != 0 || plumbingStatusJSON.exitCode != 0 || statusJSON.stdout != plumbingStatusJSON.stdout {
		t.Fatalf("status JSON differs after adding backlog entries:\nporcelain: exit=%d %q %q\nplumbing: exit=%d %q %q",
			statusJSON.exitCode, statusJSON.stdout, statusJSON.stderr,
			plumbingStatusJSON.exitCode, plumbingStatusJSON.stdout, plumbingStatusJSON.stderr)
	}
	betaStatus := runIn(t, beta, dbEnv, "status")
	if betaStatus.exitCode != 0 || !strings.Contains(betaStatus.stdout, "… 2 backlog — wip backlog") {
		t.Fatalf("beta status: exit=%d stdout=%q stderr=%q", betaStatus.exitCode, betaStatus.stdout, betaStatus.stderr)
	}
	gammaStatus := runIn(t, gamma, dbEnv, "status")
	if gammaStatus.exitCode != 0 || strings.Contains(gammaStatus.stdout, "backlog —") {
		t.Fatalf("gamma status: exit=%d stdout=%q stderr=%q", gammaStatus.exitCode, gammaStatus.stdout, gammaStatus.stderr)
	}

	hostWide := runIn(t, newGitRepo(t, "outside"), dbEnv, "status")
	if hostWide.exitCode != 0 {
		t.Fatalf("host-wide status: exit=%d stderr=%q", hostWide.exitCode, hostWide.stderr)
	}
	alphaHeader := strings.Index(hostWide.stdout, "acme/alpha")
	betaHeader := strings.Index(hostWide.stdout, "acme/beta")
	gammaHeader := strings.Index(hostWide.stdout, "acme/gamma")
	alphaFooter := strings.Index(hostWide.stdout, "… 3 backlog — wip backlog")
	betaFooter := strings.Index(hostWide.stdout, "… 2 backlog — wip backlog")
	if alphaHeader == -1 || betaHeader == -1 || gammaHeader == -1 ||
		alphaFooter < alphaHeader || alphaFooter > betaHeader || betaFooter < betaHeader || betaFooter > gammaHeader {
		t.Fatalf("host-wide status does not keep alpha's count in alpha's block: %q", hostWide.stdout)
	}
	if strings.Count(hostWide.stdout, "backlog —") != 2 {
		t.Fatalf("host-wide status = %q, want exactly two non-empty per-repo backlog footers", hostWide.stdout)
	}
	eventsAfter, err := s.Events(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(eventsAfter) != len(eventsBefore) {
		t.Fatalf("status reads appended %d event(s)", len(eventsAfter)-len(eventsBefore))
	}
}
