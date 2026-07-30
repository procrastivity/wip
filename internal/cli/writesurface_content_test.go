// End-to-end tests for the write-surface Matter's content-prose Stage
// (workplans/write-surface.md, step-04), through the actual built binary.
// Argument shape here is the `agent-path` workplan's already-resolved
// decision (stdin/--file for the three create-once verbs; positional
// argument, stdin or --file for `wip finding add`) — these tests exercise
// exactly that shape, not a shape this Stage invented.
package cli_test

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/procrastivity/wip/internal/store"
)

// runInStdin is runIn plus a stdin payload — the content verbs' default
// input path.
func runInStdin(t *testing.T, dir string, env []string, stdin string, args ...string) result {
	t.Helper()
	cmd := exec.Command(binPath, args...)
	cmd.Dir = dir
	if env != nil {
		cmd.Env = append(os.Environ(), env...)
	}
	cmd.Stdin = bytes.NewBufferString(stdin)
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	err := cmd.Run()
	exitCode := 0
	if err != nil {
		exitErr, ok := err.(*exec.ExitError)
		if !ok {
			t.Fatalf("running %v in %s: %v", args, dir, err)
		}
		exitCode = exitErr.ExitCode()
	}
	return result{stdout: stdout.String(), stderr: stderr.String(), exitCode: exitCode}
}

func TestContent_CreateOnceVerbs_OneEventEachKindDiscrimination(t *testing.T) {
	dbEnv := []string{"WIP_DB_PATH=" + filepath.Join(t.TempDir(), "wip.db")}
	dir := newGitRepo(t, "widget")
	if r := runIn(t, dir, dbEnv, "init"); r.exitCode != 0 {
		t.Fatalf("init: exit=%d stderr=%q", r.exitCode, r.stderr)
	}
	m := mustJSON[nodePayload](t, runIn(t, dir, dbEnv, "matter", "create", "--title", "Content matter", "--json").stdout)
	dbPath := dbEnvPath(dbEnv)

	cases := []struct {
		verb string
		kind string
	}{
		{"brief", string(store.KindBrief)},
		{"workplan", string(store.KindWorkplan)},
		{"body", string(store.KindBody)},
	}
	for _, c := range cases {
		r := runInStdin(t, dir, dbEnv, "hello "+c.verb, c.verb, m.ID, "--json")
		if r.exitCode != 0 {
			t.Fatalf("%s (stdin): exit=%d stderr=%q", c.verb, r.exitCode, r.stderr)
		}

		s := openTestStore(t, dbPath)
		events, err := s.EventsOfSubject(context.Background(), m.ID)
		if err != nil {
			t.Fatal(err)
		}
		last := events[len(events)-1]
		if last.Type != store.TypeContentCreated {
			t.Errorf("%s: last event type = %q, want %q", c.verb, last.Type, store.TypeContentCreated)
		}
		var p store.ContentWritten
		if err := json.Unmarshal(last.Payload, &p); err != nil {
			t.Fatal(err)
		}
		if string(p.Kind) != c.kind {
			t.Errorf("%s: payload.kind = %q, want %q", c.verb, p.Kind, c.kind)
		}

		// A second create-once call is refused, not a second event.
		before := len(events)
		refused := runInStdin(t, dir, dbEnv, "second "+c.verb, c.verb, m.ID)
		if refused.exitCode == 0 {
			t.Errorf("%s: a second create-once call should be refused", c.verb)
		}
		s = openTestStore(t, dbPath)
		afterEvents, err := s.EventsOfSubject(context.Background(), m.ID)
		if err != nil {
			t.Fatal(err)
		}
		if len(afterEvents) != before {
			t.Errorf("%s: a refused second write appended %d events, want 0", c.verb, len(afterEvents)-before)
		}
	}
}

func TestContent_FileFlag_SameSinglewriterPath(t *testing.T) {
	dbEnv := []string{"WIP_DB_PATH=" + filepath.Join(t.TempDir(), "wip.db")}
	dir := newGitRepo(t, "widget")
	if r := runIn(t, dir, dbEnv, "init"); r.exitCode != 0 {
		t.Fatalf("init: exit=%d stderr=%q", r.exitCode, r.stderr)
	}
	m := mustJSON[nodePayload](t, runIn(t, dir, dbEnv, "matter", "create", "--title", "File flag matter", "--json").stdout)

	proseFile := filepath.Join(t.TempDir(), "brief.md")
	if err := os.WriteFile(proseFile, []byte("brief via --file"), 0o644); err != nil {
		t.Fatal(err)
	}
	r := runIn(t, dir, dbEnv, "brief", m.ID, "--file", proseFile, "--json")
	if r.exitCode != 0 {
		t.Fatalf("brief --file: exit=%d stderr=%q", r.exitCode, r.stderr)
	}

	s := openTestStore(t, dbPath(dbEnv))
	wantAppendedEvent(t, s, m.ID, 1, store.TypeContentCreated) // m already carried matter.created
	got, err := s.Content(context.Background(), m.ID, store.KindBrief)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != "brief via --file" {
		t.Errorf("content = %q, want %q", got, "brief via --file")
	}

	// The source file is never touched, renamed or deleted (D45).
	if _, err := os.Stat(proseFile); err != nil {
		t.Errorf("--file source should be left untouched: %v", err)
	}
}

func dbPath(dbEnv []string) string { return dbEnvPath(dbEnv) }

// Note: "no stdin and no --file is a usage error" (agent-path step-01) turns
// on stdin being a real terminal (os.ModeCharDevice) — a distinction this
// harness cannot simulate, since exec.Command's stdin, connected or not, is
// never a TTY. That path is exercised by hand (a real terminal invocation
// with neither a pipe nor --file refuses with exit code 2); a piped empty
// stdin, which *is* reachable here, legitimately writes a zero-length
// segment instead (the schema permits byte_len >= 0), so it is not the same
// case and is not asserted here as if it were.

func TestContent_FindingAdd_AccumulatesEachCallItsOwnEvent(t *testing.T) {
	dbEnv := []string{"WIP_DB_PATH=" + filepath.Join(t.TempDir(), "wip.db")}
	dir := newGitRepo(t, "widget")
	if r := runIn(t, dir, dbEnv, "init"); r.exitCode != 0 {
		t.Fatalf("init: exit=%d stderr=%q", r.exitCode, r.stderr)
	}
	m := mustJSON[nodePayload](t, runIn(t, dir, dbEnv, "matter", "create", "--title", "Findings matter", "--json").stdout)
	dbPathStr := dbEnvPath(dbEnv)

	// Positional-argument shape (agent-path's default for finding add).
	r1 := runIn(t, dir, dbEnv, "finding", "add", m.ID, "first finding", "--json")
	if r1.exitCode != 0 {
		t.Fatalf("finding add (positional): exit=%d stderr=%q", r1.exitCode, r1.stderr)
	}
	s := openTestStore(t, dbPathStr)
	e1 := wantAppendedEvent(t, s, m.ID, 1, store.TypeContentAppended) // m already carried matter.created
	var p1 store.ContentWritten
	if err := json.Unmarshal(e1.Payload, &p1); err != nil {
		t.Fatal(err)
	}
	if p1.Kind != store.KindFindings {
		t.Errorf("payload.kind = %q, want findings", p1.Kind)
	}

	// stdin shape, second call: accumulates, does not replace.
	r2 := runInStdin(t, dir, dbEnv, "second finding via stdin", "finding", "add", m.ID, "--json")
	if r2.exitCode != 0 {
		t.Fatalf("finding add (stdin): exit=%d stderr=%q", r2.exitCode, r2.stderr)
	}
	s = openTestStore(t, dbPathStr)
	wantAppendedEvent(t, s, m.ID, 2, store.TypeContentAppended)

	// --file shape, third call.
	findingFile := filepath.Join(t.TempDir(), "finding.txt")
	if err := os.WriteFile(findingFile, []byte("third finding via file"), 0o644); err != nil {
		t.Fatal(err)
	}
	r3 := runIn(t, dir, dbEnv, "finding", "add", m.ID, "--file", findingFile, "--json")
	if r3.exitCode != 0 {
		t.Fatalf("finding add (--file): exit=%d stderr=%q", r3.exitCode, r3.stderr)
	}
	s = openTestStore(t, dbPathStr)
	wantAppendedEvent(t, s, m.ID, 3, store.TypeContentAppended)

	all, err := s.ContentSegments(context.Background(), m.ID, store.KindFindings)
	if err != nil {
		t.Fatal(err)
	}
	if len(all) != 3 {
		t.Fatalf("findings segments = %d, want 3 (each call accumulates, none replaces)", len(all))
	}
	concatenated, err := s.Content(context.Background(), m.ID, store.KindFindings)
	if err != nil {
		t.Fatal(err)
	}
	want := "first finding" + "second finding via stdin" + "third finding via file"
	if string(concatenated) != want {
		t.Errorf("concatenated findings = %q, want %q", concatenated, want)
	}
}
