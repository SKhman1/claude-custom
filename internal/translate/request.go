package translate

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"strings"
)

// BuildChatRequest converts an Anthropic Messages request into an OpenAI Chat
// Completions request for the selected provider model.
func BuildChatRequest(req MessagesRequest, model string) (ChatRequest, error) {
	out := ChatRequest{
		Model:       model,
		MaxTokens:   req.MaxTokens,
		Temperature: req.Temperature,
		TopP:        req.TopP,
		Stop:        req.StopSequences,
		Stream:      req.Stream,
	}
	if out.MaxTokens <= 0 {
		out.MaxTokens = 8192
	}
	if out.Stream {
		out.StreamOptions = &StreamOptions{IncludeUsage: true}
	}

	system, err := systemText(req.System)
	if err != nil {
		return ChatRequest{}, err
	}
	systemParts := make([]string, 0, 1)
	if strings.TrimSpace(system) != "" {
		systemParts = append(systemParts, system)
	}

	for i, message := range req.Messages {
		switch message.Role {
		case "system":
			text, err := systemText(message.Content)
			if err != nil {
				return ChatRequest{}, fmt.Errorf("messages[%d]: %w", i, err)
			}
			if strings.TrimSpace(text) != "" {
				systemParts = append(systemParts, text)
			}
		case "user":
			msgs, err := convertUserMessage(message)
			if err != nil {
				return ChatRequest{}, fmt.Errorf("messages[%d]: %w", i, err)
			}
			out.Messages = append(out.Messages, msgs...)
		case "assistant":
			msg, err := convertAssistantMessage(message)
			if err != nil {
				return ChatRequest{}, fmt.Errorf("messages[%d]: %w", i, err)
			}
			out.Messages = append(out.Messages, msg)
		default:
			return ChatRequest{}, fmt.Errorf("messages[%d]: unsupported role %q", i, message.Role)
		}
	}
	if len(systemParts) > 0 {
		out.Messages = append([]ChatMessage{{Role: "system", Content: strings.Join(systemParts, "\n\n")}}, out.Messages...)
	}

	for _, tool := range req.Tools {
		params := tool.InputSchema
		if len(params) == 0 || string(params) == "null" {
			params = json.RawMessage(`{"type":"object","properties":{}}`)
		}
		out.Tools = append(out.Tools, ChatTool{
			Type: "function",
			Function: ChatFunctionDef{
				Name:        tool.Name,
				Description: tool.Description,
				Parameters:  params,
			},
		})
	}

	if choice, parallel, err := convertToolChoice(req.ToolChoice); err != nil {
		return ChatRequest{}, err
	} else {
		out.ToolChoice = choice
		out.ParallelToolCalls = parallel
	}

	return out, nil
}

func convertUserMessage(message Message) ([]ChatMessage, error) {
	text, blocks, err := decodeContent(message.Content)
	if err != nil {
		return nil, err
	}
	if blocks == nil {
		return []ChatMessage{{Role: "user", Content: text}}, nil
	}

	var toolMessages []ChatMessage
	var textParts []string
	var parts []any
	for _, block := range blocks {
		switch block.Type {
		case "text":
			if block.Text != "" {
				textParts = append(textParts, block.Text)
			}
		case "image":
			url := imageURL(block.Source)
			if url != "" {
				parts = append(parts, map[string]any{
					"type":      "image_url",
					"image_url": map[string]any{"url": url},
				})
			}
		case "tool_result":
			content := blockText(block.Content)
			if block.IsError {
				content = "Error: " + content
			}
			toolMessages = append(toolMessages, ChatMessage{
				Role:       "tool",
				ToolCallID: block.ToolUseID,
				Content:    content,
			})
		}
	}

	joined := strings.Join(textParts, "")
	if joined != "" {
		if len(parts) > 0 {
			parts = append([]any{map[string]any{"type": "text", "text": joined}}, parts...)
		}
	}
	var out []ChatMessage
	out = append(out, toolMessages...)
	switch {
	case len(parts) > 0:
		out = append(out, ChatMessage{Role: "user", Content: parts})
	case joined != "":
		out = append(out, ChatMessage{Role: "user", Content: joined})
	case len(toolMessages) == 0:
		out = append(out, ChatMessage{Role: "user", Content: ""})
	}
	return out, nil
}

func convertAssistantMessage(message Message) (ChatMessage, error) {
	text, blocks, err := decodeContent(message.Content)
	if err != nil {
		return ChatMessage{}, err
	}
	out := ChatMessage{Role: "assistant"}
	if blocks == nil {
		out.Content = text
		return out, nil
	}

	var textParts []string
	for _, block := range blocks {
		switch block.Type {
		case "text":
			if block.Text != "" {
				textParts = append(textParts, block.Text)
			}
		case "tool_use":
			id := block.ID
			if id == "" {
				id = newID("call_")
			}
			out.ToolCalls = append(out.ToolCalls, ChatToolCall{
				ID:   id,
				Type: "function",
				Function: ChatFunctionCall{
					Name:      block.Name,
					Arguments: compactJSON(block.Input),
				},
			})
		}
	}
	if joined := strings.Join(textParts, ""); joined != "" {
		out.Content = joined
	}
	return out, nil
}

func convertToolChoice(raw json.RawMessage) (any, *bool, error) {
	if len(raw) == 0 || string(raw) == "null" {
		return nil, nil, nil
	}
	var s string
	if err := json.Unmarshal(raw, &s); err == nil {
		switch s {
		case "auto":
			return "auto", nil, nil
		case "any":
			return "required", nil, nil
		case "none":
			return "none", nil, nil
		default:
			return nil, nil, fmt.Errorf("unsupported tool_choice %q", s)
		}
	}
	var obj struct {
		Type                   string `json:"type"`
		Name                   string `json:"name"`
		DisableParallelToolUse bool   `json:"disable_parallel_tool_use"`
	}
	if err := json.Unmarshal(raw, &obj); err != nil {
		return nil, nil, fmt.Errorf("invalid tool_choice")
	}
	var parallel *bool
	if obj.DisableParallelToolUse {
		v := false
		parallel = &v
	}
	if obj.Type == "tool" && obj.Name != "" {
		return map[string]any{
			"type":     "function",
			"function": map[string]any{"name": obj.Name},
		}, parallel, nil
	}
	return nil, parallel, nil
}

func newID(prefix string) string {
	var b [12]byte
	_, _ = rand.Read(b[:])
	return prefix + hex.EncodeToString(b[:])
}
