#!/usr/bin/env bash
set -euo pipefail
cd "$(dirname "$0")/.."
_WIP_TEST_NAME="graduate"
# shellcheck source=test/helpers.sh
source test/helpers.sh

tmp="$(mktemp -d)"
trap 'rm -rf "$tmp"' EXIT
export WIP_NO_REGISTRY=1

# build_lds_enabled_root <dir> [extra-yaml]
#   Create a minimal LDS-enabled tempdir consumer:
#     - .wip.yaml (features.lds.enabled: true, root: engineering)
#     - engineering/.lds-manifest.yaml (status: approved, entries: [])
build_lds_enabled_root() {
  local dir="$1"
  mkdir -p "$dir/engineering/decisions" "$dir/.wip/initiatives/test"
  cat >"$dir/.wip.yaml" <<'YAML'
version: 1
features:
  lds:
    enabled: true
    root: engineering
current_initiative: test
initiatives:
  - slug: test
    title: Test
    status: in-flight
    brief: .wip/initiatives/test/BRIEF.md
    roadmap: .wip/initiatives/test/roadmap.md
YAML
  cat >"$dir/engineering/.lds-manifest.yaml" <<'YAML'
metadata:
  schema_version: "1.0.0"
  status: approved
  eng_docs_dir: engineering
entries: []
YAML
  : >"$dir/.wip/initiatives/test/BRIEF.md"
  : >"$dir/.wip/initiatives/test/roadmap.md"
}

# write_artifact <path> <graduate-to> <body>
write_artifact() {
  local path="$1" gto="$2" body="$3"
  mkdir -p "$(dirname "$path")"
  if [[ -n "$gto" ]]; then
    {
      printf -- '---\n'
      printf 'graduate-to: %s\n' "$gto"
      printf -- '---\n'
      printf '%s\n' "$body"
    } >"$path"
  else
    printf '%s\n' "$body" >"$path"
  fi
}

# --- 1. Happy path: front-matter graduate-to → write target. ------------------
d1="$tmp/c1"
build_lds_enabled_root "$d1"
write_artifact "$d1/.wip/initiatives/test/scratch/foo.md" \
  "decisions/0001-foo.md" \
  "# 0001 — Foo

- Status: accepted

## Context

Reasoning here."
out="$(WIP_ROOT="$d1" bin/wip-plumbing graduate \
  "$d1/.wip/initiatives/test/scratch/foo.md" 2>/dev/null)"
assert_eq "true" "$(jq -r '.ok' <<<"$out")" "[happy] ok:true"
assert_eq "engineering/decisions/0001-foo.md" \
  "$(jq -r '.target' <<<"$out")" "[happy] target"
assert_file "$d1/engineering/decisions/0001-foo.md" "[happy] target written"
assert_grep '^# 0001 — Foo' "$d1/engineering/decisions/0001-foo.md" "[happy] body preserved"
assert_not_grep 'graduate-to' "$d1/engineering/decisions/0001-foo.md" "[happy] directive stripped"

# --- 2. LDS disabled → exit 3 lds-not-enabled. --------------------------------
d2="$tmp/c2"
mkdir -p "$d2"
cat >"$d2/.wip.yaml" <<'YAML'
version: 1
features:
  lds:
    enabled: false
YAML
write_artifact "$d2/a.md" "decisions/0001-x.md" "# x"
set +e
out="$(WIP_ROOT="$d2" bin/wip-plumbing graduate "$d2/a.md" 2>/dev/null)"
rc=$?
set -e
assert_eq "3" "$rc" "[lds-disabled] exit 3"
assert_eq "lds-not-enabled" "$(jq -r '.error.kind' <<<"$out")" "[lds-disabled] kind"

# --- 3. LDS enabled but sentinel missing → exit 3 lds-sentinel-missing. -------
d3="$tmp/c3"
mkdir -p "$d3"
cat >"$d3/.wip.yaml" <<'YAML'
version: 1
features:
  lds:
    enabled: true
    root: engineering
YAML
write_artifact "$d3/a.md" "decisions/0001-x.md" "# x"
set +e
out="$(WIP_ROOT="$d3" bin/wip-plumbing graduate "$d3/a.md" 2>/dev/null)"
rc=$?
set -e
assert_eq "3" "$rc" "[no-sentinel] exit 3"
assert_eq "lds-sentinel-missing" "$(jq -r '.error.kind' <<<"$out")" "[no-sentinel] kind"

# --- 4. No graduate-to directive and no --to → exit 4 no-target. --------------
d4="$tmp/c4"
build_lds_enabled_root "$d4"
write_artifact "$d4/.wip/initiatives/test/scratch/bare.md" "" "# Body only"
set +e
out="$(WIP_ROOT="$d4" bin/wip-plumbing graduate \
  "$d4/.wip/initiatives/test/scratch/bare.md" 2>/dev/null)"
rc=$?
set -e
assert_eq "4" "$rc" "[no-target] exit 4"
assert_eq "no-target" "$(jq -r '.error.kind' <<<"$out")" "[no-target] kind"

# --- 5. --to overrides front-matter. ------------------------------------------
d5="$tmp/c5"
build_lds_enabled_root "$d5"
write_artifact "$d5/.wip/initiatives/test/scratch/x.md" \
  "decisions/0001-a.md" "# 0001 — A"
out="$(WIP_ROOT="$d5" bin/wip-plumbing graduate \
  "$d5/.wip/initiatives/test/scratch/x.md" --to decisions/0002-b.md 2>/dev/null)"
assert_eq "engineering/decisions/0002-b.md" \
  "$(jq -r '.target' <<<"$out")" "[--to override] target"
assert_file "$d5/engineering/decisions/0002-b.md" "[--to override] written"
assert_absent "$d5/engineering/decisions/0001-a.md" "[--to override] original ignored"

# --- 6. Auto-numbering for decisions/auto-<slug>.md. --------------------------
d6="$tmp/c6"
build_lds_enabled_root "$d6"
: >"$d6/engineering/decisions/0001-existing.md"
: >"$d6/engineering/decisions/0002-also-existing.md"
write_artifact "$d6/.wip/initiatives/test/scratch/baz.md" \
  "decisions/auto-baz.md" "# baz adr"
out="$(WIP_ROOT="$d6" bin/wip-plumbing graduate \
  "$d6/.wip/initiatives/test/scratch/baz.md" 2>/dev/null)"
assert_eq "engineering/decisions/0003-baz.md" \
  "$(jq -r '.target' <<<"$out")" "[auto-NNNN] picks next"
assert_file "$d6/engineering/decisions/0003-baz.md" "[auto-NNNN] written"

# Empty decisions/ → 0001.
d6b="$tmp/c6b"
build_lds_enabled_root "$d6b"
write_artifact "$d6b/.wip/initiatives/test/scratch/first.md" \
  "decisions/auto-first.md" "# first"
out="$(WIP_ROOT="$d6b" bin/wip-plumbing graduate \
  "$d6b/.wip/initiatives/test/scratch/first.md" 2>/dev/null)"
assert_eq "engineering/decisions/0001-first.md" \
  "$(jq -r '.target' <<<"$out")" "[auto-NNNN empty] 0001"

# --- 6c. Auto-NNNN is idempotent: re-running against the same artifact
#         resolves to the same existing target (find-or-create), not max+1.
d6c="$tmp/c6c"
build_lds_enabled_root "$d6c"
write_artifact "$d6c/.wip/initiatives/test/scratch/idem.md" \
  "decisions/auto-idem.md" "# idem adr"
WIP_ROOT="$d6c" bin/wip-plumbing graduate \
  "$d6c/.wip/initiatives/test/scratch/idem.md" >/dev/null 2>&1
out="$(WIP_ROOT="$d6c" bin/wip-plumbing graduate \
  "$d6c/.wip/initiatives/test/scratch/idem.md" 2>/dev/null)"
assert_eq "engineering/decisions/0001-idem.md" \
  "$(jq -r '.target' <<<"$out")" "[auto-idem] same target across runs"
assert_eq "1" "$(jq -r '.skipped_idempotent | length' <<<"$out")" \
  "[auto-idem] second run skips"

# --- 7. Idempotent: second run = skipped, exit 0. -----------------------------
d7="$tmp/c7"
build_lds_enabled_root "$d7"
write_artifact "$d7/.wip/initiatives/test/scratch/dup.md" \
  "decisions/0001-dup.md" "# dup"
WIP_ROOT="$d7" bin/wip-plumbing graduate \
  "$d7/.wip/initiatives/test/scratch/dup.md" >/dev/null 2>&1
out="$(WIP_ROOT="$d7" bin/wip-plumbing graduate \
  "$d7/.wip/initiatives/test/scratch/dup.md" 2>/dev/null)"
assert_eq "true" "$(jq -r '.ok' <<<"$out")" "[idem] ok:true"
assert_eq "0" "$(jq -r '.wrote | length' <<<"$out")" "[idem] wrote empty"
assert_eq "1" "$(jq -r '.skipped_idempotent | length' <<<"$out")" "[idem] skipped 1"

# --- 8. Content drift refuses without --force; --force overwrites. ------------
d8="$tmp/c8"
build_lds_enabled_root "$d8"
write_artifact "$d8/.wip/initiatives/test/scratch/drift.md" \
  "decisions/0001-drift.md" "# original"
WIP_ROOT="$d8" bin/wip-plumbing graduate \
  "$d8/.wip/initiatives/test/scratch/drift.md" >/dev/null 2>&1
echo "tampered" >>"$d8/engineering/decisions/0001-drift.md"
set +e
out="$(WIP_ROOT="$d8" bin/wip-plumbing graduate \
  "$d8/.wip/initiatives/test/scratch/drift.md" 2>/dev/null)"
rc=$?
set -e
assert_eq "4" "$rc" "[drift] exit 4"
assert_eq "content-drift" "$(jq -r '.error.kind' <<<"$out")" "[drift] kind"
assert_eq "engineering/decisions/0001-drift.md" \
  "$(jq -r '.error.path' <<<"$out")" "[drift] path"
# --force overwrites.
out="$(WIP_ROOT="$d8" bin/wip-plumbing graduate \
  "$d8/.wip/initiatives/test/scratch/drift.md" --force 2>/dev/null)"
assert_eq "true" "$(jq -r '.ok' <<<"$out")" "[--force] ok"
assert_eq "1" "$(jq -r '.wrote_forced | length' <<<"$out")" "[--force] wrote_forced"
assert_not_grep 'tampered' "$d8/engineering/decisions/0001-drift.md" \
  "[--force] target restored"

# --- 9. Unknown layer → exit 4 unknown-layer. ---------------------------------
d9="$tmp/c9"
build_lds_enabled_root "$d9"
write_artifact "$d9/.wip/initiatives/test/scratch/bad.md" \
  "decisons/0001-typo.md" "# typo"
set +e
out="$(WIP_ROOT="$d9" bin/wip-plumbing graduate \
  "$d9/.wip/initiatives/test/scratch/bad.md" 2>/dev/null)"
rc=$?
set -e
assert_eq "4" "$rc" "[unknown-layer] exit 4"
assert_eq "unknown-layer" "$(jq -r '.error.kind' <<<"$out")" "[unknown-layer] kind"

# --- 10. Strip ONLY graduate-to; preserve other front-matter keys. ------------
d10="$tmp/c10"
build_lds_enabled_root "$d10"
mkdir -p "$d10/.wip/initiatives/test/scratch"
cat >"$d10/.wip/initiatives/test/scratch/multi-fm.md" <<'EOF'
---
graduate-to: decisions/0001-fm.md
status: accepted
date: 2026-06-14
---

# 0001 — FM keep

Body.
EOF
WIP_ROOT="$d10" bin/wip-plumbing graduate \
  "$d10/.wip/initiatives/test/scratch/multi-fm.md" >/dev/null 2>&1
assert_grep '^status: accepted' "$d10/engineering/decisions/0001-fm.md" \
  "[fm-preserve] status: kept"
assert_grep '^date: 2026-06-14' "$d10/engineering/decisions/0001-fm.md" \
  "[fm-preserve] date: kept"
assert_not_grep 'graduate-to' "$d10/engineering/decisions/0001-fm.md" \
  "[fm-preserve] graduate-to: stripped"

# --- 11. Auto-shorthand outside decisions/ → exit 4 bad-auto-slot. ------------
d11="$tmp/c11"
build_lds_enabled_root "$d11"
write_artifact "$d11/.wip/initiatives/test/scratch/auto-spec.md" \
  "specs/auto-foo.md" "# spec"
set +e
out="$(WIP_ROOT="$d11" bin/wip-plumbing graduate \
  "$d11/.wip/initiatives/test/scratch/auto-spec.md" 2>/dev/null)"
rc=$?
set -e
assert_eq "4" "$rc" "[auto-non-decisions] exit 4"
assert_eq "bad-auto-slot" "$(jq -r '.error.kind' <<<"$out")" "[auto-non-decisions] kind"

# --- 12. graduate to specs/ works (non-auto). ---------------------------------
d12="$tmp/c12"
build_lds_enabled_root "$d12"
mkdir -p "$d12/engineering/specs"
write_artifact "$d12/.wip/initiatives/test/scratch/spec.md" \
  "specs/my-feature.md" "# Spec — my feature"
out="$(WIP_ROOT="$d12" bin/wip-plumbing graduate \
  "$d12/.wip/initiatives/test/scratch/spec.md" 2>/dev/null)"
assert_eq "engineering/specs/my-feature.md" \
  "$(jq -r '.target' <<<"$out")" "[specs-layer] target"
assert_file "$d12/engineering/specs/my-feature.md" "[specs-layer] written"

test_summary
