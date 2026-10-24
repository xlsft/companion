package openai

import (
	"bytes"
	"encoding/json"
	"fmt"

	"github.com/xlsft/openclick/internal/llm"
	"github.com/xlsft/openclick/internal/llm/internal/wire"
)

type chunk struct {
	Choices []struct {
		Index int `json:"index"`
		Delta struct {
			Content          *string `json:"content"`
			Refusal          *string `json:"refusal"`
			ReasoningContent string  `json:"reasoning_content"` // DeepSeek, vLLM
			Reasoning        string  `json:"reasoning"`         // OpenRouter, Ollama
			ToolCalls        []struct {
				Index    int    `json:"index"`
				ID       string `json:"id"`
				Function struct {
					Name      string `json:"name"`
					Arguments string `json:"arguments"`
				} `json:"function"`
			} `json:"tool_calls"`
		} `json:"delta"`
		FinishReason *string `json:"finish_reason"`
	} `json:"choices"`
	Usage *struct {
		PromptTokens        int `json:"prompt_tokens"`
		CompletionTokens    int `json:"completion_tokens"`
		PromptTokensDetails *struct {
			CachedTokens int `json:"cached_tokens"`
		} `json:"prompt_tokens_details"`
	} `json:"usage"`
	Error *apiError `json:"error"`
}

// decoder turns Chat Completions chunks into normalized events. Tool calls
// are numbered in order of appearance; a call is completed as soon as the
// next one starts, matching the block-by-block order of other providers.
type decoder struct {
	byIndex map[int]int // wire index -> ordinal
	calls   []*wire.ToolCall
	open    *wire.ToolCall
	stop    llm.StopReason
}

func newDecoder() *decoder { return &decoder{byIndex: map[int]int{}} }

func (d *decoder) Decode(ev wire.SSEEvent) ([]llm.Event, bool, error) {
	data := bytes.TrimSpace(ev.Data)
	if len(data) == 0 {
		return nil, false, nil
	}
	if string(data) == "[DONE]" {
		out, err := d.closeOpen(nil)
		if err != nil {
			return out, false, err
		}
		stop := d.stop
		if stop == "" {
			stop = llm.StopEnd
		}
		return append(out, llm.Completed{StopReason: stop}), true, nil
	}
	var c chunk
	if err := json.Unmarshal(data, &c); err != nil {
		return nil, false, wire.Malformed("invalid JSON in event")
	}
	if c.Error != nil {
		return nil, false, streamError(*c.Error)
	}
	var out []llm.Event
	for _, ch := range c.Choices {
		if ch.Index != 0 { // n > 1 is never requested
			continue
		}
		dl := ch.Delta
		if r := dl.ReasoningContent + dl.Reasoning; r != "" {
			out = append(out, llm.ThinkingDelta{Text: r})
		}
		if dl.Content != nil && *dl.Content != "" {
			out = append(out, llm.TextDelta{Text: *dl.Content})
		}
		if dl.Refusal != nil && *dl.Refusal != "" {
			out = append(out, llm.TextDelta{Text: *dl.Refusal})
		}
		for _, tc := range dl.ToolCalls {
			ord, seen := d.byIndex[tc.Index]
			if !seen {
				var err error
				if out, err = d.closeOpen(out); err != nil {
					return out, false, err
				}
				ord = len(d.calls)
				id := tc.ID
				if id == "" { // some local servers omit ids
					id = fmt.Sprintf("call_%d", ord)
				}
				call := &wire.ToolCall{Index: ord, ID: id, Name: tc.Function.Name}
				d.byIndex[tc.Index] = ord
				d.calls = append(d.calls, call)
				d.open = call
				out = append(out, llm.ToolCallStarted{Index: ord, ID: id, Name: call.Name})
			} else if d.open == nil || d.open.Index != ord {
				return out, false, wire.Malformed("interleaved tool call deltas")
			}
			if a := tc.Function.Arguments; a != "" {
				d.open.Args = append(d.open.Args, a...)
				out = append(out, llm.ToolCallDelta{Index: ord, ArgumentsDelta: a})
			}
		}
		if ch.FinishReason != nil && *ch.FinishReason != "" {
			d.stop = mapStop(*ch.FinishReason, len(d.calls) > 0)
			var err error
			if out, err = d.closeOpen(out); err != nil {
				return out, false, err
			}
		}
	}
	if u := c.Usage; u != nil {
		cached := 0
		if u.PromptTokensDetails != nil {
			cached = u.PromptTokensDetails.CachedTokens
		}
		out = append(out, llm.Usage{InputTokens: u.PromptTokens - cached, OutputTokens: u.CompletionTokens, CacheReadTokens: cached})
	}
	return out, false, nil
}

func (d *decoder) closeOpen(out []llm.Event) ([]llm.Event, error) {
	if d.open == nil {
		return out, nil
	}
	done, err := d.open.Complete()
	d.open = nil
	if err != nil {
		return out, err
	}
	return append(out, done), nil
}

func mapStop(reason string, hadToolCalls bool) llm.StopReason {
	switch reason {
	case "stop":
		// Several compatible servers report "stop" after tool calls.
		if hadToolCalls {
			return llm.StopToolUse
		}
		return llm.StopEnd
	case "length":
		return llm.StopMaxTokens
	case "tool_calls", "function_call":
		return llm.StopToolUse
	case "content_filter":
		return llm.StopContentFilter
	}
	return llm.StopOther
}
