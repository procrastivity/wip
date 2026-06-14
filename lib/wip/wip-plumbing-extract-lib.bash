# wip-plumbing-extract-lib.bash — deterministic LDS extract phase (v1).
# Sourced by lib/wip/wip-plumbing-subcommands/extract.bash.
#
# v1 supports the `verbatim` and `content` extraction modes against the
# simple-path / single-file-with-range source spec. transform/summarize
# modes and multi-file sources are recognized but routed to the
# `unsupported` ledger — they do not fail the run. SHA-256 hash
# verification of source files is parsed but not computed in v1 (the
# manifest's hash fields are informational; the ledger records
# `hash_verification: "skipped-v1"`).
#
# Manifest validation is required-fields only: schema_version 1.x.x,
# metadata.status == "approved", entries non-empty, entry ids unique,
# per-entry {id, target, mode} required (+ source when mode != content).
# Per the LDS schema doc-comment, no JSON Schema validator is used; yq
# and jq cover everything we need.
# shellcheck shell=bash

# wip_extract_lds_root <manifest-json> — echo the eng-docs root path
# from the .wip.yaml manifest JSON. Mirrors the resolution rule used by
# `_wip_feature_records` so detect/doctor and extract see the same root.
# Falls back to "engineering" when unset.
wip_extract_lds_root() {
  printf '%s' "$1" | jq -r '
    (.features.lds.root
      // (.features.lds.installs[0].root // "engineering"))
  '
}

# wip_extract_validate_manifest <manifest-json>
#
# Validate the manifest's metadata shape and the entries array. Emits
# nothing on success; prints one diagnostic line to stderr and returns
# a non-zero status on failure. Status codes intentionally mirror what
# the dispatcher emits as `error.kind`:
#   2 — incompatible-schema    (schema_version not 1.x.x)
#   3 — manifest-not-approved  (metadata.status != "approved")
#   4 — manifest-empty         (entries is missing/null/empty)
#   5 — duplicate-entry-id     (two entries share an id)
#
# (These codes are local to the lib; the dispatcher always uses exit 4
# for the user-facing surface — they're just a distinguisher for the
# kind string.)
wip_extract_validate_manifest() {
  local mj="$1" schema status count
  schema="$(printf '%s' "$mj" | jq -r '.metadata.schema_version // ""')"
  if [[ -z "$schema" ]]; then
    printf 'wip-plumbing: extract: manifest missing metadata.schema_version\n' >&2
    return 2
  fi
  if [[ ! "$schema" =~ ^1\.[0-9]+\.[0-9]+$ ]]; then
    printf 'wip-plumbing: extract: incompatible schema_version: %s (need 1.x.x)\n' "$schema" >&2
    return 2
  fi
  status="$(printf '%s' "$mj" | jq -r '.metadata.status // ""')"
  if [[ "$status" != "approved" ]]; then
    printf 'wip-plumbing: extract: manifest status is %q; need "approved"\n' "$status" >&2
    return 3
  fi
  count="$(printf '%s' "$mj" | jq -r '(.entries // []) | length')"
  if [[ "$count" -lt 1 ]]; then
    printf 'wip-plumbing: extract: manifest entries is empty\n' >&2
    return 4
  fi
  local dups
  dups="$(printf '%s' "$mj" | jq -r '
    [.entries[]?.id] | group_by(.) | map(select(length > 1)[0]) | .[]
  ')"
  if [[ -n "$dups" ]]; then
    printf 'wip-plumbing: extract: duplicate entry id(s): %s\n' "$dups" >&2
    return 5
  fi
  return 0
}

# wip_extract_entry_required_ok <entry-json>
#
# Returns 0 if the entry has the required fields for v1 dispatch:
# id (non-empty), target (non-empty), mode (one of verbatim|content|
# transform|summarize). For non-content modes a source spec is also
# required. Returns 1 otherwise. Caller turns failures into ledger
# `bad-entry-shape` rows.
wip_extract_entry_required_ok() {
  local ej="$1"
  printf '%s' "$ej" | jq -e '
    (.id // "") != "" and
    (.target // "") != "" and
    ((.mode // "verbatim") | IN("verbatim", "content", "transform", "summarize")) and
    (if (.mode // "verbatim") == "content" then true else (.source != null) end)
  ' >/dev/null
}

# wip_extract_source_kind <entry-json>
#
# Echo one of:
#   simple-path             — source is a string
#   single-file             — source is {file, ...}
#   multi-file              — source is {files: [...]}
#   none                    — source is absent (content mode)
#   unknown                 — unrecognized shape
wip_extract_source_kind() {
  local ej="$1"
  printf '%s' "$ej" | jq -r '
    .source as $s |
    if $s == null then "none"
    elif ($s | type) == "string" then "simple-path"
    elif ($s | type) == "object" and ($s.files? != null) then "multi-file"
    elif ($s | type) == "object" and ($s.file? != null) then "single-file"
    else "unknown"
    end
  '
}

# wip_extract_attribution_lines <entry-json>
#
# Emit the two-line attribution comment block per LDS §6.3. Lands at
# the top of every extracted target so an LDS audit/sync workflow can
# trace provenance. For content mode (no source) emits the
# "Generated content - no source file" form.
wip_extract_attribution_lines() {
  local ej="$1" mode kind id file start end
  mode="$(printf '%s' "$ej" | jq -r '.mode // "verbatim"')"
  id="$(printf '%s' "$ej" | jq -r '.id // ""')"
  kind="$(wip_extract_source_kind "$ej")"
  if [[ "$mode" == "content" || "$kind" == "none" ]]; then
    printf '<!-- Generated content - no source file -->\n'
    printf '<!-- Extraction ID: %s -->\n' "$id"
    return 0
  fi
  case "$kind" in
    simple-path)
      file="$(printf '%s' "$ej" | jq -r '.source')"
      printf '<!-- Migrated from %s -->\n' "$file"
      printf '<!-- Extraction ID: %s -->\n' "$id"
      ;;
    single-file)
      file="$(printf '%s' "$ej" | jq -r '.source.file')"
      start="$(printf '%s' "$ej" | jq -r '.source.start_line // ""')"
      end="$(printf '%s' "$ej" | jq -r '.source.end_line // ""')"
      if [[ -n "$start" && -n "$end" ]]; then
        printf '<!-- Migrated from %s:%s-%s -->\n' "$file" "$start" "$end"
      elif [[ -n "$start" ]]; then
        printf '<!-- Migrated from %s:%s -->\n' "$file" "$start"
      else
        printf '<!-- Migrated from %s -->\n' "$file"
      fi
      printf '<!-- Extraction ID: %s -->\n' "$id"
      ;;
  esac
}

# wip_extract_render_verbatim <entry-json> <repo-root>
#
# Read the source file (simple-path or single-file with optional
# start/end line range), prepend attribution, emit the bytes to stdout.
# 1-indexed inclusive line ranges per LDS §3.1. Missing end_line = EOF.
# Missing start_line = 1.
#
# Returns 0 on success; 2 + stderr diagnostic on a missing source file
# or unreadable range.
wip_extract_render_verbatim() {
  local ej="$1" root="$2" kind file abs start end
  kind="$(wip_extract_source_kind "$ej")"
  case "$kind" in
    simple-path) file="$(printf '%s' "$ej" | jq -r '.source')" ;;
    single-file) file="$(printf '%s' "$ej" | jq -r '.source.file')" ;;
    *)
      printf 'wip-plumbing: extract: render_verbatim called with kind=%s\n' "$kind" >&2
      return 2
      ;;
  esac
  abs="$root/$file"
  if [[ ! -f "$abs" ]]; then
    printf 'wip-plumbing: extract: source file missing: %s\n' "$file" >&2
    return 2
  fi
  start="$(printf '%s' "$ej" | jq -r '.source.start_line // empty' 2>/dev/null)"
  end="$(printf '%s' "$ej" | jq -r '.source.end_line // empty' 2>/dev/null)"
  wip_extract_attribution_lines "$ej"
  printf '\n'
  if [[ -z "$start" && -z "$end" ]]; then
    cat -- "$abs"
  else
    [[ -z "$start" ]] && start=1
    if [[ -z "$end" ]]; then
      awk -v s="$start" 'NR >= s' "$abs"
    else
      awk -v s="$start" -v e="$end" 'NR >= s && NR <= e' "$abs"
    fi
  fi
}

# wip_extract_render_content <entry-json>
#
# Emit attribution + inline_content. Returns 2 if inline_content is
# absent (caller treats as bad-entry-shape).
wip_extract_render_content() {
  local ej="$1" content
  content="$(printf '%s' "$ej" | jq -r '.inline_content // empty')"
  if [[ -z "$content" ]]; then
    printf 'wip-plumbing: extract: content mode entry missing inline_content\n' >&2
    return 2
  fi
  wip_extract_attribution_lines "$ej"
  printf '\n'
  printf '%s' "$content"
  # inline_content from YAML block scalar usually carries its own trailing newline;
  # if it doesn't, add one for tidy output.
  [[ "$content" == *$'\n' ]] || printf '\n'
}

# wip_extract_classify_entry <entry-json>
#
# Decide how the dispatcher should handle this entry. Echo one of:
#   ok-verbatim
#   ok-content
#   bad-shape:<message>
#   unsupported-mode:<mode>
#   unsupported-source:<source-kind>
#   unsupported-template
#
# The dispatcher routes ok-* to the renderer, bad-shape to the error
# ledger, unsupported-* to the skip ledger.
wip_extract_classify_entry() {
  local ej="$1" mode src tmpl
  if ! wip_extract_entry_required_ok "$ej"; then
    local missing
    missing="$(printf '%s' "$ej" | jq -r '[
      (if (.id // "") == "" then "id" else empty end),
      (if (.target // "") == "" then "target" else empty end),
      (if ((.mode // "verbatim") | IN("verbatim","content","transform","summarize") | not) then "mode" else empty end),
      (if (.mode // "verbatim") != "content" and (.source == null) then "source" else empty end)
    ] | join(",")')"
    printf 'bad-shape:missing or invalid: %s' "$missing"
    return 0
  fi
  tmpl="$(printf '%s' "$ej" | jq -r '.template // empty')"
  if [[ -n "$tmpl" ]]; then
    printf 'unsupported-template'
    return 0
  fi
  mode="$(printf '%s' "$ej" | jq -r '.mode // "verbatim"')"
  src="$(wip_extract_source_kind "$ej")"
  case "$mode" in
    verbatim)
      case "$src" in
        simple-path | single-file) printf 'ok-verbatim' ;;
        multi-file) printf 'unsupported-source:multi-file' ;;
        none) printf 'bad-shape:verbatim mode requires a source' ;;
        *) printf 'unsupported-source:%s' "$src" ;;
      esac
      ;;
    content) printf 'ok-content' ;;
    transform) printf 'unsupported-mode:transform' ;;
    summarize) printf 'unsupported-mode:summarize' ;;
    *) printf 'bad-shape:unknown mode: %s' "$mode" ;;
  esac
}
