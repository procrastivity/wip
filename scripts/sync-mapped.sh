#!/usr/bin/env bash
# Selected-store inventory and sequential passes using sync bindings (preview CLI).
set -euo pipefail
mode=${1:---list}
if (( $# > 1 )) || [[ $mode != --list && $mode != --run && $mode != --dry-run ]]; then
  printf 'Usage: bash sync-mapped.sh [--list|--dry-run|--run]\n' >&2
  exit 2
fi
command -v jq >/dev/null || { printf 'jq is required\n' >&2; exit 2; }
bin=$(command -v "${WIP_BIN:-wip}") || { printf 'wip executable not found\n' >&2; exit 2; }
# Inventory is checkout-independent; keep cwd and relative store/config paths intact.
inventory=$("$bin" plumbing sync bindings --json) || exit 2
if ! jq -e '
  (.bindings | type == "array") and
  all(.bindings[];
    (.local_repo | type == "string") and (.local_repo | length > 0) and
    (.local_repo | test("[\r\n]") | not) and
    (.active | type == "boolean") and (.connection | type == "string") and
    (.account | type == "string") and (.endpoint | type == "string")) and
  ((.bindings | length) == (.bindings | unique_by(.local_repo) | length))
' <<< "$inventory" >/dev/null; then
  printf 'Refusing unexpected binding inventory\n' >&2
  exit 2
fi
ids=$(jq -r '.bindings[] | select(.active) | .local_repo' <<< "$inventory")
printf 'Mode: %s; only the selected local store is included\n' "$mode" >&2
printf 'LocalRepo\tState\tConnection\tAccount\tEndpoint\n'
jq -r '.bindings[] | [.local_repo, (if .active then "active" else "paused" end),
  .connection, .account, .endpoint] | @tsv' <<< "$inventory"
failed=0 active=0 completed=0
skipped=$(jq '[.bindings[] | select(.active | not)] | length' <<< "$inventory")
while IFS= read -r id; do
  [[ -n $id ]] || continue
  active=$((active + 1))
  [[ $mode != --list ]] || continue
  args=(plumbing sync run --repo "$id" --json)
  [[ $mode != --dry-run ]] || args+=(--dry-run)
  # No connection/server override: each Repo keeps its retained destination.
  # Do not print full work inventories; CLI errors remain visible on stderr.
  if "$bin" "${args[@]}" >/dev/null; then
    printf 'OK %s: %s\n' "$mode" "$id" >&2
    completed=$((completed + 1))
  else
    printf 'FAILED %s: %s (not retried)\n' "$mode" "$id" >&2
    failed=$((failed + 1))
  fi
done <<< "$ids"
printf 'active=%d skipped=%d completed=%d failed=%d\n' \
  "$active" "$skipped" "$completed" "$failed" >&2
(( failed == 0 ))
