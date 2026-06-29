# wip-plumbing-flatten-lib.bash — the pure flatten renderer.
#
# Given (role, backend), resolve an agent template's four single-level
# `@`-includes and emit one fully-formed, self-contained agent file as a
# string on stdout. Pure transform: stdout only, side-effect-free, no file
# writes, no timestamps/host/path interpolation in the output. step-03 wires
# this into `setup agents`; this lib MUST NOT be the install target itself.
#
# Public entry: `wip_flatten_render <role> <backend>` -> rendered agent file on
# stdout; non-zero exit + a stderr diagnostic on error.
#
# External helpers consumed from wip-plumbing-lib.bash (sourced by the same
# bin / test harness): `wip_templates_dir`, `wip_find_root`. The renderer reads
# the canonical `roles/` + agent templates only — never a previously-rendered
# artifact — so re-render is deterministic and idempotent (D6).
#
# Contracts (workplan step-02 D1–D7):
#   - D2  signature `wip_flatten_render <role> <backend>`; role in
#         {orchestrator,coordinator,researcher,builder}; backend matches
#         ^[a-z][a-z0-9-]*$ and has a backends/<backend>.md.
#   - D3  source dirs resolve like `orchestrate backend` (env-overridable).
#   - D4  frontmatter + framing read verbatim from the agent template.
#   - D5  emit order framing + shared.md + <role>.md + tier-policy.md +
#         backends/<backend>.md; the template's `@`-include set is validated
#         against the canonical four and FAILS LOUD on drift; the
#         backends/active.md slot is the backend seam (<backend>.md substitutes).
#   - D6  deterministic join: one blank line between sections, each body
#         verbatim with a single normalized trailing newline.
#   - D7  O3 = leave-inert; `wip_flatten_neutralize_links` is the identity
#         transform, wired in as a one-function seam.
# shellcheck shell=bash

# _wip_flatten_err <message...> — diagnostic to stderr. Kept separate from
# wip_die: the renderer's stdout is reserved for the rendered file, so errors
# never touch stdout (no JSON envelope here).
_wip_flatten_err() { printf 'wip-flatten: %s\n' "$*" >&2; }

# --- Chunk 1: agent-template parser + D5 drift guard ------------------------

# _wip_flatten_frontmatter <template> — print the leading `---...---`
# frontmatter block verbatim (both fences inclusive). Empty if the file does
# not open with a `---` fence.
_wip_flatten_frontmatter() {
  awk '
    NR == 1 && $0 == "---" { print; infm = 1; next }
    infm && $0 == "---"    { print; exit }
    infm                   { print }
  ' "$1"
}

# _wip_flatten_framing <template> — print everything between the frontmatter
# fence and the first `@`-include bullet, verbatim (the `# <Role> (wip)`
# heading + the "Act as ... do not paraphrase from memory." line). Surrounding
# blank lines are normalized away at join time (D6).
_wip_flatten_framing() {
  awk '
    NR == 1 && $0 == "---" { fm = 1; next }
    fm == 1 && $0 == "---"  { fm = 2; next }
    fm == 2 {
      if ($0 ~ /^- @/) exit
      print
    }
  ' "$1"
}

# wip_flatten_parse_template <template> <role> — parse the template's ordered
# `@`-include list and validate it (the D5 drift guard) against the canonical
# four for <role>:
#   ../roles/shared.md, ../roles/<role>.md, ../roles/tier-policy.md,
#   ../roles/backends/active.md
# On any mismatch (missing, extra, or renamed include) it FAILS LOUD with a
# stderr diagnostic and returns non-zero rather than silently mis-rendering.
wip_flatten_parse_template() {
  local template="$1" role="$2"
  local expected actual
  expected="$(printf '%s\n' \
    "../roles/shared.md" \
    "../roles/$role.md" \
    "../roles/tier-policy.md" \
    "../roles/backends/active.md" | LC_ALL=C sort)"
  actual="$(grep -E '^- @' "$template" 2>/dev/null |
    sed -E 's/^- @//; s/[[:space:]]+$//' | LC_ALL=C sort)"
  if [[ "$actual" != "$expected" ]]; then
    _wip_flatten_err "drift: $template @-include set != canonical four for role '$role'"
    _wip_flatten_err "  expected: $(printf '%s' "$expected" | tr '\n' ' ')"
    _wip_flatten_err "  actual:   $(printf '%s' "${actual:-<none>}" | tr '\n' ' ')"
    return 5
  fi
  return 0
}
