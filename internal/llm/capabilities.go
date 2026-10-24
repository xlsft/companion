package llm

// Capability is a model feature a request may depend on.
type Capability string

const (
	CapVision         Capability = "vision"          // image input
	CapTools          Capability = "tools"           // function calling
	CapThinking       Capability = "thinking"        // configurable reasoning
	CapResponseSchema Capability = "response_schema" // JSON-schema constrained output
)

// AllCapabilities lists every known capability.
var AllCapabilities = []Capability{CapVision, CapTools, CapThinking, CapResponseSchema}

// Capabilities is what a provider, as configured, can do.
type Capabilities struct {
	Vision         bool
	Tools          bool
	Thinking       bool
	ResponseSchema bool
}

// Has reports whether c is supported.
func (c Capabilities) Has(cap Capability) bool {
	switch cap {
	case CapVision:
		return c.Vision
	case CapTools:
		return c.Tools
	case CapThinking:
		return c.Thinking
	case CapResponseSchema:
		return c.ResponseSchema
	}
	return false
}

// With returns c with the manual overrides applied. Users set these for
// compatible servers whose model lacks (or has) a feature the adapter
// default assumes otherwise.
func (c Capabilities) With(overrides map[Capability]bool) Capabilities {
	for cap, on := range overrides {
		switch cap {
		case CapVision:
			c.Vision = on
		case CapTools:
			c.Tools = on
		case CapThinking:
			c.Thinking = on
		case CapResponseSchema:
			c.ResponseSchema = on
		}
	}
	return c
}

func knownCapability(c Capability) bool {
	for _, k := range AllCapabilities {
		if k == c {
			return true
		}
	}
	return false
}
