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

Install the latest release to `~/.local/bin/wip` — no checkout or Go toolchain
required. The installer supports **Linux amd64** and **macOS arm64** and requires
`curl` plus either `sha256sum` or `shasum`.

```sh
curl -fsSL https://github.com/procrastivity/wip/releases/latest/download/wip-install.sh -o /tmp/wip-install.sh
sh /tmp/wip-install.sh
```

Put the destination on PATH and verify which binary your shell selects:

```sh
export PATH="$HOME/.local/bin:$PATH"
hash -r
command -v wip
wip version
```

Keep the PATH setting in your shell's startup configuration for future sessions.
Re-run the installer to update. Installation replaces an existing `wip` file
**or symlink** with the verified binary; it never follows a development-build
symlink or overwrites its target. No separate symlink removal is needed.

This installs only `wip`, not the experimental `wipd`, and never activates a
daemon, changes stores, or installs harness skills automatically.

### Optional: agent harness skills

Once the binary is on PATH, install its generated skills separately:

```sh
wip install                 # project into every detected harness
```

`wip install <harness>` targets one harness; `wip uninstall <harness>`
removes exactly what install wrote; `wip doctor` reports stale or
drifted projections.

### Installation options and verification

**Pin a release.** `WIP_VERSION` selects the binary's release tag. To pin both
the installer and the binary, use the same tag in the download URL and the
environment. For example, [v0.3.1](https://github.com/procrastivity/wip/releases/tag/v0.3.1)
publishes both:

```sh
curl -fsSL https://github.com/procrastivity/wip/releases/download/v0.3.1/wip-install.sh -o /tmp/wip-install.sh
WIP_VERSION=v0.3.1 sh /tmp/wip-install.sh
```

**Choose a destination.** Set `WIP_INSTALL_DIR` to override `~/.local/bin`.
For a disposable trial without replacing your daily binary:

```sh
install_dir=$(mktemp -d)
WIP_INSTALL_DIR="$install_dir" WIP_VERSION=v0.3.1 sh /tmp/wip-install.sh
"$install_dir/wip" version
```

`WIP_BASE_URL` selects another trusted GitHub repository with the same release
asset layout. Windows and other architectures are not published by this pipeline.

**Download verification.** You can inspect `/tmp/wip-install.sh` before running
it. The installer downloads the selected binary and `SHA256SUMS` from the same
release and checks the binary before replacing the destination. The script and
checksum file are trusted GitHub/HTTPS inputs: the checksum detects transfer
corruption, not a compromised release or installer.

**Nix alternative.** Install the binary from the Go branch instead of using a
release asset, then use the same optional harness step above:

```sh
nix profile install github:procrastivity/wip/go
```

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
