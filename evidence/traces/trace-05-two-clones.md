# Trace 5 — planned in one clone, worked from another

PLAN 1.7 trace 5: a Matter planned in one clone and worked from another,
showing which events carry which tier dimensions and what `status`
reports from each clone (`events-traces.md` step-05, D43/D38).

Produced against a throwaway store shared by two clones of the *same*
Repo — the two-clone rig the workplan calls for, not a single-clone
fixture like traces 1–4. Repo identity keys off the normalized remote
URL (`tiers` Brief, "Remote-URL normal form"), so both clones point at
one bare repo:

```
$ mkdir -p bare.git && git init -q --bare bare.git
$ git clone -q bare.git clone-X
$ git -C clone-X commit --allow-empty -q -m init
$ git -C clone-X remote set-url origin localhost:<abs-path>/bare.git
$ git -C clone-X push -q origin HEAD:refs/heads/main
$ git clone -q bare.git clone-Y
$ git -C clone-Y remote set-url origin localhost:<abs-path>/bare.git
```

**Setup note, not a fidelity gap.** `NormalizeRemote` (`internal/tiers/remote.go`)
parses the scp-like shorthand (`host:path`) and any `scheme://host/path`
form, but not a bare filesystem path — `git clone`'s own default local
remote URL (an absolute path with no scheme) trips
`validation.unparseable-remote`. This fixture uses `git remote set-url`
to rewrite each clone's `origin` to the scp-like shorthand
(`localhost:<path>`) before `wip init`, which the parser does accept.
Not a divergence — the workplan only requires "two clones of one Repo,"
not a specific transport — just a detail worth recording since it isn't
obvious from `wip init --help` alone.

Both clones then ran `wip init` against one shared `WIP_DB_PATH`, each
minting its own Clone/Worktree row against the one Repo:

```
$ cd clone-X && wip init --json
{"repo":"...MBM","repoCreated":true, ...,"cloneLabel":"clone-X", ...}
$ cd clone-Y && wip init --json
{"repo":"...MBM","repoCreated":false, ...,"cloneLabel":"clone-Y", ...}
```

Same `repo` ULID both times (`repoCreated:false` the second time confirms
it), distinct `clone`/`worktree` ULIDs — the fixture's whole premise.

Commands driven, in order:

```
# --- in clone-X (planning) ---
$ wip matter create --title "cross-clone dispatch demo"
$ echo "the workplan body" | wip workplan cross-clone-dispatch-demo   # stdin, no --file
$ wip step create cross-clone-dispatch-demo --title "first step"   # step-01
$ wip step create cross-clone-dispatch-demo --title "second step"  # step-02
$ wip status                                                   # clone-X, before Y's work
$ wip next                                                      # clone-X, before Y's work

# --- in clone-Y (working) ---
$ wip refresh                                                   # opens the dispatch, first render
$ wip next --set cross-clone-dispatch-demo/step-01              # cursor.moved
$ wip start cross-clone-dispatch-demo/step-01                   # cascades matter+step
$ wip finish cross-clone-dispatch-demo/step-01
$ wip status                                                    # clone-Y, after own work
$ wip next                                                       # clone-Y, after own work

# --- back in clone-X ---
$ wip status                                                    # clone-X, after Y's work
$ wip next                                                       # clone-X, after Y's work
```

No `wip start` was run in clone-X — planning is not work, and a
merely-planned Matter never reports In Progress (D57). Only
`step-01` is started and finished, from clone-Y; `step-02` stays
Planned throughout, exactly as the spec calls for (this trace doesn't
need to finish the Matter to make its point).

## Event table

Alias legend: `repo-A` = `01KYTC78DEY71F2JQ612DM4MBM` · `clone-X` =
`01KYTC78DEY71F2JQ612DM4MBN` · `worktree-X` = `01KYTC78DEY71F2JQ612DM4MBP`
· `clone-Y` = `01KYTC7FKHYBG80VYGQXEVD56S` · `worktree-Y` =
`01KYTC7FKHYBG80VYGQXEVD56T` · `matter-A` = `01KYTC7NPDHW0YW04VQ6WQGV90`
· `step-01` = `01KYTC7NQDHDPN6RD7R17DZ2F5` · `step-02` =
`01KYTC7NQTG8SYNS257KTYBG2D` · `batch-anon` =
`01KYTC81N1RR6RS2Y35VQ94EW0` (the anonymous batch a bare "work this
Matter" wraps in) · `dispatch-Y` = `01KYTC81N1RR6RS2Y35VQ94EW1`.

The `repo.attached`/`clone.attached`/`worktree.attached` setup events
(three from clone-X's `wip init`, two more from clone-Y's — `repo.attached`
fires only once, since the Repo already existed) are omitted below for the
same reason as traces 1–4: `tiers`' own narrative, not this trace's.

| id | type | occurred_at | actor | causation | correlation | repo | clone | worktree | subject | payload |
|---|---|---|---|---|---|---|---|---|---|---|
| ev-01 | matter.created | 2026-07-30T20:42:41.229Z | human | ev-01 | ev-01 | repo-A | | | matter-A | `{"title":"cross-clone dispatch demo","locator":"cross-clone-dispatch-demo","sort_key":0}` |
| ev-02 | content.created | 2026-07-30T20:42:41.247Z | human | ev-02 | ev-02 | repo-A | | | matter-A | `{"kind":"workplan"}` |
| ev-03 | step.created | 2026-07-30T20:42:41.261Z | human | ev-03 | ev-03 | repo-A | | | step-01 | `{"title":"first step","locator":"step-01","parent":"matter-A","sort_key":1000}` |
| ev-04 | step.created | 2026-07-30T20:42:41.274Z | human | ev-04 | ev-04 | repo-A | | | step-02 | `{"title":"second step","locator":"step-02","parent":"matter-A","sort_key":2000}` |
| ev-05 | batch.created | 2026-07-30T20:42:53.473Z | human | ev-05 | ev-05 | | clone-Y | worktree-Y | batch-anon | `{}` |
| ev-06 | dispatch.opened | 2026-07-30T20:42:53.473Z | human | ev-05 | ev-05 | repo-A | clone-Y | worktree-Y | dispatch-Y | `{}` |
| ev-07 | render.performed | 2026-07-30T20:42:53.475Z | human | ev-07 | ev-07 | repo-A | clone-Y | worktree-Y | dispatch-Y | `{}` |
| ev-08 | cursor.moved | 2026-07-30T20:42:53.497Z | human | ev-08 | ev-08 | repo-A | clone-Y | worktree-Y | worktree-Y | `{"node":"step-01"}` |
| ev-09 | matter.started | 2026-07-30T20:42:57.952Z | human | ev-09 | ev-09 | repo-A | | | matter-A | `{"from":"planned","to":"in-progress","cascade":true}` |
| ev-10 | step.started | 2026-07-30T20:42:57.952Z | human | ev-09 | ev-09 | repo-A | | | step-01 | `{"from":"planned","to":"in-progress"}` |
| ev-11 | step.finished | 2026-07-30T20:42:57.969Z | human | ev-11 | ev-11 | repo-A | | | step-01 | `{"from":"in-progress","to":"done"}` |

`ev-09→ev-10` is the D57 start-cascade sharing correlation `ev-09`, typed
from clone-Y but landing exactly like traces 1–4's cascades: `payload.cascade:
true` on the Matter (the ancestor the caller didn't name), absent on the
Step (the one the caller actually asked to start).

## Finding 1: tier dimensions record where work executes, not where planning was typed

Every planning-phase row (ev-01 through ev-04) is `repo`-only — `clone`
and `worktree` both null — even though every one of them was typed from
clone-X. This is not an omission: durable-object events carry `repo` only
(CONTRACT §A / D38); a Matter, its Workplan content, and its Steps belong
to the **Repo**, not to whichever clone happened to type the command that
birthed them. The event stream genuinely cannot answer "which clone ran
`wip matter create`" — and per D38 it should not be able to, since that
fact was never the durable object's to carry.

The only rows that carry a clone/worktree at all are the **execution**
events — `batch.created`, `dispatch.opened`, `render.performed`,
`cursor.moved` (ev-05 through ev-08) — and every one of them carries
**clone-Y**, the working clone, never clone-X. Even `matter.started` /
`step.started` / `step.finished` (ev-09 through ev-11), typed from
clone-Y in this run, come back `repo`-only with `clone`/`worktree` null:
lifecycle is a durable-object event regardless of where it was invoked
(same rule as ev-01 through ev-04, just typed from the other clone). Tier
dimensions track the kind of event, not the clone that issued the
command that produced it.

**`batch.created`'s null `repo`, not a fidelity gap.** ev-05 carries a
null `repo` alongside its populated `clone`/`worktree` — Batch-subject
events carry a null repo by design (D56, since a Batch keys at no tier
and cross-repo Batches are legal by construction, D39). `dispatch.opened`
(ev-06), one causation-chain step later and sharing ev-05's correlation,
*does* carry `repo-A` — the dispatch itself is worktree-scoped, so it can
name a Repo even though the Batch it opens for doesn't.

## Finding 2: `wip status` is repo-wide from either clone; the current-clone marker and cursor differ

`wip status`, captured in clone-X immediately after planning (before any
work happened in clone-Y):

```
$ wip status
<repo path>
  clone-X            Clone · current
  clone-Y            Clone

next to start:
  cross-clone-dispatch-demo matter · planned
  cross-clone-dispatch-demo · step-01 step · planned
  cross-clone-dispatch-demo · step-02 step · planned
```

```
$ wip next
no cursor set for this clone — 3 unblocked:
  cross-clone-dispatch-demo matter · planned
  cross-clone-dispatch-demo · step-01 step · planned
  cross-clone-dispatch-demo · step-02 step · planned
pick one: wip next --set <locator>
```

`wip status`/`wip next`, captured in clone-Y after `wip refresh`, `wip
next --set step-01`, `wip start step-01`, `wip finish step-01`:

```
$ wip status
<repo path>
  clone-X            Clone
  clone-Y            Clone · current

in progress:
  cross-clone-dispatch-demo matter · in-progress

finished:
  cross-clone-dispatch-demo · step-01 step · sealed · cursor

next to start:
  cross-clone-dispatch-demo · step-02 step · planned
```

```
$ wip next
cursor points at cross-clone-dispatch-demo · step-01, which is sealed — pick a new one
1 unblocked:
  cross-clone-dispatch-demo · step-02 step · planned
pick one: wip next --set <locator>
```

`wip status`/`wip next`, captured back in clone-X after clone-Y's work
(same store, no commands run in clone-X in between):

```
$ wip status
<repo path>
  clone-X            Clone · current
  clone-Y            Clone

in progress:
  cross-clone-dispatch-demo matter · in-progress

finished:
  cross-clone-dispatch-demo · step-01 step · sealed

next to start:
  cross-clone-dispatch-demo · step-02 step · planned
```

```
$ wip next
no cursor set for this clone — 1 unblocked:
  cross-clone-dispatch-demo · step-02 step · planned
pick one: wip next --set <locator>
```

**The finding, stated plainly.** Both clones are of the same Repo, so
`status` is repo-wide from either — the same Matter, the same
in-progress/finished/next-to-start sections, regardless of which clone
asks — exactly the `tiers` Brief's Read-scope rule. What differs per
clone is framing, not fact: the `· current` marker moves to whichever
clone is asking, and `wip next`'s cursor is keyed at Clone+Worktree
(D38) — clone-Y's cursor points at `step-01` (and reports it sealed,
since `wip finish` moved on without a new `--set`), while clone-X's
cursor was never moved for this Matter at all ("no cursor set for this
clone"), because no `cursor.moved` event was ever emitted with clone-X's
worktree in `clone`/`worktree`. Same Matter set, different
current/cursor framing per clone — the trace's second finding.

## A documented gap resurfaces here: `batch.joined` is still not emitted

The workplan's own step-05 text calls for `batch.joined` on the Matter's
first work within the dispatch (`payload.matter`, D58) — event table
above shows no such row. This is **not a new divergence discovered by
this trace**; it is `write-surface`'s already-documented gap
(`docs/write-surface/decisions.md`, "A documented gap: `batch.joined` on
first start inside an open dispatch") resurfacing in a second Matter's
evidence. That doc names two blockers: no verb that opens a dispatch
existed yet when `write-surface` shipped, and the `dispatches` table
`schema` shipped carries no `batch` column — "the dispatch's batch" has
no schema path to an answer.

The first blocker is now gone — `render-scratch`'s `wip refresh` opens
the dispatch, driven directly above (ev-06). The second is not: this
trace's own `dispatches` schema (`internal/store/schema_v1.go`, the
`dispatches` table) still carries no `batch` column, so `Start`
(`internal/writesurface/lifecycle.go`) still has nothing to join against
and, correctly per its own documented scope, does not try. `ev-09`
(`matter.started`, clone-Y's `wip start`, the Matter's first work inside
`dispatch-Y`) is exactly the event D58 says should also carry a sibling
`batch.joined` — and does not. Confirmed still-open, not fixed here (this
Matter owns no verb); recommend the existing `write-surface` decisions-doc
entry stay pointed at whichever Matter (`render-scratch` or
`orchestration`) ends up adding the `dispatches.batch` column and the
`batch.joined` emission once dispatch-opening and Batch are both wired
together enough to answer "the open dispatch's batch" honestly.

## D43's boundary, named not attempted

D43 — a Matter is worked from one clone at a time — is the invariant
this trace shows the **boundary of**: planning in clone-X, then working
from clone-Y, is sequential and legal, exactly what this trace does.
*Concurrent* work on the one Matter from both clones at once is what
D43 (and MODEL §11's refusal list) forbid; this trace names that as out
of bounds and does not attempt it — there is no third command sequence
here interleaving clone-X and clone-Y mid-Step.
