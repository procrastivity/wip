# M2 Step 8: published conformance package

Status: published test-only conformance package for M2 Steps 1–7. It adds no
daemon, listener, socket, authority store, migration executor, fresh authority
state, or current CLI behavior. The key words **MUST**, **MUST NOT**, **SHOULD**,
and **MAY** retain the meanings fixed by the owning Step 1–7 contracts; this
package does not revise them.

## Package products

The package consists of:

- `conformance-schemas.cddl`, the standalone closed CDDL catalogue for the
  protocol-1 frame and logical command, compatibility, receipt, read,
  transfer, blob, claim, and migration messages;
- `conformance-vectors.json`, the deterministic index that binds all four
  prior vector files by SHA-256, publishes eight cross-implementation scripts,
  enumerates V01–V22, and maps D115–D131 plus Q01–Q25;
- `internal/protocol/conformance`, an all-`_test.go` package containing
  independent client/server doubles, independent canonical/frame codecs,
  independent incremental/reference parsers, bounded properties, and fuzz
  entry points; and
- the four owning Step 4–7 vector files named and hashed by the index. They
  remain the detailed source vectors rather than being copied into a second
  divergent corpus.

Diagnostic JSON is not a wire encoding. Protocol payloads are deterministic
RFC 8949 CBOR under the Step 2 restrictions. A record is the Step 4 four-byte
big-endian body length followed by one `wipd.frame/1` CBOR body. The CDDL maps
are closed unless a negotiated owning profile explicitly says otherwise.

## Independent execution

From the repository root:

```sh
go test -count=1 -v ./internal/protocol/conformance
go test -count=1 -run TestBoundedConformanceProperties ./internal/protocol/conformance
go test -count=1 -fuzz=FuzzFrameParsersAgree -fuzztime=10s ./internal/protocol/conformance
```

The package imports no production persistence, transport, store, CLI, or
operation implementation. `TestIndependentClientServerAgreement` executes the
same published scripts through separately implemented client and server state
machines and prints one observable transcript for each required family:

1. accepted command;
2. refusal;
3. same-ID retry;
4. lost-response receipt query;
5. pinned read snapshot;
6. blob upload;
7. claim cycle; and
8. incompatible protocol/operation versions plus evidence-preserving reseed.

The independent codecs must produce the existing Step 2 command hash and Step
4 frame bytes exactly. The parsers consume different implementations: one is
an incremental fixed-prefix parser with an explicit frame decoder; the other
is a whole-record parser with a generic strict deterministic-CBOR decoder.
Their fuzz oracle requires the same acceptance or stable parser error.
Oversized lengths are rejected before body allocation.

## Properties and negative coverage

The bounded property suite uses asymmetric, deterministic cases and checks:

- canonical bytes agree across both encoders, identity-field changes alter the
  request hash, and the published command hash is stable;
- a same-ID/same-hash submit, retry, and receipt query execute once and retain
  one effect;
- pagination over a pinned snapshot neither gains nor loses items after the
  underlying state changes;
- blob duplicate, digest-mismatch, and length-mismatch outcomes remain
  distinct; and
- claim hydration, receipt barrier, close, and old/wrong-epoch fencing permit
  no illegal transition.

The explicit parser corpus covers partial prefixes and bodies, zero lengths,
oversized lengths before allocation, trailing bytes, indefinite maps, CBOR
tags, non-shortest lengths, wrong closed-map shapes, and invalid UTF-8. Fuzzing
extends that corpus over arbitrary record bytes and fragmentation boundaries.
The Step 4 source vectors retain chunk, stream, concurrency, backpressure,
authentication, cancellation, and redaction limits; their SHA-256 binding
prevents silent replacement underneath this index.

## Exit and traceability closure

`conformance-vectors.json.exits` is the executable review index for V01–V22.
V01–V14, V17, V19, and V22 bind their detailed Step 4–7 vectors. Step 8 adds
the previously prose-only deterministic evidence for:

- **V15:** requested and authority-assigned locator, stable Matter identity,
  and repair-required state;
- **V16:** Environment sequence and provisional-target birth ordering for a
  cursor move;
- **V18:** explicit protocol-major and operation-version refusals alongside
  evidence-preserving reseed;
- **V20:** prefix conflict, missing-blob closure, unresolved-old-command,
  promotion fence, and ahead-client/history-regressed outcomes; and
- **V21:** exact migration CBOR/hash, correlation-origin command identity,
  deterministic sequence, and rollback refusal.

The index has exactly one nonempty entry for each D115–D131 and Q01–Q25 key.
That mapping means “covered by the cited schema, vector, property, or explicit
review deferral”; it does not claim that M3/M4 production behavior exists.

## Deliberate Step 9 review boundary

The index records rather than closes these Step 9 items:

- final D115–D131 cross-contract and M3/M4 implementability review;
- portable receipt, grant, and bundle signatures;
- owner-attested handoff, restore, and stand-down ceremonies;
- enrollment, renewal, rotation, and revocation security review;
- final log/digest/identifier retention, access, and privacy review;
- D127's final event/output shape and the cursor operation/event schema; and
- D130 grouping audit, synthetic migration binding, and proof review.

No inconsistency requiring a change to a prior normative contract was found.
The CDDL transcribes those contracts; if a future implementation differs, the
owning contract and a newly versioned conformance product must resolve the
difference rather than weakening a vector.
