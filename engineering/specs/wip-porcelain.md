# Spec — `wip` porcelain (v1)

- Status: draft
- Date: 2026-06-13
- Initiative: distillation · roadmap **step-10**
- Decisions: [ADR-0001](../decisions/0001-three-layer-plumbing-porcelain.md) (layers),
  [ADR-0002](../decisions/0002-wip-yaml-manifest-and-detection.md) (manifest/detection),
  [ADR-0009](../decisions/0009-intake-as-pipeline.md) (intake pipeline)

The standalone `wip` binary — the second of the three layers in ADR-0001. It
exposes the deterministic verb surface of
[`wip-plumbing`](./wip-plumbing-cli.md) verbatim, and adds an
**OpenAI-compatible provider seam** so future judgment verbs (step-10.5's
intake shaper, later steps) have somewhere to call. v1 is intentionally
minimal: provider wiring + one proof-of-life `ask` verb + a `provider show`
diagnostic. No prose renderers; no streaming; no retries.

---

## 1. Scope

In v1, the porcelain owns exactly two verbs:

| Verb | One-line |
|------|----------|
| `ask` | Single-turn chat completion via the resolved provider. |
| `provider show` | Print the resolved provider config (sans secrets). |

Every other verb (`detect`, `doctor`, `project`, `init`, `intake`, `status`,
`next`, `roadmap`, `workplan`) is **transparently proxied** to
`wip-plumbing` via `exec`. Argv is forwarded verbatim; exit codes, stdout,
and stderr pass through. The porcelain does not parse, re-render, or augment
plumbing output in v1.

Non-goals for v1: prose renderers for any verb, streaming responses,
multi-turn conversations, retry/backoff logic, token accounting, history
state, `--system-file`, response caching. They are later roadmap steps and
get their own specs.

## 2. Global conventions

### Output discipline

- **`ask`** — always emits the assistant text on stdout as **prose** (no
  JSON envelope). Diagnostics go to stderr with a `wip:` prefix.
- **`provider show`** — JSON on stdout by default (`--json` is the default);
  `--no-json` emits a key/value prose block.
- **Proxied plumbing verbs** — keep plumbing's own JSON-by-default contract.

### Exit codes

Same family as plumbing:

| Code | Meaning |
|------|---------|
| 0 | Success. |
| 1 | Transport / response-shape error (provider call returned non-2xx, or response JSON missing the expected path). |
| 2 | Usage error (bad flags, ambiguous prompt source). |
| 3 | Provider config / dependency error (missing `provider:` block, unset env var, unsupported `kind`, no `wip-plumbing` on disk). |

The proxied plumbing verbs surface plumbing's own 0–4 set unchanged.

### Error envelope

Porcelain errors emit a JSON envelope on stdout plus a `wip: <msg>` line
on stderr. Fields specific to the failure are merged into `error`:

```json
{ "ok": false, "error": { "code": 3, "kind": "provider-env-unset",
                          "message": "env var WIP_LLM_API_KEY is unset",
                          "env": "WIP_LLM_API_KEY" } }
```

Error kinds:

- `no-provider` — `.wip.yaml` has no `provider:` block.
- `bad-provider` — `kind` or a required `*_env` field is missing.
- `unsupported-provider` — `kind` is not `openai-compatible`. The
  envelope's `error.provider_kind` echoes the rejected value.
- `provider-env-unset` — a pointed-to env var is unset or empty (only
  api_key may be explicitly empty; see §4). `error.env` names which.
- `no-manifest` — no `.wip.yaml` found from `$PWD` upward.
- `no-plumbing` — `wip-plumbing` binary not found (sibling /
  `$WIP_PLUMBING_BIN` / `$PATH`).
- `transport-error` — provider HTTP call failed (curl nonzero / mock
  command nonzero).
- `bad-response` — provider returned JSON without
  `.choices[0].message.content`.

### Global flags

- `-h/--help` (exit 0)
- `--version` (exit 0; porcelain version, separate from plumbing's)
- `-v/--verbose` — extra diagnostics on stderr. Does NOT auto-forward to
  plumbing; the proxied verb's own `-v` (placed after the verb) is the
  way to flip plumbing's verbose mode.

`--json`/`--no-json` is per-verb (`provider show` has it; plumbing verbs
pass through). `--dry-run` is plumbing-only.

### Environment

- `WIP_LLM_BASE_URL` / `WIP_LLM_API_KEY` / `WIP_LLM_MODEL` — the *default*
  env names this repo's `.wip.yaml` points to. The actual names come from
  the manifest's `provider.base_url_env` / `api_key_env` / `model_env`.
  Other repos can use different env names.
- `WIP_PLUMBING_BIN` — override the path to `wip-plumbing`. Resolution
  order is `$WIP_PLUMBING_BIN` → sibling-of-`bin/wip` → `$PATH` → exit 3.
- `WIP_PROVIDER_CMD` — test seam. When set, the provider HTTP call is
  replaced by `bash -c "$WIP_PROVIDER_CMD"`, fed the request JSON on
  stdin; the command's stdout is read as the response JSON. Production
  never sets this. The entire test suite uses this seam — no network
  required, no port allocation, no `nc`/`socat` dependency.
- `WIP_LIB`, `WIP_ROOT`, `WIP_NO_REGISTRY`, `WIP_REGISTRY_FILE` — same
  semantics as plumbing's; the porcelain honours them and passes them to
  plumbing transparently.

---

## 3. Verbs

### `wip ask [<prompt>|-] [--system <text>]`

Single-turn chat completion. The prompt is sourced in this order:

1. Positional argument, if given (non-`-`).
2. Stdin, if positional is `-` (the explicit stdin marker).
3. Stdin, if no positional is given and stdin is a pipe / redirect.
4. Else exit 2 (`usage`): "no prompt".

If both a positional prompt and stdin content are present, the positional
wins and stdin is silently dropped. `ask - "foo"` is exit 2 (ambiguous).

The request shape sent to `${base_url%/}/v1/chat/completions` is the
strict minimum:

```json
{
  "model": "<resolved-model>",
  "messages": [
    { "role": "system", "content": "<--system text>" },  // omitted if --system absent
    { "role": "user",   "content": "<prompt>" }
  ]
}
```

No `temperature`, `max_tokens`, `stream`, or other knobs in v1.

Response extraction is `jq -r '.choices[0].message.content'`. Empty / missing
content is exit 1 (`bad-response`).

`Authorization: Bearer <api_key>` is sent iff the resolved api_key is
non-empty. An env var set to "" is allowed and disables the header (for
no-auth local servers); an unset env var is exit 3.

### `wip provider show [--json|--no-json]`

Resolves the provider config and emits it on stdout.

- **stdout (JSON, default):**

```json
{
  "ok": true,
  "kind": "openai-compatible",
  "base_url": "<resolved>",
  "model": "<resolved>",
  "api_key_present": true,
  "env": { "base_url_env": "...", "api_key_env": "...", "model_env": "..." },
  "porcelain_version": "0.1.0-dev"
}
```

- **stdout (`--no-json`):** a key/value prose block of the same fields.

The api_key value itself **never appears** in stdout, stderr, or `-v`
diagnostics — only `api_key_present` does. `env.{base_url_env,api_key_env,
model_env}` are the env *names* from `.wip.yaml`, not their values, so a
typo in the manifest is diagnosable without leaking secrets.

### Proxied plumbing verbs

`wip <verb> [args...]` for any verb not in `{ask, provider}` resolves the
plumbing binary and `exec`s it with the original argv. The shell is
replaced; signals and exit codes propagate without an added PID layer. An
unknown verb bubbles plumbing's own exit-2 envelope.

---

## 4. Open questions

None lock new decisions in this step; the workplan's leans for prompt
precedence (arg beats stdin), system-prompt source (`--system <text>`
only), Authorization gating (empty-but-set env → no header), and prose
renderers (none in v1) all hold. step-10.5 will revisit when the LLM-driven
intake shaper has concrete needs.
