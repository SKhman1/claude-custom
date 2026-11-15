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

- One command from install to a working session; guided provider/API key/default model picker on first use.
- `/model` inside a claude-custom session lists your provider's catalog live, routed to the provider — no restart, no config file surgery.
- Completely separate Claude Code environment: `claude` runs with `CLAUDE_CONFIG_DIR=~/.claude-custom/cli`, so its settings, theme, model memory, sessions and plugins live in claude-custom's state, never in your own `~/.claude`.
- Anthropic Messages and SSE streaming translated to OpenAI Chat Completions, including tools, images, and reasoning passthrough.
- Per-provider admission control, retries with jittered backoff, `Retry-After` handling, and a circuit breaker.
- Automatic failover across an ordered list of `provider/model` fallbacks; rate-limited or unavailable providers are skipped instead of failing the session.
- Stream recovery: early holdback plus continuation requests to survive mid-stream disconnects.
- Claude Code timeout/watchdog overrides so long generations and long sessions are not cut off.
- API keys in the OS keyring (Windows Credential Manager, macOS Keychain, Secret Service) with an explicit local-file fallback.
- Single npm package, no Python, no binaries to sign, no telemetry.

## Install (Windows)

Paste this one line into PowerShell and press Enter:

```powershell
irm https://raw.githubusercontent.com/SKhman1/claude-custom/main/install.ps1 | iex
```

That is the whole installation. The script:

1. Installs Node.js LTS with winget when it is missing (both Claude Code and claude-custom run on Node).
2. Installs Claude Code if it is missing.
3. Installs `claude-custom` from npm (the signed `node.exe` runs it, so Windows Application Control never blocks it) and adds its launcher folder to your user `PATH`.
4. Starts claude-custom: pick a provider, paste an API key, pick a default model.
5. Claude Code launches in that same command; you are already in a session.

After installing, one command is enough:

```powershell
claude-custom        # reuse the saved profile, launch Claude Code
```

No Go toolchain, no config file editing, no environment variables.

Non-interactive install (skip the wizard, e.g. for provisioning):

```powershell
.\install.ps1 -SkipSetup
```

### npm instead of the script

The same package is on npm:

```powershell
npm install -g claude-custom
claude-custom
```

### PATH troubleshooting for existing installs

The installer verifies what `PATH` resolves and prints the full resolution
order. If an older `claude-custom` shadows the new one, upgrade it:

```powershell
npm install -g claude-custom@latest    # npm-global copy
# or remove it and reopen the terminal
```

## Troubleshooting

**"An Application Control policy has blocked this file"**

The Node package is the only build we distribute. `claude-custom.cmd` is a
tiny batch shim over signed `node.exe`, so Smart App Control, WDAC and
AppLocker allow it by default. If you see this error on a machine where
`node.exe` itself is blocked (rare, organization policy), there is nothing the
installer can do: ask IT to allow `node.exe`, or use WSL.

**`claude-custom` reports the wrong version**

An older copy is earlier on your `PATH`. The installer prints the full
resolution order when it finishes, showing which entry wins. Common shadows:
a global npm install at `%AppData%\npm` (upgrade with
`npm install -g claude-custom@latest`), or an outdated copy in
`%LOCALAPPDATA%\claude-custom\bin` (rerun the installer; it refreshes exactly
that copy).

**Node.js build differences**

API keys are stored in `~/.claude-custom/secrets.json` instead of the Windows Credential
Manager (keys saved there by the old binary are still read automatically), and
`HTTP(S)_PROXY` environment variables are not honored.

**`claude-custom` is not recognized**

The installer appends its folder to your user `PATH`; already-open terminals
do not see the change. Open a new terminal, or run the full path
`%LOCALAPPDATA%\claude-custom\bin\claude-custom.cmd`.

## Quick start

The installer ends inside a running session. Afterwards:

```powershell
claude-custom                # saved profile, launch Claude Code
claude-custom run -- --resume   # everything after -- goes to claude unchanged
claude-custom setup          # change provider or model
```

The first-run picker asks for a provider and a model with arrow keys, `↑`/`↓` to move and type to search, validates your key, and saves a profile. Nothing is preselected.

## Isolated environment

`claude-custom` launches Claude Code with its own config home:

```
CLAUDE_CONFIG_DIR=~/.claude-custom/cli
```

Everything Claude Code keeps — settings, theme, plugins, session history,
its `settings.json`, and what you pick in `/model` — lives under
`~/.claude-custom/cli`, exactly the same as a second install of Claude Code.
Your real `~/.claude` (or `%USERPROFILE%\.claude`) and `claude` in the
terminal behave byte-for-byte like they always have. Uninstalling claude-custom
leaves no trace in your own Claude Code configuration.

The catalog `/model` shows inside a claude-custom session comes from the
provider's `/models` endpoint, live. Its ids appear as `anthropic--<name>`
with the real model id shown as the display name because Claude Code's
gateway discovery only accepts ids containing `claude` or `anthropic` — a
CLI-side filter, not a claude-custom choice. `claude-custom` and
`claude-sonnet-4-5` are aliases for your profile's default model.

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
| `~/.claude-custom/cli/` | Claude Code config home for claude-custom sessions (`CLAUDE_CONFIG_DIR`) |
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
| `CLAUDE_CONFIG_DIR` | Not set by you: claude-custom always launches Claude Code with `$CLAUDE_CUSTOM_HOME/cli` |

## Security

- The proxy binds to `127.0.0.1` and requires a random per-run token.
- Provider keys are sent only to the configured provider origin.
- Keys are never written to the config file, logs, `status`, or `doctor` output.
- `claude-custom` is not affiliated with Anthropic. It does not proxy Anthropic
  subscriptions or circumvent Anthropic account limits; bring your own provider
  credentials and follow the provider's terms.

## Development

```powershell
node --check src/proxy.js   # or any file in src/
```

`src/` is the reference implementation (the npm package and the installer).
`internal/` is the older Go implementation kept for reference; the released
binary in `bin/` is not rebuilt from it anymore.

## License

MIT

## Acknowledgements

Behavioral reference: `free-claude-code` (AGPL-3.0, no code copied) and the
provider catalog/first-run UX of `opencode`.
