"use strict";

const crypto = require("crypto");

function newId(prefix) {
  return prefix + crypto.randomBytes(12).toString("hex");
}

function isAbsent(raw) {
  return raw === undefined || raw === null;
}

function decodeContent(raw) {
  if (isAbsent(raw)) {
    return { text: "", blocks: null };
  }
  if (typeof raw === "string") {
    return { text: raw, blocks: null };
  }
  if (Array.isArray(raw)) {
    return { text: "", blocks: raw };
  }
  if (typeof raw === "object") {
    return { text: "", blocks: [raw] };
  }
  throw new Error("content must be a string or an array of blocks");
}

function systemText(raw) {
  const { text, blocks } = decodeContent(raw);
  if (blocks === null) return text;
  const parts = [];
  for (const block of blocks) {
    if (block && block.type === "text" && block.text) {
      parts.push(block.text);
    }
  }
  return parts.join("\n\n");
}

function blockText(raw) {
  let decoded;
  try {
    decoded = decodeContent(raw);
  } catch (err) {
    return "";
  }
  if (decoded.blocks === null) return decoded.text;
  const parts = [];
  for (const block of decoded.blocks) {
    if (block && block.type === "text" && block.text) {
      parts.push(block.text);
    }
  }
  return parts.join("\n");
}

function compactJSON(raw) {
  if (isAbsent(raw)) return "{}";
  let text;
  try {
    text = JSON.stringify(raw);
  } catch (err) {
    return "{}";
  }
  if (!text || text === "null") return "{}";
  return text;
}

function imageURL(source) {
  if (!source) return "";
  if (source.url) return source.url;
  if (source.data) {
    const mime = source.media_type || "image/png";
    return `data:${mime};base64,${source.data}`;
  }
  return "";
}

function buildChatRequest(req, model) {
  const out = {
    model,
    messages: [],
    max_tokens: Number(req.max_tokens) > 0 ? Number(req.max_tokens) : 8192,
    stream: Boolean(req.stream),
  };
  if (req.temperature !== undefined && req.temperature !== null) out.temperature = req.temperature;
  if (req.top_p !== undefined && req.top_p !== null) out.top_p = req.top_p;
  if (Array.isArray(req.stop_sequences) && req.stop_sequences.length) out.stop = req.stop_sequences;
  if (out.stream) out.stream_options = { include_usage: true };

  const systemParts = [];
  const system = systemText(req.system);
  if (system.trim()) systemParts.push(system);

  const messages = Array.isArray(req.messages) ? req.messages : [];
  messages.forEach((message, index) => {
    const role = message && message.role;
    if (role === "system") {
      const text = systemText(message.content);
      if (text.trim()) systemParts.push(text);
      return;
    }
    if (role === "user") {
      try {
        out.messages.push(...convertUserMessage(message));
      } catch (err) {
        throw new Error(`messages[${index}]: ${err.message}`);
      }
      return;
    }
    if (role === "assistant") {
      try {
        out.messages.push(convertAssistantMessage(message));
      } catch (err) {
        throw new Error(`messages[${index}]: ${err.message}`);
      }
      return;
    }
    throw new Error(`messages[${index}]: unsupported role "${role}"`);
  });

  if (systemParts.length) {
    out.messages.unshift({ role: "system", content: systemParts.join("\n\n") });
  }

  for (const tool of req.tools || []) {
    let parameters = tool.input_schema;
    if (isAbsent(parameters) || parameters === "null") {
      parameters = { type: "object", properties: {} };
    }
    out.tools = out.tools || [];
    out.tools.push({
      type: "function",
      function: {
        name: tool.name,
        description: tool.description || "",
        parameters,
      },
    });
  }

  const choice = convertToolChoice(req.tool_choice);
  if (choice.value !== undefined && choice.value !== null) out.tool_choice = choice.value;
  if (choice.parallel !== undefined && choice.parallel !== null) out.parallel_tool_calls = choice.parallel;

  return out;
}

function convertUserMessage(message) {
  const { text, blocks } = decodeContent(message.content);
  if (blocks === null) {
    return [{ role: "user", content: text }];
  }

  const toolMessages = [];
  const textParts = [];
  const parts = [];
  for (const block of blocks) {
    if (!block || typeof block !== "object") continue;
    if (block.type === "text") {
      if (block.text) textParts.push(block.text);
    } else if (block.type === "image") {
      const url = imageURL(block.source);
      if (url) {
        parts.push({ type: "image_url", image_url: { url } });
      }
    } else if (block.type === "tool_result") {
      let content = blockText(block.content);
      if (block.is_error) content = "Error: " + content;
      toolMessages.push({ role: "tool", tool_call_id: block.tool_use_id || "", content });
    }
  }

  const joined = textParts.join("");
  if (joined && parts.length) {
    parts.unshift({ type: "text", text: joined });
  }

  const out = toolMessages.slice();
  if (parts.length) {
    out.push({ role: "user", content: parts });
  } else if (joined) {
    out.push({ role: "user", content: joined });
  } else if (!toolMessages.length) {
    out.push({ role: "user", content: "" });
  }
  return out;
}

function convertAssistantMessage(message) {
  const { text, blocks } = decodeContent(message.content);
  const out = { role: "assistant" };
  if (blocks === null) {
    out.content = text;
    return out;
  }
  const textParts = [];
  for (const block of blocks) {
    if (!block || typeof block !== "object") continue;
    if (block.type === "text") {
      if (block.text) textParts.push(block.text);
    } else if (block.type === "tool_use") {
      const id = block.id || newId("call_");
      out.tool_calls = out.tool_calls || [];
      out.tool_calls.push({
        id,
        type: "function",
        function: { name: block.name || "", arguments: compactJSON(block.input) },
      });
    }
  }
  const joined = textParts.join("");
  if (joined) out.content = joined;
  return out;
}

function convertToolChoice(raw) {
  if (isAbsent(raw) || raw === "null") {
    return { value: null, parallel: null };
  }
  if (typeof raw === "string") {
    if (raw === "auto") return { value: "auto", parallel: null };
    if (raw === "any") return { value: "required", parallel: null };
    if (raw === "none") return { value: "none", parallel: null };
    throw new Error(`unsupported tool_choice "${raw}"`);
  }
  if (typeof raw !== "object") {
    throw new Error("invalid tool_choice");
  }
  const parallel = raw.disable_parallel_tool_use ? false : null;
  if (raw.type === "tool" && raw.name) {
    return { value: { type: "function", function: { name: raw.name } }, parallel };
  }
  return { value: null, parallel };
}

function mapStopReason(reason, hasTools) {
  switch (reason) {
    case "stop":
      return "end_turn";
    case "length":
      return "max_tokens";
    case "tool_calls":
    case "function_call":
      return "tool_use";
    case "content_filter":
      return "refusal";
    case "":
    case undefined:
    case null:
      return hasTools ? "tool_use" : "end_turn";
    default:
      return "end_turn";
  }
}

function buildAnthropicResponse(resp, model) {
  const out = {
    id: resp && resp.id ? `msg_${String(resp.id).replace(/^msg_/, "")}` : newId("msg_"),
    type: "message",
    role: "assistant",
    model,
    content: [],
    stop_reason: "end_turn",
    stop_sequence: null,
    usage: {
      input_tokens: (resp && resp.usage && resp.usage.prompt_tokens) || 0,
      output_tokens: (resp && resp.usage && resp.usage.completion_tokens) || 0,
      cache_read_input_tokens:
        (resp && resp.usage && resp.usage.prompt_tokens_details && resp.usage.prompt_tokens_details.cached_tokens) || 0,
    },
  };
  if (!out.usage.cache_read_input_tokens) delete out.usage.cache_read_input_tokens;

  const choices = (resp && resp.choices) || [];
  if (!choices.length) return out;
  const choice = choices[0];
  const message = choice.message || {};

  if (typeof message.content === "string") {
    if (message.content) out.content.push({ type: "text", text: message.content });
  } else if (Array.isArray(message.content)) {
    const parts = [];
    for (const item of message.content) {
      if (item && typeof item.text === "string" && item.text) parts.push(item.text);
    }
    if (parts.length) out.content.push({ type: "text", text: parts.join("") });
  }

  const toolCalls = Array.isArray(message.tool_calls) ? message.tool_calls : [];
  for (const call of toolCalls) {
    const fn = call.function || {};
    let input = {};
    const args = String(fn.arguments || "").trim();
    if (args) {
      try {
        input = JSON.parse(args);
      } catch (err) {
        input = { _raw: fn.arguments };
      }
    }
    if (input === null || input === undefined) input = {};
    out.content.push({
      type: "tool_use",
      id: call.id || newId("toolu_"),
      name: fn.name || "",
      input,
    });
  }

  out.stop_reason = mapStopReason(choice.finish_reason, toolCalls.length > 0);
  return out;
}

function sseEvent(name, payload) {
  return `event: ${name}\ndata: ${JSON.stringify(payload)}\n\n`;
}

class StreamConverter {
  constructor(write, model) {
    this.write = write;
    this.model = model;
    this.messageId = newId("msg_");
    this.started = false;
    this.nextIndex = 0;
    this.textOpen = false;
    this.textIndex = 0;
    this.tools = new Map();
    this.toolOrder = [];
    this.usage = {};
    this.finishReason = "";
    this.hasTools = false;
    this.buffer = "";
  }

  text() {
    return this.buffer;
  }

  toolUsed() {
    return this.hasTools;
  }

  finished() {
    return this.finishReason !== "";
  }

  async emit(name, payload) {
    await this.write(sseEvent(name, payload));
  }

  async start() {
    this.started = true;
    await this.emit("message_start", {
      type: "message_start",
      message: {
        id: this.messageId,
        type: "message",
        role: "assistant",
        model: this.model,
        content: [],
        stop_reason: null,
        stop_sequence: null,
        usage: { input_tokens: 0, output_tokens: 0 },
      },
    });
  }

  async ensureStarted() {
    if (!this.started) await this.start();
  }

  async ensureText() {
    if (this.textOpen) return;
    this.textOpen = true;
    this.textIndex = this.nextIndex;
    this.nextIndex += 1;
    await this.emit("content_block_start", {
      type: "content_block_start",
      index: this.textIndex,
      content_block: { type: "text", text: "" },
    });
  }

  async closeText() {
    if (!this.textOpen) return;
    this.textOpen = false;
    await this.emit("content_block_stop", { type: "content_block_stop", index: this.textIndex });
  }

  async openTool(state) {
    state.opened = true;
    if (!state.id) state.id = newId("toolu_");
    await this.emit("content_block_start", {
      type: "content_block_start",
      index: state.blockIndex,
      content_block: {
        type: "tool_use",
        id: state.id,
        name: state.name || "unknown_tool",
        input: {},
      },
    });
    if (state.args) {
      const fragment = state.args;
      state.args = "";
      await this.emitArgs(state, fragment);
    }
  }

  async emitArgs(state, fragment) {
    await this.emit("content_block_delta", {
      type: "content_block_delta",
      index: state.blockIndex,
      delta: { type: "input_json_delta", partial_json: fragment },
    });
  }

  async handle(chunk) {
    if (!this.started) await this.start();

    const usage = chunk && chunk.usage;
    if (usage && (usage.prompt_tokens || usage.completion_tokens)) {
      this.usage = usage;
    }
    const choices = (chunk && chunk.choices) || [];
    if (!choices.length) return;
    const choice = choices[0];
    if (choice.finish_reason) this.finishReason = choice.finish_reason;

    const delta = Object.assign({}, choice.delta || {});
    const deltaToolCalls = Array.isArray(delta.tool_calls) ? delta.tool_calls : [];
    if (!delta.content && !deltaToolCalls.length) {
      const message = choice.message || {};
      if (typeof message.content === "string") delta.content = message.content;
      if (Array.isArray(message.tool_calls) && message.tool_calls.length) {
        delta.tool_calls = message.tool_calls;
      }
    }

    if (delta.content) {
      await this.ensureText();
      await this.emit("content_block_delta", {
        type: "content_block_delta",
        index: this.textIndex,
        delta: { type: "text_delta", text: delta.content },
      });
      this.buffer += delta.content;
    }

    for (const call of delta.tool_calls || []) {
      if (this.textOpen) await this.closeText();
      const index = Number.isInteger(call.index) ? call.index : 0;
      let state = this.tools.get(index);
      if (!state) {
        state = {
          blockIndex: this.nextIndex,
          id: call.id || "",
          name: (call.function && call.function.name) || "",
          args: "",
          opened: false,
        };
        this.nextIndex += 1;
        this.tools.set(index, state);
        this.toolOrder.push(index);
        this.hasTools = true;
      }
      if (call.id) state.id = call.id;
      const fn = call.function || {};
      if (fn.name) state.name = fn.name;
      if (fn.arguments) {
        if (state.opened) {
          await this.emitArgs(state, fn.arguments);
        } else {
          state.args += fn.arguments;
        }
      }
      if (!state.opened && state.name) await this.openTool(state);
    }
  }

  async finish() {
    await this.ensureStarted();
    if (this.textOpen) await this.closeText();

    this.toolOrder.sort((a, b) => a - b);
    for (const key of this.toolOrder) {
      const state = this.tools.get(key);
      if (!state.opened) await this.openTool(state);
      if (state.args) {
        const fragment = state.args;
        state.args = "";
        await this.emitArgs(state, fragment);
      }
      await this.emit("content_block_stop", { type: "content_block_stop", index: state.blockIndex });
    }

    const completionTokens = (this.usage && this.usage.completion_tokens) || 0;
    await this.emit("message_delta", {
      type: "message_delta",
      delta: { stop_reason: mapStopReason(this.finishReason, this.hasTools), stop_sequence: null },
      usage: { output_tokens: completionTokens },
    });
    await this.emit("message_stop", { type: "message_stop" });
  }
}

module.exports = {
  newId,
  decodeContent,
  systemText,
  blockText,
  compactJSON,
  imageURL,
  buildChatRequest,
  convertUserMessage,
  convertAssistantMessage,
  convertToolChoice,
  mapStopReason,
  buildAnthropicResponse,
  sseEvent,
  StreamConverter,
};
