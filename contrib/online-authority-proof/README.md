# Online authority proof Compose lab skeleton

This is only the disposable M5 lab topology. `authority-env` and `client-env`
are separate Environments with separate named volumes. The host runs `run` as
the harness; it is not a Compose service. Both containers are attached only
to an internal Docker network. No host paths are mounted, and the containers
do not receive host WIP databases, default state, keys, or credentials.

With Docker Engine, Docker Compose v2, and `jq` available, run from any
directory:

```sh
./contrib/online-authority-proof/run config # validate rendered Compose topology
./contrib/online-authority-proof/run up     # create/start the lab
./contrib/online-authority-proof/run bootstrap # initialize authority-env once
./contrib/online-authority-proof/run down   # stop and remove containers, network, and volumes
```

`bootstrap` requires exactly one running, lab-owned `authority-env`. The host
harness generates a fresh owner root, domain ID, Repo ID, and independent
per-run setup signer in memory. It pins the setup signer's public key before
passing the bounded signed grant to a temporary static worker in
`authority-env` over `docker exec` stdin. The setup private key and grant are
never mounted or written to disk; the worker is copied temporarily into the
authority-owned named volume and removed after use. The authority database
retains only the grant digest, nonce, pinned signer public-key digest, and the
authorized identity bindings in the same transaction as domain + Repo
creation. An existing/incomplete authority root is refused; `bootstrap` never
resets or reuses it. The command prints a sanitized domain/epoch/Repo/initial
high-water record. `client-env` remains uninitialized in this step.

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
This lab does not yet implement client enrollment, command exchange, journal,
receipt, or operation behavior. It makes no claim that the M4 synthetic
fixture is canonical state and does not implement migration, disconnected
claim commands, production cutover, or later acceptance work.
