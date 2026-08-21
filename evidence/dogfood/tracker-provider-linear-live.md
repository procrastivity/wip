# Linear tracker provider live verification

Date: 2026-08-21

Matter: `tracker-provider-linear`

Step: `step-04`

## Setup

- Isolated local-only git clone at `/tmp/wip-linear-live.3Uq8Ci`
- Isolated store via `WIP_DB_PATH` (dogfood store untouched; dogfood backend remains `none`)
- Built binary: `wip version v0.1.0-36-g924a082`
- Backend: `linear`
- Target team: isolated sandbox team, key `PRD` (team UUID and workspace name withheld from this public record)
- Push level: `narrated`
- Credential: `WIP_LINEAR_TOKEN` from `~/.linear-wip-live-test-token` (team-scoped personal API key)
- User approved credentialed writes before this Step ran

Project `wip-test` was not configured. The provider does not take a project input.

## Results

| Action | Result |
|---|---|
| Create via `backlog delegate` → approve → flush | `PRD-38` and `PRD-39` created in `started` / In Progress |
| Active state on bind/start | Guard read converged (`tracker.state-observed`); no duplicate write |
| Stage finish comment | One comment on `PRD-38`: `Stage "Live review stage" closed.` |
| Completed state on matter finish | `PRD-38` moved to `completed` / Done (`tracker.state-pushed`) |
| Rebind to `PRD-39` + completed flush | `PRD-39` moved to `completed` / Done |
| Live create+comment double delivery | First create `Delivered` → `PRD-40`; second create `Converged` same ref; first comment `Delivered`; second comment `Converged` |
| Read-only reread | Confirms titles, descriptions, one comment each on `PRD-38` and `PRD-40`, Done on `PRD-38`/`PRD-39`, In Progress on replay issue `PRD-40` |

## External references

Issue URLs are withheld from this public record because they name the
sandbox workspace. The issue identifiers (`PRD-38`, `PRD-39`, `PRD-40`)
and the store evidence below identify the same items.

## Durable store evidence (sandbox DB)

```
tracker.item-created {"ref":"PRD-38"}
tracker.item-created {"ref":"PRD-39"}
tracker.state-observed {"ref":"PRD-38","disposition":"active","lease":"2026-08-21T14:39:52.430Z"}
tracker.state-pushed {"ref":"PRD-38","disposition":"completed","lease":"2026-08-21T14:40:33.344Z"}
tracker.state-pushed {"ref":"PRD-39","disposition":"completed","lease":"2026-08-21T14:40:48.262Z"}
```

Session command log: `/tmp/wip-linear-live.3Uq8Ci/live.log`

## Seal

User closed `reviewed-local` and finished the Matter on 2026-08-21. The Matter is sealed.
