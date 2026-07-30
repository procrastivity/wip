package render

import (
	"bytes"
	"context"
	"fmt"
	"os/exec"
	"strings"
)

// WorktreeRoot runs `git rev-parse --show-toplevel` in dir, absolute-path
// form: the top of the working tree this location checks out to, which is
// where `.wip/` lives (MODEL §3.1). `tiers` already shells out for
// git-common-dir and git-dir; this is the one additional fact this package
// needs that no existing exported helper provides. Exported so `guards` can
// resolve the same root from a bare `dir` for its doctor-side tracked-`.wip/`
// check, which (unlike the render precondition) has no already-resolved
// Current to read it off of.
func WorktreeRoot(ctx context.Context, dir string) (string, error) {
	return runGit(ctx, dir, "rev-parse", "--path-format=absolute", "--show-toplevel")
}

func runGit(ctx context.Context, dir string, args ...string) (string, error) {
	cmd := exec.CommandContext(ctx, "git", args...)
	cmd.Dir = dir
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		return "", fmt.Errorf("git %s: %w: %s", strings.Join(args, " "), err, strings.TrimSpace(stderr.String()))
	}
	return strings.TrimSpace(stdout.String()), nil
}
