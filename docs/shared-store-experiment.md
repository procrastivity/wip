# Bounded shared-store source/effect experiment

The opt-in experiment demonstrates compatible offline starts against the real
store and projectors while the normal CLI remains independent of a companion.
Its extensions below add bounded Laravel transport and explicit CLI pairing.
It is not production sync, a daemon, or permission to use real data.

Run the disposable Hybrid/Fluke scenarios from the repository root:

```sh
nix develop --command go test -tags sharedstoreexperiment ./internal/store -run 'SharedStore|IndependentlyOpened' -count=1 -v
```

The implementation is now available in ordinary builds for `plumbing sync`;
the larger disposable-fixture acceptance suites still require that test tag.
`EnableSharedStoreExperiment` explicitly creates seven lab tables and source
immutability triggers. Normal `Open`, schema versions, and migrations do not
create or consult them. The default CLI binary built by the tests has no tag.
Tests use temporary Git checkouts and databases, real CLI subprocesses, ordinary
`writesurface.Start`, native transactions, event stamping, and native projectors.
No provider is approved or flushed by the experiment.

## Original activity and native effects have separate identities

`ExperimentExport(host, correlation)` reads a complete committed command from a
transactional log snapshot. Its companion metadata includes the preceding
subject event ID, the original birth's parent, and pre-command ancestor event
references, derived from immutable history.
Export also records the locally supplied origin for each native event; later
exports cannot relabel it. Incoming claims never establish native provenance.
This is deliberately stronger/more conservative than a lifecycle-only baseline:
intervening content or other subject changes can leave the group unresolved.
Neither the current Transition payload nor its causation names that predecessor.

`ExperimentImport` retains original envelopes, IDs, source times, actor labels,
hosts, group/cause links, baseline evidence, disposition, and per-action effect
mappings. One short writer transaction classifies the whole group and records
all bookkeeping and necessary effects atomically. An exact retry returns the
durable outcome; reusing an already retained foreign source ID with changed data
fails. Native IDs are checked globally against untouched log evidence and the
locally recorded export origin before any coalescing or Repo mapping. A native-ID
collision (including an unverified origin) is retained separately as a conflict,
without reserving source/action IDs needed by a subsequent genuine self-echo.
Exact collision retries are inert. These decisions are inspectable through
`ExperimentActivities`; group-ID-only recheck/acknowledgement addresses canonical
sources, not colliding claims. No existing-history repair/adoption is implemented.

A missing compatible start gets a fresh local ID and the fixed actor
`system:shared-store-experiment`. An equivalent start maps to the existing native
start, with no second transition. Cascade members can map to different existing
effects while only the missing child gets a new effect. Foreign causes are never
inserted into native event foreign keys. Integration effects cannot be exported
as new original actions. Native append/projection guards and local repeated-start
refusal are unchanged.

`ExperimentActivities` exposes retained source groups and local effect links.
`ExperimentActivitySnapshot` combines native originals and retained sources in a
single transactional snapshot. Original IDs deduplicate self echoes; independent
overlapping starts remain separate activities even when sharing one effect.
Original actors are attribution only. Native integration effects are excluded.
Entire separately retained collision groups are excluded from this combined
work/period view, including their non-native action IDs. They stay inspectable in
`ExperimentActivities` without replacing canonical native or foreign originals.
Ordinary canonical conflict activities remain visible. Even exact echoes cannot
fill unknown local attribution from an incoming claim.
Inline Brief content IDs are also global: reuse is a retained conflict even when
the existing row belongs to another Matter or is tombstoned. Accepted bytes remain
unchanged and unrelated later inbox records can still be processed.

## Explicit reconsideration is separate from delivery

`ExperimentRecheck(group)` evaluates the immutable retained group under the same
short writer reservation as classification, effects, mappings, current decision,
and decision history. Redelivery still returns the stored outcome. Already
integrated groups always reuse their mappings, even after later local work.

A child-only start can remain pending until a separate supported ancestor start
arrives. Source ancestor references resolve through retained source-to-local
mappings, not clocks or current InProgress alone. An ancestor's pause/resume or
terminal history remains incompatible. Missing correspondence stays pending;
unsupported content and policy conflicts cannot be forced through.

`ExperimentDecisionHistory` records changed outcomes or relevant evidence only:
the prior disposition, complete new decision/reason, native subject-event and
ancestor mapping references, and evaluation time. Clock changes and unchanged
checks add nothing; unrelated subject activity adds nothing. Meaningful evidence
changes are retained even if the disposition/reason remains the same.

`ExperimentAcknowledge(group)` retains the local statement "retained; not applied
here" separately for a conflict. Repeating it returns the original record. It
changes no source, decision, mapping, lifecycle, content, or dependency rule and
is never a shared resolution.

## Opt-in source-time activity and per-origin periods

The opt-in `readsurface.PreviewExperimentActivity` returns the combined activities
and source-reported periods. `Event.OccurredAt` and `Event.Actor` remain original
attribution. `ReceivedAt`, `LastRecordedEvaluationAt`, effect `RecordedAt`, and
any acknowledgement are separate local synchronization diagnostics. The last
recorded evaluation is the last *meaningful* decision/evidence change, not the
last unchanged check; it is not a last-seen or liveness timestamp.

Display order is reported time, origin, then original ID. Each known origin has
its own idle-gap grouping, with a split at gaps **greater than or equal to** the
threshold. Unknown origins remain unknown and get no inferred period. Discounted
superseded/reaped dispatch closes remain visible as activities but cannot create,
extend, or bridge a period. Integration, recheck, and acknowledgement records do
not count as work. This does not feed cross-origin events into native `Derive`.
Bounds are reported observations, not reliable duration or cross-host causality.

The follow-up tests print real preview JSON and before/after decision history:

```sh
nix develop --command go test -tags sharedstoreexperiment ./internal/store -run SharedStoreFollowup -count=1 -v
```

At 30 minutes, real Fluke work at 09:00 and 10:00 remains two periods despite a
09:30 integration effect; 29m59.999s remains one period and 30m splits. A Hybrid
start at 2026-12-01 09:00 imported on December 2 at 15:00 produces only day-one
reported work, while retaining both clocks. A real pending child example has
native event counts **9 -> 11 -> 12** (ancestor arrival then explicit recheck),
and decision history **pending -> integrated**. If ancestor effects already exist
but their correspondence is missing, counts are **11 -> 11 -> 12** instead.

The CLI-output parity test always compares ordinary human/JSON Session and status
before/after successful and failed lab operations. For comparison with a captured
first-experiment executable, additionally set `WIP_LAB_BASELINE_BINARY` to its path
in the Nix test environment; no such executable is a checked-in dependency.

## The necessary shared-core changes are small, but real

- `store.go`: SQLite `BEGIN IMMEDIATE` reserves the writer before transactional
  reads across independent handles/processes. `Commit` refreshes the persisted
  ID floor within that reservation. `NewID` advances the logical floor if the
  clock/handle is behind instead of waiting indefinitely for wall time.
- `project.go`: Rebuild reads its event snapshot inside its write transaction,
  not before acquiring the writer reservation.
- `envelope_test.go`: a default-build regression test opens two handles with
  asymmetric clocks. It fails on baseline `06ac992` with the monotonic-append
  constraint and passes with these changes.

There is no Open-to-Close lock and no lock held over tracker network calls.
Existing 5-second SQLite contention handling is unchanged. This does not move
every verb's pre-transaction checks into its transaction, coordinate arbitrary
concurrent maintenance/schema changes, or make a Store handle's external NewID
calls safe for arbitrary concurrent goroutines.

## Observable results

| Scenario | Checked result |
| --- | --- |
| Two same-baseline starts, exchanged in both directions | Both original activities retained; one native start per store; one native tracker candidate per store. |
| Missing-effect receipt | Exactly one fresh local transition; source ID/time kept separately; integration effect cannot be re-exported. |
| Different Steps under common Planned ancestors | Two common ancestor effects coalesced; each missing Step added; complete source group retained. |
| Retry and reopen | Same stored decision/effect mappings; no new transition or candidate. |
| Done, pause/resume, differing policy | Conflict without reopening or equating current InProgress with the old start. |
| Missing baseline/dependency, incomplete child evidence | Pending with no partial ancestor effects. |
| Competing create-once Brief | Explicit unsupported-family conflict retains incoming inline payload; accepted local bytes unchanged. |
| Injected failure on last cascade ledger row | Earlier ledger, native events, projections, and tracker candidates roll back together; reopen/retry succeeds. |
| Independent processes | Three companion imports and three ordinary CLI starts race one target; exactly one native start remains. |
| Companion absent or failed | Default CLI init/create works without one; create after failed import and pause/resume with an open companion handle work; repeated local start refuses. |
| Rebuild | Maintained nodes, content and outbox rows equal rebuilt rows in affected scenarios. |
| Foreign role provenance | Retained as source evidence; local effect uses fixed system actor; unspawned local role remains refused. |

## Limits remain explicit

Host labels are trusted fixture input, not authenticated origin credentials or
commit-time host capture. Pairing comes from closed-store copies with identical
Repo/node/tier identities; no existing-store adoption or foreign Clone/path
mapping is implemented. The input is a complete group from the exporter, not an
adversarial network protocol. No all-family dependency or structural merge is
claimed. Pending/conflict decisions are durable but this lab has no resolution
or automatic re-evaluation; explicit rechecks do not silently run on retries.
The original lab tables are not a general migration/upgrade contract for earlier
experiment databases. The CLI extension defines its own version-1 pairing state
below, without changing ordinary store migrations.

The inline content conflict preserves source bytes; spilled content is retained
only as its original unsupported payload/reference, not downloaded or made
offline-ready. No blob-transfer or cleanup coordination is implemented.

Source-time reporting is demonstrated only in the opt-in preview, not integrated
into Session/status. Native Session still sees the fresh local effect timestamp;
logical ID-floor advancement is not wall-clock correction. Host clock disagreement
and future reports are preserved, not corrected or used to authorize integration.
No follow-up change to default read surfaces or additional shared-core change was
needed beyond the first experiment's transaction/ID-floor/rebuild changes above.

Tracker candidate identity includes the **local event ID**. The two offline
starts produce different candidate keys even after lifecycle coalescing. This
experiment prevents another equivalent-import candidate on one host; it does
not provide cross-host once-only delivery or change existing approval/flush.

Linux subprocess checks establish this bounded write/import coordination, not
universal concurrency, crash-at-every-instruction recovery, Darwin behavior,
remote liveness or remote agent supervision. The separate bounded integration
below now exercises actual Laravel/REST exchange; earlier no-transport/identical
Repo fixture limitations describe the original experiment, not that extension.

## Bounded Laravel integration extension

Run against the matching `wip-cloud` integration checkout, with Composer
dependencies installed, PHP 8.5+ and PostgreSQL 17/client tools available:

```sh
WIP_CLOUD_PATH=/absolute/path/to/wip-cloud nix develop --command \
  go test -tags sharedstoreexperiment ./internal/store -run TestLaravelExchange -count=1 -v -timeout=3m
```

The Linux harness needs peer authentication on `/var/run/postgresql` as `$USER`
with create/drop database permission. It creates/removes only fresh
`wip_exchange_*` databases and temporary Git checkouts/stores, starts/stops real
PHP HTTP processes, and uses test-only fake/array mail. It sets its own app key,
DB settings and unused cache paths, overriding inherited DB URLs/config caches.
Cross-language cases explicitly skip when `WIP_CLOUD_PATH` is absent; a helper
case also skips in the parent and runs in fresh recovery subprocesses. No real
Hybrid/Fluke data, provider, mail, Cloud resource, or production database is used.

`BindExchange` explicitly associates a native Repo with the full server
`/api/organizations/{org}/repos/{repo}` endpoint and persists a per-store UUID
installation identity. Bindings cannot silently change. Two separately executed
ordinary `wip init` commands keep different native Repo/Clone IDs and paths;
neither binding nor server remote metadata changes Git configuration. Only the
business identities in an explicit seed are shared, not copied Repo lookup rows.

`ExchangePass` is the original explicit Go API, not a daemon. Its low-level
caller supplies committed original group IDs and an in-memory Sanctum token with
`foundation` ability. The version-1 envelope contains original native Repo,
installation and group IDs plus exact source JSON. The authenticated uploader and
server receipt time remain separate. Export snapshots are durable before HTTP,
so retries do not change earlier source evidence as correspondence grows. This
API does not persist tokens; the CLI keeps credentials outside the store.
HTTP happens outside every local writer transaction; redirects
are refused rather than forwarding bearer credentials.

When an observed baseline was a generated local effect, export retains that
observation as `NativeBaseline` (or ancestor `NativeEvent`) and supplies its
portable original source identity as `Baseline`/`Event`. Incoming original source
is immutable. `ExperimentImportBound` maps only the generated effect's Repo to
the chosen native Repo and resolves portable baseline correspondence. It never
rewrites retained source events or native Repo/Clone/path rows. New Matter object
IDs and inline Brief content IDs remain shared business identities; incompatible
identities, occupied addresses and competing Brief bytes are not merged.

Each downloaded committed page and its cursor commit together into the durable
inbox before classification. A failure during receipt rolls both back. A failure
during application leaves the cursor/inbox intact; `ApplyExchangeInbox` can finish
without a network connection after process restart. The inbox `applied` flag means
handed to the importer, including durable pending/conflict outcomes, not that a
business effect necessarily happened. Effects and source decisions commit together;
a process failing before the inbox mark safely retries the saved decision. Once
the incoming batch is classified, pending groups get one explicit recheck, not an
unbounded scheduler. Redelivery itself still returns the previous decision.

Tests first exchange supported seeded overlapping starts through real HTTP, then
exercise Matter/inline Brief creation, reverse-order dependencies, retained
conflicts, lost responses after server commit, PHP restart, fresh Go-process
offline recovery, post-effect/pre-inbox-mark failure, 100+3 committed pages,
tenant/credential/origin revocation and ordinary CLI/server-unavailable controls.
Existing tagged/race and ordinary CLI suites remain controls. This extends only
fresh-fixture Matter birth and inline create-once Brief integration; it does not
enable generalized families, blob transfer, existing-history adoption, a default
daemon or any production synchronization activation.

## Explicit pairing and one-pass CLI extension

The ordinary built binary now exposes these deliberately experimental commands
under `wip plumbing sync`. None runs at startup or changes ordinary local command
ownership. They are separate from the historical M6 IPC operation census; its
existing paths/contracts remain pinned, with these ten paths inventoried separately.

```sh
wip plumbing sync login --server https://your-laravel-origin --email you@example.com
wip plumbing sync repos --json
wip plumbing sync pair --org <organization-id> --server-repo <server-repo-id> --dry-run
wip plumbing sync pair --org <organization-id> --server-repo <server-repo-id>
wip plumbing sync status
wip plumbing sync run --dry-run
wip plumbing sync run
wip plumbing sync activity
wip plumbing sync recheck [group-id]
wip plumbing sync ack <canonical-conflict-group-id>
wip plumbing sync unpair
wip plumbing sync logout
```

Use `--repo <native-id-or-label>` instead of the current clone when needed. Pairing
requires explicit stable destination IDs and revalidates access with Laravel.
Discovery follows all organization/Repo pages, never remote/title matching.
Create destination Repos through the existing foundation API; this slice does
not add CLI Repo creation or a new creation retry mechanism.

Login uses Laravel's session/CSRF, verified-account login, password confirmation
and Sanctum token issuance. A terminal prompt hides the password; automation may
use `--password-file` pointing to an owner-private regular file (0600, no symlink).
Password/token values are not accepted as arguments or printed. The password is
not saved. One saved login is kept under the OS user config directory at
`wip/sync/credentials.json`, mode 0600 in a private 0700 directory, outside the
current checkout. Existing directory symlink ancestors and the checkout root
are resolved before checking containment, including missing directory suffixes;
the destination is rechecked before saving. A config symlink resolving outside
the checkout remains supported. Log out before replacing a login. Credentials
stay out of SQLite, source JSON, binding rows, Git and Repo metadata. Use HTTPS for nonlocal servers;
plain HTTP is used only by the disposable loopback tests. Redirects are refused
for both session authentication and bearer exchange. Logout revokes the saved
token and removes its file; an already-revoked token (HTTP 401) can also be
forgotten. A network failure does not pretend revocation succeeded.

**History scope is future-only.** Pairing inventories existing command groups and
pins their maximum correlation ID as the initial boundary. Older work is not
silently adopted, uploaded or merged, and pre-boundary dependency findings are
reported. Applying pairing commits installation, immutable destination, active
flag, boundary and local version together before origin-registration HTTP. A
lost registration response can be retried with that same identity/boundary.
Normal native Repo IDs, local clone/path associations and Git configuration are
unchanged. Lookup deliberately avoids ordinary opportunistic remote adoption.

`RunExchange` selects complete original commands in that Repo after the boundary,
not caller-supplied IDs. It captures every eligible exact envelope before HTTP,
uploads in local command order and acknowledges each group durably after the
server accepts it. A lost response resends the same cached key/bytes. Concurrent
new local work remains eligible for the next pass. Supported Matter births,
inline create-once Briefs and starts use the existing bounded importer; generated
effects and unrelated Repo/clone lookup work are never re-exported. Unsupported
families stay usable locally and appear in the unsupported inventory. Required
supported births/Brief baselines are included in command order when inside the
scope; old or unsupported dependencies are not silently synthesized.

Offline `status` separates pending upload, server retention, downloaded/evaluated
inbox entries, integrated groups, waiting groups and conflicts. Evaluation does
not mean a business effect was applied. A successful HTTP pass can still leave
conflicts or unsupported work; it is not a full-store-convergence claim. Both
dry-runs are read-only for sync state: no installation, registration, source
capture, checkpoint or effects. Both previews are offline: `pair --dry-run`
labels the destination as not revalidated until apply, avoiding even server
authentication bookkeeping writes. With explicit `--server` it needs no login.
`run --dry-run` explicitly does not predict unseen remote pages or their
decisions. Opening a store still has the ordinary schema
initialization/migration behavior; previews do not initialize opt-in sync tables.

`activity` scopes native work and retained original observations through the
target-Repo mapping, not by rewriting foreign event Repo IDs. Canonical conflicts
remain visible; collision groups are separate inspectable claims, never period
or canonical-attribution input. Source times are reported observations, not
duration or liveness. Recheck/ack address canonical groups only. `recheck` without
an ID also finishes durable inbox receipt offline after a failed/crashed apply;
it does not need credentials or a server. Both `run` and no-ID `recheck` reconsider
only canonical sources mapped to the selected Repo, never another Repo's waiting
work in the same store (including an unpaired Repo). Acknowledgement never resolves
a conflict, force-applies data, or changes accepted Brief bytes.

`unpair` disables future passes but keeps all work, evidence, outgoing bytes and
cursor state. Pairing the same association resumes it; it cannot silently switch
destinations or reuse a cursor elsewhere. Logout is separate and does not unpair
or erase local data. Local pairing persistence is version 1. Missing CLI tables
are initialized only on apply; an older low-level lab binding can opt in with its
existing endpoint/installation/cursor and a newly explicit future boundary. An
unknown CLI version refuses without rewriting it. No generalized real-history
upgrade/adoption guarantee is claimed.

Run the real built-CLI acceptance against Laravel and disposable PostgreSQL:

```sh
WIP_CLOUD_PATH=/absolute/path/to/wip-cloud nix develop --command \
  go test -tags sharedstoreexperiment ./internal/store -run TestLaravelExplicitSyncCLI -count=1 -v -timeout=3m
nix develop --command go test ./internal/cli ./internal/tiers -run 'TestSyncCredential|TestM6OperationCensus|TestAdopt_HappyPath' -count=1
```

These tests use actual ordinary auth and CLI subprocesses for pairing, automatic
selection, Matter/Brief creation, offline generated content, independent starts,
canonical Brief conflict/ack/recheck, unpair/resume, lost committed registration
and upload responses, concurrent local writing, server/process restart, importer
failure after durable receipt, offline recovery, and origin/token/membership
revocation. Existing API-level collision/dependency/clock/race regressions remain
additional controls, not a substitute for this CLI path. Protected-file and
redirect controls run without PHP in the ordinary CLI suite. Fixtures use
database-backed sessions so the multi-request authentication flow is real.

No background loop, real-store adoption, all-family sync, blob transfer, provider
delivery, remote execution, Cloud provisioning or deployment is added.

## Automated readiness acceptance

For fast, PHP-independent checks of the ordinary built CLI:

```sh
nix develop --command make test-sync-local
```

Without Nix, use `make test-sync-local` with Go, Git, Make and Python 3 on
Linux or macOS. Python's standard-library POSIX PTY drives the real binary's
terminal password branch; it is test tooling, not a runtime dependency. The
Nix development shell already supplies Python 3. No PTY Go module is added.
Each test uses disposable stores, checkouts and native OS config locations
(`XDG_CONFIG_HOME` on Linux; `HOME/Library/Application Support` on macOS).
No personal credentials or real stores are read.

The fast suite verifies offline previews do not create sync tables, protected
password input and redirect refusal, rejection of config ancestors resolving
inside the checkout, successful login/logout through safe directory aliases,
0600/0700 token-only persistence, credential-file symlink rejection before HTTP,
hidden Unicode password input and terminal restoration after success, rejected
password or HTTP failure, and refusal of nonterminal password input. Terminal
authentication uses dummy loopback HTTP responses, not a substitute for Laravel
auth acceptance. Ctrl-D cancellation is not asserted by this harness.

A second control retains a waiting child in unpaired Repo A and initializes
independent Repo B in the same disposable database. B's actual CLI recheck and
empty-feed HTTP run must not change A's decisions or native effects; naming A's
group while selecting B is rejected. Explicitly selecting A recovers exactly
once after the fake server and credential file are removed. The real Laravel
suite separately exercises this boundary with nonempty feed data.

For designated **real Go↔Laravel integration acceptance**, use:

```sh
WIP_CLOUD_PATH=/absolute/path/to/wip-cloud nix develop --command make test-sync-integration
```

This sets `WIP_EXCHANGE_REQUIRED=1`: absent `WIP_CLOUD_PATH` is a failure, not a
skip. Missing PHP, Composer vendor dependencies, PostgreSQL client tools/socket
or disposable database permissions also fail fixture setup. It executes the full
tagged store/readsurface suites, including the real built-CLI auth, pairing,
exchange, lost-response/restart, dependency, identity and tenancy checks.
Prerequisites remain PHP 8.5+, the integration Laravel source with `composer
install`, PostgreSQL and peer auth at `/var/run/postgresql` as `$USER` with
disposable database create/drop permission. The fixture always selects fresh
databases, test sessions and array mail, never an application's configured DB.

Ordinary optional developer runs without `WIP_CLOUD_PATH` retain their existing
skip behavior; their green result is not cross-language acceptance. Nothing here
changes normal local CLI capabilities or adds a network prerequisite to them.
Run the same fast target natively on each intended client platform; a Darwin
cross-build alone is not execution evidence. This harness does not qualify
real Hybrid/Fluke machines, hosted operation, existing-store adoption, all-family
sync, signal/EOF cancellation or production readiness.

### Joint repository CI

Both repositories carry `.github/workflows/sync-integration.yml`. A push or PR
tests that repository's exact triggering revision against the other repository's
matching `work/client-server-sync` branch when that is the triggering branch
(or PR head). Other branches test the counterpart's `main`. The selected ref is
always nonempty; a missing matching branch or inaccessible repository fails
checkout, with **no fallback to main**. Both resolved commit IDs and the required
ref are recorded in the job summary. Go runs on `main`, `wipd` and the sync
feature branch pushes, plus PRs to `main`/`wipd`; Laravel uses push/PR triggers.
Each job installs PHP 8.5 and locked Composer dependencies, checks the Laravel suite, then
runs `make test-sync-local test-sync-integration` in the Go Nix environment.
Missing Laravel prerequisites fail rather than skip. Existing CI and release
build/lint gates remain; the obsolete full-plan Step 10 acceptance and its private
Model/Workplan bootstrap are retired from Go CI and release checks. No exact-test
skip or `WIP_REBOOT_READ_TOKEN` is needed for ordinary Go checks. Independent
verifier/security and experimental-local regressions remain. The joint jobs do
not deploy, publish releases, or send mail.

Before enabling these jobs, configure the Actions secret `WIP_SYNC_READ_TOKEN`
in each repository with **read-only Contents access to the counterpart private
repository**. One fine-grained token may cover both repositories, or use separate
tokens scoped to each counterpart. The secret is passed only to its checkout
action; neither checkout persists credentials, and the tests receive no checkout
token. Missing access fails the job explicitly, including PRs without secrets.
No token or Actions setting is provisioned by this source change.

CI uses a disposable PostgreSQL 17 service with a shared local Unix socket,
local trust authentication and a test-only `runner` role. No database TCP port
is published. The fixture still creates/drops fresh `wip_exchange_*` databases;
the PHP suite uses only `laravel_foundation_test`, and mail is array-backed.
This socket setup is CI-only, not a production hosting configuration.

Coordinated feature publication needs both same-named branches, not a merge to
`main`. If a first job starts before its counterpart branch exists, it fails
explicitly; do not suppress the failure or use old `main` as compatibility
evidence. A rerun/dispatch needs its own authorization. Local paired-candidate
acceptance is not an already executed GitHub Actions run. Each run logs its
resolved counterpart revision; this is not an atomic two-repository release.

### Independent named connections and optional remote routes

Standalone commands still need no cloud configuration. Each connection names
one immutable HTTP(S) origin and server-issued account ID; multiple accounts
at the same origin have separate names/tokens. There is no global active login.
For disposable testing, or later against your own deployed server:

```sh
wip plumbing sync login --connection personal --server https://personal.example.invalid --email you@example.test
wip plumbing sync login --connection work --server https://work.example.invalid --email you@employer.example
wip plumbing sync connections
wip plumbing sync repos --connection work
wip plumbing sync pair --connection work --org '<organization-id>' --server-repo '<repo-id>' --dry-run
wip plumbing sync pair --connection work --org '<organization-id>' --server-repo '<repo-id>'
wip plumbing sync run
wip plumbing sync logout --connection work
```

Passwords use the existing hidden terminal prompt or protected `--password-file`,
never flags containing passwords. Login verifies `/api/me` before saving. Logout
revokes only the selected token, preserving the account pin, other logins and
Repo history. Unreachable revocation is an error, not successful logout. Log out
before reauthenticating the same name, and use the same origin/account. A different
identity requires a different name, not silently replacing a bound account.

Optional nonsecret configuration in `$XDG_CONFIG_HOME/wip/config.yaml` (existing
tool-config path) can select the initial connection:

```yaml
sync:
  connections:
    personal:
      server: https://personal.example.invalid
    work:
      server: https://work.example.invalid
  routes:
    - repo: github.com/**
      connection: personal
    - repo: github.com/Employer/**
      connection: work
    - repo: github.com/Employer/PersonalException
      connection: personal
    - repo: "**"
      connection: work
```

Your whole `sync` block replaces the shipped empty block; no deep merge is
implied. No token belongs here. Private credentials remain at the existing OS
config location: `$XDG_CONFIG_HOME/wip/sync/credentials.json` on Linux (default
`~/.config`), `~/Library/Application Support/wip/sync/credentials.json` on macOS.
The version-1 registry contains a `Connections` map whose entries contain
`Server`, `Account` and `Token`; a logged-out entry has no usable token. The
directory is 0700 and files are 0600, outside the checkout after resolving path
ancestors. A short local file lock and atomic replacement protect concurrent
mutations; no lock is held while prompting or doing HTTP.

`wip plumbing sync route` explains the native normalized identity key, matched
rule, connection/origin and saved credential presence offline (not credential
validity). Exact keys outrank the longest trailing `/**` prefix, then explicit
`**`; equally specific winning rules refuse rather than use file order. This
is not general globbing. Hosts are lowercase, paths case-sensitive; host/path
segment boundaries and nondefault ports matter. SSH/HTTPS normalization uses
the existing native identity rules. No implicit catch-all is installed.

Routes read the Repo's already selected/adopted `RemoteURL`, never scrape or
adopt a newly added remote. Remote-less/unadopted Repos need explicit selection,
even with `**`. An explicit initial `--connection` bypasses route selection.
With multiple choices and no route or explicit choice, setup refuses instead
of selecting the last login. Initial pairing still needs explicit organization
and server Repo IDs. No route pairs, uploads, guesses a Repo or changes Git.
The existing credential-free `pair --dry-run --server` remains supported.

Existing bindings always win over subsequent route/config/remote edits. They
retain connection/account, endpoint, installation, original history boundary,
cursor and acknowledgements. A conflicting connection/server flag refuses;
missing credentials do not prevent offline status/activity/recheck/dry-runs.
`route` shows a differing or invalid suggestion only as information. Run uses
the bound credentials and verifies the account before transfer, never another
connection as fallback. A work outage or logout does not disable personal sync
or ordinary local work. No automatic loop or all-connections operation exists.

Legacy `{Server,Token}` files are read virtually as `default`, without writes or
HTTP during ordinary commands, listing or previews. Explicit credential mutation
copies that token into the registry without rotation. An explicit online pass
establishes the legacy account at its retained origin and attaches metadata
transactionally without resetting transfer history. Existing raw correspondence
keys remain readable. New private correspondence keys are scoped by target Repo;
original source/native IDs and payloads remain untouched. Independent servers
reusing foreign IDs cannot share decisions, baselines, acknowledgements or
effects. Global native object/content ID reuse stays a conflict, not a merge or
automatic remapping; this is not generalized existing-history adoption.

`make test-sync-local` includes public routing/credential-race controls.
The strict integration target includes `TestLaravelNamedConnections` (two real
Laravel/PostgreSQL servers, shared native store, same-origin accounts, deliberately
repeated foreign IDs, independent logout/revocation/outage and retry/restart)
and `TestLaravelLegacyConnectionMigration` (legacy file/binding/correspondence
compatibility). These are disposable fixtures, not production activation.
