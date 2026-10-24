package anthropic

import (
	"bytes"
	"encoding/json"

	"github.com/xlsft/companion/internal/llm"
	"github.com/xlsft/companion/internal/llm/internal/wire"
)

type usage struct {
	InputTokens              int `json:"input_tokens"`
	OutputTokens             int `json:"output_tokens"`
	CacheCreationInputTokens int `json:"cache_creation_input_tokens"`
	CacheReadInputTokens     int `json:"cache_read_input_tokens"`
}

type event struct {
	Type    string `json:"type"`
	Index   int    `json:"index"`
	Message struct {
		Usage usage `json:"usage"`
	} `json:"message"`
	ContentBlock struct {
		Type     string `json:"type"`
		ID       string `json:"id"`
		Name     string `json:"name"`
		Text     string `json:"text"`
		Thinking string `json:"thinking"`
	} `json:"content_block"`
	Delta struct {
		Type        string `json:"type"`
		Text        string `json:"text"`
		PartialJSON string `json:"partial_json"`
		Thinking    string `json:"thinking"`
		Signature   string `json:"signature"`
		StopReason  string `json:"stop_reason"`
	} `json:"delta"`
	Usage *usage    `json:"usage"`
	Error *apiError `json:"error"`
}

type block struct {
	kind string
	call *wire.ToolCall
}

// decoder turns Messages API events into normalized events. Tool calls
// are numbered by their order among tool_use blocks, not by content-block
// index, so the numbering matches other providers.
type decoder struct {
	blocks   map[int]*block
	calls    int
	usage    llm.Usage
	gotUsage bool
	stop     llm.StopReason
}

func newDecoder() *decoder { return &decoder{blocks: map[int]*block{}} }

func (d *decoder) Decode(sse wire.SSEEvent) ([]llm.Event, bool, error) {
	data := bytes.TrimSpace(sse.Data)
	if len(data) == 0 {
		return nil, false, nil
	}
	var ev event
	if err := json.Unmarshal(data, &ev); err != nil {
		return nil, false, wire.Malformed("invalid JSON in event")
	}
	typ := ev.Type
	if typ == "" {
		typ = sse.Event
	}
	switch typ {
	case "message_start":
		u := ev.Message.Usage
		d.usage = llm.Usage{InputTokens: u.InputTokens, OutputTokens: u.OutputTokens, CacheReadTokens: u.CacheReadInputTokens, CacheWriteTokens: u.CacheCreationInputTokens}
		d.gotUsage = true
	case "content_block_start":
		cb := ev.ContentBlock
		b := &block{kind: cb.Type}
		d.blocks[ev.Index] = b
		switch cb.Type {
		case "text":
			if cb.Text != "" {
				return []llm.Event{llm.TextDelta{Text: cb.Text}}, false, nil
			}
		case "thinking":
			if cb.Thinking != "" {
				return []llm.Event{llm.ThinkingDelta{Text: cb.Thinking}}, false, nil
			}
		case "tool_use":
			b.call = &wire.ToolCall{Index: d.calls, ID: cb.ID, Name: cb.Name}
			d.calls++
			return []llm.Event{llm.ToolCallStarted{Index: b.call.Index, ID: cb.ID, Name: cb.Name}}, false, nil
		}
	case "content_block_delta":
		b := d.blocks[ev.Index]
		if b == nil {
			return nil, false, wire.Malformed("delta for unknown content block")
		}
		dl := ev.Delta
		switch dl.Type {
		case "text_delta":
			if dl.Text != "" {
				return []llm.Event{llm.TextDelta{Text: dl.Text}}, false, nil
			}
		case "thinking_delta":
			if dl.Thinking != "" {
				return []llm.Event{llm.ThinkingDelta{Text: dl.Thinking}}, false, nil
			}
		case "signature_delta":
			return []llm.Event{llm.ThinkingDelta{Signature: dl.Signature}}, false, nil
		case "input_json_delta":
			if b.call == nil {
				return nil, false, wire.Malformed("tool input delta outside a tool_use block")
			}
			if dl.PartialJSON != "" {
				b.call.Args = append(b.call.Args, dl.PartialJSON...)
				return []llm.Event{llm.ToolCallDelta{Index: b.call.Index, ArgumentsDelta: dl.PartialJSON}}, false, nil
			}
		}
	case "content_block_stop":
		b := d.blocks[ev.Index]
		delete(d.blocks, ev.Index)
		if b != nil && b.call != nil {
			done, err := b.call.Complete()
			if err != nil {
				return nil, false, err
			}
			return []llm.Event{done}, false, nil
		}
	case "message_delta":
		if ev.Delta.StopReason != "" {
			d.stop = mapStop(ev.Delta.StopReason)
		}
		if u := ev.Usage; u != nil {
			// Counts here are cumulative; zero means "not reported".
			d.usage.OutputTokens = u.OutputTokens
			if u.InputTokens > 0 {
				d.usage.InputTokens = u.InputTokens
			}
			if u.CacheReadInputTokens > 0 {
				d.usage.CacheReadTokens = u.CacheReadInputTokens
			}
			if u.CacheCreationInputTokens > 0 {
				d.usage.CacheWriteTokens = u.CacheCreationInputTokens
			}
			d.gotUsage = true
		}
	case "message_stop":
		var out []llm.Event
		if d.gotUsage {
			out = append(out, d.usage)
		}
		stop := d.stop
		if stop == "" {
			stop = llm.StopEnd
		}
		return append(out, llm.Completed{StopReason: stop}), true, nil
	case "error":
		var e apiError
		if ev.Error != nil {
			e = *ev.Error
		}
		return nil, false, streamError(e)
	}
	// "ping" and event types added after this adapter are ignored.
	return nil, false, nil
}

func mapStop(reason string) llm.StopReason {
	switch reason {
	case "end_turn", "stop_sequence":
		return llm.StopEnd
	case "max_tokens", "model_context_window_exceeded":
		return llm.StopMaxTokens
	case "tool_use":
		return llm.StopToolUse
	case "refusal":
		return llm.StopContentFilter
	}
	return llm.StopOther
}
