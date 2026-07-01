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

## Amendment — the vendored install relocates the slash-commands to `.claude/commands/wip/` (step-06)

Amended 2026-06-30 (BDS-28, *Flatten vendored orchestration agents*, step-06,
Round 2; this ADR's own step-05 consequence above nominated step-06 as the owner
of the command relocation — "commands relocate in step-06 / Round 2").

The step-05 amendment relocated the vendored **agents** to `.claude/agents/wip/`
and deferred the **commands**. Step-06 completes Round 2's first half: a
`source: vendored` install now also carries the wip slash-commands under
`.claude/commands/` — no plugin tree — mirroring the agent relocation.

- **Layout.** For each canonical command the vendored path copies
  `templates/setup/agents/commands/<name>.md` →
  `.claude/commands/wip/<name>.md`. The `wip/` subdirectory is the parallel of
  the agents' `.claude/agents/wip/` layout (ADR-0020 D1): one removable directory
  per surface, trivially detectable for the step-07 migration.
- **`/wip:<name>` colon parity (empirically verified).** The `wip/` subdirectory
  is what yields the `/wip:<name>` invocation — **byte-for-byte the same typed
  name a `source: plugin` consumer gets from the global wip plugin**. This rests
  on a live empirical probe of the current Claude Code runtime (human +
  Orchestrator, 2026-06-30): a command file in a **subdirectory** of
  `.claude/commands/` is namespaced with a **colon** —
  `.claude/commands/nstest/alpha.md` loaded and was invoked as `/nstest:alpha`,
  generalizing to `.claude/commands/<subdir>/<name>.md` → `/<subdir>:<name>`. A
  counter-probe scoped the rule: a skill with an intermediate directory
  (`.claude/skills/nstest/bravo/SKILL.md`) did **not** load, so the colon route
  is specific to `.claude/commands/<subdir>/`, not to `.claude/skills/<subdir>/`.
- **CAVEAT — this subdir→colon mapping is UNDOCUMENTED.** The `/wip:` parity
  depends entirely on runtime behavior the Claude Code docs do **not** describe.
  The docs (`https://code.claude.com/docs/en/slash-commands`) document only the
  **flat** case — a file under `.claude/commands/` takes its filename as the
  command name (`deploy.md` → `/deploy`) — and are **silent** on the subdirectory
  case. The colon namespacing is the current real behavior and is semantically
  consistent with plugin namespacing (`plugin-name:command`), but because it is
  undocumented it **could change** in a future Claude Code version. If it ever
  regresses to filename-only, the documented **back-pocket fallback** is flat
  `wip-<name>.md` files (the same form the rejected hyphen variant would have
  used); the four agent files are unaffected (separate mechanism, agent identity
  rides `name:` frontmatter). Note that `setup agents --check` (below) verifies
  the **file layout**, not the `/wip:<name>` invocation, so a runtime regression
  would **not** be caught by `make check` — it would surface as a user seeing
  `/wip:intake` no longer resolve. Future maintainers: this is a known,
  logged dependency.
- **Pure resolver-swap — the transform is already baked in (D3).** Unlike the
  agents, which flatten/render `@`-includes at install time, the commands
  relocate **verbatim**: no flatten, no `@`-include resolution, no content
  rewrite. The resolver swap decided in this ADR (`$CLAUDE_PLUGIN_ROOT` →
  `command -v wip-plumbing`, `"$WIP"` → `wip-plumbing`) is **already applied** in
  the committed `templates/setup/agents/commands/*.md` copies (regenerated by
  `contrib/sync-agents-commands`, gated by `test-agents-commands-sync.sh`). So the
  install-time action is a plain idempotent file copy — the destination
  **filename and bytes are identical** to the template; only the destination
  directory (the `wip/` subfolder) differs. Step-06 is a relocation, not a
  re-render.
- **Set-parity, never a hardcoded count (D4).** The vendored path iterates the
  template set by **glob** (`templates/setup/agents/commands/*.md`) — the same
  set-parity contract this ADR already enforces for the generated tree — so every
  canonical command installs automatically and a future addition cannot be
  silently dropped. The historical "six `/wip:*` commands" label is stale; the
  canonical set now holds nine (`bundle`, `complete-review`, `intake`, `next`,
  `orchestrate`, `review`, `start`, `status`, `sync`), and set-parity — not a
  literal count — is what governs.
- **`source` semantics unchanged (ADR-0020).** `source: vendored` writes the
  agents **and** the commands; `source: plugin` vendors **nothing** (no
  `.claude/agents/`, no `.claude/commands/`) and relies on the globally-enabled
  wip plugin's `/wip:*`. The vendored-path conservative-write guard (a foreign
  root `.claude-plugin/plugin.json` → refuse, write nothing) already fences the
  command write, since the commands land inside the same `source == vendored`
  branch — no new guard needed.
- **`setup agents --check` extends to the commands.** The read-only agent-side
  drift gate now also `cmp`s each installed `.claude/commands/wip/<name>.md`
  against its `templates/setup/agents/commands/<name>.md` (a direct template
  `cmp`, no re-render — because of D3); a missing or drifted command is the same
  drift exit (rc 4, kind `agents-drift` — the unified vendored-drift gate). This
  proves the file **layout**, not the invocation (see the caveat above).
