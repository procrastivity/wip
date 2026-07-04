# 0020 — Vendored orchestration ships flattened agents, not a plugin tree

- Status: accepted
- Date: 2026-06-29
- Source: BDS-28 *Flatten vendored orchestration agents (drop the plugin-shaped tree)* / initiative BRIEF; ADR-0001, ADR-0005, ADR-0007, ADR-0013, ADR-0015

## Context

`wip-plumbing setup agents` installs orchestration into a consumer repo by
vendoring a **full Claude Code plugin tree** (`.claude-plugin/plugin.json` +
`commands/` + `agents/` + `roles/`) at the repo root. Four findings drive a
rewrite — all worth fixing regardless of approach:

- **F1 — plugin-in-plugin collision (the trigger).** A repo root can hold only
  one `.claude-plugin/plugin.json`. Vendoring wip's manifest (named `wip`) into
  a repo that is *itself* a plugin (e.g. `clast`, `prtend`) clobbers the host
  plugin's own manifest. There is no clean way to host two plugin manifests at
  one plugin root.
- **F2 — `source: plugin` vs vendored mismatch.** `setup agents` vendors a full
  copy yet labels it `source: plugin`, while `agents/README.md` says per-project
  switching "assumes a vendored install (`source: vendored`)". The flag and the
  artifact disagree; a true `source: plugin` install should reference the global
  plugin's `roles/`, not carry its own copy.
- **F3 — vendoring `roles/` contradicts the stated philosophy.** ADR-0005 and
  `roles/README.md` make Roles centrally-shipped tooling a consumer *references*
  rather than copies — "this is what kills the copy-drift seen across
  hand-vendored playbooks." The current vendor path reintroduces exactly that
  copy.
- **F4 — phantom README agent.** `agents/README.md` is globbed as a subagent.
  Folds into the rewrite for free: the flattened layout ships no `agents/`
  README.

The fix stops shipping a plugin-shaped tree for the vendored case: `setup
agents` resolves each agent's four `@`-includes at install time against the
selected backend and emits **fully-formed, self-contained** agent files into
project scope — no `.claude-plugin/` manifest, no vendored `roles/`, no runtime
`@`-includes, no `active.md` pointer. The agents still shell out to
`wip-plumbing` (ADR-0001 unchanged); they just stop importing external files at
runtime. This ADR records the findings and the load-bearing decisions; the
renderer (step-02) and the `setup agents` rewrite (step-03) build against them.

### O1 — the load-bearing product fact

Verified against current Claude Code docs (fetched 2026-06-29):
`https://code.claude.com/docs/en/sub-agents` (canonical; the old
`docs.anthropic.com/en/docs/claude-code/sub-agents` 301-redirects here).

- **Project subagents live in `.claude/agents/`** (scope priority above user
  `~/.claude/agents/` and plugin `agents/`).
- **Discovery is recursive.** Claude Code scans `.claude/agents/` and
  `~/.claude/agents/` recursively, so definitions can be organized into
  subfolders — nested `.claude/agents/wip/*.md` **is** discovered.
- **Identity = `name:` frontmatter, path-independent (project/user scope).** The
  subdirectory path and filename do not affect how a project/user subagent is
  identified or invoked; identity comes only from the `name` field, and the
  filename doesn't have to match.
- **Plugin scope differs.** A subfolder inside a *plugin's* `agents/` directory
  **does** join the scoped id — a file at `agents/review/security.md` in plugin
  `my-plugin` registers as `my-plugin:review:security`. This asymmetry is
  exactly why the flattened artifact must land in **project** scope, never as a
  plugin subtree: in project scope the `wip/` subfolder is free; in plugin scope
  it would rewrite the spawn names.
- **Name-collision rule.** Within one scope, two files with the same `name` →
  one silently wins; `name` values must be unique tree-wide. The four
  `wip-<role>` names are unique by construction.

## Decision

Vendored orchestration ships **flattened agent files in project scope**, with
the following pinned decisions.

- **D1 — Output layout is nested under `.claude/agents/wip/`.** The vendored
  install emits exactly four files —
  `.claude/agents/wip/orchestrator.md`, `…/coordinator.md`,
  `…/researcher.md`, `…/builder.md` — **project scope, in a `wip/` subfolder,
  no `agents/README.md`** (fixes F4). Frontmatter is preserved verbatim, so each
  file keeps `name: wip-<role>`; therefore
  `Task(subagent_type="wip-orchestrator" | "wip-coordinator" |
  "wip-researcher" | "wip-builder")` resolves identically whether the files are
  flat or nested (O1: recursive scan + `name:`-only identity). The roadmap and
  BRIEF write `.claude/agents/*.md` as shorthand; the pinned target is the `wip/`
  subfolder. Both flat and nested project-scope layouts preserve the four spawn
  names; we choose **nested** for footprint hygiene — it groups wip's files,
  keeps them from intermixing with a consumer's own project subagents, and makes
  the install trivial to detect and remove (for the conservative-write guard and
  a later migration). The `wip-` name prefix namespaces spawn identity; the
  `wip/` folder namespaces the filesystem.

- **D4 — `source` semantics become real.** `source: vendored` → the flattened
  local copy under `.claude/agents/wip/` that `setup agents` writes (resolves
  F2). `source: plugin` → vendor **nothing**; rely on the globally-enabled `wip`
  plugin, whose agents resolve by the same bare `wip-<role>` name. This is the
  right default for plugin repos and sidesteps F1 (no second root
  `.claude-plugin/plugin.json`). The authoring side is untouched: the wip repo
  keeps `roles/`, the four thin-pointer agents, and the `active.md` indirection
  (ADR-0013) for dogfooding and for `source: plugin` consumers.

- **D5 — Dangling cross-link policy: leave inert + one framing disclaimer.**
  After single-level flatten, role bodies still contain inert Markdown links
  (`[shared.md](./shared.md)`, `[backends/](./backends/)`,
  `[tier-policy.md](./tier-policy.md)`, glossary links). These are **not**
  `@`-includes — Claude Code never resolves them at runtime; they are prose
  decoration in a system-prompt body. v1 leaves them **inert** and prepends one
  framing line noting that the manuals below are inlined in full and that any
  relative `./…` Markdown link is inert — not resolved at runtime, with the
  content it names reproduced inline in this same file. This satisfies the
  "zero `@` refs" self-containment test, which forbids `@`-includes, **not**
  inert Markdown links. Anchor-rewriting intra-bundle links to in-document
  anchors is a **deferred** renderer enhancement (step-02 polish), consistent
  with Lane B — stripping risks mangling inline prose ("see
  [shared.md](./shared.md) §Pause and Resume" → broken sentence), and
  anchor-rewrite needs a heading-anchor map the dumb single-pass inliner does
  not have.

- **Conservative write over clobber.** If a foreign root
  `.claude-plugin/plugin.json` (one wip does not own) is present, `setup agents`
  refuses with a drift-style exit and writes nothing — it never overwrites the
  host plugin's manifest.

This ADR **cross-references** ADR-0001 (the three-layer split — rendered agents
still instruct "run `wip-plumbing …`"; only file includes are resolved),
ADR-0005 (Roles are referenced, not copied — F3), ADR-0007 (orchestration is a
capability with pluggable backends; the seam test runs against `roles/`, so a
rendered artifact legitimately contains backend tokens), ADR-0013 (the consumer
gets no `active.md`/`roles/`; the backend choice is baked into the inlined
`backends/<backend>.md`), and ADR-0015 (the same generated-but-committed
pattern). It does **not** amend ADR-0013 or ADR-0015 — those amendments are
owned by step-04 (consumer-context re-flatten on backend switch) and step-05
(the transform grows to resolve the four `@`-includes), respectively. This ADR
only points at them.

## Consequences

- A consumer that is itself a plugin can install orchestration without losing
  its own `plugin.json` (F1 gone). The vendored artifact is four self-contained
  files under `.claude/agents/wip/`, with no `.claude-plugin/` and no `roles/`
  footprint (F2/F3 gone), and no phantom README subagent (F4 gone).
- The spawn-by-name contract is load-bearing and preserved: identity rides the
  `name:` frontmatter, not the path, so the nested layout is behaviorally
  identical to flat for the four `wip-*` names.
- **Known edge (Q2).** A `source: plugin` install resolves each agent by bare
  `wip-<role>` name; Claude Code only needs the `wip:`-scoped form to break a
  *cross-plugin* same-name tie. The `wip-` prefix makes that collision
  improbable. If a future consumer enables a second plugin that also defines
  `wip-builder`, that is a disambiguation problem for that consumer, not a reason
  to change the contract.
- The decision is docs-only here; it is **proven** later by step-05's round-trip
  / drift gate (`--check` re-renders from `roles/` + the selected backend and
  diffs the installed output) and by spawning the four `wip-*` agents by name
  against the emitted nested layout. This ADR commits the decision the gate
  checks; it changes no runtime behavior on its own.
- Anchor-rewriting inert cross-links stays deferred; revisit only if the inert
  links measurably confuse a spawned agent reading its own prompt.

## Amendment — migration path for repos on the old plugin-tree shape (step-07)

Amended 2026-07-01 (BDS-28, *Flatten vendored orchestration agents*, step-07,
Round 2; resolves BRIEF **O5**). This ADR's decision stopped *shipping* the
plugin-shaped tree; step-07 adds the supported path to *clean up* the leftover
footprint an OLD (pre-step-03) `setup agents` already wrote into a consumer repo,
so a migrated repo byte-matches a fresh flattened install.

### The real footprint — OQ-07.1 correction (roles/ + active.md were never vendored)

The Context and F3 above frame the old vendor path as copying a tree that
included `roles/`, and the roadmap/BRIEF described the leftover as a "root
`.claude-plugin/` + `roles/` + `active.md`". **Verified against the code and git
history, that is imprecise and is corrected here.** The old plugin-tree
`setup agents` was `wip_setup_walk_template_tree templates/setup/agents/** →
$root`, whose exact write set the pre-step-03 test pinned as `agents) expected=16`
(*"4 agents + 9 commands + agents/README + plugin/README + plugin.json"*). The
real on-disk old footprint is therefore exactly **16 files**:

| Root path (old vendored footprint) | Count | Ownership signal (delete predicate) |
|---|---|---|
| `.claude-plugin/plugin.json` | 1 | `.name == "wip"` |
| `.claude-plugin/README.md` | 1 | byte-equal `templates/setup/agents/.claude-plugin/README.md` |
| `agents/README.md` | 1 | byte-equal `templates/setup/agents/agents/README.md` (the F4 phantom) |
| `agents/{orchestrator,coordinator,researcher,builder}.md` | 4 | frontmatter `name: wip-<role>` (thin-pointer signature) |
| `commands/<name>.md` | 9 | byte-equal `templates/setup/agents/commands/<name>.md` |

**There is NO root `roles/` and NO root `active.md`.** The old thin-pointer agents
referenced roles at runtime via `@../roles/…` includes (resolved against the
plugin, not a vendored copy); `active.md` is an authoring-side generated pointer
(ADR-0013) that was never vendored into a consumer. Any stray root
`roles/`/`active.md` are a consumer's own or a hand-vendored copy and are handled
**warn-only-if-present**, never an auto-delete target. Migration also flips the
tell-tale mislabel: the old install recorded `features.orchestration.source:
plugin` (the F2 bug) despite vendoring a full tree. This correction is mirrored in
the initiative roadmap.

### The actor — `setup agents --migrate [--dry-run]`

`--migrate` is a flag on `setup agents` (alongside `--check`/`--source`/`--force`),
because that verb already owns the flattened write path, the idempotent writer, the
JSON ledger, and the foreign-manifest guard. It is **not** a new top-level
subcommand.

- **Keys on the on-disk footprint, not the `source` flag.** The trigger for
  cleanup is the presence of wip-owned old-footprint files on disk — never the
  manifest `source` value. This is what protects a deliberate `source: plugin`
  repo: it carries no footprint → migration is a no-op regardless of the flag.
- **Conservative delete (the deletion analog of the conservative-write guard
  above).** A file at a footprint path is **deleted only when it matches its
  ownership signal** in the table above; anything that does not match (drifted,
  version-skewed, consumer-authored) is **warned, never deleted**. A **foreign**
  `.claude-plugin/plugin.json` (`name != wip`, the F1 host-plugin case) is never
  touched. Empty parent dirs (`.claude-plugin/`, `agents/`, `commands/`) are
  `rmdir`-ed only once empty. Command/README byte-match is version-*fragile* (an
  older wip version's bytes won't match the current template → warn + manual
  cleanup); the `name`-based agent/plugin signals are version-robust.
- **Two end-states, chosen from disk (not the flag).** If a **foreign** root
  manifest is present → host-plugin end-state: clean wip's owned old
  `agents/`/`commands/`, leave the foreign manifest (warned), write no
  `.claude/agents`/`.claude/commands`, set `source: plugin` (the correct end state
  for a repo that is itself a plugin — and this also repairs a host plugin that was
  mis-installed under the old path). Otherwise → vendored end-state: clean the owned
  footprint, run the flattened vendored write (`_wip_setup_agents_vendored`, reused
  verbatim), flip `source: vendored`. The vendored end-state **byte-matches a fresh
  flattened install** (proven by `test-setup.sh` diffing the migrated `.claude/`
  against a control fresh install).
- **Dry-run + idempotence.** `--migrate --dry-run` reports the plan
  (`{dry_run:true, would_delete, would_write, would_warn}`) and touches nothing. A
  repeat `--migrate` on an already-migrated repo deletes nothing, the vendored write
  reports all-`skipped_idempotent`, and the ledger records `migrated:false`.

### Detection — `doctor` (pure-disk)

`doctor` runs a **pure-disk** legacy-footprint scan (no render) and, on an owned
footprint, appends `{kind:"orchestration", status:"legacy-footprint",
fix:"run wip-plumbing setup agents --migrate", paths:[…]}` (counts as drift → exit
4). It never deletes. The gate keys on ≥1 `owned` file, so a foreign-only or
stray-only footprint stays quiet. This pure-disk check is deliberately distinct
from the still-deferred render fan-in (ADR-0015 Q-05.4); see that ADR's step-07
amendment.

## Amendment — provenance-anchored two-axis drift detection (BDS-58, step-02)

Amended 2026-07-03 (BDS-58, *Detect & refresh stale vendored wip role/agent
copies*, initiative `wip-orchestration-robustness`, step-02). **See
[ADR-0023](./0023-vendored-role-provenance-and-two-axis-drift-detection.md).**

ADR-0020's `setup agents --check` is a single `cmp` of `render_now` vs the
on-disk file — one comparison, so it cannot say *why* a file drifted. ADR-0023
adds a **third anchor** (the vendor-time render, persisted as `baseline_hash` in
a new `.claude/agents/wip/.provenance.json` sidecar) and splits drift into two
independent axes: `upstream_advanced` (`render_now != baseline`) ⟂
`locally_modified` (`disk_now != baseline`), plus a divergence *direction* from
the stamped `plugin_version`. The refined classification lands as **additive**
flags — `setup agents --status` (read-only report, exit 0) and `setup agents
--sync [--force] [--dry-run]` (per-state actor) — sharing one classifier
(`_wip_setup_agents_provenance_classify`). `--check`'s blunt rc-4-on-any-diff
contract is **unchanged** (ADR-0023 D4). The sidecar keeps the agent/command
bytes pristine, so this ADR's render-and-diff invariant survives verbatim.
