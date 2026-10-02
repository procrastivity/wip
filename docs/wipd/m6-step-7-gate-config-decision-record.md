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

If several prerequisite failures apply, choose the witness deterministically:
check the target node and then each enclosing node outward, selecting the
unsatisfied applicable gate in ascending bytewise gate-ID order at each node.
Execution and recovery must validate the same witness.

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

## Owner-ratified Matter finish compatibility (FINISH-A)

Retain and correct `matter.finish@v1` to implement D55's already-settled,
gate-aware seal predicate. The Step 5 descendant-completion calculation was an
implementation limitation, not a separately approved meaning of “sealed.”
Preserve validation of compatible old `matter.finish@v1` receipts and histories,
including historical command effects that appended inline `batch.swept`; new
finish executions append no sweep event. Do not introduce `matter.finish@v2`.
The separately contracted post-claim-close sweep remains future work, and this
decision does not alter the M2 claim release or stand-down `@v1` maps.

## Implementation and archive status

Implementation remains partial: the ordinary gate declaration/close/dismiss
foundation, FINISH-A correction, pure detached-proof validation, and private
repair admission/terminal path are present on the M6 Step 7 feature branch, but
Step 7 is not complete. Seed/pull history remains the authority event prefix;
client-state/1 rebuilds and validates its gate projection from that history,
while detached repair proof/nonce state remains private to the authority store.
Client-state/1 does not represent Repo config, shared-reference/aggregate, or
tracker-candidate/outbox projections. Its transfer fold therefore fails closed
on `config.set`, shared-reference events, and narrated/boundary tracker effects
instead of silently claiming parity. Those client projection consequences are
deferred and must be represented before such transfer histories are accepted.
Separately negotiated command-submit v2 envelopes and Environment detached-proof
retry retention/forwarding are now present on both transport hops. That feature
is not a repair-operation capability: no repair operation is registered or
exposed through either submission version. The private terminal validator
currently forbids a client `terminal_receipts` row, `QueryCommand` explicitly
excludes repair submissions, and public canonical decoding has no repair input
contract. The smallest remaining registration boundary is a separately reviewed
canonical repair input/result and authenticated status/receipt bridge that
preserves the existing closed receipt schema, private terminal/witness history,
atomic nonce/proof admission, and original verification time. It must also
install the terminal range (including successful no-event outcomes) and claim
journal acknowledgment before repair can enter either capability set. This
transport slice does not force that bridge or duplicate authorization logic.
Role projection and the Step 17/18 public config/provider APIs remain deferred.
The separate post-claim-close sweep remains later work. The earlier
implementer-thread status at base
`a54e75563e27233013b319c2b0c344c2a02098f2` is historical context, not current
implementation status.

Relevant source contracts: `operation-contract.md`,
`frame-security-execution-contract.md`, `conformance-schemas.cddl`,
`claim-lifecycle-contract.md`, `m6-step-1-operation-census.md`,
`internal/store/payloads.go`, `internal/store/config.go`, and
`internal/writesurface/gate.go`. Model decisions: D55, D76/D126, and D121.

Coordination history: [Step 7 implementer](https://ampcode.com/threads/T-01a0f77a-4590-75d8-ba0a-61cce7a1d819),
[contract planner](https://ampcode.com/threads/T-01a0f7a3-3731-76c8-8383-34f8e414839a),
[authorization reviewer](https://ampcode.com/threads/T-01a0f7f8-8bf8-7257-91c4-ca1d167eeaf1).
