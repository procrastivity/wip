# Backlog — cross-cutting

Cross-cutting work that hasn't earned a round yet. Each item keeps enough
context for future-you to pick it up cold.

## Nice-to-have

- **Solo TODO lifecycle hygiene in the orchestration roles** (from
  closeout-write-completion, 2026-06-27). Running Round 1 left dangling **open**
  Solo todos for shipped work: the per-Step coordinator-context ledger entries
  (`379` step-03, `380` step-02) were never completed when their Steps shipped,
  and the shared pre-step's task todo (`381`) stayed open after its work landed —
  so `todo_list(completed=false)` no longer reflected reality (cleaned up by hand
  after the run). The Step Boundary in `roles/coordinator.md` closes the
  Researcher/Coordinator *processes* but never marks the Step's ledger entry (or
  its task entries) complete. Two things to tighten: (1) **confirm the roles use
  the task ledger as intended** — the contract for coordinator-context vs `task`
  entries, tags, and *who owns completion* is implicit and undocumented; and (2)
  **fold completion/cleanup into the Step Boundary** (and the prestep / one-off
  pattern): as its final closeout action, before closing processes, the
  Coordinator should `todo_complete` its task entries **and** its own
  coordinator-context entry. Consider a plumbing/`doctor` check that flags ledger
  entries tagged `<slug>/step-NN` still open for a Step the roadmap marks shipped
  (mirrors the step-06 drift idea). Found by the human noticing leftover todos
  after the autonomous step-02/03 + pre-step run.
  ([BDS-14](https://linear.app/beausimensen/issue/BDS-14))

- **Surface lane / parallelization opportunities during roadmap authoring**
  (from closeout-write-completion, 2026-06-27). When the roadmap-authoring
  hand-off drafts Rounds/Steps from a `BRIEF.md` — `/wip:intake` step 10's "say
  `go` and I'll draft Round 1", the `/wip:next` scaffold candidate, `/wip:start`
  on an empty roadmap — nothing prompts an evaluation of which proposed steps are
  *independent* (disjoint files/surfaces, no ordering) and could run as ADR-0010
  parallel lanes. Today lanes only get used if the human thinks to ask: they were
  nearly missed for this very initiative's Round 1, and surfaced only because the
  human raised it. Fix is a contract/prompt addition to the roadmap-authoring
  step(s): after proposing steps, assess independence and propose a lane shape
  where it applies (with the `main* (lane+) main*` grammar + non-conflicting
  files per lane). A pure plumbing lint is weaker on its own — the plumbing can't
  see per-step file touches — so the LLM-authoring prompt is the high-value v1; a
  `roadmap` lane-opportunity lint could complement later. Candidate to graduate
  into its own small initiative (roadmap-authoring assistance), distinct from
  closeout-write state.
  ([BDS-15](https://linear.app/beausimensen/issue/BDS-15))

- **`wip next` should foreshadow imminent lane parallelism** (from
  closeout-write-completion, 2026-06-27). At the pre-lane prereq stage of a laned
  round, `next` renders the upcoming lane steps as plain "next sequential step" —
  the concurrency hint only engages once `active_step` is already *inside* a lane
  (`next.bash` keys the lane-aware reasons off a non-empty `active_lane`). So while
  standing on the prereq, the operator isn't told the next two steps are
  parallelizable. Arguably `next` should surface "after step-NN, lanes X and Y run
  concurrently" from the prereq itself. Distinct from the authoring-suggestion item
  above: this is about `next`'s *rendering*, not roadmap authoring. Found by
  dogfooding the closeout-write-completion Round 1 lanes.
  ([BDS-16](https://linear.app/beausimensen/issue/BDS-16))

- **Surface `## Deferred` items in the plumbing** (from closeout-write-completion,
  2026-06-27). A roadmap's `## Deferred (decided-not-now)` section is write-only
  from the tooling's view: the parser recognizes the heading only to switch into a
  mode that *stops* collecting, then drops the bullets — `roadmap parse` emits no
  `deferred` key (only `backlog`/`lane_errors`/`rounds`), and no verb lists them.
  So deferred items (e.g. orchestration-backends' Duo backend, tier→model map) are
  visible only by reading the roadmap file by hand. Fix: emit a `.deferred[]` array
  from `wip_roadmap_parse` (mirroring `.backlog[]`), and optionally list deferred
  entries in `next`/`status` as a separate, clearly **not-actionable** bucket
  (distinct from backlog candidates, so they're never nominated as the next step).
  Found while auditing what surfaces backlog vs deferred.
  ([BDS-17](https://linear.app/beausimensen/issue/BDS-17))

- **`active.md` hand-edit drift gate** (from orchestration-backends step-01, Q4).
  Nothing today stops a human editing the generated `roles/backends/active.md`
  pointer and drifting it from its source binding. The existing idempotency
  tests catch *switch-time* drift, but not a manual edit between switches. A
  commit-time drift gate — modeled on the `agents-commands --check` gate — would
  catch it. Belt-and-suspenders; not a blocker. Surfaced and deferred during the
  step-01 ratification (2026-06-26).
  ([BDS-18](https://linear.app/beausimensen/issue/BDS-18))

- **`doctor` fan-in of the agent drift gate (consumer-repo scope)** (from
  flatten-vendored-orchestration-agents step-05, Q-05.4, 2026-06-30). step-05
  added `setup agents --check` — the agent-side drift gate that re-renders the
  four flattened `.claude/agents/wip/<role>.md` files from `roles/` + the
  manifest backend and exits 4 (`agents-drift`) on any drift/missing (ADR-0015
  amendment, ADR-0020). It is **not** wired into `doctor` yet: this repo is
  `source: plugin` (no vendored agents to check), so a `doctor` hook adds no
  coverage *here*. The value lands in a **vendored consumer**: wire `setup agents
  --check`'s drift detection into `doctor` so the consumer's `make check`/`doctor`
  sweep surfaces agent drift the way it already surfaces feature/sentinel drift —
  a single sweep instead of a separate `setup agents --check` invocation. Mirrors
  the `active.md` hand-edit drift-gate item above (the `source: plugin` sibling).
  Note `doctor` is a pure-disk sweep today: a fan-in must re-source `flatten-lib`
  and re-derive the backend (the reason D-05.1 chose the `setup agents` flag over
  a `doctor` integration in Round 1). Deferred with human approval (Q-05.4).

- **`duo agent launch --prompt=…` can silently fail to deliver the prompt, and a never-started agent
  is indistinguishable from a finished one.** (Hit 2026-07-12, Round 2 step-05.) The launch returns
  success. The process comes up with the correct role-scoped name and the correct escalated flags —
  everything a caller would check. And then it sits **forever at an empty prompt, having been told
  nothing**. Cost 31 minutes before anyone noticed. The dangerous property is not the lost time, it is
  the **indistinguishability**: an agent that never received its brief and an agent that has completed
  its work look identical from the outside — both idle, both with a clean working tree, both reporting
  no activity. The orchestration liveness gate is *structurally* blind to it: that gate asks "is it
  still working?", and for a never-started agent the honest answer is "no" — the same answer a finished
  agent gives. (That gate is otherwise excellent; it correctly caught ~45 false idle edges across this
  initiative.) The only defense found: **after every spawn, read the child's output and confirm it
  actually received its brief** before treating it as live. Same family as the Duo silent-downgrade
  entry above — Duo reporting success for a launch that did not do what was asked. Fix directions:
  have `duo agent launch` verify prompt delivery or return a delivery receipt; and/or make a
  post-spawn output check a required step in the Coordinator/Orchestrator role contracts, so
  "spawned" and "briefed" are not conflated.

- **`rtk` truncates `make` output, so the repo's own grep-based `make check` verification snippet can
  pass vacuously.** (Found 2026-07-12 by a step-05 Builder, confirmed independently.) The `rtk` hook
  intercepts `make` and writes a **truncated** log — `make check >/tmp/mc.log 2>&1` produced a
  **51-line** file ending in a literal `...(2958 lines truncated)` marker, where the real output is
  **3009 lines**. The verification idiom used throughout this initiative —
  `make check >/tmp/mc.log 2>&1; echo "EXIT=$?"; grep -E "passed, [0-9]+ failed" /tmp/mc.log | grep -v ", 0 failed" || echo ALL_GREEN`
  — therefore greps a file that has been cut to under 2% of its length. If a failure summary falls
  outside the truncation window, the grep finds nothing and the `||` fallback prints a confident
  **`ALL_GREEN`**. The exit code *is* reliable (`rtk` passes it through), so no green reading in this
  run was actually false — but the content check was worthless as evidence, and it was baked into
  every agent brief in the round. **A verification step that cannot see a failure is the exact defect
  class this whole initiative exists to eliminate, sitting inside the quality gate itself.** Workaround
  verified: `rtk proxy make check > <logfile> 2>&1` bypasses the filtering and yields the full log.
  Fix directions: audit every `make`-consuming verification snippet in `AGENTS.md`, the role files, and
  the plugin commands for the same shape; prefer exit codes over log-scraping; or make `rtk`'s
  truncation loud rather than a silent in-band marker.

- **The shipped-marker writers hardcode the glyph while the reader resolves it from constants —
  a latent reader/writer spelling divergence.** (Found 2026-07-12 during closeout-write-ladder
  step-03; pre-existing, not introduced by that step.) step-01 made shipped-state detection
  *positional* and deliberately put the accepted marker **spellings** in one named place —
  `_WIP_ROADMAP_SHIPPED_MARKERS` / `_WIP_ROADMAP_SHIPPED_KEYWORD` in
  `lib/wip/wip-plumbing-roadmap-lib.bash` — so that a future step could settle the canonical
  spelling without reopening the grammar. step-03 then consumed that seam correctly on the READ
  side (it calls `_wip_roadmap_extract_shipped` and never re-spells the marker). But the **write**
  side does not: `_wip_ship_mark_roadmap_shipped` rebuilds the bullet as a literal
  `local rebuilt="${prefix} ✅ shipped ${date}"` (`wip-plumbing-ship-roadmap-lib.bash:80`), and the
  new round-level writer follows the same pattern. So the reader is data-driven off the constants
  and the writer is hardcoded. Change `_WIP_ROADMAP_SHIPPED_MARKERS` and only the reader follows —
  the writer keeps emitting the old glyph, and reader and writer silently disagree about what a
  shipped marker *is*. That is the exact reader/writer divergence class this whole initiative exists
  to eliminate (step-01 was the reader half, step-02 the writer half), reappearing one level up in
  the *spelling* rather than the *position*. It is currently harmless only because there is exactly
  one spelling and nobody has changed it — i.e. the seam is untested. Fix: have both writers build
  the marker FROM the constants (e.g. a `_wip_roadmap_shipped_marker_literal` helper that renders
  the canonical spelling, used by every writer), and add a pin that changes the constant in a fixture
  and asserts reader and writer still round-trip. Cheap, and it makes the "single extension point"
  claim actually true on both sides. Surfaced by the step-03 Coordinator as a non-blocking
  observation (shared note scratchpad 124) rather than silently dropped.

- **`init` should strip its commented-out roadmap-skeleton once a real round exists.**
  `templates/roadmap.md.tmpl:12-47` ships an entire example Round/step skeleton inside one
  `<!-- … -->` span, written verbatim by `_wip_init_try_write`
  (`lib/wip/wip-plumbing-subcommands/init.bash:297-298`), and it survives in every initiative's
  roadmap until a human deletes it by hand. Before closeout-write-ladder step-02's anchor-matcher fix
  (BDS-63) landed, this scaffold repeatedly shadowed real step bullets sharing the same step-id: first
  logged in this file and pruned 2026-07-04 (filed as BDS-63, `wip ship` mis-targeting the commented
  example), then twice more on 2026-07-11 during this very initiative's own bundle explode — `ship`
  marked the commented placeholder as shipped, and separately `intake apply --kind amendment
  --insert-after` wrote an entire step body into the comment block where no reader would ever see it.
  Three occurrences in nine days. Step-02's fix makes the anchor matcher comment-aware, so the
  scaffold is now **inert rather than actively harmful** — this backlog item is the
  recurrence-prevention half, not the harm-removal half: have `init`/roadmap authoring detect that a
  real `## Round` now exists and strip (or refuse to re-write) the example skeleton at that point,
  rather than leaving it to linger indefinitely. Deliberately scoped OUT of step-02 (authoring-time
  change to `init.bash` + the template, not anchor-resolution logic; no test precedent to mirror in
  that step's regression suite).

- **Duo `launch_agent` silently downgrades an unknown preset to `default`, so requesting an
  escalation target can yield a WEAKER runtime than not escalating** (hit live 2026-07-11 during
  closeout-write-ladder step-01). The Coordinator followed `roles/tier-policy.md` — "load-bearing
  surfaces start at the Builder's escalation target" — and called
  `mcp__duo__launch_agent(preset="builder-escalated")` on the roadmap-grammar surface that every
  reader (`status`, `next`, `ship`, `workplan init`, lane accounting) depends on. Duo had no
  `builder-escalated` preset, so the launch **silently fell back to Duo's `default`** and returned
  `ok: true`. `default` is a bare `claude` with **no `--model` and no `--effort`** — weaker than the
  plain `builder` preset (`--model=sonnet --effort=high`). So asking for the strongest Builder
  produced the weakest one in the fleet, silently, with every call reporting success. Nothing in wip
  surfaced it; it was caught only because a human noticed the runtime in a process listing. Three
  distinct problems worth separating: (1) **`launch_agent` and `resolve_preset` disagree** — the same
  unknown preset hard-errors `unknown_preset` on `resolve_preset` but silently falls back on
  `launch_agent`, and the forgiving one is the one that spawns; (2) **the Duo MCP server caches config
  at process start**, so a preset added mid-session is invisible to every already-running agent while
  the `duo` CLI (which reads `~/.config/duo/config.yaml` fresh per call) sees it immediately — a
  long-lived Coordinator can therefore keep mis-staffing chunks for its whole lifetime, and the
  workaround is to launch via `duo agent launch <preset>` rather than the MCP tool; (3) **`roles/backends/duo.md`
  documents the fallback as benign** ("a requested preset with zero enabled definitions falls back to
  Duo's `default` preset") without noting that `default` may be *weaker* than the Role's base preset,
  which turns a documented convenience into a silent capability regression. Fix directions: have the
  wip side **verify the launched runtime matches what the Role asked for** (compare the spawned
  process command against the resolved preset and escalate on mismatch) rather than trusting
  `ok: true`; and/or add a `doctor` check for *a Builder dispatched to an escalation target that
  resolved weaker than its base preset*. Same defect class as the rest of this ladder — **a silent
  resolve landing somewhere nobody intended, with a success envelope on top.** Related: once
  `builder-escalated` existed it had TWO enabled definitions (anthropic/opus-4.8 and
  openai-codex/gpt-5.5) and Duo picks at random, so "escalated" is currently a coin-flip between model
  families rather than a predictable step up — worth deciding deliberately.

- **`active.md` is a committed, shipped artifact generated from a gitignored input**
  (found 2026-07-11 while switching this repo to the Duo backend to orchestrate
  closeout-write-ladder step-01). `roles/backends/active.md` is tracked and ships with
  the plugin — it has to, because the four `agents/*.md` carry a **static**
  `@../roles/backends/active.md` include that must resolve on a fresh install
  (ADR-0013; `agents/README.md:32`). But its generator input, `.wip.yaml`'s
  `features.orchestration.backend`, is **gitignored**. So the output is committed while
  the input is invisible to git, with two consequences: (1) a developer running
  `wip-plumbing orchestrate backend <name>` **in this repo** silently rewrites a shipped
  artifact — `… backend duo` produced a 155+/300- diff to `active.md`, and committing it
  would have flipped the default backend for every fresh plugin install from solo to duo,
  with no reviewable diff explaining why (the `.wip.yaml` change that caused it *cannot*
  appear in the diff); and (2) the `wip-active-backend` pre-commit hook can't catch it —
  it validates `active.md` against a backend scalar read from a file that isn't in the
  repo, so "in sync" is machine-dependent, and on a fresh clone with no `.wip.yaml` the
  check fails `no-manifest` outright. Note this only bites the repo that *has* a local
  `roles/` — `orchestrate.bash:122` prefers `$root/roles/backends` over
  `$CLAUDE_PLUGIN_ROOT`, so a consumer repo (no local `roles/`) correctly switches the
  plugin's copy instead. Fix directions to weigh: decide what backend the *release*
  should ship as default and pin it independently of the dev's local manifest (e.g.
  generate `active.md` at release/package time rather than committing a dev-written one);
  or make the drift gate read a **tracked** source of truth rather than the gitignored
  manifest. Same defect class as the rest of the closeout-write ladder — **state the
  tooling reads that nothing deterministically writes** — and a natural Round 2 candidate
  alongside step-05 (`always_commit` gitignore policy) and step-06 (backlog retirement).
  Distinct from BDS-18 (the `active.md` *hand-edit* drift gate): that one is a human
  editing the pointer between switches; this one is the generator's input being untracked
  while its output ships. Worked around for now by `git checkout roles/backends/active.md`
  after the switch (the run is unaffected — spawned plugin agents resolve their include
  against the plugin cache's copy, not the repo's).

- _(pruned 2026-07-04 → filed as BDS-63: `wip ship` roadmap-marker writer mis-targets commented-out example bullets.)_

- _(pruned 2026-07-11 → filed as BDS-91: roadmap parse silently drops a step whose title contains `*`. Re-discovered during the tracker-backends Round 2 bundle explode; the 2026-06-30 entry's extra facts — the `workplan init` `step-not-in-roadmap` failure, and the bundled-plugin-binary rebuild caveat — were folded into the issue. Scheduled as closeout-write-ladder step-01.)_
