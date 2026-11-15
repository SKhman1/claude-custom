"use strict";

const http = require("http");
const crypto = require("crypto");
const { Readable } = require("stream");
const { StringDecoder } = require("string_decoder");

const config = require("./config.js");
const catalog = require("./presets.js");
const translate = require("./translate.js");

const MAX_ATTEMPTS = 10;
const BASE_BACKOFF = 250;
const MAX_BACKOFF = 8000;
const MAX_RETRY_AFTER_WAIT = 15000;
const EARLY_HOLDBACK = 750;
const EARLY_HOLDBACK_MAX = 64 * 1024;
const MAX_RECOVERIES = 4;
const MAX_RECOVERED_TEXT = 2 * 1024 * 1024;
const BREAKER_THRESHOLD = 5;
const BREAKER_COOLDOWN = 30000;
const MODELS_TTL = 5 * 60 * 1000;
const MODELS_FAIL_TTL = 30 * 1000;
const MODELS_TIMEOUT = 10 * 1000;

class AbortedError extends Error {
  constructor(message) {
    super(message || "request aborted");
    this.name = "AbortedError";
    this.aborted = true;
  }
}

class OverloadedError extends Error {
  constructor() {
    super("admission queue is full");
    this.name = "OverloadedError";
  }
}

function sleep(ms) {
  return new Promise((resolve) => setTimeout(resolve, ms));
}

function abortableSleep(ms, signal) {
  return new Promise((resolve, reject) => {
    if (signal && signal.aborted) {
      reject(new AbortedError());
      return;
    }
    const timer = setTimeout(() => {
      if (signal) signal.removeEventListener("abort", onAbort);
      resolve();
    }, ms);
    const onAbort = () => {
      clearTimeout(timer);
      reject(new AbortedError());
    };
    if (signal) signal.addEventListener("abort", onAbort, { once: true });
  });
}

function snippet(value) {
  const text = String(value || "").trim();
  return text.length > 800 ? `${text.slice(0, 800)}...` : text;
}

function describeFetchError(err) {
  if (!err) return "request failed";
  const cause = err.cause;
  if (cause) {
    const code = cause.code ? `${cause.code}: ` : "";
    return code + (cause.message || err.message || "request failed");
  }
  return err.message || String(err);
}

function isTokenRateLimit(status, body) {
  if (status !== 413 && status !== 429) return false;
  const text = String(body || "");
  return (
    text.includes("rate_limit_exceeded") ||
    text.includes('"type":"tokens"') ||
    text.includes('"type": "tokens"')
  );
}

function isDailyQuota(body) {
  const text = String(body || "").toLowerCase();
  return (
    text.includes("per-day") ||
    text.includes("per_day") ||
    text.includes("daily") ||
    text.includes("free_daily_limit")
  );
}

const QUOTA_RESET_PATTERN = /X-RateLimit-Reset"\s*:\s*"?(\d{10,19})/;

function quotaResetTime(body) {
  const match = QUOTA_RESET_PATTERN.exec(String(body || ""));
  if (!match) return "";
  const raw = match[1];
  let millis = Number.parseInt(raw, 10);
  if (!Number.isFinite(millis)) return "";
  if (raw.length > 11) millis = Math.floor(millis / 1000);
  const date = new Date(millis * 1000);
  const pad = (n) => String(n).padStart(2, "0");
  const zone =
    new Intl.DateTimeFormat("en-US", { timeZoneName: "short" })
      .formatToParts(date)
      .find((part) => part.type === "timeZoneName") || { value: "UTC" };
  return (
    `${date.getFullYear()}-${pad(date.getMonth() + 1)}-${pad(date.getDate())} ` +
    `${pad(date.getHours())}:${pad(date.getMinutes())} ${zone.value}`
  );
}

function isHarnessGate(status, body) {
  if (status !== 403) return false;
  const text = String(body || "");
  return text.includes("agentic harness") || text.includes("Gate Free Endpoints by Agentic Harness");
}

function isModelUnavailable(status) {
  return status === 404;
}

function retryableStatus(status) {
  switch (status) {
    case 408:
    case 409:
    case 425:
    case 429:
    case 500:
    case 502:
    case 503:
    case 504:
    case 529:
      return true;
    default:
      return status >= 520 && status <= 527;
  }
}

const MAX_TOKENS_PATTERN = /max_tokens[^0-9]{0,80}?(\d{2,7})/i;

function maxTokensLimit(status, body) {
  if (status !== 400) return 0;
  const text = String(body || "");
  if (!text.toLowerCase().includes("max_tokens")) return 0;
  const match = MAX_TOKENS_PATTERN.exec(text);
  if (!match) return 0;
  const limit = Number.parseInt(match[1], 10);
  return Number.isFinite(limit) && limit > 0 ? limit : 0;
}

function withMaxTokens(payload, limit) {
  let body;
  try {
    body = JSON.parse(payload);
  } catch (err) {
    return { payload, changed: false };
  }
  if (!body || typeof body !== "object") return { payload, changed: false };
  const current = typeof body.max_tokens === "number" ? body.max_tokens : 0;
  if (body.max_tokens !== undefined && typeof body.max_tokens !== "number") {
    return { payload, changed: false };
  }
  if (current !== 0 && current <= limit) return { payload, changed: false };
  body.max_tokens = limit;
  return { payload: JSON.stringify(body), changed: true };
}

class CircuitBreaker {
  constructor() {
    this.failures = 0;
    this.openUntil = 0;
    this.probing = false;
  }

  allow() {
    const now = Date.now();
    if (!this.openUntil) return true;
    if (now < this.openUntil || this.probing) return false;
    this.probing = true;
    return true;
  }

  isOpen() {
    if (!this.openUntil) return false;
    return Date.now() < this.openUntil;
  }

  record(success) {
    if (success) {
      this.failures = 0;
      this.openUntil = 0;
      this.probing = false;
      return;
    }
    this.probing = false;
    this.failures += 1;
    if (this.failures >= BREAKER_THRESHOLD) {
      this.openUntil = Date.now() + BREAKER_COOLDOWN;
    }
  }

  recordNeutral() {
    this.probing = false;
  }

  openRemaining() {
    if (!this.openUntil) return 0;
    const remaining = this.openUntil - Date.now();
    return remaining > 0 ? remaining : 0;
  }
}

class Limiter {
  constructor(limit, windowMs, concurrency) {
    this.limit = Math.max(1, limit || 1);
    this.windowMs = windowMs > 0 ? windowMs : 1000;
    this.concurrency = Math.max(1, concurrency || 1);
    this.times = [];
    this.active = 0;
    this.maxWaiters = 16;
    this.waiters = 0;
    this.queue = [];
  }

  async acquire(signal) {
    if (this.waiters >= this.maxWaiters) throw new OverloadedError();
    this.waiters += 1;
    let released = false;
    try {
      await this.acquireSemaphore(signal);
    } finally {
      this.waiters -= 1;
    }
    try {
      await this.waitWindow(signal);
    } catch (err) {
      this.releaseSemaphore();
      throw err;
    }
    return () => {
      if (released) return;
      released = true;
      this.releaseSemaphore();
    };
  }

  acquireSemaphore(signal) {
    if (signal && signal.aborted) return Promise.reject(new AbortedError());
    if (this.active < this.concurrency) {
      this.active += 1;
      return Promise.resolve();
    }
    return new Promise((resolve, reject) => {
      const waiter = { resolve, reject };
      const onAbort = () => {
        const index = this.queue.indexOf(waiter);
        if (index >= 0) this.queue.splice(index, 1);
        reject(new AbortedError());
      };
      if (signal) signal.addEventListener("abort", onAbort, { once: true });
      waiter.signal = signal;
      waiter.onAbort = onAbort;
      this.queue.push(waiter);
    });
  }

  releaseSemaphore() {
    const next = this.queue.shift();
    if (next) {
      if (next.signal) next.signal.removeEventListener("abort", next.onAbort);
      next.resolve();
      return;
    }
    this.active = Math.max(0, this.active - 1);
  }

  async waitWindow(signal) {
    for (;;) {
      const now = Date.now();
      const cutoff = now - this.windowMs;
      this.times = this.times.filter((time) => time > cutoff);
      if (this.times.length < this.limit) {
        this.times.push(now);
        return;
      }
      const wait = Math.max(1, this.times[0] + this.windowMs - now);
      await abortableSleep(wait, signal);
    }
  }
}

class HoldWriter {
  constructor(res) {
    this.res = res;
    this.start = Date.now();
    this.buffer = "";
    this.started = false;
  }

  sent() {
    return this.started;
  }

  discard() {
    this.buffer = "";
  }

  startStream() {
    if (this.started) return;
    this.started = true;
    this.res.writeHead(200, {
      "Content-Type": "text/event-stream",
      "Cache-Control": "no-cache",
      Connection: "keep-alive",
      "X-Accel-Buffering": "no",
    });
    if (typeof this.res.flushHeaders === "function") this.res.flushHeaders();
  }

  flush() {
    if (this.buffer) {
      this.res.write(this.buffer);
      this.buffer = "";
    }
  }

  event(name, payload) {
    this.eventLine(translate.sseEvent(name, payload));
  }

  eventLine(line) {
    if (!this.started) {
      this.buffer += line;
      if (Date.now() - this.start >= EARLY_HOLDBACK || Buffer.byteLength(this.buffer) >= EARLY_HOLDBACK_MAX) {
        this.startStream();
        this.flush();
      }
      return;
    }
    this.res.write(line);
  }

  async flushNow() {
    if (!this.started) {
      if (!this.buffer) return;
      this.startStream();
    }
    this.flush();
  }
}

async function readLimited(stream, limit) {
  const decoder = new StringDecoder("utf8");
  let total = 0;
  let out = "";
  for await (const chunk of stream) {
    total += chunk.length;
    if (total > limit) {
      out += decoder.write(chunk.subarray(0, chunk.length - (total - limit)));
      break;
    }
    out += decoder.write(chunk);
  }
  out += decoder.end();
  return out;
}

async function consumeStream(stream, handle) {
  const decoder = new StringDecoder("utf8");
  let pending = "";
  let sawDone = false;
  try {
    for await (const chunk of stream) {
      pending += decoder.write(chunk);
      let index = pending.indexOf("\n");
      while (index >= 0) {
        const line = pending.slice(0, index).replace(/\r$/, "").trim();
        pending = pending.slice(index + 1);
        const result = await handleLine(line, handle);
        if (result.done) return { sawDone: true };
        index = pending.indexOf("\n");
      }
    }
    pending += decoder.end();
    const line = pending.replace(/\r$/, "").trim();
    if (line) {
      const result = await handleLine(line, handle);
      if (result.done) sawDone = true;
    }
  } catch (err) {
    throw new Error(`upstream stream error: ${err.message}`);
  }
  return { sawDone };
}

async function handleLine(line, handle) {
  if (!line || line.startsWith(":") || line.startsWith("event:")) return { done: false };
  let data = line;
  if (line.startsWith("data:")) data = line.slice(5).trim();
  if (!data) return { done: false };
  if (data === "[DONE]") return { done: true };
  if (!data.startsWith("{")) return { done: false };
  let chunk;
  try {
    chunk = JSON.parse(data);
  } catch (err) {
    throw new Error(`invalid upstream stream data: ${err.message}`);
  }
  if (chunk && chunk.error) {
    throw new Error(chunk.error.message || "upstream error");
  }
  await handle(chunk);
  return { done: false };
}

function chunkText(chunk) {
  const choices = (chunk && chunk.choices) || [];
  if (!choices.length) return "";
  const choice = choices[0];
  if (choice.delta && choice.delta.content) return choice.delta.content;
  if (choice.message && typeof choice.message.content === "string") return choice.message.content;
  return "";
}

function stripOverlap(prior, continuation) {
  let max = Math.min(prior.length, continuation.length);
  if (max > 2000) max = 2000;
  for (let n = max; n > 0; n--) {
    if (prior.endsWith(continuation.slice(0, n))) {
      return continuation.slice(n);
    }
  }
  return continuation;
}

function triesFor(index, targetCount) {
  return index + 1 < targetCount ? 1 : MAX_ATTEMPTS;
}

function isClaudeAlias(requested) {
  if (!requested || requested === "claude-custom") return true;
  return String(requested).startsWith("claude-");
}

class ProxyServer {
  constructor(cfg, options) {
    const resolved = config.resolve(cfg, options.profileName);
    this.cfg = cfg;
    this.name = options.profileName || "";
    this.profile = resolved.profile;
    this.provider = Object.assign({}, resolved.provider);
    this.provider.headers = catalog.attributionHeaders(this.profile.base_url, resolved.provider.headers);
    this.model = this.profile.model;
    this.apiKey = options.apiKey || "";
    this.token = options.token || "";
    this.fallbacks = options.fallbacks || [];
    this.logger = options.logger || null;
    this.onStop = options.onStop || null;
    this.limiter = new Limiter(
      this.profile.limits.requests_per_window,
      this.profile.limits.window_seconds * 1000,
      this.profile.limits.max_concurrency
    );
    this.startedAt = Date.now();
    this.breakers = new Map();
    this.gated = new Set();
    this.maxOut = new Map();
    this.modelsCache = [];
    this.modelsFetchedAt = 0;
    this.modelsFailedAt = 0;
    this.counters = {
      requests: 0,
      streaming: 0,
      retries: 0,
      recoveries: 0,
      fallbacks: 0,
      rate_limited: 0,
      payload_too_large: 0,
      server_errors: 0,
    };
    this.lastError = "";

    this.server = http.createServer((req, res) => {
      this.dispatch(req, res).catch((err) => {
        this.log(`internal error: ${err && err.stack ? err.stack : err}`);
        if (!res.headersSent) {
          this.writeError(res, 500, "api_error", err.message || "internal error");
        } else {
          try {
            res.end();
          } catch (ignored) {
            /* ignore */
          }
        }
      });
    });
    this.server.headersTimeout = 15000;
    this.server.requestTimeout = 60000;
    this.server.keepAliveTimeout = 120000;
    this.listener = null;
    this.stopping = false;
  }

  log(message) {
    if (this.logger) this.logger.info(message);
  }

  setLastError(message) {
    this.lastError = message;
  }

  async dispatch(req, res) {
    const url = new URL(req.url || "/", "http://localhost");
    const path = url.pathname.replace(/\/+$/, "") || "/";
    if (path === "/v1/messages") {
      if (!this.authorized(req)) return this.unauthorized(res);
      return this.handleMessages(req, res);
    }
    if (path === "/v1/messages/count_tokens") {
      if (!this.authorized(req)) return this.unauthorized(res);
      return this.handleCountTokens(req, res);
    }
    if (path === "/v1/models") {
      if (!this.authorized(req)) return this.unauthorized(res);
      return this.handleModels(req, res);
    }
    if (path === "/stop") {
      return this.handleStop(req, res);
    }
    return this.handleHealth(req, res);
  }

  unauthorized(res) {
    this.writeError(res, 401, "authentication_error", "invalid or missing claude-custom token");
  }

  authorized(req) {
    if (!this.token) return true;
    let provided = req.headers["x-api-key"];
    if (Array.isArray(provided)) provided = provided[0];
    if (!provided) {
      const header = req.headers.authorization || "";
      if (header.length > 7 && header.slice(0, 7).toLowerCase() === "bearer ") {
        provided = header.slice(7).trim();
      }
    }
    if (!provided) return false;
    const a = Buffer.from(String(provided));
    const b = Buffer.from(this.token);
    if (a.length !== b.length) return false;
    return crypto.timingSafeEqual(a, b);
  }

  writeJSON(res, status, value) {
    const body = JSON.stringify(value);
    res.writeHead(status, {
      "Content-Type": "application/json",
      "Content-Length": Buffer.byteLength(body),
    });
    res.end(body);
  }

  writeError(res, status, kind, message) {
    this.writeJSON(res, status, {
      type: "error",
      error: { type: kind, message: String(message) },
    });
  }

  start() {
    return this.startPort(this.cfg.server.port);
  }

  startPort(port) {
    const bind = this.cfg.server.bind || "127.0.0.1";
    return new Promise((resolve, reject) => {
      const onError = (err) => {
        this.server.removeListener("error", onError);
        reject(err);
      };
      this.server.once("error", onError);
      this.server.listen(port, bind, () => {
        this.server.removeListener("error", onError);
        this.listener = this.server.address();
        resolve(`${this.listener.address}:${this.listener.port}`);
      });
    });
  }

  shutdown() {
    this.stopping = true;
    return new Promise((resolve) => {
      try {
        this.server.close(() => resolve());
        setTimeout(() => resolve(), 5000).unref();
      } catch (err) {
        resolve();
      }
    });
  }

  profileName() {
    if (this.name) return this.name;
    if (this.cfg.default_profile) return this.cfg.default_profile;
    return this.profile.provider;
  }

  handleHealth(req, res) {
    const status = this.breakerFor(this.primaryTarget()).isOpen() ? "degraded" : "ok";
    this.writeJSON(res, 200, {
      status,
      provider: this.profile.provider,
      profile: this.profileName(),
      model: this.model,
      uptime_seconds: Math.floor((Date.now() - this.startedAt) / 1000),
      counters: Object.assign({}, this.counters),
      last_error: this.lastError,
    });
  }

  handleStop(req, res) {
    if (!this.authorized(req)) return this.unauthorized(res);
    if (req.method !== "POST") {
      return this.writeError(res, 405, "invalid_request_error", "use POST");
    }
    this.writeJSON(res, 200, { status: "stopping" });
    if (this.onStop) {
      setImmediate(() => this.onStop());
    } else {
      setImmediate(() => {
        this.shutdown();
      });
    }
  }

  async handleModels(req, res) {
    if (req.method !== "GET") {
      return this.writeError(res, 405, "invalid_request_error", "use GET");
    }
    const display = `${this.profile.provider}/${this.model}`;
    const items = [
      { id: "claude-custom", type: "model", display_name: display, created_at: "2026-01-01T00:00:00Z" },
      {
        id: "claude-sonnet-4-5",
        type: "model",
        display_name: `${display} (alias)`,
        created_at: "2026-01-01T00:00:00Z",
      },
    ];
    const live = catalog.filterChat(await this.liveModels());
    for (const model of this.syntheticList(live)) {
      const item = {
        id: model.alias,
        type: "model",
        display_name: model.id,
        created_at: "2026-01-01T00:00:00Z",
      };
      if (model.name && model.name !== model.id) item.description = model.name;
      items.push(item);
    }
    this.writeJSON(res, 200, {
      data: items,
      has_more: false,
      first_id: items[0].id,
      last_id: items[items.length - 1].id,
    });
  }

  async liveModels() {
    const now = Date.now();
    if (this.modelsCache.length && now - this.modelsFetchedAt < MODELS_TTL) {
      return this.modelsCache;
    }
    if (now - this.modelsFailedAt < MODELS_FAIL_TTL) {
      return [];
    }
    let models = [];
    try {
      models = await catalog.fetchModels(this.profile.base_url, this.apiKey, this.provider.headers);
    } catch (err) {
      this.modelsFailedAt = Date.now();
      this.log(`live model fetch failed: ${err.message}`);
      return [];
    }
    if (!models.length) {
      this.modelsFailedAt = Date.now();
      return [];
    }
    this.modelsCache = models;
    this.modelsFetchedAt = Date.now();
    this.modelsFailedAt = 0;
    return models;
  }

  slugify(id) {
    const slug = String(id)
      .toLowerCase()
      .replace(/[^a-z0-9_-]+/g, "-")
      .replace(/-{2,}/g, "-")
      .replace(/^-+|-+$/g, "")
      .slice(0, 48);
    return slug || "model";
  }

  syntheticList(models) {
    const used = new Set();
    const out = [];
    for (const model of models) {
      const base = this.slugify(model.id);
      let alias = `anthropic--${base}`;
      let n = 2;
      while (used.has(alias)) {
        alias = `anthropic--${base}-${n}`;
        n += 1;
      }
      used.add(alias);
      out.push({ alias, id: model.id, name: model.name });
    }
    return out;
  }

  isSynthetic(id) {
    return typeof id === "string" && id.startsWith("anthropic--");
  }

  async liveModelByAlias(requested) {
    const live = catalog.filterChat(await this.liveModels());
    for (const model of this.syntheticList(live)) {
      if (model.alias === requested) return model.id;
    }
    return null;
  }

  knownLiveModel(id) {
    return this.modelsCache.some((model) => model.id === id);
  }

  async handleCountTokens(req, res) {
    if (req.method !== "POST") {
      return this.writeError(res, 405, "invalid_request_error", "use POST");
    }
    let body;
    try {
      body = await readBody(req, 32 * 1024 * 1024);
    } catch (err) {
      return this.writeError(res, 400, "invalid_request_error", `invalid JSON: ${err.message}`);
    }
    let parsed;
    try {
      parsed = JSON.parse(body);
    } catch (err) {
      return this.writeError(res, 400, "invalid_request_error", `invalid JSON: ${err.message}`);
    }
    let total = 0;
    if (parsed && parsed.system !== undefined) total += Buffer.byteLength(JSON.stringify(parsed.system));
    for (const message of (parsed && parsed.messages) || []) {
      if (message && message.content !== undefined) {
        total += Buffer.byteLength(JSON.stringify(message.content));
      }
    }
    for (const tool of (parsed && parsed.tools) || []) {
      if (!tool) continue;
      total += Buffer.byteLength(String(tool.name || ""));
      total += Buffer.byteLength(String(tool.description || ""));
      if (tool.input_schema !== undefined) total += Buffer.byteLength(JSON.stringify(tool.input_schema));
    }
    const tokens = Math.max(1, Math.floor(total / 4));
    this.writeJSON(res, 200, { input_tokens: tokens });
  }

  resolveModel(requested) {
    return isClaudeAlias(requested) ? this.model : requested;
  }

  primaryTarget() {
    return {
      name: this.profile.provider,
      baseURL: this.profile.base_url,
      apiKey: this.apiKey,
      model: this.model,
      maxOutput: this.profile.max_output_tokens || 0,
      headers: this.provider.headers,
    };
  }

  messageTargets() {
    return [this.primaryTarget(), ...this.fallbacks];
  }

  async targetsFor(requested) {
    if (isClaudeAlias(requested)) return this.messageTargets();
    if (this.isSynthetic(requested)) {
      const real = await this.liveModelByAlias(requested);
      if (real) return [Object.assign(this.primaryTarget(), { model: real })];
      return this.messageTargets();
    }
    for (const fallback of this.fallbacks) {
      if (requested === `${fallback.name}/${fallback.model}`) return [fallback];
    }
    if (this.knownLiveModel(requested)) {
      return [Object.assign(this.primaryTarget(), { model: requested })];
    }
    return this.messageTargets();
  }

  breakerFor(target) {
    const key = `${target.name}|${target.baseURL}|${target.model}`;
    let breaker = this.breakers.get(key);
    if (!breaker) {
      breaker = new CircuitBreaker();
      this.breakers.set(key, breaker);
    }
    return breaker;
  }

  useFallback(target) {
    this.counters.fallbacks += 1;
    this.log(`falling back to ${target.name}/${target.model}`);
  }

  markGated(target) {
    this.gated.add(`${target.baseURL}|${target.model}`);
    this.log(`${target.name}/${target.model} is gated upstream; skipping it for new requests`);
  }

  isGated(target) {
    return this.gated.has(`${target.baseURL}|${target.model}`);
  }

  nextUsable(targets, after) {
    for (let i = after + 1; i < targets.length; i++) {
      if (!this.isGated(targets[i])) return i;
    }
    return -1;
  }

  payloadFor(chatReq, target) {
    const next = Object.assign({}, chatReq);
    next.model = target.model;
    let limit = this.learnedMaxOutput(target);
    if (target.maxOutput > 0 && (!limit || target.maxOutput < limit)) limit = target.maxOutput;
    if (limit > 0 && next.max_tokens > limit) next.max_tokens = limit;
    return JSON.stringify(next);
  }

  learnedMaxOutput(target) {
    return this.maxOut.get(`${target.baseURL}|${target.model}`) || 0;
  }

  learnMaxOutput(target, limit) {
    if (!(limit > 0)) return;
    const key = `${target.baseURL}|${target.model}`;
    const current = this.maxOut.get(key) || 0;
    if (current && current <= limit) return;
    this.maxOut.set(key, limit);
    this.log(`${target.name}/${target.model} caps max_tokens at ${limit}; clamping future requests`);
  }

  async handleMessages(req, res) {
    if (req.method !== "POST") {
      return this.writeError(res, 405, "invalid_request_error", "use POST");
    }
    let body;
    try {
      body = await readBody(req, 64 * 1024 * 1024);
    } catch (err) {
      return this.writeError(res, 400, "invalid_request_error", `invalid JSON: ${err.message}`);
    }
    let parsed;
    try {
      parsed = JSON.parse(body);
    } catch (err) {
      return this.writeError(res, 400, "invalid_request_error", `invalid JSON: ${err.message}`);
    }
    this.counters.requests += 1;
    if (parsed && parsed.stream) this.counters.streaming += 1;
    if (!parsed || !Array.isArray(parsed.messages) || parsed.messages.length === 0) {
      return this.writeError(res, 400, "invalid_request_error", "messages must not be empty");
    }

    const controller = new AbortController();
    res.on("close", () => {
      if (!res.writableEnded) controller.abort();
    });

    let release;
    try {
      release = await this.limiter.acquire(controller.signal);
    } catch (err) {
      if (err.aborted) return;
      return this.writeError(res, 503, "overloaded_error", "too many concurrent requests");
    }
    try {
      const chatModel = this.resolveModel(parsed.model);
      let chatReq;
      try {
        chatReq = translate.buildChatRequest(parsed, chatModel);
      } catch (err) {
        return this.writeError(res, 400, "invalid_request_error", err.message);
      }
      if (this.profile.max_output_tokens > 0 && chatReq.max_tokens > this.profile.max_output_tokens) {
        chatReq.max_tokens = this.profile.max_output_tokens;
      }
      chatReq.stream = Boolean(parsed.stream);
      if (chatReq.stream) {
        chatReq.stream_options = { include_usage: true };
      } else {
        delete chatReq.stream_options;
      }
      if (chatReq.stream) {
        await this.streamMessages(res, chatReq, chatModel, parsed.model, controller.signal);
        if (!res.writableEnded) res.end();
      } else {
        await this.jsonMessages(res, chatReq, chatModel, parsed.model, controller.signal);
      }
    } finally {
      release();
    }
  }

  async jsonMessages(res, chatReq, chatModel, requested, signal) {
    const targets = await this.targetsFor(requested);
    let limitStatus = 0;
    let limitBody = "";
    let skipped = 0;

    for (let i = 0; i < targets.length; i++) {
      const target = targets[i];
      if (this.isGated(target)) {
        skipped += 1;
        this.log(`skipping gated ${target.name}/${target.model}`);
        continue;
      }
      const payload = this.payloadFor(chatReq, target);
      const result = await this.doUpstreamTarget(target, payload, false, triesFor(i, targets.length), signal);
      if (result.error) {
        if (result.retryable && signal.aborted) return;
        const next = this.nextUsable(targets, i);
        if (result.retryable && next >= 0) {
          this.useFallback(targets[next]);
          continue;
        }
        if (limitStatus) {
          this.writeUpstreamError(res, limitStatus, limitBody);
          return;
        }
        this.writeError(res, 502, "api_error", result.error.message);
        return;
      }
      if (result.status < 200 || result.status >= 300) {
        if (result.status === 429 || isTokenRateLimit(result.status, result.body)) {
          limitStatus = result.status;
          limitBody = result.body;
        }
        if (isModelUnavailable(result.status)) {
          this.markGated(target);
          this.log(`${target.name}/${target.model} is not available upstream; skipping it for new requests`);
        }
        const next = this.nextUsable(targets, i);
        if ((result.retryable || isModelUnavailable(result.status)) && next >= 0) {
          this.useFallback(targets[next]);
          continue;
        }
        if (limitStatus && result.status !== limitStatus) {
          this.writeUpstreamError(res, limitStatus, limitBody);
          return;
        }
        this.writeUpstreamError(res, result.status, result.body);
        return;
      }

      let chat;
      try {
        chat = JSON.parse(result.body);
      } catch (err) {
        this.writeError(res, 502, "api_error", `decode upstream response: ${err.message}`);
        return;
      }
      if (chat.error) {
        this.writeError(res, 502, "api_error", chat.error.message || "upstream error");
        return;
      }
      this.writeJSON(res, 200, translate.buildAnthropicResponse(chat, chatModel));
      return;
    }

    if (limitStatus) {
      this.writeUpstreamError(res, limitStatus, limitBody);
      return;
    }
    this.writeError(
      res,
      502,
      "api_error",
      `all providers failed (${skipped} gated upstream by policy; pick another model or provider)`
    );
  }

  async streamMessages(res, chatReq, chatModel, requested, signal) {
    const targets = await this.targetsFor(requested);
    let response = null;
    let body = "";
    let target = null;
    let limitStatus = 0;
    let limitBody = "";

    for (let i = 0; i < targets.length; i++) {
      const candidate = targets[i];
      if (this.isGated(candidate)) {
        this.log(`skipping gated ${candidate.name}/${candidate.model}`);
        continue;
      }
      const payload = this.payloadFor(chatReq, candidate);
      const result = await this.doUpstreamTarget(candidate, payload, true, triesFor(i, targets.length), signal);
      if (result.error) {
        if (signal.aborted) return;
        const next = this.nextUsable(targets, i);
        if (result.retryable && next >= 0) {
          this.useFallback(targets[next]);
          continue;
        }
        this.writeError(res, 502, "api_error", result.error.message);
        return;
      }
      if (result.status < 200 || result.status >= 300) {
        if (result.status === 429 || isTokenRateLimit(result.status, result.body)) {
          limitStatus = result.status;
          limitBody = result.body;
        }
        if (isModelUnavailable(result.status)) {
          this.markGated(candidate);
          this.log(`${candidate.name}/${candidate.model} is not available upstream; skipping it for new requests`);
        }
        const next = this.nextUsable(targets, i);
        if ((result.retryable || isModelUnavailable(result.status)) && next >= 0) {
          this.useFallback(targets[next]);
          continue;
        }
        response = { status: result.status };
        body = result.body;
        target = candidate;
        break;
      }
      response = { status: 200, stream: result.stream };
      target = candidate;
      break;
    }

    if (!response) {
      if (limitStatus) {
        this.writeUpstreamError(res, limitStatus, limitBody);
        return;
      }
      this.writeError(res, 502, "api_error", "all providers failed");
      return;
    }
    if (response.status < 200 || response.status >= 300) {
      if (limitStatus && response.status !== limitStatus) {
        this.writeUpstreamError(res, limitStatus, limitBody);
        return;
      }
      this.writeUpstreamError(res, response.status, body);
      return;
    }

    const streamReq = Object.assign({}, chatReq);
    streamReq.model = target.model;
    const hold = new HoldWriter(res);
    const conv = new translate.StreamConverter((line) => hold.eventLine(line), chatModel);

    let sawDone = false;
    let failure = null;
    try {
      const result = await consumeStream(Readable.fromWeb(response.stream), (chunk) => conv.handle(chunk));
      sawDone = result.sawDone;
    } catch (err) {
      failure = err;
    }
    if (signal.aborted) return;
    if (failure || (!sawDone && !conv.finished())) {
      if (!hold.sent() && conv.text() !== "") {
        await hold.flushNow();
      }
      if (!hold.sent() || conv.toolUsed() || conv.text() === "") {
        const reason = failure ? failure.message : "upstream stream ended before completion";
        this.failStream(res, hold, conv, reason);
        return;
      }
      try {
        await this.recoverStream(streamReq, conv, target, signal);
      } catch (err) {
        if (signal.aborted) return;
        this.failStream(res, hold, conv, err.message);
        return;
      }
    }
    if (signal.aborted) return;
    try {
      await conv.finish();
    } catch (err) {
      return;
    }
    await hold.flushNow();
  }

  async recoverStream(chatReq, conv, target, signal) {
    for (let attempt = 0; attempt < MAX_RECOVERIES; attempt++) {
      if (conv.toolUsed() || conv.finished()) return;
      const prior = conv.text();
      if (!prior) throw new Error("upstream stream ended before any text was delivered");
      if (prior.length >= MAX_RECOVERED_TEXT) throw new Error("stream recovery limit reached");

      this.counters.recoveries += 1;
      const next = Object.assign({}, chatReq);
      next.stream = true;
      next.stream_options = { include_usage: true };
      next.messages = [
        ...chatReq.messages,
        { role: "assistant", content: prior },
        {
          role: "user",
          content:
            "Continue from exactly where the previous response stopped. Do not repeat any text that was already written.",
        },
      ];
      const payload = JSON.stringify(next);

      const result = await this.doUpstreamTarget(target, payload, true, MAX_ATTEMPTS, signal);
      if (result.error) {
        throw new Error(`stream recovery failed: ${result.error.message}`);
      }
      if (result.status < 200 || result.status >= 300) {
        throw new Error(`stream recovery failed: ${snippet(result.body)}`);
      }

      const buffered = [];
      let continuation = "";
      let sawDone = false;
      try {
        const scanned = await consumeStream(Readable.fromWeb(result.stream), async (chunk) => {
          buffered.push(chunk);
          continuation += chunkText(chunk);
        });
        sawDone = scanned.sawDone;
      } catch (err) {
        if (signal.aborted) throw new AbortedError();
        throw new Error(`stream recovery interrupted: ${err.message}`);
      }

      const text = stripOverlap(prior, continuation);
      if (text) {
        await conv.handle({ choices: [{ delta: { content: text } }] });
      }
      for (const chunk of buffered) {
        if (chunk.choices && chunk.choices.length) {
          chunk.choices[0].delta = chunk.choices[0].delta || {};
          chunk.choices[0].delta.content = "";
          if (chunk.choices[0].message) chunk.choices[0].message.content = null;
        }
        await conv.handle(chunk);
      }
      if (sawDone || conv.finished()) return;
    }
    throw new Error("stream recovery limit reached");
  }

  failStream(res, hold, conv, message) {
    this.setLastError(message);
    if (!hold.sent()) {
      hold.discard();
      this.writeError(res, 502, "api_error", message);
      return;
    }
    hold.event("error", { type: "error", error: { type: "api_error", message } });
    hold.flush();
  }

  async doUpstreamTarget(target, payload, stream, maxTries, signal) {
    const tries = Math.max(1, maxTries || 1);
    const breaker = this.breakerFor(target);
    if (!breaker.allow()) {
      const seconds = Math.floor(breaker.openRemaining() / 1000) + 1;
      const message = `${target.name}/${targetModel(target)} is cooling down after repeated failures (${seconds}s left)`;
      this.setLastError(message);
      return { error: new Error(message), retryable: true };
    }
    let success = false;
    let neutral = false;
    try {
      let lastError = null;
      let currentPayload = payload;
      let clampRetries = 0;

      for (let attempt = 0; attempt < tries; attempt++) {
        if (signal.aborted) throw new AbortedError();
        const controller = new AbortController();
        const onAbort = () => controller.abort();
        if (signal.aborted) controller.abort();
        else signal.addEventListener("abort", onAbort, { once: true });
        const timer = setTimeout(() => controller.abort(), 120000);

        const headers = {
          "Content-Type": "application/json",
          Accept: stream ? "text/event-stream" : "application/json",
        };
        if (target.apiKey) headers.Authorization = `Bearer ${target.apiKey}`;
        Object.assign(headers, target.headers || {});

        let response;
        try {
          response = await fetch(`${trimURL(target.baseURL)}/chat/completions`, {
            method: "POST",
            headers,
            body: currentPayload,
            signal: controller.signal,
          });
        } catch (err) {
          clearTimeout(timer);
          signal.removeEventListener("abort", onAbort);
          if (signal.aborted) throw new AbortedError();
          lastError = new Error(describeFetchError(err));
          this.counters.retries += 1;
          this.setLastError(lastError.message);
          if (attempt === tries - 1) {
            return { error: lastError, retryable: true };
          }
          await this.backoff(attempt + 1, "", signal);
          continue;
        }
        clearTimeout(timer);
        signal.removeEventListener("abort", onAbort);

        if (response.ok) {
          success = true;
          if (stream) {
            return { status: response.status, stream: response.body };
          }
          const text = await readLimited(Readable.fromWeb(response.body), 1 << 20);
          return { status: response.status, body: text };
        }

        const text = await readLimited(Readable.fromWeb(response.body), 1 << 20);
        lastError = new Error(`upstream returned ${response.status} ${response.statusText}: ${snippet(text)}`);
        this.log(`upstream ${target.name} ${response.status}: ${snippet(text)}`);
        this.setLastError(lastError.message);

        const tokenLimit = isTokenRateLimit(response.status, text);
        if (response.status === 429 || tokenLimit) this.counters.rate_limited += 1;
        if (response.status === 413) this.counters.payload_too_large += 1;
        if (response.status >= 500) this.counters.server_errors += 1;

        if (tokenLimit) {
          neutral = true;
          return { status: response.status, body: text, retryable: true };
        }

        const limit = maxTokensLimit(response.status, text);
        if (limit > 0 && clampRetries < 2) {
          const adjusted = withMaxTokens(currentPayload, limit);
          if (adjusted.changed) {
            currentPayload = adjusted.payload;
            this.learnMaxOutput(target, limit);
            clampRetries += 1;
            if (attempt > 0) attempt -= 1;
            continue;
          }
        }

        if (isHarnessGate(response.status, text)) {
          this.markGated(target);
          neutral = true;
          return { status: response.status, body: text, retryable: true };
        }

        if (response.status === 429) {
          neutral = true;
          return { status: response.status, body: text, retryable: true };
        }

        if (!retryableStatus(response.status)) {
          success = true;
          return { status: response.status, body: text, retryable: false };
        }
        if (attempt === tries - 1) {
          return { status: response.status, body: text, retryable: true };
        }
        this.counters.retries += 1;
        await this.backoff(attempt + 1, response.headers.get("retry-after") || "", signal);
      }
      return { error: lastError || new Error("upstream request failed"), retryable: true };
    } finally {
      if (neutral) breaker.recordNeutral();
      else breaker.record(success);
    }
  }

  async backoff(attempt, retryAfter, signal) {
    let delay = BASE_BACKOFF * 2 ** (attempt - 1);
    if (!(delay > 0) || delay > MAX_BACKOFF) delay = MAX_BACKOFF;
    const after = String(retryAfter || "").trim();
    if (after) {
      const seconds = Number.parseInt(after, 10);
      if (Number.isFinite(seconds) && String(seconds) === after) {
        delay = seconds * 1000;
      } else {
        const when = Date.parse(after);
        if (Number.isFinite(when)) {
          const delta = when - Date.now();
          if (delta > 0) delay = delta;
        }
      }
    }
    if (delay > MAX_RETRY_AFTER_WAIT) delay = MAX_RETRY_AFTER_WAIT;
    delay += Math.floor(Math.random() * 250);
    await abortableSleep(delay, signal);
  }

  writeUpstreamError(res, status, body) {
    let effective = status;
    const text = String(body || "");
    const tokenLimit = isTokenRateLimit(status, text);
    if (status === 413 && tokenLimit) effective = 429;

    let kind = "api_error";
    switch (effective) {
      case 400:
      case 422:
      case 413:
        kind = "invalid_request_error";
        break;
      case 401:
        kind = "authentication_error";
        break;
      case 403:
        kind = "permission_error";
        break;
      case 404:
        kind = "not_found_error";
        break;
      case 429:
        kind = "rate_limit_error";
        break;
      case 529:
        kind = "overloaded_error";
        break;
      default:
        break;
    }

    if (effective === 429 && isDailyQuota(text)) {
      let message = snippet(text);
      const envelope = parseErrorEnvelope(text);
      if (envelope) message = envelope;
      message += " (daily free-model quota exhausted";
      const reset = quotaResetTime(text);
      if (reset) message += `; resets ${reset}`;
      message += " - add credits or pick another provider with `claude-custom profile`)";
      this.writeError(res, 400, "invalid_request_error", message);
      return;
    }

    let message = snippet(text);
    const envelope = parseErrorEnvelope(text);
    if (envelope) message = envelope;
    if (!message) message = http.STATUS_CODES[effective] || "upstream error";
    if (tokenLimit || effective === 413) {
      message +=
        " (provider input-token-per-minute/ITPM limit - reduce context, trim MCP tools, or switch to a model/provider with a larger input limit)";
    }
    if (effective === 403 && text.includes("agentic harness")) {
      message +=
        " (this free model is reserved for apps listed on openrouter.ai/apps; pick another model with `claude-custom model list --refresh`)";
    }
    this.writeError(res, effective, kind, message);
  }
}

function trimURL(base) {
  return String(base || "").trim().replace(/\/+$/, "");
}

function targetModel(target) {
  const model = String(target.model || "").trim();
  return model || "(provider default)";
}

function parseErrorEnvelope(text) {
  try {
    const parsed = JSON.parse(text);
    if (parsed && parsed.error && parsed.error.message) return String(parsed.error.message);
  } catch (err) {
    return null;
  }
  return null;
}

function readBody(req, limit) {
  return new Promise((resolve, reject) => {
    const chunks = [];
    let total = 0;
    req.on("data", (chunk) => {
      total += chunk.length;
      if (total > limit) {
        reject(new Error("http: request body too large"));
        req.destroy();
        return;
      }
      chunks.push(chunk);
    });
    req.on("end", () => resolve(Buffer.concat(chunks).toString("utf8")));
    req.on("error", reject);
  });
}

module.exports = {
  ProxyServer,
  HoldWriter,
  CircuitBreaker,
  Limiter,
  AbortedError,
  OverloadedError,
  consumeStream,
  stripOverlap,
  isTokenRateLimit,
  isDailyQuota,
  isHarnessGate,
  isModelUnavailable,
  retryableStatus,
  maxTokensLimit,
  withMaxTokens,
  snapshot: {
    MAX_ATTEMPTS,
    BREAKER_THRESHOLD,
    BREAKER_COOLDOWN,
    MODELS_TTL,
  },
};
