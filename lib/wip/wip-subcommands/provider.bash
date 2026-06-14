# provider — inspect provider configuration. v1 has a single subverb: show.
# shellcheck shell=bash

wip_cmd_provider() {
  local sub="${1:-}"
  [[ -n "$sub" ]] || wip_p_die 2 usage "provider: missing subcommand (try: show)"
  shift
  case "$sub" in
    show) _wip_provider_show "$@" ;;
    *) wip_p_die 2 usage "provider: unknown subcommand: $sub" ;;
  esac
}

_wip_provider_show() {
  local emit_json="${WIP_P_JSON:-1}"
  while [[ $# -gt 0 ]]; do
    case "$1" in
      --json)
        emit_json=1
        shift
        ;;
      --no-json)
        emit_json=0
        shift
        ;;
      -*) wip_p_die 2 usage "provider show: unknown flag: $1" ;;
      *) wip_p_die 2 usage "provider show: unexpected arg: $1" ;;
    esac
  done

  local root="" d="$PWD"
  if [[ -n "${WIP_ROOT:-}" ]]; then
    if [[ -f "$WIP_ROOT/.wip.yaml" ]]; then
      root="$WIP_ROOT"
    else
      wip_p_die 3 no-manifest "WIP_ROOT=$WIP_ROOT has no .wip.yaml"
    fi
  else
    while :; do
      if [[ -f "$d/.wip.yaml" ]]; then
        root="$d"
        break
      fi
      [[ "$d" == "/" ]] && break
      d="$(dirname "$d")"
    done
    [[ -n "$root" ]] || wip_p_die 3 no-manifest "no .wip.yaml found from $PWD upward"
  fi

  wip_provider_load "$root"
  local cfg="$WIP_PROVIDER_CFG"

  # Redact the api_key from output. Keep api_key_present and the env names so
  # diagnosis of "wrong env name in manifest" stays trivial.
  if [[ "$emit_json" == "1" ]]; then
    jq -nc \
      --argjson cfg "$cfg" \
      --arg ver "$WIP_PORCELAIN_VERSION" '
      { ok: true,
        kind: $cfg.kind,
        base_url: $cfg.base_url,
        model: $cfg.model,
        api_key_present: $cfg.api_key_present,
        env: $cfg.env,
        porcelain_version: $ver }'
  else
    local kind base_url model present base_env key_env model_env
    kind="$(jq -r '.kind' <<<"$cfg")"
    base_url="$(jq -r '.base_url' <<<"$cfg")"
    model="$(jq -r '.model' <<<"$cfg")"
    present="$(jq -r '.api_key_present' <<<"$cfg")"
    base_env="$(jq -r '.env.base_url_env' <<<"$cfg")"
    key_env="$(jq -r '.env.api_key_env' <<<"$cfg")"
    model_env="$(jq -r '.env.model_env' <<<"$cfg")"
    cat <<EOF
kind:              $kind
base_url:          $base_url
model:             $model
api_key_present:   $present
env.base_url_env:  $base_env
env.api_key_env:   $key_env
env.model_env:     $model_env
porcelain_version: $WIP_PORCELAIN_VERSION
EOF
  fi
}
