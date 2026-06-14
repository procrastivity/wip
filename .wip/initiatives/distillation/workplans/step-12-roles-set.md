# Workplan — step-12 · Roles set

Distill `workflow-portable-stub/playbook/` (6 markdown files, gitignored
study slice) into the canonical `roles/` directory under the
**backend-agnostic structure** locked by
[ADR-0007](../../../../engineering/decisions/0007-orchestration-backend-seam.md).
The behavior files must say "spawn a `<tier>` agent" and "the task
ledger" — they must never mention `mcp__solo__*`, `agent_tool_id`,
`spawn_process`, `whoami`, todos, scratchpads, or any other
Solo-specific name. Solo-specific bindings live in **exactly one** file:
`roles/backends/solo.md`. That structural seam is what lets a
hypothetical future `roles/backends/native.md` land with **zero touches**
to behavior files or `tier-policy.md`.

Step-11 left `.claude-plugin/agents/README.md` as a 3-line stub pointing
at `roles/`. This step lands the actual `roles/` content plus the
plugin-side agent files that reference it as a single source of truth —
the same prompt-sharing seam shape step-11 introduced for shaper
prompts, applied one layer up.

## Decisions (made here, feed later steps)

- **Layout matches the planned-shape block in `roles/README.md` exactly.**
  Seven files under `roles/`, no extra subdirectories yet:

  ```
  roles/
    README.md           # already exists; minor status update only
    orchestrator.md     # behavior — backend-agnostic
    coordinator.md      # behavior — backend-agnostic
    researcher.md       # behavior — backend-agnostic
    builder.md          # behavior — backend-agnostic
    shared.md           # cross-Role behavior — backend-agnostic
    tier-policy.md      # abstract Tier semantics + per-Role defaults
    backends/
      solo.md           # the ONLY doc naming Solo MCP tools + the Solo Tier resolver
  ```

  No `_index.md`, no per-role subdirectories, no per-role glossary
  partials (those already live under `templates/glossary/`). Roles are
  intentionally flat — they're a small set, not a hierarchy.

- **Vocabulary contract — only abstract terms in behavior files.**
  Every reference to a runtime primitive in the playbook source maps to
  an abstract term from `templates/glossary/orchestration.md`:

  | Playbook (Solo-coupled) | Behavior file (abstract) |
  |---|---|
  | `whoami()` | "confirm your role on activation" |
  | `mcp__solo__spawn_process` / `kind=agent` | "spawn a `<tier>` agent" |
  | `agent_tool_id` | (never appears) |
  | `todo_*`, "todo", todo tags | "task ledger entry", "tag the ledger" |
  | `scratchpad_*`, "scratchpad" | "shared note" / "rolling context" |
  | `timer_*`, "Solo timer" | "idle timer", "pause-and-resume signal" |
  | `kv_*` | "shared state" |
  | `list_agent_tools()` | (lives in `backends/solo.md` only) |
  | "Solo MCP" | (removed; capability is backend-agnostic) |

  This vocabulary is enforced mechanically by `test-roles-backend-seam.sh`
  (see test strategy).

- **`tier-policy.md` carries the abstract Tier policy verbatim from the
  playbook**, less the runtime-id bits. Sections:
  - Tier semantics (`small`/`medium`/`large`) — copy of glossary plus
    the request-not-resolve discipline (callers request a tier; the
    backend resolves it).
  - Per-Role default tiers (the "Role Policy" block from
    `agent-tool-selection.md`):
    - Orchestrator: `small`
    - Coordinator: `small` default, `medium` for active
      escalation/retry handling
    - Researcher: `large` default, `medium` allowed for low-risk narrow
      decisions
    - Builder: `medium` default, `large` for load-bearing/high-risk or
      novel implementation paths
  - Tier-escalation guardrails (the playbook's "load-bearing builder
    escalation" list, distilled): repeated same-shape failures on
    `medium` → next attempt is `large` or escalate; load-bearing surfaces
    (data-store/runtime boundaries, file watching, MCP protocol behavior,
    cross-cutting refactors, schema/indexing, novel/hard-to-reverse
    paths) default to `large` from the start.
  - Manifest override hook — `features.<backend>.agent_tier_policy.
    force_tier` (e.g. `.wip.yaml`'s `features.solo.agent_tier_policy.
    force_tier: large` on this repo) may pin every spawn to a single
    tier; the resolver still honors the Role's preference for
    audit/`selection_reason`, but the spawned runtime is the forced one.

  **No runtime tool ids appear here.** `agent_tool_id`, the
  command-token classification table, the `list_agent_tools()` resolver
  — all of that moves to `backends/solo.md`.

- **`backends/solo.md` is the ONLY file naming Solo MCP tools.** It
  contains:
  - Identity: `whoami()`, role-scoped process-name conventions
    (`<slug>-step-NN-coordinator` etc.).
  - Substrate bindings table (capability term → Solo primitive), copied
    from `templates/glossary/solo.md` with one extra column adding the
    concrete `mcp__solo__*` tool name(s) per row.
  - Tier resolver: the full `list_agent_tools()` flow from
    `agent-tool-selection.md` — command-first token classification,
    name-fallback, `enabled` filter, `exclude_ids`, deterministic vs
    random strategy, return shape, failure modes. Verbatim with light
    edits to remove `/spawn-agent <tier>` (that's a future `wip`
    porcelain verb — w4 — not a Solo-binding doc concern).
  - Tag glossary: `roadmap`, `step-NN`, `task`, `needs-human`,
    `escalation`, `coordinator-context`.
  - Anti-pattern: "Do not use `mcp-cli` from bash" (full block from
    `shared-static.md`).
  - Cross-link back to each behavior file ("This binds the abstract
    primitives that `roles/<role>.md` invokes").

- **`shared.md` is the cross-Role surface — abstract only.** Filename
  matches `roles/README.md` line 48 (the planned layout calls it
  `shared.md`, dropping the `-static` suffix from the playbook source).
  Contents:
  - Role invariants (Orchestrator ≠ Coordinator; Researcher ≠
    Coordinator; Builders ephemeral; Researcher long-lived for a Step;
    never spawn a Coordinator without a Researcher).
  - The naming conventions table (step-NN-coordinator etc.) — written
    in abstract terms ("process / agent names" not "Solo process
    names"; the Solo binding adds the `mcp__solo__rename_process` bit).
  - The shared-note (was: scratchpad) template — abstract markdown only.
  - The "pause and resume" semantic ("an idle-timer signal whose body
    is injected as a fresh turn on fire; bodies must be self-contained")
    without naming `mcp__solo__timer_*`.
  - Pointer to `tier-policy.md` for tier selection; pointer to
    `backends/<active>.md` for the concrete tool surface.

  No `## Use Solo MCP` section, no `mcp-cli` anti-pattern (that moves
  to `backends/solo.md`), no scratchpad/todo language outside the
  abstract terms above.

- **Per-Role behavior files are tight distillations of the playbook
  sources** (`orchestrator.md`, `coordinator.md`, `researcher.md`,
  `builder.md`). Each:
  - Opens with "Audience" + "Read first: [shared.md](./shared.md) and
    [tier-policy.md](./tier-policy.md)".
  - Drops the `## Use Solo MCP` section (replaced by a one-line "On
    activation, confirm your role via the active backend; see
    [backends/](./backends/).").
  - Keeps every durable behavior phase, rewritten in abstract
    vocabulary (Step Kickoff, Polling Loop, Escalation Surfacing,
    Workplan Production, Build Orchestration, Wake-up Routing, Step
    Boundary, etc.). Section names and ordering may change to fit
    `wip`'s glossary but the behavior content must round-trip — a
    reviewer comparing the playbook source side-by-side should see
    every behavioral rule preserved, just re-vocabularized.
  - Drops the playbook's specific path conventions (`notes/roadmap/`,
    `notes/backlog.md`, `notes/proposals/`) and replaces them with
    `wip`'s vocabulary (Roadmap = `.wip/initiatives/<slug>/roadmap.md`;
    backlog = `.wip/backlog.md`; ledger = abstract). The playbook's
    `notes/proposals/` step in the Orchestrator's "Round Candidate
    Sourcing" maps to the (future) intake-pipeline shaped artifacts;
    the role file mentions both sources in `wip` vocabulary without
    over-specifying.
  - References Tier defaults by pointer to `tier-policy.md`, not by
    naming the default tier inline. This keeps tier policy single-
    sourced.

- **`roles/README.md` updates** — surgical. Switch the "Status" block's
  🚧 sentence to "✅ shipped step-12 — files distilled from the
  gitignored `workflow-portable-stub/playbook/` study slice". Leave the
  rest (the ADR-0007 explainer, the planned layout block, the
  acceptance-shape clause on lines 29–30) intact — those are the
  contract this step satisfies, not status.

- **Plugin-side `agents/*.md` files reference `roles/` as the single
  source of truth.** Same shape as step-11's prompt-sharing seam: the
  plugin's agent definitions live under `.claude-plugin/agents/<role>.md`
  with Claude Code's standard agent front-matter; the body is short and
  *points* at the role files via `@<relative-path>` references rather
  than copying their content. Three options were considered:

  1. **Plugin agents reference `roles/*.md` via `@`-file pointers.**
     **Lean.** Single source of truth in `roles/`; the plugin layer is
     a thin frontend that exposes the role to Claude Code as a spawn
     target. Matches the step-11 pattern (plugin commands shell out to
     `wip-plumbing template show`; behavior lives upstream of the
     plugin). Cost: zero — `@`-file references are a stock Claude Code
     feature.
  2. **Copy role content into the plugin agent body.** Rejected — drift
     risk; two sources of truth; the explicit lesson of ADR-0006 and
     the prtend `CLAUDE.md ≡ AGENTS.md` failure mode.
  3. **Build step that renders role files into plugin agent bodies at
     install time.** Rejected — adds a build dependency, ships a
     synthetic artifact, gains nothing over option 1.

  Concrete shape per file (e.g. `.claude-plugin/agents/orchestrator.md`):

  ```markdown
  ---
  name: wip-orchestrator
  description: Human-facing control plane for a wip initiative. ...
  tools: Read, Bash, Task
  ---

  # Orchestrator (wip)

  Your operating manual is in the wip `roles/` directory. Read these
  files before acting:

  - @../../roles/shared.md
  - @../../roles/orchestrator.md
  - @../../roles/tier-policy.md
  - @../../roles/backends/solo.md   <!-- active backend per .wip.yaml -->

  Then act per those instructions ...
  ```

  The plugin agent body is **deliberately short** — its job is to point
  at the canonical role files, not to re-explain the role. If a future
  consumer wants a different backend, only the `backends/<name>.md`
  reference changes.

- **Plugin agent names are `wip-<role>`** to namespace them under the
  `wip` plugin and avoid collisions with any agents the user (or
  another plugin) might define under bare names like `orchestrator`.

- **Backend selection in plugin agents — hardcode `backends/solo.md`
  for v1.** ADR-0007 says Solo is the only backend today. When a
  second backend lands, the plugin agent body either (a) ships a
  second variant per backend or (b) gains a one-line shell-out that
  reads `features.orchestration.backend` from `.wip.yaml` and points
  at the right `backends/<name>.md`. That's a follow-up step's
  decision, not this one. **Lean: hardcode now; revisit when a second
  backend is real.**

- **No new spec.** `roles/README.md` already serves as the contract
  (purpose, planned layout, acceptance shape). The behavior content
  itself is the contract for the Roles capability. A separate spec
  would be redundant with both — defer until a real consumer asks for
  one.

- **No `.wip.yaml` schema changes.** `features.orchestration.{enabled,
  backend}` and `features.solo.agent_tier_policy.force_tier` are
  already in place (ADR-0007 landed them ahead of this step). No new
  knobs needed; the role files reference what's already configured.

- **No new `wip-plumbing` verbs.** Roles are static markdown; no
  runtime fetch path is required from this step. (The `wip spawn` /
  `wip orchestrate` verbs that *will* use the tier policy are w4/w5
  on the roadmap — explicitly out of scope here.)

- **`.claude-plugin/agents/README.md` is updated, not deleted.** It
  becomes a one-screen index of the agent files (mirroring
  `.claude-plugin/commands/`'s implicit pattern), points at `roles/`
  for the canonical behavior content, and notes the
  single-source-of-truth invariant.

## Chunks

1. **Draft the behavior files (abstract-only).**
   - `roles/orchestrator.md` — distill from
     `workflow-portable-stub/playbook/orchestrator.md`. Drop "Use Solo
     MCP" section. Keep: responsibility list, step-kickoff sequence,
     polling loop, escalation surfacing, round-candidate sourcing,
     ambiguous-start rule. Rewrite all `todo_*`/`needs-human` refs as
     "task ledger" / `needs-human` ledger tag.
   - `roles/coordinator.md` — distill from
     `playbook/coordinator.md`. Phase 1 (workplan production), Phase 2
     (build orchestration), research-consult routing, wake-up routing,
     retry/escalation policy, step boundary (including the
     close-round archive sub-step in `wip` vocabulary —
     `.wip/initiatives/<slug>/` archive paths instead of
     `notes/roadmap/archive/`).
   - `roles/researcher.md` — distill from
     `playbook/researcher.md`. Workplan phase, build-phase sidecar
     consult shapes, response contract (`Question` / `Recommendation`
     / `Why` / `Concrete next action` / `Risk notes`), boundaries.
   - `roles/builder.md` — distill from `playbook/builder.md`.
     Responsibility, startup sequence (read ledger entry + workplan
     section + shared note), reporting contract (gates green; commit
     "Step N · Task M: <summary>"; ledger results comment with files
     touched / tests / commit sha / decisions; mark ledger entry
     complete; stop). Soft flags. Escalation. "Builders do not contact
     researcher directly — route via coordinator."

2. **Draft `roles/shared.md`.**
   - Role invariants block (verbatim semantics from playbook
     `shared-static.md` `## Role Invariants`).
   - Naming conventions table (abstract — "process / agent name", not
     "Solo process name").
   - Shared-note template (the Step N context markdown block, with
     "todo" rewritten as "ledger entry" and `mcp__solo__todo_list`
     swapped for "query the ledger by `<slug>/step-NN` tag").
   - Pause-and-resume semantic (one paragraph, no `mcp__solo__timer_*`).
   - Pointer block: "for tier policy, see [`tier-policy.md`]; for the
     concrete substrate bindings, see [`backends/<active>.md`]."

3. **Draft `roles/tier-policy.md`.**
   - "Request capability, not runtime id" discipline (one paragraph;
     cross-link `templates/glossary/orchestration.md` Abstract
     substrate row).
   - Per-Role tier defaults table.
   - Tier-escalation guardrails block (load-bearing surfaces; repeated
     same-shape failures policy).
   - Manifest override hook (`features.<backend>.agent_tier_policy.
     force_tier`) — one paragraph, no Solo-specific binding.

4. **Draft `roles/backends/solo.md`.**
   - Header: "This file binds the Roles capability to the Solo
     orchestration backend. The Roles behavior (in the sibling files)
     is backend-agnostic; this file is the only place that names Solo
     MCP tools."
   - Identity & process naming (`whoami`,
     `mcp__solo__rename_process`).
   - Substrate bindings table (capability term → Solo primitive → MCP
     tool names). Three required rows: agent process (`spawn_process`,
     `kind=agent`), task ledger (`todo_*`), shared note
     (`scratchpad_*`), pause-resume (`timer_*`, `wait_for_bound_port`),
     shared state (`kv_*`).
   - Tier resolver (the `list_agent_tools()` flow, command-first token
     classification, name fallback, `enabled` filter, `exclude_ids`,
     strategy, return shape, failure modes — verbatim semantics from
     `playbook/agent-tool-selection.md` minus the `/spawn-agent
     <tier>` interface block).
   - Tag glossary (`roadmap`, `step-NN`, `task`, `needs-human`,
     `escalation`, `coordinator-context`).
   - Anti-pattern: do not use `mcp-cli` from bash (full block from
     `playbook/shared-static.md`).
   - Cross-links back to each behavior file.

5. **Update `roles/README.md`.** Replace the 🚧 Status block with
   ✅ shipped step-12; leave the ADR-0007 explainer and the planned
   layout (now actual layout) intact.

6. **Author plugin agent files.**
   - `.claude-plugin/agents/orchestrator.md` — `name: wip-orchestrator`,
     description from the role's responsibility, body points at
     `@../../roles/{shared,orchestrator,tier-policy}.md` plus
     `@../../roles/backends/solo.md`.
   - Same shape for `coordinator.md`, `researcher.md`, `builder.md`.
   - `tools` front-matter per role (Orchestrator and Coordinator get
     Task for spawning; Researcher gets Read + Write + Edit + Grep +
     Bash; Builder gets Read + Write + Edit + Bash). Exact tool sets
     can be tightened during implementation if a sharper line emerges
     — these defaults match the role responsibilities.
   - Update `.claude-plugin/agents/README.md` to an index pointing at
     the four agent files + a one-paragraph note on the
     single-source-of-truth contract.

7. **Acceptance test (`test/test-roles-backend-seam.sh`).** Pins the
   ADR-0007 acceptance shape mechanically. See *Test strategy* for
   the full assertion list.

8. **Mark step-12 shipped on the roadmap; bump `active_step`.**
   - `.wip/initiatives/distillation/roadmap.md` step-12 bullet gets
     `✅ shipped <YYYY-MM-DD>` and a one-line outcome.
   - `.wip.yaml`'s `initiatives[0].active_step: step-12` → `step-13`.
   - `nix develop --command bin/wip-plumbing doctor` still reports
     zero drift.

9. **Audit transcript in the commit body.** Capture the output of
   the acceptance grep showing **zero matches** in the behavior +
   `tier-policy` files and the **expected matches** in
   `backends/solo.md`. The commit body is the canonical record of
   the seam.

## Test strategy

One new test file, `test/test-roles-backend-seam.sh`. Plain bash,
sourcing `test/helpers.sh`. Assertions:

- **Layout.** All seven planned files exist
  (`roles/{orchestrator,coordinator,researcher,builder,shared,tier-policy}.md`
  and `roles/backends/solo.md`).
- **Behavior + `tier-policy` files are backend-agnostic.** For each
  Solo-specific token in the forbidden set
  `(mcp__solo|solo_process_id|agent_tool_id|spawn_process|scratchpad|todo_create|todo_list|whoami|list_agent_tools|mcp-cli|kv_set|kv_get|timer_set|timer_fire_when_idle|rename_process|wait_for_bound_port|kind="agent"|kind=\"agent\")`,
  assert `grep -E` finds **zero** matches across `roles/*.md` and
  `roles/tier-policy.md`. (Implemented as one combined `grep -rE` so
  failure messages name the offending file/line for fast iteration.)
- **`backends/solo.md` does name Solo MCP tools.** Assert at least
  one match for each of: `mcp__solo__spawn_process` (or
  `spawn_process`), `agent_tool_id`, `list_agent_tools`, `whoami`,
  the Solo timer family (`timer_`), and `mcp-cli` (the anti-pattern
  block). This confirms the extraction landed in the right file
  rather than being silently dropped.
- **`backends/` only contains `solo.md`.** Assert
  `find roles/backends -mindepth 1 -not -name solo.md | wc -l` is
  `0`. Catches accidental addition of an unauthored backend file.
- **Plugin agent files exist and reference `roles/` via `@`.** For
  each of `.claude-plugin/agents/{orchestrator,coordinator,researcher,
  builder}.md`:
  - File present.
  - Front-matter has `name:`, `description:`.
  - Body contains `@../../roles/shared.md`, `@../../roles/<role>.md`,
    `@../../roles/tier-policy.md`, and `@../../roles/backends/solo.md`.
  - Body does NOT name Solo MCP tools directly (same forbidden token
    grep as the behavior files — plugin agents are thin pointers).
- **`tier-policy.md` defines per-Role defaults.** Assert it grep-
  matches each role name in a Tier-defaults context (cheap structural
  check that the policy didn't drift out).
- **`roles/README.md` no longer marks `roles/` as not-yet-authored.**
  Assert it does NOT contain the literal "🚧 Not yet authored".

Existing tests stay green. `make check`'s budget: one new test file,
~25 added assertions. No new lib/, no new bin/, no new dependencies
(plain `grep` + `find`).

`test-plugin-manifest.sh` from step-11 asserts that `agents/` contains
only `README.md`. **That assertion is now stale** (step-12 is
explicitly when files land) and must be updated as part of chunk 6:
the assertion becomes "`agents/` contains exactly the four role files
+ README.md".

**Coverage targets:**

- Backend-agnostic seam (ADR-0007 acceptance shape) pinned mechanically.
- Solo binding presence pinned (extraction landed in the right place).
- Plugin agent layout pinned (single-source-of-truth invariant).
- No regressions in any prior suite.

## Definition of done

- `roles/{orchestrator,coordinator,researcher,builder,shared,tier-policy}.md`
  committed; each is a tight distillation of the corresponding
  `workflow-portable-stub/playbook/` source (or, for `shared.md`, of
  `shared-static.md`); every behavior phase from the source has a
  corresponding section in the role file; all references are in the
  abstract vocabulary defined by `templates/glossary/orchestration.md`.
- `roles/backends/solo.md` committed; contains identity, substrate
  bindings table, the full Tier resolver, tag glossary, and the
  `mcp-cli` anti-pattern block — all transplanted from the playbook
  sources.
- `roles/README.md` updated: status block flipped from 🚧 to ✅
  shipped step-12; planned-layout block left intact (it now describes
  the actual layout).
- `.claude-plugin/agents/{orchestrator,coordinator,researcher,
  builder}.md` committed; each is a thin agent definition that
  references `roles/*.md` via `@`-file pointers (no role content
  duplicated).
- `.claude-plugin/agents/README.md` updated from the step-11 stub to
  an index of the four agent files.
- `test/test-roles-backend-seam.sh` committed and green under
  `nix develop --command make check`.
- `test/test-plugin-manifest.sh` updated to reflect step-12's
  `agents/` contents (no longer "README-only").
- All previously-passing tests still pass (no regressions).
- `nix develop --command pre-commit run --all-files` exits 0.
- `nix develop --command bin/wip-plumbing doctor` still reports zero
  drift.
- `.wip/initiatives/distillation/roadmap.md` step-12 bullet marked
  `✅ shipped <YYYY-MM-DD>` with a one-line outcome.
- `.wip.yaml`'s `initiatives[0].active_step: step-12` → `step-13`.
- Branch + commit + merge into `main` (no-ff merge commit, matching
  the pattern step-08.5 / step-09 / step-10 / step-10.5 / step-11
  used).
- Commit body includes the audit transcript: paste the output of the
  acceptance grep showing **zero matches** against the behavior +
  `tier-policy` files and the **expected matches** in
  `backends/solo.md`.

## Open questions to resolve during execution

- **Plugin agent `tools:` front-matter — exhaustive list or
  permissive?** Lean: **per-role minimal**. Builders get
  Read/Write/Edit/Bash; Researchers add Grep/Glob/WebFetch;
  Coordinators add Task (for spawning Builders); Orchestrators add
  Task (for spawning Coordinators) but not Write/Edit (they don't
  write code, per the playbook's hard rule). If a real ask surfaces
  for one more tool per role, that's a one-line update.

- **`backends/solo.md` — should it duplicate the substrate bindings
  table from `templates/glossary/solo.md`, or just link to it?**
  Lean: **duplicate (with an `mcp__solo__*` tool-names column added)**.
  The glossary partial is the *vocabulary*; this file is the
  *behavior binding*. Readers shouldn't have to bounce between
  `templates/glossary/solo.md` and `roles/backends/solo.md` to know
  which MCP tool implements which abstract primitive. Cost is a small
  documented redundancy that's easy to keep in sync (both files are
  small; a cross-link in the glossary partial flags the duplication).

- **Should `tier-policy.md` reference `.wip.yaml`'s force_tier path
  by absolute key (`features.solo.agent_tier_policy.force_tier`) or
  abstract (`features.<backend>.agent_tier_policy.force_tier`)?**
  Lean: **abstract pattern + one example for solo** (i.e.
  `features.<backend>.agent_tier_policy.force_tier`, "e.g.
  `features.solo.agent_tier_policy.force_tier: large` on this
  repo"). Keeps the policy file backend-shape-agnostic; the example
  is illustrative, not normative.

- **Drop `playbook/README.md`'s "Long-form prompts" block, or carry
  forward in some shape?** Lean: **drop entirely**. Those are the
  bootstrap prompts a human types into a fresh Claude Code session
  before `/wip:*` existed; the plugin's `commands/*.md` are their
  successor. Keeping them in `roles/` would re-introduce the same
  copy-drift the plugin was built to kill.

- **How aggressively to rewrite playbook section titles?** Lean:
  **keep the playbook's section names where they're already abstract
  enough** ("Responsibility", "Step Kickoff", "Polling Loop",
  "Escalation Surfacing", "Workplan Production", "Build Orchestration",
  "Wake-up Routing", "Retry / Escalation Policy", "Step Boundary",
  "Startup Sequence", "Reporting Contract", "Soft Flags",
  "Escalation"). Rewrite only the section that needs it for the
  vocabulary swap (`## Use Solo MCP` → removed; replaced by a
  one-line "On activation, confirm your role; see the active
  [backend](./backends/) for the binding."). Preserving section names
  makes a side-by-side diff with the playbook easy to audit.

- **`shared.md` — keep the entire scratchpad template, or trim?**
  Lean: **keep the template** (with "todo" rewritten as "ledger
  entry"). It's three actionable subsections (Decisions made during
  build, Escalations, Per-task outcomes) plus a header — small and
  load-bearing for Coordinators to bootstrap a shared note. Trimming
  would make the Coordinator's "create shared note from template"
  step have nothing to bind to.

- **Acceptance grep — `grep -rE` or per-file loop?** Lean: **`grep
  -rEn` with a tight excluded-set token regex**. Single command, the
  failure message names file+line+pattern, lowest implementation
  cost. Use `-l` if listing offending files alone is enough; `-n`
  if we want the operator to see the offending line on red.

- **Is the playbook's `notes/playbook/shared-static.md` "Read first:
  …" header carried into each role file?** Lean: **yes** ("Read
  first: [shared.md](./shared.md) and [tier-policy.md](./tier-
  policy.md)"). It's how a freshly-spawned role bootstraps; the
  pointer is the whole point of shared content existing.

- **Should the close-round archive sub-step in `coordinator.md` be
  written against `.wip/initiatives/<slug>/` paths or remain
  abstract?** Lean: **concrete paths under `.wip/`**. The role
  files reference `wip` vocabulary directly elsewhere (Roadmap =
  `.wip/initiatives/<slug>/roadmap.md`); the archive seam is a
  `wip`-shape thing, not a backend-shape thing. The Solo binding
  is what makes "archive the shared note" call `mcp__solo__
  scratchpad_archive`; the path semantics are `wip`'s.

- **Do plugin agent bodies need any instruction beyond the
  `@`-references?** Lean: **one sentence of framing** — "Act as the
  Orchestrator role for this wip initiative per the linked manuals."
  Anything more risks duplicating role content; less risks Claude
  ignoring the references. One sentence is the minimum viable
  framing.
