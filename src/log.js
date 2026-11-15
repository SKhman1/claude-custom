"use strict";

const fs = require("fs");
const path = require("path");

const paths = require("./paths.js");

const MAX_SIZE = 10 * 1024 * 1024;

function rotate(file) {
  let size = 0;
  try {
    size = fs.statSync(file).size;
  } catch (err) {
    return;
  }
  if (size < MAX_SIZE) return;
  for (let i = 4; i >= 1; i--) {
    const from = `${file}.${i}`;
    const to = `${file}.${i + 1}`;
    try {
      fs.renameSync(from, to);
    } catch (err) {
      if (err.code !== "ENOENT") throw err;
    }
  }
  try {
    fs.renameSync(file, `${file}.1`);
  } catch (err) {
    if (err.code !== "ENOENT") throw err;
  }
}

function timestamp() {
  const now = new Date();
  const pad = (value) => String(value).padStart(2, "0");
  return (
    `${now.getFullYear()}/${pad(now.getMonth() + 1)}/${pad(now.getDate())} ` +
    `${pad(now.getHours())}:${pad(now.getMinutes())}:${pad(now.getSeconds())}`
  );
}

function create(file) {
  let target = file;
  let enabled = true;
  if (!target) {
    try {
      target = paths.logPath();
    } catch (err) {
      enabled = false;
    }
  }
  if (enabled) {
    try {
      fs.mkdirSync(path.dirname(target), { recursive: true, mode: 0o700 });
      rotate(target);
    } catch (err) {
      process.stderr.write(`claude-custom: cannot prepare log file: ${err.message}\n`);
      enabled = false;
    }
  }
  return {
    info(message) {
      const line = `${timestamp()} claude-custom ${message}\n`;
      if (enabled) {
        try {
          fs.appendFileSync(target, line, { mode: 0o600 });
          return;
        } catch (err) {
          enabled = false;
        }
      }
      process.stderr.write(line);
    },
    error(message) {
      this.info(message);
    },
    close() {},
  };
}

function noop() {
  return {
    info() {},
    error() {},
    close() {},
  };
}

module.exports = { create, noop, rotate };
