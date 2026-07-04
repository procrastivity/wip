# Workplan — step-01 · Make coordinator step-teardown deterministic (close live Solo scratchpads)

Started: 2026-07-03. Lane: roles. Linear: BDS-57.

Roadmap entry (Round 1, Lane roles, step-01): "Rewrite the coordinator
Step-Boundary procedure so closing the live Solo scratchpad via
`mcp__solo__scratchpad_archive` is an explicit ordered action, not inferred from
'archive the shared note'; apply to the plugin role sources **and** the Duo
vendored copy (BDS-57)."

## Problem restated (what actually leaks)

`roles/coordinator.md` §Step Boundary item 3 today reads:

> 3. Archive the workplan and the shared note under
>    `.wip/initiatives/<slug>/archive/`.

That one clause conflates **two distinct actions** and only names one of them:

- (a) a **filesystem** artifact — copy the workplan + the shared-note's *content*
  into `.wip/initiatives/<slug>/archive/` (prior steps land this as
  `step-NN-context.md` / `step-NN-rolling-context.md`); and
- (b) **closing the live shared note itself** — the rolling-context Scratchpad
  (`<slug>-step-NN-context`). Under Solo the shared note *is* a live Scratchpad
  (Solo binding substrate table: **Shared note → Scratchpad →
  `mcp__solo__scratchpad_archive`**). "Archive the shared note under
  `.wip/…/archive/`" reads as a file move, so a Coordinator does (a) and never
  calls the backend's shared-note close primitive. The live Scratchpad leaks —
  it stays visible in `scratchpad_list` forever.

`mcp__solo__scratchpad_archive` is precisely "hide a scratchpad from lists
without deleting it" — the intended teardown-close. The fix makes (b) an
**explicit, ordered** teardown action distinct from (a).

## Two edit surfaces (concrete paths — confirmed with the tools)

1. **Plugin role source (canonical, editable):**
   `/Users/beausimensen/Code/wip/roles/coordinator.md` §Step Boundary. This is
   the single source of truth for the Coordinator manual. The repo-root
   `agents/coordinator.md` and `templates/setup/agents/agents/coordinator.md` are
   521-byte thin wrappers that `@`-include `@../roles/coordinator.md` (verified);
   they carry **none** of the Step-Boundary prose, so they need **no** edit —
   they inherit the fix transitively (same finding as
   closeout-write-completion step-05, decision B).

2. **Duo vendored copy (flattened, self-contained):**
   `/Users/beausimensen/Code/duo/.claude/agents/wip/coordinator.md`. The Duo repo
   installed the wip roles with `features.orchestration.source: vendored` +
   `backend: solo` (Duo `.wip.yaml`), so `setup agents` **inlined** the manuals
   (`shared.md` + `coordinator.md` + `tier-policy.md` + `backends/active.md`) into
   one 37.9 KB self-contained agent file via `wip_flatten_render`. Because it
   inlines rather than `@`-includes, the Step-Boundary prose is physically copied
   into it and does **not** update when the plugin source changes — this is the
   exact staleness gap BDS-57 (this step) fixes for the current bytes and BDS-58
   (step-02) makes self-detecting. Its §Step Boundary is byte-identical to the
   plugin source today (both carry the ambiguous item 3). This is currently the
   **sole** vendored consumer (`grep -rl 'Archive the workplan' ~/Code` → only
   `wip/roles/coordinator.md` and `duo/.claude/agents/wip/coordinator.md`).

## Decisions (made here, feed later steps)

- **(A — the role text stays backend-agnostic; it must NOT name the Solo tool.)**
  The roadmap phrases the fix "via `mcp__solo__scratchpad_archive`", but
  `roles/coordinator.md` is a **behavior file** guarded by
  `test/test-roles-backend-seam.sh`, whose `FORBIDDEN` set explicitly includes
  `scratchpad` (and `mcp__solo`). Writing the literal tool name into
  coordinator.md would **fail the seam test**. Only `roles/backends/solo.md` /
  `active.md` may name Solo tools — and they already list
  `mcp__solo__scratchpad_archive` in the substrate table. So the coordinator
  edit uses the **abstract** primitive and points at the backend binding; the
  concrete `scratchpad_archive` reaches the Solo reader through the inlined
  substrate row. This resolves the apparent contradiction between the roadmap
  wording and the seam invariant.

- **(B — split, don't just append.)** Rewrite item 3 into **two** explicit
  ordered actions: keep the filesystem-archive of the workplan + shared-note
  *content* (a), then add a **new, separately-numbered** action (b) that closes
  the live shared note via the backend's shared-note archival/close primitive.
  Renumber the trailing items (4→5 … 9→10). Ordering: (b) comes after (a) so the
  content is safely persisted to `archive/` **before** the live note is hidden —
  and (b) must land **before** item "Close the Researcher and the Coordinator
  processes" so the note is closed while the Coordinator is still alive to do it.

- **(C — blast radius stays at one plugin file + one vendored file.)** The fix
  touches **only** `roles/coordinator.md`. It deliberately does **not** edit
  `roles/shared.md` or `roles/backends/solo.md`/`active.md`. Rationale: only
  `coordinator.md` carries §Step Boundary; and the flatten inlines `shared.md` +
  the backend file into **all four** vendored agents, so touching either would
  force re-rendering `orchestrator.md`/`researcher.md`/`builder.md` too. Editing
  only `coordinator.md` means **only the vendored `coordinator.md`** must be
  re-rendered. (Strengthening the Solo substrate row is captured as an Open
  Question, leaning defer.)

- **(D — regenerate the vendored copy from the working tree, don't hand-edit
  it.)** The Duo copy is a machine render; keep it that way. Re-render it with the
  repo's own renderer pointed at the **wip working-tree** roles/ via the
  `WIP_ROLES_DIR` seam (the renderer's highest-precedence source-dir override;
  `_wip_flatten_roles_dir`), **not** from the installed 0.0.17 plugin cache
  (which predates the fix). This guarantees the vendored bytes equal a render of
  the fixed source, ahead of any plugin re-release.

- **(E — proof-of-sync is a render-and-diff, the `setup agents --check`
  invariant run cross-repo.)** The durable, self-detecting propagation gate is
  BDS-58 (step-02). For step-01 the sync proof is local and mechanical:
  `WIP_ROLES_DIR=<wip>/roles wip_flatten_render coordinator solo` must be
  **byte-identical** to the committed Duo vendored `coordinator.md`. That is
  exactly the invariant `setup agents --check` enforces in-repo
  (`agents-drift`/`content-drift`, rc 4), just run from the wip working tree
  against the Duo file because the fix isn't a released plugin version yet.

## Chunks

Each chunk is one focused commit. Chunk 1 lands in the **wip** repo; Chunk 2
lands in the **Duo** repo (separate git repo — commit there).

1. **[wip repo] Rewrite `roles/coordinator.md` §Step Boundary — split item 3 and
   add the explicit live-shared-note close.** Replace today's item 3 with two
   ordered items and renumber the rest. Proposed prose (match the file's existing
   `<slug>` placeholder + backtick idiom; keep it backend-agnostic per decision
   A):

   > 3. Archive the workplan and **persist the shared note's content** under
   >    `.wip/initiatives/<slug>/archive/` (the durable file record of the Step's
   >    rolling context).
   > 4. **Close the live shared note.** Archive the Step shared note
   >    (`<slug>-step-NN-context`) itself via the backend's **shared-note
   >    archival** primitive (see the active backend binding's substrate table),
   >    so no live rolling-context note leaks past teardown. This is distinct from
   >    item 3's filesystem copy — persisting the content does **not** close the
   >    live note. Under a backend where the shared note is a live handle this is
   >    the only action that stops it showing as open.

   Then renumber existing items 4–9 → 5–10 (the `ship` item, the round-closer,
   the ledger-comment item, the ledger-completion sweep, the operator-guard item,
   and "Close the Researcher and the Coordinator processes"). Verify item (4)
   sits **before** the process-close item. No other `roles/` file is touched.

2. **[Duo repo] Regenerate the vendored `coordinator.md` from the fixed working
   tree.** From the wip repo, render with the working-tree source seam and write
   over the Duo file, e.g.:

   ```sh
   WIP_LIB=/Users/beausimensen/Code/wip/lib/wip \
   WIP_ROLES_DIR=/Users/beausimensen/Code/wip/roles \
   WIP_TEMPLATES_DIR=/Users/beausimensen/Code/wip/templates \
   bash -c 'source "$WIP_LIB/wip-plumbing-lib.bash"; \
            source "$WIP_LIB/wip-plumbing-flatten-lib.bash"; \
            wip_flatten_render coordinator solo' \
     > /Users/beausimensen/Code/duo/.claude/agents/wip/coordinator.md
   ```

   (Mirror how `test/test-flatten-render.sh` and `test/test-setup.sh` source the
   libs and set `WIP_ROLES_DIR`; the Builder confirms the exact `wip-plumbing-lib`
   basename/env at run time.) Then commit the single changed file in the Duo repo.
   Confirm the three sibling vendored agents (`orchestrator`/`researcher`/
   `builder`) are **unchanged** (they don't inline `coordinator.md`).

## Test strategy

- **wip repo — `make check` (= `make lint test`) green.** Load-bearing gates:
  - `test/test-roles-backend-seam.sh` — proves the new coordinator prose names
    **no** forbidden Solo token (`scratchpad`, `mcp__solo`, …). This is the guard
    that decision A satisfies; a literal `scratchpad_archive` in coordinator.md
    would turn it red.
  - `test/test-flatten-render.sh` — the renderer still produces a deterministic,
    self-contained `coordinator solo` render over the edited source
    (it already renders `coordinator solo` twice for its determinism check).
  - `test/test-setup.sh` / `test/test-agents-commands-sync.sh` — unaffected
    (prose-only; no command file, no `@`-include wrapper changed).
- **Cross-repo drift assertion (decision E), the acceptance test for surface 2:**
  `WIP_ROLES_DIR=<wip>/roles wip_flatten_render coordinator solo | diff -
  /Users/beausimensen/Code/duo/.claude/agents/wip/coordinator.md` → **empty diff**.
  Run it after Chunk 2 as the DoD check that the two surfaces are in sync.
- **No new unit test authored.** step-01 is a prose/render step; the render path
  and the seam are already covered by the tests above. The *durable* drift gate
  (a committed `--check` that self-detects future staleness of vendored copies)
  is BDS-58 / step-02's deliverable, not step-01's — flag, don't build it here.

## Definition of done

- `roles/coordinator.md` §Step Boundary contains a **distinct, numbered** action
  that closes the live shared note via the backend's shared-note archival
  primitive, ordered after the filesystem-archive item and before "Close the
  Researcher and the Coordinator processes"; trailing items correctly renumbered.
- That new prose names **no** Solo MCP tool (stays backend-agnostic);
  `make check` is green (seam + flatten-render + setup gates all pass).
- `/Users/beausimensen/Code/duo/.claude/agents/wip/coordinator.md` carries the
  same fix, produced by re-rendering from the fixed working-tree source; the
  render-and-diff assertion is empty (surfaces in sync).
- The other three Duo vendored agents are byte-unchanged; no `roles/shared.md`,
  `roles/backends/*.md`, `agents/*.md`, or `templates/setup/agents/**` file was
  edited.
- Exactly two commits: one in the wip repo (`roles/coordinator.md` + this
  workplan), one in the Duo repo (the vendored `coordinator.md`).

## Open questions to resolve during execution

- **Strengthen the Solo substrate row too? (lean: DEFER.)** The substrate table
  in `roles/backends/solo.md`/`active.md` already lists
  `mcp__solo__scratchpad_archive` among the Scratchpad tools, but not with an
  explicit "this is the *teardown-close* action" note. Adding that note would
  make the Solo reader's mapping fully unambiguous — but it edits a file inlined
  into **all four** vendored agents (re-render ×4, bigger blast radius) and edges
  into BDS-58 territory. Lean: keep step-01 to `coordinator.md`; the abstract
  primitive + the existing substrate row already resolve deterministically. Note
  the option for step-02.
- **Plugin re-release / version lag (lean: proceed; note it).** The vendored copy
  is being brought current ahead of a wip plugin re-release, so a `setup agents`
  in Duo run against the *old* 0.0.17 cache would re-introduce the stale bytes
  until the plugin ships. That is the propagation gap BDS-58 closes; step-01's
  cross-repo regenerate is the interim. Record this in the Step's rolling context
  so step-02 inherits it. Do not add version-guard prose to the role manual.
- **Exact renumber target for item (4).** Confirm at edit time that the current
  §Step Boundary is the 9-item list ending "Close the Researcher and the
  Coordinator processes" (verified at authoring: items 1–9, item 3 is the target);
  if the file has drifted, re-locate the filesystem-archive item and insert the
  close action immediately after it, still before the process-close item.
