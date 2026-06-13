# ask — single-turn chat completion via the resolved provider.
# stdout: assistant text (no JSON envelope). Errors via wip_p_die.
# shellcheck shell=bash

wip_cmd_ask() {
  local prompt="" system="" prompt_source=""
  while [[ $# -gt 0 ]]; do
    case "$1" in
      --system)
        [[ $# -ge 2 ]] || wip_p_die 2 usage "ask: --system requires an argument"
        system="$2"
        shift 2
        ;;
      --system=*)
        system="${1#--system=}"
        shift
        ;;
      -)
        # Explicit stdin marker. Cannot be combined with a positional prompt.
        if [[ -n "$prompt_source" ]]; then
          wip_p_die 2 usage "ask: '-' (stdin) and a positional prompt are mutually exclusive"
        fi
        prompt_source="stdin-explicit"
        shift
        ;;
      --)
        shift
        break
        ;;
      -*) wip_p_die 2 usage "ask: unknown flag: $1" ;;
      *)
        if [[ "$prompt_source" == "stdin-explicit" ]]; then
          wip_p_die 2 usage "ask: '-' (stdin) and a positional prompt are mutually exclusive"
        fi
        if [[ -n "$prompt_source" ]]; then
          wip_p_die 2 usage "ask: multiple positional prompts"
        fi
        prompt="$1"
        prompt_source="arg"
        shift
        ;;
    esac
  done

  # Remaining argv after `--`: nothing accepted (keeps shape conservative).
  if [[ $# -gt 0 ]]; then
    wip_p_die 2 usage "ask: unexpected trailing args"
  fi

  # Resolve prompt. Arg beats stdin; '-' forces stdin.
  case "$prompt_source" in
    arg) : ;; # prompt already set
    stdin-explicit) prompt="$(cat)" ;;
    "")
      # No arg, no '-'. If stdin is a pipe / redirect, read it; else exit 2.
      if [[ ! -t 0 ]]; then
        prompt="$(cat)"
      else
        wip_p_die 2 usage "ask: no prompt (pass an arg, '-', or pipe to stdin)"
      fi
      ;;
  esac
  [[ -n "$prompt" ]] || wip_p_die 2 usage "ask: empty prompt"

  # Locate the repo root through plumbing's contract — walk-up for .wip.yaml
  # so the provider config can be read.
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
  local model
  model="$(jq -r '.model' <<<"$cfg")"

  # Build request JSON with proper escaping (jq handles it).
  local req
  if [[ -n "$system" ]]; then
    req="$(jq -nc --arg model "$model" --arg sys "$system" --arg user "$prompt" '
      { model: $model,
        messages: [ {role:"system", content:$sys},
                    {role:"user",   content:$user} ] }')"
  else
    req="$(jq -nc --arg model "$model" --arg user "$prompt" '
      { model: $model,
        messages: [ {role:"user", content:$user} ] }')"
  fi

  local resp
  if ! resp="$(wip_provider_chat "$req" "$cfg")"; then
    wip_p_die 1 transport-error "provider call failed"
  fi

  # Validate the response shape and extract the message content.
  local content
  content="$(jq -r '.choices[0].message.content // empty' <<<"$resp" 2>/dev/null || true)"
  if [[ -z "$content" ]]; then
    if [[ "${WIP_VERBOSE:-0}" == "1" ]]; then
      wip_p_warn "raw response: $resp"
    fi
    wip_p_die 1 bad-response "response missing .choices[0].message.content"
  fi

  printf '%s\n' "$content"
}
