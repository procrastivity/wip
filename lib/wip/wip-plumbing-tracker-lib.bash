# wip-plumbing-tracker-lib.bash — the wip ⇄ issue-tracker node mapping mirror
# (ADR-0019 §C). The roadmap node body is the source of truth for `[tracker: ID]`
# keys; the `.wip.yaml` initiative `tracker_map` is a writer-generated mirror;
# `doctor` checks they agree. Shared by the `tracker` verb and `doctor`.
# shellcheck shell=bash

# _wip_tracker_id_valid <id> — return 0 iff <id> is a whole, well-formed tracker
# issue id (e.g. BDS-56): an uppercase key (letter then uppercase-alnum), a dash,
# then digits. Anchored — no surrounding text. This MIRRORS the id sub-pattern in
# `_wip_roadmap_extract_tracker` (wip-plumbing-roadmap-lib.bash), which extracts
# the same shape from a bracketed `[tracker: ID]` marker; both must stay in step.
# (Deliberately mirrored, not factored into a shared constant: test-roadmap-parse
# sources roadmap-lib in isolation, so a constant it depended on from here would
# be undefined there.) Used to validate an intake `tracker_anchor` at capture.
_wip_tracker_id_valid() {
  [[ "$1" =~ ^[A-Z][A-Z0-9]*-[0-9]+$ ]]
}

# _wip_tracker_map_from_roadmap <roadmap-doc> — echo {node-id: tracker-id} for
# every addressable node carrying a `tracker` key, in roadmap order. `{}` when
# none. Nodes are steps (`step-NN`) and rounds (`round-N`, ADR-0024 / D2): a round
# heading's `[tracker: ID]` is unioned in ahead of its own steps. Lanes are
# excluded by construction — the parser never records a lane tracker (ADR-0024 §D1).
_wip_tracker_map_from_roadmap() {
  jq -c '
    [ .rounds[]
      | ( [ select(.tracker != null) | {key: ("round-" + (.n | tostring)), value: .tracker} ]
          + [ .steps[] | select(.tracker != null) | {key: .id, value: .tracker} ] )
    ]
    | (add // [])
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
