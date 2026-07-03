#!/usr/bin/env bash
set -euo pipefail
cd "$(dirname "$0")/.."
_WIP_TEST_NAME="forge-observe"
# shellcheck source=test/helpers.sh
source test/helpers.sh

# `wip-plumbing forge observe` (ADR-0018, step-04 / Lane B): map an observed
# forge PR/MR state to a transition intent. Driven hermetically through the
# step-02 transport seam (WIP_FORGE_OBSERVE_CMD = `cat <fixture>.json`) — never a
# real gh/glab, never a network. --branch is passed explicitly so the test does
# not depend on the tmp dir being a git repo.

export WIP_NO_REGISTRY=1

tmp="$(wip_mktemp)"
mkdir -p "$tmp/.wip/initiatives/demo"
cat >"$tmp/.wip.yaml" <<'YAML'
version: 1
features:
  wip: { enabled: true, root: .wip }
  forge: { enabled: true }
current_initiative: demo
initiatives:
  - slug: demo
    status: in-flight
    active_step: step-02
    roadmap: .wip/initiatives/demo/roadmap.md
YAML
printf '# Roadmap — demo\n\n## Round 1 — One\n\n- **step-02 — Second** — current.\n' \
  >"$tmp/.wip/initiatives/demo/roadmap.md"

# Fixture PR/MR payloads. gh uses uppercase state + mergedAt + url; glab uses
# lowercase state + merged_at + web_url — both must normalize identically.
printf '%s' '{"state":"OPEN","mergedAt":null,"url":"http://x/1"}' >"$tmp/open.json"
printf '%s' '{"state":"MERGED","mergedAt":"2026-06-28T00:00:00Z","url":"http://x/1"}' >"$tmp/merged.json"
printf '%s' '{"state":"CLOSED","mergedAt":null,"url":"http://x/1"}' >"$tmp/closed.json"
printf '%s' '{"state":"merged","merged_at":"2026-06-28","web_url":"http://gl/1"}' >"$tmp/glab.json"

obs() { WIP_ROOT="$tmp" WIP_FORGE_OBSERVE_CMD="cat $1" bin/wip-plumbing forge observe --branch feat; }

# --- intent mapping ---------------------------------------------------------
o1="$(obs "$tmp/open.json")"
assert_eq "true" "$(jq -r '.ok' <<<"$o1")" "open: ok"
assert_eq "demo" "$(jq -r '.initiative' <<<"$o1")" "open: initiative echo"
assert_eq "feat" "$(jq -r '.branch' <<<"$o1")" "open: branch echo"
assert_eq "in-review" "$(jq -r '.intent' <<<"$o1")" "open PR -> in-review"
assert_eq "true" "$(jq -r '.forge.reachable' <<<"$o1")" "open: forge reachable"
assert_eq "OPEN" "$(jq -r '.observed.state' <<<"$o1")" "open: observed state passed through"
assert_eq "0" "$(jq -r '.signals | length' <<<"$o1")" "open: no signals"

o2="$(obs "$tmp/merged.json")"
assert_eq "done" "$(jq -r '.intent' <<<"$o2")" "merged PR -> done"
assert_eq "0" "$(jq -r '.signals | length' <<<"$o2")" "merged: no signals"

o3="$(obs "$tmp/closed.json")"
assert_eq "none" "$(jq -r '.intent' <<<"$o3")" "closed-unmerged -> none"
assert_eq "1" "$(jq -r '.signals | map(select(. == "pr-closed-unmerged")) | length' <<<"$o3")" \
  "closed-unmerged -> pr-closed-unmerged signal"

# glab's lowercase merged shape normalizes to the same intent + url.
o4="$(obs "$tmp/glab.json")"
assert_eq "done" "$(jq -r '.intent' <<<"$o4")" "glab merged -> done"
assert_eq "http://gl/1" "$(jq -r '.observed.url' <<<"$o4")" "glab web_url normalized to url"

# --- config pin (.features.forge.backend) reaches detection (step-06) -------
# Pin the backend in the manifest; with WIP_FORGE_CLI unset, _wip_forge_detect's
# config layer selects it (authoritative as a value, regardless of PATH) and it
# surfaces via the already-emitted .forge.cli. Proves C2's read+pass wiring
# end-to-end. Restore the fixture afterward so later assertions are unaffected.
WIP_ROOT="$tmp" yq -i '.features.forge.backend = "glab"' "$tmp/.wip.yaml"
oc="$(
  unset WIP_FORGE_CLI
  obs "$tmp/open.json"
)"
assert_eq "glab" "$(jq -r '.forge.cli' <<<"$oc")" "config pin backend=glab -> .forge.cli glab"
WIP_ROOT="$tmp" yq -i 'del(.features.forge.backend)' "$tmp/.wip.yaml"

# --- no PR / forge didn't answer (cmd exits nonzero) ------------------------
o5="$(WIP_ROOT="$tmp" WIP_FORGE_OBSERVE_CMD="false" bin/wip-plumbing forge observe --branch feat)"
assert_eq "none" "$(jq -r '.intent' <<<"$o5")" "no PR -> intent none"
assert_eq "null" "$(jq -r '.forge.reachable' <<<"$o5")" "no PR -> reachability unknown"
assert_eq "null" "$(jq -r '.observed' <<<"$o5")" "no PR -> observed null"

# --- forge_available echo: declared vs not ----------------------------------
assert_eq "true" "$(jq -r '.forge.available' <<<"$(obs "$tmp/open.json")")" "forge declared -> available true"
WIP_ROOT="$tmp" yq -i '.features.forge.enabled = false' "$tmp/.wip.yaml"
assert_eq "false" "$(jq -r '.forge.available' <<<"$(obs "$tmp/open.json")")" "forge undeclared -> available false"
assert_eq "in-review" "$(jq -r '.intent' <<<"$(obs "$tmp/open.json")")" "intent still computed when undeclared (informational)"
WIP_ROOT="$tmp" yq -i '.features.forge.enabled = true' "$tmp/.wip.yaml"

# --- error envelopes --------------------------------------------------------
set +e
WIP_ROOT="$tmp" bin/wip-plumbing forge >/dev/null 2>&1
assert_eq "2" "$?" "missing subcommand -> exit 2"
WIP_ROOT="$tmp" bin/wip-plumbing forge bogus >/dev/null 2>&1
assert_eq "2" "$?" "unknown subcommand -> exit 2"
WIP_ROOT="$tmp" bin/wip-plumbing forge observe --initiative nope --branch feat >/dev/null 2>&1
assert_eq "3" "$?" "unknown initiative -> exit 3"
WIP_ROOT="$tmp" bin/wip-plumbing forge observe --branch >/dev/null 2>&1
assert_eq "2" "$?" "--branch without arg -> exit 2"
set -e

test_summary
