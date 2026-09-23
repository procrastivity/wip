# M3 Step 1: fresh authority-store foundation and transaction map

Status: design/contract-only starting point for `fresh-authority-store` Steps
2–8. The sealed M2 contracts and vectors remain authoritative; the API names
below are proposed internal boundaries, not new protocol messages or a table
schema. This step creates no authority state or production persistence code.

## Ownership and storage decision

Create a separate `internal/authoritystore` package and a separately supplied
authority data root. It owns domain truth, transactional admission, projection
folds, evidence ledgers, and read snapshots. It must not import
`internal/store`, `internal/writesurface`, Cobra, or transport packages. The M1
`internal/operation` catalogue supplies versioned semantic definitions and
typed outputs; M2 canonical identity supplies command bytes/hash, not a store
handle. A later adapter can bind an authenticated, negotiated principal and
operation evaluator to the authority-store API. The store rechecks domain,
epoch, membership, sequence, claim, and static footprint under its transaction;
an adapter's prior check is not authority evidence.

Use a **fresh SQLite database** under that root, with one domain-keyed schema
and a global uniqueness constraint for Repo membership, plus an immutable
content-addressed blob area. This is a candidate for Step 2 to prove, not a
mandate to reuse the legacy schema. One database permits atomic membership
exclusion across domains and promotion/proof records in the same durable
transaction as their domain state. Domain IDs scope every identity/foreign key
that can cross a domain boundary; `(domain_id, command_id)` is unique for the
domain lifetime, not per epoch. The legacy `internal/store` already uses
`database/sql` + `modernc.org/sqlite`, numbered transactional migrations,
single-connection writes, append-and-project transactions, and temporary-file
test stores. Those mechanics are useful precedents. Its host-local `wip.db`
path, JSON event shape, ULID-as-global-order assumption, `ErrNoEvent`, and
rebuildable-*everything* projection model are not: M2 has exact retained event
record bytes and prefix order, no-effect terminal receipts, and non-event
correctness evidence. No legacy `WIP_DB_PATH`, host-key path, local blob
sidecar, or automatic old-store migration is an authority opening path.

The authority root is explicitly supplied by the embedding service/setup;
ordinary open never discovers a legacy DB or implicitly bootstraps a domain.
`OpenExisting(root)` refuses a missing store, symlink/path escape, incomplete
initialization, or unsupported newer schema; `CreateEmpty(root)` requires an
absent dedicated root and creates only the baseline schema. The caller owns
process-level single-writer isolation and secure root permissions; the store
also obtains an OS-released exclusive writer lock or fails closed on a second
writable open, rather than assuming the M4 daemon singleton protects all
future embeddings. A stale PID file is not a lock. Read-only inspection must
not bypass authorization.
The exact filesystem root layout and lease implementation remain Step 2
details, tested against simultaneous opens and crash/reopen.

An explicit `BootstrapDomain` transaction takes a validated new domain ID,
immutable owner public-root identity, initial positive epoch, initial Repo
membership, and an authority artifact-key certificate/reference; it refuses
duplicate membership, nonempty prior lineage, or conflicting owner/epoch.
Private owner keys never enter this store. Authority signing keys are managed
through a separate restricted key facility/reference, not exported in bundles
or query results. Setup/authorization of those inputs belongs to the later
owner workflow, not `OpenExisting`. On reopen, verify schema, domain/epoch and
owner invariants, active artifact-key/fence and chain continuity, and prefix
anchor before admitting writes; do not silently repair or rebuild missing
correctness evidence. Recovery resumes only a retained submission with its
original ID/hash. Step 2 must implement this minimum bootstrap boundary
before any operation adoption.

Maintain independent version registers: internal numbered forward-only SQL
migrations (baseline v1, atomic version row and data change, refuse downgrade),
projection derivation version, and the protocol-advertised `wipd.store/1` base
schema. Neither SQL nor projection versions are protocol, operation, or
`wipd.command/1` identity versions. A schema change affecting transferable
base bytes/meaning requires an explicit compatible `wipd.store/N` plan, not a
silent SQL bump. Fresh open applies the baseline; existing-store upgrades
require an explicit backup/upgrade entry point with recovery validation, not
automatic migration during ordinary open. No legacy-store import is implied.

## Isolation and scheduling

All mutations, prefix reads/snapshot acquisition, grant pinning, return/fold,
and activation for one domain enter one per-domain serialization lane. Within
it, a SQL transaction reads guards and current epoch, commits a complete
transition, then releases the lane. Snapshot readers use a pinned immutable
view/anchor; subsequent writes cannot change item set, order, or provenance.
SQLite may serialize writers across *different* domains too; M3 promises
correctness, not cross-domain write throughput. Cross-domain Repo attachment
and domain creation also take a registry/global admission lock before the
domain lane, in one ordering, so unique membership cannot race. Never hold a
domain lane across network transfer, hydration, or a caller's wait; pin the
product/anchor durably, release, and resume by exact ID/hash/anchor. The M4
daemon schedules local journal-before-pull/write and local base installation
in its own lane; it does not open this DB. This authority lane checks one
returned command at a time and never turns an unavailable authority command
into a future queue. Cancellation before submission leaves no submission;
after its durable commit it only ends the wait, not the execution owner.

Transaction ownership is explicit: M3 owns authority-side submission, return
acknowledgments, signed products, and claim/registry/proof state. M4 owns
Environment sequence allocation, immutable pending/quarantined journals,
overlay, receipt/delta/manifest atomic local install, pins, and evidence-safe
reseed. The authority validates Environment sequence and contiguous returned
head against retained authority acknowledgments; it does not invent or rewrite
the Environment's local journal. `acted_at` and Environment sequence are
narration/local order, never authority fold order. A cursor depending on a
provisional birth remains in the Worktree Environment journal and cannot pass
that birth's receipt, not in a mixed provisional stream.

## Proposed typed authority transaction surface

Names below describe internal typed inputs/results. No arbitrary caller SQL,
generic `Commit(func(*sql.Tx))`, mutable receipt builder, raw transport frame,
or `operation.Result` alone may make a durable transition. Each method accepts
an authenticated domain/Environment scope where applicable, explicit expected
epoch, and validated typed proof/identity; it revalidates inside the lane and
returns exact persisted evidence. `outcome-unknown` is a caller inference after
an ambiguous response, never a stored terminal `result.failed`.

| M3 step / boundary | Typed internal operation and one durable commit |
|---|---|
| 2: identity and membership | `BootstrapDomain`, `AttachRepo`: check immutable owner and exclusive Repo membership; no eventful domain accepts implicit membership changes. Persist active epoch and promotion fence fields here; Step 7 alone implements next-epoch activation after full proof verification. |
| 3: enrollment and registries | `IssueEnvironmentCertificate(grant, csrDigest, certificate)`: verify scope, SPKI, epoch, one-use nonce/grant and PKCS#10 proof; commit consumption, Environment generation, exact issued certificate and retry mapping together. Same grant/CSR returns identical bytes, different CSR refuses. `RenewOrRotate` commits new generation before old revocation. Environment sequence registry checks the delivery-specific contiguous head with submission/return acknowledgment, not by advancing on an attempted request. `AppendSignedArtifact` commits exact wrapper, per-generation sequence/predecessor and new chain head under serialization; key fences are retained. |
| 4: submission and terminal | `SubmitCommand(canonicalCommand, assertedHash, authenticatedScope)`: decode closed identity, recompute hash, check supported operation/metadata, active epoch, Environment, claim and prerequisites; atomically insert immutable `(domain, command_id, hash, bytes, submitted, execution owner)`. This commit alone is the Step 5 submission point. Same ID/hash attaches or returns exact terminal evidence; conflicting hash has no effect/disclosure. `CompleteCommand(submissionID, typedDecision)`: only the durable owner may atomically append contiguous exact event bytes, advance prefix, fold projections, promote named verified blobs, persist typed output/range and terminal receipt, sign its portable wrapper and advance artifact chain. For a returned command, the same commit records its exact journal generation/position, ID/hash and terminal barrier facts; no later independent acknowledgment can let the next head pass an unrecorded result. A non-success or statically declared deterministic no-op commits no model/blob effect and a null range. A failed DB/signing step leaves submission pending, not a fabricated terminal receipt; reopen must recover the unique owner without double execution. |
| 5: claim lifecycle and return | `CompleteAcquire`: within the Step 5 successful terminal commit, exclude a second Matter claim/open Dispatch, allocate strictly increasing Matter claim epoch/new ID, reuse/create its one-Matter anonymous Batch, append lifecycle events and receipt. `PinGrant`: after successful acquisition, durably bind one grant ID to its exact receipt, tail end anchor and complete manifest; sign grant/manifest artifacts with their chain advances before returning any grant product. Retry retrieves the same pin. Returned heads admit only the next eligible contiguous position; non-success stops the suffix, and explicit repair records proof without rewriting old entries. `CompleteRepair`, `CompleteRelease`, `CompleteStandDown` enforce exact claim epoch, receipt barrier or owner proof/nonce respectively in their successful terminal transactions; stand-down consumes nonce with loss/close event and digest-only permanent reason. |
| 6: prefix/blob/snapshot | `StageBlobChunk` retains exact contiguous offset, length and verified end; staged bytes are not model truth. `PinSnapshot` fixes as-of prefix and complete manifest for stable pages/transfer tokens. `PublishManifest` binds sorted digest/length/requirement entries to the exact pinned anchor. `CompleteCommand` alone promotes referenced verified blobs on effectful success. `ExportTransfer` exposes one complete pinned prefix/manifest and proof closure; temporary partial segments never become installed state. Environment shadow-base install/reseed belongs to M4. |
| 7: continuity/proof | `QuiesceAndRelinquish`, `ActivateVerifiedBundle`: verify exact source prefix, blob/receipt/registry/claim/artifact closure and signed owner intent/nonce, then durably consume the appropriate nonce, fence old epoch/trust and install the complete next-epoch state with signed activation/chain head. No activation on intent alone or divergent/ahead history. `RecordMigrationProof`, then `AcceptMigrationSeal`: persist the signed proof first; verify the distinct owner seal binds that exact proof and prior authorization before ordinary admission. A migration proof is distinct from ordinary command receipts; first post-migration submission commits the irreversible rollback fence with that submission. M6 builds/imports legacy groups and supplies verified proofs; M3 only persists/checks the fence and artifacts. |

The Step 5 submission and terminal commits are deliberately **two**
transactions. Claim acquire, repair, release, and stand-down cannot bypass
them via convenience mutation methods. Migration-specific imported groups and
the synthetic binding use their own sealed proof boundary, not invented M1
terminal receipts. Signed
artifacts that belong to a terminal transition are prepared/validated before
commit and their exact bytes/chain advance land inside it; the separate grant
pin is a recoverable post-receipt obligation, never an assumption that a lost
grant response undid acquisition. Signing may be performed under the lane or
by a reserved, durably recoverable sequence; Step 4 must prove that a crash
cannot expose a signed product not linked to its committed chain or reuse a
sequence. Owner nonce consumption must be in the authorized transition, not
in a prior preflight. Every transition binds domain and epoch from stored
state, not a caller-supplied profile, actor string, certificate label, or
client-side projection.

## Contract trace for implementation review

| Boundary | Normative source / schema and distinguishing invariant |
|---|---|
| Bootstrap, membership, version | `authority-protocol-traceability.md` D115–D118; `version-compatibility-contract.md` §§1–4 (`wipd.store/1` is not the SQL version); `design-security-privacy-review.md` §8. |
| Admission, terminal, signing | `canonical-semantic-identity.md` §§2–5; `result-receipt-idempotency-contract.md` §§2–5; `conformance-schemas.cddl` `command`, `terminal-receipt`, `signed-artifact`. Submission is separate from terminal and has one durable owner; null success range only under Step 9's declared no-op exception. |
| Enrollment and owner trust | `frame-security-execution-contract.md` §2.3; `design-security-privacy-review.md` §§2–4; CDDL `authority-artifact-key`, `authority-key-fence`, `owner-attestation`, `enrollment-grant`. The owner signs authority keys/proofs; TLS/Environment keys cannot sign portable receipts. |
| Claims, return and repair | `claim-lifecycle-contract.md` §§2–9; `read-transfer-snapshot-contract.md` §6; CDDL `claim-grant-start`, `claim-grant-end`, `journal-barrier`, `fold-result`. Acquisition terminality precedes recoverable grant pin/transfer; local journal installation remains M4. |
| Prefix, blobs, snapshots | `read-transfer-snapshot-contract.md` §§1–8; CDDL `prefix-anchor`, `blob-manifest`, `snapshot`, `page-token-claims`, `seed-start`, `fold-result`. Event bytes and complete prefix/manifest anchor are canonical; cache/partial transfer are not truth. Step 6 defines the `wipd.transfer-token/1` binding and expiry in prose, without a standalone CDDL production for its claims; Step 6 implementation must retain that exact scope rather than treating an opaque token as a free offset. |
| Continuity and migration | `design-security-privacy-review.md` §§3, 7–8; `read-transfer-snapshot-contract.md` §9; CDDL `bundle-manifest`, `authority-relinquishment`, `authority-activation`, `migration-proof`. Owner intent, source relinquishment, owner final attestation, activation and migration seal have different proof/commit points. |

## Failure, reopen, race, and conformance plan

Use `t.TempDir()` isolated roots and real SQLite transactions, plus injection
points **before submission commit**, **after submission commit**, at each
terminal event/projection/blob/receipt/signing write, **before/after terminal
commit**, grant pin/chain advance, enrollment consumption/certificate issue,
owner nonce/activation, and migration fence commit. Fail or process-stop at
each point; close and reopen the same store, not a mock transaction. Compare
exact receipt/wrapper bytes, ID/hash, prefix count/digest/high-water, projected
state, blob references, claim epoch, nonce/registry/chain heads and pending
owner. Before-commit faults leave no partial effect; after-commit response loss
must replay the same durable product. Inject signing failure to require a
pending submission and no half receipt. Test crash after staged blob file
durability but before DB reference: orphan is collectable, never visible.

For blobs larger than a DB page, write into an untrusted temporary area;
verify full length and digest, fsync immutable content-addressed bytes and
directory before any DB transaction references them. A DB rollback may leave
an unreferenced file; GC never deletes a referenced byte range, unresolved
staged evidence, or pinned transfer product. SQLite WAL durability and sync
settings must be exercised by reopen/process-kill tests; an in-memory mock
does not establish the crash boundary.

Race two openings of one root, same-ID/same-hash submits, same-ID/different-hash
submits, two Environment sequences, competing claims on one Matter,
grant/enrollment nonce reuses, append/sign/rotation, snapshot pagination versus
write, and promotion versus old-epoch submission. Assert **one** execution
owner, receipt, open claim, consumption and canonical chain successor; no
cross-domain membership duplicate. Test both schedules around the commit,
not just eventual counts. A process crash after a submission with an
unresolved nontransactional external effect must block replay/adoption until
that operation has a command-ID-idempotent effect protocol.

Step 8 exercises the eight scripts and V01–V22 in
`conformance-vectors.json` against real transaction APIs: accepted/refused
commands, same-ID retry, response-lost receipt query, pinned read, verified
blob upload, claim cycle, and incompatible-version/evidence-preserving reseed.
Add Step 9 portable-signature golden bytes and negative altered-chain/nonce
cases. Tests derive expected anchors, ranges, digest chains and no-effect
states from the published vectors/independent decoder, not by asking the
store to compute its own expected value. No production transport is necessary
to exercise the store API.

## Decisions to prove in Steps 2–4

- **Durability/lease:** a single SQLite file favors cross-domain membership
  exclusion and transaction closure, but global writer contention and a
  process-level lease must be measured/proven. If this is unacceptable, a
  split-domain design needs a separately transactional global membership
  registry and promotion recovery protocol before it can replace this choice.
- **Signed commit:** determine whether signing while holding the domain lane
  is viable; if not, specify a durable reservation/recovery protocol with no
  externally visible uncommitted wrapper or forked chain. Reopen verification
  must include historical owner-certified fences, not only the current head.
- **Execution recovery:** a persistent `submitted` row is not permission for
  two evaluators. Step 4 must define how one owner resumes after a crash and
  restrict advertised handlers to transactional effects or command-ID-safe
  external effects. No fabricated `result.failed` for storage failure.
- **Large-product closure:** prove blob fsync/DB reference/GC ordering and
  stable snapshot/manifest retention under space pressure and long transfers.
  Bound live pinned resources by the Step 4/6 limits, without deleting domain
  truth or correctness evidence.

This maps the M3 workplan's Steps 2–7 onto store primitives; Step 8 seals them
with faults and conformance. M4 supplies daemon, peer authentication,
transport, scheduling and local evidence store; M5+ adopt operations; M6
executes migration; later owner-ceremony UX obtains attested approval. None
is silently supplied by these proposed methods.
