#!/usr/bin/env bash
# test-shaper-templates — pin the prompt-sharing seam (step-11).
#
# The /wip:* plugin (step-11) and the CLI shaper (step-10.5) MUST read the
# same shaper prompts. The seam:
#   1. templates/prompts/intake/{preamble,<kind>}.md is the source of truth
#   2. `wip-plumbing template show intake/<id>` prints those bytes verbatim
#   3. lib/wip/wip-intake-shaper-lib.bash reads them from disk
#
# This test pins that for each kind:
#   (a) `template show intake/<k>` byte-equals templates/prompts/intake/<k>.md
#   (b) `wip_shaper_system_prompt <k>` (the lib's public surface)
#       *contains* both the preamble bytes AND the per-kind bytes.
set -euo pipefail
cd "$(dirname "$0")/.."
_WIP_TEST_NAME="shaper-templates"
# shellcheck source=test/helpers.sh
source test/helpers.sh

# shellcheck source=lib/wip/wip-intake-shaper-lib.bash
WIP_LIB="$PWD/lib/wip" source lib/wip/wip-intake-shaper-lib.bash

KINDS=(preamble brief amendment workplan-seed spec handoff bundle)

# (a) verb-show byte-equiv against the source files.
for k in "${KINDS[@]}"; do
  file_md5="$(md5sum "templates/prompts/intake/$k.md" | awk '{print $1}')"
  verb_md5="$(bin/wip-plumbing template show "intake/$k" | md5sum | awk '{print $1}')"
  assert_eq "$file_md5" "$verb_md5" "template show intake/$k byte-equiv"
done

# (b) lib's system-prompt contains both halves verbatim.
preamble_bytes="$(cat templates/prompts/intake/preamble.md)"
for k in brief amendment workplan-seed spec handoff bundle; do
  prompt="$(WIP_LIB="$PWD/lib/wip" wip_shaper_system_prompt "$k")"
  rules_bytes="$(cat "templates/prompts/intake/$k.md")"
  if [[ "$prompt" == *"$preamble_bytes"* ]]; then
    _WIP_PASS=$((_WIP_PASS + 1))
    printf '  ok   %s\n' "system prompt $k contains preamble"
  else
    _WIP_FAIL=$((_WIP_FAIL + 1))
    printf '  FAIL %s\n' "system prompt $k missing preamble" >&2
  fi
  if [[ "$prompt" == *"$rules_bytes"* ]]; then
    _WIP_PASS=$((_WIP_PASS + 1))
    printf '  ok   %s\n' "system prompt $k contains $k rules"
  else
    _WIP_FAIL=$((_WIP_FAIL + 1))
    printf '  FAIL %s\n' "system prompt $k missing $k rules" >&2
  fi
done

# Unknown kind falls back to the literal "unknown" line (legacy parity).
unknown_prompt="$(WIP_LIB="$PWD/lib/wip" wip_shaper_system_prompt bogus)"
case "$unknown_prompt" in
  *"Target kind: bogus — unknown."*)
    _WIP_PASS=$((_WIP_PASS + 1))
    printf '  ok   %s\n' "unknown kind falls back to literal"
    ;;
  *)
    _WIP_FAIL=$((_WIP_FAIL + 1))
    printf '  FAIL %s\n' "unknown kind missing literal" >&2
    ;;
esac

# Plugin would read via the verb; assert the verb gives bytes the shaper
# would also assemble. (Catches accidental separator drift.)
preamble_via_verb="$(bin/wip-plumbing template show intake/preamble)"
case "$(WIP_LIB="$PWD/lib/wip" wip_shaper_system_prompt brief)" in
  "$preamble_via_verb"*)
    _WIP_PASS=$((_WIP_PASS + 1))
    printf '  ok   %s\n' "system prompt begins with verb-served preamble"
    ;;
  *)
    _WIP_FAIL=$((_WIP_FAIL + 1))
    printf '  FAIL %s\n' "system prompt drifts from verb-served preamble" >&2
    ;;
esac

test_summary
