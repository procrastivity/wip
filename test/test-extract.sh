#!/usr/bin/env bash
set -euo pipefail
cd "$(dirname "$0")/.."
_WIP_TEST_NAME="extract"
# shellcheck source=test/helpers.sh
source test/helpers.sh

tmp="$(mktemp -d)"
trap 'rm -rf "$tmp"' EXIT
export WIP_NO_REGISTRY=1

# build_lds_enabled_root <dir>
#   Minimal LDS-enabled tempdir consumer with engineering/.lds-manifest.yaml
#   left empty; per-test cases overwrite it with the manifest they want
#   extract to consume.
build_lds_enabled_root() {
  local dir="$1"
  mkdir -p "$dir/engineering/decisions" "$dir/engineering/specs" "$dir/legacy"
  cat >"$dir/.wip.yaml" <<'YAML'
version: 1
features:
  lds:
    enabled: true
    root: engineering
YAML
  : >"$dir/engineering/.lds-manifest.yaml"
}

# write_manifest <dir> <yaml-body>
write_manifest() {
  printf '%s' "$2" >"$1/engineering/.lds-manifest.yaml"
}

# --- 1. Happy path: verbatim + content modes both write. ----------------------
d1="$tmp/c1"
build_lds_enabled_root "$d1"
cat >"$d1/legacy/source.md" <<'EOF'
line 1
line 2
line 3
line 4
line 5
EOF
write_manifest "$d1" '
metadata:
  schema_version: "1.0.0"
  status: approved
  eng_docs_dir: engineering
entries:
  - id: adr-verbatim
    source:
      file: legacy/source.md
      start_line: 2
      end_line: 4
    target: decisions/0001-verbatim.md
    mode: verbatim
    confidence: high
    classification_reason: testing
  - id: spec-inline
    target: specs/inline.md
    mode: content
    confidence: high
    classification_reason: testing
    inline_content: |
      # Inline spec body
      one
      two
'
out="$(WIP_ROOT="$d1" bin/wip-plumbing extract 2>/dev/null)"
assert_eq "true" "$(jq -r '.ok' <<<"$out")" "[happy] ok:true"
assert_eq "2" "$(jq -r '.entries_total' <<<"$out")" "[happy] total=2"
assert_eq "2" "$(jq -r '.wrote | length' <<<"$out")" "[happy] wrote=2"
assert_eq "skipped-v1" "$(jq -r '.hash_verification' <<<"$out")" "[happy] hash skipped"
assert_file "$d1/engineering/decisions/0001-verbatim.md" "[happy] verbatim target"
assert_file "$d1/engineering/specs/inline.md" "[happy] content target"
assert_grep '<!-- Migrated from legacy/source.md:2-4 -->' \
  "$d1/engineering/decisions/0001-verbatim.md" "[happy] verbatim attribution"
assert_grep '^line 2' "$d1/engineering/decisions/0001-verbatim.md" "[happy] verbatim body start"
assert_grep '^line 4' "$d1/engineering/decisions/0001-verbatim.md" "[happy] verbatim body end"
assert_not_grep '^line 5' "$d1/engineering/decisions/0001-verbatim.md" "[happy] line range respected"
assert_grep '<!-- Generated content - no source file -->' \
  "$d1/engineering/specs/inline.md" "[happy] content attribution"
assert_grep '^# Inline spec body' "$d1/engineering/specs/inline.md" "[happy] content body"

# --- 2. Unsupported modes get skipped (not failed). ---------------------------
d2="$tmp/c2"
build_lds_enabled_root "$d2"
echo "src" >"$d2/legacy/x.md"
write_manifest "$d2" '
metadata:
  schema_version: "1.0.0"
  status: approved
entries:
  - id: ok
    source: legacy/x.md
    target: decisions/0001-ok.md
    mode: verbatim
    confidence: high
    classification_reason: testing
  - id: skip-transform
    source: legacy/x.md
    target: decisions/0002-tr.md
    mode: transform
    confidence: high
    classification_reason: testing
    transform_config:
      type: heading_adjust
      options:
        level_offset: 1
  - id: skip-summarize
    source: legacy/x.md
    target: decisions/0003-sum.md
    mode: summarize
    confidence: high
    classification_reason: testing
'
out="$(WIP_ROOT="$d2" bin/wip-plumbing extract 2>/dev/null)"
assert_eq "true" "$(jq -r '.ok' <<<"$out")" "[unsupported] ok:true"
assert_eq "1" "$(jq -r '.wrote | length' <<<"$out")" "[unsupported] wrote=1"
assert_eq "2" "$(jq -r '.unsupported | length' <<<"$out")" "[unsupported] count=2"
assert_eq "transform" "$(jq -r '.unsupported[] | select(.id=="skip-transform") | .mode' <<<"$out")" \
  "[unsupported] transform mode"
assert_eq "summarize" "$(jq -r '.unsupported[] | select(.id=="skip-summarize") | .mode' <<<"$out")" \
  "[unsupported] summarize mode"
assert_file "$d2/engineering/decisions/0001-ok.md" "[unsupported] supported wrote"
assert_absent "$d2/engineering/decisions/0002-tr.md" "[unsupported] transform not written"

# --- 3. Multi-file source → unsupported-source. -------------------------------
d3="$tmp/c3"
build_lds_enabled_root "$d3"
echo "a" >"$d3/legacy/a.md"
echo "b" >"$d3/legacy/b.md"
write_manifest "$d3" '
metadata:
  schema_version: "1.0.0"
  status: approved
entries:
  - id: multi
    source:
      files:
        - file: legacy/a.md
        - file: legacy/b.md
      separator: "\n---\n"
    target: specs/multi.md
    mode: verbatim
    confidence: high
    classification_reason: testing
'
out="$(WIP_ROOT="$d3" bin/wip-plumbing extract 2>/dev/null)"
assert_eq "1" "$(jq -r '.unsupported | length' <<<"$out")" "[multi-file] unsupported=1"
assert_eq "multi-file" "$(jq -r '.unsupported[0].source_kind' <<<"$out")" "[multi-file] kind"
assert_absent "$d3/engineering/specs/multi.md" "[multi-file] not written"

# --- 4. Manifest not approved → exit 4 manifest-not-approved. -----------------
d4="$tmp/c4"
build_lds_enabled_root "$d4"
write_manifest "$d4" '
metadata:
  schema_version: "1.0.0"
  status: pending
entries:
  - id: x
    source: legacy/x.md
    target: decisions/0001-x.md
    mode: verbatim
    confidence: high
    classification_reason: testing
'
set +e
out="$(WIP_ROOT="$d4" bin/wip-plumbing extract 2>/dev/null)"
rc=$?
set -e
assert_eq "4" "$rc" "[not-approved] exit 4"
assert_eq "manifest-not-approved" "$(jq -r '.error.kind' <<<"$out")" "[not-approved] kind"

# --- 5. Incompatible schema_version → exit 4 incompatible-schema. -------------
d5="$tmp/c5"
build_lds_enabled_root "$d5"
write_manifest "$d5" '
metadata:
  schema_version: "2.0.0"
  status: approved
entries:
  - id: x
    source: legacy/x.md
    target: decisions/0001-x.md
    mode: verbatim
    confidence: high
    classification_reason: testing
'
set +e
out="$(WIP_ROOT="$d5" bin/wip-plumbing extract 2>/dev/null)"
rc=$?
set -e
assert_eq "4" "$rc" "[bad-schema] exit 4"
assert_eq "incompatible-schema" "$(jq -r '.error.kind' <<<"$out")" "[bad-schema] kind"

# --- 6. Empty entries → exit 4 manifest-empty. --------------------------------
d6="$tmp/c6"
build_lds_enabled_root "$d6"
write_manifest "$d6" '
metadata:
  schema_version: "1.0.0"
  status: approved
entries: []
'
set +e
out="$(WIP_ROOT="$d6" bin/wip-plumbing extract 2>/dev/null)"
rc=$?
set -e
assert_eq "4" "$rc" "[empty] exit 4"
assert_eq "manifest-empty" "$(jq -r '.error.kind' <<<"$out")" "[empty] kind"

# --- 7. Idempotent: 2nd run all skipped. --------------------------------------
d7="$tmp/c7"
build_lds_enabled_root "$d7"
echo "hello" >"$d7/legacy/h.md"
write_manifest "$d7" '
metadata:
  schema_version: "1.0.0"
  status: approved
entries:
  - id: one
    source: legacy/h.md
    target: decisions/0001-h.md
    mode: verbatim
    confidence: high
    classification_reason: t
'
WIP_ROOT="$d7" bin/wip-plumbing extract >/dev/null 2>&1
out="$(WIP_ROOT="$d7" bin/wip-plumbing extract 2>/dev/null)"
assert_eq "true" "$(jq -r '.ok' <<<"$out")" "[idem] ok"
assert_eq "0" "$(jq -r '.wrote | length' <<<"$out")" "[idem] wrote=0"
assert_eq "1" "$(jq -r '.skipped_idempotent | length' <<<"$out")" "[idem] skipped=1"

# --- 8. Content drift → exit 4; --force overwrites. ---------------------------
d8="$tmp/c8"
build_lds_enabled_root "$d8"
echo "orig" >"$d8/legacy/h.md"
write_manifest "$d8" '
metadata:
  schema_version: "1.0.0"
  status: approved
entries:
  - id: one
    source: legacy/h.md
    target: decisions/0001-h.md
    mode: verbatim
    confidence: high
    classification_reason: t
'
WIP_ROOT="$d8" bin/wip-plumbing extract >/dev/null 2>&1
echo "tampered" >>"$d8/engineering/decisions/0001-h.md"
set +e
out="$(WIP_ROOT="$d8" bin/wip-plumbing extract 2>/dev/null)"
rc=$?
set -e
assert_eq "4" "$rc" "[drift] exit 4"
assert_eq "content-drift" "$(jq -r '.error.kind' <<<"$out")" "[drift] kind"
assert_eq "1" "$(jq -r '.error.paths | length' <<<"$out")" "[drift] paths=1"
out="$(WIP_ROOT="$d8" bin/wip-plumbing extract --force 2>/dev/null)"
assert_eq "true" "$(jq -r '.ok' <<<"$out")" "[--force] ok"
assert_eq "1" "$(jq -r '.wrote_forced | length' <<<"$out")" "[--force] wrote_forced=1"

# --- 9. --manifest override. --------------------------------------------------
d9="$tmp/c9"
build_lds_enabled_root "$d9"
echo "ovr" >"$d9/legacy/o.md"
cat >"$d9/custom-manifest.yaml" <<'EOF'
metadata:
  schema_version: "1.0.0"
  status: approved
entries:
  - id: one
    source: legacy/o.md
    target: decisions/0001-o.md
    mode: verbatim
    confidence: high
    classification_reason: t
EOF
out="$(WIP_ROOT="$d9" bin/wip-plumbing extract --manifest custom-manifest.yaml 2>/dev/null)"
assert_eq "true" "$(jq -r '.ok' <<<"$out")" "[--manifest] ok"
assert_eq "custom-manifest.yaml" "$(jq -r '.manifest' <<<"$out")" "[--manifest] path echoed"
assert_file "$d9/engineering/decisions/0001-o.md" "[--manifest] target written"

# --- 10. LDS disabled / sentinel missing — same shape as graduate. ------------
d10="$tmp/c10"
mkdir -p "$d10"
cat >"$d10/.wip.yaml" <<'YAML'
version: 1
features:
  lds:
    enabled: false
YAML
set +e
out="$(WIP_ROOT="$d10" bin/wip-plumbing extract 2>/dev/null)"
rc=$?
set -e
assert_eq "3" "$rc" "[lds-disabled] exit 3"
assert_eq "lds-not-enabled" "$(jq -r '.error.kind' <<<"$out")" "[lds-disabled] kind"

d10b="$tmp/c10b"
mkdir -p "$d10b/engineering"
cat >"$d10b/.wip.yaml" <<'YAML'
version: 1
features:
  lds:
    enabled: true
    root: engineering
YAML
set +e
out="$(WIP_ROOT="$d10b" bin/wip-plumbing extract 2>/dev/null)"
rc=$?
set -e
assert_eq "3" "$rc" "[no-sentinel] exit 3"
assert_eq "lds-sentinel-missing" "$(jq -r '.error.kind' <<<"$out")" "[no-sentinel] kind"

# --- 11. Duplicate entry ids → exit 4 duplicate-entry-id. ---------------------
d11="$tmp/c11"
build_lds_enabled_root "$d11"
echo "x" >"$d11/legacy/x.md"
write_manifest "$d11" '
metadata:
  schema_version: "1.0.0"
  status: approved
entries:
  - id: dup
    source: legacy/x.md
    target: decisions/0001-a.md
    mode: verbatim
    confidence: high
    classification_reason: t
  - id: dup
    source: legacy/x.md
    target: decisions/0002-b.md
    mode: verbatim
    confidence: high
    classification_reason: t
'
set +e
out="$(WIP_ROOT="$d11" bin/wip-plumbing extract 2>/dev/null)"
rc=$?
set -e
assert_eq "4" "$rc" "[dup-id] exit 4"
assert_eq "duplicate-entry-id" "$(jq -r '.error.kind' <<<"$out")" "[dup-id] kind"

# --- 12. Bad-shape entry: verbatim with no source. ----------------------------
d12="$tmp/c12"
build_lds_enabled_root "$d12"
write_manifest "$d12" '
metadata:
  schema_version: "1.0.0"
  status: approved
entries:
  - id: bad
    target: decisions/0001-bad.md
    mode: verbatim
    confidence: high
    classification_reason: t
'
set +e
out="$(WIP_ROOT="$d12" bin/wip-plumbing extract 2>/dev/null)"
rc=$?
set -e
assert_eq "4" "$rc" "[bad-shape] exit 4"
assert_eq "bad-entry-shape" "$(jq -r '.error.kind' <<<"$out")" "[bad-shape] kind"

# --- 13. Template field bumps entry to unsupported (v1 deferral). -------------
d13="$tmp/c13"
build_lds_enabled_root "$d13"
echo "x" >"$d13/legacy/x.md"
write_manifest "$d13" '
metadata:
  schema_version: "1.0.0"
  status: approved
entries:
  - id: tpl
    source: legacy/x.md
    target: decisions/0001-tpl.md
    mode: verbatim
    confidence: high
    classification_reason: t
    template: adr-madr-minimal.md
    field_mappings:
      title: Hello
'
out="$(WIP_ROOT="$d13" bin/wip-plumbing extract 2>/dev/null)"
assert_eq "true" "$(jq -r '.ok' <<<"$out")" "[template] ok"
assert_eq "1" "$(jq -r '.unsupported | length' <<<"$out")" "[template] unsupported=1"
assert_eq "template/field_mappings not supported in v1" \
  "$(jq -r '.unsupported[0].reason' <<<"$out")" "[template] reason"
assert_absent "$d13/engineering/decisions/0001-tpl.md" "[template] not written"

test_summary
