# `schema` — session handoff

**A session artifact, not a deliverable.** Delete it at step-11, before the seal.

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

## State

**step-01: complete.** D46 consumed — the store is **event-sourced** (D61
appended to MODEL §12 verbatim). `internal/store` descends from the winning spike
`internal/spike/eventsourced`, which **stays where it is**: D61 names its path, so
relocating it would falsify a MODEL entry. It is evidence; nothing imports it.

**steps 02–09: code written, largely untested.** `internal/store/` is ~2,600
lines across `doc.go`, `ulid.go`, `paths.go`, `taxonomy.go`, `schema_v1.go`,
`migrations.go`, `store.go`, `payloads.go`, `project.go`, `content.go`,
`edges.go`, `cycle.go`, `view.go`, `config.go`.

Verified so far: **only** that the v1 baseline DDL applies, the taxonomy seeds,
the projection version records, and an empty store answers `InProgress` with
nothing (`open_test.go`). **Everything else has never executed** — every
projection rule, the cycle check, content spill, `Rebuild`, migrations beyond v1.
Expect real bugs; the first agent to write tests will find them.

**steps 10–11: not started.**

Nothing is committed. `wip-go` has two untracked dirs (`internal/store/`,
`docs/schema/`); `wip-reboot` has the `MODEL.md` append.

## Read these first

1. `workplans/schema.md` — the Brief and the Steps. Non-negotiable.
2. **`docs/schema/decisions.md`** — every call this Matter has already made and
   why: the actor-in-cascade resolution, the taxonomy-as-a-table divergence, the
   two-clocks closure, one `nodes` table, Archive as a view, config as the
   non-projection exception, content segments, the spill threshold, subject
   choices per event type. **Do not re-litigate these**; they are step-11's
   input.
3. `docs/store-fork/decision-record.md` §"Carried forward to `schema`" — the
   seven findings and what was done with each (all seven are addressed in
   `decisions.md`).

## What remains, per Step

Each Step's "Done:" clause in the workplan is the spec. Condensed:

- **step-02** — tests only. Append-only refused through raw SQL (`UPDATE`/
  `DELETE`); ids strictly ascend; causation/correlation forms (`a:a:a`, `b:a:a`,
  `c:b:a` for a linear cascade; `a:a:a, b:a:a, c:a:a` for unrelated siblings);
  a cause never points forward; actor never empty and prefix-validated;
  `occurred_at` equals the ULID's own timestamp.
- **step-03** — tests for every entity table and its projection rule: tier rows
  (D37 natural keys, remote adoption keeping the ULID), nodes at all three
  scales, Config, Archive (the view), Batch (no tier), Run, BacklogEntry,
  OutboxEntry, cursor, dispatch. Verify the `(D46-dependent)` role: these are
  **projections**, so `Rebuild` must reproduce them column-for-column.
- **step-04** — content tests: create-once vs. append; a second create-once call
  refused by the unique index; findings accumulate; spill above `SpillThreshold`
  writes the sidecar and reads back identically; a missing/corrupt blob is
  reported, never silently short.
- **step-05** — edge insert/tombstone; the generic tombstone also serving
  `step.removed`; a tombstoned node stays resolvable by identity; **Canceled is
  explicitly not a tombstone** (a canceled node stays live and addressable).
- **step-06** — *discharges half the seal condition.* Table-driven cycle tests,
  exactly the five the workplan names: two-node cycle refused; multi-hop refused;
  diamond (shared ancestor) accepted; self-edge refused; an edge whose only cycle
  runs through a **tombstoned** edge accepted. Also exercise `Cycles` (the
  store-wide audit `guards`/`doctor` will call).
- **step-07** — gate state at Matter/Stage/Step scale; `gate.closed`'s payload
  round-trips; **declare no gate beyond `reviewed-local: matter`** (HANDOFF §1.2
  — declaring others would make Matters unable to seal).
- **step-08** — invariant 2 demonstrated: a query answering "what is in
  progress", plus an `EXPLAIN QUERY PLAN` assertion that it is an index seek on
  `nodes_lifecycle` with **no** `USE TEMP B-TREE FOR ORDER BY`. Under D61 this is
  a read of the maintained projection, never a replay.
- **step-09** — *discharges half the seal condition.* Migration tests: a
  **synthetic v1→v2** end to end (backup written, migration applied,
  `schema_version` advanced, pre-existing rows still answer correctly, the log
  untouched), and a no-op open (already current → no backup, no migration).
  `openAt(path, reg, target)` takes an injectable register precisely so a test
  can append a synthetic v2 without shipping one.
- **step-10** — round-trip: every entity holds and returns with ULID identity
  preserved and locators mutable; **tier dimensions are a static function of type
  (D56)** — repo on all but `batch.*`, clone/worktree exactly on execution types;
  `sort_key` reorders without implying execution order (D51); a removed node
  leaves a resolvable tombstone; invariant 1 (one mutation → one event) and the
  source-of-truth assertion written against the **event-sourced** branch.
  Consider porting the losing spike's best technique: raw-SQL bypass attempts
  *around* the API (`internal/spike/_archive/README.md` names it).
- **step-11** — re-read the Brief against what was built; fold divergences into
  the Brief text **in place** in `workplans/schema.md`; check both D46 branches in
  the fenced passage and leave the unused (tables+audit-log) branch documented,
  not deleted. `docs/schema/decisions.md` is the divergence list — work from it.
  Then delete this handoff file.

## How to run it

**Sequential subagents, one Step at a time.** The workplan's Subagent notes are
binding: *"Strict Step sequence, one Builder — nothing dispatches concurrently
within this Matter (HANDOFF §1.5). The intra-Matter fan-out exception belongs to
`store-fork` alone."* Never run two Step agents at once, even though they'd fit.

Suggested loop per Step:

1. Dispatch one subagent with: the Step's "Done:" clause verbatim, the relevant
   Brief section, the file list it may touch, and "the code exists but is
   untested — fix what the tests expose, and say what you changed."
2. Review its diff yourself. Subagents will be tempted to weaken an assertion to
   make a test pass; the assertion is usually right and the code is usually
   wrong.
3. Run `go test ./internal/store/` and `gofumpt -l -w internal/store/`.
4. Commit, then move to the next Step.

Steps 02 and 03 are the big ones (03 covers ~12 tables and every projection
rule); 05–08 are small enough to pair up in one agent if context is tight.

## Verification and hygiene

- `go test ./internal/store/` — the working loop.
- `make check` = `golangci-lint run` + `go test ./...`. It is the same gate
  pre-commit runs. Needs the nix devshell (`direnv allow`); if `golangci-lint` is
  missing, `go vet ./...` is the fallback but is **not** equivalent.
- **Run `go mod tidy`** — `modernc.org/sqlite` is still marked `// indirect` in
  `go.mod` and `internal/store` now imports it directly. CI will notice.
- **Check the nix `vendorHash`** after that: `flake.nix` pins one, and two prior
  commits exist purely to refresh it when go.mod changed
  (`build(nix): refresh vendorHash …`). `nix build` reports the correct hash on
  mismatch.
- `CGO_ENABLED=0` is load-bearing (packaging commitment 1) — the SQLite driver
  must stay `modernc.org/sqlite`, never `mattn/go-sqlite3`.
- Commits: conventional, enforced by pre-commit. Match the repo's style —
  `feat(schema): step-04 — prose and content storage`.

## Report to the user at the end

Two items are already owed, and more may surface (the user asked to be told when
anything invalidates a later workplan — `wip-reboot` is read-only, so these are
*reported*, never edited):

1. **`workplans/write-surface.md` needs an amendment.** Its
   `birth-and-amendment` step-01 and step-03 state `store-fork`'s actor posture
   (`system:wip` on cascade-auto-started ancestors) as their own. `schema`
   resolved the flagged call the other way — actor is the requesting actor for
   the whole chain, with `payload.cascade: true` recording wip's initiative
   (rationale in `decisions.md`). step-03 already defers to "whatever `schema`'s
   Brief §A settles", so it is self-deferring rather than invalidated, but those
   two sentences are now wrong.
2. **The Brief's "`type` is an open token column" sentence is superseded.** The
   taxonomy is a real table with `events.type` a foreign key into it, because
   D56's "checked in schema" is not expressible otherwise. This one is inside
   `workplans/schema.md`, so step-11 fixes it in place rather than reporting it.
