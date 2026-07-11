#!/usr/bin/env bash
set -euo pipefail
cd "$(dirname "$0")/.."
_WIP_TEST_NAME="tracker-gitlab"
# shellcheck source=test/helpers.sh
source test/helpers.sh

# step-02 (ADR-0026, BDS-76): the `gitlab` issue-tracker backend adapter.
#
# CAVEAT — READ THIS BEFORE TRUSTING A GREEN RUN. This test STUBS `glab` on PATH
# DELIBERATELY, to keep the suite hermetic: deterministic, no network, no
# credentials, and green on a machine that has never installed `glab`. It must stay
# that way — do not reach for the real binary here. The consequence is that what it
# validates is *wip's* own behavior — the jq state reduction, the issue-ref parsing,
# the emitted argv, and the seam precedence — and NOT the real CLI's runtime
# behavior, since a stub happily accepts any flag.
#
# The flag spelling and the `-R` / `--output` / `--label` / `--unlabel` semantics
# asserted here were verified against real `glab` 1.102.0 and the docs cited in
# workplan D2/D3: `issue view` has `-F, --output` (text, json) and `-R, --repo`
# (OWNER/REPO or GROUP/NAMESPACE/REPO); `issue update` has `-l, --label` and
# `-u, --unlabel`; `issue close [<id> | <url>]` takes `-R, --repo`.
#
# Still doc-verified rather than live-verified: the JSON field names `.state`
# (`opened|closed`) and `.labels` (a string array) — confirming those against a live
# project needs network + credentials. The D6 defensive label normalization
# (`if type == "object" then .name else . end`) hedges exactly that. A live
# end-to-end smoke against an actual GitLab project (network + credentials +
# pre-existing `wip:*` labels) remains DEFERRED.
#
# The adapter's functions emit a command STRING that the callers re-parse in a
# fresh shell (`sync.bash:112` runs `bash -c "$read_cmd \"\$1\"" _ "$issue"`;
# `sync.bash:144` runs `bash -c "$write_cmd \"\$1\" \"\$2\"" _ "$issue" "$target"`).
# Every execution below goes through that same `bash -c` shape, so the re-parse is
# under test too, not just the string.

export WIP_NO_REGISTRY=1
# shellcheck source=lib/wip/wip-plumbing-tracker-cache-lib.bash
source lib/wip/wip-plumbing-tracker-cache-lib.bash
# Sourcing the transport lib glob-loads lib/wip/tracker-backends/*.bash — this is
# the first test to exercise that loader against a real adapter file.
# shellcheck source=lib/wip/wip-plumbing-tracker-transport-lib.bash
source lib/wip/wip-plumbing-tracker-transport-lib.bash
WIP=bin/wip-plumbing

# --- the glab stub ----------------------------------------------------------
# Appends its argv to $GLAB_LOG (one invocation per line) and, for `issue view`,
# cats the fixture JSON at $GLAB_FIXTURE. Asserting on BOTH sides — the token the
# adapter prints AND the argv the stub recorded — is what makes the stub a test of
# the adapter rather than a test of itself.
stub="$(wip_mktemp)"
cat >"$stub/glab" <<'STUB'
#!/usr/bin/env bash
printf '%s\n' "$*" >>"$GLAB_LOG"
if [[ "${1:-}" == "issue" && "${2:-}" == "view" ]]; then
  cat "$GLAB_FIXTURE"
fi
STUB
chmod +x "$stub/glab"
export GLAB_LOG="$stub/argv.log" GLAB_FIXTURE="$stub/issue.json"
export PATH="$stub:$PATH"

# The adapter is loaded and dispatched by backend name (proves the glob loader ran).
assert_eq "true" "$(declare -F _wip_tracker_gitlab_read_cmd >/dev/null && echo true || echo false)" \
  "glob loader sourced the gitlab adapter"
read_cmd="$(_wip_tracker_transport_read_cmd gitlab)"
write_cmd="$(_wip_tracker_transport_write_cmd gitlab)"

# run_read <issue> — execute the emitted read string exactly as sync.bash:112 does.
run_read() { bash -c "$read_cmd \"\$1\"" _ "$1"; }
# run_write <issue> <token> — execute the emitted write string as sync.bash:144 does.
run_write() { bash -c "$write_cmd \"\$1\" \"\$2\"" _ "$1" "$2"; }
# assert_state <json> <expected-token> <msg> — reduce a fixture through the REAL jq.
assert_state() {
  printf '%s' "$1" >"$GLAB_FIXTURE"
  assert_eq "$2" "$(run_read '#12')" "$3"
}

# --- 1. the five states, through the real jq reduction ----------------------
assert_state '{"state":"opened","labels":[]}' "todo" "opened, no labels -> todo"
assert_state '{"state":"opened","labels":["wip:in-progress"]}' "in-progress" "opened + wip:in-progress -> in-progress"
assert_state '{"state":"opened","labels":["wip:in-review"]}' "in-review" "opened + wip:in-review -> in-review"
assert_state '{"state":"opened","labels":["wip:in-progress","wip:in-review"]}' "in-review" \
  "both labels -> in-review (in-review wins)"
# Unrelated labels do not disturb the reduction.
assert_state '{"state":"opened","labels":["bug","backend"]}' "todo" "unrelated labels -> todo"

# --- 2. the closed split (canceled is label-carried; GitLab has no stateReason) ---
assert_state '{"state":"closed","labels":[]}' "done" "closed, no wip:canceled -> done"
assert_state '{"state":"closed","labels":["wip:canceled"]}' "canceled" "closed + wip:canceled -> canceled"
assert_state '{"state":"closed","labels":["wip:canceled","wip:in-review"]}' "canceled" \
  "closed + wip:canceled + wip:in-review -> still canceled"

# --- 3. labels-as-objects are normalized (D6) -------------------------------
# Labels serialize as plain strings today; the `if type == "object" then .name`
# guard means a future object-shaped array cannot silently reduce every issue to
# `todo`.
assert_state '{"state":"opened","labels":[{"name":"wip:in-review"}]}' "in-review" \
  "labels-as-objects -> in-review (D6)"

# --- 4. issue-ref parsing, asserted on the RECORDED argv (D4) ---------------
printf '%s' '{"state":"opened","labels":[]}' >"$GLAB_FIXTURE"
: >"$GLAB_LOG"
run_read '#12' >/dev/null
assert_eq "issue view 12 --output json" "$(cat "$GLAB_LOG")" "bare #N -> no -R"
: >"$GLAB_LOG"
run_read 'own/rep#12' >/dev/null
assert_eq "issue view 12 -R own/rep --output json" "$(cat "$GLAB_LOG")" "owner/repo#N -> -R own/rep"
: >"$GLAB_LOG"
run_read 'grp/sub/proj#12' >/dev/null
assert_eq "issue view 12 -R grp/sub/proj --output json" "$(cat "$GLAB_LOG")" \
  "nested group/sub/proj#N -> -R grp/sub/proj"

# --- 5. the write matrix (D3) -----------------------------------------------
: >"$GLAB_LOG"
run_write '#12' todo
assert_eq "issue update 12 --unlabel wip:in-progress --unlabel wip:in-review" "$(cat "$GLAB_LOG")" \
  "write todo -> strips both middle labels (no reopen, D7)"
: >"$GLAB_LOG"
run_write '#12' in-progress
assert_eq "issue update 12 --label wip:in-progress --unlabel wip:in-review" "$(cat "$GLAB_LOG")" \
  "write in-progress"
: >"$GLAB_LOG"
run_write '#12' in-review
assert_eq "issue update 12 --label wip:in-review --unlabel wip:in-progress" "$(cat "$GLAB_LOG")" \
  "write in-review"
: >"$GLAB_LOG"
run_write '#12' 'done' # quoted: bare `done` is a reserved word here (SC1010)
assert_eq "issue close 12" "$(cat "$GLAB_LOG")" "write done -> close (no label strip, D7)"
# canceled is a TWO-command chain: label, then close. `&&` means a failed label
# write never closes the issue -- sync buckets that row as `write failed` rather
# than half-applying.
: >"$GLAB_LOG"
run_write '#12' canceled
assert_eq "issue update 12 --label wip:canceled
issue close 12" "$(cat "$GLAB_LOG")" "write canceled -> label-then-close chain"
# A qualified ref threads -R through a write arm too.
: >"$GLAB_LOG"
run_write 'grp/sub/proj#12' in-review
assert_eq "issue update 12 -R grp/sub/proj --label wip:in-review --unlabel wip:in-progress" \
  "$(cat "$GLAB_LOG")" "write with a nested ref -> -R grp/sub/proj"
# An unknown token exits 2 and calls no glab at all.
: >"$GLAB_LOG"
set +e
run_write '#12' bogus 2>/dev/null
rc=$?
set -e
assert_eq "2" "$rc" "unknown semantic token -> exit 2"
assert_eq "" "$(cat "$GLAB_LOG")" "unknown token calls no glab"

# --- 6. seam precedence (ADR-0026 §Decision 2) ------------------------------
# Per-backend seam beats the adapter's default string...
assert_eq "rd" "$(WIP_GITLAB_READ_CMD=rd _wip_tracker_transport_read_cmd gitlab)" \
  "WIP_GITLAB_READ_CMD beats the default"
assert_eq "wr" "$(WIP_GITLAB_WRITE_CMD=wr _wip_tracker_transport_write_cmd gitlab)" \
  "WIP_GITLAB_WRITE_CMD beats the default"
# ...and the dispatcher's generic seam outranks the per-backend one.
assert_eq "GEN" "$(WIP_TRACKER_READ_CMD=GEN WIP_GITLAB_READ_CMD=rd _wip_tracker_transport_read_cmd gitlab)" \
  "WIP_TRACKER_READ_CMD outranks WIP_GITLAB_READ_CMD"
assert_eq "GENW" "$(WIP_TRACKER_WRITE_CMD=GENW WIP_GITLAB_WRITE_CMD=wr _wip_tracker_transport_write_cmd gitlab)" \
  "WIP_TRACKER_WRITE_CMD outranks WIP_GITLAB_WRITE_CMD"
# With no seam set the adapter emits its default glab string. Assert it is
# non-empty and mentions the CLI -- NOT the exact %q-quoted blob, which would be a
# brittle assertion on quoting rather than on behavior.
assert_eq "true" "$([[ -n "$read_cmd" ]] && echo true || echo false)" "default read cmd is non-empty"
assert_eq "true" "$([[ "$read_cmd" == *"glab issue view"* ]] && echo true || echo false)" \
  "default read cmd shells out to glab issue view"
assert_eq "true" "$([[ "$write_cmd" == *"glab issue update"* && "$write_cmd" == *"glab issue close"* ]] && echo true || echo false)" \
  "default write cmd shells out to glab issue update + close"

# --- 7. sync gitlab -> transport: cli, END-TO-END through bin/wip-plumbing ---
# The assertion that proves the adapter is reachable from the SHIPPED dispatcher,
# not just from a direct function call: no env seam is set here, so `sync` resolves
# the write cmd through _wip_tracker_transport_write_cmd -> the glob-loaded adapter.
tmp="$(wip_mktemp)"
mkdir -p "$tmp/.wip/initiatives/demo"
cat >"$tmp/.wip.yaml" <<'YAML'
version: 1
features: { wip: { enabled: true, root: .wip }, issue-tracker: { enabled: true, backend: gitlab } }
current_initiative: demo
initiatives:
  - slug: demo
    status: in-flight
    tracker_map: { step-01: "#12" }
    roadmap: .wip/initiatives/demo/roadmap.md
YAML
# The roadmap must MIRROR the manifest's tracker_map exactly: sync.bash:80 fails
# with `tracker-mirror-drift` when roadmap != manifest.
printf '# Roadmap — demo\n\n## Round 1 — One\n\n- **step-01 — First** — x. [tracker: #12]\n' \
  >"$tmp/.wip/initiatives/demo/roadmap.md"
# wip's truth: step-01 is in-review (rank 2). The stub reports the issue as still
# `opened` with no labels (-> todo, rank 0), so this is a FORWARD transition and
# sync applies it through the adapter's write string.
_wip_tracker_cache_set "$tmp" "demo/step-01" "in-review" "ship" "2026-07-11" >/dev/null
printf '%s' '{"state":"opened","labels":[]}' >"$GLAB_FIXTURE"
: >"$GLAB_LOG"

s="$(WIP_ROOT="$tmp" $WIP sync gitlab)"
assert_eq "gitlab" "$(jq -r '.backend' <<<"$s")" "sync: backend echo"
assert_eq "cli" "$(jq -r '.transport' <<<"$s")" "sync gitlab -> transport cli (adapter reachable from the dispatcher)"
assert_eq "1" "$(jq -r '.applied | length' <<<"$s")" "sync: the forward row lands in .applied"
assert_eq "demo/step-01" "$(jq -r '.applied[0].node' <<<"$s")" "sync: applied node"
assert_eq "#12" "$(jq -r '.applied[0].issue' <<<"$s")" "sync: applied issue"
assert_eq "in-review" "$(jq -r '.applied[0].to' <<<"$s")" "sync: applied target (passthrough — gitlab has no provider rename)"
assert_eq "0" "$(jq -r '.pending | length' <<<"$s")" "sync: nothing left pending on the cli path"
# The recorded argv proves the WRITE actually went through the adapter: the read
# (issue view) and then the in-review update.
assert_grep "^issue view 12 --output json$" "$GLAB_LOG" "sync: read went through the adapter"
assert_grep "^issue update 12 --label wip:in-review --unlabel wip:in-progress$" "$GLAB_LOG" \
  "sync: write went through the adapter"

test_summary
