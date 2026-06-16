---
description: Activate a roadmap step and brief it, then offer to start the work.
argument-hint: "<step-id> [--initiative <slug>]"
allowed-tools: [Bash, Read, Write, Edit]
---

# /wip:start — kick off a roadmap step by name

Turns "start `<step-id>`" into a deterministic activation plus a tight brief.
This is **set-up + hand-off** — it activates the step
(`initiatives.<slug>.active_step`), scaffolds/locates its workplan, briefs you,
and then waits for your `go`. How the work runs once you say `go` — solo in this
session, or fanned across agents via the Roles — is decided at hand-off (step 6),
not here.

The activation is deterministic plumbing (`wip-plumbing workplan init …
--activate`); Claude only orchestrates the call and does the LLM brief on top.

## Procedure

1. **Resolve `wip-plumbing`.** The plugin bundles the CLI; prefer the bundled
   copy, then an explicit override, then PATH. Run once:
   ```bash
   if [[ -n "${CLAUDE_PLUGIN_ROOT:-}" && -x "$CLAUDE_PLUGIN_ROOT/bin/wip-plumbing" ]]; then
     WIP="$CLAUDE_PLUGIN_ROOT/bin/wip-plumbing"
   elif [[ -n "${WIP_PLUMBING_BIN:-}" && -x "${WIP_PLUMBING_BIN}" ]]; then
     WIP="$WIP_PLUMBING_BIN"
   elif command -v wip-plumbing >/dev/null 2>&1; then
     WIP="wip-plumbing"
   else
     echo "wip-plumbing not found — enable the wip plugin or install it (see the project README)"
   fi
   ```
   If the resolver printed the not-found message (`$WIP` unset), stop. Use
   `"$WIP"` in place of `wip-plumbing` for every command below; re-run this
   resolver if a later step starts in a fresh shell.

2. **Parse `$ARGUMENTS`.** Extract the positional `<step-id>` plus optional
   `--initiative <slug>`. `<step-id>` is required; if missing, stop and ask the
   user which step to start.

3. **Resolve the initiative.** If the user passed `--initiative <slug>`, use it.
   Otherwise resolve the current one the way `/wip:status` does — run
   `"$WIP" detect` and read `.current_initiative`. If that is null and no
   `--initiative` was given, stop and ask the user which initiative to start in.

4. **Activate (plumbing).** Run
   `"$WIP" workplan init <slug> <step-id> --activate`. This sets
   `active_step` and scaffolds the workplan if absent; an existing workplan is
   kept (no error). On exit ≠ 0, surface the error envelope verbatim (e.g.
   `step-not-in-roadmap`, `unknown-initiative`) and stop. Note the workplan path
   from the ledger's `wrote[]` or `skipped[]`.

5. **Brief the step.** Read the roadmap step's title + body (from
   `.wip/initiatives/<slug>/roadmap.md`) and the workplan file (the path from
   step 4). Print a tight summary: one line on what `<step-id>` is, plus the
   workplan path on its own line.

6. **Offer, don't auto-run.** End with exactly this line, verbatim:

   Say `go` and I'll start working on it.

   Do NOT begin editing code until the user says `go`. On `go`, establish your
   role first — do not assume you are the worker:

   - **Check whether you already hold a WIP role** via the active orchestration
     backend (e.g. Solo `whoami` / your process name; see `roles/backends/`).
   - **If you already hold a WIP role**, defer: keep acting in that role per its
     manual (`roles/<role>.md`); do not re-drive a start from inside a
     Coordinator/Researcher/Builder.
   - **If you're a plain session** (no WIP role), you own this step — ask the
     user how to run it instead of choosing silently:
       - **Solo here** — work the step yourself against the workplan, asking
         clarifying questions inline.
       - **Orchestrate** — hand off to **`/wip:orchestrate`**, the ergonomic
         wrapper for this branch: it preps the active step and has you become
         the Orchestrator (`roles/orchestrator.md` + `roles/backends/solo.md`)
         to spawn a Coordinator for the active step via Solo. (Run it now, or
         invoke `/wip:orchestrate` directly later.)

## Notes

- This command body is the contract; do not improvise off-script.
- `--dry-run` is not exposed at this layer; run `wip-plumbing --dry-run workplan
  init <slug> <step-id> --activate` directly to preview the activation.
