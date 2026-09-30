# M6 Step 1: executable operation and owner census

Status: census for base `bb0520988e6e0894d958e13e84d97e17911ee678` on
`origin/go`. This closes Step 1 of the approved BDS-253 M6 plan. It records
current ownership and proposed semantic contracts; it does not register an
operation, implement a handler, change default CLI routing, or migrate normal
CLI direct-Store ownership.

The matrix is intentionally split into four executable TSVs:

- [`m6-operations.tsv`](m6-operations.tsv) lists operations with current CLI
  modes, including the seven exact `internal/operation.Catalogue()` entries.
- [`m6-engine-operations.tsv`](m6-engine-operations.tsv) adds non-CLI
  scheduler, authority-control, identity-control, and retention operations
  that have no current runnable CLI mode.
- [`m6-cli-modes.tsv`](m6-cli-modes.tsv) accounts for every runnable manifest
  path and each distinct semantic mode sharing that path.
- [`m6-noncli-owners.tsv`](m6-noncli-owners.tsv) accounts for all 58
  production domain `Store.Commit` call sites and the cross-cutting
  filesystem, blob, lock, Git, provider, daemon-control, and scheduler-hook
  owners which a commit-only scan misses.

The TSV operation identity is `name@vN`. `catalogued-m5` means the exact
identity is present in the current runtime catalogue. Every
`proposed-not-registered-not-implemented` identity is a Step 1 proposal only;
it is not accepted by the registry and no handler is implied. The separate
`authenticated-control-existing` status names current M5 authenticated
controls, not entries in `operation.Catalogue()` and not new M6 protocol
semantics. `none` denotes an explicitly empty metadata set. Semicolon-separated
cells are complete sets, not prose examples. For operation rows, context,
claim requirement, guard footprint, read footprint, write footprint, staged
blob inputs, and external effects are distinct fields. Delivery and claim
requirement are independent: for example `matter.finish@v1` remains statically
authority-delivered while requiring the exact active Matter claim as fencing
and context proof.

The primary owner Step in the operation tables follows the approved mapping
below. Dependencies list prerequisite/shared Steps without transferring
primary ownership. A CLI mode can reference more than one operation where its
current behavior is composite; each referenced operation retains its own
delivery and owner. The CLI mode table leaves `owner_step` as `-` for domain
rows because the referenced semantic operation rows are authoritative and one
CLI mode can span several owner Steps; all such modes are only adapted later by
Step 21. This does not change the current direct-store path. Only install,
uninstall, manifest, and version are classified CLI-local with explicit owner
Step 20. This plan does not defer or migrate an otherwise covered capability
by default.

## Approved M6 Step mapping

| Step | Primary owner scope |
|---|---|
| 1 | This recensus and executable matrix. |
| 2 | Shared operation registry, durable transaction, Environment install, authenticated IPC seam. |
| 3 | Deterministic snapshot/read families: status, backlog, next, content, gate, role, Run, references, and outbox. |
| 4 | Stage/Step birth and edit/reorder/replace/remove; versioned `matter.create@v2`. |
| 5 | Remaining start/pause/resume/cancel/finish lifecycle operations. |
| 6 | Brief/body/workplan/finding writes and verified content reads/blob references. |
| 7 | Gate/exemption lifecycle and evented config setters. |
| 8 | Dependency and bind/unbind/rebind reference operations. |
| 9 | Named Batch membership operations and reads. |
| 10 | Run/Dispatch/role and non-CLI scheduler orchestration/resume. |
| 11 | D115–D116 route resolution/discovery, remote-less creation, one-use attach, remote adoption. |
| 12 | D117–D118 certificate renewal/rotation/lost-key Environment identity and revocation. |
| 13 | Tier enrollment and Clone/Worktree administration/relink/labels. |
| 14 | D125 cursor delivery and D124 domain-wide Session ordering. |
| 15 | D126 cross-Environment claim stand-down and claim journal repair/close controls. |
| 16 | BacklogEntry-keyed connected capture for entry-local footprints. |
| 17 | Tracker/outbox configuration and approval/retry/candidate/proposal mutations; no provider contact in the semantic transaction. |
| 18 | Provider credentials, outbox flush, tracker-read, and other external provider effects/idempotency. |
| 19 | Refresh/render/Dispatch-close/clean and seal-triggered checkout file effects. |
| 20 | Doctor/status/run-lock/diagnostics; install/uninstall/manifest/version explicitly remain CLI-local. |
| 21 | Explicit opt-in isolated CLI adapters for classified domain operations. |
| 22 | Executable parity matrix and completeness/static ownership checks. |
| 23 | Final focused/race/full/M5-regression and independent-review validation evidence. |

Rows spanning boundaries use one primary owner and list prerequisites. Examples:
`backlog.plan@v1` belongs to Step 16 but depends on Step 4 because it links an
existing Matter; seal-time checkout rendering belongs to Step 19 rather than
the Step 5/7 authority transition; a provider-neutral outbox candidate belongs
to Step 17 while provider contact and idempotency belong to Step 18.

## Current census

Building the Cobra tree and manifest in a Go test (without invoking any WIP
verb) yields **79 unique runnable paths**. The surface table expands shared
commands into **95 distinct path/mode rows**; path/mode pairs are unique and
both sides of the `next` alias are retained. Parent help namespaces are not
runnable modes.

The non-CLI owner table records **58 production semantic `Store.Commit`
callsites**: 35 in `writesurface`, 5 in `tiers`, 3 in `render`, 2 cursor writes
in `readsurface`, 10 in `scheduler`, and 3 evented configuration writes in
`store`. SQL transaction `Tx.Commit` calls, projection rebuilds, migrations,
and pre-Store migration events are not counted as semantic `Store.Commit`
callers; their owners are separately described. The table also makes explicit
that Store open/migration, pre-commit content blob writes and reads, orphan
blob reaping, checkout rendering/scratch/exclude changes, advisory lock
probes, Git discovery, credentials/provider HTTP effects, and injected
scheduler hooks have owners outside those 58 call sites.

Several static boundaries are deliberate:

- `content.write-once@v1` retains one identity for brief/body/workplan; the
  kind is operation input metadata, not three invented aliases.
- Matter/Step findings remain claim operations; `backlog.finding.append@v1`
  is a separate proposed BacklogEntry-keyed capture, with no Matter claim
  implied.
- `backlog.add@v1` remains authority-class because its current config-driven
  auto-delegation can also mutate shared outbox state. It is not incorrectly
  classified as entry-local capture.
- Matter/gate finish and optional aggregate Batch sweep are authority-class
  operations. Authenticated checkout alignment/render is a separate Step 19
  Environment effect; Step 1 does not claim full CLI parity or settle its
  failure ordering.
- `outbox.flush@v1` is Environment-delivered because provider contact and
  credentials are Environment-owned; the exact resulting authority receipt
  is not a generic provider/path command.
- Run authority reads and host lock liveness are separate rows. Store-open and
  migration effects are visible rather than smuggled into “read-only” claims.
- M5 `claim.acquire@v1`, `claim.release@v1`, and claim-journal controls remain
  authenticated control paths. They are not redefined as semantic command
  catalogue entries; Step 15 owns its specified extensions.

## Executable checks

`internal/cli/m6_census_test.go` is the executable counterpart of these
tables. It builds—but does not execute—the Cobra command tree to check exact
manifest path and alias coverage, detects duplicate/missing path-mode rows,
parses all operation metadata and references, compares every
`catalogued-m5` metadata field with `operation.Catalogue()`, rejects proposed
rows that accidentally become registered, and requires every operation to
have a CLI surface or non-CLI owner. It also AST-enumerates production
`Store.Commit` caller sites and compares them one-for-one with the owner table,
so a removed or multiply-owned row fails the test. The owner categories for
filesystem/provider/control effects are required explicitly.

Step 1 validation must not invoke WIP CLI verbs or mutate any WIP store. The
focused Census test, package tests, static checks, and `git diff --check` are
safe; do not run a manifest or other WIP executable as a substitute for the
in-process Cobra/manifest test.
