package cli

import (
	"errors"
	"fmt"
	"strings"

	"github.com/spf13/cobra"

	"github.com/procrastivity/wip/internal/exitcode"
	"github.com/procrastivity/wip/internal/iostreams"
	"github.com/procrastivity/wip/internal/wiperr"
)

// Execute runs root and returns the process exit code, per the chassis
// Brief's exit-code table. It distinguishes a Cobra argument-parsing error
// (code 2) from a verb-raised structured error (codes 1/3/4, read off the
// error's own code field) — two distinct return paths, not one blanket
// non-zero exit.
func Execute(root *cobra.Command, streams *iostreams.Streams) int {
	cmd, err := root.ExecuteC()
	if err == nil {
		return exitcode.Success
	}

	var werr *wiperr.Error
	if errors.As(err, &werr) {
		jsonOut, _ := cmd.Flags().GetBool("json")
		wiperr.Render(streams.Err, verbPath(root, cmd), werr, jsonOut)
		return exitcode.FromError(werr)
	}

	// Anything that isn't our own structured error type reached here via
	// Cobra's own argument-parsing path (bad flags, unknown command,
	// missing required arg) — a usage error, not a verb failure.
	_, _ = fmt.Fprintf(streams.Err, "wip: %s\n", err)
	return exitcode.Usage
}

// verbPath names the failing verb for the human-mode "wip: <verb>: <message>"
// line — the full subcommand path minus the root command's own name, so
// nested verbs (e.g. "gate declare") read naturally without redesign later.
func verbPath(root, cmd *cobra.Command) string {
	path := strings.TrimPrefix(cmd.CommandPath(), root.Name())
	return strings.TrimSpace(path)
}
