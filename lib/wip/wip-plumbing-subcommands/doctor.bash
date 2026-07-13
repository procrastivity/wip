# doctor — verify .wip.yaml against disk; report drift. Exit 4 on any drift.
# --fix is advisory in v1 (warns; writes nothing). Pure read otherwise.
# shellcheck shell=bash

# _wip_doctor_tracker_closed <root> <tracker-id> — return 0 iff the tracker cache
# KNOWS this issue to be closed. Used by §2o.
#
# The cache keyspace is `issue:<TRACKER-ID>` (workplan step-06 D3) — deliberately
# NOT the bare `<slug>/<node-id>` keyspace every other cache entry uses, which
# holds wip's own lifecycle labels for wip nodes, not a tracker's native status
# for an arbitrary issue. A github/gitlab id can itself contain a `/`
# (`owner/repo#123`, ADR-0026), so "has a slash" cannot tell the two apart; the
# prefix can.
#
# ABSENT ⇒ NOT CLOSED (return 1). This is the fail-open rule (D4), not an
# oversight: with no entry we have no data, and the whole check family — §2f,
# `--probe-tracker` — treats "can't tell" as non-actionable. Guessing closed here
# would make doctor demand the pruning of a live backlog item, which is the exact
# plausible-wrong implementation this reads the cache to avoid. Nothing populates
# `issue:*` entries yet (out of scope for step-06), so today this returns 1 for
# every tracker in the live repo, and §2o is correctly silent.
_wip_doctor_tracker_closed() {
  local entry state
  entry="$(_wip_tracker_cache_get "$1" "issue:$2")"
  [[ -n "$entry" && "$entry" != "null" ]] || return 1
  state="$(jq -r '.state // ""' <<<"$entry" | tr '[:upper:]' '[:lower:]')"
  case "$state" in
    done | canceled | cancelled) return 0 ;;
    *) return 1 ;;
  esac
}

wip_plumbing_cmd_doctor() {
  local fix=0 probe_solo=0 probe_tracker=0
  while [[ $# -gt 0 ]]; do
    case "$1" in
      --fix) fix=1 ;;
      --probe-solo) probe_solo=1 ;;
      # `--probe-tracker` is the canonical, backend-neutral flag (ADR-0026);
      # `--probe-linear` is the retained deprecated alias.
      --probe-tracker | --probe-linear) probe_tracker=1 ;;
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

  # 2j. Round-level closeout drift — the round heading's `✅ shipped` marker must
  #     agree with its steps: every step in the round shipped ∧ heading unmarked is
  #     drift the round-marker writer (`ship`, when the round's last step lands)
  #     would otherwise have written. Pairs with that writer the same way §2b pairs
  #     with the step-level one. Pure-disk, and the same scope guard as §2b: a
  #     `status: shipped`/`archived` initiative is closed out at the initiative
  #     level, so auditing its historical round hygiene would be noise.
  #     The `length > 0` guard is load-bearing, not defensive: jq's `all` is
  #     vacuously true on an empty array, so without it an empty/placeholder round
  #     would read as "fully shipped" and false-positive. Lane steps need no special
  #     handling — the parser lands them in `.rounds[].steps[]` like any other step.
  #     Healthy rounds add no entry — keep checks[] quiet.
  local rrec rslug rstatus rrpath rdoc round_json rn rlast
  while IFS= read -r rrec; do
    [[ -n "$rrec" ]] || continue
    rslug="$(jq -r '.slug // ""' <<<"$rrec")"
    [[ -n "$rslug" ]] || continue
    rstatus="$(jq -r '.status // ""' <<<"$rrec")"
    [[ "$rstatus" == "shipped" || "$rstatus" == "archived" ]] && continue
    rrpath="$(jq -r '.roadmap // empty' <<<"$rrec")"
    [[ -n "$rrpath" ]] || rrpath=".wip/initiatives/$rslug/roadmap.md"
    rdoc="$(wip_roadmap_parse "$root/$rrpath")"
    while IFS= read -r round_json; do
      [[ -n "$round_json" ]] || continue
      rn="$(jq -r '.n' <<<"$round_json")"
      rlast="$(jq -r '.steps[-1].id' <<<"$round_json")"
      obj="$(jq -nc --arg slug "$rslug" --argjson round "$rn" \
        --arg fix "run wip ship $rslug $rlast (or re-run it) to write the round marker" \
        '{kind:"closeout-round", slug:$slug, round:$round, status:"round-not-marked-shipped", fix:$fix}')"
      checks="$(jq -nc --argjson a "$checks" --argjson o "$obj" '$a + [$o]')"
    done < <(jq -c '.rounds[]
      | select(.steps | length > 0 and all(.shipped))
      | select(.shipped == false)' <<<"$rdoc")
  done < <(printf '%s' "$mj" | jq -c '.initiatives[]?')

  # 2k. Stale `current_initiative` pointer — the top-level pointer names an
  #     initiative whose `status` is `shipped`/`archived`. Unlike every other check
  #     in this file this is a ONE-SHOT read of a single scalar, not a loop over
  #     `.initiatives[]?`: there is exactly one pointer. It is also the one check
  #     with NO shipped/archived skip-guard — a shipped initiative being pointed at
  #     is the entire subject of the check, not noise to skip past.
  #     `closeout` cannot fix this by being re-run on the initiative the pointer
  #     already names (that initiative is shipped; closeout would repoint only if
  #     the pointer were aimed at something it is closing). The fix is a manual
  #     repoint, or running `closeout` on whatever IS in flight so the pointer
  #     resolves naturally. Actionable drift, not an informational note.
  local ci_slug ci_status
  ci_slug="$(printf '%s' "$mj" | jq -r '.current_initiative // ""')"
  if [[ -n "$ci_slug" && "$ci_slug" != "null" ]]; then
    ci_status="$(jq -r --arg s "$ci_slug" '
      [.initiatives[]? | select(.slug == $s)] | (.[0].status // "")' <<<"$mj")"
    if [[ "$ci_status" == "shipped" || "$ci_status" == "archived" ]]; then
      obj="$(jq -nc --arg slug "$ci_slug" --arg istatus "$ci_status" \
        --arg fix "run wip-plumbing closeout <the in-flight initiative>, or point current_initiative at an in-flight initiative by hand" \
        '{kind:"current-initiative", slug:$slug, status:"current-initiative-shipped",
          initiative_status:$istatus, fix:$fix}')"
      checks="$(jq -nc --argjson a "$checks" --argjson o "$obj" '$a + [$o]')"
    fi
  fi

  # 2l. An initiative whose every non-empty round is shipped but whose manifest
  #     `status` is still `in-flight` — the initiative-level closeout `closeout`
  #     (ADR-0016, step-04) now writes. This check INVERTS the §2b/§2j skip-guard:
  #     it considers ONLY `status: in-flight`. `shipped`/`archived` are already
  #     closed out (the whole point); `proposed` with a fully-written-out roadmap is
  #     a degenerate edge case not worth flagging; `paused` is intentionally
  #     not-progressing, not a closeout omission.
  #     The predicate is the SAME one `closeout`'s own refuse-unless-all-shipped
  #     guard uses — `length > 0 and all(.shipped)` over the non-empty rounds — so
  #     this check predicts exactly when `closeout <slug>` would succeed. `length > 0`
  #     is load-bearing for the same reason it is there: jq's `all` is vacuously true
  #     over an empty array, so a roadmap with zero rounds (or only empty ones) must
  #     not read as "fully shipped". Healthy initiatives add no entry.
  local crec cslug cstatus crpath cdoc call_shipped
  while IFS= read -r crec; do
    [[ -n "$crec" ]] || continue
    cslug="$(jq -r '.slug // ""' <<<"$crec")"
    [[ -n "$cslug" ]] || continue
    cstatus="$(jq -r '.status // ""' <<<"$crec")"
    [[ "$cstatus" == "in-flight" ]] || continue
    crpath="$(jq -r '.roadmap // empty' <<<"$crec")"
    [[ -n "$crpath" ]] || crpath=".wip/initiatives/$cslug/roadmap.md"
    cdoc="$(wip_roadmap_parse "$root/$crpath")"
    call_shipped="$(jq -r '
      [.rounds[] | select((.steps | length) > 0)] | length > 0 and all(.shipped)' <<<"$cdoc")"
    [[ "$call_shipped" == "true" ]] || continue
    obj="$(jq -nc --arg slug "$cslug" --arg fix "run wip-plumbing closeout $cslug" \
      '{kind:"closeout-initiative", slug:$slug, status:"initiative-shipped-not-closed", fix:$fix}')"
    checks="$(jq -nc --argjson a "$checks" --argjson o "$obj" '$a + [$o]')"
  done < <(printf '%s' "$mj" | jq -c '.initiatives[]?')

  # 2m/2n. `gitignore.always_commit` policy drift (step-05). The manifest DECLARES
  #     which files under an otherwise-ignored `.wip/` stay tracked; `.gitignore`
  #     and the git index are what make (or fail to make) that declaration true.
  #     Two directions, deliberately TWO `kind`s rather than one merged check —
  #     they have different fixes, different iteration shapes, and keeping them
  #     apart is what makes each independently pin-able (a check that fires in only
  #     one direction, or one over-eager predicate answering for both, is otherwise
  #     invisible from the outside):
  #       §2m `gitignore-declared-but-ignored`   — declared, yet still ignored.
  #       §2n `gitignore-tracked-but-undeclared` — tracked under `.wip/`, yet not
  #                                                declared.
  #     Both are ACTIONABLE drift, not the informational status:"ok" pattern §2f/§2h
  #     use, for the same reason §2k/§2l are: each has a deterministic fix
  #     (`gitignore sync` for §2m; a manifest edit or `git rm --cached` for §2n).
  #     Unlike `--probe-solo`/`--probe-tracker` these are NOT behind an opt-in flag:
  #     `check-ignore`/`ls-files` are local, offline and fast — there is no live
  #     service to be down, so nothing to make opt-in.
  #     Both share ONE availability guard (`rev-parse --is-inside-work-tree`): with
  #     no git, or a root that is not a worktree, neither direction is knowable, so
  #     emit a SINGLE combined informational note (kind:"gitignore" — the family
  #     name; the two drift kinds are its specializations) and skip both loops. A
  #     missing git never fails doctor, and never false-positives either.
  local gi_declared gi_path gi_tracked gi_fix
  gi_declared="$(printf '%s' "$mj" | jq -c '.gitignore.always_commit // []')"
  if ! git -C "$root" rev-parse --is-inside-work-tree >/dev/null 2>&1; then
    obj="$(jq -nc '{kind:"gitignore", status:"ok", probe:"unavailable",
      message:"gitignore always_commit checks skipped: git is unavailable, or the wip root is not a git worktree"}')"
    checks="$(jq -nc --argjson a "$checks" --argjson o "$obj" '$a + [$o]')"
  else
    # §2m — every declared path that git still ignores. `check-ignore -q` exits 0
    #       when a path IS matched by an ignore rule (drift) and 1 when it is not
    #       (healthy). Path existence on disk is irrelevant: this is pattern
    #       matching, so a declared-but-not-yet-created file is checked too.
    #       Note (`--no-index` is deliberately NOT passed): check-ignore consults the
    #       index, so an ALREADY-TRACKED declared path reports not-ignored whatever
    #       `.gitignore` says. That is the right answer, not a blind spot — an ignore
    #       rule never untracks an indexed file, so a tracked declared file already
    #       keeps the policy's promise ("stays tracked"). §2m is about the declared
    #       file that is ignored AND therefore un-addable, which is the live repo's
    #       state today.
    while IFS= read -r gi_path; do
      [[ -n "$gi_path" ]] || continue
      git -C "$root" check-ignore -q -- "$gi_path" || continue
      obj="$(jq -nc --arg path "$gi_path" --arg fix "run wip-plumbing gitignore sync" \
        '{kind:"gitignore-declared", path:$path, status:"gitignore-declared-but-ignored", fix:$fix}')"
      checks="$(jq -nc --argjson a "$checks" --argjson o "$obj" '$a + [$o]')"
    done < <(jq -r '.[]' <<<"$gi_declared")

    # §2n — every path git TRACKS under `.wip/` that the manifest never declared.
    #       Reads the INDEX (`ls-files`), not the worktree: ignore rules never
    #       retroactively untrack an already-indexed file, which is exactly how a
    #       stray file gets tracked under a blanket-ignored directory and stays
    #       that way unnoticed. Exact string match against the declared list.
    while IFS= read -r gi_tracked; do
      [[ -n "$gi_tracked" ]] || continue
      jq -e --arg p "$gi_tracked" 'index($p) != null' <<<"$gi_declared" >/dev/null && continue
      gi_fix="either add $gi_tracked to gitignore.always_commit in .wip.yaml, or git rm --cached $gi_tracked if it was tracked in error"
      obj="$(jq -nc --arg path "$gi_tracked" --arg fix "$gi_fix" \
        '{kind:"gitignore-tracked", path:$path, status:"gitignore-tracked-but-undeclared", fix:$fix}')"
      checks="$(jq -nc --argjson a "$checks" --argjson o "$obj" '$a + [$o]')"
    done < <(git -C "$root" ls-files -- .wip/ 2>/dev/null || true)
  fi

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

  # 2e. Tracker live drift (opt-in `--probe-tracker`, alias `--probe-linear`). A
  #     READ-ONLY live probe of the issue tracker, mirroring
  #     `--probe-solo`/`--probe-forge`: for each mapped node, compare the tracker's
  #     reported state to wip's expected (cached → provider) state. A concrete
  #     mismatch is drift; the tracker not answering (empty read / no transport
  #     wired) is non-actionable — a down tracker never fails doctor. The read
  #     transport is resolved by the backend dispatcher (ADR-0026): a github/gitlab
  #     backend's adapter, or the WIP_TRACKER_READ_CMD / WIP_LINEAR_READ_CMD seams,
  #     invoked as `<cmd> <issue>`; without one (Linear's agent-side MCP path)
  #     plumbing records an informational unavailable note.
  if [[ "$probe_tracker" == "1" ]] && [[ "$(_wip_tracker_enabled "$mj")" == "true" ]]; then
    local lbackend lread_cmd lslug lbind b lnode lissue lexpected lactual
    lbackend="$(jq -r '.features["issue-tracker"].backend // ""' <<<"$mj")"
    lread_cmd="$(_wip_tracker_transport_read_cmd "$lbackend")"
    if [[ -z "$lread_cmd" ]]; then
      obj="$(jq -nc '{kind:"tracker-probe", status:"ok", probe:"unavailable",
        message:"tracker probe requested but no read transport is wired (Linear MCP path is agent-side, not a plumbing shell-out; a github/gitlab adapter or the WIP_TRACKER_READ_CMD seam supplies one)"}')"
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
          lactual="$(bash -c "$lread_cmd \"\$1\"" _ "$lissue" 2>/dev/null || true)"
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

  # 2f. Unfiled tracker items (BRIEF §7) — deferred / backlog entries with no
  #     `[tracker: ID]` mapping, surfaced as an INFORMATIONAL suggestion to file
  #     them (never auto-filed, never drift: status stays "ok" so doctor does not
  #     fail). Only when issue-tracker is enabled and an in-flight initiative has
  #     unfiled items.
  if [[ "$(_wip_tracker_enabled "$mj")" == "true" ]]; then
    local urec uslug ustatus urpath udoc uitems ucount
    while IFS= read -r urec; do
      [[ -n "$urec" ]] || continue
      uslug="$(jq -r '.slug // ""' <<<"$urec")"
      [[ -n "$uslug" ]] || continue
      ustatus="$(jq -r '.status // ""' <<<"$urec")"
      [[ "$ustatus" == "shipped" || "$ustatus" == "archived" ]] && continue
      urpath="$(jq -r '.roadmap // empty' <<<"$urec")"
      [[ -n "$urpath" ]] || urpath=".wip/initiatives/$uslug/roadmap.md"
      udoc="$(wip_roadmap_parse "$root/$urpath")"
      uitems="$(jq -c '
        [ (.deferred[]? | . + {source:"deferred"}), (.backlog[]? | . + {source:"backlog"}) ]
        | map(select(.tracker == null) | {id, title, source})' <<<"$udoc")"
      ucount="$(jq 'length' <<<"$uitems")"
      [[ "$ucount" -gt 0 ]] || continue
      obj="$(jq -nc --arg slug "$uslug" --argjson items "$uitems" --argjson count "$ucount" \
        '{kind:"tracker-unfiled", slug:$slug, status:"ok", count:$count, items:$items,
          message:"deferred/backlog items not filed as tracker issues (suggestion; never auto-filed)"}')"
      checks="$(jq -nc --argjson a "$checks" --argjson o "$obj" '$a + [$o]')"
    done < <(printf '%s' "$mj" | jq -c '.initiatives[]?')
  fi

  # 2h. Missing tracker anchor (ADR-0024 / D7). An initiative captured before the
  #     anchor field existed — or via an intake that ran without `--anchor` — has
  #     no durable initiative→source-issue link (`.wip.yaml tracker_anchor`).
  #     Surface it as an INFORMATIONAL suggestion, modeled on §2f (tracker-unfiled),
  #     NOT §2d (mirror-drift): status stays "ok", so doctor never fails / exits 4
  #     just because a pre-existing initiative predates the anchor. Retrofitting
  #     anchors fleet-wide must not turn every anchor-less repo red on the day the
  #     feature lands; harden to real drift in a follow-on once anchors are
  #     backfilled (Open Q3). Scoped to enabled + in-flight/proposed initiatives,
  #     mirroring every other doctor tracker check.
  if [[ "$(_wip_tracker_enabled "$mj")" == "true" ]]; then
    local arec aslug astatus aanchor
    while IFS= read -r arec; do
      [[ -n "$arec" ]] || continue
      aslug="$(jq -r '.slug // ""' <<<"$arec")"
      [[ -n "$aslug" ]] || continue
      astatus="$(jq -r '.status // ""' <<<"$arec")"
      [[ "$astatus" == "shipped" || "$astatus" == "archived" ]] && continue
      aanchor="$(jq -r '.tracker_anchor // ""' <<<"$arec")"
      [[ -n "$aanchor" ]] && continue
      obj="$(jq -nc --arg slug "$aslug" \
        --arg fix "re-run intake with --anchor <ID>, or add a tracker-anchor: <ID> BRIEF front-matter key and wip init --tracker-anchor" \
        '{kind:"tracker-anchor", slug:$slug, status:"ok",
          message:"initiative has no tracker_anchor (durable source-issue link); suggestion, never auto-set",
          fix:$fix}')"
      checks="$(jq -nc --argjson a "$checks" --argjson o "$obj" '$a + [$o]')"
    done < <(printf '%s' "$mj" | jq -c '.initiatives[]?')
  fi

  # 2o. Backlog entries whose tracker issue is already CLOSED (step-06, D3-D5) —
  #     the read half of "retire shipped backlog items instead of re-nominating
  #     them". `ship`/`closeout`/`backlog retire` prune an entry when they ship the
  #     step that carries its tracker; this catches the entry nobody pruned, whose
  #     issue the tracker itself reports done/canceled. ACTIONABLE drift (status is
  #     not "ok"), so it counts toward drift_count and trips exit 4 — unlike §2f/§2h
  #     there is a deterministic fix: prune the entry.
  #     Gated on `_wip_tracker_enabled` like §2f/§2h, and DEFAULT-ON, not behind a
  #     `--probe-*` flag: those flags exist to gate live shell-outs, and this makes
  #     none. It reads `.wip/tracker-cache.json` off disk and nothing else. Cache
  #     staleness is accepted (D3) — the cache has no TTL anywhere in wip.
  #     Two sweeps, ONE kind (`backlog`), separated by `source` — the two files are
  #     different grammars (see wip-plumbing-repo-backlog-lib.bash's header) but the
  #     same defect, and a reader fixing them does the same thing to each:
  #       source:"repo"    — `.wip/backlog.md`, multi-paragraph, parsed by the
  #                          step-06 lib (wip_roadmap_parse sees nothing in it).
  #       source:"roadmap" — an initiative roadmap's own `## Backlog` one-liners,
  #                          already `.tracker`-tagged by the roadmap parser today.
  #     Same in-flight/proposed scope guard as every other per-initiative check.
  if [[ "$(_wip_tracker_enabled "$mj")" == "true" ]]; then
    local bk_entry bk_trk bk_id bk_title
    local brec bslug bstatus brpath bdoc

    while IFS= read -r bk_entry; do
      [[ -n "$bk_entry" ]] || continue
      bk_trk="$(jq -r '.tracker // ""' <<<"$bk_entry")"
      [[ -n "$bk_trk" ]] || continue
      _wip_doctor_tracker_closed "$root" "$bk_trk" || continue
      bk_id="$(jq -r '.id' <<<"$bk_entry")"
      bk_title="$(jq -r '.title' <<<"$bk_entry")"
      obj="$(jq -nc --arg id "$bk_id" --arg title "$bk_title" --arg trk "$bk_trk" \
        --arg fix "prune $bk_id (tracker $bk_trk)" \
        '{kind:"backlog", source:"repo", id:$id, tracker:$trk, title:$title,
          status:"backlog-tracker-closed", fix:$fix}')"
      checks="$(jq -nc --argjson a "$checks" --argjson o "$obj" '$a + [$o]')"
    done < <(_wip_repo_backlog_parse "$root/.wip/backlog.md" | jq -c '.[]')

    while IFS= read -r brec; do
      [[ -n "$brec" ]] || continue
      bslug="$(jq -r '.slug // ""' <<<"$brec")"
      [[ -n "$bslug" ]] || continue
      bstatus="$(jq -r '.status // ""' <<<"$brec")"
      [[ "$bstatus" == "shipped" || "$bstatus" == "archived" ]] && continue
      brpath="$(jq -r '.roadmap // empty' <<<"$brec")"
      [[ -n "$brpath" ]] || brpath=".wip/initiatives/$bslug/roadmap.md"
      bdoc="$(wip_roadmap_parse "$root/$brpath")"
      while IFS= read -r bk_entry; do
        [[ -n "$bk_entry" ]] || continue
        bk_trk="$(jq -r '.tracker // ""' <<<"$bk_entry")"
        [[ -n "$bk_trk" ]] || continue
        _wip_doctor_tracker_closed "$root" "$bk_trk" || continue
        bk_id="$(jq -r '.id' <<<"$bk_entry")"
        bk_title="$(jq -r '.title' <<<"$bk_entry")"
        obj="$(jq -nc --arg slug "$bslug" --arg id "$bk_id" --arg title "$bk_title" \
          --arg trk "$bk_trk" --arg fix "prune $bk_id (tracker $bk_trk)" \
          '{kind:"backlog", source:"roadmap", slug:$slug, id:$id, tracker:$trk, title:$title,
            status:"backlog-tracker-closed", fix:$fix}')"
        checks="$(jq -nc --argjson a "$checks" --argjson o "$obj" '$a + [$o]')"
      done < <(jq -c '.backlog[]?' <<<"$bdoc")
    done < <(printf '%s' "$mj" | jq -c '.initiatives[]?')
  fi

  # 2g. Orchestration legacy-footprint (pure-disk; ADR-0020 migration path / D8).
  #     Detect the OLD plugin-tree `setup agents` footprint a pre-flatten install
  #     left at $root (the 16-file write set: root `.claude-plugin/*`, `agents/*`,
  #     `commands/*`). Reuse the setup-family classifier VERBATIM — the `--migrate`
  #     actor and this detector share one oracle, no duplication — by sourcing its
  #     subcommand file the way intake.bash pulls in its siblings. This is a
  #     pure-disk existence + ownership-signal scan (does root plugin.json read
  #     `name==wip`? do `agents/<role>.md` carry `name: wip-<role>`? …); it does
  #     NOT re-derive the backend or re-render agents — the deferred render fan-in
  #     (ADR-0015 Q-05.4) stays deferred. Gate on ≥1 `owned` line: a foreign-only
  #     (host plugin.json, F1) or stray-only (`roles/`/`active.md`) footprint is
  #     NOT a wip footprint → stay quiet (D5; matches the plugin/fresh-repo case).
  #     When owned files are present it is real, actionable, F1-risky drift → exit
  #     4 with a `fix` steering to the safe, tested actor (doctor never deletes).
  local lf_td lf_owned=() lf_class lf_rel lf_reason lf_paths
  lf_td="$(wip_templates_dir 2>/dev/null || printf '')"
  if [[ -n "$lf_td" && -d "$lf_td" ]]; then
    # shellcheck source=lib/wip/wip-plumbing-subcommands/setup.bash
    source "$WIP_LIB/wip-plumbing-subcommands/setup.bash"
    # shellcheck disable=SC2034  # lf_reason: unused third TSV field (class<TAB>relpath<TAB>reason)
    while IFS=$'\t' read -r lf_class lf_rel lf_reason; do
      [[ "$lf_class" == "owned" ]] || continue
      lf_owned+=("$lf_rel")
    done < <(_wip_setup_agents_legacy_footprint "$root" "$lf_td")
    if [[ "${#lf_owned[@]}" -gt 0 ]]; then
      lf_paths="$(printf '%s\n' "${lf_owned[@]}" | jq -Rc . | jq -sc 'sort')"
      obj="$(jq -nc --argjson paths "$lf_paths" \
        '{kind:"orchestration", status:"legacy-footprint",
          fix:"run wip-plumbing setup agents --migrate", paths:$paths}')"
      checks="$(jq -nc --argjson a "$checks" --argjson o "$obj" '$a + [$o]')"
    fi
  fi

  # 2i. Vendored role/command drift (ADR-0023 D5 — CLOSES ADR-0015 Q-05.4). Gated
  #     STRICTLY on `.features.orchestration.source == "vendored"`: only a vendored
  #     consumer has installed agent/command copies to re-render and compare, so
  #     this repo's own `source: plugin` doctor pays NO render cost and is
  #     unaffected. Runs the SAME two-axis classifier as `setup agents --status` /
  #     `--sync` (one oracle, ADR-0020 D8) and, for each non-`clean` file, appends
  #     a `vendored-drift` check GROUPED BY state (one object per distinct state,
  #     each carrying that state's `fix` + its paths). This IS the render fan-in
  #     backlogged as Q-05.4 — distinct from §2g's pure-disk legacy-footprint scan
  #     (a stale on-disk footprint vs. installed-render drift are orthogonal). A
  #     classifier/render failure is swallowed (no crash, no false drift) — the
  #     blunt `setup agents --check` gate is the place that surfaces a render error.
  if [[ "$(jq -r '.features.orchestration.source // ""' <<<"$mj")" == "vendored" ]]; then
    local vd_td vd_raw vd_rc vd_map vd_state vd_dir vd_path vd_fix vd_paths vd_key
    vd_td="$(wip_templates_dir 2>/dev/null || printf '')"
    if [[ -n "$vd_td" && -d "$vd_td" ]]; then
      # shellcheck source=lib/wip/wip-plumbing-subcommands/setup.bash
      source "$WIP_LIB/wip-plumbing-subcommands/setup.bash"
      set +e
      vd_raw="$(_wip_setup_agents_provenance_classify "$root" "$vd_td")"
      vd_rc=$?
      set -e
      if [[ "$vd_rc" == "0" ]]; then
        # Group by state+direction — for upstream-advanced the DIRECTION (D2a)
        # decides the fix (ahead→sync, behind→upgrade, indeterminate→--sync --force
        # override), so a mixed install emits one check per distinct (state,dir).
        vd_map="{}"
        while IFS=$'\t' read -r vd_state vd_path vd_dir; do
          [[ -n "$vd_path" && "$vd_state" != "clean" ]] || continue
          vd_key="$vd_state|$vd_dir"
          vd_map="$(jq -c --arg k "$vd_key" --arg p "$vd_path" '.[$k] += [$p]' <<<"$vd_map")"
        done <<<"$vd_raw"
        while IFS= read -r vd_key; do
          [[ -n "$vd_key" ]] || continue
          vd_state="${vd_key%%|*}"
          vd_dir="${vd_key##*|}"
          case "$vd_state" in
            locally-modified | both-diverged) vd_fix="setup agents --sync --force" ;;
            upstream-advanced)
              case "$vd_dir" in
                ahead) vd_fix="setup agents --sync" ;;
                behind) vd_fix="upgrade the plugin" ;;
                *) vd_fix="setup agents --sync --force" ;; # indeterminate
              esac
              ;;
            *) vd_fix="setup agents --sync" ;; # unstamped, missing
          esac
          vd_paths="$(jq -c --arg k "$vd_key" '.[$k] | sort' <<<"$vd_map")"
          obj="$(jq -nc --arg state "$vd_state" --arg dir "$vd_dir" --arg fix "$vd_fix" --argjson paths "$vd_paths" \
            '{kind:"orchestration", status:"vendored-drift", state:$state, direction:$dir, fix:$fix, paths:$paths}')"
          checks="$(jq -nc --argjson a "$checks" --argjson o "$obj" '$a + [$o]')"
        done < <(jq -r 'keys[]' <<<"$vd_map")
      fi
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
