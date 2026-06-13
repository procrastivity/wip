# Workplan — step-10 · `wip` porcelain

Lands the standalone `wip` binary — the second layer in
[ADR-0001](../../../../engineering/decisions/0001-three-layer-plumbing-porcelain.md).
v1's job is intentionally narrow: **wire up an OpenAI-compatible provider**
over `wip-plumbing` so the next step (10.5) can hang the LLM-driven intake
shaper off it. Step-10 itself does not ship a single judgment verb beyond a
proof-of-life `ask`; every existing plumbing verb is exposed unchanged as a
transparent proxy.

## Decisions (made here, feed later steps)

- **Verb surface for v1 (intentionally minimal).**
  - `wip ask [<prompt>] [--system <text>]` — single-turn chat-completion via
    the resolved provider; stdout is the assistant text (prose), exit 1 on
    HTTP/JSON failure. Prompt arg if given, else stdin. The proof-of-life
    that the provider wiring works end-to-end; also the substrate step-10.5
    will reuse.
  - `wip provider show` — print the resolved provider config sans secrets
    (`{kind, base_url, model, api_key_present}` JSON by default; prose with
    `--no-json`). Exits 3 if `provider:` block is absent or a required env
    var is unset.
  - `wip <plumbing-verb> [...args]` — every other verb is a **transparent
    proxy** to `bin/wip-plumbing`. `argv` after the verb name (and any
    porcelain-only flags) is passed verbatim; stdout/stderr/exit pass through.
    No prose renderers in v1 — the porcelain's identity in this step is "the
    LLM-aware shell over plumbing," not "the prose-rendering shell." Prose
    renderers for `status`/`next` are deferred to a later step.
  - `wip help` / `wip -h` / `wip --help` — print the porcelain usage
    (showing `ask`, `provider`, and the deferred-to-plumbing verb list) plus
    a hint to `wip-plumbing --help` for verb-level help.
  - `wip --version` — print porcelain version (separate from plumbing).
- **Provider config contract.** Read from `.wip.yaml`'s top-level `provider:`
  block (already present in this repo's manifest, lines 27–31). v1 supports
  exactly `kind: openai-compatible`; any other kind exits 3 with
  `error.kind=unsupported-provider`. Required fields are the three env
  pointers — `base_url_env`, `api_key_env`, `model_env`. Missing block →
  exit 3 `no-provider`; missing required key inside the block → exit 3
  `bad-provider`; pointed-to env var unset → exit 3 `provider-env-unset`
  naming which env name was empty. Secrets never appear in `wip-plumbing`
  output, in errors, or in `-v` diagnostics.
- **No new fields invented in `.wip.yaml`.** The existing `provider:` block
  is the contract. step-10 does not add `provider.timeout` /
  `provider.temperature` / `provider.system_prompt` / etc. — those are
  step-10.5+ concerns and we don't pre-bake decisions for them.
- **HTTP transport: `curl`. Mockable via `WIP_PROVIDER_CMD`.** Production
  reaches the provider through a single helper in `lib/wip/wip-provider-lib.bash`
  that POSTs to `${BASE_URL%/}/v1/chat/completions` with
  `Authorization: Bearer <key>` and a JSON body of `{model, messages}`. The
  helper has one test seam: if `WIP_PROVIDER_CMD` is set, the request JSON
  is piped to that command on stdin and the command's stdout is read as the
  chat-completions response. **This is the entire mocking strategy** —
  tests set `WIP_PROVIDER_CMD="cat <fixture-response.json>"` (and, when
  asserting on the request, a tiny tee-then-cat shell snippet). No network
  in tests, no `nc`/`socat`, no port allocation.
- **Layout.**
  - `bin/wip` — main dispatcher; parses porcelain-global flags, dispatches
    `ask`/`provider` to local subcommand files, proxies everything else to
    `bin/wip-plumbing`.
  - `lib/wip/wip-porcelain-lib.bash` — version, usage, `wip_p_die`, prose
    diagnostics. Distinct from `wip-plumbing-lib.bash` so the two binaries
    can evolve independently (the porcelain doesn't `source` plumbing libs).
  - `lib/wip/wip-provider-lib.bash` — `wip_provider_load <root>`,
    `wip_provider_chat <request-json>`. Pure bash + jq + curl.
  - `lib/wip/wip-subcommands/ask.bash`, `lib/wip/wip-subcommands/provider.bash`
    — porcelain-native verbs. Function naming mirrors plumbing
    (`wip_cmd_ask`, `wip_cmd_provider`) to keep the dispatcher pattern
    identical.
- **Locating `wip-plumbing` for the proxy.** Resolution order:
  1. `$WIP_PLUMBING_BIN` if set (test override).
  2. Sibling: `$(dirname "$0")/wip-plumbing` if that file exists and is
     executable.
  3. `command -v wip-plumbing` on `$PATH`.
  4. Else exit 3 `no-plumbing` with a prose hint.
  The porcelain `exec`s plumbing for proxied verbs so signals and exit
  codes propagate cleanly; `wip` adds no PID layer.
- **Porcelain-global flags v1.**
  - `-h/--help` (exit 0).
  - `--version` (exit 0; porcelain version, not plumbing's).
  - `-v/--verbose` — currently routes to the porcelain libs only;
    `wip-plumbing` gets the flag too **only** when explicitly placed after
    the verb (matches plumbing's existing arg-prelude). v1 does not
    auto-forward; tests pin this so step-10.5 doesn't surprise itself.
  No `--json`/`--no-json` on the porcelain wrapper itself — proxied
  plumbing already honours it; `ask` is always prose; `provider show`
  carries its own `--json`/`--no-json` per verb.
- **JSON vs prose defaults.** `ask` is always prose. `provider show`
  defaults to JSON (consistent with plumbing) and accepts `--no-json` for
  the rare interactive case. Proxied plumbing verbs keep plumbing's JSON
  default verbatim.
- **`ask` request shape.** Strict minimum:
  ```json
  {"model": "<resolved-model>", "messages": [
    {"role":"system","content":"<--system if given>"},
    {"role":"user","content":"<prompt or stdin>"}
  ]}
  ```
  No system message when `--system` is omitted. No `temperature`,
  `max_tokens`, `stream`, or other knobs in v1 — provider defaults apply.
  Response extraction: `jq -r '.choices[0].message.content'`. A response
  that lacks that path exits 1 with the raw JSON on stderr (under `-v`).
- **`ask` exit codes.**
  - 0 — got a non-empty assistant message.
  - 1 — HTTP/transport error (curl nonzero) **or** response JSON missing
    the expected path.
  - 2 — usage error (no prompt + empty stdin, conflicting flags).
  - 3 — provider config error (same kinds as `provider show`).
- **No retries, no streaming, no token accounting.** v1 sends one request,
  reads one response, prints, exits. step-10.5 can add retry/streaming when
  it has a concrete need.
- **Dependency additions.** `curl` joins `bash/jq/yq-go/shellcheck/shfmt/
  git/gnumake/pre-commit/coreutils` in `flake.nix`'s devShell and in
  `Makefile`'s `deps-check`. (`curl` is already on every dev macOS by
  default, but the flake is our shipping contract.)
- **Versioning.** `WIP_PORCELAIN_VERSION="0.1.0-dev"` in
  `wip-porcelain-lib.bash`. Independent of `wip-plumbing`'s `WIP_VERSION`
  so the two can rev separately; `wip --version` shows the porcelain's.
  `wip provider show` includes a `porcelain_version` field for downstream
  scripts. Doctor parity is out of scope (no `wip doctor` augmentation
  here).
- **No new `.wip.yaml` features flipped.** This step adds the
  porcelain binary itself; it is not a feature that the manifest declares.
  The `provider:` block already exists and stays as-is.

## Chunks

1. **`flake.nix` + Makefile deps.** Add `curl` to the devShell packages and
   to `Makefile`'s `deps-check` list. Run `nix flake update` only if needed
   (lean: no update — `curl` is already in `nixpkgs/nixos-25.05`).
2. **`lib/wip/wip-porcelain-lib.bash`.** Mirror the shape of
   `wip-plumbing-lib.bash`: `WIP_PORCELAIN_VERSION`, `wip_p_version`,
   `wip_p_usage`, `wip_p_warn`, `wip_p_die <code> <kind> <msg>`. Keep
   prefixed (`wip_p_*`) so a future combined harness doesn't collide.
3. **`lib/wip/wip-provider-lib.bash`.**
   - `wip_provider_load <root>` — read `.wip.yaml`, emit a JSON config
     object `{kind, base_url, api_key, model, env: {base_url_env, ...}}`
     on stdout; nonzero + structured error on missing/bad config. Used by
     both `ask` and `provider show`. The `api_key` field is included
     in-memory but redacted by the caller before printing.
   - `wip_provider_chat <request-json> <config-json>` — POST to
     `${base_url%/}/v1/chat/completions`. Honours `WIP_PROVIDER_CMD` test
     seam: when set, pipe request JSON to `bash -c "$WIP_PROVIDER_CMD"`
     stdin, read the response from its stdout. Returns the response JSON;
     curl errors map to nonzero exit, with `-v` emitting the curl stderr.
4. **`lib/wip/wip-subcommands/ask.bash`.** Parse `--system <text>`. Read
   prompt from arg or stdin (`-` is allowed as the arg to mean stdin even
   when something is piped). Build the request JSON via `jq -nc`. Call
   `wip_provider_chat`. Extract `.choices[0].message.content`; emit to
   stdout; non-empty check.
5. **`lib/wip/wip-subcommands/provider.bash`.** Dispatch `show` (only
   subverb in v1; anything else exit 2). `show` calls `wip_provider_load`,
   emits `{kind, base_url, model, api_key_present, env, porcelain_version}`
   JSON; `--no-json` prints the same fields as a prose block. Never emits
   the api_key value itself.
6. **`bin/wip` dispatcher.** Mirrors `bin/wip-plumbing`'s argv loop for
   porcelain-global flags (`--help`, `--version`, `-v`). The verb table:
   - `ask` / `provider` → source `lib/wip/wip-subcommands/<verb>.bash` and
     call `wip_cmd_<verb>`.
   - `help` (no arg) → `wip_p_usage; exit 0`.
   - everything else → resolve plumbing path; `exec <plumbing> <verb>
     "$@"`. Unknown verbs become the plumbing's problem (its dispatcher
     emits the correct error envelope).
7. **`bin/wip-plumbing` parity check.** No changes to plumbing in this
   step. The porcelain treats it as a black box; if we *do* need to expose
   anything new from plumbing, that's a signal we're scope-creeping.
8. **Tests** (plain-bash, matches established harness).
   - `test/test-wip-help.sh` — `wip --help`, `wip help`, `wip --version`,
     `wip` (no args) all exit 0 with sane output. `wip --help` mentions
     `ask` and `provider`.
   - `test/test-wip-provider.sh` — happy path: env vars set →
     `wip provider show` JSON has expected fields, `api_key_present:true`,
     no raw key in output. Missing `provider:` block → exit 3 `no-provider`.
     Missing env var → exit 3 `provider-env-unset` with the env name in
     the error message. Unsupported `kind: anthropic` → exit 3
     `unsupported-provider`.
   - `test/test-wip-ask.sh` — uses `WIP_PROVIDER_CMD` to mock:
     - Fixture response → stdout is the assistant text (exit 0).
     - Tee the request to a tempfile via `WIP_PROVIDER_CMD='tee /tmp/req.json
       >/dev/null; cat /tmp/resp.json'` → assert the request JSON has
       `model`, `messages[0].role=="user"`, `messages[0].content=="..."`.
     - With `--system "be terse"` → request has a system message before
       the user one.
     - Stdin path: `printf "hi" | wip ask` produces the same request
       shape (prompt read from stdin).
     - Response missing `.choices[0].message.content` → exit 1.
     - No `provider:` block in fixture `.wip.yaml` → exit 3.
   - `test/test-wip-proxy.sh` — fixture repo with a minimal `.wip.yaml`
     and roadmap; `wip detect`, `wip status`, `wip next`, `wip doctor` all
     emit byte-identical JSON to the equivalent `wip-plumbing` call. Uses
     `WIP_PLUMBING_BIN="$PWD/bin/wip-plumbing"`. Also pins: unknown verb
     bubbles plumbing's exit-2 envelope; `--project foo` resolves through
     plumbing.
9. **README + spec.** Append a short "Porcelain" subsection to `README.md`
   showing the three-line dogfood path:
   ```
   export WIP_LLM_BASE_URL=...
   export WIP_LLM_API_KEY=...
   export WIP_LLM_MODEL=...
   bin/wip provider show
   echo "hello" | bin/wip ask
   ```
   Add a one-pager spec at `engineering/specs/wip-porcelain.md` mirroring
   `wip-plumbing-cli.md`'s structure: scope, conventions, verbs (`ask`,
   `provider show`, transparent proxy), exit codes, env. Cross-link from
   `wip-plumbing-cli.md` §1 ("the standalone `wip` porcelain — see
   wip-porcelain.md"). Add a one-line `engineering/decisions/README.md`
   note if any new ADR locks; lean: **no new ADR** — step-10 only
   *implements* ADR-0001/0002/0009 without locking new decisions.
10. **Mark step-10 shipped on the roadmap; bump `active_step`.** Update
    `.wip/initiatives/distillation/roadmap.md`'s step-10 bullet with
    `✅ shipped <YYYY-MM-DD>` and a one-line outcome (verb set, mock seam,
    dogfood criterion). Bump `.wip.yaml`'s `active_step: step-10` →
    `step-10.5`. Commit.

## Test strategy

Same harness as steps 06–08.5: plain bash, `test/helpers.sh`, `mktemp` for
fixture repos, `WIP_NO_REGISTRY=1` + `WIP_ROOT=<tmp>` + `WIP_PLUMBING_BIN=`
pointing at this checkout's plumbing binary. The provider gate uses
`WIP_PROVIDER_CMD` for every test that would otherwise hit the network —
no real HTTP in `make check`, ever.

**Coverage targets:**

- **Provider config.** Every exit-3 kind (`no-provider`, `bad-provider`,
  `provider-env-unset`, `unsupported-provider`) gets a dedicated assertion.
  Each pins the `error.kind` *and* the prose stderr.
- **`ask`.**
  - Request shape (model, single user message, optional system message,
    proper JSON escaping of `"` / `\n` in the prompt).
  - Response extraction (good path; missing path).
  - Prompt sourcing (arg, stdin, `-` literal).
  - No api_key leakage in any output stream when `-v` is on. (Specifically:
    redact before `wip_p_warn`.)
- **Proxy parity.** `wip <verb>` output equals `wip-plumbing <verb>`
  output byte-for-byte for `detect` / `status` / `next` / `doctor`, and
  exit codes match for unknown verb / `--project` resolution failure.
- **Dogfood (manual, captured in the commit body).**
  - `nix develop --command bin/wip --version` exits 0 and prints
    `wip 0.1.0-dev` (or whatever the locked porcelain version reads).
  - `nix develop --command bin/wip provider show` exits 3 with
    `provider-env-unset` (because the three env vars aren't set in the
    devShell by default). With env set, exits 0; verify
    `api_key_present:true` and that the key value does not appear in
    stdout or stderr.
  - `nix develop --command bin/wip status` produces byte-identical JSON
    to `nix develop --command bin/wip-plumbing status` against this repo.
  - `nix develop --command make check` exits 0 — the test count grows
    by four new files; no existing test regresses.
  - `nix develop --command bin/wip-plumbing doctor` still exits 0 (no
    drift introduced).

## Definition of done

- `bin/wip` + `lib/wip/wip-porcelain-lib.bash` +
  `lib/wip/wip-provider-lib.bash` + `lib/wip/wip-subcommands/{ask,provider}.bash`
  committed and executable.
- `flake.nix` adds `curl` to the devShell; `Makefile` adds `curl` to
  `deps-check`'s loop and to the `SRC`/`TESTS` makefile pattern if
  needed (lean: `bin/wip` joins `SRC`).
- Four new tests pass under `nix develop --command make check`; the
  existing 12 tests still pass.
- `wip --help`, `wip --version`, `wip provider show`, `wip ask` (with
  `WIP_PROVIDER_CMD` mock) all behave per the assertions above.
- `wip <plumbing-verb>` is a transparent proxy: byte-identical JSON output
  vs `wip-plumbing <verb>` for `detect`/`status`/`next`/`doctor`.
- `engineering/specs/wip-porcelain.md` lands; `wip-plumbing-cli.md` gains
  a one-line pointer to it; `README.md` gains a short Porcelain subsection.
- `.wip/initiatives/distillation/roadmap.md` step-10 bullet marked
  `✅ shipped <YYYY-MM-DD>` with the outcome summary.
- `.wip.yaml`'s `initiatives[0].active_step` bumped from `step-10` →
  `step-10.5`.
- `nix develop --command bin/wip-plumbing doctor` still reports zero drift
  (no manifest edits beyond `active_step`).
- Branch + commit + merge into `main` (no-ff merge commit, matches the
  pattern step-08.5 / step-09 used).

## Open questions to resolve during execution

- **`ask`'s prompt-arg vs stdin precedence.** When both are present
  (`echo foo | wip ask "bar"`), prefer the arg or concatenate? Lean:
  **prefer the arg**, drop stdin silently. Concatenation is cute but
  surprising for shell pipelines that pipe in a heredoc by accident.
  `wip ask -` always reads stdin even if an arg follows (so
  `wip ask - "foo"` is exit 2, ambiguous).
- **System prompt source.** `--system <text>` only, or also
  `--system-file <path>`? Lean: **`--system <text>` only** in v1.
  step-10.5 will need richer prompt composition; we don't want to bake
  a half-shape now. `wip ask --system "$(<prompt.md)"` works fine
  meanwhile.
- **Default `Authorization` header when api_key is empty.** Some
  OpenAI-compatible local servers (llama.cpp, ollama via the openai
  proxy) accept no auth. Lean: **always send the header when the env
  resolves to a non-empty value**; skip the header when the value is
  empty *and* the env var was explicitly set to empty. If the env var
  is unset, exit 3 (the contract). This lets users opt into "no auth"
  with `WIP_LLM_API_KEY=`.
- **Should `wip detect`/`wip status` get prose renderers in this step?**
  Lean: **no**. Defer to a dedicated step once we know what `/wip:*`
  needs. v1's job is provider wiring; mixing in prose-rendering work
  doubles the surface and blurs the success criterion. The four-line
  human path through `wip-plumbing status | jq` is fine for now.
- **Stream vs. block response.** Lean: **block (non-streaming)**. The
  request body omits `stream`. Streaming becomes valuable for shaper
  interactions in 10.5+; not for a one-shot `ask`.
- **Should the porcelain emit a `wip-porcelain: ...` prefix on stderr
  like plumbing does?** Lean: **yes, `wip: ...`** (matches the binary
  name). Distinguishes the layer when both binaries' stderr is captured
  together (e.g. in CI logs).
- **Should we package a `wip-porcelain` symlink alongside `wip` (for
  symmetry with `wip-plumbing`)?** Lean: **no**. The binary is `wip`;
  the lib prefix is `wip-porcelain-*` for filesystem clarity. A user
  who wants to be explicit can `which wip` and see one entry.
