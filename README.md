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
- Single static binary, or an npm build that runs inside signed `node.exe` where unsigned executables are blocked. No Python, no telemetry.

## Install (Windows)

Paste this one line into PowerShell and press Enter:

```powershell
irm https://raw.githubusercontent.com/SKhman1/claude-custom/main/install.ps1 | iex
```

That is the whole installation. The script:

1. Installs Claude Code if it is missing (Node.js LTS via winget first, if needed).
2. Installs `claude-custom` to `%LOCALAPPDATA%\claude-custom\bin` and adds it to your user `PATH`.
3. Starts the setup wizard: pick a provider, paste an API key, pick a model.
4. Launches Claude Code through that provider.

When the wizard ends you are already in a working Claude Code session. After
that, one command is enough:

```powershell
claude-custom        # reuse the saved profile, launch Claude Code
```

No Go toolchain, no config file editing, no environment variables.

Non-interactive install (skip the wizard, e.g. for provisioning):

```powershell
.\install.ps1 -SkipSetup
```

If Windows Application Control (Smart App Control, WDAC, AppLocker) blocks
unsigned executables, the installer switches to the npm build automatically,
which runs inside signed `node.exe` and is allowed. Full details are under
[Troubleshooting](#troubleshooting).

### npm instead of the script

The same build is on npm:

```powershell
npm install -g claude-custom
claude-custom setup
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
unsigned binary. The installer handles this automatically: it first downloads
the Go toolchain and builds claude-custom locally, which these policies
generally allow. If the locally built binary is blocked too, it installs the
Node.js build instead — `node.exe` is signed, so policy allows running
claude-custom inside it. You can force either route directly:

```powershell
.\install.ps1 -BuildFromSource
.\install.ps1 -Runtime node
```

If neither route works and the machine has no Node.js, the installer prints the
policy state and the SHA-256 so IT can add an allow rule. Path-based AppLocker
policies can instead be satisfied by installing elsewhere from an elevated
PowerShell:

```powershell
.\install.ps1 -InstallDir "C:\Program Files\claude-custom"
```

The Node.js build is on npm, so it can be installed without the script too:

```powershell
npm install -g claude-custom
claude-custom
```

**Node.js build differences**

The Node.js build behaves like the binary, with two differences: API keys are
stored in `~/.claude-custom/secrets.json` instead of the Windows Credential
Manager (keys saved there by the binary are still read automatically), and
`HTTP(S)_PROXY` environment variables are not honored.

**`claude-custom` is not recognized**

The installer appends its folder to your user `PATH`; already-open terminals
do not see the change. Open a new terminal, or run the full path
`%LOCALAPPDATA%\claude-custom\bin\claude-custom.exe` (or `claude-custom.cmd`
for the Node.js build).

## Quick start

The installer ends inside a running session. Afterwards:

```powershell
claude-custom                # saved profile, launch Claude Code
claude-custom run -- --resume   # everything after -- goes to claude unchanged
claude-custom setup          # change provider or model
```

The first-run picker asks for a provider and a model, validates the key against
the provider's model list, and saves a profile. Nothing is preselected.

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
