"use strict";

const fs = require("fs");
const path = require("path");

const paths = require("./paths.js");

const VERSION = 1;

function defaultLimits() {
  return { max_concurrency: 1, requests_per_window: 1, window_seconds: 2 };
}

function withDefaults(limits) {
  const d = defaultLimits();
  const out = limits || {};
  return {
    max_concurrency: out.max_concurrency > 0 ? out.max_concurrency : d.max_concurrency,
    requests_per_window: out.requests_per_window > 0 ? out.requests_per_window : d.requests_per_window,
    window_seconds: out.window_seconds > 0 ? out.window_seconds : d.window_seconds,
  };
}

function defaults() {
  return {
    version: VERSION,
    default_profile: "",
    profiles: {},
    providers: {},
    server: { bind: "127.0.0.1", port: 8787 },
  };
}

function load() {
  const file = paths.configPath();
  let data;
  try {
    data = fs.readFileSync(file, "utf8");
  } catch (err) {
    if (err.code === "ENOENT") {
      return defaults();
    }
    throw err;
  }
  data = data.replace(/^\uFEFF/, "");
  let cfg;
  try {
    cfg = JSON.parse(data);
  } catch (err) {
    throw new Error(`parse ${file}: ${err.message}`);
  }
  if (!cfg || typeof cfg !== "object") {
    throw new Error(`parse ${file}: expected a JSON object`);
  }
  if (!cfg.version) cfg.version = VERSION;
  if (!cfg.profiles) cfg.profiles = {};
  if (!cfg.providers) cfg.providers = {};
  if (!cfg.server) cfg.server = {};
  if (!cfg.server.bind) cfg.server.bind = "127.0.0.1";
  if (!cfg.server.port) cfg.server.port = 8787;
  return cfg;
}

function save(cfg) {
  const file = paths.configPath();
  fs.mkdirSync(path.dirname(file), { recursive: true, mode: 0o700 });
  const data = JSON.stringify(cfg, null, 2) + "\n";
  const tmp = file + ".tmp";
  fs.writeFileSync(tmp, data, { mode: 0o600 });
  try {
    fs.unlinkSync(file);
  } catch (err) {
    if (err.code !== "ENOENT") throw err;
  }
  fs.renameSync(tmp, file);
}

function resolve(cfg, name) {
  let profileName = (name || "").trim();
  if (!profileName) {
    profileName = cfg.default_profile || "";
  }
  if (!profileName) {
    throw new Error("no profile configured; run `claude-custom setup`");
  }
  const profile = (cfg.profiles || {})[profileName];
  if (!profile) {
    throw new Error(`profile "${profileName}" not found`);
  }
  const provider = (cfg.providers || {})[profile.provider];
  if (!provider) {
    throw new Error(`provider "${profile.provider}" for profile "${profileName}" not found`);
  }
  const out = Object.assign({}, profile);
  out.limits = withDefaults(profile.limits);
  out.base_url = profile.base_url || provider.base_url || "";
  if (!out.base_url) {
    throw new Error(`profile "${profileName}" has no base URL`);
  }
  if (!out.model) {
    throw new Error(`profile "${profileName}" has no model`);
  }
  return { name: profileName, profile: out, provider };
}

function applyEnv(cfg) {
  const port = (process.env.CLAUDE_CUSTOM_PORT || "").trim();
  if (port) {
    const parsed = Number.parseInt(port, 10);
    if (Number.isInteger(parsed) && parsed > 0 && parsed < 65536) {
      cfg.server.port = parsed;
    }
  }
  const profile = (process.env.CLAUDE_CUSTOM_PROFILE || "").trim();
  if (profile) {
    cfg.default_profile = profile;
  }
  return cfg;
}

module.exports = {
  VERSION,
  defaultLimits,
  withDefaults,
  defaults,
  load,
  save,
  resolve,
  applyEnv,
};
