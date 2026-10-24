package anthropic

import (
	"encoding/base64"
	"encoding/json"

	"github.com/xlsft/openclick/internal/llm"
)

type messagesRequest struct {
	Model        string        `json:"model"`
	MaxTokens    int           `json:"max_tokens"`
	System       []textBlock   `json:"system,omitempty"`
	Messages     []message     `json:"messages"`
	Stream       bool          `json:"stream"`
	Tools        []tool        `json:"tools,omitempty"`
	ToolChoice   *toolChoice   `json:"tool_choice,omitempty"`
	Temperature  *float64      `json:"temperature,omitempty"`
	Thinking     *thinking     `json:"thinking,omitempty"`
	OutputConfig *outputConfig `json:"output_config,omitempty"`
}

type message struct {
	Role    string `json:"role"`
	Content []any  `json:"content"`
}

type textBlock struct {
	Type string `json:"type"` // "text"
	Text string `json:"text"`
}

type imageBlock struct {
	Type   string      `json:"type"` // "image"
	Source imageSource `json:"source"`
}

type imageSource struct {
	Type      string `json:"type"` // "base64"
	MediaType string `json:"media_type"`
	Data      string `json:"data"`
}

type thinkingBlock struct {
	Type      string `json:"type"` // "thinking"
	Thinking  string `json:"thinking"`
	Signature string `json:"signature"`
}

type toolUseBlock struct {
	Type  string          `json:"type"` // "tool_use"
	ID    string          `json:"id"`
	Name  string          `json:"name"`
	Input json.RawMessage `json:"input"`
}

type toolResultBlock struct {
	Type      string `json:"type"` // "tool_result"
	ToolUseID string `json:"tool_use_id"`
	Content   []any  `json:"content,omitempty"`
	IsError   bool   `json:"is_error,omitempty"`
}

type tool struct {
	Name        string          `json:"name"`
	Description string          `json:"description,omitempty"`
	InputSchema json.RawMessage `json:"input_schema"`
}

type toolChoice struct {
	Type string `json:"type"`
}

type thinking struct {
	Type         string `json:"type"`
	BudgetTokens int    `json:"budget_tokens,omitempty"`
}

type outputConfig struct {
	Effort string  `json:"effort,omitempty"`
	Format *format `json:"format,omitempty"`
}

type format struct {
	Type   string          `json:"type"` // "json_schema"
	Schema json.RawMessage `json:"schema"`
}

var emptySchema = json.RawMessage(`{"type":"object","properties":{}}`)

func buildRequest(model string, req llm.ChatRequest) messagesRequest {
	p := req.Policy
	out := messagesRequest{
		Model:       model,
		MaxTokens:   p.MaxOutputTokens,
		Stream:      true,
		Temperature: p.Temperature,
	}
	if out.MaxTokens == 0 {
		out.MaxTokens = DefaultMaxTokens
		if p.Thinking == llm.ThinkingBudget {
			// max_tokens must exceed the thinking budget.
			out.MaxTokens += p.ThinkingBudget
		}
	}
	out.System, out.Messages = buildMessages(req.WithAttachments())
	for _, t := range req.Tools {
		schema := t.Schema
		if len(schema) == 0 {
			schema = emptySchema
		}
		out.Tools = append(out.Tools, tool{Name: t.Name, Description: t.Description, InputSchema: schema})
	}
	switch p.ToolChoice {
	case llm.ToolChoiceNone:
		out.ToolChoice = &toolChoice{Type: "none"}
	case llm.ToolChoiceRequired:
		out.ToolChoice = &toolChoice{Type: "any"}
	}
	switch p.Thinking {
	case llm.ThinkingAdaptive:
		out.Thinking = &thinking{Type: "adaptive"}
	case llm.ThinkingBudget:
		out.Thinking = &thinking{Type: "enabled", BudgetTokens: p.ThinkingBudget}
	}
	if p.Effort != "" || req.ResponseSchema != nil {
		out.OutputConfig = &outputConfig{Effort: p.Effort}
		if s := req.ResponseSchema; s != nil {
			out.OutputConfig.Format = &format{Type: "json_schema", Schema: s.Schema}
		}
	}
	return out
}

func buildMessages(msgs []llm.Message) ([]textBlock, []message) {
	var system []textBlock
	var out []message
	// Tool results travel in user messages; consecutive user-side content
	// (parallel tool results, then the next user turn) is merged into one
	// message, as the API expects all results of a turn together.
	appendUser := func(blocks []any) {
		if n := len(out); n > 0 && out[n-1].Role == "user" {
			out[n-1].Content = append(out[n-1].Content, blocks...)
			return
		}
		out = append(out, message{Role: "user", Content: blocks})
	}
	for _, m := range msgs {
		switch m.Role {
		case llm.RoleSystem:
			for _, p := range m.Content {
				if p.Kind == llm.PartText && p.Text != "" {
					system = append(system, textBlock{Type: "text", Text: p.Text})
				}
			}
		case llm.RoleUser:
			appendUser(contentBlocks(m.Content))
		case llm.RoleAssistant:
			var blocks []any
			// Thinking must precede the text and tool use it led to.
			for _, p := range m.Content {
				if p.Kind == llm.PartThinking {
					blocks = append(blocks, thinkingBlock{Type: "thinking", Thinking: p.Text, Signature: p.Signature})
				}
			}
			for _, p := range m.Content {
				if p.Kind == llm.PartText && p.Text != "" {
					blocks = append(blocks, textBlock{Type: "text", Text: p.Text})
				}
			}
			for _, c := range m.ToolCalls {
				input := c.Arguments
				if len(input) == 0 {
					input = json.RawMessage("{}")
				}
				blocks = append(blocks, toolUseBlock{Type: "tool_use", ID: c.ID, Name: c.Name, Input: input})
			}
			out = append(out, message{Role: "assistant", Content: blocks})
		case llm.RoleTool:
			appendUser([]any{toolResultBlock{
				Type:      "tool_result",
				ToolUseID: m.ToolCallID,
				Content:   contentBlocks(m.Content),
				IsError:   m.IsError,
			}})
		}
	}
	return system, out
}

func contentBlocks(parts []llm.Part) []any {
	var blocks []any
	for _, p := range parts {
		switch p.Kind {
		case llm.PartText:
			if p.Text != "" { // the API rejects empty text blocks
				blocks = append(blocks, textBlock{Type: "text", Text: p.Text})
			}
		case llm.PartImage:
			blocks = append(blocks, imageBlock{Type: "image", Source: imageSource{
				Type:      "base64",
				MediaType: p.Image.MediaType,
				Data:      base64.StdEncoding.EncodeToString(p.Image.Data),
			}})
		}
	}
	return blocks
}
