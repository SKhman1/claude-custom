"use strict";

const config = require("./config.js");
const catalog = require("./presets.js");
const keystore = require("./keystore.js");
const prompt = require("./prompt.js");
const ui = require("./ui.js");
const { UsageError, parse } = require("./flags.js");

const SETUP_FLAGS = {
  provider: { type: "string", default: "", usage: "provider preset ID" },
  model: { type: "string", default: "", usage: "model ID" },
  "base-url": { type: "string", default: "", usage: "override the provider base URL" },
  key: { type: "string", default: "", usage: "API key (prefer the interactive prompt)" },
  profile: { type: "string", default: "", usage: "profile name" },
  fallbacks: { type: "string", default: "", usage: "comma-separated provider/model fallbacks" },
  "no-auth": { type: "bool", default: false, usage: "provider needs no API key" },
  "no-launch": { type: "bool", default: false, usage: "do not launch Claude Code after saving" },
};

const SETUP_SYNOPSIS =
  "setup [--provider ID] [--model ID] [--base-url URL] [--profile NAME] [--fallbacks A/B,C/D] [--no-auth] [--no-launch]";

function isLocalURL(base) {
  const text = String(base || "");
  return (
    text.startsWith("http://127.0.0.1") ||
    text.startsWith("http://localhost") ||
    text.startsWith("http://[::1]")
  );
}

function hostLabel(preset) {
  let detail = String(preset.baseURL || "").replace(/^https:\/\//, "").replace(/^http:\/\//, "");
  detail = detail.replace(/\/v1$/, "").replace(/\/$/, "");
  return detail || "enter base URL";
}

async function promptProvider() {
  const items = catalog.presets.map((preset) => ({ label: preset.name, detail: hostLabel(preset), value: preset.id }));
  const index = await prompt.choose("Choose a provider", items, 0, { search: true, pageSize: 14 });
  if (index < 0) throw new prompt.CancelledError();
  return catalog.presets[index];
}

function contextLabel(tokens) {
  if (!(tokens > 0)) return "";
  if (tokens >= 1000) {
    if (tokens % 1000 === 0) return `${tokens / 1000}k context`;
    return `${(tokens / 1000).toFixed(1)}k context`;
  }
  return `${tokens} context`;
}

function modelDetail(model) {
  let detail = model.name || "";
  if (detail.toLowerCase() === model.id.toLowerCase()) detail = "";
  const context = contextLabel(model.contextWindow);
  if (context) detail = detail ? `${detail}  ${context}` : context;
  return detail;
}

async function promptModelsForSession(models, chosenId, preselect) {
  if (!models.length) return [chosenId];
  const items = models.map((model) => ({ label: model.id, detail: modelDetail(model), value: model.id }));
  const wanted = new Set([chosenId]);
  for (const id of Array.isArray(preselect) ? preselect : []) wanted.add(String(id));
  const defaults = [];
  for (let i = 0; i < items.length; i++) {
    if (wanted.has(items[i].value)) defaults.push(i);
  }
  let picks = await prompt.chooseMulti(
    "Models to show inside Claude Code (space toggles, enter confirms)",
    items,
    defaults.length ? defaults : [0],
    { search: true, pageSize: 14 }
  );
  if (!picks || !picks.length) picks = defaults.length ? defaults : [0];
  return picks.map((index) => models[index].id);
}

async function promptModelWithDefault(models, current) {
  const selected = String(current || "").trim();
  if (!models.length) return prompt.ask("Model ID", selected);
  const items = [];
  let haveCurrent = selected === "";
  let defaultIndex = 0;
  for (const model of models) {
    let detail = model.name || "";
    if (detail.toLowerCase() === model.id.toLowerCase()) detail = "";
    const context = contextLabel(model.contextWindow);
    if (context) detail = detail ? `${detail}  ${context}` : context;
    if (selected && model.id === selected) {
      detail = detail ? `${detail}  current` : "current";
      if (!haveCurrent) {
        defaultIndex = items.length;
        haveCurrent = true;
      }
    }
    items.push({ label: model.id, detail, value: model.id });
  }
  if (!haveCurrent && selected) {
    items.push({ label: selected, detail: "current", value: selected });
    defaultIndex = items.length - 1;
  }
  items.push({ label: "Enter model ID manually", detail: "type any model identifier", value: "__manual__" });
  const index = await prompt.choose("Choose a model (Enter keeps the current one)", items, defaultIndex, {
    search: true,
    pageSize: 12,
  });
  if (index < 0) throw new prompt.CancelledError();
  const chosen = items[index].value;
  if (chosen === "__manual__") return prompt.ask("Model ID", selected);
  return chosen;
}

function welcome() {
  process.stdout.write("\n");
  process.stdout.write(`  ${ui.bold("claude-custom")}\n`);
  process.stdout.write(`  ${ui.dim("Use Claude Code with any provider.")}\n`);
  process.stdout.write("\n");
}

async function setupCommand(args) {
  const parsed = parse(args, SETUP_FLAGS, SETUP_SYNOPSIS);
  if (parsed.help) {
    process.stderr.write(require("./flags.js").usageText(SETUP_SYNOPSIS, SETUP_FLAGS) + "\n");
    return;
  }
  const flags = parsed.values;
  if (
    !flags.provider &&
    !flags.model &&
    !flags["base-url"] &&
    !flags.key &&
    !flags.profile &&
    !flags.fallbacks
  ) {
    welcome();
  }

  const cfg = config.load();
  config.applyEnv(cfg);

  let preset;
  if (flags.provider) {
    preset = catalog.find(flags.provider);
    if (!preset) throw new UsageError(`unknown provider "${flags.provider}"`);
  } else {
    preset = await promptProvider();
  }

  let base = String(flags["base-url"] || "").trim();
  if (!base) base = preset.baseURL || "";
  if (!base) base = await prompt.ask("Base URL (OpenAI-compatible, usually ends in /v1)", "");
  if (!base) throw new Error("base URL is required");
  base = base.replace(/\/+$/, "");

  let name = String(flags.profile || "").trim();
  if (!name) {
    name = preset.id;
    if (preset.id === "custom") {
      name = await prompt.ask("Profile / provider name", "custom");
    }
  }
  if (!name) throw new Error("profile name is required");

  let apiKey = String(flags.key || "").trim();
  if (!apiKey && !flags["no-auth"] && !isLocalURL(base)) {
    const stored = keystore.get(name).trim();
    if (stored) {
      process.stdout.write(`${ui.dim("i")} An API key is already saved for "${name}".\n`);
      apiKey = String(await prompt.askSecret("API key (press Enter to keep the saved one)")).trim();
      if (!apiKey) apiKey = stored;
    } else {
      apiKey = String(await prompt.askSecret("API key")).trim();
      if (!apiKey) throw new Error("API key is required (or use --no-auth for keyless endpoints)");
    }
  }

  let models = [];
  if (!flags.model) {
    process.stdout.write(`${ui.dim("Fetching model list...")}\n`);
    try {
      models = await catalog.fetchModels(base, apiKey, catalog.attributionHeaders(base));
    } catch (err) {
      process.stdout.write(`${ui.dim("Could not fetch models:")} ${err.message}\n`);
      models = preset.models || [];
    }
    models = catalog.filterChat(models);
    if (!models.length) {
      process.stdout.write(`${ui.dim("Enter the model ID exactly as the provider expects it.")}\n`);
    }
  }

  let model = String(flags.model || "").trim();
  if (!model) model = await promptModelWithDefault(models, "");
  const sessionModels = await promptModelsForSession(models, model, cfg.profiles[name] && cfg.profiles[name].models);

  const limits = config.withDefaults(preset.limits || config.defaultLimits());

  const profile = {
    provider: name,
    model,
    base_url: base,
    limits: {
      max_concurrency: limits.max_concurrency,
      requests_per_window: limits.requests_per_window,
      window_seconds: limits.window_seconds,
    },
  };
  if (sessionModels && sessionModels.length) profile.models = sessionModels;
  const fallbacks = [];
  for (const raw of String(flags.fallbacks || "").split(",")) {
    const ref = raw.trim();
    if (!ref) continue;
    if (!ref.includes("/")) throw new Error(`invalid fallback "${ref}"; expected provider/model`);
    fallbacks.push(ref);
  }
  if (fallbacks.length) profile.fallbacks = fallbacks;
  for (const candidate of models) {
    if (candidate.id === model) {
      if (candidate.contextWindow > 0) profile.context_window = candidate.contextWindow;
      if (candidate.maxOutput > 0) profile.max_output_tokens = candidate.maxOutput;
      break;
    }
  }

  if (apiKey) {
    try {
      keystore.set(name, apiKey);
    } catch (err) {
      throw new Error(`store API key: ${err.message}`);
    }
    if (keystore.source(name) === "file") {
      process.stderr.write(
        "warning: no OS keyring available; the key was stored in the local secrets file (~/.claude-custom/secrets.json)\n"
      );
    }
  }
  cfg.providers[name] = { preset: preset.id, base_url: base, no_auth: Boolean(flags["no-auth"]) };
  cfg.profiles[name] = profile;
  cfg.default_profile = name;
  config.save(cfg);

  process.stdout.write("\n");
  process.stdout.write(`Profile "${name}" saved: ${base} -> ${model}\n`);
  if (fallbacks.length) process.stdout.write(`Fallbacks: ${fallbacks.join(", ")}\n`);
  if (preset.keyURL && !apiKey && !flags["no-auth"]) {
    process.stdout.write(`Get a key at ${preset.keyURL}\n`);
  }
  process.stdout.write(`${ui.dim("Setup complete.")}\n`);
  if (!flags["no-launch"] && process.stdin.isTTY && process.stdout.isTTY) {
    try {
      const { launchClaude } = require("./commands.js");
      await launchClaude();
    } catch (err) {
      if (err && err.name === "ExitError") throw err;
      process.stdout.write(`claude-custom: Claude Code did not start (${err && err.message ? err.message : err})\n`);
      process.stdout.write("Run `claude-custom run` to launch it later.\n");
    }
  }
}

module.exports = { setupCommand, promptModelWithDefault, isLocalURL };
