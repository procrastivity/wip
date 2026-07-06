# orchestrate — deterministic prep + backend selection for orchestration.
#
# `orchestrate prep` (ADR-0012) resolves an initiative's active step, gates
# orchestration readiness, and emits a "what to orchestrate" brief that
# /wip:orchestrate consumes before it adopts the Orchestrator role and spawns a
# Coordinator.
#
# `orchestrate backend [<name>]` (ADR-0013) shows or switches the active
# orchestration backend by regenerating the generated pointer
# roles/backends/active.md from roles/backends/<name>.md and flipping
# features.orchestration.backend.
#
# Neither verb spawns or names a backend *tool* (no mcp__solo__*, no
# agent_tool_id) — spawning is a plugin/MCP concern. prep emits the FACTS about
# the work (initiative / step / workplan), not the STAFFING of it (Tier,
# process names) which lives in the Roles + backend binding (ADR-0007); backend
# selects the *binding*, not a tool. Pure function of .wip.yaml + roadmap + disk.
# shellcheck shell=bash

# orchestrate.bash does NOT source flatten-lib via the bin dispatcher (which
# only sources this one subcommand file), so wire it here: the vendored
# (`source: vendored`) backend switch re-renders the four flattened agent files
# through `wip_flatten_render` (ADR-0020 / step-04).
# shellcheck source=lib/wip/wip-plumbing-flatten-lib.bash
source "$WIP_LIB/wip-plumbing-flatten-lib.bash"

wip_plumbing_cmd_orchestrate() {
  local sub="${1:-}"
  shift || true
  case "$sub" in
    prep) _wip_orchestrate_cmd_prep "$@" ;;
    backend) _wip_orchestrate_cmd_backend "$@" ;;
    "") wip_die 2 usage "orchestrate: missing subcommand (prep|backend)" ;;
    *) wip_die 2 usage "orchestrate: unknown subcommand: $sub" ;;
  esac
}

# _wip_orchestrate_active_drift <backends_dir> <backend> — the ONE drift oracle
# for the generated `active.md` backend pointer (ADR-0013 / step-04 D2). Byte-
# compares roles/backends/<backend>.md against roles/backends/active.md;
# generation is a literal `cp`, so identity is byte-identity. Pure read — NEVER
# writes. Echoes exactly one status token on stdout and always returns 0:
#
#   in-sync    — active.md exists and is byte-identical to <backend>.md
#   drift      — active.md is missing OR differs from <backend>.md (D5: a
#                missing pointer is drift, never a silent "in sync")
#   no-source  — <backend> is empty, or roles/backends/<backend>.md itself is
#                missing (the caller decides: the show path folds it into
#                active_in_sync:false; --check surfaces it as `unknown-backend`)
#
# The offending path is invariant — always <backends_dir>/active.md — so callers
# hardcode the repo-relative `roles/backends/active.md` for their error `paths`.
# One oracle, three callers: the show path, `--check`, and a future `wip doctor`
# probe (D2).
_wip_orchestrate_active_drift() {
  local backends_dir="$1" backend="$2"
  local src="$backends_dir/$backend.md" dst="$backends_dir/active.md"
  if [[ -z "$backend" || ! -f "$src" ]]; then
    printf 'no-source\n'
    return 0
  fi
  if [[ -f "$dst" ]] && cmp -s "$src" "$dst"; then
    printf 'in-sync\n'
    return 0
  fi
  printf 'drift\n'
  return 0
}

# orchestrate backend [<name>] — show or switch the active orchestration
# backend. With no argument, reports the configured backend + whether the
# generated pointer `roles/backends/active.md` is in sync with it. With a
# <name>, sets features.orchestration.backend and regenerates active.md from
# roles/backends/<name>.md (idempotent). This selects a backend *binding*; it
# names no backend tool, so the ADR-0007 seam stays intact. It's the verb the
# /wip:status fallback offer calls when Solo is unreachable (ADR-0014).
_wip_orchestrate_cmd_backend() {
  local name="" check=0
  while [[ $# -gt 0 ]]; do
    case "$1" in
      --check)
        check=1
        shift
        ;;
      -*) wip_die 2 usage "orchestrate backend: unknown flag: $1" ;;
      *)
        [[ -z "$name" ]] || wip_die 2 usage "orchestrate backend: unexpected arg: $1"
        name="$1"
        shift
        ;;
    esac
  done

  # OQ3/D3: `--check` gates the CONFIGURED backend; pairing it with a switch
  # target is ambiguous — reject as a usage error (mirrors `setup agents`'
  # --migrate/--check mutual exclusion).
  [[ "$check" == "0" || -z "$name" ]] ||
    wip_die 2 usage "orchestrate backend: --check takes no backend name (it gates the configured backend); got: $name"

  local root mj
  root="$(wip_find_root)" || wip_die 4 no-manifest "no .wip.yaml found from $PWD upward"
  mj="$(wip_manifest_json "$root")"
  [[ -n "$mj" ]] || wip_die 4 bad-manifest "could not parse $root/.wip.yaml" ".wip.yaml"

  # D-04.1: branch on .features.orchestration.source. A vendored (flattened)
  # install has no local roles/ or active.md — its four self-contained
  # .claude/agents/wip/<role>.md files ARE the agents — so a backend switch
  # re-renders those files via wip_flatten_render instead of regenerating the
  # active.md pointer. Anything else (`plugin`, or absent → "plugin") keeps the
  # existing active.md path below byte-for-byte (this repo is `source: plugin`).
  local source
  source="$(jq -r '.features.orchestration.source // "plugin"' <<<"$mj")"
  if [[ "$source" == "vendored" ]]; then
    _wip_orchestrate_backend_vendored "$root" "$mj" "$name" "$check"
    return
  fi

  # Resolve the roles/ dir. The generated active.md lives next to the authored
  # backend bindings. In the dev/vendored layout roles/ sits at the repo root;
  # for a shared plugin install it lives under CLAUDE_PLUGIN_ROOT.
  local roles_dir=""
  if [[ -d "$root/roles/backends" ]]; then
    roles_dir="$root/roles"
  elif [[ -n "${CLAUDE_PLUGIN_ROOT:-}" && -d "$CLAUDE_PLUGIN_ROOT/roles/backends" ]]; then
    roles_dir="$CLAUDE_PLUGIN_ROOT/roles"
  else
    wip_die 4 no-roles-dir \
      "orchestrate backend: roles/backends/ not found (looked in \$root and \$CLAUDE_PLUGIN_ROOT); a \`source: plugin\` install switches the plugin's active.md, not a per-project copy"
  fi
  local backends_dir="$roles_dir/backends"

  # Available backends = authored *.md under backends/, excluding the
  # generated active.md pointer.
  local available
  available="$(find "$backends_dir" -maxdepth 1 -type f -name '*.md' ! -name 'active.md' \
    -exec basename {} .md \; 2>/dev/null | LC_ALL=C sort | jq -R . | jq -sc .)"
  [[ -n "$available" ]] || available="[]"

  local current
  current="$(jq -r '.features.orchestration.backend // ""' <<<"$mj")"

  # --check (plugin path): read-only commit-time drift gate on the generated
  # active.md pointer (D3/D5). Consumes the shared oracle, emits the D3 JSON,
  # exits 0 (in sync) / 4 (drift or unknown-backend). NEVER writes, in any
  # branch. Dispatched before both the show and switch paths.
  if [[ "$check" == "1" ]]; then
    _wip_orchestrate_backend_check "$backends_dir" "$current" "$source"
  fi

  # No name → report current + sync state, do not mutate. The drift check is the
  # shared oracle (D2): in-sync → true; drift or no-source (missing <backend>.md)
  # → false, preserving this path's original behavior byte-for-byte.
  if [[ -z "$name" ]]; then
    local in_sync="false"
    [[ "$(_wip_orchestrate_active_drift "$backends_dir" "$current")" == "in-sync" ]] && in_sync="true"
    if [[ "${WIP_JSON:-1}" == "1" ]]; then
      jq -nc --arg b "$current" --argjson avail "$available" --argjson sync "$in_sync" '
        {ok:true, verb:"orchestrate backend", backend:$b, available:$avail, active_in_sync:$sync}'
    fi
    return 0
  fi

  # Switch. Validate the requested backend exists (active is reserved).
  [[ "$name" =~ ^[a-z][a-z0-9-]*$ ]] ||
    wip_die 2 usage "orchestrate backend: backend name must match ^[a-z][a-z0-9-]*$: $name"
  [[ "$name" != "active" ]] ||
    wip_die 2 usage "orchestrate backend: 'active' is the generated pointer, not a backend name"
  local src="$backends_dir/$name.md" dst="$backends_dir/active.md"
  [[ -f "$src" ]] ||
    wip_die 4 unknown-backend \
      "orchestrate backend: no such backend '$name' (available: $(jq -r 'join(", ")' <<<"$available"))"

  # Regenerate the pointer iff it differs (idempotent). Honor --dry-run env.
  local active_regenerated=false
  if [[ ! -f "$dst" ]] || ! cmp -s "$src" "$dst"; then
    if [[ "${WIP_DRY_RUN:-0}" != "1" ]]; then
      cp "$src" "$dst" || wip_die 1 internal "orchestrate backend: failed to regenerate active.md"
    fi
    active_regenerated=true
  fi

  # Flip features.orchestration.backend (idempotent; honors WIP_DRY_RUN).
  # Surgical scalar set — NOT a whole-node rewrite — so block style and the
  # manifest's inline comments survive a switch (this verb runs repeatedly,
  # incl. from the /wip:status fallback offer).
  local manifest="$root/.wip.yaml" manifest_updated_json="null"
  if [[ "$current" != "$name" ]]; then
    if [[ "${WIP_DRY_RUN:-0}" != "1" ]]; then
      NAME="$name" yq -i '.features.orchestration.backend = strenv(NAME)' "$manifest" ||
        wip_die 1 internal "orchestrate backend: manifest update failed"
    fi
    manifest_updated_json='".wip.yaml"'
  fi

  if [[ "${WIP_JSON:-1}" == "1" ]]; then
    jq -nc \
      --arg b "$name" --argjson avail "$available" \
      --argjson regen "$active_regenerated" \
      --argjson manifest_updated "$manifest_updated_json" '
      {ok:true, verb:"orchestrate backend", backend:$b, available:$avail,
       active_regenerated:$regen, manifest_updated:$manifest_updated}'
  fi
}

# _wip_orchestrate_backend_check <backends_dir> <backend> <source> — the
# read-only `orchestrate backend --check` drift gate for the plugin
# (`source != vendored`) path (step-04 D3/D5). Consumes the shared oracle
# (_wip_orchestrate_active_drift) and emits the D3 JSON contract, then EXITS with
# the plumbing exit-code contract (matching `setup agents --check` + `doctor`,
# NOT the contrib script's exit 1):
#
#   in sync              → exit 0, {ok:true, verb, backend, source,
#                          active_in_sync:true, drift:[]}
#   drift / missing ptr  → exit 4, kind `backend-drift`,
#                          paths:["roles/backends/active.md"] (D5: a missing
#                          active.md is drift, never a silent "in sync")
#   missing <backend>.md → exit 4, kind `unknown-backend` (D5: a manifest backend
#                          naming a binding with no roles/backends/<backend>.md is
#                          a config error, surfaced not swallowed)
#
# Writes NOTHING in every branch (the oracle and this gate are pure reads).
_wip_orchestrate_backend_check() {
  local backends_dir="$1" backend="$2" source="$3"
  local status
  status="$(_wip_orchestrate_active_drift "$backends_dir" "$backend")"
  case "$status" in
    no-source)
      # D5: the configured backend names a binding that has no source file to
      # compare against. A config error (exit 4, `unknown-backend`) — never a
      # false "in sync". wip_die emits the {ok:false,error:{…}} envelope + exits.
      wip_die 4 unknown-backend \
        "orchestrate backend --check: configured backend '$backend' has no roles/backends/$backend.md" \
        "roles/backends/$backend.md"
      ;;
    in-sync)
      if [[ "${WIP_JSON:-1}" == "1" ]]; then
        jq -nc --arg b "$backend" --arg src "$source" '
          {ok:true, verb:"orchestrate backend", backend:$b, source:$src,
           active_in_sync:true, drift:[]}'
      fi
      exit 0
      ;;
    *)
      # drift — active.md missing or byte-differs from <backend>.md. Custom JSON
      # (D3 uses a `paths` array, which wip_die's singular `path` cannot emit),
      # mirroring `setup agents --check`'s drift envelope. Exit 4, `backend-drift`.
      if [[ "${WIP_JSON:-1}" == "1" ]]; then
        jq -nc --arg b "$backend" --arg src "$source" '
          {ok:false, verb:"orchestrate backend", backend:$b, source:$src,
           error:{code:4, kind:"backend-drift",
                  message:"roles/backends/active.md differs from roles/backends/\($b).md — run `make active` (or `orchestrate backend \($b)`) to regenerate",
                  paths:["roles/backends/active.md"]}}'
      fi
      printf 'wip-plumbing: orchestrate backend --check: active.md drift on: roles/backends/active.md\n' >&2
      exit 4
      ;;
  esac
}

# _wip_orchestrate_backend_vendored <root> <mj> <name> — the vendored
# (`source: vendored`, flattened install) branch of `orchestrate backend`
# (ADR-0020 / step-04 D-04.*). A flattened consumer has no roles/ or active.md,
# so a switch RE-RENDERS the four self-contained .claude/agents/wip/<role>.md
# files via the pure renderer rather than regenerating the active.md pointer.
# The renderer resolves roles/ via $CLAUDE_PLUGIN_ROOT (the wip plugin must be
# enabled at switch time — the same dependency `setup agents` has).
#
#   - No <name> → show: report the manifest backend, a best-effort `available`
#     list, and `active_in_sync:null` (no pointer to compare; D-04.5). A read
#     must not hard-fail when the plugin is unreachable → `available:[]`.
#   - <name> → switch: validate the name (reuse the active.md regex), render all
#     four roles to tmpfiles FIRST (so a render failure leaves NOTHING partial,
#     D-04.4), flip the manifest backend with the same surgical `yq` set, then
#     land each file iff it differs (`cmp -s || cp`, mirroring the active.md
#     branch — D-04.3; honors WIP_DRY_RUN). Emits the D-04.6 JSON shape (with
#     `reflattened`, no `active_regenerated`).
_wip_orchestrate_backend_vendored() {
  local root="$1" mj="$2" name="$3" check="${4:-0}"

  local current
  current="$(jq -r '.features.orchestration.backend // ""' <<<"$mj")"

  # --check (D4): a flattened consumer has no active.md / roles/ pointer to gate,
  # so the commit-time pointer drift gate is a clean no-op here — exit 0,
  # active_in_sync:null (symmetric with the vendored show path), drift:[]. Emitted
  # BEFORE any plugin-reachability work; NEVER writes. (The vendored agent-FILE
  # drift gate is `setup agents --check`, a separate surface — ADR-0015/0020.)
  if [[ "$check" == "1" ]]; then
    if [[ "${WIP_JSON:-1}" == "1" ]]; then
      jq -nc --arg b "$current" '
        {ok:true, verb:"orchestrate backend", backend:$b, source:"vendored",
         active_in_sync:null, drift:[]}'
    fi
    return 0
  fi

  # Available backends = best-effort from the plugin's backends/, resolved via
  # the renderer's own roles/ seam ($WIP_ROLES_DIR → $root/roles →
  # $CLAUDE_PLUGIN_ROOT/roles). Lenient: a read never hard-fails when roles/ is
  # unreachable (D-04.5) → available:[].
  local available="[]" rdir=""
  if rdir="$(_wip_flatten_roles_dir 2>/dev/null)" && [[ -d "$rdir/backends" ]]; then
    available="$(find "$rdir/backends" -maxdepth 1 -type f -name '*.md' ! -name 'active.md' \
      -exec basename {} .md \; 2>/dev/null | LC_ALL=C sort | jq -R . | jq -sc .)"
    [[ -n "$available" ]] || available="[]"
  else
    rdir=""
  fi

  # No name → show. No consumer active.md exists, so active_in_sync is null.
  if [[ -z "$name" ]]; then
    if [[ "${WIP_JSON:-1}" == "1" ]]; then
      jq -nc --arg b "$current" --argjson avail "$available" '
        {ok:true, verb:"orchestrate backend", backend:$b, source:"vendored",
         available:$avail, active_in_sync:null}'
    fi
    return 0
  fi

  # Switch. Validate the requested backend name (same rules as the active.md
  # path; active is the reserved generated-pointer name there and is never a
  # backend).
  [[ "$name" =~ ^[a-z][a-z0-9-]*$ ]] ||
    wip_die 2 usage "orchestrate backend: backend name must match ^[a-z][a-z0-9-]*$: $name"
  [[ "$name" != "active" ]] ||
    wip_die 2 usage "orchestrate backend: 'active' is the generated pointer, not a backend name"

  # Precise unknown-backend error when backends/ is reachable (mirrors the
  # active.md path); otherwise fall through to the renderer's loud failure
  # (Q-04.1 / D-04.4).
  if [[ -n "$rdir" && ! -f "$rdir/backends/$name.md" ]]; then
    wip_die 4 unknown-backend \
      "orchestrate backend: no such backend '$name' (available: $(jq -r 'join(", ")' <<<"$available"))"
  fi

  # Re-render the four self-contained agent files (ADR-0020 D1). Render ALL
  # roles to tmpfiles first so a render failure (D-04.4) leaves NOTHING partial
  # — only after every render succeeds do we touch the manifest or any file.
  local -a roles=(orchestrator coordinator researcher builder) # ADR-0020 D1
  local -a tmps=() rels=()
  local role rel tmp rc
  for role in "${roles[@]}"; do
    rel=".claude/agents/wip/$role.md" # ADR-0020 D1
    tmp="$(mktemp)" || {
      [[ ${#tmps[@]} -gt 0 ]] && rm -f -- "${tmps[@]}"
      wip_die 1 internal "orchestrate backend: mktemp failed"
    }
    tmps+=("$tmp")
    set +e
    wip_flatten_render "$role" "$name" >"$tmp"
    rc=$?
    set -e
    if [[ "$rc" != "0" ]]; then
      rm -f -- "${tmps[@]}"
      case "$rc" in
        2) wip_die 2 usage "orchestrate backend: renderer rejected role '$role' / backend '$name' (rc=2)" ;;
        4) wip_die 4 no-roles-dir \
          "orchestrate backend: cannot render flattened agents — roles/ unreachable for role '$role' (backend '$name'); the wip plugin must be enabled (\$CLAUDE_PLUGIN_ROOT/roles) at switch time" ;;
        *) wip_die 4 render-failed \
          "orchestrate backend: failed to render flattened agent for role '$role' (backend '$name', rc=$rc); no files written" ;;
      esac
    fi
    rels+=("$rel")
  done

  # All renders succeeded. Flip features.orchestration.backend (idempotent;
  # honors WIP_DRY_RUN) using the same surgical scalar `yq` set the active.md
  # path uses, so block style + inline manifest comments survive (D-04.3).
  local manifest="$root/.wip.yaml" manifest_updated_json="null"
  if [[ "$current" != "$name" ]]; then
    if [[ "${WIP_DRY_RUN:-0}" != "1" ]]; then
      NAME="$name" yq -i '.features.orchestration.backend = strenv(NAME)' "$manifest" ||
        {
          rm -f -- "${tmps[@]}"
          wip_die 1 internal "orchestrate backend: manifest update failed"
        }
    fi
    manifest_updated_json='".wip.yaml"'
  fi

  # Land each rendered file iff it differs (cmp -s || cp, mirroring the
  # active.md branch's idempotent regenerate). mkdir -p the parent defensively
  # so a missing file self-heals (Q-04.4). Honor WIP_DRY_RUN: report would-be
  # re-renders, write nothing.
  local -a reflattened=()
  local i dest
  for i in "${!roles[@]}"; do
    rel="${rels[$i]}"
    tmp="${tmps[$i]}"
    dest="$root/$rel"
    if [[ ! -f "$dest" ]] || ! cmp -s "$tmp" "$dest"; then
      if [[ "${WIP_DRY_RUN:-0}" != "1" ]]; then
        mkdir -p -- "$(dirname -- "$dest")" ||
          {
            rm -f -- "${tmps[@]}"
            wip_die 1 internal "orchestrate backend: failed to create parent of $rel"
          }
        cp -- "$tmp" "$dest" ||
          {
            rm -f -- "${tmps[@]}"
            wip_die 1 internal "orchestrate backend: failed to write $rel"
          }
      fi
      reflattened+=("$rel")
    fi
  done
  rm -f -- "${tmps[@]}"

  if [[ "${WIP_JSON:-1}" == "1" ]]; then
    local reflattened_json="[]"
    if [[ "${#reflattened[@]}" -gt 0 ]]; then
      reflattened_json="$(printf '%s\n' "${reflattened[@]}" | jq -R . | jq -sc .)"
    fi
    jq -nc \
      --arg b "$name" --argjson avail "$available" \
      --argjson reflat "$reflattened_json" \
      --argjson manifest_updated "$manifest_updated_json" '
      {ok:true, verb:"orchestrate backend", backend:$b, source:"vendored",
       available:$avail, reflattened:$reflat, manifest_updated:$manifest_updated}'
  fi
}

_wip_orchestrate_cmd_prep() {
  local slug=""
  while [[ $# -gt 0 ]]; do
    case "$1" in
      --initiative)
        [[ $# -ge 2 ]] || wip_die 2 usage "orchestrate prep: --initiative requires an argument"
        slug="$2"
        shift 2
        ;;
      --initiative=*)
        slug="${1#--initiative=}"
        shift
        ;;
      -*) wip_die 2 usage "orchestrate prep: unknown flag: $1" ;;
      *) wip_die 2 usage "orchestrate prep: unexpected arg: $1" ;;
    esac
  done

  local root mj
  root="$(wip_find_root)" || wip_die 4 no-manifest "no .wip.yaml found from $PWD upward"
  mj="$(wip_manifest_json "$root")"
  [[ -n "$mj" ]] || wip_die 4 bad-manifest "could not parse $root/.wip.yaml" ".wip.yaml"

  # Gate 1: the orchestration capability must be enabled (ADR-0007). Feature
  # not enabled => exit 3 (per the prtend exit-code contract).
  local orch_enabled backend
  orch_enabled="$(jq -r '.features.orchestration.enabled // false' <<<"$mj")"
  backend="$(jq -r '.features.orchestration.backend // ""' <<<"$mj")"
  if [[ "$orch_enabled" != "true" ]]; then
    wip_die 3 orchestration-not-enabled \
      "orchestrate prep: features.orchestration.enabled is not true — run \`wip-plumbing setup agents\` or enable it in .wip.yaml"
  fi

  # Gate 1b: a backend that delegates runtime selection to an external control
  # plane must be REACHABLE at preflight, or the run hard-errors — no silent
  # fall-back (ADR-0025 §4; ADR-0012 amendment). Only the `duo` backend requires
  # this today: Solo/Task resolve in-process, so they need no reachability gate
  # here (Solo's unreachable path is a warn+offer in `status`, ADR-0014 — not a
  # hard error). The probe is a bash CLI call, never MCP (the deterministic core
  # cannot reach MCP, ADR-0012); `WIP_DUO_PROBE_CMD` is the test seam.
  if [[ "$backend" == "duo" ]]; then
    local duo_probe="${WIP_DUO_PROBE_CMD:-}"
    if [[ -z "$duo_probe" ]] && command -v duo >/dev/null 2>&1; then
      duo_probe="duo whoami --json"
    fi
    local duo_reachable="false" duo_out
    if [[ -n "$duo_probe" ]]; then
      duo_out="$(bash -c "$duo_probe" 2>/dev/null || true)"
      # `duo whoami --json` resolves a project when Duo↔Solo is live; require a
      # project id. (Do not trust exit code — `duo doctor` exits 0 with failing
      # checks; a resolved project is the clean positive-liveness signal.)
      if [[ -n "$duo_out" ]] &&
        [[ -n "$(jq -r '.project_id // empty' <<<"$duo_out" 2>/dev/null)" ]]; then
        duo_reachable="true"
      fi
    fi
    if [[ "$duo_reachable" != "true" ]]; then
      wip_die 3 backend-unreachable \
        "orchestrate prep: features.orchestration.backend is 'duo' but Duo is not installed or reachable — install/start Duo (the \`duo\` CLI) or switch the backend with \`orchestrate backend <name>\`. No silent fall-back (ADR-0025)."
    fi
  fi

  # Resolve the initiative (default current; --initiative overrides). Mirrors
  # status' resolution so the two agree on "which initiative".
  if [[ -z "$slug" ]]; then
    slug="$(jq -r '.current_initiative // ""' <<<"$mj")"
    [[ -n "$slug" ]] || wip_die 3 no-initiative \
      "orchestrate prep: no current_initiative; pass --initiative <slug>"
  fi
  local init_record
  init_record="$(jq -c --arg s "$slug" '
    [.initiatives[]? | select(.slug == $s)] | (.[0] // null)
  ' <<<"$mj")"
  [[ "$init_record" != "null" ]] ||
    wip_die 3 unknown-initiative "orchestrate prep: initiative not in manifest: $slug"

  local active_step_id roadmap_path
  active_step_id="$(jq -r '.active_step // ""' <<<"$init_record")"
  roadmap_path="$(jq -r '.roadmap // ""' <<<"$init_record")"
  [[ -n "$roadmap_path" ]] || roadmap_path=".wip/initiatives/$slug/roadmap.md"

  # Gate 2: an active step must be set. Orchestration boots the ACTIVE step;
  # without one there is nothing to orchestrate => exit 4 (data prevents
  # safe action; a human runs /wip:start first).
  [[ -n "$active_step_id" ]] || wip_die 4 no-active-step \
    "orchestrate prep: no active_step for '$slug' — run \`/wip:start <step-id>\` first" "$roadmap_path"

  local doc
  doc="$(wip_roadmap_parse "$root/$roadmap_path")"

  # Gate 3: the active step must exist in the roadmap => else exit 4.
  local step_record
  step_record="$(wip_roadmap_step "$doc" "$active_step_id")"
  if [[ -z "$step_record" || "$step_record" == "null" ]]; then
    wip_die 4 step-not-in-roadmap \
      "orchestrate prep: active_step not in roadmap: $active_step_id" "$roadmap_path"
  fi

  local step_title step_shipped step_lane round
  step_title="$(jq -r '.title // ""' <<<"$step_record")"
  step_shipped="$(jq -r '.shipped // false' <<<"$step_record")"
  step_lane="$(jq -c '.lane // null' <<<"$step_record")"
  round="$(wip_roadmap_active_round "$doc" "$active_step_id")"
  [[ -n "$round" ]] || round="null"

  # Locate the step's workplan. Glob <step-id>-*.md under workplans/; the `-`
  # delimiter after the step id keeps step-01 from matching step-01.5. A
  # MISSING workplan is NOT an error: the Coordinator's Researcher produces it
  # in Phase 1 (Roles). We still emit a canonical path (derived from the step
  # title, mirroring `workplan init`) so the Orchestrator has a stable target.
  local wp_dir_rel=".wip/initiatives/$slug/workplans"
  local wp_path="" wp_exists="false"
  local match
  match="$(find "$root/$wp_dir_rel" -maxdepth 1 -type f -name "$active_step_id-*.md" 2>/dev/null | LC_ALL=C sort | head -1)"
  if [[ -n "$match" ]]; then
    wp_path="$wp_dir_rel/$(basename "$match")"
    wp_exists="true"
  else
    # Derive the canonical slug the same way `workplan init` does.
    local derived
    derived="$(printf '%s' "$step_title" | tr '[:upper:]' '[:lower:]' |
      sed -E -e 's/[^a-z0-9]+/-/g' -e 's/^-+//' -e 's/-+$//')"
    [[ -n "$derived" ]] || derived="$active_step_id"
    wp_path="$wp_dir_rel/$active_step_id-$derived.md"
  fi

  # Advisory signals (non-fatal). Mirrors status' divergence reporting: an
  # already-shipped active step is surfaced, not refused.
  local signals="[]"
  if [[ "$step_shipped" == "true" ]]; then
    signals="$(jq -nc '["active-step-shipped"]')"
  fi

  jq -nc \
    --arg slug "$slug" \
    --arg backend "$backend" \
    --argjson round "$round" \
    --arg sid "$active_step_id" --arg stitle "$step_title" \
    --argjson sshipped "$step_shipped" --argjson slane "$step_lane" \
    --arg wppath "$wp_path" --argjson wpexists "$wp_exists" \
    --arg roadmap "$roadmap_path" \
    --argjson signals "$signals" '
    {
      ok: true,
      initiative: $slug,
      orchestration: { enabled: true, backend: $backend },
      round: $round,
      active_step: { id: $sid, title: $stitle, shipped: $sshipped, lane: $slane },
      workplan: { path: $wppath, exists: $wpexists },
      roadmap: $roadmap,
      signals: $signals
    }'
}
