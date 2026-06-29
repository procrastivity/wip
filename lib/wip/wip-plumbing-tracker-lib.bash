# wip-plumbing-tracker-lib.bash — the wip ⇄ issue-tracker node mapping mirror
# (ADR-0019 §C). The roadmap node body is the source of truth for `[tracker: ID]`
# keys; the `.wip.yaml` initiative `tracker_map` is a writer-generated mirror;
# `doctor` checks they agree. Shared by the `tracker` verb and `doctor`.
# shellcheck shell=bash

# _wip_tracker_map_from_roadmap <roadmap-doc> — echo {step-id: tracker-id} for
# every step carrying a `tracker` key, in roadmap order. `{}` when none.
_wip_tracker_map_from_roadmap() {
  jq -c '
    [ .rounds[].steps[] | select(.tracker != null) | {key: .id, value: .tracker} ]
    | from_entries
  ' <<<"$1"
}

# _wip_tracker_map_from_manifest <manifest-json> <slug> — echo the mirror object
# initiatives[slug].tracker_map (or `{}` when absent).
_wip_tracker_map_from_manifest() {
  jq -c --arg s "$2" '
    ([.initiatives[]? | select(.slug == $s)] | (.[0] // {}))
    | (.tracker_map // {})
  ' <<<"$1"
}
