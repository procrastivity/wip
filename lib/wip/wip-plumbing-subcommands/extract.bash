# extract — run the deterministic LDS Extract phase against an approved
# extraction manifest (step-15). The LDS seam per ADR-0006: this verb
# does the deterministic core; the LLM-driven analyze/review phases stay
# in the porcelain layer.
#
# Usage: extract [--manifest <path>] [--force] [--verify-hashes]
#
# v1 supports verbatim + content modes against simple-path /
# single-file-with-range sources. transform/summarize and multi-file
# sources land in `unsupported[]` (skip, don't fail). Source-hash
# verification is opt-in via `--verify-hashes`: off by default (ledger
# records `hash_verification: "skipped-v1"`), and when on it runs as a
# pre-write gate — a mismatch fails the run (`exit 4 hash-mismatch`)
# before any target is written.
# shellcheck shell=bash

# shellcheck source=lib/wip/wip-plumbing-extract-lib.bash
source "$WIP_LIB/wip-plumbing-extract-lib.bash"
# shellcheck source=lib/wip/wip-plumbing-setup-lib.bash
source "$WIP_LIB/wip-plumbing-setup-lib.bash"

wip_plumbing_cmd_extract() {
  local manifest_override="" force=0 verify_hashes=0
  while [[ $# -gt 0 ]]; do
    case "$1" in
      --manifest)
        [[ $# -ge 2 ]] || wip_die 2 usage "extract: --manifest requires a value"
        manifest_override="$2"
        shift 2
        ;;
      --manifest=*)
        manifest_override="${1#--manifest=}"
        shift
        ;;
      --force)
        force=1
        shift
        ;;
      --verify-hashes)
        verify_hashes=1
        shift
        ;;
      -*) wip_die 2 usage "extract: unknown flag: $1" ;;
      *) wip_die 2 usage "extract: unexpected arg: $1" ;;
    esac
  done

  if [[ "$force" == "1" ]]; then
    WIP_SETUP_FORCE=1
    export WIP_SETUP_FORCE
  fi

  local root
  root="${WIP_ROOT:-}"
  if [[ -z "$root" ]]; then
    set +e
    root="$(wip_find_root)"
    set -e
  fi
  [[ -n "$root" && -f "$root/.wip.yaml" ]] ||
    wip_die 3 missing-manifest "extract: no .wip.yaml found; run \`init\` first"

  # LDS preconditions (same shape as graduate).
  local mj features lds_enabled lds_sentinel_exists eng
  mj="$(wip_manifest_json "$root")"
  features="$(wip_features_json "$root" "$mj")"
  lds_enabled="$(printf '%s' "$features" | jq -r '.[] | select(.name=="lds") | .enabled // false')"
  lds_sentinel_exists="$(printf '%s' "$features" | jq -r '.[] | select(.name=="lds") | .sentinel_exists // false')"
  if [[ "$lds_enabled" != "true" ]]; then
    wip_die 3 lds-not-enabled \
      "extract: features.lds.enabled is false; LDS install is not in step-15's scope (backlog: setup-lds-verb)"
  fi
  eng="$(wip_extract_lds_root "$mj")"
  if [[ "$lds_sentinel_exists" != "true" ]]; then
    wip_die 3 lds-sentinel-missing \
      "extract: $eng/.lds-manifest.yaml missing; run the LDS install workflow (backlog: setup-lds-verb)" \
      "$eng/.lds-manifest.yaml"
  fi

  # Manifest path: explicit override > LDS sentinel default.
  local mpath
  if [[ -n "$manifest_override" ]]; then
    mpath="$manifest_override"
  else
    mpath="$eng/.lds-manifest.yaml"
  fi
  local mabs="$root/$mpath"
  [[ -f "$mabs" ]] || wip_die 4 manifest-missing "extract: manifest not found: $mpath" "$mpath"

  # Parse manifest to JSON.
  local extract_mj
  extract_mj="$(yq -o=json '.' "$mabs" 2>/dev/null)"
  if [[ -z "$extract_mj" || "$extract_mj" == "null" ]]; then
    wip_die 4 manifest-unparseable "extract: manifest is not parseable YAML: $mpath" "$mpath"
  fi

  # Validate manifest shape.
  local vrc kind
  set +e
  wip_extract_validate_manifest "$extract_mj" 2>/tmp/wip-extract-$$.err
  vrc=$?
  set -e
  if [[ "$vrc" != "0" ]]; then
    local vmsg
    vmsg="$(cat /tmp/wip-extract-$$.err 2>/dev/null)"
    rm -f /tmp/wip-extract-$$.err
    case "$vrc" in
      2) kind="incompatible-schema" ;;
      3) kind="manifest-not-approved" ;;
      4) kind="manifest-empty" ;;
      5) kind="duplicate-entry-id" ;;
      *) kind="manifest-invalid" ;;
    esac
    [[ -n "$vmsg" ]] && printf '%s\n' "$vmsg" >&2
    wip_die 4 "$kind" "extract: ${vmsg:-manifest validation failed}"
  fi
  rm -f /tmp/wip-extract-$$.err

  local total
  total="$(printf '%s' "$extract_mj" | jq -r '.entries | length')"

  # content_hash_check object threaded into the §7 report. Default (flag off)
  # is the literal "skipped-v1" so the report stays byte-identical to step-17;
  # the --verify-hashes pre-write gate (below) replaces it with a real verdict.
  local content_hash_json='{"status":"skipped-v1"}'

  # --verify-hashes pre-write gate (LDS source_hash_mismatch_handling): runs
  # AFTER manifest validation and BEFORE the entry write loop. It is read-only,
  # so it runs even under --dry-run. On any mismatch the run writes ZERO targets
  # (the loop is skipped) and fails exit 4 hash-mismatch — the §7 report is still
  # written first (below). hash_verification reflects the enum on the ok:true
  # path: skipped-v1 (off) | verified (≥1 hash checked, all matched) | no-hashes
  # (flag on, zero verifiable hashes).
  local hash_verification_value="skipped-v1"
  local hash_mismatch=0
  local hash_mismatch_paths_json="[]" hash_mismatches_json="[]"
  if [[ "$verify_hashes" == "1" ]]; then
    set +e
    content_hash_json="$(wip_extract_verify_hashes "$extract_mj" "$root")"
    set -e
    local ch_status ch_checked
    ch_status="$(printf '%s' "$content_hash_json" | jq -r '.status')"
    ch_checked="$(printf '%s' "$content_hash_json" | jq -r '.entries_checked')"
    if [[ "$ch_status" == "fail" ]]; then
      hash_mismatch=1
      hash_mismatches_json="$(printf '%s' "$content_hash_json" | jq -c '.mismatches')"
      hash_mismatch_paths_json="$(printf '%s' "$content_hash_json" | jq -c '[.mismatches[].source] | unique')"
    elif [[ "$ch_checked" -gt 0 ]]; then
      hash_verification_value="verified"
    else
      hash_verification_value="no-hashes"
    fi
  fi

  local wrote=() skipped=() wrote_forced=() refused=()
  local unsupported_json="[]" bad_json="[]"
  local i ej cls action rc status tmp dest target_rel

  # The pre-write gate: when --verify-hashes found a mismatch the loop body
  # runs zero iterations, so ZERO targets are written (LDS "never partial").
  # The loop body itself is unchanged.
  for ((i = 0; i < total && hash_mismatch == 0; i++)); do
    ej="$(printf '%s' "$extract_mj" | jq -c ".entries[$i]")"
    cls="$(wip_extract_classify_entry "$ej")"
    case "$cls" in
      ok-verbatim) action="verbatim" ;;
      ok-content) action="content" ;;
      ok-transform) action="transform" ;;
      unsupported-mode:*)
        local m="${cls#unsupported-mode:}"
        unsupported_json="$(printf '%s' "$unsupported_json" | jq -c \
          --arg id "$(printf '%s' "$ej" | jq -r '.id // ""')" \
          --arg mode "$m" \
          --arg reason "$m mode not supported in v1" \
          '. + [{id:$id, mode:$mode, reason:$reason}]')"
        continue
        ;;
      unsupported-source:*)
        local s="${cls#unsupported-source:}"
        unsupported_json="$(printf '%s' "$unsupported_json" | jq -c \
          --arg id "$(printf '%s' "$ej" | jq -r '.id // ""')" \
          --arg source_kind "$s" \
          --arg reason "$s source not supported in v1" \
          '. + [{id:$id, source_kind:$source_kind, reason:$reason}]')"
        continue
        ;;
      unsupported-transform:*)
        local tt="${cls#unsupported-transform:}"
        unsupported_json="$(printf '%s' "$unsupported_json" | jq -c \
          --arg id "$(printf '%s' "$ej" | jq -r '.id // ""')" \
          --arg transform_type "$tt" \
          --arg reason "$tt transform not supported in v1" \
          '. + [{id:$id, mode:"transform", transform_type:$transform_type, reason:$reason}]')"
        continue
        ;;
      unsupported-template)
        unsupported_json="$(printf '%s' "$unsupported_json" | jq -c \
          --arg id "$(printf '%s' "$ej" | jq -r '.id // ""')" \
          --arg reason "template/field_mappings not supported in v1" \
          '. + [{id:$id, reason:$reason}]')"
        continue
        ;;
      bad-shape:*)
        local msg="${cls#bad-shape:}"
        bad_json="$(printf '%s' "$bad_json" | jq -c \
          --arg id "$(printf '%s' "$ej" | jq -r '.id // ""')" \
          --arg reason "$msg" \
          '. + [{id:$id, reason:$reason}]')"
        continue
        ;;
      *)
        bad_json="$(printf '%s' "$bad_json" | jq -c \
          --arg id "$(printf '%s' "$ej" | jq -r '.id // ""')" \
          --arg reason "internal: unknown classification: $cls" \
          '. + [{id:$id, reason:$reason}]')"
        continue
        ;;
    esac

    target_rel="$eng/$(printf '%s' "$ej" | jq -r '.target')"
    dest="$root/$target_rel"

    tmp="$(mktemp -t wip-extract.XXXXXX)" ||
      wip_die 1 internal "extract: mktemp failed"

    set +e
    case "$action" in
      verbatim)
        wip_extract_render_verbatim "$ej" "$root" >"$tmp" 2>/tmp/wip-extract-$$.err
        rc=$?
        ;;
      transform)
        wip_extract_render_transform "$ej" "$root" >"$tmp" 2>/tmp/wip-extract-$$.err
        rc=$?
        ;;
      content)
        wip_extract_render_content "$ej" >"$tmp" 2>/tmp/wip-extract-$$.err
        rc=$?
        ;;
    esac
    set -e
    if [[ "$rc" != "0" ]]; then
      local rmsg
      rmsg="$(cat /tmp/wip-extract-$$.err 2>/dev/null)"
      rm -f /tmp/wip-extract-$$.err "$tmp"
      bad_json="$(printf '%s' "$bad_json" | jq -c \
        --arg id "$(printf '%s' "$ej" | jq -r '.id // ""')" \
        --arg reason "${rmsg:-render failed}" \
        '. + [{id:$id, reason:$reason}]')"
      continue
    fi
    rm -f /tmp/wip-extract-$$.err

    set +e
    status="$(wip_setup_write_idempotent "$tmp" "$dest")"
    rc=$?
    set -e
    rm -f "$tmp"
    if [[ "$rc" != "0" && "$rc" != "4" ]]; then
      wip_die 1 internal "extract: write helper failed (rc=$rc) for $target_rel"
    fi
    case "$status" in
      wrote) wrote+=("$target_rel") ;;
      skipped) skipped+=("$target_rel") ;;
      wrote_forced) wrote_forced+=("$target_rel") ;;
      refused) refused+=("$target_rel") ;;
    esac
  done

  local bad_count refused_count
  bad_count="$(printf '%s' "$bad_json" | jq 'length')"
  refused_count=${#refused[@]}

  local ok="true"
  local err_kind="" err_msg="" err_paths_json="[]"
  if ((hash_mismatch == 1)); then
    # Pre-write gate tripped: no targets were written (loop skipped above).
    ok="false"
    err_kind="hash-mismatch"
    err_msg="source hash verification failed; no targets written (run with the correct source or regenerate the manifest hashes)"
    err_paths_json="$hash_mismatch_paths_json"
  elif ((refused_count > 0)); then
    ok="false"
    err_kind="content-drift"
    err_msg="extracted targets differ from manifest output; re-run with --force to overwrite"
    err_paths_json="$(wip_json_string_array "${refused[@]}")"
  elif ((bad_count > 0)); then
    ok="false"
    err_kind="bad-entry-shape"
    err_msg="one or more entries failed shape validation"
  fi

  if [[ "${WIP_JSON:-1}" == "1" ]]; then
    if [[ "$ok" == "true" ]]; then
      jq -nc \
        --arg verb "extract" \
        --arg manifest "$mpath" \
        --argjson entries_total "$total" \
        --argjson wrote "$(wip_json_string_array "${wrote[@]+"${wrote[@]}"}")" \
        --argjson skipped "$(wip_json_string_array "${skipped[@]+"${skipped[@]}"}")" \
        --argjson wrote_forced "$(wip_json_string_array "${wrote_forced[@]+"${wrote_forced[@]}"}")" \
        --argjson refused "$(wip_json_string_array "${refused[@]+"${refused[@]}"}")" \
        --argjson unsupported "$unsupported_json" \
        --argjson bad "$bad_json" \
        --arg hash_verification "$hash_verification_value" '
        {ok:true, verb:$verb, manifest:$manifest, entries_total:$entries_total,
         wrote:$wrote, skipped_idempotent:$skipped,
         wrote_forced:$wrote_forced, refused:$refused,
         unsupported:$unsupported, bad_entries:$bad,
         hash_verification:$hash_verification}'
    else
      jq -nc \
        --arg verb "extract" \
        --arg manifest "$mpath" \
        --arg kind "$err_kind" \
        --arg message "$err_msg" \
        --argjson paths "$err_paths_json" \
        --argjson bad "$bad_json" \
        --argjson mismatches "$hash_mismatches_json" '
        {ok:false, verb:$verb, manifest:$manifest,
         error:({code:4, kind:$kind, message:$message}
                + (if ($paths | length) > 0 then {paths:$paths} else {} end)
                + (if ($bad | length) > 0 then {bad_entries:$bad} else {} end)
                + (if ($mismatches | length) > 0 then {mismatches:$mismatches} else {} end))}'
    fi
  fi

  # LDS §7 extraction report — serialize the ledger to disk as
  # <eng-docs>/extraction-report.{yaml,md} (step-17). Additive: the stdout
  # JSON envelope and stderr one-liner above are unchanged. The report is
  # written in BOTH the ok:true and ok:false branches and BEFORE the exit 4
  # below, so §7.3 (report on partial failure) holds. It is a PLAIN
  # OVERWRITE — explicitly NOT wip_setup_write_idempotent: the report
  # embeds a fresh executed_at, so routing it through the idempotency helper
  # would make every second run report spurious content-drift on the report
  # file itself. --dry-run (WIP_DRY_RUN=1) writes nothing.
  if [[ "${WIP_DRY_RUN:-0}" != "1" ]]; then
    local report_executed_at report_hash
    report_executed_at="$(date -u +%Y-%m-%dT%H:%M:%SZ)"
    set +e
    report_hash="$(shasum -a 256 "$mabs" 2>/dev/null | awk '{print $1}')"
    set -e

    # file_existence_check (§7.2): stat each successful target live.
    local _p existence_missing="[]" existence_expected=0 existence_created=0
    for _p in "${wrote[@]+"${wrote[@]}"}" "${wrote_forced[@]+"${wrote_forced[@]}"}"; do
      existence_expected=$((existence_expected + 1))
      if [[ -f "$root/$_p" ]]; then
        existence_created=$((existence_created + 1))
      else
        existence_missing="$(printf '%s' "$existence_missing" | jq -c --arg p "$_p" '. + [$p]')"
      fi
    done
    local existence_status="pass"
    [[ "$existence_created" -lt "$existence_expected" ]] && existence_status="fail"
    local existence_json
    existence_json="$(jq -nc \
      --arg s "$existence_status" \
      --argjson e "$existence_expected" \
      --argjson c "$existence_created" \
      --argjson m "$existence_missing" \
      '{status:$s, expected_files:$e, created_files:$c, missing_files:$m}')"

    local report_wrote report_skipped report_forced report_refused
    report_wrote="$(wip_json_string_array "${wrote[@]+"${wrote[@]}"}")"
    report_skipped="$(wip_json_string_array "${skipped[@]+"${skipped[@]}"}")"
    report_forced="$(wip_json_string_array "${wrote_forced[@]+"${wrote_forced[@]}"}")"
    report_refused="$(wip_json_string_array "${refused[@]+"${refused[@]}"}")"

    local eng_abs="$root/$eng" report_yaml report_md
    set +e
    report_yaml="$(wip_extract_report_yaml \
      "$mpath" "$total" "$report_wrote" "$report_skipped" "$report_forced" \
      "$report_refused" "$unsupported_json" "$bad_json" "$force" \
      "$report_executed_at" "${report_hash:-}" "$eng" "$existence_json" \
      "$content_hash_json")"
    report_md="$(wip_extract_report_md \
      "$mpath" "$total" "$report_wrote" "$report_skipped" "$report_forced" \
      "$report_refused" "$unsupported_json" "$bad_json" "$force" \
      "$report_executed_at" "${report_hash:-}" "$eng" "$existence_json" \
      "$content_hash_json")"
    set -e
    printf '%s\n' "$report_yaml" >"$eng_abs/extraction-report.yaml"
    printf '%s\n' "$report_md" >"$eng_abs/extraction-report.md"
  fi

  if [[ "$ok" != "true" ]]; then
    printf 'wip-plumbing: extract: %s\n' "$err_msg" >&2
    exit 4
  fi

  if [[ "${WIP_QUIET:-0}" != "1" ]]; then
    printf 'wip-plumbing: extract: %d entries: wrote=%d skipped=%d unsupported=%d\n' \
      "$total" "${#wrote[@]}" "${#skipped[@]}" \
      "$(printf '%s' "$unsupported_json" | jq 'length')" >&2
  fi
}
