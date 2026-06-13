# Workplan — step-06 · `detect` + `doctor`

Builds the first `wip-plumbing` code against [`engineering/specs/wip-plumbing-cli.md`](../../../../engineering/specs/wip-plumbing-cli.md).
Also establishes the repo's `bin/`/`lib/`/`test/` layout and dep choices for all of Round 2.

## Decisions (made here, feed later steps)

- **Deps:** bash 5+, `jq`, `yq` (yq-go v4), `git`. `.wip.yaml` is read by `yq -o=json '.'`
  once → all logic in `jq`. Dev: `shellcheck`, `shfmt`. (Flake wiring is step-09.)
- **Layout (clast shape):** `bin/wip-plumbing` dispatcher → sources `lib/wip/wip-plumbing-lib.bash`
  + `lib/wip/wip-plumbing-subcommands/<verb>.bash` (each defines `wip_plumbing_cmd_<verb>`).
- **Tests:** plain-bash harness (`test/helpers.sh`), no bats; fixtures built in `mktemp` dirs
  driven by `WIP_ROOT`.

## Chunks

1. **lib** — `wip-plumbing-lib.bash`: `wip_find_root` (walk up to `.wip.yaml`), `wip_manifest_json`
   (yq→json), `wip_features_json` (resolve sentinel existence + drift), `wip_die` (error envelope),
   `wip_usage`/`wip_version`.
2. **detect** — emit `{ok,root,wip_yaml,current_initiative,features,initiatives}` per spec §3.
3. **doctor** — feature drift + initiative registry-vs-disk + lds/diataxis root collision;
   `drift_count`; exit 4 on drift. `--fix` accepted but **advisory in v1** (warns, writes nothing;
   real autofix deferred — note in spec).
4. **harness + Makefile** — `test/helpers.sh`, `test/test-detect.sh`, `test/test-doctor.sh`;
   `Makefile` (`fmt/lint/test/check/deps-check`).

## Test strategy

`WIP_ROOT=<tmp>` points the CLI at a fixture repo. Cover: active-no-sentinel (solo),
active-with-sentinel (changelog), declared-but-missing (lds), initiative count;
doctor exit 4 on drift then exit 0 once the sentinel exists.

## Definition of done

- `make check` (shellcheck + shfmt + tests) green under the nix toolchain.
- `detect`/`doctor` run against this repo's real `.wip.yaml` and produce valid JSON.
- Spec note added that `doctor --fix` is advisory in v1.
