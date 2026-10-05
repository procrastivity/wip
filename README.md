# wip

wip is a personal, agent-friendly project-management CLI. It tracks real
work — Matters, Stages, Steps — through a verb surface (`wip next`,
`wip plumbing start`, `wip plumbing finish`, …), records every write as an event, and
queues tracker writes in an outbox that only a person approves and
flushes. It is built for a solo developer who works alongside coding
agents: the same verbs serve both, and `wip install` projects the tool
into each detected agent harness as a generated, stamped skill.

Built on the [toolsmith contract](https://github.com/procrastivity/toolsmith)
(`toolsmith/v1` — the manifest declares it): a single static Go binary
that projects itself into agent harnesses as generated, stamped skills.

This is the Go line, on the `go` branch. `main` still carries the
original bash implementation until cutover.

## Install

Install the binary first, then optionally project it into an agent harness.
Neither binary installation nor `wip version` opens or migrates a WIP store.

### GitHub release binary

The release installer supports **Linux amd64** and **macOS arm64**. It requires
`curl` and either `sha256sum` or `shasum`, downloads the binary and `SHA256SUMS`,
and verifies the binary before installing it. Windows and other architectures
are not published by this release pipeline.

Until a release includes the new `wip-install.sh` asset, run the installer from
this checkout against the existing Go release assets:

```sh
WIP_VERSION=v0.3.0 sh scripts/install.sh
```

After an installer-bearing release is published, download and inspect its
installer, then run it (the default selects the latest release):

```sh
curl -fsSL https://github.com/procrastivity/wip/releases/latest/download/wip-install.sh -o /tmp/wip-install.sh
less /tmp/wip-install.sh
sh /tmp/wip-install.sh
```

Set `WIP_VERSION=vX.Y.Z` on the `sh` command to pin the binary's release tag.
For a fully pinned bootstrap, also replace `latest/download` in the installer
URL with `download/vX.Y.Z` from an installer-bearing release. `WIP_BASE_URL`
selects another trusted GitHub repository with the same asset layout.
The installer and checksum file are trusted GitHub/HTTPS inputs; checking the
binary checksum detects transfer corruption, not a compromised release.

The default destination is `~/.local/bin/wip`; set `WIP_INSTALL_DIR` to choose
another directory, including a disposable destination for a trial:

```sh
WIP_INSTALL_DIR="$PWD/release-bin" WIP_VERSION=v0.3.0 sh scripts/install.sh
./release-bin/wip version
```

Installation replaces an existing `wip` file **or symlink** by renaming a
verified executable into place. It does not follow a development-build symlink
or overwrite its target. To replace your daily symlink, deliberately run the
installer with its default destination; no separate symlink removal is needed.
Ensure `~/.local/bin` is on PATH, run `hash -r` if your shell caches commands,
then check `command -v wip` and `wip version`. A different WIP earlier on PATH
can still shadow the installed binary.

This installs only `wip`, not the experimental `wipd`, and never activates a
daemon, changes stores, or installs harness skills automatically.

### Nix and harness projection

```
nix profile install github:procrastivity/wip/go   # the binary, system-wide
wip install                                       # project into every detected harness
```

`wip install <harness>` targets one harness; `wip uninstall <harness>`
removes exactly what install wrote; `wip doctor` reports stale or
drifted projections.

## Develop

```
direnv allow      # or: nix develop
make check        # lint + test
make hooks        # pre-commit, both stages
```

### Amp orbs

Project orbs install the locked Nix development shell and Docker Engine from
`.agents/setup`. Docker runs as the supervised `docker-daemon` service declared
in `.amp/services.yaml`; start or repair it with:

```
amp orb services ensure
make orb-smoke
```

`make orb-smoke` builds and runs two hardened containers on an internal Compose
network with separate named volumes, verifies their byte exchange, and removes
all runtime resources. The smoke refuses bind mounts, Docker-socket mounts,
privileged or host-network containers, inherited credentials, and legacy-store
routes.

Orb files and Docker state are disposable. GitHub commits and Linear roadmap
state are the recovery anchors. If dogfooding needs a legacy WIP store, create a
fresh orb-local store; never import, mount, copy, or route to a live store from
another machine.

Version stamps come from release tags (`git describe --match 'v[0-9]*'`);
pushing an annotated `vX.Y.Z` tag is the only human release action.
CHANGELOG.md is generated per release, never committed.

## Design of record

The design of record lives in a sidecar planning repository (`wip-reboot`:
MODEL.md, one workplan per Matter), deliberately outside this repo.
Decisions worth keeping land here as findings on the Matters that made
them; the code and its comments carry the rest.
