# M5 test-lab genesis grant

This contract is limited to the disposable `authority-env` bootstrap harness
for M5 Step 3. It is not an M2 frame, a `wipd.signed-artifact/1`, an
enrollment grant, an owner attestation, or a production authorization
endpoint. It adds no CDDL production or `wipd.store/1` content.

## Issuer and pin

The trusted offline owner workflow supplies the retained owner's raw Ed25519
public key to the harness as canonical base64. The harness never generates a
replacement owner root and never receives or retains the owner's private key;
that same root must remain available to sign later owner enrollment grants and
CA delegations. The genesis grant binds the supplied root's DER-SPKI digest.

For each bootstrap attempt, the host harness independently generates a fresh
Ed25519 setup signer. Before it sends grant bytes, it passes the setup
signer's 32-byte public key to the worker as an independent pin. The setup
key is not embedded in the grant. The worker rejects a setup signer whose
SPKI digest equals the owner-root SPKI digest. The setup private key remains
in harness memory and neither it nor the owner private key is copied to a
container or persisted. Grant bytes travel only over the harness's
`docker exec` stdin.

## Grant bytes

The input is at most 4096 bytes and is deterministic CBOR. Its outer map is
closed to exactly `payload` and `signature`, both byte strings. `payload` is a
second deterministic-CBOR closed map with exactly:

```text
scope = "create-domain"
domain_id = canonical target ULID
initial_repo_id = canonical first-Repo ULID
epoch = 1
owner_root_spki_digest = "sha256:" + lowercase hex SHA-256(DER-SPKI)
nonce = exactly 16 random bytes
issued_at = canonical UTC RFC3339Nano
expires_at = canonical UTC RFC3339Nano
```

The signature is Ed25519 over:

```text
UTF8("wip/m5-test-lab/create-domain-grant") || 0x00 || payload
```

The public key used for verification is only the separately pinned key above.
The verifier requires exact scope and equality with the requested domain ID,
first Repo ID, epoch, and owner-root SPKI digest. It requires
`issued_at <= now < expires_at`, a positive interval, and a lifetime no longer
than ten minutes. No clock skew or alternate scope is accepted.

## Consumption and resulting state

The harness verifies the grant before asking `CreateEmpty` to create the
authority root, so malformed, untrusted, expired, or wrongly bound input does
not create authority state. The authority store re-verifies the same bytes
against the pin, then one SQLite transaction inserts the domain, first Repo
membership, and grant-consumption record. A uniqueness constraint on the
nonce makes replay fail across calls and reopen; immutable SQL triggers retain
that result. If any insert fails, the transaction rolls back all three rows.
The stored consumption record contains only the nonce, grant digest, pinned
signer SPKI digest, authorized identity bindings, and consumption time, never
the grant bytes or private key.

The authority-store SQL version advances from v5 to v6 for this consumption
record. Ordinary open still refuses v5; `UpgradeV5` requires a validated,
equivalent retained backup before installing the new schema. The lab always
starts with a fresh root, so this upgrade path does not participate in its
bootstrap flow.

The output is a sanitized harness record of the persisted domain ID, active
epoch, initial Repo ID, and current high-water. A fresh domain has event count
zero, null high-water event ID, and the normative empty-prefix digest
`SHA-256(UTF8("wipd/event-prefix/v1") || 0x00)`. Bootstrap creates no event.
The authoritystore continuity test uses the same supplied owner root to sign
an M3 enrollment grant and verifies issuance against the bootstrapped domain;
it does not add enrollment behavior to this lab step.
