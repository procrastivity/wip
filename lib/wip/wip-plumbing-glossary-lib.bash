# wip-plumbing-glossary-lib.bash — pure helpers for the `glossary` subcommand.
# Sourced by lib/wip/wip-plumbing-subcommands/glossary.bash. No side effects
# beyond stdout/stderr; the subcommand owns file writes.
#
# Inclusion-rule table (single source of truth — adding a new partial is a
# one-row addition here). Each row is `<partial>\t<predicate-name>\t<jq-expr>`,
# in declaration order (which is also the emit order).
#
# `solo.md` / `task.md` are gated on `features.orchestration.backend` per
# ADR-0007 / ADR-0013 — exactly one fires for the active binding;
# `features.solo.enabled` is the Solo backend's *availability* flag, distinct
# from the *active binding* the partial follows.
# shellcheck shell=bash

wip_glossary_rules() {
  # partial<TAB>predicate-name<TAB>jq-expression
  printf 'core.md\talways\ttrue\n'
  printf 'orchestration.md\tfeatures.orchestration.enabled\t.features.orchestration.enabled == true\n'
  printf 'solo.md\tfeatures.orchestration.backend\t(.features.orchestration.enabled == true) and (.features.orchestration.backend == "solo")\n'
  printf 'task.md\tfeatures.orchestration.backend\t(.features.orchestration.enabled == true) and (.features.orchestration.backend == "task")\n'
  printf 'lds.md\tfeatures.lds.enabled\t.features.lds.enabled == true\n'
  printf 'diataxis.md\tfeatures.diataxis.enabled\t.features.diataxis.enabled == true\n'
}

# wip_glossary_resolve <manifest-json>
#
# Emit a JSON array of inclusion records — one per partial whose predicate is
# true. Records:
#   { name, source_path, predicate, body_present }
# body_present=false means the predicate fired but the partial file isn't on
# disk yet (the lds/diataxis future-row case). The caller (assemble or check)
# decides whether to render the body or surface a ledger entry.
wip_glossary_resolve() {
  local mj="$1"
  local dir
  dir="$(wip_templates_dir)"
  [[ -n "$dir" && -d "$dir" ]] || return 1

  local partial pred expr present out="[]" obj path included
  while IFS=$'\t' read -r partial pred expr; do
    [[ -n "$partial" ]] || continue
    if [[ "$expr" == "true" ]]; then
      included="true"
    else
      included="$(printf '%s' "$mj" | jq -r "$expr // false" 2>/dev/null || printf 'false')"
    fi
    [[ "$included" == "true" ]] || continue
    path="$dir/glossary/$partial"
    if [[ -f "$path" ]]; then present="true"; else present="false"; fi
    obj="$(jq -nc \
      --arg name "$partial" \
      --arg source_path "$path" \
      --arg predicate "$pred" \
      --argjson body_present "$present" '
      {name:$name, source_path:$source_path, predicate:$predicate, body_present:$body_present}')"
    out="$(jq -nc --argjson a "$out" --argjson o "$obj" '$a + [$o]')"
  done < <(wip_glossary_rules)
  printf '%s' "$out"
}

# wip_glossary_strip_header <file>
#
# Print the file body with the leading HTML comment block removed. Strip rule
# is positional: skip any blank lines at the top, then skip the first
# contiguous `<!-- … -->` block (single comment, may span multiple lines),
# then skip any blank lines that follow, then emit the rest verbatim.
#
# Only the first block is stripped; later comments survive. BSD/GNU awk safe.
wip_glossary_strip_header() {
  local file="$1"
  awk '
    BEGIN { phase = "lead" }
    phase == "lead" {
      if ($0 ~ /^[[:space:]]*$/) { next }
      if ($0 ~ /^[[:space:]]*<!--/) {
        phase = "comment"
        if ($0 ~ /-->[[:space:]]*$/) { phase = "trail" }
        next
      }
      phase = "body"
    }
    phase == "comment" {
      if ($0 ~ /-->/) { phase = "trail" }
      next
    }
    phase == "trail" {
      if ($0 ~ /^[[:space:]]*$/) { next }
      phase = "body"
    }
    phase == "body" { print }
  ' "$file"
}

# wip_glossary_render <root> <manifest-json>
#
# Print the full assembled glossary markdown to stdout. Composes:
#   - H1 + GENERATED header (with Source / Driven by / Regenerate / Verify lines)
#   - one-paragraph intro blockquote
#   - per-partial divider + stripped partial body
#
# Stderr-side caveat: partials whose predicate is true but whose body is not on
# disk yet (lds/diataxis future-rows) are silently skipped here; the subcommand
# layer carries them in the JSON ledger.
wip_glossary_render() {
  local root="$1" mj="$2"
  local resolved
  resolved="$(wip_glossary_resolve "$mj")" || return 1

  local sources driven names
  # Source line: paths-with-brace-expansion. Single-partial form collapses to
  # the bare path; multi-partial form to {a,b,c} after the common prefix.
  names="$(printf '%s' "$resolved" |
    jq -r '[.[] | select(.body_present) | .name | rtrimstr(".md")] | join(",")')"
  if [[ "$names" == *","* ]]; then
    sources="templates/glossary/{$names}.md"
  else
    sources="templates/glossary/$names.md"
  fi
  # List only emitted partials' predicates — those are what would actually
  # change the file if flipped. Future-rows whose body isn't shipped yet
  # (predicate true but partial-not-on-disk) are NOT listed; they don't
  # change the output until the partial lands.
  driven="$(printf '%s' "$resolved" |
    jq -r '[.[] | select(.body_present and .predicate != "always") | .predicate] | join(", ")')"
  [[ -n "$driven" ]] || driven="(only core; no feature predicates active)"

  cat <<HEADER
# wip — Effective Glossary (this project)

<!-- GENERATED by \`wip-plumbing glossary assemble\`. Do not hand-edit.
     Source:     $sources
     Driven by:  $driven
     Regenerate: wip-plumbing glossary assemble > .wip/GLOSSARY.md
     Verify:     wip-plumbing glossary check -->

> **This is this project's effective glossary**, assembled from \`core\` plus
> one partial per feature enabled in \`.wip.yaml\`. It is **not** what every
> consumer inherits: each project gets \`core\` plus only the partials for the
> features *they* enable. The canonical, editable source is
> [\`templates/glossary/\`](../templates/glossary/); the *rationale* behind the
> terms lives in [\`engineering/decisions/\`](../engineering/decisions/) (ADRs).
HEADER

  local name source_path predicate body_present rel reason
  while IFS= read -r row; do
    [[ -n "$row" ]] || continue
    name="$(jq -r '.name' <<<"$row")"
    source_path="$(jq -r '.source_path' <<<"$row")"
    predicate="$(jq -r '.predicate' <<<"$row")"
    body_present="$(jq -r '.body_present' <<<"$row")"
    [[ "$body_present" == "true" ]] || continue
    rel="${source_path#"$root/"}"
    case "$rel" in
      /*) rel="$source_path" ;;
    esac
    if [[ "$predicate" == "always" ]]; then
      reason="universal"
    else
      reason="$predicate"
    fi
    printf '\n<!-- partial: %s  source: %s  reason: %s -->\n\n' "$name" "$rel" "$reason"
    wip_glossary_strip_header "$source_path"
  done < <(printf '%s' "$resolved" | jq -c '.[]')
}

# wip_glossary_target_path <root> <manifest-json>
#
# Print the on-disk path the assembled glossary should live at. Looks for
# `.wip/GLOSSARY.md` in the manifest's `gitignore.always_commit[]` (cheap
# membership check); falls back to literal `.wip/GLOSSARY.md` if absent.
# Always relative to the repo root.
wip_glossary_target_path() {
  local _root="$1" mj="$2" found
  found="$(printf '%s' "$mj" | jq -r '
    (.gitignore.always_commit // []) | map(select(. == ".wip/GLOSSARY.md")) | .[0] // ""')"
  if [[ -n "$found" ]]; then
    printf '%s' "$found"
  else
    printf '.wip/GLOSSARY.md'
  fi
}
