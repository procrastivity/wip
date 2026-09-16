package cli_test

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/procrastivity/wip/internal/buildinfo"
	"github.com/procrastivity/wip/internal/cli"
	"github.com/procrastivity/wip/internal/iostreams"
	"github.com/procrastivity/wip/internal/store"
	"github.com/procrastivity/wip/internal/tracker"
)

type cliAlignmentReader struct {
	states map[string]tracker.LiveState
	reads  []string
}

func (*cliAlignmentReader) Deliver(context.Context, store.OutboxEntry) (tracker.Result, error) {
	return tracker.Result{}, nil
}

func (f *cliAlignmentReader) ReadState(_ context.Context, ref string) (tracker.LiveState, error) {
	f.reads = append(f.reads, ref)
	return f.states[ref], nil
}

func runWithProviders(t *testing.T, providers *tracker.Registry, args ...string) result {
	t.Helper()
	var stdout, stderr bytes.Buffer
	streams := &iostreams.Streams{Out: &stdout, Err: &stderr}
	root := cli.NewRootCommandWithProviders(streams, buildinfo.Info{Version: "test", Commit: "test", Date: "test"}, providers)
	root.SetArgs(args)
	code := cli.Execute(root, streams)
	return result{stdout: stdout.String(), stderr: stderr.String(), exitCode: code}
}

func TestPostSealAlignmentThroughFinishAndFinalGate(t *testing.T) {
	dir := t.TempDir()
	if out, err := exec.Command("git", "init", "-q", dir).CombinedOutput(); err != nil {
		t.Fatalf("git init: %v\n%s", err, out)
	}
	previousDir, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Chdir(dir); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chdir(previousDir) })
	t.Setenv("WIP_DB_PATH", filepath.Join(t.TempDir(), "wip.db"))

	reader := &cliAlignmentReader{states: map[string]tracker.LiveState{
		"R-behind": {Class: tracker.LiveActive, Display: "In Progress", Lease: "lease-1"},
		"R-gated":  {Class: tracker.LiveCompleted, Display: "Done", Lease: "lease-2"},
	}}
	providers := tracker.NewRegistry()
	providers.Register("fake", func(tracker.FactoryInput) (tracker.Seam, error) { return reader, nil })

	for _, args := range [][]string{
		{"init", "--json"},
		{"plumbing", "outbox", "backend", "fake"},
		{"plumbing", "matter", "create", "--title", "Behind", "--locator", "behind"},
		{"plumbing", "bind", "behind", "R-behind"},
		{"plumbing", "start", "behind"},
	} {
		if r := runWithProviders(t, providers, args...); r.exitCode != 0 {
			t.Fatalf("%v: exit=%d stderr=%q", args, r.exitCode, r.stderr)
		}
	}
	finished := runWithProviders(t, providers, "plumbing", "finish", "behind", "--json")
	if finished.exitCode != 0 || finished.stderr != "" {
		t.Fatalf("finish: exit=%d stdout=%q stderr=%q", finished.exitCode, finished.stdout, finished.stderr)
	}
	var finishPayload struct {
		Lifecycle string                   `json:"lifecycle"`
		Alignment *tracker.AlignmentReport `json:"alignment"`
	}
	if err := json.Unmarshal([]byte(finished.stdout), &finishPayload); err != nil {
		t.Fatalf("finish JSON = %q: %v", finished.stdout, err)
	}
	if finishPayload.Lifecycle != "done" || finishPayload.Alignment == nil || finishPayload.Alignment.Items[0].Classification != tracker.AlignmentBehind {
		t.Fatalf("finish payload = %+v", finishPayload)
	}

	for _, args := range [][]string{
		{"plumbing", "gate", "declare", "reviewed-local", "--scale", "matter"},
		{"plumbing", "matter", "create", "--title", "Gated", "--locator", "gated"},
		{"plumbing", "bind", "gated", "R-gated"},
		{"plumbing", "start", "gated"},
	} {
		if r := runWithProviders(t, providers, args...); r.exitCode != 0 {
			t.Fatalf("%v: exit=%d stderr=%q", args, r.exitCode, r.stderr)
		}
	}
	reader.reads = nil
	if r := runWithProviders(t, providers, "plumbing", "finish", "gated"); r.exitCode != 0 || len(reader.reads) != 0 {
		t.Fatalf("unsealed finish: exit=%d stdout=%q stderr=%q reads=%v", r.exitCode, r.stdout, r.stderr, reader.reads)
	}
	closed := runWithProviders(t, providers, "plumbing", "gate", "close", "reviewed-local", "gated")
	if closed.exitCode != 0 || closed.stderr != "" || closed.stdout != "closed reviewed-local on gated\n" {
		t.Fatalf("aligned final gate: exit=%d stdout=%q stderr=%q", closed.exitCode, closed.stdout, closed.stderr)
	}
	if !reflect.DeepEqual(reader.reads, []string{"R-gated"}) {
		t.Fatalf("final-gate reads = %v, want one aligned read", reader.reads)
	}
}

func TestPostSealAlignmentReadsAtPushOffAndBackendNoneIsNonfatal(t *testing.T) {
	dir := t.TempDir()
	if out, err := exec.Command("git", "init", "-q", dir).CombinedOutput(); err != nil {
		t.Fatalf("git init: %v\n%s", err, out)
	}
	previousDir, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Chdir(dir); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chdir(previousDir) })
	t.Setenv("WIP_DB_PATH", filepath.Join(t.TempDir(), "wip.db"))

	reader := &cliAlignmentReader{states: map[string]tracker.LiveState{
		"R-off":     {Class: tracker.LiveActive, Display: "In Progress", Lease: "lease-off"},
		"R-aligned": {Class: tracker.LiveCompleted, Display: "Done", Lease: "lease-done"},
	}}
	providers := tracker.NewRegistry()
	providers.Register("fake", func(tracker.FactoryInput) (tracker.Seam, error) { return reader, nil })

	for _, args := range [][]string{
		{"init", "--json"},
		{"plumbing", "outbox", "backend", "fake"},
		{"plumbing", "outbox", "level", "off"},
		{"plumbing", "matter", "create", "--title", "Push off", "--locator", "push-off"},
		{"plumbing", "bind", "push-off", "R-off"},
		{"plumbing", "start", "push-off"},
	} {
		if r := runWithProviders(t, providers, args...); r.exitCode != 0 {
			t.Fatalf("%v: exit=%d stderr=%q", args, r.exitCode, r.stderr)
		}
	}
	finished := runWithProviders(t, providers, "plumbing", "finish", "push-off")
	if finished.exitCode != 0 || finished.stderr != "" || !strings.Contains(finished.stdout, "offer: move to completed") {
		t.Fatalf("push-off finish: exit=%d stdout=%q stderr=%q", finished.exitCode, finished.stdout, finished.stderr)
	}
	if !reflect.DeepEqual(reader.reads, []string{"R-off"}) {
		t.Fatalf("push-off reads = %v, want R-off once", reader.reads)
	}
	listed := runWithProviders(t, providers, "plumbing", "outbox", "list", "--json")
	var outbox struct {
		Entries []json.RawMessage `json:"entries"`
	}
	if listed.exitCode != 0 || json.Unmarshal([]byte(listed.stdout), &outbox) != nil || len(outbox.Entries) != 0 {
		t.Fatalf("push-off outbox = exit %d, stdout %q, stderr %q", listed.exitCode, listed.stdout, listed.stderr)
	}

	for _, args := range [][]string{
		{"plumbing", "outbox", "backend", "none"},
		{"plumbing", "matter", "create", "--title", "No backend", "--locator", "no-backend"},
		{"plumbing", "bind", "no-backend", "R-none"},
		{"plumbing", "start", "no-backend"},
	} {
		if r := runWithProviders(t, providers, args...); r.exitCode != 0 {
			t.Fatalf("%v: exit=%d stderr=%q", args, r.exitCode, r.stderr)
		}
	}
	unavailable := runWithProviders(t, providers, "plumbing", "finish", "no-backend", "--json")
	var unavailablePayload struct {
		Alignment *tracker.AlignmentReport `json:"alignment"`
	}
	if unavailable.exitCode != 0 || json.Unmarshal([]byte(unavailable.stdout), &unavailablePayload) != nil || unavailablePayload.Alignment == nil || unavailablePayload.Alignment.Items[0].Classification != tracker.AlignmentUnavailable {
		t.Fatalf("backend-none finish: exit=%d stdout=%q stderr=%q", unavailable.exitCode, unavailable.stdout, unavailable.stderr)
	}

	for _, args := range [][]string{
		{"plumbing", "outbox", "backend", "fake"},
		{"plumbing", "matter", "create", "--title", "Aligned JSON", "--locator", "aligned-json"},
		{"plumbing", "bind", "aligned-json", "R-aligned"},
		{"plumbing", "start", "aligned-json"},
	} {
		if r := runWithProviders(t, providers, args...); r.exitCode != 0 {
			t.Fatalf("%v: exit=%d stderr=%q", args, r.exitCode, r.stderr)
		}
	}
	aligned := runWithProviders(t, providers, "plumbing", "finish", "aligned-json", "--json")
	var alignedPayload map[string]json.RawMessage
	if aligned.exitCode != 0 || json.Unmarshal([]byte(aligned.stdout), &alignedPayload) != nil {
		t.Fatalf("aligned JSON finish: exit=%d stdout=%q stderr=%q", aligned.exitCode, aligned.stdout, aligned.stderr)
	}
	if _, present := alignedPayload["alignment"]; present {
		t.Fatalf("aligned JSON unexpectedly contains alignment: %s", aligned.stdout)
	}
}

func TestRrulerReplayAlreadyDoneIssueProducesNoCloseOffer(t *testing.T) {
	dir := t.TempDir()
	if out, err := exec.Command("git", "init", "-q", dir).CombinedOutput(); err != nil {
		t.Fatalf("git init: %v\n%s", err, out)
	}
	previousDir, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Chdir(dir); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chdir(previousDir) })
	t.Setenv("WIP_DB_PATH", filepath.Join(t.TempDir(), "wip.db"))

	reader := &cliAlignmentReader{states: map[string]tracker.LiveState{
		"BDS-132": {Class: tracker.LiveCompleted, Display: "Done", Lease: "2026-08-20T13:48:49Z"},
	}}
	providers := tracker.NewRegistry()
	providers.Register("linear-fixture", func(tracker.FactoryInput) (tracker.Seam, error) { return reader, nil })
	for _, args := range [][]string{
		{"init", "--json"},
		{"plumbing", "outbox", "backend", "linear-fixture"},
		{"plumbing", "outbox", "level", "off"},
		{"plumbing", "gate", "declare", "reviewed-local", "--scale", "matter"},
		{"plumbing", "matter", "create", "--title", "Release framing", "--locator", "v001-release-framing"},
		{"plumbing", "bind", "v001-release-framing", "BDS-132"},
		{"plumbing", "start", "v001-release-framing"},
		{"plumbing", "finish", "v001-release-framing"},
	} {
		if r := runWithProviders(t, providers, args...); r.exitCode != 0 {
			t.Fatalf("%v: exit=%d stdout=%q stderr=%q", args, r.exitCode, r.stdout, r.stderr)
		}
	}
	reader.reads = nil
	closed := runWithProviders(t, providers, "plumbing", "gate", "close", "reviewed-local", "v001-release-framing")
	if closed.exitCode != 0 || closed.stderr != "" || closed.stdout != "closed reviewed-local on v001-release-framing\n" {
		t.Fatalf("rruler final gate: exit=%d stdout=%q stderr=%q", closed.exitCode, closed.stdout, closed.stderr)
	}
	if !reflect.DeepEqual(reader.reads, []string{"BDS-132"}) {
		t.Fatalf("rruler reads = %v, want one BDS-132 read", reader.reads)
	}
	listed := runWithProviders(t, providers, "plumbing", "outbox", "list", "--json")
	var outbox struct {
		Entries []json.RawMessage `json:"entries"`
	}
	if listed.exitCode != 0 || json.Unmarshal([]byte(listed.stdout), &outbox) != nil || len(outbox.Entries) != 0 {
		t.Fatalf("rruler outbox = exit %d, stdout %q, stderr %q", listed.exitCode, listed.stdout, listed.stderr)
	}
}
