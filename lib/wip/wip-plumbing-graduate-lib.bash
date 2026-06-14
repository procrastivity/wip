# wip-plumbing-graduate-lib.bash — promote a single wip-internal planning
# artifact to its canonical LDS slot. Sourced by
# lib/wip/wip-plumbing-subcommands/graduate.bash.
#
# Per step-15: graduate is the single-artifact LDS seam. It reads a
# `graduate-to:` front-matter directive (or a `--to` CLI override),
# strips that one key, and writes the body verbatim to
# `<eng_docs_dir>/<target>` via the three-way idempotency helper from
# wip-plumbing-setup-lib.bash. Auto-numbering is supported for ADRs only
# via the `decisions/auto-<slug>.md` shorthand.
#
# All judgement (what kind, what slot) is on the artifact author. The
# verb is pure deterministic plumbing.
# shellcheck shell=bash

# Canonical LDS layer directories (the 7 layers + maintenance + appendices).
# Anything outside this set exits 4 unknown-layer.
WIP_GRADUATE_LAYERS="decisions product architecture specs reference features implementation maintenance appendices"

# wip_graduate_layer_known <layer-name> — return 0 if the given top-level
# directory is a recognized LDS layer; non-zero otherwise.
wip_graduate_layer_known() {
  local layer="$1" x
  for x in $WIP_GRADUATE_LAYERS; do
    [[ "$x" == "$layer" ]] && return 0
  done
  return 1
}

# wip_graduate_target_from_artifact <artifact-path> — read the `graduate-to:`
# front-matter directive. Empty stdout when absent.
wip_graduate_target_from_artifact() {
  local file="$1" fm
  fm="$(wip_intake_read_front_matter "$file")"
  printf '%s' "$fm" | jq -r '."graduate-to" // empty'
}

# wip_graduate_next_adr_number <decisions-dir> — emit the next 4-digit ADR
# number for the directory (max existing prefix + 1, or 0001 if empty).
# Recognizes files matching `^[0-9]{4}-.*\.md$`. Always 4-padded.
wip_graduate_next_adr_number() {
  local dir="$1" max=0 n
  if [[ -d "$dir" ]]; then
    while IFS= read -r f; do
      n="${f##*/}"
      n="${n%%-*}"
      [[ "$n" =~ ^[0-9]{4}$ ]] || continue
      n=$((10#$n))
      ((n > max)) && max=$n
    done < <(find "$dir" -maxdepth 1 -type f -name '[0-9][0-9][0-9][0-9]-*.md' 2>/dev/null | LC_ALL=C sort)
  fi
  printf '%04d' $((max + 1))
}

# _wip_graduate_find_adr_by_slug <decisions-dir> <slug>
#
# If a file `<NNNN>-<slug>.md` already exists in <decisions-dir>, echo its
# basename. Empty stdout when not found. Used by the auto- shorthand to
# preserve idempotency: re-running graduate against the same artifact
# should re-resolve to the same target, not pick the next free number.
_wip_graduate_find_adr_by_slug() {
  local dir="$1" slug="$2" base
  [[ -d "$dir" && -n "$slug" ]] || return 0
  while IFS= read -r f; do
    base="${f##*/}"
    # Match exactly `<4 digits>-<slug>.md`
    if [[ "$base" =~ ^[0-9]{4}-(.+)\.md$ ]]; then
      if [[ "${BASH_REMATCH[1]}" == "$slug" ]]; then
        printf '%s' "$base"
        return 0
      fi
    fi
  done < <(find "$dir" -maxdepth 1 -type f -name '[0-9][0-9][0-9][0-9]-*.md' 2>/dev/null | LC_ALL=C sort)
}

# wip_graduate_resolve_target <eng-docs-dir> <directive> <decisions-dir>
#
# Resolve the final repo-root-relative target path. <directive> is the
# value of `graduate-to:` (or `--to`). <eng-docs-dir> is the LDS root.
# <decisions-dir> is the absolute path to <root>/<eng-docs-dir>/decisions
# (used for auto-NNNN scanning; pass even when not auto — helper resolves
# it once at the caller).
#
# Stdout: repo-root-relative target path (e.g.
# "engineering/decisions/0003-foo.md").
#
# Exit codes:
#   0 — resolved
#   4 — bad-directive    (empty, absolute, or contains "..")
#   4 — unknown-layer    (first path segment not in WIP_GRADUATE_LAYERS)
#   4 — bad-auto-slot    (auto- shorthand used outside decisions/)
#
# Error messages go to stderr (prose); caller turns them into envelopes.
wip_graduate_resolve_target() {
  local eng="$1" directive="$2" decisions_abs="$3"
  if [[ -z "$directive" ]]; then
    printf 'wip-plumbing: graduate: no graduate-to directive (front-matter or --to)\n' >&2
    return 4
  fi
  case "$directive" in
    /*)
      printf 'wip-plumbing: graduate: target must be eng-docs-relative, not absolute: %s\n' "$directive" >&2
      return 4
      ;;
  esac
  if [[ "$directive" == *".."* ]]; then
    printf 'wip-plumbing: graduate: target contains "..": %s\n' "$directive" >&2
    return 4
  fi
  local layer="${directive%%/*}"
  local rest="${directive#*/}"
  if [[ "$layer" == "$directive" ]]; then
    printf 'wip-plumbing: graduate: target must be <layer>/<file>: %s\n' "$directive" >&2
    return 4
  fi
  if ! wip_graduate_layer_known "$layer"; then
    printf 'wip-plumbing: graduate: unknown layer: %s (allowed: %s)\n' "$layer" "$WIP_GRADUATE_LAYERS" >&2
    return 4
  fi
  # auto-<slug>.md shorthand: decisions/ only.
  case "$rest" in
    auto-*.md)
      if [[ "$layer" != "decisions" ]]; then
        printf 'wip-plumbing: graduate: auto-numbering is decisions-only: %s\n' "$directive" >&2
        return 4
      fi
      local slug="${rest#auto-}"
      slug="${slug%.md}"
      if [[ -z "$slug" ]]; then
        printf 'wip-plumbing: graduate: auto- shorthand needs a slug: %s\n' "$directive" >&2
        return 4
      fi
      # Find-or-create: if <NNNN>-<slug>.md already exists, reuse that
      # number — preserves idempotency on re-run. Otherwise allocate max+1.
      local existing
      existing="$(_wip_graduate_find_adr_by_slug "$decisions_abs" "$slug")"
      if [[ -n "$existing" ]]; then
        printf '%s/decisions/%s' "$eng" "$existing"
        return 0
      fi
      local n
      n="$(wip_graduate_next_adr_number "$decisions_abs")"
      printf '%s/decisions/%s-%s.md' "$eng" "$n" "$slug"
      return 0
      ;;
  esac
  printf '%s/%s' "$eng" "$directive"
  return 0
}

# wip_graduate_render_body <artifact-path> — emit the artifact body with the
# `graduate-to:` front-matter key removed. Other front-matter keys are
# preserved. When the resulting front-matter would be empty, the entire
# `--- ... ---` block is omitted (a body-only file).
#
# Stdout: rendered file bytes (the exact bytes the target should contain
# *before* idempotency comparison).
wip_graduate_render_body() {
  local file="$1"
  awk '
    BEGIN { state = "start"; fm = ""; body_started = 0 }
    state == "start" {
      if ($0 ~ /^---[[:space:]]*$/) { state = "fm"; next }
      state = "body"
    }
    state == "fm" {
      if ($0 ~ /^---[[:space:]]*$/) { state = "after_fm"; next }
      fm = fm $0 "\n"
      next
    }
    state == "after_fm" || state == "body" {
      if (!body_started) { body_started = 1 }
      body = body $0 "\n"
      next
    }
    END {
      if (state == "fm") {
        # Unterminated front-matter; emit nothing — caller treats as empty body.
        printf "%s", fm
        exit
      }
      # Filter graduate-to: out of the front-matter
      n = split(fm, lines, "\n")
      kept = ""
      for (i = 1; i <= n; i++) {
        if (lines[i] == "") continue
        if (lines[i] ~ /^[[:space:]]*graduate-to[[:space:]]*:/) continue
        kept = kept lines[i] "\n"
      }
      if (kept != "") {
        printf "---\n%s---\n", kept
      }
      printf "%s", body
    }
  ' "$file"
}
