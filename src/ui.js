"use strict";

function enabled() {
  if (process.env.NO_COLOR) return false;
  if (process.env.FORCE_COLOR && process.env.FORCE_COLOR !== "0") return true;
  return Boolean(process.stdout.isTTY);
}

function wrap(open, close) {
  return (value) => (enabled() ? `\u001b[${open}m${value}\u001b[${close}m` : String(value));
}

const bold = wrap("1", "22");
const dim = wrap("2", "22");
const cyan = wrap("36", "39");
const green = wrap("32", "39");
const yellow = wrap("33", "39");
const red = wrap("31", "39");

module.exports = { bold, dim, cyan, green, yellow, red, enabled };
