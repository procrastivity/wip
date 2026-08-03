# Schema known-gaps sweep

Triage of `docs/schema/decisions.md` §"Known gaps, deliberately not closed
here" (lines ~315–382). Triage only — no code changes. Each item gets one
disposition: **already handled**, **real and actionable** (a backlog entry
follows), or **deliberately open** (belongs to a phase that hasn't started).

Git-log check: the four most recent commits (`2a29b01` matter-grain
workplan, `95fd957` release-engineering, `171ee47`/`a835372`/`1d0b0c1`
install-target harnesses, `29022c2` install harness validation order) touch
`internal/render`, `internal/harness`, `.github/workflows`, and CLI wiring —
none touch `internal/store`, `internal/guards`, or the decisions doc. The
nearest store/write-surface-adjacent commit, `4559d66` (`--locator`
override on matter create), only touches `internal/writesurface/birth.go`
and `internal/verbs/matter/matter.go`. So none of the items below were
closed by recent work except where noted.

## 1. Two Matters of one Repo may share a slug

**Real and actionable.** `nodes_locator` is still
`UNIQUE INDEX ... ON nodes(matter, locator) WHERE tombstone_event IS NULL`
(`internal/store/schema_v1.go:275`), and a Matter's `matter` column equals
its own `id` (`schema_v1.go:260`), so the constraint on a Matter's own
locator is vacuous. No later commit added a Repo-scoped index or a scope
decision.

→ Backlog `01KZ4NXWXQK7AW3RZJ40AB28MX` — owner: the addressing Matter.

## 2a. `backlog.planned` accepts any node where a Matter is meant

**Already handled.** `writesurface.BacklogPlan` calls `ResolveMatter`,
which refuses a non-Matter with `validation.not-a-matter`
(`internal/writesurface/resolve.go:53-60`). The verb layer did exactly what
the doc said it would.

## 2b. `batch.joined` accepts any node where a Matter is meant

**Deliberately open.** Nothing emits `TypeBatchJoined` yet.
`internal/writesurface/lifecycle.go:10-17` documents this as a D58 gap,
explicitly deferred to whichever Matter (`render-scratch` or
`orchestration`) wires dispatch-opening first. `render-scratch` has since
wired `dispatch.opened`/`refresh` but not `batch.joined` — the kind-check
question is moot until that Matter exists.

## 3. `clone.attached` never checks `payload.repo == event.repo`

**Real and actionable.** `insertClone` (`internal/store/project.go:493-506`)
inserts `p.Repo` with no comparison to `ev.Repo`; the sibling `insertRepo`
checks the analogous invariant at `project.go:480`. No test exercises a
mismatched pair.

→ Backlog `01KZ4NXWZ8M28MF0WY3Z08Q1BE` — owner: the verb layer.

## 4. A tombstone can be undone by raw SQL

**Real and actionable.** The `_advance` trigger
(`internal/store/schema_v1.go:575-577`) only checks that `last_event`
strictly increases; it does not guard `tombstone_event`, so
`UPDATE edges SET tombstone_event = NULL, last_event = <newer>` passes.

→ Backlog `01KZ4NXX0T23MDE9PXW5J0BFVC` — owner: a migration adding a
`*_tombstone_final` trigger.

## 5. `content.created` / `cursor.moved` / `dependency.added` / `gate.closed` accept a tombstoned target

**Deliberately open** — matches the doc's own conclusion, still accurate.
`moveCursor` (`internal/store/project.go:574-590`) does no liveness check;
`closeGate` explicitly comments "the subject's liveness is deliberately
not asked" (`project.go:395-397`). Both named mitigations still hold:
`edges_in_force` (`schema_v1.go:348`) excludes tombstoned-endpoint edges,
and `archived_matters` (`schema_v1.go:509`) excludes tombstoned Matters
before ever inspecting gate state. This is `doctor`'s territory; no
regression, no new guard needed yet.

## 6. A `gate.closed` may run in another Repo's tier context

**Real and actionable.** `closeGate` (`internal/store/project.go:380-417`)
checks only scale-match on the subject (lines 398-405); it never compares
the subject's `repo` to the command's tier-resolved Repo. Same family and
same fix shape as item 3.

→ Backlog `01KZ4NXX2HR36245GPTG108QX5` — owner: the verb layer (gate-close
path).

## 7. `InProgress` answers store-wide, with no Repo dimension

**Already handled, functionally.** The underlying query (`inProgressSQL`,
`internal/store/view.go:169-171`) is still an unindexed store-wide scan —
that part is unchanged. But the doc explicitly left "whether the filter
belongs in the query/index" to read-surface's call, and read-surface has
made it: `noCursorView` (`internal/readsurface/next.go:148-153`) and
`Content` (`internal/readsurface/status.go:36,50-54`) both scope the
store-wide result to one Repo in application code before any caller sees
it. Correctness is closed at the surface the doc named as the decider. A
pure performance question remains (client-side filter vs. an indexed
Repo-scoped seek) but that is the call the doc already deferred, made —
no backlog entry filed for it.

## 8. `backlog.declined` overwrites `detail`

**Real and actionable** — confirmed live (hit in dogfood session 01), and
still true: `declineBacklogEntry` (`internal/store/project.go:454-469`)
executes `UPDATE ... SET detail = ?` binding the decline reason
(line 463), overwriting whatever `detail` held. `backlog_entries` has
exactly one free-text column (`schema_v1.go:395-410`).

→ Backlog `01KZ4NXX3YC68JDTWPBC1G34WZ` — owner: a migration adding a
`decline_reason` column, plus the read-surface change to stop clobbering
`detail`.

## 9. `SegmentBytes`/`Content` are on `*Store`, `ContentSegments` is on `View`

**Real and actionable.** `ContentSegments` is `(v View)` (`content.go:176`)
but `SegmentBytes` (`content.go:208`) and `Content` (`content.go:244`) are
both `(s *Store)`, reading `s.db` directly rather than through a `Tx`. A
decide function only has `*Tx` (embeds `View`), so it can list segments
but not read their bytes inside its own transaction.

→ Backlog `01KZ4NXX59KTN3B452TAVCPX58` — owner: whichever verb first needs
to append to findings after reading content.

## 10. A nested `Commit` inside a decide function deadlocks

**Real and actionable.** `db.SetMaxOpenConns(1)` (`store.go:187`) means a
`decide` function that calls `Commit` again blocks forever holding the
sole connection; no guard on `Tx` catches this.

→ Backlog `01KZ4NXX6VTAXHKHMZ5H85KD2Q` — cheap fix: a re-entrancy guard on
`Tx`/`Commit`.

## 11. `runs`/`outbox_entries` are declared projections with no fold rule

**Deliberately open** — correct posture today. Both tables are in
`v1ProjectionTables` (`schema_v1.go:26-30`) so guard triggers apply and
`Rebuild` clears them, but `applyEvent`'s switch (`project.go:22-111`) has
no case writing either table — no P2 dispatch/orchestration work has
started writing to them. Per MODEL §10 the row exists so P1 never needs a
retrofit; the trap is recorded for whichever P2 Matter first needs to fill
`runs` — that event's fold rule must land with it.

## 12. A migration cannot backfill a projection column

**Deliberately open** — by design (D61). The `_advance` trigger
(`schema_v1.go:575-577`) refuses a non-increasing `last_event`, and no
migration beyond the v1 baseline exists yet in `internal/store/migrations.go`.
A schema-meaning change is a `projection_version` bump plus a refold, not
an `UPDATE` backfill. Recorded as a trap for the first v2 author.

## 13. The backup copies `wip.db` and not the blob sidecar directory

**Real and actionable.** `backup()` (`internal/store/migrations.go:176-206`)
copies only the checkpointed `wip.db`; nothing in `migrations.go`
references `blobs/` (`internal/store/paths.go:60-65`), which spilled
content lives in and `clean`/`ReapOrphanBlobs` reaps from. Harmless for
migrations (no blob touched); a real hole for backup-retention-then-restore
after `clean` has reaped orphans a kept backup still references.

→ Backlog `01KZ4NXX8CZ2TZE0VV2KMF3ASR` — owner: whoever owns
backup-retention policy.

## Summary

| # | Item | Disposition |
|---|------|-------------|
| 1 | Matter slug collision across Matters | real and actionable |
| 2a | `backlog.planned` kind check | already handled |
| 2b | `batch.joined` kind check | deliberately open (event not yet emitted, D58) |
| 3 | `clone.attached` repo mismatch | real and actionable |
| 4 | Tombstone undo via raw SQL | real and actionable |
| 5 | Tombstoned target on content/cursor/dependency/gate | deliberately open |
| 6 | `gate.closed` cross-Repo tier context | real and actionable |
| 7 | `InProgress` store-wide query | already handled (functionally; perf residue not filed) |
| 8 | `backlog.declined` overwrites `detail` | real and actionable (confirmed live) |
| 9 | `SegmentBytes`/`Content` not `Tx`-scoped | real and actionable |
| 10 | Nested `Commit` deadlock | real and actionable |
| 11 | `runs`/`outbox_entries` no fold rule | deliberately open (P2 not started) |
| 12 | Migration can't backfill | deliberately open (by design, D61) |
| 13 | Backup excludes `blobs/` | real and actionable |

Eight backlog entries filed (`wip backlog add --provenance found`), one per
"real and actionable" row above.
