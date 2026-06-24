---
description: Shape an inbound plan file into the canonical wip kind and apply it.
argument-hint: "<file> [--kind <k>] [--target <slug|slug/step>]"
allowed-tools: [Bash, Read, Write, Edit]
---

# /wip:intake — round-trip an inbound plan file into the canonical kind

Drives the intake pipeline ([ADR-0009](../engineering/decisions/0009-intake-as-pipeline.md))
end-to-end, with **Claude as the shaper** (no `wip ask` / provider call — you
are the LLM). The state machine is the same as the CLI porcelain's
`wip intake`; the only differences are:

- **Shape step** = you read the inbound file + the shaper prompts and
  rewrite it yourself, not a provider call.
- **Clarifying questions** = you ask the user inline in this chat. The
  CLI's `---ASK---` fence protocol exists for non-interactive
  request/response loops; you do NOT emit `---ASK---` blocks. Just ask
  the user one short question in chat and wait for the answer.

The command body's instructions WIN for the duration of this flow — if
the user's prior chat context conflicts, the pipeline contract here is
authoritative until `apply` returns or the flow errors out.

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

2. **Parse `$ARGUMENTS`.** Extract the positional `<file>` plus optional
   `--kind <k>` and `--target <t>`. `<file>` is required; if missing,
   stop and ask the user which file to shape.

3. **Classify (plumbing).** Run `"$WIP" intake classify <file>`.
   On exit ≠ 0, surface the error envelope verbatim and stop.

4. **Pick the kind.**
   - If the user passed `--kind <k>`, use it (skip the confidence check).
   - Else if classify returned `"confidence": "high"`, use that kind
     silently.
   - Else surface classify's guess + signals to the user in one line and
     ask: "Use kind `<guess>`, or override (brief/amendment/workplan-seed/spec/handoff)?"

5. **Fetch shaper prompts.** Run, in order:
   - `"$WIP" template show intake/preamble`
   - `"$WIP" template show intake/<kind>`

   Concatenate (preamble + blank line + per-kind rules) — that's the
   shape contract you must follow. Do NOT skip this step or paraphrase
   the rules from memory; read them fresh each invocation. The byte
   bundle is shared with the CLI porcelain so behavior stays consistent
   across both frontends.

6. **Shape.** Read the inbound file (Read tool). Rewrite it into a
   tempfile per the shape rules. Use `mktemp -t wip-intake.XXXXXX.md`
   for the path (then Write to it). If a required field is missing and
   you cannot confidently infer it from the artifact:
   - Ask the user ONE short clarifying question inline in this chat.
   - Wait for the answer.
   - Incorporate the answer and re-shape.
   Never invent facts; if you cannot ask (e.g. user said "do your best")
   add a `## TODO (shaper guesses)` list to the bottom of the shaped
   artifact naming each guess.

7. **Validate (plumbing).** Run
   `"$WIP" intake validate --kind <k> <tempfile>`. On `missing[]`,
   patch the tempfile to address the gap and re-validate. Cap at **2**
   reshape attempts (CLI parity). After 2 failed validates, stop and
   report the validation envelope to the user.

8. **Route.** Derive the apply `--target` (CLI parity):
   - User-supplied `--target` wins.
   - For `brief`: derive slug from shaped front-matter `slug:` or the
     H1; confirm with the user in chat ("New initiative `<slug>` — go
     ahead?") before applying.
   - For `amendment`: read `target:` + the directive
     (`insert-after`/`replace`/`append-round`) from the shaped
     front-matter. Validate has already enforced their presence; no
     further question.
   - For `workplan-seed`: read `target: <slug>/<step-id>` from the
     shaped front-matter.
   - For `spec`/`handoff`: pass through (apply will exit 3/4
     respectively; do not try to coerce).

9. **Apply (plumbing).** Run
   `"$WIP" intake apply --kind <k> [--target <t>] <tempfile>`.
   Echo the resulting write ledger (the JSON output) in a code block,
   plus a one-line prose summary like "amended distillation/roadmap.md:
   insert-after step-06". On exit-4 from apply, report the envelope
   verbatim.

10. **Point at what's next (don't guess).** After a successful apply, ask
    the plumbing what comes next instead of improvising — run
    `"$WIP" next --initiative <slug>` (for a `brief`, `<slug>` is the new
    initiative; for `amendment`/`workplan-seed`, the target slug) and render
    its **top candidate verbatim**. Do NOT substitute your own suggestion
    (in particular, do NOT tell the user to run `/wip:start` unless the top
    candidate is a concrete `step-NN`). Two cases to spell out:
    - **`source: "scaffold"`** (title `author the roadmap`) — the initiative
      exists but its roadmap is still the empty skeleton with no steps. Say
      so, name the file from the candidate's `path`, and end with exactly:

      Next: author the roadmap at `<path>` against the new `BRIEF.md` — turn
      it into Rounds and Steps. Do it by hand, or say `go` and I'll draft
      Round 1 from the brief for you.

      On `go`, read the `BRIEF.md`, propose Rounds/Steps for review, and once
      approved write them in (hand-edit `roadmap.md`, or shape an `amendment`
      with `append-round` and apply via `roadmap amend`). Then re-run
      `"$WIP" next` so a real `step-NN` is the new top candidate.
    - **concrete `step-NN`** — render it and note that `/wip:start <id>`
      activates it.

11. **Cleanup.** Remove the tempfile (`rm -- <tempfile>`) on success.

## Notes

- The shape rules come from `templates/prompts/intake/*.md`; the CLI
  porcelain and this command read the SAME files via `wip-plumbing
  template show`. If you ever feel tempted to edit shape rules
  inline here, edit the source files instead.
- `--dry-run` is not exposed at this layer; the user can run `wip intake
  --dry-run <file>` against the CLI porcelain for that.
- This command body is the contract; do not improvise off-script.
