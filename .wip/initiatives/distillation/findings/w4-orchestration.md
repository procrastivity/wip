# w4 — Orchestration substrate (Solo/Duo) & playbook repair

## TL;DR

- The portable playbooks (`workflow-portable-stub/playbook/*.md`) are **structurally
  correct but path-broken**: every `Read first: notes/playbook/shared-static.md` line,
  the README's "verify `notes/playbook/README.md`" checklist, and the
  `agent-tool-selection.md` cross-reference all point at a `notes/playbook/` directory
  that **does not exist** — the installer puts the files at `playbook/` top-level. Fix
  is mechanical (s/notes\/playbook/playbook/g across 4 files + README) and must land
  *before* anyone bases new copies on this stub.
- **New home for these playbooks is a `.wip`-aware feature directory, not `.wip/`
  itself.** Playbooks are durable how-to (Diátaxis "how-to guide"), not in-flight
  planning artifacts. Recommend installing to **`playbook/`** at repo root (matches
  what bizapps/symfony actually do today) and letting `wip.yaml` advertise its
  presence (`features.playbook: playbook/`). `.wip/<initiative>/` holds the
  *outputs* (brief, roadmap, workplans); `playbook/` holds the *roles*.
- **When to use sub-agents**: default to a **single agent**. Spawn the
  orchestrator → coordinator → researcher → builders fan-out only when the work
  has (a) ≥3 independent build chunks **or** ≥30 min wall-clock, (b) a workplan worth
  writing down, **and** (c) risk that justifies a separate reviewer voice. Below that
  threshold the spawn overhead (process bookkeeping, todo plumbing, idle timers)
  costs more than it saves.
- **Source of truth for "what's next" is split, deliberately**: `.wip/roadmap.md` is
  the **plan of record** (versioned, reviewable, survives machine loss); **Solo todos
  are the live execution mirror** (current ownership, blockers, comments, locks).
  `wip next` reads roadmap first, then asks Solo for the active todo on that step.
  One-way sync: roadmap → todos at step-kickoff; todos never silently mutate the
  roadmap (only the coordinator's step-boundary archive step does).
- Tiered spawn (`small`/`medium`/`large` → `agent_tool_id`) is the right
  abstraction and should be promoted from `agent-tool-selection.md` into a `wip`
  CLI verb (`wip spawn <role>`), with role→tier defaults baked in. The BRIEF says
  "Claude Opus only (`agent_tool_id=3`)" today — treat that as a **policy override**
  in `wip.yaml`, not as a reason to delete the tier interface.

## Recommendations

### R1 — Repair the portable playbook set (mechanical, blocking)

Apply these exact edits to `workflow-portable-stub/`:

| File | Current | Replace with |
|---|---|---|
| `playbook/orchestrator.md:5` | `Read first: notes/playbook/shared-static.md` | `Read first: playbook/shared-static.md` |
| `playbook/coordinator.md:5` | `Read first: notes/playbook/shared-static.md` | `Read first: playbook/shared-static.md` |
| `playbook/researcher.md:5` | `Read first: notes/playbook/shared-static.md` | `Read first: playbook/shared-static.md` |
| `playbook/builder.md:5` | `Read first: notes/playbook/shared-static.md` | `Read first: playbook/shared-static.md` |
| `playbook/shared-static.md:59` | `[notes/playbook/agent-tool-selection.md](./agent-tool-selection.md)` | `[playbook/agent-tool-selection.md](./agent-tool-selection.md)` |
| `README.md:51` | `Verify notes/playbook/README.md prompt wording` | `Verify playbook/README.md prompt wording` |
| `README.md:29` | `Run with the three commands in playbook/README.md.` | (already correct — leave) |

Also: the stub's own `playbook/README.md` does not exist in the tree
(`workflow-portable-stub/playbook/` lists no README). Either create it as a
one-paragraph index of the four roles + tool-selection, or strike the README.md:29
line. Recommend **create** — it's the entry point the installer's checklist asks
the human to verify.

### R2 — Adopt this layout in repos that install the stub

```
<repo>/
  wip.yaml              # features.playbook: playbook/      <— advertise presence
  playbook/
    README.md           # one-paragraph index
    shared-static.md
    orchestrator.md
    coordinator.md
    researcher.md
    builder.md
    agent-tool-selection.md
  .wip/
    <initiative>/
      brief.md
      roadmap.md
      workplans/step-NN.md
      archive/
```

Rationale:

- **Playbooks are how-to docs, not WIP.** Putting them under `.wip/` would either
  force gitignore (lose them on team handoff) or force a per-repo opt-in to commit
  (friction). They belong with other Diátaxis how-tos.
- **`.wip/<initiative>/` holds outputs the playbooks produce.** Researcher writes
  to `.wip/<initiative>/workplans/step-NN.md`; coordinator archives to
  `.wip/<initiative>/archive/`. This matches w1's seed layout in the BRIEF and the
  bizapps `.wip/<initiative>/{COMMON,phase-*}` pattern.
- **`wip.yaml` feature flag makes presence deterministic.** A consumer can detect
  the playbook is installed via the manifest, not by `find . -name shared-static.md`.

**Updates required to playbooks under R2**: every reference to
`notes/roadmap/`, `notes/proposals/`, `notes/backlog.md`,
`notes/project-planning-workflow-notes.md` (in `orchestrator.md`,
`coordinator.md`, `researcher.md`, `README.md`) must become `.wip/<initiative>/...`
paths. Specifically:

- `researcher.md:21` `notes/roadmap/step-NN-workplan.md` → `.wip/<initiative>/workplans/step-NN.md`
- `orchestrator.md:55-63` `notes/backlog.md`, `notes/proposals/`, `notes/roadmap/archive/`
  → `.wip/<initiative>/backlog.md`, `.wip/<initiative>/proposals/`, `.wip/<initiative>/archive/roadmap-N.md`
- `coordinator.md:74` `scripts/check-proposal-hygiene.sh` reference: keep, but note
  the script needs the same path migration (out of scope for w4 — flag for w1/w3).
- `README.md:32-40` "Required notes/ Paths" section → rename to
  "Required `.wip/<initiative>/` paths" with the new tree.

### R3 — Decision rubric: single-agent vs fan-out

| Signal | Single-agent (default) | Spawn fan-out |
|---|---|---|
| Build chunks independent of each other | 1–2 | ≥3 |
| Estimated wall-clock | <30 min | ≥30 min |
| Risk / blast-radius | low (docs, refactor in one file) | medium/high (schema, runtime, cross-cutting) |
| Workplan worth writing down | no | yes (you'd write one anyway) |
| Researcher voice valuable | no (you can hold the spec in head) | yes (spec interpretation, ADR rationale, design alternatives) |
| Human review at workplan boundary needed | no | yes |
| Step-shipping criteria exist | no | yes |

**Hard rule**: never spawn coordinator without a researcher (the coordinator playbook
has no non-researcher fallback path — `coordinator.md:28`). If you wouldn't pay for
a researcher process for this step, don't spawn a coordinator either; stay
single-agent.

**Anti-pattern**: spawning fan-out for "let's see what happens." The orchestration
overhead — todo creation, scratchpad init, idle timer arming, escalation routing —
is real wall-clock cost. Treat it like CI: worth it for shippable units, not for
exploration.

### R4 — Model / harness / provider rubric (tiered spawn)

Promote the tiered interface from `agent-tool-selection.md` and add a `wip` verb:

```
wip spawn <role> [--tier small|medium|large] [--name <process-name>] [--purpose "..."]
```

Role → default tier (from `agent-tool-selection.md:137-141`):

| Role | Default | Escalate to `large` when |
|---|---|---|
| Orchestrator | `small` | (never — surface only) |
| Coordinator | `small` | actively driving escalations / retries |
| Researcher | `large` | default; downshift to `medium` only for low-risk narrow steps |
| Builder | `medium` | load-bearing (SQLite, async, MCP protocol, schema, novel paths) **or** same-shape failure twice |

Provider/harness axis (Solo `tool_type` from `list_agent_tools()`):

| Need | Pick |
|---|---|
| Cheap exploration, surface-level edits | `codex` small or `claude` haiku |
| Production code changes, default | `claude` sonnet (medium) |
| Spec-heavy reasoning, novel design | `claude` opus (large) |
| Code-completion-style scaffolding | `codex` medium |
| Tool-call-heavy MCP work | `claude` (better MCP tool-use fidelity) |

**`wip.yaml` policy override** (resolves the BRIEF's "Opus only" constraint
without deleting the rubric):

```yaml
features:
  playbook: playbook/
solo:
  agent_tier_policy:
    force_tier: large           # current project policy
    # or
    role_overrides:
      builder: large
```

When `force_tier` is set, `wip spawn` ignores the role default and uses that tier.
Keeps the abstraction; honors the constraint.

### R5 — Source of truth for "what's next"

Be opinionated: **two surfaces, one direction of truth.**

| Surface | Holds | Lifetime | Authoritative for |
|---|---|---|---|
| `.wip/<initiative>/roadmap.md` | rounds, steps, shipping criteria, intake links | initiative lifetime → archive | "what is the plan" |
| Solo todos (tagged `step-NN`, `roadmap`) | ownership, comments, blockers, locks, status | step lifetime → completed/archived | "who is doing what right now" |

**Sync direction**: roadmap → todos at step kickoff (coordinator reads
`roadmap.md` step section, mints todos under shared naming convention from
`shared-static.md:74-81`). Todos never silently mutate the roadmap. Only the
coordinator's step-boundary archive step (coordinator.md:71-77) writes back to
roadmap (archive of `roadmap-N.md`).

**`wip status`** (w5's verb) reads:

1. `.wip/<initiative>/roadmap.md` — current round, current step, shipping criteria.
2. Solo `todo_list(tags=["needs-human"], completed=false)` — escalations first.
3. Solo `todo_list(tags=[f"step-NN"], completed=false, sort=priority)` — active work.
4. Solo `get_process_status(coordinator_pid)` — is anyone driving.

**`wip next`** returns the highest-priority unblocked todo for the active step
(falls back to "spawn coordinator for step N+1" if step N is shipped).

**Why this split, not "todos as the only truth"**: todos are machine-local to Solo's
project DB. They do not survive a team handoff, a `solo` reinstall, or a different
operator. The roadmap is the durable, reviewable, git-trackable artifact. The BRIEF's
finding #5 — "Planning escapes the repo" via `~/.claude/plans/` — is the same
failure mode; don't reintroduce it via Solo-only state.

**Why this split, not "roadmap as the only truth"**: a markdown roadmap is a poor
fit for live ownership, blockers, idle timers, and lock leases. Solo todos exist
because that data wants relational structure.

### R6 — Idle-timer / escalation patterns (distilled)

Three reusable shapes:

**Pattern A — "Wait for worker quiet, then route":**
Coordinator arms `timer_fire_when_idle_any([builder_pid], max_wait_ms=600_000, body=...)`
after spawning a builder. Timer body must be self-contained for a cold follow-up
(builder pid, step/task id, what to check). On fire, coordinator runs the wake-up
routing decision tree from `coordinator.md:56-64`:

```
1. Check todo + comments
2. completed + results comment      → append outcome, close builder
3. needs-human tagged               → create coordinator escalation todo, pause
4. idle, no comment                 → status-check prompt, re-arm SHORT timer
5. dead process                     → respawn ONCE, then escalate
```

**Pattern B — "Heartbeat status surfacer":**
Orchestrator arms `timer_set(120_000, "check needs-human queue and surface", loop=true)`.
On each fire: `todo_list(tags=["needs-human"], completed=false)` → if any new,
surface to human; else re-fire. Cancel when human takes over the surfaced item.

**Pattern C — "Workplan-ready gate":**
Orchestrator arms `timer_fire_when_idle_any([researcher_pid], max_wait_ms=N, body=...)`
after coordinator confirms researcher spawn. On fire: read workplan path from
scratchpad, surface to human with one-line summary.

**Retry / escalation policy** (codify from `coordinator.md:67-69` —
keep verbatim, don't loosen):

- Up to 2 retries for fixable failures **with clear error context**.
- Same failure twice → escalate (not 3 retries).
- Ambiguity / spec conflict / scope question → **escalate immediately**, no retry.
- Builder same-shape failure on `medium` → next attempt uses `large` or escalates
  (`agent-tool-selection.md:142-144`).

**Anti-patterns to call out in shared-static.md** (add a new section):

- ❌ Polling `get_process_status` in a bash loop. Use idle timers.
- ❌ Builder talks to researcher directly. Routes through coordinator
  (`builder.md:62-64`, `coordinator.md:44-54`).
- ❌ Timer body that says only "check builder". Body is injected as a fresh user
  turn; it must carry pid, scratchpad id, and next action verbatim.
- ❌ Using `mcp-cli solo ...` from bash inside an MCP-connected agent
  (`shared-static.md:22-44` — already documented, keep).

## Evidence

- Broken self-references (BRIEF finding #3 confirmed):
  `workflow-portable-stub/playbook/orchestrator.md:5`,
  `workflow-portable-stub/playbook/coordinator.md:5`,
  `workflow-portable-stub/playbook/researcher.md:5`,
  `workflow-portable-stub/playbook/builder.md:5`,
  `workflow-portable-stub/playbook/shared-static.md:59`,
  `workflow-portable-stub/README.md:51`.
- Coordinator has no non-researcher fallback:
  `workflow-portable-stub/playbook/coordinator.md:28`
  ("No non-researcher fallback path is defined in this playbook.").
- Role/tier defaults: `workflow-portable-stub/playbook/agent-tool-selection.md:137-158`.
- Load-bearing escalation list:
  `workflow-portable-stub/playbook/agent-tool-selection.md:148-158`.
- Coordinator wake-up routing tree:
  `workflow-portable-stub/playbook/coordinator.md:56-64`.
- Retry/escalation policy: `workflow-portable-stub/playbook/coordinator.md:67-69`.
- Solo timer delivery contract (body injected as fresh user turn): Solo MCP
  `help(topic="timers")` — "When a timer fires, `body` is injected into the
  delivery process conversation as a fresh user turn".
- Solo todos vs scratchpads vs KV split: Solo MCP `help(topic="coordination")` —
  "Use todos when work needs ownership, blockers, comments, locks"; scratchpads
  for "larger shared text such as findings, plans, and reports"; KV for "small
  shared status values".
- BRIEF `.wip/` seed layout: `.wip/distillation/BRIEF.md:76-87`.
- BRIEF Opus-only constraint: `.wip/distillation/BRIEF.md:26`.
- BRIEF planning-escapes-repo finding: `.wip/distillation/BRIEF.md:57-59`.
- Anti-pattern (no `mcp-cli` from bash):
  `workflow-portable-stub/playbook/shared-static.md:22-44`.

## Open questions / escalations for the human

1. **`playbook/` at repo root vs `.wip/playbook/`** — I recommend repo root
   (durable how-to, not WIP). If the human's preference is "everything tool-related
   under `.wip/` for visibility", I can flip — but then commit policy must be
   "playbook always committed" regardless of the `wip.yaml` gitignore toggle.
2. **Step ≡ Phase naming collapse** — BRIEF #4 / proposed vocabulary section.
   Playbooks all say "Step"; bizapps says "Phase". I assumed "Step" wins (it's
   in every naming convention, every scratchpad template, every todo tag). If
   the human wants "Phase" instead, every table in `shared-static.md:74-90` and
   every role file needs a rename — flag, don't quietly do.
3. **`scripts/check-proposal-hygiene.sh`** referenced at `coordinator.md:74`
   needs path migration when `notes/` moves to `.wip/<initiative>/`. Not in
   w4's scope but a real blocker for the coordinator's step-boundary action.
   Flag for w1 (layout) and w3 (LDS/lifecycle scripts).
4. **Multi-initiative concurrency** — if two initiatives have active steps at the
   same time, Solo todos with the same `step-NN` tag collide. Recommend extending
   the naming convention to `<initiative-slug>/step-NN` (todo tag) and process
   name `<initiative-slug>-step-NN-coordinator`. Confirm before w5 wires it into
   `wip status`.

## Dependencies on other workstreams

- **w1 (`.wip` layout / vocabulary)**: needs to confirm R2's layout
  (`playbook/` at root, `.wip/<initiative>/` for outputs) and the Step/Phase
  naming decision. My recommendations in R2 and R5 hard-depend on this.
- **w2 (LDS/Diátaxis)**: `playbook/` is a how-to surface. If w2 decides
  how-tos live somewhere specific (e.g., `docs/how-to/` or `engineering/how-to/`),
  the playbook home should follow. My R2 assumes "wherever how-tos go is fine,
  default to `playbook/`."
- **w3 (baseline tooling / installer)**: the stub installer
  (`workflow-portable-stub/scripts/install-workflow-stub`) needs updates for the
  new layout and path fixes from R1. Also: `wip.yaml` schema needs a
  `features.playbook` field and a `solo.agent_tier_policy` block (R4).
- **w5 (`wip` CLI / `/wip:*` slash commands)**: R5 is the contract for
  `wip status` / `wip next`. R4 is the contract for `wip spawn`. R6's idle-timer
  patterns are the primitives `wip` should expose as helpers (so callers don't
  hand-roll `timer_fire_when_idle_any` bodies).
