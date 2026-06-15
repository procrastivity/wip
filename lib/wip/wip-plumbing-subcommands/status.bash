# status — "where am I": initiative + round + active step + dirty .wip files.
# Deterministic; reads .wip.yaml + roadmap + (optional) git state.
# shellcheck shell=bash

wip_plumbing_cmd_status() {
  local slug=""
  while [[ $# -gt 0 ]]; do
    case "$1" in
      --initiative)
        [[ $# -ge 2 ]] || wip_die 2 usage "status: --initiative requires an argument"
        slug="$2"
        shift 2
        ;;
      --initiative=*)
        slug="${1#--initiative=}"
        shift
        ;;
      -*) wip_die 2 usage "status: unknown flag: $1" ;;
      *) wip_die 2 usage "status: unexpected arg: $1" ;;
    esac
  done

  local root mj
  root="$(wip_find_root)" || wip_die 4 no-manifest "no .wip.yaml found from $PWD upward"
  mj="$(wip_manifest_json "$root")"
  [[ -n "$mj" ]] || wip_die 4 bad-manifest "could not parse $root/.wip.yaml" ".wip.yaml"

  # Resolve initiative slug.
  if [[ -z "$slug" ]]; then
    slug="$(jq -r '.current_initiative // ""' <<<"$mj")"
    [[ -n "$slug" ]] || wip_die 3 no-initiative \
      "status: no current_initiative; pass --initiative <slug>"
  fi
  local init_record
  init_record="$(jq -c --arg s "$slug" '
    [.initiatives[]? | select(.slug == $s)] | (.[0] // null)
  ' <<<"$mj")"
  [[ "$init_record" != "null" ]] ||
    wip_die 3 unknown-initiative "status: initiative not in manifest: $slug"

  local status_field active_step_id roadmap_path
  status_field="$(jq -r '.status // "unknown"' <<<"$init_record")"
  active_step_id="$(jq -r '.active_step // ""' <<<"$init_record")"
  roadmap_path="$(jq -r '.roadmap // ""' <<<"$init_record")"
  [[ -n "$roadmap_path" ]] || roadmap_path=".wip/initiatives/$slug/roadmap.md"

  local doc
  doc="$(wip_roadmap_parse "$root/$roadmap_path")"

  # If active_step is unset in the manifest, infer it from first_unshipped.
  local inferred signals="[]"
  inferred="$(wip_roadmap_first_unshipped "$doc")"
  if [[ -z "$active_step_id" ]]; then
    active_step_id="$(jq -r '.id // ""' <<<"$inferred")"
  else
    # Surface a divergence signal when the manifest names a shipped step.
    local step_rec
    step_rec="$(wip_roadmap_step "$doc" "$active_step_id")"
    if [[ "$(jq -r '.shipped // false' <<<"$step_rec")" == "true" ]]; then
      signals="$(jq -nc --argjson a "$signals" '$a + ["manifest-step-ahead"]')"
    fi
  fi

  local active_step round
  if [[ -n "$active_step_id" ]]; then
    active_step="$(wip_roadmap_step "$doc" "$active_step_id")"
    if [[ "$active_step" == "null" || -z "$active_step" ]]; then
      active_step="$(jq -nc --arg id "$active_step_id" '{id:$id,title:null,shipped:false,shipped_date:null,lane:null}')"
    fi
    round="$(wip_roadmap_active_round "$doc" "$active_step_id")"
    [[ -n "$round" ]] || round="null"
  else
    active_step="null"
    round="null"
  fi

  # lanes_in_flight: the next actionable (first unshipped) step per lane that has
  # unshipped work in the active round — only when two+ lanes are in flight at
  # once (ADR-0010 §7). Preserves declared lane order; [] otherwise.
  local lanes_in_flight="[]" active_round_n
  active_round_n="$(jq -r '.n // empty' <<<"$round")"
  if [[ -n "$active_round_n" ]]; then
    lanes_in_flight="$(jq -c --argjson n "$active_round_n" '
      (.rounds[] | select(.n == $n)) as $r
      | [ $r.lanes[] as $ln
          | ($r.steps | map(select(.shipped == false and .lane == $ln)) | (.[0] // null)) as $s
          | select($s != null)
          | {lane: $ln, step: $s.id} ]
      | if length >= 2 then . else [] end
    ' <<<"$doc")"
  fi

  # dirty .wip files via git porcelain. Quietly empty when .wip/ is gitignored.
  local dirty="[]"
  if command -v git >/dev/null 2>&1; then
    local porcelain
    porcelain="$(git -C "$root" status --porcelain -- .wip 2>/dev/null || true)"
    if [[ -n "$porcelain" ]]; then
      dirty="$(printf '%s\n' "$porcelain" | awk '{ $1=""; sub(/^ +/, ""); print }' |
        jq -R . | jq -sc '.')"
    fi
  fi

  # solo_available derived from the same feature resolver detect uses.
  local features solo_available
  features="$(wip_features_json "$root" "$mj")"
  solo_available="$(jq -r '
    map(select(.name == "solo")) | (.[0].active // false)
  ' <<<"$features")"
  [[ -n "$solo_available" ]] || solo_available="false"

  jq -nc \
    --arg slug "$slug" --arg status "$status_field" \
    --argjson round "$round" --argjson active_step "$active_step" \
    --argjson lanes_in_flight "$lanes_in_flight" \
    --argjson dirty "$dirty" --argjson solo "$solo_available" \
    --argjson signals "$signals" '
    {
      ok: true,
      initiative: $slug,
      status: $status,
      round: $round,
      active_step: $active_step,
      lanes_in_flight: $lanes_in_flight,
      dirty_wip_files: $dirty,
      solo_available: $solo,
      signals: $signals
    }'
}
