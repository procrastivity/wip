# `events-traces` — session handoff

Working notes for continuing this Matter in a fresh session. Read
`/Users/beausimensen/Code/wip-reboot/workplans/events-traces.md` first (the
Step-by-step spec); this file is *how*, not *what*.

## Done so far

- `evidence/traces/trace-01-smallest.md` — committed, complete.
- `evidence/traces/trace-02-stages-and-steps.md` — committed, complete.
- `evidence/traces/trace-03-session-two-matters.md` — committed, complete.
- `evidence/traces/trace-04-amendment.md` — committed, complete.
- `evidence/traces/trace-05-two-clones.md` — committed, complete.

## Remaining

- **step-06 — fidelity audit** against MODEL §10's list, over all five
  trace files. Not started; run last, now that traces 4 and 5 both exist.

All upstream Matters (`write-surface`, `read-surface`, `tiers`,
`render-scratch`) are locally complete — there is no unblock-ordering
constraint left to respect; only the audit (step-06) remains.

## How a trace gets produced (the method traces 1–3 used)

1. A throwaway git repo + throwaway store, isolated via env vars:
   ```
   export WIP_DB_PATH=<scratch>/tN.db
   export XDG_CONFIG_HOME=<scratch>/config
   cd <scratch>/tN && git init -q && git config user.email t@t.local && git config user.name t
   /tmp/wipbin init --json   # or `go build -o /tmp/wipbin ./cmd/wip` first if stale
   ```
2. Drive the real verbs — either the CLI binary directly (traces 1, 2), or,
   when wall-clock control matters (trace 3's ~6h gap), the actual
   `internal/writesurface` Go functions directly via `store.OpenWithClock`
   with a hand-advanced fake clock (see the now-deleted
   `cmd/tracegen3/main.go`, described in trace 3's own artifact — rebuild
   an equivalent throwaway `cmd/tracegenN/main.go`, `go build`, run, then
   `rm -rf` it before committing; never leave a generator in the tree).
   **Gotcha:** the fake clock must start *after* the store's floor (the
   highest id already on disk from `wip init`'s real-clock events) or
   `Store.NewID` spins forever waiting for its own clock to catch up —
   seed the fake base time from `time.Now().UTC().Add(time.Minute)`, not a
   literal illustrative date.
3. Dump the event table: `sqlite3 -json "$DB" "select id,type,occurred_at,
   actor,causation,correlation,repo,clone,worktree,subject,payload from
   events order by id;"` piped to a file.
4. Alias it: `python3 /tmp/events-traces-storage/mktable.py
   events.json alias.json <skip_n>` — `<skip_n>` drops the leading N
   tier-attachment setup rows (see "Setup events" below). Write
   `alias.json` by hand as `{"<ULID>": "matter-A", ...}`; anything not
   aliased gets an automatic `ev-NN` by appearance order, and a legend is
   printed to stderr. The script already trims `content.created`/
   `content.appended` payloads down to `{"kind": ...}` (the "type-relevant
   fields" the workplan's resolved format calls for); extend `keep_keys`
   in the script if a new event type needs the same treatment.
5. Capture the rendered pane by running the real CLI (`wip status`, `wip
   next`, `wip session`, both human-mode and sometimes `--json`) against
   the *same* db, at the moments the trace's spec calls for (e.g. trace
   2's "before the gate closes" capture).
6. Write `evidence/traces/trace-0N-<slug>.md`: intro + exact commands
   driven + the aliased table (GFM, 11 columns, CONTRACT §A order) +
   rendered pane(s), following traces 1–3's structure as the template.

**Setup events.** `wip init` always emits `repo.attached` /
`clone.attached` / `worktree.attached` first. Traces 1–3 all treat these
as fixture setup — `tiers`' own narrative, not the trace's — and omit them
from the committed table (but note that `wip session`'s event count
*includes* them, since Session reads the whole store). Keep this
convention for traces 4 and 5 too, for consistency across the set.

## Known divergences / findings to report, not silently fix

Do **not** edit anything under `/Users/beausimensen/Code/wip-reboot` — per
the task's standing instruction. These are flagged for the user to
apply upstream.

1. **`events-traces.md` step-01's illustrative sequence has
   `backlog.planned` before `matter.created`; the real verb surface
   cannot produce that order.** `wip backlog plan <entry-id>
   <matter-locator>` takes an *existing* matter locator — it promotes an
   entry into a Matter that already exists, never births one — so
   `matter.created` must precede `backlog.planned`. Trace 1's artifact
   documents this inline and shows the real (correct) order. Recommend:
   amend `events-traces.md` step-01's illustrative list to match.
2. **A correctness bug in `wip next`, found incidentally while producing
   trace 1**, not a MODEL §10 fidelity gap (the event log itself is
   correct) but worth a Backlog entry: `internal/readsurface/next.go`'s
   `noCursorView` only ever consults the **Planned** frontier
   (`Frontier()`), never in-progress nodes. A Matter that is started
   immediately with no cursor ever set (trace 1's own bugfix-with-no-plan
   shape, before `wip next --set` is called) and has nothing else Planned
   makes `next` report `EverythingSealed` (`"nothing in progress or
   planned — every Matter sealed"`) even though the Matter is actively In
   Progress and nothing is sealed. None of `vocabulary`'s five drafted
   outputs covers "something in progress, no cursor, empty Planned
   frontier" — output 5 is being reused for a state it doesn't describe.
   Not fixed here (out of this Matter's remit — it owns no verb); flagged
   for a Backlog entry against `read-surface`. Trace 1's own artifact
   sidesteps it by only ever rendering `wip status`/`wip session` for that
   Step, which is all its spec calls for — `wip next` was never required
   there.

## Trace 4 — pointers (step-04)

Sequence per the spec: `matter.created`, `step.created` ×3
(`step-01`/`step-02`/`step-03`, all under the Matter directly — no Stage
needed, D2), `wip start step-01` (cascades `matter.started` +
`step.started(step-01)`), `wip finish step-01`, `wip start step-02` (**in
flight — do not finish it**), then `wip step insert
<matter-locator> --after step-01 --title "..."` (or `--before step-02` —
either anchors between the two existing steps; the workplan's own point is
the placement, not which flag). Confirm in the write-up that the inserted
step's ULID is new and that `step-02`'s own ULID is byte-for-byte
unchanged in the alias legend before/after — that's the whole finding
(identity, not position, D44). Rendered pane: `wip status` and `wip next`
after the insert, showing the new node in its placed position. No gate,
no seal — this trace never finishes the Matter.

## Trace 5 — done (step-05)

Produced as `evidence/traces/trace-05-two-clones.md`, committed. Notes
for the audit (step-06), not re-derivable from the pointers below without
running it again:

- **`git clone`'s own default local remote URL doesn't parse.**
  `NormalizeRemote` (`internal/tiers/remote.go`) accepts the scp-like
  shorthand (`host:path`) and any `scheme://host/path` form, but not a
  bare filesystem path — a fresh `git clone` of a local bare repo leaves
  `origin` as a plain absolute path, which trips
  `validation.unparseable-remote` on `wip init`. Worked around with `git
  remote set-url origin localhost:<abs-path>` on each clone before `wip
  init`; not a fidelity gap (the workplan doesn't mandate a transport),
  just a fixture detail the trace's own text now records so it isn't
  rediscovered.
- **`batch.joined` still does not fire.** Confirmed still-open in this
  Matter's own event table: `write-surface`'s documented gap
  (`docs/write-surface/decisions.md`, "A documented gap: `batch.joined`
  on first start inside an open dispatch") — no `dispatches.batch`
  column exists in `internal/store/schema_v1.go` yet, even though
  `render-scratch`'s `wip refresh` now opens the dispatch. This is the
  same pre-existing gap reappearing, not a new one; do **not** list it as
  a fresh MODEL §10 finding in the step-06 audit — it's already tracked
  upstream in `write-surface`'s decisions doc.

Original pointers (kept for reference; superseded by the actual write-up
above where they differ):

Needs **two clone directories of the *same* Repo**. `wip init` keys Repo
identity off a normalized remote URL (`tiers` Brief, "Remote-URL normal
form"), so:

```
mkdir -p <scratch>/bare.git && git init -q --bare <scratch>/bare.git
git clone -q <scratch>/bare.git <scratch>/clone-X && git -C <scratch>/clone-X commit --allow-empty -q -m init
git -C <scratch>/clone-X push -q origin HEAD:refs/heads/main
git clone -q <scratch>/bare.git <scratch>/clone-Y
```

then `wip init` inside each (same `WIP_DB_PATH` for both — they share one
store, since they're the same host in this fixture — but each needs its
own `cd` and its own `wip init` call to mint its own Clone/Worktree row).
Sequence:

- **From clone-X:** `wip matter create`, `wip workplan ...` (or body),
  `wip step create ...` ×N — birth only, **no `wip start`** (planning
  isn't work — D57's "a merely-planned Matter never reports In Progress").
- **From clone-Y:** `wip refresh` (opens the dispatch — mints
  `batch.created` + `dispatch.opened`, the anonymous-batch bracket,
  `render-scratch` step-06), `wip next --set <the matter or its first
  step>` (emits `cursor.moved`, execution event, clone+worktree = Y), `wip
  start <step-locator>` (cascades `matter.started` + `step.started`,
  `batch.joined` on the Matter's first work in this dispatch — D58 — plus
  the lifecycle pair being durable/`repo`-only regardless of where typed),
  `wip finish <step-locator>`.
- Capture `wip status` and `wip next` from **both** clone-X and clone-Y
  (`cd` into each, same `WIP_DB_PATH`) — same Matter set either way
  (repo-wide), but the current-clone marker and `next`'s cursor differ per
  clone (no cursor move ever happened from X).
- The event table must show `clone`/`worktree` populated on exactly the
  execution events (`cursor.moved`, `render.performed` if `wip refresh`
  triggers one, `batch.created`/`dispatch.opened`/`batch.joined`) and null
  on every durable-object event (`matter.created`, `step.created`,
  `matter.started`, `step.started`, `step.finished`) — including the ones
  typed from clone-Y, since lifecycle is durable regardless of where it
  was invoked. State this explicitly in the write-up (this is the trace's
  first named finding).
- `render.performed`'s payload is empty of ULIDs worth aliasing (it
  projects nothing per `schema`'s taxonomy note) — don't over-alias it.

## Step-06 — pointers (the audit)

Walk MODEL §10 (`/Users/beausimensen/Code/wip-reboot/MODEL.md` §10, lines
~335–376) item by item against all five committed trace files as a
checklist — the six items are listed in `events-traces.md` step-06
verbatim (append-only; identity-not-locator; tier dimensions; system-driven
events first-class, including the causation/correlation-never-forward pair
and that a P1 trace showing exactly one actor value (`human`) is the
*expected* result, never a finding; enough to reconstruct in-flight work,
witnessed by traces 2 and 4; sealed-Matter narratability, witnessed by
traces 1 and 2). Write the result as
`evidence/traces/fidelity-audit.md` (or append to a `step-06` section
wherever the workplan expects it — the workplan doesn't name a file, so
pick the natural one and say so). If a gap is found, the fix is an
amendment to the *upstream* workplan — report it for the user to apply,
then regenerate only the affected trace; never patch the trace or the
audit text itself. The two items above ("Known divergences") are not
MODEL §10 fidelity gaps and do not belong in this audit's findings list —
report them separately, as this file already does.

## Reusable tooling

- `/tmp/events-traces-storage/mktable.py` — the table generator described
  above. This lives outside the per-session scratchpad specifically so it
  survives across the fresh sessions doing traces 4, 5, and the audit —
  don't regenerate it, just call it. Usage:
  `python3 /tmp/events-traces-storage/mktable.py events.json alias.json
  <skip_n> > table.md 2> legend.txt`. If it's ever missing, it's ~70
  lines, reconstructable from trace 1–3's artifacts' structure (read one
  of them plus this file's step 4 above).
- Full CLI surface (`--help` output for every verb) was captured once
  during this Matter's setup and is not re-pasted here — run `wip <verb>
  --help` directly, it's cheap.
