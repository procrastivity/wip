# sync — reconcile wip ⇄ issue-tracker, PUSH-FORWARD ONLY (ADR-0019 §6, BRIEF
# §6). Applies wip→tracker transitions that advance the lifecycle; never moves an
# issue backward, never writes wip's truth from the tracker. A tracker found
# ahead of wip is reported for visibility, not mutated. When no write transport
# is wired (the agent/MCP path), sync emits the forward plan as `pending` for the
# agent to apply — plumbing stays pure. Every emitted row (pending and applied)
# carries `min_rank`, the semantic rank of its target state, so the MCP applier
# has a floor to enforce (apply only when the issue's live rank < min_rank) on the
# path where plumbing has no live tracker read. Honors --dry-run.
# shellcheck shell=bash

# wip_plumbing_cmd_sync [services…] [--initiative <slug>] [--dry-run]
wip_plumbing_cmd_sync() {
  local slug="" dry_run="${WIP_DRY_RUN:-0}"
  local -a services=()
  while [[ $# -gt 0 ]]; do
    case "$1" in
      --initiative)
        [[ $# -ge 2 ]] || wip_die 2 usage "sync: --initiative requires an argument"
        slug="$2"
        shift 2
        ;;
      --initiative=*)
        slug="${1#--initiative=}"
        shift
        ;;
      --dry-run)
        dry_run=1
        shift
        ;;
      -*) wip_die 2 usage "sync: unknown flag: $1" ;;
      *)
        services+=("$1")
        shift
        ;;
    esac
  done

  local root mj init_record backend
  root="$(wip_find_root)" || wip_die 4 no-manifest "no .wip.yaml found from $PWD upward"
  mj="$(wip_manifest_json "$root")"
  [[ -n "$mj" ]] || wip_die 4 bad-manifest "could not parse $root/.wip.yaml" ".wip.yaml"
  if [[ -z "$slug" ]]; then
    slug="$(jq -r '.current_initiative // ""' <<<"$mj")"
    [[ -n "$slug" ]] || wip_die 3 no-initiative "sync: no current_initiative; pass --initiative <slug>"
  fi
  init_record="$(jq -c --arg s "$slug" '[.initiatives[]? | select(.slug == $s)] | (.[0] // null)' <<<"$mj")"
  [[ "$init_record" != "null" ]] ||
    wip_die 3 unknown-initiative "sync: initiative not in manifest: $slug"
  backend="$(jq -r '.features["issue-tracker"].backend // ""' <<<"$mj")"

  # `services` selects which transports to reconcile; default = the configured
  # backend. A service other than the backend is a no-op here (forward-compat:
  # `sync solo linear` lists both, only the tracker is reconciled in this verb).
  if [[ ${#services[@]} -gt 0 ]]; then
    local want=0 s
    for s in "${services[@]}"; do
      # A requested service reconciles iff it names the configured backend
      # (github/gitlab/linear). The old `s == linear && backend == linear`
      # special-case was subsumed by this generic match (ADR-0026) and removed.
      if [[ "$s" == "$backend" ]]; then
        want=1
      fi
    done
    [[ "$want" == "1" ]] || {
      jq -nc --arg slug "$slug" --arg backend "$backend" \
        '{ok:true, initiative:$slug, backend:(if $backend=="" then null else $backend end),
          transport:"none", applied:[], pending:[], skipped:[], observed:[],
          note:"no requested service matched the issue-tracker backend"}'
      return 0
    }
  fi

  local roadmap_path doc rmap mmap
  roadmap_path="$(jq -r '.roadmap // empty' <<<"$init_record")"
  [[ -n "$roadmap_path" ]] || roadmap_path=".wip/initiatives/$slug/roadmap.md"
  doc="$(wip_roadmap_parse "$root/$roadmap_path")"
  rmap="$(_wip_tracker_map_from_roadmap "$doc")"
  mmap="$(_wip_tracker_map_from_manifest "$mj" "$slug")"
  if ! jq -ne --argjson a "$rmap" --argjson b "$mmap" '$a == $b' >/dev/null; then
    wip_die 4 tracker-mirror-drift "sync: tracker mirror drift; run wip tracker map $slug --write" "$roadmap_path"
  fi

  local write_cmd read_cmd bind transport="mcp"
  write_cmd="$(_wip_tracker_transport_write_cmd "$backend")"
  read_cmd="$(_wip_tracker_transport_read_cmd "$backend")"
  [[ -n "$write_cmd" ]] && transport="cli"
  bind="$(_wip_tracker_bind_plan "$root" "$mj" "$slug")"

  local applied="[]" pending="[]" skipped="[]" observed="[]"
  local b node issue sem target wip_rank tracker_sem="" tracker_rank=-1 actual
  while IFS= read -r b; do
    [[ -n "$b" ]] || continue
    node="$(jq -r '.node' <<<"$b")"
    issue="$(jq -r '.issue' <<<"$b")"
    sem="$(jq -r '.semantic_state // ""' <<<"$b")"
    target="$(jq -r '.target_state // ""' <<<"$b")"
    if [[ -z "$sem" || -z "$target" ]]; then
      skipped="$(jq -nc --argjson a "$skipped" --arg n "$node" --arg i "$issue" \
        '$a + [{node:$n, issue:$i, reason:"no wip state"}]')"
      continue
    fi
    wip_rank="$(_wip_tracker_semantic_rank "$sem")"

    # Read the tracker's current state (visibility + backward guard). Absent read
    # transport ⇒ unknown (rank -1): we still emit the forward plan as pending,
    # but the emitted row now carries `min_rank` (below) — a floor the MCP applier
    # enforces via a live re-read, since plumbing cannot guard backward moves here.
    tracker_sem=""
    tracker_rank=-1
    if [[ -n "$read_cmd" ]]; then
      actual="$(bash -c "$read_cmd \"\$1\"" _ "$issue" 2>/dev/null || true)"
      if [[ -n "$actual" ]]; then
        tracker_sem="$(_wip_tracker_provider_to_semantic "$backend" "$actual")"
        tracker_rank="$(_wip_tracker_semantic_rank "$tracker_sem")"
      fi
    fi

    # Already in sync — nothing to do.
    if [[ -n "$tracker_sem" && "$tracker_sem" == "$sem" ]]; then
      skipped="$(jq -nc --argjson a "$skipped" --arg n "$node" --arg i "$issue" \
        '$a + [{node:$n, issue:$i, reason:"in sync"}]')"
      continue
    fi
    # Tracker is AHEAD of wip — pull for visibility ONLY; never move it backward,
    # never overwrite wip's truth.
    if [[ "$tracker_rank" -gt "$wip_rank" ]]; then
      observed="$(jq -nc --argjson a "$observed" --arg n "$node" --arg i "$issue" --arg t "$tracker_sem" \
        '$a + [{node:$n, issue:$i, tracker_state:$t}]')"
      continue
    fi

    # Forward transition (wip ahead of, or unknown to, the tracker). Apply when a
    # write transport is wired and not --dry-run; otherwise it is pending for the
    # agent/MCP path to apply.
    # min_rank = the semantic rank of the target state (the in-scope wip_rank).
    # Both pending and applied rows carry it uniformly: it is the applier's floor
    # on the MCP path and merely informational on rows the CLI guard already
    # cleared.
    local row
    row="$(jq -nc --arg n "$node" --arg i "$issue" --arg to "$target" --argjson mr "$wip_rank" \
      '{node:$n, issue:$i, to:$to, min_rank:$mr}')"
    if [[ -n "$write_cmd" && "$dry_run" != "1" ]]; then
      if bash -c "$write_cmd \"\$1\" \"\$2\"" _ "$issue" "$target" >/dev/null 2>&1; then
        applied="$(jq -nc --argjson a "$applied" --argjson r "$row" '$a + [$r]')"
      else
        skipped="$(jq -nc --argjson a "$skipped" --arg n "$node" --arg i "$issue" \
          '$a + [{node:$n, issue:$i, reason:"write failed"}]')"
      fi
    else
      pending="$(jq -nc --argjson a "$pending" --argjson r "$row" '$a + [$r]')"
    fi
  done < <(jq -c '.[]' <<<"$bind")

  jq -nc \
    --arg slug "$slug" --arg backend "$backend" --arg transport "$transport" \
    --argjson applied "$applied" --argjson pending "$pending" \
    --argjson skipped "$skipped" --argjson observed "$observed" \
    --arg dry "$dry_run" '
    { ok: true, initiative: $slug,
      backend: (if $backend == "" then null else $backend end),
      transport: $transport,
      applied: $applied, pending: $pending, skipped: $skipped, observed: $observed }
    + (if $dry == "1" then { dry_run: true } else {} end)'
}
