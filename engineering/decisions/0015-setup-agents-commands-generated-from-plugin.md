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
