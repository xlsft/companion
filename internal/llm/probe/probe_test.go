package probe_test

import (
	"context"
	"testing"
	"time"

	"github.com/xlsft/openclick/internal/llm"
	_ "github.com/xlsft/openclick/internal/llm/anthropic"
	"github.com/xlsft/openclick/internal/llm/llmtest"
	_ "github.com/xlsft/openclick/internal/llm/openai"
	"github.com/xlsft/openclick/internal/llm/probe"
)

var textOK = llmtest.Response{Steps: []llmtest.Step{llmtest.Text("pong"), llmtest.Finish(llm.StopEnd, llm.Usage{})}}
var redOK = llmtest.Response{Steps: []llmtest.Step{llmtest.Text("It is RED."), llmtest.Finish(llm.StopEnd, llm.Usage{})}}
var toolCall = llmtest.Response{Steps: []llmtest.Step{
	llmtest.ToolStart("c1", probe.ToolName), llmtest.ToolArgs("{}"), llmtest.ToolEnd(),
	llmtest.Finish(llm.StopToolUse, llm.Usage{})}}
var answer = llmtest.Response{Steps: []llmtest.Step{llmtest.Text("The number is 42."), llmtest.Finish(llm.StopEnd, llm.Usage{})}}

func setup(t *testing.T, d llmtest.Dialect) (*llmtest.Server, llm.Provider) {
	srv := llmtest.NewServer(t, d)
	kind := llm.KindOpenAI
	if d == llmtest.Anthropic {
		kind = llm.KindAnthropic
	}
	p, err := llm.New(llm.ProviderConfig{Name: "f", Kind: kind, BaseURL: srv.BaseURL(), Model: "m",
		Timeout: 5 * time.Second, Retry: llm.RetryPolicy{MaxAttempts: 3, BaseDelay: time.Millisecond}}, "key", srv.Client())
	if err != nil {
		t.Fatal(err)
	}
	return srv, p
}

func TestProbe(t *testing.T) {
	for _, d := range []llmtest.Dialect{llmtest.OpenAI, llmtest.Anthropic} {
		t.Run(string(d), func(t *testing.T) {
			tests := []struct {
				name string
				seq  []llmtest.Response
				want map[llm.Capability]string // "" = ok, else ErrCode
				reqs int
			}{
				{"all pass", []llmtest.Response{textOK, redOK, toolCall, answer},
					map[llm.Capability]string{probe.CapText: "", probe.CapStream: "", llm.CapVision: "", llm.CapTools: ""}, 4},
				{"vision rejected", []llmtest.Response{textOK, {Status: 400, ErrorMessage: "no images"}, toolCall, answer},
					map[llm.Capability]string{probe.CapText: "", llm.CapVision: "provider", llm.CapTools: ""}, 4},
				{"tools absent", []llmtest.Response{textOK, redOK, answer},
					map[llm.Capability]string{llm.CapVision: "", llm.CapTools: "no_tool_call"}, 3},
				{"auth stops", []llmtest.Response{{Status: 401, ErrorMessage: "bad key"}},
					map[llm.Capability]string{probe.CapText: "auth", llm.CapVision: "auth", llm.CapTools: "auth"}, 1},
			}
			for _, tc := range tests {
				t.Run(tc.name, func(t *testing.T) {
					srv, p := setup(t, d)
					srv.RespondSeq(tc.seq...)
					rep := probe.Probe(context.Background(), p, nil)
					for c, code := range tc.want {
						r, ok := rep.Get(c)
						if !ok {
							t.Fatalf("no result for %s", c)
						}
						if r.OK != (code == "") || r.ErrCode != code {
							t.Errorf("%s: ok=%v code=%q, want code %q (%s)", c, r.OK, r.ErrCode, code, r.Detail)
						}
					}
					if n := len(srv.Requests()); n != tc.reqs {
						t.Errorf("requests = %d, want %d", n, tc.reqs)
					}
					if tc.name == "auth stops" {
						if r, _ := rep.Get(llm.CapVision); !r.Skipped {
							t.Error("vision should be skipped")
						}
					}
				})
			}
		})
	}
}

func TestProbeSendsFixedContent(t *testing.T) {
	srv, p := setup(t, llmtest.OpenAI)
	srv.RespondSeq(textOK, redOK, toolCall, answer)
	probe.Probe(context.Background(), p, []llm.Capability{llm.CapVision})
	reqs := srv.Requests()
	if len(reqs) != 1 {
		t.Fatalf("requests = %d, want 1 (vision only)", len(reqs))
	}
}

func TestOverridesAndStored(t *testing.T) {
	rep := probe.Report{At: time.Now(), Results: []probe.Result{
		{Capability: llm.CapVision, OK: false, ErrCode: "provider"},
		{Capability: llm.CapTools, OK: false, ErrCode: "timeout"},
	}}
	rep.ApplyOverrides(map[llm.Capability]bool{llm.CapVision: true})
	if r, _ := rep.Get(llm.CapVision); r.Override == nil || !r.Effective() {
		t.Error("override must win")
	}
	d := rep.Detect()
	if v, ok := d.Caps[llm.CapVision]; !ok || v {
		t.Errorf("vision detection = %v,%v", v, ok)
	}
	if _, ok := d.Caps[llm.CapTools]; ok {
		t.Error("transient failure must not be recorded as detection")
	}
	s := probe.Stored{Detected: &d, Overrides: map[llm.Capability]bool{llm.CapVision: true}}
	got := s.Apply(llm.Capabilities{Vision: false, Tools: true})
	if !got.Vision || !got.Tools {
		t.Errorf("Apply = %+v", got)
	}
}
