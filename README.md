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

Version stamps come from release tags (`git describe --match 'v[0-9]*'`);
pushing an annotated `vX.Y.Z` tag is the only human release action.
CHANGELOG.md is generated per release, never committed.

## Design of record

The design of record lives in a sidecar planning repository (`wip-reboot`:
MODEL.md, one workplan per Matter), deliberately outside this repo.
Decisions worth keeping land here as findings on the Matters that made
them; the code and its comments carry the rest.
