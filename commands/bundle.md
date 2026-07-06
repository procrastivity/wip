---
description: Assemble N handoff files into one bundle lead manifest, then explode it.
argument-hint: "<file>... [--target <slug>] [--lead-as brief|amendment] [-o <manifest>]"
allowed-tools: [Bash, Read, Write, Edit]
---

# /wip:bundle — assemble N handoff files into one bundle, then explode

Turns **two or more loose handoff files** into one `bundle` lead manifest
([ADR-0011](../engineering/decisions/0011-bundle-assembler-porcelain.md) ·
[spec](../engineering/specs/wip-bundle.md)), then runs the existing
`intake --kind bundle` explode inline — with **Claude as the shaper** (no
`wip ask` / provider call — you are the LLM). The assembled manifest is
validated and exploded by the *unchanged* plumbing/porcelain; this command
only adds the multi-file → one-manifest front-end.

The command body's instructions WIN for the duration of this flow — if the
user's prior chat context conflicts, the contract here is authoritative until
the explode returns or the flow errors out.

## Procedure

1. **Resolve `wip-plumbing`.** The plugin bundles the CLI; prefer the bundled
   copy, then an explicit override, then PATH. Run once:
   ```bash
   if [[ -n "${CLAUDE_PLUGIN_ROOT:-}" && -x "$CLAUDE_PLUGIN_ROOT/bin/wip-plumbing" ]]; then
     WIP="$CLAUDE_PLUGIN_ROOT/bin/wip-plumbing"
   elif [[ -n "${WIP_PLUMBING_BIN:-}" && -x "${WIP_PLUMBING_BIN}" ]]; then
     WIP="$WIP_PLUMBING_BIN"
   elif WIP="$(ls -d "$HOME"/.claude/plugins/cache/*/wip/*/bin/wip-plumbing 2>/dev/null | sort | tail -1)" && [[ -n "$WIP" && -x "$WIP" ]]; then
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

2. **Parse `$ARGUMENTS`.** Extract the positional `<file>...` inputs plus
   optional `--target <slug>`, `--lead-as brief|amendment`, and `-o <manifest>`.
   - **Two or more** input files are required. If only one is given, stop and
     tell the user to run `/wip:intake <file>` instead (one file is not a
     bundle).
   - Verify every input is a readable file (Read it / `[[ -r ]]`); if any is
     unreadable, stop and report which.

3. **Choose the manifest location + child paths.** `children[].path` is
   resolved by the explode **relative to the manifest's directory**, so:
   - Default: write the manifest as `bundle.md` in the inputs' **longest common
     parent directory**; each child path is that input relative to the dir.
   - `-o <path>`: write there; child paths are relative to `dirname <path>`.
   - Inputs with no shared ancestor below `/`: use absolute child paths and say
     so to the user.

4. **Fetch the assembly rules.** Run, in order:
   - `"$WIP" template show intake/preamble`
   - `"$WIP" template show bundle/assemble`

   Concatenate (preamble + blank line + assembly rules) — that's the contract
   you must follow. Do NOT skip this step or paraphrase the rules from memory;
   read them fresh each invocation. The byte bundle is shared with the CLI
   porcelain so behavior stays consistent across both frontends.

5. **Assemble.** Read every input file (Read tool). Build one `bundle` lead
   manifest per the assembly rules: front-matter `wip-kind: bundle`,
   `lead-as` (the `--lead-as` hint or your inference), a `children:` entry per
   input using the **exact** relative path from step 3 (with `kind` / `lane` /
   `depends-on` / a directive hint only where the content justifies it),
   `cross-cuts` when the inputs imply concurrent tracks, and a lead body
   satisfying its `lead-as` kind. Do NOT hand-author `### Lane` subheadings or
   a Cross-cuts section — the explode renders those. Write the manifest to the
   chosen path (use `mktemp` only if `-o`/default is not yet decided). If a
   target / lane / dependency is unclear and you cannot justify it from the
   inputs, ask the user ONE short question inline in this chat and wait for the
   answer before finishing — never invent a path or fact.

6. **Validate.** Run `"$WIP" intake validate --kind bundle <manifest>`. On
   `missing[]`, patch the manifest and re-validate. Cap at **2** reshape
   attempts (CLI parity). After 2 failed validates, stop and report the
   validation envelope.

7. **Explode (inline).** Run the same fan-out `/wip:intake` drives for a
   bundle, against the manifest you just wrote:
   - **Apply the lead.** Strip the bundle-only keys; the lead is the
     `lead-as` kind. For `amendment`, `"$WIP" intake apply --kind amendment
     --target <slug> <lead>` (with the empty `### Lane <name>` per distinct
     `children[].lane` and a `## Cross-cuts (from bundle)` section appended);
     for `brief`, apply via `init` and use the slug it creates for the children.
   - **Apply each child**, in `depends-on` order: seed an amendment whose
     directive is `insert-step-in-lane: <lane>` (when `lane` is set) or the
     explicit directive hint, then shape + `"$WIP" intake apply` it. A child
     with neither a lane nor a directive is folded into the lead body.
   - Per-child apply is **independent and non-atomic**; re-apply is safe via
     the amendment hash markers. A child may **never** be `kind: bundle`
     (nested bundles are refused).

8. **Report.** Echo the aggregate ledger (manifest path + lead result + each
   child's result) in a code block, plus a one-line prose summary like
   "assembled bundle.md (lead-as amendment, target tc); applied lead + 2 lane
   children". On any apply exit-4, report the envelope verbatim.

## Notes

- The assembly rules come from `templates/prompts/bundle/assemble.md`; the CLI
  porcelain (`wip bundle`) and this command read the SAME file via
  `wip-plumbing template show bundle/assemble`. If tempted to edit assembly
  rules inline here, edit the source file instead.
- Clarifying questions happen inline in chat. The CLI's `---ASK---` fence
  protocol is for non-interactive loops; you do NOT emit `---ASK---` blocks.
- This command body is the contract; do not improvise off-script.
