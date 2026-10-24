package talk

import (
	"context"
	"encoding/json"
	"strings"
	"sync"
	"testing"

	"github.com/xlsft/companion/internal/llm"
	"github.com/xlsft/companion/internal/overlay"
	"github.com/xlsft/companion/internal/session"
	"github.com/xlsft/companion/internal/walkthrough"
	"github.com/xlsft/companion/pkg/contracts"
)

type fakeGuide struct {
	mu      sync.Mutex
	view    walkthrough.View
	started []walkthrough.Plan
	capture contracts.CaptureID
	cmds    []walkthrough.Command
	hushes  int
	stops   int
	// order records Start relative to speech.
	order *[]string
}

func (g *fakeGuide) View() walkthrough.View { g.mu.Lock(); defer g.mu.Unlock(); return g.view }
func (g *fakeGuide) Start(_ context.Context, p walkthrough.Plan, c contracts.CaptureID) error {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.started, g.capture = append(g.started, p), c
	if g.order != nil {
		*g.order = append(*g.order, "start")
	}
	return nil
}
func (g *fakeGuide) Do(_ context.Context, c walkthrough.Command) error {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.cmds = append(g.cmds, c)
	return nil
}
func (g *fakeGuide) Hush()                { g.mu.Lock(); g.hushes++; g.mu.Unlock() }
func (g *fakeGuide) Stop(context.Context) { g.mu.Lock(); g.stops++; g.mu.Unlock() }

// orderSpeaker records speech into a shared order.
type orderSpeaker struct{ order *[]string }

func (s orderSpeaker) Say(_ context.Context, text string, started func()) error {
	started()
	*s.order = append(*s.order, "say:"+text)
	return nil
}

const threeSteps = `{"goal":"Export as PNG","steps":[
 {"title":"Select the frame","say":"Click the frame.","shapes":[{"kind":"ring","points":[[0.3,0.4]]}]},
 {"title":"Open Export","say":"Open the Export panel."},
 {"title":"Export","say":"Press Export."}]}`

func TestReasonStartsWalkthrough(t *testing.T) {
	tests := []struct {
		name    string
		args    string
		started bool
	}{
		{"valid plan", threeSteps, true},
		{"invalid plan goes back to the model", `{"goal":"x","steps":[]}`, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			p := &scriptedProvider{rounds: [][]llm.Event{
				{llm.TextDelta{Text: "Let's do it step by step."}, llm.ToolCallCompleted{ID: "w1", Name: walkthrough.ToolStart, Arguments: json.RawMessage(tt.args)}, llm.Completed{}},
				{llm.TextDelta{Text: "Sorry."}, llm.Completed{}},
			}}
			var order []string
			g := &fakeGuide{order: &order}
			o := &Orchestrator{Provider: func(context.Context) (llm.Provider, error) { return p, nil },
				Overlay: &fakeOverlay{}, Guide: g, Speaker: orderSpeaker{&order}}
			_, err := o.Reason(context.Background(), ref, session.Request{Text: "how do I export?", Captures: []*contracts.Capture{screenCapture()}}, (&events{}).emit)
			if err != nil {
				t.Fatal(err)
			}
			var offered bool
			for _, tl := range p.reqs[0].Tools {
				offered = offered || tl.Name == walkthrough.ToolStart
			}
			if !offered {
				t.Fatal("start_walkthrough not offered")
			}
			if got := len(g.started) == 1; got != tt.started {
				t.Fatalf("started %v, want %v", g.started, tt.started)
			}
			if !tt.started {
				if len(p.reqs) != 2 {
					t.Fatalf("model rounds %d, want the error round too", len(p.reqs))
				}
				return
			}
			if len(p.reqs) != 1 {
				t.Errorf("model asked %d times; the steps speak for themselves after the plan", len(p.reqs))
			}
			if g.capture != "cap1" || len(g.started[0].Steps) != 3 {
				t.Errorf("started %+v on %q", g.started[0], g.capture)
			}
			if strings.Join(order, ",") != "say:Let's do it step by step.,start" {
				t.Errorf("order %v: the walkthrough must start after the answer was spoken", order)
			}
		})
	}
}

func TestReasonRoutesIntents(t *testing.T) {
	tests := []struct {
		name   string
		text   string
		active bool
		cmd    walkthrough.Command // "" when the model is asked
	}{
		{"next while running", "Дальше.", true, walkthrough.CmdNext},
		{"stop while paused", "stop", true, walkthrough.CmdStop},
		{"question while running", "а где кнопка экспорта?", true, ""},
		{"command without a walkthrough", "дальше", false, ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			p := &scriptedProvider{}
			g := &fakeGuide{}
			if tt.active {
				g.view = walkthrough.View{Status: walkthrough.StatusRunning, Goal: "g", Total: 2, Title: "t"}
			}
			o := &Orchestrator{Provider: func(context.Context) (llm.Provider, error) { return p, nil }, Guide: g}
			shot := screenCapture()
			if _, err := o.Reason(context.Background(), ref, session.Request{Text: tt.text, Captures: []*contracts.Capture{shot}}, (&events{}).emit); err != nil {
				t.Fatal(err)
			}
			if shot.Image != nil {
				t.Error("capture not released")
			}
			if tt.cmd != "" {
				if len(g.cmds) != 1 || g.cmds[0] != tt.cmd || len(p.reqs) != 0 {
					t.Fatalf("cmds %v, model rounds %d", g.cmds, len(p.reqs))
				}
				return
			}
			if len(g.cmds) != 0 || len(p.reqs) != 1 || g.hushes != 1 {
				t.Fatalf("cmds %v, model rounds %d, hushes %d", g.cmds, len(p.reqs), g.hushes)
			}
			if sys := p.reqs[0].Messages[0].Content[0].Text; tt.active != strings.Contains(sys, "walkthrough is in progress") {
				t.Errorf("system prompt mentions the walkthrough: %v, want %v", !tt.active, tt.active)
			}
		})
	}
}

type fakeCapturer struct{ n int }

func (f *fakeCapturer) Capture(context.Context, contracts.CaptureRequest) ([]*contracts.Capture, error) {
	f.n++
	c := screenCapture()
	c.Meta.ID = contracts.CaptureID("fresh")
	return []*contracts.Capture{c}, nil
}

func TestPresenterShow(t *testing.T) {
	stepRef := contracts.SessionRef{ID: "walkthrough-w", Generation: 2}
	step := walkthrough.Step{Title: "Open Export", Say: "Open the Export panel.", Shapes: json.RawMessage(`[{"kind":"ring","points":[[0.3,0.4]]}]`)}
	tests := []struct {
		name     string
		capture  contracts.CaptureID
		rounds   [][]llm.Event
		captures int
		drawnOn  contracts.CaptureID
		draws    int
		title    string
	}{
		{"plan screenshot draws the planned shapes", "plan", nil, 0, "plan", 1, "Open Export"},
		{"later showing re-marks on a new screenshot", "", [][]llm.Event{
			{llm.ToolCallCompleted{ID: "p", Name: overlay.ToolPointAt, Arguments: json.RawMessage(`{"x":0.6,"y":0.2}`)}, llm.Completed{}},
		}, 1, "fresh", 2, "Open Export"},
		{"model revises the step", "", [][]llm.Event{
			{llm.ToolCallCompleted{ID: "r", Name: walkthrough.ToolRevise, Arguments: json.RawMessage(`{"title":"Open the menu","say":"Open the menu first."}`)}, llm.Completed{}},
		}, 1, "", 0, "Open the menu"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			p := &scriptedProvider{rounds: tt.rounds}
			ov, capt := &fakeOverlay{}, &fakeCapturer{}
			att := &recordingSink{}
			o := &Orchestrator{Provider: func(context.Context) (llm.Provider, error) { return p, nil }, Overlay: ov, Capturer: capt, Attachments: att}
			pr := &Presenter{Orch: o}
			got, err := pr.Show(context.Background(), walkthrough.Presentation{Ref: stepRef, Goal: "Export", Index: 1, Total: 3, Step: step, Capture: tt.capture})
			if err != nil {
				t.Fatal(err)
			}
			if got.Title != tt.title {
				t.Errorf("step title %q, want %q", got.Title, tt.title)
			}
			if capt.n != tt.captures {
				t.Errorf("captures %d, want %d", capt.n, tt.captures)
			}
			if len(ov.cmds) != tt.draws {
				t.Fatalf("drew %d, want %d: %+v", len(ov.cmds), tt.draws, ov.cmds)
			}
			for _, c := range ov.cmds {
				if c.Session != stepRef || c.Capture != tt.drawnOn {
					t.Errorf("drew %+v, want session %v on %q", c, stepRef, tt.drawnOn)
				}
			}
			if att.n != 0 {
				t.Error("a step screenshot was attached to the chat")
			}
			if tt.capture == "" {
				msgs := p.reqs[0].Messages
				if user := msgs[len(msgs)-1]; !strings.Contains(user.Content[0].Text, "step 2 of 3") || user.Content[1].Kind != llm.PartImage {
					t.Errorf("step request %+v", user.Content)
				}
			}
		})
	}
}

type recordingSink struct{ n int }

func (r *recordingSink) ScreenAttached(contracts.SessionRef, []contracts.ScreenAttachment) { r.n++ }
