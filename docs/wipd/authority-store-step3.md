# M3 Step 3: authority artifact and Environment registries

Status: fresh-store SQL v2 and typed public-evidence APIs. Step 2's exact SQL
v1 definition remains intact. `CreateEmpty` installs v1 then v2 before it
returns; `OpenExisting` accepts exactly v2 and never migrates. `UpgradeV1`
requires a closed writer, an exact v1 schema/state, the retained flock, and a
private, validated `authority-v1.backup.db` made with `VACUUM INTO` before the
single v2 schema/version transaction. An incomplete backup blocks upgrade
until explicitly inspected/removed; an interrupted migration leaves either
v1 (retry explicitly) or v2 (open normally). The backup is not a second writer
or a legacy import. Internal SQL v2 does not change `wipd.store/1`.

The owner root remains a raw Ed25519 public key with DER-SPKI SHA-256 key ID.
Owner-role deterministic-CBOR wrappers are verified directly against it, with
the exact `wipd/signed-artifact/v1\0` preimage and exact closed payload map.
Artifact-key certificates and fences are immutable public evidence. Each key
generation begins at 1 and requires its predecessor's owner-signed fence;
fences bind the exact final sequence/digest (0/null before the first product).
The ledger reserves one contiguous chain per `(domain, epoch, generation)` and
reopen verifies complete wrapper signatures, digest/predecessor continuity,
key validity at issue time and final fence/head equality. No standalone
`AppendSignedArtifact` is exposed: Step 4 must link the signed product and
chain advance to its owning terminal transaction, with closed payload semantic
validation. Signing private keys remain outside SQLite.

The narrowly amended M2 §4 profile makes a separately signed, Ed25519,
self-signed X.509 issuing CA the only Environment TLS trust anchor. The
owner-signed delegation binds exact DER, domain/epoch, owner SPKI ID, contiguous
CA generation, CA SPKI ID and finite validity. Its owner-signed immutable
fence binds that delegation's complete artifact digest; applying the fence
immediately disables its leaves. Issuance verifies a fresh signed grant,
PKCS#10 proof, exact requested SPKI, critical canonical URI SAN, exact
leaf-then-CA DER chain, direct CA signature, clientAuth/leaf constraints and
the 24-hour maximum nested interval. One SQL transaction registers the
Environment's immutable domain/ID/epoch, next certificate generation, exact
issued public DER/serial/SPKI, and one-use grant ID/nonce/digest/CSR digest.
An identical grant+CSR response-lost retry retrieves the exact stored chain;
another CSR/Environment ID cannot consume it. Same-key renewal checks the
authenticated old leaf and a fresh same-SPKI CSR; owner-granted rotation binds
the new SPKI and prior Environment ID. Both commit a successor generation and
old-leaf revocation together. Explicit current-leaf revocation and CA fences
block retained-connection exchanges. Connection state must come from a real
completed TLS handshake in the later adapter; the store rechecks the exact
registered DER, epoch, owner binding, generation, clock interval and fences
before every exchange. No private key or bearer grant bytes enter the store.

The Environment sequence head starts at zero and v2 rejects **all** isolated
head updates, including ack-only increments. A Step 4 versioned schema change
must permit exactly `head+1` inside the owning submission/return terminal
acknowledgment transaction and persist enough exact command/journal evidence
to validate it on reopen. Step 3 deliberately does not create a submission,
return acknowledgment, terminal receipt, command admission or callback SQL
surface. Similarly Step 4's actual mTLS adapter must take the completed TLS
state, not a caller-supplied certificate struct or forwarded header.

Remaining Step 4 risks: (1) a lost response to same-key **renewal** cannot be
replayed on a now-revoked predecessor mTLS connection without violating the
before-every-exchange revocation rule; define a recovery query authenticated
with the new certificate or an explicit owner grant, not a bypass. (2) A
restricted issuer may have produced an unused public certificate before the
store commit; it is never trusted unless the exact DER is registered, and
signing and response ordering must preserve that invariant. (3) OIDC/JTI
issuer/JWKS/audience/claim validation and its one-use digest-only consumption
belong with the later authenticated enrollment adapter; bearer assertions
must not be persisted. (4) Step 7 promotion must install next-epoch
delegations and certificates through an explicitly verified transition; the
current epoch check already fences old credentials.
