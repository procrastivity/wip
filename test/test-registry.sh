#!/usr/bin/env bash
set -euo pipefail
cd "$(dirname "$0")/.."
_WIP_TEST_NAME="registry"
# shellcheck source=test/helpers.sh
source test/helpers.sh

# Load the registry lib directly to unit-test encode/decode.
# shellcheck source=lib/wip/wip-plumbing-registry-lib.bash
export WIP_VERBOSE=0
export WIP_QUIET=0
source lib/wip/wip-plumbing-registry-lib.bash

# --- segment encode/decode bijection (spaces, dots) ---
for p in \
  "/Users/beausimensen/Code/wip" \
  "/tmp/has space/in.it" \
  "/a.b/c.d/e.f" \
  "/var/folders/x_y/T/tmp.AbCd"; do
  enc="$(wip_registry_segment_encode "$p")"
  dec="$(wip_registry_segment_decode "$enc")"
  assert_eq "$p" "$dec" "encode/decode bijection: $p"
done

tmp="$(mktemp -d)"
trap 'rm -rf "$tmp"' EXIT
mkdir -p "$tmp/proj"
cat >"$tmp/proj/.wip.yaml" <<'YAML'
version: 1
features: {}
YAML

reg="$tmp/state/projects.jsonl"
export WIP_REGISTRY_FILE="$reg"
export XDG_STATE_HOME="$tmp/state"

# 1. First touch creates the file with one record.
out="$(WIP_ROOT="$tmp/proj" bin/wip-plumbing detect)"
assert_eq "true" "$(jq -r '.ok' <<<"$out")" "detect ok with registry path"
assert_eq "1" "$(grep -c . "$reg")" "registry has one line"
id_expected="-$(printf '%s' "$tmp/proj" | sed 's|^/||; s|/|-|g')"
assert_eq "$id_expected" "$(jq -r '.id' "$reg")" "id is dash-encoded path"
assert_eq "$tmp/proj" "$(jq -r '.path' "$reg")" "path field is abs"
assert_eq "null" "$(jq -r '.slug' "$reg")" "slug defaults to null"

# 2. Second touch within 60s is a no-op (mtime unchanged).
mtime1="$(stat -f %m "$reg" 2>/dev/null || stat -c %Y "$reg")"
sleep 1
WIP_ROOT="$tmp/proj" bin/wip-plumbing detect >/dev/null
mtime2="$(stat -f %m "$reg" 2>/dev/null || stat -c %Y "$reg")"
assert_eq "$mtime1" "$mtime2" "fast-path: mtime unchanged within 60s"

# 3. Backdate last_seen -> slow-path rewrite.
jq -c '.last_seen = "2000-01-01T00:00:00Z"' "$reg" >"$reg.tmp" && mv "$reg.tmp" "$reg"
WIP_ROOT="$tmp/proj" bin/wip-plumbing detect >/dev/null
last_after="$(jq -r '.last_seen' "$reg")"
[[ "$last_after" != "2000-01-01T00:00:00Z" ]] && pass_rewrite=1 || pass_rewrite=0
assert_eq "1" "$pass_rewrite" "slow-path rewrites last_seen after backdate"

# 4. Slug change propagates.
cat >"$tmp/proj/.wip.yaml" <<'YAML'
version: 1
slug: alpha
features: {}
YAML
# Force slow path by backdating again.
jq -c '.last_seen = "2000-01-01T00:00:00Z"' "$reg" >"$reg.tmp" && mv "$reg.tmp" "$reg"
WIP_ROOT="$tmp/proj" bin/wip-plumbing detect >/dev/null
assert_eq "alpha" "$(jq -r '.slug' "$reg")" "slug propagated from .wip.yaml"

# 5. WIP_NO_REGISTRY=1 suppresses writes.
rm -f "$reg"
WIP_NO_REGISTRY=1 WIP_ROOT="$tmp/proj" bin/wip-plumbing detect >/dev/null
if [[ ! -f "$reg" ]]; then suppress_env=1; else suppress_env=0; fi
assert_eq "1" "$suppress_env" "WIP_NO_REGISTRY=1 suppresses writes"

# 6. plumbing.register: false suppresses writes.
cat >"$tmp/proj/.wip.yaml" <<'YAML'
version: 1
plumbing:
  register: false
features: {}
YAML
WIP_ROOT="$tmp/proj" bin/wip-plumbing detect >/dev/null
if [[ ! -f "$reg" ]]; then suppress_manifest=1; else suppress_manifest=0; fi
assert_eq "1" "$suppress_manifest" "plumbing.register:false suppresses writes"

# 7. Unwritable XDG state dir -> verb still exits 0 with valid JSON.
cat >"$tmp/proj/.wip.yaml" <<'YAML'
version: 1
features: {}
YAML
locked="$tmp/locked"
mkdir -p "$locked"
chmod 000 "$locked"
set +e
XDG_STATE_HOME="$locked" WIP_REGISTRY_FILE="$locked/wip/projects.jsonl" \
  WIP_ROOT="$tmp/proj" bin/wip-plumbing detect >"$tmp/out.json" 2>/dev/null
rc=$?
set -e
chmod 755 "$locked"
assert_eq "0" "$rc" "unwritable XDG state -> verb still exit 0"
assert_eq "true" "$(jq -r '.ok' <"$tmp/out.json")" "verb still emits ok:true JSON"

test_summary
