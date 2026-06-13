# Workplan — step-06.5 · global project registry + `--project`

Implements [ADR-0008](../../../../engineering/decisions/0008-global-project-registry.md)
and [`engineering/specs/wip-plumbing-registry.md`](../../../../engineering/specs/wip-plumbing-registry.md)
on top of step-06's `bin/wip-plumbing` + `lib/wip/` layout.

## Decisions (made here, feed later steps)

- **Layout:** new `lib/wip/wip-plumbing-registry-lib.bash` (functions prefixed
  `wip_registry_*`) sourced by the dispatcher; new
  `lib/wip/wip-plumbing-subcommands/project.bash` defining `wip_plumbing_cmd_project`.
- **Tests:** same plain-bash harness step-06 established (`test/helpers.sh` +
  `test/test-*.sh`). Do **not** introduce bats. Use `WIP_REGISTRY_FILE=<tmp>` and
  `XDG_STATE_HOME=<tmp>` to point the registry at fixtures.
- **Touchpoint:** add one call to `wip_registry_touch` from `wip_find_root` (or its
  caller) — the moment any verb has resolved an absolute root. Suppression checks
  (`WIP_NO_REGISTRY`, `plumbing.register: false`) live in `wip_registry_touch` itself.
- **`--project` arg-prelude:** shared prelude in the dispatcher resolves `--project
  <id>` to `WIP_ROOT=<abs-path>` before the verb runs. Existing verbs need no
  changes beyond declaring they accept the flag in `--help`.

## Chunks

1. **registry lib** — `wip_registry_path`, `wip_registry_with_lock`,
   `wip_registry_segment_encode` / `_decode`, `wip_registry_touch <abs> <slug> <remote>`
   (fast-then-slow upsert), `wip_registry_iter`, `wip_registry_resolve <id>` per spec §4.
   Errors swallowed by default; `WIP_VERBOSE=1` emits one-line stderr diagnostics.
2. **`project` subcommand** — `list [--json] [--prune]` / `register [<path>] [--slug]`
   / `resolve <id>` / `forget <id>` per spec §5.
3. **dispatcher wiring** — register `project` verb; add `--project` arg-prelude that
   calls `wip_registry_resolve` and exports `WIP_ROOT`. Wire `wip_registry_touch` into
   the post-resolution path.
4. **doc updates** — append `project` to spec §1 verb table; document `--project` in
   spec §2 "Common flags"; mention `WIP_NO_REGISTRY` / `WIP_REGISTRY_FILE` alongside
   `WIP_LIB` / `WIP_ROOT`; add a `project` entry in spec §3.

## Test strategy

`WIP_REGISTRY_FILE=<tmp>` plus `WIP_ROOT=<fixture>` cover:

- first-touch creates the file with one record (`id`, `slug=null`, `remote=null|<git>`);
- second touch within 60s no-ops (mtime unchanged);
- second touch after backdating `last_seen` rewrites; `slug` change propagates;
- `WIP_NO_REGISTRY=1` and `plumbing.register: false` both suppress writes;
- unwritable `$XDG_STATE_HOME` → calling verb still exits 0 with valid JSON;
- `project list / register / resolve / forget` round-trips;
- `detect --project <segment>`, `--project <slug>`, `--project <abs-path>` all
  resolve; ambiguous slug → exit 4; unknown id → exit 3;
- segment encode/decode is a bijection over realistic paths (spaces, dots).

## Definition of done

- `make check` green; all new `test/test-*.sh` pass.
- `bin/wip-plumbing detect` against this repo leaves
  `~/.local/state/wip/projects.jsonl` with one valid record.
- `bin/wip-plumbing detect --project wip` (after `project register --slug wip`)
  works from `/tmp`.
- `chmod 000 ~/.local/state/wip && bin/wip-plumbing detect` still exits 0 with valid
  JSON.
- `bin/wip-plumbing doctor` on this repo still passes — no drift introduced.
- Spec [`wip-plumbing-cli.md`](../../../../engineering/specs/wip-plumbing-cli.md)
  updated per "doc updates" above.

## Open questions to resolve during execution

- **Slug canonical home.** Two viable spots: (a) `.wip.yaml` top-level `slug:`
  (committed, team-wide); (b) registry record only (machine-local, per-developer).
  Plan supports both — if `.wip.yaml` has `slug:`, that's the default; `project
  register --slug` overrides it locally. Confirm during impl.
- **`init` and `--project`.** `init` with a slug scaffolds inside the repo
  `--project` points at; `init` at repo level remains path-only. Decide if
  `--project` should be rejected in the path-only mode or accepted as a no-op.
