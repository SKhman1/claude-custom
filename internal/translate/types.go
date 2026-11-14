package translate

import (
	"bytes"
	"encoding/json"
	"fmt"
	"strings"
)

// Anthropic Messages API request types (the subset Claude Code uses).

type MessagesRequest struct {
	Model         string          `json:"model"`
	MaxTokens     int             `json:"max_tokens"`
	Messages      []Message       `json:"messages"`
	System        json.RawMessage `json:"system,omitempty"`
	Tools         []Tool          `json:"tools,omitempty"`
	ToolChoice    json.RawMessage `json:"tool_choice,omitempty"`
	Stream        bool            `json:"stream,omitempty"`
	Temperature   *float64        `json:"temperature,omitempty"`
	TopP          *float64        `json:"top_p,omitempty"`
	TopK          *int            `json:"top_k,omitempty"`
	StopSequences []string        `json:"stop_sequences,omitempty"`
	Metadata      map[string]any  `json:"metadata,omitempty"`
	Thinking      json.RawMessage `json:"thinking,omitempty"`
}

type Message struct {
	Role    string          `json:"role"`
	Content json.RawMessage `json:"content"`
}

type Tool struct {
	Name        string          `json:"name"`
	Description string          `json:"description,omitempty"`
	InputSchema json.RawMessage `json:"input_schema"`
}

type Block struct {
	Type      string          `json:"type"`
	Text      string          `json:"text,omitempty"`
	Source    *ImageSource    `json:"source,omitempty"`
	ID        string          `json:"id,omitempty"`
	Name      string          `json:"name,omitempty"`
	Input     json.RawMessage `json:"input,omitempty"`
	ToolUseID string          `json:"tool_use_id,omitempty"`
	Content   json.RawMessage `json:"content,omitempty"`
	IsError   bool            `json:"is_error,omitempty"`
	Thinking  string          `json:"thinking,omitempty"`
}

type ImageSource struct {
	Type      string `json:"type"`
	MediaType string `json:"media_type,omitempty"`
	Data      string `json:"data,omitempty"`
	URL       string `json:"url,omitempty"`
}

// OpenAI Chat Completions types.

type ChatRequest struct {
	Model               string         `json:"model"`
	Messages            []ChatMessage  `json:"messages"`
	MaxTokens           int            `json:"max_tokens,omitempty"`
	MaxCompletionTokens int            `json:"max_completion_tokens,omitempty"`
	Temperature         *float64       `json:"temperature,omitempty"`
	TopP                *float64       `json:"top_p,omitempty"`
	Stop                []string       `json:"stop,omitempty"`
	Stream              bool           `json:"stream,omitempty"`
	StreamOptions       *StreamOptions `json:"stream_options,omitempty"`
	Tools               []ChatTool     `json:"tools,omitempty"`
	ToolChoice          any            `json:"tool_choice,omitempty"`
	ParallelToolCalls   *bool          `json:"parallel_tool_calls,omitempty"`
}

type StreamOptions struct {
	IncludeUsage bool `json:"include_usage"`
}

type ChatMessage struct {
	Role       string         `json:"role"`
	Content    any            `json:"content,omitempty"`
	Name       string         `json:"name,omitempty"`
	ToolCalls  []ChatToolCall `json:"tool_calls,omitempty"`
	ToolCallID string         `json:"tool_call_id,omitempty"`
}

type ChatToolCall struct {
	ID       string           `json:"id,omitempty"`
	Index    int              `json:"index,omitempty"`
	Type     string           `json:"type,omitempty"`
	Function ChatFunctionCall `json:"function"`
}

type ChatFunctionCall struct {
	Name      string `json:"name,omitempty"`
	Arguments string `json:"arguments,omitempty"`
}

type ChatTool struct {
	Type     string          `json:"type"`
	Function ChatFunctionDef `json:"function"`
}

type ChatFunctionDef struct {
	Name        string          `json:"name"`
	Description string          `json:"description,omitempty"`
	Parameters  json.RawMessage `json:"parameters,omitempty"`
}

type ChatResponse struct {
	ID      string       `json:"id"`
	Model   string       `json:"model"`
	Choices []ChatChoice `json:"choices"`
	Usage   ChatUsage    `json:"usage"`
	Error   *APIError    `json:"error,omitempty"`
}

type ChatChoice struct {
	Index        int         `json:"index"`
	Message      ChatMessage `json:"message"`
	Delta        ChatDelta   `json:"delta"`
	FinishReason string      `json:"finish_reason"`
}

type ChatDelta struct {
	Role             string         `json:"role"`
	Content          string         `json:"content"`
	ReasoningContent string         `json:"reasoning_content"`
	ToolCalls        []ChatToolCall `json:"tool_calls"`
}

type ChatUsage struct {
	PromptTokens        int `json:"prompt_tokens"`
	CompletionTokens    int `json:"completion_tokens"`
	TotalTokens         int `json:"total_tokens"`
	PromptTokensDetails struct {
		CachedTokens int `json:"cached_tokens"`
	} `json:"prompt_tokens_details"`
	CompletionTokensDetails struct {
		ReasoningTokens int `json:"reasoning_tokens"`
	} `json:"completion_tokens_details"`
}

type APIError struct {
	Message string `json:"message"`
	Type    string `json:"type"`
	Code    any    `json:"code"`
}

// Content helpers.

func decodeContent(raw json.RawMessage) (text string, blocks []Block, err error) {
	if len(raw) == 0 || bytes.Equal(bytes.TrimSpace(raw), []byte("null")) {
		return "", nil, nil
	}
	var s string
	if err := json.Unmarshal(raw, &s); err == nil {
		return s, nil, nil
	}
	if err := json.Unmarshal(raw, &blocks); err != nil {
		return "", nil, fmt.Errorf("content must be a string or an array of blocks")
	}
	return "", blocks, nil
}

func systemText(raw json.RawMessage) (string, error) {
	if len(raw) == 0 {
		return "", nil
	}
	text, blocks, err := decodeContent(raw)
	if err != nil {
		return "", err
	}
	if blocks == nil {
		return text, nil
	}
	var parts []string
	for _, b := range blocks {
		if b.Type == "text" && b.Text != "" {
			parts = append(parts, b.Text)
		}
	}
	return strings.Join(parts, "\n\n"), nil
}

func blockText(raw json.RawMessage) string {
	if len(raw) == 0 {
		return ""
	}
	text, blocks, err := decodeContent(raw)
	if err != nil {
		return ""
	}
	if blocks == nil {
		return text
	}
	var parts []string
	for _, b := range blocks {
		if b.Type == "text" && b.Text != "" {
			parts = append(parts, b.Text)
		}
	}
	return strings.Join(parts, "\n")
}

func compactJSON(raw json.RawMessage) string {
	if len(raw) == 0 {
		return "{}"
	}
	var buf bytes.Buffer
	if err := json.Compact(&buf, raw); err != nil {
		return "{}"
	}
	s := buf.String()
	if s == "" || s == "null" {
		return "{}"
	}
	return s
}

func imageURL(src *ImageSource) string {
	if src == nil {
		return ""
	}
	if src.URL != "" {
		return src.URL
	}
	if src.Data != "" {
		mime := src.MediaType
		if mime == "" {
			mime = "image/png"
		}
		return "data:" + mime + ";base64," + src.Data
	}
	return ""
}
