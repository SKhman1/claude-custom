"use strict";

class UsageError extends Error {
  constructor(message) {
    super(message);
    this.name = "UsageError";
  }
}

function parse(args, defs, synopsis) {
  const values = {};
  for (const [name, def] of Object.entries(defs)) {
    values[name] = def.default;
  }
  const rest = [];
  let index = 0;
  while (index < args.length) {
    const arg = args[index];
    if (arg === "--") {
      rest.push(...args.slice(index + 1));
      break;
    }
    if (arg === "-h" || arg === "--help" || arg === "-help") {
      return { help: true, values, rest, synopsis };
    }
    if (!arg.startsWith("-") || arg === "-") {
      rest.push(...args.slice(index));
      break;
    }
    const prefix = arg.startsWith("--") ? 2 : 1;
    const body = arg.slice(prefix);
    const eq = body.indexOf("=");
    const name = eq >= 0 ? body.slice(0, eq) : body;
    const inline = eq >= 0 ? body.slice(eq + 1) : undefined;
    const def = defs[name];
    if (!def) throw new UsageError(`flag provided but not defined: -${name}`);
    if (def.type === "bool") {
      values[name] = inline === undefined ? true : inline === "true" || inline === "1";
      index += 1;
      continue;
    }
    let raw = inline;
    if (raw === undefined) {
      index += 1;
      if (index >= args.length) throw new UsageError(`flag needs an argument: -${name}`);
      raw = args[index];
    }
    if (def.type === "int") {
      const parsed = Number.parseInt(raw, 10);
      if (!Number.isInteger(parsed)) throw new UsageError(`invalid value "${raw}" for flag -${name}`);
      values[name] = parsed;
    } else {
      values[name] = raw;
    }
    index += 1;
  }
  return { help: false, values, rest, synopsis };
}

function usageText(synopsis, defs) {
  const lines = [`Usage: claude-custom ${synopsis}`, "", "Flags:"];
  for (const [name, def] of Object.entries(defs)) {
    const type = def.type === "bool" ? "" : " value";
    const fallback = def.default === undefined || def.default === false || def.default === "" ? "" : ` (default ${def.default})`;
    lines.push(`  -${name}${type}`, `        ${def.usage || ""}${fallback}`);
  }
  return lines.join("\n");
}

module.exports = { UsageError, parse, usageText };
