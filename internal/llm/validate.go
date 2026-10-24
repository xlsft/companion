package llm

import (
	"encoding/json"
	"regexp"
)

// Both wire protocols restrict tool names to this alphabet.
var toolNameRE = regexp.MustCompile(`^[a-zA-Z0-9_-]{1,64}$`)

// CheckRequest validates req and that caps cover everything it needs.
// Adapters call it before sending anything, so an unsupported request
// fails locally with *ErrUnsupportedCapability instead of being degraded.
func CheckRequest(caps Capabilities, req ChatRequest) error {
	if err := validateRequest(req); err != nil {
		return err
	}
	for _, cap := range requiredCapabilities(req) {
		if !caps.Has(cap) {
			return &ErrUnsupportedCapability{Cap: cap}
		}
	}
	return nil
}

func requiredCapabilities(req ChatRequest) []Capability {
	var caps []Capability
	if hasImages(req) {
		caps = append(caps, CapVision)
	}
	if len(req.Tools) > 0 {
		caps = append(caps, CapTools)
	}
	if req.Policy.Thinking != ThinkingDefault || req.Policy.Effort != "" {
		caps = append(caps, CapThinking)
	}
	if req.ResponseSchema != nil {
		caps = append(caps, CapResponseSchema)
	}
	return caps
}

func hasImages(req ChatRequest) bool {
	if len(req.Attachments) > 0 {
		return true
	}
	for _, m := range req.Messages {
		for _, p := range m.Content {
			if p.Kind == PartImage {
				return true
			}
		}
	}
	return false
}

func validateRequest(req ChatRequest) error {
	if len(req.Messages) == 0 {
		return invalidRequest("no messages")
	}
	lastUser := -1
	seenNonSystem := false
	for i, m := range req.Messages {
		if m.Role == RoleSystem {
			if seenNonSystem {
				return invalidRequest("message %d: system messages must come first", i)
			}
		} else {
			seenNonSystem = true
		}
		if m.Role == RoleUser {
			lastUser = i
		}
		if err := validateMessage(m); err != nil {
			return invalidRequest("message %d: %v", i, err)
		}
	}
	if len(req.Attachments) > 0 && lastUser < 0 {
		return invalidRequest("attachments need a user message")
	}
	for i, img := range req.Attachments {
		if err := validateImage(img); err != nil {
			return invalidRequest("attachment %d: %v", i, err)
		}
	}
	names := map[string]bool{}
	for i, t := range req.Tools {
		if !toolNameRE.MatchString(t.Name) {
			return invalidRequest("tool %d: invalid name", i)
		}
		if names[t.Name] {
			return invalidRequest("tool %d: duplicate name", i)
		}
		names[t.Name] = true
		if len(t.Schema) > 0 && !isJSONObject(t.Schema) {
			return invalidRequest("tool %d: schema is not a JSON object", i)
		}
	}
	p := req.Policy
	switch p.ToolChoice {
	case ToolChoiceAuto, ToolChoiceNone:
	case ToolChoiceRequired:
		if len(req.Tools) == 0 {
			return invalidRequest("tool choice required without tools")
		}
	default:
		return invalidRequest("unknown tool choice")
	}
	switch p.Thinking {
	case ThinkingDefault, ThinkingAdaptive:
	case ThinkingBudget:
		if p.ThinkingBudget <= 0 {
			return invalidRequest("thinking budget must be positive")
		}
	default:
		return invalidRequest("unknown thinking mode")
	}
	if p.MaxOutputTokens < 0 {
		return invalidRequest("negative max output tokens")
	}
	if p.Temperature != nil && (*p.Temperature < 0 || *p.Temperature > 2) {
		return invalidRequest("temperature out of range")
	}
	if s := req.ResponseSchema; s != nil && !isJSONObject(s.Schema) {
		return invalidRequest("response schema is not a JSON object")
	}
	return nil
}

func validateMessage(m Message) error {
	switch m.Role {
	case RoleSystem, RoleUser, RoleAssistant, RoleTool:
	default:
		return errorString("unknown role")
	}
	if m.Role != RoleAssistant && len(m.ToolCalls) > 0 {
		return errorString("only assistant messages carry tool calls")
	}
	if (m.Role == RoleTool) != (m.ToolCallID != "") {
		return errorString("tool call id must be set exactly on tool messages")
	}
	if m.Role != RoleTool && m.IsError {
		return errorString("only tool messages can be errors")
	}
	if len(m.Content) == 0 && len(m.ToolCalls) == 0 && m.Role != RoleTool {
		return errorString("empty message")
	}
	for _, p := range m.Content {
		switch p.Kind {
		case PartText:
		case PartImage:
			if m.Role == RoleSystem || m.Role == RoleAssistant {
				return errorString("images are only allowed in user and tool messages")
			}
			if err := validateImage(p.Image); err != nil {
				return err
			}
		case PartThinking:
			if m.Role != RoleAssistant {
				return errorString("thinking is only allowed in assistant messages")
			}
		default:
			return errorString("unknown part kind")
		}
	}
	for _, c := range m.ToolCalls {
		if c.ID == "" || !toolNameRE.MatchString(c.Name) {
			return errorString("tool call needs an id and a valid name")
		}
		if len(c.Arguments) > 0 && !isJSONObject(c.Arguments) {
			return errorString("tool call arguments are not a JSON object")
		}
	}
	return nil
}

func validateImage(img Image) error {
	if img.MediaType != MediaPNG && img.MediaType != MediaJPEG {
		return errorString("unsupported image media type")
	}
	if len(img.Data) == 0 {
		return errorString("empty image")
	}
	return nil
}

func isJSONObject(b []byte) bool {
	var v map[string]json.RawMessage
	return json.Unmarshal(b, &v) == nil && v != nil
}

type errorString string

func (e errorString) Error() string { return string(e) }
