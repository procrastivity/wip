# M6 Step 7: gate and config decision record

Status: owner-ratified design decisions; implementation is not complete. This
record captures decisions made through 2026-10-01. It is a design record, not
evidence that the operations, projections, or transport are implemented.

The operation inventory and ownership baseline remain
[`m6-step-1-operation-census.md`](m6-step-1-operation-census.md) and its TSVs.
This record supersedes those proposed Step 7 classifications where they
conflict with the ratifications below; it does not silently register operations
or claim full CLI parity.

## Ratified semantics

### Gate lifecycle and evented configuration

Preserve the legacy event names, payload meaning, Repo/node subjects, and
projection behavior in `internal/store` and `internal/writesurface`:

| Event | Subject | Payload core | Meaning |
|---|---|---|---|
| `gate.declared` | Repo | `gate`, `scale`, explicit `exempt` snapshot | Fixes the Repo's gate scale and prospective applicability boundary. |
| `gate.exemption-repaired` | Node | `gate` | Adds a distinct exemption; it is not a close or dismissal. |
| `gate.closed` | Node | `gate`, subject `scale`, existing tracker-push snapshot | Normal gate satisfaction. |
| `gate.dismissed` | Node | `gate`, subject `scale`, required `reason`, existing tracker-push snapshot | Exceptional satisfaction of an open gate. |
| `config.set` | Repo | `key`, `value` | Evented Repo-scoped configuration; later values win. |

Optional legacy payload fields retain their existing omission rules. Gate
configuration, a prospective exemption, a normal close, and a dismissal remain
distinct states. Gate ownership and actor/role checks remain enforced; an
unsupported role-owned gate mutation fails closed.

Retain these command/result distinctions:

- An identical declaration and an already-present exemption are successful
  no-event outcomes; their receipt has `accepted_events: null`.
- Setting a configuration key to its current value still appends `config.set`.
- A fresh repeated close or dismissal refuses; dismissal requires a Done node
  and a nonblank reason.
- Exact command replay returns the original terminal result and receipt without
  repeating effects.
- Ordinary close does not require Done. D55 makes finish and normal close
  order-independent; either may occur first, and sealing is evaluated from the
  shared lifecycle-plus-gate predicate.

### Claim fencing and authority delivery

D121 claim fencing is independent of delivery class. Every direct gate or
subtree mutation requires the exact active Matter claim and epoch, including a
mutation delivered to the authority because its footprint is shared or
Repo-wide. A local lock or authority delivery alone is not a claim. Update the
Step 1 matrix's `claim=none` proposals for direct gate mutations accordingly.

Static guard/write/read footprints must include the affected gate history,
node ancestry and lifecycle, role policy, Repo config, and any tracker
candidate, shared-reference, or outbox projections touched by the legacy fold.
Do not imply candidate parity when a footprint is omitted. Public
`tracker.config.set` remains Step 17; any Step 7 deferral must say exactly what
is deferred and must not turn an unsupported effect into a success claim.

### Anonymous Batch sweep

Honor the current D76/D126 model: Step 7 computes whether the seal predicate
became true, but an anonymous Batch sweep occurs only after the Matter claim
closes, through a separately contracted/versioned authority operation. Do not
silently append `batch.swept` to the M2 claim-release or stand-down `@v1`
event/receipt maps. Preserve their closed schemas. This does not change the
meaning of `matter.finish@v1`'s existing claim fence or authorize immediate
seal-time sweep.

## Owner-attested exemption repair

Repair is limited to a missing prospective exemption and requires a verified
owner-root authorization bound to the exact command and historical declaration
boundary. Reuse `wipd.signed-artifact/1` with kind `owner-attestation` and
`wipd.owner-attestation/1`; add action `gate-exemption-repair` and subject
schema `wipd.gate-exemption-repair-subject/1`. The closed subject binds
`command_id`, final `request_hash`, `repo_id`, `node_id`, `gate`, the inclusive
declaration boundary (`event_count`, `high_water_event_id`, `prefix_digest`),
`incident_ref`, `reason_digest`, and `evidence_refs`.

The signed payload binds the domain and current authority epoch, null
`next_epoch`, the subject schema/digest/bytes, a 16-byte CSPRNG nonce,
canonical UTC issue/expiry times, and `loss_accepted: false`. Wrapper and
payload domain, epoch, and issue time agree; the wrapper uses the owner signer
role/root and null key-generation, artifact-sequence, and predecessor fields.
`subject_digest` hashes the deterministic-CBOR subject bytes. The complete
deterministic-CBOR signed artifact is at most 1 MiB.

The immutable repair command input carries the incident reference, reason,
evidence references, and declaration boundary; duplicated subject values must
match. The boundary is an inclusive event prefix ending at the actual
`gate.declared` event for that Repo and gate. Compute the final canonical
command and request hash first; the hash binds the Environment, sequence, and
exact claim; then sign that hash. The authorization proof and its digest are
absent from canonical command bytes, the request hash input, and blobs. Verify
historical eligibility at the supplied declaration boundary, not only from
present-day Done or satisfied state. Preserve D121's transactional claim/epoch
fences. The proof does not authorize normal close, dismissal, or Batch sweep,
and cannot replace factual guard checks.

Accepted reference and freshness rules:

- `gate`: NFC, control-free, 1–256 UTF-8 bytes. `reason`: exact NFC bytes,
  1–4096 bytes; `reason_digest` hashes those exact bytes.
- `incident_ref`: NFC, 1–2048 UTF-8 bytes, no whitespace or controls; accept an
  absolute HTTPS URI with a nonempty host and no userinfo, or a valid URN.
  Identity is the accepted exact byte spelling; do not fetch it during
  authorization.
- `evidence_refs`: 1–32 bytewise-sorted unique lowercase
  `sha256:<64 hex>` digests of raw evidence bytes. They are reference-only:
  the owner attests consideration; the authority makes no claim about evidence
  availability or truth.
- `issued_at <= verified_at < expires_at`, with a positive lifetime no longer
  than ten minutes and no implicit clock skew.
- Repair nonce uniqueness is `(domain_id, nonce)` across epochs for repair
  authorizations only; it is not shared with other owner ceremonies.
- Atomically reserve the nonce and persist exact proof bytes plus verification
  time at durable admission. A later rejected/refused outcome does not free the
  nonce; a pre-admission failure does. Recovery reuses the stored proof and
  original verification time. Expiry does not revoke admitted work. Exact
  authenticated replay returns the original status without fresh
  semantic/expiry checks; the proof may be omitted on replay, but if supplied
  it must byte-match the stored proof and cannot replace it.

## Detached proof transport

Preserve closed `wipd.command-submit/1` unchanged. Add a separately negotiated
v2 submission capability on both local wipd IPC and authority submission, using
the existing feature-negotiation mechanism. The feature token and closed-map
spelling are to be fixed in protocol implementation. Version 2 carries the
detached owner authorization outside canonical command identity. Persist the
exact proof bytes alongside the Environment's durable retry identity without
adding them to that identity. A repair must fail closed if either hop lacks v2;
it must never downgrade to v1. Non-repair v1 clients and peers retain existing
behavior.

## Versioning question still open

The implementation draft introduced `matter.finish@v2`, but this was not
owner-ratified. The planner's best-supported reading is to correct
`matter.finish@v1` to implement D55's already-settled gate-aware seal predicate;
the Step 5 gate-free calculation is an implementation limitation, not a
separately approved meaning of “sealed.” The operation compatibility contract
requires a version bump for an incompatible contract change. Our recommendation
is to retain `@v1`: its `BecameSealed` result keeps the D55 meaning and the
change corrects an incomplete predicate. The owner has not explicitly
ratified this versioning conclusion, so record it as open until confirmed. Do
not treat the draft's v2 definition as authoritative.

## Implementation and archive status

At base `a54e75563e27233013b319c2b0c344c2a02098f2`, the M6 Step 7 gate/config
authority projections, command handlers, and v2 submit path are not
implemented. The Step 7 implementer thread reported no edits or commits; its
checkout was clean at that base. This design record preserves the settled
decisions independently of that thread, while the versioning question remains
visible rather than being silently decided.

Relevant source contracts: `operation-contract.md`,
`frame-security-execution-contract.md`, `conformance-schemas.cddl`,
`claim-lifecycle-contract.md`, `m6-step-1-operation-census.md`,
`internal/store/payloads.go`, `internal/store/config.go`, and
`internal/writesurface/gate.go`. Model decisions: D55, D76/D126, and D121.

Coordination history: [Step 7 implementer](https://ampcode.com/threads/T-01a0f77a-4590-75d8-ba0a-61cce7a1d819),
[contract planner](https://ampcode.com/threads/T-01a0f7a3-3731-76c8-8383-34f8e414839a),
[authorization reviewer](https://ampcode.com/threads/T-01a0f7f8-8bf8-7257-91c4-ca1d167eeaf1).
