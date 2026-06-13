# wip-provider-lib.bash — provider config resolution + chat-completions HTTP.
# Pure bash + jq + curl. The only test seam is WIP_PROVIDER_CMD; production
# never sets it, so `make check` stays network-free.
# shellcheck shell=bash

# wip_provider_load <root> — read .wip.yaml's provider: block, resolve the
# three pointer env vars, populate the global WIP_PROVIDER_CFG with a JSON
# config object. On failure calls wip_p_die directly (with one of):
#   - no-provider          : provider: block absent
#   - bad-provider         : kind missing or required *_env field missing
#   - unsupported-provider : kind not in {openai-compatible}
#   - provider-env-unset   : a pointer's env var is unset; error.env names it
#
# IMPORTANT: this function MUST be called in the parent shell (never inside
# a `$()` capture) — wip_p_die writes the error envelope to stdout, and a
# subshell would swallow it. Callers read the result from $WIP_PROVIDER_CFG.
#
# WIP_PROVIDER_CFG JSON shape:
#   {
#     "kind": "openai-compatible",
#     "base_url": "<resolved>",
#     "model":    "<resolved>",
#     "api_key":  "<resolved | empty when explicitly empty>",
#     "api_key_present": <bool>,
#     "env": {
#       "base_url_env": "<env name>",
#       "api_key_env":  "<env name>",
#       "model_env":    "<env name>"
#     }
#   }
#
# An empty-but-explicit api_key (env set to "") is allowed and signalled by
# api_key_present:false; the chat helper then omits the Authorization header.
# An unset env var is exit 3 (the contract). base_url and model env vars must
# resolve non-empty.

# shellcheck disable=SC2034  # consumed by callers across sourced subcommand files
WIP_PROVIDER_CFG=""

wip_provider_load() {
  local root="$1"
  WIP_PROVIDER_CFG=""
  local mj
  mj="$(yq -o=json '.' "$root/.wip.yaml" 2>/dev/null)"
  [[ -n "$mj" ]] || wip_p_die 3 bad-provider "could not parse $root/.wip.yaml"

  local has_provider
  has_provider="$(jq -r 'has("provider")' <<<"$mj")"
  [[ "$has_provider" == "true" ]] ||
    wip_p_die 3 no-provider "no provider: block in $root/.wip.yaml"

  local kind base_env key_env model_env
  kind="$(jq -r '.provider.kind // ""' <<<"$mj")"
  base_env="$(jq -r '.provider.base_url_env // ""' <<<"$mj")"
  key_env="$(jq -r '.provider.api_key_env // ""' <<<"$mj")"
  model_env="$(jq -r '.provider.model_env // ""' <<<"$mj")"

  [[ -n "$kind" ]] || wip_p_die 3 bad-provider "provider.kind is missing"
  if [[ "$kind" != "openai-compatible" ]]; then
    wip_p_die 3 unsupported-provider \
      "provider.kind '$kind' is not supported in v1 (want: openai-compatible)" \
      "$(jq -nc --arg k "$kind" '{provider_kind:$k}')"
  fi
  [[ -n "$base_env" ]] || wip_p_die 3 bad-provider "provider.base_url_env is missing"
  [[ -n "$key_env" ]] || wip_p_die 3 bad-provider "provider.api_key_env is missing"
  [[ -n "$model_env" ]] || wip_p_die 3 bad-provider "provider.model_env is missing"

  # Resolve env pointers. Unset → exit 3. Empty-but-set is allowed only for
  # api_key (lets users opt into no-auth local servers).
  if [[ -z "${!base_env+x}" ]]; then
    wip_p_die 3 provider-env-unset "env var $base_env is unset" \
      "$(jq -nc --arg e "$base_env" '{env:$e}')"
  fi
  if [[ -z "${!model_env+x}" ]]; then
    wip_p_die 3 provider-env-unset "env var $model_env is unset" \
      "$(jq -nc --arg e "$model_env" '{env:$e}')"
  fi
  if [[ -z "${!key_env+x}" ]]; then
    wip_p_die 3 provider-env-unset "env var $key_env is unset" \
      "$(jq -nc --arg e "$key_env" '{env:$e}')"
  fi

  local base_url model api_key
  base_url="${!base_env}"
  model="${!model_env}"
  api_key="${!key_env}"

  [[ -n "$base_url" ]] ||
    wip_p_die 3 provider-env-unset "env var $base_env is empty" \
      "$(jq -nc --arg e "$base_env" '{env:$e}')"
  [[ -n "$model" ]] ||
    wip_p_die 3 provider-env-unset "env var $model_env is empty" \
      "$(jq -nc --arg e "$model_env" '{env:$e}')"

  local key_present="true"
  [[ -n "$api_key" ]] || key_present="false"

  WIP_PROVIDER_CFG="$(jq -nc \
    --arg kind "$kind" --arg base "$base_url" --arg model "$model" \
    --arg key "$api_key" --argjson present "$key_present" \
    --arg base_env "$base_env" --arg key_env "$key_env" --arg model_env "$model_env" '
    {
      kind: $kind,
      base_url: $base,
      model: $model,
      api_key: $key,
      api_key_present: $present,
      env: { base_url_env: $base_env, api_key_env: $key_env, model_env: $model_env }
    }')"
}

# wip_provider_chat <request-json> <config-json> — POST the request JSON to
# the provider's /v1/chat/completions and emit the raw response JSON on stdout.
# When $WIP_PROVIDER_CMD is set, the network is replaced by piping the request
# JSON to `bash -c "$WIP_PROVIDER_CMD"` and reading its stdout as the response.
#
# Safe to call from $() capture — does not call wip_p_die.
#
# Exit codes:
#   0 — got a response (caller is responsible for path-checking it)
#   1 — transport failure (curl nonzero, or the mock command exited nonzero)
wip_provider_chat() {
  local req="$1" cfg="$2"
  local base_url api_key key_present
  base_url="$(jq -r '.base_url' <<<"$cfg")"
  api_key="$(jq -r '.api_key' <<<"$cfg")"
  key_present="$(jq -r '.api_key_present' <<<"$cfg")"

  # Strip any trailing slash so a base_url of either ".../v1" or "..." works
  # consistently. Always append "/v1/chat/completions" per the OpenAI shape.
  local url="${base_url%/}/v1/chat/completions"

  if [[ -n "${WIP_PROVIDER_CMD:-}" ]]; then
    # Test seam: pipe the request to a shell snippet, take its stdout as the
    # response. This is the entire mocking strategy.
    local resp rc=0
    resp="$(printf '%s' "$req" | bash -c "$WIP_PROVIDER_CMD")" || rc=$?
    if [[ $rc -ne 0 ]]; then
      [[ "${WIP_VERBOSE:-0}" == "1" ]] && wip_p_warn "WIP_PROVIDER_CMD exited $rc"
      return 1
    fi
    printf '%s' "$resp"
    return 0
  fi

  # Real HTTP path. Only send Authorization when the key resolved non-empty.
  local curl_args=(-sS -fL -X POST -H 'Content-Type: application/json' --data-binary @-)
  if [[ "$key_present" == "true" ]]; then
    curl_args+=(-H "Authorization: Bearer $api_key")
  fi

  local err_file resp rc=0
  err_file="$(mktemp -t wip-curl-err.XXXXXX)"
  resp="$(printf '%s' "$req" | curl "${curl_args[@]}" "$url" 2>"$err_file")" || rc=$?
  if [[ $rc -ne 0 ]]; then
    if [[ "${WIP_VERBOSE:-0}" == "1" && -s "$err_file" ]]; then
      while IFS= read -r line; do wip_p_warn "curl: $line"; done <"$err_file"
    fi
    rm -f "$err_file"
    return 1
  fi
  rm -f "$err_file"
  printf '%s' "$resp"
  return 0
}
