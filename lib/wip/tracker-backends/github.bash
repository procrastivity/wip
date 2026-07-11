# github.bash — the github issue-tracker backend, over the `gh issue` CLI
# (ADR-0026 §Decision 2). Glob-sourced by ../wip-plumbing-tracker-transport-lib.bash,
# which dispatches to the two functions below.
#
# Both commands are emitted as a SELF-CONTAINED shell snippet, not a bare `gh`
# string. The dispatcher runs the emitted text in a fresh shell with the issue
# appended as a positional arg:
#
#   bash -c "$read_cmd \"\$1\"" _ "$issue"
#   bash -c "$write_cmd \"\$1\" \"\$2\"" _ "$issue" "$target"
#
# so the snippet must consume `$1` (and `$2`) and split `owner/repo#N` itself
# before `gh` ever sees it — a bare `gh issue view` would get `octo/hello#7`
# verbatim as the issue number. Shell functions do not survive into a `bash -c`
# subshell, hence define-a-function-then-name-it: the caller's appended `"$1"`
# becomes the named function's argument.
# shellcheck shell=bash

# _wip_tracker_github_read_cmd — echo the read shell-out. Honors WIP_GITHUB_READ_CMD
# (test/process seam) first, else the default `gh issue view` snippet, which reduces
# the provider JSON to ONE semantic token via `gh --jq`:
#   CLOSED + NOT_PLANNED → canceled; any other CLOSED → done; else the label arms.
# CLOSED is tested BEFORE the label arms, so a closed issue carrying a stale
# `wip:in-progress` still reduces to `done`; `wip:in-review` is tested before
# `wip:in-progress`, so an issue carrying both reduces to `in-review`.
_wip_tracker_github_read_cmd() {
  if [[ -n "${WIP_GITHUB_READ_CMD:-}" ]]; then
    printf '%s' "$WIP_GITHUB_READ_CMD"
    return 0
  fi
  # shellcheck disable=SC2016  # deliberate: emitted verbatim, expanded by the caller
  printf '%s' '_wip_gh_read() { local n="${1##*#}" r="${1%%#*}"; [ "$r" = "$1" ] && r=""; gh issue view "$n" ${r:+--repo "$r"} --json state,stateReason,labels --jq "if .state == \"CLOSED\" then (if .stateReason == \"NOT_PLANNED\" then \"canceled\" else \"done\" end) elif ([.labels[].name] | index(\"wip:in-review\")) then \"in-review\" elif ([.labels[].name] | index(\"wip:in-progress\")) then \"in-progress\" else \"todo\" end"; }; _wip_gh_read'
}

# _wip_tracker_github_write_cmd — echo the write shell-out. Honors WIP_GITHUB_WRITE_CMD
# first, else the default snippet, invoked as `<cmd> <issue> <semantic-token>`. One
# `gh issue edit` per label transition (add+remove composed into a single call);
# `done`/`canceled` are pure closes, split on `--reason`.
#
# `canceled` is a bare `close --reason "not planned"` with NO `wip:canceled` label:
# GitHub's native NOT_PLANNED stateReason is the carrier the read already keys on,
# so the label would be written but never read — and would make every cancel depend
# on a third label existing in the repo. github therefore needs only TWO pre-existing
# labels, `wip:in-progress` and `wip:in-review`. (gitlab still needs `wip:canceled`:
# it has no stateReason, so there the label IS the only carrier.)
#
# An unrecognized token returns 2 and makes NO gh call, so `sync` buckets it as
# skipped{write failed} rather than falsely reporting it applied.
_wip_tracker_github_write_cmd() {
  if [[ -n "${WIP_GITHUB_WRITE_CMD:-}" ]]; then
    printf '%s' "$WIP_GITHUB_WRITE_CMD"
    return 0
  fi
  # shellcheck disable=SC2016  # deliberate: emitted verbatim, expanded by the caller
  printf '%s' '_wip_gh_write() { local n="${1##*#}" r="${1%%#*}" t="$2"; [ "$r" = "$1" ] && r=""; case "$t" in todo) gh issue edit "$n" ${r:+--repo "$r"} --remove-label wip:in-progress --remove-label wip:in-review ;; in-progress) gh issue edit "$n" ${r:+--repo "$r"} --add-label wip:in-progress --remove-label wip:in-review ;; in-review) gh issue edit "$n" ${r:+--repo "$r"} --add-label wip:in-review --remove-label wip:in-progress ;; "done") gh issue close "$n" ${r:+--repo "$r"} --reason completed ;; canceled) gh issue close "$n" ${r:+--repo "$r"} --reason "not planned" ;; *) return 2 ;; esac; }; _wip_gh_write'
}
