# Workplan — step-20 · orchestration idle-routing guard

Bake the **routing guard** we applied ad hoc on every Round-4 step into the
canonical Roles docs, so future runs don't rediscover it. The defect: the
idle-timer "fire when watched agents go idle" signal can fire on a
*between-step* idle — an agent momentarily quiet between tool calls (or while
the model is thinking/planning) — so a watcher treats the bare idle edge as
"done" and routes the watched agent/task as complete prematurely (closes a
process / advances a ledger entry before work actually finished).

The fix is docs-only: a named, backend-agnostic gate that every watcher must
pass before routing completion — (1) re-check the backend **liveness signal**
and re-arm if still active, and (2) require an **explicit final-report
comment / ledger signal**. Never route on the bare idle edge.

Anchors: Roadmap Round 5 step-20; ADR-0007 (backend seam — keep intact).
Scope: `roles/shared.md`, `roles/coordinator.md`, `roles/backends/solo.md`.
No CLI/lib code.

Started: 2026-06-19.

## Decisions (made here, feed later steps)

- **Name the guard the "liveness-and-report gate."** It is a single named
  principle defined once in `roles/shared.md` (§Pause and Resume) and
  referenced *by name* from `roles/coordinator.md` (§Wake-up Routing). Naming
  it lets future steps and any future watcher (incl. the Orchestrator) cite
  one rule instead of re-deriving it.

- **The gate has two conjunctive conditions; a bare idle edge satisfies
  neither on its own.** Before routing a watched agent/task as *complete*:
  (1) **liveness re-check** — re-read the liveness signal; if the agent is
  still active / producing / only briefly quiet, treat it as a between-step
  lull → re-arm the wait and do nothing; (2) **explicit terminal signal** —
  require a final-report comment or a completed ledger entry authored by the
  watched agent. Route to "complete/close" only when BOTH hold.

- **Behavior files stay backend-agnostic.** `shared.md` and `coordinator.md`
  say "liveness signal" and "final-report comment / ledger signal" — never a
  Solo tool name, timer name, or response-field name. This is what keeps
  `test/test-roles-backend-seam.sh` green (ADR-0007 seam).

- **The Solo binding owns the concrete signal shape.** `roles/backends/solo.md`
  binds "liveness signal" → `mcp__solo__get_process_status`'s
  `agent_state.idle` / `idle_seconds` (and `status: "Running"` for the
  live-vs-dead distinction). The status tool is *already* named in solo.md's
  substrate-bindings table (line 43); this step adds the read semantics, not
  a new tool reference.

- **No seam-test edits are required.** `test-roles-backend-seam.sh` is a
  presence/absence test (forbidden-token grep + `assert_grep` presence), not
  a count test. Adding gate wording to behavior files (token-clean) and
  liveness read-semantics to solo.md (already-present tool) changes no
  assertion. See Test strategy.

## Chunks

Each chunk is one file and one focused commit. They can also land as a single
commit; the file split mirrors the ADR-0007 seam (behavior-agnostic vs Solo
binding), which is the natural review boundary.

1. **`roles/shared.md` §Pause and Resume — define the named gate.** Add a
   short subsection introducing the **liveness-and-report gate** as a
   cross-role rule: an idle edge is *not* a completion signal; before routing
   any watched agent/task complete, (a) re-check the liveness signal and
   re-arm if still active, (b) require an explicit final-report / terminal
   ledger signal. Backend-agnostic vocabulary only. Proposed wording:

   > ### An idle edge is not a completion signal
   >
   > The "fire when watched agents go idle" timer can fire on a *between-step*
   > idle — a watched agent momentarily quiet between tool calls, not finished.
   > Before routing any watched agent or task as **complete**, apply the
   > **liveness-and-report gate**:
   >
   > 1. **Liveness re-check** — re-read the backend's **liveness signal**. If
   >    the agent is still active, producing output, or only briefly quiet,
   >    treat it as a between-step lull: **re-arm the wait and take no routing
   >    action.**
   > 2. **Explicit terminal signal** — require an explicit **final-report
   >    comment or a completed ledger entry** authored by the watched agent.
   >
   > Route to "complete" / close a process only when **both** hold. Never
   > route on the bare idle edge. See [`backends/`](./backends/) for the
   > concrete liveness signal.

2. **`roles/coordinator.md` §Wake-up Routing — apply the gate by name.**
   Reframe the section so the gate runs first and no branch can route to
   complete/close on a bare idle edge. Proposed replacement for the section
   body:

   > When a Builder's watch timer fires, **first apply the
   > liveness-and-report gate** ([`shared.md`](./shared.md) §Pause and
   > Resume): re-check the liveness signal, and require an explicit
   > final-report comment — a bare idle edge routes nothing.
   >
   > 1. Read the Builder's ledger entry + comments **and** re-check the
   >    liveness signal.
   > 2. **Still active / only briefly quiet** → between-step lull; re-arm the
   >    watch timer and wait. Do not route.
   > 3. **Quiet + final-report results comment** → append the per-task
   >    outcome to the shared note and close the Builder.
   > 4. **Quiet, no final-report comment** → send a status-check prompt and
   >    re-arm a short timer (do **not** treat as done).
   > 5. **`needs-human` tag** → create a Coordinator escalation ledger entry
   >    and pause further spawning until the human resolves.
   > 6. **Dead process** (not running, no terminal signal) → respawn once,
   >    then escalate.

   Note: the same gate also governs Phase 1 (waiting on the Researcher) and
   the Research Consult wait — those already key off the Researcher's posted
   summary (an explicit signal), so no rewording is required there; the named
   principle now backs them.

3. **`roles/backends/solo.md` — bind the liveness signal.** Add a short
   subsection (after the substrate-bindings table) binding the abstract
   liveness signal to the already-listed status tool. Proposed wording:

   > ## Liveness signal (re-check before routing)
   >
   > The abstract **liveness signal** (see [`shared.md`](../shared.md)
   > §Pause and Resume — the liveness-and-report gate) binds to
   > `mcp__solo__get_process_status`:
   >
   > - `agent_state.idle` — `false` means the agent is actively producing
   >   (thinking/planning/emitting); **re-arm and wait**, do not route.
   > - `agent_state.idle_seconds` — how long the agent has been quiet. A small
   >   value after an idle-timer fire indicates a **between-step lull**, not
   >   completion. There is no magic threshold: the explicit final-report
   >   comment is the real completion signal; this read is the cheap fast-path
   >   that catches a premature wake.
   > - `status` — `"Running"` distinguishes a live process from a dead one
   >   (the Wake-up Routing "dead process" branch).
   >
   > Re-check this signal on every idle-timer fire before treating a watched
   > agent as done.

## Test strategy

- **`test/test-roles-backend-seam.sh` must stay green** (HARD invariant).
  - Forbidden-token grep over behavior files + tier-policy.md must stay zero.
    The chunk-1/2 wording names **no** Solo tokens — it uses "liveness
    signal" and "final-report comment / ledger signal", avoiding the entire
    forbidden set (`mcp__solo`, `timer_set`, `timer_fire_when_idle`, `whoami`,
    `get_process_status` is *not* in the forbidden set but `mcp__solo` is, so
    the qualified name is excluded from behavior files anyway).
  - Expected-token `assert_grep`s over `backends/solo.md` are presence checks
    for `mcp__solo__spawn_process`, `agent_tool_id`, `list_agent_tools`,
    `whoami`, `timer_`, `mcp-cli` — all still present; chunk 3 only adds
    `mcp__solo__get_process_status` read-semantics (the tool was already
    named at line 43). No assertion is count-based, so **no expected-count
    edits are required.**
- **`make check`** (shellcheck/shfmt + markdown lint) clean — match existing
  line-wrap (~74 cols), heading depth, and list style in each file so the
  markdown linter stays quiet.
- **All other suites green** — docs-only change touches no CLI/lib path;
  full `make test` should be unchanged except the unaffected seam suite.
- Manual read-through verifying `shared.md` ↔ `coordinator.md` reference the
  gate by the same name and `solo.md` is the only file naming the status
  tool/fields.

## Definition of done

- `roles/shared.md` §Pause and Resume defines the named
  **liveness-and-report gate** (two conjunctive gates: liveness re-check +
  explicit terminal signal), backend-agnostic, with a `backends/` pointer for
  the concrete signal.
- `roles/coordinator.md` §Wake-up Routing applies the gate **by name**, and
  no branch routes to complete/close on a bare idle edge (a quiet-but-no-
  final-comment wake re-arms / status-checks instead of completing).
- `roles/backends/solo.md` binds the liveness signal to
  `mcp__solo__get_process_status` (`agent_state.idle`, `idle_seconds`,
  `status`), and is the **only** edited file naming Solo tokens.
- `test/test-roles-backend-seam.sh` passes (forbidden grep zero; expected
  tokens present); `make check` clean; all other suites green.
- ADR-0007 backend seam intact: a hypothetical `backends/native.md` could
  supply its own liveness-signal binding without touching any behavior file.

## Open questions to resolve during execution

- **Where does the named gate live, and what is it called?** _Lean:_ define
  it once in `roles/shared.md` §Pause and Resume (the cross-role behavior
  surface) and name it the **liveness-and-report gate**; `coordinator.md`
  references it by name to avoid duplicating the rationale. shared.md owns the
  rule; coordinator.md owns the Wake-up Routing ordering/branches.

- **Add a short *named* principle at all (vs inline prose in each file)?**
  _Lean:_ yes — naming it lets coordinator.md (and implicitly the
  Orchestrator) cite one rule, and gives future steps a stable handle.

- **How much Solo-specific detail in `backends/solo.md`?** _Lean:_ name the
  status tool + the three reads that matter (`agent_state.idle`,
  `idle_seconds`, `status`) and explicitly say there is *no* hard threshold
  (the explicit final-report comment is the real signal). Keep all rationale
  abstract in the behavior files; solo.md is just the binding.

- **Does `test-roles-backend-seam.sh` need expected-count updates?** _Lean:_
  no. It is a presence/absence test, not a count test. The only requirement
  is that behavior-file additions name zero forbidden tokens (they do) and
  solo.md keeps its existing expected tokens (it does — we only add
  read-semantics for an already-named tool).

- **Should `roles/orchestrator.md` also be edited?** The Orchestrator does the
  same wake-on-idle routing (orchestrator.md:28-29, 38-40 — "Arm an idle
  timer to wake when the workplan completes" / "Re-arm the idle timer if no
  action is needed"). _Lean:_ **no edit required for this small step** — the
  gate lives in `shared.md`, which every Role reads, so it is already
  normative for the Orchestrator. Optionally add a one-line by-name pointer in
  orchestrator.md for belt-and-suspenders; flag to the human, but keep out of
  the "small" scope unless they ask.

- **Name a concrete idle-seconds threshold?** _Lean:_ no. Thresholds are
  operational and brittle; the docs already speak qualitatively ("short
  timer"). The explicit final-report gate is the guard; the liveness re-check
  is the cheap fast-path. solo.md says "no magic threshold" explicitly.
