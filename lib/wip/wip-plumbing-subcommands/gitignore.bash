# gitignore — make the manifest's `gitignore.always_commit` policy real in
# `.gitignore`. The declaration has always been there ("tracked even when
# `commit: false`"); nothing on disk made it true. This verb is what does.
#
# Not a rung of the closeout ladder (ADR-0016) — `always_commit` is a standing
# repo-hygiene invariant, not a step/round/initiative lifecycle state. It shares
# the ladder's SHAPE (a declared state that nothing deterministically enforced)
# and therefore its plumbing conventions, not its contract. Workplan step-05.
#
# The verb is a thin arg-parse + resolve + emit wrapper: every mechanic that
# matters (the three-line un-ignore shape, the marker block, idempotency,
# the refusals) lives in `_wip_gitignore_sync_always_commit` and is unit-tested
# there. Like `ship`, this is an UN-GATED writer — the drift it would gate on is
# precisely the drift it exists to erase.
# shellcheck shell=bash

# wip_plumbing_cmd_gitignore sync [--dry-run]
#
# Resolution mirrors `closeout`: `wip_find_root` locates the repo root, and both
# operands are derived from it (`<root>/.wip.yaml`, `<root>/.gitignore`) — the
# verb takes no path arguments, since a `.gitignore` that is not the root's is
# not the file this policy is about.
#
# Error codes: missing/unknown subcommand or unknown flag -> exit 2 (usage); no
# manifest -> exit 4 (no-manifest, closeout parity); a generator refusal
# (unreadable operand, an `always_commit` entry outside `.wip/`, a nested entry
# the one-level un-ignore cannot express, a missing `.wip/` anchor line, a
# corrupt block) -> exit 1 (internal). The generator's own stderr line names
# WHICH refusal fired; the exit code and JSON envelope only say that one did —
# the same division of labor `closeout` uses for its writer seams.
wip_plumbing_cmd_gitignore() {
  local action="" dry_run="${WIP_DRY_RUN:-0}"
  while [[ $# -gt 0 ]]; do
    case "$1" in
      --dry-run)
        dry_run=1
        shift
        ;;
      -*) wip_die 2 usage "gitignore: unknown flag: $1" ;;
      *)
        if [[ -z "$action" ]]; then
          action="$1"
        else
          wip_die 2 usage "gitignore: unexpected arg: $1"
        fi
        shift
        ;;
    esac
  done

  [[ -n "$action" ]] || wip_die 2 usage "gitignore: missing subcommand (expected: sync)"
  [[ "$action" == "sync" ]] || wip_die 2 usage "gitignore: unknown subcommand: $action"

  # Thread --dry-run to the writer seam via the env var it reads.
  WIP_DRY_RUN="$dry_run"
  export WIP_DRY_RUN

  local root
  root="$(wip_find_root)" || wip_die 4 no-manifest "no .wip.yaml found from $PWD upward"
  local manifest="$root/.wip.yaml" gitignore="$root/.gitignore"

  local sync_status
  sync_status="$(_wip_gitignore_sync_always_commit "$manifest" "$gitignore")" ||
    wip_die 1 internal "gitignore: sync writer failed"

  # `changed` restates the status word as the boolean every other verb's ledger
  # carries, so a caller can branch on one key across the whole family rather
  # than learning each verb's status vocabulary.
  local changed=false
  [[ "$sync_status" == "updated" ]] && changed=true

  jq -nc \
    --arg status "$sync_status" --arg gitignore "$gitignore" \
    --argjson changed "$changed" --arg dry "$dry_run" '
    { ok: true, action: "sync", status: $status, changed: $changed,
      gitignore: $gitignore }
    + (if $dry == "1" then { dry_run: true } else {} end)
  '
}
