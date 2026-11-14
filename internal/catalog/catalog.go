package catalog

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"sort"
	"strings"
	"time"

	"claude-custom/internal/config"
)

type Model struct {
	ID            string
	Name          string
	ContextWindow int
	MaxOutput     int
}

type Preset struct {
	ID      string
	Name    string
	BaseURL string
	KeyURL  string
	Local   bool
	Limits  config.Limits
	Models  []Model
}

var Presets = []Preset{
	{
		ID: "nvidia_nim", Name: "NVIDIA NIM", BaseURL: "https://integrate.api.nvidia.com/v1",
		KeyURL: "https://build.nvidia.com", Limits: config.Limits{MaxConcurrency: 1, RequestsPerWindow: 1, WindowSeconds: 2},
		Models: []Model{
			{ID: "deepseek-ai/deepseek-v3.2", Name: "DeepSeek V3.2", ContextWindow: 128000, MaxOutput: 8192},
			{ID: "nvidia/nemotron-3-ultra-550b-a55b", Name: "Nemotron 3 Ultra", ContextWindow: 200000, MaxOutput: 8192},
			{ID: "qwen/qwen3-coder-480b-a35b-instruct", Name: "Qwen3 Coder 480B", ContextWindow: 256000, MaxOutput: 8192},
			{ID: "openai/gpt-oss-120b", Name: "GPT-OSS 120B", ContextWindow: 128000, MaxOutput: 8192},
		},
	},
	{
		ID: "openrouter", Name: "OpenRouter", BaseURL: "https://openrouter.ai/api/v1",
		KeyURL: "https://openrouter.ai/keys", Limits: config.Limits{MaxConcurrency: 2, RequestsPerWindow: 4, WindowSeconds: 2},
		Models: []Model{
			{ID: "deepseek/deepseek-chat-v3.1", Name: "DeepSeek V3.1", ContextWindow: 163840, MaxOutput: 8192},
			{ID: "anthropic/claude-sonnet-4", Name: "Claude Sonnet 4", ContextWindow: 200000, MaxOutput: 8192},
			{ID: "google/gemini-2.5-pro", Name: "Gemini 2.5 Pro", ContextWindow: 1048576, MaxOutput: 8192},
			{ID: "qwen/qwen3-coder", Name: "Qwen3 Coder", ContextWindow: 262144, MaxOutput: 8192},
		},
	},
	{
		ID: "deepseek", Name: "DeepSeek", BaseURL: "https://api.deepseek.com/v1",
		KeyURL: "https://platform.deepseek.com/api_keys", Limits: config.Limits{MaxConcurrency: 1, RequestsPerWindow: 1, WindowSeconds: 2},
		Models: []Model{
			{ID: "deepseek-chat", Name: "DeepSeek Chat", ContextWindow: 128000, MaxOutput: 8192},
			{ID: "deepseek-reasoner", Name: "DeepSeek Reasoner", ContextWindow: 128000, MaxOutput: 8192},
		},
	},
	{
		ID: "groq", Name: "Groq", BaseURL: "https://api.groq.com/openai/v1",
		KeyURL: "https://console.groq.com/keys", Limits: config.Limits{MaxConcurrency: 2, RequestsPerWindow: 10, WindowSeconds: 60},
		Models: []Model{
			{ID: "llama-3.3-70b-versatile", Name: "Llama 3.3 70B", ContextWindow: 128000, MaxOutput: 8192},
			{ID: "openai/gpt-oss-120b", Name: "GPT-OSS 120B", ContextWindow: 128000, MaxOutput: 8192},
			{ID: "qwen/qwen3-32b", Name: "Qwen3 32B", ContextWindow: 128000, MaxOutput: 8192},
		},
	},
	{ID: "cerebras", Name: "Cerebras", BaseURL: "https://api.cerebras.ai/v1", KeyURL: "https://cloud.cerebras.ai", Limits: config.Limits{MaxConcurrency: 2, RequestsPerWindow: 10, WindowSeconds: 60}},
	{ID: "together", Name: "Together AI", BaseURL: "https://api.together.ai/v1", KeyURL: "https://api.together.ai/settings/api-keys", Limits: config.DefaultLimits()},
	{ID: "fireworks", Name: "Fireworks", BaseURL: "https://api.fireworks.ai/inference/v1", KeyURL: "https://fireworks.ai/account/api-keys", Limits: config.DefaultLimits()},
	{ID: "mistral", Name: "Mistral", BaseURL: "https://api.mistral.ai/v1", KeyURL: "https://console.mistral.ai/api-keys", Limits: config.DefaultLimits()},
	{ID: "xai", Name: "xAI", BaseURL: "https://api.x.ai/v1", KeyURL: "https://console.x.ai", Limits: config.DefaultLimits()},
	{ID: "google_ai_studio", Name: "Google AI Studio", BaseURL: "https://generativelanguage.googleapis.com/v1beta/openai/", KeyURL: "https://aistudio.google.com/apikey", Limits: config.DefaultLimits()},
	{ID: "zai", Name: "Z.ai", BaseURL: "https://api.z.ai/api/paas/v4", KeyURL: "https://z.ai/manage-apikey/apikey-list", Limits: config.DefaultLimits()},
	{ID: "moonshot", Name: "Moonshot / Kimi", BaseURL: "https://api.moonshot.ai/v1", KeyURL: "https://platform.moonshot.ai/console/api-keys", Limits: config.DefaultLimits()},
	{ID: "minimax", Name: "MiniMax", BaseURL: "https://api.minimax.io/v1", KeyURL: "https://platform.minimaxi.com", Limits: config.DefaultLimits()},
	{ID: "siliconflow", Name: "SiliconFlow", BaseURL: "https://api.siliconflow.com/v1", KeyURL: "https://cloud.siliconflow.com", Limits: config.DefaultLimits()},
	{ID: "nebius", Name: "Nebius Token Factory", BaseURL: "https://api.tokenfactory.nebius.com/v1", KeyURL: "https://tokenfactory.nebius.com", Limits: config.DefaultLimits()},
	{ID: "vercel_ai_gateway", Name: "Vercel AI Gateway", BaseURL: "https://ai-gateway.vercel.sh/v1", KeyURL: "https://vercel.com/ai-gateway", Limits: config.DefaultLimits()},
	{ID: "huggingface", Name: "Hugging Face Router", BaseURL: "https://router.huggingface.co/v1", KeyURL: "https://huggingface.co/settings/tokens", Limits: config.DefaultLimits()},
	{ID: "ollama", Name: "Ollama (local)", BaseURL: "http://localhost:11434/v1", Local: true, Limits: config.Limits{MaxConcurrency: 1, RequestsPerWindow: 20, WindowSeconds: 1},
		Models: []Model{
			{ID: "qwen3-coder", Name: "Qwen3 Coder", ContextWindow: 128000, MaxOutput: 8192},
			{ID: "deepseek-coder-v2", Name: "DeepSeek Coder V2", ContextWindow: 128000, MaxOutput: 8192},
			{ID: "llama3.3", Name: "Llama 3.3", ContextWindow: 128000, MaxOutput: 8192},
		},
	},
	{ID: "lmstudio", Name: "LM Studio (local)", BaseURL: "http://localhost:1234/v1", Local: true, Limits: config.Limits{MaxConcurrency: 1, RequestsPerWindow: 20, WindowSeconds: 1}},
	{ID: "llamacpp", Name: "llama.cpp (local)", BaseURL: "http://localhost:8080/v1", Local: true, Limits: config.Limits{MaxConcurrency: 1, RequestsPerWindow: 20, WindowSeconds: 1}},
	{ID: "custom", Name: "Custom OpenAI-compatible", BaseURL: "", Limits: config.DefaultLimits()},
}

func Find(id string) (Preset, bool) {
	for _, p := range Presets {
		if p.ID == id {
			return p, true
		}
	}
	return Preset{}, false
}

func FetchModels(ctx context.Context, baseURL, apiKey string) ([]Model, error) {
	return FetchModelsWithHeaders(ctx, baseURL, apiKey, nil)
}

// AttributionHeaders adds OpenRouter's app-attribution headers when the base
// URL points at OpenRouter and the caller set neither. Free OpenRouter
// endpoints are gated to recognised apps, and these headers are how a request
// identifies itself.
func AttributionHeaders(baseURL string, headers map[string]string) map[string]string {
	if !strings.Contains(strings.ToLower(baseURL), "openrouter.ai") {
		return headers
	}
	if hasHeader(headers, "HTTP-Referer") || hasHeader(headers, "X-Title") {
		return headers
	}
	out := make(map[string]string, len(headers)+2)
	for k, v := range headers {
		out[k] = v
	}
	out["HTTP-Referer"] = "https://claude-custom.local"
	out["X-Title"] = "claude-custom"
	return out
}

func hasHeader(headers map[string]string, name string) bool {
	for k := range headers {
		if strings.EqualFold(k, name) {
			return true
		}
	}
	return false
}

func FetchModelsWithHeaders(ctx context.Context, baseURL, apiKey string, headers map[string]string) ([]Model, error) {
	baseURL = strings.TrimRight(strings.TrimSpace(baseURL), "/")
	if baseURL == "" {
		return nil, fmt.Errorf("base URL is empty")
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, baseURL+"/models", nil)
	if err != nil {
		return nil, err
	}
	if apiKey != "" {
		req.Header.Set("Authorization", "Bearer "+apiKey)
	}
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	req.Header.Set("Accept", "application/json")
	client := &http.Client{Timeout: 20 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, 8<<20))
	if err != nil {
		return nil, err
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, fmt.Errorf("model list returned %s: %s", resp.Status, snippet(body))
	}
	return parseModels(body)
}

func parseModels(body []byte) ([]Model, error) {
	var envelope struct {
		Data   []json.RawMessage `json:"data"`
		Models []json.RawMessage `json:"models"`
	}
	if err := json.Unmarshal(body, &envelope); err != nil {
		return nil, err
	}
	raw := envelope.Data
	if len(raw) == 0 {
		raw = envelope.Models
	}
	seen := map[string]bool{}
	var out []Model
	for _, item := range raw {
		var base struct {
			ID                  string `json:"id"`
			Name                string `json:"name"`
			DisplayName         string `json:"display_name"`
			ContextLength       int    `json:"context_length"`
			ContextWindow       int    `json:"context_window"`
			MaxContextLength    int    `json:"max_context_length"`
			MaxCompletionTokens int    `json:"max_completion_tokens"`
			MaxOutputTokens     int    `json:"max_output_tokens"`
			TopProvider         struct {
				ContextLength int `json:"context_length"`
			} `json:"top_provider"`
		}
		if err := json.Unmarshal(item, &base); err != nil {
			continue
		}
		id := strings.TrimSpace(base.ID)
		if id == "" || seen[id] {
			continue
		}
		seen[id] = true
		name := base.DisplayName
		if name == "" {
			name = base.Name
		}
		if name == "" {
			name = id
		}
		ctxLen := base.ContextLength
		if ctxLen == 0 {
			ctxLen = base.ContextWindow
		}
		if ctxLen == 0 {
			ctxLen = base.MaxContextLength
		}
		if ctxLen == 0 {
			ctxLen = base.TopProvider.ContextLength
		}
		maxOut := base.MaxCompletionTokens
		if maxOut == 0 {
			maxOut = base.MaxOutputTokens
		}
		out = append(out, Model{ID: id, Name: name, ContextWindow: ctxLen, MaxOutput: maxOut})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out, nil
}

func snippet(b []byte) string {
	s := strings.TrimSpace(string(b))
	if len(s) > 300 {
		s = s[:300] + "..."
	}
	return s
}

// nonChatHints are substrings of model IDs or display names that identify
// endpoints Claude Code cannot chat with: speech, audio, guardrails,
// embeddings, reranking and moderation models.
var nonChatHints = []string{
	"whisper", "tts", "asr", "vad",
	"guard", "moderation", "safety",
	"embed", "rerank", "orpheus",
}

// IsChatModel reports whether a model looks chat-capable (usable as the
// model behind Claude Code). It errs on the side of inclusion.
func IsChatModel(m Model) bool {
	text := strings.ToLower(m.ID + " " + m.Name)
	for _, hint := range nonChatHints {
		if strings.Contains(text, hint) {
			return false
		}
	}
	return true
}

// FilterChat drops models that are clearly not chat-capable.
func FilterChat(models []Model) []Model {
	out := make([]Model, 0, len(models))
	for _, m := range models {
		if IsChatModel(m) {
			out = append(out, m)
		}
	}
	return out
}

func (p Preset) DefaultModel() string {
	if len(p.Models) == 0 {
		return ""
	}
	return p.Models[0].ID
}
