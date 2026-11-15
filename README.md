# claude-custom

Run [Claude Code](https://claude.com/claude-code) against **any** OpenAI-compatible
model provider — NVIDIA NIM, OpenRouter, DeepSeek, Groq, Cerebras, Together,
Mistral, xAI, Google AI Studio, local Ollama, and more.

Claude Code always speaks the Anthropic Messages API. `claude-custom` starts a
loopback-only proxy that translates those requests to the OpenAI Chat
Completions API (`POST <base>/chat/completions`, `GET <base>/models`), stores
your keys in the OS credential store, and launches `claude` with the right
environment. No provider needs to expose an Anthropic endpoint.

## Features

- One command from install to a working session; guided provider/model picker on first use.
- Anthropic Messages and SSE streaming translated to OpenAI Chat Completions, including tools, images, and reasoning passthrough.
- Per-provider admission control, retries with jittered backoff, `Retry-After` handling, and a circuit breaker.
- Automatic failover across an ordered list of `provider/model` fallbacks; rate-limited or unavailable providers are skipped instead of failing the session.
- Stream recovery: early holdback plus continuation requests to survive mid-stream disconnects.
- Claude Code timeout/watchdog overrides so long generations and long sessions are not cut off.
- API keys in the OS keyring (Windows Credential Manager, macOS Keychain, Secret Service) with an explicit local-file fallback.
- Single static binary. No Python, no Node, no telemetry.

## Install (Windows)

One line in PowerShell:

```powershell
irm https://raw.githubusercontent.com/SKhman1/claude-custom/main/install.ps1 | iex
```

The installer:

1. Checks Windows, PowerShell, and CPU architecture.
2. Checks for Claude Code and installs it with npm (installing Node.js LTS via winget first if needed).
3. Downloads the latest `claude-custom` release binary for your architecture.
4. Installs it to `%LOCALAPPDATA%\claude-custom\bin` and adds that directory to your user `PATH`.
5. Starts `claude-custom setup`.

Nothing else has to be installed by hand — no Go toolchain, no manual config.

To install without launching setup, save the script and run:

```powershell
.\install.ps1 -SkipSetup
```

### Build from source

Requires Go 1.27+ and Claude Code installed (`claude` on `PATH`).

```powershell
.\build.ps1
```

Or manually:

```powershell
go build -trimpath -ldflags "-s -w" -o bin\claude-custom.exe .\cmd\claude-custom
```

On macOS/Linux:

```sh
go build -trimpath -ldflags "-s -w" -o bin/claude-custom ./cmd/claude-custom
```

## Troubleshooting

**"An Application Control policy has blocked this file"**

Windows Smart App Control, WDAC, or AppLocker is refusing the prebuilt
unsigned binary. The installer handles this automatically: it downloads the Go
toolchain and builds claude-custom locally, which these policies generally
allow. You can also force that route directly:

```powershell
.\install.ps1 -BuildFromSource
```

If the machine blocks locally built binaries too, the installer prints the
policy state and the SHA-256 so IT can add an allow rule. Path-based AppLocker
policies can instead be satisfied by installing elsewhere from an elevated
PowerShell:

```powershell
.\install.ps1 -InstallDir "C:\Program Files\claude-custom"
```

**`claude-custom` is not recognized**

The installer appends its folder to your user `PATH`; already-open terminals
do not see the change. Open a new terminal, or run the full path
`%LOCALAPPDATA%\claude-custom\bin\claude-custom.exe`.

## Quick start

```powershell
claude-custom            # first run: pick provider, paste key, pick model, launch
claude-custom run        # launch again with the saved profile
claude-custom run -- --resume   # everything after -- goes to claude unchanged
```

The picker asks for a provider and a model, validates the key against the
provider's model list, and saves a profile. Nothing is preselected.

## Commands

| Command | Purpose |
| --- | --- |
| `claude-custom` | Choose a profile and launch Claude Code |
| `claude-custom run [flags] [-- claude args...]` | Launch with passthrough args |
| `claude-custom setup` | Reconfigure provider/model or script setup |
| `claude-custom serve [--profile P] [--port N]` | Run the proxy in the foreground |
| `claude-custom stop` | Stop a running proxy |
| `claude-custom status` | Proxy state, profile, model, failover chain, counters |
| `claude-custom doctor` | Check config, keys, Claude Code, port, failover wiring |
| `claude-custom logs [-f] [--lines N]` | Read or follow the proxy log |
| `claude-custom profile list\|use <n>\|show [n]\|remove <n>` | Manage profiles |
| `claude-custom provider list` | List provider presets |
| `claude-custom model list [--profile P] [--refresh]` | Known models, or live fetch with `--refresh` |
| `claude-custom key set\|remove\|status <profile>` | Manage API keys |
| `claude-custom config path\|show` | Inspect configuration |
| `claude-custom version` | Print the version |

Run `claude-custom <command> -h` for flags.

## Providers

Bundled presets: `nvidia_nim`, `openrouter`, `deepseek`, `groq`, `cerebras`,
`together`, `fireworks`, `mistral`, `xai`, `google_ai_studio`, `zai`,
`moonshot`, `minimax`, `siliconflow`, `nebius`, `vercel_ai_gateway`,
`huggingface`, `ollama`, `lmstudio`, `llamacpp`, `custom`.

Any other OpenAI-compatible endpoint works through the `custom` preset or a
custom `--base-url`. The base URL must be the OpenAI-compatible root, usually
ending in `/v1`, and receives `Authorization: Bearer <key>` unless the preset is
local or auth is disabled.

Free tiers are respected, not bypassed: when a provider's rate limit is too
small for Claude Code's payloads, the request fails over to the next target in
the profile's `fallbacks` instead of hanging.

## Configuration

State lives outside the repository:

| Path | Contents |
| --- | --- |
| `~/.claude-custom/config.json` | Profiles, providers, server settings |
| `~/.claude-custom/run.json` | Runtime state of the running proxy |
| `~/.claude-custom/logs/proxy.log` | Proxy log |
| OS keyring (`service=claude-custom`) | API keys |
| `~/.claude-custom/secrets.json` | Key fallback, only when no OS keyring exists |

`config.example.json` shows the schema. Keys never live in the config file.

Environment variables:

| Variable | Effect |
| --- | --- |
| `CLAUDE_CUSTOM_HOME` | Override the state directory |
| `CLAUDE_CUSTOM_PROFILE` | Override the default profile |
| `CLAUDE_CUSTOM_PORT` | Override the proxy port |
| `CLAUDE_CUSTOM_<PROVIDER>_KEY` | Supply a provider key (wins over keyring) |

## Security

- The proxy binds to `127.0.0.1` and requires a random per-run token.
- Provider keys are sent only to the configured provider origin.
- Keys are never written to the config file, logs, `status`, or `doctor` output.
- `claude-custom` is not affiliated with Anthropic. It does not proxy Anthropic
  subscriptions or circumvent Anthropic account limits; bring your own provider
  credentials and follow the provider's terms.

## Development

```powershell
go vet ./...
go build ./...
```

The proxy package (`internal/proxy`) contains the translation, admission,
retry/fallback, and stream recovery logic.

## License

MIT

## Acknowledgements

Behavioral reference: `free-claude-code` (AGPL-3.0, no code copied) and the
provider catalog/first-run UX of `opencode`.
