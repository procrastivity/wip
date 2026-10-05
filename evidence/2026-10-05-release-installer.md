# Release installer verification — 2026-10-05

Ubuntu x86_64 runner `ubuntu-8gb-hel1-1`, WIP `go` at
[`e767080`](https://github.com/procrastivity/wip/commit/e7670802f110df8a884e43921aaa62640ac3ff2c)
plus local installer changes. The branch initially matched origin/go;
pre-existing untracked `model-session-plan/` files were preserved. No daily
binary, live store or globally installed skill was used as a write target.

## Installed toolsmith identity

The runner's initial PATH lacks `~/.local/bin`; `command -v toolsmith`
therefore initially found nothing. With that existing directory prepended:

```text
$ PATH="$HOME/.local/bin:$PATH" command -v toolsmith
/home/dev/.local/bin/toolsmith
$ readlink -f /home/dev/.local/bin/toolsmith
/home/dev/Code/toolsmith/bin/toolsmith
$ PATH="$HOME/.local/bin:$PATH" toolsmith version
toolsmith version v0.0.3 (commit 20e9e44, built 2026-10-05T20:48:19Z)
```

`git -C /home/dev/Code/toolsmith rev-parse 'v0.0.3^{}'` resolves to
[`20e9e44b2a70e37216b28a58142b5e3e17cf2259`](https://github.com/procrastivity/toolsmith/commit/20e9e44b2a70e37216b28a58142b5e3e17cf2259).
The toolsmith checkout is clean. Both installed `toolsmith doc CONTRACT.md`
and `git show v0.0.3:CONTRACT.md` have SHA256
`9f31037975877c088795174f31817d71c864ade0c379bfc4c2b18e79bd60aa30`.
No replacement installation was performed.

## Real GitHub release, not fixture assets

GitHub's releases API reports v0.3.0, published October 5 at 20:25:16Z,
with `wip-linux-amd64`, `wip-darwin-arm64`, `SHA256SUMS`, and `CHANGELOG.md`.
It has **no `wip-install.sh` asset**. The new local script was run with the
real curl command and default GitHub repository, using pinned and latest
release URLs, in two disposable destinations under `.amp/in/installer/`.

The pinned destination initially contained a symlink to a disposable
development-build sentinel; the latest destination was absent. Commands
equivalent to the actual runs (absolute disposable paths abbreviated):

```sh
env -i PATH=/usr/bin:/bin HOME="$sandbox/home" TMPDIR="$sandbox/tmp" \
  WIP_INSTALL_DIR="$sandbox/pinned" WIP_VERSION=v0.3.0 sh scripts/install.sh
test ! -L "$sandbox/pinned/wip"
sha256sum -c "$sandbox/development-before.sha256"
env -i PATH=/usr/bin:/bin HOME="$sandbox/home" \
  XDG_DATA_HOME="$sandbox/home/data" XDG_CONFIG_HOME="$sandbox/home/config" \
  "$sandbox/pinned/wip" version

env -i PATH=/usr/bin:/bin HOME="$sandbox/home" TMPDIR="$sandbox/tmp" \
  WIP_INSTALL_DIR="$sandbox/latest" sh scripts/install.sh
env -i PATH=/usr/bin:/bin HOME="$sandbox/home" \
  XDG_DATA_HOME="$sandbox/home/data" XDG_CONFIG_HOME="$sandbox/home/config" \
  "$sandbox/latest/wip" version
```

Both installations succeeded, verified `wip-linux-amd64: OK`, and reported:

```text
wip version v0.3.0 (commit e767080, built 2026-10-05T20:24:33Z)
```

Both installed executables have SHA256
`7f791d9329daef5d48a5a3906a345a987ee6133450d13874594ebc1ce0149d77`,
matching the separately fetched v0.3.0 SHA256SUMS entry; `cmp` also confirmed
byte equality. The disposable symlink became a regular executable and its
development target's original checksum still passed. Disposable HOME and
TMPDIR remained empty after bootstrap and version checks. The installed
release's actual manifest reports `toolsmith/v1`, schema 1, 79 plumbing-kind
verbs, `next` → `plumbing next`, and no `contractReconciledMinor`.

## Fixture and packaging checks

The repository's locked development-shell tools were loaded from the existing
`.direnv/flake-profile-*.rc`; no `make build` or write to `bin/wip` was run.

`go test ./scripts -count=1 -v` passed all 19 installer cases plus
`TestReleaseInstallerPackaging`. The installer cases check selected latest
and pinned URLs, asset/platform names, a missing pin without latest fallback,
SHA256 mismatch and missing entries, failed downloads, regular-file and
symlink preservation on failure, successful symlink and dangling-symlink
replacement without target writes, executable bytes/mode, HOME-default and
relative destinations with spaces, unsupported-platform refusal before
network access, and cleanup. Darwin arm64 and its `shasum -a 256` fallback
were **simulated on Linux**, not executed on native macOS hardware.

The packaging test parses the actual release workflow and executes its
installer, checksums and publish shell commands in a fixture tree. A fake
`gh` captures the exact release-create argument list. Assertions pin copied
script bytes, both independent binary digests, explicit installer attachment,
and release notes as the body rather than a globbed asset. This verifies the
**unpublished packaging locally**; it is not evidence of a GitHub Actions run
or a published installer asset.

`shellcheck scripts/install.sh`, `golangci-lint run ./scripts/...`, and
`git diff --check` passed. The first full lint run caught one tagged-switch
suggestion in the new tests; it was fixed and the rerun reported zero issues.

The first full `make check` test run failed in existing wipd private-profile
checks because this runner inherits `umask 0002`: Go's numbered test-directory
children become group-writable, which `internal/wipdprofile/profile.go`
deliberately refuses (`Mode().Perm() & 0o022`). The parent temporary directory
was mode 0700, but the refusal correctly identified its writable children.
No daemon security check or production code was changed. With `umask 0022`
and `TMPDIR=/tmp` in the test shell, a focused uncached rerun of
`internal/wipdprofile`, `internal/wipdjournal`, and `internal/wipdremote`
passed (0.022s, 55.017s, and 0.618s respectively).

The subsequent full gate passed with the same safe test-shell permissions:

```sh
umask 0022
export TMPDIR=/tmp
make check
```

ShellCheck and golangci-lint reported zero issues, and `go test ./...`
exited 0, including `internal/cli` (145.949s), `internal/wipdauthority`
(156.152s), `internal/wipdjournal` (73.911s), and the installer package
(0.414s). The original failing run is retained separately; this is an
environment correction, not a suppressed test or relaxed security rule.

`nix build path:<disposable-source-copy> --out-link <disposable-result>` also
passed with the unchanged locked flake/vendor hash. The source copy includes
tracked files and this change's new files, excluding other untracked work;
it does not stage the original checkout. `file` reports a statically linked
x86-64 ELF; the package's bin directory contains only `wip`, with shipped
assets under `share/wip/assets`. Its version is `dev`/`unknown` with the fixed
epoch because this source-copy check has no Git metadata, not because the
release installer downloaded the wrong version. Nix's check phase exercises
the configured `cmd/wip` subpackage, not the entire repository test suite.

## Contract checker result and remaining gap

With the dev shell and installed toolsmith on PATH,
`toolsmith check . --json` exited **1**, with exactly one finding:

```json
{"findings":[{"code":"C3.10","message":"manifest --json carries no contractReconciledMinor"}],"audited":["C1.1","C1.2","C1.6","C2.1","C3.1","C3.10","C3.4","C3.6","C6.2","C6.3","C6.5","C6.6","C6.7","C7.5"]}
```

Stderr separately reports `check.findings-present` and notes multiple cmd/
entries while correctly selecting `wip`. This is not a clean-conformance
claim. The v1.3 baseline, v1.4 already-shipped verb-pair behavior, v1.5 gap,
and T37 installer adoption are recorded in
`docs/release-installer/decisions.md`. C3.10 is a follow-up outside this
installer-focused implementation, not a reason to alter binary installation.

## Safety and delivery state

Before and after real-release verification, `~/.local/bin/wip` still points
to `/home/dev/Code/wip/bin/wip`, whose SHA256 remains
`40f601cd07e3e9b0636f55c110f075e28dbb0bc9bf47d43c34bd479a81f78331`.
No system installation, global harness projection, live store, daemon
activation, branch push, tag, publication or deployment occurred. Changes
were initially left uncommitted for review. The owner subsequently authorized
official T37 adoption and a local commit of the seven installer task files;
delivery is that local adoption commit only, with no push, publication, or
live-install change. This does not close the separately recorded C3.10 gap.
A future release must include `wip-install.sh` before its standalone GitHub
bootstrap URL works; the local installer already works against the existing
v0.3.0 binary/checksum assets.

Temporary destinations, downloaded executables and the Nix source copy were
removed after verification. Review logs and observed manifests remain under
the repository-locally excluded `.amp/in/artifacts/` directory.
