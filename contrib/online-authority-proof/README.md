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
`WIP_AUTHORITY_LAB_PROJECT` to use a different isolated project name.

`./contrib/online-authority-proof/test-policy` checks the Compose rendering and
proves the topology guard rejects representative host mounts, shared volumes,
external/host networking, published ports, credentials, and extra services.
Neither container performs domain initialization or any authority, TLS, grant,
journal, receipt, or operation behavior. This skeleton makes no claim that the
M4 synthetic fixture is canonical state and does not implement migration,
disconnected claim commands, production cutover, or later acceptance work.
