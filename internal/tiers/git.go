package tiers

import (
	"bytes"
	"context"
	"fmt"
	"os/exec"
	"strings"
)

// gitCommonDir runs `git rev-parse --git-common-dir` in dir, absolute-path
// form, and returns what it reports — the Clone natural key (MODEL §7, D37).
// It resolves identically from a subdirectory and from a linked worktree,
// which is the load-bearing property the whole tier scheme rests on.
func gitCommonDir(ctx context.Context, dir string) (string, error) {
	return runGit(ctx, dir, "rev-parse", "--path-format=absolute", "--git-common-dir")
}

// gitDir runs `git rev-parse --git-dir` in dir, absolute-path form. It
// differs from gitCommonDir exactly when dir is a linked worktree: git-dir
// then names that worktree's own `<main>/.git/worktrees/<name>` directory,
// while git-common-dir still names the main clone's `.git`. Comparing the
// two is how wip tells "this is the main worktree (or an ordinary clone with
// none)" from "this is a linked worktree, and its name is git-dir's own
// basename" — which is exactly the name git itself uses as the Worktree's
// natural key (D37).
func gitDir(ctx context.Context, dir string) (string, error) {
	return runGit(ctx, dir, "rev-parse", "--path-format=absolute", "--git-dir")
}

// gitRemotes lists dir's configured remotes by name, each mapped to its
// fetch URL (`git remote get-url <name>`).
func gitRemotes(ctx context.Context, dir string) (map[string]string, error) {
	out, err := runGit(ctx, dir, "remote")
	if err != nil {
		return nil, err
	}
	remotes := map[string]string{}
	if out == "" {
		return remotes, nil
	}
	for _, name := range strings.Split(out, "\n") {
		name = strings.TrimSpace(name)
		if name == "" {
			continue
		}
		u, err := runGit(ctx, dir, "remote", "get-url", name)
		if err != nil {
			return nil, err
		}
		remotes[name] = u
	}
	return remotes, nil
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
