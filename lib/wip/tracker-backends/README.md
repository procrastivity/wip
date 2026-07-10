# Tracker backend adapters

One file per issue-tracker backend, `<backend>.bash`, glob-sourced at load time
by `../wip-plumbing-tracker-transport-lib.bash` (ADR-0026 §Decision 2).

## Contract

Each adapter defines exactly two functions:

```bash
_wip_tracker_<backend>_read_cmd()   # echo a read shell-out, or ""
_wip_tracker_<backend>_write_cmd()  # echo a write shell-out, or ""
```

- **Read command.** Invoked as `<cmd> <issue>`; it must print **one semantic
  token** on stdout — `todo|in-progress|in-review|done|canceled` — reducing the
  provider's own JSON to that token itself (via `gh --jq`, `jq` over `glab
  --output json`, etc.). Because the token *is* wip's semantic vocabulary, the
  backend needs **no** arm in `_wip_tracker_provider_state` /
  `_wip_tracker_provider_to_semantic` — the existing `*)` passthrough is correct.
- **Write command.** Invoked as `<cmd> <issue> <semantic-token>`; it applies the
  transition (labels + open/close) for that token.
- Each function should honor its own `WIP_<BACKEND>_{READ,WRITE}_CMD` env seam
  first (for tests / process pins), then emit its default CLI string. The
  generic `WIP_TRACKER_{READ,WRITE}_CMD` seam is handled one level up by the
  dispatcher and takes precedence over everything here.

## Notes

- `<issue>` may be a bare `#N`, a qualified `owner/repo#N`, or a nested
  `group/sub/proj#N` (ADR-0026 §Decision 4). Split it with `${1##*#}` (number)
  and `${1%%#*}` (optional repo → the CLI's `--repo`/`-R` flag).
- The three `wip:*` labels the label-carried states use must **pre-exist** in the
  target repo/project (ADR-0026 §Consequences).
- Linear is **not** an adapter here — it stays inline in the transport lib (the
  agent/MCP path). See ADR-0026.
