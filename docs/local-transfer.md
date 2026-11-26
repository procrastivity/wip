# Offline local transfer

`wip export` and `wip import` move selected current work snapshots using local
files. They do not require Cloud, an account, a checkout or a hostname override.
The same commands exist under `wip plumbing` for scripts, with `--json` and
structured `transfer.invalid` errors.

## Preserve both originals before starting

Keep the old-host and new-host originals as private backups. Stop all WIP,
agent and companion writers while copying each DB **and its sibling `blobs/`**.
Keep the committed `wip.db-wal` if present; copying a live database without its
WAL can lose committed work. Make a separate frozen recovery copy for export
and a separate populated destination copy for your first import trial.

Inspection copies DB/WAL/blobs before opening SQLite, checks integrity and
normalizes only the private capture. Schema adaptation and projection rebuild
for previews happen on scratch copies, never on either selected original DB.
Captures detect changing files and fail; they are not a replacement for stopping
writers or a general live-backup facility. Linked/non-regular blob files are
refused, not followed. No tool configuration or credentials are loaded.

## Browse and select old work

The following paths and IDs are placeholders. Use the explicit absolute path to
your recovery-copy database, not the normal new-host database. No command falls
back to `WIP_DB_PATH` or the hostname-selected store.

```sh
wip export list --source-db /recovery/old-copy/wip.db --json
wip export list --source-db /recovery/old-copy/wip.db --repo SOURCE_REPO --json
wip export plan --source-db /recovery/old-copy/wip.db --repo SOURCE_REPO \
  --matter MATTER_ID --backlog PLANNED_ENTRY_ID --out /recovery/export.json
wip export write --plan /recovery/export.json --out /recovery/old-work.wiparchive
```

Omit `--repo` on `list` to discover native Repo IDs. Inventory includes terminal
backlog entries, not just active intake. Select at least one Matter or backlog
entry. Selectors repeat or take comma-separated IDs. A planned backlog entry
includes its resulting Matter's required tree and content; origin-node links
can add another Matter. Review that added closure in the plan before writing.
Unselected external blockers must be selected too; they cannot become silently
satisfied dependencies. No whole-store or implicit whole-project export exists.

The export plan saves a private sibling `.snapshot.wiparchive`. Keep it beside
the plan until `export write` succeeds. The saved preview, captured archive and
digest must agree; editing the preview requires a fresh plan rather than silently
exporting the original metadata. The hidden `.wip-export-lineage-*` file
in the plan directory provides a stable origin namespace for later exports of
the same source path/Repo. Reuse that directory and source-copy path for later
exports. Losing/moving this workspace can create a new lineage; do not assume
automatic deduplication across independently prepared recovery copies. Imported
objects preserve known upstream origins when re-exported.

Plans, captured snapshots and archives contain private work. New files are
0600 and never overwrite existing files. The `.wiparchive` is a versioned ZIP
of typed snapshot JSON and digest-addressed content, not a database merge or
an executable package. Hashes detect corruption; they do not authenticate an
archive's author or encrypt its content. Treat copies accordingly.

## Inspect, preview and apply locally

```sh
wip import inspect /recovery/old-work.wiparchive --json
wip import inspect /recovery/old-work.wiparchive --content CONTENT_ID
wip import plan /recovery/old-work.wiparchive \
  --target-db /recovery/new-copy/wip.db --repo TARGET_REPO \
  --out /recovery/import.json
wip import apply --plan /recovery/import.json
```

`inspect --content` reads exact segment bytes; `--json` returns bytes as base64.
Preview chooses fresh target-local IDs and retains stable source mappings.
It never matches work by title or remote. Resolve a conflicting Matter locator
explicitly with `--rename SOURCE_MATTER_ID=new-locator` on `import plan`.
Unknown/changed origins, incomplete required content and unsupported work fail
before work is imported. A changed source snapshot never overwrites prior imports.
An import-time locator rename is a destination-local alias. Browsing shows the
alias; re-export preserves the upstream locator while that alias is unchanged,
so overlapping transfers still reuse the same origin. If a later descendant
uses the upstream locator, export keeps the local Matter alias to avoid a
collision. This edited snapshot retains its origins and can be imported into a
fresh store; recipients of the older revision still reject the changed source.
A later locator change that differs from the import-time alias remains a source
change on re-export.

The target Repo must already exist; initializing/registering it is a separate
local action. Import never registers or relinks an old Clone/Worktree path.
If the target needs a schema upgrade, the plan shows both versions. Create a
reviewed plan with `--allow-migration` to acknowledge the upgrade on apply.
The pre-upgrade backup is retained, but migrations can commit separately from
the atomic work import. A failure must not be described as rolling migrations
back automatically.

Apply retains a DB/WAL/blob backup under the destination's `transfer-backups/`,
then commits imported work, source mappings and its receipt atomically. Stop
writers for the backup capture. Stale/conflicting plans fail rather than applying
different mappings silently. Retry the **same plan** after an uncertain result;
the receipt reconciles the original operation without duplicate work. Repeat
imports do not resurrect objects subsequently changed or removed locally.
Receipt-only recovery still works after the importing role closes; it neither
creates another event nor requires respawning that role.
Do not restore a whole backup over intervening work without reviewing it first.

## Pairing remains the only project-sync opt-in

Import itself performs no HTTP, upload, pairing, resume or destination change.
Its preview shows the destination Repo's locally retained pairing state:

- Active paired project: supported imported work and later edits are eligible
  for its next ordinary sync under that pairing. No hold/release step exists.
- Paused project: stays paused; importing does not resume it.
- Unpaired project: stays local until explicitly paired.

Compatible clients synchronize the supported snapshot-v1 state and subsequent
ordinary portable edits under existing pilot pairings, including imports made
before pairing. Each new import commits an immutable portable state root and
an atomic private receipt/plan companion. Only the state root travels; no
pairing/account metadata or execution authority is included. A snapshot records
observed state, not invented historical creation or start commands.

Receivers keep the shared native object identities and verify each object's
membership in its snapshot before accepting later edits. Independently importing
the same archive on different peers does not establish that correspondence:
divergent identities, origins or occupied addresses remain visible conflicts,
not title-based merges. Older clients retain unknown snapshot families as
unsupported evidence; upgrade peers rather than bypassing validation.

Content is applied with the whole snapshot, never partially. Spilled content
may remain visibly pending until a later ordinary pass fetches and verifies its
bytes. Cloud's existing 32 MiB per-blob transport limit still applies. A larger
local import remains valid locally, but sync reports the incompatible content
instead of uploading an incomplete snapshot or imposing a local archive quota.

Development stores containing the older private-only `work.imported` shape
remain readable and rebuildable. Their immutable events and captured upload
identities are not rewritten automatically. A later edit requiring one of those
legacy imports reports the missing portable snapshot anchor before upload;
legacy conversion is not part of this bridge.

For private staging, use an unpaired project. Pausing only delays transfer.

## Snapshot-v1 coverage and limits

Supported: selected Matter/Stage/Step trees and order, current lifecycle and
cancellation reasons, Brief/Workplan/body, ordered findings including spilled
bytes, internal dependencies, and entered/declined/planned backlog with resolved
origin/planned-Matter links. Combined and overlapping selections reuse known
origin mappings. Exact session/activity chronology is not reconstructed; import
events record honest local import attribution separately from source identity.
`import apply` honors the current invocation's `--as-role` or `WIP_AS_ROLE`,
including the normal requirement for an open spawned role for new import writes.
The archive cannot supply that authority.

Native tree order is preserved even when source nodes share sort keys: those
Matters use portable ordered ranks rather than relying on old birth-event IDs.
Incoming tied keys remain accepted. Because archives do not carry native birth
order, imports break those ties by stable origin identity, then use the same
ranks for fingerprints and native storage. Archive aliases and slice order do
not decide the result; unchanged re-exports retain the same origins.
Content order numbers are likewise canonicalized to relative findings order;
nonzero or gapped orders remain accepted without causing false origin conflicts.

Selected work requiring gates, external references, delegated backlog,
replacement/removal/tombstone relationships or other unsupported families is
refused with diagnostics rather than silently omitted. This initial profile
also refuses source or target Repos with gate declarations; use an ungated
staging Repo rather than changing an existing project's gate policy. The complete original
backup remains the source for later broader profiles. Whole-project coverage,
exact historical audit and changed-source reconciliation are later work.

Credentials, config, old paths as active bindings, pairing state, tracker queues,
delivery retry keys, active Runs/Dispatches/roles and execution ownership never
travel. Import and projection rebuild do not manufacture execution or delivery
candidates. Ordinary future local commands use the destination's own policy.

The initial codec holds snapshot content bytes in memory during validation and
import; it does not impose an item quota. Very large archives need sufficient
memory/disk. Streaming improvements can preserve the same format and semantics.
