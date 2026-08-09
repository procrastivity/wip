# Trace 6 — a Batch Run interrupted by restart

PLAN 1.7's sixth trace (renumbered at Step 0 — the `orch-traces` findings
record why): a two-member Batch dispatched as a Run, the coordinator
process killed mid-step, the interruption visible from the CLI, and the
Run resumed on the same Clone — reconstructed from durable state while
the Batch was never lost (`orch-traces` step-01; F8, S2–S4).

Produced by driving the real system against a throwaway store
(`WIP_DB_PATH`), a throwaway runtime dir (`WIP_RUNTIME_DIR`), and a
throwaway git clone (`wip init`). Everything with a verb went through the
built binary. Starting a Run and driving a pass have no CLI verb *by
design* (S6; MODEL §11's control-plane refusal), so those two acts were
driven by a small in-module Go program calling the real engine — one
`store.Commit` of `run.started`, then `scheduler.Orchestrate`, then
`scheduler.Resume` — with a Work hook that calls `os.Exit(2)` mid-step to
crash for real: the kernel released the advisory Run lock, and every
durable row stayed exactly as the dead coordinator left it. The store
also holds the three tier-attachment events preceding the table below
(`tiers`' narrative, omitted as in traces 1–5); `wip session`'s event
count (46) includes them.

The cast: Matter P `port-the-exporter` with three Steps (`step-03`
blocked-by `step-02`), Matter Q `refresh-the-exporter-docs` with no plan
at all — worked as its own smallest node (D2). Both joined to named Batch
`exporter-push` (D58). Run cap 1 (D31: plain sequential).

Commands driven, in order:

```
$ wip init
$ wip matter create --title "port the exporter"
$ wip step create port-the-exporter --title "extract the writer interface"
$ wip step create port-the-exporter --title "port the writer"
$ wip step create port-the-exporter --title "delete the legacy shim"
$ wip depend add port-the-exporter/step-03 --blocked-by port-the-exporter/step-02
$ wip matter create --title "refresh the exporter docs"
$ wip batch create exporter-push
$ wip batch join exporter-push port-the-exporter
$ wip batch join exporter-push refresh-the-exporter-docs
$ wip refresh                       # the plain bracket the Orchestrator binds to
$ wip status ; wip next             # captured before the Run
# control-plane driver: run.started, then Orchestrate — crashes (os.Exit)
# while a Builder holds step-02
$ wip run list ; wip run show <run-01> ; wip status   # captured interrupted
# control-plane driver: Resume — refused (see note below)
$ wip role close orchestrator       # clear the stale instance by hand
# control-plane driver: Resume — reaps the orphan claim, finishes the Run
$ wip run list ; wip run show <run-01> ; wip status ; wip session
```

The crashed pass got exactly this far: claim Dispatch opened on P,
`step-01` worked to Done by a Builder, `step-02` started with a second
Builder spawned into the claim — then the process died. Q was never
touched.

## Event table

Alias legend: `repo-A` = `01KZJ8NYT43D0D91TAAH7QQ29X` · `clone-A` =
`…QQ29Y` · `worktree-A` = `…QQ29Z` · `matter-P` =
`01KZJ8NYVW155CPCK6BBZ977DN` (`port-the-exporter`) · `step-P1`–`step-P3`
= its Steps · `matter-Q` = `01KZJ8NZ57TSDES9G3R772GZ3Y`
(`refresh-the-exporter-docs`) · `batch-01` = `01KZJ8NZ7BRKT5BRJ4EGQB5EEA`
(`exporter-push`) · `run-01` = `01KZJ8NZHJE83SQFPJ353CNRY7` · `disp-plain`
= the `wip refresh` bracket · `claim-P1` = the crashed pass's claim on P ·
`claim-P2` = the resumed pass's claim on P · `claim-Q` = the claim on Q ·
`orch-1`/`orch-2` = the two Orchestrator instances · `builder-1`–`builder-5`
= the five Builder instances, in spawn order.

| id | type | occurred_at | actor | causation | correlation | repo | clone | worktree | subject | payload |
|---|---|---|---|---|---|---|---|---|---|---|
| ev-01 | matter.created | 2026-08-09T03:22:21.436Z | human | ev-01 | ev-01 | repo-A |  |  | matter-P | `{"title":"port the exporter","locator":"port-the-exporter","sort_key":0}` |
| ev-02 | step.created | 2026-08-09T03:22:21.502Z | human | ev-02 | ev-02 | repo-A |  |  | step-P1 | `{"title":"extract the writer interface","locator":"step-01","parent":"matter-P","sort_key":1000}` |
| ev-03 | step.created | 2026-08-09T03:22:21.560Z | human | ev-03 | ev-03 | repo-A |  |  | step-P2 | `{"title":"port the writer","locator":"step-02","parent":"matter-P","sort_key":2000}` |
| ev-04 | step.created | 2026-08-09T03:22:21.622Z | human | ev-04 | ev-04 | repo-A |  |  | step-P3 | `{"title":"delete the legacy shim","locator":"step-03","parent":"matter-P","sort_key":3000}` |
| ev-05 | dependency.added | 2026-08-09T03:22:21.681Z | human | ev-05 | ev-05 | repo-A |  |  | step-P3 | `{"edge":"edge-01","blocker":"step-P2"}` |
| ev-06 | matter.created | 2026-08-09T03:22:21.735Z | human | ev-06 | ev-06 | repo-A |  |  | matter-Q | `{"title":"refresh the exporter docs","locator":"refresh-the-exporter-docs","sort_key":0}` |
| ev-07 | batch.created | 2026-08-09T03:22:21.803Z | human | ev-07 | ev-07 |  | clone-A | worktree-A | batch-01 | `{"name":"exporter-push"}` |
| ev-08 | batch.joined | 2026-08-09T03:22:21.855Z | human | ev-08 | ev-08 |  | clone-A | worktree-A | batch-01 | `{"matter":"matter-P"}` |
| ev-09 | batch.joined | 2026-08-09T03:22:21.923Z | human | ev-09 | ev-09 |  | clone-A | worktree-A | batch-01 | `{"matter":"matter-Q"}` |
| ev-10 | dispatch.opened | 2026-08-09T03:22:21.991Z | human | ev-10 | ev-10 | repo-A | clone-A | worktree-A | disp-plain | `{}` |
| ev-11 | render.performed | 2026-08-09T03:22:22.014Z | human | ev-11 | ev-11 | repo-A | clone-A | worktree-A | disp-plain | `{}` |
| ev-12 | run.started | 2026-08-09T03:22:22.130Z | human | ev-12 | ev-12 | repo-A | clone-A | worktree-A | run-01 | `{"batch":"batch-01","locator":"run-01","matters":["matter-P","matter-Q"]}` |
| ev-13 | role.spawned | 2026-08-09T03:22:22.149Z | human | ev-13 | ev-13 | repo-A | clone-A | worktree-A | orch-1 | `{"dispatch":"disp-plain","name":"orchestrator"}` |
| ev-14 | dispatch.opened | 2026-08-09T03:22:22.159Z | role:orchestrator | ev-14 | ev-14 | repo-A | clone-A | worktree-A | claim-P1 | `{"run":"run-01","matter":"matter-P"}` |
| ev-15 | matter.started | 2026-08-09T03:22:22.168Z | role:orchestrator | ev-15 | ev-15 | repo-A |  |  | matter-P | `{"from":"planned","to":"in-progress","cascade":true}` |
| ev-16 | step.started | 2026-08-09T03:22:22.169Z | role:orchestrator | ev-15 | ev-15 | repo-A |  |  | step-P1 | `{"from":"planned","to":"in-progress"}` |
| ev-17 | role.spawned | 2026-08-09T03:22:22.178Z | role:orchestrator | ev-17 | ev-17 | repo-A | clone-A | worktree-A | builder-1 | `{"dispatch":"claim-P1","name":"builder"}` |
| ev-18 | step.finished | 2026-08-09T03:22:22.188Z | role:builder | ev-18 | ev-18 | repo-A |  |  | step-P1 | `{"from":"in-progress","to":"done"}` |
| ev-19 | role.closed | 2026-08-09T03:22:22.196Z | role:builder | ev-19 | ev-19 | repo-A | clone-A | worktree-A | builder-1 | `{"reason":"completed"}` |
| ev-20 | step.started | 2026-08-09T03:22:22.208Z | role:orchestrator | ev-20 | ev-20 | repo-A |  |  | step-P2 | `{"from":"planned","to":"in-progress"}` |
| ev-21 | role.spawned | 2026-08-09T03:22:22.217Z | role:orchestrator | ev-21 | ev-21 | repo-A | clone-A | worktree-A | builder-2 | `{"dispatch":"claim-P1","name":"builder"}` |
| ev-22 | role.closed | 2026-08-09T03:22:29.415Z | role:orchestrator | ev-22 | ev-22 | repo-A | clone-A | worktree-A | orch-1 | `{"reason":"completed"}` |
| ev-23 | role.spawned | 2026-08-09T03:22:29.451Z | human | ev-23 | ev-23 | repo-A | clone-A | worktree-A | orch-2 | `{"dispatch":"disp-plain","name":"orchestrator"}` |
| ev-24 | run.resumed | 2026-08-09T03:22:29.472Z | role:orchestrator | ev-24 | ev-24 | repo-A | clone-A | worktree-A | run-01 | `{}` |
| ev-25 | dispatch.closed | 2026-08-09T03:22:29.473Z | role:orchestrator | ev-24 | ev-24 | repo-A | clone-A | worktree-A | claim-P1 | `{"reason":"reaped"}` |
| ev-26 | dispatch.opened | 2026-08-09T03:22:29.483Z | role:orchestrator | ev-26 | ev-26 | repo-A | clone-A | worktree-A | claim-P2 | `{"run":"run-01","matter":"matter-P"}` |
| ev-27 | role.spawned | 2026-08-09T03:22:29.492Z | role:orchestrator | ev-27 | ev-27 | repo-A | clone-A | worktree-A | builder-3 | `{"dispatch":"claim-P2","name":"builder"}` |
| ev-28 | step.finished | 2026-08-09T03:22:29.500Z | role:builder | ev-28 | ev-28 | repo-A |  |  | step-P2 | `{"from":"in-progress","to":"done"}` |
| ev-29 | role.closed | 2026-08-09T03:22:29.509Z | role:builder | ev-29 | ev-29 | repo-A | clone-A | worktree-A | builder-3 | `{"reason":"completed"}` |
| ev-30 | step.started | 2026-08-09T03:22:29.520Z | role:orchestrator | ev-30 | ev-30 | repo-A |  |  | step-P3 | `{"from":"planned","to":"in-progress"}` |
| ev-31 | role.spawned | 2026-08-09T03:22:29.529Z | role:orchestrator | ev-31 | ev-31 | repo-A | clone-A | worktree-A | builder-4 | `{"dispatch":"claim-P2","name":"builder"}` |
| ev-32 | step.finished | 2026-08-09T03:22:29.539Z | role:builder | ev-32 | ev-32 | repo-A |  |  | step-P3 | `{"from":"in-progress","to":"done"}` |
| ev-33 | role.closed | 2026-08-09T03:22:29.547Z | role:builder | ev-33 | ev-33 | repo-A | clone-A | worktree-A | builder-4 | `{"reason":"completed"}` |
| ev-34 | matter.finished | 2026-08-09T03:22:29.556Z | role:orchestrator | ev-34 | ev-34 | repo-A |  |  | matter-P | `{"from":"in-progress","to":"done"}` |
| ev-35 | dispatch.closed | 2026-08-09T03:22:29.564Z | role:orchestrator | ev-35 | ev-35 | repo-A | clone-A | worktree-A | claim-P2 | `{"reason":"completed"}` |
| ev-36 | dispatch.opened | 2026-08-09T03:22:29.574Z | role:orchestrator | ev-36 | ev-36 | repo-A | clone-A | worktree-A | claim-Q | `{"run":"run-01","matter":"matter-Q"}` |
| ev-37 | matter.started | 2026-08-09T03:22:29.583Z | role:orchestrator | ev-37 | ev-37 | repo-A |  |  | matter-Q | `{"from":"planned","to":"in-progress"}` |
| ev-38 | role.spawned | 2026-08-09T03:22:29.591Z | role:orchestrator | ev-38 | ev-38 | repo-A | clone-A | worktree-A | builder-5 | `{"dispatch":"claim-Q","name":"builder"}` |
| ev-39 | matter.finished | 2026-08-09T03:22:29.599Z | role:builder | ev-39 | ev-39 | repo-A |  |  | matter-Q | `{"from":"in-progress","to":"done"}` |
| ev-40 | role.closed | 2026-08-09T03:22:29.607Z | role:builder | ev-40 | ev-40 | repo-A | clone-A | worktree-A | builder-5 | `{"reason":"completed"}` |
| ev-41 | dispatch.closed | 2026-08-09T03:22:29.615Z | role:orchestrator | ev-41 | ev-41 | repo-A | clone-A | worktree-A | claim-Q | `{"reason":"completed"}` |
| ev-42 | run.finished | 2026-08-09T03:22:29.626Z | role:orchestrator | ev-42 | ev-42 | repo-A | clone-A | worktree-A | run-01 | `{}` |
| ev-43 | role.closed | 2026-08-09T03:22:29.634Z | role:orchestrator | ev-43 | ev-43 | repo-A | clone-A | worktree-A | orch-2 | `{"reason":"completed"}` |

What the table shows, family by family:

- **The Batch never carries a repo.** ev-07–ev-09 have a null `repo`
  column with `clone`/`worktree` filled — D56's Batch-subject rule and
  D39 (a Batch requires no Repo), visible in the envelope itself. Every
  Batch event in the store is `batch.created`/`batch.joined`; nothing
  ever touches the Batch again — it survives the crash without a single
  compensating event, which is the trace's title claim.
- **The crash is an absence, not an event.** After ev-21 (builder-2
  spawned into claim-P1 for step-P2) the log simply stops (seven wall-clock
  seconds here — the operator noticing and recovering).
  No `run.finished`, no `dispatch.closed`, no `role.closed` for
  `builder-2` or `orch-1` — the interruption *is* the dangling open
  state, and liveness is advisory (the released lock), never an event.
- **Resume and its reap are one chain.** ev-25 (`dispatch.closed`,
  reason `reaped`) carries `causation`/`correlation` = ev-24
  (`run.resumed`) — F8's pairing, one command emitting a chain. Builder-2
  is never individually closed: it reaps *with* its bracket (D59), which
  is why `wip session` counts 5 Builders spawned but only 4 closed.
- **No replay, no second start.** step-P1 has exactly one
  `step.started`/`step.finished` pair (ev-16/ev-18) — Done work stays
  Done. step-P2 has exactly one `step.started` (ev-20, pre-crash) and
  its `step.finished` (ev-28) lands in the *resumed* pass under a fresh
  claim — the orphan is worked in place, its pre-crash start honored as
  real history.
- **The cascade keeps its depth.** ev-15/ev-16 share
  `causation`/`correlation` (D57: starting step-P1 auto-started matter-P,
  ancestor-first, `"cascade":true`), reading `a:a`, `b:a` exactly as
  MODEL §10 writes it.
- **The actor is always the spawner, never the role being born.**
  `human` spawns the Orchestrator (ev-13, ev-23); `role:orchestrator`
  spawns every Builder it dispatches (ev-17, ev-21, ev-27, ev-31,
  ev-38), starts nodes, and brackets claims; `role:builder` finishes
  what it worked and closes itself. The Run's whole middle is
  system-driven events as first-class citizens. (This attribution is
  post-fix: the audit Step found mid-pass spawns speaking as the
  driver and the emitter was corrected before this trace was driven —
  the fidelity audit records the gap.)
- **Q is D2 live.** matter-Q was never planned; the loop worked the
  Matter itself as its own smallest node — start (ev-37), finish
  (ev-39) — with no Researcher step, because no Plan hook was configured.

## Rendered pane

`wip status` and `wip next`, captured before the Run:

```
local repo 01KZJ8NYT43D0D91TAAH7QQ29X
  clone              Clone · current

next to start:
  port-the-exporter        matter · planned
  refresh-the-exporter-docs matter · planned

blocked:
  port-the-exporter · step-03 blocked-by: port-the-exporter · step-02
```

`wip run list` and `wip run show`, captured after the crash — the Run row
is durable state; `interrupted` is the advisory liveness probe (the
crashed process's lock is gone) and `owned` says this Clone may resume
it (F9):

```
$ wip run list
01KZJ8NZHJE83SQFPJ353CNRY7 run-01       open         interrupted (owned, 01KZJ8NZ7BRKT5BRJ4EGQB5EEA)

$ wip run show 01KZJ8NZHJE83SQFPJ353CNRY7
Run 01KZJ8NZHJE83SQFPJ353CNRY7 (run-01)
  state: open
  liveness: interrupted
  ownership: owned
```

`wip status`, captured at the same moment — the half-worked graph is
readable straight from durable state, nothing reconstructed:

```
local repo 01KZJ8NYT43D0D91TAAH7QQ29X
  clone              Clone · current

in progress:
  port-the-exporter        matter · in-progress
  port-the-exporter · step-02 step · in-progress

finished:
  port-the-exporter · step-01 step · sealed

next to start:
  refresh-the-exporter-docs matter · planned

blocked:
  port-the-exporter · step-03 blocked-by: port-the-exporter · step-02
```

After Resume, the Run is closed `completed` and everything is sealed
(no gates are declared in this fixture, so Done coincides with sealed —
D55 with an empty gate set):

```
$ wip run list
01KZJ8NZHJE83SQFPJ353CNRY7 run-01       closed       - (owned, 01KZJ8NZ7BRKT5BRJ4EGQB5EEA)

$ wip status
local repo 01KZJ8NYT43D0D91TAAH7QQ29X
  clone              Clone · current

finished:
  port-the-exporter        matter · sealed
  port-the-exporter · step-01 step · sealed
  port-the-exporter · step-02 step · sealed
  port-the-exporter · step-03 step · sealed
  refresh-the-exporter-docs matter · sealed
```

`wip session`, over the whole store — the role census is derived from
the log, and its asymmetries are the crash's fingerprint (one Builder
reaped with its bracket, never individually closed):

```
2026-08-09T03:22:21Z — 2026-08-09T03:22:29Z (46 events)
  role:orchestrator 21 event(s) · 2 spawned · 2 closed
  role:builder      8 event(s) · 5 spawned · 4 closed
```

**Recovery wrinkle, recorded honestly (candidate finding for the audit
Step, not silently reconciled):** the first Resume attempt was *refused* —
`refusal.role-contention: Dispatch … already has an open orchestrator`.
The crashed pass's Orchestrator instance (`orch-1`) lives in the *plain*
bracket (`disp-plain`), and Resume's reap scope is the Run's claim
Dispatches only — the plain bracket belongs to the worktree, not the Run,
and Resume tries to spawn its own instance there *before* reaping. The
only CLI remedy is `wip role close orchestrator` (ev-22), whose close
reason says `completed` for an instance that in fact died. Two smells:
Resume-after-crash needs a manual step the F8 flow never names, and the
record then claims a crashed role completed. Handed to the fidelity audit
(`orch-traces` step-03) to classify.
