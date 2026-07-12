# wip-plumbing-amend-lib.bash — render + hash + in-place edit primitives
# behind `roadmap amend`. Pure bash + awk + sha256. Portable on BSD awk.
#
# Idempotency model: the hash covers the rendered insertion payload (the
# bullet block or the appended round), not the source artifact. Identical
# inserts shaped from differently-framed artifacts collapse to the same
# hash. Marker line: `<!-- wip-amend: <sha256> -->`.
# shellcheck shell=bash

# wip_amend_extract_directive_from_fm <fm-json> — emit "<kind>\t<value>" for
# the present directive, or "" if none. Caller has already parsed JSON.
wip_amend_extract_directive_from_fm() {
  local fm="$1" kind v
  for kind in insert-after replace append-round append-lane insert-step-in-lane; do
    v="$(printf '%s' "$fm" | jq -r --arg k "$kind" '.[$k] // empty' 2>/dev/null)"
    if [[ -n "$v" ]]; then
      printf '%s\t%s\n' "$kind" "$v"
      return 0
    fi
  done
  return 0
}

# wip_amend_extract_body <file> — strip the leading `---`…`---` head if
# present and echo the rest. No FM → echo file as-is.
wip_amend_extract_body() {
  awk '
    NR == 1 && /^---[[:space:]]*$/ { fm = 1; next }
    fm && !past && /^---[[:space:]]*$/ { past = 1; next }
    fm && !past { next }
    { print }
  ' "$1"
}

# wip_amend_hash <text-on-stdin> — emit lowercase hex sha256 of stdin.
wip_amend_hash() {
  if command -v shasum >/dev/null 2>&1; then
    shasum -a 256 | awk '{print $1}'
  else
    sha256sum | awk '{print $1}'
  fi
}

# Build the marker comment line.
wip_amend_marker() {
  printf '<!-- wip-amend: %s -->' "$1"
}

# wip_amend_render_step_bullet <body-on-stdin> [existing_title] — read body
# from stdin: a single `### step-XX — <title>` heading + one-or-more
# paragraphs. Emit one bullet line followed by collapsed body text:
#   - **step-XX — Title** — <body collapsed to single paragraph>
# When existing_title is supplied AND the heading omits a title, reuse it.
# Returns 1 if no valid heading found.
wip_amend_render_step_bullet() {
  local existing_title="${1:-}"
  awk -v existing="$existing_title" '
    BEGIN { step_id = ""; title = ""; body = "" }
    /^### step-/ {
      if (step_id != "") { next }   # only first heading
      # Extract step id (step-NN or step-NN.M).
      s = $0
      sub(/^### step-/, "", s)
      # id continues while alnum or .
      i = 0
      while (i < length(s) && substr(s, i+1, 1) ~ /[0-9.]/) i++
      step_id = "step-" substr(s, 1, i)
      rest = substr(s, i + 1)
      # Trim leading " — " or " - " or whitespace.
      sub(/^[[:space:]]*[—-][[:space:]]*/, "", rest)
      title = rest
      next
    }
    step_id != "" {
      # Collect body lines.
      if (body == "") body = $0
      else body = body " " $0
    }
    END {
      if (step_id == "") exit 1
      # Collapse whitespace runs.
      gsub(/[[:space:]]+/, " ", body)
      sub(/^[[:space:]]+/, "", body)
      sub(/[[:space:]]+$/, "", body)
      if (title == "" && existing != "") title = existing
      if (title == "") title = "(untitled)"
      if (body == "") printf "- **%s — %s**\n", step_id, title
      else printf "- **%s — %s** — %s\n", step_id, title, body
    }
  '
}

# wip_amend_render_lane_block <name> <body-on-stdin> — read a body of one or
# more `### step-XX — <title>` sections from stdin and emit a lane block:
#   ### Lane <name>
#   - **step-XX — Title** — <body collapsed to one paragraph>
#   - **step-YY — Title** — <body…>
# Bullets are contiguous (no blank lines) so the lane block stays intact under
# the parser's blank-line lane terminator (ADR-0010 §5). Mirrors the canonical
# bullet rendering used elsewhere. Returns 1 if no step heading is found.
wip_amend_render_lane_block() {
  local lane_name="$1"
  printf '### Lane %s\n' "$lane_name"
  awk '
    function flush() {
      if (step_id != "") {
        gsub(/[[:space:]]+/, " ", body)
        sub(/^[[:space:]]+/, "", body)
        sub(/[[:space:]]+$/, "", body)
        if (title == "") title = "(untitled)"
        if (body == "") printf "- **%s — %s**\n", step_id, title
        else printf "- **%s — %s** — %s\n", step_id, title, body
        emitted = 1
      }
      step_id = ""; title = ""; body = ""
    }
    /^### step-/ {
      flush()
      s = $0
      sub(/^### step-/, "", s)
      i = 0
      while (i < length(s) && substr(s, i+1, 1) ~ /[0-9.]/) i++
      step_id = "step-" substr(s, 1, i)
      rest = substr(s, i + 1)
      sub(/^[[:space:]]*[—-][[:space:]]*/, "", rest)
      title = rest
      next
    }
    step_id != "" {
      if (body == "") body = $0
      else body = body " " $0
    }
    END { flush(); if (!emitted) exit 1 }
  '
}

# wip_amend_render_round_block <body-on-stdin> — read body from stdin and
# emit it verbatim, trimmed of trailing blank lines, terminated with one
# trailing newline. Used by append-round.
wip_amend_render_round_block() {
  awk '
    { lines[NR] = $0 }
    END {
      # Find last non-blank line.
      last = NR
      while (last > 0 && lines[last] ~ /^[[:space:]]*$/) last--
      for (i = 1; i <= last; i++) print lines[i]
    }
  '
}

# Build a regex-safe literal pattern fragment from <step-id>.
_wip_amend_sid_pattern() {
  printf '%s' "$1" | sed 's/[][\/.^$*+?(){}|\\]/\\&/g'
}

# wip_amend_has_marker <roadmap-path> <hash> — exit 0 if marker line present.
wip_amend_has_marker() {
  local path="$1" hash="$2"
  [[ -f "$path" ]] || return 1
  grep -F -q "<!-- wip-amend: $hash -->" "$path"
}

# Find the start (0-indexed) of the bullet block for <step-id> in <lines
# array name>. Returns the index via stdout when found in real content.
# Exit contract: 0 = found, 1 = absent, 2 = only found inside an HTML comment span.
_wip_amend_find_step_block_start() {
  local sid="$1" arr_name="$2"
  local sid_re
  sid_re="$(_wip_amend_sid_pattern "$sid")"
  local i n L in_comment=0 found_comment=0
  local first_match=-1 tracker_match=-1 tracker_count=0
  eval 'n=${#'"$arr_name"'[@]}'
  for ((i = 0; i < n; i++)); do
    eval 'L=${'"$arr_name"'[$i]}'

    # Mirror wip_roadmap_parse's HTML comment skip exactly: a line starting
    # `<!--` (with optional leading whitespace) is skipped as comment content;
    # a multi-line skip stays open until a later line contains `-->`.
    if [[ "$in_comment" == "1" ]]; then
      if [[ "$L" =~ ^-[[:space:]]\*\*${sid_re}[[:space:]]— ]]; then
        found_comment=1
      fi
      [[ "$L" == *"-->"* ]] && in_comment=0
      continue
    fi
    if [[ "$L" =~ ^[[:space:]]*\<!-- ]]; then
      if [[ "$L" =~ ^-[[:space:]]\*\*${sid_re}[[:space:]]— ]]; then
        found_comment=1
      fi
      [[ "$L" == *"-->"* ]] || in_comment=1
      continue
    fi

    if [[ "$L" =~ ^-[[:space:]]\*\*${sid_re}[[:space:]]— ]]; then
      if ((first_match < 0)); then
        first_match=$i
      fi
      if [[ "$L" =~ \[tracker:[^]]+\] ]]; then
        tracker_match=$i
        tracker_count=$((tracker_count + 1))
      fi
    fi
  done
  if ((first_match >= 0)); then
    if ((tracker_count == 1)); then
      printf '%d\n' "$tracker_match"
    else
      printf '%d\n' "$first_match"
    fi
    return 0
  fi
  [[ "$found_comment" == "1" ]] && return 2
  return 1
}

# Find the (exclusive) end of the bullet block starting at <start-index>
# in <lines array name>. The block runs from `start` until the first
# line that does NOT start with `- ` (continuation, blank, heading, EOF).
# Continuation lines must begin with whitespace.
_wip_amend_find_step_block_end() {
  local start="$1" arr_name="$2"
  local i=$((start + 1)) n L
  eval 'n=${#'"$arr_name"'[@]}'
  while ((i < n)); do
    eval 'L=${'"$arr_name"'[$i]}'
    if [[ "$L" =~ ^[[:space:]] ]]; then
      i=$((i + 1))
      continue
    fi
    break
  done
  printf '%d\n' "$i"
}

# wip_amend_apply_insert_after <roadmap-path> <step-id> <bullet> <marker>
# Insert <bullet>\n<marker>\n immediately after <step-id>'s block.
# Returns 1 if the target step bullet is absent.
wip_amend_apply_insert_after() {
  local path="$1" step_id="$2" bullet="$3" marker="$4"
  local lines=()
  mapfile -t lines <"$path"
  local start end rc=0
  start="$(_wip_amend_find_step_block_start "$step_id" lines)" || rc=$?
  [[ "$rc" == "0" ]] || return "$rc"
  end="$(_wip_amend_find_step_block_end "$start" lines)"
  local out=() i n=${#lines[@]}
  for ((i = 0; i < end; i++)); do out+=("${lines[i]}"); done
  out+=("$bullet")
  out+=("$marker")
  for ((i = end; i < n; i++)); do out+=("${lines[i]}"); done
  printf '%s\n' "${out[@]}" >"$path"
  return 0
}

# wip_amend_apply_replace <roadmap-path> <step-id> <bullet> <marker>
# Replace <step-id>'s bullet block with <bullet>\n<marker>\n. Strips any
# pre-existing `<!-- wip-amend: -->` marker lines from the replaced block.
wip_amend_apply_replace() {
  local path="$1" step_id="$2" bullet="$3" marker="$4"
  local lines=()
  mapfile -t lines <"$path"
  local start end rc=0
  start="$(_wip_amend_find_step_block_start "$step_id" lines)" || rc=$?
  [[ "$rc" == "0" ]] || return "$rc"
  end="$(_wip_amend_find_step_block_end "$start" lines)"
  # Strip any wip-amend marker line that immediately follows the replaced
  # block (it belonged to the previous insert/replace).
  if ((end < ${#lines[@]})) && [[ "${lines[end]}" =~ \<!--[[:space:]]wip-amend ]]; then
    end=$((end + 1))
  fi
  local out=() i n=${#lines[@]}
  for ((i = 0; i < start; i++)); do out+=("${lines[i]}"); done
  out+=("$bullet")
  out+=("$marker")
  for ((i = end; i < n; i++)); do out+=("${lines[i]}"); done
  printf '%s\n' "${out[@]}" >"$path"
  return 0
}

# wip_amend_apply_append_round <roadmap-path> <block> <marker>
# Insert <block>\n<marker>\n before the first `## Deferred` / `## Backlog`
# heading. If neither is present, append at EOF.
wip_amend_apply_append_round() {
  local path="$1" block="$2" marker="$3"
  local lines=()
  mapfile -t lines <"$path"
  local n=${#lines[@]}
  local insert_at="$n"
  local i
  for ((i = 0; i < n; i++)); do
    if [[ "${lines[i]}" =~ ^\#\#[[:space:]](Deferred|Backlog) ]]; then
      insert_at=$i
      break
    fi
  done
  # Walk back over trailing blanks before insert_at to avoid stacking
  # multiple blank lines around the new block.
  while ((insert_at > 0)) && [[ -z "${lines[insert_at - 1]}" ]]; do
    insert_at=$((insert_at - 1))
  done
  local out=()
  for ((i = 0; i < insert_at; i++)); do out+=("${lines[i]}"); done
  # Blank line, block, marker, blank line, then the rest.
  out+=("")
  while IFS= read -r line; do out+=("$line"); done <<<"$block"
  out+=("$marker")
  out+=("")
  for ((i = insert_at; i < n; i++)); do out+=("${lines[i]}"); done
  printf '%s\n' "${out[@]}" >"$path"
  return 0
}

# wip_amend_apply_append_lane <roadmap-path> <round-n> <block> <marker>
# Insert <block>\n<marker>\n into round <round-n> at the END of its lane section —
# after the last existing lane block, but BEFORE any post-lane main-lane sync
# steps — so the round stays `main* (lane+)? main*` (ADR-0010 §5). When the round
# has no lanes yet, insert at the round's end (its main steps become pre-lane
# prereqs). Returns 1 if round <round-n> is not present. Assumes the round is
# already well-formed (the amend command refuses malformed roadmaps first).
wip_amend_apply_append_lane() {
  local path="$1" round_n="$2" block="$3" marker="$4"
  local lines=()
  mapfile -t lines <"$path"
  local n=${#lines[@]} i
  # Locate the round heading.
  local start=-1
  for ((i = 0; i < n; i++)); do
    if [[ "${lines[i]}" =~ ^\#\#\ Round\ ${round_n}\ — ]]; then
      start=$i
      break
    fi
  done
  ((start >= 0)) || return 1
  # End of the round = the next H2 (`## `) heading after start, else EOF.
  local end=$n
  for ((i = start + 1; i < n; i++)); do
    if [[ "${lines[i]}" =~ ^\#\#\  ]]; then
      end=$i
      break
    fi
  done
  # Find the end of the round's lane section by mirroring the parser's lane
  # tracking (blank line terminates a lane block). last_lane_line is the last
  # line that belongs to any lane block; we insert right after it so the new
  # lane lands before any trailing main-lane sync steps.
  local cur_lane="" saw_step=0 last_lane_line=-1 j L
  for ((j = start + 1; j < end; j++)); do
    L="${lines[j]}"
    if [[ "$L" =~ ^[[:space:]]*$ ]]; then
      if [[ -n "$cur_lane" && "$saw_step" == "1" ]]; then
        cur_lane=""
        saw_step=0
      fi
    elif [[ "$L" =~ ^\#\#\#\ Lane\  ]]; then
      cur_lane="lane"
      saw_step=0
      last_lane_line=$j
    elif [[ "$L" =~ ^-\ \*\*step- || "$L" =~ ^\#\#\#\ step- ]]; then
      if [[ -n "$cur_lane" ]]; then
        saw_step=1
        last_lane_line=$j
      fi
    elif [[ -n "$cur_lane" ]]; then
      # A continuation/body line within the current lane block.
      last_lane_line=$j
    fi
  done

  local insert_at
  if ((last_lane_line >= 0)); then
    # Insert immediately after the last lane block (before post-lane sync steps).
    insert_at=$((last_lane_line + 1))
  else
    # No lanes yet: insert at the round's end, walking back over trailing blanks.
    insert_at=$end
    while ((insert_at > start + 1)) && [[ -z "${lines[insert_at - 1]}" ]]; do
      insert_at=$((insert_at - 1))
    done
  fi
  local out=()
  for ((i = 0; i < insert_at; i++)); do out+=("${lines[i]}"); done
  out+=("")
  while IFS= read -r line; do out+=("$line"); done <<<"$block"
  out+=("$marker")
  out+=("")
  for ((i = insert_at; i < n; i++)); do out+=("${lines[i]}"); done
  printf '%s\n' "${out[@]}" >"$path"
  return 0
}

# wip_amend_apply_insert_step_in_lane <roadmap-path> <round-n> <lane-name>
#   <bullet> <marker>
# Append <bullet>\n<marker> at the END of the already-declared lane <lane-name>
# in round <round-n> (ADR-0010 §6, promoted for the bundle kind). The bullet
# lands contiguous with the lane's existing steps (no blank line) so the lane
# block stays intact under the parser's blank-line terminator. Works on an
# EMPTY lane (a `### Lane <name>` heading with no steps), inserting right after
# the heading — the bundle's lead-emits-empty-lanes pattern. Exit codes:
#   0 inserted, 2 round <round-n> not found, 1 lane not found in the round.
wip_amend_apply_insert_step_in_lane() {
  local path="$1" round_n="$2" lane_name="$3" bullet="$4" marker="$5"
  local lines=()
  mapfile -t lines <"$path"
  local n=${#lines[@]} i
  # Locate the round heading.
  local start=-1
  for ((i = 0; i < n; i++)); do
    if [[ "${lines[i]}" =~ ^\#\#\ Round\ ${round_n}\ — ]]; then
      start=$i
      break
    fi
  done
  ((start >= 0)) || return 2
  # End of the round = the next H2 (`## `) heading after start, else EOF.
  local end=$n
  for ((i = start + 1; i < n; i++)); do
    if [[ "${lines[i]}" =~ ^\#\#\  ]]; then
      end=$i
      break
    fi
  done
  # Walk to the target lane heading, then track the lane block's last line.
  # Mirrors the parser: a blank line terminates the block once it has a step;
  # a blank right after the heading (empty lane) does not. Another `### Lane`
  # heading also ends the block.
  local j L in_lane=0 saw_step=0 last_line=-1
  for ((j = start + 1; j < end; j++)); do
    L="${lines[j]}"
    if [[ "$in_lane" == "0" ]]; then
      if [[ "$L" =~ ^\#\#\#\ Lane\ (.+)$ ]]; then
        local nm="${BASH_REMATCH[1]}"
        nm="${nm%"${nm##*[![:space:]]}"}"
        if [[ "$nm" == "$lane_name" ]]; then
          in_lane=1
          last_line=$j
          saw_step=0
        fi
      fi
      continue
    fi
    if [[ "$L" =~ ^[[:space:]]*$ ]]; then
      [[ "$saw_step" == "1" ]] && break
      continue
    elif [[ "$L" =~ ^\#\#\#\ Lane\  || "$L" =~ ^\#\#\#\#+\ Lane ]]; then
      break
    elif [[ "$L" =~ ^-\ \*\*step- || "$L" =~ ^\#\#\#\ step- ]]; then
      saw_step=1
      last_line=$j
    else
      # Continuation/body line within the lane block.
      last_line=$j
    fi
  done
  ((in_lane == 1)) || return 1
  local insert_at=$((last_line + 1))
  local out=() k
  for ((k = 0; k < insert_at; k++)); do out+=("${lines[k]}"); done
  out+=("$bullet")
  out+=("$marker")
  for ((k = insert_at; k < n; k++)); do out+=("${lines[k]}"); done
  printf '%s\n' "${out[@]}" >"$path"
  return 0
}
