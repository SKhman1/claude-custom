"use strict";

const crypto = require("crypto");
const fs = require("fs");
const net = require("net");

const config = require("./config.js");
const catalog = require("./presets.js");
const keystore = require("./keystore.js");
const state = require("./state.js");
const log = require("./log.js");
const prompt = require("./prompt.js");
const ui = require("./ui.js");
const claude = require("./claude.js");
const { ProxyServer } = require("./proxy.js");
const { setupCommand, promptModelWithDefault } = require("./setup.js");
const { UsageError, parse, usageText } = require("./flags.js");

const MAX_FALLBACK_TARGETS = 6;

const COLORS = {
  bold: ui.bold,
  dim: ui.dim,
  cyan: ui.cyan,
  green: ui.green,
  yellow: ui.yellow,
  red: ui.red,
};

function cyan(text) {
  return COLORS.cyan(text);
}

function dim(text) {
  return COLORS.dim(text);
}

function bold(text) {
  return COLORS.bold(text);
}

function newToken() {
  return crypto.randomBytes(24).toString("hex");
}

function isLocalURL(base) {
  const text = String(base || "");
  return (
    text.startsWith("http://127.0.0.1") ||
    text.startsWith("http://localhost") ||
    text.startsWith("http://[::1]")
  );
}

function loadConfig() {
  const cfg = config.load();
  config.applyEnv(cfg);
  return cfg;
}

function lookupKey(cfg, profileName) {
  const resolved = config.resolve(cfg, profileName);
  const provider = resolved.provider;
  const profile = resolved.profile;
  if (provider.no_auth) return { provider: profile.provider, key: "" };
  const key = keystore.get(profile.provider);
  if (!key && !isLocalURL(profile.base_url)) {
    throw new Error(
      `no API key stored for profile "${profileName}"; run \`claude-custom key set ${profileName}\``
    );
  }
  return { provider: profile.provider, key };
}

function lookupKeyFor(cfg, profileName) {
  return lookupKey(cfg, profileName).key;
}

function fallbackTarget(cfg, name) {
  let resolved;
  try {
    resolved = config.resolve(cfg, name);
  } catch (err) {
    return null;
  }
  const profile = resolved.profile;
  const provider = resolved.provider;
  let key = keystore.get(profile.provider);
  if (!key && !provider.no_auth && !isLocalURL(profile.base_url)) return null;
  return {
    name,
    baseURL: profile.base_url,
    apiKey: key,
    model: profile.model,
    maxOutput: profile.max_output_tokens || 0,
    headers: catalog.attributionHeaders(profile.base_url, provider.headers),
  };
}

function automaticFallbacks(cfg, profileName, profile) {
  const names = Object.keys(cfg.profiles)
    .filter((name) => name !== profileName)
    .sort();
  const out = [];
  for (const wantDifferentProvider of [true, false]) {
    for (const name of names) {
      const candidate = cfg.profiles[name];
      const differentProvider = candidate.provider !== profile.provider;
      if (differentProvider !== wantDifferentProvider) continue;
      if (candidate.base_url === profile.base_url && candidate.model === profile.model) continue;
      const target = fallbackTarget(cfg, name);
      if (target) out.push(target);
    }
  }
  return out;
}

function resolveFallbacks(cfg, profileName, profile) {
  const targets = [];
  const seen = new Set();
  const add = (target) => {
    const key = `${target.name}|${target.baseURL}|${target.model}`;
    if (seen.has(key) || targets.length >= MAX_FALLBACK_TARGETS) return;
    seen.add(key);
    targets.push(target);
  };
  for (const raw of profile.fallbacks || []) {
    const ref = String(raw || "").trim();
    if (!ref) continue;
    const index = ref.indexOf("/");
    if (index <= 0 || index === ref.length - 1) {
      throw new Error(`invalid fallback "${ref}"; expected provider/model`);
    }
    const name = ref.slice(0, index).trim();
    const model = ref.slice(index + 1).trim();
    if (!name || !model) throw new Error(`invalid fallback "${ref}"; expected provider/model`);
    let resolved;
    try {
      resolved = config.resolve(cfg, name);
    } catch (err) {
      throw new Error(`fallback "${ref}": ${err.message}`);
    }
    const target = fallbackTarget(cfg, name);
    if (!target) throw new Error(`fallback "${ref}": no API key`);
    target.model = model;
    add(target);
  }
  for (const target of automaticFallbacks(cfg, profileName, profile)) add(target);
  return targets;
}

function proxyLogger() {
  const logger = log.create();
  return logger;
}

async function startProxyWithStop(cfg, profileName, token, onStop) {
  const key = lookupKeyFor(cfg, profileName);
  const resolved = config.resolve(cfg, profileName);
  const fallbacks = resolveFallbacks(cfg, profileName, resolved.profile);
  const logger = proxyLogger();
  let server;
  try {
    server = new ProxyServer(cfg, {
      profileName,
      apiKey: key,
      token,
      fallbacks,
      logger,
      onStop,
    });
  } catch (err) {
    logger.close();
    throw err;
  }
  let addr;
  try {
    addr = await server.start();
  } catch (err) {
    if (cfg.server.port === 0) {
      logger.close();
      throw err;
    }
    process.stderr.write(`claude-custom: port ${cfg.server.port} unavailable, using a free port\n`);
    try {
      addr = await server.startPort(0);
    } catch (err2) {
      logger.close();
      throw err2;
    }
  }
  logger.info(`serving ${profileName} on http://${addr}`);
  return { server, addr, logger };
}

function startProxy(cfg, profileName, token) {
  return startProxyWithStop(cfg, profileName, token, null);
}

function runInfo(addr, token, profileName, profile) {
  return {
    pid: process.pid,
    addr,
    token,
    profile: profileName,
    provider: profile.provider,
    model: profile.model,
    started_at: new Date().toISOString(),
  };
}

async function runCommand(args) {
  const flags = {
    profile: { type: "string", default: "", usage: "profile name" },
    ask: { type: "bool", default: false, usage: "choose a profile before running" },
  };
  const synopsis = "run [--profile <name>] [--ask] [-- claude args...]";
  const parsed = parse(args, flags, synopsis);
  if (parsed.help) {
    process.stderr.write(usageText(synopsis, flags) + "\n");
    return 0;
  }
  const claudeArgs = parsed.rest;

  const exe = claude.lookPath();
  if (!exe) {
    throw new Error(
      "claude executable not found on PATH; install Claude Code with `npm install -g @anthropic-ai/claude-code` or add it to PATH"
    );
  }

  let cfg = loadConfig();
  if (parsed.values.ask || !cfg.default_profile) {
    const name = await pickProfile(cfg, cfg.default_profile);
    if (!cfg.profiles[name]) cfg = loadConfig();
    cfg.default_profile = name;
  }
  const profileName = parsed.values.profile || cfg.default_profile;
  const resolved = config.resolve(cfg, profileName);
  const profile = resolved.profile;

  const token = newToken();
  const started = await startProxy(cfg, profileName, token);
  const { server, addr, logger } = started;
  try {
    state.write(runInfo(addr, token, profileName, profile));
  } catch (err) {
    process.stderr.write(`warning: could not write run state: ${err.message}\n`);
  }

  printLaunchTarget(profile, resolved.provider, addr);

  const baseEnv = Object.entries(process.env)
    .filter(([, value]) => value !== undefined)
    .map(([name, value]) => `${name}=${value}`);
  const childEnv = claude.env(baseEnv, `http://${addr}`, token, profile.context_window || 0);

  const controller = new AbortController();
  const onSignal = () => controller.abort();
  process.once("SIGINT", onSignal);
  process.once("SIGTERM", onSignal);

  let code = 0;
  try {
    await claude.run(exe, claudeArgs, childEnv, { signal: controller.signal });
  } catch (err) {
    if (err.cancelled) {
      code = 0;
    } else if (err instanceof claude.ExitError) {
      code = err.exitCode;
    } else {
      throw err;
    }
  } finally {
    process.removeListener("SIGINT", onSignal);
    process.removeListener("SIGTERM", onSignal);
    await server.shutdown();
    try {
      state.remove();
    } catch (err) {
      /* ignore */
    }
    logger.close();
  }
  return code;
}

function printLaunchTarget(profile, provider, addr) {
  process.stderr.write(
    `claude-custom: ${profile.provider} -> ${provider.base_url} (proxy ${addr})\n`
  );
}

async function serveCommand(args) {
  const flags = {
    profile: { type: "string", default: "", usage: "profile name" },
    port: { type: "int", default: 0, usage: "override listen port" },
    token: { type: "string", default: "", usage: "override the generated proxy token" },
  };
  const synopsis = "serve [--profile <name>] [--port N] [--token T]";
  const parsed = parse(args, flags, synopsis);
  if (parsed.help) {
    process.stderr.write(usageText(synopsis, flags) + "\n");
    return;
  }
  const cfg = loadConfig();
  const profileName = parsed.values.profile || cfg.default_profile;
  if (!profileName) throw new Error("no profile configured; run `claude-custom setup`");
  if (parsed.values.port > 0) cfg.server.port = parsed.values.port;
  const token = parsed.values.token || newToken();

  const controller = new AbortController();
  const stop = () => controller.abort();

  const { server, addr, logger } = await startProxyWithStop(cfg, profileName, token, stop);
  const resolved = config.resolve(cfg, profileName);
  const profile = resolved.profile;
  try {
    state.write(runInfo(addr, token, profileName, profile));
  } catch (err) {
    process.stderr.write(`warning: could not write run state: ${err.message}\n`);
  }

  process.stdout.write(`Listening on http://${addr}\n`);
  process.stdout.write(`Profile: ${profile.provider} (${resolved.provider.base_url})\n`);
  process.stdout.write(`Model:   ${profile.model}\n`);
  process.stdout.write(`Token:   ${token}\n`);
  process.stdout.write("Press Ctrl+C to stop.\n");

  const onSignal = () => controller.abort();
  process.once("SIGINT", onSignal);
  process.once("SIGTERM", onSignal);
  await new Promise((resolve) => {
    if (controller.signal.aborted) resolve();
    else controller.signal.addEventListener("abort", () => resolve(), { once: true });
  });
  process.removeListener("SIGINT", onSignal);
  process.removeListener("SIGTERM", onSignal);
  await server.shutdown();
  try {
    state.remove();
  } catch (err) {
    /* ignore */
  }
  logger.close();
}

function healthBase(addr) {
  const text = String(addr || "");
  const index = text.lastIndexOf(":");
  if (index <= 0) return `http://${text}`;
  let host = text.slice(0, index);
  const port = text.slice(index + 1);
  if (host === "" || host === "0.0.0.0" || host === "::" || host === "[::]") host = "127.0.0.1";
  if (host.includes(":") && !host.startsWith("[")) host = `[${host}]`;
  return `http://${host}:${port}`;
}

async function requestJSON(url, options, timeoutMs) {
  const controller = new AbortController();
  const timer = setTimeout(() => controller.abort(), timeoutMs);
  try {
    const response = await fetch(url, Object.assign({}, options, { signal: controller.signal }));
    const text = await response.text();
    let body = null;
    try {
      body = text ? JSON.parse(text) : null;
    } catch (err) {
      body = null;
    }
    return { response, body, text };
  } finally {
    clearTimeout(timer);
  }
}

async function stopCommand() {
  const info = state.read();
  if (!info) {
    process.stdout.write("claude-custom is not running.\n");
    return;
  }
  if (!state.alive(info)) {
    state.remove();
    process.stdout.write("claude-custom is not running (removed stale state).\n");
    return;
  }
  try {
    await requestJSON(
      `${healthBase(info.addr)}/stop`,
      { method: "POST", headers: { "x-api-key": info.token } },
      5000
    );
  } catch (err) {
    throw new Error(`stop request failed: ${err.message}`);
  }
  state.remove();
  process.stdout.write("Stopped.\n");
}

function formatDuration(seconds) {
  const total = Math.max(0, Math.floor(seconds));
  const hours = Math.floor(total / 3600);
  const minutes = Math.floor((total % 3600) / 60);
  const secs = total % 60;
  if (hours > 0) return `${hours}h${minutes}m${secs}s`;
  if (minutes > 0) return `${minutes}m${secs}s`;
  return `${secs}s`;
}

async function statusCommand() {
  const info = state.read();
  if (!info) {
    process.stdout.write("claude-custom is not running.\n");
    return;
  }
  const running = state.alive(info);
  process.stdout.write(`PID:      ${info.pid}\n`);
  process.stdout.write(`Address:  http://${info.addr}\n`);
  process.stdout.write(`Profile:  ${info.profile}\n`);
  process.stdout.write(`Provider: ${info.provider}\n`);
  process.stdout.write(`Model:    ${info.model}\n`);
  try {
    const cfg = loadConfig();
    const profile = (cfg.profiles || {})[info.profile];
    if (profile) {
      try {
        for (const target of resolveFallbacks(cfg, info.profile, profile)) {
          process.stdout.write(`Failover: ${target.name}/${target.model}\n`);
        }
      } catch (err) {
        /* ignore */
      }
    }
  } catch (err) {
    /* ignore */
  }
  process.stdout.write(`Started:  ${info.started_at}\n`);
  if (!running) {
    process.stdout.write("Status:   not running (stale state file)\n");
    return;
  }
  process.stdout.write("Process:  running\n");

  let result;
  try {
    result = await requestJSON(`${healthBase(info.addr)}/health`, {}, 2000);
  } catch (err) {
    process.stdout.write("Status:   degraded\n");
    process.stdout.write(`Health:   unreachable (${err.message})\n`);
    return;
  }
  const body = result.body || {};
  process.stdout.write(`Status:   ${body.status === "degraded" ? "degraded" : "running"}\n`);
  process.stdout.write(`Health:   ${body.status}\n`);
  if (typeof body.uptime_seconds === "number") {
    process.stdout.write(`Uptime:   ${formatDuration(body.uptime_seconds)}\n`);
  }
  const counters = body.counters || {};
  process.stdout.write(
    `Requests: ${counters.requests || 0} (streaming ${counters.streaming || 0}, retries ${
      counters.retries || 0
    }, recoveries ${counters.recoveries || 0}, fallbacks ${counters.fallbacks || 0})\n`
  );
  process.stdout.write(
    `Errors:   rate_limited ${counters.rate_limited || 0}, server ${counters.server_errors || 0}\n`
  );
  if (body.last_error) process.stdout.write(`Last err: ${body.last_error}\n`);
}

function portAvailable(bind, port) {
  return new Promise((resolve) => {
    const tester = net.createServer();
    tester.once("error", () => resolve(false));
    tester.once("listening", () => tester.close(() => resolve(true)));
    tester.listen({ host: bind, port, exclusive: true });
  });
}

async function doctorCommand() {
  process.stdout.write("claude-custom doctor\n");
  process.stdout.write(`  config dir: ${require("./paths.js").dir()}\n`);
  process.stdout.write(`  config:     ${require("./paths.js").configPath()}\n`);
  process.stdout.write(`  log:        ${require("./paths.js").logPath()}\n`);
  if (process.env.ANTHROPIC_BASE_URL) {
    process.stdout.write(
      `  env:        ANTHROPIC_BASE_URL=${process.env.ANTHROPIC_BASE_URL} (overridden while running)\n`
    );
  }
  if (process.env.ANTHROPIC_API_KEY) {
    process.stdout.write("  env:        ANTHROPIC_API_KEY is set (removed while running)\n");
  }

  const cfg = loadConfig();
  if (!cfg.default_profile) {
    process.stdout.write("  profile:    none (run `claude-custom setup`)\n");
  } else {
    try {
      const resolved = config.resolve(cfg, cfg.default_profile);
      const profile = resolved.profile;
      process.stdout.write(`  profile:    ${cfg.default_profile}\n`);
      process.stdout.write(`  provider:   ${profile.provider} (${resolved.provider.base_url})\n`);
      process.stdout.write(`  model:      ${profile.model}\n`);
      process.stdout.write(`  key source: ${keystore.source(profile.provider)}\n`);
      try {
        for (const target of resolveFallbacks(cfg, cfg.default_profile, profile)) {
          process.stdout.write(`  failover:   ${target.name}/${target.model} -> ${target.baseURL}\n`);
        }
      } catch (err) {
        process.stdout.write(`  failover:   error: ${err.message}\n`);
      }
      let valid = false;
      try {
        const parsed = new URL(resolved.provider.base_url);
        valid = parsed.protocol === "http:" || parsed.protocol === "https:";
      } catch (err) {
        valid = false;
      }
      if (!valid) process.stdout.write("  base URL:   invalid (must be http or https)\n");
    } catch (err) {
      process.stdout.write(`  profile:    ${cfg.default_profile} (error: ${err.message})\n`);
    }
  }

  const exe = claude.lookPath();
  if (!exe) {
    process.stdout.write("  claude:     not found on PATH\n");
    process.stdout.write("              install: npm install -g @anthropic-ai/claude-code\n");
  } else {
    process.stdout.write(`  claude:     ${exe}\n`);
  }

  const port = cfg.server.port || 8787;
  const bind = cfg.server.bind || "127.0.0.1";
  const free = await portAvailable(bind, port);
  if (free) process.stdout.write(`  port ${port}:    available\n`);
  else process.stdout.write(`  port ${port}:    in use (another proxy may be running)\n`);
}

function tailLines(text, count) {
  const lines = String(text).replace(/[\r\n]+$/, "").split("\n");
  const start = Math.max(0, lines.length - count);
  return lines.slice(start);
}

async function logsCommand(args) {
  const flags = {
    lines: { type: "int", default: 100, usage: "number of lines to show" },
    f: { type: "bool", default: false, usage: "follow new log entries" },
    follow: { type: "bool", default: false, usage: "follow new log entries" },
  };
  const synopsis = "logs [-f] [--lines N]";
  const parsed = parse(args, flags, synopsis);
  if (parsed.help) {
    process.stderr.write(usageText(synopsis, flags) + "\n");
    return;
  }
  const path = require("./paths.js").logPath();
  let data;
  try {
    data = fs.readFileSync(path, "utf8");
  } catch (err) {
    if (err.code === "ENOENT") {
      process.stdout.write(
        `No log file yet at ${path}. Start the proxy with \`claude-custom serve\` or \`claude-custom run\`.\n`
      );
      return;
    }
    throw err;
  }
  for (const line of tailLines(data, parsed.values.lines)) process.stdout.write(`${line}\n`);
  if (!parsed.values.f && !parsed.values.follow) return;

  const handle = await fs.promises.open(path, "r");
  try {
    let position = (await handle.stat()).size;
    for (;;) {
      const buffer = Buffer.alloc(32 * 1024);
      const { bytesRead } = await handle.read(buffer, 0, buffer.length, position);
      if (bytesRead > 0) {
        position += bytesRead;
        process.stdout.write(buffer.subarray(0, bytesRead));
      } else {
        await new Promise((resolve) => setTimeout(resolve, 300));
      }
    }
  } finally {
    await handle.close();
  }
}

function sortedProfileNames(cfg) {
  return Object.keys(cfg.profiles || {}).sort();
}

async function profileCommand(args) {
  if (!args.length) throw new UsageError("usage: claude-custom profile list|use|show|remove");
  const cfg = loadConfig();
  switch (args[0]) {
    case "list": {
      if (!Object.keys(cfg.profiles).length) {
        process.stdout.write("No profiles. Run `claude-custom setup`.\n");
        return;
      }
      for (const name of sortedProfileNames(cfg)) {
        const profile = cfg.profiles[name];
        const marker = name === cfg.default_profile ? " *" : "";
        process.stdout.write(`${name}${marker}  ${profile.provider} -> ${profile.model}\n`);
      }
      return;
    }
    case "use": {
      if (args.length < 2) throw new UsageError("usage: claude-custom profile use <name>");
      const name = args[1];
      if (!cfg.profiles[name]) throw new Error(`profile "${name}" not found`);
      cfg.default_profile = name;
      config.save(cfg);
      return;
    }
    case "show": {
      const name = args.length > 1 ? args[1] : cfg.default_profile;
      if (!name) throw new Error("no default profile");
      const resolved = config.resolve(cfg, name);
      const profile = resolved.profile;
      process.stdout.write(`profile:  ${name}\n`);
      process.stdout.write(`provider: ${profile.provider}\n`);
      process.stdout.write(`base URL: ${resolved.provider.base_url}\n`);
      process.stdout.write(`model:    ${profile.model}\n`);
      process.stdout.write(`key:      ${keystore.source(profile.provider)}\n`);
      const limits = config.withDefaults(profile.limits);
      process.stdout.write(
        `limits:   ${limits.requests_per_window} request(s) / ${limits.window_seconds}s, concurrency ${limits.max_concurrency}\n`
      );
      return;
    }
    case "remove": {
      if (args.length < 2) throw new UsageError("usage: claude-custom profile remove <name>");
      const name = args[1];
      const profile = cfg.profiles[name];
      if (!profile) throw new Error(`profile "${name}" not found`);
      delete cfg.profiles[name];
      const inUse = Object.values(cfg.profiles).some((item) => item.provider === profile.provider);
      if (!inUse) delete cfg.providers[profile.provider];
      if (cfg.default_profile === name) cfg.default_profile = "";
      config.save(cfg);
      return;
    }
    default:
      throw new Error(`unknown profile command "${args[0]}"`);
  }
}

async function providerCommand(args) {
  if (!args.length || args[0] !== "list") throw new UsageError("usage: claude-custom provider list");
  const cfg = loadConfig();
  const configured = new Set();
  for (const provider of Object.values(cfg.providers || {})) {
    if (provider.preset) configured.add(provider.preset);
  }
  for (const preset of catalog.presets) {
    const marker = configured.has(preset.id) ? " (configured)" : "";
    process.stdout.write(`${preset.id.padEnd(22)} ${preset.baseURL}${marker}\n`);
  }
}

function presetFor(provider) {
  if (provider.preset) return catalog.find(provider.preset);
  return null;
}

async function modelCommand(args) {
  if (!args.length || args[0] !== "list") {
    throw new UsageError("usage: claude-custom model list [--profile <name>] [--refresh]");
  }
  const flags = {
    profile: { type: "string", default: "", usage: "profile name" },
    refresh: { type: "bool", default: false, usage: "fetch the live model list from the provider" },
  };
  const synopsis = "model list [--profile <name>] [--refresh]";
  const parsed = parse(args.slice(1), flags, synopsis);
  if (parsed.help) {
    process.stderr.write(usageText(synopsis, flags) + "\n");
    return;
  }
  const cfg = loadConfig();
  const name = parsed.values.profile || cfg.default_profile;
  if (!name) throw new Error("no profile configured");
  const resolved = config.resolve(cfg, name);
  const profile = resolved.profile;
  const provider = resolved.provider;

  if (!parsed.values.refresh) {
    const preset = presetFor(provider);
    if (preset && preset.models && preset.models.length) {
      for (const model of preset.models) {
        const marker = model.id === profile.model ? " (current)" : "";
        process.stdout.write(`${model.id.padEnd(60)} ${model.name}${marker}\n`);
      }
    } else if (profile.model) {
      process.stdout.write(`${profile.model}\n`);
    }
    process.stderr.write(
      `${name}: showing known presets; use --refresh for the live provider list\n`
    );
    return;
  }

  const key = lookupKeyFor(cfg, name);
  let models;
  try {
    models = await catalog.fetchModels(profile.base_url, key, provider.headers);
  } catch (err) {
    process.stderr.write(`could not fetch models: ${err.message}\n`);
    const preset = presetFor(provider);
    if (preset && preset.models && preset.models.length) {
      process.stdout.write("Known models for this preset:\n");
      for (const model of preset.models) process.stdout.write(`  ${model.id}\n`);
    }
    return;
  }
  models = catalog.filterChat(models);
  for (const model of models) {
    const marker = model.id === profile.model ? " (current)" : "";
    process.stdout.write(`${model.id.padEnd(60)} ${model.name}${marker}\n`);
  }
}

async function keyCommand(args) {
  if (args.length < 2) throw new UsageError("usage: claude-custom key set|remove|status <profile>");
  const cfg = loadConfig();
  const name = args[1];
  const profile = cfg.profiles[name];
  if (!profile) throw new Error(`profile "${name}" not found`);
  const providerKey = profile.provider;
  switch (args[0]) {
    case "set": {
      const secret = String(await prompt.askSecret("API key")).trim();
      if (!secret) throw new Error("key is empty");
      keystore.set(providerKey, secret);
      if (keystore.source(providerKey) === "file") {
        process.stderr.write(
          "warning: no OS keyring available; the key was stored in the local secrets file (~/.claude-custom/secrets.json)\n"
        );
      }
      process.stdout.write(`Key stored for ${name} (${keystore.source(providerKey)})\n`);
      return;
    }
    case "remove":
      try {
        keystore.delete(providerKey);
      } catch (err) {
        throw new Error("secret not found");
      }
      process.stdout.write(`Key removed for ${name}\n`);
      return;
    case "status":
      process.stdout.write(`${name}: ${keystore.source(providerKey)}\n`);
      return;
    default:
      throw new Error(`unknown key command "${args[0]}"`);
  }
}

async function configCommand(args) {
  if (!args.length) throw new UsageError("usage: claude-custom config path|show");
  switch (args[0]) {
    case "path":
      process.stdout.write(`${require("./paths.js").configPath()}\n`);
      return;
    case "show": {
      const cfg = loadConfig();
      process.stdout.write(`${JSON.stringify(cfg, null, 2)}\n`);
      return;
    }
    default:
      throw new Error(`unknown config command "${args[0]}"`);
  }
}

const CONFIGURE_VALUE = "__configure__";

function profileDetail(profile) {
  return `${profile.provider} / ${profile.model}`.trim();
}

async function chooseLaunchProfile(cfg) {
  const names = sortedProfileNames(cfg);
  if (!names.length) return { name: "", configure: true };
  const labels = [];
  let defaultIndex = 0;
  for (const name of names) {
    const profile = cfg.profiles[name];
    let detail = profileDetail(profile);
    if (name === cfg.default_profile) {
      detail = `default  ${detail}`;
      defaultIndex = labels.length;
    }
    labels.push({ label: name, detail, value: name });
  }
  labels.push({ label: "Configure another provider", detail: "add or change provider, model, or API key", value: CONFIGURE_VALUE });
  const index = await prompt.choose("Launch Claude Code", labels, defaultIndex, { search: false });
  if (index < 0) throw new prompt.CancelledError();
  if (index === labels.length - 1) return { name: "", configure: true };
  return { name: names[index], configure: false };
}

async function pickProfile(cfg, current) {
  if (!Object.keys(cfg.profiles || {}).length) {
    await setupCommand([]);
    const reloaded = loadConfig();
    if (!reloaded.default_profile) throw new Error("setup did not create a profile");
    return reloaded.default_profile;
  }
  const names = sortedProfileNames(cfg);
  const items = names.map((name) => ({
    label: name,
    detail: name === current ? "default" : "",
    value: name,
  }));
  const index = await prompt.choose("Choose a profile", items, 0, { search: false });
  if (index < 0) throw new prompt.CancelledError();
  const name = names[index];
  await rechooseModel(cfg, name);
  return name;
}

async function rechooseModel(cfg, name) {
  const resolved = config.resolve(cfg, name);
  const profile = resolved.profile;
  const current = profile.model;
  process.stdout.write(`${dim("Fetching model list...")}\n`);
  let models = [];
  let key = "";
  try {
    key = lookupKeyFor(cfg, name);
  } catch (err) {
    process.stdout.write(`${dim("Could not load API key:")} ${err.message}\n`);
  }
  if (key || resolved.provider.no_auth || isLocalURL(profile.base_url)) {
    try {
      models = await catalog.fetchModels(profile.base_url, key, resolved.provider.headers);
      models = catalog.filterChat(models);
    } catch (err) {
      process.stdout.write(`${dim("Could not fetch models:")} ${err.message}\n`);
    }
  }
  let model = await promptModelWithDefaultShared(models, current);
  model = String(model || "").trim();
  if (!model || model === current) return;
  const updated = Object.assign({}, profile);
  updated.model = model;
  for (const candidate of models) {
    if (candidate.id === model) {
      if (candidate.contextWindow > 0) updated.context_window = candidate.contextWindow;
      if (candidate.maxOutput > 0) updated.max_output_tokens = candidate.maxOutput;
      break;
    }
  }
  cfg.profiles[name] = updated;
  try {
    config.save(cfg);
  } catch (err) {
    throw new Error(`save profile: ${err.message}`);
  }
  process.stdout.write(`${dim("~")} Model updated: ${current} -> ${model}\n`);
}

async function promptModelWithDefaultShared(models, current) {
  return promptModelWithDefault(models, current);
}

async function launchClaude() {
  process.stdout.write("\n");
  process.stdout.write(`${cyan(">_")} ${dim("Launching Claude Code...")}\n`);
  const code = await runCommand([]);
  if (code !== 0) throw new claude.ExitError(code);
}

async function defaultCommand() {
  const cfg = loadConfig();
  if (!cfg.default_profile) {
    try {
      await setupCommand([]);
    } catch (err) {
      if (err instanceof prompt.CancelledError) return;
      throw err;
    }
    await launchClaude();
    return;
  }

  let choice;
  try {
    choice = await chooseLaunchProfile(cfg);
  } catch (err) {
    if (err instanceof prompt.CancelledError) return;
    throw err;
  }
  if (choice.configure) {
    try {
      await setupCommand([]);
    } catch (err) {
      if (err instanceof prompt.CancelledError) return;
      throw err;
    }
    await launchClaude();
    return;
  }
  if (choice.name !== cfg.default_profile) {
    cfg.default_profile = choice.name;
    config.save(cfg);
  }
  try {
    await rechooseModel(cfg, choice.name);
  } catch (err) {
    if (err instanceof prompt.CancelledError) return;
    throw err;
  }
  await launchClaude();
}

module.exports = {
  defaultCommand,
  setupCommand,
  launchClaude,
  runCommand,
  serveCommand,
  stopCommand,
  statusCommand,
  doctorCommand,
  logsCommand,
  profileCommand,
  providerCommand,
  modelCommand,
  keyCommand,
  configCommand,
  newToken,
  resolveFallbacks,
  startProxy,
  startProxyWithStop,
  lookupKey,
  lookupKeyFor,
  isLocalURL,
  healthBase,
};
