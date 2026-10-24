package openai

import (
	"encoding/base64"
	"encoding/json"
	"strings"

	"github.com/xlsft/openclick/internal/llm"
)

type chatRequest struct {
	Model               string          `json:"model"`
	Messages            []message       `json:"messages"`
	Stream              bool            `json:"stream"`
	StreamOptions       streamOptions   `json:"stream_options"`
	Tools               []tool          `json:"tools,omitempty"`
	ToolChoice          string          `json:"tool_choice,omitempty"`
	MaxCompletionTokens int             `json:"max_completion_tokens,omitempty"`
	Temperature         *float64        `json:"temperature,omitempty"`
	ReasoningEffort     string          `json:"reasoning_effort,omitempty"`
	ResponseFormat      *responseFormat `json:"response_format,omitempty"`
}

type streamOptions struct {
	IncludeUsage bool `json:"include_usage"`
}

type message struct {
	Role       string     `json:"role"`
	Content    any        `json:"content"` // string, []contentPart or nil
	ToolCalls  []toolCall `json:"tool_calls,omitempty"`
	ToolCallID string     `json:"tool_call_id,omitempty"`
}

type contentPart struct {
	Type     string    `json:"type"`
	Text     string    `json:"text,omitempty"`
	ImageURL *imageURL `json:"image_url,omitempty"`
}

type imageURL struct {
	URL string `json:"url"`
}

type toolCall struct {
	ID       string       `json:"id"`
	Type     string       `json:"type"`
	Function functionCall `json:"function"`
}

type functionCall struct {
	Name      string `json:"name"`
	Arguments string `json:"arguments"` // JSON encoded as a string
}

type tool struct {
	Type     string       `json:"type"`
	Function functionDecl `json:"function"`
}

type functionDecl struct {
	Name        string          `json:"name"`
	Description string          `json:"description,omitempty"`
	Parameters  json.RawMessage `json:"parameters"`
}

type responseFormat struct {
	Type       string     `json:"type"`
	JSONSchema jsonSchema `json:"json_schema"`
}

type jsonSchema struct {
	Name   string          `json:"name"`
	Schema json.RawMessage `json:"schema"`
}

var emptySchema = json.RawMessage(`{"type":"object","properties":{}}`)

func buildRequest(model string, req llm.ChatRequest) chatRequest {
	out := chatRequest{
		Model:               model,
		Messages:            buildMessages(req.WithAttachments()),
		Stream:              true,
		StreamOptions:       streamOptions{IncludeUsage: true},
		MaxCompletionTokens: req.Policy.MaxOutputTokens,
		Temperature:         req.Policy.Temperature,
		ReasoningEffort:     req.Policy.Effort,
	}
	switch req.Policy.ToolChoice {
	case llm.ToolChoiceNone:
		out.ToolChoice = "none"
	case llm.ToolChoiceRequired:
		out.ToolChoice = "required"
	}
	for _, t := range req.Tools {
		params := t.Schema
		if len(params) == 0 {
			params = emptySchema
		}
		out.Tools = append(out.Tools, tool{Type: "function", Function: functionDecl{Name: t.Name, Description: t.Description, Parameters: params}})
	}
	if s := req.ResponseSchema; s != nil {
		name := s.Name
		if name == "" {
			name = "response"
		}
		out.ResponseFormat = &responseFormat{Type: "json_schema", JSONSchema: jsonSchema{Name: name, Schema: s.Schema}}
	}
	return out
}

func buildMessages(msgs []llm.Message) []message {
	var out []message
	// Tool messages carry text only in Chat Completions; images a tool
	// returned (e.g. a screenshot) follow the run of tool messages as one
	// user message, which is where the protocol accepts images.
	var pendingImages []contentPart
	flush := func() {
		if len(pendingImages) > 0 {
			out = append(out, message{Role: "user", Content: pendingImages})
			pendingImages = nil
		}
	}
	for _, m := range msgs {
		if m.Role != llm.RoleTool {
			flush()
		}
		switch m.Role {
		case llm.RoleSystem:
			out = append(out, message{Role: "system", Content: joinText(m.Content)})
		case llm.RoleUser:
			out = append(out, message{Role: "user", Content: userContent(m.Content)})
		case llm.RoleAssistant:
			am := message{Role: "assistant"}
			// Reasoning parts cannot be replayed through this protocol.
			if text := joinText(m.Content); text != "" {
				am.Content = text
			}
			for _, c := range m.ToolCalls {
				args := string(c.Arguments)
				if args == "" {
					args = "{}"
				}
				am.ToolCalls = append(am.ToolCalls, toolCall{ID: c.ID, Type: "function", Function: functionCall{Name: c.Name, Arguments: args}})
			}
			out = append(out, am)
		case llm.RoleTool:
			text := joinText(m.Content)
			if m.IsError {
				text = "Error: " + text
			}
			out = append(out, message{Role: "tool", ToolCallID: m.ToolCallID, Content: text})
			var imgs []contentPart
			for _, p := range m.Content {
				if p.Kind == llm.PartImage {
					imgs = append(imgs, imagePart(p.Image))
				}
			}
			if len(imgs) > 0 {
				pendingImages = append(pendingImages, contentPart{Type: "text", Text: "Image output of tool call " + m.ToolCallID + ":"})
				pendingImages = append(pendingImages, imgs...)
			}
		}
	}
	flush()
	return out
}

// userContent uses the plain string form when possible; some compatible
// servers accept only that for text-only messages.
func userContent(parts []llm.Part) any {
	textOnly := true
	for _, p := range parts {
		if p.Kind != llm.PartText {
			textOnly = false
		}
	}
	if textOnly {
		return joinText(parts)
	}
	var out []contentPart
	for _, p := range parts {
		switch p.Kind {
		case llm.PartText:
			out = append(out, contentPart{Type: "text", Text: p.Text})
		case llm.PartImage:
			out = append(out, imagePart(p.Image))
		}
	}
	return out
}

func imagePart(img llm.Image) contentPart {
	url := "data:" + img.MediaType + ";base64," + base64.StdEncoding.EncodeToString(img.Data)
	return contentPart{Type: "image_url", ImageURL: &imageURL{URL: url}}
}

func joinText(parts []llm.Part) string {
	var texts []string
	for _, p := range parts {
		if p.Kind == llm.PartText {
			texts = append(texts, p.Text)
		}
	}
	return strings.Join(texts, "\n")
}
