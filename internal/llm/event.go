package llm

import "encoding/json"

// Event is one normalized provider event: TextDelta, ThinkingDelta,
// ToolCallStarted, ToolCallDelta, ToolCallCompleted, Usage, Completed or
// Failed.
type Event interface{ isEvent() }

// TextDelta is a fragment of the visible answer.
type TextDelta struct{ Text string }

// ThinkingDelta is a fragment of reasoning. Signature, when set, belongs
// to the current thinking block and must be replayed with it.
type ThinkingDelta struct {
	Text      string
	Signature string
}

// ToolCallStarted opens tool call Index (0-based, in response order).
type ToolCallStarted struct {
	Index int
	ID    string
	Name  string
}

// ToolCallDelta is a fragment of the call's JSON arguments.
type ToolCallDelta struct {
	Index          int
	ArgumentsDelta string
}

// ToolCallCompleted carries the complete, valid JSON arguments.
type ToolCallCompleted struct {
	Index     int
	ID        string
	Name      string
	Arguments json.RawMessage
}

// Usage reports token accounting. InputTokens excludes cache reads and
// writes, which are reported separately.
type Usage struct {
	InputTokens      int
	OutputTokens     int
	CacheReadTokens  int
	CacheWriteTokens int
}

// StopReason says why generation ended.
type StopReason string

const (
	StopEnd           StopReason = "end"            // natural end or stop sequence
	StopMaxTokens     StopReason = "max_tokens"     // output budget exhausted
	StopToolUse       StopReason = "tool_use"       // model waits for tool results
	StopContentFilter StopReason = "content_filter" // provider refused or filtered
	StopOther         StopReason = "other"
)

// Completed ends a successful stream.
type Completed struct{ StopReason StopReason }

// Failed ends a stream with a normalized error (see errors.go).
type Failed struct{ Err error }

func (TextDelta) isEvent()         {}
func (ThinkingDelta) isEvent()     {}
func (ToolCallStarted) isEvent()   {}
func (ToolCallDelta) isEvent()     {}
func (ToolCallCompleted) isEvent() {}
func (Usage) isEvent()             {}
func (Completed) isEvent()         {}
func (Failed) isEvent()            {}
