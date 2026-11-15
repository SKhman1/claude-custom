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

function cliDir() {
  return path.join(dir(), "cli");
}

function logPath() {
  return path.join(dir(), "logs", "proxy.log");
}

module.exports = { dir, configPath, secretsPath, runPath, logPath, cliDir, DIR_NAME };
