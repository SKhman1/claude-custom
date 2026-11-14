package translate

import (
	"encoding/json"
	"strings"
)

type AnthropicResponse struct {
	ID           string           `json:"id"`
	Type         string           `json:"type"`
	Role         string           `json:"role"`
	Model        string           `json:"model"`
	Content      []AnthropicBlock `json:"content"`
	StopReason   string           `json:"stop_reason"`
	StopSequence *string          `json:"stop_sequence"`
	Usage        AnthropicUsage   `json:"usage"`
}

type AnthropicBlock struct {
	Type  string `json:"type"`
	Text  string `json:"text,omitempty"`
	ID    string `json:"id,omitempty"`
	Name  string `json:"name,omitempty"`
	Input any    `json:"input,omitempty"`
}

type AnthropicUsage struct {
	InputTokens              int `json:"input_tokens"`
	OutputTokens             int `json:"output_tokens"`
	CacheCreationInputTokens int `json:"cache_creation_input_tokens,omitempty"`
	CacheReadInputTokens     int `json:"cache_read_input_tokens,omitempty"`
}

func BuildAnthropicResponse(resp ChatResponse, model string) AnthropicResponse {
	out := AnthropicResponse{
		ID:         "msg_" + strings.TrimPrefix(resp.ID, "msg_"),
		Type:       "message",
		Role:       "assistant",
		Model:      model,
		Content:    []AnthropicBlock{},
		StopReason: "end_turn",
		Usage: AnthropicUsage{
			InputTokens:          resp.Usage.PromptTokens,
			OutputTokens:         resp.Usage.CompletionTokens,
			CacheReadInputTokens: resp.Usage.PromptTokensDetails.CachedTokens,
		},
	}
	if resp.ID == "" {
		out.ID = newID("msg_")
	}
	if len(resp.Choices) == 0 {
		return out
	}
	choice := resp.Choices[0]
	if choice.Message.Content != nil {
		switch v := choice.Message.Content.(type) {
		case string:
			if v != "" {
				out.Content = append(out.Content, AnthropicBlock{Type: "text", Text: v})
			}
		case []any:
			var parts []string
			for _, item := range v {
				m, ok := item.(map[string]any)
				if !ok {
					continue
				}
				if text, ok := m["text"].(string); ok && text != "" {
					parts = append(parts, text)
				}
			}
			if len(parts) > 0 {
				out.Content = append(out.Content, AnthropicBlock{Type: "text", Text: strings.Join(parts, "")})
			}
		}
	}
	for _, call := range choice.Message.ToolCalls {
		var input any = map[string]any{}
		if strings.TrimSpace(call.Function.Arguments) != "" {
			if err := json.Unmarshal([]byte(call.Function.Arguments), &input); err != nil {
				input = map[string]any{"_raw": call.Function.Arguments}
			}
		}
		if input == nil {
			input = map[string]any{}
		}
		id := call.ID
		if id == "" {
			id = newID("toolu_")
		}
		out.Content = append(out.Content, AnthropicBlock{
			Type:  "tool_use",
			ID:    id,
			Name:  call.Function.Name,
			Input: input,
		})
	}
	out.StopReason = mapStopReason(choice.FinishReason, len(choice.Message.ToolCalls) > 0)
	return out
}

func mapStopReason(reason string, hasTools bool) string {
	switch reason {
	case "stop":
		return "end_turn"
	case "length":
		return "max_tokens"
	case "tool_calls", "function_call":
		return "tool_use"
	case "content_filter":
		return "refusal"
	case "":
		if hasTools {
			return "tool_use"
		}
		return "end_turn"
	default:
		return "end_turn"
	}
}
