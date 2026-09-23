# M2 Step 4: framing, transport security, and execution controls

Status: normative protocol design for M2 Steps 1–3. This contract closes Q04,
Q05, Q18–Q21, and the Step 4 portions of Q23/Q24. It defines records and trust
boundaries that later implementations must conform to. It does not add a
daemon, listener, store, migration, or CLI behavior.

The key words **MUST**, **MUST NOT**, **SHOULD**, and **MAY** are normative.
Stable problem codes are lowercase dotted tokens. Presentation text is never a
protocol decision input.

## 1. Layers and fixed invariants

The protocol has three byte domains:

```text
semantic command
  -> wipd.command/1 deterministic CBOR
  -> wipd/request-hash/v1 digest
  -> command.submit payload
  -> wipd.frame/1 record
  -> HTTP/2 DATA
```

Only the first two arrows define command identity. A frame can be split across
HTTP/2 DATA frames, retransmitted on another connection, assigned another
transport request ID, or rejected by a parser without changing the canonical
command bytes or `request_hash`. Compression is forbidden at the application
and HTTP content layers. HTTP/2 header compression is transport-internal and
has no semantic effect.

`command.submit` carries the exact canonical command bytes as a CBOR byte
string plus the asserted `request_hash`. The receiver MUST:

1. enforce frame and command byte limits before semantic decoding;
2. decode the closed identity schema, reject non-deterministic or unknown
   identity fields, and validate its typed operation schema;
3. recompute the Step 2 hash from the exact validated canonical bytes and
   compare it in constant time; and
4. compare the hashed domain, epoch, and Environment fields with the
   authenticated connection before handing the command to admission.

Re-encoding the surrounding frame or changing transport metadata MUST NOT
re-encode, normalize, or mutate the embedded command. The protocol version,
transport request ID, deadline, endpoint, peer credential, certificate, and
HTTP/2 stream are excluded from command identity exactly as
`canonical-semantic-identity.md` requires.

## 2. Transport profiles and session establishment

Both profiles carry the same records and route to the same future handlers.
Neither profile grants authority merely because a connection exists.

### 2.1 Local profile

The local profile is HTTP/2 prior knowledge (h2c) over a reliable local stream:

- Unix-like systems use an `AF_UNIX` socket inside a daemon-owner directory
  with mode `0700`; the socket is mode `0600` and rejects symlinks and a socket
  owner different from the daemon owner.
- Linux obtains `SO_PEERCRED`; macOS/BSD obtains the platform peer effective
  UID; Windows uses a named pipe restricted to the daemon owner's SID and
  verifies the impersonated client token. A request/header field is never a
  peer credential.
- The verified effective UID/SID MUST equal the daemon owner's identity. Root,
  an administrator, a group member, a container label, and a matching username
  string receive no implicit exception.
- Peer verification happens before HTTP parsing, capability exchange, store
  access, route resolution, or logging of caller-asserted identity. A mismatch
  closes the stream and has no application-state effect.

The daemon obtains an exclusive, atomically created per-user process lock
before publishing the socket. The lock records diagnostic process data but PID
existence alone neither authenticates a peer nor proves a stale lock. Platform
activation adapters race through the same lock. A loser MUST exit without
unlinking or replacing the winner's socket. The implementation and lifecycle
tests belong to M4; this step fixes the conformance boundary.

### 2.2 Remote profile

The remote profile is HTTPS over HTTP/2 with TLS 1.3. TLS 1.2, plaintext
fallback, HTTP/1 downgrade, redirects, alternate origins, and opportunistic
trust-on-first-use are forbidden. The negotiated ALPN MUST be `h2`.

An authority profile contains, as separate values:

```text
origin: https origin with an exact host and port
domain_id: canonical domain ULID
authority_epoch: positive uint64
authority_spki_pin: sha256 of the server SubjectPublicKeyInfo
owner_root_spki: sha256 of the immutable owner-root SubjectPublicKeyInfo
```

The client validates all of the following: ordinary certificate validity and
server-auth EKU; hostname SAN against the configured origin; exact SPKI pin;
and the Step 9 critical canonical `wipd://authority/...` URI SAN containing the
configured domain ID, authority epoch, and owner-root SPKI digest. A DNS match,
public CA chain,
route name, or profile label cannot replace the pin and binding. Pin or epoch
change requires an authenticated enrollment/handoff update; it is never
learned from the failing connection.

The authority requires a client certificate with client-auth EKU and the Step
9 critical canonical `wipd://environment/...` URI SAN containing:

```text
domain_id, authority_epoch, owner_root_spki, environment_id
```

It verifies the domain owner chain, proof of the private key in the TLS
handshake, validity interval, current revocation state, and exact equality with
the active domain/epoch. For every exchange it then requires the certificate's
domain and Environment ID to equal the corresponding canonical command fields.
Queries and transfer requests are scoped to the same authenticated domain even
when they have no command identity. Hostname, local path, Clone label,
connection profile, semantic actor, and a copied certificate string are not
Environment identity.

TLS MUST terminate in the authority process that performs these checks. A
reverse proxy may pass bytes only if it preserves end-to-end TLS; a header that
asserts a client certificate is not conforming.

### 2.3 Enrollment, renewal, rotation, and revocation

The protocol floor for generic enrollment is an operator-issued, signed,
one-use grant bound to one domain, immutable owner-root digest, scope, expiry,
grant ID, and requested Environment public-key digest. Enrollment validates the
grant and CSR, atomically consumes the grant ID, and returns a certificate only
for that key and domain. Reuse, wrong scope/owner/domain/key, expiry, or an
already consumed ID refuses before certificate issuance. Grants and bearer
material never appear in ordinary command/query frames.

Enrollment uses `POST /wipd/v1/enroll` after pinned server authentication but
before client-certificate authentication. Its bounded `enrollment.request`
contains exactly one grant/OIDC credential as an opaque byte string, one
PKCS#10 DER CSR, and an optional prior Environment ID only for an authorized
rotation. `enrollment.issued` returns the DER certificate chain and its binding
values. The authority persists grant/JTI consumption and the issued certificate
atomically. A response-lost retry with the same credential and CSR digest may
return that same certificate; another CSR refuses as `auth.grant-consumed`.
`POST /wipd/v1/renew` requires current mTLS and a same-key CSR. Both endpoints
use the bootstrap 65,536-byte frame limit and never accept commands or queries.

Amp enrollment substitutes a thread-scoped OIDC assertion for the operator
grant only after validating issuer/JWKS, audience, expiry, token use, one-use
`jti`, exact owner `user_id`, `thread_id`, and required project/workspace
claims. The exchange consumes the `jti`; no reusable Amp secret is persisted.

Renewal uses the current mTLS identity and a CSR for the same public key, so it
preserves the Environment ID. Planned key rotation proves the old key or uses a
new operator-authorized one-use grant. Lost key/state creates a new Environment
identity. Revocation is checked at connection establishment and before every
new exchange on a retained connection. Revocation stops new work but never
rolls back an already submitted command or closes a claim. Promotion to a new
authority epoch invalidates old-epoch authority and Environment certificates.

Step 9 closes enrollment message schemas, standard SAN encoding, one-use
consumption, and ceremony proofs. Registry persistence and production
ceremonies are implemented in M3/M5/M6.

## 3. HTTP and negotiation

The authority listener is disabled unless explicitly configured. It exposes
only:

- `GET /healthz`, returning process readiness without domain, epoch, store,
  owner, Environment, grant, claim, receipt, or high-water data; and
- `POST /wipd/v1/enroll`, with pinned server TLS and a one-use credential, and
  `POST /wipd/v1/renew`, with current mTLS, as specified in §2.3; and
- `POST /wipd/v1/negotiate` and `POST /wipd/v1/exchange` over an authenticated
  connection.

HTTP cookies, bearer authorization, URL query parameters, redirects, server
push, and semantic/authentication headers are forbidden. HTTP status reports
only whether an exchange stream could be established; protocol problems use
records once record exchange is safe. An unauthenticated remote or local peer
receives no domain-sensitive record.

The first application exchange on a connection MUST be `negotiate`. It carries
the Step 3 `ClientHello` and `ServerHello` logical messages in `wipd.frame/1`
records. Before selection, the frame has no implied protocol version. A
successful server response additionally carries these effective session
parameters:

```text
frame_schema:            "wipd.frame/1"
max_frame_body:          uint32, at most 1,048,576
max_chunk_data:          uint32, at most 65,536
max_stream_bytes:        uint64, at most 1,099,511,627,776 (1 TiB)
max_concurrent_exchanges:uint16, at most 64
receive_window_bytes:    uint32, at most 16,777,216
```

The bootstrap `ClientHello` and refusal frame body limit is 65,536 bytes.
Implementations MUST support the defaults 1,048,576 frame bytes, 65,536 chunk
bytes, 8 GiB per stream, 32 concurrent exchanges, and a 1 MiB receive window;
the authority MAY advertise lower values. Values above an absolute maximum are
invalid capabilities, not permission to allocate more memory.

The session is usable only after the Step 3 protocol, operation, identity, store
schema, and feature intersection succeeds. `wipd.frame/1` is a required
feature. A command names an exact advertised operation/identity-schema pair;
there is no post-frame version fallback. A connection is invalidated by an
authority epoch or authenticated domain change and MUST renegotiate.

## 4. Record format

Every HTTP/2 request or response body is a sequence of records:

```text
+----------------------+------------------------------+
| body_length (u32 BE) | body (body_length bytes)     |
+----------------------+------------------------------+
```

`body_length` counts only `body`. Zero is invalid. A parser reads the four-byte
prefix into fixed storage, checks it against the effective limit, and only then
allocates/reads the body. A partial prefix or body waits for more bytes without
semantic side effects. End-of-stream with a partial record is
`protocol.truncated-frame`. An oversized length resets only that HTTP/2 stream
with `protocol.frame-too-large`; the declared body is not drained into memory.

The body is deterministic RFC 8949 CBOR under the Step 2 scalar restrictions
and this closed map:

```text
Frame {
  "schema":     "wipd.frame/1",
  "request_id": canonical ULID,
  "sequence":   uint64,
  "kind":       lowercase dotted token,
  "payload":    definite-length byte string
}
```

All fields are present. Unknown fields, duplicate keys, tags, indefinite
values, non-shortest integers, and non-deterministic key order are malformed.
`payload` is one complete deterministic-CBOR logical message selected by
`kind`. A `stream.chunk` payload is the closed map `{offset: uint64, data:
bytes}`; its content offset and total were declared by the preceding
stream-start message. Empty logical payload is the deterministic encoding of an
empty map, not absent bytes.

The sender chooses a fresh `request_id` for each attempt. Every record in the
HTTP/2 exchange has that ID. Reconnecting or retrying uses a new transport ID
while retaining the canonical `command_id` and `request_hash`. Thus transport
correlation is never semantic command correlation. `sequence` starts at zero
and increments by one independently in each direction. A gap, duplicate,
regression, mismatched request ID, wrong-direction kind, or frame after a final
record terminates the exchange without being interpreted out of order.

## 5. Exchange kinds, ordering, and multiplexing

The first client record on `/exchange` is exactly one of:

| Kind | Required payload | Boundary |
|---|---|---|
| `command.submit` | `CommandSubmit` below | One immutable command attempt. The embedded bytes own all semantic identity. |
| `query.request` | `QueryRequest` below | Read-only request. It has no command ID, receipt, or mutation semantics. |
| `receipt.query` | command ID and request hash | Reserved integration point for Step 5, which owns its payload and response schema. |
| transfer/claim kinds | selected feature plus kind-specific start payload | Reserved for Steps 6–7; Step 4 supplies only framing, limits, authentication, and cancellation. |

The two Step 4 request messages are closed maps with every field present:

```text
CommandSubmit {
  "schema": "wipd.command-submit/1",
  "canonical_command": byte string,
  "request_hash": canonical sha256 text,
  "deadline": canonical UTC RFC3339Nano text or null
}

QueryRequest {
  "schema": "wipd.query-request/1",
  "query": {"name": lowercase dotted token, "version": positive uint16},
  "query_payload": byte string containing that query version's deterministic CBOR,
  "deadline": canonical UTC RFC3339Nano text or null
}
```

Query payload bytes are deterministic for interoperable decoding but do not
acquire command identity, receipt, or replay semantics. Step 6 defines each
query version's fields, consistency/snapshot policy, and output. Unknown fields
in either outer message reject under the negotiated protocol rather than being
silently copied into the semantic payload.

After the first client record, the only generic client record is
`control.cancel` with an empty-map payload. The request side remains open until
the server sends a final record or the client cancels/resets it. Generic server
records are `submission.accepted`, `response.start`, `stream.chunk`,
`response.end`, and `problem`; a kind-specific later contract may narrow this
set. Negotiation uses `client.hello`, `server.hello`, `session.parameters`, and
`problem`. Enrollment uses `enrollment.request`, `enrollment.issued`, and
`problem`. A kind in the wrong endpoint, direction, or state is
`protocol.unsupported-kind` or `protocol.out-of-order` as applicable.

The server responds on the same HTTP/2 stream and request ID. It may send a
bounded `response.start`, zero or more ordered `stream.chunk` records, then one
`response.end` or `problem`. A streaming start declares content kind, exact
total byte length, and digest when the owning later contract requires one.
Chunks are contiguous: the first offset is zero, each next offset equals the
previous offset plus data length, and final length equals the declaration.
Step 6 owns manifest/digest meanings and resumable transfer, not this step.

Responses from different HTTP/2 streams may interleave and complete in any
order. Records within one direction of one exchange never reorder. No response
for one request ID may appear on another stream. There is no server-initiated
application exchange or server push in protocol major 1.

Query execution is snapshot-stable once the owning read layer starts it, but
the snapshot token, as-of/provenance fields, pagination, and query catalogue
belong to Step 6. Commands and queries do not share ordering merely because
their HTTP/2 streams were opened in an order. D128's per-domain serialization
of pull/return/write and stable reads is an execution-layer scheduler rule; a
frame does not claim the lock or establish authority order.

The local execution controller MUST nevertheless expose one per-domain lane.
At reachable command start it holds that lane across the Step 6-owned sync
barrier: return the eligible pending prefix first when one exists, install the
returned receipts/event tail atomically, rebuild the overlay, otherwise install
the pulled tail directly, and only then hand the new command to guard/admission.
Reads acquire a stable snapshot from the same controller. This fixes D128's
ordering dependency without defining Step 6 message bodies, persistence, or
the eventual scheduler implementation.

## 6. Limits, backpressure, and parser behavior

HTTP/2 per-stream and connection flow-control windows are the only byte-credit
mechanism. The local profile uses HTTP/2 for the same reason. A receiver MUST
stop issuing window credit when its bounded per-exchange queue is full and MUST
not read into an unbounded staging buffer. The effective receive window is the
negotiated value; aggregate connection buffering is bounded by that value and
the negotiated concurrent-exchange count.

The rules are:

- control data MUST fit in one frame; it cannot evade `max_frame_body` by being
  split into several logical fragments;
- `stream.chunk` data length MUST NOT exceed `max_chunk_data`;
- a stream start whose declared total exceeds `max_stream_bytes` is refused
  before any chunk is accepted;
- bytes beyond the declared total, noncontiguous offsets, digest/length
  disagreement, or end before the declared total terminate that exchange;
- opening more than `max_concurrent_exchanges` is refused as
  `transport.overloaded` before submission; clients retry with bounded jitter
  and a new request ID;
- application and HTTP content compression are rejected; and
- malformed input never contributes a command, query result, receipt, journal
  entry, store mutation, or log payload.

HTTP/2 stream reset releases bounded transport resources. It does not mean the
command was cancelled or rolled back. A connection-level parser/auth failure
may close the connection; one well-formed exchange's semantic refusal does not.

## 7. Deadlines, submission, cancellation, and uncertainty

A deadline is an optional canonical UTC RFC3339Nano timestamp in the first
request payload. It is transport/execution budget, not `acted_at`, authority
time, command identity, guard input, or fold order. Clients also enforce local
connect, TLS, negotiation, and idle timeouts. The server checks a deadline
before submission and SHOULD stop pre-submission work when it expires. Clock
skew can cause conservative early refusal, so a caller needing deterministic
proof uses the explicit outcomes below rather than inferring from elapsed time.

Step 5 owns the exact durable submission point for each delivery class. Step 4
defines its transport consequences:

```text
received/validating --cancel or deadline wins--> not submitted
received/validating --owning layer commits-----> submitted
submitted ----------cancel/deadline/disconnect-> command continues;
                                               only waiting stops
```

The server emits `submission.accepted` only after the owning layer reports that
Step 5/7's durable boundary was crossed. The acknowledgment is evidence of
submission, not a terminal receipt, success, fold, or permission to reexecute.
The exact acknowledgment payload and receipt link are finalized in Step 5.

Before submission, an explicit client cancel, expired deadline, overload, or
validated refusal to hand off can return one of these stable no-effect codes:

- `transport.cancelled-before-submission`
- `transport.deadline-before-submission`
- `transport.overloaded`
- `transport.unavailable`

Only an explicit server response from the authenticated handler, or local proof
that no command byte was written to any handler, establishes definitely
unsubmitted `unavailable`. Sending any command bytes and then losing the stream
before such proof creates uncertainty; absence of `submission.accepted` is not
proof of absence.

After submission, cancel, deadline, HTTP/2 reset, caller exit, certificate
expiry/revocation, and connection loss cancel only the wait. They MUST NOT
abort, compensate, delete, dequeue, reuse the command ID, close a claim, erase
a journal entry, or report rollback. The local result is
`transport.wait-cancelled` if the caller deliberately stopped waiting, and
`outcome-unknown` for an authority command whose terminal outcome cannot be
proved. The caller blocks dependent commands and uses Step 5 receipt
query/same-ID retry. A non-authority command keeps the Step 7-owned
pending-return state. These are protocol states outside M1 `ResultCode`.

A query can end as `query.cancelled` because it has no mutation or receipt.
Cancellation may release its snapshot resources. It does not alter the as-of
or provenance of any response already emitted.

## 8. Authentication, signing, and field treatment

Protocol major 1 adds **no application-level command or query signature**.
TLS 1.3 server authentication, client-certificate proof, and the handshake
transcript cryptographically bind remote bytes to the authenticated channel;
local OS peer credentials bind the local stream. TLS termination at the
authority handler is therefore mandatory. Adding a per-frame signature would
not replace canonical hash recomputation or Step 5 replay state and would
create a second, unnecessary identity surface.

Certificate/grant signatures and the TLS handshake are still signatures. The
following table states every field class explicitly:

| Field/class | Authority recomputes or validates | Authenticated binding | Signed/protected | Logging rule |
|---|---|---|---|---|
| Canonical command bytes and `request_hash` | Closed-schema validation and Step 2 hash recomputation | Hashed domain, epoch, and Environment must equal authenticated values | Integrity/confidentiality in TLS; no app signature | Command ID is routine; full hash only restricted diagnostic/audit |
| Domain ID, authority epoch, owner root | Exact active-state check | Server pin/binding and Environment cert must agree | Certificate signatures plus TLS | Verified values allowed; caller claims use `claimed_*` names or are omitted |
| Environment ID/public key | Registry, cert validity, revocation, epoch | Certificate and canonical command/query scope | Client-certificate proof in TLS | Verified Environment ID allowed; no key/cert bytes |
| Operation name/version and typed input | Exact advertised definition and typed schema | Context IDs checked inside authenticated domain | TLS only; semantic bytes covered by request hash | Operation allowed; input/payload forbidden |
| Command ID and Environment sequence | Shape/current stream rules; Step 5 owns uniqueness/replay | Canonical command plus authenticated Environment | TLS only; command ID is inside request hash | Allowed as operational identity |
| Actor, acted time, causal/correlation IDs | Vocabulary/shape and later authority rules | Semantic attribution is not authentication | Inside request hash; TLS in transit | Actor omitted by default; IDs/time allowed when needed |
| Claim ID/epoch, context IDs, blob refs | Authority/domain/claim/schema checks; blob bytes later verified | Exact domain/Environment where required | Inside request hash; TLS in transit | IDs policy-controlled; blob digest/length omitted by default |
| Protocol/request ID/deadline/frame sequence | Frame validation | Connection and HTTP/2 stream | TLS remotely; OS-authenticated local stream | Request ID, version, byte counts, duration allowed |
| Results/events/receipts | Owning schemas validate/assign | Contracts bind domain/epoch/command | Step 9 portable artifacts sign receipts and transfer products, not frames/events | Stable outcome/problem/range allowed; payload forbidden |

No intermediary, log entry, asserted hash, route, or certificate header may
substitute for these validations. Step 9's portable artifact signature covers
receipts, grant descriptors, manifests, and handoff/migration products; it does
not sign commands, queries, frames, local journals, or authority events.

## 9. Stable framing/authentication problem boundary

These codes are stable classifications, not terminal command dispositions:

| Code family/examples | Meaning and effect |
|---|---|
| `auth.local-peer-mismatch`, `auth.authority-pin-mismatch`, `auth.authority-binding-mismatch`, `auth.environment-certificate-required`, `auth.environment-domain-mismatch`, `auth.environment-revoked`, `auth.grant-invalid`, `auth.grant-consumed`, `auth.owner-attestation-invalid` | Authentication/enrollment/owner authorization failed before application admission. No command/query/store effect. Sensitive detail is not returned to an unauthenticated peer. |
| `protocol.invalid-frame`, `protocol.truncated-frame`, `protocol.frame-too-large`, `protocol.chunk-too-large`, `protocol.stream-too-large`, `protocol.stream-offset`, `protocol.out-of-order`, `protocol.wrong-correlation`, `protocol.unsupported-kind` | Record cannot be safely interpreted under the negotiated protocol. The exchange resets; submission status follows §7 rather than the parser code alone. |
| `protocol.incompatible-version`, `protocol.invalid-capabilities`, `protocol.malformed-message`, `protocol.unsupported-extension`, `operation.unknown`, `operation.unsupported-version` | Exact Step 3 compatibility outcomes, unchanged by framing. No submission. |
| `transport.overloaded`, `transport.unavailable`, `transport.cancelled-before-submission`, `transport.deadline-before-submission` | Authenticated handler proves no submission. No terminal receipt is implied. |
| `transport.wait-cancelled`, `outcome-unknown` | Waiting ended after possible/known submission. Neither means semantic failure or rollback. |
| `query.cancelled` | Read stopped with no mutation. |

Once semantic admission begins, M1 result dispositions and the Step 5 terminal
response/receipt schema own rejection, refusal, failure, and success. A
transport problem MUST NOT be converted into `result.refused`; a lost response
MUST NOT be invented as `result.failed`.

## 10. Redacted logging and retention

Logging is an allowlist projection, never serialization of a frame, command,
query, error object, certificate, or context. Routine structured records MAY
contain only:

```text
timestamp, level, event, stable_code, transport, selected_protocol,
request_id, verified domain_id/authority_epoch/environment_id,
command_id, operation name/version, sequence, frame/chunk/total byte counts,
duration bucket, submission_state, terminal disposition, event-range IDs
```

Unverified caller claims are omitted or explicitly prefixed `claimed_`; they
must never be rendered as verified identity. Free-form decoder/TLS/library
errors are mapped to stable codes before logging because they can echo input or
credentials.

The following are forbidden in logs, traces, crash annotations, metrics
labels, and error strings: canonical command bytes; typed arguments or query
filters; titles, bodies, locators, content or blob bytes; raw paths/remotes;
HTTP headers; grants, OIDC assertions/claims, cookies, tokens, private keys;
certificate/CSR bytes; payload-derived presentation messages; and raw socket
buffers. Actor tokens, context/claim IDs, blob digests/lengths, certificate
fingerprints, owner-root pins, and full `request_hash` are omitted from routine
logs and may appear only in an access-controlled audit/diagnostic sink enabled
by explicit owner policy. Secrets and payloads remain forbidden there.

Routine transport logs default to 30-day retention. Authentication/security
audit metadata defaults to 90 days. Restricted command-hash diagnostics
default to 7 days. Operators MAY shorten these periods; extending them requires
an explicit owner policy and applies the same field allowlist. In-memory rings,
exported traces, support bundles, and test failure output obey the shortest
applicable period. Metrics use bounded categorical labels, never command,
request, Environment, user, payload, digest, or certificate identifiers, and
never participate in admission or correctness.

## 11. Decision and question traceability

| Source | Step 4 decision | Trust/recompute/bind/sign/log consequence | Later-owned integration or non-goal |
|---|---|---|---|
| **D115 / Q18** | OS credential equality before parsing; owner-only socket; atomic singleton; h2c local profile; no header auth or direct-store path | Peer UID/SID is kernel-bound and not logged as semantic actor; mismatch has no state effect | M4 implements process/socket lifecycle and direct-store isolation |
| **D117 / Q19** | TLS 1.3 pinned HTTPS, owner/domain/epoch server binding, domain/epoch Environment mTLS, one-use enrollment, renewal/rotation/revocation rules | Cert chains and TLS prove keys; command domain/Environment are independently compared; secrets are never logged | Step 9 fixes canonical URI SANs and ceremonies; M3 owns registries and M5/M6 implement them |
| **D120 / Q04** | One command per exchange; exact canonical bytes inside a frame; fresh transport request ID per attempt; ordered response on same stream | Authority recomputes hash and resolves metadata; frame fields do not alter identity | Step 5 owns admission/result/receipt; no current CLI change |
| **D122 / Q04–Q05** | Ordered, bounded record/chunk streams with no implicit replay/reorder | Sequence and contiguous offsets are validated; refusal cannot skip to a suffix | Steps 6–7 define journal return/fold and quarantine; framing alone does not advance a journal |
| **D123 / Q20–Q21** | Explicit pre-submission no-effect outcomes; post-submission cancellation stops waiting only; ambiguous loss is `outcome-unknown` | Hash remains immutable; no transport event proves rollback; dependent work blocks later | Step 5 fixes durable submission, idempotency, terminal receipts, and same-ID retry |
| **D124 / Q24** | Frame correlation is distinct from semantic causation/correlation and authority event order | Environment/sequence/acted time remain hashed and checked; authority assigns event ID/time later | Step 5/6 define result/event schemas and narration |
| **D128 / Q04–Q05** | HTTP/2 multiplexing with per-stream order, bounded flow control, and no command ordering inferred from stream order | Per-domain scheduler, not framing, serializes pull/return/write; limits prevent resource admission by assertion | Step 6 defines pull/return/snapshot/reseed content and preservation |
| **D131 / Q23** | Structured allowlist logs, payload/secret redaction, bounded retention, metrics excluded from correctness | Verified identities/outcomes may be logged; sensitive IDs/hashes require restricted policy | Step 6 adds provenance fields; Step 8 adds redaction checks; Step 9 performs privacy/security review |
| **Q05** | Fixed prefix, deterministic CBOR body, hard parser ceilings, declared bounded streams, HTTP/2 backpressure | Length checked before allocation; malformed/oversized input has no semantic effect | Step 8 adds independent parsers and fuzz/property suites |
| **Q24** | Recompute semantic hashes/definitions; bind domain/epoch/Environment to OS/mTLS identity; no app request signature | Classification is explicit in §8; logs are a separate projection | Step 9's portable product signatures are separate from requests and frames |

## 12. Deterministic vectors and later boundaries

`frame-security-execution-vectors.json` uses notation
`wipd.frame-security-vector/1`. It pins an exact length-prefixed negotiation
frame and diagnostic auth, limit/backpressure, cancellation, identity
independence, and redaction cases. JSON is vector notation only, never a wire
encoding or identity input. `internal/protocol/step4_vectors_test.go` validates
the fixture without providing a production parser or transport.

This step deliberately leaves these contracts to their owners:

- **Step 5:** exact durable submission point; terminal response and receipt
  schemas; success/rejection/refusal/failure; same-ID/hash idempotency; event
  ranges; receipt query; and whether receipts are durably signed.
- **Step 6:** seed/pull/return/fold, manifests, blob upload/hydration, transfer
  digests, snapshot/as-of/provenance, pagination, resume, and reseed execution.
- **Step 7:** claim acquire/grant/return/release, journal barriers, quarantine,
  and stand-down proof.
- **Step 8:** standalone schemas, all accepted/refusal/retry/read/blob/claim
  vectors, independent client/server doubles, parser fuzzing, and properties.
- **Step 9:** `design-security-privacy-review.md` closes certificate binding,
  operational retention, durable product signatures, owner ceremonies, and
  cross-contract consistency.

There is no production daemon, socket listener, certificate issuer, store
change, fresh authority state, migration, M3/M4 code, or current CLI behavior in
this step.
