"use strict";

const config = require("./config.js");

const presets = [
  {
    id: "nvidia_nim",
    name: "NVIDIA NIM",
    baseURL: "https://integrate.api.nvidia.com/v1",
    keyURL: "https://build.nvidia.com",
    local: false,
    limits: { max_concurrency: 1, requests_per_window: 1, window_seconds: 2 },
    models: [
      { id: "deepseek-ai/deepseek-v3.2", name: "DeepSeek V3.2", contextWindow: 128000, maxOutput: 8192 },
      { id: "nvidia/nemotron-3-ultra-550b-a55b", name: "Nemotron 3 Ultra", contextWindow: 200000, maxOutput: 8192 },
      { id: "qwen/qwen3-coder-480b-a35b-instruct", name: "Qwen3 Coder 480B", contextWindow: 256000, maxOutput: 8192 },
      { id: "openai/gpt-oss-120b", name: "GPT-OSS 120B", contextWindow: 128000, maxOutput: 8192 },
    ],
  },
  {
    id: "openrouter",
    name: "OpenRouter",
    baseURL: "https://openrouter.ai/api/v1",
    keyURL: "https://openrouter.ai/keys",
    local: false,
    limits: { max_concurrency: 2, requests_per_window: 4, window_seconds: 2 },
    models: [
      { id: "deepseek/deepseek-chat-v3.1", name: "DeepSeek V3.1", contextWindow: 163840, maxOutput: 8192 },
      { id: "anthropic/claude-sonnet-4", name: "Claude Sonnet 4", contextWindow: 200000, maxOutput: 8192 },
      { id: "google/gemini-2.5-pro", name: "Gemini 2.5 Pro", contextWindow: 1048576, maxOutput: 8192 },
      { id: "qwen/qwen3-coder", name: "Qwen3 Coder", contextWindow: 262144, maxOutput: 8192 },
    ],
  },
  {
    id: "deepseek",
    name: "DeepSeek",
    baseURL: "https://api.deepseek.com/v1",
    keyURL: "https://platform.deepseek.com/api_keys",
    local: false,
    limits: { max_concurrency: 1, requests_per_window: 1, window_seconds: 2 },
    models: [
      { id: "deepseek-chat", name: "DeepSeek Chat", contextWindow: 128000, maxOutput: 8192 },
      { id: "deepseek-reasoner", name: "DeepSeek Reasoner", contextWindow: 128000, maxOutput: 8192 },
    ],
  },
  {
    id: "groq",
    name: "Groq",
    baseURL: "https://api.groq.com/openai/v1",
    keyURL: "https://console.groq.com/keys",
    local: false,
    limits: { max_concurrency: 2, requests_per_window: 10, window_seconds: 60 },
    models: [
      { id: "llama-3.3-70b-versatile", name: "Llama 3.3 70B", contextWindow: 128000, maxOutput: 8192 },
      { id: "openai/gpt-oss-120b", name: "GPT-OSS 120B", contextWindow: 128000, maxOutput: 8192 },
      { id: "qwen/qwen3-32b", name: "Qwen3 32B", contextWindow: 128000, maxOutput: 8192 },
    ],
  },
  { id: "cerebras", name: "Cerebras", baseURL: "https://api.cerebras.ai/v1", keyURL: "https://cloud.cerebras.ai", local: false, limits: { max_concurrency: 2, requests_per_window: 10, window_seconds: 60 } },
  { id: "together", name: "Together AI", baseURL: "https://api.together.ai/v1", keyURL: "https://api.together.ai/settings/api-keys", local: false, limits: config.defaultLimits() },
  { id: "fireworks", name: "Fireworks", baseURL: "https://api.fireworks.ai/inference/v1", keyURL: "https://fireworks.ai/account/api-keys", local: false, limits: config.defaultLimits() },
  { id: "mistral", name: "Mistral", baseURL: "https://api.mistral.ai/v1", keyURL: "https://console.mistral.ai/api-keys", local: false, limits: config.defaultLimits() },
  { id: "xai", name: "xAI", baseURL: "https://api.x.ai/v1", keyURL: "https://console.x.ai", local: false, limits: config.defaultLimits() },
  { id: "google_ai_studio", name: "Google AI Studio", baseURL: "https://generativelanguage.googleapis.com/v1beta/openai/", keyURL: "https://aistudio.google.com/apikey", local: false, limits: config.defaultLimits() },
  { id: "zai", name: "Z.ai", baseURL: "https://api.z.ai/api/paas/v4", keyURL: "https://z.ai/manage-apikey/apikey-list", local: false, limits: config.defaultLimits() },
  { id: "moonshot", name: "Moonshot / Kimi", baseURL: "https://api.moonshot.ai/v1", keyURL: "https://platform.moonshot.ai/console/api-keys", local: false, limits: config.defaultLimits() },
  { id: "minimax", name: "MiniMax", baseURL: "https://api.minimax.io/v1", keyURL: "https://platform.minimaxi.com", local: false, limits: config.defaultLimits() },
  { id: "siliconflow", name: "SiliconFlow", baseURL: "https://api.siliconflow.com/v1", keyURL: "https://cloud.siliconflow.com", local: false, limits: config.defaultLimits() },
  { id: "nebius", name: "Nebius Token Factory", baseURL: "https://api.tokenfactory.nebius.com/v1", keyURL: "https://tokenfactory.nebius.com", local: false, limits: config.defaultLimits() },
  { id: "vercel_ai_gateway", name: "Vercel AI Gateway", baseURL: "https://ai-gateway.vercel.sh/v1", keyURL: "https://vercel.com/ai-gateway", local: false, limits: config.defaultLimits() },
  { id: "huggingface", name: "Hugging Face Router", baseURL: "https://router.huggingface.co/v1", keyURL: "https://huggingface.co/settings/tokens", local: false, limits: config.defaultLimits() },
  {
    id: "ollama",
    name: "Ollama (local)",
    baseURL: "http://localhost:11434/v1",
    keyURL: "",
    local: true,
    limits: { max_concurrency: 1, requests_per_window: 20, window_seconds: 1 },
    models: [
      { id: "qwen3-coder", name: "Qwen3 Coder", contextWindow: 128000, maxOutput: 8192 },
      { id: "deepseek-coder-v2", name: "DeepSeek Coder V2", contextWindow: 128000, maxOutput: 8192 },
      { id: "llama3.3", name: "Llama 3.3", contextWindow: 128000, maxOutput: 8192 },
    ],
  },
  { id: "lmstudio", name: "LM Studio (local)", baseURL: "http://localhost:1234/v1", keyURL: "", local: true, limits: { max_concurrency: 1, requests_per_window: 20, window_seconds: 1 } },
  { id: "llamacpp", name: "llama.cpp (local)", baseURL: "http://localhost:8080/v1", keyURL: "", local: true, limits: { max_concurrency: 1, requests_per_window: 20, window_seconds: 1 } },
  { id: "custom", name: "Custom OpenAI-compatible", baseURL: "", keyURL: "", local: false, limits: config.defaultLimits() },
];

function find(id) {
  for (const preset of presets) {
    if (preset.id === id) return preset;
  }
  return null;
}

function hasHeader(headers, name) {
  if (!headers) return false;
  return Object.keys(headers).some((key) => key.toLowerCase() === name.toLowerCase());
}

function attributionHeaders(baseURL, headers) {
  const out = Object.assign({}, headers || {});
  if (!String(baseURL || "").toLowerCase().includes("openrouter.ai")) {
    return headers || undefined;
  }
  if (hasHeader(headers, "HTTP-Referer") || hasHeader(headers, "X-Title")) {
    return headers || undefined;
  }
  out["HTTP-Referer"] = "https://claude-custom.local";
  out["X-Title"] = "claude-custom";
  return out;
}

function parseModels(payload) {
  const raw = Array.isArray(payload.data) && payload.data.length ? payload.data : payload.models || [];
  const seen = new Set();
  const out = [];
  for (const item of raw) {
    if (!item || typeof item !== "object") continue;
    const id = String(item.id || "").trim();
    if (!id || seen.has(id)) continue;
    seen.add(id);
    const name = item.display_name || item.name || id;
    let contextWindow = item.context_length || item.context_window || item.max_context_length || 0;
    if (!contextWindow && item.top_provider) {
      contextWindow = item.top_provider.context_length || 0;
    }
    const maxOutput = item.max_completion_tokens || item.max_output_tokens || 0;
    out.push({ id, name, contextWindow, maxOutput });
  }
  out.sort((a, b) => (a.id < b.id ? -1 : a.id > b.id ? 1 : 0));
  return out;
}

async function fetchModels(baseURL, apiKey, headers) {
  const base = String(baseURL || "").trim().replace(/\/+$/, "");
  if (!base) {
    throw new Error("base URL is empty");
  }
  const requestHeaders = { Accept: "application/json" };
  if (apiKey) requestHeaders.Authorization = `Bearer ${apiKey}`;
  Object.assign(requestHeaders, headers || {});
  let response;
  try {
    response = await fetch(`${base}/models`, {
      method: "GET",
      headers: requestHeaders,
      signal: AbortSignal.timeout(20000),
    });
  } catch (err) {
    throw new Error(fetchError(err));
  }
  const text = await response.text();
  if (!response.ok) {
    throw new Error(`model list returned ${response.status} ${response.statusText}: ${snippet(text)}`);
  }
  let payload;
  try {
    payload = JSON.parse(text);
  } catch (err) {
    throw new Error(`parse model list: ${err.message}`);
  }
  return parseModels(payload || {});
}

function fetchError(err) {
  const cause = err && err.cause ? err.cause : null;
  if (cause && cause.code) return `${cause.code}: ${cause.message || "request failed"}`;
  return (err && err.message) || "request failed";
}

function snippet(value) {
  const text = String(value || "").trim();
  return text.length > 300 ? `${text.slice(0, 300)}...` : text;
}

const nonChatHints = ["whisper", "tts", "asr", "vad", "guard", "moderation", "safety", "embed", "rerank", "orpheus"];

function isChatModel(model) {
  const text = `${model.id} ${model.name}`.toLowerCase();
  return !nonChatHints.some((hint) => text.includes(hint));
}

function filterChat(models) {
  return (models || []).filter(isChatModel);
}

function defaultModel(preset) {
  return preset && preset.models && preset.models.length ? preset.models[0].id : "";
}

module.exports = {
  presets,
  find,
  attributionHeaders,
  fetchModels,
  parseModels,
  filterChat,
  isChatModel,
  defaultModel,
  fetchError,
};
