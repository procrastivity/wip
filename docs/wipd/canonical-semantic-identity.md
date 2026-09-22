# M2 Step 2: canonical semantic schema and identity

Status: specified and executable for the sealed M1 catalogue. The implementation
is `internal/operation/command_identity.go` and `canonical_cbor.go`; focused
vectors are in `command_identity_test.go`. This record closes M2 questions Q01
and Q02 and the identity part of Q24. It does not define a transport, a protocol
version, a receipt, or any authority behavior beyond validation and
recomputation obligations.

## 1. Layer and invariants

There are three distinct byte domains:

```text
typed semantic command
  -> deterministic CBOR identity bytes
  -> domain-separated request_hash

transport frame = later wrapper(identity fields/bytes, asserted hash, auth,
                                protocol version, correlation, limits, ...)
```

Changing a frame boundary, codec, compression, chunking, connection, retry, or
authentication mechanism cannot change canonical command bytes. Conversely, a
transport decoder must produce exactly this semantic command before an
authority may recompute `request_hash`. Canonical bytes are not themselves a
wire frame and this step supplies no decoder or listener.

`wipd.command/1` is an **identity-schema identifier**, not the negotiated
protocol version and not an operation version. Step 3 must map a compatible
protocol representation to one exact identity schema; negotiation data is not
hashed as command intent.

Once journaled or submitted, every command field is immutable. Replacement is
a new command ID. The Go value is intentionally inert: this step does not
journal, submit, execute, persist, or mutate authority state.

## 2. Canonical value model

Canonical bytes use RFC 8949 deterministic CBOR, restricted as follows:

| Value | Rule |
|---|---|
| null | CBOR simple `null` (`f6`). Optional schema fields are present and null; missing and null are not alternate spellings. |
| boolean | CBOR `false`/`true`; no numeric substitutes. |
| unsigned/signed integer | Major type 0/1, shortest encoding. Schema bounds apply. Epochs and sequences are positive `uint64`; operation versions are positive `uint16`. |
| float/decimal | Floating point is forbidden in an identity schema, including NaN and infinities. M1 can represent a float structurally, but an operation cannot enter the authority catalogue until its explicit identity schema maps the value to an integer unit or schema-defined canonical decimal string. |
| text | Definite-length UTF-8 text. It must be valid UTF-8 and already Unicode NFC. Canonicalization rejects rather than silently repairs non-NFC text, so execution and identity cannot observe different strings. No trimming, case folding, newline conversion, compatibility normalization, or locale transform occurs. |
| raw bytes | Forbidden in semantic DTOs. Content uses a staged blob reference. Canonical-vector byte products use lowercase hex only as diagnostic notation. |
| array | Definite length. Sequence order is preserved. A schema-defined set is sorted by its semantic key and rejects duplicate keys. |
| map/object | Definite length, text keys only. Keys are sorted by RFC 8949 deterministic order: encoded-key length, then encoded bytes. Duplicate keys are invalid. |
| tags/undefined/indefinite values | Forbidden. |

There is no generic “marshal this Go struct” rule. Each operation version owns
an explicit semantic field map. Renaming/reordering Go fields therefore cannot
silently change identity bytes. An operation without an identity schema cannot
be canonicalized or admitted.

Identity text is the 26-character uppercase canonical ULID form: first digit
`0`–`7`, followed by the Crockford alphabet
`0123456789ABCDEFGHJKMNPQRSTVWXYZ`. Alternative case, omitted leading digits,
locators, paths, and endpoint/profile names are not equivalent identities.

Timestamps are UTC RFC3339Nano strings in the unique form emitted by
`time.Time.UTC().Format(time.RFC3339Nano)`, ending in `Z`. Offsets, redundant
fractional zeroes, and alternate spellings are rejected. `acted_at` is hashed
for attribution and narration, but remains forbidden as authority fold order,
guard time, or `occurred_at`.

## 3. Immutable command schema

The top-level value is this fixed map. Every field is present.

| Field | Type / normalization | Identity and authority rule |
|---|---|---|
| `schema` | text, exactly `wipd.command/1` | Separates this value from later identity schemas. |
| `command_id` | canonical ULID | Allocated before local IPC, stable on retry, included in the hash. |
| `authority` | map | Contains `domain_id` (canonical ULID) and `expected_epoch` (positive `uint64`). Both are hashed and authority-checked. |
| `environment` | map | Contains `id` (canonical ULID) and durable `sequence` (positive `uint64`). Both are hashed and auth-bound. |
| `acted_at` | canonical UTC RFC3339Nano text | Environment-observed narration time, hashed but never authority order. |
| `actor` | NFC text in M1's `human`, `role:<name>`, or `system:<source>` vocabulary | Semantic attribution, hashed; it is not authentication. |
| `causation_command_id` | canonical ULID or null | Null for an origin; otherwise names the command that entailed this command and may not self-reference. |
| `correlation_command_id` | canonical ULID | Chain origin. An origin command self-correlates. |
| `operation` | map | `name` is the validated semantic token and `version` its positive semantic version. Protocol versions never appear here. |
| `context` | map | `repo_id`, `clone_id`, and `worktree_id`, each canonical ULID or null. A definition's required dimensions must be non-null; supplied dimensions are hashed and checked for domain membership. |
| `claim` | map or null | Null when the operation accepts no claim; otherwise `id` is a canonical ULID and `epoch` is a positive `uint64`. |
| `input` | operation-version-specific map | Typed semantic input only. All keys and nullability are schema-defined. |
| `blobs` | array | A semantic set sorted by `name`; each entry is `{name, digest, byte_length}`. Names are unique, digest is canonical, and length is nonnegative `uint64`. Empty is `[]`, never null. |

The canonical value deliberately excludes:

- asserted `request_hash`, receipts, outcomes, event IDs/ranges, authority
  `occurred_at`, and projections;
- protocol/capability versions, frame/session/request IDs, headers, chunking,
  compression, deadlines, cancellation, endpoints, routes, and connection
  profiles;
- certificates, keys, signatures, bearer tokens, grants, and other proof
  material;
- presentation text such as problem messages and rendered output; and
- static `Definition` metadata (`Delivery`, guard/write footprints, claim
  requirement, side effects). The authority resolves that metadata from the
  hashed operation ID/version and must not trust a caller-supplied copy.

Exclusion means “not semantic intent,” not “unchecked.” Later steps must check
the excluded protocol and auth fields at their owning boundary.

### `matter.create@v1`

The only sealed M1 input has this exact `input` map:

```text
{
  "title": <NFC text>,
  "requested_locator": <NFC text>
}
```

Both fields are present. Empty `requested_locator` retains M1's “derive one”
intent and differs from null, which this schema does not admit. D127's assigned
locator and `locator-repair-required` are authority output/event facts and do
not rewrite the immutable request. Their result/event schema belongs to later
M2 work.

## 4. Digests and domain separation

### Blob digest

For raw blob bytes `B`:

```text
blob_digest = "sha256:" + lowercase_hex(SHA-256(B))
```

There is no text, newline, Unicode, or framing transform. Empty content is the
SHA-256 of zero bytes. A reference also carries `byte_length`; the authority
must receive bytes whose digest **and** length match before they can participate
in fold. Upload, cache promotion, manifests, and range transfer remain Step 6.

### Request hash

Let `C` be the exact deterministic CBOR bytes of the complete command above.
The NUL below is one byte `00`, not two text characters:

```text
request_hash = "sha256:" + lowercase_hex(
  SHA-256(UTF8("wipd/request-hash/v1") || 00 || C)
)
```

The prefix prevents command identity bytes from being reused as an event,
manifest, frame, or untyped hash preimage. The in-value `schema` separates
command schema revisions inside that hash domain. Any later identity product
must allocate its own ASCII domain ending in NUL.

An asserted hash must be exactly `sha256:` plus 64 lowercase hexadecimal
digits. The authority recomputes from the validated semantic command and uses a
constant-time comparison at a security boundary. A match proves equality of
identity bytes, not caller authenticity or authorization. Same-ID replay and
different-hash receipt behavior belong to Step 5.

## 5. Environment and authority binding

Canonical identity and authenticated identity are complementary:

1. The authority obtains the authenticated domain owner and Environment
   principal from the Step 4 transport/auth mechanism.
2. It requires exact equality between that principal and hashed
   `authority.domain_id` plus `environment.id`. A route, endpoint, profile,
   hostname, path, actor token, or copied Environment label cannot substitute.
3. It verifies the Environment belongs to that domain and owner, the expected
   authority epoch is active, and the durable sequence satisfies the rule for
   this delivery stream. It independently checks every non-null context ID and
   claim ID/epoch belongs to that domain and Environment where required.
4. It resolves the operation definition and checks type, required dimensions,
   delivery class, complete guards/writes, staged references, and actor
   vocabulary before execution.
5. It recomputes `request_hash`; no caller assertion, local journal value, or
   previously logged value replaces recomputation.

The exact local peer credential, mTLS certificate, enrollment/renewal flow, and
whether an application signature additionally covers the hash are Step 4
choices. This step fixes what those mechanisms must bind without selecting
one. Hashes and digests are payload-derived identifiers and are not secret; the
privacy/retention decision about logging them remains Steps 4/9. Raw payloads,
blob bytes, credentials, and proof material are never a substitute log field.

## 6. Deterministic migration identity

D130 migration does not reinterpret old event JSON bytes as canonical. For
each legacy event, migration parses the eleven old envelope fields (`id`,
`type`, `occurred_at`, `actor`, `causation`, `correlation`, nullable `repo`,
nullable `clone`, nullable `worktree`, `subject`, and `payload`) and emits a
canonical CBOR map under schema `wipd.legacy-event/1`. `payload` must parse as
one JSON object with no duplicate keys or trailing data; its null, boolean,
integer, NFC-text, array, and object values convert recursively under §2.
Floating-point payload numbers are refused. Events are ordered strictly by
their preserved event ULIDs.

For one correlation group, the migration preimage value is:

```text
{
  "schema": "wipd.migration-command/1",
  "authority_domain_id": <assigned domain ULID>,
  "environment_id": <assigned Environment ULID>,
  "correlation_origin": <preserved origin event ULID>,
  "legacy_events": [<canonical legacy event maps in authority order>]
}
```

Its deterministic `request_hash` uses the same request-hash prefix and CBOR
rules above. The correlation origin is also the migrated `command_id`; the
group's per-Environment sequence is assigned deterministically by first-event
authority order as D130 requires, and every resulting event carries that hash.
The migration implementation, grouping audit, synthetic binding event,
cutover, and rollback refusal are not implemented here.

## 7. Canonical vector notation and first vector

Vectors are diagnostic JSON documents with
`notation = "wipd.canonical-vector/1"`. This JSON is **not** canonical input and
is never hashed. It must contain:

- `name` and `semantic`, with every schema field present and null explicit;
- JSON integer tokens for integer values (consumers must not round through an
  IEEE-754 number);
- `canonical_cbor_hex`, lowercase with no whitespace or `0x` prefix;
- `hash_domain_utf8`, written with `\u0000` for the final NUL;
- `request_hash`; and
- optional negative mutations, each naming the expected rejection or changed
  identity property rather than a language-specific error string.

The first executable vector uses:

```json
{
  "notation": "wipd.canonical-vector/1",
  "name": "matter-create-v1-origin",
  "semantic": {
    "schema": "wipd.command/1",
    "command_id": "01K5V8JQFM6Q3Q0XZ6F1Z7A2BC",
    "authority": {"domain_id": "01K5V8K1Q5VX6Y0J8C9W3M4N5P", "expected_epoch": 7},
    "environment": {"id": "01K5V8K8A4J2N7R9T0V3X6Y8ZB", "sequence": 42},
    "acted_at": "2026-09-22T17:31:42.123456789Z",
    "actor": "role:builder",
    "causation_command_id": null,
    "correlation_command_id": "01K5V8JQFM6Q3Q0XZ6F1Z7A2BC",
    "operation": {"name": "matter.create", "version": 1},
    "context": {"repo_id": "01K5V8KGD3F6H9J2M4N7Q0R5TW", "clone_id": null, "worktree_id": null},
    "claim": null,
    "input": {"title": "Café protocol identity", "requested_locator": "protocol-identity"},
    "blobs": []
  },
  "canonical_cbor_hex": "ad656163746f726c726f6c653a6275696c64657265626c6f62738065636c61696df665696e707574a2657469746c6577436166c3a92070726f746f636f6c206964656e74697479717265717565737465645f6c6f6361746f727170726f746f636f6c2d6964656e7469747966736368656d616e776970642e636f6d6d616e642f3167636f6e74657874a3677265706f5f6964781a30314b3556384b474433463648394a324d344e3751305235545768636c6f6e655f6964f66b776f726b747265655f6964f66861637465645f6174781e323032362d30392d32325431373a33313a34322e3132333435363738395a69617574686f72697479a269646f6d61696e5f6964781a30314b3556384b31513556583659304a38433957334d344e35506e65787065637465645f65706f636807696f7065726174696f6ea2646e616d656d6d61747465722e6372656174656776657273696f6e016a636f6d6d616e645f6964781a30314b3556384a51464d3651335130585a3646315a37413242436b656e7669726f6e6d656e74a2626964781a30314b3556384b3841344a324e37523954305633583659385a426873657175656e6365182a74636175736174696f6e5f636f6d6d616e645f6964f676636f7272656c6174696f6e5f636f6d6d616e645f6964781a30314b3556384a51464d3651335130585a3646315a3741324243",
  "hash_domain_utf8": "wipd/request-hash/v1\u0000",
  "request_hash": "sha256:ad15faaa76992d045529ab28b7bd9ddd63799fa851641c131b881e141e6a9503"
}
```

The same `canonical_cbor_hex` and hash are pinned in
`TestCanonicalCommandGoldenVector`. Step 8 will publish standalone vectors and
independent client/server doubles using this notation. Current negative tests
cover malformed/case-varied identities,
zero epochs/sequences, noncanonical timestamps, causation/correlation errors,
invalid UTF-8, decomposed Unicode, unsupported operation versions, malformed
and mismatched request hashes, blob order, duplicate blob names, bad digest
spelling, bad lengths, and changes across every populated command field.

## 8. Step boundary and remaining work

This step does not decide or implement protocol negotiation, unknown frame
fields, framing, parsing limits, IPC, TLS, enrollment, signatures, execution,
receipts, idempotency, outcomes, event envelopes, transfer, hydration, reads,
pagination, claims, journal return, handoff, migration execution, or the Step 8
cross-implementation conformance package. It adds no daemon/socket and changes
no current CLI, dispatcher, event, projection, or store ownership behavior.

There are no unresolved Step 2 encoding/hash questions. Later work must not
silently vary the field set, normalization, identity schema, hash prefix, or
blob algorithm. A change to any of those creates a new identity schema and
requires the compatibility treatment Step 3 defines.
