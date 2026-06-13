# Spec — Global project registry & `--project` selector (v1)

- Status: draft
- Date: 2026-06-13
- Initiative: distillation · roadmap **step-06.5**
- Decisions: [ADR-0008](../decisions/0008-global-project-registry.md) (registry as
  derived cache), [ADR-0002](../decisions/0002-wip-yaml-manifest-and-detection.md)
  (manifest is source of truth)

Defines the on-disk registry format, write semantics, the `wip-plumbing project`
verb, and the cross-cutting `--project <id>` flag. Consumed by `bin/wip-plumbing`
and the per-verb subcommand modules under `lib/wip/wip-plumbing-subcommands/`.

---

## 1. Storage

- **File:** `$XDG_STATE_HOME/wip/projects.jsonl` (default
  `~/.local/state/wip/projects.jsonl`).
- **Override:** `WIP_REGISTRY_FILE=<path>` (parallels xcind's `XCIND_REGISTRY_FILE`).
- **Suppress:** `WIP_NO_REGISTRY=1` (env) or `plumbing.register: false` in `.wip.yaml`
  suppresses both reads and writes for that invocation.
- **Lock file:** sibling `projects.lock`; exclusive `flock(1)` when available, lock-
  free fallback otherwise (matches xcind's stance).

## 2. Record shape

One JSON object per line. Field order is not significant; readers must tolerate
unknown fields.

```json
{"id":"-Users-beausimensen-Code-wip","path":"/Users/beausimensen/Code/wip","slug":"wip","first_seen":"2026-06-13T14:22:01Z","last_seen":"2026-06-13T14:22:01Z","remote":"git@github.com:dragonmantank/wip.git"}
```

| Field        | Type           | Notes                                                                                                                |
|--------------|----------------|----------------------------------------------------------------------------------------------------------------------|
| `id`         | string         | Dash-encoded absolute path segment. **Primary key.** Always present. Reversible: replace `-` → `/`, prepend `/`.     |
| `path`       | string         | Absolute repo root. Usually equals decode(`id`); kept explicit for safety.                                          |
| `slug`       | string \| null | Opt-in shorter name. Set via `project register --slug`, or from a top-level `slug:` in `.wip.yaml` if present.        |
| `first_seen` | string         | ISO-8601 UTC.                                                                                                        |
| `last_seen`  | string         | ISO-8601 UTC.                                                                                                        |
| `remote`     | string \| null | `git config --get remote.origin.url` at first sight; refreshed on the slow-path rewrite. `null` if not a git repo.   |

## 3. Write semantics

Triggered after every `wip-plumbing` verb that successfully resolves `.wip.yaml`
(unless suppressed per §1).

1. Acquire exclusive lock on `projects.lock` (or skip if no `flock`).
2. **Fast path:** if a record for `id` exists with `last_seen` within the last 60s
   and `slug`/`path`/`remote` unchanged, return without rewriting.
3. **Slow path:** stream existing records, update the matching record (or append if
   none), write to `projects.jsonl.tmp`, atomic-rename over `projects.jsonl`.
4. Any error — unwritable directory, malformed line, lock contention — is swallowed.
   The calling verb's exit code and stdout are unaffected. With `-v`, a one-line
   diagnostic is written to stderr.

There is **no automatic cleanup.** Stale entries persist until `project list --prune`.

## 4. Identifier resolution (`--project <id>` and `project resolve`)

Three accepted forms; resolution tries them in order and returns the first match:

| Form              | Example                              | Source                                       |
|-------------------|--------------------------------------|----------------------------------------------|
| Absolute path     | `/Users/beau/Code/wip`               | Filesystem. No registry needed.              |
| Segment (default) | `-Users-beausimensen-Code-wip`       | Decoded directly; verified against registry. |
| Slug (opt-in)     | `wip`                                | Registry record's `slug` field; must be unique. |

Resolution order:

1. If `<id>` parses as an existing absolute path containing `.wip.yaml`, use it.
2. Else look up in the registry: exact match on `id` → use record's `path`; exact
   match on `slug` → use record's `path`. Ambiguous slug → exit 4 with the candidate
   records listed on stderr.
3. Else → exit 3 (not found).

`WIP_ROOT` is unchanged and remains equivalent to `--project <abs-path>`. If both
are set, `--project` wins for that invocation.

## 5. `wip-plumbing project` verb

A new top-level plumbing verb. All subcommands are deterministic (no LLM, no
judgment).

| Subcommand                                          | Behavior                                                                                                                                                                              |
|-----------------------------------------------------|---------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------|
| `project list [--json] [--prune]`                   | Enumerate registered projects. Default output: table with columns `ID  SLUG  PATH  LAST_SEEN`. `--json` emits the raw JSONL stream. `--prune` first removes records whose `path` no longer exists or no longer contains `.wip.yaml`. |
| `project register [<path>] [--slug <slug>]`         | Idempotent upsert. `<path>` defaults to `$PWD`. Sets/updates `slug`. Useful for backfilling pre-existing projects or renaming.                                                          |
| `project resolve <id>`                              | Resolve a path/segment/slug per §4. Exit 0 + JSON record on success; exit 4 + candidate list on ambiguity; exit 3 if not found. Used internally by `--project` and exposed for porcelain. |
| `project forget <id>`                               | Remove a record. Does not touch the project's `.wip.yaml` or files.                                                                                                                   |

Output of `project list` deliberately echoes `docker compose ps`: stable machine ID
first, friendly slug second, full path third.

## 6. `--project <id>` flag on existing verbs

Added to every verb in [`wip-plumbing-cli.md`](./wip-plumbing-cli.md) §3 that
performs the `$PWD` walk-up: `detect`, `doctor`, `status`, `next`, `init`. `intake`
operates on file paths and stays as-is.

Implementation note: a shared arg-prelude converts `--project <id>` into
`WIP_ROOT=<resolved-path>` before the verb's logic runs, so the existing walk-up
code path stays unchanged.

When `--project` is **not** passed, walk-up behavior is unchanged.

## 7. Exit codes

In addition to the existing codes in [`wip-plumbing-cli.md`](./wip-plumbing-cli.md):

- `3` — `--project <id>` or `project resolve <id>` matched nothing.
- `4` — slug is ambiguous across multiple registered projects.

## 8. Out of scope (v1)

- Worktree grouping by `remote`. The field is captured; no verb consumes it yet.
- Per-project preferences or other machine-local state — the registry is identity
  only.
- Sync across machines. The registry is intentionally local-only.
