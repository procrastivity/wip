# gitlab.bash — the `gitlab` issue-tracker backend adapter (ADR-0026 §Decision 2).
# Glob-sourced by ../wip-plumbing-tracker-transport-lib.bash; see ./README.md for
# the contract. Defines exactly the two contract functions. Each takes NO
# arguments and echoes a COMMAND STRING on stdout — the dispatcher calls it bare.
#
# The emitted string is re-parsed in a FRESH shell by the callers (sync.bash runs
# `bash -c "$read_cmd \"\$1\"" _ "$issue"` and `bash -c "$write_cmd \"\$1\" \"\$2\"" _
# "$issue" "$target"`), so it cannot call back into any wip function and must do
# its own `$1`/`$2` issue-ref parsing. Hence `bash -c <script> _`, with <script>
# shell-quoted via `printf %q` — that is what lets the nested jq filter (literal
# single quotes, double quotes, `$l`) survive the re-parse intact.
#
# Issue refs are `#N`, `owner/repo#N`, or nested `group/sub/proj#N`: number is
# ${1##*#}, project path is ${1%%#*}, passed as `-R <path>` only when non-empty
# AND different from $1 (so a bare `12` degrades to no `-R`). The flag is built as
# an array so an absent repo contributes zero argv words.
#
# GitLab has no `stateReason`, so `canceled` is label-carried. The three `wip:*`
# labels must pre-exist in the target project (ADR-0026 §Consequences).
# shellcheck shell=bash

# _wip_tracker_gitlab_read_cmd — echo the read shell-out. Invoked by the caller as
# `<cmd> <issue>`; prints ONE semantic token. Reduces `glab issue view <n> [-R p]
# --output json` through real `jq` (not `glab --jq`), so the reduction is wip's own
# logic and is exercised by the test rather than by the CLI:
#   closed + wip:canceled    → canceled
#   closed                   → done
#   opened + wip:in-review   → in-review    (in-review wins over in-progress)
#   opened + wip:in-progress → in-progress
#   otherwise                → todo
# Labels are normalized through `if type == "object" then .name else . end`: they
# serialize as plain strings today, and this keeps a future object-shaped array from
# silently reducing every issue to `todo`.
_wip_tracker_gitlab_read_cmd() {
  if [[ -n "${WIP_GITLAB_READ_CMD:-}" ]]; then
    printf '%s' "$WIP_GITLAB_READ_CMD"
    return 0
  fi
  local script
  IFS= read -r -d '' script <<'SH' || true
n=${1##*#}
p=${1%%#*}
r=()
[[ -n "$p" && "$p" != "$1" ]] && r=(-R "$p")
glab issue view "$n" "${r[@]}" --output json | jq -r '
  [.labels[]? | if type == "object" then .name else . end] as $l
  | if .state == "closed" then
      (if ($l | index("wip:canceled")) then "canceled" else "done" end)
    elif ($l | index("wip:in-review")) then "in-review"
    elif ($l | index("wip:in-progress")) then "in-progress"
    else "todo" end'
SH
  printf 'bash -c %q _' "$script"
}

# _wip_tracker_gitlab_write_cmd — echo the write shell-out. Invoked by the caller as
# `<cmd> <issue> <semantic-token>`; applies the transition (labels + close). An
# unknown token goes to stderr and exits 2.
#
# The `canceled` arm chains with `&&` so a failed label write does not close the
# issue — sync then buckets the row as `write failed` rather than half-applying.
#
# Push-forward-only, deliberately (sync's `_wip_tracker_semantic_rank` never applies a
# backward target): `todo` only strips the middle-state labels and never reopens, and
# `done`/`canceled` close without stripping `wip:in-progress`/`wip:in-review`.
_wip_tracker_gitlab_write_cmd() {
  if [[ -n "${WIP_GITLAB_WRITE_CMD:-}" ]]; then
    printf '%s' "$WIP_GITLAB_WRITE_CMD"
    return 0
  fi
  local script
  IFS= read -r -d '' script <<'SH' || true
n=${1##*#}
p=${1%%#*}
r=()
[[ -n "$p" && "$p" != "$1" ]] && r=(-R "$p")
case "$2" in
  todo)
    glab issue update "$n" "${r[@]}" --unlabel wip:in-progress --unlabel wip:in-review
    ;;
  in-progress)
    glab issue update "$n" "${r[@]}" --label wip:in-progress --unlabel wip:in-review
    ;;
  in-review)
    glab issue update "$n" "${r[@]}" --label wip:in-review --unlabel wip:in-progress
    ;;
  done)
    glab issue close "$n" "${r[@]}"
    ;;
  canceled)
    glab issue update "$n" "${r[@]}" --label wip:canceled &&
      glab issue close "$n" "${r[@]}"
    ;;
  *)
    printf 'wip: gitlab write: unknown semantic state: %s\n' "$2" >&2
    exit 2
    ;;
esac
SH
  printf 'bash -c %q _' "$script"
}
