# wip-porcelain-lib.bash — shared helpers for the `wip` porcelain.
# Mirrors the shape of wip-plumbing-lib.bash but is intentionally separate so
# the two layers (ADR-0001) rev independently. Pure bash + jq + curl.
# shellcheck shell=bash

WIP_PORCELAIN_VERSION="0.2.0-dev"

wip_p_version() { printf '%s\n' "$WIP_PORCELAIN_VERSION"; }

wip_p_usage() {
  cat <<'EOF'
wip — porcelain over wip-plumbing, with an OpenAI-compatible provider

usage: wip [global flags] <command> [args]

porcelain commands (LLM-aware):
  ask         single-turn chat completion via the resolved provider
              usage: ask [<prompt>|-] [--system <text>]
                     (prompt arg wins over stdin; `-` reads stdin)
  intake      LLM-driven shape/route pipeline for inbound artifacts
              usage: intake <file> [--kind <k>] [--target <slug|slug/step>]
                            [--yes] [--dry-run] [--output <path>]
                            [--max-rounds <n>]
  bundle      assemble N handoff files into one bundle lead manifest, then
              optionally chain into the intake explode
              usage: bundle <file>... [--target <slug>]
                            [--lead-as brief|amendment] [-o <manifest>]
                            [--intake] [--dry-run] [--yes]
  provider    inspect provider config
              usage: provider show [--json|--no-json]

every other command is forwarded verbatim to wip-plumbing. Run
`wip-plumbing --help` for the deterministic verb surface (detect / doctor /
project / init / intake / status / next / roadmap / workplan).

global flags:
  -h, --help        print this and exit 0
  --version         print porcelain version and exit 0
  -v, --verbose     extra diagnostics on stderr

env:
  WIP_LLM_BASE_URL  base URL of the OpenAI-compatible endpoint
                    (pointer name comes from .wip.yaml provider.base_url_env)
  WIP_LLM_API_KEY   bearer token (provider.api_key_env)
  WIP_LLM_MODEL     model identifier (provider.model_env)
  WIP_PLUMBING_BIN  path to wip-plumbing binary (default: sibling of `wip`)
  WIP_PROVIDER_CMD  test seam — when set, the provider HTTP call is replaced
                    by `bash -c "$WIP_PROVIDER_CMD"`, fed the request JSON on
                    stdin, with its stdout read as the response JSON.
EOF
}

# Diagnostic line to stderr. Always uses the `wip:` prefix so a mixed
# stderr stream from both binaries stays disambiguable.
wip_p_warn() { printf 'wip: %s\n' "$*" >&2; }

# wip_p_die <code> <kind> <msg> [extra-json] — emit the error envelope to
# stdout (JSON), prose to stderr, then exit. `extra-json` (optional) merges
# additional fields into `error` — e.g. {"env":"WIP_LLM_API_KEY"} so that
# step-10.5 can branch on the missing env name without scraping prose.
wip_p_die() {
  local code="$1" kind="$2" msg="$3" extra="${4:-}"
  if [[ -n "$extra" ]]; then
    jq -nc --argjson code "$code" --arg kind "$kind" --arg msg "$msg" --argjson extra "$extra" \
      '{ok:false, error: ({code:$code, kind:$kind, message:$msg} + $extra)}'
  else
    jq -nc --argjson code "$code" --arg kind "$kind" --arg msg "$msg" \
      '{ok:false, error: {code:$code, kind:$kind, message:$msg}}'
  fi
  printf 'wip: %s\n' "$msg" >&2
  exit "$code"
}

# wip_p_prompt <msg> — ask the user a question, echo their one-line answer.
# When stdin is a pipe / redirect (not a tty), read it — that's the test &
# scripted-input path. Otherwise fall back to /dev/tty for the interactive
# case. Writes the prompt prefix to stderr so stdout stays reserved for
# envelopes. Returns nonzero (no echo) when no input source is available.
wip_p_prompt() {
  local msg="$1" answer=""
  printf 'wip: %s ' "$msg" >&2
  if [[ ! -t 0 ]]; then
    IFS= read -r answer || return 1
  elif [[ -r /dev/tty && -w /dev/tty ]]; then
    IFS= read -r answer </dev/tty || return 1
  else
    printf '\n' >&2
    return 1
  fi
  printf '%s' "$answer"
}

# wip_p_confirm <msg> — yes/no prompt with default-no. Exit 0 on yes, 1 else.
wip_p_confirm() {
  local msg="$1" answer
  answer="$(wip_p_prompt "$msg [y/N]")" || return 1
  case "$answer" in
    y | Y | yes | YES | Yes) return 0 ;;
    *) return 1 ;;
  esac
}

# wip_p_find_plumbing — echo the path to the wip-plumbing binary, or nonzero.
# Resolution order: $WIP_PLUMBING_BIN, sibling-of-self, $PATH lookup.
wip_p_find_plumbing() {
  if [[ -n "${WIP_PLUMBING_BIN:-}" ]]; then
    [[ -x "$WIP_PLUMBING_BIN" ]] || return 1
    printf '%s\n' "$WIP_PLUMBING_BIN"
    return 0
  fi
  local self_dir
  # shellcheck disable=SC1007  # CDPATH= neutralizes CDPATH, not an assignment
  self_dir="$(CDPATH= cd -- "$(dirname -- "$_WIP_P_SELF")" && pwd)"
  if [[ -x "$self_dir/wip-plumbing" ]]; then
    printf '%s\n' "$self_dir/wip-plumbing"
    return 0
  fi
  local on_path
  on_path="$(command -v wip-plumbing 2>/dev/null || true)"
  if [[ -n "$on_path" && -x "$on_path" ]]; then
    printf '%s\n' "$on_path"
    return 0
  fi
  return 1
}
