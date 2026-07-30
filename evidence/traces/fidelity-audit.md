# `events-traces` step-06 — fidelity audit against MODEL §10

Walks MODEL §10's fidelity list (`/Users/beausimensen/Code/wip-reboot/MODEL.md`
§10, lines ~335–376), item by item exactly as enumerated in
`events-traces.md` step-06, against all five committed traces
(`evidence/traces/trace-0{1..5}-*.md`) — every event of every trace, not
sampled (D65). Checked directly against the source (`internal/store/taxonomy.go`,
`internal/store/payloads.go`, `internal/readsurface/next.go`) wherever a
trace's own aliasing or trimming could otherwise hide a divergence, not
just against the rendered tables.

**Result: no fidelity gap found. All six items hold across all five
streams.**

## 1. Append-only

No event in any of the five streams is mutated or deleted in place. `id` is
monotonic and matches `occurred_at` order in every trace. Trace 4 (the
primary witness) confirms the one amendment exercised across the set,
`step.inserted` (ev-09), is purely additive: `step-02`'s own `step.created`
(ev-03) and `step.started` (ev-08) rows are unchanged in `id`, `subject`,
and payload before and after the insert, confirmed directly against `wip
status --json`'s post-insert read of the same node identity. No trace in
this set exercises a tombstone (`step.removed`) or the rest of the
amendment family (`step.reordered`, `step.replaced`) — out of scope for
what these five streams contain, not a gap in what they do contain.

## 2. Identity-not-locator references

Every `subject` and every intra-payload entity reference resolves to a
ULID, never a locator, across all five tables — confirmed against
`internal/store/payloads.go`'s payload structs, not just the rendered
(aliased) tables, since aliasing alone can't distinguish "real ULID shown
as an alias" from "a locator string that happens to print the same":

- `NodeBirth.Parent`, `BacklogPlanned.Matter`, `BatchMembership.Matter` are
  all typed as the referenced entity's ULID.
- `CursorMoved.Node` (trace 5 ev-08, table shows `{"node":"step-01"}`) is
  set from `target.ID` in `internal/readsurface/next.go:248` — a ULID, not
  the locator string the CLI took as input — confirmed by reading the
  emitter, not inferred from the trimmed table.
- The `locator` field every birth event's own payload carries (e.g.
  `matter.created`'s `payload.locator`) is the node announcing its *own*
  new presentation label, not a reference to another entity — correctly
  distinct from the identity-reference rule this item audits.
- `gate.closed`'s `payload.gate` is a gate *name* (`GateClosed.Gate
  string`), not an entity reference — gates carry no ULID identity in this
  model (CONTRACT), so a name is the only thing there is to carry.

## 3. Tier dimensions

Checked against `internal/store/taxonomy.go`'s `P1Taxonomy` (the static
per-type dimension rule, D56) as well as the five tables directly. Every
type present in the evidence matches its declared rule exactly:

- Durable-object types (`matter.*`/`stage.*`/`step.*`, `content.*`,
  `step.inserted`, `backlog.*`, `gate.closed`) carry `repo` only,
  `clone`/`worktree` null — every row of traces 1–4 and the durable rows of
  trace 5 (including the ones typed from clone-Y: `matter.started`,
  `step.started`, `step.finished`, ev-09–ev-11).
- `batch.created` (trace 5 ev-05) is `batchScoped`: `repo` null,
  `clone`/`worktree` populated — matches.
- `dispatch.opened`, `render.performed`, `cursor.moved` (trace 5 ev-06–08)
  are `execution`: `repo`, `clone`, and `worktree` all populated — matches.

Trace 5 is the primary witness and the only stream exercising anything but
durable-object dimensions; no divergence between the taxonomy's static rule
and what any trace's stream actually carries.

## 4. System-driven events first-class

No `role:*` or `system:<source>` actor appears in any of the five traces —
expected for P1 (HANDOFF §1.1: no outer-loop roles yet, so no emitter for
either exists). Every event in every trace names `actor: human`, never
empty — **a single distinct actor value across all five streams is the
correct result, not a finding**, per step-06's own explicit instruction not
to demand a second value P1 cannot produce.

Every D57 cascade reads as one chain, ancestor-first, sharing one
correlation, `causation`/`correlation` never pointing forward:

- Trace 2: three generations, `ev-08→ev-09→ev-10` (matter→stage→step),
  correlation `ev-08`; `cascade: true` on ev-08/ev-09, absent on ev-10. A
  second, two-generation cascade at `ev-15→ev-16` (stage→step; the matter
  was already started), correlation `ev-15`, `cascade: true` on ev-15 only.
- Trace 4: two generations, `ev-05→ev-06` (matter→step, no Stage exists in
  this Matter), correlation `ev-05`, `cascade: true` on ev-05 only.
- Trace 5: two generations, `ev-09→ev-10` (matter→step, typed from
  clone-Y), correlation `ev-09`, `cascade: true` on ev-09 only.
- Traces 1 and 3 contain no cascades (no descendant node exists to cascade
  into); every event in both is its own origin, self-referential
  `id:causation:correlation`, correctly per MODEL §10's origin-event rule.

`payload.cascade: true` lands on exactly the ancestors the caller did not
name, and never on the node the caller actually asked to start, in every
occurrence across the set — consistent.

## 5. Enough to reconstruct in-flight work

Traces 2 and 4, the mid-flight witnesses, are both reconstructable from
their streams via a SELECT/projection read (the real `wip status`/`wip
next` CLI against the same store), not ad-hoc replay:

- Trace 2's first capture (after ev-18, before the gate closes):
  all-children-Done, Matter still `in-progress` — recoverable directly from
  the `stage.finished`×2 / no `matter.finished` yet state of the stream.
- Trace 4's captures (after `step-02` starts, before and after the insert):
  `step-02` in-progress, `step-01` sealed, no cursor set — recoverable
  directly from the lifecycle events plus the absence of any `cursor.moved`
  row in this trace.

## 6. Sealed-Matter narratability

Traces 1 and 2, the sealed witnesses: both streams' types, timestamps,
subjects, payloads, and order are sufficient to narrate the sealed Matter
after the fact with no access to the machine — birth, plan/body, start,
(Stage crossings in trace 2), gate close, finish, in strict `id`/timestamp
order, every transition payload carrying its own `from`/`to`.

One point worth recording, not a gap: the committed tables' `content.created`
payloads are trimmed to `{"kind": ...}` by `mktable.py` for table
readability (`session-handoff.md` step 4) — the *real* `ContentWritten`
payload (`internal/store/payloads.go`) carries the content bytes (or a
spill reference plus sidecar file) inline, per D36/D61's "the log is source
of truth for content." The trimming is a display choice this evidence set
makes consistently, not a claim about what the actual stream carries, and
the sidecar-spill case is an already-made, already-documented design call
(D36/D61), not something this audit is re-opening.

Export-on-seal itself remains deferred-earned and unmet by design (MODEL
§10: "one file, one machine") — this item confirms the narratability
*precondition* holds, not that export-on-seal is implemented.

## Non-audit notes (standing findings, not MODEL §10 gaps)

Per `session-handoff.md`'s "Known divergences" section, folded in here for
completeness, not as audit findings:

1. `events-traces.md` step-01's illustrative sequence lists
   `backlog.planned` before `matter.created`; the real verb surface cannot
   produce that order (`wip backlog plan` takes an existing matter
   locator). Trace 1 documents and shows the real order. Recommend
   amending step-01's illustrative list upstream to match.
2. A correctness bug in `wip next`, found incidentally while producing
   trace 1, not a MODEL §10 fidelity gap: `internal/readsurface/next.go`'s
   `noCursorView` only ever consults the Planned frontier, never
   in-progress nodes, so a Matter started with no cursor set and nothing
   else Planned reports `EverythingSealed` even though it is actively In
   Progress. Worth a Backlog entry against `read-surface`; out of this
   Matter's remit to fix.

## Seal condition

All five traces exist, showing event sequence and state/session evolution;
this audit finds no fidelity gap. Per the workplan's done-criterion, the
Matter's seal condition is discharged as written — **no amendment is
required as a result of this audit.**
