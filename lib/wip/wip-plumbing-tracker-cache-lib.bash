# wip-plumbing-tracker-cache-lib.bash — the lifecycle-state cache (ADR-0019 §A
# state floor / BRIEF §3 "cache-as-floor"). A local JSON cache of each mapped
# node's lifecycle state is the durable, headless floor: boundary commands
# (step-04) write the emitted intent here, and `status` / `sync` read it. A live
# transport (Round 4) later refreshes it and is skipped (cache-only) when down.
# The cache lives under `.wip/` (gitignored) — workspace-local truth.
# shellcheck shell=bash

# _wip_tracker_cache_path <root> — echo the cache file path.
_wip_tracker_cache_path() {
  printf '%s/.wip/tracker-cache.json' "$1"
}

# _wip_tracker_cache_read <root> — echo the whole cache object, or `{}` when the
# file is absent or unparseable (a corrupt cache must never crash a read).
_wip_tracker_cache_read() {
  local f
  f="$(_wip_tracker_cache_path "$1")"
  if [[ -f "$f" ]] && jq -e . "$f" >/dev/null 2>&1; then
    jq -c . "$f"
  else
    printf '{}'
  fi
}

# _wip_tracker_cache_get <root> <node-key> — echo the entry for "<slug>/<node>"
# (e.g. {state, reason, updated}), or `null` when absent.
_wip_tracker_cache_get() {
  local key="$2"
  _wip_tracker_cache_read "$1" | jq -c --arg k "$key" '.[$k] // null'
}

# _wip_tracker_cache_set <root> <node-key> <state> <reason> <updated> — upsert
# the entry and write the cache atomically (tmp + mv). Echoes the written entry.
_wip_tracker_cache_set() {
  local root="$1" key="$2" state="$3" reason="$4" updated="$5"
  local f cur next tmp
  f="$(_wip_tracker_cache_path "$root")"
  mkdir -p "$root/.wip"
  cur="$(_wip_tracker_cache_read "$root")"
  next="$(jq -c \
    --arg k "$key" --arg s "$state" --arg r "$reason" --arg u "$updated" '
    .[$k] = { state: $s, reason: $r, updated: $u }' <<<"$cur")"
  tmp="$f.tmp.$$"
  printf '%s\n' "$next" >"$tmp" && mv -f "$tmp" "$f"
  jq -c --arg k "$key" '.[$k]' <<<"$next"
}
