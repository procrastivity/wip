# `/wip:start` + `workplan init --activate` (spec)

Status: shipped 2026-06-16.

Closes the "how do I actually kick off a step by name?" gap. Today `/wip:next` is
advisory and `workplan init` (the scaffold) is plumbing-only with no plugin surface, and
nothing *sets* `initiatives.<slug>.active_step` (status/next only read it). This adds a
deterministic activate path in plumbing and a `/wip:start` plugin command on top of it.

No ADR — additive (a flag + a plugin command); ADR-0001's seam is preserved (the manifest
edit is deterministic plumbing; the plugin only orchestrates + does the LLM work).

## 1. Plumbing — `workplan init … --activate`

Extend the existing verb (see `wip-plumbing-cli.md` workplan section):

```
wip-plumbing workplan init <slug> <step-id> [--from <seed>] [--slug <s>] [--force] [--activate]
```

- `--activate` — after the workplan is in place, set `active_step` for that initiative in
  `.wip.yaml` (deterministic manifest edit, same machinery as `setup`'s feature-flag flips
  / `roadmap amend`). The exact key is whatever `detect`/`status` read today
  (`initiatives.<slug>.active_step`); reuse, don't invent.
- **Idempotent with an existing workplan.** Without `--activate`, an existing workplan is
  still `file-exists` (exit 4). *With* `--activate`, an existing workplan is **not** an
  error — skip the write, still set `active_step`, and report it (so "start" is re-runnable
  and works for a step whose workplan already exists).
- Ledger gains `active_step: <step-id>` (and, when nothing changed, the usual idempotent
  shape). `--dry-run` shows the would-be activation without touching the manifest.
- Validation unchanged: step must be in the roadmap (`step-not-in-roadmap`, exit 4),
  initiative must be in the manifest (`unknown-initiative`, exit 3).

## 2. Plugin — `/wip:start <step-id>`

```
argument-hint: "<step-id> [--initiative <slug>]"
allowed-tools: [Bash, Read, Write, Edit]
```

Procedure:

1. **Resolve `wip-plumbing`** — the `${CLAUDE_PLUGIN_ROOT}/bin/wip-plumbing` step copied
   from `commands/intake.md`.
2. **Resolve the initiative** — `--initiative <slug>` if given, else the current one (as
   `status` resolves it). Stop with a clear message if ambiguous/none.
3. **Activate** — run `wip-plumbing workplan init <slug> <step-id> --activate`. Surface any
   error envelope verbatim (e.g. `step-not-in-roadmap`).
4. **Brief the step** — read the roadmap step's title + body and the workplan file; print a
   tight summary: one line on what `<step-id>` is, plus the workplan path.
5. **Offer, don't auto-run.** End with exactly: **"Say `go` and I'll start working on it."**
   Do not begin editing code until the user says go. On `go`, work the step against the
   workplan (Claude is the agent), asking clarifying questions inline as needed.

This is set-up + hand-off, not orchestration — the future `wip spawn`/`wip orchestrate`
(deferred) remain the path for fanning work across agents.

## 3. Tests

- `test/test-workplan-init.sh` — `--activate` sets `active_step` (assert via `status`/the
  manifest); `--activate` on an existing workplan is a no-op write but still activates (no
  exit 4); `--dry-run --activate` touches nothing; non-roadmap step still exits 4.
- `test/test-plugin-manifest.sh` — `/wip:start` present, front-matter, bundled-binary
  resolution, the literal "Say `go`" offer string, no auto-run.
