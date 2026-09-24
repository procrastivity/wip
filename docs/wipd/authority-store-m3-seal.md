# M3 authority-store conformance and seal evidence

M3 Steps 7–8 add durable continuity/migration proof records and verify the
authority-store boundaries against the sealed M2 contracts. This is a
store-package seal, not a claim that the M3 package implements a WIP daemon,
Environment store, network protocol, migration importer, or user-facing
operation router.

## Conformance disposition

The references below are the `V01`–`V22` entries in
`conformance-vectors.json`. “Store exercised” means the listed test drives the
real authority-store transaction API. “Partial” identifies the M3-owned
portion and the boundary that remains elsewhere; it is not an end-to-end pass
for the whole vector.

| Vector | M3 disposition | Authority-store evidence / remaining owner |
|---|---|---|
| V01 accepted command | Store exercised | `TestCommandDurableSubmissionTerminalAndReplay`, `TestSignedArtifactSealedGolden`; canonical receipt, event range, projection and replay. |
| V02 malformed / semantic rejection | Partial | Typed canonical validation and receipt-only no-effect paths are covered by command tests. Frame decoding and pre-submission malformed-record handling are M4 transport. |
| V03 valid guard refusal | Store exercised | `TestCommandConcurrentCoalescingAndNoEffect`, `TestClaimNoEffectProblemNamespaceBeforePersistence`; refusal commits no model effects. |
| V04 same-ID / same-hash retry | Store exercised | `TestCommandDurableSubmissionTerminalAndReplay`; pending coalescing, terminal replay, and reopen preserve one receipt/effect. |
| V05 same-ID / changed-hash | Store exercised | `TestCommandDurableSubmissionTerminalAndReplay`; conflict leaves the original submission unchanged and creates no receipt. |
| V06 definitely unsent | Partial | Cancel-before-admission leaves no store submission in `TestCommandDurableSubmissionTerminalAndReplay`; determining whether command bytes crossed a transport boundary is M4. |
| V07 lost-response receipt query | Store exercised | `TestCommandDurableSubmissionTerminalAndReplay`, `TestCommandProcessCrashBoundaries`; recovery queries/replays the durable terminal result without execution. |
| V08 environment read snapshot | Partial | `TestStep6SnapshotDeltaRaceRollbackAndReopen` exercises the authority's pinned as-of prefix. Environment overlay and provenance composition belong to M4. |
| V09 pinned pagination | Partial | Snapshot page stability and scope/expiry are exercised by Step 6 tests. The authority-store API uses an ordinal; query/filter-bound page-token MACs belong to the M4 query adapter. |
| V10 blob upload / integrity | Store exercised | `TestStep6SealedBlobVectorsAgainstSQLite`, `TestBlobFilesCrashBoundariesAndCollection`, and `TestBlobPromotionRepeatDigestKeepsFirstAnchor`. |
| V11 seed / claim pin readiness | Partial | Step 6 transfer and Step 5 grant tests exercise prefix/manifest identity, pin metadata, and durable grant query. The current M1 `matter.create@v1` declares no blobs; a future blob-bearing operation must derive its required-digest closure from its registered operation contract rather than trusting caller input. Hydration transport is M4. |
| V12 claim lifecycle | Partial | `TestClaimAcquireGrantReopenAndContention`, `TestClaimReleaseEmptySealedJournalReopenAndEpochFence`, and journal-repair tests exercise claim/grant/journal transactions. A registered production claim-return operation and its local install/ack adapter are not part of the current operation catalogue; do not expose journal primitives as that admission surface. |
| V13 claim contention / footprint refusal | Partial | One-Matter exclusion/contention is exercised by `TestClaimAcquireGrantReopenAndContention`. Static cross-Matter/aggregate footprint classification requires a concrete registered operation; none exists in the current catalogue. |
| V14 cross-Environment stand-down | Store exercised | `TestClaimStandDownCrossEnvironmentSignedScopeAndLoss`; exact owner proof, acting/owning Environment, loss acknowledgment, nonce use and close are checked. |
| V15 locator collision / v2 output | Outside current M3 API | The store retains `matter.create@v1` behavior and does not register `matter.create@v2`. Versioned collision success and repair are a later operation-adoption boundary. |
| V16 cursor move / provisional target | Outside current M3 API | Environment journal ordering, provisional birth dependency, and cursor semantics belong to M4 and the owning operation implementation. |
| V17 return refusal / repair | Partial | Durable append, pending-head fencing, proof-gated repair, quarantine and generation replacement are exercised by claim journal tests. Return scheduling, pull ordering, and production claim-delivery admission/install are M4 integration. |
| V18 compatibility / reseed | Outside current M3 API | M3 validates explicit SQLite schema upgrades; protocol negotiation, operation-version selection, Environment reseed and evidence-preserving base swap are separate M4 boundaries. |
| V19 peer / owner trust | Partial | Step 3 certificate-profile, owner-root, nonce and portable-artifact tests exercise persisted trust validation. OS peer credentials, live mTLS exchange, and transport refusal ordering are M4. |
| V20 handoff / restore | Store exercised | Step 7 tests cover quiescence, complete local closure, disaster-restore proof, monotone promotion history, epoch fencing and reopen. Bundle import into another running authority, ahead-Environment evidence handling and ceremony UX are outside M3. |
| V21 deterministic legacy migration | Partial | Step 7 validates and persists owner authorization, authority-signed migration proof, distinct owner seal, and the first-submission rollback fence. M6 owns source audit/grouping/import execution and production proof construction. |
| V22 observability / privacy | Outside current M3 API | The authority-store has no daemon logger, metrics, or human read surface. M4 and the owning product surfaces must implement redaction/provenance behavior; durable correctness retention is tested separately from logs. |

Step 8 also adds `TestStep8ClosedClaimIsIncludedInVerifiedHandoffBundle`. It
executes a Matter create, anonymous claim acquire, exact successful release,
handoff quiescence, bundle reopen verification, next-epoch activation, old
grant fencing, and a second reopen through the real store APIs. This checks a
cross-step closure that isolated Step 4/5/7 tests cannot establish on their own.

## Ownership and compatibility boundaries

- The implementation is confined to `internal/authoritystore`; it does not
  replace the existing local WIP store or add CLI, daemon, socket, HTTP, or
  tracker behavior.
- `operation.Catalogue` still contains only `matter.create@v1`. Claim lifecycle
  persistence and journal methods are internal store primitives, not a
  negotiated claim-delivery handler. Required blob closure and static
  footprint classification must be integrated with a concrete registered
  operation before such a handler is exposed.
- Step 7 records/verifies continuity and migration proofs. It does not run the
  M6 legacy importer or owner-ceremony workflow.
- Step 6 remediation changed the unsealed v4 `blob_chunks` layout. No v4-to-v4
  migration was added: the prior experimental layout is outside the supported
  fresh-store and explicit v3-to-current-v4 upgrade surface. A future release
  that persists that prior layout as supported data must add a separately
  versioned migration before changing its schema in place.
- The seal covers only the M3-owned portions above. It does not change or
  reinterpret any sealed M2 contract, schema, vector, or diagnostic byte.

## Verification record

From the repository root, the M3 authority-store checks are:

```sh
nix develop . --command make check
nix develop . --command go test -race ./internal/authoritystore -count=1
nix develop . --command go vet ./...
nix develop . --command bash -c 'test -z "$(gofumpt -l internal/authoritystore)"'
git diff --check
```

The M3 seal is accepted only with all commands passing on the exact integrated
worktree, plus review of this vector disposition and the M3 ownership limits.
