# Trace 8 — config history is narratable

`evented-config` step-05: the existing log read surface narrates a config
change, a gate declaration with its explicit exemption snapshot, and a
repair, end to end — with no new read verb built for it
(`evented-config.md` step-05).

Produced the same way traces 1–7 were: driving the real binary against a
throwaway store (`WIP_DB_PATH` pointed at a scratch SQLite file) and a
throwaway git clone (`wip init`), then dumping the store's own event rows
and capturing the real rendered panes. The store also holds three
tier-attachment events (`repo.attached`, `clone.attached`,
`worktree.attached`) that precede the table below; as in every prior trace
they are `tiers`' own setup narrative and are omitted from the committed
table (`wip plumbing session`'s event count below, 23, includes them —
Session derives over the whole store).

This trace tells one story across two Matters so the three properties the
Step asks for show up in a natural order rather than three disconnected
fixtures: Matter A is built and sealed under one gate; the Repo's
`tracker.push-level` is then changed; a second gate is declared and
*because Matter A is already sealed*, its declaration snapshots Matter A
into its `Exempt` list — the explicit exemption payload (`evented-config`
step-01's resolved open call). Matter B is then built under both gates,
but a third, Step-scale gate is declared for it *before* Matter B's own
gates are closed, so Matter B's Step is not yet sealed at declare time and
gets no exemption; once the enclosing gates close, the Step's exemption is
restored explicitly with a repair.

Commands driven, in order:

```
$ wip init
$ wip plumbing gate declare reviewed-local --scale matter

# --- Matter A: built and sealed under reviewed-local ---
$ wip plumbing matter create --title "ship the config exemption path"
$ wip plumbing step create ship-the-config-exemption-path --title "wire the exemption snapshot"
$ wip plumbing start ship-the-config-exemption-path/step-01
$ wip plumbing finish ship-the-config-exemption-path/step-01
$ wip plumbing finish ship-the-config-exemption-path
$ wip plumbing gate close reviewed-local ship-the-config-exemption-path
$ wip plumbing status                                          # sealed

# --- the config change ---
$ wip plumbing outbox level narrated                            # config.set tracker.push-level=narrated

# --- a gate declared after a Matter is already sealed: non-empty Exempt ---
$ wip plumbing gate declare second-look --scale matter

# --- Matter B: built under both gates, one gate declared mid-flight ---
$ wip plumbing matter create --title "backfill the missed exemption"
$ wip plumbing step create backfill-the-missed-exemption --title "restore the pre-v8 snapshot"
$ wip plumbing start backfill-the-missed-exemption/step-01
$ wip plumbing finish backfill-the-missed-exemption/step-01
$ wip plumbing gate declare local-check --scale step             # Matter B's own gates still open here
$ wip plumbing gate close reviewed-local backfill-the-missed-exemption
$ wip plumbing gate close second-look backfill-the-missed-exemption
$ wip plumbing gate status backfill-the-missed-exemption/step-01 # local-check: still open

# --- the repair ---
$ wip plumbing gate repair local-check backfill-the-missed-exemption/step-01
$ wip plumbing gate status backfill-the-missed-exemption/step-01 # local-check: exempt; sealed: yes
$ wip plumbing finish backfill-the-missed-exemption
$ wip plumbing gate list
$ wip plumbing status
$ wip plumbing session
```

Gate names `reviewed-local`, `second-look`, and `local-check` were chosen
deliberately human-owned (`store.GateOwner` names only `verified`,
`reviewed`, and `ci-green` as outer-loop-role-owned — `schema`'s D14
table). Using a role-owned name here would have pulled the inert-Warden
refusal lattice (trace 7's story) into a trace about config and exemption
narratability, which is not this Step's concern.

## Event table

Alias legend: `repo-C` = `01M2Y70P86ZJP39S2QBPFK4VN5` · `clone-C` =
`01M2Y70P86ZJP39S2QBPFK4VN6` · `worktree-C` =
`01M2Y70P86ZJP39S2QBPFK4VN7` · `matter-A` =
`01M2Y70WWMYD0MRK2Y4Y19CXP1` (`ship-the-config-exemption-path`) ·
`step-A1` = `01M2Y7125DNZBRFP044WJ9PTFW` (its `step-01`) · `matter-B` =
`01M2Y71DHP9QFXNY54HN0FVGXR` (`backfill-the-missed-exemption`) ·
`step-B1` = `01M2Y71JSW3AGCDPSR6GB177B8` (its `step-01`). The three
tier-attachment events are omitted as in every trace.

| id | type | occurred_at | actor | causation | correlation | repo | clone | worktree | subject | payload |
|---|---|---|---|---|---|---|---|---|---|---|
| ev-01 | gate.declared | 2026-09-20T01:31:21.301Z | human | ev-01 | ev-01 | repo-C | | | repo-C | `{"gate":"reviewed-local","scale":"matter"}` |
| ev-02 | matter.created | 2026-09-20T01:31:21.364Z | human | ev-02 | ev-02 | repo-C | | | matter-A | `{"title":"ship the config exemption path","locator":"ship-the-config-exemption-path","sort_key":0}` |
| ev-03 | step.created | 2026-09-20T01:31:26.765Z | human | ev-03 | ev-03 | repo-C | | | step-A1 | `{"title":"wire the exemption snapshot","locator":"step-01","parent":"matter-A","sort_key":1000}` |
| ev-04 | matter.started | 2026-09-20T01:31:32.747Z | human | ev-04 | ev-04 | repo-C | | | matter-A | `{"from":"planned","to":"in-progress","cascade":true,"tracker_push_level":"off"}` |
| ev-05 | step.started | 2026-09-20T01:31:32.748Z | human | ev-04 | ev-04 | repo-C | | | step-A1 | `{"from":"planned","to":"in-progress","tracker_push_level":"off"}` |
| ev-06 | step.finished | 2026-09-20T01:31:32.814Z | human | ev-06 | ev-06 | repo-C | | | step-A1 | `{"from":"in-progress","to":"done","tracker_push_level":"off"}` |
| ev-07 | matter.finished | 2026-09-20T01:31:32.885Z | human | ev-07 | ev-07 | repo-C | | | matter-A | `{"from":"in-progress","to":"done","tracker_push_level":"off"}` |
| ev-08 | gate.closed | 2026-09-20T01:31:32.950Z | human | ev-08 | ev-08 | repo-C | | | matter-A | `{"gate":"reviewed-local","scale":"matter","tracker_push_level":"off"}` |
| ev-09 | config.set | 2026-09-20T01:31:33.055Z | human | ev-09 | ev-09 | repo-C | | | repo-C | `{"key":"tracker.push-level","value":"narrated"}` |
| ev-10 | gate.declared | 2026-09-20T01:31:33.108Z | human | ev-10 | ev-10 | repo-C | | | repo-C | `{"gate":"second-look","scale":"matter","exempt":["matter-A"]}` |
| ev-11 | matter.created | 2026-09-20T01:31:38.423Z | human | ev-11 | ev-11 | repo-C | | | matter-B | `{"title":"backfill the missed exemption","locator":"backfill-the-missed-exemption","sort_key":0}` |
| ev-12 | step.created | 2026-09-20T01:31:43.804Z | human | ev-12 | ev-12 | repo-C | | | step-B1 | `{"title":"restore the pre-v8 snapshot","locator":"step-01","parent":"matter-B","sort_key":1000}` |
| ev-13 | matter.started | 2026-09-20T01:31:49.708Z | human | ev-13 | ev-13 | repo-C | | | matter-B | `{"from":"planned","to":"in-progress","cascade":true,"tracker_push_level":"narrated"}` |
| ev-14 | step.started | 2026-09-20T01:31:49.709Z | human | ev-13 | ev-13 | repo-C | | | step-B1 | `{"from":"planned","to":"in-progress","tracker_push_level":"narrated"}` |
| ev-15 | step.finished | 2026-09-20T01:31:49.761Z | human | ev-15 | ev-15 | repo-C | | | step-B1 | `{"from":"in-progress","to":"done","tracker_push_level":"narrated"}` |
| ev-16 | gate.declared | 2026-09-20T01:31:49.804Z | human | ev-16 | ev-16 | repo-C | | | repo-C | `{"gate":"local-check","scale":"step","exempt":["step-A1"]}` |
| ev-17 | gate.closed | 2026-09-20T01:31:56.527Z | human | ev-17 | ev-17 | repo-C | | | matter-B | `{"gate":"reviewed-local","scale":"matter","tracker_push_level":"narrated"}` |
| ev-18 | gate.closed | 2026-09-20T01:31:56.587Z | human | ev-18 | ev-18 | repo-C | | | matter-B | `{"gate":"second-look","scale":"matter","tracker_push_level":"narrated"}` |
| ev-19 | gate.exemption-repaired | 2026-09-20T01:31:56.647Z | human | ev-19 | ev-19 | repo-C | | | step-B1 | `{"gate":"local-check"}` |
| ev-20 | matter.finished | 2026-09-20T01:32:02.594Z | human | ev-20 | ev-20 | repo-C | | | matter-B | `{"from":"in-progress","to":"done","tracker_push_level":"narrated"}` |

Every row is `repo`-only, `clone`/`worktree` null: every event here is a
durable-object event (CONTRACT §A / D38), same as every trace before this
one that carries no execution events. `id` order is the total order
(D44/D51) and matches `occurred_at`'s order exactly.

**A finding the raw table makes visible, not a fidelity gap.** `ev-16`'s
`exempt` is `["step-A1"]`, not `step-B1` — the Step this declaration was
driven right in the middle of building. `sealedNodesAtScale`
(`internal/store/config.go`) snapshots every Done node *at that scale,
repo-wide* whose own and enclosing gates are already satisfied, not just
nodes related to the Matter the caller happens to be thinking about.
Step-A1 already qualifies: it is Done, has no own Step-scale gate, and its
enclosing Matter (`matter-A`) is fully sealed — including under
`second-look`, by the exemption `ev-10` granted it. Step-B1 does not
qualify yet: at `ev-16`, Matter B's own `reviewed-local` and `second-look`
are still open, so Step-B1 is Done but not sealed, and the snapshot
correctly leaves it out. That is exactly why `ev-19`'s explicit repair is
needed once Matter B's gates close (`ev-17`, `ev-18`) — the same
declare-time-only, no-retroactive-extension rule `evented-config` step-02
built (`GateExemptionRepaired` stays a separate event type "so a repeated
declaration can never extend its own original applicability boundary",
`internal/store/payloads.go`). The log narrates this precisely: read
`ev-16`'s `exempt` list, `ev-17`/`ev-18`'s closes, and `ev-19`'s repair
together and the whole reason is on the record, no replay of fold logic
required.

## Rendered pane

`wip plumbing status`, captured right after Matter A seals (before the
config change or the second gate):

```
local repo 01M2Y70P86ZJP39S2QBPFK4VN5
  repo               Clone · current

finished:
  ship-the-config-exemption-path matter · sealed
```

`wip plumbing gate status backfill-the-missed-exemption/step-01`, captured
after Matter B's own gates close but before the repair — `local-check`
still open, the Matter not yet locally complete:

```
backfill-the-missed-exemption · step-01  step · done
locally-complete: no
sealed: no
requirements:
  local-check [step, own, backfill-the-missed-exemption · step-01, owner human]: open
  reviewed-local [matter, enclosing, backfill-the-missed-exemption, owner human]: closed by human at 2026-09-20T01:31:56.527Z
  second-look [matter, enclosing, backfill-the-missed-exemption, owner human]: closed by human at 2026-09-20T01:31:56.587Z
```

The same command, captured immediately after `wip plumbing gate repair
local-check backfill-the-missed-exemption/step-01` — `local-check` reads
`exempt`, and the Step is locally complete and sealed:

```
backfill-the-missed-exemption · step-01  step · done
locally-complete: yes
sealed: yes
requirements:
  local-check [step, own, backfill-the-missed-exemption · step-01, owner human]: exempt
  reviewed-local [matter, enclosing, backfill-the-missed-exemption, owner human]: closed by human at 2026-09-20T01:31:56.527Z
  second-look [matter, enclosing, backfill-the-missed-exemption, owner human]: closed by human at 2026-09-20T01:31:56.587Z
```

`wip plumbing gate list`, captured at the end — the three declarations
this trace made, all still human-owned:

```
local-check      step    human
reviewed-local   matter  human
second-look      matter  human
```

`wip plumbing status`, captured at the end — both Matters sealed:

```
local repo 01M2Y70P86ZJP39S2QBPFK4VN5
  repo               Clone · current

finished:
  ship-the-config-exemption-path matter · sealed
  backfill-the-missed-exemption matter · sealed
```

`wip plumbing session`, captured at the end (over the whole store,
including the three tier-attachment events noted above):

```
2026-09-20T01:31:14Z — 2026-09-20T01:32:02Z (23 events)
```

## What this proves, and what it does not touch

- **Who set `tracker.push-level`, and when, reads straight off `ev-09`**:
  `actor = human`, `occurred_at = 2026-09-20T01:31:33.055Z`,
  `payload = {"key":"tracker.push-level","value":"narrated"}`. No
  dedicated "config history" view exists or was built for this — the same
  `Store.Events` read that `wip plumbing session`/`wip plumbing status`
  already derive from is what produced this row, exactly the way the
  event table for every trace before this one was produced (`docs/events-
  traces/session-handoff.md`'s "how a trace gets produced"). D36's "the
  store is the source of truth" now covers config with no asterisk, and
  no new read verb exists for it: this trace is evidence that none was
  needed, not a proposal for one.
- **The gate declaration's explicit exemption snapshot is not
  recomputed or inferred — it is printed verbatim in `ev-10`'s payload**:
  `exempt: ["matter-A"]`. A reader with no access to this machine can see
  exactly which node the declaration stepped over without replaying fold
  logic, the narratability call `evented-config` step-01 resolved.
- **The repair is a distinct event, `ev-19`, correlated to nothing but
  itself** (it is not derived from `ev-16`'s declaration or from the two
  gate closes that made it eligible) — it is its own fact: someone backfilled
  an exemption a declaration could not capture at the time. `gate.declared`
  and `gate.exemption-repaired` staying separate event types is exactly
  what keeps a repeated declaration from ever being able to extend its
  own original applicability boundary (`internal/store/payloads.go`'s
  `GateExemptionRepaired` doc comment).
- **The existing read surface needed no change.** `wip plumbing status`,
  `wip plumbing gate status`, `wip plumbing gate list`, and `wip plumbing
  session` are all pre-existing `read-surface`/`gates-and-dependencies`
  verbs; none was touched to produce this trace, and none was extended.
  The one thing this trace's own event table needed — showing config and
  gate-declaration events with full envelope columns — is the same direct
  read over `events` every prior trace's table already used.
