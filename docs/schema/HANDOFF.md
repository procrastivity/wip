# `schema` — session handoff

**A session artifact, not a deliverable.** Delete it at step-11, before the seal.

*This is the second handoff. The first covered steps 01–11 from a standing start;
steps 02–06 are now done and this one replaces it.*

## The task

Implement the Matter **`schema`** for the wip Go rewrite. Workplan:
`/Users/beausimensen/Code/wip-reboot/workplans/schema.md` — its embedded **Brief
is the canonical carrier of the core-build contract** (§A envelope, §B taxonomy,
§D branch-neutrality). Follow the Steps exactly.

Binding context in `/Users/beausimensen/Code/wip-reboot`: `MODEL.md` (§9, §10,
§12), `PLAN.md` 1.2, `HANDOFF.md` §1, `RESOLUTIONS.md` (D55–D60).

**Write constraints on `/Users/beausimensen/Code/wip-reboot` — read-only except:**

- step-01's append to `MODEL.md` §12 — **already done** (D61). Do not touch again.
- step-11's reconciliation may amend **the Brief inside `workplans/schema.md`**.
- Nothing else in that repo, ever. Never copy those docs into `wip-go`.

Work to the seal condition, then stop and report. The `reviewed-local` gate is
closed by the user, not by you.

## Seal condition

> Brief documents every 1.2 decision; schema + migrations + backup-before-migrate
> implemented; static cycle check tested.

**Half of it is discharged.** step-06 tested the static cycle check (and found it
wrong — see below). step-09's migration tests and step-11's Brief reconciliation
are what remain of it.

## State

**Committed and green.** Six commits on `matter/schema`, from `5ccc601` to
`90dc87b`. `go test ./...` passes, `golangci-lint run ./...` reports **0 issues**,
`nix build .#default` succeeds (the vendorHash did not need refreshing after
`go mod tidy`).

- **step-01 — complete.** D46 consumed; the store is **event-sourced** (D61
  appended to MODEL §12 verbatim). `internal/store` descends from the winning
  spike `internal/spike/eventsourced`, which **stays where it is**: D61 names its
  path, so relocating it would falsify a MODEL entry. It is evidence; nothing
  imports it.
- **steps 02–06 — complete and tested.** ~4,400 lines of tests across
  `envelope_test.go`, `entities_test.go`, `rebuild_test.go`, `content_test.go`,
  `edges_test.go`, `cycle_test.go`, over the shared `fixture_test.go`.
- **steps 07–10 — implementation written, tests not written.** The code exists
  and has never executed except incidentally.
- **step-11 — not started.**

`docs/schema/decisions.md` is up to date and is step-11's input. It now also
records every bug the tests found, and a "Known gaps, deliberately not closed
here" section. Read it before writing anything.

## What the first five Steps taught, and what to keep doing

This is the part that matters most for the remaining Steps.

**The implementation was written before any of it ran, and it had real bugs — one
per Step, roughly, and none of them typos.** `applyReplace` silently converted a
Stage into a Step. `matterOf` and `applyTransition` both let writes land on
tombstoned rows. `tombstoneEdge` retired other nodes' edges. `Cycles` reported
one loop per tangle instead of every loop. Assume steps 07–10 hide the same kind
of thing.

**The five cycle cases the workplan names all pass against the broken `Cycles`.**
That is the single most useful thing this Matter has learned about itself: a
Step's named cases are a floor, not a ceiling. What caught the bug was a case
nobody asked for (two loops closed by one edge) and an independent oracle. Expect
the same of step-09's "a synthetic v1→v2 migration" and step-08's query plan.

**The working method, which has earned its keep:**

1. Dispatch one subagent per Step (or per tight pair) with the Step's "Done:"
   clause **verbatim**, the relevant Brief passage, the settled decisions it must
   not re-litigate, and an explicit list of the tests owed.
2. Tell it: *the code exists but is untested — when a test fails the assertion is
   usually right and the code is usually wrong; fix the code, and never weaken an
   assertion to make a test pass. If you think an assertion is wrong, say so in
   your report instead of changing it quietly.* This instruction is what produced
   the bug fixes rather than a suite that agrees with the code.
3. **Verify the report by mutation yourself.** Break the guard the fix added, or
   the code path the test claims to cover, and confirm the owning test fails.
   Every claim in this Matter has been checked that way; several reports were
   more modest than the code deserved and one was a rewrite that needed real
   scrutiny.
4. Insist on **controls** for negative tests. `envelope_test.go`'s
   `TestARawRowIsRefusedOnlyForTheFieldUnderTest` inserts a *pristine* raw row and
   asserts it lands, so a typo in the INSERT cannot make ten refusal tests pass for
   the wrong reason. `rebuild_test.go`'s snapshot comparison is itself tested
   against a single changed column. `cycle_test.go` has an oracle. Ask for this.
5. Have the **Builder** run `gofumpt -l -w internal/store/` and
   `golangci-lint run ./...` and leave them clean. You check exit codes, not
   output. Then commit.

## Keeping your own context small

Steps 02–06 ran the orchestrator's context up much higher than the work needed,
and the post-mortem is unusually clear-cut: **the expensive activities and the
useful ones were almost disjoint.** Spend the savings on step-11, which is the one
Step that genuinely has to hold the whole Brief and `decisions.md` in view at once.

What was expensive and earned nothing:

- **Reading the whole implementation up front** (~3,900 lines across 14 files),
  to be able to review diffs credibly. It caught no bug. Every bug was caught by
  the Builder writing a test, or by mutation afterwards.
- **Reading test files after each Step** — `envelope_test.go` alone is 917 lines,
  `entities_test.go` 1,742. Reading them confirmed nothing that a mutation did not
  confirm faster.
- **A trap worth knowing**: once you have Read a file, the harness echoes later
  modifications to it back into your context automatically. Reading
  `fixture_test.go` and `envelope_test.go` early meant every subsequent Builder
  edit to them arrived as a large unsolicited excerpt, several times over. **Do not
  read a file a Builder is going to modify.**

What was cheap and caught everything:

- **Mutation verification.** Break the guard, run the suite filtered to
  `grep -E '^(---|FAIL|ok)'`, confirm the owning test fails, restore. About twenty
  lines of output per check. This is what confirmed all five bugs, and it is what
  proved the workplan's five cycle cases were insufficient — reverting `cycle.go`
  to `HEAD` and watching all five still pass.
- **`grep -n 'func (h \*harness)'`** over `fixture_test.go` instead of reading it:
  the helper inventory in one screen.
- **`sed -n '/func TestName/,/^}/p'`** to read *one* test when a specific claim
  needs checking, rather than the file it lives in.

So, concretely, for 07–11:

- Read `doc.go`, `docs/schema/decisions.md`, and the workplan. Then stop. Let each
  Builder read the files its own Step touches — that is what it is for.
- Ask for a report with a **hard length cap** and a fixed shape: tests added
  (one line each), non-test changes with the bug each fixes, findings not fixed,
  fixture additions. Ask it to include **the exact mutation commands it ran**, so
  re-verifying is copy-paste rather than reconstruction.
- Filter every command. `go test ... | tail -3`, `golangci-lint ... | tail -2`.
- **Pair the Steps.** 07+08 in one Builder and 09+10 in another is two dispatches
  instead of four, and halves the report-and-review overhead. Both pairs are
  natural (gate storage plus the query over it; migrations plus round-trip), and
  05–06 already established that pairing inside one agent works. This does not
  breach the Subagent notes — one Builder, sequential Steps.
- If you want a diff reviewed by something other than mutation, dispatch a
  **reader** agent for it and take its summary. Do not read the diff yourself
  unless a mutation has already told you something is wrong.

**Do not run two Step agents at once.** The workplan's Subagent notes are binding:
*"Strict Step sequence, one Builder — nothing dispatches concurrently within this
Matter (HANDOFF §1.5). The intra-Matter fan-out exception belongs to `store-fork`
alone."* Pairing two Steps inside **one** agent is fine and was done for 05–06.

## The shared test harness — every Builder's first read

*The inventory below is here so **you** do not have to open the file: paste the
relevant part into a Builder's prompt and tell it to read
`internal/store/fixture_test.go` itself. `grep -n 'func (h \*harness)'` refreshes
it in one screen if it has grown.*

Nothing in this store can be written without a tier context (D56 makes the repo
dimension mandatory on every event but `batch.*`, and the projection tables carry
real foreign keys), so every test goes through one bootstrap. `newHarness(t)`
opens a store under `t.TempDir()` with a Repo, Clone and Worktree attached.

Available on `*harness`, roughly in the order you will want them:

- commands: `commit`, `commitWith`, `commitError`, `commitOne`, `req`, `with(Env)`
- tiers: `attachRepo`, `attachClone`, `attachWorktree`
- nodes: `matter`, `stage`, `step`, `node`, `remove` (in `rebuild_test.go`)
- lifecycle: `move`, `start`, `finish`, `cancel`, `pause`, `resume`,
  `transitionDraft`, `wantLifecycle`, and the `lifecycleTaxonomy` table
- gates: `closeGate`
- content: `write`, `writeError`, `wantContent`
- edges: `depend`, `dependError`, `undepend`, `wantBlockers`, `wantLiveEdges`,
  `edgePicture`, `rawEdge`, `rawEdgeInsert`, `dependDecide`
- the log: `eventsOf`, `birthEventOf`, `lastEventOf`, `newEvent`
- around the API: `rawExec`, `rawRefusedBy`, `refusalMentions`
- column-for-column readers: `columnsOf` (via `pragma_table_info`), `rowsOf`,
  `rowOf`, `wantRow` (**strict** — it fails on a column the test says nothing
  about, so a column a later migration adds cannot slip past unasserted),
  `wantRowCount`

Two conventions worth preserving. `rawRefusedBy` requires the error to *name* the
guard under test, because several constraints overlap — `DELETE FROM events` also
trips a foreign key, so a bare "it was refused" would pass with the append-only
trigger deleted. And **an unused helper is a lint failure in this repo**, so add
helpers when a Step needs them rather than ahead of need; speculative ones were
already removed once for exactly this reason.

`snapshotProjection` / `diffProjection` live in `rebuild_test.go` and are generic
over columns — use them, don't hand-list columns.

## What remains, per Step

Each Step's "Done:" clause in the workplan is the spec. Suggested dispatch:
**07+08 in one Builder, 09+10 in another, then 11 yourself.** Condensed:

- **step-07 — gate state, tests only.** Gate state at Matter/Stage/Step scale;
  `gate.closed`'s payload round-trips; **declare no gate beyond
  `reviewed-local: matter`** (HANDOFF §1.2 — declaring others would make Matters
  unable to seal, MODEL §2.3). Holding state at any scale is a storage capability,
  not a declaration. Note `closeGate`'s projection is a plain INSERT on a
  `(node, gate)` primary key, so closing twice refuses via the PK with an oblique
  message; `entities_test.go` already asserts that as-is. `gate_declarations` is
  one of the two non-projection tables — `Rebuild` must leave it alone, which
  `rebuild_test.go` already asserts.
- **step-08 — invariant 2, tests only.** A query answering "what is in progress",
  plus an **`EXPLAIN QUERY PLAN` assertion** that it is an index seek on
  `nodes_lifecycle` with **no** `USE TEMP B-TREE FOR ORDER BY`. Under D61 this is
  a read of the maintained projection, never a replay. `inProgressSQL` in
  `view.go` is the query, and its comment already names the test that should
  exist (`TestInProgressIsAnIndexSeek`). Assert the plan on the real SQL constant,
  not a copy of it.
- **step-09 — migrations. Discharges half the seal condition.** A **synthetic
  v1→v2** end to end (backup written, migration applied, `schema_version`
  advanced, pre-existing rows still answer correctly, the log untouched), and a
  no-op open (already current → no backup, no migration). `openAt(path, reg,
  target)` takes an injectable register precisely so a test can append a synthetic
  v2 without shipping one — use it; do not add a real v2. Also worth covering:
  the downgrade refusal, `applyMigration`'s all-or-nothing property, and
  `ensureProjectionVersion`'s rebuild-on-bump (including the newer-projection
  refusal).
- **step-10 — round-trip.** Every entity holds and returns with ULID identity
  preserved and locators mutable; **tier dimensions are a static function of type
  (D56)** — repo on all but `batch.*`, clone/worktree exactly on execution types;
  `sort_key` reorders without implying execution order (D51); a removed node
  leaves a resolvable tombstone; invariant 1 (one mutation → one event) and the
  source-of-truth assertion written against the **event-sourced** branch. Much of
  this is now covered incidentally by steps 02–06 — **read those files first and
  make step-10 the assertions that are genuinely missing**, not a fourth pass over
  the same ground. The honest deliverable here may be small; say so if it is.
- **step-11 — Brief reconciliation.** Re-read the Brief against what was built;
  fold divergences into the Brief text **in place** in `workplans/schema.md`;
  check both D46 branches in the fenced passage and leave the unused
  (tables+audit-log) branch documented, **not deleted**. `docs/schema/decisions.md`
  is the divergence list — work from it. Then delete this handoff file.

### Divergences step-11 must fold into the Brief

All are recorded in `decisions.md` with rationale; these are the ones that
contradict the Brief's own text:

1. **"`type` is an open token column" is superseded.** The taxonomy is a real
   table with `events.type` a foreign key into it, because D56's "checked in
   schema" is not expressible otherwise. This sentence is inside
   `workplans/schema.md`, so step-11 fixes it in place.
2. **The entity table's three rows for Matter/Stage/Step are one `nodes` table**,
   discriminated by `kind`.
3. **"Sealing moves a Matter into archived state" is wrong** — Archive is the
   `archived_matters` view, because D55 makes *sealed* a predicate.
4. **A second view exists that the Brief does not mention**: `edges_in_force`. An
   edge is in force only while both its ends are.
5. **Config and gate declarations are the documented exception** to "every durable
   table is a projection."
6. **The actor-in-cascade call was resolved against the spike's posture** — actor
   is the requesting actor for the whole chain, with `payload.cascade: true`
   recording wip's initiative.
7. **"Stage equivalents would be four `INSERT`s and no Go change" is false** for
   `step.replaced` and `step.reordered`.

## Verification and hygiene

- `go test ./internal/store/ -count=2` — the working loop. `-count=2` because a
  test that leaks state between runs is a test that lies.
- `make check` = `golangci-lint run` + `go test ./...`, the same gate pre-commit
  runs. Needs the nix devshell (`direnv allow`). **It is at 0 issues — keep it
  there.**
- `nix build .#default` after any `go.mod` change; it reports the correct
  `vendorHash` on mismatch. Two prior commits exist purely to refresh it.
- `CGO_ENABLED=0` is load-bearing (packaging commitment 1) — the SQLite driver
  must stay `modernc.org/sqlite`, never `mattn/go-sqlite3`.
- **`schema_v1.go` may be edited until v1 ships**, and v1 has not shipped. Two
  additions were made on that basis (the Crockford alphabet CHECK, the
  `edges_in_force` view) rather than as v2 migrations. Once anything ships, the
  file's own header rule applies and a change is a new numbered migration.
- Commits: conventional, enforced by pre-commit. Match the repo's style —
  `test(schema): step-04 — prose and content storage`.

## Report to the user at the end

The user asked to be told when anything invalidates a later workplan.
`wip-reboot` is read-only, so these are **reported, never edited**:

1. **`workplans/write-surface.md` needs an amendment.** Its
   `birth-and-amendment` step-01 and step-03 state `store-fork`'s actor posture
   (`system:wip` on cascade-auto-started ancestors) as their own. `schema`
   resolved the flagged call the other way — actor is the requesting actor for the
   whole chain, with `payload.cascade: true` recording wip's initiative (rationale
   in `decisions.md`). step-03 already defers to "whatever `schema`'s Brief §A
   settles", so it is self-deferring rather than invalidated, but those two
   sentences are now wrong.
2. **The addressing Matter has a decision waiting.** Two Matters of one Repo may
   share a slug: `nodes_locator` is `(matter, locator)` and a Matter is its own
   matter, so the constraint on a Matter's own locator is vacuous. Needs a new
   index *and* a decision about the scope.
3. **`guards`/`doctor` inherits more than the cycle check.** The "Known gaps"
   section of `decisions.md` lists several conditions the store permits and
   nothing currently audits — a tombstone undoable by raw SQL, events targeting
   tombstoned nodes, tier payloads that can disagree with their event's own
   dimension. Worth reading when that Matter's five checks are chosen.
4. **`Cycles` was wrong, and the workplan's five cases did not catch it.** Worth
   knowing as a fact about how the workplans specify tests, not just about this
   Matter: the five named cases all pass against an implementation that reported
   one loop per tangle. If other workplans enumerate test cases the same way,
   they are floors.
