// Package probe detects what a configured provider really supports by
// sending a few small fixed requests (OC-23, FR-BYOK-004, FR-ONB-008).
//
// The requests are constants of this package: no user content is ever
// sent, and nothing from a response is logged or kept besides pass/fail,
// the normalized error kind and a short technical detail.
package probe

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"image"
	"image/color"
	"image/png"
	"io"
	"strings"
	"time"

	"github.com/xlsft/companion/internal/llm"
)

// Pseudo-capabilities checked besides llm.CapVision and llm.CapTools.
// They are not part of llm.Capabilities (every provider must stream text).
const (
	CapText   llm.Capability = "text"
	CapStream llm.Capability = "stream"
)

// DefaultWant is what the onboarding wizard checks.
var DefaultWant = []llm.Capability{CapText, CapStream, llm.CapVision, llm.CapTools}

// DefaultTimeout bounds each check on its own.
const DefaultTimeout = 30 * time.Second

// Result is the outcome of one check.
type Result struct {
	Capability llm.Capability `json:"capability"`
	OK         bool           `json:"ok"`
	// Skipped: the check did not run because an earlier one failed in a way
	// that makes further requests pointless (auth, network, rate limit).
	Skipped bool          `json:"skipped,omitempty"`
	Latency time.Duration `json:"latency_ns,omitempty"`
	// ErrCode is the normalized llm error kind (llm.ErrorKind), or one of
	// "no_tool_call", "bad_tool_args", "wrong_answer", "no_text", "no_stream".
	ErrCode string `json:"err_code,omitempty"`
	// Detail is technical and log-safe: status codes and capability names.
	Detail string `json:"detail,omitempty"`
	// Override is the user's manual setting for this capability, if any.
	// It wins over OK for Effective.
	Override *bool `json:"override,omitempty"`
}

// Effective is the value that applies: the manual override, else the
// detected result.
func (r Result) Effective() bool {
	if r.Override != nil {
		return *r.Override
	}
	return r.OK
}

// Report is the outcome of one Probe run.
type Report struct {
	At      time.Time `json:"at"`
	Results []Result  `json:"results"`
}

// Get returns the result for c.
func (r Report) Get(c llm.Capability) (Result, bool) {
	for _, res := range r.Results {
		if res.Capability == c {
			return res, true
		}
	}
	return Result{}, false
}

// ApplyOverrides marks the results of overridden capabilities so the UI
// can show "set manually" next to what was detected.
func (r *Report) ApplyOverrides(o map[llm.Capability]bool) {
	for i := range r.Results {
		if v, ok := o[r.Results[i].Capability]; ok {
			v := v
			r.Results[i].Override = &v
		}
	}
}

// Options tunes a run. The zero value is fine.
type Options struct {
	Timeout time.Duration // per check
	Now     func() time.Time
}

// Probe runs the checks in want (nil means DefaultWant) against p. Callers
// should pass a provider built without retries, so a wrong key costs one
// request instead of a retry storm.
func Probe(ctx context.Context, p llm.Provider, want []llm.Capability) Report {
	return ProbeWith(ctx, p, want, Options{})
}

// ProbeWith is Probe with options.
func ProbeWith(ctx context.Context, p llm.Provider, want []llm.Capability, opt Options) Report {
	if len(want) == 0 {
		want = DefaultWant
	}
	if opt.Timeout <= 0 {
		opt.Timeout = DefaultTimeout
	}
	now := time.Now
	if opt.Now != nil {
		now = opt.Now
	}
	wants := map[llm.Capability]bool{}
	for _, c := range want {
		wants[c] = true
	}
	rep := Report{At: now().UTC()}
	var stopped *Result // the failure that ended the run

	// run executes one request and reports it under every capability it
	// answers (text and stream share a request).
	run := func(caps []llm.Capability, f func(context.Context) Result) {
		var res Result
		if stopped != nil {
			res = Result{Skipped: true, ErrCode: stopped.ErrCode,
				Detail: "skipped after " + string(stopped.Capability) + " check failed: " + stopped.ErrCode}
		} else {
			cctx, cancel := context.WithTimeout(ctx, opt.Timeout)
			start := time.Now()
			res = f(cctx)
			res.Latency = time.Since(start)
			cancel()
		}
		for _, c := range caps {
			r := res
			r.Capability = c
			rep.Results = append(rep.Results, r)
		}
		if stopped == nil && !res.OK && stopsRun(res.ErrCode) {
			stopped = &rep.Results[len(rep.Results)-len(caps)]
		}
	}

	var textCaps []llm.Capability
	for _, c := range []llm.Capability{CapText, CapStream} {
		if wants[c] {
			textCaps = append(textCaps, c)
		}
	}
	if len(textCaps) > 0 {
		run(textCaps, func(cctx context.Context) Result { return checkText(cctx, p) })
	}
	if wants[llm.CapVision] {
		run([]llm.Capability{llm.CapVision}, func(cctx context.Context) Result { return checkVision(cctx, p) })
	}
	if wants[llm.CapTools] {
		run([]llm.Capability{llm.CapTools}, func(cctx context.Context) Result { return checkTools(cctx, p) })
	}
	return rep
}

// stopsRun: failures after which more requests are wasted or harmful.
func stopsRun(code string) bool {
	switch code {
	case "auth", "network", "rate_limit", "canceled":
		return true
	}
	return false
}

// collected is what one streamed answer boiled down to.
type collected struct {
	text      strings.Builder
	deltas    int
	tools     []llm.ToolCallCompleted
	completed bool
}

func collect(ctx context.Context, p llm.Provider, req llm.ChatRequest) (*collected, error) {
	s, err := p.Stream(ctx, req)
	if err != nil {
		return nil, err
	}
	defer s.Close()
	c := &collected{}
	for {
		ev, err := s.Recv()
		if errors.Is(err, io.EOF) {
			if !c.completed {
				return c, fmt.Errorf("%w: ended without completion", llm.ErrMalformedStream)
			}
			return c, nil
		}
		if err != nil {
			return c, err
		}
		switch e := ev.(type) {
		case llm.TextDelta:
			c.deltas++
			c.text.WriteString(e.Text)
		case llm.ToolCallCompleted:
			c.tools = append(c.tools, e)
		case llm.Completed:
			c.completed = true
		case llm.Failed:
			return c, e.Err
		}
	}
}

// fail keeps the error text as Detail: llm errors are built from status
// codes and provider error codes only (see llm/errors.go), so it is
// log-safe.
func fail(err error) Result {
	return Result{ErrCode: llm.ErrorKind(err), Detail: err.Error()}
}

func bad(code, detail string) Result { return Result{ErrCode: code, Detail: detail} }

const textPrompt = "Reply with the single word: pong"

func checkText(ctx context.Context, p llm.Provider) Result {
	c, err := collect(ctx, p, llm.ChatRequest{
		Messages: []llm.Message{{Role: llm.RoleUser, Content: []llm.Part{llm.Text(textPrompt)}}},
		Policy:   llm.ModelPolicy{MaxOutputTokens: 256},
	})
	switch {
	case err != nil:
		return fail(err)
	case c.deltas < 1:
		return bad("no_text", "stream completed without text")
	}
	return Result{OK: true}
}

func checkVision(ctx context.Context, p llm.Provider) Result {
	c, err := collect(ctx, p, llm.ChatRequest{
		Messages: []llm.Message{{Role: llm.RoleUser, Content: []llm.Part{
			llm.Text("What colour is the square in this image? Answer with one word."),
		}}},
		Attachments: []llm.Image{{MediaType: llm.MediaPNG, Data: TestImage()}},
		Policy:      llm.ModelPolicy{MaxOutputTokens: 256},
	})
	switch {
	case err != nil:
		return fail(err)
	case !strings.Contains(strings.ToLower(c.text.String()), "red"):
		return bad("wrong_answer", "reply did not name the expected colour")
	}
	return Result{OK: true}
}

// ToolName is the only tool the probe offers.
const ToolName = "get_secret_number"

func checkTools(ctx context.Context, p llm.Provider) Result {
	tool := llm.Tool{
		Name:        ToolName,
		Description: "Returns the secret number. Takes no arguments.",
		Schema:      json.RawMessage(`{"type":"object","properties":{},"additionalProperties":false}`),
	}
	user := llm.Message{Role: llm.RoleUser, Content: []llm.Part{llm.Text("Call the " + ToolName + " tool, then tell me the number.")}}
	c, err := collect(ctx, p, llm.ChatRequest{
		Messages: []llm.Message{user},
		Tools:    []llm.Tool{tool},
		Policy:   llm.ModelPolicy{MaxOutputTokens: 256},
	})
	if err != nil {
		return fail(err)
	}
	if len(c.tools) == 0 {
		return bad("no_tool_call", "model answered without calling the tool")
	}
	call := c.tools[0]
	var args map[string]any
	if call.Name != ToolName || json.Unmarshal(call.Arguments, &args) != nil {
		return bad("bad_tool_args", "tool call has a wrong name or invalid JSON arguments")
	}
	id := call.ID
	if id == "" {
		id = "call_probe"
	}
	c, err = collect(ctx, p, llm.ChatRequest{
		Messages: []llm.Message{
			user,
			{Role: llm.RoleAssistant, ToolCalls: []llm.ToolCall{{ID: id, Name: ToolName, Arguments: call.Arguments}}},
			{Role: llm.RoleTool, ToolCallID: id, Content: []llm.Part{llm.Text(`{"number": 42}`)}},
		},
		Tools:  []llm.Tool{tool},
		Policy: llm.ModelPolicy{MaxOutputTokens: 256},
	})
	switch {
	case err != nil:
		return fail(err)
	case c.deltas < 1:
		return bad("no_text", "no final answer after the tool result")
	}
	return Result{OK: true}
}

// TestImage returns a 64x64 PNG with a red square on white.
func TestImage() []byte {
	img := image.NewRGBA(image.Rect(0, 0, 64, 64))
	white, red := color.RGBA{255, 255, 255, 255}, color.RGBA{220, 0, 0, 255}
	for y := 0; y < 64; y++ {
		for x := 0; x < 64; x++ {
			if x >= 12 && x < 52 && y >= 12 && y < 52 {
				img.SetRGBA(x, y, red)
			} else {
				img.SetRGBA(x, y, white)
			}
		}
	}
	var buf bytes.Buffer
	if err := png.Encode(&buf, img); err != nil {
		panic(err) // encoding to memory cannot fail
	}
	return buf.Bytes()
}
