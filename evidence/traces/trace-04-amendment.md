# Trace 4 — an amendment mid-flight (Step inserted)

PLAN 1.7 trace 4: a Step inserted into a Matter that is already in flight,
demonstrating why events key on identity, not position (D44)
(`events-traces.md` step-04).

Produced by driving the real binary against a throwaway store and clone,
identically to traces 1–3. The store also holds the same three
tier-attachment setup events (`repo.attached`/`clone.attached`/
`worktree.attached`) preceding `matter.created`; omitted from the table
below for the same reason as traces 1–3 — they are `tiers`' narrative, not
this trace's.

Commands driven, in order:

```
$ wip matter create --title "wire the exporter events"
$ wip step create wire-the-exporter-events --title "wire the exporter"    # step-01
$ wip step create wire-the-exporter-events --title "add format tests"     # step-02
$ wip step create wire-the-exporter-events --title "tidy CLI help"        # step-03
$ wip start wire-the-exporter-events/step-01   # cascades matter+step
$ wip finish wire-the-exporter-events/step-01
$ wip start wire-the-exporter-events/step-02   # in flight — never finished
$ wip status                                                    # before insert
$ wip next                                                      # before insert
$ wip step insert wire-the-exporter-events \
    --after wire-the-exporter-events/step-01 --title "add retry logic"
$ wip status                                                    # after insert
$ wip next                                                      # after insert
```

No Stage was created (D2: Steps may hang directly under a Matter); the
spec calls for none here. No gate closes and nothing finishes past
`step-01` — this trace never seals the Matter; its whole point is the
insert into an in-flight Matter, not a finish.

**CLI note, not a fidelity gap:** `wip step insert <parent-locator>
--after/--before <sibling-locator>` resolves both the parent and the
sibling as full locators — `--after step-01` alone fails (`no matter
labeled "step-01"`; the flag value is resolved the same way any other
locator argument is, and a bare `step-01` names nothing on its own). The
sibling must be addressed the same way any Step is addressed elsewhere in
the CLI: `wire-the-exporter-events/step-01`. This is exactly how `--help`
documents the flags (`--after string`/`--before string`, no special
short-form carve-out) — not a divergence from the workplan, just a detail
worth recording so the next session doesn't rediscover it.

## Event table

Alias legend: `repo-A` = `01KYTB7PBF90JDX90GYE1RS08K` · `matter-A` =
`01KYTB7WS0G31HJXJV84A9D2FQ` · `step-01` = `01KYTB83CGXPBMF5DHDBYDJYGV` ·
`step-02` = `01KYTB83D714X3F19AFW00X2CG` · `step-03` =
`01KYTB83DYKHZ6ZM8Q85SJ9HSY` · `step-04-inserted` =
`01KYTB8VXCRWR1WSNWJD2ET32M` (the newly inserted Step — named
`step-04-inserted` in this legend to keep it visually distinct from
`step-01`/`-02`/`-03`; its own CLI-presentation locator, seen in its
`payload` below, is `step-04`).

| id | type | occurred_at | actor | causation | correlation | repo | clone | worktree | subject | payload |
|---|---|---|---|---|---|---|---|---|---|---|
| ev-01 | matter.created | 2026-07-30T20:25:19.904Z | human | ev-01 | ev-01 | repo-A | | | matter-A | `{"title":"wire the exporter events","locator":"wire-the-exporter-events","sort_key":0}` |
| ev-02 | step.created | 2026-07-30T20:25:26.672Z | human | ev-02 | ev-02 | repo-A | | | step-01 | `{"title":"wire the exporter","locator":"step-01","parent":"matter-A","sort_key":1000}` |
| ev-03 | step.created | 2026-07-30T20:25:26.695Z | human | ev-03 | ev-03 | repo-A | | | step-02 | `{"title":"add format tests","locator":"step-02","parent":"matter-A","sort_key":2000}` |
| ev-04 | step.created | 2026-07-30T20:25:26.718Z | human | ev-04 | ev-04 | repo-A | | | step-03 | `{"title":"tidy CLI help","locator":"step-03","parent":"matter-A","sort_key":3000}` |
| ev-05 | matter.started | 2026-07-30T20:25:26.738Z | human | ev-05 | ev-05 | repo-A | | | matter-A | `{"from":"planned","to":"in-progress","cascade":true}` |
| ev-06 | step.started | 2026-07-30T20:25:26.738Z | human | ev-05 | ev-05 | repo-A | | | step-01 | `{"from":"planned","to":"in-progress"}` |
| ev-07 | step.finished | 2026-07-30T20:25:26.759Z | human | ev-07 | ev-07 | repo-A | | | step-01 | `{"from":"in-progress","to":"done"}` |
| ev-08 | step.started | 2026-07-30T20:25:26.780Z | human | ev-08 | ev-08 | repo-A | | | step-02 | `{"from":"planned","to":"in-progress"}` |
| **ev-09** | **step.inserted** | 2026-07-30T20:25:51.788Z | human | ev-09 | ev-09 | repo-A | | | step-04-inserted | `{"title":"add retry logic","locator":"step-04","parent":"matter-A","sort_key":1500}` |

Every row is `repo`-only, `clone`/`worktree` null: every event here is a
durable-object event (CONTRACT §A / D38) — `step.inserted` is a birth
event exactly like `step.created` in that respect, not an execution
event. `id` order is the total order (D44/D51) and matches `occurred_at`'s
order exactly.

**ev-05→ev-06 is the D57 start-cascade**, one chain sharing correlation
`ev-05` (matter → step, ancestor-first), `payload.cascade: true` on the
ancestor the caller did not name (ev-05, the Matter) and not on ev-06 (the
Step the caller actually asked to start) — the same shape traces 1–2 show,
here with no Stage in between since this Matter has none (D2).

**The finding: ev-09 is purely additive.** `step.inserted`'s `subject` is
`step-04-inserted` — a **brand-new ULID** (`01KYTB8VXCRWR1WSNWJD2ET32M`),
not `step-02`'s. No prior event in the table above is rewritten, appended
to, or superseded by ev-09; `step-02`'s own `step.created` row (ev-03) and
its `step.started` row (ev-08) are byte-for-byte the same before and after
the insert — same `id`, same `subject`
(`01KYTB83D714X3F19AFW00X2CG`), same payload. Only ev-09's own payload
carries the placement: `sort_key: 1500`, strictly between `step-01`'s
`1000` and `step-02`'s `2000` (CONTRACT §A / D44 / D51) — the new Step is
placed between the two existing ones by an ordering value living in its
own birth event's payload, never by editing anyone else's `sort_key` or
`subject`. Had events keyed on position instead of identity ("the 2nd
Step"), inserting between step-01 and step-02 would have had to rewrite
or renumber every later reference to shift it down a slot; here nothing
downstream of the insert needed to change at all.

**Presentation-locator note, not a fidelity gap.** The inserted Step's
*CLI-facing* locator is `step-04` (`payload.locator` in ev-09), not a
renumbered `step-02` sitting between the old `step-01` and `step-02` —
`s.NextStepLocator` mints the next unused per-Matter ordinal regardless of
sort position (the same per-Matter-not-per-Stage numbering trace 2 already
documents for creation order). The two kinds of "position" are genuinely
different things: `sort_key` (ev-09's payload, `1500`) is where the Step
*sits* among siblings; the presentation locator (`step-04`) is just a
display label minted at birth and never renumbered afterward, in this
build. Both are consistent with the workplan's own wording — "Presentation
locators after it may renumber" is phrased as permission, not a
requirement — and neither touches the finding above: identity
(`subject`) is what every other event and every edge into this Step
would reference, and identity never moves.

## Rendered pane

`wip status`, captured after `step-02` starts and before the insert:

```
local repo 01KYTB7PBF90JDX90GYE1RS08K
  t4                 Clone · current

in progress:
  wire-the-exporter-events matter · in-progress
  wire-the-exporter-events · step-02 step · in-progress

finished:
  wire-the-exporter-events · step-01 step · sealed

next to start:
  wire-the-exporter-events · step-03 step · planned
```

`wip next`, same moment:

```
no cursor set for this clone — 1 unblocked:
  wire-the-exporter-events · step-03 step · planned
pick one: wip next --set <locator>
```

`wip status`, captured after `wip step insert`:

```
local repo 01KYTB7PBF90JDX90GYE1RS08K
  t4                 Clone · current

in progress:
  wire-the-exporter-events matter · in-progress
  wire-the-exporter-events · step-02 step · in-progress

finished:
  wire-the-exporter-events · step-01 step · sealed

next to start:
  wire-the-exporter-events · step-03 step · planned
  wire-the-exporter-events · step-04 step · planned
```

`wip next`, same moment:

```
no cursor set for this clone — 2 unblocked:
  wire-the-exporter-events · step-03 step · planned
  wire-the-exporter-events · step-04 step · planned
pick one: wip next --set <locator>
```

The new node (`step-04`) appears in both panes immediately after the
insert, alongside `step-03`, both `planned`; `step-02` stays
`in-progress` under `in progress:` throughout, unchanged by the insert
that happened around it, and `step-01` stays `sealed` under `finished:`.

**Display-order note, not a fidelity gap.** Both lists above show
`step-03` before `step-04`, even though `step-04`'s `sort_key` (`1500`)
sits *before* `step-03`'s (`3000`) — and before `step-02`'s (`2000`) too.
This is not a rendering bug: `internal/readsurface/frontier.go`'s
`Frontier` function returns the ready/unblocked set in **creation order**,
documented inline as "presentation-only" (D51) — the list order is not a
promise about sibling position, only `sort_key` (carried in each birth
event's own payload, per the table above) is. `wip status`'s `next to
start:` section and `wip next`'s `unblocked:` section both consume the
same `Frontier` call, so they agree with each other, just not with
sort-key order. The insert's placement (D44's actual claim) is evidenced
by ev-09's payload in the event table, not by list position in these
rendered panes.

## Confirming the identity claim directly

Before the insert, `step-02`'s node identity: `01KYTB83D714X3F19AFW00X2CG`
(ev-03's `subject`, ev-08's `subject`). After the insert (`wip status
--json`, same store):

```json
{
  "id": "01KYTB83D714X3F19AFW00X2CG",
  "address": "wire-the-exporter-events · step-02",
  "kind": "step",
  "lifecycle": "in-progress"
}
```

Same ULID, byte-for-byte, in the same position in the `inProgress` list —
the insert of an unrelated Step neither moved nor touched it. The new
Step appears in the same JSON's `ready` array under its own new identity:

```json
{
  "id": "01KYTB8VXCRWR1WSNWJD2ET32M",
  "address": "wire-the-exporter-events · step-04",
  "kind": "step",
  "lifecycle": "planned"
}
```
