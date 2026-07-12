# wip-plumbing-ship-roadmap-lib.bash — the `ship` verb's roadmap marker writer.
# Sourced by bin/wip-plumbing. Pairs with the manifest pointer writer in
# wip-plumbing-ship-manifest-lib.bash. Contract: ADR-0016.
# shellcheck shell=bash

# _wip_ship_mark_roadmap_shipped <roadmap-path> <step-id> <date>
#
# Insert/normalize <step-id>'s `✅ shipped <date>` bullet marker in the roadmap.
# Prints a status word to stdout and returns 0, or returns 1 on internal error:
#   updated — the marker was inserted, or a present-but-wrong/missing date was
#             corrected to <date>.
#   noop    — the bullet already carries the exact `✅ shipped <date>`; no write
#             is performed, so the file stays byte-identical.
#
# Reads the bullet's current shipped-state with `_wip_roadmap_extract_shipped`
# against the post-`**` remainder (`srest`) ONLY — never the whole line — so a
# `✅ shipped` that lives inside the bold title (e.g. step-02's own title) is not
# misread as a marker. Honors $WIP_DRY_RUN: the status is still computed and
# printed, but no file write happens when $WIP_DRY_RUN == 1.
#
# Rewrite mechanism: locate the bullet's block via the amend lib's
# `_wip_amend_find_step_block_start`/`_wip_amend_find_step_block_end` helpers,
# replace ONLY the bullet's first line in place, and keep its wrapped
# continuation lines verbatim. We splice that first line directly rather than
# calling `wip_amend_apply_replace` because that helper structurally appends an
# extra `<bullet>\n<marker>\n` line — a stray blank, or a `<!-- wip-amend: … -->`
# comment — which is alien to `ship`'s clean, marker-free bullet form (cf.
# step-01's manual marking).
_wip_ship_mark_roadmap_shipped() {
  local roadmap="$1" step_id="$2" date="$3"

  [[ -f "$roadmap" ]] || return 1

  local lines=()
  mapfile -t lines <"$roadmap"

  # Locate the bullet block [start, end): start is the bullet's first line,
  # [start+1, end) are its wrapped continuation lines (preserved verbatim).
  local start end
  start="$(_wip_amend_find_step_block_start "$step_id" lines)" || return 1
  end="$(_wip_amend_find_step_block_end "$start" lines)"

  # Split the first line on the parser's own bullet grammar so the bold title
  # (everything up to the closing `**`) is isolated from `srest` (the post-`**`
  # remainder). Reading shipped-state against `srest` ONLY is load-bearing.
  # The title capture matches up to the CLOSING `**` — mirroring the parser in
  # wip-plumbing-roadmap-lib.bash, so the writer accepts exactly the titles the
  # reader accepts, including ones carrying a literal `*` or an inline code span.
  local first="${lines[start]}"
  if [[ ! "$first" =~ ^(-\ \*\*step-[0-9]+(\.[0-9]+)?\ —\ (([^*]|\*[^*])+)\*\*)(.*)$ ]]; then
    return 1
  fi
  local prefix="${BASH_REMATCH[1]}" # - **step-NN — Title**
  local srest="${BASH_REMATCH[5]}"  # post-`**` remainder

  # Read the bullet's current shipped-state from `srest` only, with the `head`
  # anchor: a real marker sits immediately after the closing `**`. This also
  # hands back `tail` — `srest` with any existing marker run peeled off — which
  # is exactly the clean descriptive remainder the rebuild below re-attaches. We
  # take it from the parser rather than re-deriving it here so the marker's
  # spelling lives in exactly one place (the SHIPPED-MARKER SPELLING block in
  # wip-plumbing-roadmap-lib.bash) and reader and writer cannot drift apart.
  local shipped="false" cur_date="" tail=""
  _wip_roadmap_extract_shipped "$srest" shipped cur_date tail head

  # noop iff already shipped with the exact target date; every other case
  # (absent marker, or present-but-wrong/missing date) is an update.
  local status="updated"
  if [[ "$shipped" == "true" && "$cur_date" == "$date" ]]; then
    status="noop"
  fi

  if [[ "$status" == "updated" && "${WIP_DRY_RUN:-0}" != "1" ]]; then
    # Reconstruct the first line: prefix, then the marker immediately after the
    # closing `**`, then the clean tail (when any).
    local rebuilt="${prefix} ✅ shipped ${date}"
    [[ -n "$tail" ]] && rebuilt="${rebuilt} ${tail}"

    # Splice: keep [0, start) verbatim, swap the bullet's first line, keep its
    # continuation lines [start+1, end) and the rest of the file [end, n)
    # verbatim. No marker/comment line is injected.
    local out=() i n=${#lines[@]}
    for ((i = 0; i < start; i++)); do out+=("${lines[i]}"); done
    out+=("$rebuilt")
    for ((i = start + 1; i < end; i++)); do out+=("${lines[i]}"); done
    for ((i = end; i < n; i++)); do out+=("${lines[i]}"); done
    printf '%s\n' "${out[@]}" >"$roadmap"
  fi

  printf '%s' "$status"
  return 0
}
