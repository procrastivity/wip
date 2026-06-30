# 0015 — setup-agents command copies are generated from the plugin

- Status: accepted
- Date: 2026-06-24
- Source: drift discovered while closing the Brief→Roadmap `next` gap; ADR-0001, ADR-0013

## Context

`wip` ships its `/wip:*` surface twice. The canonical copy is the plugin at the
repo root (`commands/*.md`, loaded when the global wip plugin is enabled). The
second copy lives under `templates/setup/agents/commands/` and is what
`wip-plumbing setup agents` installs into a **plugin-less consumer repo** so that
repo gets project-local `/wip:*` commands. The two are *meant* to be the same
commands; they differ only in how each resolves the plumbing binary:

- the plugin prefers its bundled binary (`$CLAUDE_PLUGIN_ROOT/bin/wip-plumbing`,
  via a `$WIP` resolver), whereas
- the consumer copy must use `command -v wip-plumbing` on `$PATH`
  (`test/test-setup.sh` forbids `bin/wip-plumbing` references in that tree).

Maintained as two hand-edited copies, they drifted badly and **bidirectionally**:
the plugin grew `bundle`/`orchestrate`/`start` commands the consumer tree never
got, while the consumer `intake.md` carried bundle-explode handling the plugin
`intake.md` had lost. The consumer README still advertised only three commands,
`test/test-setup.sh` hard-coded "3 commands" (locking the drift in), and the
consumer plugin manifest's version skewed from the root manifest because
`contrib/release` only ever bumped the root.

## Decision

Make the plugin `commands/*.md` the **single source of truth** and treat the
consumer copies as a **generated, committed** artifact — the same
generated-but-committed pattern as `roles/backends/active.md` (ADR-0013) and the
assembled `.wip/GLOSSARY.md`.

- **`contrib/sync-agents-commands`** regenerates
  `templates/setup/agents/commands/*.md` from `commands/*.md`. The only transform
  is mechanical: replace the plugin's `$CLAUDE_PLUGIN_ROOT` resolver (step 1) with
  the `command -v wip-plumbing` PATH form, and rewrite `"$WIP"` → `wip-plumbing`.
  Everything else — the procedure, prose, links — is copied verbatim. `--check`
  exits non-zero on drift.
- **`make agents-commands`** runs the generator; **`test/test-agents-commands-sync.sh`**
  runs `--check` so `make check` (and CI) fail on any drift, and asserts set
  parity (every plugin command has a copy) plus the no-bundled-path invariant.
- **All six commands mirror** (`intake`, `next`, `status`, `bundle`,
  `orchestrate`, `start`). A plugin-less consumer can now intake *and*
  start/orchestrate/bundle locally — previously it could only intake/next/status.
- **Version parity:** `contrib/release` bumps both `.claude-plugin/plugin.json`
  manifests in lockstep, so the installed consumer tree never skews from the
  release that produced it.

To regenerate cleanly, the plugin `commands/intake.md` was first restored to the
complete, canonical version (bundle classify/route/explode/cleanup steps) so
generation does not drop behavior.

## Consequences

- Drift is now structurally impossible: the consumer copies cannot diverge from
  the plugin without failing `make check`. Editing a consumer copy by hand is a
  mistake the gate catches — edit `commands/*.md` and regenerate.
- The transform is intentionally dumb (resolver + `"$WIP"`). If a future command
  needs consumer-specific content beyond the resolver, the generator — not a
  hand-edit — is the place to express it.
- The consumer README (`templates/setup/agents/.claude-plugin/README.md`) stays
  hand-maintained (it is prose, low-churn) but now documents all six commands and
  points at this ADR; its command table is the one spot that can still drift, by
  inspection rather than by gate.
- `test/test-setup.sh`'s agents file count moves from 10 to 13 (3 → 6 commands).
  **Superseded by the step-05 amendment below:** that 10→13 count described the
  pre-vendored command-install shape. Under ADR-0020's `source: vendored` model,
  `setup agents` installs the **four** flattened role files and **no** commands in
  Round 1 (commands relocate in step-06 / Round 2). The command-copy decision
  above still stands for the `templates/setup/agents/commands/` generated tree;
  only the consumer install *shape* changed.
- **Deferred (Q-05.4, step-05): `doctor` fan-in of the agent drift gate.** The
  `setup agents --check` agent-side drift gate (added in step-05, see amendment
  below) is intentionally **not** wired into `doctor` this step. This repo is
  `source: plugin` — it vendors no agents, so a `doctor` hook would add no
  coverage here. Backlogged for the consumer-repo case: wire `--check`'s agent
  drift detection into `doctor` so a vendored consumer's `make check` surfaces
  agent drift the way it surfaces feature/sentinel drift (see `.wip/backlog.md`).

## Amendment — the transform also governs vendored agents (step-05)

Amended 2026-06-30 (BDS-28, *Flatten vendored orchestration agents*, step-05;
ADR-0020 nominates step-05 as owner of this amendment — `0020-…:128`).

The generated-but-committed, `--check`-gated pattern decided above was scoped to
the consumer **command** copies (`templates/setup/agents/commands/*.md`,
regenerated by `contrib/sync-agents-commands`). ADR-0020 extends the **same
pattern** to the vendored **agent** files: a `source: vendored` install emits
four self-contained `.claude/agents/wip/{orchestrator,coordinator,researcher,builder}.md`
files, flattened from `roles/` against the selected backend, with no `roles/`
and no `active.md` shipped (ADR-0013 consumer context, ADR-0020).

- **The transform grows.** For commands it is the mechanical resolver swap only
  (`$CLAUDE_PLUGIN_ROOT` → `command -v wip-plumbing`, `"$WIP"` → `wip-plumbing`).
  For agents it additionally **resolves the four `@`-includes against the
  selected backend, then emits self-contained agent files** — the backend is
  baked into the inlined `backends/<backend>.md`, so the consumer carries no
  pointer to regenerate.
- **`setup agents --check` is the agent-side drift gate** — the direct analog of
  `contrib/sync-agents-commands --check`. It re-renders the four roles for the
  manifest's recorded `features.orchestration.backend` and `cmp`s each against the
  installed `.claude/agents/wip/<role>.md`; clean → exit 0, any drift/missing →
  exit 4 (kind `agents-drift`). It writes nothing, so the vendored agent copies
  cannot diverge from `roles/` + the selected backend without failing `make
  check`, exactly as the command copies cannot diverge from `commands/`.

This amendment does **not** disturb the command-copy decision: the
`templates/setup/agents/commands/` generated tree, `contrib/sync-agents-commands`,
and `test/test-agents-commands-sync.sh` stand exactly as decided above. It records
that the agent flatten transform now lives under the same governing pattern, and
corrects the superseded file-count consequence.
