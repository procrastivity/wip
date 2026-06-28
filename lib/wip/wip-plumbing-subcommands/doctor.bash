# doctor — verify .wip.yaml against disk; report drift. Exit 4 on any drift.
# --fix is advisory in v1 (warns; writes nothing). Pure read otherwise.
# shellcheck shell=bash

wip_plumbing_cmd_doctor() {
  local fix=0
  while [[ $# -gt 0 ]]; do
    case "$1" in
      --fix) fix=1 ;;
      *) wip_die 2 usage "doctor: unknown arg: $1" ;;
    esac
    shift
  done

  local root mj features
  root="$(wip_find_root)" || wip_die 4 no-manifest "no .wip.yaml found from $PWD upward"
  mj="$(wip_manifest_json "$root")" || true
  [[ -n "$mj" ]] || wip_die 4 bad-manifest "could not parse $root/.wip.yaml" ".wip.yaml"
  features="$(wip_features_json "$root" "$mj")"

  local checks="[]" obj fjson reg slug d

  # 1. Feature checks — iterate the resolved feature objects as JSON lines
  #    (no field-splitting; drift may be empty).
  while IFS= read -r fjson; do
    [[ -n "$fjson" ]] || continue
    obj="$(jq -nc --argjson f "$fjson" '
      ($f.drift // "") as $d
      | {kind:"feature", name:$f.name, status:(if $d == "" then "ok" else $d end)}
      + (if $f.sentinel == null then {} else {sentinel:$f.sentinel} end)
      + (if $d == "declared-but-missing"
         then {fix:("install or disable feature " + $f.name + " (sentinel: " + ($f.sentinel // "") + ")")}
         else {} end)')"
    checks="$(jq -nc --argjson a "$checks" --argjson o "$obj" '$a + [$o]')"
  done < <(printf '%s' "$features" | jq -c '.[]')

  # 2. Initiative registry vs disk.
  reg="$(printf '%s' "$mj" | jq -r '.initiatives[]?.slug')"
  while IFS= read -r slug; do
    [[ -n "$slug" ]] || continue
    local status="missing-dir"
    [[ -d "$root/.wip/initiatives/$slug" ]] && status="ok"
    obj="$(jq -nc --arg slug "$slug" --arg status "$status" '{kind:"initiative", slug:$slug, status:$status}')"
    checks="$(jq -nc --argjson a "$checks" --argjson o "$obj" '$a + [$o]')"
  done <<<"$reg"

  if [[ -d "$root/.wip/initiatives" ]]; then
    for d in "$root"/.wip/initiatives/*/; do
      [[ -d "$d" ]] || continue
      slug="$(basename "$d")"
      if ! printf '%s\n' "$reg" | grep -qxF "$slug"; then
        obj="$(jq -nc --arg slug "$slug" '{kind:"initiative", slug:$slug, status:"unregistered"}')"
        checks="$(jq -nc --argjson a "$checks" --argjson o "$obj" '$a + [$o]')"
      fi
    done
  fi

  # 2b. Closeout drift — per roadmap step, the `✅ shipped` marker must agree
  #     with whether its workplan is archived (pure-disk, no git; pairs with the
  #     `ship` writer). marked != archived is drift in one of two directions:
  #       archived ∧ ¬marked → "half-done-closeout"  (the bug ship/next would miss)
  #       marked ∧ ¬archived → "shipped-not-archived" (Step Boundary archive skipped)
  #     Healthy steps (marked == archived) add no entry — keep checks[] quiet.
  #     Scope: in-flight/proposed initiatives only. A `status: shipped`/`archived`
  #     initiative is already closed out at the initiative level; auditing its
  #     historical per-step archive hygiene would be noise (legacy initiatives
  #     predate the ship→archive discipline). Skip them.
  local irec istatus rpath adir cdoc steps step_json sid marked archived
  while IFS= read -r irec; do
    [[ -n "$irec" ]] || continue
    slug="$(jq -r '.slug // ""' <<<"$irec")"
    [[ -n "$slug" ]] || continue
    istatus="$(jq -r '.status // ""' <<<"$irec")"
    [[ "$istatus" == "shipped" || "$istatus" == "archived" ]] && continue
    rpath="$(jq -r '.roadmap // empty' <<<"$irec")"
    [[ -n "$rpath" ]] || rpath=".wip/initiatives/$slug/roadmap.md"
    adir="$root/.wip/initiatives/$slug/archive"
    cdoc="$(wip_roadmap_parse "$root/$rpath")"
    steps="$(jq -c '[.rounds[].steps[] | {id, shipped}]' <<<"$cdoc")"
    while IFS= read -r step_json; do
      [[ -n "$step_json" ]] || continue
      sid="$(jq -r '.id' <<<"$step_json")"
      marked="$(jq -r '.shipped' <<<"$step_json")"
      archived=false
      _wip_archived_workplan_exists "$adir" "$sid" && archived=true
      [[ "$marked" == "$archived" ]] && continue
      if [[ "$archived" == "true" ]]; then
        obj="$(jq -nc --arg slug "$slug" --arg step "$sid" --arg fix "run wip ship $slug $sid" \
          '{kind:"closeout", slug:$slug, step:$step, status:"half-done-closeout", fix:$fix}')"
      else
        obj="$(jq -nc --arg slug "$slug" --arg step "$sid" \
          '{kind:"closeout", slug:$slug, step:$step, status:"shipped-not-archived"}')"
      fi
      checks="$(jq -nc --argjson a "$checks" --argjson o "$obj" '$a + [$o]')"
    done < <(jq -c '.[]' <<<"$steps")
  done < <(printf '%s' "$mj" | jq -c '.initiatives[]?')

  # 3. Root collision: lds and diataxis must not share a root.
  local collide
  collide="$(printf '%s' "$mj" | jq -r '
    (.features.lds.root // .features.lds.installs[0].root // null) as $l
    | (.features.diataxis.root // null) as $d
    | if ($l != null and $d != null and $l == $d
          and (.features.lds.enabled // false) and (.features.diataxis.enabled // false))
      then $l else empty end')"
  if [[ -n "$collide" ]]; then
    obj="$(jq -nc --arg root "$collide" '{kind:"conflict", status:"shared-root", root:$root, message:"lds and diataxis both claim this root"}')"
    checks="$(jq -nc --argjson a "$checks" --argjson o "$obj" '$a + [$o]')"
  fi

  local drift_count
  drift_count="$(printf '%s' "$checks" | jq '[.[] | select(.status != "ok")] | length')"

  if [[ "$fix" == "1" && "$drift_count" -gt 0 ]]; then
    wip_warn "--fix is advisory in v1: no changes written"
  fi

  printf '%s' "$checks" | jq --argjson dc "$drift_count" '{ok: ($dc == 0), checks: ., drift_count: $dc}'
  [[ "$drift_count" -gt 0 ]] && exit 4
  exit 0
}
