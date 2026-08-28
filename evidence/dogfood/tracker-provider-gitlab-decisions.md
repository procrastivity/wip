# GitLab tracker provider decisions

Date: 2026-08-28

Matter: `tracker-provider-gitlab`

This record closes the nine decisions assigned to Step 01. The decisions use
the provider-neutral tracker contract from the sealed predecessor Matters and
the GitHub provider as the structural template. They also use the current
GitLab REST API documentation and read-only observations of the sandbox
instance (`gitlab.xes-sysdev.com`, GitLab 19.0.3-ee).

## Operative decisions

| Local | Operative decision |
|---|---|
| G1 | **Derive host and project from the normalized `Repo.RemoteURL`.** The store holds `host[:port]/path` (no scheme, no user, no `.git`). The parser reads that form first. It also accepts the raw shapes `git@host:path.git`, `https://host/path.git`, and `ssh://git@host[:port]/path`. The path must have two or more segments (GitLab nests groups). The API base is `https://<host>/api/v4` with any port removed, because an ssh port is not the web port. The project id in every path is `url.PathEscape` of the full project path, for example `xes%2Fxesapps%2Ftest-project`. A remote on `github.com` is refused. |
| G2 | **Ignore `tracker.target`.** The GitLab provider reads no target value. Host and project come only from the remote. This matches GitHub. |
| G3 | **Store the issue `web_url` as the tracker reference, and accept two URL shapes.** GitLab 19 with the work-items view returns `https://<host>/<path>/-/work_items/<iid>`. Older or differently configured instances return `https://<host>/<path>/-/issues/<iid>`. The reference parser accepts both, extracts host, project path, and `iid`, and refuses a host that differs from the adapter host. API calls use `/projects/<id>/issues/<iid>`; the stored reference keeps the exact `web_url` GitLab returned. |
| G4 | **Use explicit token precedence with a glab fallback.** `Options.Token` wins in tests. Production then reads `WIP_GITLAB_TOKEN`, then `GITLAB_TOKEN`. When both are empty, the adapter reads `hosts.<host>.token` from the glab config file: `$GLAB_CONFIG_DIR/config.yml`, else `$XDG_CONFIG_HOME/glab-cli/config.yml`, else `~/.config/glab-cli/config.yml`. A missing file or host yields no token; malformed YAML is an error. Requests send the token in the `PRIVATE-TOKEN` header. OAuth is outside scope. |
| G5 | **Use a SHA-256 HTML-comment marker for create and comment identity.** The marker `<!-- wip-idempotency:<hex> -->` goes at the end of the issue `description` or note `body`, as in the GitHub provider. GitLab stores and returns the raw Markdown, so the marker survives. Before a create, the adapter lists `GET /projects/<id>/issues?state=all&per_page=100&page=N` and scans `description`. Before a comment, it lists `GET /projects/<id>/issues/<iid>/notes?per_page=100&page=N` and scans `body`, skipping notes with `"system": true`. A hit returns `Converged`. |
| G6 | **Deliver state through a guarded `PUT` with `state_event`.** GitLab issue `state` is `opened` or `closed`. Active maps to `opened`; completed and canceled map to `closed`. Writes send `state_event: close` or `state_event: reopen`. The lease is `updated_at`. The adapter reads the issue immediately before each possible write and follows the GitHub guard matrix: a blank lease converges only on an `opened` issue for an active candidate; a terminal issue is never reopened; a matching candidate returns `Converged`; a changed lease or a competing terminal returns `LeaseMismatch`. The post-write response must show the candidate state, or the result is a permanent refusal. |
| G7 | **Carry canceled in an optional, per-Repo label.** The Repo-tier key `tracker.canceled-label` reaches the adapter as `FactoryInput.CanceledLabel`. **Unset (default):** canceled closes the issue with no label, and `ReadState` maps `closed` to `LiveCompleted`; a canceled Matter then reads `conflict` at alignment. **Set:** canceled sends `state_event: close` and `add_labels: <label>` in the same `PUT`; `ReadState` maps `closed` with the label to `LiveCanceled` and `closed` without it to `LiveCompleted`. A completed candidate against `closed` with the label, or a canceled candidate against `closed` without it, is a competing terminal and returns `LeaseMismatch`. Before the first write that adds the label, the adapter calls `GET /projects/<id>/labels?search=<label>` and requires an exact `name` match; when absent it returns `PermanentRefusal` that names the label. GitLab creates missing labels on `add_labels`, so this check is what prevents auto-creation. |
| G8 | **Classify by HTTP status and rate-limit headers.** HTTP 408, 429, and 5xx are retryable. A response with a `Retry-After` header or `RateLimit-Remaining: 0` is retryable at any status. Every other 4xx is a permanent refusal. GitLab returns `404 Project Not Found` for a private project when the token is missing, expired, or lacks the `api` scope, so the 404 message names the project path and points at token scope. Transport and decode errors return as Go errors, which `Flush` records as retryable. |
| G9 | **Page with `per_page=100` and stop on a short page.** The issues list passes `state=all` explicitly; the project-level default `scope` is `all`, so issues by other authors are visible without a `scope` parameter. Notes and labels page the same way. The adapter does not depend on `X-Next-Page` or `X-Total` headers. |

## Consequences

The provider needs no store or event change. It adds one Repo-tier config
value, `tracker.canceled-label`, which GitHub and Linear ignore. The name
`gitlab` appears only in provider registration and Repo configuration.

The reference parser must accept the work-items URL. The sandbox instance
returns `/-/work_items/<iid>` for an ordinary issue today. A parser that
accepted only `/-/issues/<iid>` would refuse every reference GitLab returns.

A canceled label is opt-in because GitLab creates a label that does not
exist. Without the existence check, a typo would create a stray project
label. With no label configured, GitLab cannot separate completed from
canceled, so alignment reports a canceled Matter as `conflict`. That is a
documented limit, not a defect.

The glab config fallback lets a user who already authenticates `glab` per
host use wip with no extra setup. The env variables still win, so a
credential for tests or CI never depends on a file in the home directory.

## API facts verified

| Fact | Source |
|---|---|
| `state` values `opened` and `closed`; `state_event` values `close` and `reopen`. | Issues API docs; sandbox issue read. |
| `add_labels` creates a project label that does not exist. | Issues API docs: "If a label does not already exist, this creates a new project label and assigns it to the issue." |
| Project-level issues list `scope` defaults to `all`. | Issues API docs: "Defaults to `all`." |
| `labels` is an array of strings on an issue. | Sandbox issue read (`["ATE::MFG Test"]`). |
| `web_url` uses `/-/work_items/<iid>` on GitLab 19 with the work-items view. | Sandbox issue read on project 79. |
| Notes carry `system` (boolean) and `body`; system notes are ordinary rows in the list. | Notes API docs; sandbox notes read. |
| `GET /projects/<id>/labels?search=` is a substring match on name (`wf::` returns `wf::in-progress`; `wf::canceled` returns nothing). | Sandbox label reads on project 82. |
| Rate-limit headers are `RateLimit-Limit`, `RateLimit-Observed`, `RateLimit-Remaining`, `RateLimit-Reset`, plus `RateLimit-ResetTime` and `Retry-After` with a 429. | Rate-limit docs. |
| A private or unknown project returns `404 Project Not Found`. | Issues API docs; sandbox read of a missing path. |
| PATs work in the `PRIVATE-TOKEN` header. | Authentication docs. |
| Pagination headers `X-Page`, `X-Per-Page`, `X-Next-Page`, `X-Total` are present; `per_page=100` is honored. | Sandbox issues list with `-i`. |
| `wf::canceled` does not exist in project 82. | Sandbox label read. |

## API facts that remain undocumented

- Whether a new note bumps the parent issue `updated_at`. GitHub does this.
  The live step must flush a comment and a state candidate separately and
  record the observed lease behavior.
- Whether the label `search` parameter also matches `description`. The
  adapter applies an exact `name` filter to the result, so a wider match is
  harmless.
- The sandbox instance sent no `RateLimit-*` headers on normal responses.
  The adapter must not require them.
- Whether `web_url` can change shape after an instance setting changes. The
  parser accepts both shapes so a stored reference stays valid.

## Sources

- [GitLab Issues API](https://docs.gitlab.com/api/issues/)
- [GitLab Notes API](https://docs.gitlab.com/api/notes/)
- [GitLab Labels API](https://docs.gitlab.com/api/labels/)
- [GitLab REST API authentication](https://docs.gitlab.com/api/rest/authentication/)
- [GitLab user and IP rate limits](https://docs.gitlab.com/administration/settings/user_and_ip_rate_limits/)
- Read-only sandbox observations on `gitlab.xes-sysdev.com` (projects 79 and 82), 2026-08-28
