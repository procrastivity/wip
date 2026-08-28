package install

import (
	"bytes"
	"errors"
	"strings"
	"testing"

	"github.com/spf13/cobra"

	"github.com/procrastivity/wip/internal/buildinfo"
	"github.com/procrastivity/wip/internal/iostreams"
	"github.com/procrastivity/wip/internal/wiperr"
)

func TestCommand_UnknownHarnessPrecedesManifestBuild(t *testing.T) {
	root := &cobra.Command{Use: "wip"}
	root.AddCommand(&cobra.Command{
		Use:  "malformed",
		RunE: func(*cobra.Command, []string) error { return nil },
	})

	cmd := Command(&iostreams.Streams{Out: &bytes.Buffer{}, Err: &bytes.Buffer{}}, buildinfo.Info{}, root)
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
	root := &cobra.Command{Use: "wip"}

	out := &bytes.Buffer{}
	cmd := Command(&iostreams.Streams{Out: out, Err: &bytes.Buffer{}}, buildinfo.Info{}, root)
	cmd.SetArgs([]string{})
	if err := cmd.Execute(); err != nil {
		t.Fatalf("Execute() = %v, want nil (bare invocation lists targets)", err)
	}

	want := "available harnesses: claude-code, codex, devin, pi, opencode\nusage: wip install <harness>\n"
	if out.String() != want {
		t.Fatalf("output = %q, want %q", out.String(), want)
	}
}

func TestCommand_HelpListsHarnesses(t *testing.T) {
	root := &cobra.Command{Use: "wip"}

	out := &bytes.Buffer{}
	cmd := Command(&iostreams.Streams{Out: out, Err: &bytes.Buffer{}}, buildinfo.Info{}, root)
	cmd.SetOut(out)
	cmd.SetArgs([]string{"--help"})
	if err := cmd.Execute(); err != nil {
		t.Fatalf("Execute() = %v, want nil", err)
	}

	if !strings.Contains(out.String(), "Available harnesses: claude-code, codex, devin, pi, opencode.") {
		t.Fatalf("--help output does not list the harnesses:\n%s", out.String())
	}
}
