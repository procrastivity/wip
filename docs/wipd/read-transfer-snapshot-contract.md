# M2 Step 6: reads, transfer, and snapshots

Status: normative protocol design over `wipd.command/1` and M2 Steps 3–5.
This contract closes Q09–Q14 and the Step 6 portions of Q15, Q16, Q22, Q24,
and Q25. It defines logical messages and integrity/ordering rules, not a wire
encoding, daemon, authority store, or production transfer implementation. The
key words **MUST**, **MUST NOT**, **SHOULD**, and **MAY** are normative.

## 1. Common identity, anchors, and integrity

Every message in this contract runs after Step 3 negotiation on a Step 4
authenticated exchange. Its `domain_id` and `authority_epoch` MUST equal the
authenticated active domain and epoch. A route, endpoint, profile, path,
semantic actor, or token claim cannot replace that check. Control messages fit
one `wipd.frame/1`; record and blob bytes use Step 4's ordered chunks and
negotiated flow control.

The authority prefix is an ordered sequence of the authority's exact retained
event-record bytes. This contract does not reinterpret or replace the event
schema. An Environment installs those bytes unchanged and folds projections
from them. Journal commands, provisional overlays, receipts, and claim metadata
never enter the local authority-event table. An event position is the closed
map:

```text
PrefixAnchor {
  "event_count": uint64,
  "high_water_event_id": canonical event ULID or null,
  "prefix_digest": canonical sha256 text
}
```

The empty anchor has count zero, null high-water, and:

```text
P0 = SHA-256(UTF8("wipd/event-prefix/v1") || 00)
```

For event bytes `E_i`, let `L_i` be their unsigned 64-bit big-endian byte
length. The cumulative digest is:

```text
P_i = SHA-256(UTF8("wipd/event-prefix-step/v1") || 00 ||
             P_(i-1) || L_i || E_i)
```

`prefix_digest` is `sha256:` plus lowercase hex of `P_i`. The event's own ID
inside the validated record MUST equal the transfer record's event ID; event
IDs strictly increase in authority fold order. Thus a receiver can extend an
installed anchor without possessing a second interpretation of event bytes.
An event count, ID, digest, or start-anchor disagreement is
`transfer.prefix-mismatch`; no partial prefix becomes visible.

A `PrefixDelta` is a start anchor, an end anchor, and every event record after
the start through the end, in fold order. It is complete: omission, duplicate,
reorder, or bytes after the end is invalid. A seed uses the empty start anchor.
A pull or fold starts at the receiver's exact installed anchor. The authority
MUST NOT describe a sparse selection as a prefix or delta.

All protocol-1 content digests use Step 2's raw-byte SHA-256 spelling. Length
and digest are checked independently. Hash agreement authenticates nothing;
Step 4 authentication and the domain/epoch checks remain mandatory.

## 2. Blob manifests

A blob manifest is snapshot-bound and contains sorted unique entries:

```text
BlobManifest {
  "schema": "wipd.blob-manifest/1",
  "domain_id": canonical domain ULID,
  "authority_epoch": positive uint64,
  "as_of": PrefixAnchor,
  "entries": sorted unique [BlobManifestEntry],
  "manifest_digest": canonical sha256 text
}

BlobManifestEntry {
  "digest": canonical sha256 text,
  "byte_length": uint64,
  "requirement": lazy | pin-before-use
}
```

Entries sort by the 32 raw digest bytes. Duplicate digests, including a digest
with another length or requirement, are malformed. The manifest is complete
for all blobs referenced by the transferred prefix and snapshot-visible
projection state. `pin-before-use` is used when the owning contract requires
complete local bytes before offline execution, including Step 7 claim grants;
all other entries are `lazy`. A manifest says the authority has verified the
full bytes. It does not say an Environment cache is hydrated.

The manifest digest is a chain. `M0` is SHA-256 of
`"wipd/blob-manifest/v1" || NUL`. For each sorted entry, `D_i` is the raw
32-byte digest, `L_i` is its big-endian uint64 length, and `R_i` is one byte
(`00` for `lazy`, `01` for `pin-before-use`):

```text
M_i = SHA-256(UTF8("wipd/blob-manifest-step/v1") || 00 ||
             M_(i-1) || D_i || L_i || R_i)
```

The final `manifest_digest` is `sha256:` plus lowercase hex of `M_i`. This
digest, the snapshot anchor, domain, and epoch are all carried by the enclosing
transfer; a manifest copied between snapshots or domains is rejected.

## 3. Stable reads and provenance

Each advertised query version MUST define a closed deterministic filter schema,
a closed item schema, and one total order ending in an immutable identity
tiebreaker. Locale, map iteration, wall-clock arrival, presentation text, and
mutable labels alone are forbidden ordering keys. A query without that
definition cannot be advertised.

The deterministic filter hash is:

```text
filter_hash = "sha256:" + lowercase_hex(
  SHA-256(UTF8("wipd/query-filter/v1") || 00 || deterministic_filter_cbor)
)
```

The Step 4 `QueryRequest.query_payload` decodes to:

```text
ReadQuery {
  "schema": "wipd.read-query/1",
  "domain_id": canonical domain ULID,
  "expected_epoch": positive uint64,
  "source": authority | environment,
  "filter": query-version-specific deterministic map,
  "overlay_policy": folded-only | folded-and-provisional |
                    all-local-evidence,
  "page_size": uint16,
  "page_token": opaque text or null
}
```

`authority` permits only `folded-only`. `environment` may request any policy.
`folded-and-provisional` adds active provisional items;
`all-local-evidence` also exposes quarantined items, never as folded truth. A
continuation repeats the same query name/version and all fields except
`page_size`; its page token is authoritative for the original page size and
bindings. A changed source, filter, overlay policy, domain, epoch, or query is
`query.page-token-scope`.

The first page acquires one immutable snapshot from the Step 4 per-domain lane.
It returns:

```text
ReadResponse {
  "schema": "wipd.read-response/1",
  "query": {"name": lowercase dotted token, "version": positive uint16},
  "filter_hash": canonical sha256 text,
  "snapshot": Snapshot,
  "provenance": Provenance,
  "items": ordered [ReadItem],
  "next_page_token": opaque text or null,
  "complete": boolean
}

Snapshot {
  "id": canonical ULID,
  "domain_id": canonical domain ULID,
  "authority_epoch": positive uint64,
  "as_of": PrefixAnchor,
  "overlay_policy": ReadQuery.overlay_policy,
  "expires_at": canonical UTC RFC3339Nano text
}

Provenance {
  "source": authority | environment,
  "authority_reachability": reachable | unavailable | not-attempted,
  "authority_as_of": PrefixAnchor or null,
  "local_base_as_of": PrefixAnchor or null,
  "pending_journal_count": uint64,
  "provisional_item_count": uint64,
  "quarantined_item_count": uint64,
  "history_state": current | behind | history-regressed
}

ReadItem {
  "value": query-version-specific deterministic CBOR byte string,
  "state": folded | provisional | quarantined,
  "environment_sequence": positive uint64 or null
}
```

An authority response has reachable authority provenance, matching
`authority_as_of` and snapshot `as_of`, null `local_base_as_of`, zero local
journal/overlay counts, and only folded items. An Environment snapshot `as_of`
equals its installed `local_base_as_of`. `authority_as_of` is the last anchor
actually authenticated during this read, or null; it is never guessed from a
route, cache age, metric, or failed probe. Provisional items carry their durable
Environment sequence. Quarantined items remain explicitly quarantined. The
response metadata is mandatory even for an empty result.

Writes, pulls, journal changes, and hydration after snapshot acquisition do not
change its item set, order, anchor, or provenance. Cancellation releases the
snapshot and returns `query.cancelled`; it changes no emitted metadata.

D124 narration is one explicitly ordered query family. It preserves each
Environment's durable sequence. Among the next eligible command from each
Environment it orders by `acted_at`, then command ID; events within a command
remain in authority fold order. Clock regression is exposed as a diagnostic
item flag and never rewrites either per-Environment sequence or authority
truth. Ordinary authority-history queries remain in event fold order.

## 4. Pagination tokens

A page token is an opaque capability with logical claims:

```text
PageTokenClaims {
  "schema": "wipd.page-token/1",
  "issuer": authority | environment,
  "domain_id": canonical domain ULID,
  "authority_epoch": positive uint64,
  "query_name": lowercase dotted token,
  "query_version": positive uint16,
  "filter_hash": canonical sha256 text,
  "snapshot_id": canonical ULID,
  "as_of_event_count": uint64,
  "as_of_event_id": canonical event ULID or null,
  "as_of_prefix_digest": canonical sha256 text,
  "overlay_policy": ReadQuery.overlay_policy,
  "page_size": uint16,
  "cursor": deterministic query-version-specific CBOR byte string,
  "expires_at": canonical UTC RFC3339Nano text
}
```

Protocol 1 represents it as `pt1.` plus unpadded base64url of deterministic
CBOR claims, a dot, and unpadded base64url HMAC-SHA-256 over those exact claim
bytes. The issuer uses a private, purpose-specific rotating key; keys and raw
claims are never logged or returned separately. A local issuer and authority
do not share keys. Verification uses the authenticated request's domain/epoch,
recomputes the filter hash, checks every scope field and expiry in constant
time where applicable, and reacquires the exact live snapshot ID.

Tamper or wrong issuer is `query.invalid-page-token`; valid authentication with
a scope mismatch is `query.page-token-scope`; expiry or a released snapshot is
`query.snapshot-expired`. None silently starts a new snapshot. A client starts
a changed query explicitly with a null token. Default page size is 100 and the
maximum is 1,000. Snapshot lifetime defaults to five minutes and MUST NOT
exceed fifteen minutes; every continuation token expires no later than its
snapshot. Page boundaries never duplicate or omit an item from the pinned
ordered result, even if authority state changes between pages.

## 5. Seed and resumable prefix installation

An Environment requests a base with:

```text
SeedRequest {
  "schema": "wipd.seed-request/1",
  "domain_id": canonical domain ULID,
  "expected_epoch": positive uint64,
  "store_schema": negotiated StoreSchemaID,
  "resume_token": opaque text or null
}
```

The response order is fixed:

```text
SeedStart {
  "schema": "wipd.seed-start/1",
  "transfer_id": canonical ULID,
  "domain_id": canonical domain ULID,
  "authority_epoch": positive uint64,
  "store_schema": StoreSchemaID,
  "snapshot_id": canonical ULID,
  "prefix": {"start": empty PrefixAnchor, "end": PrefixAnchor},
  "event_count": uint64,
  "event_byte_length": uint64,
  "blob_manifest_digest": canonical sha256 text
}
event.record × event_count, in authority order
BlobManifest
SeedEnd {
  "schema": "wipd.seed-end/1",
  "transfer_id": same ULID,
  "verified_prefix": SeedStart.prefix.end,
  "verified_blob_manifest_digest": SeedStart.blob_manifest_digest,
  "complete": true
}
```

`SeedStart` pins one authority snapshot. The transfer starts at genesis and
contains every event through `prefix.end`; `event_count` and total raw event
byte length MUST agree. The manifest is complete at that same anchor. Events
and manifest are verified into a shadow base. Only `SeedEnd` agreement permits
one atomic installation of the prefix and derived projections. A cancelled,
expired, malformed, truncated, mismatched, or over-limit seed leaves the old
base visible and the shadow disposable.

Large seed, pull, manifest, and blob exchanges are resumable only at a verified
record/chunk boundary. The issuer's protected transfer token binds transfer
kind, transfer ID, domain, epoch, snapshot ID/anchor, manifest digest, next
ordinal/offset, negotiated store schema, and expiry. Resume repeats the last
verified boundary at most; the receiver compares and discards that duplicate.
It never splices transfers, changes snapshots, or trusts a caller-supplied
offset. Transfer tokens use the same authenticated deterministic-CBOR/HMAC
construction and error boundary as page tokens with schema
`wipd.transfer-token/1`; their maximum lifetime is fifteen minutes.

`event_count` and `event_byte_length` in `SeedStart` describe the complete
pinned product. Each HTTP/2 exchange additionally declares one bounded segment
with its own start/end anchors, event count, byte length, and final flag. The
first segment starts at the empty anchor; each later segment starts at the
prior verified end; only the final segment reaches `SeedStart.prefix.end`.
Nonfinal segments end with a protected transfer token, not `SeedEnd`. Manifest
entries are likewise segmented only at sorted entry boundaries; the complete
manifest digest is checked after the final entry. No segment's declared bytes
may exceed Step 4's negotiated stream limit, and no segment or partial manifest
is installed visibly.

## 6. Pull, return, and fold ordering

Pull, return/fold, authority writes, and base installation serialize in the
Step 4 per-domain lane. At reachable command start the Environment determines
whether any journal head is eligible under its owning delivery/claim contract.
If one is eligible, it MUST return pending work before pulling or admitting the
new command. Across eligible heads it chooses the lowest durable
`environment.sequence`; within a journal it never skips a position. A capture
journal contributes exactly one entry at a time. Authority commands are never
journaled for this process.

### Pull with no eligible pending head

```text
PullRequest {
  "schema": "wipd.pull-request/1",
  "domain_id": canonical domain ULID,
  "expected_epoch": positive uint64,
  "installed": PrefixAnchor,
  "resume_token": opaque text or null
}

PullStart {
  "schema": "wipd.pull-start/1",
  "transfer_id": canonical ULID,
  "domain_id": canonical domain ULID,
  "authority_epoch": positive uint64,
  "prefix": {"start": exact installed anchor, "end": pinned authority anchor},
  "event_count": uint64,
  "event_byte_length": uint64,
  "blob_manifest_digest": canonical sha256 text
}
event.record × event_count
BlobManifest
PullEnd {transfer_id, verified_prefix, verified_blob_manifest_digest,
         complete: true}
```

An empty delta is valid and preserves the anchor. The receiver verifies and
atomically installs the complete delta and manifest before rebuilding its
overlay. A start anchor not in the authority lineage returns
`store.history-regressed` or `transfer.prefix-mismatch`; local journals,
receipts, staged blobs, and evidence remain untouched.

### One returned command transaction

Step 7 owns journal creation, eligibility, claim state, and repair
authorization. Step 6 fixes the transfer integration map:

```text
ReturnCommand {
  "schema": "wipd.return-command/1",
  "domain_id": canonical domain ULID,
  "expected_epoch": positive uint64,
  "journal": {
    "id": canonical ULID,
    "delivery": claim | provisional | capture | environment,
    "position": positive uint64,
    "claim_id": canonical ULID or null,
    "claim_epoch": positive uint64 or null
  },
  "base_as_of": exact installed PrefixAnchor,
  "canonical_command": exact `wipd.command/1` bytes,
  "request_hash": recomputed canonical sha256 text
}
```

The journal position and Environment sequence are contiguous under the owning
journal contract. Claim ID and epoch are both non-null only for claim delivery
and MUST equal the immutable command. The authority applies Step 5 validation,
same-ID/hash lookup, submission point, and terminal transaction separately to
this one command. The response is:

```text
FoldResult {
  "schema": "wipd.fold-result/1",
  "journal_id": ReturnCommand.journal.id,
  "journal_position": ReturnCommand.journal.position,
  "receipt": exact `wipd.terminal-receipt/1`,
  "prefix_delta": complete PrefixDelta from base_as_of through fold_as_of,
  "blob_manifest": complete BlobManifest at fold_as_of,
  "fold_as_of": prefix_delta.end,
  "continue": boolean
}
```

The delta includes every intervening authority event from other commands and,
for a newly completed success, the receipt's exact accepted range. On receipt
replay, that range either appears in the delta or already exists and validates
in the installed prefix; it MUST be in the lineage at or before `fold_as_of`.
A non-success receipt has no accepted events as Step 5 requires, but the delta
may still carry preceding concurrent authority events. The Environment
atomically installs the delta, manifest, and exact terminal receipt before
removing that command from the active overlay or considering another head. It
then rebuilds the overlay.

`continue` is true only for `result.succeeded` and only when the next immutable
journal entry is independently eligible. On `result.rejected`,
`result.refused`, or `result.failed`, it is false: return stops at that head,
and the head plus dependent suffix become quarantined local evidence. Pull,
suffix return, and a new dependent write remain blocked. Step 7 may authorize
an explicit abandon or a replacement, but replacement is a new immutable
`wipd.command/1` with a new command ID/hash and revalidated claim/guards. This
contract defines no repair control message and never permits skipping the
head. A receipt replay still goes through the same prefix/receipt installation
check and does not execute again.

This ordering also fixes D125's integration point: an Environment cursor
command targeting a provisional object follows that object's birth in the
same provisional stream by Environment sequence. It cannot be returned,
folded, or read as folded before the birth receipt and event range install.

## 7. Blob upload, cache promotion, and hydration

Blob upload is content-addressed within the authenticated domain:

```text
BlobUploadStart {
  "schema": "wipd.blob-upload/1",
  "domain_id": canonical domain ULID,
  "digest": canonical sha256 text,
  "byte_length": uint64,
  "resume_offset": uint64
}
```

The authority replies with `blob.available` and accepts no bytes when it
already has verified bytes for that digest and exact length. If the digest is
known with a different length it returns `blob.length-mismatch`. Otherwise it
returns the exact contiguous verified temporary offset. The client MUST use
that offset; upload chunks follow Step 4 ordering. At end the authority checks
received length first, then SHA-256 over all bytes. A mismatch returns
`blob.length-mismatch` or `blob.digest-mismatch`, deletes/unreferences the
unverified temporary product, and creates no command submission, receipt,
event, projection, or ordinary cache entry. A verified end returns
`blob.staged` and makes same-digest/length retry idempotent.

Verified staged authority bytes become ordinary referenced domain blob state
only in the Step 5 successful terminal transaction for a command that names
them. Rejection, refusal, failure, cancellation, disconnect, and an asserted
digest do not promote them. Temporary retention and garbage collection are
operational policy, not evidence that a command completed. An Environment's
staged bytes remain evidence-preserved through reseed and quarantine.

Hydration uses:

```text
BlobRead {
  "schema": "wipd.blob-read/1",
  "domain_id": canonical domain ULID,
  "manifest_digest": canonical sha256 text,
  "digest": canonical sha256 text,
  "byte_length": uint64,
  "offset": uint64,
  "length": uint64
}

BlobRangeStart {
  "schema": "wipd.blob-range/1",
  "digest": BlobRead.digest,
  "byte_length": BlobRead.byte_length,
  "offset": BlobRead.offset,
  "length": BlobRead.length,
  "range_digest": canonical sha256 text
}
```

`offset + length` MUST be within the manifest entry without uint64 overflow.
The exact range bytes follow in chunks. The receiver verifies returned byte
count and `range_digest`. Range verification permits use of that authenticated
range but does not mark the full blob hydrated. Only complete nonoverlapping
coverage assembled to exactly `byte_length`, followed by verification of the
manifest's full digest, changes cache state to `verified-complete`. Cached bytes
with bad length/hash are discarded and rehydrated; a local `hydrated` flag is
never authority evidence.

`lazy` blobs may remain absent until read. A missing lazy blob is
`blob.not-hydrated`, not model absence. `pin-before-use` entries MUST reach
`verified-complete` and be durably pinned before the Step 7 owner reports
offline readiness or permits a command that requires them. Failed or partial
hydration remains `hydrating` or `failed` and blocks that readiness. The
authority cannot infer local readiness from having served ranges.

Each blob is bounded by the negotiated `max_stream_bytes` (default 8 GiB,
absolute 1 TiB); larger content is not representable as one protocol-1 blob.
Chunks are at most `max_chunk_data`. A range or upload resumes only at the
receiver's exact durably retained contiguous offset; full digest verification
still happens only after all declared bytes arrive. Blob, manifest, and event
transfer never bypass Step 4's stream-byte, frame, backpressure, concurrency,
deadline, or cancellation rules.

## 8. Compatible-store reseed

Step 3's `store.reseed-required` starts no mutation by itself. After protocol,
authentication, domain, epoch, strict-lineage, and preservation checks succeed,
the Environment freezes its per-domain lane and inventories:

- immutable pending/quarantined journals with canonical command bytes,
  command IDs/hashes, operation versions, ordering, and claim context;
- all locally installed terminal receipts and unresolved submission evidence;
- staged blob bytes and their declared digest/length; and
- local evidence required for explicit repair, abandon, or history diagnosis.

Events, derived projections, snapshot resources, and ordinary cache blobs are
the disposable base. Verified ordinary cache bytes MAY be reused only after
digest/length verification against the new manifest. Reseed then performs §5
into a shadow base, verifies the complete prefix and manifest, checks every
preserved receipt against any duplicate authority receipt, and revalidates each
journal entry's exact `wipd.command/1`, request hash, domain/epoch, operation
version, and preserved ordering. It atomically swaps only the disposable base,
reattaches the untouched evidence inventory, and rebuilds the explicit overlay.

Any prefix conflict, history regression, receipt disagreement, missing staged
bytes, altered journal identity, unpreservable evidence, auth/epoch change, or
seed failure refuses reseed and leaves the old base/evidence visible. Reseed
never upgrades an operation, rewrites a command, manufactures a receipt,
submits pending work, or treats a preserved journal as folded truth.

## 9. Claim, receipt, handoff, restore, and migration integration

Step 7 claim acquisition owns its request, exclusion transaction, grant, and
lifecycle. Its grant MUST reuse this contract's `PrefixDelta` from the stated
grant base to grant `as_of` and a complete `BlobManifest` at that same anchor.
Claim-required entries are `pin-before-use`. Step 7 may consume a local
hydration report only after this contract's complete digest/length
verification; the report is not authority truth. Claim journal return uses
`ReturnCommand` with exact claim ID/epoch. Normal release still requires Step
5 terminal receipts for every active journal entry; Step 6 transfer cannot
weaken that barrier. Q15 and Q16 remain Step 7-owned beyond these exact
integration points.

D129 handoff/restore products MUST use `PrefixAnchor` and `BlobManifest` to
prove a complete event prefix and blob closure, and MUST retain every Step 5
receipt, claim record, public authority key/certificate, and unconsumed grant
required by the selected source epoch. A bundle section carries kind, exact
byte length, raw-byte digest, and record count; sections sort by kind and are
all verified before activation. Routes, endpoint profiles, private keys,
bearer/OIDC material, and local journals are excluded. Planned handoff also
requires the separately owned quiescence proof. Restore requires a strict
compatible prefix and complete closure; conflicting branches or missing bytes
refuse. Activation and owner attestation are later ceremony/store work, but
they MUST start the destination at the next epoch, invalidate old trust, never
execute an unresolved old-epoch command, and report `history-regressed` to an
ahead Environment while preserving its evidence.

D130 migrated events use Step 2's deterministic migration identity and enter
the transferred prefix in preserved event-ID order. Their migration binding
and exact nonempty ranges travel as migration evidence, not ordinary M1
receipts. Seed/pull verification does not regroup, rehash, or reinterpret them.
After a new-authority event exists, transfer cannot authorize rollback to the
legacy store. This contract defines no migration executor or authority-store
schema.

## 10. Limits and stable problems

In addition to Step 4 limits:

- page size defaults to 100 and is at most 1,000;
- page and transfer token lifetime is at most fifteen minutes;
- each event record, manifest entry, receipt, and transfer control map MUST fit
  one negotiated frame body; large semantic content is a blob, not a fragmented
  control record;
- each seed/pull/fold event or manifest segment MUST fit the negotiated stream
  limit; a nonfinal segment ends at a verified resumable boundary and issues a
  protected transfer token;
- one `ReturnCommand` exchange carries one command and one terminal receipt;
  batching cannot cross the Step 5 transaction or refusal boundary; and
- all counts, offsets, lengths, and additions are checked for uint64 overflow
  before allocation or state change.

Stable Step 6 problem codes are protocol states, not M1 results:

| Code | Meaning/effect |
|---|---|
| `query.invalid-page-token`, `query.page-token-scope`, `query.snapshot-expired`, `query.page-size` | Read continuation invalid; no replacement snapshot is guessed. |
| `transfer.prefix-mismatch`, `transfer.manifest-mismatch`, `transfer.resume-invalid`, `transfer.incomplete` | Transfer is not installable; prior base/evidence remains. |
| `store.reseed-required`, `store.reseed-refused`, `store.history-regressed` | Step 3 boundary or reseed/history outcome; no command result or implicit migration. |
| `blob.not-found`, `blob.not-hydrated`, `blob.range-invalid`, `blob.length-mismatch`, `blob.digest-mismatch` | Blob bytes cannot be claimed complete; no fold/cache promotion follows. |
| `journal.return-blocked`, `journal.quarantined` | Return cannot pass its current head; Step 7 repair is required. |

Malformed, unauthenticated, over-limit, cancelled, or incomplete exchanges use
the owning Step 3/4 problem where applicable. After a returned command crosses
the Step 5 submission point, cancellation only stops waiting and the exact
receipt query/idempotency rules apply.

## 11. Decision and question traceability

| Source | Step 6 closure | Later-owned boundary |
|---|---|---|
| **D119 / Q09–Q10** | Complete byte-identical prefix, cumulative anchor, snapshot-bound complete manifest, verified upload/cache/hydration, atomic shadow install. | M3 implements storage/cache; Step 7 declares claim pins. |
| **D122 / Q12** | One immutable returned command transaction; contiguous lowest-sequence head; stop/quarantine on non-success; no suffix skip; replacement gets a new command identity. | Step 7 owns journal durability, eligibility, repair authorization, abandon, and claim state. |
| **D123 / Q09** | Digest/length verification precedes submission/fold; success alone promotes; duplicate upload is idempotent; `FoldResult` carries the exact receipt/range. | Step 8 supplies independent doubles and properties. |
| **D124** | Prefix order is authority event order; query versions define deterministic presentation order; narration never rewrites fold truth. | Event-schema implementation and narration views remain later work. |
| **D125** | Cursor reads are snapshot-visible; a provisional target follows its birth in the same Environment sequence stream and remains visibly provisional until fold. | Cursor operation/event schema remains its owning later work. |
| **D128 / Q11 / Q13 / Q14 / Q22** | Pull semantics, stable snapshots, protected scoped pagination, pending-return-before-pull/write, atomic receipt/tail install, explicit provenance, and evidence-preserving disposable-base reseed are fixed. | Scheduler/store implementation remains M3/M4. |
| **D129** | Transfer integrity uses strict prefix, full blob/receipt closure, section hashes/lengths, next-epoch and history-regressed boundaries. | Activation, quiescence/owner ceremony, backup implementation, and recovery vectors remain Steps 8–9/M3. |
| **D130** | Migration prefix/range evidence transfers without regrouping or rollback. | Migration execution, audit, and proof review remain later work. |
| **D131** | Every read exposes domain/epoch/as-of/source/reachability/local base/pending/provisional/quarantine/history without guessed freshness; tokens and payloads stay out of logs. | Human rendering and final privacy review remain Step 9/current-product work. |
| **Q15–Q16** | Claim grants reuse exact delta/manifest integrity; pin completion and terminal receipt barriers are explicit integration points. | Step 7 owns all claim and journal lifecycle messages beyond `ReturnCommand`/`FoldResult`. |
| **Q24** | Authority validates prefix/manifest/blob hashes and assigns authority snapshots; Environment derives local overlay/hydration; HMAC protects issuer tokens; TLS/OS auth binds peers. | Durable portable bundle signatures are reviewed in Step 9. |
| **Q25** | Deterministic diagnostic vectors and strict focused validators cover Step 6 behavior. | Step 8 publishes standalone schemas, wire products, independent client/server doubles, fuzzing, and property tests. |

`read-transfer-snapshot-vectors.json` uses diagnostic notation
`wipd.read-transfer-vector/1`. JSON is not wire encoding, token claims, event
encoding, or hash input unless a vector explicitly supplies byte hex. The
focused validators check read/pagination binding, prefix and manifest chains,
return/fold refusal boundaries, blob outcomes/ranges, and reseed preservation.

This step adds no daemon, socket/listener, authority store or migration,
production token/key handling, fresh authority state, claim lifecycle, current
CLI behavior, tracker/WIP/outbox mutation, or M3/M4 implementation.

There are no unresolved Step 6 decisions. Changes to the read response, token
binding, prefix/manifest digest, seed/pull/fold ordering, blob
verification/promotion, hydration readiness, or reseed preservation rules
require a new negotiated protocol contract; they are not implementation
choices.
