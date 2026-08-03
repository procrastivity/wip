package install

import (
	"bytes"
	"errors"
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
