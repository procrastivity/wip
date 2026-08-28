# GitLab tracker provider live verification

Date: 2026-08-28

Matter: `tracker-provider-gitlab`

Step: `step-06`

## Setup

- Isolated local-only git repo with `origin` set to
  `git@gitlab.xes-sysdev.com:xes/xesapps/test-project.git` (no fetch; the
  ssh key for that host had expired, and the provider needs no git transport)
- Isolated store via `WIP_DB_PATH` (dogfood store untouched; dogfood backend
  remains `none`)
- Built binary from commit `926e1e9` (branch `matter/tracker-provider-gitlab`)
- Remote as stored: `gitlab.xes-sysdev.com/xes/xesapps/test-project`
- Backend: `gitlab`; push level: `narrated`; target: `none`
- Canceled label: `wf::canceled` (created in the project as the first write,
  label id 88; it did not exist before)
- Credential: **glab config fallback only** (`WIP_GITLAB_TOKEN` and
  `GITLAB_TOKEN` unset for the whole run), which proves G4 live
- Sandbox: project `xes/xesapps/test-project` (id 82), GitLab 19.0.3-ee,
  zero issues before the run
- User approved credentialed writes before this Step ran

## Results

| Action | Result |
|---|---|
| Create via `backlog delegate` → approve → flush | `#1` created `opened`; `web_url` is `/-/work_items/1` (G3); marker present in description |
| Active state on bind/start | Guard read converged (`tracker.state-observed`); no write |
| Stage finish comment (`narrated`) | One note on `#1`: `Stage "Live review stage" closed.` with marker |
| Completed state on matter finish | `#1` moved to `closed`, no label (`tracker.state-pushed` completed) |
| Alignment inside `wip finish` (before the push) | `behind`, expected `completed`, observed `opened`, offer `completed` |
| Canceled state on matter cancel, label configured | `#2` moved to `closed` with `['wf::canceled']` in one `PUT` (`tracker.state-pushed` canceled) |
| Canceled state with a missing label (`wf::does-not-exist`) | `withheld`: `label "wf::does-not-exist" does not exist in project xes/xesapps/test-project; create it or clear tracker.canceled-label`. `#3` stayed `opened []`; no label was created (T5) |
| Live create + comment double delivery (probe) | First create `Delivered` → `#4`; second create `Converged` same ref; first comment on `#3` `Delivered`; second `Converged`. One issue, one note |
| `ReadState` (probe) | `#1` → `completed` / `closed`; `#2` → `canceled` / `closed (wf::canceled)`; `#3` → `nonterminal` / `opened` |
| Rebind `#1` → `#4` + completed flush | `#4` moved to `closed []` (`tracker.state-pushed` completed on the new ref) |
| Read-only re-read | `#1` closed, 1 note; `#2` closed + `wf::canceled`; `#3` opened, 1 note; `#4` closed |

## Observed API facts

- A new note does **not** bump the issue `updated_at` on this instance:
  `#3` has one user note and `updated_at` equals `created_at`. The lease
  therefore survives a Stage-closure comment.
- The issue `web_url` shape is `/-/work_items/<iid>` on GitLab 19 with the
  work-items view. API paths stay `/projects/<id>/issues/<iid>`.
- `updated_at` carries a `-05:00` offset. The adapter treats it as opaque.

## wip observation outside this provider

`wip backlog plan <id> <matter>` on an entry already in state `delegated`
printed `store: event ... (backlog.planned) touched 0 projection rows, want 1`.
The live run bound the Matter with `wip bind` instead. This is a backlog
lifecycle question, not a provider defect.

## External references

- `https://gitlab.xes-sysdev.com/xes/xesapps/test-project/-/work_items/1`
  through `/4`; label `wf::canceled` (id 88). All left in place as evidence.

## Durable store evidence (sandbox DB)

```
tracker.item-created {"ref":".../-/work_items/1"}
tracker.state-observed {"ref":".../-/work_items/1","disposition":"active","lease":"2026-08-28T13:12:13.568-05:00"}
tracker.state-pushed {"ref":".../-/work_items/1","disposition":"completed","lease":"2026-08-28T13:13:48.859-05:00"}
tracker.item-created {"ref":".../-/work_items/2"}
tracker.state-observed {"ref":".../-/work_items/2","disposition":"active","lease":"2026-08-28T13:13:50.445-05:00"}
tracker.state-pushed {"ref":".../-/work_items/2","disposition":"canceled","lease":"2026-08-28T13:13:57.198-05:00"}
tracker.item-created {"ref":".../-/work_items/3"}
tracker.state-observed {"ref":".../-/work_items/3","disposition":"active","lease":"2026-08-28T13:14:23.494-05:00"}
tracker.state-pushed {"ref":".../-/work_items/4","disposition":"completed","lease":"2026-08-28T13:15:19.576-05:00"}
```

(`...` stands for `https://gitlab.xes-sysdev.com/xes/xesapps/test-project`.)

## Seal

User closed `reviewed-local` and finished the Matter on 2026-08-28. The
Matter is sealed.
