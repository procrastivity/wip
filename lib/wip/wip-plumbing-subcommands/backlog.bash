# backlog — retire an entry from the repo-level `.wip/backlog.md` by tracker id.
#
# Why this verb exists, given `ship` and `closeout` already retire backlog
# entries as part of their own writes (workplan step-06 chunk 7): those two
# retire only what THEIR step/initiative carries. An operator holding a tracker
# id that was filed and shipped out-of-band has no way to prune it without a
# fake `ship` invocation. This verb is that way in — a direct, idempotent
# `retire <tracker-id>`, not a general replacement for the ladder's own writers.
#
# REPO BACKLOG ONLY. `ship`/`closeout` also touch the in-roadmap `## Backlog`
# sections, because each already knows which initiative — and therefore which
# roadmap.md — is in scope. This bare verb knows neither: it takes a tracker id
# and nothing else, so there is no roadmap to resolve, and guessing one would be
# a write against a file the operator never named. Those sections stay reachable
# only through the verbs that have the context to target them.
#
# The verb is a thin arg-parse + resolve + emit wrapper (`gitignore`'s shape):
# every mechanic that matters — the entry grammar, the tracker-form fallback,
# the splice boundary that preserves prior pruned history, idempotency — lives
# in `_wip_backlog_retire_entry` (wip-plumbing-repo-backlog-lib.bash) and is
# unit-tested there.
# shellcheck shell=bash

# wip_plumbing_cmd_backlog retire <tracker-id> [--dry-run]
#
# Resolution mirrors `gitignore`/`closeout`: `wip_find_root` locates the repo
# root and the operand is derived from it (`<root>/.wip/backlog.md`) — the verb
# takes no path argument, since a backlog that is not the root's is not the file
# this verb is about.
#
# Error codes: missing/unknown subcommand, missing tracker-id, unknown flag, or
# an unexpected extra arg -> exit 2 (usage); no manifest -> exit 4 (no-manifest,
# closeout parity).
#
# A tracker with NO matching entry is exit 0 / `status: "noop"` — deliberately
# not an error. `ship`'s hard-refuse-on-mismatch shape is wrong here: the caller
# of `backlog retire` is asserting an end state ("this tracker is not in the
# backlog"), and an already-pruned or never-present key already satisfies it. A
# missing `.wip/backlog.md` is `noop` for the same reason: a repo need not have
# a backlog to be in the state this verb establishes.
wip_plumbing_cmd_backlog() {
  local action="" tracker="" dry_run="${WIP_DRY_RUN:-0}"
  while [[ $# -gt 0 ]]; do
    case "$1" in
      --dry-run)
        dry_run=1
        shift
        ;;
      -*) wip_die 2 usage "backlog: unknown flag: $1" ;;
      *)
        if [[ -z "$action" ]]; then
          action="$1"
        elif [[ -z "$tracker" ]]; then
          tracker="$1"
        else
          wip_die 2 usage "backlog: unexpected arg: $1"
        fi
        shift
        ;;
    esac
  done

  [[ -n "$action" ]] || wip_die 2 usage "backlog: missing subcommand (expected: retire)"
  [[ "$action" == "retire" ]] || wip_die 2 usage "backlog: unknown subcommand: $action"
  [[ -n "$tracker" ]] || wip_die 2 usage "backlog: missing tracker-id (usage: backlog retire <tracker-id>)"

  # Thread --dry-run to the writer seam via the env var it reads.
  WIP_DRY_RUN="$dry_run"
  export WIP_DRY_RUN

  local root
  root="$(wip_find_root)" || wip_die 4 no-manifest "no .wip.yaml found from $PWD upward"
  local backlog="$root/.wip/backlog.md"

  # The pruned marker's date honors $WIP_NOW (the seam every other dated writer
  # uses). The reason names the verb rather than a step: an out-of-band retirement
  # has no step to cite, and a marker that invented one would be a lie in the file
  # the marker exists to keep honest.
  local retire_status
  retire_status="$(_wip_backlog_retire_entry \
    "$backlog" "$tracker" "$(wip_scaffold_now)" "retired via wip backlog retire")" ||
    wip_die 1 internal "backlog: retirement writer failed"

  # `changed` restates the status word as the boolean every other verb's ledger
  # carries, so a caller can branch on one key across the whole family rather
  # than learning each verb's status vocabulary.
  local changed=false
  [[ "$retire_status" == "retired" ]] && changed=true

  jq -nc \
    --arg status "$retire_status" --arg path "$backlog" --arg tracker "$tracker" \
    --argjson changed "$changed" --arg dry "$dry_run" '
    { ok: true, action: "retire", status: $status, changed: $changed,
      tracker: $tracker, path: $path }
    + (if $dry == "1" then { dry_run: true } else {} end)
  '
}
