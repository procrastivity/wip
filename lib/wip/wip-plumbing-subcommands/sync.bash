# sync — reconcile wip ⇄ issue-tracker, PUSH-FORWARD ONLY (ADR-0019 §6, BRIEF
# §6). Applies wip→tracker transitions that advance the lifecycle; never moves an
# issue backward, never writes wip's truth from the tracker. A tracker found
# ahead of wip is reported for visibility, not mutated. When no write transport
# is wired (the agent/MCP path), sync emits the forward plan as `pending` for the
# agent to apply — plumbing stays pure. Honors --dry-run.
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
    for s in "${services[@]}"; do [[ "$s" == "$backend" || "$s" == "linear" ]] && want=1; done
    [[ "$want" == "1" ]] || {
      jq -nc --arg slug "$slug" --arg backend "$backend" \
        '{ok:true, initiative:$slug, backend:(if $backend=="" then null else $backend end),
          transport:"none", applied:[], pending:[], skipped:[], observed:[],
          note:"no requested service matched the issue-tracker backend"}'
      return 0
    }
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
    # transport ⇒ unknown (rank -1): we still emit the forward plan as pending.
    tracker_sem=""
    tracker_rank=-1
    if [[ -n "$read_cmd" ]]; then
      actual="$(bash -c "$read_cmd $issue" 2>/dev/null || true)"
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
    local row
    row="$(jq -nc --arg n "$node" --arg i "$issue" --arg to "$target" '{node:$n, issue:$i, to:$to}')"
    if [[ -n "$write_cmd" && "$dry_run" != "1" ]]; then
      if bash -c "$write_cmd $issue \"$target\"" >/dev/null 2>&1; then
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
