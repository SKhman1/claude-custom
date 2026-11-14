package translate

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestBuildChatRequest(t *testing.T) {
	req := MessagesRequest{
		Model:     "claude-sonnet-4-5",
		MaxTokens: 1024,
		Stream:    true,
		System:    json.RawMessage(`[{"type":"text","text":"be brief"}]`),
		Messages: []Message{
			{Role: "user", Content: json.RawMessage(`"hello"`)},
			{Role: "assistant", Content: json.RawMessage(`[
				{"type":"text","text":"calling"},
				{"type":"tool_use","id":"toolu_1","name":"read","input":{"path":"a.txt"}}
			]`)},
			{Role: "user", Content: json.RawMessage(`[
				{"type":"tool_result","tool_use_id":"toolu_1","content":"contents"},
				{"type":"text","text":"continue"}
			]`)},
		},
		Tools: []Tool{{
			Name:        "read",
			Description: "read a file",
			InputSchema: json.RawMessage(`{"type":"object","properties":{"path":{"type":"string"}}}`),
		}},
		ToolChoice: json.RawMessage(`{"type":"tool","name":"read"}`),
	}

	chat, err := BuildChatRequest(req, "upstream/model")
	if err != nil {
		t.Fatal(err)
	}
	if chat.Model != "upstream/model" {
		t.Fatalf("model = %q", chat.Model)
	}
	if !chat.Stream || chat.StreamOptions == nil || !chat.StreamOptions.IncludeUsage {
		t.Fatal("stream options missing")
	}
	if len(chat.Messages) != 5 {
		t.Fatalf("messages = %d: %+v", len(chat.Messages), chat.Messages)
	}
	if chat.Messages[0].Role != "system" || chat.Messages[0].Content != "be brief" {
		t.Fatalf("system = %+v", chat.Messages[0])
	}
	if chat.Messages[2].Role != "assistant" || len(chat.Messages[2].ToolCalls) != 1 {
		t.Fatalf("assistant = %+v", chat.Messages[2])
	}
	if chat.Messages[2].ToolCalls[0].Function.Arguments != `{"path":"a.txt"}` {
		t.Fatalf("args = %q", chat.Messages[2].ToolCalls[0].Function.Arguments)
	}
	if chat.Messages[3].Role != "tool" || chat.Messages[3].ToolCallID != "toolu_1" {
		t.Fatalf("tool = %+v", chat.Messages[3])
	}
	if chat.Messages[4].Role != "user" || chat.Messages[4].Content != "continue" {
		t.Fatalf("user = %+v", chat.Messages[4])
	}
	if len(chat.Tools) != 1 || chat.Tools[0].Function.Name != "read" {
		t.Fatalf("tools = %+v", chat.Tools)
	}
	choice, ok := chat.ToolChoice.(map[string]any)
	if !ok || choice["type"] != "function" {
		t.Fatalf("tool choice = %+v", chat.ToolChoice)
	}
}

func TestBuildChatRequestMergesSystemMessages(t *testing.T) {
	req := MessagesRequest{
		System: json.RawMessage(`"base rules"`),
		Messages: []Message{
			{Role: "user", Content: json.RawMessage(`"hello"`)},
			{Role: "system", Content: json.RawMessage(`[{"type":"text","text":"mid-conversation reminder"}]`)},
			{Role: "user", Content: json.RawMessage(`"again"`)},
		},
	}

	chat, err := BuildChatRequest(req, "upstream/model")
	if err != nil {
		t.Fatal(err)
	}
	if len(chat.Messages) != 3 {
		t.Fatalf("messages = %d: %+v", len(chat.Messages), chat.Messages)
	}
	if chat.Messages[0].Role != "system" || chat.Messages[0].Content != "base rules\n\nmid-conversation reminder" {
		t.Fatalf("system = %+v", chat.Messages[0])
	}
	if chat.Messages[1].Role != "user" || chat.Messages[1].Content != "hello" {
		t.Fatalf("first user = %+v", chat.Messages[1])
	}
	if chat.Messages[2].Role != "user" || chat.Messages[2].Content != "again" {
		t.Fatalf("second user = %+v", chat.Messages[2])
	}
}

func TestBuildAnthropicResponse(t *testing.T) {
	resp := ChatResponse{
		ID: "abc",
		Choices: []ChatChoice{{
			FinishReason: "tool_calls",
			Message: ChatMessage{
				Role:    "assistant",
				Content: "thinking out loud",
				ToolCalls: []ChatToolCall{{
					ID:   "call_1",
					Type: "function",
					Function: ChatFunctionCall{
						Name:      "exec",
						Arguments: `{"cmd":"ls"}`,
					},
				}},
			},
		}},
		Usage: ChatUsage{PromptTokens: 11, CompletionTokens: 7},
	}
	out := BuildAnthropicResponse(resp, "upstream")
	if out.StopReason != "tool_use" {
		t.Fatalf("stop reason = %q", out.StopReason)
	}
	if len(out.Content) != 2 || out.Content[0].Type != "text" || out.Content[1].Type != "tool_use" {
		t.Fatalf("content = %+v", out.Content)
	}
	if out.Usage.InputTokens != 11 || out.Usage.OutputTokens != 7 {
		t.Fatalf("usage = %+v", out.Usage)
	}
	if out.ID != "msg_abc" {
		t.Fatalf("id = %q", out.ID)
	}
}

type captureWriter struct {
	names    []string
	payloads []map[string]any
}

func (c *captureWriter) Event(name string, payload any) error {
	c.names = append(c.names, name)
	data, _ := json.Marshal(payload)
	var m map[string]any
	_ = json.Unmarshal(data, &m)
	c.payloads = append(c.payloads, m)
	return nil
}

func TestStreamConverter(t *testing.T) {
	w := &captureWriter{}
	conv := NewStreamConverter(w, "upstream")

	toolDelta := func(calls ...ChatToolCall) ChatResponse {
		return ChatResponse{Choices: []ChatChoice{{Delta: ChatDelta{ToolCalls: calls}}}}
	}
	chunks := []ChatResponse{
		{Choices: []ChatChoice{{Delta: ChatDelta{Role: "assistant", Content: "Hel"}}}},
		{Choices: []ChatChoice{{Delta: ChatDelta{Content: "lo"}}}},
		toolDelta(ChatToolCall{
			Index: 0, ID: "call_1", Type: "function",
			Function: ChatFunctionCall{Name: "read"},
		}),
		toolDelta(ChatToolCall{
			Index:    0,
			Function: ChatFunctionCall{Arguments: `{"path":`},
		}),
		{
			Choices: []ChatChoice{{
				Delta: ChatDelta{ToolCalls: []ChatToolCall{{
					Index:    0,
					Function: ChatFunctionCall{Arguments: `"x"}`},
				}}},
				FinishReason: "tool_calls",
			}},
		},
	}
	for _, chunk := range chunks {
		if err := conv.Handle(chunk); err != nil {
			t.Fatal(err)
		}
	}
	if err := conv.Finish(); err != nil {
		t.Fatal(err)
	}

	want := []string{
		"message_start",
		"content_block_start",
		"content_block_delta",
		"content_block_delta",
		"content_block_stop",
		"content_block_start",
		"content_block_delta",
		"content_block_delta",
		"content_block_stop",
		"message_delta",
		"message_stop",
	}
	if strings.Join(w.names, ",") != strings.Join(want, ",") {
		t.Fatalf("events = %v", w.names)
	}
	if w.payloads[6]["delta"].(map[string]any)["partial_json"] != `{"path":` {
		t.Fatalf("first arg fragment = %+v", w.payloads[6])
	}
	lastDelta := w.payloads[9]["delta"].(map[string]any)
	if lastDelta["stop_reason"] != "tool_use" {
		t.Fatalf("stop reason = %+v", lastDelta)
	}
}

func TestStreamConverterNonStreamingFallback(t *testing.T) {
	w := &captureWriter{}
	conv := NewStreamConverter(w, "upstream")
	chunk := ChatResponse{Choices: []ChatChoice{{
		FinishReason: "stop",
		Message:      ChatMessage{Role: "assistant", Content: "hi there"},
	}}}
	if err := conv.Handle(chunk); err != nil {
		t.Fatal(err)
	}
	if err := conv.Finish(); err != nil {
		t.Fatal(err)
	}
	if len(w.names) == 0 || w.names[0] != "message_start" {
		t.Fatalf("events = %v", w.names)
	}
}
