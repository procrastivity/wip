---
description: Activate a roadmap step and brief it, then offer to start the work.
argument-hint: "<step-id> [--initiative <slug>] [--agent <name|id>]"
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
   elif WIP="$(ls -d "$HOME"/.claude/plugins/cache/*/wip/*/bin/wip-plumbing 2>/dev/null | sort -V | tail -1)" && [[ -n "$WIP" && -x "$WIP" ]]; then
     : # bundled copy from the installed plugin cache (CLAUDE_PLUGIN_ROOT not exported to this shell)
   elif command -v wip-plumbing >/dev/null 2>&1; then
     WIP="wip-plumbing"
   else
     echo "wip-plumbing not found — enable the wip plugin (settings.json → enabledPlugins) or install it (see the project README)"
   fi
   ```
   If the resolver printed the not-found message (`$WIP` unset), stop. Use
   `"$WIP"` in place of `wip-plumbing` for every command below; re-run this
   resolver if a later step starts in a fresh shell.

2. **Parse `$ARGUMENTS`.** Extract the positional `<step-id>` plus optional
   `--initiative <slug>` and optional `--agent <name|id>`. `<step-id>` is
   required. If it is missing, do **not** just ask for one blindly — first run
   `"$WIP" next` (forwarding `--initiative` if given) and look at the top
   candidate:
   - If it is **`source: "scaffold"`** (title `author the roadmap`), the
     roadmap has no steps yet — there is nothing to start. Tell the user the
     roadmap at the candidate's `path` is still the empty skeleton and point
     them to author it first (hand-edit it, or `/wip:intake` an amendment that
     appends a round), noting that independent steps (disjoint files, no
     ordering) can run as ADR-0010 parallel lanes rather than a forced sequence.
     Then stop — do not ask for a step-id.
   - Otherwise, list the candidate `step-NN`s and ask which one to start.

   - `--agent <name|id>` **pins which agent tool every spawn uses this
     run** — a tool **name** or a numeric tool **id** (all-digits → id;
     otherwise a name). It only takes effect on the **Orchestrate**
     hand-off (step 6); carry it through to that branch as the **session
     spawn pin** — the request override at the top of the resolver's
     fallback ladder — so it governs the Coordinator→Builder spawns for
     the rest of the run and **bypasses the resolver's interactive
     fallback prompt** (the operator pre-selects the tool instead of
     being asked when tier classification is non-confident). The command
     body does **not** persist the pin or name any backend tool — the
     live Role flow records it (see `roles/backends/solo.md` and
     `roles/tier-policy.md`).

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
         invoke `/wip:orchestrate` directly later.) If `--agent <name|id>`
         was parsed, pass it through to `/wip:orchestrate` so the session
         spawn pin is set for the run.

## Notes

- This command body is the contract; do not improvise off-script.
- `--dry-run` is not exposed at this layer; run `wip-plumbing --dry-run workplan
  init <slug> <step-id> --activate` directly to preview the activation.
