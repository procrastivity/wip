# M2 Step 9: design, security, privacy, and implementability seal

Status: independently reviewed and sealed normative design record for M2. This
record closes the Step 9 deferrals in the published conformance package. It
adds no daemon, listener, socket, authority store, migration executor, CLI
behavior, tracker/WIP/outbox mutation, or M3/M4 production code. The key words
**MUST**, **MUST NOT**, **SHOULD**, and **MAY** are normative.

This review used the complete M2 Step 1–8 contract and conformance set, the M1
operation/event boundary, D115–D131, the M2 roadmap questions Q01–Q25, and the
M3/M4 acceptance boundaries. Where this record says **amends**, that narrow
rule replaces the conflicting sentence in the owning earlier M2 contract. All
other earlier requirements and vectors remain in force.

## 1. Review verdict and resolved findings

No owner decision remains open. M2 is implementable by M3's fresh authority
store and M4's isolated daemon chassis after the following review findings are
made explicit:

| Finding | Resolution |
|---|---|
| V15 used `alpha--01K6M0`, contradicting D127's `<requested>-<identity-suffix>`. | Corrected to `alpha-01K6M0`. The collision still succeeds with the same identity and repair requirement. |
| V16 placed an Environment-owned cursor write “in the same provisional stream,” contradicting D120's one static delivery class and newborn-subtree-only provisional footprint. | `cursor.move@v1` remains `environment` delivery, keyed by Worktree. An explicit birth dependency and Environment sequence order it behind the provisional birth; it is never a provisional command. |
| Step 5 required an event range for every success, but a deterministic operation no-op can preserve existing user-visible success without inventing an event. | A null success range is allowed only when that exact operation version declares a deterministic no-op output and no model, blob, projection, or external effect. Ordinary accepted success remains nonempty. |
| Protocol-1 channel authentication did not make receipts, grants, or bundles independently portable. | A separate `wipd.signed-artifact/1` wrapper signs deterministic products; command identity and frames remain unsigned. |
| Handoff, restore, and cross-Environment stand-down named owner authority but did not close its proof or replay boundary. | Versioned owner attestations bind exact intent, epoch, products, loss, nonce, and expiry. They are verified and consumed atomically at the state transition. |
| Certificate bindings, rotation, and historical signature revocation were not concrete enough for independent implementations. | Critical canonical URI SANs, Ed25519 artifact keys, exact SPKI digests, one-use consumption, and an append-only signed-artifact chain with owner-signed fences are fixed below. |
| Routine log redaction was strong, but terminal command bytes and stand-down reason text could outlive their recovery purpose. | Correctness evidence is retained independently; terminal command payloads become compactable, while unresolved payloads remain. Durable stand-down truth stores a digest, not reason text. |
| D127 did not fix event/output/cursor shapes; changing v1 collision refusal to success would violate Step 3 operation compatibility. | Keep `matter.create@v1` and its output/event behavior unchanged. Define collision success, repair event, and requested/assigned output under `matter.create@v2`. Cursor input/output/event maps are fixed below. |
| D130 did not define the coupling audit, ambiguous group failures, synthetic binding identity, or portable proof. | The graph, grouping, ordering, refusal cases, synthetic command, and signed migration proof are fixed below. |

These changes do not relax any existing accepted-command, refusal, retry,
recovery, transfer, claim, or security vector. The successful-no-op rule is a
closed, operation-version-declared exception; an implementation cannot infer it
from an empty event list after execution.

## 2. Portable signed artifacts

### 2.1 Key hierarchy and algorithms

An authority domain has the immutable D117 owner root. The owner root and every
M2 portable artifact key use Ed25519. A key ID is
`sha256:<lowercase hex SHA-256 of DER SubjectPublicKeyInfo>`. Private keys never
enter a bundle, receipt, grant, route, event, log, or Environment seed.

The offline owner root signs:

- authority-artifact-key certificates and their revocation/final fences;
- one-use operator enrollment grants;
- planned-handoff, disaster-restore, and stand-down attestations; and
- the migration authorization/final proof attestation.

One owner-certified, epoch-scoped authority artifact key signs:

- every portable terminal receipt;
- claim-grant descriptors and complete blob manifests;
- backup/handoff manifests and source relinquishment records;
- destination activation records; and
- migration binding proofs.

TLS keys and Environment certificate keys are channel identities and MUST NOT
sign these products. Possession of an Environment key never grants owner or
authority signing power. Protocol major 1 still has no per-frame, per-query, or
per-command application signature; OS-peer authentication or mTLS remains
mandatory for live exchanges, and the authority still recomputes command,
range, blob, prefix, and manifest digests.

### 2.2 Authority key certificate

An owner-signed `wipd.authority-artifact-key/1` payload contains exactly:

```text
domain_id, authority_epoch, key_generation, key_id, ed25519_public_key,
not_before, not_after
```

`key_generation` is positive and strictly increases within an authority epoch.
The validity interval is UTC RFC3339Nano. The payload is carried as an
owner-role signed artifact. It cannot authorize a different domain or epoch.
The active certificate and private key are authority metadata included in
fresh-store setup; only the public certificate enters transfer products.

### 2.3 Signed wrapper and preimage

`wipd.signed-artifact/1` is a deterministic-CBOR closed map with the shape in
`conformance-schemas.cddl`. The `payload` is itself complete deterministic CBOR
under `payload_schema`; `payload_digest` is the SHA-256 of those exact bytes.
The signing preimage is exactly:

```text
UTF8("wipd/signed-artifact/v1") || 0x00 ||
deterministic-CBOR(unsigned-artifact-map)
```

The unsigned map is the artifact map with only `signature` removed. Ed25519
signs that preimage directly. The portable artifact digest is:

```text
sha256(UTF8("wipd/artifact-digest/v1") || 0x00 ||
       deterministic-CBOR(complete-signed-artifact-map))
```

The wrapper's `kind`, `domain_id`, `authority_epoch`, signer role/key ID,
payload schema/digest, issue time, and authority-chain fields are therefore
covered. A verifier MUST decode the declared closed payload schema and require
all repeated domain, epoch, command, range, prefix, grant, and manifest values
to agree. A signature never replaces semantic validation.

Owner-role artifacts set `key_generation`, `artifact_sequence`, and
`previous_artifact_digest` to null. Authority-role artifacts set all three:
sequence starts at 1 for each `(domain, epoch, key_generation)`, increments by
one, and names the preceding complete artifact digest (null only at sequence
1). A product's embedded predecessor `artifact_chain_head` is also null when
the domain has no earlier authority-signed artifact. The authority atomically
stores the wrapper and chain head with the product it signs. Concurrent signing
is serialized within that key generation.

### 2.4 Rotation, revocation, and historical verification

An owner-signed key fence names domain, epoch, generation, key ID, final
sequence, final artifact digest, effective time, and reason digest. Planned
rotation commits the old fence before the new generation signs. Emergency
revocation stops the key immediately; products not already on the canonical
chain ending at the owner-approved fence are untrusted. A key revoked before
its first product has final sequence zero and null final digest; otherwise
sequence is positive and the digest names that exact last artifact.

An artifact verifies historically only when its complete chain reaches either
the currently active certified chain head or an owner-signed final fence. This
prevents a compromised key from backdating a parallel chain. Revocation does
not invalidate products already included in the fenced canonical chain.
Authority public certificates, owner attestations, fences, and chain records
are domain-lifetime correctness evidence and survive handoff/restore. An
immutable owner-root compromise has no continuity-preserving in-domain repair;
recovery requires a new domain and explicit trust remap rather than pretending
the old owner remained trustworthy.

Portable signatures are generated and verified in M3/M5/M9. They do not make
M4's local IPC remotely trustworthy or authorize a direct-store path.

## 3. Owner-attested ceremonies

Every owner attestation is an owner-role `wipd.signed-artifact/1` carrying a
closed `wipd.owner-attestation/1` payload. It contains domain, current and next
epoch as applicable, one action, exact subject digests/identities, a random
128-bit nonce, `issued_at`, `expires_at`, and an explicit loss boolean. The
`subject_digest` is SHA-256 of the exact deterministic-CBOR `subject` bytes;
the action selects exactly one subject schema: `planned-handoff` selects
`wipd.planned-handoff-subject/1`, `disaster-restore` selects
`wipd.disaster-restore-subject/1`, `claim-stand-down` selects
`wipd.claim-stand-down-subject/1`, and `migration-authorize` selects
`wipd.migration-authorization-subject/1`; `migration-seal` selects
`wipd.migration-seal-subject/1`, and `activation-intent` selects
`wipd.activation-intent-subject/1`. Activation intent, planned handoff, and
restore require `next_epoch = current_epoch + 1` (and equality with the intent
subject); the other actions require null `next_epoch`.
Only restore and stand-down permit `loss_accepted=true`. A mismatched action,
schema, digest, epoch, or loss value refuses before transition.
The accepting authority checks the immutable owner key, scope, active epoch,
expiry with bounded clock skew, and one-use nonce, then consumes the nonce in
the same transaction as the authorized transition. Attestations cannot be
retargeted or replayed.

### 3.1 Planned handoff

Planned handoff is ordered as follows:

1. The owner signs an `activation-intent` attestation binding source
   domain/epoch and TLS pin, destination endpoint/TLS pin, destination
   owner-certified artifact key, exact next epoch (inside the closed subject),
   nonce, and expiry. The source consumes this intent nonce when it commits
   quiescence; the final owner attestation has a distinct nonce.
2. The source refuses quiescence while any claim, submitted-but-nonterminal
   command, unresolved outcome, return, signing operation, or incomplete blob
   exists. It seals the complete bundle manifest, artifact-chain head, prefix,
   receipt/claim/registry closure, and source relinquishment as authority-signed
   artifacts.
3. The owner verifies those products and signs a `planned-handoff` attestation
   binding the intent and product artifact digests and asserting
   `loss_accepted=false`.
4. The destination verifies every byte/hash/signature/closure and atomically
   activates exactly the next epoch, consumes the owner nonce, fences old
   claims/certificates, and signs `wipd.authority-activation/1`.

The source MUST NOT resume admission after relinquishment. The destination MUST
NOT activate from an intent alone or from a bundle lacking source signature and
owner attestation. Route updates consume the signed activation product; a
failing connection never teaches a new pin or epoch.

An `artifact_chain_head` embedded in a product is the complete chain head
immediately **before** that product is signed. Its own artifact digest becomes
the next head. The signed bundle manifest's digest is its complete artifact
digest, not a self-referential field inside the manifest. Source relinquishment
refers to that completed bundle digest as its predecessor head; the final
attestation names the resulting relinquishment artifact and chain head.

### 3.2 Disaster restore

Disaster restore does not require a fresh source signature because the source
is declared dead. The selected candidate must still satisfy D129 prefix
relatedness, complete referenced-blob closure, receipt reconstruction, and
unknown-command non-execution. The owner signs a `disaster-restore` attestation
binding old/new epoch, dead source identity, selected bundle/proof digest,
recovered prefix/high-water, recovered artifact-chain head/fence, the exact set
or digest of unresolved old commands, and `loss_accepted=true` for everything
after that high-water. The destination atomically consumes it and emits the
same next-epoch activation product. A candidate cannot use this ceremony to
join divergent histories or truncate an ahead Environment.

### 3.3 Cross-Environment stand-down

**This amends Step 7 §8.** `wipd.claim-stand-down/1` carries an additional
required `owner_authorization` signed artifact outside the canonical command.
Its owner-attestation payload binds action `claim-stand-down`, domain and
authority epoch, command ID and request hash, exact claim ID/epoch, owner and
acting Environment IDs, `sha256` of the exact NFC reason bytes, accepted loss,
nonce, and expiry. Keeping the proof outside `wipd.command/1` avoids a signature
cycle and leaves command identity unchanged.

The authority verifies exact equality with the canonical command and outer
fields. Missing/malformed proof is malformed before submission; invalid,
expired, reused, or wrong-scope proof is `auth.owner-attestation-invalid` with
no submission. A successful stand-down consumes the nonce in the same terminal
transaction. Durable events/projections/receipt retain both Environments,
claim/epoch, loss acceptance, and reason digest. Exact reason text is restricted
audit material under §6, not permanent domain truth.

## 4. Enrollment and certificate security

### 4.1 Canonical certificate bindings

This closes Step 4's encoding/OID deferral without allocating a private OID.
Every WIP binding is one canonical URI in the standard X.509 Subject
Alternative Name extension (OID 2.5.29.17), and the SAN extension is critical.
An authority certificate has ordinary hostname SANs plus exactly one:

```text
wipd://authority/<domain-id>?epoch=<base10>&owner=<64-lowercase-hex>
```

An Environment certificate has client-auth EKU, no authority URI, and exactly
one:

```text
wipd://environment/<environment-id>?domain=<domain-id>&epoch=<base10>&owner=<64-lowercase-hex>
```

Components are ASCII and unescaped in this fixed grammar; query keys appear in
the shown order, with no duplicates, fragment, user info, alternate spelling,
or unknown parameter. The digest omits the `sha256:` prefix. Multiple WIP
binding URIs are invalid. Authorities still require hostname validation and an
exact configured server SPKI pin; the URI does not replace either check.

### 4.2 Grant, enrollment, renewal, and rotation

An operator enrollment grant is an owner-signed artifact whose payload binds
grant ID, domain, current authority epoch, owner key ID, exact scope, requested
DER-SPKI digest, optional prior Environment ID for authorized rotation,
issue/expiry times, and nonce. Only Environment enrollment/rotation scopes are
valid at `/enroll`; D115 domain-create/Repo-attach authorization cannot be
replayed as an enrollment grant or treated as a certificate-issuance scope.
Default maximum lifetime is ten minutes. The enrollment transaction verifies
the PKCS#10 proof, grant signature/scope/key/expiry, active epoch, and absence of
consumption; it atomically records consumption and the exact issued certificate.
A response-lost retry with the same grant and CSR digest returns that
certificate. Any different CSR or second use refuses `auth.grant-consumed`.

Same-key renewal requires current nonrevoked mTLS, the same SPKI, active domain
and epoch, and a fresh CSR proof. It preserves Environment ID and Clone natural
keys. Planned key rotation requires proof by the old key plus an owner-authorized
grant for the new SPKI, or that grant alone when policy explicitly authorizes
rotation. The new certificate and registry generation commit before the old
certificate is revoked; retries are idempotent by grant/CSR digest. Lost key or
state creates a new Environment and new Clones, never a silent rotation.

The authority checks certificate validity, registry generation, revocation,
domain/epoch, owner, Environment ID, and command/query binding at connection
and before every exchange on a retained connection. Promotion invalidates all
old-epoch authority and Environment certificates. Registry, issuance,
consumption, and revocation records are correctness metadata, not logs. Amp
OIDC follows the same atomic one-use and SPKI-binding rules; it cannot issue an
operator grant or persist its bearer assertion.

## 5. Final D127 operation and event shapes

`matter.create@v1` retains its current collision refusal, exact successful
output `{id, locator, title}`, and `matter.created` payload. Step 3 forbids
reinterpreting it as a successful collision. A future `matter.create@v2` is
provisional with the same input and static footprint, but explicitly advertises
collision success. Its output is exactly
`{id, title, requested_locator, assigned_locator, locator_repair_required}`.
The Step 8 V18 `matter.create@v2` unsupported-version vector remains true for
its v1-only negotiated capability set; implementing v2 requires advertising
that distinct version. No current M1 production DTO/event changes here.

When the requested locator is free, v2 emits only `matter.created` and returns
`locator_repair_required=false`. On collision v2 succeeds with
`locator_repair_required=true` and emits, contiguously in one command range:

1. `matter.created` with the assigned locator; then
2. `matter.locator-repair-required` with payload
   `{requested_locator, assigned_locator}` and the Matter as subject.

The assigned candidate starts as `<requested>-<first 6 Matter-identity
characters>`. If occupied, extend one identity character at a time through the
26-character ULID. If still occupied, extend from successive Crockford-base32
blocks of `sha256("wipd/locator-suffix/v1\0" || Matter-ID || uint64be(counter))`
until unique. The one separator is supplied by the algorithm; `alpha` therefore
produces `alpha-01K6M0`, not `alpha--01K6M0`. Candidate checks and event append
occur under the Repo locator guard in one fold transaction.

The repair projection clears only on authority-delivery
`matter.locator-repair@v1`, which emits `matter.locator-repaired` with payload
`{action: "rename" / "accept", requested_locator, previous_locator,
assigned_locator}`. `rename` requires a distinct free valid locator and changes
the address; `accept` requires `assigned_locator == previous_locator`. The
repair command output is the same payload plus `id`. No local/provisional write,
read side effect, or unrelated rename can clear the condition.

`cursor.move@v1` has static `environment` delivery, context Repo/Clone/Worktree,
and a Worktree ordering key. Its input is `{target_id: ULID-or-null}`. Its output
and `cursor.moved` event payload are
`{previous_target_id: ULID-or-null, target_id: ULID-or-null, changed: bool}`;
the event subject is the Worktree and its envelope carries Repo/Clone/Worktree.
An operation-version-declared unchanged target returns `changed=false` with a
null accepted range under the narrow no-op rule; it emits no event.

For a provisional target, `causation_command_id` MUST name that node's birth,
the cursor sequence MUST be greater, and the Environment scheduler MUST treat
the birth receipt as an eligibility dependency. The cursor stays in its
Worktree environment journal. It cannot return/fold or appear folded until the
birth succeeds; birth refusal or quarantine quarantines the dependent cursor
command. Thus “ordered in the provisional stream” in D125 means ordered behind
that birth dependency, not reclassified into or physically appended to the
provisional journal. This preserves D120's static delivery and footprint rules.

## 6. Privacy, logging, and retention

Step 4's logging allowlist and defaults remain: routine transport records 30
days, authentication/security metadata 90 days, and restricted command-hash
diagnostics 7 days. Shortening is always allowed; extension requires explicit
owner policy. Payload, title, locator, reason, command bytes, query filters,
event content, blob identity/content, grants/tokens, private keys, certificate
bytes, raw paths/remotes, and free-form peer/parser errors remain forbidden in
logs, traces, metrics, crash annotations, and support bundles. Audit access is
owner-admin only, encrypted at rest, access-audited, and excluded from metrics.

Durable correctness state is not a log and has purpose-specific retention:

| Data | Minimum retention and access |
|---|---|
| Events and referenced blobs | Domain truth under product retention; never removed merely because a log period elapsed. |
| Terminal receipt, request hash, event range, signed wrapper/chain | Domain lifetime; owner/domain-authorized readers only. |
| Submitted, outcome-unknown, pending, or quarantined canonical command bytes | Until terminal resolution or explicit evidenced repair/abandon; payload access restricted. |
| Terminal canonical command bytes | MAY be compacted after the configured audit window once receipt, typed output, event/range reconstruction evidence, blobs, and every recovery obligation are durable. Command ID/hash and receipt remain. |
| Enrollment/OIDC grant consumption and certificate registry | Through expiry/skew and the security-audit period; issued certificate identity/revocation and any response-retry mapping needed for correctness remain through domain life or epoch fence. Bearer bytes are never retained after validation. |
| Exact stand-down reason | Restricted encrypted audit only, default 90 days and owner-shortenable; durable truth retains its digest and typed loss outcome. |
| Owner attestations, authority key certificates/fences, handoff/bundle/migration proofs | Domain lifetime because they establish authority continuity. |

Compaction cannot alter request-hash identity, make an unresolved command look
unsent, delete evidence needed to reconstruct a receipt, or remove bytes still
referenced by an event, manifest, claim, journal, or proof. Backups inherit the
same classification and exclude routes, TLS/artifact private keys, bearer
credentials, local overlays, and derived caches.

## 7. D130 deterministic migration and proof

### 7.1 Coupling audit

Migration builds an undirected hypergraph over native Repo identities. It
examines every Batch/Run/Dispatch membership, dependency endpoint, shared
tracker aggregate/reference, cursor Worktree and target, causal/correlation
chain, Clone/Worktree ownership relation, subject relation, and Repo-null fact.
Each fact resolves transitively to the set of Repos reached through those
typed relations. A fact reaching multiple Repos adds one hyperedge; connected
components are the minimum domains. A fact reaching no Repo is reported with
its native identity and relation path and makes migration refuse rather than
guess. Users may union whole components in the signed migration plan but MUST
NOT split one or omit a fact.

The audit output is canonical: relation kinds and native IDs are sorted by
UTF-8 bytes, Repo IDs within a component are sorted, and components are sorted
by lowest Repo ID; `component_id` is that lowest Repo ID. Its deterministic
CBOR digest is included in the migration proof. Rebuild/backup, no
writers/claims, explicit owner/destination, and this audit all precede
conversion.

### 7.2 Legacy command groups and Environments

Within a selected domain, correlation links form command groups. A null legacy
origin self-roots at that event; otherwise the root must exist, be the unique
acyclic origin, and agree with every causal/correlation link. A group spanning
selected domains, containing conflicting roots, or containing execution
dimensions that resolve to more than one Environment refuses migration.

The migration plan binds legacy Clone/Worktree execution dimensions to one
Environment. Groups with dimensions use that binding. Groups without them use
exactly one synthetic migration Environment per `(source-store digest,
selected domain)`. Its ULID is the Crockford encoding of the first 128 bits of
`sha256("wipd/migration-environment/v1\0" || raw-source-store-digest ||
ASCII-domain-ULID || uint64be(counter))`, starting at counter zero. If it
collides with a bound or existing Environment ID, increment counter until
unused; this allocation is recorded in the signed plan. A binding to an
existing Environment must be proven by the legacy execution dimensions, never
chosen from a mutable hostname. Groups sort by first preserved event ULID,
then correlation origin as a defensive tie-breaker, and receive contiguous
per-Environment sequence in that order. `command_id` and
`correlation_command_id` equal the correlation origin. `acted_at` is the origin
event's original `occurred_at`.
Canonical legacy event bytes and `request_hash` remain exactly Step 2's form;
event IDs, event bytes, and global authority order are preserved.

### 7.3 Synthetic clone binding and rollback fence

One owner-approved `migration_id` ULID exists per selected destination domain.
After all preserved legacy events, one synthetic migration-specific
`wipd.migration-binding/1` value uses that ID, the synthetic migration
Environment, its next sequence, the signed plan's cutover time as `acted_at`,
and the complete Clone-to-Environment map sorted by Clone ID. It is not an M1
operation or a `wipd.command/1`; its deterministic CBOR and Step 2 request-hash
prefix bind it without reinterpreting any legacy `wipd.migration-command/1`
group. It emits one
`clone.environment-bound` event per Clone in that order under
`system:migration`; authority event IDs are allocated monotonically after the
preserved high-water. Its request hash and exact event range enter the proof.
With no legacy Clones, no synthetic binding command/event is emitted; the three
synthetic identity/hash/range fields in the proof are all null together. The
synthetic Environment is still reserved for dimensionless legacy groups.

The destination emits no ordinary write until a `wipd.migration-proof/1`
payload has been authority-signed and a separate owner `migration-seal`
attestation binds its complete signed artifact digest and the earlier owner
`migration-authorize` digest. The proof binds source
store digest and schema, source backup/high-water, coupling-audit digest and
component membership, selected-domain unions, legacy group origin/Environment/
sequence/acted-at/request-hash/ranges, Clone bindings and synthetic range,
destination domain/epoch/prefix, blob closure, artifact-chain head, migration
ID, and rollback fence. Once any post-migration command is submitted, the
fence permanently returns `migration.rollback-forbidden`; rollback is allowed
only to the verified pre-cutover backup before that point.
The proof's embedded artifact-chain head is the predecessor to its own signed
artifact, avoiding a self-referential digest.

## 8. M3 and M4 implementability

M3 can implement this contract without legacy implicit input. Its fresh store
needs durable domain/Repo/owner/epoch identity, membership, owner and authority
public-key records, artifact chain/fences, Environment certificate registry and
sequence, submissions/receipts, events/projections/blobs, claims/grants,
snapshots/tokens, and transfer/migration proofs. The required atomic boundaries
are: grant consumption plus certificate issuance; submission ownership;
events/projections/receipt plus signed receipt and artifact-chain advance;
claim lifecycle; blob promotion; owner nonce consumption plus stand-down or
activation; and migration proof/fence. No SQL/table layout is prescribed.

M4 needs only the isolated profile/path guard, singleton and OS-peer check,
local frame handler, per-domain lane, Environment key/certificate reference,
sequence allocator, disposable authority prefix/projections, immutable
journals/overlays, received signed receipts, staged/cache blobs, and provenance.
It never stores an owner private key, signs authority artifacts, promotes an
epoch, opens the authority DB directly, or depends on a remote listener. The
cursor cross-stream dependency is schedulable from local Environment sequence
and receipt state; it requires no mixed-delivery journal.

M3 and M4 may choose persistence and crypto libraries, but their external bytes
and state transitions must satisfy the published schemas/vectors. M5/M6/M7/M9
own production operation adoption, migration execution, and ceremonies.

## 9. D115–D131 and roadmap closure

| Decision | Final review result |
|---|---|
| D115 | One OS-authenticated per-user daemon, deterministic routing, discovery, and disabled-by-default authority listener remain consistent with M4. |
| D116 | Domain membership and all audited relation/cursor boundaries are explicit; migration refuses unresolved ownership. |
| D117 | Immutable owner, pin + mTLS, exact SAN binding, one-use grant, and owner ceremonies now have portable proof. |
| D118 | Same-key renewal, authorized rotation, lost-key replacement, and Environment-qualified Clone binding are deterministic. |
| D119 | Prefix/events/blobs remain truth; signed receipts/claims/proofs are protocol metadata and bundle closure is verifiable. |
| D120 | One immutable command, static delivery, complete footprint, no authority future queue, and cursor dependency now agree. |
| D121 | Claim grant is authority- and artifact-signed; pin-before-use and exclusion remain unchanged. |
| D122 | Contiguous return/quarantine remains unchanged; cross-stream cursor dependency cannot skip a refused birth. |
| D123 | Idempotency/range reconstruction remains; only a declared deterministic no-op has null success range. Portable receipt does not alter identity. |
| D124 | Event metadata/order remains unchanged; D127 and D130 now have exact event/acted-at shapes. |
| D125 | Cursor is evented and Environment-owned; provisional targeting is a dependency, not an illegal provisional footprint. |
| D126 | Release remains receipt-barrier based; stand-down now additionally requires owner authorization and privacy-safe reason evidence. |
| D127 | Versioned collision success, suffix, output, repair events/state, and clear operations are deterministic without changing v1. |
| D128 | Stable snapshots, lane serialization, reseed preservation, and provenance remain implementable by M3/M4. |
| D129 | Source-signed handoff, owner-attested restore, portable bundle closure, and historical key verification close promotion ambiguity. |
| D130 | Coupling graph, grouping, Environment binding, synthetic event, proof, and rollback fence are deterministic or explicitly refuse. |
| D131 | Product truth, security audit, routine logs, metrics, command-payload compaction, and exact-reason retention are separated. |

Q01–Q25 remain covered by the Step 2–8 products plus this record. The eight
conformance agreement families remain exactly: accepted command, refusal,
same-ID retry, lost-response receipt query, read snapshot, blob upload, claim
cycle, and incompatible versions/reseed. Step 9 adds review assertions and a
portable-signature golden product; it does not add a ninth family or substitute
prose for an existing executable family.

## 10. Seal

The complete M2 contract now has no deferred Step 9 decision, no known
cross-contract contradiction, and no obligation that requires M3 or M4 to
invent a protocol boundary. The conformance index binds this record and its
vectors, maps every D115–D131 and Q01–Q25 item, and keeps all V01–V22 exits.
Production behavior remains unchanged. **M2 is sealed for M3 and M4.**
