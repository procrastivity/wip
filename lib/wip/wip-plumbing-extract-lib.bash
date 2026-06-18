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

# wip_extract_sha256 [<file-path>]
#
# Echo the lowercase SHA-256 hex digest of a file (when $1 is given) or
# of stdin (when no arg). Leads with `sha256sum` (provided by coreutils,
# already a flake dep) and falls back to `shasum -a 256` (the perl tool);
# returns non-zero + a stderr diagnostic if neither hasher is on PATH.
# Side-effect-free: the only output on success is the bare digest.
# shellcheck disable=SC2120  # $1 is an optional file path; stdin callers pass none
wip_extract_sha256() {
  local src="${1:-}"
  if command -v sha256sum >/dev/null 2>&1; then
    if [[ -n "$src" ]]; then
      sha256sum -- "$src" | awk '{print $1}'
    else
      sha256sum | awk '{print $1}'
    fi
  elif command -v shasum >/dev/null 2>&1; then
    if [[ -n "$src" ]]; then
      shasum -a 256 -- "$src" | awk '{print $1}'
    else
      shasum -a 256 | awk '{print $1}'
    fi
  else
    printf 'wip-plumbing: extract: no SHA-256 hasher (sha256sum/shasum) found\n' >&2
    return 1
  fi
}

# wip_extract_source_body <entry-json> <repo-root>
#
# Emit just the extracted source-range bytes — the `cat`/`awk` body that
# `wip_extract_render_verbatim` wraps with attribution — and NOTHING else
# (no attribution comment block, no separating blank line). 1-indexed
# inclusive line ranges per LDS §3.1; missing end_line = EOF, missing
# start_line = 1; no range at all = whole file (raw `cat` bytes).
#
# This is the single source of truth for "what bytes count as the source"
# — both the verbatim renderer and the `--verify-hashes` hash recipe
# (OQ1: hash these exact bytes, trailing newline included for ranges)
# read through it, so the rendered target and the verified digest can
# never drift apart.
#
# Returns 0 on success; 2 + stderr diagnostic on a missing source file
# or a source kind that carries no body.
wip_extract_source_body() {
  local ej="$1" root="$2" kind file abs start end
  kind="$(wip_extract_source_kind "$ej")"
  case "$kind" in
    simple-path) file="$(printf '%s' "$ej" | jq -r '.source')" ;;
    single-file) file="$(printf '%s' "$ej" | jq -r '.source.file')" ;;
    *)
      printf 'wip-plumbing: extract: source_body called with kind=%s\n' "$kind" >&2
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

# wip_extract_heading_adjust [<level_offset> [<skip_first>]]
#
# Pure, fence-aware ATX-heading level shifter: reads markdown on stdin,
# writes the shifted markdown to stdout, NOTHING else. The `heading_adjust`
# transform engine (LDS extract.md §3.3) — the deterministic "small markdown
# engine" the roadmap envisioned. Side-effect-free and unit-testable in
# isolation, exactly like wip_extract_source_body / wip_extract_sha256.
#
# Args (both optional, faithful defaults per D5):
#   level_offset  integer added to each ATX heading level; default 0 (no-op).
#   skip_first    "true" leaves the first document-wide ATX heading at its
#                 original level (LDS "useful for document titles");
#                 anything else (default) shifts every heading.
#
# Behavior:
#   - Only ATX headings (≤3 leading spaces, 1–6 `#`, then space/tab or EOL)
#     outside fenced code are adjusted. `new_level = clamp(level+offset, 1, 6)`
#     (D4) — never emits `#######` or drops below `#`.
#   - Fence-aware (D3, OQ5): a line whose first non-space run (≤3 leading
#     spaces) is ≥3 backticks or ≥3 tildes toggles fenced-code state; `#`
#     lines inside a fence are left untouched. This is the simplified fence
#     rule — info strings and exact open/close fence-length matching are not
#     modeled (unnecessary for a heading shifter).
#   - A 4-space-indented `#` (code, not a heading), a `#tag` with no following
#     space (not a heading), and 7+ `#` (not a heading) are all left untouched.
#   - Setext headings (`===` / `---` underline) are left unchanged in v1
#     (OQ2): a numeric offset on an underline form is ill-defined.
wip_extract_heading_adjust() {
  local offset="${1:-0}" skip_first="${2:-false}"
  awk -v off="$offset" -v skipfirst="$skip_first" '
    BEGIN { infence = 0; firstseen = 0 }
    {
      line = $0
      match(line, /^ */); nsp = RLENGTH
      body = substr(line, nsp + 1)
      # Fenced-code toggle: <=3 leading spaces then >=3 backticks or tildes.
      if (nsp <= 3 && (body ~ /^```/ || body ~ /^~~~/)) {
        infence = !infence; print line; next
      }
      if (infence) { print line; next }
      # ATX heading candidate: <=3 leading spaces then a run of `#`.
      if (nsp <= 3 && body ~ /^#/) {
        match(body, /^#+/); level = RLENGTH
        after = substr(body, level + 1)
        if (level >= 1 && level <= 6 && (after == "" || after ~ /^[ \t]/)) {
          if (skipfirst == "true" && firstseen == 0) {
            firstseen = 1; print line; next
          }
          firstseen = 1
          newlevel = level + off
          if (newlevel < 1) newlevel = 1
          if (newlevel > 6) newlevel = 6
          hashes = ""
          for (i = 0; i < newlevel; i++) hashes = hashes "#"
          print substr(line, 1, nsp) hashes after
          next
        }
      }
      print line
    }
  '
}

# wip_extract_render_verbatim <entry-json> <repo-root>
#
# Read the source file (simple-path or single-file with optional
# start/end line range), prepend attribution, emit the bytes to stdout.
# The body bytes come from wip_extract_source_body so the rendered target
# and the verified hash stay in lock-step (see that helper).
#
# Returns 0 on success; 2 + stderr diagnostic on a missing source file
# or unreadable range.
wip_extract_render_verbatim() {
  local ej="$1" root="$2" kind
  kind="$(wip_extract_source_kind "$ej")"
  case "$kind" in
    simple-path | single-file) ;;
    *)
      printf 'wip-plumbing: extract: render_verbatim called with kind=%s\n' "$kind" >&2
      return 2
      ;;
  esac
  wip_extract_attribution_lines "$ej"
  printf '\n'
  wip_extract_source_body "$ej" "$root"
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

# wip_extract_render_transform <entry-json> <repo-root>
#
# Render a `transform` + `heading_adjust` entry: attribution (IDENTICAL to
# verbatim — a transform has a real source, D2) + a blank line + the extracted
# source body piped through wip_extract_heading_adjust. `level_offset` /
# `skip_first` are read from `transform_config.options` with the faithful
# defaults (0 / false, D5): a missing options block is a no-op shift, not a
# failure.
#
# The source body is captured (not streamed) so a missing-source failure from
# wip_extract_source_body still surfaces as this function's return code — a
# bare `source_body | heading_adjust` pipeline would mask it behind awk's
# exit 0. awk normalizes the body's trailing newline regardless, so the
# captured-vs-streamed distinction is byte-invisible in the output.
#
# Returns 0 on success; 2 + stderr diagnostic on a missing source file or a
# source kind that carries no body (same contract as render_verbatim).
wip_extract_render_transform() {
  local ej="$1" root="$2" kind offset skip_first body
  kind="$(wip_extract_source_kind "$ej")"
  case "$kind" in
    simple-path | single-file) ;;
    *)
      printf 'wip-plumbing: extract: render_transform called with kind=%s\n' "$kind" >&2
      return 2
      ;;
  esac
  offset="$(printf '%s' "$ej" | jq -r '.transform_config.options.level_offset // 0')"
  skip_first="$(printf '%s' "$ej" | jq -r '.transform_config.options.skip_first // false')"
  body="$(wip_extract_source_body "$ej" "$root")" || return 2
  wip_extract_attribution_lines "$ej"
  printf '\n'
  printf '%s' "$body" | wip_extract_heading_adjust "$offset" "$skip_first"
}

# wip_extract_classify_entry <entry-json>
#
# Decide how the dispatcher should handle this entry. Echo one of:
#   ok-verbatim
#   ok-content
#   ok-transform
#   bad-shape:<message>
#   unsupported-mode:<mode>
#   unsupported-source:<source-kind>
#   unsupported-transform:<transform-type>
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
    transform)
      # D5: only an absent/non-map transform_config is bad-shape; a present
      # map with missing options defaults faithfully (handled at render).
      local tc_kind ttype
      tc_kind="$(printf '%s' "$ej" | jq -r '.transform_config | type')"
      if [[ "$tc_kind" != "object" ]]; then
        printf 'bad-shape:transform mode requires a transform_config map'
      else
        ttype="$(printf '%s' "$ej" | jq -r '.transform_config.type // ""')"
        case "$src" in
          multi-file) printf 'unsupported-source:multi-file' ;;
          simple-path | single-file)
            if [[ "$ttype" == "heading_adjust" ]]; then
              printf 'ok-transform'
            else
              printf 'unsupported-transform:%s' "$ttype"
            fi
            ;;
          none) printf 'bad-shape:transform mode requires a source' ;;
          *) printf 'unsupported-source:%s' "$src" ;;
        esac
      fi
      ;;
    summarize) printf 'unsupported-mode:summarize' ;;
    *) printf 'bad-shape:unknown mode: %s' "$mode" ;;
  esac
}

# wip_extract_verify_hashes <manifest-json> <repo-root>
#
# Pure source-hash verification pass (LDS source_hash_mismatch_handling).
# Walks the manifest entries, selects the verifiable ones — an entry is
# verifiable iff it classifies `ok-verbatim` AND its source is the
# single-file object form AND that object carries a non-empty `hash` —
# computes the body digest via the OQ1 recipe (`wip_extract_source_body`
# bytes through `wip_extract_sha256`), compares it to the declared hash,
# and echoes the §7 `content_hash_check` JSON object:
#
#   { status: pass|fail,
#     entries_checked, entries_matched, entries_no_hash,
#     mismatches: [ {id, source, expected_hash, actual_hash, status} ] }
#
# where a mismatch row's `status` is "mismatch" (digests differ) or
# "missing" (hashed source file absent). Non-verifiable entries (simple-path
# strings, single-file without a hash, content mode, unsupported/bad
# entries) are counted in `entries_no_hash` and never fail. `status` is
# "fail" iff there is at least one mismatch row, else "pass" (including the
# zero-verifiable-hash case — the no-op signal is carried by the caller's
# `hash_verification: "no-hashes"` enum + the report warning). The flag-off
# `{status:"skipped-v1"}` object is supplied by the command layer, never by
# this function. Side-effect-free and unit-testable.
wip_extract_verify_hashes() {
  local mj="$1" root="$2"
  local total i ej cls kind hash id source_file actual
  local checked=0 matched=0 no_hash=0 mismatches="[]"
  total="$(printf '%s' "$mj" | jq -r '(.entries // []) | length')"
  for ((i = 0; i < total; i++)); do
    ej="$(printf '%s' "$mj" | jq -c ".entries[$i]")"
    cls="$(wip_extract_classify_entry "$ej")"
    kind="$(wip_extract_source_kind "$ej")"
    if [[ "$cls" != "ok-verbatim" || "$kind" != "single-file" ]]; then
      no_hash=$((no_hash + 1))
      continue
    fi
    hash="$(printf '%s' "$ej" | jq -r '.source.hash // ""')"
    if [[ -z "$hash" ]]; then
      no_hash=$((no_hash + 1))
      continue
    fi
    checked=$((checked + 1))
    id="$(printf '%s' "$ej" | jq -r '.id // ""')"
    source_file="$(printf '%s' "$ej" | jq -r '.source.file')"
    if [[ ! -f "$root/$source_file" ]]; then
      mismatches="$(printf '%s' "$mismatches" | jq -c \
        --arg id "$id" --arg source "$source_file" --arg expected "$hash" \
        '. + [{id:$id, source:$source, expected_hash:$expected, actual_hash:null, status:"missing"}]')"
      continue
    fi
    # shellcheck disable=SC2119  # hashing stdin, not forwarding our own args
    actual="$(wip_extract_source_body "$ej" "$root" 2>/dev/null | wip_extract_sha256)"
    if [[ "$actual" != "$hash" ]]; then
      mismatches="$(printf '%s' "$mismatches" | jq -c \
        --arg id "$id" --arg source "$source_file" --arg expected "$hash" --arg actual "$actual" \
        '. + [{id:$id, source:$source, expected_hash:$expected, actual_hash:$actual, status:"mismatch"}]')"
    else
      matched=$((matched + 1))
    fi
  done
  local status="pass"
  [[ "$(printf '%s' "$mismatches" | jq 'length')" -gt 0 ]] && status="fail"
  jq -nc \
    --arg status "$status" \
    --argjson checked "$checked" \
    --argjson matched "$matched" \
    --argjson no_hash "$no_hash" \
    --argjson mismatches "$mismatches" \
    '{status:$status, entries_checked:$checked, entries_matched:$matched, entries_no_hash:$no_hash, mismatches:$mismatches}'
}

# --- LDS §7 extraction report renderers (pure, no I/O) ----------------------
#
# Both renderers are side-effect-free: they take the already-computed
# ledger data and emit report text to stdout. The command layer
# (extract.bash) owns every filesystem write. Governing rule:
# FAITHFUL-SUBSET SERIALIZATION — render the §7 shape, populate every
# field derivable from the ledger plus the caller's cheap stat, and set
# genuinely-unavailable fields (source/output line counts, source-hash
# verification) to null / "skipped-v1". Never fabricate numbers. See
# workplan step-17 §"Ledger → §7 vocabulary mapping" for the field map.
#
# Shared positional args (identical order for both functions):
#   $1  manifest          manifest path (metadata.manifest_file)
#   $2  entries_total     integer
#   $3  wrote_json        JSON array of success target paths
#   $4  skipped_json      JSON array of idempotent-skip target paths
#   $5  wrote_forced_json JSON array of forced-overwrite target paths
#   $6  refused_json      JSON array of content-drift target paths
#   $7  unsupported_json  JSON array of unsupported entry objects (ledger verbatim)
#   $8  bad_json          JSON array of {id, reason} bad-entry objects
#   $9  force             "0" | "1"
#   $10 executed_at       ISO-8601 timestamp (caller computes once)
#   $11 manifest_hash     sha256 hex of the manifest file, or "" → null
#   $12 eng               eng-docs root (for layer_breakdown prefix strip)
#   $13 existence_json    file_existence_check object (caller stats live)
#   $14 content_hash_json content_hash_check object (caller computes; the
#                         literal {status:"skipped-v1"} when --verify-hashes
#                         is off, so flag-off output is byte-identical)

# wip_extract_report_yaml — render the §7.2 machine-readable YAML report.
wip_extract_report_yaml() {
  local manifest="$1" total="$2" wrote="$3" skipped="$4" forced="$5" \
    refused="$6" unsupported="$7" bad="$8" force="$9" executed_at="${10}" \
    manifest_hash="${11}" eng="${12}" existence="${13}" content_hash="${14}"
  jq -n \
    --arg manifest "$manifest" \
    --argjson total "$total" \
    --argjson wrote "$wrote" \
    --argjson skipped "$skipped" \
    --argjson forced "$forced" \
    --argjson refused "$refused" \
    --argjson unsupported "$unsupported" \
    --argjson bad "$bad" \
    --arg force "$force" \
    --arg executed_at "$executed_at" \
    --arg manifest_hash "$manifest_hash" \
    --arg eng "$eng" \
    --argjson existence "$existence" \
    --argjson content_hash "$content_hash" '
    ($force == "1") as $forced_flag
    | ($wrote + $forced) as $created
    | {
        extraction_report: {
          metadata: {
            manifest_file: $manifest,
            manifest_hash: (if $manifest_hash == "" then null else $manifest_hash end),
            executed_at: $executed_at,
            executed_by: null,
            flags: { force: $forced_flag, resume: false }
          },
          summary: {
            total_entries: $total,
            successful: ($created | length),
            failed: (($refused | length) + ($bad | length)),
            skipped: ($skipped | length),
            unsupported: ($unsupported | length)
          },
          line_statistics: {
            source_lines_processed: null,
            output_lines_generated: null,
            variance_percentage: null
          },
          layer_breakdown: (
            $created
            | map(ltrimstr($eng + "/") | split("/")[0])
            | group_by(.)
            | map({ key: .[0], value: { files_created: length, total_lines: null } })
            | from_entries
          ),
          files_created: (
            ($wrote   | map({ target: ., source: null, source_lines: null, output_lines: null, template: null, status: "success" }))
            + ($forced | map({ target: ., source: null, source_lines: null, output_lines: null, template: null, status: "success" }))
            + ($refused | map({ target: ., source: null, source_lines: null, output_lines: null, template: null, status: "failed", error: "content-drift" }))
            + ($bad | map({ target: null, id: .id, source: null, source_lines: null, output_lines: null, template: null, status: "failed", error: .reason }))
          ),
          unsupported: $unsupported,
          verification_results: {
            line_count_check: { status: "skipped-v1" },
            file_existence_check: $existence,
            content_hash_check: $content_hash
          },
          warnings: (
            if ($content_hash.status != "skipped-v1") and (($content_hash.entries_checked // 0) == 0)
            then [{ type: "no-verifiable-hashes", message: "--verify-hashes was set but no entry carried a verifiable source.hash; hash verification was a no-op" }]
            else []
            end
          ),
          errors: (
            ($refused | map({ entry: ., type: "content-drift", message: "extracted target differs from manifest output" }))
            + ($bad | map({ entry: .id, type: "bad-entry-shape", message: .reason }))
          ),
          source_changes: { detected: false, files_changed: [], force_flag_used: $forced_flag }
        }
      }
  ' | yq -P -o=yaml '.'
}

# wip_extract_report_md — render the §7.4 human-readable summary to a file.
# The final `Status:` line is COMPLETED when no entries failed, else
# COMPLETED WITH ERRORS (failed = refused + bad, the same data ok:false
# is derived from in extract.bash).
wip_extract_report_md() {
  local manifest="$1" total="$2" wrote="$3" skipped="$4" forced="$5" \
    refused="$6" unsupported="$7" bad="$8" force="$9" executed_at="${10}" \
    manifest_hash="${11}" eng="${12}" existence="${13}" content_hash="${14}"

  local created successful failed skipped_n unsupported_n
  created="$(jq -cn --argjson w "$wrote" --argjson f "$forced" '$w + $f')"
  successful="$(printf '%s' "$created" | jq 'length')"
  failed="$(jq -n --argjson r "$refused" --argjson b "$bad" '($r | length) + ($b | length)')"
  skipped_n="$(printf '%s' "$skipped" | jq 'length')"
  unsupported_n="$(printf '%s' "$unsupported" | jq 'length')"

  local force_flag="false"
  [[ "$force" == "1" ]] && force_flag="true"
  local status_line="COMPLETED"
  [[ "$failed" -gt 0 ]] && status_line="COMPLETED WITH ERRORS"

  local ex_status ex_expected ex_created
  ex_status="$(printf '%s' "$existence" | jq -r '.status')"
  ex_expected="$(printf '%s' "$existence" | jq -r '.expected_files')"
  ex_created="$(printf '%s' "$existence" | jq -r '.created_files')"

  # Content hash check line — mirrors the File existence line. Dynamic from
  # the threaded content_hash object: skipped-v1 verbatim when the flag is
  # off (byte-identical to step-17), else a pass/fail summary with counts.
  local ch_status ch_line
  ch_status="$(printf '%s' "$content_hash" | jq -r '.status')"
  case "$ch_status" in
    pass)
      ch_line="$(printf '%s' "$content_hash" | jq -r '"pass (\(.entries_matched)/\(.entries_checked) entries matched)"')"
      ;;
    fail)
      ch_line="$(printf '%s' "$content_hash" | jq -r '"fail (\(.entries_matched)/\(.entries_checked) matched, \(.mismatches | length) mismatch)"')"
      ;;
    *)
      ch_line="$ch_status"
      ;;
  esac

  printf 'EXTRACTION REPORT\n'
  printf '=================\n\n'
  printf 'Date: %s\n' "$executed_at"
  printf 'Manifest: %s\n' "$manifest"
  printf 'Manifest hash: %s\n' "${manifest_hash:-(none)}"
  printf 'Flags: force=%s, resume=false\n\n' "$force_flag"

  printf 'FILES CREATED\n'
  printf -- '-------------\n'
  if [[ "$successful" -gt 0 ]]; then
    printf '%s' "$created" | jq -r '.[]'
  else
    printf '(none)\n'
  fi
  printf '\n'

  printf 'LAYER SUMMARY\n'
  printf -- '-------------\n'
  if [[ "$successful" -gt 0 ]]; then
    printf '%s' "$created" | jq -r --arg eng "$eng" '
      map(ltrimstr($eng + "/") | split("/")[0])
      | group_by(.)
      | map("\(.[0]): \(length) files (lines: n/a)")[]'
  else
    printf '(none)\n'
  fi
  printf '\n'

  printf 'SUMMARY\n'
  printf -- '-------\n'
  printf 'Total entries processed: %s\n' "$total"
  printf 'Files created: %s\n' "$successful"
  printf 'Errors: %s\n' "$failed"
  printf 'Skipped (idempotent): %s\n' "$skipped_n"
  printf 'Unsupported: %s\n' "$unsupported_n"
  printf 'Warnings: 0\n\n'
  printf 'Source lines: not tracked in v1\n'
  printf 'Output lines: not tracked in v1\n\n'

  printf 'VERIFICATION\n'
  printf -- '------------\n'
  printf 'Line count check:     skipped-v1\n'
  printf 'File existence check: %s (%s/%s files)\n' "$ex_status" "$ex_created" "$ex_expected"
  printf 'Content hash check:   %s\n\n' "$ch_line"

  printf 'Report saved to: %s/extraction-report.yaml\n\n' "$eng"
  printf 'Status: %s\n' "$status_line"
}
