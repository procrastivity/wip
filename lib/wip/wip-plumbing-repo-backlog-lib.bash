# wip-plumbing-repo-backlog-lib.bash — parse and retire entries in the
# repo-level `.wip/backlog.md`, and in a roadmap's own `## Backlog` section.
# Sourced by bin/wip-plumbing. Pure bash + jq. Contract: workplan step-06
# (closeout-write-ladder), Chunks 1-2.
#
# Why a second parser at all, when wip_roadmap_parse already has a `backlog`
# mode: the two files are different grammars wearing the same name.
#
#   roadmap.md `## Backlog`  — terse ONE-LINE bullets, tracker in a literal
#                              `[tracker: ID]` marker on the bullet line.
#                              Already parsed correctly today.
#   .wip/backlog.md          — MULTI-PARAGRAPH prose blocks under `## Nice-to-have`
#                              (no `## Backlog` H2 at all), tracker on a TRAILING
#                              line, spelled as a markdown LINK.
#
# `wip_roadmap_parse` sees nothing in the second: its backlog mode only arms on a
# `## Backlog` heading, which that file does not have, so `roadmap parse
# .wip/backlog.md` returns an empty `.backlog[]`. This lib is what reads it.
#
# TRACKER SPELLING — two forms, both permanent (workplan D2, as corrected).
# The bracket form is primary/authoritative; the link form is the shape the live
# file actually uses (all 5 of its tracked entries; zero use the bracket form).
# The parser adapts to the file, never the reverse — `.wip/backlog.md` is not
# edited to match the parser.
#
#   [tracker: BDS-14]                          <- primary; _wip_roadmap_extract_tracker
#   ([BDS-14](https://linear.app/…/BDS-14))    <- fallback; TRAILING LINE ONLY
#
# The trailing-line anchor on the fallback is load-bearing, not cosmetic. Entry
# bodies routinely name OTHER issues in prose ("Distinct from BDS-18 …"), so a
# "first link anywhere in the body" fallback would silently attribute an entry to
# an issue it merely cites — and retirement matches on that id, so a
# mis-attribution deletes the wrong entry.
# shellcheck shell=bash

# The ADR-0026 tracker-id union, as a bare ERE fragment: a Linear key (BDS-22) or
# a github/gitlab ref (#123, owner/repo#123, grp/sub/proj#45). MIRRORS the union
# in `_wip_roadmap_extract_tracker` (wip-plumbing-roadmap-lib.bash) and
# `_wip_tracker_id_valid` (wip-plumbing-tracker-lib.bash); all three must stay in
# step.
_WIP_REPO_BACKLOG_ID_RE='[A-Z][A-Z0-9]*-[0-9]+|([A-Za-z0-9._-]+(/[A-Za-z0-9._-]+)+)?#[0-9]+'

# _wip_repo_backlog_extract_link_tracker <line> — echo the tracker id from a
# markdown-link reference `([<ID>](<url>))` in <line>, or empty.
#
# Deliberately its own function rather than inlined: it is the fallback half of
# the extraction order, and keeping it separately addressable is what lets the
# test suite STUB IT OUT and prove the live-shaped fixture reverts to a null
# tracker — i.e. prove the fallback is the thing doing the work, rather than
# merely asserting it (workplan Test strategy, chunk-1 mutation pin 1).
_wip_repo_backlog_extract_link_tracker() {
  local line="$1"
  if [[ "$line" =~ \(\[($_WIP_REPO_BACKLOG_ID_RE)\]\( ]]; then
    printf '%s' "${BASH_REMATCH[1]}"
  fi
}

# _wip_repo_backlog_parse <path> — emit a JSON array of the file's entries:
#   [{id, title, tracker, start_line, end_line}]
# Line numbers are 1-indexed; `end_line` is EXCLUSIVE, so chunk 2 splices out
# exactly `[start_line, end_line)`. Missing path => `[]`.
#
# An entry OPENS on a column-0 `- **Title**` bullet and RUNS UNTIL its end
# boundary: the next column-0 bullet of ANY kind (`^- `), any `#`/`##` heading,
# or EOF.
#
# The "any kind" in that boundary is the whole correction. The obvious rule —
# "runs until the next `- **` bullet" — gets entry DETECTION right (a pruned line
# opens `- _(`, never `- **`, so it is never mistaken for a real entry) but gets
# body ACCUMULATION wrong: an existing `- _(pruned …)_` line would fall INSIDE
# the preceding entry's [start,end) span, and chunk 2 splices that span out. So
# retiring the last real entry would silently delete the file's accumulated
# retirement history. A retirement writer that destroys retirement history is the
# worst possible version of this bug. The wider `^- ` boundary makes a pruned line
# TERMINATE the entry above it, so it is never in range for a splice.
#
# Indented sub-bullets stay inside the body by construction: both the open regex
# and the boundary regex are column-0 anchored, so a nested `  - foo` matches
# neither.
_wip_repo_backlog_parse() {
  local path="$1"
  if [[ ! -f "$path" ]]; then
    jq -nc '[]'
    return 0
  fi

  local -a lines=()
  local line
  while IFS= read -r line || [[ -n "$line" ]]; do
    lines+=("$line")
  done <"$path"

  local n=${#lines[@]} i start end
  local out='[]'

  for ((start = 0; start < n; start++)); do
    [[ "${lines[start]}" =~ ^-\ \*\* ]] || continue

    # Find this entry's end boundary (0-based, exclusive).
    end=$n
    for ((i = start + 1; i < n; i++)); do
      if [[ "${lines[i]}" =~ ^-\  ]] || [[ "${lines[i]}" =~ ^\# ]]; then
        end=$i
        break
      fi
    done

    # Join the body. A title may WRAP across lines (the live file has one), so
    # both title and tracker are read from the joined text, never from the
    # opening line alone.
    local body="" trailing=""
    for ((i = start; i < end; i++)); do
      body+="${lines[i]} "
      # Track the last NON-BLANK line: that is the "trailing line" the link-form
      # fallback is anchored to. Blank separator lines before the next entry must
      # not shadow it.
      [[ -n "${lines[i]//[[:space:]]/}" ]] && trailing="${lines[i]}"
    done

    # Title: everything between the opening `- **` and its CLOSING `**`. The
    # `(([^*]|\*[^*])+)` idiom is the roadmap lib's — a run that cannot cross a
    # `**`, so a title containing a literal `*` or an inline code span survives,
    # and a `**bold**` span later in the body cannot be mistaken for the close.
    local title=""
    if [[ "$body" =~ ^-\ \*\*(([^*]|\*[^*])+)\*\* ]]; then
      title="${BASH_REMATCH[1]}"
    fi
    # Collapse the whitespace runs introduced by joining wrapped lines.
    while [[ "$title" == *"  "* ]]; do title="${title//  / }"; done
    title="${title#"${title%%[![:space:]]*}"}"
    title="${title%"${title##*[![:space:]]}"}"

    # Tracker: bracket form first (authoritative), link form second (trailing
    # line only). Order is load-bearing — see the header block.
    local tracker
    tracker="$(_wip_roadmap_extract_tracker "$body")"
    if [[ -z "$tracker" ]]; then
      tracker="$(_wip_repo_backlog_extract_link_tracker "$trailing")"
    fi

    local id
    id="$(_wip_roadmap_slugify "$title")"

    out="$(jq -c \
      --arg id "$id" --arg title "$title" --arg trk "$tracker" \
      --argjson start "$((start + 1))" --argjson end "$((end + 1))" '
      . += [{
        id: $id,
        title: $title,
        tracker: (if $trk == "" then null else $trk end),
        start_line: $start,
        end_line: $end
      }]' <<<"$out")"
  done

  printf '%s' "$out"
}

# _wip_backlog_pruned_line <tracker-id> <date> <reason> — render the canonical
# pruned marker, mirroring the convention already live at `.wip/backlog.md:232-234`:
#
#   - _(pruned 2026-07-04 → filed as BDS-63: `wip ship` roadmap-marker writer …)_
#
# The reason is emitted with exactly one terminating `.` — a caller that already
# punctuated its reason does not get `..`.
_wip_backlog_pruned_line() {
  local tracker="$1" date="$2" reason="$3"
  reason="${reason%"${reason##*[![:space:]]}"}" # rtrim
  reason="${reason%.}"                          # drop a trailing period if the caller supplied one
  printf -- '- _(pruned %s → filed as %s: %s.)_' "$date" "$tracker" "$reason"
}

# _wip_backlog_write_retired <path> <start> <end> <pruned-line> — splice out the
# 1-indexed half-open range [start, end) and append <pruned-line> at EOF,
# separated from the preceding content by exactly one blank line.
#
# Shared by both front-ends below, so the repo backlog and a roadmap's `## Backlog`
# section can never drift in how they write the marker.
_wip_backlog_write_retired() {
  local path="$1" start="$2" end="$3" pruned="$4" anchor="${5:-}"

  local -a lines=()
  local line
  while IFS= read -r line || [[ -n "$line" ]]; do
    lines+=("$line")
  done <"$path"

  local n=${#lines[@]} i
  local -a out=()
  for ((i = 0; i < n; i++)); do
    # Skip the retired entry's lines (convert the 1-indexed half-open range).
    if ((i + 1 >= start && i + 1 < end)); then
      continue
    fi
    out+=("${lines[i]}")
  done

  # Where the pruned marker lands. With no <anchor> it goes at EOF (the repo
  # backlog, whose entries run to the end of the file). With an <anchor> — the
  # 1-indexed line of the heading that OPENS the section — it goes at the end of
  # that section instead, so a roadmap's `## Backlog` marker never lands under a
  # later `## Deferred`.
  local insert=${#out[@]} floor=0
  if [[ -n "$anchor" ]]; then
    # The splice only ever removes lines AFTER the section heading, so the
    # heading's index in `out` is the same as it was in `lines`.
    floor=$anchor # 0-based index of the line just after the heading
    for ((i = floor; i < ${#out[@]}; i++)); do
      if [[ "${out[i]}" =~ ^\#\#[^\#] ]]; then
        break
      fi
    done
    insert=$i
  fi
  # Back off the section's (or file's) trailing blank lines so the marker lands
  # immediately after the last real content, not after a gap.
  while ((insert > floor)) && [[ -z "${out[insert - 1]//[[:space:]]/}" ]]; do
    insert=$((insert - 1))
  done

  local -a final=()
  for ((i = 0; i < insert; i++)); do final+=("${out[i]}"); done
  final+=("" "$pruned")
  for ((i = insert; i < ${#out[@]}; i++)); do final+=("${out[i]}"); done

  printf '%s\n' "${final[@]}" >"$path"
}

# _wip_backlog_retire_entry <path> <tracker-id> <date> <reason>
#
# Retire the `.wip/backlog.md` entry carrying <tracker-id>: splice out its whole
# multi-paragraph block and append a `- _(pruned …)_` marker at EOF. Prints a bare
# status word and returns 0.
#
#   retired — an entry matched <tracker-id> and was spliced out.
#   noop    — no entry carries that tracker. NOT an error, and the common case:
#             most shipped steps have no matching backlog item at all, and a
#             re-run against an already-retired tracker must be quiet (this is
#             what makes `ship`/`closeout`/`backlog retire` idempotent).
#
# A missing file is `noop`, not an error — a repo need not have a backlog.
# Honors $WIP_DRY_RUN=1: the status word is still computed and printed, but no
# write happens.
_wip_backlog_retire_entry() {
  local path="$1" tracker="$2" date="$3" reason="$4"

  [[ -f "$path" ]] || {
    printf 'noop'
    return 0
  }

  local entries match
  entries="$(_wip_repo_backlog_parse "$path")"
  match="$(jq -c --arg trk "$tracker" '
    [.[] | select(.tracker == $trk)] | (.[0] // null)' <<<"$entries")"

  if [[ "$match" == "null" ]]; then
    printf 'noop'
    return 0
  fi

  if [[ "${WIP_DRY_RUN:-0}" == "1" ]]; then
    printf 'retired'
    return 0
  fi

  local start end
  start="$(jq -r '.start_line' <<<"$match")"
  end="$(jq -r '.end_line' <<<"$match")"

  _wip_backlog_write_retired "$path" "$start" "$end" \
    "$(_wip_backlog_pruned_line "$tracker" "$date" "$reason")"

  printf 'retired'
  return 0
}

# _wip_roadmap_backlog_retire_entry <roadmap-path> <tracker-id> <date> <reason>
#
# The same retirement, against a roadmap's own `## Backlog` section — a DIFFERENT
# grammar (terse one-line bullets, `[tracker: ID]` inline) that already
# round-trips trackers correctly today, so it needs no new parser: a matched entry
# is always exactly one line (`start_line == end_line - 1`).
#
# The pruned marker is appended at the end of the `## Backlog` SECTION, not at EOF
# — a roadmap keeps `## Deferred` (and other sections) after its backlog, and a
# marker appended at EOF would land under the wrong heading.
#
# Same status words, same `noop`-not-an-error contract, same $WIP_DRY_RUN honoring
# as `_wip_backlog_retire_entry`.
_wip_roadmap_backlog_retire_entry() {
  local path="$1" tracker="$2" date="$3" reason="$4"

  [[ -f "$path" ]] || {
    printf 'noop'
    return 0
  }

  local -a lines=()
  local line
  while IFS= read -r line || [[ -n "$line" ]]; do
    lines+=("$line")
  done <"$path"

  local n=${#lines[@]} i
  local in_backlog=0 heading=0 hit=-1

  for ((i = 0; i < n; i++)); do
    line="${lines[i]}"
    if [[ "$line" =~ ^\#\#\ Backlog ]]; then
      in_backlog=1
      heading=$((i + 1))
      continue
    fi
    # Any other `## ` heading closes the section.
    if ((in_backlog)) && [[ "$line" =~ ^\#\#[^\#] ]]; then
      break
    fi
    ((in_backlog)) || continue

    if [[ "$line" =~ ^-\ \*\* ]]; then
      local trk
      trk="$(_wip_roadmap_extract_tracker "$line")"
      if [[ -n "$trk" && "$trk" == "$tracker" ]]; then
        hit=$i
        break
      fi
    fi
  done

  if ((hit < 0)); then
    printf 'noop'
    return 0
  fi

  if [[ "${WIP_DRY_RUN:-0}" == "1" ]]; then
    printf 'retired'
    return 0
  fi

  _wip_backlog_write_retired "$path" "$((hit + 1))" "$((hit + 2))" \
    "$(_wip_backlog_pruned_line "$tracker" "$date" "$reason")" "$heading"

  printf 'retired'
  return 0
}
