# status — "where am I": initiative + round + active step + dirty .wip files.
# Deterministic; reads .wip.yaml + roadmap + (optional) git state.
# shellcheck shell=bash

wip_plumbing_cmd_status() {
  local slug="" probe_solo=0 probe_forge=0
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
      --probe-solo)
        probe_solo=1
        shift
        ;;
      --probe-forge)
        probe_forge=1
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

  # Closeout hint: active_step names a not-yet-shipped step whose workplan is
  # already archived — the same "half-done-closeout" drift doctor flags, surfaced
  # here so status/next don't silently nominate an already-closed-out step.
  # Reuses the shared archive probe (single-sources "archived" with doctor).
  if [[ -n "$active_step_id" ]] &&
    [[ "$(jq -r '.shipped // false' <<<"$active_step")" == "false" ]] &&
    _wip_archived_workplan_exists "$root/.wip/initiatives/$slug/archive" "$active_step_id"; then
    signals="$(jq -nc --argjson a "$signals" '$a + ["half-done-closeout"]')"
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

  # solo_available derived from the same feature resolver detect uses. This is
  # a CONFIG echo — "Solo is declared", not "Solo answers".
  local features solo_available
  features="$(wip_features_json "$root" "$mj")"
  solo_available="$(jq -r '
    map(select(.name == "solo")) | (.[0].active // false)
  ' <<<"$features")"
  [[ -n "$solo_available" ]] || solo_available="false"

  # solo_reachable: a LIVE probe of the Solo control plane (opt-in via
  # --probe-solo, since it shells out and is non-deterministic). Distinct from
  # solo_available: config says declared, this says actually answering.
  #   null  = not probed (no flag).
  #   true  = `solo status --json` returned ok + data.ready.
  #   false = the probe ran and Solo did not answer ready, or Solo is declared
  #           but the `solo` CLI is missing.
  # WIP_SOLO_STATUS_CMD overrides the probe command (test seam).
  local solo_reachable="null" orch_backend
  orch_backend="$(jq -r '.features.orchestration.backend // ""' <<<"$mj")"
  if [[ "$probe_solo" == "1" && "$solo_available" == "true" ]]; then
    local probe_cmd="${WIP_SOLO_STATUS_CMD:-}"
    if [[ -z "$probe_cmd" ]] && command -v solo >/dev/null 2>&1; then
      probe_cmd="solo status --json"
    fi
    if [[ -n "$probe_cmd" ]]; then
      local probe ok ready
      probe="$(bash -c "$probe_cmd" 2>/dev/null || true)"
      ok="$(jq -r '.ok // false' <<<"$probe" 2>/dev/null || printf 'false')"
      ready="$(jq -r '.data.ready // false' <<<"$probe" 2>/dev/null || printf 'false')"
      if [[ "$ok" == "true" && "$ready" == "true" ]]; then
        solo_reachable="true"
      else
        solo_reachable="false"
      fi
    else
      solo_reachable="false"
    fi
  fi
  # Actionable signal: the active orchestration backend is solo but Solo isn't
  # answering — orchestration would stall at the first spawn. /wip:status keys
  # off this to warn + offer the Task-backend fallback (ADR-0014).
  if [[ "$solo_reachable" == "false" && "$orch_backend" == "solo" ]]; then
    signals="$(jq -nc --argjson a "$signals" '$a + ["solo-unreachable"]')"
  fi

  # forge_available: CONFIG echo — "a forge is declared the active transition
  # owner" (features.forge.enabled), the Tier-1 owner from ADR-0018. Read raw
  # from the manifest so Lane A does not depend on Lane B's features.forge
  # detector; both lanes key off the same manifest key.
  local forge_available
  forge_available="$(jq -r '.features.forge.enabled // false' <<<"$mj")"
  [[ -n "$forge_available" ]] || forge_available="false"

  # forge_reachable: a LIVE probe of the forge CLI (opt-in via --probe-forge,
  # since it shells out). Mirrors solo_reachable (ADR-0014):
  #   null  = not probed (no flag, or no forge declared).
  #   true  = the resolved liveness command (e.g. `gh auth status`) exited 0.
  #   false = the probe ran and the forge did not answer, or a forge is declared
  #           but no gh/glab CLI is present. The transport seam (ADR-0018,
  #           step-02) resolves the command; WIP_FORGE_STATUS_CMD overrides it.
  local forge_reachable="null"
  if [[ "$probe_forge" == "1" && "$forge_available" == "true" ]]; then
    local fcli fcmd forge_backend
    forge_backend="$(jq -r '.features.forge.backend // ""' <<<"$mj")"
    fcli="$(_wip_forge_detect "$forge_backend")"
    fcmd="$(_wip_forge_status_cmd "$fcli")"
    if [[ -n "$fcmd" ]] && _wip_forge_run "$fcmd" >/dev/null 2>&1; then
      forge_reachable="true"
    else
      forge_reachable="false"
    fi
  fi
  # Actionable signal: a forge owns the Tier-1 transition but isn't answering, so
  # the push/merge observation would stall. Mirrors solo-unreachable's
  # "non-actionable → no signal" discipline — silent unless a forge is declared.
  if [[ "$forge_reachable" == "false" && "$forge_available" == "true" ]]; then
    signals="$(jq -nc --argjson a "$signals" '$a + ["forge-unreachable"]')"
  fi

  # tracker_available + tracker_state: the issue-tracker config echo (parallel to
  # solo_available) and the active node's cached lifecycle state — the durable
  # "cache-as-floor" read (BRIEF §3 / ADR-0019 §A). Live reachability
  # (tracker_reachable) arrives with the Round 4 transport/probe; for now the
  # cache is the floor. tracker_state is null when the active node is unmapped or
  # the cache has no entry yet.
  local tracker_available tracker_state="null"
  tracker_available="$(jq -r '.features["issue-tracker"].enabled // false' <<<"$mj")"
  [[ -n "$tracker_available" ]] || tracker_available="false"
  if [[ -n "$active_step_id" ]]; then
    tracker_state="$(_wip_tracker_cache_get "$root" "$slug/$active_step_id")"
  fi

  # Deferred items (## Deferred in the roadmap) — informational, clearly
  # NOT-actionable context, surfaced so "where am I" can see consciously
  # postponed work without it ever being nominated as a next step (BDS-17).
  local deferred
  deferred="$(jq -c '.deferred' <<<"$doc")"

  # unfiled_tracker_items: deferred + backlog entries with no `[tracker: ID]`
  # mapping — candidate work not yet filed as a tracker issue (BRIEF §7). A
  # SUGGESTION only (never auto-filed); surfaced solely when issue-tracker is
  # enabled. Each carries its source so the user knows where it lives.
  local unfiled="[]"
  if [[ "$tracker_available" == "true" ]]; then
    unfiled="$(jq -c '
      [ (.deferred[]? | . + {source:"deferred"}),
        (.backlog[]?  | . + {source:"backlog"}) ]
      | map(select(.tracker == null) | {id, title, source})
    ' <<<"$doc")"
  fi

  jq -nc \
    --arg slug "$slug" --arg status "$status_field" \
    --argjson round "$round" --argjson active_step "$active_step" \
    --argjson lanes_in_flight "$lanes_in_flight" \
    --argjson dirty "$dirty" --argjson solo "$solo_available" \
    --argjson solo_reachable "$solo_reachable" \
    --argjson forge_available "$forge_available" \
    --argjson forge_reachable "$forge_reachable" \
    --argjson tracker_available "$tracker_available" \
    --argjson tracker_state "$tracker_state" \
    --argjson unfiled "$unfiled" \
    --argjson deferred "$deferred" \
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
      solo_reachable: $solo_reachable,
      forge_available: $forge_available,
      forge_reachable: $forge_reachable,
      tracker_available: $tracker_available,
      tracker_state: $tracker_state,
      unfiled_tracker_items: $unfiled,
      deferred: $deferred,
      signals: $signals
    }'
}
