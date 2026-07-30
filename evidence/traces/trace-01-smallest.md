# Trace 1 — smallest complete trace

PLAN 1.7 trace 1: a bugfix Matter with no plan, the `reviewed-local` gate
bound at matter scale, carried from a backlog intake all the way to sealed
(`events-traces.md` step-01).

Produced by driving the real binary against a throwaway store
(`WIP_DB_PATH` pointed at a scratch SQLite file) and a throwaway git clone
(`wip init`). The store also holds three tier-attachment events
(`repo.attached`, `clone.attached`, `worktree.attached`) that precede the
table below — they are `wip init` establishing this fixture's Repo, Clone,
and Worktree, which is `tiers`' own narrative, not this trace's. This
trace's own story begins at `backlog.entered`. `wip session`'s event count
below (10) includes those three setup events, since Session is derived
over the whole store, not over this table's subset.

Commands driven, in order:

```
$ wip init
$ wip gate declare reviewed-local --scale matter
$ wip backlog add --provenance intake --title "clone-detect flakes on CI"
$ wip matter create --title "fix flaky clone detect"
$ wip backlog plan <backlog-01> fix-flaky-clone-detect
$ echo "Bugfix: clone-detect flakes intermittently on CI when two remotes race during common-dir resolution." | wip body fix-flaky-clone-detect
$ wip start fix-flaky-clone-detect
$ wip status                          # captured before the gate closes
$ wip gate close reviewed-local fix-flaky-clone-detect
$ wip finish fix-flaky-clone-detect
$ wip status                          # captured after
$ wip session
```

**Implementer's note, not a fidelity gap:** the workplan's illustrative
sequence lists `backlog.planned` before `matter.created`. The real verb
surface cannot produce that order — `wip backlog plan <entry-id>
<matter-locator>` takes an *existing* matter locator (it promotes an entry
into a Matter that already exists; it does not birth one), so
`matter.created` necessarily precedes `backlog.planned`. This is what
actually happens when the real verb is driven, and is what the table
below shows. Flagged for the workplan text to be corrected upstream
(`events-traces.md` step-01), not silently reconciled here.

## Event table

Alias legend: `repo-A` = `01KYTA3A2VWPN90FT1T1D73V5W` · `clone-A` =
`01KYTA3A2VWPN90FT1T1D73V5X` · `worktree-A` = `01KYTA3A2VWPN90FT1T1D73V5Y` ·
`backlog-01` = `01KYTA3A46BRKTY5X6ETHNDERR` · `matter-A` =
`01KYTA3A5KFW0S4MBGKCBV7JFV`.

| id | type | occurred_at | actor | causation | correlation | repo | clone | worktree | subject | payload |
|---|---|---|---|---|---|---|---|---|---|---|
| ev-01 | backlog.entered | 2026-07-30T20:05:21.158Z | human | ev-01 | ev-01 | repo-A | | | backlog-01 | `{"provenance":"intake","title":"clone-detect flakes on CI"}` |
| ev-02 | matter.created | 2026-07-30T20:05:21.203Z | human | ev-02 | ev-02 | repo-A | | | matter-A | `{"title":"fix flaky clone detect","locator":"fix-flaky-clone-detect","sort_key":0}` |
| ev-03 | backlog.planned | 2026-07-30T20:05:21.224Z | human | ev-03 | ev-03 | repo-A | | | backlog-01 | `{"matter":"matter-A"}` |
| ev-04 | content.created | 2026-07-30T20:05:21.247Z | human | ev-04 | ev-04 | repo-A | | | matter-A | `{"kind":"body"}` |
| ev-05 | matter.started | 2026-07-30T20:05:21.268Z | human | ev-05 | ev-05 | repo-A | | | matter-A | `{"from":"planned","to":"in-progress"}` |
| ev-06 | gate.closed | 2026-07-30T20:05:21.363Z | human | ev-06 | ev-06 | repo-A | | | matter-A | `{"gate":"reviewed-local","scale":"matter"}` |
| ev-07 | matter.finished | 2026-07-30T20:05:21.385Z | human | ev-07 | ev-07 | repo-A | | | matter-A | `{"from":"in-progress","to":"done"}` |

Every row is `repo`-only, `clone`/`worktree` null: every event here is a
durable-object event (CONTRACT §A / D38), consistent since there is no
Stage, Step, or execution event anywhere in this trace. `id` order is the
total order (D44/D51) and matches `occurred_at`'s order exactly.

## Rendered pane

`wip status`, captured immediately after `wip start` and before the gate
closes:

```
local repo 01KYTA3A2VWPN90FT1T1D73V5W
  t1                 Clone · current

in progress:
  fix-flaky-clone-detect   matter · in-progress
```

`wip status`, captured after `wip gate close` + `wip finish`:

```
local repo 01KYTA3A2VWPN90FT1T1D73V5W
  t1                 Clone · current

finished:
  fix-flaky-clone-detect   matter · sealed
```

The Matter is In Progress with its gate still open in the first capture,
and reported `finished`/`sealed` in the second — with nothing appearing
under `in progress:` at all, since the section is omitted when empty. The
sealed state is reachable only through the `reviewed-local` close: no
bespoke `review` verb exists (CONTRACT §C, `vocabulary` step-02) — the
close above went through `wip gate close`.

`wip session`, captured after `wip finish` (over the whole store,
including the three tier-attachment events noted above):

```
2026-07-30T15:05:21-05:00 — 2026-07-30T15:05:21-05:00 (10 events)
```

One contiguous working period, as expected — every event in this fixture
was emitted well inside a single idle-gap window.
