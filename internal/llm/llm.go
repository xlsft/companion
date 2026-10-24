// Package llm is the provider-neutral LLM gateway (PRD §9.4). Adapters for
// concrete wire protocols live in subpackages (openai, anthropic) and
// register themselves with Register; callers build providers with New.
//
// A provider never compensates for a missing capability by switching to
// another provider or silently dropping input: requests that need a
// capability the provider lacks fail with *ErrUnsupportedCapability.
package llm

import (
	"context"
	"encoding/json"

	"github.com/xlsft/companion/pkg/contracts"
)

// Provider streams one model response per request.
type Provider interface {
	// Stream sends req and returns once the provider accepted it (response
	// headers received). Failures before that point are returned here and
	// are the only ones the retry policy may retry; later failures arrive
	// as a Failed event.
	Stream(ctx context.Context, req ChatRequest) (EventStream, error)
	Capabilities() Capabilities
}

// EventStream yields normalized events of one response. The last event is
// always Completed or Failed; after it Recv returns io.EOF. Cancelling the
// context passed to Stream ends the stream with Failed promptly.
type EventStream interface {
	Recv() (Event, error)
	// Close releases the connection. It is safe to call at any time, from
	// any goroutine and more than once; reading the stream to the end
	// closes it as well. After Close, Recv returns io.EOF.
	Close() error
}

// Role of a message author.
type Role string

const (
	RoleSystem    Role = "system"
	RoleUser      Role = "user"
	RoleAssistant Role = "assistant"
	RoleTool      Role = "tool"
)

// PartKind is the type of one content part.
type PartKind string

const (
	PartText     PartKind = "text"
	PartImage    PartKind = "image"
	PartThinking PartKind = "thinking" // assistant reasoning replayed to the provider
)

// Supported image media types.
const (
	MediaPNG  = "image/png"
	MediaJPEG = "image/jpeg"
)

// Image is an inline image; Data holds the encoded file bytes.
type Image struct {
	MediaType string
	Data      []byte
}

// Part is one piece of message content.
type Part struct {
	Kind  PartKind
	Text  string // PartText, PartThinking
	Image Image  // PartImage
	// Signature authenticates a PartThinking block; providers that issue
	// signatures reject replayed reasoning without it.
	Signature string
}

func Text(s string) Part             { return Part{Kind: PartText, Text: s} }
func ImagePart(img Image) Part       { return Part{Kind: PartImage, Image: img} }
func Thinking(text, sig string) Part { return Part{Kind: PartThinking, Text: text, Signature: sig} }

// ToolCall is a tool invocation requested by the model.
type ToolCall struct {
	ID        string
	Name      string
	Arguments json.RawMessage // a JSON object
}

// Message is one conversation turn.
type Message struct {
	Role    Role
	Content []Part
	// ToolCalls are the calls an assistant message made.
	ToolCalls []ToolCall
	// ToolCallID links a RoleTool message to the call it answers.
	ToolCallID string
	// IsError marks a RoleTool result as a failed tool execution.
	IsError bool
}

// Tool is a function the model may call.
type Tool struct {
	Name        string
	Description string
	Schema      json.RawMessage // JSON Schema of the arguments object
}

// ResponseSchema asks the model to answer with JSON matching Schema.
type ResponseSchema struct {
	Name   string
	Schema json.RawMessage
}

// ThinkingMode selects provider-side reasoning.
type ThinkingMode string

const (
	ThinkingDefault  ThinkingMode = ""         // whatever the provider does by default
	ThinkingAdaptive ThinkingMode = "adaptive" // model decides how much to think
	ThinkingBudget   ThinkingMode = "budget"   // fixed budget, for older models
)

// ToolChoice constrains tool use.
type ToolChoice string

const (
	ToolChoiceAuto     ToolChoice = ""
	ToolChoiceNone     ToolChoice = "none"
	ToolChoiceRequired ToolChoice = "required"
)

// ModelPolicy holds per-request generation settings. Zero values mean
// "do not send", so providers apply their own defaults.
type ModelPolicy struct {
	MaxOutputTokens int
	Temperature     *float64
	Thinking        ThinkingMode
	ThinkingBudget  int    // tokens, for ThinkingBudget
	Effort          string // e.g. "low", "medium", "high"; provider-defined
	ToolChoice      ToolChoice
}

// ChatRequest is one model call.
type ChatRequest struct {
	Messages []Message
	// Attachments are images appended to the last user message, e.g. the
	// screen capture of the current turn.
	Attachments    []Image
	Tools          []Tool
	ResponseSchema *ResponseSchema
	Policy         ModelPolicy
	// Session identifies the interaction for correlation in diagnostics.
	// It is never sent to the provider.
	Session contracts.SessionRef
}

// WithAttachments returns the messages with req.Attachments merged into
// the last user message, which is how adapters send them.
func (req ChatRequest) WithAttachments() []Message {
	if len(req.Attachments) == 0 {
		return req.Messages
	}
	msgs := append([]Message(nil), req.Messages...)
	for i := len(msgs) - 1; i >= 0; i-- {
		if msgs[i].Role != RoleUser {
			continue
		}
		parts := append([]Part(nil), msgs[i].Content...)
		for _, img := range req.Attachments {
			parts = append(parts, ImagePart(img))
		}
		msgs[i].Content = parts
		return msgs
	}
	return msgs
}
