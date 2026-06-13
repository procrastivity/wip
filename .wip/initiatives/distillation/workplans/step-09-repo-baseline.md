# Workplan — step-09 · repo baseline

The dogfood test for the eventual `wip setup` family. We bootstrap — by hand,
on this very repo — the four files that the porcelain will later install for
*other* repos: `flake.nix`, `.envrc`, `.pre-commit-config.yaml`, and the
Makefile additions that tie them together. After this step a fresh contributor
can clone the repo, `direnv allow`, and have a green `make check` in one shot
— and pre-commit blocks bad patches before they hit the branch.

## Decisions (made here, feed later steps)

- **DevShell-only flake.** No packaging the CLI yet. `flake.nix` exposes one
  `devShells.default` carrying every tool the Makefile and tests need:
  `bash`, `jq`, `yq-go`, `shellcheck`, `shfmt`, `git`, `gnumake`, `coreutils`,
  `pre-commit`. Inputs: `nixpkgs` pinned to `nixos-25.05` (current stable).
  `flake-utils` for `eachDefaultSystem`.
- **.envrc is a nix-direnv shim.** Two lines: `use flake` plus a `if ! has
  nix; then` fallback that prints a friendly hint. Matches what the future
  `wip setup direnv` will emit. `.envrc` is the sentinel for the `direnv`
  feature (already mapped in `wip-plumbing-lib.bash:114`), so this step also
  flips `features.direnv.enabled: true` in `.wip.yaml`.
- **pre-commit hooks are local.** No upstream `repo:` entries — every hook
  shells out to either `shfmt`, `shellcheck`, or `make test`. Keeps the
  dependency graph identical to `make check` and means the only thing
  pre-commit adds is *when* the checks run (pre-commit) and *how* git
  enforces them. Stock whitespace/EOF hooks from `pre-commit-hooks` are
  fine to include because they're tiny and zero-config.
  - `shfmt-diff` — `shfmt -d -i 2 -ci` over the same SRC list the Makefile
    uses.
  - `shellcheck` — `shellcheck -x` over the same SRC list.
  - `wip-tests` — `make test` (the whole suite; fast enough at this size).
  - Stock `trailing-whitespace` + `end-of-file-fixer` + `check-yaml` +
    `check-merge-conflict`.
- **Makefile gains exactly one target.** `make hooks` runs `pre-commit
  install` (idempotent). No `make setup` aggregator yet — that's the job of
  the future `wip setup` porcelain, and we don't want to invent a parallel
  vocabulary here.
- **No flake.lock pinning ceremony.** `nix flake update` once at land time;
  thereafter Renovate / manual bumps. The lock is committed.
- **Dogfood criterion (the actual shipping signal).**
  1. `nix develop --command make check` exits 0 from a fresh clone.
  2. `nix develop --command bin/wip-plumbing doctor` reports zero drift
     (including the newly-enabled `direnv` feature picking up `.envrc`).
  3. `nix develop --command pre-commit run --all-files` exits 0.
- **Out of scope.** Packaging `wip-plumbing` as a flake `package`,
  CI workflows (`.github/workflows/*`), changelog tooling, agent registration
  scripts. All deferred to their own steps (changelog → step-14, plugin →
  step-11, CI → backlog).

## Chunks

1. **`flake.nix` + `flake.lock`.** Author the devShell; run `nix flake
   update` to materialise the lock; verify `nix develop --command bash -c
   'jq --version && yq --version && shellcheck --version && shfmt --version
   && pre-commit --version'`.
2. **`.envrc`.** Two-line nix-direnv shim + fallback. (Don't run `direnv
   allow` here; that's the user's call once they've reviewed the diff.)
3. **`.pre-commit-config.yaml`.** Local hooks per the decision above. Verify
   `nix develop --command pre-commit run --all-files` exits 0.
4. **Makefile: add `hooks` target.** One target, two lines.
5. **`.wip.yaml`: flip `features.direnv.enabled` to `true`.** Verify doctor
   no longer flags `present-but-undeclared`.
6. **README update.** One short "Develop" subsection: `direnv allow` →
   `make hooks` → `make check`. Keep it terse.
7. **Mark step-09 shipped on the roadmap.** Bump `active_step` to `step-10`
   so `next` ranks Round 3 correctly. Commit.

## Test strategy

This step has no new bash code in `lib/wip/` or `bin/` — the tests are the
three dogfood commands listed above, run inside `nix develop`. No new
`test/test-*.sh` file is added.

- **Lint/test parity.** Pre-commit must run the same commands the Makefile
  runs. If `make check` is green but pre-commit fails (or vice versa), that's
  a regression in this step's invariants.
- **Doctor parity.** The `direnv` feature flipping to active must coincide
  exactly with `.envrc` landing on disk. If doctor reports `direnv` active
  without `.envrc` present (or absent with `.envrc` present), invariants are
  broken.
- **Manual smoke.** Run each dogfood command and capture the exit code in
  the commit message body so reviewers can confirm.

## Definition of done

- `flake.nix`, `flake.lock`, `.envrc`, `.pre-commit-config.yaml` committed.
- Makefile has a `hooks` target.
- `.wip.yaml` has `features.direnv.enabled: true`.
- `nix develop --command make check` exits 0.
- `nix develop --command bin/wip-plumbing doctor` exits 0 with no drift.
- `nix develop --command pre-commit run --all-files` exits 0.
- README has a short Develop section pointing to the three-command path.
- Roadmap entry marked `✅ shipped 2026-06-13`; `active_step` advanced to
  `step-10`.

## Open questions to resolve during execution

- **Pin nixpkgs to `nixos-25.05` or `nixos-unstable`?** Lean: **25.05**.
  Stable channel matches what most contributors will already have, and the
  devShell deps (jq, yq-go, shellcheck, shfmt) are mature — no reason to
  chase HEAD.
- **Should the `wip-tests` pre-commit hook be `pass_filenames: false` (run
  the whole suite always) or filtered to bash files?** Lean: **whole
  suite, pass_filenames: false**. The suite is fast at this size and a
  template change can break a test without touching a `.bash` file.
- **Include `.pre-commit-config.yaml` itself under the `check-yaml` hook?**
  Lean: **yes**. Catches a corrupted config the same way it'd catch a
  corrupted `.wip.yaml`. Free.
- **Should `flake.nix` also include `git-cliff` (for the future
  `changelog` feature)?** Lean: **no, not yet**. step-14 will add it when
  the feature actually lands. Keeps the dev-shell minimal.
