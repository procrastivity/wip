# Online authority proof Compose lab skeleton

This is only the disposable M5 lab topology. `authority-env` and `client-env`
are separate, inert Environments. The host runs `run` as the harness; it is
not a Compose service. Both containers are attached only to an internal
Docker network and receive separate named volumes. No host paths are mounted,
and the containers do not receive host WIP databases, default state, keys, or
credentials.

With Docker Engine, Docker Compose v2, and `jq` available, run from any
directory:

```sh
./contrib/online-authority-proof/run config # validate rendered Compose topology
./contrib/online-authority-proof/run up     # create/start the lab
./contrib/online-authority-proof/run down   # stop and remove containers, network, and volumes
```

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
external, or explicitly shared volumes, secret/config/env-file channels,
external or explicitly shared networks, inherited mounts, shared PID/IPC
namespaces, external/host networking, published ports, credentials, and extra
services. These adversarial topology mutations are normalized by `docker
compose config` before policy rejection for the build-context, env-file,
volumes-from, network-name, PID, and IPC cases. `--no-env-resolution` is used
so env-file declarations remain visible even when they only repeat an allowed
environment variable. Other focused policy mutations operate on rendered
Compose JSON directly. The tests also cover invalid/colliding project names
and prove an invalid config is rejected before Docker resource inspection or
teardown.
Neither container performs domain initialization or any authority, TLS, grant,
journal, receipt, or operation behavior. This skeleton makes no claim that the
M4 synthetic fixture is canonical state and does not implement migration,
disconnected claim commands, production cutover, or later acceptance work.
