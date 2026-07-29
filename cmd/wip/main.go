// Command wip is the entrypoint for the wip CLI. It does nothing beyond
// constructing the root command, calling Execute, and mapping the result to
// an exit code — every other concern belongs to internal/cli and the verb
// packages it registers.
package main

import (
	"os"

	"github.com/procrastivity/wip/internal/buildinfo"
	"github.com/procrastivity/wip/internal/cli"
	"github.com/procrastivity/wip/internal/iostreams"
)

// version, commit, and date are set via -ldflags at build time. Both the
// Makefile's build/cross-compile targets and the Nix buildGoModule package
// target this exact package path and these exact var names (chassis Brief,
// "Version metadata") — a locally-built binary and a Nix-built one carry
// identical labels.
var (
	version = "dev"
	commit  = "unknown"
	date    = "unknown"
)

func main() {
	streams := iostreams.System()
	build := buildinfo.Info{Version: version, Commit: commit, Date: date}
	root := cli.NewRootCommand(streams, build)
	os.Exit(cli.Execute(root, streams))
}
