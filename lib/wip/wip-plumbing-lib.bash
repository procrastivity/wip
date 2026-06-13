# wip-plumbing-lib.bash — shared helpers. Sourced by bin/wip-plumbing.
# Pure bash + jq + yq. No LLM, no network.
# shellcheck shell=bash

WIP_VERSION="0.1.0-dev"

wip_version() { printf '%s\n' "$WIP_VERSION"; }

wip_usage() {
  cat <<'EOF'
wip-plumbing — deterministic core for wip

usage: wip-plumbing [global flags] <command> [args]

commands:
  detect    report features + initiatives from .wip.yaml (mandatory first call)
  doctor    verify the manifest against disk; report drift  [--fix advisory in v1]
  project   list / register / resolve / forget entries in the global registry
  init      scaffold manifest and/or initiative from templates/
            usage: init [<slug>] [--title <t>] [--intake ad-hoc|structured]
  intake    classify / validate / apply inbound planning artifacts
            usage: intake classify <file>
                   intake validate <file> [--kind brief|amendment|workplan-seed|spec|handoff]
                   intake apply <file> --kind <k> [--target <slug|slug/step>]
  status    where am I: initiative / round / active step / dirty .wip files
            usage: status [--initiative <slug>]
  next      ranked candidates for what to do next
            usage: next [--initiative <slug>]

global flags:
  -h, --help        print this and exit 0
  --version         print version and exit 0
  -v, --verbose     extra diagnostics on stderr
  -q, --quiet       suppress non-error stderr
  --json|--no-json  structured output on stdout (default: json)
  --dry-run         print the write ledger; touch nothing
  --project <id>    operate on the named project (abs path / segment / slug)

env:
  WIP_LIB           override lib/wip/ path (dev installs)
  WIP_ROOT          force repo root, skipping the walk-up
  WIP_REGISTRY_FILE override the global registry path
  WIP_NO_REGISTRY=1 suppress registry reads and writes
EOF
}

# Diagnostic line to stderr (suppressed by --quiet).
wip_warn() { [[ "${WIP_QUIET:-0}" == "1" ]] || printf 'wip-plumbing: %s\n' "$*" >&2; }

# wip_die <code> <kind> <message> [path] — emit the error envelope (stdout JSON
# when --json) + a prose line (stderr), then exit <code>.
wip_die() {
  local code="$1" kind="$2" msg="$3" path="${4:-}"
  if [[ "${WIP_JSON:-1}" == "1" ]]; then
    jq -nc --argjson code "$code" --arg kind "$kind" --arg msg "$msg" --arg path "$path" \
      '{ok:false, error: ({code:$code, kind:$kind, message:$msg} + (if $path=="" then {} else {path:$path} end))}'
  fi
  printf 'wip-plumbing: %s\n' "$msg" >&2
  exit "$code"
}

# wip_find_root — echo the repo root (nearest ancestor with .wip.yaml). Honors
# $WIP_ROOT as an override. Returns 1 if none found. On success, fires
# wip_registry_touch_root (best-effort; never fails the caller).
wip_find_root() {
  local root=""
  if [[ -n "${WIP_ROOT:-}" ]]; then
    if [[ -f "$WIP_ROOT/.wip.yaml" ]]; then
      root="$WIP_ROOT"
    else
      return 1
    fi
  else
    local d="$PWD"
    while :; do
      if [[ -f "$d/.wip.yaml" ]]; then
        root="$d"
        break
      fi
      [[ "$d" == "/" ]] && break
      d="$(dirname "$d")"
    done
    [[ -n "$root" ]] || return 1
  fi
  # Best-effort registry touch — declared by the registry lib when sourced.
  if declare -F wip_registry_touch_root >/dev/null 2>&1; then
    wip_registry_touch_root "$root" 2>/dev/null || true
  fi
  printf '%s\n' "$root"
  return 0
}

# wip_manifest_json <root> — convert .wip.yaml to JSON once. Empty output on parse error.
wip_manifest_json() {
  local root="$1"
  yq -o=json '.' "$root/.wip.yaml" 2>/dev/null
}

# Emit "name <US> enabled <US> sentinel" per declared feature, where <US> is the
# 0x1f unit separator (a non-whitespace delimiter, so empty fields survive `read`).
# Sentinel is the declared-feature contract (ADR-0002); empty when the feature has
# no on-disk sentinel (active when enabled).
_wip_feature_records() {
  jq -r '
    def sentinel($n; $f):
      if   $n == "lds"       then (($f.root // ($f.installs[0].root // "engineering")) + "/.lds-manifest.yaml")
      elif $n == "diataxis"  then (($f.root // "docs") + "/README.md")
      elif $n == "changelog" then "CHANGELOG.md"
      elif $n == "direnv"    then ".envrc"
      else "" end;
    (.features // {}) | to_entries[]
    | [ .key, ((.value.enabled // false) | tostring), sentinel(.key; .value) ] | join("")'
}

# wip_features_json <root> <manifest-json> — JSON array of resolved feature
# objects: {name, enabled, active, sentinel, sentinel_exists?, drift?}.
wip_features_json() {
  local root="$1" mj="$2"
  local arr="[]" name enabled sentinel exists active drift obj
  while IFS=$'\037' read -r name enabled sentinel; do
    [[ -n "$name" ]] || continue
    exists="null"
    drift=""
    if [[ -n "$sentinel" ]]; then
      if [[ -e "$root/$sentinel" ]]; then exists="true"; else exists="false"; fi
    fi
    if [[ "$enabled" == "true" ]]; then
      if [[ -z "$sentinel" ]]; then
        active="true"
      elif [[ "$exists" == "true" ]]; then
        active="true"
      else
        active="false"
        drift="declared-but-missing"
      fi
    else
      active="false"
      [[ -n "$sentinel" && "$exists" == "true" ]] && drift="present-but-undeclared"
    fi
    obj="$(jq -nc \
      --arg name "$name" --argjson enabled "$enabled" --arg sentinel "$sentinel" \
      --argjson exists "$exists" --argjson active "$active" --arg drift "$drift" '
      {name:$name, enabled:$enabled, active:$active,
       sentinel:(if $sentinel == "" then null else $sentinel end)}
      + (if $exists == null then {} else {sentinel_exists:$exists} end)
      + (if $drift == "" then {} else {drift:$drift} end)')"
    arr="$(jq -nc --argjson a "$arr" --argjson o "$obj" '$a + [$o]')"
  done < <(printf '%s' "$mj" | _wip_feature_records)
  printf '%s' "$arr"
}
