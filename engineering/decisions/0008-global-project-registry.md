# 0008 — Global project registry as a derived cache

- Status: accepted
- Date: 2026-06-13
- Source: scratch plan `.wip/scratch/global-project-entry.md` (2026-06-13); ADR-0002

## Context

ADR-0002 makes `.wip.yaml` the single source of truth and every `wip-plumbing` verb
walks up from `$PWD` to find it. That is correct for *durable* state — findings (w4 §R5)
explicitly reject machine-local global state. But it leaves two adjacent gaps:

1. **No enumeration.** There is no way to ask "which projects on this machine use
   `wip`?" Claude Code's `~/.claude/projects/` directory answers the analogous question
   for free; xcind's `~/.local/state/xcind/workspaces.tsv` does the same; `wip` writes
   nothing outside the project tree, so it cannot.
2. **No way to operate on a project from outside its tree.** Every verb requires
   either `cd <project>` or `WIP_ROOT=<abs-path>`. There is no friendlier identifier.

## Decision

Add an **opt-out JSONL registry** at `$XDG_STATE_HOME/wip/projects.jsonl` (default
`~/.local/state/wip/projects.jsonl`), written by every `wip-plumbing` verb that
successfully resolves a `.wip.yaml`. The registry powers two surfaces:

- a new `wip-plumbing project` verb (`list` / `register` / `resolve` / `forget`);
- a `--project <id>` flag on every walk-up verb, accepting an absolute path, a
  dash-encoded path segment (clast-style, e.g. `-Users-beausimensen-Code-wip`), or an
  opt-in human slug.

The registry is a **derived cache.** `.wip.yaml` remains source of truth. Detection
never reads the registry. Deleting `~/.local/state/wip/` must change no behavior
except `project list` enumeration and bare `--project <id>` resolution (`--project
<abs-path>` still works without it).

Opt-out: `WIP_NO_REGISTRY=1` (env) or `plumbing.register: false` in `.wip.yaml` (for
sandboxes / shared trees). Registry write errors never fail the calling verb.

The dash-encoded path segment is the canonical identifier — deterministic, stable,
reversible, no naming required. The slug is an opt-in convenience set via `project
register --slug` (or read from a future top-level `slug:` in `.wip.yaml`).

## Consequences

- ADR-0002 holds: the manifest is still the only source of truth. The registry is a
  rebuildable cache; any verb's first invocation after `rm -rf ~/.local/state/wip`
  recreates the record for that repo.
- New surface area is deterministic and small: one file under XDG state, four
  subcommands, one flag. No LLM, no judgment — it stays in the plumbing layer.
- The pattern (XDG state file + flock + atomic rename + auto-touch on every invocation)
  is lifted from xcind verbatim; the schema is JSONL instead of TSV so per-record
  metadata (`slug`, `remote`, timestamps) can grow without breaking compatibility.
- Future "concept view" — grouping worktrees of the same project by `remote` — becomes
  a pure read over this registry. Out of scope here; the `remote` field is captured to
  enable it later.
