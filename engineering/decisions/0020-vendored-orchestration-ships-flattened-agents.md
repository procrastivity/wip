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
  framing line noting that the manuals below are inlined and any `./…`
  cross-reference points to a section within this same file. This satisfies the
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
