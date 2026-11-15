"use strict";

const readline = require("readline");

class CancelledError extends Error {
  constructor() {
    super("cancelled");
    this.name = "CancelledError";
  }
}

let rl = null;

function interfaceFor() {
  if (rl) return rl;
  rl = readline.createInterface({ input: process.stdin, output: process.stdout, terminal: Boolean(process.stdin.isTTY) });
  return rl;
}

function close() {
  if (rl) {
    rl.close();
    rl = null;
  }
}

function ask(question, defaultValue) {
  const suffix = defaultValue ? ` [${defaultValue}]` : "";
  return new Promise((resolve, reject) => {
    const reader = interfaceFor();
    let settled = false;
    const done = (value) => {
      if (settled) return;
      settled = true;
      resolve(value);
    };
    reader.question(`${question}${suffix}: `, (answer) => {
      const text = String(answer).trim();
      done(text === "" ? defaultValue || "" : text);
    });
    reader.once("close", () => done(defaultValue || ""));
    reader.once("SIGINT", () => {
      close();
      reject(new CancelledError());
    });
  });
}

function askSecret(question) {
  if (!process.stdin.isTTY || !process.stdout.isTTY) {
    return ask(question, "");
  }
  return new Promise((resolve, reject) => {
    process.stdout.write(question);
    const stdin = process.stdin;
    const wasRaw = stdin.isRaw;
    stdin.setRawMode(true);
    stdin.resume();
    stdin.setEncoding("utf8");
    let value = "";
    let done = false;

    const finish = (fn, payload) => {
      if (done) return;
      done = true;
      stdin.removeListener("data", onData);
      stdin.setRawMode(Boolean(wasRaw));
      if (!stdin.isTTY) stdin.pause();
      fn(payload);
    };

    const onData = (chunk) => {
      for (const ch of chunk) {
        if (ch === "\u0003") {
          process.stdout.write("\n");
          finish(reject, new CancelledError());
          return;
        }
        if (ch === "\r" || ch === "\n") {
          process.stdout.write("\n");
          finish(resolve, value);
          return;
        }
        if (ch === "\u007f" || ch === "\b") {
          if (value) {
            value = value.slice(0, -1);
            process.stdout.write("\b \b");
          }
          continue;
        }
        if (ch < " ") continue;
        value += ch;
        process.stdout.write("*");
      }
    };

    stdin.on("data", onData);
  });
}

function confirm(question, defaultYes) {
  const hint = defaultYes ? "[Y/n]" : "[y/N]";
  return ask(`${question} ${hint}`, defaultYes ? "y" : "n").then((answer) => {
    const text = answer.toLowerCase();
    if (text === "y" || text === "yes") return true;
    if (text === "n" || text === "no") return false;
    return Boolean(defaultYes);
  });
}

async function choose(title, items, defaultIndex, options) {
  if (!items.length) return -1;
  const tui = require("./tui.js");
  const settings = options || {};
  if (tui.tuiAvailable()) {
    try {
      const index = await tui.selectMenu({
        title,
        items,
        defaultIndex,
        search: settings.search !== false,
        pageSize: settings.pageSize,
      });
      return index;
    } catch (err) {
      if (err && err.code === "CANCELED") throw new CancelledError();
      throw err;
    }
  }
  process.stdout.write(`${title}\n`);
  items.forEach((item, index) => {
    const label = typeof item === "string" ? item : item.label;
    const detail = typeof item === "string" ? "" : item.detail;
    process.stdout.write(`  ${index + 1}. ${label}${detail ? `  ${detail}` : ""}\n`);
  });
  const initial = defaultIndex >= 0 && defaultIndex < items.length ? defaultIndex : 0;
  for (;;) {
    const answer = await ask("Select", String(initial + 1));
    const number = Number.parseInt(answer, 10);
    if (Number.isInteger(number) && number >= 1 && number <= items.length) return number - 1;
    process.stdout.write(`Enter a number between 1 and ${items.length}.\n`);
  }
}

module.exports = {
  CancelledError,
  ask,
  askSecret,
  confirm,
  choose,
  close,
};
