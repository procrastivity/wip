# Trace 3 — a Session spanning two Matters, with an idle gap

PLAN 1.7 trace 3: two Matters worked across one idle gap, rendered as one
session at a 12h gap, two at 2h, from the same stream — proof that Session
is a derived query parameterized by idle gap (D17; MODEL §2.4), never
persisted (`events-traces.md` step-03).

**How this trace was produced, honestly stated.** The CLI has no
clock-override flag, and producing a gap strictly between 2h and 12h by
literally waiting is not practical. So the durable events below were
emitted by calling the real `internal/writesurface` verb functions —
`CreateMatter`, `WriteOnce`, `Start`, `Finish` — the exact Go functions
Cobra's `wip matter create` / `wip body` / `wip start` / `wip finish`
commands call, with the store opened via `store.OpenWithClock` and a fake
clock advanced by hand between calls, rather than via the CLI shell. This
is the same posture `read-surface`'s own step-07 tests and `tiers`'
worked-examples Stage take when the verb they need predates their own
Matter in the register: driving the real write path directly, not
hand-authoring rows. The **rendered pane** below, in contrast, is the real
`wip session` CLI command reading this same store — Session doesn't care
who wrote an event, only what its timestamp says, so the read side of this
trace is undiminished. The one-off generator lived at
`cmd/tracegen3/main.go` in this repo for the run and was deleted
immediately after; it is not part of the delivered binary.

Sequence produced: Matter A (`backfill-widget-cache-metrics`) created,
given a body, started, and finished across a ~35-minute window; then a
gap; then Matter B (`tidy-the-clone-relink-message`) taken through the
same four steps across a ~25-minute window. The gap between Matter A's
last event and Matter B's first is **6 hours exactly** — strictly between
the trace's two test values, 2h and 12h.

## Event table

Alias legend: `repo-A` = `01KYTAHG2ZKWGH4QERQ5TR7VS3` · `matter-A`
(`backfill-widget-cache-metrics`) = `01KYTAKB0G0K2RRDGS2VHVFDBR` ·
`matter-B` (`tidy-the-clone-relink-message`) =
`01KYV16KHGAS5NCA3TZBZZ18PR`. The three tier-attachment setup events
(`repo.attached`/`clone.attached`/`worktree.attached`, from the real `wip
init` that preceded this generator run) precede the table and are omitted
for the same reason as traces 1–2; `wip session`'s event counts below
include them.

| id | type | occurred_at | actor | causation | correlation | repo | clone | worktree | subject | payload |
|---|---|---|---|---|---|---|---|---|---|---|
| ev-01 | matter.created | 2026-07-30T20:14:06.352Z | human | ev-01 | ev-01 | repo-A | | | matter-A | `{"title":"backfill widget cache metrics","locator":"backfill-widget-cache-metrics","sort_key":0}` |
| ev-02 | content.created | 2026-07-30T20:17:06.352Z | human | ev-02 | ev-02 | repo-A | | | matter-A | `{"kind":"body"}` |
| ev-03 | matter.started | 2026-07-30T20:19:06.352Z | human | ev-03 | ev-03 | repo-A | | | matter-A | `{"from":"planned","to":"in-progress"}` |
| ev-04 | matter.finished | 2026-07-30T20:49:06.352Z | human | ev-04 | ev-04 | repo-A | | | matter-A | `{"from":"in-progress","to":"done"}` |
| | | *(6h idle gap — no events)* | | | | | | | | |
| ev-05 | matter.created | 2026-07-31T02:49:06.352Z | human | ev-05 | ev-05 | repo-A | | | matter-B | `{"title":"tidy the clone-relink message","locator":"tidy-the-clone-relink-message","sort_key":0}` |
| ev-06 | content.created | 2026-07-31T02:51:06.352Z | human | ev-06 | ev-06 | repo-A | | | matter-B | `{"kind":"body"}` |
| ev-07 | matter.started | 2026-07-31T02:54:06.352Z | human | ev-07 | ev-07 | repo-A | | | matter-B | `{"from":"planned","to":"in-progress"}` |
| ev-08 | matter.finished | 2026-07-31T03:14:06.352Z | human | ev-08 | ev-08 | repo-A | | | matter-B | `{"from":"in-progress","to":"done"}` |

## Rendered pane

`wip session --idle-gap 12h` — the 6h gap is below the 12h threshold, so
it does **not** split: one session spans both Matters.

```
2026-07-30T15:13:06-05:00 — 2026-07-30T22:14:06-05:00 (11 events)
```

`wip session --idle-gap 2h` — the same 6h gap exceeds the 2h threshold, so
it **splits** at the gap: two sessions, one per Matter.

```
2026-07-30T15:13:06-05:00 — 2026-07-30T15:49:06-05:00 (7 events)

2026-07-30T21:49:06-05:00 — 2026-07-30T22:14:06-05:00 (4 events)
```

(Timestamps print in the local `-05:00` offset; the underlying
`occurred_at` values are the UTC ones in the event table above — 20:14Z
through 20:49Z for the first window, 02:49Z through 03:14Z the next day
for the second, a 6-hour gap between them.)

Nothing about the stream changed between the two renderings — the same
eight domain events (plus the three tier-attachment events) exist either
way. Only the query parameter, `--idle-gap`, changed, and it alone decided
whether the 6-hour silence between Matter A's last event and Matter B's
first reads as one working period or two. This is Session's whole
contract (D17): a derived view, recomputed from the log on every call,
persisting nothing.
