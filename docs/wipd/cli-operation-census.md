# M1 Step 1: CLI read and mutation census

Status: fresh census at `go` commit `a221387c15d6ed47c88a25c6bd44bf42a2586ffd`,
after `tracker-plumbing` and BDS-223. This is the BDS-227/M1 Step 1 inventory.
It records current ownership; it does not define operation request/result types,
wire encoding, a dispatcher, a daemon, or changed storage ownership.

The authoritative future constraints are MODEL D115–D131 and the
`wipd-implementation-roadmap` M1/M6 records. The classifications below are
inputs to later BDS-227 steps, not that later operation registry. Counts are
omission evidence, not a contract.

## How to read the census

Current class:

- **R** reads durable state; **M** mutates durable model state; **F** reads or
  changes a filesystem; **X** contacts an external tracker.
- **R\*** is semantically a read, but is not physically read-only today. Every
  `tiers.OpenStore` creates the database parent as needed and runs migrations.
  A path that resolves the current Clone can also call
  `tiers.AdoptIfPossible` and append `repo.key-adopted` for a legacy local-only
  Repo. These incidental writes are part of the current behavior census, not a
  proposed operation contract.
- “Projection” includes the event fold and any outbox candidates it derives.
  “Blob” means the hash-addressed sidecar under the store path, not `.wip/`.
- Guard/write footprints are the narrowest current facts visible in code.
  Later metadata must be complete and static; this document does not settle
  the semantic request shape or wire representation.

Future owner:

- **authority**: a semantic operation handled by `wipd`, with truth reads or
  writes ultimately owned by the authority.
- **environment**: an Environment-local `wipd` handler owns authenticated local
  Git, checkout, runtime-lock, or filesystem work.
- **split**: one operation needs an authority result plus an authenticated
  Environment-local effect; M6 must settle ordering and failure semantics.
- **CLI-local refusal**: deliberately do not make this an authority operation.
  It remains local process/tooling behavior and must refuse any attempt to
  route it as domain work.

Every persistence-sensitive operation below ultimately moves behind `wipd`;
“environment” never means a future direct-DB CLI fallback.

## Baseline and cross-cutting effects

The generated manifest has **79 runnable paths**: 8 top-level behavior paths
before `manifest`, 70 under `plumbing`, and `manifest` itself. Parent help
namespaces (`plumbing`, `batch`, `clone`, `depend`, `dispatch`, `finding`,
`gate`, `matter`, `outbox`, `role`, `run`, `stage`, `step`, `tracker`, and
`tracker propose`) are not operations.

There are **31 lexical `tiers.OpenStore` sites** and **58 production
`Store.Commit` caller sites**. The latter are currently owned by:

| Owner | Sites |
|---|---:|
| `internal/writesurface` | 35 |
| `internal/tiers` | 5 |
| `internal/render` | 3 |
| `internal/readsurface` cursor writes | 2 |
| `internal/scheduler` including resume | 10 |
| `internal/store` evented configuration | 3 |

All 31 opens converge on `tiers.OpenStore` → `store.Open`. Opening may create
the XDG store directory, database, WAL/SHM files, apply schema migrations,
append schema-v13 synthetic configuration history during migration, rebuild
projections when required, and read/verify taxonomy and the identity floor.
Therefore no current store-opening command is safe to implement later as a
client-side SQLite “read.”

Most Repo-scoped paths resolve Git metadata (`--git-common-dir`, `--git-dir`,
worktree name, remotes) and the current Clone/Worktree before their named
operation. That resolution is a guard footprint shared by the rows below.
Role-qualified commits additionally validate the open spawned role in
`Store.Commit`. No command runs `git clone`, creates/deletes a Git worktree, or
changes Git remotes; Clone/Worktree administration currently changes only wip
rows after read-only Git discovery.

## Runnable CLI operation inventory

### Porcelain and local tooling

| CLI path | Current owner and class; durable/filesystem/external effect | Current guard and write footprint | Intended future owner or refusal |
|---|---|---|---|
| `wip status` | `verbs/status` → `tiers.Status` + `readsurface.Content`; **R\***. Reads current/all Repo work, cursor orientation and backlog counts. | Host or current Repo snapshot; no semantic write. | **authority** read through local `wipd`; Environment supplies authenticated current-checkout context. |
| `wip backlog` | `verbs/backlog`; **R\***. Reads active Backlog entries for current Repo. | Current Repo; no semantic write. | **authority** snapshot read. |
| `wip next` | Exact implementation alias of `plumbing next`; see its three modes below. | Same as canonical path. | Same as canonical path. |
| `wip init` | `tiers.Init`; **M/F-read**. Appends `repo.attached` when needed, `clone.attached`, and `worktree.attached`; reads Git common dir/worktree/remotes. It does not modify the Git repo. | Existing common-dir/remote identity, label uniqueness, worktree attachment; writes Repo/Clone/Worktree projections. | **split**: Environment-local Git discovery; authority tier-enrollment command. D115 routing must precede project config. |
| `wip doctor` | `tiers.CheckUnknownClone` + `guards`; **R\*/F-read**. Reads Git metadata, dependency/gate/tracker projections, `git ls-files .wip`, harness targets and generated files. Reports only; no repair. | Candidate/current Repo plus host harness trees; no semantic write. | **environment** diagnostic coordinator over authority snapshot plus authenticated local Git/harness checks. |
| `wip install [harness]` | `manifest` + `harness`; **F**. Detects harnesses, reads target/stamp/checksums, writes generated files and stamp; `--force` may overwrite. No store. | Registered harness and target ownership/drift policy. | **CLI-local refusal**: host tool installation, never authority/domain work. |
| `wip uninstall [harness]` | `harness`; **F**. Validates stamp/ownership and removes the harness target tree. No store. | Registered harness and safe stamped-tree ownership. | **CLI-local refusal**: host tool removal, never authority/domain work. |
| `wip version` | `buildinfo`; local read only. | Compiled metadata; no state. | **CLI-local refusal**: render locally. |
| `wip manifest` | Cobra tree + shipped/override assets; **F-read**. No store. | Registered commands/assets/providers. | **CLI-local refusal**: build locally; it is not an executable domain registry. |

### Backlog, structure, content, and dependencies

| CLI path | Current owner and class; durable/filesystem/external effect | Current guard and write footprint | Intended future owner or refusal |
|---|---|---|---|
| `wip plumbing backlog list` | `verbs/backlog` → `ActiveBacklog`; **R\***. | Current Repo; no semantic write. | **authority** read. |
| `wip plumbing backlog show` | `verbs/backlog` → entry/content reads; **R\*/F-read**. May read sidecar blob bytes. | Entry belongs to current Repo. | **authority** read with verified blob hydration. |
| `wip plumbing backlog add` | `writesurface.BacklogAdd`; **M**. Appends `backlog.entered`; under auto push with a backend also appends `backlog.delegated`, projecting one queued create. | New BacklogEntry, Repo config and optional origin locator; writes only the new entry/outbox birth. | **authority handler**, future delivery-class candidate **capture** keyed by BacklogEntry. |
| `wip plumbing backlog plan` | `writesurface.BacklogPlan`; **M**. Appends `backlog.planned`. | Entered entry and existing Matter in same Repo; writes entry exit. | **authority**: touches pre-existing Matter and entry. |
| `wip plumbing backlog decline` | `writesurface.BacklogDecline`; **M**. Appends `backlog.declined`. | Entered entry and required reason; writes entry exit. | **authority** initially; later metadata may prove capture eligibility for this entry-local exit. |
| `wip plumbing backlog delegate` | `writesurface.BacklogDelegate`; **M**. Appends `backlog.delegated`, projecting a queued tracker create. No network. | Entered entry; writes entry and outbox aggregate. | **authority** because it creates shared delivery work. |
| `wip plumbing matter create` | `writesurface.CreateMatter`; **M**. Appends `matter.created`. | Repo locator uniqueness; writes newborn Matter only. | **authority handler**, future **provisional** class candidate. |
| `wip plumbing stage create` | `writesurface.CreateStage`; **M**. Appends `stage.created`. | Existing Matter/parent and locator uniqueness; writes one subtree node. | **authority handler**; **claim** when exact Matter claim exists. |
| `wip plumbing step create` | `writesurface.CreateStep`; **M**. Appends `step.created`. | Existing Matter/parent, next locator/sort key; writes one subtree node. | **authority handler**; **claim**. |
| `wip plumbing step insert` | `writesurface.InsertStep`; **M**. Appends `step.inserted`, and may prepend `step.reordered` when sort-key space is exhausted. | Parent, sibling order/anchors, locator uniqueness; writes one subtree sibling set. | **authority handler**; **claim** only while every guard/write remains in one claimed subtree. |
| `wip plumbing step reorder` | `writesurface.ReorderStep`; **M**. Appends `step.reordered`. | Complete live sibling set/order; rewrites subtree sort keys. | **authority handler**; **claim**. |
| `wip plumbing step replace` | `writesurface.ReplaceStep`; **M**. Appends `step.replaced`; tombstones old identity and births replacement. | Live Step and next locator; writes one subtree. | **authority handler**; **claim**. |
| `wip plumbing step remove` | `writesurface.RemoveStep`; **M**. Appends `step.removed`. | Live Step and reason; tombstones one subtree node. | **authority handler**; **claim**. |
| `wip plumbing brief` | `verbs/content` → `writesurface.WriteOnce`; **M/F-read/F-write**. Reads stdin or `--file`; appends `content.created`; oversized bytes are written to sidecar blob storage before commit. | Resolved node, write-once kind, content hash/length; node/content row plus optional blob. | **split**: client input becomes authenticated staged blob; authority owns content event/bytes. Future **claim**. |
| `wip plumbing body` | Same path as `brief`, kind `body`; **M/F-read/F-write**. | Same, body kind. | **split**, future **claim**. |
| `wip plumbing workplan` | Same path as `brief`, kind `workplan`; **M/F-read/F-write**. | Same, workplan kind. | **split**, future **claim**. |
| `wip plumbing finding add` | `writesurface.AppendFinding`; **M/F-read/F-write**. Reads positional/stdin/`--file`; appends `content.appended`; may spill blob before commit. | Node or active BacklogEntry finding subject; append-only content; optional blob. | **split**. Matter finding is **claim**; BacklogEntry finding needs later static classification (likely capture only if entry-local). |
| `wip plumbing depend add` | `writesurface.DependAdd`; **M**. Appends `dependency.added`. | Resolves both nodes and runs cycle guard; writes edge and can change readiness. | **authority**: command admits cross-Matter footprint. |
| `wip plumbing depend remove` | `writesurface.DependRemove`; **M**. Appends `dependency.removed`. | Resolves both endpoints/existing live edge; writes edge. | **authority**: command admits cross-Matter footprint. |

### Lifecycle, gates, references, batches, roles, and Runs

| CLI path | Current owner and class; durable/filesystem/external effect | Current guard and write footprint | Intended future owner or refusal |
|---|---|---|---|
| `wip plumbing start` | `writesurface.Start`; **M**. Appends scale-specific `*.started` for Planned ancestors and target; projections may queue tracker state candidates. | Target/ancestor lifecycle, actor role; writes one Matter subtree cascade. | **authority handler**; **claim** for existing Matter subtree. |
| `wip plumbing pause` | `writesurface.Pause`; **M**. Appends scale-specific `*.paused`. | Target must be In Progress; writes one node. | **authority handler**; **claim**. |
| `wip plumbing resume` | `writesurface.Resume`; **M**. Appends scale-specific `*.resumed`. This is node lifecycle resume, not scheduler Run resume. | Target must be Paused; writes one node. | **authority handler**; **claim**. |
| `wip plumbing cancel` | `writesurface.Cancel`; **M**. Appends scale-specific `*.canceled`; then computes a read-only cursor handoff. | In Progress target and reason; writes one node/candidates. | **authority handler**; **claim**; handoff is result derivation. |
| `wip plumbing finish` | `writesurface.FinishWithEnvResult`; **M/X-read/F**. Appends scale-specific `*.finished`, possibly `batch.swept`; projection may queue tracker work. If the Matter seals, performs best-effort live tracker alignment reads and writes the final generated tree. | Lifecycle/child/gate completion, anonymous Batch sweep, current execution context; subtree + possible Batch aggregate, then local path. | **split**. Authority owns finish/sweep and post-seal truth read; Environment owns authenticated render path. Aggregate sweep may force **authority**, otherwise claim-local finish. |
| `wip plumbing gate list` | `verbs/gate` read surface; **R\***. | Current Repo declarations; no semantic write. | **authority** read. |
| `wip plumbing gate status` | `verbs/gate` read surface; **R\***. | Node plus own/enclosing declarations/completions; no write. | **authority** read. |
| `wip plumbing gate declare` | `writesurface.DeclareGate` → `store.DeclareGate`; **M**. Runs gate-order guard and appends `gate.declared` with exemption snapshot. | Repo declarations, gate order, all sealed nodes at scale; writes Repo-wide config/projections. | **authority** by D120. |
| `wip plumbing gate repair` | `writesurface.RepairGateExemption` → store config owner; **M**. Appends `gate.exemption-repaired`. | Incident-only safety checks over declaration/node history; writes exemption. | **authority** by D120. |
| `wip plumbing gate close` | `writesurface.CloseGateWithEnvResult`; **M/X-read/F**. Appends `gate.closed`, possibly `batch.swept`; final seal triggers alignment reads and final render. | Effective gate ownership/state, Done node, role actor, possible Batch aggregate/local path. | **split** as for `finish`; claim-local only when no aggregate footprint. |
| `wip plumbing gate dismiss` | `writesurface.DismissGateWithEnvResult`; **M/X-read/F**. Appends `gate.dismissed`, possibly `batch.swept`; final seal has the same alignment/render effects. | Done node, open gate, emergency reason, actor; possible Batch aggregate/local path. | **split**; authority mutation plus Environment render. |
| `wip plumbing bind` | `writesurface.Bind`; **M**. Appends `reference.added`; projection can queue current aggregate state. No network. | Matter, reference set, push-level snapshot and shared-reference aggregate. | **authority**: references/aggregates may span Matters. |
| `wip plumbing unbind` | `writesurface.Unbind`; **M**. Appends `reference.removed`; may recompute/queue aggregate. | Existing membership and all remaining bound Matters. | **authority**. |
| `wip plumbing rebind` | `writesurface.Rebind`; **M**. Appends `reference.rebound`; may recompute source/destination aggregates. | Existing membership plus two shared aggregates. | **authority**. |
| `wip plumbing batch create` | `writesurface.CreateBatch`; **M**. Appends `batch.created`. | Batch-name uniqueness; Repo-null Batch projection. | **authority**. |
| `wip plumbing batch join` | `writesurface.JoinBatch`; **M**. Appends `batch.joined`. | Named Batch, Matter, membership/domain relation. | **authority**. |
| `wip plumbing batch leave` | `writesurface.LeaveBatch`; **M**. Appends `batch.left`. | Existing named membership. | **authority**. |
| `wip plumbing batch dismiss` | `writesurface.DismissBatch`; **M**. Appends `batch.dismissed`. | Named open Batch and members/state. | **authority**. |
| `wip plumbing role active` | Gate declarations → `writesurface.Activation`; **R\***. | Repo gate config; no write. | **authority** read. |
| `wip plumbing role list` | Reads open Dispatch roles; **R\***. | Current Worktree/open Dispatch. | **authority** snapshot read with Environment context. |
| `wip plumbing role spawn` | `writesurface.SpawnRole`; **M**. Appends `role.spawned`. | Current open Dispatch, role activation and no duplicate open role. | **authority handler**; **claim** within Dispatch/Matter. |
| `wip plumbing role close` | `writesurface.CloseRole`; **M**. Appends `role.closed` as that role. | Matching open role in current Dispatch. | **authority handler**; **claim**. |
| `wip plumbing run list` | Store Run reads + `runlock.Probe`; **R\*/F**. Probe creates runtime lock directory/file if absent, briefly locks it, and reports liveness. | All Runs plus current Clone ownership and host-local advisory locks. | **environment** read coordinator over authority snapshot; lock state never routes or becomes truth. |
| `wip plumbing run show` | Same as Run list for one ULID; **R\*/F**. | Run identity/current Clone/local lock. | **environment** read coordinator. |
| `wip plumbing run stand-down` | `writesurface.StandDownRun`; **M**. Acquires/probes the local advisory lock, appends `run.stood-down` and `dispatch.closed(reaped)` for every open Run Dispatch. It does not sweep scratch directories. | Run open/interrupted state, current Clone ownership and free lock; Run plus all open Dispatches. | **authority** operation coordinated by **environment** lock today. D126 later changes cross-Environment guards; never a force path. |

### Navigation, render, cleanup, and tier administration

| CLI path | Current owner and class; durable/filesystem/external effect | Current guard and write footprint | Intended future owner or refusal |
|---|---|---|---|
| `wip plumbing next` (read mode) | `scheduler.Derive` for a multi-node open-Run frontier, else `readsurface.Next`; **R\***. | Current Repo/Clone/Worktree cursor, lifecycle, gates, dependencies, Run cap/frontier; no semantic write. | **authority** snapshot read; Environment supplies cursor/worktree identity. |
| `wip plumbing next --set` | `readsurface.SetCursor`; **M/F-read**. Appends `cursor.moved`; also resolves worktree root for output. | Target identity/liveness and current Worktree cursor key. | **environment** command through `wipd`, future D125 delivery class **environment**. |
| `wip plumbing next --clear` | `readsurface.ClearCursor`; **M** when set, no-op when already clear. Appends `cursor.moved` with empty target. | Current Worktree cursor key. | **environment** command through `wipd`. |
| `wip plumbing refresh [locator]` | `render.Refresh`/`Render`; **M/F**. Refuses tracked `.wip`; ensures `.wip/generated`, `.wip/work`, and `.git/info/exclude`; opens/reuses or supersedes Dispatch (`dispatch.opened`/`dispatch.closed`), creates/sweeps scratch, writes/prunes 0444 generated files, and appends `render.performed`. | Current Repo/Clone/Worktree, tracked-`.wip` check, Dispatch staleness/content snapshot and local root. | **split**: authority owns Dispatch/render events; Environment owns authenticated checkout effects. Future claim acquisition semantics replace the P1 plain bracket. |
| `wip plumbing dispatch close` | `render.CloseExplicit`; **M/F**. Appends `dispatch.closed(completed)` then removes that Dispatch scratch tree. | Current Worktree open Dispatch. | **split**: authority close + Environment scratch sweep. |
| `wip plumbing clean` | `render.Clean`; **R/M/F**. Scans work scratch; stale open Dispatches append `dispatch.closed(reaped)`; removes unknown/closed/stale scratch trees; `Store.ReapOrphanBlobs` deletes old unreferenced sidecars without an event. | Current Worktree directories/Dispatch rows, staleness cutoff, global content references/blob mtimes. | **split**: authority owns Dispatch/blob retention decision; Environment owns checkout scratch deletion. Blob deletion stays authority-owned. |
| `wip plumbing clone list` | `verbs/clone`; **R\*/F-read**. Reads Repo/Clone projections and optionally current Git identity. | Selected/current Repo; no semantic write. | **authority** read with Environment discovery context. |
| `wip plumbing clone relink` | `tiers.Relink`; **M/F-read**. Reads current Git common dir/remotes and appends `clone.relinked`. Does not move/create a checkout. | Known Repo, selected old Clone, uniqueness of new common-dir binding. | **split**: Environment discovery; authority tier-administration write. |
| `wip plumbing label` | `tiers.SetLabel`; **M/F-read**. Appends `clone.labeled`. | Current Clone and Repo label uniqueness. | **authority** tier-administration operation with Environment identity. |
| `wip plumbing status` | Same gatherer as porcelain, full tier/content render and optional history expansion; **R\***. | Host/current Repo snapshot; no semantic write. | **authority** read via local daemon. |
| `wip plumbing session` | `readsurface.Derive`; **R\*/F-read**. Reads the whole event log and tool config for default idle gap; persists nothing. | Current host-wide legacy log and idle-gap parameter. | **authority-domain** snapshot read. D124 later changes narration order/scope. |

### Evented tracker configuration, outbox, and tracker seam

| CLI path | Current owner and class; durable/filesystem/external effect | Current guard and write footprint | Intended future owner or refusal |
|---|---|---|---|
| `wip plumbing outbox list` | Store outbox view; **R\***. | Current Repo outbox. | **authority** read. |
| `wip plumbing outbox level [value]` | No arg: **R\***. Arg: `Store.SetTrackerPushLevel`; **M**, appends `config.set`. | Current Repo config key; validates enum; no retroactive candidates. | **authority** config operation. |
| `wip plumbing outbox backlog-push [value]` | No arg: **R\***. Arg: `Store.SetTrackerBacklogPush`; **M**, appends `config.set`. | Current Repo config key/backend warning; lazy effect. | **authority** config operation. |
| `wip plumbing outbox backend [value]` | No arg: **R\***. Arg: `Store.SetConfig`; **M**, appends `config.set`. No tracker contact. | Registered backend name/current Repo key. | **authority** config operation. |
| `wip plumbing outbox target [value]` | No arg: **R\***. Arg: `Store.SetConfig`; **M**, appends `config.set`. No tracker contact. | Opaque target/current Repo key. | **authority** config operation. |
| `wip plumbing outbox project [value]` | No arg: **R\***. Arg: `Store.SetConfig`; **M**, appends `config.set`. No tracker contact. | Opaque project/current Repo key. | **authority** config operation. |
| `wip plumbing outbox canceled-label [value]` | No arg: **R\***. Arg: `Store.SetConfig`; **M**, appends `config.set`. No tracker contact. | Label/current Repo key. | **authority** config operation. |
| `wip plumbing outbox approve` | `writesurface.OutboxApprove`; **M**. Appends `outbox.approved`. | Queued entry state. | **authority**. |
| `wip plumbing outbox decline` | `writesurface.OutboxDecline`; **M**. Appends `outbox.declined`. | Eligible entry and reason. | **authority**. |
| `wip plumbing outbox retry` | `writesurface.OutboxRetry`; **M**. Appends `outbox.retried`. | Failed/withheld entry. | **authority**. |
| `wip plumbing outbox flush` | `tracker.Flush`; **R/M/X/F-read**. Resolves provider/config/credentials; reads and composes approved entries; provider may guard-read and write tracker state/item/comment; records one of `outbox.withheld`, `outbox.delivery-failed`, `outbox.flushed`, `tracker.item-created`, `tracker.state-pushed`, or `tracker.state-observed` per result. | Whole Repo outbox, shared-reference push record, provider lease/state and credentials; writes tracker plus durable outcomes. | **authority** external-effect operation. It is never deferred; credentials/effect placement must be explicit in M6. |
| `wip plumbing tracker capabilities` | Resolves configured seam and reports local capabilities; **R\*/F-read**. May read provider credential/config sources; no provider request. | Current Repo config/provider construction. | **authority/environment daemon read**, not CLI direct store access. |
| `wip plumbing tracker read` | Provider `ReadContent` or `ReadState`; **R\*/X/F-read**. Persists nothing. | Configured backend/target/ref and provider credentials. | **authority** external read so it observes the same configured seam; never inbound mutation. |
| `wip plumbing tracker refs` | Local reference/aggregate read; **R\***. No seam/network. | Current Repo or Matter references/shared aggregate. | **authority** read. |
| `wip plumbing tracker propose create` | `writesurface.ProposeTrackerCreate`; **M**. Appends `tracker.adhoc-proposed`, projecting queued unapproved create. No network. | Current Repo, nonempty title/detail, deterministic idempotency. | **authority**: creates shared outbox work. |
| `wip plumbing tracker propose comment` | `writesurface.ProposeTrackerComment`; **M**. Same event family for comment. | Ref/body validation; writes outbox proposal. | **authority**. |
| `wip plumbing tracker propose state` | `writesurface.ProposeTrackerState`; **M**. Same event family for state. | Ref/disposition validation; writes outbox proposal. | **authority**. |

## Engine owners not exposed as CLI operations

These paths are included because a CLI-only manifest walk would miss direct
store owners that M6 must migrate. There is deliberately no production CLI
entry point for either scheduler function: MODEL D87/D89 and the scheduler
package keep live control-plane start/resume out of the CLI.

| Engine operation | Current owner/effects and footprint | Intended future owner or refusal |
|---|---|---|
| `scheduler.Orchestrate` | **R/M/F**. Acquires the host-local Run lock; reads Run/frontier/Dispatch; directly appends `dispatch.opened`, `dispatch.closed`, planned `step.created`, lifecycle starts/finishes, `run.skipped`, `run.finished`, `role.spawned`, and `role.closed`; invokes Work/Plan/Ask/PostSeal hooks. Footprint spans Run, Batch members, Matter claims, roles, and possibly several Repos. | **authority daemon scheduler**, with Environment-local advisory lock only as liveness. **Explicit CLI refusal:** no live control-plane CLI operation. |
| `scheduler.Resume` | **R/M/F**. Requires owning Clone and free local Run lock; appends `run.resumed` plus orphan `dispatch.closed(reaped)`, then re-enters the same orchestration path without replaying completed work. | **authority daemon scheduler** under D126 claim semantics. **Explicit CLI refusal:** no partial/degraded `wip run resume`. |
| `writesurface.CreateAnonymousBatch` | Direct helper, not currently a command; appends `batch.created` for scheduler/seal mechanics. | **authority**, atomically with D121 claim acquisition when migrated. |

## Every `tiers.OpenStore` path

All sites below call the one implementation at `internal/tiers/tiers.go:20-25`.
Shared helpers intentionally account for several runnable commands.

| Source site | Runnable consumers |
|---|---|
| `internal/verbs/backlog/backlog.go:94` | porcelain backlog; plumbing backlog add/list/show/plan/decline/delegate |
| `internal/verbs/batch/batch.go:47` | batch create/join/leave/dismiss |
| `internal/verbs/bind/bind.go:32` | bind |
| `internal/verbs/bind/bind.go:97` | unbind, rebind |
| `internal/verbs/clean/clean.go:36` | clean |
| `internal/verbs/clone/clone.go:47` | clone list |
| `internal/verbs/clone/clone.go:129` | clone relink |
| `internal/verbs/content/content.go:30` | brief, body, workplan, finding add |
| `internal/verbs/depend/depend.go:36` | depend add/remove |
| `internal/verbs/dispatch/dispatch.go:44` | dispatch close |
| `internal/verbs/doctor/doctor.go:45` | doctor |
| `internal/verbs/gate/gate.go:315` | gate list/status/declare/repair shared opener |
| `internal/verbs/gate/gate.go:413` | gate close |
| `internal/verbs/gate/gate.go:491` | gate dismiss |
| `internal/verbs/init/init.go:37` | init |
| `internal/verbs/label/label.go:33` | label |
| `internal/verbs/lifecycle/lifecycle.go:59` | start/pause/resume/cancel shared opener |
| `internal/verbs/lifecycle/lifecycle.go:159` | finish |
| `internal/verbs/matter/matter.go:45` | matter create |
| `internal/verbs/next/next.go:64` | porcelain/plumbing next, all modes |
| `internal/verbs/outbox/outbox.go:378` | every outbox command through `openRepo` |
| `internal/verbs/refresh/refresh.go:43` | refresh |
| `internal/verbs/role/role.go:45` | role spawn/close/list shared opener |
| `internal/verbs/role/role.go:94` | role active |
| `internal/verbs/run/run.go:195` | run stand-down |
| `internal/verbs/run/run.go:246` | run list/show through `openCurrent` |
| `internal/verbs/session/session.go:37` | session |
| `internal/verbs/stage/stage.go:45` | stage create |
| `internal/verbs/status/status.go:124` | porcelain/plumbing status |
| `internal/verbs/step/step.go:43` | every step command |
| `internal/verbs/tracker/tracker.go:96` | every tracker command through the package opener |

No other production package calls `store.Open`; `tiers.OpenStore` itself is the
single production lexical `store.Open` caller. `store.OpenWithClock` is a test
seam.

## Every production `Store.Commit` caller path

The append/project implementation is `internal/store/store.go:397`; callers
below are all production call sites outside that implementation. Event lists
name the normal drafts, not every projection side effect already noted above.

| Current owner | Commit site(s) and operation |
|---|---|
| cursor | `internal/readsurface/next.go:325` `SetCursor` → `cursor.moved`; `:372` `ClearCursor` → `cursor.moved` |
| render/dispatch | `internal/render/dispatch.go:62` open → `dispatch.opened`; `:94` close/supersede/reap → `dispatch.closed`; `internal/render/refresh.go:158` → `render.performed` |
| tiers | `internal/tiers/adopt.go:57` → `repo.key-adopted`; `internal/tiers/init.go:84` existing Clone → `worktree.attached`; `:177` new tier attachment → `repo.attached`/`clone.attached`/`worktree.attached`; `internal/tiers/move.go:136` → `clone.relinked`; `internal/tiers/relabel.go:54` → `clone.labeled` |
| evented config | `internal/store/config.go:172` `SetConfig` → `config.set`; `:300` `DeclareGate` → `gate.declared`; `:335` `RepairGateExemption` → `gate.exemption-repaired` |
| birth | `internal/writesurface/birth.go:41` Matter; `:79` Stage; `:115` Step |
| amendment | `internal/writesurface/amendment.go:98` insert/rebalance; `:183` reorder; `:213` replace; `:238` remove |
| content | `internal/writesurface/content.go:41` write-once; `:65` append finding |
| dependencies | `internal/writesurface/depend.go:42` add; `:75` remove |
| lifecycle | `internal/writesurface/lifecycle.go:97` start cascade; `:158` finish/cancel/pause/resume plus optional seal sweep |
| gates | `internal/writesurface/gate.go:231` dismiss plus optional sweep; `:363` close plus optional sweep |
| references | `internal/writesurface/bind.go:20` bind; `:43` unbind; `:62` rebind |
| backlog | `internal/writesurface/backlog.go:50` add/optional auto-delegate; `:102` plan; `:126` decline; `:145` delegate; `:197` confirm tracker creation |
| batches | `internal/writesurface/batch.go:36` named create; `:55` anonymous create; `:86` join; `:118` leave; `:154` dismiss |
| roles | `internal/writesurface/role.go:96` spawn; `:146` close |
| Run stand-down | `internal/writesurface/run.go:39` → `run.stood-down` plus open `dispatch.closed` drafts |
| outbox outcomes | `internal/writesurface/outbox.go:66` tracker state pushed; `:83` tracker state observed; `:97` approve/decline/retry/withhold/fail/flush |
| ad-hoc tracker proposals | `internal/writesurface/tracker.go:101` → `tracker.adhoc-proposed` |
| scheduler | `internal/scheduler/loop.go:326` open claim Dispatch; `:348` close claim Dispatch; `:374` planned Step births; `:394` start cascade; `:428` finish; `:514` skip; `:581` finish Run; `:594` spawn role; `:609` close role; `internal/scheduler/resume.go:97` resume + reap orphan Dispatches |

That table contains 58 caller sites: 35 writesurface + 5 tiers + 3 render +
2 cursor + 10 scheduler + 3 config.

Four additional production `.Commit()` tokens are SQL transaction mechanics,
not semantic `Store.Commit` callers:

- `internal/store/store.go:457` commits the append-plus-project transaction;
- `internal/store/migrations.go:220` commits a schema migration;
- `internal/store/project.go:269` commits projection rebuild;
- `internal/store/config.go:274` commits the legacy schema-v8–v12 direct gate
  declaration branch. Current `Open` migrates to latest before CLI execution,
  but the branch remains production code for version-pinned internals.

Schema-v13 migration synthetic history uses `appendMigrationEvent` before a
`Store` exists; it is the named exception to live `Store.Commit` and is reached
indirectly by any `OpenStore` against a pre-v13 database.

## Filesystem and external-effect owner census

This is the non-SQL side-effect checklist used to challenge the operation
tables above:

| Owner | Effects reachable from current CLI/engine |
|---|---|
| `store.Open` / migrations | Store parent, SQLite database/WAL/SHM, migration backup copy, schema/projection writes. |
| `store.ContentDraft` / content reads | Sidecar blob directory/write before commit; verified blob reads for content. A failed commit can leave an orphan for `clean`. |
| `store.ReapOrphanBlobs` | Deletes old unreferenced sidecars directly, with no model event. |
| `render` | Creates `.wip/generated` and `.wip/work`; appends `.wip/` to `.git/info/exclude`; creates/removes Dispatch scratch trees; writes, chmods, and prunes generated files. |
| `runlock` | Creates host runtime directory and persistent empty lock files; probes/acquires kernel advisory locks. |
| `harness` / `manifest` | Reads harness targets/assets/stamps/checksums; install writes generated trees/stamps; uninstall removes a stamped tree. |
| `tiers` / tracked-`.wip` guard | Executes read-only Git metadata and `git ls-files`; no Git mutation subprocess exists. |
| content CLI | Reads an arbitrary explicit `--file` path or stdin before content submission. Future protocol must use staged bytes/hash, not transport a generic path. |
| tracker providers | Read environment/provider config credentials; make HTTP reads/writes for `tracker read`, seal alignment, and `outbox flush`. Only flush writes external tracker state. |
| scheduler hooks | `Work`, `Plan`, `Ask`, and `PostSeal` are injected external behavior. Their semantic effects are not captured by a generic filesystem path or callback in a future command. |

## Reproducible checks and omission traps

Run from the repository root after activating the committed Nix development
shell. These commands do not initialize or mutate wip domain state; the
manifest command builds local metadata only.

```sh
. "$HOME/.config/wip/orb-dev-shell.sh" >/dev/null

# Runnable command baseline: expected 79 at the census commit.
go run ./cmd/wip manifest --json > /tmp/wip-manifest.json
jq '.verbs | length' /tmp/wip-manifest.json
jq -r '.verbs[].name' /tmp/wip-manifest.json

# Every production OpenStore site: expected 31. The second query must name
# only internal/tiers/tiers.go as a production direct store.Open caller.
rg -n --glob '*.go' --glob '!**/*_test.go' \
  'tiers\.OpenStore\(' internal
rg -n --glob '*.go' --glob '!**/*_test.go' --glob '!internal/spike/**' \
  'store\.Open\(' cmd internal

# Every production Commit token. Expected: 62 total = 58 semantic caller
# sites plus four SQL transaction commits. Review owner files, not only count.
rg -n --glob '*.go' --glob '!**/*_test.go' --glob '!internal/spike/**' \
  '\.Commit\(' cmd internal

# Side effects that can be missed by Store.Commit-only searches.
rg -n --glob '*.go' --glob '!**/*_test.go' --glob '!internal/spike/**' \
  'os\.(WriteFile|Mkdir|MkdirAll|Remove|RemoveAll|Rename|Chmod|OpenFile)|io\.Copy|exec\.Command(Context)?\(' \
  cmd internal
rg -n --glob '*.go' --glob '!**/*_test.go' \
  'ReapOrphanBlobs|ContentDraft|ReadContent|ReadState|\.Deliver\(' internal

# Scheduler APIs must remain visible even though the manifest has no start or
# resume control-plane command.
rg -n --glob '*.go' --glob '!**/*_test.go' \
  'func (Orchestrate|Resume)\(|scheduler\.(Orchestrate|Resume)' internal cmd
```

Targeted repository validation for this documentation-only step is:

```sh
. "$HOME/.config/wip/orb-dev-shell.sh" >/dev/null
go test ./internal/cli ./internal/verbs/... ./internal/tiers \
  ./internal/render ./internal/readsurface ./internal/scheduler \
  ./internal/store ./internal/tracker ./internal/writesurface
```

The omission traps are intentional: manifest-only review misses scheduler;
`OpenStore`-only review misses direct owners after the handle is passed;
`Store.Commit`-only review misses migrations, blob deletion, local files,
locks, provider effects, and pre-commit blob writes; command-name review misses
read/set modes sharing one Cobra command.

## Classification questions deliberately left for later BDS-227/M6 work

1. **Composite seal/render ordering.** `finish`, gate close/dismiss, refresh,
   Dispatch close, and clean combine authority truth with Environment-local
   files. M6 must choose and test the authenticated path capability, ordering,
   retry, and partial-failure result; Step 1 only identifies the split.
2. **Tracker effect placement and credentials.** Outbox truth/delivery is
   authority-class, while current credentials are process-local. The future
   handler must state where provider credentials live and execute; this census
   does not turn them into command fields.
3. **Finding and Backlog delivery class.** Matter findings are claim-local;
   BacklogEntry findings and some entry-local exits may qualify for capture.
   Static metadata must split semantic operations if one operation name cannot
   honestly declare one complete footprint.
4. **Seal-time anonymous Batch sweep.** An otherwise subtree-local finish/gate
   write can also write the Batch aggregate. Under D120/D121 that makes the
   complete operation authority-class unless claim-release design moves the
   sweep to a distinct authority operation.
5. **Harness operations.** This census explicitly keeps install/uninstall,
   manifest, and version local and refuses authority routing. If M6 interprets
   “every CLI capability” as requiring daemon mediation even for tool
   installation, that must be an explicit reversal with a host-local security
   contract, not accidental generic command routing.

No question above blocks the Step 1 census. Each names a later ownership or
metadata decision without starting BDS-227 Steps 2–4.
