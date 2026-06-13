# detect — report features + initiatives from .wip.yaml. Pure read.
# shellcheck shell=bash

wip_plumbing_cmd_detect() {
  local root mj features inits current
  root="$(wip_find_root)" || wip_die 4 no-manifest "no .wip.yaml found from $PWD upward"
  mj="$(wip_manifest_json "$root")" || true
  [[ -n "$mj" ]] || wip_die 4 bad-manifest "could not parse $root/.wip.yaml" ".wip.yaml"

  features="$(wip_features_json "$root" "$mj")"
  inits="$(printf '%s' "$mj" | jq -c '
    [ .initiatives[]? | {
        slug, status,
        active_step: (.active_step // null),
        brief: (.brief // null),
        roadmap: (.roadmap // null)
      } ]')"
  current="$(printf '%s' "$mj" | jq -r '.current_initiative // ""')"

  jq -n \
    --arg root "$root" --arg cur "$current" \
    --argjson features "$features" --argjson inits "$inits" '
    {
      ok: true,
      root: $root,
      wip_yaml: ".wip.yaml",
      current_initiative: (if $cur == "" then null else $cur end),
      features: $features,
      initiatives: $inits
    }'
}
