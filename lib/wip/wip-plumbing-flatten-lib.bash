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

# --- Chunk 2: roles/backend resolution + the inliner ------------------------

# _wip_flatten_roles_dir — echo the roles/ directory, resolved exactly like
# `orchestrate backend` (D3), with a leading env override for test isolation:
#   1. $WIP_ROLES_DIR (must contain backends/)            [test/install seam]
#   2. $root/roles      when $root/roles/backends exists  [dev/vendored layout]
#   3. $CLAUDE_PLUGIN_ROOT/roles when it has backends/    [shared plugin install]
#   4. else: stderr diagnostic + non-zero (no-roles-dir)
_wip_flatten_roles_dir() {
  if [[ -n "${WIP_ROLES_DIR:-}" ]]; then
    if [[ -d "$WIP_ROLES_DIR/backends" ]]; then
      printf '%s' "$WIP_ROLES_DIR"
      return 0
    fi
    _wip_flatten_err "no-roles-dir: \$WIP_ROLES_DIR is set but has no backends/: $WIP_ROLES_DIR"
    return 4
  fi
  local root
  if root="$(wip_find_root 2>/dev/null)" && [[ -d "$root/roles/backends" ]]; then
    printf '%s' "$root/roles"
    return 0
  fi
  if [[ -n "${CLAUDE_PLUGIN_ROOT:-}" && -d "$CLAUDE_PLUGIN_ROOT/roles/backends" ]]; then
    printf '%s' "$CLAUDE_PLUGIN_ROOT/roles"
    return 0
  fi
  _wip_flatten_err "no-roles-dir: roles/backends/ not found (looked in \$WIP_ROLES_DIR, \$root/roles, \$CLAUDE_PLUGIN_ROOT/roles)"
  return 4
}

# _wip_flatten_trim — read stdin, strip leading and trailing blank lines, and
# emit the remaining body with NO trailing newline. The deterministic join
# (D6) re-supplies exactly one blank line between sections and a single
# trailing newline for the whole document, so each section is normalized here
# to its bare content regardless of incidental surrounding whitespace.
_wip_flatten_trim() {
  awk '
    { lines[NR] = $0 }
    END {
      start = 1
      while (start <= NR && lines[start] ~ /^[[:space:]]*$/) start++
      end = NR
      while (end >= start && lines[end] ~ /^[[:space:]]*$/) end--
      for (i = start; i <= end; i++) {
        printf "%s", lines[i]
        if (i < end) printf "\n"
      }
    }
  '
}

# _wip_flatten_join <section>... — print sections in order separated by exactly
# one blank line, with a single trailing newline (D6). Each argument is one
# already-trimmed section body.
_wip_flatten_join() {
  local first=1 s
  for s in "$@"; do
    if ((first)); then
      printf '%s' "$s"
      first=0
    else
      printf '\n\n%s' "$s"
    fi
  done
  printf '\n'
}

# wip_flatten_render <role> <backend> — the public entry point (D2). Resolve
# the source dirs (D3), validate inputs, run the D5 drift guard, then inline
# the four canonical bodies in the fixed emit order (D5) with the
# backends/active.md -> backends/<backend>.md seam collapse, joined
# deterministically (D6). Prints the rendered agent file on stdout; emits a
# stderr diagnostic and returns non-zero on any error.
wip_flatten_render() {
  local role="${1:-}" backend="${2:-}"
  if [[ -z "$role" || -z "$backend" ]]; then
    _wip_flatten_err "usage: wip_flatten_render <role> <backend>"
    return 2
  fi
  case "$role" in
    orchestrator | coordinator | researcher | builder) ;;
    *)
      _wip_flatten_err "unknown role: $role (expected orchestrator|coordinator|researcher|builder)"
      return 2
      ;;
  esac
  if [[ ! "$backend" =~ ^[a-z][a-z0-9-]*$ ]]; then
    _wip_flatten_err "invalid backend name: $backend (must match ^[a-z][a-z0-9-]*\$)"
    return 2
  fi

  local roles_dir templates_dir
  roles_dir="$(_wip_flatten_roles_dir)" || return $?
  templates_dir="$(wip_templates_dir)" || true
  if [[ -z "$templates_dir" || ! -d "$templates_dir" ]]; then
    _wip_flatten_err "no-templates-dir: could not resolve templates/ (set \$WIP_TEMPLATES_DIR or \$WIP_LIB)"
    return 4
  fi

  local template="$templates_dir/setup/agents/agents/$role.md"
  local shared="$roles_dir/shared.md"
  local rolebody="$roles_dir/$role.md"
  local tier="$roles_dir/tier-policy.md"
  local backendfile="$roles_dir/backends/$backend.md"

  local f
  for f in "$template" "$shared" "$rolebody" "$tier" "$backendfile"; do
    if [[ ! -f "$f" ]]; then
      _wip_flatten_err "missing source: $f"
      return 4
    fi
  done

  # D5 drift guard: the template's @-include set must equal the canonical four.
  wip_flatten_parse_template "$template" "$role" || return $?

  # Fixed emit order (D5): framing + shared.md + <role>.md + tier-policy.md +
  # backends/<backend>.md, with the verbatim frontmatter ahead of the framing.
  # The active.md seam collapses by reading backends/<backend>.md directly.
  local -a sections=()
  sections+=("$(_wip_flatten_frontmatter "$template" | _wip_flatten_trim)")
  sections+=("$(_wip_flatten_framing "$template" | _wip_flatten_trim)")
  sections+=("$(_wip_flatten_trim <"$shared")")
  sections+=("$(_wip_flatten_trim <"$rolebody")")
  sections+=("$(_wip_flatten_trim <"$tier")")
  sections+=("$(_wip_flatten_trim <"$backendfile")")

  _wip_flatten_join "${sections[@]}"
}
