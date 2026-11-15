"use strict";

const fs = require("fs");
const path = require("path");

const paths = require("./paths.js");

function write(info) {
  const file = paths.runPath();
  fs.mkdirSync(path.dirname(file), { recursive: true, mode: 0o700 });
  const data = JSON.stringify(info, null, 2) + "\n";
  const tmp = file + ".tmp";
  fs.writeFileSync(tmp, data, { mode: 0o600 });
  try {
    fs.unlinkSync(file);
  } catch (err) {
    if (err.code !== "ENOENT") throw err;
  }
  fs.renameSync(tmp, file);
}

function read() {
  const file = paths.runPath();
  let data;
  try {
    data = fs.readFileSync(file, "utf8");
  } catch (err) {
    if (err.code === "ENOENT") return null;
    throw err;
  }
  const parsed = JSON.parse(data);
  if (!parsed || typeof parsed !== "object") return null;
  return parsed;
}

function remove() {
  try {
    fs.unlinkSync(paths.runPath());
  } catch (err) {
    if (err.code !== "ENOENT") throw err;
  }
}

function alive(info) {
  if (!info || !(info.pid > 0)) return false;
  try {
    process.kill(info.pid, 0);
    return true;
  } catch (err) {
    return err.code === "EPERM";
  }
}

module.exports = { write, read, remove, alive };
