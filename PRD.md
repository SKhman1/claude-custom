# PRD: claude-custom

Status: Draft v0.1
Date: 2026-11-05
Owner: claude-custom contributors
Related references: `free-claude-code` (AGPL-3.0, behavioral reference only), `opencode` (provider catalog and first-run UX reference)

> Planning document. The shipped CLI implements a focused subset of this
> specification; `README.md` describes the current command surface and behavior.
> Sections below are kept as design rationale and roadmap, and are not a
> statement that every command or feature listed here exists yet.

---

## 1. Summary

`claude-custom` is a lightweight, CLI-only local proxy and launcher that lets Claude Code run against any user-selected OpenAI-compatible inference provider (NVIDIA NIM, OpenRouter, DeepSeek, Groq, local Ollama, and others).

**Clarification of the core mechanism:** Claude Code always speaks the Anthropic Messages protocol to `claude-custom` on loopback. `claude-custom` translates that traffic to the OpenAI v1 protocol (`/v1/chat/completions`, `/v1/models`, Bearer auth) and sends it to the provider base URL the user supplies. The provider does not need to expose an Anthropic API. The user only needs an OpenAI-compatible base URL such as `https://integrate.api.nvidia.com/v1` or `https://openrouter.ai/api/v1`.

One command installs the experience:

```
claude-custom
```

On first run it asks for a provider, an API key, and a model, stores them securely, starts a loopback-only Anthropic-compatible proxy, and launches the installed `claude` binary with the correct environment. No provider or model is preselected. On later runs it reuses the saved profile and starts immediately; `--ask` forces the picker again.

The product exists to remove three pain points:

1. Claude Code's non-streaming request timeout (5 minutes / 300 s) kills long generations. The proxy plus environment overrides remove that cap.
2. Free and low-cost providers enforce strict rate limits. The proxy serializes and retries requests instead of letting Claude Code fail.
3. Provider setup is fragmented. A guided provider/model picker with secure key storage makes a working setup take under two minutes.

## 2. Problem and context

Claude Code speaks the Anthropic Messages API. Most low-cost inference providers speak the OpenAI Chat Completions API. Bridging them requires:

- An Anthropic-to-OpenAI translation layer, including streaming, tools, images, and reasoning.
- Correct Claude Code environment variables so the client talks to the local proxy and does not apply its own short timeout or watchdogs.
- Rate-limit admission control, retries, and stream recovery so a long agent session survives provider throttling and mid-stream disconnects.

`free-claude-code` (FCC) proves this model works, but it is a large Python application with a web admin UI, many harnesses, messaging integrations, and an AGPL-3.0 license. `opencode` proves the provider-catalog and first-run UX model. `claude-custom` should deliver the smallest useful subset of both: one CLI, Claude Code only, no browser UI, no Python runtime, no telemetry.

### The timeout issue

Claude Code applies a short non-streaming request timeout. FCC neutralizes it by launching Claude Code with:

- `API_TIMEOUT_MS=2147483647`
- `API_FORCE_IDLE_TIMEOUT=0`
- `CLAUDE_ENABLE_STREAM_WATCHDOG=0`
- `CLAUDE_ENABLE_BYTE_WATCHDOG=0`

If the user observed a "30000s" limit, the first task in implementation is to reproduce and confirm whether it is the 300 s (5 minute) non-streaming cap, a 30,000 ms value, or a provider-side timeout. The product must remove the Claude Code cap and also avoid introducing its own.

## 3. Goals

- G1: A fresh user can go from install to a working Claude Code session in under two minutes with one command.
- G2: Any OpenAI-compatible provider can be used, including a custom base URL, with no code changes.
- G3: Long generations (30+ minutes) and long agent sessions (hours) complete without client-side timeout or watchdog aborts.
- G4: Provider rate limits are respected by default without failing the Claude Code session.
- G5: API keys are stored in the OS credential store by default and never appear in logs, config, or crash output.
- G6: The tool is lightweight: single binary, no runtime dependency, fast startup, low idle memory.
- G7: Reliability behavior (retry, stream continuation, fallback) is observable through `status`, `doctor`, and logs.

## 4. Non-goals

- No web UI, desktop app, tray icon, or browser-based admin panel.
- No Anthropic subscription/OAuth proxying and no circumvention of Anthropic account limits.
- No model hosting, no fine-tuning, no prompt marketplace.
- No multi-user server, no LAN/public exposure by default.
- No support for every agent harness in v1. Claude Code only; the design leaves room for adapters later.
- No requirement that the provider expose an Anthropic API. v1 targets OpenAI-compatible `/v1` endpoints. Anthropic-native passthrough is not a v1 feature.
- No transparent TLS interception or network-wide proxy.
- No attempt to hide provider usage from the provider. Users bring their own keys and are responsible for provider terms.

## 5. Users and jobs

Primary user: a developer on Windows who has Claude Code installed and an API key for a low-cost provider. They want Claude Code's UX without Anthropic subscription costs.

Jobs:

- "Let me use DeepSeek/NIM/OpenRouter inside Claude Code."
- "Do not make me edit environment variables or config files."
- "Do not let a 5-minute timeout kill a long refactor."
- "Do not lose my response when the provider drops the stream."
- "Do not leak my API key."
- "Keep it running while I work all day."

Secondary user: a developer on macOS/Linux who wants the same experience. Windows is the first-class target; the design stays portable and CI builds all three.

## 6. Product decisions

| ID | Decision | Choice | Rationale |
| --- | --- | --- | --- |
| D1 | Implementation stack | Go single static binary (decided) | No runtime dependency, ~10-25 MB binary, fast startup, low RSS, easy cross-compile, simple SSE proxy, straightforward process supervision. |
| D2 | Interface | CLI only, interactive TUI wizard | Matches request; no web UI. |
| D3 | Client scope | Claude Code only in v1; providers are OpenAI-compatible v1 endpoints | Claude Code is the only client. The proxy translates Anthropic Messages to OpenAI Chat Completions; the provider needs only an OpenAI-compatible base URL. |
| D4 | Proxy bind | `127.0.0.1` only, random per-serve token | Local security; no accidental LAN exposure. |
| D5 | Key storage | OS keyring by default (`go-keyring`: Windows Credential Manager, macOS Keychain, Secret Service), file fallback with explicit warning | Secure by default; portable. |
| D6 | Config location | `~/.claude-custom/` | Cross-platform, shell-agnostic, easy to inspect and remove. |
| D7 | Model IDs | `provider/model` for stored config; proxy maps any `claude-*` alias to the selected model | Claude Code may request a Claude ID even when using a gateway. |
| D8 | License | Apache-2.0 or MIT (open question) | Clean-room implementation; no FCC code copied. |
| D9 | Update model | Manual `claude-custom update`; optional update check | Avoid surprise restarts. |
| D10 | First-run defaults | No default provider or model; setup requires an explicit choice. Later launches reuse the saved default profile; `--ask` reopens the picker. | Matches user preference and avoids silently sending traffic to an unexpected provider. |

### Why Go over Python

FCC is AGPL-3.0. Copying or porting its code would force `claude-custom` to be AGPL. A clean-room Go implementation:

- avoids that license obligation;
- ships as one signed `.exe` with no Python 3.14 pin, virtualenv, or dependency resolution;
- starts in milliseconds and idles under 30 MB;
- handles SSE streaming and cancellation cleanly with `net/http` and `context`;
- cross-compiles to macOS/Linux from one CI job.

The cost is more implementation work than reusing FCC. That is acceptable for a focused v1.

## 7. Core UX

### 7.1 First run

```
$ claude-custom

  claude-custom 0.1.0
  Use Claude Code with any provider.

  ? Choose a provider
  > NVIDIA NIM                  integrate.api.nvidia.com
    OpenRouter                  openrouter.ai
    DeepSeek                    api.deepseek.com
    Groq                        api.groq.com
    Ollama (local)              localhost:11434
    Custom OpenAI-compatible    enter base URL
    ...

  ? Paste your NVIDIA NIM API key  ************************
    Validating key... ok (42 models)

  ? Choose a model (type to search)
  > deepseek-ai/deepseek-v3.2            128k context
    nvidia/nemotron-3-ultra-550b-a55b    200k context
    ...

  Saved profile "default".
  Starting proxy on 127.0.0.1:8787...
  Launching Claude Code...
```

Provider endpoint contract (what the user enters for "Custom"):

- Base URL must be the OpenAI-compatible root, typically ending in `/v1`, for example `https://integrate.api.nvidia.com/v1`.
- The proxy calls `POST <base>/chat/completions` for inference and `GET <base>/models` for discovery.
- Auth is `Authorization: Bearer <key>` unless the preset says otherwise. A custom provider may disable auth for local servers.
- The tool must not require the endpoint to implement Anthropic `/v1/messages`. If a user pastes an Anthropic base URL, `provider test` explains that v1 expects an OpenAI-compatible endpoint.

Rules:

- The key input is masked, never echoed, and supports paste.
- No provider or model is preselected; the user must make an explicit choice.
- Validation calls the provider's model-list endpoint. If validation fails, the user can retry, continue anyway (offline/local providers), or go back.
- Local providers (Ollama, LM Studio, llama.cpp) do not require a key and skip validation if the server is not running, with a warning.
- The model picker is searchable and paginated. It shows context window when known.
- If `claude` is not on `PATH`, print an actionable message with install instructions and exit non-zero.
- If a proxy is already running, reuse it when the profile matches; restart only if configuration changed.

### 7.2 Daily use

```
claude-custom                 # launch default profile
claude-custom --profile fast  # named profile
claude-custom run --model deepseek-ai/deepseek-v3.2 -- --resume
```

`claude-custom` must be a drop-in wrapper: everything after `--` is passed to `claude` unchanged. The proxy stays running after Claude Code exits by default, so the next launch is instant. `claude-custom stop` shuts it down. An optional idle timeout (default off, since the product targets 24/7 use) can stop it after N hours.

### 7.3 Command surface

| Command | Purpose |
| --- | --- |
| `claude-custom` | Launch Claude Code with the default profile; run wizard on first use. |
| `claude-custom setup` | Re-run the guided setup. |
| `claude-custom run [flags] [-- args]` | Launch with explicit profile/provider/model and passthrough args. |
| `claude-custom serve [--foreground] [--port N]` | Start only the proxy. |
| `claude-custom stop` | Stop the proxy for this user. |
| `claude-custom status` | Show proxy state, uptime, profile, provider, model, request counters, last error. |
| `claude-custom doctor` | Check binary, Claude Code version, config validity, key presence, provider reachability, port, env conflicts, log redaction. |
| `claude-custom logs [-f] [--lines N]` | Read or follow the proxy log. |
| `claude-custom provider list` | List presets and custom providers. |
| `claude-custom provider add` | Add a custom OpenAI-compatible provider. |
| `claude-custom provider test <id>` | Validate endpoint and key. |
| `claude-custom provider remove <id>` | Remove a provider and its key reference. |
| `claude-custom model list [--provider P]` | List known models. |
| `claude-custom model search <query>` | Search models. |
| `claude-custom model refresh [--provider P]` | Re-fetch models from the provider. |
| `claude-custom key set <provider>` | Store or replace a key. |
| `claude-custom key remove <provider>` | Delete a key. |
| `claude-custom key list` | Show which providers have keys (never the values). |
| `claude-custom profile list` | List profiles. |
| `claude-custom profile use <name>` | Set the default profile. |
| `claude-custom profile show [name]` | Print resolved profile (secrets redacted). |
| `claude-custom profile remove <name>` | Delete a profile. |
| `claude-custom config get <key>` / `set <key> <value>` | Inspect or change settings. |
| `claude-custom env --shell powershell` | Print the environment exports for manual use. |
| `claude-custom completion powershell` | Emit a PowerShell completion script. |
| `claude-custom update` | Check for and install a new version. |
| `claude-custom version` | Print version and build metadata. |

Command aliases: none in v1 to avoid collisions. If a short alias is desired, `ccx` is the candidate.

### 7.4 Profiles

A profile is a named bundle of provider, model, limits, fallbacks, and model metadata. Example:

```json
{
  "version": 1,
  "default_profile": "default",
  "profiles": {
    "default": {
      "provider": "nvidia_nim",
      "model": "deepseek-ai/deepseek-v3.2",
      "limits": {
        "max_concurrency": 1,
        "requests_per_window": 1,
        "window_seconds": 2
      },
      "fallbacks": [
        "openrouter/deepseek/deepseek-chat"
      ],
      "context_window": 128000,
      "max_output_tokens": 8192
    },
    "fast": {
      "provider": "groq",
      "model": "llama-3.3-70b-versatile",
      "limits": {
        "max_concurrency": 2,
        "requests_per_window": 10,
        "window_seconds": 60
      }
    }
  },
  "providers": {
    "nvidia_nim": {
      "preset": "nvidia_nim",
      "base_url": "https://integrate.api.nvidia.com/v1",
      "key_ref": "claude-custom/nvidia_nim"
    }
  },
  "server": {
    "bind": "127.0.0.1",
    "port": 8787
  }
}
```

Rules:

- Keys never live in this file. `key_ref` points to an OS keyring entry.
- `limits` are per provider-profile; defaults are conservative.
- `fallbacks` is an ordered list of alternate `provider/model` entries used only for retryable provider failures, not for content-policy refusals.
- CLI flags override profile values; environment variables override defaults; profile overrides defaults; built-in defaults last.

## 8. Functional requirements

### FR-1 Provider catalog

- Ship a bundled catalog of at least 20 OpenAI-compatible provider presets with display name, base URL, key page URL, local/remote flag, default limits, and capability hints.
- Every preset must name an OpenAI-compatible base URL (normally ending in `/v1`) and use the OpenAI v1 protocol. Presets must not require Anthropic `/v1/messages` support.
- Include at minimum: NVIDIA NIM, OpenRouter, DeepSeek, Groq, Cerebras, Together AI, Fireworks, Mistral, xAI, Google AI Studio (OpenAI-compat), Z.ai, Moonshot/Kimi, MiniMax, SiliconFlow, Nebius, Vercel AI Gateway, Hugging Face Router, Ollama, LM Studio, llama.cpp.
- Support a `custom` provider with user-supplied base URL, optional key, and protocol selection (`openai-chat` only in v1).
- Update the catalog with `claude-custom provider update` from a signed/bundled JSON feed. A curated bundled copy must always work offline.
- Provider/model catalog data may be seeded from models.dev, but must be normalized into a local schema and validated. Never require network access to list built-in presets.

### FR-2 Model discovery

- For providers with a `/models` endpoint, fetch and cache the list with a TTL (default 24 h).
- For providers without one (or on failure), fall back to a bundled curated list for that preset.
- Store context window, max output tokens, input modalities, and reasoning support when the provider supplies them; otherwise use conservative defaults (200k context, 8k output) and label them as estimates.
- `claude-custom model refresh` forces a refetch.

### FR-3 Credential storage

- Store keys in the OS keyring under service `claude-custom` and account `<provider_id>`.
- If the keyring is unavailable, create `~/.claude-custom/config.json` with restrictive permissions and store the key encrypted with a machine-bound key (DPAPI on Windows; a local key file with 0600 on other platforms is an explicit fallback and must warn).
- Environment variable `CLAUDE_CUSTOM_<PROVIDER>_KEY` overrides the stored key for CI and power users.
- Never write a key to logs, error messages, `status`, `doctor`, crash reports, or shell history. `env --shell powershell` must print a placeholder, not the key, unless `--show-secrets` is explicitly passed.

### FR-4 Proxy lifecycle

- Bind to `127.0.0.1` on a configurable port (default 8787). If busy, pick the next free port and record it in the runtime state file.
- Generate a random 32-byte auth token per server start; accept it via `x-api-key` or `Authorization: Bearer`.
- Write `~/.claude-custom/run.json` with PID, port, token hash (not the token itself where possible), start time, and version. Use an OS-level lock to prevent two servers.
- Support graceful shutdown on `SIGINT`/`SIGTERM`; close open streams; do not leave child processes.
- `claude-custom stop` must stop the server even if the PID file is stale, and must never kill an unrelated process.

### FR-5 Anthropic-compatible API

Implement these endpoints:

| Method | Path | Behavior |
| --- | --- | --- |
| POST | `/v1/messages` | Translate and proxy; `stream=true` emits Anthropic SSE. |
| POST | `/v1/messages/count_tokens` | Approximate token count; provider-native count when available. |
| GET | `/v1/models` | Advertise the selected profile's models plus configured fallbacks. |
| GET | `/health` | Unauthenticated loopback health with version and uptime. |
| POST | `/stop` | Authenticated shutdown (same as `claude-custom stop`). |
| HEAD/OPTIONS | Above paths | Probes with correct `Allow` headers. |

Optional in v1.1: `/v1/responses` for OpenAI Responses clients. Not required for Claude Code.

### FR-6 Request translation

Support the Anthropic Messages request shape used by Claude Code:

- `system` as string or content blocks.
- `messages` with `user`/`assistant` roles; content string or blocks.
- `text`, `image` (base64 and URL), `tool_use`, `tool_result`, `thinking`/`redacted_thinking` where present.
- `tools` with JSON Schema, `tool_choice` (`auto`, `any`, `tool`, `none`), and `parallel_tool_calls` when derivable.
- `max_tokens`, `temperature`, `top_p`, `top_k`, `stop_sequences`, `metadata.user_id`.
- Streaming and non-streaming.
- `count_tokens` with the same content normalization.

Translation rules:

- Anthropic `system` -> OpenAI `system` message at the front.
- Anthropic `tool_result` -> OpenAI `tool` message with matching `tool_call_id`.
- Anthropic `tool_use` -> OpenAI `tool_calls` on an assistant message.
- Anthropic image blocks -> OpenAI `image_url` content parts.
- Anthropic `thinking` -> provider reasoning fields when supported; otherwise omit and log at debug.
- Preserve `stop_reason` mapping: `end_turn`, `max_tokens`, `tool_use`, `stop_sequence`, `refusal`.
- Map provider usage to Anthropic `usage` fields, including cache read/write tokens when available.
- Never drop tool definitions silently. If a provider rejects a schema, attempt one documented downgrade (for example strip `extra_body.chat_template` for NIM) and surface a clear error if it still fails.

### FR-7 Streaming contract

Emit a valid Anthropic SSE sequence:

```
event: message_start
event: content_block_start
event: content_block_delta   (repeated)
event: content_block_stop
event: message_delta
event: message_stop
```

Rules:

- Exactly one `message_start` and one `message_stop` per successful response.
- Emit `ping` events during upstream silence (default every 15 s).
- Flush after every event; do not buffer beyond the recovery holdback.
- On upstream error before any client-visible content, retry invisibly.
- On upstream error after partial text/thinking and no tool calls, continue with a continuation request and splice deltas.
- On upstream error after any tool call has been delivered, stop with an error; never replay a tool call.
- Set `Content-Type: text/event-stream`, `Cache-Control: no-cache`, and `Connection: keep-alive`.

### FR-8 Rate limiting and admission

- Per provider profile: strict sliding-window limiter (`requests_per_window` per `window_seconds`) and a concurrency semaphore (`max_concurrency`).
- Defaults: 1 request per 2 s, 1 concurrent request. Presets may suggest higher values (for example Groq/Cerebras paid tiers), but defaults must avoid 429 storms on free tiers.
- Admission is per provider, not global, so fallbacks can proceed while a primary is throttled.
- Requests wait in a bounded queue. When the queue exceeds a limit (default 16), return a clear Anthropic `overloaded_error` instead of unbounded memory growth.
- Count token-estimation calls against a lighter budget or exempt them, but never allow them to starve real requests.

### FR-9 Retries and failure policy

- Retry on: 408, 409, 425, 429, 5xx, connection reset, TLS handshake failure, unexpected EOF before terminal event, and provider-specific retryable errors.
- Do not retry: 400, 401, 403, 404, 422, content-policy refusals, invalid tool schema, context-window exceeded.
- Attempts: up to 10, exponential backoff starting at 250 ms, factor 2, cap 8 s, full jitter.
- Honor `Retry-After` (seconds or HTTP date) and provider reset headers.
- Use a single-probe recovery model: when multiple requests hit a rate limit, only one probes recovery; others wait for its result instead of stampeding.
- Add a circuit breaker per provider: open after N consecutive retryable failures (default 5), half-open after a cooldown (default 30 s).
- Fallbacks are attempted in order only after the primary exhausts retryable attempts, and never after client-visible content has been delivered.

### FR-10 Stream recovery and continuation

- Early holdback: retain the first 750 ms or 64 KiB of SSE so an early cutoff can be retried with no client-visible artifact.
- After content is committed:
  - If no tools were delivered and the prefix is safe, issue one or more continuation requests with the delivered prefix as assistant prefill, up to 4 continuations or 2 MiB of spliced content per response.
  - If completed tool calls were delivered, hand off or stop; do not retry.
  - If structured output or an unsafe prefix is detected, stop with an Anthropic error.
- Never emit duplicate text or duplicate tool calls across a splice.
- Record recovery events in structured logs and counters exposed by `status`.

### FR-11 Claude Code launch

Launch the installed `claude` binary with a clean environment:

1. Remove inherited `ANTHROPIC_*` variables.
2. Remove `CLAUDE_CODE_DISABLE_NONESSENTIAL_TRAFFIC` (it suppresses gateway model discovery).
3. Set:

| Variable | Value | Purpose |
| --- | --- | --- |
| `ANTHROPIC_BASE_URL` | `http://127.0.0.1:<port>` | Point Claude Code at the proxy. |
| `ANTHROPIC_AUTH_TOKEN` | per-serve token | Authenticate to the proxy (not the provider key). |
| `CLAUDE_CODE_ENABLE_GATEWAY_MODEL_DISCOVERY` | `1` | Use `/v1/models` for the model picker. |
| `CLAUDE_CODE_AUTO_MODE_SERVER` | `0` | Use local classifier requests, not Anthropic server checks. |
| `CLAUDE_CODE_DISABLE_ADVISOR_TOOL` | `1` | Avoid advisor tool overhead. |
| `CLAUDE_CODE_AUTO_COMPACT_WINDOW` | `190000` | Leave headroom for compaction. |
| `CLAUDE_ENABLE_STREAM_WATCHDOG` | `0` | Prevent watchdog aborts during provider retries. |
| `CLAUDE_ENABLE_BYTE_WATCHDOG` | `0` | Same. |
| `API_FORCE_IDLE_TIMEOUT` | `0` | Keep waiting while the proxy recovers. |
| `API_TIMEOUT_MS` | `2147483647` | Disable the 5-minute non-streaming cap. |
| `DISABLE_AUTOUPDATER` | `1` | Prevent mid-session updates. |
| `DISABLE_FEEDBACK_COMMAND` | `1` | Reduce nonessential traffic. |
| `DISABLE_ERROR_REPORTING` | `1` | No telemetry from the child. |

- Do not set `ANTHROPIC_API_KEY`; it conflicts with token auth in some versions.
- Pass through all user arguments after `--`.
- Propagate the child's exit code. On `Ctrl+C`, forward the signal and wait briefly before exiting.

### FR-12 Status, diagnostics, and logs

`claude-custom status` prints:

- server PID/port/uptime/version
- active profile, provider, model, fallbacks
- requests total, streaming total, retries, recoveries, 429s, 5xxs
- last error with timestamp (redacted)
- key presence per provider (never values)

`claude-custom doctor` checks and exits non-zero on hard failures:

- binary on `PATH`, `claude` on `PATH`, version compatibility
- config schema validity, profile references, key presence
- provider reachability and auth (with a short timeout)
- port availability or existing server health
- conflicting `ANTHROPIC_*` variables in the current shell
- log readability and redaction self-test

Logs: JSON lines at `~/.claude-custom/logs/proxy.log`, rotated at 10 MB with 5 files. Default level `info`; `--debug` enables request-shape logging without bodies or secrets. A `--trace` mode may log bodies only with an explicit redaction pass and a warning.

### FR-13 24/7 operation

- `serve` runs in the background by default and survives the launching shell.
- `run` auto-starts the server if absent and reuses it if healthy.
- Optional `claude-custom service install` registers a Windows Scheduled Task at logon (and launchd/systemd equivalents) with restart-on-failure.
- Server never exits because of idle time unless configured.
- A supervisor loop restarts the proxy on panic with exponential backoff and logs the cause.
- `status` differentiates "running", "degraded" (provider unreachable/circuit open), and "stopped".

## 9. Architecture

```
┌────────────────────┐        Anthropic Messages API        ┌──────────────────────┐
│   Claude Code      │ ───────────────────────────────────> │  claude-custom proxy │
│  (claude binary)   │ <─────────────────────────────────── │  127.0.0.1:8787      │
└────────────────────┘         Anthropic SSE                └──────────┬───────────┘
                                                                        │
                                        OpenAI Chat Completions         │
                                        (HTTP/SSE, per provider)        │
                                                                        v
                                                             ┌──────────────────────┐
                                                             │ NVIDIA NIM /        │
                                                             │ OpenRouter / ...    │
                                                             └──────────────────────┘
```

Components:

- `cmd/claude-custom`: CLI parsing, wizard, profile management, launch.
- `internal/config`: schema, load/save, precedence, migration.
- `internal/keystore`: OS keyring plus encrypted-file fallback.
- `internal/catalog`: bundled provider presets, model cache, models.dev import.
- `internal/proxy`: HTTP server, auth, routing, lifecycle.
- `internal/anthropic`: Anthropic request/response/SSE types and validators.
- `internal/openai`: OpenAI Chat Completions types and SSE parser.
- `internal/translate`: request/response/stream translation.
- `internal/admission`: sliding-window limiter, semaphore, queue, circuit breaker.
- `internal/recovery`: holdback buffer, continuation, prefix safety, deduplication.
- `internal/provider`: preset adapters and quirks (NIM `chat_template`, reasoning fields, tool schema).
- `internal/claude`: environment construction and process spawning.
- `internal/state`: run state, PID/lock, counters, logs.

Dependency choices (Go): standard `net/http`, `context`, `encoding/json`; `golang.org/x/sync/semaphore`; `golang.org/x/sys/windows` for DPAPI if needed; `zalando/go-keyring`; a small TUI library (`charmbracelet/huh` or `bubbletea`) or a minimal hand-rolled picker; `tiktoken-go` or an embedded heuristic for token counts.

## 10. Configuration, precedence, and storage

Paths:

- Config: `~/.claude-custom/config.json`
- Runtime state: `~/.claude-custom/run.json`
- Logs: `~/.claude-custom/logs/`
- Cache: `~/.claude-custom/cache/`
- Credentials: OS keyring; fallback `~/.claude-custom/secrets.json` (encrypted)

Precedence (highest to lowest):

1. CLI flags
2. `CLAUDE_CUSTOM_*` environment variables
3. Selected profile
4. Global config
5. Built-in defaults

Config versioning: `version: 1` with forward-compatible unknown-field preservation. Provide `claude-custom config migrate` and never silently discard user data.

## 11. Security, privacy, and terms

- The proxy binds to loopback and requires a per-serve token. Refuse non-loopback binds unless `--allow-lan` is passed with an explicit warning and a persisted token.
- Provider keys are sent only to the configured provider origin. Reject redirects to a different origin. Validate TLS by default; allow `--insecure` only for explicit local endpoints.
- Redact `Authorization`, `x-api-key`, `api_key`, `token`, and key-like query parameters from all logs, errors, and diagnostics. Add a self-test that feeds a fake key through the logging path.
- Do not log request or response bodies by default.
- No analytics, telemetry, crash uploads, or update pings that include identifiers. An update check may send only version/OS.
- Document that users must comply with provider terms of service and that free tiers may throttle.
- Explicitly state that the tool is not affiliated with Anthropic and does not bypass Anthropic account limits. Users must have their own provider credentials.
- `claude-custom uninstall` removes the binary; `claude-custom purge` removes config, cache, logs, and keyring entries after confirmation.

## 12. Performance budgets

| Metric | Target |
| --- | --- |
| Cold CLI start to proxy listening | < 500 ms |
| Proxy added latency (p50 / p95) | < 15 ms / < 40 ms |
| Time to first SSE event after upstream first byte | < 50 ms |
| Idle RSS | < 30 MB |
| Binary size | < 25 MB |
| Memory per active stream | < 2 MB |
| Config read/write | < 10 ms |

## 13. Milestones

M0 - Skeleton (2-3 days)
- Repo, Go module, CI cross-compile, version/`doctor` skeleton, config schema, keyring, `~/.claude-custom` layout.

M1 - Working proxy (4-6 days)
- `/v1/messages` non-stream and stream, translation core, `/v1/models`, `/health`, `/v1/messages/count_tokens`, NIM and OpenRouter presets, Claude Code env launch.

M2 - Setup UX (3-4 days)
- First-run wizard, provider/model pickers, key validation, profiles, custom provider, `provider`/`model`/`key` commands, PowerShell completion.

M3 - Reliability (4-6 days)
- Sliding-window limiter, concurrency, queue, retries/backoff, circuit breaker, holdback, continuation, fallbacks, structured counters.

M4 - 24/7 and release (3-4 days)
- Background serve, `run` auto-start, `status`, `logs`, restart supervisor, Windows Scheduled Task, install script, signed release, docs.

M5 - Hardening (2-3 days)
- Acceptance suite, fault injection, key-redaction test, long-run soak, cross-platform smoke tests.

Total: roughly 3-4 weeks for one developer.

## 14. Acceptance criteria

1. On a clean Windows 11 machine with Claude Code installed, `claude-custom` guides the user from provider selection to a running session in under two minutes, with no provider or model preselected.
2. Given only an OpenAI-compatible base URL (for example NVIDIA NIM), the tool completes a Claude Code session end to end without the provider exposing an Anthropic endpoint.
3. A single response lasting 10+ minutes completes without a Claude Code timeout or watchdog abort.
4. A 20-turn tool-calling session at the default 1 request / 2 s limit completes without an unhandled 429.
5. Killing the upstream connection after partial text results in a continued, coherent response with exactly one `message_stop`.
6. An invalid key produces a clear, actionable error and never prints the key.
7. `config.json` contains no plaintext key; the key is readable only through the OS credential store.
8. Logs, `status`, `doctor`, and crash output redact secrets; the redaction self-test passes.
9. `claude-custom stop` stops only the proxy; `claude-custom status` reports stopped; a subsequent `claude-custom` restarts cleanly.
10. `claude-custom doctor` catches a missing `claude` binary, a port conflict, a malformed config, and a conflicting `ANTHROPIC_BASE_URL` in the shell.
11. `--` passthrough sends arguments to Claude Code unchanged and propagates its exit code.
12. The binary runs with no Python/Node runtime installed.
13. A local Ollama profile works with no API key.

## 15. Risks and mitigations

| Risk | Impact | Mitigation |
| --- | --- | --- |
| Claude Code's timeout env vars are undocumented and may change | The core timeout fix breaks | Pin tested Claude Code versions in `doctor`; detect behavior; keep env policy in one module; document fallbacks. |
| Provider OpenAI-compat quirks (tool schema, reasoning, NIM `chat_template`) | Failed requests or broken tools | Per-provider adapters and a documented single downgrade retry; capability flags; integration tests per preset. |
| Rate limits on free tiers | Failed sessions | Conservative defaults, queueing, circuit breaker, clear `status`, preset tuning. |
| AGPL contamination from FCC | License obligation | Clean-room implementation; FCC used only for behavioral study; no code copied; keep a design-notes provenance file. |
| models.dev or provider catalogs drift | Stale model lists | Bundled curated seed, tolerant parser, manual overrides, offline-first behavior. |
| Unsigned Go binary triggers SmartScreen/antivirus | Install friction | Code-sign releases when possible; publish checksums; document "More info -> Run anyway"; provide Scoop/winget manifests. |
| Token counting inaccuracy | Poor auto-compaction decisions | Label counts as estimates; use provider data when available; expose a config override for context window. |
| Stream continuation duplicates content | Corrupted output | Strict prefix state machine, deduplication, continuation caps, stop on tool calls/structured output, tests with fault injection. |
| Secrets in shell history or process args | Key leak | Never accept keys as CLI args by default; prompt or read from stdin/env; warn if `--key` is used. |
| Proxy port conflict | Launch failure | Auto-select the next free port and record it; `doctor` reports conflicts. |

## 16. Open questions

1. Platform scope: Windows-first only, or cross-platform builds from day one? Recommendation: Windows-first UX, cross-platform CI from M1.
2. Key storage fallback: is an encrypted local file acceptable when no OS keyring exists, or should the tool refuse to store keys?
3. Harness scope after v1: Claude Code only, or add opencode/Codex/Aider adapters?
4. License: MIT or Apache-2.0?
5. Command name: keep `claude-custom`, or add `ccx` as a short alias?
6. Auto-update: manual only, or check-on-launch with opt-in?
7. Confirm the observed limit: 300 s (5 minutes), 30,000 ms, or 30,000 s? This determines the exact timeout test.

## Appendix A: Behavior extracted from free-claude-code (reference only)

- Local FastAPI proxy on `127.0.0.1`; Claude Code pointed at it with `ANTHROPIC_BASE_URL` and `ANTHROPIC_AUTH_TOKEN`.
- Anthropic Messages <-> OpenAI Chat translation with SSE re-emission and stream recovery.
- Strict sliding-window limiter with an async context manager and `acquire_if`; default 1 request per 2 s.
- Provider concurrency semaphore, coordinated single-probe recovery, `Retry-After`, jitter, up to ~10 attempts.
- Early holdback (~750 ms / 64 KiB) and suffix continuation after partial delivery; no retry after tool calls.
- Claude Code environment policy as listed in FR-11, including `API_TIMEOUT_MS=2147483647`.
- Default model pattern: `provider/model`, e.g. `nvidia_nim/nvidia/nemotron-3-ultra-550b-a55b`.
- HTTP read timeout 120 s, connect timeout 30 s (proxy-side, distinct from Claude Code's client timeout).

## Appendix B: Compatibility matrix (v1)

| Surface | v1 | Notes |
| --- | --- | --- |
| Provider side: OpenAI-compatible base URL | Yes (required) | Proxy calls `<base>/chat/completions` and `<base>/models`; Bearer auth by default. |
| Provider side: Anthropic `/v1/messages` | No | Not required and not used for v1 providers. |
| Client side: Anthropic `/v1/messages` | Yes | Claude Code talks to the local proxy; the proxy translates. |
| `/v1/messages` SSE | Yes | Streaming with recovery. |
| `/v1/messages/count_tokens` | Yes | Approximate; provider-native when available. |
| `/v1/models` | Yes | Gateway discovery shape with context/output metadata. |
| `/v1/responses` | No (v1.1) | Not needed by Claude Code. |
| Images | Yes | Base64 and URL blocks. |
| Tool use / tool results | Yes | Parallel tool calls when supported. |
| Thinking / reasoning | Best effort | Mapped when the provider exposes it; otherwise omitted. |
| Prompt caching | Best effort | Usage fields mapped when reported; no Anthropic cache control semantics. |
| Native Anthropic passthrough | No | Out of scope for v1. |
| Non-loopback clients | No | Requires `--allow-lan`. |
