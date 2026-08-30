package uninstall

import (
	"bytes"
	"errors"
	"strings"
	"testing"

	"github.com/procrastivity/wip/internal/iostreams"
	"github.com/procrastivity/wip/internal/wiperr"
)

func TestCommand_UnknownHarness(t *testing.T) {
	cmd := Command(&iostreams.Streams{Out: &bytes.Buffer{}, Err: &bytes.Buffer{}})
	cmd.SetArgs([]string{"some-unknown-harness"})
	err := cmd.Execute()

	var got *wiperr.Error
	if !errors.As(err, &got) {
		t.Fatalf("error = %T %v, want *wiperr.Error", err, err)
	}
	if got.Code != "validation.unknown-harness" {
		t.Fatalf("error code = %q, want %q", got.Code, "validation.unknown-harness")
	}
}

func TestCommand_NoArgsListsHarnesses(t *testing.T) {
	out := &bytes.Buffer{}
	cmd := Command(&iostreams.Streams{Out: out, Err: &bytes.Buffer{}})
	cmd.SetArgs([]string{})
	if err := cmd.Execute(); err != nil {
		t.Fatalf("Execute() = %v, want nil (bare invocation lists targets)", err)
	}

	want := "available harnesses: claude-code, amp, codex, devin, pi, opencode\nusage: wip uninstall <harness>\n"
	if out.String() != want {
		t.Fatalf("output = %q, want %q", out.String(), want)
	}
}

func TestCommand_HelpListsHarnesses(t *testing.T) {
	out := &bytes.Buffer{}
	cmd := Command(&iostreams.Streams{Out: out, Err: &bytes.Buffer{}})
	cmd.SetOut(out)
	cmd.SetArgs([]string{"--help"})
	if err := cmd.Execute(); err != nil {
		t.Fatalf("Execute() = %v, want nil", err)
	}

	if !strings.Contains(out.String(), "Available harnesses: claude-code, amp, codex, devin, pi, opencode.") {
		t.Fatalf("--help output does not list the harnesses:\n%s", out.String())
	}
}
