# Online authority proof Compose lab

This is only the disposable M5 lab topology. `authority-env` and `client-env`
are separate Environments with separate named volumes. The host runs `run` as
the harness; it is not a Compose service. Both containers are attached only
to an internal Docker network. No host paths are mounted and no host WIP
database or default state is imported. During the explicit enrollment step,
the authority worker receives the operator-provided delegated Environment CA
key and an ephemeral lab TLS key over `docker exec` stdin; both remain in
worker memory and are never written to SQLite or a host-mounted path.

With Docker Engine, Docker Compose v2, and `jq` available, run from any
directory:

```sh
./contrib/online-authority-proof/run config # validate rendered Compose topology
./contrib/online-authority-proof/run up     # create/start the lab
./contrib/online-authority-proof/run prepare-client # create pending key and export CSR
./contrib/online-authority-proof/run bootstrap --owner-root-public-key "$OWNER_ROOT_PUBLIC_KEY_B64"
./contrib/online-authority-proof/run enroll \
  --domain-id "$DOMAIN_ID" --repo-id "$REPO_ID" --epoch 1 \
  --owner-root-public-key "$OWNER_ROOT_PUBLIC_KEY_B64" \
  --environment-ca-certificate ./environment-ca.der \
  --environment-ca-private-key ./environment-ca-key.pk8 \
  --environment-ca-delegation ./environment-ca-delegation.cbor \
  --enrollment-grant ./enrollment-grant.cbor
./contrib/online-authority-proof/run down   # stop and remove containers, network, and volumes
```

`prepare-client` creates one pending Ed25519 identity in the client volume and
prints only its public CSR in base64. After `bootstrap` prints the new domain
and initial Repo IDs, the existing offline owner workflow must sign an
Environment-CA delegation for that domain/epoch and a one-use enrollment grant
bound to the CSR's SPKI. The operator supplies those signed artifacts plus the
delegated CA certificate and its restricted signing key to `enroll`; this lab
does not implement or replace the offline owner signer. The retained owner
root private key is never supplied to the harness or either container.

`enroll` starts a temporary pinned TLS 1.3/HTTP/2 authority worker, installs
the owner-signed CA delegation, and serves enrollment for only the supplied
grant and exact prepared CSR. The client authenticates and negotiates M2 on the
same mTLS HTTP/2 connection before seed exchange, validates the returned leaf
against the supplied delegated CA, proves key possession over mTLS, then
verifies the complete seed prefix and manifest (which may be empty or
non-empty). It atomically creates the client shadow only after the complete
`SeedEnd` agrees; failed or truncated exchanges leave no installed identity.
The authority verifies the configured Repo's persisted domain membership
before enrollment and seed exchange. The result prints only the
domain/epoch/Repo/Environment bindings and verified prefix/manifest digests.

The bounded `wipdseed.PullAndInstall` client API negotiates on each new
connection and installs a complete M2 pull delta only after `PullEnd`. The
client validates event-record order and bytes, the cumulative prefix digest,
and the complete manifest, then rebuilds the supported M1 `matter.created`
projection from event bytes before atomically replacing its local base.
Unsupported event kinds fail closed. The deterministic non-empty seed/pull
acceptance uses an ephemeral authority store and synthetic M1 commands only in
tests; run it with:

```sh
go test ./internal/wipdseed -run 'TestNegotiationIsRequiredBeforeSeedExchange|TestPullRejectsAnAnchorAheadOfAuthorityAsPrefixMismatch|TestPullInstallsAsymmetricAuthorityEventOrderAndProjection' -count=1
```

That test compares installed event bytes and authority order plus derived
projection values/order against a pinned authority snapshot. The ordinary
Compose `enroll` flow remains reproducible with the external signed
CA-delegation/enrollment-grant artifacts described above; it does not seed
authority mutations itself.

`bootstrap` requires exactly one running, lab-owned `authority-env`. The
trusted offline owner workflow supplies the retained owner's raw Ed25519
public key as canonical base64. The owner private key stays outside the
harness, container, and authority store. The harness creates fresh domain and
Repo IDs plus an independent per-run setup signer in memory. It pins the
setup signer's public key before passing the bounded signed grant to a
temporary static worker in `authority-env` over `docker exec` stdin. The setup
private key and grant are never mounted or written to disk; the worker is
copied temporarily into the authority-owned named volume and removed after
use. The authority database retains only the grant digest, nonce, pinned
signer public-key digest, and authorized identity bindings in the same
transaction as domain + Repo creation. An existing/incomplete authority root
is refused; `bootstrap` never resets or reuses it. The command prints a
sanitized domain/epoch/Repo/initial high-water record.

The signed create-domain grant is a narrow M5 test-lab setup contract, not an
M2 frame, enrollment grant, owner attestation, or production authorization
endpoint. Its bounded canonical-CBOR shape and atomic consumption boundary are
documented in `docs/wipd/m5-lab-genesis-grant.md`; no M2 wire schema or CDDL
production is added.

For an explicitly fresh disposable run, `./contrib/online-authority-proof/run
reset` removes the project's old containers, network, and named volumes before
starting fresh. The default Compose project is `wip-authority-proof-$UID`; set
`WIP_AUTHORITY_LAB_PROJECT` to use a different project name with a lowercase
alphanumeric/underscore/hyphen suffix (1–32 characters). The harness validates
the rendered config before any resource inspection or teardown and refuses
resources bearing the project label without its lab ownership label, as well
as pre-existing named lab volumes/networks that are not lab-owned. It does not
remove Compose orphans.

`./contrib/online-authority-proof/test-policy` checks the Compose rendering and
proves the topology guard rejects representative host mounts, bind-backed,
external, or explicitly shared volumes, secret/config/env/label-file channels,
external or explicitly shared networks, inherited mounts, shared PID/IPC
namespaces, multi-replica services, external providers, external/host
networking, published ports, credentials, and extra services. These adversarial
topology mutations are normalized by `docker
compose config` before policy rejection for the build-context, env-file,
volumes-from, network-name, PID/IPC, scale/replica, and provider cases.
`--no-env-resolution` is used
so env-file declarations remain visible even when they only repeat an allowed
environment variable. Other focused policy mutations operate on rendered
Compose JSON directly. The tests also cover invalid/colliding project names
and prove an invalid config is rejected before Docker resource inspection or
teardown.
Only the explicit `bootstrap` step initializes authority-env domain identity.
This lab implements initial enrollment plus bounded seed/pull installation for
the supported M1 event projection; it does not implement command exchange,
journal, receipt, or production operation behavior. It makes no claim that
the M4 synthetic fixture is canonical state and does not implement migration,
disconnected claim commands, production cutover, or later acceptance work.
