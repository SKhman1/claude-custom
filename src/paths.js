"use strict";

const os = require("os");
const path = require("path");

const DIR_NAME = ".claude-custom";

function dir() {
  const override = (process.env.CLAUDE_CUSTOM_HOME || "").trim();
  if (override) {
    return override;
  }
  return path.join(os.homedir(), DIR_NAME);
}

function configPath() {
  return path.join(dir(), "config.json");
}

function secretsPath() {
  return path.join(dir(), "secrets.json");
}

function runPath() {
  return path.join(dir(), "run.json");
}

function logPath() {
  return path.join(dir(), "logs", "proxy.log");
}

module.exports = { dir, configPath, secretsPath, runPath, logPath, DIR_NAME };
