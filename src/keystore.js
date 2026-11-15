"use strict";

const fs = require("fs");
const path = require("path");

const paths = require("./paths.js");
const keyring = require("./keyring.js");

function envName(provider) {
  let out = "CLAUDE_CUSTOM_";
  for (const ch of String(provider).toUpperCase()) {
    out += /[A-Z0-9]/.test(ch) ? ch : "_";
  }
  return out + "_KEY";
}

function readFile() {
  const file = paths.secretsPath();
  let data;
  try {
    data = fs.readFileSync(file, "utf8");
  } catch (err) {
    if (err.code === "ENOENT") return {};
    throw err;
  }
  try {
    const parsed = JSON.parse(data);
    return parsed && typeof parsed === "object" ? parsed : {};
  } catch (err) {
    throw new Error(`parse ${file}: ${err.message}`);
  }
}

function writeFile(store) {
  const file = paths.secretsPath();
  fs.mkdirSync(path.dirname(file), { recursive: true, mode: 0o700 });
  const data = JSON.stringify(store, null, 2) + "\n";
  const tmp = file + ".tmp";
  fs.writeFileSync(tmp, data, { mode: 0o600 });
  try {
    fs.unlinkSync(file);
  } catch (err) {
    if (err.code !== "ENOENT") throw err;
  }
  fs.renameSync(tmp, file);
}

function get(provider) {
  const fromEnv = (process.env[envName(provider)] || "").trim();
  if (fromEnv) return fromEnv;
  const stored = readFile()[provider];
  if (typeof stored === "string" && stored) return stored;
  const legacy = keyring.readCredential(provider);
  if (legacy) return legacy;
  return "";
}

function set(provider, secret) {
  if (!String(secret || "").trim()) {
    throw new Error("secret is empty");
  }
  const store = readFile();
  store[provider] = secret;
  writeFile(store);
}

function remove(provider) {
  const store = readFile();
  if (!(provider in store)) {
    throw new Error("secret not found");
  }
  delete store[provider];
  writeFile(store);
}

function has(provider) {
  return get(provider) !== "";
}

function source(provider) {
  if ((process.env[envName(provider)] || "").trim()) return "environment";
  const stored = readFile()[provider];
  if (typeof stored === "string" && stored) return "file";
  if (keyring.readCredential(provider)) return "keyring";
  return "missing";
}

module.exports = { envName, get, set, delete: remove, has, source };
