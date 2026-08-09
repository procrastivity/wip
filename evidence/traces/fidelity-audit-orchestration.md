# `orch-traces` step-03 — fidelity audit of the batch/run/dispatch/role families

Walks MODEL §10's fidelity list against the four event families Phase 2
added — `batch.*` (the P2 remainder), `run.*`, `dispatch.*` (its P2 Run
shape), and `role.*` — exhaustively per D65: **every instance of every
new-family event in both committed evidence stores** (trace 6's stream:
26 new-family events of 46; trace 7's stream: 1 of 15), re-checked
mechanically against the raw `events` tables rather than the aliased
markdown, plus the emitters and static declarations for every type the
streams do not exercise (`internal/store/taxonomy.go`, `payloads.go`,
`project.go`, `internal/scheduler/loop.go`/`resume.go`,
`internal/writesurface/{batch,run,role}.go`). A mechanical pass over both
stores checked id/occurred_at monotonicity, non-empty actors, no forward
or dangling causation/correlation, per-class tier dimensions, and
ULID-shape of every entity reference in every new-family payload:
**zero envelope problems in either store.**

**Findings list (D65: present even when empty): two findings — one found
and fixed during this audit, one open pending a design decision.**

## Findings

### F1 — mid-pass `role.spawned` spoke as the driver, not the spawner. Fixed.

`internal/scheduler/loop.go`'s `spawnRole` committed every spawn under
`p.driver` (`human`), including the Builders and Researchers the
Orchestrator itself dispatches mid-pass — while the same pass's other
writes (`dispatch.opened` claims, `run.skipped`, the resume reap)
correctly speak as `role:orchestrator`. That contradicted the write
surface's own rule ("the actor is the spawner — a human today, an
Orchestrator once the scheduler exists", `writesurface/role.go`) and MODEL
§10's "the envelope carries **who**": the log said a human spawned five
Builders that a system role actually dispatched. No test pinned the
actor, so nothing caught it. **Fixed**: `spawnRole` now takes the actor;
the driver spawns the Orchestrator (its true spawner), and the
Orchestrator spawns everything it dispatches. Full test suite green;
trace 6 was (re-)driven against the fixed engine, so the committed
evidence shows the corrected attribution (trace 6 ev-17/21/27/31/38,
`actor: role:orchestrator`, versus ev-13/23 where `human` genuinely is
the spawner).

### F2 — a crash-orphaned Orchestrator instance blocks Resume, and its only remedy writes a false close. Open — needs a decision.

Found by driving the restart for real (trace 6, both times). The crashed
pass leaves its Orchestrator instance open in the worktree's *plain*
bracket. `scheduler.Resume` reaps only the Run's claim Dispatches, and
tries to spawn its own instance *before* reaping — so it is refused
(`refusal.role-contention`) until a human intervenes. The one CLI remedy,
`wip role close orchestrator`, then commits `role.closed` with
`reason: completed` and `actor: role:orchestrator` (the verb always
speaks as the role closing itself) — for an instance that in fact died.
Two distinct defects: the F8 recovery flow needs a manual step its record
never names, and that step's event misstates both who and why.

Why this audit did not fix it: an unconditional auto-reap in Resume would
be wrong — an open Orchestrator in the same bracket may belong to a
*live* pass over a different Run, and per-instance staleness is not
provable from the store (the advisory lock is per-Run). The honest fixes
are surface or model decisions: give the close verb an honest reason
(`reaped` exists in the vocabulary — `RoleCloseReaped` — and is currently
reachable only through the bracket-reap projection), or give role
instances advisory liveness so Resume can distinguish stale from live and
reap the stale one itself. Recorded as a finding on `orch-traces`;
deferred to the gate.

## The fidelity list, item by item

### 1. Append-only

`id` is monotonic and matches `occurred_at` order across all 46 + 15
events of the two stores (mechanical check). The interruption mutated
nothing: trace 6's crash leaves ev-01–ev-21 byte-identical through the
resume — recovery is strictly additive (`run.resumed` + a `reaped`
close + fresh brackets), and the pre-crash `step.started` for step-P2 is
honored as history rather than re-emitted (exactly one start, ev-20, for
a step worked across two passes).

### 2. Identity-not-locator references

Every entity reference in every new-family payload in both streams is a
26-char ULID (mechanical check), and the payload structs type them as
such (`payloads.go`): `RunStarted.Batch`/`.Matters`, `RunSkipped.Run`,
`DispatchOpened.Run`/`.Matter`, `BatchMembership.Matter`,
`RoleSpawned.Dispatch`, `RunStoodDown.ActingClone`/`.OwningClone`.
Correctly *not* entity references: `RunStarted.Locator` and
`BatchCreated.Name` are the subject's own presentation label (the same
pattern as birth events), and `RoleSpawned.Name` is one of the six role
names — roles are named kinds, not referenced entities, the same way
`gate.closed` carries a gate name (P1 audit, item 2).

### 3. Tier dimensions

Checked against the frozen taxonomy sets (`V2Taxonomy`–`V4Taxonomy`) and
against every row (mechanical check): `run.*`, `dispatch.*`, and `role.*`
are `execution` — `repo`, `clone`, `worktree` all populated on every
instance; `batch.*` is `batchScoped` — **null `repo`** with
`clone`/`worktree` populated on all three instances (D56/D39, visible in
trace 6 ev-07–ev-09). D38 holds in the projections the events feed: the
Run keys at Clone (`runs.clone`, F9's ownership test), roles at
Clone/Dispatch, the Batch at neither. One nuance, correct on inspection:
`run.skipped`'s *subject* is the skipped Matter (a durable object) with
the Run in the payload — the event is still execution-classed and carries
all three dimensions, since the skip is an act of this Clone's pass, not
a property of the Matter.

### 4. System-driven events first-class

The P1 audit could only note that `role:*` actors did not exist yet. They
do now, and the two streams show the full alternation: `role:orchestrator`
opens/reaps claims, starts nodes, skips, and closes the Run;
`role:builder` finishes what it worked and closes itself; `human` appears
exactly where a person acted (verb-surface writes, spawning the
Orchestrator, trace 6's manual `role close` — F2's caveat noted). The
actor claim is *verified, not asserted*: committing as `role:X` requires
an open spawn behind it, shown refusing live in trace 7
(`actor role:warden claims a role with no open spawn behind it`), and
gate ownership rides the same check (`refusal.gate-owner`, D14) — trace
7's whole live pane is this rule enforced three ways. F1 (fixed above)
was found by auditing this item.

### 5. Enough to reconstruct in-flight work

Trace 6's interrupted pane is this item live: after a real `os.Exit`
mid-step, `wip status` reads the half-worked graph and `wip run
show`/`run list` read the open Run — all from maintained projections
(D46 invariant 2), no replay. Liveness is correctly *not* state:
`interrupted` derives from the released advisory lock, `ownership` from
the Run's Clone (F9). The crash itself is an absence, not an event — the
dangling open rows are the record, and Resume's reconstruction consumed
exactly them. The reaped-with-bracket rule (D59) is projection-backed,
not narrative: builder-2 has no `role.closed` event, yet its row reads
`closed/reaped` (projected from its bracket's reap, `project.go`), so
`wip session`'s census asymmetry (5 spawned · 4 closed) loses nothing.

### 6. Enough to narrate a sealed Matter after the fact

Both traces are the demonstration: each narrates its whole story — who
did what, in what order, entailed by what — from the event table alone,
to a reader with no access to the fixture machine. The Run's arc
(`run.started` → interruption-as-absence → `run.resumed` →
`run.finished`) and the Batch's non-arc (created, joined twice, then
untouched through a crash — `state: live` throughout) are both fully
recoverable. Export-on-seal remains deferred-earned and unclaimed, as
MODEL §10 states.

## Types with zero instances in the evidence streams

Audited statically (emitter + payload + taxonomy + projection), not
exercised, listed so absence is never mistaken for coverage (D65):

- `batch.left` (`writesurface/batch.go`: membership check then a
  `BatchMembership` draft — same shape as `batch.joined`), `batch.dismissed`
  (named Batches only; the anonymous-batch refusal guards it), and
  `batch.swept` (`writesurface/seal.go`: emitted by the seal path for an
  anonymous Batch) — all `batchScoped`, all subject-the-Batch.
- `run.skipped` (`loop.go` `skip()`: orchestrator-actor, Matter subject,
  Run + typed reason in payload) and `run.stood-down`
  (`writesurface/run.go`: one commit chaining the stand-down and every
  orphan `dispatch.closed`, each orphan under its own original tier
  dimensions, acting and owning Clone both carried — F9's
  differ-by-design pair). Both are exercised by the scheduler and CLI
  test suites (`loop_test.go`, `run_e2e_test.go`,
  `orchestrator_e2e_test.go`), just not by these two stories.

The `forge.*` names in trace 7's narrated pane are not audited: they are
explicitly illustrative — the P4 family's taxonomy does not exist to
audit, which is the honest boundary of this exercise.
