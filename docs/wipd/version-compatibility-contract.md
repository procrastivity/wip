# M2 Step 3: version and compatibility contract

Status: specified for the canonical identity schema from M2 Step 2. This
record closes Q03 and Q22 and the compatibility portion of V18. It defines the
logical negotiation and refusal contract that later framing transports must
carry. It does not select a byte frame, socket, TLS profile, daemon, store
implementation, or production compatibility shim.

## 1. Version layers

WIP has four version layers with different owners and failure boundaries:

| Layer | Example | Owner | May change without changing command identity? |
|---|---|---|---|
| Protocol | `wipd.protocol/1.2` | M2 protocol | Yes, when the negotiated minor is backward-compatible and the semantic command decodes to the same identity schema. |
| Operation | `matter.create@v1` | M1/M2 operation catalogue | No. A new semantic input, output, guard, write, delivery rule, or behavior contract is a new operation version. |
| Identity schema | `wipd.command/1` | M2 Step 2 | No. A changed canonical field set, normalization, scalar representation, or hash domain is a new identity schema and requires an explicit compatibility boundary. |
| Store/base | `wipd.store/1` plus installed authority prefix | M3/M2 transfer design | No for the installed base, but a compatible peer may request disposable-base reseed while preserving evidence-bearing local journals, receipts, and staged blobs. |

Protocol negotiation never upgrades an operation or identity schema by
implication. An implementation may advertise protocol support for an
operation only when it can produce and consume that operation's exact
identity schema and metadata. Conversely, a supported operation does not
imply that every protocol peer can carry it.

The protocol version is represented as a numeric pair `(major, minor)` and a
stable family name `wipd.protocol`. The operation version is the semantic
`operation.ID` pair `(name, version)` already defined by M1. The identity
schema is named explicitly in capabilities and in the decoded command; it is
not inferred from a protocol minor.

## 2. Capability exchange

Before a command, query, seed, or journal exchange is accepted, peers perform
a capability exchange at the transport's handshake boundary. The logical
messages are:

```text
ClientHello {
  protocol_min: Version
  protocol_max: Version
  identity_schemas: sorted unique [SchemaID]
  operations: sorted unique [OperationCapability]
  store_schemas: sorted unique [StoreSchemaID]
  features: sorted unique [FeatureID]
}

OperationCapability {
  name: SemanticName
  versions: sorted unique [positive uint16]
  identity_schemas: sorted unique [SchemaID]
}

ServerHello {
  selected_protocol: Version
  identity_schemas: sorted unique [SchemaID]
  operations: sorted unique [OperationCapability]
  store_schemas: sorted unique [StoreSchemaID]
  features: sorted unique [FeatureID]
}
```

This is a logical schema, not a wire encoding. Later framing owns correlation,
lengths, ordering, authentication, and extension envelopes. Capability lists
are canonicalized before comparison: duplicates are invalid, values are
sorted, and unsupported values are not echoed as if selected.

The selected protocol is the greatest common version under this rule:

1. The major versions must be equal.
2. The selected minor is the greatest value in the intersection of the two
   advertised inclusive ranges.
3. A peer whose range is empty, reversed, or outside the supported numeric
   bounds is malformed and receives `protocol.invalid-capabilities`.
4. If the major intersection is empty, or no minor intersects, negotiation
   ends with `protocol.incompatible-version`; no command is accepted.

The exchange returns the selected protocol and the exact operation/schema
intersection available under it. A peer must not claim an operation merely
because it received it from the other side. Operation capabilities are
advertised by semantic name and exact versions; each version maps to exactly
one identity schema. A name with no common version is unsupported, not a
protocol-version failure.

### Protocol compatibility classes

Within one major family, a higher minor may add optional message fields or
features only if an older peer can ignore them without changing the meaning of
any accepted message. A minor release may not change the meaning, requiredness,
canonical identity, result disposition, receipt identity, ordering, or store
preservation rule of an existing field. Such a change requires a new protocol
major.

An implementation may advertise a range rather than one version, but it must
implement every minor in the advertised range's interoperability profile. It
must not select a version and then silently downgrade to a different minor.
There is no automatic major-version translation.

## 3. Unknown fields and extensions

Unknown fields are handled according to the owning layer:

| Location | Unknown-field rule |
|---|---|
| Canonical command identity | Reject. The operation identity schema is closed; an unknown semantic field changes the preimage and cannot be ignored. Use a new identity schema/operation version. |
| Required protocol message field | Reject with `protocol.malformed-message`; do not guess a default. |
| Optional protocol field in the selected major/minor profile | Ignore for semantics and preserve no effect. It may be accepted only when the profile marks it explicitly ignorable. |
| Extension namespace | Ignore only when the namespace is declared optional by the selected protocol and its value is not required to authorize, order, hash, execute, or interpret the message. Otherwise reject with `protocol.unsupported-extension`. |
| Capability entry | Reject malformed or duplicate entries. Unknown feature IDs are ignored as unavailable; unknown operation versions are not treated as supported. |
| Store/base metadata | Reject if it affects prefix identity, event order, blob closure, or receipt meaning. Advisory diagnostic fields may be ignored after validation. |

Unknown fields are never copied into canonical identity bytes, logs, receipts,
or events merely because a decoder saw them. A peer cannot use an unknown
field to smuggle a guard, write, claim, authorization, ordering, or payload
value past a lower-version implementation.

## 4. Operation compatibility

An operation version is compatible only by exact semantic identity. The
following are incompatible changes and require `vN+1` (or a new operation
name when the concept is no longer the same):

- adding, removing, renaming, or changing the meaning/nullability of an
  identity input field;
- changing scalar normalization, collection ordering, blob digest or length
  rules, identity schema, or request-hash domain;
- changing a required Repo/Clone/Worktree dimension, claim admission, guard,
  write footprint, delivery class, external effect, or authorization rule;
- changing the result disposition, stable problem-code namespace, output
  meaning, event/receipt binding, or retry/idempotency behavior; or
- changing an operation's authority/environment ownership in a way that could
  admit or reorder work differently.

An implementation may add an operation version while retaining older versions
side by side. The selected version is explicit in the command and response;
the peer must not reinterpret `matter.create@v1` as `matter.create@v2`.
There is no server-selected operation upgrade for a submitted command. A
client may construct a new command with a new command ID after an explicit
capability check; it may not mutate an existing command ID or request hash.

For an operation name known to the peer but with no requested version, the
deterministic terminal response is `operation.unsupported-version`. For an
unknown operation name, the response is `operation.unknown`. Both responses
are protocol-level refusal of admission with no journal, event, receipt, or
model effect. Their stable fields are:

```text
CompatibilityProblem {
  code: operation.unknown | operation.unsupported-version
  operation_name: semantic name
  requested_version: positive uint16 or null
  supported_versions: sorted unique [positive uint16]
  identity_schema: schema ID or null
}
```

`operation.unknown` has an empty `supported_versions` list and null schema.
`operation.unsupported-version` lists only versions the peer can actually
accept under the selected protocol and names the schema only when the
requested version itself is absent but exactly one safe schema can be shown
for diagnostics. It never suggests a different version as an implicit retry.
Problem messages, timestamps, endpoint labels, and local stack details are not
part of this deterministic response.

## 5. Minimum/maximum and lifecycle behavior

The protocol state machine is:

```text
unnegotiated
  -> negotiated(protocol, capabilities)
  -> admitted(operation, identity-schema)
  -> submitted(command)
  -> terminal or outcome-unknown
```

The following transitions are forbidden:

- `unnegotiated -> admitted` without a successful capability exchange;
- a protocol or operation change after `submitted`;
- treating a version refusal as `unavailable`, `outcome-unknown`, or a
  semantic handler result;
- retrying a different version under the same command ID; and
- executing a command after negotiation is invalidated by an authority epoch
  or domain change.

Before submission, a client may choose any exact operation version present in
the negotiated intersection. After submission, the selected protocol and
operation identity are immutable for that command. A connection may be
renegotiated for a new command, but a receipt query for an existing command
uses its stored protocol-independent command ID and request hash.

If a peer receives a message with a protocol major outside its range, it sends
one deterministic `protocol.incompatible-version` response and closes or
resets the session according to Step 4 framing rules. If a negotiated minor
cannot decode a required field, it sends `protocol.malformed-message`; it does
not fall back to an earlier minor after accepting bytes from the failed
message.

## 6. Store compatibility and reseed boundary

Protocol compatibility and installed-store compatibility are separate checks.
After protocol negotiation succeeds, a local Environment compares its
installed store schema and authority prefix metadata with the authority's
advertised requirements:

```text
StoreCompatibility {
  store_schema: StoreSchemaID
  authority_domain_id: DomainID
  authority_epoch: positive uint64
  installed_prefix: PrefixID
  required_prefix: PrefixID
  authority_high_water: EventPosition
  required_blobs: sorted unique [BlobDigestAndLength]
}
```

The authority may report `store.reseed-required` only when protocol and
authentication checks have already succeeded and the mismatch is limited to a
disposable installed base. The reseed response is not a version negotiation
success and is not a command result. It carries:

```text
ReseedRequired {
  code: store.reseed-required
  authority_domain_id: DomainID
  authority_epoch: positive uint64
  required_prefix: PrefixID
  high_water: EventPosition
  required_blobs: sorted unique [BlobDigestAndLength]
  preserve: [journal, terminal-receipts, staged-blobs, local-evidence]
}
```

Reseed replaces only the disposable materialized base after the Step 6
verified seed/prefix and blob protocol succeeds. It preserves immutable local
journals, terminal receipts, staged blobs, and evidence needed to reconcile
pending work. It must refuse rather than reseed when any of these is true:

- the domain identity or active authority epoch is not authenticated;
- the history is not a strict compatible prefix or the authority reports
  history-regressed;
- a required receipt, journal head, or staged blob would be discarded;
- the local store schema cannot preserve the required evidence; or
- the peer requests a semantic operation/version upgrade as a condition of
  reseed.

Such cases use the owning protocol/store refusal, never a guessed migration.
Store schema migration, seed transfer, hydration, rollback refusal, and
history-regressed handling are implemented and vectorized by Steps 6–9. This
step fixes only the signal boundary and preservation invariant.

## 7. Deterministic compatibility vectors

Compatibility vectors use diagnostic JSON notation
`wipd.compatibility-vector/1`. JSON is not a wire encoding and is never part
of command identity. Lists are sorted before comparison. The response omits
unstable data and is byte-for-byte stable when serialized by the later vector
package.

### V18-A: incompatible protocol major

```json
{
  "notation": "wipd.compatibility-vector/1",
  "name": "protocol-major-incompatible",
  "client": {"protocol_min": "2.0", "protocol_max": "2.1"},
  "server": {"protocol_min": "1.0", "protocol_max": "1.3"},
  "result": {
    "code": "protocol.incompatible-version",
    "common_protocol": null,
    "supported_protocols": ["1.0", "1.1", "1.2", "1.3"]
  },
  "effects": "no-session-no-command-no-store-change"
}
```

### V18-B: operation version unavailable

```json
{
  "notation": "wipd.compatibility-vector/1",
  "name": "operation-version-incompatible",
  "selected_protocol": "1.2",
  "operation": "matter.create",
  "requested_version": 2,
  "server_capability": {
    "versions": [1],
    "identity_schemas": ["wipd.command/1"]
  },
  "result": {
    "code": "operation.unsupported-version",
    "operation_name": "matter.create",
    "requested_version": 2,
    "supported_versions": [1],
    "identity_schema": null
  },
  "effects": "no-journal-no-event-no-receipt-no-model-change"
}
```

### V18-C: protocol compatible, disposable base reseed required

```json
{
  "notation": "wipd.compatibility-vector/1",
  "name": "compatible-protocol-store-reseed",
  "selected_protocol": "1.2",
  "store": {
    "store_schema": "wipd.store/1",
    "installed_prefix": "event:00000000000000000000000000",
    "required_prefix": "event:00000000000000000000000042",
    "preserve": ["journal", "terminal-receipts", "staged-blobs", "local-evidence"]
  },
  "result": {
    "code": "store.reseed-required",
    "required_prefix": "event:00000000000000000000000042",
    "high_water": "event:00000000000000000000000042"
  },
  "effects": "replace-disposable-base-only"
}
```

V18-C is not permission to discard a pending command. After reseed, the
Environment must revalidate each preserved journal entry against its stored
command ID/hash, authority epoch, and exact operation version before return.
The receipt and journal protocol owns that admission in Steps 5–7.

## 8. Step boundary and closure

This contract closes the protocol/operation version distinction, capability
selection, unknown-field policy, incompatible-change rules, minimum/maximum
behavior, deterministic version refusal, and the protocol-versus-store reseed
boundary. It does not implement a decoder, frame, transport, authentication,
receipt, blob transfer, seed, journal return, claim, or store migration.

Step 4 must carry these logical messages without changing their semantics and
must define framing limits, auth, correlation, and extension envelopes. Step 5
must preserve the exact operation version and command hash through receipts and
retries. Step 6 must implement the verified reseed/prefix and blob closure.
Step 8 must publish executable independent vectors, including all three V18
cases, and Step 9 must review D115–D131 and M3/M4 implementability.

There are no unresolved Step 3 version/compatibility decisions. Any future
change to the selected-version rule, unknown-field meaning, operation
compatibility, or evidence-preserving reseed boundary is a contract change,
not an implementation detail.
