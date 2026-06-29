# doctor — verify .wip.yaml against disk; report drift. Exit 4 on any drift.
# --fix is advisory in v1 (warns; writes nothing). Pure read otherwise.
# shellcheck shell=bash

wip_plumbing_cmd_doctor() {
  local fix=0 probe_solo=0 probe_linear=0
  while [[ $# -gt 0 ]]; do
    case "$1" in
      --fix) fix=1 ;;
      --probe-solo) probe_solo=1 ;;
      --probe-linear) probe_linear=1 ;;
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

  # 2d. Tracker mapping mirror drift (ADR-0019 §C). The roadmap's `[tracker: ID]`
  #     keys are the source of truth; `.wip.yaml`'s initiative `tracker_map` is a
  #     writer-generated mirror. Disagreement is drift, fixable with
  #     `wip tracker map <slug> --write`. Pure-disk (roadmap + manifest); scoped
  #     to in-flight/proposed initiatives like 2b. Quiet when both are empty.
  local trec tslug tstatus trpath tdoc rmap mmap
  while IFS= read -r trec; do
    [[ -n "$trec" ]] || continue
    tslug="$(jq -r '.slug // ""' <<<"$trec")"
    [[ -n "$tslug" ]] || continue
    tstatus="$(jq -r '.status // ""' <<<"$trec")"
    [[ "$tstatus" == "shipped" || "$tstatus" == "archived" ]] && continue
    trpath="$(jq -r '.roadmap // empty' <<<"$trec")"
    [[ -n "$trpath" ]] || trpath=".wip/initiatives/$tslug/roadmap.md"
    tdoc="$(wip_roadmap_parse "$root/$trpath")"
    rmap="$(_wip_tracker_map_from_roadmap "$tdoc")"
    mmap="$(_wip_tracker_map_from_manifest "$mj" "$tslug")"
    [[ "$rmap" == "{}" && "$mmap" == "{}" ]] && continue
    jq -ne --argjson a "$rmap" --argjson b "$mmap" '$a == $b' >/dev/null && continue
    obj="$(jq -nc --arg slug "$tslug" --arg fix "run wip tracker map $tslug --write" \
      '{kind:"tracker", slug:$slug, status:"tracker-mirror-drift", fix:$fix}')"
    checks="$(jq -nc --argjson a "$checks" --argjson o "$obj" '$a + [$o]')"
  done < <(printf '%s' "$mj" | jq -c '.initiatives[]?')

  # 2c. Ledger drift (opt-in `--probe-solo`). A shipped step must leave no open
  #     `<slug>/step-NN` ledger entry — the invariant documented in
  #     roles/shared.md §Ledger Ownership & Completion and completed at the
  #     coordinator Step Boundary (BDS-14). The ledger lives in the Solo control
  #     plane, not on disk, so unlike 2b this is an opt-in LIVE probe mirroring
  #     `status --probe-solo`; without the flag doctor stays a pure-disk read.
  #     One shell-out per project (open todos, each with a `.tags` array); the
  #     `<slug>/step-NN` tag is matched per shipped step. A probe that can't run
  #     (Solo absent / project unresolved) records an informational
  #     status:"ok" note — never drift, so a down Solo never fails doctor.
  #     WIP_SOLO_TODOS_CMD overrides the list command (test seam).
  if [[ "$probe_solo" == "1" ]] &&
    [[ "$(jq -r '.features.orchestration.backend // ""' <<<"$mj")" == "solo" ]]; then
    local todos_cmd="${WIP_SOLO_TODOS_CMD:-}" proj_id="${SOLO_PROJECT_ID:-}"
    if [[ -z "$todos_cmd" ]] && command -v solo >/dev/null 2>&1; then
      if [[ -z "$proj_id" ]]; then
        proj_id="$(solo projects list --json 2>/dev/null |
          jq -r --arg p "$root" '.data.projects[]? | select(.path == $p) | .id' | head -1)"
      fi
      [[ -n "$proj_id" ]] && todos_cmd="solo todos list --project-id $proj_id --completed false --json"
    fi
    if [[ -n "$todos_cmd" ]]; then
      local todos_json open_tags lrec lslug lstatus lrpath ldoc lsid ltag lcount todos_rc=0
      todos_json="$(bash -c "$todos_cmd" 2>/dev/null)" || todos_rc=$?
      if [[ "$todos_rc" == "0" ]] &&
        open_tags="$(jq -ec '[.data.todos[]? | (.tags // [])]' <<<"$todos_json" 2>/dev/null)"; then
        [[ -n "$open_tags" ]] || open_tags='[]'
        while IFS= read -r lrec; do
          [[ -n "$lrec" ]] || continue
          lslug="$(jq -r '.slug // ""' <<<"$lrec")"
          [[ -n "$lslug" ]] || continue
          lstatus="$(jq -r '.status // ""' <<<"$lrec")"
          [[ "$lstatus" == "shipped" || "$lstatus" == "archived" ]] && continue
          lrpath="$(jq -r '.roadmap // empty' <<<"$lrec")"
          [[ -n "$lrpath" ]] || lrpath=".wip/initiatives/$lslug/roadmap.md"
          ldoc="$(wip_roadmap_parse "$root/$lrpath")"
          while IFS= read -r lsid; do
            [[ -n "$lsid" && "$lsid" != "null" ]] || continue
            ltag="$lslug/$lsid"
            lcount="$(jq --arg t "$ltag" '[.[] | select(index($t))] | length' <<<"$open_tags")"
            if [[ "${lcount:-0}" -gt 0 ]]; then
              obj="$(jq -nc --arg slug "$lslug" --arg step "$lsid" --argjson count "$lcount" \
                --arg fix "complete the open $ltag ledger entries (roles/shared.md §Ledger Ownership & Completion)" \
                '{kind:"ledger", slug:$slug, step:$step, status:"shipped-step-open-ledger", count:$count, fix:$fix}')"
              checks="$(jq -nc --argjson a "$checks" --argjson o "$obj" '$a + [$o]')"
            fi
          done < <(jq -r '[.rounds[].steps[] | select(.shipped == true) | .id] | .[]' <<<"$ldoc")
        done < <(printf '%s' "$mj" | jq -c '.initiatives[]?')
      else
        obj="$(jq -nc '{kind:"ledger", status:"ok", probe:"unavailable",
          message:"solo ledger probe requested but todos could not be fetched or parsed"}')"
        checks="$(jq -nc --argjson a "$checks" --argjson o "$obj" '$a + [$o]')"
      fi
    else
      obj="$(jq -nc '{kind:"ledger", status:"ok", probe:"unavailable",
        message:"solo ledger probe requested but the Solo project could not be resolved"}')"
      checks="$(jq -nc --argjson a "$checks" --argjson o "$obj" '$a + [$o]')"
    fi
  fi

  # 2e. Tracker live drift (opt-in `--probe-linear`). A READ-ONLY live probe of
  #     the issue tracker, mirroring `--probe-solo`/`--probe-forge`: for each
  #     mapped node, compare the tracker's reported state to wip's expected
  #     (cached → provider) state. A concrete mismatch is drift; the tracker not
  #     answering (empty read / no transport wired) is non-actionable — a down
  #     tracker never fails doctor. WIP_LINEAR_READ_CMD is the transport seam
  #     (test/CLI), invoked as `<cmd> <issue>`; without it the MCP path is
  #     agent-side, so plumbing records an informational unavailable note.
  if [[ "$probe_linear" == "1" ]] && [[ "$(_wip_tracker_enabled "$mj")" == "true" ]]; then
    local lbackend lread_cmd lslug lbind b lnode lissue lexpected lactual
    lbackend="$(jq -r '.features["issue-tracker"].backend // ""' <<<"$mj")"
    lread_cmd="$(_wip_tracker_transport_read_cmd "$lbackend")"
    if [[ -z "$lread_cmd" ]]; then
      obj="$(jq -nc '{kind:"tracker-probe", status:"ok", probe:"unavailable",
        message:"linear probe requested but no read transport is wired (MCP path is agent-side, not a plumbing shell-out; CLI transport is BDS-23)"}')"
      checks="$(jq -nc --argjson a "$checks" --argjson o "$obj" '$a + [$o]')"
    else
      while IFS= read -r lslug; do
        [[ -n "$lslug" ]] || continue
        lbind="$(_wip_tracker_bind_plan "$root" "$mj" "$lslug")"
        while IFS= read -r b; do
          [[ -n "$b" ]] || continue
          lnode="$(jq -r '.node' <<<"$b")"
          lissue="$(jq -r '.issue' <<<"$b")"
          lexpected="$(jq -r '.target_state // ""' <<<"$b")"
          [[ -n "$lexpected" ]] || continue # no cached state to compare
          lactual="$(bash -c "$lread_cmd $lissue" 2>/dev/null || true)"
          [[ -n "$lactual" ]] || continue # tracker didn't answer -> non-actionable
          [[ "$lactual" == "$lexpected" ]] && continue
          obj="$(jq -nc --arg slug "$lslug" --arg node "$lnode" --arg issue "$lissue" \
            --arg expected "$lexpected" --arg actual "$lactual" \
            '{kind:"tracker-probe", slug:$slug, node:$node, issue:$issue, status:"tracker-state-drift", expected:$expected, actual:$actual}')"
          checks="$(jq -nc --argjson a "$checks" --argjson o "$obj" '$a + [$o]')"
        done < <(jq -c '.[]' <<<"$lbind")
      done < <(printf '%s' "$mj" | jq -r '.initiatives[]?.slug')
    fi
  fi

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
