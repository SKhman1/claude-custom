"use strict";

const fs = require("fs");
const path = require("path");
const { spawn } = require("child_process");
const paths = require("./paths.js");

class NotFoundError extends Error {
  constructor() {
    super("claude executable not found on PATH");
    this.name = "NotFoundError";
  }
}

class ExitError extends Error {
  constructor(code) {
    super(`claude exited with code ${code}`);
    this.name = "ExitError";
    this.exitCode = code;
  }
}

function isWindows() {
  return process.platform === "win32";
}

function pathExts() {
  const raw = process.env.PATHEXT || ".COM;.EXE;.BAT;.CMD;.VBS;.JS;.WS;.MSC";
  return raw
    .split(";")
    .map((ext) => ext.trim())
    .filter(Boolean);
}

function isExecutableFile(file) {
  try {
    const stat = fs.statSync(file);
    if (!stat.isFile()) return false;
    if (!isWindows()) fs.accessSync(file, fs.constants.X_OK);
    return true;
  } catch (err) {
    return false;
  }
}

function lookPath() {
  const dirs = (process.env.PATH || "").split(path.delimiter).filter(Boolean);
  for (const dir of dirs) {
    if (isWindows()) {
      for (const ext of pathExts()) {
        const candidate = path.join(dir, `claude${ext}`);
        if (isExecutableFile(candidate)) return candidate;
      }
      const bare = path.join(dir, "claude");
      if (isExecutableFile(bare)) return bare;
      continue;
    }
    const candidate = path.join(dir, "claude");
    if (isExecutableFile(candidate)) return candidate;
  }
  return null;
}

function autoCompactWindow(contextWindow) {
  const fallback = 190000;
  if (!(contextWindow > 0)) return fallback;
  let limit = Math.floor((contextWindow * 9) / 10);
  if (limit > fallback) return fallback;
  if (limit < 4096) return 4096;
  return limit;
}

function env(baseEnv, proxyURL, token, contextWindow) {
  const out = [];
  for (const entry of baseEnv) {
    const index = entry.indexOf("=");
    const name = index >= 0 ? entry.slice(0, index) : entry;
    if (name.startsWith("ANTHROPIC_")) continue;
    if (name === "CLAUDE_CODE_DISABLE_NONESSENTIAL_TRAFFIC") continue;
    out.push(entry);
  }
  out.push(
    `CLAUDE_CONFIG_DIR=${paths.cliDir()}`,
    `ANTHROPIC_BASE_URL=${proxyURL}`,
    `ANTHROPIC_AUTH_TOKEN=${token}`,
    "CLAUDE_CODE_ENABLE_GATEWAY_MODEL_DISCOVERY=1",
    "CLAUDE_CODE_AUTO_MODE_SERVER=0",
    "CLAUDE_CODE_DISABLE_ADVISOR_TOOL=1",
    `CLAUDE_CODE_AUTO_COMPACT_WINDOW=${autoCompactWindow(contextWindow)}`,
    "CLAUDE_ENABLE_STREAM_WATCHDOG=0",
    "CLAUDE_ENABLE_BYTE_WATCHDOG=0",
    "API_FORCE_IDLE_TIMEOUT=0",
    "API_TIMEOUT_MS=2147483647",
    "DISABLE_AUTOUPDATER=1",
    "DISABLE_FEEDBACK_COMMAND=1",
    "DISABLE_ERROR_REPORTING=1"
  );
  return out;
}

function quoteWinArg(value) {
  const text = String(value);
  if (text !== "" && !/[\s"&|<>^%]/.test(text)) return text;
  return `"${text.replace(/"/g, '\\"')}"`;
}

function commandFor(exe, args) {
  const ext = path.extname(exe).toLowerCase();
  if (isWindows() && (ext === ".cmd" || ext === ".bat")) {
    const shell = process.env.ComSpec || "cmd.exe";
    return {
      command: shell,
      args: ["/d", "/s", "/c", [exe, ...args].map(quoteWinArg).join(" ")],
      verbatim: true,
    };
  }
  return { command: exe, args, verbatim: false };
}

function run(exe, args, childEnv, options) {
  if (!exe) return Promise.reject(new NotFoundError());
  try {
    fs.mkdirSync(paths.cliDir(), { recursive: true, mode: 0o700 });
  } catch (_) {
    // best effort: claude will create it if missing
  }
  const spec = commandFor(exe, args);
  const opts = options || {};
  return new Promise((resolve, reject) => {
    let child;
    try {
      child = spawn(spec.command, spec.args, {
        env: childEnv,
        stdio: "inherit",
        windowsVerbatimArguments: spec.verbatim,
      });
    } catch (err) {
      reject(new Error(`start claude: ${err.message}`));
      return;
    }
    let cancelled = false;
    const kill = () => {
      cancelled = true;
      try {
        child.kill();
      } catch (err) {
        /* ignore */
      }
    };
    if (opts.signal) {
      if (opts.signal.aborted) kill();
      else opts.signal.addEventListener("abort", kill, { once: true });
    }
    child.once("error", (err) => {
      reject(new Error(`start claude: ${err.message}`));
    });
    child.once("exit", (code) => {
      if (opts.signal) opts.signal.removeEventListener("abort", kill);
      if (cancelled) {
        const err = new ExitError(code === null ? 1 : code);
        err.cancelled = true;
        reject(err);
        return;
      }
      if (code === null) {
        reject(new ExitError(1));
        return;
      }
      if (code === 0) {
        resolve(0);
        return;
      }
      reject(new ExitError(code));
    });
  });
}

module.exports = {
  NotFoundError,
  ExitError,
  lookPath,
  autoCompactWindow,
  env,
  run,
};
