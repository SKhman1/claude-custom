"use strict";

const ui = require("./ui.js");

const CANCELED = "CANCELED";

function canceledError() {
  const err = new Error("canceled");
  err.code = CANCELED;
  return err;
}

function tuiAvailable() {
  return Boolean(process.stdin.isTTY && process.stdout.isTTY);
}

function stripANSI(text) {
  return String(text).replace(/\u001b\[[0-9;?]*[ -/]*[@-~]/g, "").replace(/\u001b/g, "");
}

function displayWidth(text) {
  return [...stripANSI(text)].length;
}

function truncate(text, maxWidth) {
  const raw = String(text);
  if (maxWidth <= 0) return "";
  if (displayWidth(raw) <= maxWidth) return raw;
  let out = "";
  let visible = 0;
  let sawANSI = false;
  for (let i = 0; i < raw.length; ) {
    if (raw[i] === "\u001b") {
      const match = /^\u001b\[[0-9;?]*[ -/]*[@-~]/.exec(raw.slice(i));
      if (match) {
        out += match[0];
        sawANSI = true;
        i += match[0].length;
        continue;
      }
      i += 1;
      continue;
    }
    const char = String.fromCodePoint(raw.codePointAt(i));
    if (visible >= maxWidth - 1) break;
    out += char;
    visible += 1;
    i += char.length;
  }
  return out + "ÃƒÂ¢Ã¢â€šÂ¬Ã‚Â¦" + (sawANSI ? "\u001b[0m" : "");
}

function normalizeItems(items) {
  return items.map((item) => {
    if (typeof item === "string") {
      const split = item.indexOf("  ");
      if (split > 0) {
        return { label: item.slice(0, split), detail: item.slice(split + 2), value: item.slice(0, split) };
      }
      return { label: item, detail: "", value: item };
    }
    return {
      label: String(item.label == null ? "" : item.label),
      detail: String(item.detail == null ? "" : item.detail),
      value: item.value == null ? String(item.label == null ? "" : item.label) : String(item.value),
    };
  });
}

function filterItems(items, query) {
  const needle = String(query || "").trim().toLowerCase();
  if (!needle) return items.map((item, index) => ({ item, index }));
  return items
    .map((item, index) => ({ item, index }))
    .filter(({ item }) => `${item.label} ${item.detail} ${item.value}`.toLowerCase().includes(needle));
}

function labelWidth(rows) {
  let width = 0;
  for (const row of rows) width = Math.max(width, [...row.item.label].length);
  return Math.min(width, 56);
}

function renderMenu(title, rows, query, cursor, offset, options) {
  const lines = [`${ui.cyan("?")} ${ui.bold(title)}`];
  if (options.search && query) lines.push(`  ${ui.dim("search: ")}${query}`);
  lines.push("");
  const end = Math.min(offset + options.pageSize, rows.length);
  const width = labelWidth(rows);
  for (let i = offset; i < end; i++) {
    const item = rows[i].item;
    const marker = i === cursor ? ui.cyan("ÃƒÂ¢Ã‚ÂÃ‚Â¯ ") : "  ";
    const label = i === cursor ? ui.bold(item.label) : item.label;
    const padding = Math.max(width - [...item.label].length, 2);
    let line = marker;
    line += label + " ".repeat(padding);
    if (item.detail) line += ui.dim(item.detail);
    lines.push(line);
  }
  if (!rows.length) lines.push(`  ${ui.dim("no matches")}`);
  if (rows.length > options.pageSize) {
    const first = offset + 1;
    lines.push(`  ${ui.dim(`${first}-${end} of ${rows.length}`)}`);
  }
  lines.push("");
  const hint = options.search
    ? "up/down move  type to search  enter select  ctrl+c cancel"
    : "up/down move  enter select  ctrl+c cancel";
  lines.push(`  ${ui.dim(hint)}`);
  return lines.map((line) => truncate(line, options.width));
}

function decode(buffer) {
  if (!buffer.length) return null;
  const first = buffer[0];
  if (first === 3) return { event: { kind: "cancel" }, used: 1 };
  if (first === 13 || first === 10) return { event: { kind: "enter" }, used: 1 };
  if (first === 127 || first === 8) return { event: { kind: "backspace" }, used: 1 };
  if (first === 27) {
    if (buffer.length < 2) return null;
    const introducer = buffer[1];
    if (introducer !== 0x5b && introducer !== 0x4f) {
      return { event: { kind: "cancel" }, used: 1 };
    }
    if (buffer.length < 3) return null;
    const code = buffer[2];
    if (code === 0x41) return { event: { kind: "up" }, used: 3 };
    if (code === 0x42) return { event: { kind: "down" }, used: 3 };
    if (code === 0x48) return { event: { kind: "home" }, used: 3 };
    if (code === 0x46) return { event: { kind: "end" }, used: 3 };
    for (let i = 3; i < buffer.length; i++) {
      const byte = buffer[i];
      if (byte >= 0x40 && byte <= 0x7e) return { event: { kind: "none" }, used: i + 1 };
    }
    return null;
  }
  if (first < 32) return { event: { kind: "none" }, used: 1 };
  let length = 1;
  if (first >= 0xc0 && first < 0xe0) length = 2;
  else if (first >= 0xe0 && first < 0xf0) length = 3;
  else if (first >= 0xf0) length = 4;
  if (buffer.length < length) return null;
  const rune = [...Buffer.from(buffer.slice(0, length)).toString("utf8")][0];
  if (!rune) return { event: { kind: "none" }, used: length };
  return { event: { kind: "rune", rune }, used: length };
}

function selectMenu(spec) {
  const title = String(spec.title || "");
  const items = normalizeItems(spec.items || []);
  if (!items.length || !tuiAvailable()) return Promise.resolve(-1);

  const options = {
    search: Boolean(spec.search),
    pageSize: spec.pageSize && spec.pageSize > 0 ? spec.pageSize : 12,
    width: spec.width && spec.width > 0 ? spec.width : process.stdout.columns || 100,
    height: spec.height && spec.height > 0 ? spec.height : process.stdout.rows || 0,
  };
  const pageSizeAdjust = options.height > 0 ? Math.max(options.height - 6, 1) : 0;
  if (pageSizeAdjust > 0 && options.pageSize > pageSizeAdjust) options.pageSize = pageSizeAdjust;

  const stdin = process.stdin;
  const stdout = process.stdout;
  const wasRaw = stdin.isRaw;
  let drawn = 0;
  let cursor = Number.isInteger(spec.defaultIndex) && spec.defaultIndex >= 0 && spec.defaultIndex < items.length ? spec.defaultIndex : 0;
  let offset = 0;
  let query = "";
  let pending = Buffer.alloc(0);

  const write = (text) => stdout.write(text);

  const draw = (lines) => {
    if (drawn > 0) write(`\u001b[${drawn - 1}A`);
    const heightNow = Math.max(lines.length, drawn);
    for (let i = 0; i < heightNow; i++) {
      write("\r\u001b[2K");
      if (i < lines.length) write(lines[i]);
      if (i < heightNow - 1) write("\n");
    }
    drawn = heightNow;
  };

  const clamp = (visible) => {
    if (cursor >= visible.length) cursor = Math.max(visible.length - 1, 0);
    if (cursor < 0) cursor = 0;
    if (cursor < offset) offset = cursor;
    if (cursor >= offset + options.pageSize) offset = cursor - options.pageSize + 1;
    if (offset < 0) offset = 0;
    const maxOffset = Math.max(visible.length - options.pageSize, 0);
    if (offset > maxOffset) offset = maxOffset;
  };

  return new Promise((resolve, reject) => {
    let settled = false;

    const finish = (resultLine, value, error) => {
      if (settled) return;
      settled = true;
      stdin.removeListener("data", onData);
      stdin.removeListener("error", onError);
      write("\u001b[?25h");
      if (drawn > 0) write(`\u001b[${drawn - 1}A`);
      for (let i = 0; i < drawn; i++) {
        write("\r\u001b[2K");
        if (i < drawn - 1) write("\n");
      }
      drawn = 0;
      if (resultLine) write(`${resultLine}\n`);
      try {
        stdin.setRawMode(Boolean(wasRaw));
      } catch (_) {
        // stream already closed
      }
      stdin.pause();
      if (error) reject(error);
      else resolve(value);
    };

    const apply = (event) => {
      const visible = filterItems(items, query);
      clamp(visible);
      switch (event.kind) {
        case "cancel":
          finish(null, -1, canceledError());
          return false;
        case "up":
          if (cursor > 0) cursor -= 1;
          break;
        case "down":
          if (cursor < visible.length - 1) cursor += 1;
          break;
        case "home":
          cursor = 0;
          break;
        case "end":
          if (visible.length) cursor = visible.length - 1;
          break;
        case "backspace":
          if (options.search && query) {
            query = [...query].slice(0, -1).join("");
            cursor = 0;
            offset = 0;
          }
          break;
        case "rune":
          if (options.search) {
            query += event.rune;
            cursor = 0;
            offset = 0;
          }
          break;
        case "enter": {
          if (!visible.length) break;
          const chosen = visible[cursor];
          finish(`${ui.cyan("?")} ${title}: ${ui.bold(chosen.item.label)}`, chosen.index);
          return false;
        }
        default:
          break;
      }
      return true;
    };

    const onData = (chunk) => {
      if (settled) return;
      pending = Buffer.concat([pending, Buffer.from(chunk)]);
      for (;;) {
        const decoded = decode(pending);
        if (!decoded) return;
        pending = pending.slice(decoded.used);
        if (!apply(decoded.event)) return;
        const visible = filterItems(items, query);
        clamp(visible);
        draw(renderMenu(title, visible, query, cursor, offset, options));
      }
    };

    const onError = (err) => finish(null, -1, err);

    try {
      stdin.setRawMode(true);
    } catch (_) {
      resolve(-1);
      return;
    }
    stdin.resume();
    write("\u001b[?25l");
    const visible = filterItems(items, query);
    clamp(visible);
    draw(renderMenu(title, visible, query, cursor, offset, options));
    stdin.on("data", onData);
    stdin.on("error", onError);
    process.once("exit", () => {
      if (!settled) write("\u001b[?25h");
    });
  });
}

module.exports = { selectMenu, tuiAvailable, displayWidth, truncate, decode };
