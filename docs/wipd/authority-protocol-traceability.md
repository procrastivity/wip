# M2 Step 1: authority-protocol contract and invariant traceability

Status: design-only traceability artifact. This is the sequential prerequisite
for the remaining M2 steps. It maps the sealed M1 boundary and BDS-217's
D115–D131 constraints; it does not select an encoding, define frames, or
implement a protocol.

Step 9 seal note: historical “open” and “later” statements in the original Step
1 inventory describe that step's state, not unresolved M2 obligations. The
normative final closure is `design-security-privacy-review.md` and its owning
Step 2–8 contracts. In particular, v1 collision refusal is not retroactively
changed; D127 collision success is `matter.create@v2`.

The implementation checkout is branch `go` at commit
`a221387c15d6ed47c88a25c6bd44bf42a2586ffd` when this artifact was prepared.
The supplied M2 workplan was `m2-step1-workplan-context.md`. The D115–D131
source is the operative `MODEL.md` and authority design in the sibling
`wip-reboot` source set at commit
`f913226aae34e3640aaf2453c9d45dc1d3fc8fc5`; the clause summaries below retain
their decision numbers so this artifact remains reviewable without making that
source tree a build dependency.

## 1. Reading rules and scope

This record has four kinds of statement:

1. **M1 contract input** is an existing semantic type, metadata dimension,
   ownership rule, or preserved behavior. It is not silently widened here.
2. **D115–D131 invariant** is a settled authority constraint that later
   protocol work must make representable and testable.
3. **Traceability obligation** names the future protocol field, actor,
   boundary, state transition, or verification responsibility needed to carry
   the input or invariant.
4. **Open** is deliberately unresolved. An open item is not a protocol choice
   and must not be inferred from a convenient Go or transport library.

The contract has three separate layers:

```text
M1 semantic values and static metadata
    -> future canonical command/event identity bytes
    -> future transport frames and authenticated exchanges
```

- **Semantic contract:** `operation.Request`, its typed DTO, static
  `Definition` metadata, and `operation.Result` describe behavior. Go
  reflection, interfaces, `context.Context`, Cobra values, streams, paths, and
  store handles are not protocol fields.
- **Canonical identity bytes:** a later M2 step must define deterministic bytes
  for the immutable command identity and the resulting `request_hash`. This
  layer is named by D120/D123/D124 but is not specified by this record. It is
  independent of frame boundaries, stream chunking, connection profiles,
  retries at the transport layer, and presentation text.
- **Transport framing:** a later M2 step must carry commands, queries, results,
  receipts, blobs, seeds, pulls, returns, folds, and auth/session information.
  Framing can add protocol version, correlation, limits, and authentication
  material without changing semantic DTOs or identity bytes. This record does
  not choose a codec, envelope layout, streaming protocol, or security
  mechanism.

The words “field” and “message” below therefore mean a required future
contractual concept, not a prescribed wire spelling or serialized layout.

## 2. Exact M1 inputs

These are the inputs used; no current CLI census row is promoted into a new
operation schema merely by appearing in this list.

| Input | Exact source | What it contributes |
|---|---|---|
| Seeded roadmap and M2 exit contract | `m2-step1-workplan-context.md:1-39` | The required questions, nine-step order, non-goals, and eight named exit vectors. |
| M1 semantic operation contract | `docs/wipd/operation-contract.md:1-107` | The transport-neutral boundary, result dispositions, metadata dimensions, DTO coupling rule, and explicit M2 deferrals. |
| M1 request/result types | `internal/operation/contract.go:9-125`, `internal/operation/validate.go:9-124` | The exact envelope-shaped semantic values, validation rules, stable codes, and the fact that cancellation context is execution-only. |
| M1 metadata types | `internal/operation/metadata.go:10-100`, `:117-150`, `:164-243` | Access, delivery, tier context, guard/write footprints, blob declarations, claim requirement, and external-effect vocabulary. |
| M1 catalogue seed | `internal/operation/catalog.go:3-44` | The only registered definition: `matter.create@v1`, with its exact provisional footprint. |
| M1 operation census | `docs/wipd/cli-operation-census.md:1-44`, `:46-305`, `:307-386` | The 79 runnable paths, ownership/effect census, all current guard/write facts, scheduler omissions, and deliberately open split classifications. |
| M1 vertical slice | `docs/wipd/matter-create-slice.md:1-51` | The adapter/handler/storage boundary, preserved output/error behavior, one event owner, and the explicit absence of daemon, wire, and storage changes. |
| M1 focused tests | `internal/operation/contract_test.go`, `dispatcher_test.go`, `ownership_test.go` | Executable evidence for DTO coupling, metadata completeness, result namespaces, dispatcher validation, and one writesurface owner. |

### 2.1 M1 semantic DTO inventory

The following inventory is exact. It is the input boundary that later M2
schemas must preserve or explicitly version; it is not a proposed wire schema.

| M1 value | Fields and current meaning | Protocol trace |
|---|---|---|
| `operation.ID` | `Name string`, `Version uint16`; `name@vN` is diagnostic only. | Operation selector and semantic operation version. Its wire spelling and negotiation remain open. |
| `operation.Actor` | `human`, `role:<name>`, or `system:<source>`. | Semantic attribution. It must be related to, but is not interchangeable with, the authenticated Environment installation. |
| `operation.Context` | `Repo`, `Clone`, `Worktree` strings; already-resolved tier identities. | Required semantic tier dimensions. A definition decides which are required; paths and ambient discovery do not cross this boundary. |
| `operation.ClaimContext` | `ID`, `Epoch`; accepted only when metadata requires an exact claim. | Claim proof input for direct claimed-subtree commands. The authenticated proof/grant representation is open. |
| `operation.BlobInput` | `Name`, `Digest`, `Size`; staged content reference, never a path or stream. | Blob name/reference, digest, and declared length. Digest algorithm, manifest, upload, and hydration protocol are later work. |
| `operation.Request` | `Operation`, `Actor`, `Context`, optional `Claim`, typed `Input`, `Blobs`. | Semantic command contents. D120 requires additional command/authority context that M1 intentionally does not yet carry. |
| `MatterCreateInput` | `Title`, `Locator`. | The first typed operation payload. D127's requested/assigned locator and repair status are a later protocol/event gap, not added here. |
| `operation.ResultCode` | `result.succeeded`, `result.rejected`, `result.refused`, `result.failed`. | Semantic disposition only. `unavailable`, `pending-return`, and `outcome-unknown` are protocol outcomes, not new M1 result codes. |
| `operation.Problem` | Stable `Code`, presentation `Message`. | Problem code is machine-facing; message is not identity and must not be required for replay. Logging and redaction rules remain to be closed. |
| `MatterCreateOutput` | `ID`, `Locator`, `Title`. | Typed successful output. The authority/result/receipt envelope around it is later work. |
| `operation.Result` | `Code`, either typed `Output` or one `Problem`; namespace rules are validated. | Candidate terminal semantic result carried by a future response/receipt, with protocol uncertainty represented outside this value. |

`Handler` receives `context.Context` as an in-process execution argument. It is
not semantic request data and is not a future cancellation field. The reflected
`Definition.InputType` and `OutputType` are dispatcher checks, not a choice of
wire encoding.

### 2.2 M1 metadata inventory and `matter.create@v1`

Every definition must classify all eight dimensions. `nil` means omitted and
invalid; an allocated empty slice means explicitly “none.” That distinction is
an M1 completeness invariant and must survive catalogue/schema generation.

| M1 metadata dimension | `matter.create@v1` value | Future protocol responsibility |
|---|---|---|
| `Operation` | `matter.create@v1` | Select the semantic operation. Compatibility and negotiation are separate from this identity. |
| `Access` | `mutation` | Distinguish command admission from read/query behavior. |
| `Delivery` | `provisional` | Select ordering/exclusion semantics: provisional Matter birth plus its implicit claim; no disconnected authority event. |
| `RequiredContext` | `[repo]` | Carry and validate the required Repo identity; do not infer it from a path or remote. |
| `Guards` | `[repo.matter-locators]` | Authority evaluates the complete guard footprint rather than trusting a caller's classification. |
| `Writes` | `[newborn-matter-subtree]` | Bound the provisional write and prevent an unmarked pre-existing aggregate touch. |
| `BlobInputs` | `[]` | Explicitly no staged blobs for this operation. |
| `Claim` | `none` | No existing claim proof; the future provisional contract must still account for its implicit birth exclusion. |
| `ExternalEffects` | `[]` | No effect outside authority events/projections in the M1 slice. |

The census is broader than the catalogue. It identifies reads, mutations,
filesystem/Git/lock/tracker effects, scheduler APIs, and split operations so
later definitions can be complete. It does not itself define DTOs or make a
future operation registry. In particular, the census leaves composite
seal/render ordering, tracker credentials/effect placement, Backlog finding
classification, seal-time Batch sweep, and local harness policy open.

## 3. Protocol field traceability

### 3.1 Fields derived from M1 and fields required by D115–D131

| Future field/concept | M1 source or D source | Semantic / identity / frame role | Authority and trust treatment |
|---|---|---|---|
| Operation name and semantic version | `Request.Operation`; M1 contract | Semantic selector; candidate identity input. | Authority resolves the registered definition. Protocol-version negotiation must not silently change operation semantics. |
| Typed operation input | `Request.Input`, e.g. `MatterCreateInput` | Semantic value; canonical identity input after Step 2 defines its bytes. | Authority validates type/schema and guards. It must not trust a client-side projection or presentation form. |
| Actor token | `Request.Actor`; D120/D124 | Semantic attribution and event attribution; likely identity input, exact inclusion to be closed in Step 2. | Authority validates vocabulary and relates it to authenticated owner/Environment. A role/system token does not itself authenticate a human or installation. |
| Repo/Clone/Worktree context | `Request.Context`; D56, D118, D120, D124 | Required tier dimensions; candidate identity/envelope input. | Authority checks domain membership and event-specific required dimensions. A local adapter resolves ambient checkout identity before creating the semantic request. |
| Claim ID and claim epoch | `Request.Claim`; D120/D121/D126 | Semantic admission context and candidate identity input for claim commands. | Authority checks the current claim and epoch. A local lock is not a claim and cannot satisfy this field. |
| Staged blob name/digest/length | `Request.Blobs`; D119/D120/D123 | Semantic reference and canonical identity input; bytes are exchanged separately. | Authority verifies uploaded bytes against digest and length before fold. Raw paths, streams, and unverified caller claims are not accepted. |
| Complete guard/write footprint | `Definition.Metadata`; D120/D121 | Static operation contract, not caller-selected payload. | Definition/registry validation establishes a candidate; authority admission must enforce the actual complete footprint and classify cross-boundary work as `authority`. |
| Delivery class and ordering key | `Metadata.Delivery`; D120/D122 | Command control semantics, not operation payload. | Authority/environment journal state follows `authority`, `claim`, `provisional`, `capture`, or `environment` rules; no authority command becomes a future-execution queue. |
| Command ID | D120/D123 | New immutable command identity; candidate canonical identity input; stable across retry. | CLI allocates before IPC; authority makes it idempotency-unique and binds it to the request hash. Exact ID format is open. |
| Authority domain ID | D115–D120/D124 | Authority envelope and identity scope; not a Repo path or connection profile. | Route and authority validate it. Cross-domain native relations and ambiguous routing refuse. |
| Expected authority epoch | D115, D120, D126, D129 | Admission/fencing context; likely canonical command input. | Authority checks it against the active epoch; handoff/promotion changes it and invalidates old claims/certificates. |
| Acting Environment ID | D118–D120/D124 | Authenticated execution identity and event envelope; candidate identity input. | Bound to the authenticated Environment certificate/key and Clone natural key; Step 9 fixes the critical canonical URI SAN encoding. |
| Durable Environment sequence | D120/D124/D130 | Ordering and migration identity input; not authority fold order. | Authority verifies monotonic/contiguous rules per Environment where required. It is not replaced by wall-clock order. |
| `acted_at` | D120/D124/D130 | Narration/context time; candidate command identity input only if Step 2 says so. | Environment reports it; it never changes guard, authority timestamp, or fold order. Clock anomalies warn. |
| Causation and correlation | D120/D124/D130 | Semantic provenance and event/command identity context. | Authority validates direction/self-reference rules and preserves chains; migration sets deterministic values as specified by D130. |
| Canonical `request_hash` | D120/D123/D124/D129/D130 | Identity digest of future canonical command bytes; never a frame hash by assumption. | Authority recomputes it and rejects an assertion that does not match. Accepted events and receipts carry the resulting value. Algorithm/domain separation/covered fields are Step 2 questions. |
| Authority event ID and `occurred_at` | D119/D124 | Authority event envelope, not client command identity. | Authority assigns both and establishes fold order. Environments never mint authority events. |
| Required tier dimensions on events | D56/D124 | Event envelope; Batch-subject events have null Repo while domain is always present. | Authority derives/checks dimensions per event type; later schemas must make nullability explicit rather than guessing from a Repo filter. |
| Typed output and semantic problem | M1 `Result` | Semantic result payload; future terminal response/receipt content. | Handler/authority validates output type and problem namespace. `Problem.Message` remains presentation text. |
| Protocol outcome | D120/D123 | Frame/receipt control result outside M1 `ResultCode`: pending-return, unavailable, outcome-unknown, and terminal. | Transport/authority state determines it; no protocol uncertainty may be misreported as semantic refusal or success. |
| Receipt ID/hash/event range | D123/D129 | Authority protocol metadata and recovery evidence, not projection input. | Authority persists it transactionally and binds command ID, request hash, disposition, and accepted event range. Query/retry behavior is Step 5. |
| As-of, provenance, overlay/quarantine state | D119/D128/D131 | Read/transfer metadata, not mutation identity. | Authority/environment derives it from installed prefix/journal state. Reads must expose it rather than silently presenting overlay as folded truth. |
| Protocol version/capabilities | Workplan Q03/Q25; D128 | Transport negotiation, not semantic operation version or request identity unless later contract says otherwise. | Peers negotiate before accepting messages; incompatible version behavior and reseed boundary are Step 3. |
| Correlation/request transport framing | Workplan Q04/Q05 | Frame/session metadata; not automatically semantic correlation or canonical bytes. | Framing must preserve command identity across retries/chunks and enforce limits without changing DTOs. Step 4 owns the choice. |

### 3.2 M1 result versus protocol outcome

The distinction below is mandatory for later schemas and vectors:

| Situation | M1 semantic value | Protocol-level state to define later |
|---|---|---|
| Handler accepts and returns typed output | `result.succeeded` with `Output` | Terminal success/receipt, event range if accepted, and read provenance. |
| Request is malformed or semantically invalid | `result.rejected` with an allowed problem code | Terminal rejection; no model effects. |
| A valid intent fails a model guard | `result.refused` with `refusal.*` | Terminal refusal; D123 requires a persisted refusal receipt. |
| Unexpected execution/storage failure | `result.failed` with `internal.*` | Whether and how this becomes a terminal authority receipt versus a transport failure is a Step 5 contract question. |
| Command definitely did not reach an authority | No `Result` yet | `unavailable`; must not become deferred future execution. |
| Authority may have accepted it but response was lost | No `Result` yet | `outcome-unknown`; query/retry by command ID and hash, with dependents blocked until resolved. |
| Non-authority command is durably journaled but not returned | No `Result` yet | `pending-return`; return/fold/refusal/quarantine sequencing is governed by D122 and Step 6/7. |

Same ID with the same canonical hash must be replay-safe; same ID with a
different hash must refuse. This is a future receipt/idempotency contract, not
a new `ResultCode`.

## 4. Actors and trust boundaries

### 4.1 Actor inventory

| Actor | Authority in the design | What it may assert | What it must not be allowed to establish alone |
|---|---|---|---|
| CLI/client process | Constructs semantic input and allocates the command ID before local IPC (D120). | User intent, operation DTO, caller-observed time, staged references. | Domain ownership, authority epoch, request hash, event order, claim ownership, or a second store path. |
| Local Environment `wipd` | One per-user daemon; local IPC adapter, route resolver, journal/overlay owner, and client of the authority. | Authenticated Environment identity, durable sequence, local journal state, checkout-derived context. | Authority truth, authority event IDs, claim acquisition, or a direct-database fallback. |
| Authority `wipd` | One canonical authority per domain; owns event fold, projections, receipts, claims, epoch, and referenced domain blobs. | Admission, guard/refusal, canonical hash, event order, terminal outcome, read as-of. | Knowledge of unreturned remote work or local process liveness. |
| Owner/grant/enrollment control | Establishes domain owner trust, one-use grants, and approved Environment enrollment under D117. | Authorization to attach/create/handoff. | A reusable secret, a second human owner, or silent ownership transfer. |
| Environment certificate/key | Authenticated installation identity, not a user and not a semantic actor. | Proof of the installation acting for the domain owner. | A new domain, claim, event, or owner by possession of copied bytes. |
| Semantic human/role/system actor | Attribution token inside the operation/event model. | Who/what the command says acted. | Transport authentication or permission to cross the domain owner boundary. |
| Connection profile/route | Contact information and routing configuration. | Where a daemon attempts to connect. | Domain identity, authority promotion, or membership. |
| External tracker/provider and agent hooks | Effects identified by the census; not inbound authority truth. | Provider response or local hook result within its declared operation boundary. | Reconciliation or mutation of wip history without a wip command/event. |

### 4.2 Trust-boundary trace

1. **CLI → local `wipd`:** OS-peer authenticated local IPC and the per-user
   process singleton are required by D115. The daemon must treat all semantic
   values as input until validated; it must not open the store on the CLI's
   behalf through a second direct path.
2. **Local `wipd` → route/authority:** normalized Repo routing is provisioning
   state, separate from domain identity. Exact/longest-prefix/catch-all
   selection, equal-best/no-match refusal, discovered identity, and proven
   handoff are separate states.
3. **Environment ↔ authority:** pinned HTTPS plus domain-scoped Environment
   mTLS is the D117 floor for remote transport. A returned prefix, receipt,
   manifest, or event tail is untrusted until identity, epoch, continuity,
   hash, and length checks pass.
4. **Staged blob → authority fold:** a digest/length reference is not the
   bytes. Upload is idempotent by digest; authority verification precedes fold;
   only terminal acceptance promotes staged bytes to ordinary cache state.
5. **Authority → external provider:** tracker reads may guard outgoing writes;
   tracker state never becomes inbound wip mutation. Credentials and provider
   placement are not M1 DTO fields and remain a later ownership question.
6. **State → logs/presentation:** logs may key on identities and outcomes but
   redact payloads and secrets. Machine and human reads must expose authority
   reachability, as-of, overlay, and history conditions without claiming more
   freshness or liveness than the authority knows.

## 5. State-machine traceability

The labels below are behavioral states to preserve in later schemas and vectors;
they are not a choice of enum spelling or storage table.

### 5.1 Routing and domain identity

```text
unresolved route
  -> one exact/longest/catch-all route selected
  -> existing domain/epoch discovered and agrees
  -> Environment attached and ready

unresolved -> no-match/equal-best refusal
discovered -> disagreement refusal until proven handoff
history-empty domain -> explicit membership change -> immutable after first event
```

The profile, endpoint, domain, Repo, Environment, and authority epoch are
distinct identities. A routing change alone cannot promote an authority.

### 5.2 Command, journal, and outcome

```text
constructed
  -> locally authenticated / classified
  -> sent or durably journaled
  -> unavailable | pending-return | outcome-unknown
  -> returned contiguous prefix
  -> folded terminal receipt | refused terminal receipt
  -> cached / visible, or quarantined for explicit replace or abandon
```

Authority commands are never queued for future execution by an unavailable
authority. Claim, provisional, capture, and environment journals return one
command transaction at a time and stop at the first refusal. A refused head and
dependent suffix leave the active overlay for quarantine; journal commands are
immutable. The exact transition names, retry timing, and frame acknowledgments
are open.

### 5.3 Claim and Dispatch

```text
no claim
  -> authority acquire (existing Matter)
  -> grant returned: claim/epoch + as-of + event tail + blob manifest
  -> hydrating/held -> offline-ready and pinned
  -> active direct-write exclusion
  -> return barrier: all journal commands have terminal receipts
  -> closed; old epoch invalid
```

An acquisition transaction may atomically create/join the anonymous Batch and
open the Dispatch. Authority enforces one claim/Dispatch per Matter across the
domain. Normal release waits for the complete journal receipt barrier. A
cross-Environment stand-down requires the exact claim/epoch, reason, and an
explicit acknowledgment that unknown unreturned work may be lost. TTL,
heartbeat loss, certificate expiry, daemon loss, and local lock loss do not
close a claim.

### 5.4 Blob lifecycle

```text
declared reference -> staged bytes -> uploaded -> hash/length verified
  -> accepted receipt -> ordinary cache

manifest entry -> held-but-hydrating -> offline-ready | failed
```

Claim-required bytes must verify and pin before claim commands execute. Missing
authority bytes remain unknown; a local “hydrated” assertion is not authority
truth.

### 5.5 Authority epoch, handoff, and restore

```text
active/admitting
  -> quiescing (no claims, no unresolved outcomes)
  -> source relinquished + complete bundle verified
  -> destination active at next epoch

backup + candidate continuation
  -> strict prefix/blob closure verified -> promote
  -> conflicting branch, missing bytes, or incomplete closure -> refuse
```

An old-epoch command with neither a receipt nor recovered events is never
executed after disaster restore. It is explicit loss evidence requiring owner
attestation and abandon or a new ID after current-epoch guard/claim checks.

### 5.6 Read/snapshot state

Every persistence-sensitive read must state at least the domain, epoch, as-of,
and provenance. An authority read names authority as-of; an Environment read
names installed authority as-of and whether a provisional overlay is included.
Provisional and quarantined objects remain visibly distinct from folded truth.
Stable snapshot, pagination, token, and snapshot-pinning behavior is a later
read contract, not implied by this state trace.

## 6. Recomputed, bound, signed, and loggable fields

This table separates “the authority recomputes or assigns it” from “the
authenticated Environment is allowed to originate it.” It intentionally does
not claim that a field must be digitally signed; signature placement and
algorithm are unresolved M2 questions.

| Field/class | Authority recomputes or assigns | Must be bound to authenticated Environment/domain | Signing status left open | Logging posture |
|---|---|---|---|---|
| `request_hash` | Recompute from future canonical command bytes; reject mismatched assertions. | Bind to command ID, domain/epoch, and resulting event/receipt context. | Commands stay unsigned; Step 9's portable receipt wrapper covers the hash. Recompute remains mandatory. | Full hash is restricted audit data; never log raw payload as a substitute. |
| `command_id` | Enforce uniqueness/idempotency; do not replace on retry. | Bind to the authenticated submission context and receipt. | Signature coverage is open. | Safe identity key candidate; exact retention/access is open. |
| Domain ID and expected epoch | Resolve/check against route and active authority. | Route/profile must not substitute for domain identity. | Authority metadata/certificate binding is open in representation. | Log domain and epoch/outcome; do not log credentials or endpoint secrets. |
| Acting Environment ID and durable sequence | Verify identity and sequence; migration assigns deterministic sequence. | Certificate/key, domain, Clone natural key, and sequence must agree. | mTLS/certificate proof is required by D117; no Environment application signature is accepted. | Log Environment ID and sequence as operational identity. |
| Actor token | Validate M1 vocabulary and authority attribution rules. | Relate semantic actor to authenticated owner/Environment; `system:*` approval cannot admit another human. | Whether actor attribution is separately signed is open. | Log actor identity when safe; redact payloads and secrets. |
| Claim ID/epoch and grant as-of | Check current claim, epoch, exclusion, and as-of. | Bind to the Environment holding the claim and the domain. | Step 9 wraps grant descriptors and receipts in authority-signed portable artifacts. | Log claim ID/epoch and terminal outcome; never log private proof material. |
| Operation ID/version and typed arguments | Resolve definition, validate DTO, guards, and output. | Context identities must belong to the authenticated domain/Environment. | Canonical identity coverage is Step 2; no frame signature choice here. | Log operation identity and problem code; treat titles, bodies, and arguments as payload. |
| Blob digest/length and manifest | Verify bytes, length, references, and closure before fold/hydration. | Manifest and claim grant must bind to domain/as-of where required. | Step 9 wraps complete manifests in authority-signed portable artifacts. | Digests stay out of routine logs; raw blob bytes and secrets are always redacted. |
| Event ID and `occurred_at` | Authority assigns; event order is fold truth. | Domain and command context bind the event. | Event signing/sealing is open; Environment cannot mint an authority event. | Log event identity/range and outcomes, not payload bytes. |
| `acted_at` | Do not use for guards, authority time, or fold order; warn on anomalies. | Attribute to the acting Environment sequence. | Proof of clock source is open. | Log only as needed for narration/debugging; do not present it as authority order. |
| Causation/correlation | Validate no-forward/self-reference and migration grouping rules. | Bind to the command/event chain and domain. | Signature coverage is open. | Log chain IDs as identities; payload-bearing context remains redacted. |
| Receipt disposition/event range | Persist transactionally with accepted events or refusal. | Bind to command ID, hash, domain epoch, and authority high-water. | Step 9 requires an atomic authority-signed portable wrapper. | Log terminal disposition/range and problem code. |
| As-of/provenance/overlay/quarantine | Derive from installed prefix, authority state, and journal state. | Bind to the domain/epoch and reader Environment. | Read response integrity mechanism is open. | Always expose in machine results; human output flags snapshot/overlay/unreachable/history states. |
| Local lock/liveness | Environment derives a probe; authority never treats it as claim truth. | Bound only to local Run/Environment context. | No remote signature can turn a local lock into an authority claim. | Log probe result/cause without claiming process or claim closure. |
| Protocol/session/auth metadata | Validate at the transport boundary, not in semantic DTOs. | OS peer, pinned HTTPS, mTLS, grants, and any future session binding. | Exact credentials, signatures, token exchange, and frame binding remain open. | Never log private keys, bearer tokens, OIDC material, or endpoint secrets. |

## 7. D115–D131 clause coverage

The following is the complete clause-to-contract map. “Later closure” names the
M2 step that must turn the trace into a normative schema, vector, or review
finding; Step 1 records the obligation without doing that work.

| Decision | Settled invariant retained here | Protocol fields/actors/state affected | Recomputed, bound, signed, and logged implications | Later closure |
|---|---|---|---|---|
| **D115** | One per-user `wipd`, authenticated local IPC, singleton lock, no direct DB; routing is normalized and separate from domain/profile; discovery disagreement refuses; explicit grants govern creation/attachment; remote listener is disabled by default and health is stateless. | Route key, profile, domain ID/epoch, Environment identity, Repo identity, attach/create grant, local IPC and health exchanges. Actors: CLI, local Environment, authority, enrollment grant. States: route unresolved/discovered/attached/handoff. | Authority checks route/domain binding; local peer and remote auth bind the Environment; log route/auth outcome and identities, never secrets. | Step 3 compatibility/identity; Step 4 local IPC, HTTPS, mTLS, limits; Step 6 seed/attach; Step 9 review. |
| **D116** | One canonical authority per domain; membership changes only while history-empty; populated domains move as units; native relations never cross domains. | Domain ID, membership status, epoch, Repo/Batch/Run/dependency/reference/cursor target identities. | Authority recomputes membership and cross-domain guards; domain is the trust/backup unit; log refusal and domain IDs. | Steps 2, 5, 6, and 9; cross-domain refusal vector. |
| **D117** | One immutable owner root; Environment certificates are installations, not users; non-owner operations refuse; remote transport uses pinned HTTPS and domain-scoped mTLS; enrollment is one-use and scoped; Amp OIDC is thread-scoped; copied keys revoke. | Owner root, Environment certificate/public key, grant ID/use, issuer/audience/expiry/jti/owner/thread/project/workspace claims where applicable, profile/domain. | Authenticator binds Environment to owner/domain; Step 9 fixes URI SANs, one-use artifact grants, owner proofs, and retention. | Step 4 auth contract; Step 8 auth/refusal vectors; Step 9 security seal. |
| **D118** | Environment is domain-scoped; Clone natural key is `(domain, Environment, git common dir)`; same key renews, lost key/state creates new identity; hostname/path labels are mutable. | Environment ID, Clone ID/context, git-common-dir discovery, renewal/rotation proof. | Authority verifies binding; local discovery must not turn paths into domain identity; log stable IDs, not private key/path data by default. | Steps 2 and 4 identity binding; Step 9 rotation review. |
| **D119** | Authority owns complete events/projections/blobs/receipts/claims/epoch; Environment holds byte-identical authority prefix, overlay, journals, receipts, staged/cache blobs; only authority events enter local event table; seed manifest and lazy verified hydration. | Seed/as-of, event tail, blob manifest, receipts/claims metadata, overlay/journal/provenance, hydration state. Actors: authority and Environment. States: prefix installed, hydration held/ready/failed. | Verify byte identity, digest/length, claim-time pins; receipts/claims are metadata, not projections; log as-of/hydration and redacted outcomes. | Step 2 hash/identity; Step 6 seed/blob/hydration; Step 7 claim readiness; Step 8 vectors. |
| **D120** | One immutable command to local `wipd`; CLI allocates ID; command carries domain/epoch, Environment/sequence, acted time, actor/correlation, kind, targets/args/blob hashes, claim; authority folds; five delivery classes and complete footprints. | Command ID, operation, domain/epoch, Environment/sequence, actor/correlation, acted time, context/targets, arguments, blob refs, claim, metadata class/footprint. | Authority recomputes hash and actual guard/write admission; Environment/claim binding is mandatory; logs identity/outcome only. | Step 2 canonical command identity; Step 3 versions; Step 4 framing/execution; Step 5 results. |
| **D121** | Existing-Matter claim acquire is authority class and atomically handles anonymous Batch/Dispatch; returns claim/epoch, grant as-of, tail, complete manifest; one claim/Dispatch per Matter; exact claim guards direct writes; local lock is insufficient. | Acquire/grant, claim ID/epoch, Matter/Dispatch/Batch IDs, as-of, tail, manifest, exact claim context, footprint. | Authority recomputes exclusion and aggregate classification; Step 9 authority-signs the portable grant; log claim state/outcomes. | Step 7 claim protocol, with Step 6 transfer and Step 5 receipt barrier; claim-cycle vector. |
| **D122** | Claim/provisional/environment journal returns are contiguous prefixes, one command transaction at a time, stop at refusal; suffix quarantines; explicit replace/abandon only; capture is per entry; journals immutable. | Journal sequence, prefix/high-water, return/fold/refusal/quarantine/replacement/abandon controls, BacklogEntry key. | Authority verifies continuity and command immutability; Environment sequence is bound; log refused heads and quarantine reason without payload. | Steps 4, 6, 7, and 8; contiguous-prefix vector. |
| **D123** | One terminal receipt per command ID/hash; accepted events/receipt atomic; refusal receipt has no effects; same ID/hash replays receipt/range; different hash refuses; upload checks hash/length; unavailable differs from outcome-unknown; dependents block. | Receipt, command ID/hash, disposition, event range, blob upload/ack, query/retry, dependent barrier, high-water. | Authority recomputes hash/range or declared no-effect; Step 9 signs the portable receipt wrapper; log terminal outcome/range. | Step 5 results/receipts/idempotency; Step 6 blobs; Steps 8–9 vectors. |
| **D124** | Event envelope adds domain, command ID/hash, acted time, Environment sequence; authority event ID/occurred time remains fold order; narration preserves per-Environment sequences and merges by acted time then command ID; clock anomalies do not rewrite truth; Session is domain-wide. | Event envelope, causation/correlation, tier dimensions, authority order, narration metadata, session query scope. | Authority assigns event ID/time and validates sequence/chain; Environment supplies acted time; logs IDs/order diagnostics, not payload. | Step 2 identity/event schema; Step 6 reads/narration; Step 8 ordering vectors; Step 9 fidelity review. |
| **D125** | Cursor is evented, one per Worktree, Environment-owned, snapshot-visible; cursor command uses environment delivery; a provisional target has an explicit birth dependency. | Worktree/cursor ID, Environment ID, target identity, birth causation and sequence, snapshot/as-of. | Authority/environment verifies ownership and dependency ordering; local lock is unrelated; target identity is not routinely logged. | Step 9 cursor schema and V16 resolve Step 6's provisional-stream wording. |
| **D126** | Normal release waits for terminal receipts for full active journal; no automatic expiry; failure/loss/auth expiry does not close; cross-Environment stand-down needs exact claim/epoch, reason, loss acknowledgment; close records both Environments and invalidates old epoch. | Release/stand-down command, claim/epoch, reason, acknowledgment, acting/owning Environment IDs, receipt barrier, invalidated epoch. | Authority recomputes journal completeness and epoch invalidation; Step 9 owner attestation binds both Environments and reason digest; never routinely log reason text. | Step 5 receipt barrier, Step 7 claim lifecycle, and Step 9 owner ceremony. |
| **D127** | Provisional locator collision succeeds with identity-suffixed assigned locator, records requested/assigned values, sets nonblocking repair-required, and clears only by authority rename or explicit accept. | `matter.create` requested/assigned locator, final identity, repair-required status, rename/accept follow-up. | Authority chooses the Step 9 suffix, emits the repair event, and owns repair state; locators stay out of logs. | Step 9 fixes output/event/cursor schemas and vectors without changing M1 production types. |
| **D128** | Reachable command start pulls; pending work returns eligible prefix first; pull/return/write serialize per domain; reads use stable snapshots; version mismatch reseeds disposable base while preserving journals/receipts/staged blobs; overlay/provenance explicit. | Pull/return response, as-of, high-water, snapshot token, overlay/provenance, version mismatch/reseed, preservation set. | Authority validates prefix and as-of; Environment binds installed base and preserves immutable journals; log reseed/history conditions. | Step 3 compatibility/reseed; Step 4 sequencing/limits; Step 6 reads/transfer/snapshots; Step 8 read/reseed vectors. |
| **D129** | Handoff is quiescent and verified; bundle includes DB/receipts/claims/keys/grants/blobs/manifest but excludes secrets/routes/private keys; no automatic failover; restore needs strict prefix/blob closure; old unresolved command never executes; promotion starts new epoch and invalidates old trust; ahead clients preserve evidence. | Handoff state, source/destination/epoch, high-water/checksum manifest, receipt reconstruction, strict prefix, blob closure, promotion/loss/history-regressed. | Step 9 fixes source-signed bundle/relinquishment, owner handoff/death attestation, key fences, and destination activation. | Step 6 transfer/restore; Step 5 receipt reconstruction; Steps 8–9 vectors. |
| **D130** | Legacy host-store migration forms minimum coupling domains, preserves IDs/order, assigns Environment/sequence and command/correlation deterministically, hashes canonical pre-migration event bytes, emits migration binding, and forbids rollback after new writes. | Migration domain/Environment, correlation-origin command ID, acted time, sequence, canonical legacy event bytes, synthetic binding event, cutover state. | Step 9 fixes coupling/group refusal, synthetic binding, authority-signed proof, owner authorization, and rollback fence. | Step 2 migration identity bytes; Step 6 restore/transfer; Steps 8–9 vectors. |
| **D131** | Observability is split across work, doctor, daemon, authority, and journal repair; machine results carry domain/epoch/provenance/reachability/as-of/local base/pending/provisional; human output flags conditions; no guessed freshness/liveness; redacted logs; metrics never correctness. | Read result metadata, status/repair messages, log identity/outcome fields, freshness/overlay/history flags. | Authority derives reachability/as-of and never asserts unreturned work/liveness; Environment derives local lock/cache facts; no metric may be an admission input. | Step 4 logging/execution; Step 6 read schema; Step 8 observability/redaction checks; Step 9 privacy review. |

## 8. M2 questions and exit vectors to close later

The roadmap's questions are enumerated here so a later step cannot claim closure
by answering only a neighboring concern. Step 1 assigns ownership; it does not
answer them.

### 8.1 Question register

| ID | Question that must close | Owner step | Evidence required at M2 exit |
|---|---|---|---|
| Q01 | What is the canonical semantic serialization of a command and its typed DTO? | Step 2 | Published schema and deterministic bytes. |
| Q02 | Which scalar/collection/null/Unicode rules and domain separation define `request_hash`; which fields are covered or excluded? | Step 2 | Independent hash agreement and negative coverage cases. |
| Q03 | How are protocol version and operation version distinct, negotiated, and represented? | Step 3 | Capability/version exchange and incompatible-version behavior. |
| Q04 | What are command and query frames, correlation rules, response ordering, and multiplexing boundaries? | Step 4 | Frame schema and client/server agreement. |
| Q05 | What streaming, message/blob size, backpressure, and parser-limit rules apply? | Step 4 | Limit vectors and parser/stream behavior. |
| Q06 | How do semantic success, rejection, refusal, and failure map to stable terminal responses? | Step 5 | Accepted and refusal vectors with no ambiguity. |
| Q07 | What is the exact submission point and same-ID/same-hash versus same-ID/different-hash retry rule? | Step 5 | No-reexecution and mismatch-refusal vectors. |
| Q08 | What is the terminal receipt schema/query and event-range binding? | Step 5 | Lost-response receipt query vector. |
| Q09 | How are blob manifests, upload, idempotency, hash/length verification, and cache promotion represented? | Step 6 | Blob upload/verification vector. |
| Q10 | What is the seed envelope and complete-prefix/blob-manifest contract? | Step 6 | Seed/prefix agreement vector. |
| Q11 | What does pull return, at what as-of, and how does it interact with pending work? | Step 6 | Pull/return ordering vector. |
| Q12 | What do return and fold messages carry, and how are contiguous prefixes/quarantine expressed? | Steps 6–7 | Refused-head and dependent-suffix vector. |
| Q13 | What is the exact read/query response model, including provenance and stable snapshots? | Step 6 | One read snapshot vector. |
| Q14 | How are pagination tokens scoped, protected, expired, and bound to a snapshot? | Step 6 | Pinned pagination vector. |
| Q15 | How are claim acquire/grant fields, event tail, as-of, manifest, and hydration readiness represented? | Step 7 | Claim acquisition vector. |
| Q16 | How do claim journal return/fold and terminal-receipt barriers work? | Step 7 | Claim return/fold vector. |
| Q17 | How do normal release, cross-Environment stand-down, refusal boundaries, and epoch invalidation work? | Step 7 | Claim close/stand-down vector. |
| Q18 | What is authenticated local OS-peer authentication and singleton behavior? | Step 4 | Local auth/refusal checks. |
| Q19 | What is pinned HTTPS and domain-scoped Environment mTLS, including enrollment, renewal, revocation, and grants? | Step 4 | Remote auth/refusal checks. |
| Q20 | What are deadlines/timeouts and which cancellation is possible before submission? | Step 4 | Pre-submission cancellation vector. |
| Q21 | What can cancellation never mean after submission, journaling, acceptance, or fold? | Steps 4–5/7 | Post-submission cancellation vector proving no false rollback. |
| Q22 | What is the compatibility policy for unknown fields, incompatible changes, minimum/maximum versions, and store reseed? | Step 3 | Incompatible-version and reseed vectors. |
| Q23 | What is the privacy-safe logging/redaction and retention contract for fields, payloads, secrets, and digests? | Steps 4/9 | Redaction review and log assertions. |
| Q24 | Which fields are authority-recomputed, Environment-bound, authority-assigned, or additionally signed? | Steps 2/4/9 | Field classification table with security review. |
| Q25 | What canonical golden-vector notation and independent test-double agreement package is published? | Step 8 | Reproducible schemas, vectors, and cross-implementation results. |

### 8.2 Required M2 exit vectors

These include every vector named by the seeded workplan plus the negative and
authority-boundary cases needed to demonstrate D115–D131 rather than merely
mention them.

| ID | Required vector | Invariant it proves | Planned closure |
|---|---|---|---|
| V01 | Accepted command with typed output and terminal receipt | Semantic result is distinct from authority outcome; receipt/event binding works. | Steps 5 and 8. |
| V02 | Malformed/rejected command | `result.rejected` has stable code and no model effects. | Steps 5 and 8. |
| V03 | Valid intent refused by a guard | `result.refused` and refusal receipt are distinct from transport failure. | Steps 5 and 8. |
| V04 | Same command ID and same hash retry | Receipt/event range returns without re-execution. | Steps 5 and 8. |
| V05 | Same command ID and different hash | Authority refuses identity reuse with changed intent. | Steps 2, 5, and 8. |
| V06 | Definitely unsent command | `unavailable` is not a deferred authority queue. | Steps 4, 5, and 8. |
| V07 | Lost response followed by receipt query/retry | `outcome-unknown` resolves without duplicate execution and blocks dependents until resolved. | Steps 5 and 8. |
| V08 | One read snapshot | Domain, epoch, as-of, provenance, overlay policy, and stable snapshot are explicit. | Steps 6 and 8. |
| V09 | Pinned pagination across a changed underlying state | Page tokens cannot silently move between snapshots or domains. | Steps 6 and 8. |
| V10 | Blob upload, duplicate upload, bad hash, and bad length | Idempotent upload and verification precede fold/cache promotion. | Steps 6 and 8. |
| V11 | Seed plus lazy hydration and claim-required pinning | Prefix/blob manifest is complete; local hydration status is advisory until verified. | Steps 6–8. |
| V12 | Claim acquisition/grant, one claim command, return/fold, and release | One-Matter exclusion, exact epoch, contiguous journal, and terminal receipt barrier. | Steps 5–8. |
| V13 | Claim contention and cross-boundary command | Second claim and aggregate/cross-Matter footprint refuse; local lock cannot grant authority. | Steps 5, 7, and 8. |
| V14 | Cross-Environment stand-down with wrong epoch, missing reason, and accepted loss acknowledgment | D126 refusal boundaries and old-epoch invalidation are explicit. | Steps 7–9. |
| V15 | Provisional `matter.create` locator collision | D127 preserves final identity, records requested/assigned locator, and exposes repair-required. | Steps 2, 5, and 8. |
| V16 | Environment cursor move, including provisional target | D125 ownership and stream ordering are not confused with authority claim state. | Steps 2, 6, and 8. |
| V17 | Pull with pending journal, return refusal, quarantine, replace/abandon | D122/D128 sequencing and repair boundaries hold. | Steps 3, 6–8. |
| V18 | Protocol/operation incompatible versions and store reseed | Negotiation, deterministic refusal, and disposable-base preservation are compatible. | Steps 3 and 8. |
| V19 | Local peer refusal and remote owner/mTLS/grant refusal | D115/D117 trust boundaries reject unauthenticated or wrong-owner actors without state effects. | Steps 4, 8, and 9. |
| V20 | Planned handoff and disaster restore with conflict, missing blob, old unresolved command, and ahead client | D129 strict-prefix, complete-closure, no-old-execution, new-epoch, and history-regressed rules hold. | Steps 5–9. |
| V21 | Deterministic legacy migration grouping/hash | D130 preserves IDs/order and produces the specified migration binding without rollback. | Steps 2, 6, 8, and 9. |
| V22 | Log/observability redaction and provenance output | D131 exposes required state while omitting payloads/secrets and never claims false liveness. | Steps 4, 6, 8, and 9. |

The seeded M2 exit additionally requires independent client/server test doubles
to agree on every published vector, canonical determinism checks, parser/limit
fuzzing, and properties for hash identity, retry non-reexecution, pinned
pagination, blobs, and claim transitions.

## 9. Explicit M2 Step 1 non-goals

This artifact deliberately does **not**:

- define canonical serialization, scalar normalization, domain separation,
  digest algorithm, `request_hash` coverage, or golden-vector bytes;
- define command/query/receipt/blob/seed/pull/return/fold/claim wire frames or
  stream/chunk/size/backpressure behavior;
- implement local OS-peer authentication, pinned HTTPS, Environment mTLS,
  enrollment, OIDC, grants, signatures, or key rotation;
- implement receipts, idempotency, outcome recovery, blob upload/hydration,
  pagination, snapshots, claims, journals, handoff, restore, or migration;
- add fields to the M1 Go DTOs, register additional operations, change the
  dispatcher, change event envelopes, or move storage ownership;
- choose a daemon chassis, authority store, protocol library, or production
  client/server;
- resolve the census's split ownership/classification questions, including
  composite seal/render ordering, tracker credential placement, Backlog finding
  class, seal-time Batch sweep, or harness policy;
- initialize or mutate WIP state, Linear, tracker configuration, outbox,
  devbox/legacy stores, or any authority state;
- start M2 Steps 2–9 or implement M3/M4 behavior.

The only choices made here are traceability choices: M1 semantic values remain
distinct from future identity bytes, identity bytes remain distinct from future
transport framing, and every D115–D131 clause and seeded M2 question/vector has
an explicit later closure location or non-goal.

## 10. Unresolved design decisions carried forward

No item below is resolved by implication:

1. Canonical encoding format, field order, optional/null treatment, integer and
   Unicode normalization, map/collection ordering, domain separation, and the
   exact set of command fields covered by `request_hash`.
2. Whether operation/version negotiation data, auth-bound Environment data,
   protocol extensions, or acted time are inside canonical identity bytes or
   only outside them as framing/context.
3. Protocol version capability exchange, unknown-field policy, incompatible
   operation behavior, and store-version/reseed signaling.
4. Frame shape, multiplexing, correlation, streaming, chunk boundaries,
   deadlines, cancellation, backpressure, size limits, and parser failure
   behavior.
5. Exact local IPC mechanism and OS-peer credential checks; pinned HTTPS/mTLS
   profile, certificate/grant exchange, OIDC validation boundary, revocation,
   renewal, and whether any application-level signature is needed in addition
   to transport authentication.
6. Exact result/receipt schema, submission point, terminal state vocabulary,
   event-range encoding, response-loss recovery, and receipt authentication.
7. Blob digest algorithm, manifest form, upload/range protocol, cache
   promotion, hydration retry, and missing-byte behavior at each message
   boundary.
8. Query shapes, pagination token contents/protection/expiry, snapshot pinning,
   authority versus Environment as-of, and how provisional/quarantined objects
   are represented in each read.
9. Environment sequence allocation/recovery, journal persistence semantics,
   contiguous-prefix acknowledgments, quarantine repair, and replacement or
   abandon authorization.
10. Claim grant proof, claim/Dispatch message order, normal release barrier,
    cross-Environment stand-down proof, and cancellation semantics after every
    submission state.
11. D127's exact event/output shape for requested versus assigned locator and
    `locator-repair-required`, which M1 `MatterCreateOutput` does not contain.
12. Handoff/restore bundle framing, checksums/signatures, owner attestation,
    receipt reconstruction, migration proof, and history-regressed client
    behavior at the protocol boundary.
13. Logging fields, payload/digest sensitivity, retention/access, redaction
    tests, and the precise division between identity logs, audit evidence, and
    metrics.

Each decision must be resolved in the later M2 contract or explicitly marked a
non-goal before M2 is sealed. None is a license to add a provisional wire
format or implementation in this step.
