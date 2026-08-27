// Package trackedwip implements guards step-04: the tracked-`.wip/` check
// and the render precondition. A tracked `.wip/` had inverted meaning under
// the old design; tracked-ness, never mere presence, is the test (MODEL
// §11). One function (IsWipTracked), two callers: CheckTrackedWipDir (the
// `doctor` check) and RenderPrecondition, wired into `render-scratch`
// step-09's already-exposed precondition hook (internal/render.Refresh /
// Render / Exit) by internal/verbs/refresh, finish, and gate close,
// refusing before any write reaches `.wip/generated/` or `.wip/work/`.
//
// This lives in its own subpackage, separate from internal/guards' other
// three checks, purely to avoid an import cycle: internal/render itself
// imports internal/writesurface (for node resolution), and internal/
// writesurface imports internal/guards (for the gate-order precondition,
// step-03) — so internal/guards proper cannot also import internal/render.
// Splitting the one check that genuinely needs render.Current out here
// keeps that edge one-directional.
package trackedwip

import (
	"bytes"
	"context"
	"fmt"
	"os/exec"
	"strings"

	"github.com/procrastivity/wip/internal/guards"
	"github.com/procrastivity/wip/internal/render"
	"github.com/procrastivity/wip/internal/store"
	"github.com/procrastivity/wip/internal/wiperr"
)

// code is vocabulary step-09's ratified tracked-`.wip/` refusal code.
const code = "refusal.tracked-wip-dir"

// message is vocabulary step-09's ratified `--json` body, reused as the one
// Message a *wiperr.Error carries in both modes — the same convention
// `tiers`'s and `render-scratch`'s own refusals already follow (and the
// exact string `internal/selftest`'s fixture command manufactures ahead of
// this Matter existing, "the proving case both Matters converged on
// independently").
const message = "refused — .wip/ is tracked by git in this repo; untrack it and add it to .git/info/exclude, then re-run"

// IsWipTracked reports whether `.wip/` has any file tracked by git in the
// worktree rooted at root — `git ls-files -- .wip` against the index, not a
// check of what happens to exist on disk right now.
func IsWipTracked(ctx context.Context, root string) (bool, error) {
	cmd := exec.CommandContext(ctx, "git", "ls-files", "--", ".wip")
	cmd.Dir = root
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		return false, fmt.Errorf("trackedwip: checking whether .wip/ is tracked: %w: %s", err, strings.TrimSpace(stderr.String()))
	}
	return strings.TrimSpace(stdout.String()) != "", nil
}

// RenderPrecondition is the render.Precondition guards supplies to
// render-scratch's hook: a live refusal of an in-progress render, raised
// before EnsureLayout or any generated/scratch write reaches disk.
func RenderPrecondition(ctx context.Context, cur render.Current) error {
	tracked, err := IsWipTracked(ctx, cur.Root)
	if err != nil {
		return err
	}
	if !tracked {
		return nil
	}
	return wiperr.New(code, message)
}

// CheckTrackedWipDir is the `doctor` check: the same condition, reported as
// a finding ("found: …") rather than raised as a live refusal.
func CheckTrackedWipDir(ctx context.Context, _ *store.Store, dir string) ([]guards.Finding, error) {
	root, err := render.WorktreeRoot(ctx, dir)
	if err != nil {
		return nil, err
	}
	tracked, err := IsWipTracked(ctx, root)
	if err != nil {
		return nil, err
	}
	if !tracked {
		return nil, nil
	}
	return []guards.Finding{{
		Code:    code,
		Message: "found: " + strings.TrimPrefix(message, "refused — "),
	}}, nil
}
