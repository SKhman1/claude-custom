package translate

import (
	"encoding/json"
	"fmt"
	"io"
	"sort"
	"strings"
)

type EventWriter interface {
	Event(name string, payload any) error
}

func WriteSSE(w io.Writer, name string, payload any) error {
	data, err := json.Marshal(payload)
	if err != nil {
		return err
	}
	if _, err := fmt.Fprintf(w, "event: %s\ndata: %s\n\n", name, data); err != nil {
		return err
	}
	return nil
}

type toolStreamState struct {
	blockIndex int
	id         string
	name       string
	args       strings.Builder
	opened     bool
}

type StreamConverter struct {
	w         EventWriter
	model     string
	messageID string
	started   bool

	nextIndex int
	textOpen  bool
	textIndex int

	tools     map[int]*toolStreamState
	toolOrder []int

	usage        ChatUsage
	finishReason string
	hasTools     bool
	text         strings.Builder
}

func NewStreamConverter(w EventWriter, model string) *StreamConverter {
	return &StreamConverter{
		w:         w,
		model:     model,
		messageID: newID("msg_"),
		tools:     map[int]*toolStreamState{},
	}
}

func (c *StreamConverter) Text() string   { return c.text.String() }
func (c *StreamConverter) HasTools() bool { return c.hasTools }
func (c *StreamConverter) Finished() bool { return c.finishReason != "" }

func (c *StreamConverter) Handle(chunk ChatResponse) error {
	if !c.started {
		if err := c.start(); err != nil {
			return err
		}
	}
	if chunk.Usage.PromptTokens != 0 || chunk.Usage.CompletionTokens != 0 {
		c.usage = chunk.Usage
	}
	if len(chunk.Choices) == 0 {
		return nil
	}
	choice := chunk.Choices[0]
	if choice.FinishReason != "" {
		c.finishReason = choice.FinishReason
	}
	delta := choice.Delta
	if delta.Content == "" && len(delta.ToolCalls) == 0 {
		// Some gateways ignore stream=true and return one full completion.
		if text, ok := choice.Message.Content.(string); ok {
			delta.Content = text
		}
		if len(choice.Message.ToolCalls) > 0 {
			delta.ToolCalls = choice.Message.ToolCalls
		}
	}

	if delta.Content != "" {
		if err := c.ensureText(); err != nil {
			return err
		}
		if err := c.w.Event("content_block_delta", map[string]any{
			"type":  "content_block_delta",
			"index": c.textIndex,
			"delta": map[string]any{"type": "text_delta", "text": delta.Content},
		}); err != nil {
			return err
		}
		c.text.WriteString(delta.Content)
	}

	for _, call := range delta.ToolCalls {
		if c.textOpen {
			if err := c.closeText(); err != nil {
				return err
			}
		}
		state, ok := c.tools[call.Index]
		if !ok {
			state = &toolStreamState{blockIndex: c.nextIndex, id: call.ID, name: call.Function.Name}
			c.nextIndex++
			c.tools[call.Index] = state
			c.toolOrder = append(c.toolOrder, call.Index)
			c.hasTools = true
		}
		if call.ID != "" {
			state.id = call.ID
		}
		if call.Function.Name != "" {
			state.name = call.Function.Name
		}
		if call.Function.Arguments != "" {
			if state.opened {
				if err := c.emitArgs(state, call.Function.Arguments); err != nil {
					return err
				}
			} else {
				state.args.WriteString(call.Function.Arguments)
			}
		}
		if !state.opened && state.name != "" {
			if err := c.openTool(state); err != nil {
				return err
			}
		}
	}
	return nil
}

func (c *StreamConverter) EnsureStarted() error {
	if c.started {
		return nil
	}
	return c.start()
}

func (c *StreamConverter) start() error {
	c.started = true
	return c.w.Event("message_start", map[string]any{
		"type": "message_start",
		"message": map[string]any{
			"id":            c.messageID,
			"type":          "message",
			"role":          "assistant",
			"model":         c.model,
			"content":       []any{},
			"stop_reason":   nil,
			"stop_sequence": nil,
			"usage": map[string]any{
				"input_tokens":  0,
				"output_tokens": 0,
			},
		},
	})
}

func (c *StreamConverter) ensureText() error {
	if c.textOpen {
		return nil
	}
	c.textOpen = true
	c.textIndex = c.nextIndex
	c.nextIndex++
	return c.w.Event("content_block_start", map[string]any{
		"type":          "content_block_start",
		"index":         c.textIndex,
		"content_block": map[string]any{"type": "text", "text": ""},
	})
}

func (c *StreamConverter) openTool(state *toolStreamState) error {
	state.opened = true
	id := state.id
	if id == "" {
		id = newID("toolu_")
		state.id = id
	}
	name := state.name
	if name == "" {
		name = "unknown_tool"
	}
	if err := c.w.Event("content_block_start", map[string]any{
		"type":  "content_block_start",
		"index": state.blockIndex,
		"content_block": map[string]any{
			"type":  "tool_use",
			"id":    id,
			"name":  name,
			"input": map[string]any{},
		},
	}); err != nil {
		return err
	}
	if state.args.Len() > 0 {
		fragment := state.args.String()
		state.args.Reset()
		return c.emitArgs(state, fragment)
	}
	return nil
}

func (c *StreamConverter) emitArgs(state *toolStreamState, fragment string) error {
	return c.w.Event("content_block_delta", map[string]any{
		"type":  "content_block_delta",
		"index": state.blockIndex,
		"delta": map[string]any{"type": "input_json_delta", "partial_json": fragment},
	})
}

func (c *StreamConverter) closeText() error {
	if !c.textOpen {
		return nil
	}
	c.textOpen = false
	return c.w.Event("content_block_stop", map[string]any{
		"type": "content_block_stop", "index": c.textIndex,
	})
}

func (c *StreamConverter) Finish() error {
	if err := c.EnsureStarted(); err != nil {
		return err
	}

	if c.textOpen {
		if err := c.closeText(); err != nil {
			return err
		}
	}

	sort.Ints(c.toolOrder)
	for _, key := range c.toolOrder {
		state := c.tools[key]
		if !state.opened {
			if err := c.openTool(state); err != nil {
				return err
			}
		}
		if state.args.Len() > 0 {
			fragment := state.args.String()
			state.args.Reset()
			if err := c.emitArgs(state, fragment); err != nil {
				return err
			}
		}
		if err := c.w.Event("content_block_stop", map[string]any{
			"type": "content_block_stop", "index": state.blockIndex,
		}); err != nil {
			return err
		}
	}

	stopReason := mapStopReason(c.finishReason, c.hasTools)
	if err := c.w.Event("message_delta", map[string]any{
		"type":  "message_delta",
		"delta": map[string]any{"stop_reason": stopReason, "stop_sequence": nil},
		"usage": map[string]any{"output_tokens": c.usage.CompletionTokens},
	}); err != nil {
		return err
	}
	return c.w.Event("message_stop", map[string]any{"type": "message_stop"})
}
