# Trace 2 — Stages and Steps

PLAN 1.7 trace 2: a planned Matter with two Stages, one Stage boundary
crossed, showing that locally-complete and sealed are distinct moments in
the stream (D29) (`events-traces.md` step-02).

Produced by driving the real binary against a throwaway store and clone,
identically to trace 1. The store also holds the same three tier-attachment
setup events (`repo.attached`/`clone.attached`/`worktree.attached`)
preceding `matter.created`; omitted from the table below for the same
reason as trace 1 — they are `tiers`' narrative, not this trace's.

Commands driven, in order:

```
$ wip matter create --title "ship the widget exporter"
$ echo "Workplan: export widgets in two stages." | wip workplan ship-the-widget-exporter
$ wip stage create ship-the-widget-exporter --title "build"
$ wip stage create ship-the-widget-exporter --title "polish"
$ wip step create ship-the-widget-exporter/build   --title "wire the exporter"   # step-01
$ wip step create ship-the-widget-exporter/build   --title "add format tests"    # step-02
$ wip step create ship-the-widget-exporter/polish  --title "tidy CLI help"       # step-03
$ wip start ship-the-widget-exporter/build/step-01     # cascades matter+stage+step
$ wip finish ship-the-widget-exporter/build/step-01
$ wip start ship-the-widget-exporter/build/step-02
$ wip finish ship-the-widget-exporter/build/step-02
$ wip finish ship-the-widget-exporter/build            # Stage 1 boundary crossed
$ wip status                                           # all-children-Done, own gate open
$ wip start ship-the-widget-exporter/polish/step-03    # cascades stage+step
$ wip finish ship-the-widget-exporter/polish/step-03
$ wip finish ship-the-widget-exporter/polish
$ wip status                                           # captured a second time, same moment
$ wip gate close reviewed-local ship-the-widget-exporter
$ wip finish ship-the-widget-exporter                  # sealed
$ wip status
$ wip next
```

**Note on ordering:** the first `wip status` capture below is taken after
both Stages finish (all children Done), immediately before the gate
closes — this is the all-children-Done-but-not-sealed window the trace
exists to show. (Locator note: step-locators are numbered per-Matter, not
per-Stage — the third step created, under `polish`, is `step-03`, not a
fresh `step-01`; the driver script above reflects the real locators the
store assigned, not the illustrative `step-NN` addresses the workplan
text uses generically.)

## Event table

Alias legend: `repo-A` = `01KYTA6TA47KA3NAHWFQS60JY8` · `matter-A` =
`01KYTA6TBGNM2FVVEACW8MCED7` · `stage-build` = `01KYTA6TCXM0WBMTSWV69YVV49`
· `stage-polish` = `01KYTA6TDK3D6MB57ESPX252HM` · `step-01` =
`01KYTA6TE9R7DKSVEMWBXNV6S9` · `step-02` = `01KYTA6TF06ZX6HTGQYN59PBET` ·
`step-03` = `01KYTA6TFPT5QJG212Z94EQAPW`.

| id | type | occurred_at | actor | causation | correlation | repo | clone | worktree | subject | payload |
|---|---|---|---|---|---|---|---|---|---|---|
| ev-01 | matter.created | 2026-07-30T20:07:16.080Z | human | ev-01 | ev-01 | repo-A | | | matter-A | `{"title":"ship the widget exporter","locator":"ship-the-widget-exporter","sort_key":0}` |
| ev-02 | content.created | 2026-07-30T20:07:16.103Z | human | ev-02 | ev-02 | repo-A | | | matter-A | `{"kind":"workplan"}` |
| ev-03 | stage.created | 2026-07-30T20:07:16.125Z | human | ev-03 | ev-03 | repo-A | | | stage-build | `{"title":"build","locator":"build","parent":"matter-A","sort_key":1000}` |
| ev-04 | stage.created | 2026-07-30T20:07:16.147Z | human | ev-04 | ev-04 | repo-A | | | stage-polish | `{"title":"polish","locator":"polish","parent":"matter-A","sort_key":2000}` |
| ev-05 | step.created | 2026-07-30T20:07:16.169Z | human | ev-05 | ev-05 | repo-A | | | step-01 | `{"title":"wire the exporter","locator":"step-01","parent":"stage-build","sort_key":1000}` |
| ev-06 | step.created | 2026-07-30T20:07:16.192Z | human | ev-06 | ev-06 | repo-A | | | step-02 | `{"title":"add format tests","locator":"step-02","parent":"stage-build","sort_key":2000}` |
| ev-07 | step.created | 2026-07-30T20:07:16.215Z | human | ev-07 | ev-07 | repo-A | | | step-03 | `{"title":"tidy CLI help","locator":"step-03","parent":"stage-polish","sort_key":1000}` |
| ev-08 | matter.started | 2026-07-30T20:07:16.237Z | human | **ev-08** | **ev-08** | repo-A | | | matter-A | `{"from":"planned","to":"in-progress","cascade":true}` |
| ev-09 | stage.started | 2026-07-30T20:07:16.238Z | human | ev-08 | ev-08 | repo-A | | | stage-build | `{"from":"planned","to":"in-progress","cascade":true}` |
| ev-10 | step.started | 2026-07-30T20:07:16.238Z | human | ev-09 | ev-08 | repo-A | | | step-01 | `{"from":"planned","to":"in-progress"}` |
| ev-11 | step.finished | 2026-07-30T20:07:16.260Z | human | ev-11 | ev-11 | repo-A | | | step-01 | `{"from":"in-progress","to":"done"}` |
| ev-12 | step.started | 2026-07-30T20:07:16.282Z | human | ev-12 | ev-12 | repo-A | | | step-02 | `{"from":"planned","to":"in-progress"}` |
| ev-13 | step.finished | 2026-07-30T20:07:16.303Z | human | ev-13 | ev-13 | repo-A | | | step-02 | `{"from":"in-progress","to":"done"}` |
| **ev-14** | **stage.finished** | 2026-07-30T20:07:16.326Z | human | ev-14 | ev-14 | repo-A | | | stage-build | `{"from":"in-progress","to":"done"}` |
| ev-15 | stage.started | 2026-07-30T20:07:16.371Z | human | **ev-15** | **ev-15** | repo-A | | | stage-polish | `{"from":"planned","to":"in-progress","cascade":true}` |
| ev-16 | step.started | 2026-07-30T20:07:16.371Z | human | ev-15 | ev-15 | repo-A | | | step-03 | `{"from":"planned","to":"in-progress"}` |
| ev-17 | step.finished | 2026-07-30T20:07:16.394Z | human | ev-17 | ev-17 | repo-A | | | step-03 | `{"from":"in-progress","to":"done"}` |
| **ev-18** | **stage.finished** | 2026-07-30T20:07:16.416Z | human | ev-18 | ev-18 | repo-A | | | stage-polish | `{"from":"in-progress","to":"done"}` |
| ev-19 | gate.closed | 2026-07-30T20:07:16.494Z | human | ev-19 | ev-19 | repo-A | | | matter-A | `{"gate":"reviewed-local","scale":"matter"}` |
| **ev-20** | **matter.finished** | 2026-07-30T20:07:16.516Z | human | ev-20 | ev-20 | repo-A | | | matter-A | `{"from":"in-progress","to":"done"}` |

**The one Stage boundary crossed** is ev-14 (`stage.finished` on
`stage-build`), followed by ev-15/ev-16 — the second cascade — starting
`stage-polish`.

**The two D57 cascades, one chain each:** ev-08→ev-09→ev-10 is one
three-generation chain sharing correlation `ev-08` (matter → stage → step,
ancestor-first), with `payload.cascade: true` on exactly the two ancestors
the caller did not name (ev-08, ev-09) and not on ev-10, the step the
caller actually asked to start. ev-15→ev-16 is the second, two-generation
cascade (stage → step; the matter was already started, so it does not
recur), sharing correlation `ev-15`, `cascade: true` on ev-15 only.

**The divergence this trace exists to show** is between **ev-18**
(`stage.finished` on `stage-polish` — the last child finishes, marking
**all-children-Done**) and the **ev-19/ev-20 pair** (`gate.closed` +
`matter.finished` — marking **sealed**). Between ev-18 and ev-19 the Matter
is done-but-not-sealed: every child is Done, but the Matter's own
`reviewed-local` gate is still open. The first `wip status` capture below
is taken in exactly that window.

## Rendered pane

`wip status`, captured after ev-18 (`stage.finished` on `polish`) and
before the gate closes — all-children-Done, own gate still open:

```
local repo 01KYTA6TA47KA3NAHWFQS60JY8
  t2                 Clone · current

in progress:
  ship-the-widget-exporter matter · in-progress

finished:
  ship-the-widget-exporter/build stage · locally complete
  ship-the-widget-exporter/polish stage · locally complete
  ship-the-widget-exporter/build · step-01 step · locally complete
  ship-the-widget-exporter/build · step-02 step · locally complete
  ship-the-widget-exporter/polish · step-03 step · locally complete
```

Every child reports `locally complete` (D29's satisfied-at-locally-complete
reading, distinct from `sealed`); the Matter itself still shows
`in-progress` under the `in progress:` heading — its own gate has not
closed, so it is not yet locally complete or sealed even though every
descendant is Done.

`wip status`, captured after `wip gate close` + `wip finish` — sealed:

```
local repo 01KYTA6TA47KA3NAHWFQS60JY8
  t2                 Clone · current

finished:
  ship-the-widget-exporter matter · sealed
  ship-the-widget-exporter/build stage · sealed
  ship-the-widget-exporter/polish stage · sealed
  ship-the-widget-exporter/build · step-01 step · sealed
  ship-the-widget-exporter/build · step-02 step · sealed
  ship-the-widget-exporter/polish · step-03 step · sealed
```

`wip next`, captured at the same final moment:

```
nothing in progress or planned — every Matter sealed
```

All-children-Done (the first capture) and sealed (the second) are the two
distinct `id` positions the trace names them as: `locally complete` never
becomes `sealed` by itself — only the Matter's own `gate.closed` +
`matter.finished` pair, order-independent as a predicate over the pair
(D55), moves every descendant's reported status from `locally complete` to
`sealed` in the same read.
